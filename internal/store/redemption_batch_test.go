package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func requestID(t *testing.T) string {
	t.Helper()
	id, err := security.ID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRedemptionBatchAllowsOneCodePerAccountAndPreservesRetries(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	first := testutil.Account(t, s, "batch-first")
	second := testutil.Account(t, s, "batch-second")
	codes, err := s.GenerateCodes(ctx, first.ID, 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	if codes[0].BatchID == "" || codes[0].BatchID != codes[1].BatchID || codes[1].BatchID != codes[2].BatchID {
		t.Fatal("one generation must have one explicit batch ID")
	}
	request := requestID(t)
	got, err := s.Redeem(ctx, first.ID, codes[0].Code, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{request, requestID(t)} {
		again, err := s.Redeem(ctx, first.ID, codes[0].Code, id)
		if err != nil || again != got {
			t.Fatal("same-code retry must return the original receipt", err)
		}
	}
	if _, err = s.Redeem(ctx, first.ID, codes[1].Code, requestID(t)); !errors.Is(err, store.ErrBatchRedeemed) {
		t.Fatal("second code from the same batch must be rejected", err)
	}
	e, err := s.Entitlement(ctx, first.ID, true)
	if err != nil || !e.ExpiresAt.Equal(got.ExpiresAt) {
		t.Fatal("rejection changed service time", err)
	}
	var used bool
	if err = s.Pool.QueryRow(ctx, "SELECT used_at IS NOT NULL FROM redemption_codes WHERE id=$1", codes[1].ID).Scan(&used); err != nil || used {
		t.Fatal("rejected code was consumed", err)
	}
	if _, err = s.Redeem(ctx, second.ID, codes[1].Code, requestID(t)); err != nil {
		t.Fatal("another account must be able to redeem the batch", err)
	}
	next, err := s.GenerateCodes(ctx, first.ID, 7, 1)
	if err != nil || next[0].BatchID == codes[0].BatchID {
		t.Fatal("separate generation must get a separate batch", err)
	}
	extended, err := s.Redeem(ctx, first.ID, next[0].Code, requestID(t))
	if err != nil || extended.ExpiresAt.Sub(got.ExpiresAt) != 7*24*time.Hour {
		t.Fatal("different batches must extend service", err)
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO redemptions(id,account_id,code_id,batch_id,days,expires_at)
		VALUES($1,$2,$3,$4,7,now()+interval '7 days')`, requestID(t), first.ID, codes[2].ID, codes[2].BatchID)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23505" || pgerr.ConstraintName != "redemptions_account_batch_unique" {
		t.Fatal("database must independently enforce account/batch uniqueness", err)
	}
}

func TestConcurrentDifferentCodesInOneBatchGrantOnlyOnce(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a := testutil.Account(t, s, "parallel-batch")
	codes, err := s.GenerateCodes(ctx, a.ID, 3, 16)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, len(codes))
	start := make(chan struct{})
	for _, code := range codes {
		request := requestID(t)
		wg.Add(1)
		go func(code store.GeneratedCode) {
			defer wg.Done()
			<-start
			_, err := s.Redeem(ctx, a.ID, code.Code, request)
			results <- err
		}(code)
	}
	close(start)
	wg.Wait()
	close(results)
	winners, rejected := 0, 0
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, store.ErrBatchRedeemed) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || rejected != 15 {
		t.Fatal("concurrent batch claim was not exclusive", winners, rejected)
	}
	var count, used, requests int
	if err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM redemptions WHERE account_id=$1),
		(SELECT count(*) FROM redemption_codes WHERE used_by=$1),(SELECT count(*) FROM redeem_requests WHERE account_id=$1)`, a.ID).Scan(&count, &used, &requests); err != nil || count != 1 || used != 1 || requests != 1 {
		t.Fatal("failed claims changed the ledger or consumed codes", err, count, used, requests)
	}
}

func TestBatchMigrationPreservesCodesReceiptsAndExistingClaims(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a := testutil.Account(t, s, "migration-batch")
	// Recreate the deployed 001 schema inside this test's private schema.
	_, err := s.Pool.Exec(ctx, `ALTER TABLE redemptions DROP COLUMN batch_id;
		ALTER TABLE redemption_codes DROP COLUMN batch_id;
		DROP TABLE redemption_batches;
		DELETE FROM schema_migrations WHERE name='002_redemption_batches.sql'`)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{requestID(t), requestID(t), requestID(t)}
	codes := []string{"SYNTHETIC-OLD-CODE-A", "SYNTHETIC-OLD-CODE-B", "SYNTHETIC-OLD-CODE-C"}
	for i := range ids {
		stamp := "2026-11-01T00:00:00Z"
		if i == 2 {
			stamp = "2026-11-02T00:00:00Z"
		}
		if _, err = s.Pool.Exec(ctx, "INSERT INTO redemption_codes(id,digest,days,created_at) VALUES($1,$2,7,$3::timestamptz)", ids[i], store.CodeDigest(codes[i]), stamp); err != nil {
			t.Fatal(err)
		}
	}
	receipt, request := requestID(t), requestID(t)
	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = s.Pool.Exec(ctx, "UPDATE redemption_codes SET used_by=$2,used_at=now() WHERE id=$1", ids[0], a.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, "INSERT INTO redemptions(id,account_id,code_id,days,expires_at) VALUES($1,$2,$3,7,$4)", receipt, a.ID, ids[0], expires)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, "INSERT INTO redeem_requests(account_id,request_id,code_digest,redemption_id) VALUES($1,$2,$3,$4)", a.ID, request, store.CodeDigest(codes[0]), receipt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, "UPDATE entitlements SET expires_at=$2 WHERE account_id=$1", a.ID, expires)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var batches int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM redemption_batches").Scan(&batches); err != nil || batches != 2 {
		t.Fatal("old generation groups were not preserved", err)
	}
	again, err := s.Redeem(ctx, a.ID, codes[0], request)
	if err != nil || again.ID != receipt || !again.ExpiresAt.Equal(expires) {
		t.Fatal("migration changed existing receipt", err)
	}
	if _, err = s.Redeem(ctx, a.ID, codes[1], requestID(t)); !errors.Is(err, store.ErrBatchRedeemed) {
		t.Fatal("historical use did not fence its batch", err)
	}
	other, err := s.Redeem(ctx, a.ID, codes[2], requestID(t))
	if err != nil || !other.ExpiresAt.Equal(expires.Add(7*24*time.Hour)) {
		t.Fatal("another historical batch should remain redeemable", err)
	}
	var unchanged bool
	for i, id := range ids {
		if err = s.Pool.QueryRow(ctx, "SELECT digest=$2 FROM redemption_codes WHERE id=$1", id, store.CodeDigest(codes[i])).Scan(&unchanged); err != nil || !unchanged {
			t.Fatal(fmt.Sprintf("code %d changed during migration", i), err)
		}
	}
}

func TestBatchMigrationRefusesHistoricalDuplicatesWithoutChangingReceipts(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a := testutil.Account(t, s, "duplicate-history")
	_, err := s.Pool.Exec(ctx, `ALTER TABLE redemptions DROP COLUMN batch_id;
		ALTER TABLE redemption_codes DROP COLUMN batch_id;
		DROP TABLE redemption_batches;
		DELETE FROM schema_migrations WHERE name='002_redemption_batches.sql'`)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		codeID := requestID(t)
		_, err = s.Pool.Exec(ctx, `INSERT INTO redemption_codes(id,digest,days,used_by,used_at,created_at)
			VALUES($1,$2,7,$3,now(),'2026-11-01T00:00:00Z')`, codeID, store.CodeDigest(fmt.Sprint("duplicate-fixture-", i)), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Pool.Exec(ctx, `INSERT INTO redemptions(id,account_id,code_id,days,expires_at)
			VALUES($1,$2,$3,7,'2030-01-01T00:00:00Z')`, requestID(t), a.ID, codeID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Migrate(ctx); err == nil {
		t.Fatal("duplicate historical claims must not be silently rewritten")
	}
	var receipts, columns, migrations int
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM redemptions),
		(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='redemption_codes' AND column_name='batch_id'),
		(SELECT count(*) FROM schema_migrations)`).Scan(&receipts, &columns, &migrations)
	if err != nil || receipts != 2 || columns != 0 || migrations != 1 {
		t.Fatal("failed migration was not atomic", err, receipts, columns, migrations)
	}
}
