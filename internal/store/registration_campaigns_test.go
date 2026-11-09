package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func campaignAdmin(t *testing.T, s *store.Store) store.Principal {
	t.Helper()
	a := testutil.Account(t, s, "campaign-admin-"+requestID(t))
	if _, err := s.Pool.Exec(context.Background(), "UPDATE accounts SET administrator=true WHERE id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	a.Administrator = true
	p, _ := testutil.Principal(t, s, a)
	return p
}
func currentCampaign(t *testing.T, s *store.Store) string {
	t.Helper()
	var id string
	if err := s.Pool.QueryRow(context.Background(), "SELECT id FROM registration_campaigns WHERE state<>'ended'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func configureCampaign(t *testing.T, s *store.Store, p store.Principal, in store.CampaignInput) string {
	t.Helper()
	id := currentCampaign(t, s)
	if _, err := s.ChangeRegistrationCampaign(context.Background(), p, id, "edit", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeRegistrationCampaign(context.Background(), p, id, "activate", store.CampaignInput{}); err != nil {
		t.Fatal(err)
	}
	return id
}
func completeSignup(t *testing.T, s *store.Store, login string) store.Account {
	t.Helper()
	proof := signupProof(t, s, login)
	a, err := s.CompleteRegistration(context.Background(), login, proof.Token, "synthetic-password-hash", emailConfig())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRegistrationCampaignMigrationAndDraftDoNotGrant(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	p := campaignAdmin(t, s)
	old := testutil.Account(t, s, "preserved-user")
	if _, err := s.Pool.Exec(ctx, `UPDATE entitlements SET expires_at='2030-01-01' WHERE account_id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	codes, err := s.GenerateCodes(ctx, p.ID, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	redeemed, err := s.Redeem(ctx, old.ID, codes[0].Code, requestID(t))
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the deployed 004 shape in this isolated schema, then upgrade twice.
	if _, err := s.Pool.Exec(ctx, `DROP TABLE registration_campaign_grants; DROP FUNCTION preserve_registration_grant(); DROP TABLE registration_campaigns;
		DELETE FROM schema_migrations WHERE name='005_registration_campaigns.sql'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var n, days int
	var state string
	if err := s.Pool.QueryRow(ctx, "SELECT count(*),min(days),min(state) FROM registration_campaigns").Scan(&n, &days, &state); err != nil || n != 1 || days != 7 || state != "draft" {
		t.Fatal(n, days, state, err)
	}
	e, err := s.Entitlement(ctx, old.ID, true)
	if err != nil || !e.ExpiresAt.Equal(redeemed.ExpiresAt) {
		t.Fatal(e, err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM redemptions WHERE id=$1", redeemed.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("migration lost redemption history", n, err)
	}
	a := completeSignup(t, s, "draft@example.test")
	e, err = s.Entitlement(ctx, a.ID, true)
	if err != nil || e.Active || e.ExpiresAt != nil {
		t.Fatal("draft granted service", e, err)
	}
	id := currentCampaign(t, s)
	if _, err = s.ChangeRegistrationCampaign(ctx, p, id, "stop", store.CampaignInput{}); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM registration_campaigns WHERE state<>'ended'").Scan(&n); err != nil || n != 0 {
		t.Fatal("migration recreated stopped campaign", n, err)
	}
	if _, err = s.CreateRegistrationCampaign(ctx, p, store.CampaignInput{Name: "next", Days: 7}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationTrialAtomicOnceAndRedemptionExtends(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	p := campaignAdmin(t, s)
	oldProof := signupProof(t, s, "existing@example.test")
	old := testutil.Account(t, s, "existing@example.test")
	id := configureCampaign(t, s, p, store.CampaignInput{Name: "trial", Days: 7})
	if _, err := s.CompleteRegistration(ctx, old.Login, oldProof.Token, "cannot-replace", emailConfig()); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old account re-registered", err)
	}
	oldEntitlement, err := s.Entitlement(ctx, old.ID, true)
	if err != nil || oldEntitlement.ExpiresAt != nil {
		t.Fatal("old account was backfilled", oldEntitlement, err)
	}
	proof := signupProof(t, s, "trial@example.test")
	var n int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM registration_campaign_grants").Scan(&n); err != nil || n != 0 {
		t.Fatal("proof reserved a grant", n, err)
	}
	a, err := s.CompleteRegistration(ctx, "trial@example.test", proof.Token, "synthetic-hash", emailConfig())
	if err != nil {
		t.Fatal(err)
	}
	var granted, expires time.Time
	var days int
	if err = s.Pool.QueryRow(ctx, "SELECT days,granted_at,expires_at FROM registration_campaign_grants WHERE account_id=$1 AND campaign_id=$2", a.ID, id).Scan(&days, &granted, &expires); err != nil || days != 7 || expires.Sub(granted) != 7*24*time.Hour {
		t.Fatal(days, granted, expires, err)
	}
	e, err := s.Entitlement(ctx, a.ID, true)
	if err != nil || !e.Active || !e.ExpiresAt.Equal(expires) {
		t.Fatal(e, err)
	}
	if _, err = s.CompleteRegistration(ctx, a.Login, proof.Token, "do-not-replace", emailConfig()); !errors.Is(err, store.ErrDenied) {
		t.Fatal(err)
	}
	if _, err = s.ChangeRegistrationCampaign(ctx, p, id, "stop", store.CampaignInput{}); err != nil {
		t.Fatal(err)
	}
	e, _ = s.Entitlement(ctx, a.ID, true)
	if !e.ExpiresAt.Equal(expires) {
		t.Fatal("stop reclaimed trial")
	}
	codes, err := s.GenerateCodes(ctx, p.ID, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Redeem(ctx, a.ID, codes[0].Code, requestID(t))
	if err != nil || r.ExpiresAt.Sub(expires) != 3*24*time.Hour {
		t.Fatal(r, err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE entitlements SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	e, err = s.Entitlement(ctx, a.ID, true)
	if err != nil || e.Active {
		t.Fatal("expired trial remains active", e, err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM sessions WHERE account_id=$1)+(SELECT count(*) FROM gateways WHERE account_id=$1)+(SELECT count(*) FROM installations WHERE account_id=$1)", a.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("trial created device/session authority", n, err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO registration_campaign_grants(id,campaign_id,account_id,days,granted_at,expires_at) VALUES(gen_random_uuid(),$1,$2,7,now(),now()+interval '7 days')`, id, a.ID); err == nil {
		t.Fatal("database allowed duplicate account grant")
	}
}

func TestRegistrationCampaignFinalSlotAndProofConcurrency(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	p := campaignAdmin(t, s)
	limit := 1
	id := configureCampaign(t, s, p, store.CampaignInput{Name: "last slot", Days: 7, MaxGrants: &limit})
	proofs := make([]store.RegistrationProof, 12)
	for i := range proofs {
		proofs[i] = signupProof(t, s, fmt.Sprintf("race-%d@example.test", i))
	}
	var wg sync.WaitGroup
	for i := range proofs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.CompleteRegistration(ctx, fmt.Sprintf("race-%d@example.test", i), proofs[i].Token, "test-hash", emailConfig()); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var granted, accounts int
	var state, reason string
	if err := s.Pool.QueryRow(ctx, "SELECT granted_count,state,end_reason FROM registration_campaigns WHERE id=$1", id).Scan(&granted, &state, &reason); err != nil || granted != 1 || state != "ended" || reason != "exhausted" {
		t.Fatal(granted, state, reason, err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM accounts WHERE login LIKE 'race-%'").Scan(&accounts); err != nil || accounts != 12 {
		t.Fatal("exhaustion blocked registration", accounts, err)
	}
	if _, err := s.CreateRegistrationCampaign(ctx, p, store.CampaignInput{Name: "next", Days: 5}); err != nil {
		t.Fatal(err)
	}
	configureCampaign(t, s, p, store.CampaignInput{Name: "next", Days: 5})
	proof := signupProof(t, s, "same-proof@example.test")
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CompleteRegistration(ctx, "same-proof@example.test", proof.Token, "test-hash", emailConfig())
			if err != nil && !errors.Is(err, store.ErrDenied) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM registration_campaign_grants").Scan(&granted); err != nil || granted != 2 {
		t.Fatal("proof replay duplicated gift", granted, err)
	}
}

func TestRegistrationCampaignDatesStopAndImmutableRules(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	p := campaignAdmin(t, s)
	future := time.Now().Add(time.Hour)
	id := configureCampaign(t, s, p, store.CampaignInput{Name: "future", Days: 7, StartsAt: &future})
	a := completeSignup(t, s, "before@example.test")
	e, _ := s.Entitlement(ctx, a.ID, true)
	if e.Active {
		t.Fatal("scheduled gift early")
	}
	if _, err := s.ChangeRegistrationCampaign(ctx, p, id, "edit", store.CampaignInput{Name: "changed", Days: 9}); !errors.Is(err, store.ErrCampaignState) {
		t.Fatal(err)
	}
	if _, err := s.ChangeRegistrationCampaign(ctx, p, id, "activate", store.CampaignInput{}); err != nil {
		t.Fatal("activation retry", err)
	}
	if _, err := s.CreateRegistrationCampaign(ctx, p, store.CampaignInput{Name: "overlap", Days: 7}); !errors.Is(err, store.ErrCampaignOpen) {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE registration_campaigns SET starts_at=clock_timestamp() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	a = completeSignup(t, s, "after-start@example.test")
	e, _ = s.Entitlement(ctx, a.ID, true)
	if !e.Active {
		t.Fatal("active gift missing")
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE registration_campaigns SET starts_at=clock_timestamp()-interval '1 day',ends_at=clock_timestamp() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	a = completeSignup(t, s, "at-end@example.test")
	e, _ = s.Entitlement(ctx, a.ID, true)
	if e.Active {
		t.Fatal("deadline included")
	}
	for range 2 {
		if _, err := s.ChangeRegistrationCampaign(ctx, p, id, "stop", store.CampaignInput{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ChangeRegistrationCampaign(ctx, p, id, "activate", store.CampaignInput{}); !errors.Is(err, store.ErrCampaignState) {
		t.Fatal("ended campaign resumed", err)
	}
	next, err := s.CreateRegistrationCampaign(ctx, p, store.CampaignInput{Name: "next", Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeRegistrationCampaign(ctx, p, next.ID, "activate", store.CampaignInput{}); err != nil {
		t.Fatal(err)
	}
	// Create settles natural expiry even without registration traffic or a worker.
	if _, err = s.Pool.Exec(ctx, "UPDATE registration_campaigns SET starts_at=clock_timestamp()-interval '1 day',ends_at=clock_timestamp()-interval '1 second' WHERE id=$1", next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRegistrationCampaign(ctx, p, store.CampaignInput{Name: "third", Days: 7}); err != nil {
		t.Fatal("expiry needs background worker", err)
	}
	var n int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='campaign.activate' AND target_id=$1", id).Scan(&n); err != nil || n != 1 {
		t.Fatal("repeat activation duplicated audit", n, err)
	}
}

func TestRegistrationCampaignAuditFailureRollsBackAndStaleAdminDenied(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	p := campaignAdmin(t, s)
	limit := 1
	id := configureCampaign(t, s, p, store.CampaignInput{Name: "atomic", Days: 7, MaxGrants: &limit})
	proof := signupProof(t, s, "rollback@example.test")
	if _, err := s.Pool.Exec(ctx, `CREATE FUNCTION reject_trial_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.action IN ('entitlement.registration_trial','campaign.end') THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_trial_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_trial_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteRegistration(ctx, "rollback@example.test", proof.Token, "test-hash", emailConfig()); err == nil {
		t.Fatal("audit failure silently skipped")
	}
	if _, err := s.AccountByLogin(ctx, "rollback@example.test"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("account leaked from rollback", err)
	}
	if _, err := s.ChangeRegistrationCampaign(ctx, p, id, "stop", store.CampaignInput{}); err == nil {
		t.Fatal("admin audit failure ignored")
	}
	var count int
	var state string
	if err := s.Pool.QueryRow(ctx, "SELECT granted_count,state FROM registration_campaigns WHERE id=$1", id).Scan(&count, &state); err != nil || count != 0 || state != "enabled" {
		t.Fatal(count, state, err)
	}
	// Fail after the grant and exhaustion have both been written, at final account audit.
	if _, err := s.Pool.Exec(ctx, `CREATE OR REPLACE FUNCTION reject_trial_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.action='account.create' THEN RAISE EXCEPTION 'synthetic final audit failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteRegistration(ctx, "rollback@example.test", proof.Token, "test-hash", emailConfig()); err == nil {
		t.Fatal("final audit failure ignored")
	}
	if err := s.Pool.QueryRow(ctx, "SELECT granted_count,state FROM registration_campaigns WHERE id=$1", id).Scan(&count, &state); err != nil || count != 0 || state != "enabled" {
		t.Fatal("late failure retained grant/exhaustion", count, state, err)
	}
	if _, err := s.Pool.Exec(ctx, "DROP TRIGGER reject_trial_audit ON audit_log; DROP FUNCTION reject_trial_audit()"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteRegistration(ctx, "rollback@example.test", proof.Token, "test-hash", emailConfig()); err != nil {
		t.Fatal("rollback consumed proof", err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE accounts SET administrator=false WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeRegistrationCampaign(ctx, p, id, "stop", store.CampaignInput{}); !errors.Is(err, store.ErrDenied) {
		t.Fatal("stale admin changed activity", err)
	}
}

func TestRegistrationCampaignConcurrentCreationAndDatabaseConstraint(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	p := campaignAdmin(t, s)
	id := currentCampaign(t, s)
	if _, err := s.ChangeRegistrationCampaign(ctx, p, id, "stop", store.CampaignInput{}); err != nil {
		t.Fatal(err)
	}
	actors := []store.Principal{p, campaignAdmin(t, s), campaignAdmin(t, s)}
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateRegistrationCampaign(ctx, actors[i%len(actors)], store.CampaignInput{Name: "next", Days: 7})
			if err != nil && !errors.Is(err, store.ErrCampaignOpen) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM registration_campaigns WHERE state<>'ended'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	_, err := s.Pool.Exec(ctx, "INSERT INTO registration_campaigns(id,name,days) VALUES(gen_random_uuid(),'bypass',7)")
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.ConstraintName != "registration_campaign_single_open" {
		t.Fatal("missing single-open database constraint", err)
	}
}

func TestRegistrationCampaignStopAndRegistrationSerialize(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	p := campaignAdmin(t, s)
	id := configureCampaign(t, s, p, store.CampaignInput{Name: "stop race", Days: 7})
	proof := signupProof(t, s, "stop-race@example.test")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.CompleteRegistration(ctx, "stop-race@example.test", proof.Token, "hash", emailConfig())
		if err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		_, err := s.ChangeRegistrationCampaign(ctx, p, id, "stop", store.CampaignInput{})
		if err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	var invalid int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM registration_campaign_grants g JOIN registration_campaigns c ON c.id=g.campaign_id WHERE g.granted_at>c.ended_at`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatal("grant occurred after stop", invalid, err)
	}
	a := completeSignup(t, s, "stopped@example.test")
	e, err := s.Entitlement(ctx, a.ID, true)
	if err != nil || e.Active {
		t.Fatal("registration after stop was gifted", e, err)
	}
}

func TestRegistrationCampaignExactDatabaseClockBoundaries(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	p := campaignAdmin(t, s)
	// Override only this isolated schema's database clock so equality is tested
	// without timing sleeps or a production test hook. pg_catalog remains explicit.
	if _, err := s.Pool.Exec(ctx, `CREATE TABLE campaign_test_clock(stamp timestamptz NOT NULL);
		INSERT INTO campaign_test_clock VALUES(date_trunc('second',clock_timestamp()));
		CREATE FUNCTION clock_timestamp() RETURNS timestamptz LANGUAGE sql VOLATILE AS 'SELECT stamp FROM campaign_test_clock'`); err != nil {
		t.Fatal(err)
	}
	poolConfig := s.Pool.Config()
	poolConfig.ConnConfig.RuntimeParams["search_path"] += ",pg_catalog"
	controlled, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	s.Pool.Close()
	s.Pool = controlled
	var now time.Time
	if err = s.Pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	start, end := now.Add(time.Minute), now.Add(2*time.Minute)
	configureCampaign(t, s, p, store.CampaignInput{Name: "exact clock", Days: 7, StartsAt: &start, EndsAt: &end})
	for i, stamp := range []time.Time{start.Add(-time.Microsecond), start, end.Add(-time.Microsecond), end} {
		if _, err = s.Pool.Exec(ctx, "UPDATE campaign_test_clock SET stamp=$1", stamp); err != nil {
			t.Fatal(err)
		}
		a := completeSignup(t, s, fmt.Sprintf("boundary-%d@example.test", i))
		e, err := s.Entitlement(ctx, a.ID, true)
		want := i == 1 || i == 2
		if err != nil || e.Active != want {
			t.Fatalf("at %s active=%v want=%v: %v", stamp, e.Active, want, err)
		}
		if want && !e.ExpiresAt.Equal(stamp.Add(7*24*time.Hour)) {
			t.Fatal("gift uses wrong clock", e)
		}
	}
}
