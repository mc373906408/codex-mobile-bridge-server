package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestSessionCleanupRetainsCurrentDirectoryWithoutGrantingAccess(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a := testutil.Account(t, s, "directory-retention")
	g := enrollDirectoryDevice(t, s, a, "gateway", "Desktop", 1)
	m := enrollDirectoryDevice(t, s, a, "mobile", "Phone", 2)
	b := directory(t, s, m)[0]
	if _, err := s.Pool.Exec(ctx, "UPDATE sessions SET refresh_expires_at=now()-interval '1 year' WHERE id IN ($1,$2)", g.SessionID, m.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM bindings WHERE id=$1 AND status='pending'", b.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("session cleanup removed the device relationship", err)
	}
	if _, err := s.Binding(ctx, b.ID, g, true); !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired credentials authorized the pending device", err)
	}
}
