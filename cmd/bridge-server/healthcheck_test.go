package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
)

func TestHealthcheckUsesLocalConnectionAndExactHost(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		if r.Host != "relay.example.test" || r.URL.Path != "/readyz" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("health check changed the public host or sent authority")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	c := config.Config{Origin: "https://relay.example.test", Listen: strings.TrimPrefix(server.URL, "http://")}
	if err := checkHealth(c); err != nil || !called.Load() {
		t.Fatalf("local readiness failed: %v", err)
	}
}

func TestHealthcheckRejectsUnreadyAndDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://127.0.0.1:1/private")
			w.WriteHeader(status)
		}))
		c := config.Config{Origin: "https://relay.example.test", Listen: strings.TrimPrefix(server.URL, "http://")}
		if err := checkHealth(c); err == nil || err.Error() != "health check not ready" {
			t.Errorf("status %d must fail without following redirects: %v", status, err)
		}
		server.Close()
	}
}

func TestHealthcheckRejectsRemoteAndInvalidListeners(t *testing.T) {
	for _, listen := range []string{"remote.example.test:8080", "192.0.2.1:8080", "invalid"} {
		if err := checkHealth(config.Config{Origin: "https://relay.example.test", Listen: listen}); err == nil {
			t.Errorf("unsafe health listener accepted: %s", listen)
		}
	}
}

func TestInvalidCommandFailsBeforeConfigurationAndMigration(t *testing.T) {
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	for _, args := range [][]string{{"bridge-server", "unexpected"}, {"bridge-server", "serve", "extra"}} {
		os.Args = args
		if err := run(); err == nil || !strings.HasPrefix(err.Error(), "usage:") {
			t.Fatalf("invalid command must be rejected before reading configuration: %v", err)
		}
	}
}
