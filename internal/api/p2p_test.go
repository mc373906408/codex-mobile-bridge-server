package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestBootstrapProxyUsesOnlyFixedTargetAndRejectsBrowserOrigin(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	c := testutil.Config()
	c.Development = true
	c.P2PBootstrap = []string{"wss://relay.example.test/v1/p2p/bootstrap"}
	c.P2PProxyTarget = listener.Addr().String()
	a, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	server := httptest.NewServer(a)
	defer server.Close()
	a.Config.Origin = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/p2p/bootstrap"
	_, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err == nil || response.StatusCode != 403 {
		t.Fatal("browser accepted")
	}
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err = ws.Write(ctx, websocket.MessageBinary, []byte("synthetic-easytier-rpc")); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := ws.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || string(payload) != "synthetic-easytier-rpc" {
		t.Fatal("proxy bytes changed", err)
	}
}

func TestP2PDirectoryDistinguishesOfflineFromDenied(t *testing.T) {
	s := testutil.Database(t)
	c := testutil.Config()
	c.P2PBootstrap = []string{"tcp://bootstrap.example.test:11010"}
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	gateway, mobile, binding := testutil.Devices(t, s)
	headers := map[string]string{"Authorization": "Bearer " + mobile.Token}
	read := func(want bool) {
		t.Helper()
		response := webRequest(t, a, "GET", "/v1/mobile/bindings", nil, headers)
		expectStatus(t, response, 200, "")
		var body struct {
			Bindings []struct {
				Allowed bool `json:"access_allowed"`
				Online  bool `json:"gateway_online"`
			} `json:"bindings"`
			Access struct {
				Origin  string `json:"origin"`
				Account string `json:"account_id"`
				Device  string `json:"device_id"`
			} `json:"access"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Bindings) != 1 || body.Bindings[0].Allowed != want || body.Bindings[0].Online {
			t.Fatal("offline presence replaced authority", response.Body.String())
		}
		if body.Access.Origin != c.Origin || body.Access.Account != gateway.AccountID || body.Access.Device != mobile.ID {
			t.Fatal("unscoped authority")
		}
	}
	read(true)
	path := "/v1/gateway/bindings/" + binding.ID + "/p2p"
	expectStatus(t, webRequest(t, a, "GET", path, nil, map[string]string{"Authorization": "Bearer " + gateway.Token}), 200, "")
	expectStatus(t, webRequest(t, a, "GET", path, nil, headers), 403, "")
	_, err = s.Pool.Exec(context.Background(), "UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1", gateway.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	read(false)
}

func TestP2PControlSignalArrivesBeforeRevocationClosesSocket(t *testing.T) {
	s := testutil.Database(t)
	c := testutil.Config()
	c.Development = true
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	server := httptest.NewServer(a)
	t.Cleanup(server.Close)
	a.Config.Origin = server.URL
	gateway, _, _ := testutil.Devices(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/ws/gateway", &websocket.DialOptions{
		Subprotocols: []string{"bridge-relay-v2"}, HTTPHeader: http.Header{
			"Authorization": {"Bearer " + gateway.Token}, "X-Bridge-Control": {"access-v1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	a.Hub.CloseAccount(gateway.AccountID)
	kind, payload, err := socket.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatal("signal lost before close", err)
	}
	var signal map[string]any
	if json.Unmarshal(payload, &signal) != nil || signal["kind"] != "access_changed" || signal["account_id"] != gateway.AccountID || signal["device_id"] != gateway.ID {
		t.Fatal("incorrect signal scope")
	}
	if _, _, err = socket.Read(ctx); err == nil {
		t.Fatal("revoked relay still open")
	}
}
