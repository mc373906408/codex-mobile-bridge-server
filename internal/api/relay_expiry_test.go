package api

import (
	"context"
	"github.com/coder/websocket"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostedPersistentRelayExpiresWithoutClientQualificationQueries(t *testing.T) {
	s := testutil.Database(t)
	c := testutil.Config()
	c.Mode = "hosted"
	c.Development = true
	app, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Hub.Shutdown)
	server := httptest.NewServer(app)
	app.Config.Origin = server.URL
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g, m, b := testutil.Devices(t, s)
	if _, err = s.Pool.Exec(ctx, "UPDATE entitlements SET expires_at=clock_timestamp()+interval '2 seconds' WHERE account_id=$1", g.AccountID); err != nil {
		t.Fatal(err)
	}
	dial := func(role string, d store.Device, binding, protocol string) (*websocket.Conn, *http.Response, error) {
		headers := http.Header{"Authorization": []string{"Bearer " + d.Token}, "X-Bridge-Mode": []string{"selfhost"}}
		if binding != "" {
			headers.Set("X-Bridge-Binding", binding)
		}
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/ws/"+role, &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{protocol}})
	}
	old, response, err := dial("gateway", g, "", "bridge-relay-v1")
	if old != nil {
		old.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != 426 {
		t.Fatal("v1 not explicitly rejected", err)
	}
	gateway, _, err := dial("gateway", g, "", "bridge-relay-v2")
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.CloseNow()
	mobile, _, err := dial("mobile", m, b.ID, "bridge-relay-v2")
	if err != nil {
		t.Fatal(err)
	}
	defer mobile.CloseNow()
	if _, _, err = mobile.Read(ctx); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, _, err = mobile.Read(ctx); err == nil {
		t.Fatal("expired service retained its connection")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("expiry waited for the 30-second review")
	}
	rejected, response, err := dial("mobile", m, b.ID, "bridge-relay-v2")
	if rejected != nil {
		rejected.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != 402 {
		t.Fatal("forged selfhost client bypassed hosted expiry", err)
	}
}
