// Loopback-only integration fixture with isolated schema and synthetic identities.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/api"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func main() {
	ctx := context.Background()
	raw := os.Getenv("BRIDGE_TEST_DATABASE_URL")
	u, err := url.Parse(raw)
	must(err)
	if u.Hostname() != "127.0.0.1" {
		log.Fatal("isolated loopback PostgreSQL required")
	}
	admin, err := pgxpool.New(ctx, raw)
	must(err)
	defer admin.Close()
	id, err := security.ID()
	must(err)
	schema := "bridge_web_fixture_" + strings.ReplaceAll(id, "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	must(err)
	defer admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := store.Open(ctx, u.String(), make([]byte, 32))
	must(err)
	defer s.Pool.Close()
	must(s.Migrate(ctx))
	c := testutil.Config()
	c.Development = true
	c.Origin = "http://127.0.0.1:18878"
	c.WebOrigin = "http://127.0.0.1:18879"
	c.DatabaseURL = u.String()
	hash, err := security.Password("test-only-password-123")
	must(err)
	account, err := s.CreateAccount(ctx, "web-fixture", hash, "")
	must(err)
	token, err := s.NewSession(ctx, account, c, false)
	must(err)
	principal, err := s.Authenticate(ctx, token.Access, false)
	must(err)
	devices := []store.Device{}
	for _, name := range []string{"Fixture One", "Fixture Two"} {
		d, e := s.Enroll(ctx, principal, "gateway", "", name, append([]byte{1}, make([]byte, 31)...), 8)
		must(e)
		devices = append(devices, d)
	}
	app, err := api.New(c, s)
	must(err)
	defer app.Hub.Shutdown()
	data, _ := json.Marshal(map[string]any{"origin": c.Origin, "web_origin": c.WebOrigin, "devices": devices})
	must(os.WriteFile(os.Args[1], data, 0600))
	done := make(chan error, 2)
	one := &http.Server{Addr: "127.0.0.1:18878", Handler: app, ReadHeaderTimeout: 10 * time.Second}
	two := &http.Server{Addr: "127.0.0.1:18879", Handler: app, ReadHeaderTimeout: 10 * time.Second}
	go func() { done <- one.ListenAndServe() }()
	go func() { done <- two.ListenAndServe() }()
	select {
	case err := <-done:
		log.Print(err)
	case <-time.After(40 * time.Minute):
	}
	one.Close()
	two.Close()
}
