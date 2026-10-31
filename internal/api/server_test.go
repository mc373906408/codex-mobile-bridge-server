package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestLivenessAndReadinessTrackDatabaseAvailability(t *testing.T) {
	s := testutil.Database(t)
	a, err := New(testutil.Config(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	request := func(path string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, a.Config.Origin+path, nil))
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: expected %d without caching, got %d", path, status, w.Code)
		}
	}
	request("/healthz", http.StatusOK)
	request("/readyz", http.StatusOK)
	s.Pool.Close()
	request("/healthz", http.StatusOK)
	request("/readyz", http.StatusServiceUnavailable)
}

func TestHealthEndpointsRequireExactPublicHost(t *testing.T) {
	a, err := New(testutil.Config(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	for _, host := range []string{"Relay.example.test", "relay.example.test:443", "other.example.test"} {
		for _, path := range []string{"/healthz", "/readyz"} {
			r := httptest.NewRequest(http.MethodGet, a.Config.Origin+path, nil)
			r.Host = host
			r.Header.Set("X-Forwarded-Host", "relay.example.test")
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("noncanonical host %q must fail before dispatching %s", host, path)
			}
		}
	}
}

func TestClientIPTrustsOnlyTheExplicitProxyChain(t *testing.T) {
	a := &Server{Config: config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.30.74.2/32")}}}
	for _, test := range []struct{ peer, forwarded, want string }{
		{"198.51.100.20:1234", "203.0.113.10", "198.51.100.20"},
		{"172.30.74.2:1234", "203.0.113.10", "203.0.113.10"},
		{"172.30.74.2:1234", "192.0.2.4, 203.0.113.10", "203.0.113.10"},
		{"172.30.74.2:1234", "invalid", "172.30.74.2"},
		{"172.30.74.2:1234", strings.Repeat("203.0.113.10,", 17), "172.30.74.2"},
	} {
		r := httptest.NewRequest(http.MethodGet, "https://relay.example.test/", nil)
		r.RemoteAddr = test.peer
		r.Header.Set("X-Forwarded-For", test.forwarded)
		if got := a.clientIP(r); got != test.want {
			t.Fatalf("peer %s: expected client %s, got %s", test.peer, test.want, got)
		}
	}
}
