package relay

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func TestPhoneCloseAcknowledgementDoesNotDisconnectGateway(t *testing.T) {
	hub := New(nil, config.Config{})
	gatewayCtx, cancelGateway := context.WithCancel(context.Background())
	defer cancelGateway()
	gateway := &peer{device: store.Device{ID: "11111111-1111-4111-8111-111111111111", Role: "gateway"},
		ctx: gatewayCtx, cancel: cancelGateway, out: make(chan []byte, 8)}
	phoneCtx, cancelPhone := context.WithCancel(context.Background())
	defer cancelPhone()
	phone := &peer{device: store.Device{Role: "mobile"},
		binding: store.Binding{ID: "22222222-2222-4222-8222-222222222222", GatewayID: gateway.device.ID},
		channel: "33333333-3333-4333-8333-333333333333", ctx: phoneCtx, cancel: cancelPhone, out: make(chan []byte, 8), gateway: gateway}
	hub.gateways[gateway.device.ID] = gateway
	hub.mobiles[phone.channel] = phone
	// The mobile finishes reading its HTTP response and closes its WebSocket.
	hub.remove(phone)
	closed, err := Decode(<-gateway.out)
	if err != nil || closed.Kind != Close {
		t.Fatal("gateway was not told that the phone channel closed", err)
	}
	// The desktop channel worker acknowledges in its finally block. Returning an
	// error here makes Serve tear down the shared gateway and every other phone.
	data, _ := closed.Encode()
	if err := hub.forward(gateway, closed, data); err != nil {
		t.Fatal("normal channel close acknowledgement disconnects the shared gateway:", err)
	}
	late := closed
	late.Kind, late.Payload = Record, make([]byte, 16)
	data, _ = late.Encode()
	if err := hub.forward(gateway, late, data); err != nil {
		t.Fatal("in-flight record from closed channel disconnects gateway:", err)
	}
	late.BindingID = "44444444-4444-4444-8444-444444444444"
	data, _ = late.Encode()
	if err := hub.forward(gateway, late, data); err == nil {
		t.Fatal("closed channel accepted a different binding")
	}
	late.BindingID = closed.BindingID
	late.ChannelID = "55555555-5555-4555-8555-555555555555"
	data, _ = late.Encode()
	if err := hub.forward(gateway, late, data); err == nil {
		t.Fatal("gateway accepted an unknown channel")
	}
	other := &peer{device: store.Device{ID: "66666666-6666-4666-8666-666666666666", Role: "gateway"},
		ctx: gatewayCtx, cancel: cancelGateway, out: make(chan []byte, 8)}
	if err := hub.forward(other, closed, data); err == nil {
		t.Fatal("another gateway accepted the retired channel")
	}
}

func TestSlowOrCanceledPhoneDoesNotInterruptOtherGatewayChannels(t *testing.T) {
	hub := New(nil, config.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateway := &peer{device: store.Device{ID: "gateway", Role: "gateway"}, ctx: ctx, cancel: cancel}
	phoneCtx, cancelPhone := context.WithCancel(ctx)
	defer cancelPhone()
	phone := &peer{device: store.Device{Role: "mobile"}, binding: store.Binding{ID: "binding", GatewayID: "gateway"},
		ctx: phoneCtx, cancel: cancelPhone, out: make(chan []byte, 1), channel: "slow", gateway: gateway}
	other := &peer{device: store.Device{Role: "mobile"}, binding: store.Binding{ID: "binding", GatewayID: "gateway"},
		ctx: ctx, out: make(chan []byte, 1), channel: "other", gateway: gateway}
	hub.mobiles[phone.channel], hub.mobiles[other.channel] = phone, other
	phone.out <- []byte("already full")
	frame := Frame{Kind: Record, BindingID: "binding", ChannelID: phone.channel}
	if err := hub.forward(gateway, frame, []byte("next")); err != nil || phoneCtx.Err() == nil || ctx.Err() != nil {
		t.Fatal("slow phone was not isolated", err)
	}
	if err := hub.forward(gateway, frame, []byte("late")); err != nil {
		t.Fatal("already canceled phone interrupted gateway", err)
	}
	frame.ChannelID = other.channel
	if err := hub.forward(gateway, frame, []byte("unaffected")); err != nil || string(<-other.out) != "unaffected" {
		t.Fatal("other channel stopped forwarding", err)
	}
	frame.BindingID = "wrong binding"
	if err := hub.forward(gateway, frame, nil); err == nil {
		t.Fatal("live channel accepted a mismatched binding")
	}
}

func TestPhoneRoutesCannotCrossGatewayReconnect(t *testing.T) {
	hub := New(nil, config.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := &peer{device: store.Device{ID: "gateway", Role: "gateway"}, ctx: ctx, cancel: cancel, out: make(chan []byte, 8)}
	new := &peer{device: old.device, ctx: ctx, cancel: cancel, out: make(chan []byte, 8)}
	phone := &peer{device: store.Device{Role: "mobile"}, binding: store.Binding{ID: "binding", GatewayID: "gateway"},
		ctx: ctx, cancel: cancel, channel: "old-channel", gateway: old, out: make(chan []byte, 8)}
	hub.mobiles[phone.channel] = phone
	hub.gateways["gateway"] = new
	frame := Frame{Kind: Record, BindingID: "binding", ChannelID: phone.channel}
	if err := hub.forward(phone, frame, nil); err == nil {
		t.Fatal("old phone routed a request to a replacement gateway connection")
	}
	if err := hub.forward(new, frame, nil); err == nil {
		t.Fatal("replacement gateway accessed an earlier connection's phone channel")
	}
	hub.remove(phone)
	if len(new.closed) != 0 || len(new.out) != 0 {
		t.Fatal("retiring an old phone polluted the replacement gateway connection")
	}
}

func TestRetiredGatewayRoutesAreBoundedAndExpire(t *testing.T) {
	p := &peer{}
	for n := range closedRouteLimit + 5 {
		p.rememberClosed(fmt.Sprint(n), "binding")
	}
	if len(p.closed) != closedRouteLimit || len(p.retired) != closedRouteLimit {
		t.Fatal("retired routes exceeded fixed bound", len(p.closed), len(p.retired))
	}
	if _, exists := p.closed["0"]; exists {
		t.Fatal("oldest retired route was not evicted")
	}
	last := p.retired[len(p.retired)-1]
	p.rememberClosed(last, "other binding")
	if len(p.closed) != closedRouteLimit || p.closed[last].binding != "binding" {
		t.Fatal("duplicate retirement replaced route identity or grew history")
	}
	p.expireClosed(time.Now().Add(closedRouteTTL))
	if len(p.closed) != 0 || len(p.retired) != 0 {
		t.Fatal("retired routes did not expire")
	}
}
