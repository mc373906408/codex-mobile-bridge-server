package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestAdministratorResetPreservesAccountKeysAndBindings(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	if err := s.Bootstrap(ctx, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	a, err := s.AccountByLogin(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, web := range []bool{true, false} {
		if _, err := s.NewSession(ctx, a, c, web); !errors.Is(err, store.ErrDenied) {
			t.Fatal("first-login admin must not create any regular session", err)
		}
	}
	setup, err := s.NewAdministratorSetup(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := security.Password("changed-test-password-123")
	id, err := s.FinishAdministratorSetup(ctx, setup.Token, "operator", hash)
	if err != nil || id != a.ID {
		t.Fatal("setup must preserve account identity", err)
	}
	a, err = s.AccountByLogin(ctx, "operator")
	if err != nil || a.CredentialsChangeRequired {
		t.Fatal(err)
	}
	p, tokens := testutil.Principal(t, s, a)
	webTokens, err := s.NewSession(ctx, a, c, true)
	if err != nil {
		t.Fatal("initialized administrators must support website sessions", err)
	}
	key := append([]byte{1}, make([]byte, 31)...)
	g, err := s.Enroll(ctx, p, "gateway", "", "Test desktop", key, 8)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Enroll(ctx, p, "mobile", "", "", append([]byte{2}, make([]byte, 31)...), 16)
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
	if err = s.ResetAdministrator(ctx); err != nil {
		t.Fatal(err)
	}
	reset, err := s.AccountByLogin(ctx, "admin")
	if err != nil || reset.ID != a.ID || !reset.CredentialsChangeRequired || !security.VerifyPassword("admin", reset.PasswordHash) {
		t.Fatal("reset must restore setup-only default credentials without changing account ID", err)
	}
	if _, err = s.Authenticate(ctx, tokens.Access, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("reset must invalidate old client sessions", err)
	}
	if _, err = s.Authenticate(ctx, webTokens.Web, true); !errors.Is(err, store.ErrDenied) {
		t.Fatal("reset must invalidate old website sessions", err)
	}
	if _, err = s.Refresh(ctx, tokens.Refresh, c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("reset must invalidate refresh tokens", err)
	}
	var matches bool
	if err = s.Pool.QueryRow(ctx, "SELECT account_id=$2 AND control_public_key=$3 FROM gateways WHERE id=$1", g.ID, a.ID, key).Scan(&matches); err != nil || !matches {
		t.Fatal("reset must preserve gateway identity and public keys", err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT gateway_id=$2 AND installation_id=$3 AND status='active' FROM bindings WHERE id=$1", b.ID, g.ID, m.ID).Scan(&matches); err != nil || !matches {
		t.Fatal("reset must not delete or revoke the approved binding", err)
	}
	if _, err = s.Device(ctx, g.Token, "gateway"); !errors.Is(err, store.ErrDenied) {
		t.Fatal("old device authorization must require reauthentication", err)
	}
	setup, err = s.NewAdministratorSetup(ctx, reset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishAdministratorSetup(ctx, setup.Token, "admin", hash); err != nil {
		t.Fatal(err)
	}
	reset, _ = s.AccountByLogin(ctx, "admin")
	p, _ = testutil.Principal(t, s, reset)
	g, err = s.Enroll(ctx, p, "gateway", g.ID, g.Name, key, 8)
	if err != nil || g.ID == "" {
		t.Fatal("the same locally held key must allow reenrollment of the existing gateway", err)
	}
}

func TestConcurrentAdministratorSetupIsSingleUseAndExpires(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.AccountByLogin(ctx, "admin")
	expired, err := s.NewAdministratorSetup(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE administrator_setup_sessions SET expires_at=now()-interval '1 second' WHERE digest=$1", security.Digest(expired.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdministratorSetup(ctx, expired.Token); !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired setup sessions must be rejected", err)
	}
	hash, _ := security.Password("changed-test-password-123")
	if _, err = s.FinishAdministratorSetup(ctx, expired.Token, "operator", hash); !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired setup sessions must not change credentials", err)
	}
	first, err := s.NewAdministratorSetup(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.NewAdministratorSetup(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, setup := range []store.AdministratorSetup{first, second} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := s.FinishAdministratorSetup(ctx, token, "operator", hash)
			results <- err
		}(setup.Token)
	}
	wg.Wait()
	close(results)
	succeeded, denied := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, store.ErrDenied) {
			denied++
		} else {
			t.Fatal("concurrent setup failed unexpectedly", err)
		}
	}
	if succeeded != 1 || denied != 1 {
		t.Fatal("exactly one setup may succeed", succeeded, denied)
	}
	for _, setup := range []store.AdministratorSetup{first, second} {
		if _, err := s.AdministratorSetup(ctx, setup.Token); !errors.Is(err, store.ErrDenied) {
			t.Fatal("all setup sessions must be invalidated after a credential change", err)
		}
	}
	if err = s.Bootstrap(ctx, "different-admin", "different-test-password-123"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM accounts WHERE administrator").Scan(&count); err != nil || count != 1 {
		t.Fatal("deployment changes must not create a second administrator", err)
	}
}
