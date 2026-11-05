package store_test

import (
	"context"
	"errors"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
	"sync"
	"testing"
)

func TestRefreshLostResponseRecoveryAndReplay(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	a := testutil.Account(t, s, "refresh-recovery")
	_, first := testutil.Principal(t, s, a)
	request, _ := security.ID()
	next, err := s.Refresh(ctx, first.Refresh, c, request)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Refresh(ctx, first.Refresh, c, request)
	if err != nil || next.Access != recovered.Access || next.Refresh != recovered.Refresh || recovered.ExpiresIn > next.ExpiresIn {
		t.Fatal("rotation was not recovered unchanged", err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM spent_refresh_tokens").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate rotation", count, err)
	}
	another, _ := security.ID()
	if _, err = s.Refresh(ctx, first.Refresh, c, another); !errors.Is(err, store.ErrDenied) {
		t.Fatal("token theft not rejected", err)
	}
	if _, err = s.Authenticate(ctx, next.Access, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("token family remains usable", err)
	}
}

func TestRefreshConcurrentSameAttemptRotatesOnce(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	a := testutil.Account(t, s, "refresh-parallel")
	_, first := testutil.Principal(t, s, a)
	request, _ := security.ID()
	var wg sync.WaitGroup
	tokens := make([]store.Tokens, 8)
	failures := make([]error, 8)
	for i := range tokens {
		wg.Add(1)
		go func() { defer wg.Done(); tokens[i], failures[i] = s.Refresh(ctx, first.Refresh, c, request) }()
	}
	wg.Wait()
	for i := range tokens {
		if failures[i] != nil || tokens[i].Refresh != tokens[0].Refresh {
			t.Fatalf("parallel attempt %d failed: %v", i, failures[i])
		}
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE refresh_results SET expires_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, first.Refresh, c, request); !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired recovery accepted", err)
	}
}
