package testutil

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func Config() config.Config {
	return config.Config{Mode: "selfhost", Origin: "https://relay.example.test", Listen: "127.0.0.1:8080", DataKey: make([]byte, 32), MaxGateways: 8, MaxInstallations: 16, MaxConnections: 32, MaxPerGateway: 16, BytesPerSecond: 1048576, AccessTTL: 15 * time.Minute, RefreshTTL: 30 * 24 * time.Hour}
}

func Database(t *testing.T) *store.Store {
	t.Helper()
	raw := os.Getenv("BRIDGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set BRIDGE_TEST_DATABASE_URL to an isolated PostgreSQL test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	id, err := security.ID()
	if err != nil {
		t.Fatal(err)
	}
	name := "bridge_test_" + strings.ReplaceAll(id, "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal("cannot create isolated test schema")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("test URL required")
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	s, err := store.Open(ctx, u.String(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	return s
}

var once sync.Once
var passwordHash string

func Account(t *testing.T, s *store.Store, login string) store.Account {
	t.Helper()
	once.Do(func() { passwordHash, _ = security.Password("test-only-password-123") })
	a, err := s.CreateAccount(context.Background(), login, passwordHash, true, Config(), "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func Principal(t *testing.T, s *store.Store, a store.Account) (store.Principal, store.Tokens) {
	t.Helper()
	tokens, err := s.NewSession(context.Background(), a, Config(), false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(context.Background(), tokens.Access, false)
	if err != nil {
		t.Fatal(err)
	}
	return p, tokens
}
func Devices(t *testing.T, s *store.Store) (store.Device, store.Device, store.Binding) {
	t.Helper()
	ctx := context.Background()
	a := Account(t, s, "devices")
	gp, _ := Principal(t, s, a)
	mp, _ := Principal(t, s, a)
	g, err := s.Enroll(ctx, gp, "gateway", "", "Test desktop", append([]byte{1}, make([]byte, 31)...), 8)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Enroll(ctx, mp, "mobile", "", "", append([]byte{2}, make([]byte, 31)...), 16)
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := s.NewPairing(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.JoinPairing(ctx, pairing.ID, m)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApproveBinding(ctx, b.ID, g, m.PublicKey); err != nil {
		t.Fatal(err)
	}
	b, err = s.Binding(ctx, b.ID, g, false)
	if err != nil {
		t.Fatal(err)
	}
	return g, m, b
}
