// Package webproxy carries account-authenticated HTTP over an outbound desktop
// socket. Unlike the mobile relay, the cloud service intentionally sees plaintext.
package webproxy

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

const Protocol = "bridge-web-proxy-v1"
const Chunk = 32768
const MaxBody = 20 * 1024 * 1024

var ErrOffline = errors.New("gateway_web_offline")
var ErrCapacity = errors.New("web_capacity")
var ErrBodyTooLarge = errors.New("body_too_large")

type Message struct {
	Kind        string            `json:"kind"`
	ID          uint64            `json:"stream_id"`
	Count       int               `json:"count,omitempty"`
	Status      int               `json:"status,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Session     string            `json:"session_id,omitempty"`
	Account     string            `json:"account_id,omitempty"`
	Method      string            `json:"method,omitempty"`
	Path        string            `json:"path,omitempty"`
	Length      int               `json:"length"`
	ContentType string            `json:"content_type,omitempty"`
	Base        string            `json:"base,omitempty"`
	Origin      string            `json:"origin,omitempty"`
	CSRF        string            `json:"csrf,omitempty"`
	Expires     int64             `json:"expires,omitempty"`
	Data        []byte            `json:"-"`
}

type stream struct {
	session string
	large   bool
	in      chan Message
	cancel  context.CancelFunc
}
type peer struct {
	device   store.Device
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	write    sync.Mutex
	ready    bool // guarded by Hub.mu; conn is immutable after publication
	rate     sync.Mutex
	nextByte time.Time
	streams  map[uint64]*stream
	next     uint64
}
type Hub struct {
	mu     sync.Mutex
	peers  map[string]*peer
	store  *store.Store
	config config.Config
}

func New(s *store.Store, c config.Config) *Hub {
	return &Hub{peers: make(map[string]*peer), store: s, config: c}
}
func (h *Hub) Online(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.peers[id]
	return p != nil && p.ready && p.ctx.Err() == nil
}
func (p *peer) send(ctx context.Context, m Message) error {
	kind := websocket.MessageText
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if m.Data != nil {
		kind = websocket.MessageBinary
		data = make([]byte, 9+len(m.Data))
		data[0] = 1
		binary.BigEndian.PutUint64(data[1:9], m.ID)
		copy(data[9:], m.Data)
	}
	p.write.Lock()
	defer p.write.Unlock()
	if p.conn == nil {
		return ErrOffline
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return p.conn.Write(writeCtx, kind, data)
}
func (h *Hub) closeMatching(match func(*peer) bool, session string) {
	h.mu.Lock()
	var notices []*peer
	for _, p := range h.peers {
		if match(p) {
			p.cancel()
			continue
		}
		if session != "" {
			for _, s := range p.streams {
				if s.session == session {
					s.cancel()
				}
			}
			if p.ready {
				notices = append(notices, p)
			}
		}
	}
	h.mu.Unlock()
	for _, p := range notices {
		go func(p *peer) { _ = p.send(p.ctx, Message{Kind: "close_session", Session: session}) }(p)
	}
}
func (h *Hub) CloseAccount(id string) {
	h.closeMatching(func(p *peer) bool { return p.device.AccountID == id }, "")
}
func (h *Hub) CloseDevice(id string) {
	h.closeMatching(func(p *peer) bool { return p.device.ID == id }, "")
}
func (h *Hub) CloseSession(id string) {
	h.closeMatching(func(p *peer) bool { return p.device.SessionID == id }, id)
}
func (h *Hub) Shutdown() { h.closeMatching(func(*peer) bool { return true }, "") }

func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, d store.Device, token string) {
	if r.Header.Get("Origin") != "" || r.URL.RawQuery != "" {
		http.Error(w, "desktop only", 403)
		return
	}
	protocol := false
	for _, v := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if strings.TrimSpace(v) == Protocol {
			protocol = true
		}
	}
	if !protocol {
		http.Error(w, "protocol_upgrade_required", 426)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	p := &peer{device: d, ctx: ctx, cancel: cancel, streams: make(map[uint64]*stream)}
	h.mu.Lock()
	if h.peers[d.ID] != nil || len(h.peers) >= h.config.MaxConnections {
		h.mu.Unlock()
		http.Error(w, "capacity", 409)
		return
	}
	h.peers[d.ID] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.peers[d.ID] == p {
			delete(h.peers, d.ID)
		}
		for _, s := range p.streams {
			s.cancel()
		}
		h.mu.Unlock()
	}()
	// Recheck after reservation so session/device revocation cannot race admission.
	current, checkErr := h.store.Device(ctx, token, "gateway")
	if checkErr != nil || current.ID != d.ID || current.AccountID != d.AccountID || ctx.Err() != nil {
		http.Error(w, "access_denied", 403)
		return
	}
	qualification, checkErr := h.store.Entitlement(ctx, d.AccountID, h.config.Paid())
	if checkErr != nil || !qualification.Active || ctx.Err() != nil {
		http.Error(w, "entitlement_required", 402)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Protocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	h.mu.Lock()
	p.conn = conn
	p.ready = true
	h.mu.Unlock()
	defer conn.CloseNow()
	conn.SetReadLimit(Chunk + 9)
	go h.monitor(p, token)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			ping, done := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(ping)
			done()
			if err != nil {
				cancel()
				return
			}
		}
	}()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var m Message
		if kind == websocket.MessageBinary {
			if len(data) <= 9 || data[0] != 1 {
				return
			}
			m.ID = binary.BigEndian.Uint64(data[1:9])
			m.Kind = "data"
			m.Data = data[9:]
		} else if kind != websocket.MessageText || len(data) > 16384 || json.Unmarshal(data, &m) != nil {
			return
		}
		if m.ID == 0 || m.ID > 1<<53-1 {
			return
		}
		h.mu.Lock()
		s := p.streams[m.ID]
		if s != nil {
			select {
			case s.in <- m:
			default:
				s.cancel()
			}
		}
		h.mu.Unlock()
	}
}

func (h *Hub) monitor(p *peer, token string) {
	defer p.cancel()
	for {
		check, cancel := context.WithTimeout(p.ctx, 5*time.Second)
		d, err := h.store.Device(check, token, "gateway")
		if err != nil || d.ID != p.device.ID || d.AccountID != p.device.AccountID {
			cancel()
			return
		}
		e, err := h.store.Entitlement(check, d.AccountID, h.config.Paid())
		cancel()
		if err != nil || !e.Active {
			return
		}
		delay := 30 * time.Second
		if e.Required && e.ExpiresAt != nil && e.ExpiresAt.Sub(e.ServerTime) < delay {
			delay = e.ExpiresAt.Sub(e.ServerTime)
		}
		timer := time.NewTimer(delay)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// A bounded reservation spaces both upload and download chunks per gateway.
func (h *Hub) pace(ctx context.Context, p *peer, n int) error {
	if h.config.BytesPerSecond <= 0 {
		return nil
	}
	p.rate.Lock()
	now := time.Now()
	start := p.nextByte
	if start.Before(now) {
		start = now
	}
	p.nextByte = start.Add(time.Duration(n) * time.Second / time.Duration(h.config.BytesPerSecond))
	p.rate.Unlock()
	timer := time.NewTimer(time.Until(start))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hub) promote(p *peer, s *stream) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.large {
		return nil
	}
	count := 0
	for _, other := range p.streams {
		if other.large {
			count++
			if other.session == s.session {
				return ErrCapacity
			}
		}
	}
	if count >= 2 {
		return ErrCapacity
	}
	s.large = true
	return nil
}

// Proxy never retries a request. An interrupted write must remain unknown.
func (h *Hub) Proxy(w http.ResponseWriter, r *http.Request, principal store.Principal, gateway, path, origin string, expires time.Time) error {
	// Reject oversized metadata as a request error before it can exceed the
	// desktop's control-frame bound and disconnect other browser streams.
	for _, name := range []string{"Content-Type", "Range", "If-Range", "If-None-Match"} {
		if value := r.Header.Get(name); len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid_request_header")
		}
	}
	if r.ContentLength > MaxBody {
		return ErrBodyTooLarge
	}
	ctx, cancel := context.WithDeadline(r.Context(), expires)
	defer cancel()
	h.mu.Lock()
	p := h.peers[gateway]
	if p == nil || p.ctx.Err() != nil || !p.ready {
		h.mu.Unlock()
		return ErrOffline
	}
	if p.device.AccountID != principal.ID {
		h.mu.Unlock()
		return store.ErrDenied
	}
	count := 0
	for _, s := range p.streams {
		if s.session == principal.SessionID {
			count++
		}
	}
	if count >= 8 || len(p.streams) >= 64 || p.next >= 1<<53-1 {
		h.mu.Unlock()
		return ErrCapacity
	}
	p.next++
	id := p.next
	s := &stream{session: principal.SessionID, in: make(chan Message, 8), cancel: cancel}
	p.streams[id] = s
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(p.streams, id)
		h.mu.Unlock()
		_ = p.send(p.ctx, Message{Kind: "cancel", ID: id})
	}()
	// Register before rechecking, so revocation cannot race admission.
	if _, err := h.store.WebClientDeadline(ctx, principal.SessionID, principal.ID); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-p.ctx.Done():
				cancel()
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, done := context.WithTimeout(ctx, 5*time.Second)
				_, err := h.store.WebClientDeadline(check, principal.SessionID, principal.ID)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	var body []byte
	var err error
	if r.Method == http.MethodPost {
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(45 * time.Second))
		buffer := make([]byte, Chunk)
		for {
			n, e := r.Body.Read(buffer)
			if len(body)+n > MaxBody {
				err = ErrBodyTooLarge
				break
			}
			if len(body)+n > 1024*1024 {
				if err = h.promote(p, s); err != nil {
					break
				}
			}
			body = append(body, buffer[:n]...)
			if e == io.EOF {
				break
			}
			if e != nil {
				err = e
				break
			}
		}
		_ = controller.SetReadDeadline(time.Time{})
		if err != nil {
			return err
		}
		if len(body) > MaxBody {
			return ErrBodyTooLarge
		}
	}
	headers := map[string]string{}
	for _, name := range []string{"Range", "If-Range", "If-None-Match"} {
		if value := r.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	err = p.send(ctx, Message{Kind: "request", ID: id, Session: principal.SessionID, Account: principal.ID, Method: r.Method, Path: path, Length: len(body), ContentType: r.Header.Get("Content-Type"), Headers: headers, Base: "/d/" + gateway, Origin: origin, CSRF: principal.CSRF, Expires: expires.Unix()})
	if err != nil {
		return err
	}
	receive := func() (Message, error) {
		select {
		case m := <-s.in:
			return m, nil
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
	}
	offset := 0
	for offset < len(body) {
		m, e := receive()
		if e != nil {
			return e
		}
		if m.Kind != "credit" || m.Count < 1 || m.Count > 4 {
			return errors.New("invalid_request_credit")
		}
		for n := 0; n < m.Count && offset < len(body); n++ {
			end := min(len(body), offset+Chunk)
			if err = h.pace(ctx, p, end-offset); err != nil {
				return err
			}
			if err = p.send(ctx, Message{ID: id, Data: body[offset:end]}); err != nil {
				return err
			}
			offset = end
		}
	}
	if err = p.send(ctx, Message{Kind: "request_end", ID: id}); err != nil {
		return err
	}
	started := false
	var received int64
	var expected int64 = -1
	for {
		m, err := receive()
		if err != nil {
			if started {
				panic(http.ErrAbortHandler)
			}
			return err
		}
		switch m.Kind {
		case "credit": // Upload credits already in flight after request_end.
			if started || m.Count < 1 || m.Count > 4 {
				if started {
					panic(http.ErrAbortHandler)
				}
				return errors.New("invalid_credit")
			}
		case "response_start":
			if started || m.Status < 200 || m.Status > 599 || (m.Status >= 300 && m.Status < 400 && m.Status != 304) {
				if started {
					panic(http.ErrAbortHandler)
				}
				return errors.New("invalid_response")
			}
			for _, value := range m.Headers {
				if strings.ContainsAny(value, "\r\n\x00") {
					return errors.New("invalid_response_header")
				}
			}
			if v := m.Headers["Content-Length"]; v != "" {
				expected, err = strconv.ParseInt(v, 10, 64)
				if err != nil || expected < 0 || expected > MaxBody {
					return errors.New("invalid_response_length")
				}
			}
			if m.Status == 304 || m.Status == 204 {
				expected = 0
				delete(m.Headers, "Content-Length")
			}
			if expected > 1024*1024 {
				if err = h.promote(p, s); err != nil {
					return err
				}
			}
			for _, name := range []string{"Content-Type", "Content-Length", "Content-Disposition", "ETag", "Cache-Control", "Content-Range", "Accept-Ranges", "Content-Security-Policy", "X-Frame-Options"} {
				if value := m.Headers[name]; value != "" {
					w.Header().Set(name, value)
				}
			}
			if value := w.Header().Get("Cache-Control"); strings.Contains(value, "public") {
				w.Header().Set("Cache-Control", strings.ReplaceAll(value, "public", "private"))
			}
			w.WriteHeader(m.Status)
			started = true
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if err = p.send(ctx, Message{Kind: "credit", ID: id, Count: 4}); err != nil {
				panic(http.ErrAbortHandler)
			}
		case "data":
			if !started {
				return errors.New("response_not_started")
			}
			received += int64(len(m.Data))
			if received > 1024*1024 && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
				if err = h.promote(p, s); err != nil {
					panic(http.ErrAbortHandler)
				}
			}
			if expected >= 0 && received > expected {
				panic(http.ErrAbortHandler)
			}
			if received > MaxBody && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
				panic(http.ErrAbortHandler)
			}
			if err = h.pace(ctx, p, len(m.Data)); err != nil {
				panic(http.ErrAbortHandler)
			}
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err = w.Write(m.Data); err != nil {
				panic(http.ErrAbortHandler)
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
			if err = p.send(ctx, Message{Kind: "credit", ID: id, Count: 1}); err != nil {
				panic(http.ErrAbortHandler)
			}
		case "response_end":
			if !started {
				return errors.New("response_not_started")
			}
			if expected >= 0 && received != expected {
				panic(http.ErrAbortHandler)
			}
			return nil
		case "stream_error":
			if started {
				panic(http.ErrAbortHandler)
			}
			return errors.New("gateway_request_failed")
		default:
			if started {
				panic(http.ErrAbortHandler)
			}
			return errors.New("invalid_response")
		}
	}
}
