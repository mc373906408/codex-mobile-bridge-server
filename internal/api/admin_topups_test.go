package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestAdminTopupExpiryIdempotencyAndConcurrency(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a, admin, tokens, cookie := managementFixture(t, s)
	a.Config.Mode = "hosted"
	user := testutil.Account(t, s, "topup@example.test")
	path := "/v1/admin/users/" + user.ID + "/topups"
	for _, state := range []string{"inactive", "expired", "active"} {
		var original *time.Time
		if state != "inactive" {
			value := time.Now().UTC().Truncate(time.Microsecond).Add(-48 * time.Hour)
			if state == "active" {
				value = value.Add(30 * 24 * time.Hour)
			}
			original = &value
		}
		if _, err := s.Pool.Exec(ctx, "UPDATE entitlements SET expires_at=$2 WHERE account_id=$1", user.ID, original); err != nil {
			t.Fatal(err)
		}
		id, _ := security.ID()
		input := map[string]any{"days": 30, "note": "manual order", "request_id": id}
		before := time.Now().UTC()
		response := webRequest(t, a, "POST", path, input, csrfHeaders(a, tokens.CSRF), cookie)
		expectStatus(t, response, 200, "")
		var result store.AdminTopup
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if state == "active" {
			if !result.ExpiresAt.Equal(original.Add(30 * 24 * time.Hour)) {
				t.Fatal("active entitlement was not extended", result)
			}
		} else if result.ExpiresAt.Before(before.Add(30*24*time.Hour)) || result.ExpiresAt.After(time.Now().UTC().Add(30*24*time.Hour)) {
			t.Fatal("server clock not used", result)
		}
		retry := webRequest(t, a, "POST", path, input, csrfHeaders(a, tokens.CSRF), cookie)
		expectStatus(t, retry, 200, "")
		if retry.Body.String() != response.Body.String() {
			t.Fatal("retry changed receipt")
		}
		input["days"] = 31
		expectStatus(t, webRequest(t, a, "POST", path, input, csrfHeaders(a, tokens.CSRF), cookie), 409, "state_conflict")
	}
	other := testutil.Account(t, s, "other-topup@example.test")
	sharedID, _ := security.ID()
	if _, err := s.Topup(ctx, admin, user.ID, sharedID, 1, "scoped"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Topup(ctx, admin, other.ID, sharedID, 1, "scoped"); err != store.ErrConflict {
		t.Fatal("request ID crossed accounts", err)
	}
	grant, err := s.Entitlement(ctx, user.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _ := security.ID()
			_, err := s.Topup(ctx, admin, user.ID, id, 1, "concurrent")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.Entitlement(ctx, user.ID, true)
	if err != nil || !after.ExpiresAt.Equal(grant.ExpiresAt.Add(6*24*time.Hour)) {
		t.Fatal("concurrent grants lost days", err)
	}
	var audits int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE account_id=$1 AND action='entitlement.topup' AND session_id=$2`, user.ID, admin.SessionID).Scan(&audits); err != nil || audits != 10 {
		t.Fatal("exactly one audit per grant required", audits, err)
	}
	if _, err = s.Pool.Exec(ctx, "DELETE FROM accounts WHERE id=$1", user.ID); err != nil {
		t.Fatal("top-up receipts must not block account deletion", err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM admin_topups WHERE account_id=$1", user.ID).Scan(&audits); err != nil || audits != 0 {
		t.Fatal("account deletion left receipts", audits, err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE account_id=$1 AND action='entitlement.topup'", user.ID).Scan(&audits); err != nil || audits != 10 {
		t.Fatal("account deletion removed historical audit", audits, err)
	}
}

func TestAdminTopupAuthorizationAndAuditRollback(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a, admin, tokens, cookie := managementFixture(t, s)
	a.Config.Mode = "hosted"
	user := testutil.Account(t, s, "target@example.test")
	_, ordinary := testutil.Principal(t, s, user)
	id, _ := security.ID()
	input := map[string]any{"days": 30, "request_id": id, "note": "support"}
	path := "/v1/admin/users/" + user.ID + "/topups"
	expectStatus(t, webRequest(t, a, "POST", path, input, nil, cookie), 403, "csrf_required")
	headers := csrfHeaders(a, tokens.CSRF)
	headers["Origin"] = "https://other.example.test"
	expectStatus(t, webRequest(t, a, "POST", path, input, headers, cookie), 403, "csrf_required")
	expectStatus(t, webRequest(t, a, "POST", path, input, map[string]string{"Authorization": "Bearer " + ordinary.Access}), 403, "access_denied")
	for _, days := range []any{0, -1, 3651, 1.5, "30"} {
		input["days"] = days
		expectStatus(t, webRequest(t, a, "POST", path, input, csrfHeaders(a, tokens.CSRF), cookie), 400, "")
	}
	input["days"] = 30
	a.Config.Mode = "selfhost"
	expectStatus(t, webRequest(t, a, "POST", path, input, csrfHeaders(a, tokens.CSRF), cookie), 404, "topup_disabled")
	a.Config.Mode = "hosted"
	for _, field := range []string{"disabled", "administrator"} {
		if _, err := s.Pool.Exec(ctx, fmt.Sprintf("UPDATE accounts SET %s=true WHERE id=$1", field), user.ID); err != nil {
			t.Fatal(err)
		}
		expectStatus(t, webRequest(t, a, "POST", path, input, csrfHeaders(a, tokens.CSRF), cookie), 403, "access_denied")
		if _, err := s.Pool.Exec(ctx, fmt.Sprintf("UPDATE accounts SET %s=false WHERE id=$1", field), user.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE accounts SET verified=false WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, webRequest(t, a, "POST", path, input, csrfHeaders(a, tokens.CSRF), cookie), 403, "access_denied")
	if _, err := s.Pool.Exec(ctx, "UPDATE accounts SET verified=true WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `CREATE FUNCTION reject_topup_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic'; END $$; CREATE TRIGGER reject_topup_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_topup_audit()`); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, webRequest(t, a, "POST", path, input, csrfHeaders(a, tokens.CSRF), cookie), 500, "unavailable")
	var expiry *time.Time
	var count int
	if err := s.Pool.QueryRow(ctx, "SELECT expires_at FROM entitlements WHERE account_id=$1", user.ID).Scan(&expiry); err != nil || expiry != nil {
		t.Fatal("audit failure committed days", err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM admin_topups").Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure committed receipt", err)
	}
	if _, err := s.Pool.Exec(ctx, "DROP TRIGGER reject_topup_audit ON audit_log; DROP FUNCTION reject_topup_audit()"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE accounts SET administrator=false WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Topup(ctx, admin, user.ID, id, 30, ""); err != store.ErrDenied {
		t.Fatal("stale principal retained admin permission", err)
	}
}
