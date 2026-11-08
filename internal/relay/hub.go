package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/webproxy"
)

type peer struct {
	device    store.Device
	binding   store.Binding
	channel   string
	conn      *websocket.Conn
	out       chan []byte
	ctx       context.Context
	cancel    context.CancelFunc
	window    time.Time
	bytes     int
	closed    map[string]closedRoute
	retired   []string
	gateway   *peer
	queues    map[string][][]byte
	order     []string
	wake      chan struct{}
	nextWrite map[string]time.Time
	signals   chan []byte
}

const closedRouteLimit = 128
const closedRouteTTL = time.Minute

type closedRoute struct {
	binding string
	at      time.Time
}

type Hub struct {
	Web      *webproxy.Hub
	mu       sync.Mutex
	gateways map[string]*peer
	mobiles  map[string]*peer
	store    *store.Store
	config   config.Config
}

func New(s *store.Store, c config.Config) *Hub {
	return &Hub{Web: webproxy.New(s, c), gateways: make(map[string]*peer), mobiles: make(map[string]*peer), store: s, config: c}
}

func (h *Hub) CloseAccount(id string) {
	h.Web.CloseAccount(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.gateways {
		if p.device.AccountID == id {
			h.signalClose(p, "", "account_changed")
		}
	}
	for _, p := range h.mobiles {
		if p.device.AccountID == id {
			h.signalClose(p, p.binding.ID, "account_changed")
		}
	}
}
func (h *Hub) CloseSession(id string) {
	h.Web.CloseSession(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.gateways {
		if p.device.SessionID == id {
			h.signalClose(p, "", "session_changed")
		}
	}
	for _, p := range h.mobiles {
		if p.device.SessionID == id {
			h.signal(p.gateway, p.binding.ID, "session_changed")
			h.signalClose(p, p.binding.ID, "session_changed")
		}
	}
}
func (h *Hub) CloseDevice(id string) {
	h.Web.CloseDevice(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.gateways {
		if p.device.ID == id {
			h.signalClose(p, "", "device_changed")
		}
	}
	for _, p := range h.mobiles {
		if p.device.ID == id {
			h.signal(p.gateway, p.binding.ID, "device_changed")
			h.signalClose(p, p.binding.ID, "device_changed")
		}
	}
}
func (h *Hub) CloseBinding(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.mobiles {
		if p.binding.ID == id {
			h.signal(p.gateway, id, "binding_changed")
			h.signalClose(p, id, "binding_changed")
		}
	}
}

// A signal requests an authoritative recheck rather than carrying a stale grant
// or revocation across a concurrent renewal. Only opted-in TLS peers receive it.
func (h *Hub) signal(p *peer, binding, reason string) {
	h.sendSignal(p, binding, reason, false)
}

func (h *Hub) signalClose(p *peer, binding, reason string) {
	h.sendSignal(p, binding, reason, true)
}

func (h *Hub) sendSignal(p *peer, binding, reason string, closeAfter bool) {
	if p == nil || p.signals == nil || p.conn == nil {
		if p != nil && closeAfter {
			p.cancel()
		}
		return
	}
	data, _ := json.Marshal(map[string]any{"kind": "access_changed", "origin": h.config.Origin,
		"account_id": p.device.AccountID, "device_id": p.device.ID, "binding_id": binding, "reason": reason})
	select {
	case p.signals <- data:
	default:
		if closeAfter {
			p.cancel()
		}
		return
	}
	go func() {
		defer func() {
			<-p.signals
			if closeAfter {
				p.cancel()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.conn.Write(ctx, websocket.MessageText, data)
	}()
}
func (h *Hub) Shutdown() {
	h.Web.Shutdown()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.gateways {
		p.cancel()
	}
	for _, p := range h.mobiles {
		p.cancel()
	}
}
func (h *Hub) Status() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.gateways), len(h.mobiles)
}

func (h *Hub) GatewayOnline(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.gateways[id]
	return p != nil && p.ctx.Err() == nil
}

func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, d store.Device, b store.Binding, token string) {
	// Browser E2EE is deliberately not part of v1. Native clients send no Origin.
	if r.Header.Get("Origin") != "" || r.URL.RawQuery != "" {
		http.Error(w, "native relay clients only", http.StatusForbidden)
		return
	}
	if !hasProtocol(r.Header.Values("Sec-WebSocket-Protocol"), "bridge-relay-v2") {
		http.Error(w, "protocol_upgrade_required", http.StatusUpgradeRequired)
		return
	}
	channel, err := security.ID()
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	p := &peer{device: d, binding: b, channel: channel, out: make(chan []byte, 128), ctx: ctx, cancel: cancel, window: time.Now(),
		queues: make(map[string][][]byte), wake: make(chan struct{}, 1), nextWrite: make(map[string]time.Time)}
	if r.Header.Get("X-Bridge-Control") == "access-v1" {
		p.signals = make(chan []byte, 16)
	}
	started := time.Now()
	reason := "transport_closed"
	defer func() {
		slog.Info("relay_connection_end", "role", d.Role, "channel", channel, "reason", reason, "duration_ms", time.Since(started).Milliseconds())
	}()
	defer cancel()
	h.mu.Lock()
	if len(h.gateways)+len(h.mobiles) >= h.config.MaxConnections {
		h.mu.Unlock()
		http.Error(w, "capacity reached", 429)
		return
	}
	if d.Role == "gateway" {
		if old := h.gateways[d.ID]; old != nil {
			h.mu.Unlock()
			http.Error(w, "gateway already connected", 409)
			return
		}
		h.gateways[d.ID] = p
	} else {
		p.gateway = h.gateways[b.GatewayID]
		if p.gateway == nil {
			h.mu.Unlock()
			http.Error(w, "gateway offline", 503)
			return
		}
		count := 0
		for _, m := range h.mobiles {
			if m.binding.ID == b.ID {
				// A newly authenticated transport supersedes only this binding.
				m.cancel()
			}
			if m.binding.GatewayID == b.GatewayID {
				count++
			}
		}
		if count >= h.config.MaxPerGateway {
			h.mu.Unlock()
			http.Error(w, "gateway capacity reached", 429)
			return
		}
		h.mobiles[channel] = p
	}
	h.mu.Unlock()
	defer h.remove(p)
	if d.Role == "mobile" {
		// Register before rechecking: a concurrent close must see this peer too.
		if _, err := h.store.Binding(ctx, b.ID, d, true); err != nil {
			http.Error(w, "binding unavailable", http.StatusForbidden)
			return
		}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"bridge-relay-v2"}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	h.mu.Lock()
	p.conn = conn
	h.mu.Unlock()
	defer p.conn.CloseNow()
	p.conn.SetReadLimit(MaxFrame)
	slog.Info("relay_connection_open", "role", d.Role, "channel", channel, "protocol", 2)
	if d.Role == "mobile" {
		h.mu.Lock()
		gateway := h.gateways[b.GatewayID]
		if gateway != p.gateway || !h.enqueue(gateway, Frame{Kind: Open, BindingID: b.ID, ChannelID: channel, Payload: b.MobilePublicKey}) {
			h.mu.Unlock()
			return
		}
		h.enqueue(p, Frame{Kind: Open, BindingID: b.ID, ChannelID: channel, Payload: b.GatewayPublicKey})
		h.mu.Unlock()
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); h.write(p) }()
	go func() { defer workers.Done(); h.review(p, token) }()
	defer func() { cancel(); workers.Wait() }()
	for {
		kind, data, err := p.conn.Read(ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageBinary {
			reason = "non_binary_frame"
			return
		}
		frame, err := Decode(data)
		if err != nil || frame.Kind == Open {
			reason = "invalid_frame"
			return
		}
		if err = h.forward(p, frame, data); err != nil {
			reason = "route_rejected"
			return
		}
	}
}

func hasProtocol(values []string, want string) bool {
	for _, v := range values {
		for _, p := range splitProtocols(v) {
			if p == want {
				return true
			}
		}
	}
	return false
}

func splitProtocols(v string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == ',' {
			s := v[start:i]
			for len(s) > 0 && s[0] == ' ' {
				s = s[1:]
			}
			for len(s) > 0 && s[len(s)-1] == ' ' {
				s = s[:len(s)-1]
			}
			out = append(out, s)
			start = i + 1
		}
	}
	return out
}

func (h *Hub) enqueue(p *peer, f Frame) bool {
	if p == nil || p.ctx.Err() != nil {
		return false
	}
	data, err := f.Encode()
	if err != nil {
		return false
	}
	return h.queue(p, f.ChannelID, data)
}

// Caller holds Hub.mu. Queue pressure belongs to one authenticated binding.
func (h *Hub) queue(p *peer, channel string, data []byte) bool {
	if p.device.Role == "gateway" && p.queues != nil {
		q, exists := p.queues[channel]
		if !exists && len(p.queues) >= 64 {
			if mobile := h.mobiles[channel]; mobile != nil {
				mobile.cancel()
			}
			return false
		}
		if len(q) >= 128 {
			if mobile := h.mobiles[channel]; mobile != nil {
				mobile.cancel()
			}
			// Poison only this Noise route. Send its Close after discarding its backlog.
			f, err := Decode(data)
			if err == nil {
				closing, _ := (Frame{Kind: Close, BindingID: f.BindingID, ChannelID: channel}).Encode()
				p.queues[channel] = [][]byte{closing}
			}
			slog.Warn("relay_backpressure", "channel", channel, "reason", "binding_queue_full")
			return false
		}
		if !exists {
			p.order = append(p.order, channel)
		}
		p.queues[channel] = append(q, data)
		select {
		case p.wake <- struct{}{}:
		default:
		}
		return true
	}
	select {
	case p.out <- data:
		return true
	default:
		p.cancel()
		return false
	}
}

func (h *Hub) forward(sender *peer, f Frame, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sender.ctx.Err() != nil {
		return errors.New("peer disconnected")
	}
	var target *peer
	if sender.device.Role == "gateway" {
		target = h.mobiles[f.ChannelID]
		if target == nil {
			sender.expireClosed(time.Now())
			if route, ok := sender.closed[f.ChannelID]; ok && route.binding == f.BindingID {
				return nil
			}
			return errors.New("unauthorized route")
		}
		if target.gateway != sender || target.binding.GatewayID != sender.device.ID || target.binding.ID != f.BindingID {
			return errors.New("unauthorized route")
		}
		if target.ctx.Err() != nil {
			return nil
		}
	} else {
		if f.BindingID != sender.binding.ID || f.ChannelID != sender.channel {
			return errors.New("unauthorized route")
		}
		target = h.gateways[sender.binding.GatewayID]
		if target == nil || target != sender.gateway {
			return errors.New("gateway offline")
		}
	}
	if target.ctx.Err() != nil {
		if sender.device.Role == "gateway" {
			return nil
		}
		return errors.New("peer disconnected")
	}
	if h.queue(target, f.ChannelID, data) {
		if f.Kind == Close && sender.device.Role == "mobile" {
			sender.cancel()
		}
		return nil
	}
	if sender.device.Role == "mobile" {
		sender.cancel()
	}
	return nil
}

// Accessed only under Hub.mu. A tombstone is bound to this gateway connection
// and the exact binding, so late worker frames cannot become a route elsewhere.
func (p *peer) expireClosed(now time.Time) {
	for len(p.retired) > 0 {
		channel := p.retired[0]
		if len(p.retired) <= closedRouteLimit && now.Sub(p.closed[channel].at) < closedRouteTTL {
			break
		}
		delete(p.closed, channel)
		p.retired[0] = ""
		p.retired = p.retired[1:]
	}
}

func (p *peer) rememberClosed(channel, binding string) {
	now := time.Now()
	if p.closed == nil {
		p.closed = make(map[string]closedRoute)
	}
	if _, exists := p.closed[channel]; !exists {
		p.closed[channel] = closedRoute{binding: binding, at: now}
		p.retired = append(p.retired, channel)
	}
	p.expireClosed(now)
}

func (h *Hub) write(p *peer) {
	defer p.cancel()
	for {
		data, err := h.next(p)
		if err != nil {
			return
		}
		{
			ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
			err := p.conn.Write(ctx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				return
			}
			// Drain earlier response records before closing a mobile channel.
			if p.device.Role == "mobile" && len(data) >= HeaderSize && data[1] == Close {
				return
			}
		}
	}
}

// Fair scheduling across bindings with a bounded burst, before socket writes.
func (h *Hub) next(p *peer) ([]byte, error) {
	if p.device.Role != "gateway" || p.queues == nil {
		select {
		case <-p.ctx.Done():
			return nil, p.ctx.Err()
		case data := <-p.out:
			if err := h.pace(p, p.channel, len(data)); err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	for {
		h.mu.Lock()
		now := time.Now()
		wait := time.Second
		for n := len(p.order); n > 0; n-- {
			id := p.order[0]
			p.order = p.order[1:]
			q := p.queues[id]
			if len(q) == 0 {
				delete(p.queues, id)
				delete(p.nextWrite, id)
				continue
			}
			if delay := p.nextWrite[id].Sub(now); delay > 0 {
				if delay < wait {
					wait = delay
				}
				p.order = append(p.order, id)
				continue
			}
			data := q[0]
			p.queues[id] = q[1:]
			p.order = append(p.order, id)
			if h.config.BytesPerSecond > 0 {
				p.nextWrite[id] = now.Add(time.Duration(len(data)) * time.Second / time.Duration(h.config.BytesPerSecond))
			}
			h.mu.Unlock()
			return data, nil
		}
		h.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return nil, p.ctx.Err()
		case <-p.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (h *Hub) pace(p *peer, id string, size int) error {
	if h.config.BytesPerSecond <= 0 {
		return nil
	}
	if delay := time.Until(p.nextWrite[id]); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-p.ctx.Done():
			return p.ctx.Err()
		case <-timer.C:
		}
	}
	p.nextWrite[id] = time.Now().Add(time.Duration(size) * time.Second / time.Duration(h.config.BytesPerSecond))
	return nil
}

func (h *Hub) review(p *peer, token string) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var expiry time.Time
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-timer.C:
			ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
			_, err := h.store.Device(ctx, token, p.device.Role)
			if err == nil {
				var e store.Entitlement
				e, err = h.store.Entitlement(ctx, p.device.AccountID, h.config.Paid())
				if err == nil && !e.Active {
					err = store.ErrDenied
				}
				if err == nil && e.Required && e.ExpiresAt != nil {
					// Database server time is authoritative; use a local monotonic deadline.
					expiry = time.Now().Add(e.ExpiresAt.Sub(e.ServerTime))
				}
			}
			if err == nil && p.device.Role == "mobile" {
				_, err = h.store.Binding(ctx, p.binding.ID, p.device, true)
			}
			cancel()
			if err != nil {
				slog.Info("relay_access_closed", "channel", p.channel, "reason", "access_review_failed")
				// Database/network failures are not authority decisions.
				if errors.Is(err, store.ErrDenied) {
					h.mu.Lock()
					h.signalClose(p, p.binding.ID, "access_expired_or_revoked")
					if p.device.Role == "mobile" {
						h.signal(p.gateway, p.binding.ID, "access_expired_or_revoked")
					}
					h.mu.Unlock()
					return
				}
				p.cancel()
				return
			}
			next := 30 * time.Second
			if !expiry.IsZero() && time.Until(expiry) < next {
				next = time.Until(expiry)
			}
			timer.Reset(max(next, 0))
		}
	}
}

func (h *Hub) remove(p *peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p.device.Role == "gateway" {
		if h.gateways[p.device.ID] == p {
			delete(h.gateways, p.device.ID)
		}
		for _, m := range h.mobiles {
			if m.binding.GatewayID == p.device.ID {
				m.cancel()
			}
		}
	} else {
		delete(h.mobiles, p.channel)
		if g := h.gateways[p.binding.GatewayID]; g != nil && g == p.gateway {
			g.rememberClosed(p.channel, p.binding.ID)
			h.enqueue(g, Frame{Kind: Close, BindingID: p.binding.ID, ChannelID: p.channel})
		}
	}
}
