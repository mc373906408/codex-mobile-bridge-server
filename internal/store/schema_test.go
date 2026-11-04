package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestCurrentSchemaRejectsRetiredDevelopmentMigrations(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema=current_schema() AND table_name IN ('pairings','email_verification_codes')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("fresh schema contains retired tables", err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES('001_core.sql')"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "unsupported development database schema") {
		t.Fatal("old migration history must fail explicitly instead of claiming compatibility", err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 2 {
		t.Fatal("refusing an old database must not rewrite its migration history", err)
	}
}
