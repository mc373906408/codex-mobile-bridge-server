package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/relay"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestDiscoveredPendingSocketsRouteProofAndDisableOnlyTheTargetPhone(t *testing.T) {
	s := testutil.Database(t)
	c := testutil.Config()
	c.Development = true
	app, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Hub.Shutdown)
	server := httptest.NewServer(app)
	app.Config.Origin = server.URL
	t.Cleanup(server.Close)
	account := testutil.Account(t, s, "socket-directory")
	p, _ := testutil.Principal(t, s, account)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	g, err := s.Enroll(ctx, p, "gateway", "", "Desktop", append([]byte{1}, make([]byte, 31)...), 8)
	if err != nil {
		t.Fatal(err)
	}
	phones := []store.Device{}
	bindings := []store.Binding{}
	for n := range 2 {
		m, err := s.Enroll(ctx, p, "mobile", "", "Phone", append([]byte{byte(n + 2)}, make([]byte, 31)...), 16)
		if err != nil {
			t.Fatal(err)
		}
		items, err := s.DeviceBindings(ctx, m)
		if err != nil || len(items) != 1 || items[0].Status != "pending" {
			t.Fatal("enrollment did not discover a pending desktop", err)
		}
		phones = append(phones, m)
		bindings = append(bindings, items[0])
	}
	dial := func(role string, d store.Device, binding string) *websocket.Conn {
		t.Helper()
		headers := http.Header{"Authorization": []string{"Bearer " + d.Token}}
		if binding != "" {
			headers.Set("X-Bridge-Binding", binding)
		}
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/ws/"+role,
			&websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"bridge-relay-v1"}})
		if err != nil {
			t.Fatal("fixture socket failed", err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn
	}
	read := func(conn *websocket.Conn) relay.Frame {
		t.Helper()
		kind, data, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageBinary {
			t.Fatal("fixture frame unavailable", err)
		}
		frame, err := relay.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		return frame
	}
	gateway := dial("gateway", g, "")
	m1 := dial("mobile", phones[0], bindings[0].ID)
	open1 := read(gateway)
	if open1.Kind != relay.Open || open1.BindingID != bindings[0].ID || string(open1.Payload) != string(phones[0].PublicKey) {
		t.Fatal("wrong pending phone routed")
	}
	read(m1)
	m2 := dial("mobile", phones[1], bindings[1].ID)
	open2 := read(gateway)
	read(m2)
	if open2.BindingID != bindings[1].ID || open2.ChannelID == open1.ChannelID {
		t.Fatal("phone channels were not isolated")
	}
	// The server only routes opaque proof bytes; it does not grant chat authority.
	if current, err := s.DirectoryBinding(ctx, bindings[0].ID, g); err != nil || current.Status != "pending" {
		t.Fatal("socket granted approval", err)
	}
	body, _ := json.Marshal(map[string]any{"allowed": false, "expected_revision": 0, "mobile_public_key": base64.StdEncoding.EncodeToString(phones[0].PublicKey)})
	request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/gateway/bindings/"+bindings[0].ID+"/access", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+g.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("close failed", response.StatusCode)
	}
	if _, _, err = m1.Read(ctx); err == nil {
		t.Fatal("disabled phone socket survived")
	}
	closed := read(gateway)
	if closed.Kind != relay.Close || closed.BindingID != bindings[0].ID {
		t.Fatal("wrong socket closed")
	}
	data, _ := (relay.Frame{Kind: relay.Record, BindingID: bindings[1].ID, ChannelID: open2.ChannelID, Payload: bytes.Repeat([]byte{7}, 32)}).Encode()
	if err = m2.Write(ctx, websocket.MessageBinary, data); err != nil {
		t.Fatal("unrelated phone was disconnected", err)
	}
	if forwarded := read(gateway); forwarded.BindingID != bindings[1].ID || forwarded.Kind != relay.Record {
		t.Fatal("unrelated phone stopped routing")
	}
	headers := http.Header{"Authorization": []string{"Bearer " + phones[0].Token}, "X-Bridge-Binding": []string{bindings[0].ID}}
	conn, denied, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/ws/mobile", &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"bridge-relay-v1"}})
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil || denied == nil || denied.StatusCode != 403 {
		t.Fatal("disabled phone reconnected")
	}
}

func TestAdministratorDeviceAndSessionRevocationCloseOnlyTargetSockets(t *testing.T) {
	s := testutil.Database(t)
	app, _, tokens, _ := managementFixture(t, s)
	app.Config.Development = true
	server := httptest.NewServer(app)
	app.Config.Origin = server.URL
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owner := testutil.Account(t, s, "admin-socket-owner")
	gp, _ := testutil.Principal(t, s, owner)
	g, err := s.Enroll(ctx, gp, "gateway", "", "Desktop", append([]byte{1}, make([]byte, 31)...), 8)
	if err != nil {
		t.Fatal(err)
	}
	var phones []store.Device
	var bindings []store.Binding
	for n := range 2 {
		p, _ := testutil.Principal(t, s, owner)
		m, err := s.Enroll(ctx, p, "mobile", "", "Phone", append([]byte{byte(n + 2)}, make([]byte, 31)...), 16)
		if err != nil {
			t.Fatal(err)
		}
		items, err := s.DeviceBindings(ctx, m)
		if err != nil || len(items) != 1 {
			t.Fatal("fixture discovery failed", err)
		}
		phones, bindings = append(phones, m), append(bindings, items[0])
	}
	dial := func(role string, d store.Device, binding string) *websocket.Conn {
		t.Helper()
		headers := http.Header{"Authorization": []string{"Bearer " + d.Token}}
		if binding != "" {
			headers.Set("X-Bridge-Binding", binding)
		}
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/ws/"+role,
			&websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"bridge-relay-v1"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn
	}
	read := func(conn *websocket.Conn) relay.Frame {
		t.Helper()
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		frame, err := relay.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		return frame
	}
	gateway := dial("gateway", g, "")
	first := dial("mobile", phones[0], bindings[0].ID)
	read(gateway)
	read(first)
	second := dial("mobile", phones[1], bindings[1].ID)
	openSecond := read(gateway)
	read(second)
	headers := map[string]string{"Authorization": "Bearer " + tokens.Access}
	base := "/v1/admin/users/" + owner.ID
	expectStatus(t, webRequest(t, app, "DELETE", base+"/installations/"+phones[0].ID, nil, headers), 200, "")
	if _, _, err = first.Read(ctx); err == nil {
		t.Fatal("revoked phone socket remained connected")
	}
	if frame := read(gateway); frame.Kind != relay.Close || frame.BindingID != bindings[0].ID {
		t.Fatal("administrator closed the wrong phone")
	}
	data, _ := (relay.Frame{Kind: relay.Record, BindingID: bindings[1].ID, ChannelID: openSecond.ChannelID, Payload: bytes.Repeat([]byte{7}, 32)}).Encode()
	if err = second.Write(ctx, websocket.MessageBinary, data); err != nil {
		t.Fatal(err)
	}
	if frame := read(gateway); frame.Kind != relay.Record || frame.BindingID != bindings[1].ID {
		t.Fatal("unrelated phone was interrupted")
	}
	expectStatus(t, webRequest(t, app, "DELETE", base+"/sessions/"+phones[1].SessionID, nil, headers), 200, "")
	if _, _, err = second.Read(ctx); err == nil {
		t.Fatal("revoked session socket remained connected")
	}
	if frame := read(gateway); frame.Kind != relay.Close || frame.BindingID != bindings[1].ID {
		t.Fatal("session revocation did not notify the desktop")
	}
}
