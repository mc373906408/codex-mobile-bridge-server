package relay

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

type peer struct {
	device  store.Device
	binding store.Binding
	channel string
	conn    *websocket.Conn
	out     chan []byte
	ctx     context.Context
	cancel  context.CancelFunc
	window  time.Time
	bytes   int
	closed  map[string]closedRoute
	retired []string
	gateway *peer
}

const closedRouteLimit = 128
const closedRouteTTL = time.Minute

type closedRoute struct {
	binding string
	at      time.Time
}

type Hub struct {
	mu       sync.Mutex
	gateways map[string]*peer
	mobiles  map[string]*peer
	store    *store.Store
	config   config.Config
}

func New(s *store.Store, c config.Config) *Hub {
	return &Hub{gateways: make(map[string]*peer), mobiles: make(map[string]*peer), store: s, config: c}
}

func (h *Hub) CloseAccount(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.gateways {
		if p.device.AccountID == id {
			p.cancel()
		}
	}
	for _, p := range h.mobiles {
		if p.device.AccountID == id {
			p.cancel()
		}
	}
}
func (h *Hub) CloseSession(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.gateways {
		if p.device.SessionID == id {
			p.cancel()
		}
	}
	for _, p := range h.mobiles {
		if p.device.SessionID == id {
			p.cancel()
		}
	}
}
func (h *Hub) CloseDevice(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.gateways {
		if p.device.ID == id {
			p.cancel()
		}
	}
	for _, p := range h.mobiles {
		if p.device.ID == id {
			p.cancel()
		}
	}
}
func (h *Hub) CloseBinding(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.mobiles {
		if p.binding.ID == id {
			p.cancel()
		}
	}
}
func (h *Hub) Shutdown() {
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
	if !hasProtocol(r.Header.Values("Sec-WebSocket-Protocol"), "bridge-relay-v1") {
		http.Error(w, "protocol required", http.StatusBadRequest)
		return
	}
	channel, err := security.ID()
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	p := &peer{device: d, binding: b, channel: channel, out: make(chan []byte, 8), ctx: ctx, cancel: cancel, window: time.Now()}
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
	p.conn, err = websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"bridge-relay-v1"}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer p.conn.CloseNow()
	p.conn.SetReadLimit(MaxFrame)
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
			return
		}
		now := time.Now()
		if now.Sub(p.window) >= time.Second {
			p.window = now
			p.bytes = 0
		}
		p.bytes += len(data)
		if p.bytes > h.config.BytesPerSecond {
			return
		}
		frame, err := Decode(data)
		if err != nil || frame.Kind == Open {
			return
		}
		if err = h.forward(p, frame, data); err != nil {
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
	if p.ctx.Err() != nil {
		return false
	}
	data, err := f.Encode()
	if err != nil {
		return false
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
	select {
	case <-target.ctx.Done():
		if sender.device.Role == "gateway" {
			return nil
		}
		return errors.New("peer disconnected")
	case target.out <- data:
		if f.Kind == Close && sender.device.Role == "mobile" {
			sender.cancel()
		}
		return nil
	default:
		target.cancel()
		if sender.device.Role == "gateway" {
			return nil
		}
		return errors.New("slow consumer")
	}
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
		select {
		case <-p.ctx.Done():
			return
		case data := <-p.out:
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

func (h *Hub) review(p *peer, token string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
			_, err := h.store.Device(ctx, token, p.device.Role)
			if err == nil {
				var e store.Entitlement
				e, err = h.store.Entitlement(ctx, p.device.AccountID, h.config.Paid())
				if err == nil && !e.Active {
					err = store.ErrDenied
				}
			}
			if err == nil && p.device.Role == "mobile" {
				_, err = h.store.Binding(ctx, p.binding.ID, p.device, true)
			}
			cancel()
			if err != nil {
				p.cancel()
				return
			}
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
