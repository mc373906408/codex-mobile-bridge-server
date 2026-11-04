package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func managementFixture(t *testing.T, s *store.Store) (*Server, store.Principal, store.Tokens, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	admin := testutil.Account(t, s, "management-admin")
	if _, err := s.Pool.Exec(ctx, "UPDATE accounts SET administrator=true WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	admin.Administrator = true
	c := testutil.Config()
	c.Umeng.AppKey, c.Umeng.MasterSecret = "synthetic-app", "synthetic-secret"
	tokens, err := s.NewSession(ctx, admin, c, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, tokens.Web, true)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	return a, p, tokens, &http.Cookie{Name: a.cookieName(), Value: tokens.Web}
}

func pageItems(t *testing.T, a *Server, cookie *http.Cookie, path, key string) ([]map[string]any, string) {
	t.Helper()
	w := webRequest(t, a, "GET", path, nil, nil, cookie)
	expectStatus(t, w, 200, "")
	var value map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	var next string
	if err := json.Unmarshal(value[key], &items); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(value["next_cursor"], &next); err != nil {
		t.Fatal(err)
	}
	return items, next
}

func TestAdministratorTargetOwnershipAndAuditRollback(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a, admin, tokens, cookie := managementFixture(t, s)
	g, m, b := testutil.Devices(t, s)
	owner, err := s.AccountByLogin(ctx, "devices")
	if err != nil {
		t.Fatal(err)
	}
	_, ownerTokens := testutil.Principal(t, s, owner)
	other := testutil.Account(t, s, "other-owner")
	base := "/v1/admin/users/" + owner.ID
	for path, key := range map[string]string{"/devices?role=gateway": "devices", "/devices?role=mobile": "devices", "/bindings": "bindings", "/sessions": "sessions"} {
		items, _ := pageItems(t, a, cookie, base+path, key)
		if len(items) == 0 {
			t.Fatal("administrator cannot inspect another account", path)
		}
		payload, _ := json.Marshal(items)
		for _, secret := range []string{g.Token, m.Token, tokens.Access, tokens.Refresh, ownerTokens.Access, base64.StdEncoding.EncodeToString(g.PublicKey)} {
			if strings.Contains(string(payload), secret) {
				t.Fatal("management metadata leaked a credential or key", path)
			}
		}
		expectStatus(t, webRequest(t, a, "GET", base+path, nil, map[string]string{"Authorization": "Bearer " + ownerTokens.Access}), 403, "access_denied")
	}
	self := webRequest(t, a, "GET", "/v1/gateways", nil, nil, cookie)
	expectStatus(t, self, 200, "")
	if strings.Contains(self.Body.String(), g.ID) {
		t.Fatal("ordinary route expanded administrator scope")
	}
	path := base + "/bindings/" + b.ID
	expectStatus(t, webRequest(t, a, "DELETE", path, nil, nil, cookie), 403, "csrf_required")
	expectStatus(t, webRequest(t, a, "DELETE", "/v1/admin/users/"+other.ID+"/bindings/"+b.ID, nil, csrfHeaders(a, tokens.CSRF), cookie), 403, "access_denied")
	if _, err = s.Pool.Exec(ctx, `CREATE FUNCTION reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$;
		CREATE TRIGGER reject_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_audit()`); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, webRequest(t, a, "DELETE", path, nil, csrfHeaders(a, tokens.CSRF), cookie), 500, "unavailable")
	current, err := s.DirectoryBinding(ctx, b.ID, m)
	if err != nil || current.Status != "active" || current.AccessRevision != b.AccessRevision {
		t.Fatal("audit failure did not roll back authorization change", err)
	}
	if _, err = s.Pool.Exec(ctx, "DROP TRIGGER reject_audit ON audit_log; DROP FUNCTION reject_audit()"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, webRequest(t, a, "DELETE", path, nil, csrfHeaders(a, tokens.CSRF), cookie), 200, "")
	expectStatus(t, webRequest(t, a, "DELETE", path, nil, csrfHeaders(a, tokens.CSRF), cookie), 200, "")
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE actor_id=$1 AND account_id=$2 AND session_id=$3
		AND action='binding.revoke' AND target_id=$4 AND details->'before'->>'status'='active'
		AND details->'after'->>'status'='revoked'`, admin.ID, owner.ID, admin.SessionID, b.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("revocation must have one scoped transition, not one event per retry", err, count)
	}
	// An authenticated snapshot does not retain administrator power after role/session changes.
	if _, err = s.Pool.Exec(ctx, "UPDATE accounts SET administrator=false WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeResource(ctx, admin, owner.ID, "gateway", g.ID); err != store.ErrDenied {
		t.Fatal("stale administrator principal granted access", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE accounts SET administrator=true WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE sessions SET web_expires_at=now()-interval '1 second' WHERE id=$1", admin.SessionID); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeResource(ctx, admin, owner.ID, "gateway", g.ID); err != store.ErrDenied {
		t.Fatal("expired administrator web principal granted access", err)
	}
}

func TestAdministratorPaginationSearchAndCursorScopes(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a, _, _, cookie := managementFixture(t, s)
	if _, err := s.Pool.Exec(ctx, `WITH inserted AS (INSERT INTO accounts(id,login,password_hash,verified,created_at)
		SELECT gen_random_uuid(),'page-user-'||lpad(n::text,3,'0'),'synthetic-no-login',true,'2030-01-01'::timestamptz
		FROM generate_series(0,214) n RETURNING id) INSERT INTO entitlements(account_id) SELECT id FROM inserted`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	next := ""
	firstCursor := ""
	for {
		path := "/v1/admin/users?q=page-user-&limit=37"
		if next != "" {
			path += "&cursor=" + url.QueryEscape(next)
		}
		items, cursor := pageItems(t, a, cookie, path, "users")
		if len(items) > 37 || len(items) == 0 {
			t.Fatal("unexpected user page size", len(items))
		}
		for _, item := range items {
			id := item["id"].(string)
			if seen[id] {
				t.Fatal("pagination repeated a tied timestamp row")
			}
			seen[id] = true
		}
		if firstCursor == "" {
			firstCursor = cursor
		}
		next = cursor
		if next == "" {
			break
		}
	}
	if len(seen) != 215 {
		t.Fatal("pagination truncated accounts beyond the old limit", len(seen))
	}
	items, _ := pageItems(t, a, cookie, "/v1/admin/users?q=page-user-214", "users")
	if len(items) != 1 || items[0]["login"] != "page-user-214" {
		t.Fatal("server search missed an unloaded account")
	}
	for _, path := range []string{"/v1/admin/users?limit=0", "/v1/admin/users?limit=201", "/v1/admin/users?cursor=bad",
		"/v1/admin/users?q=other&cursor=" + firstCursor, "/v1/admin/audit?cursor=" + firstCursor} {
		expectStatus(t, webRequest(t, a, "GET", path, nil, nil, cookie), 400, "invalid_page")
	}
	for _, path := range []string{"/v1/admin/users?token=secret", "/v1/account?limit=1", "/v1/ws/mobile?cursor=x", "/v1/admin/status?limit=1"} {
		expectStatus(t, webRequest(t, a, "GET", path, nil, nil, cookie), 400, "query_not_allowed")
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO audit_log(action,details) SELECT 'synthetic.page','{}' FROM generate_series(1,25)"); err != nil {
		t.Fatal(err)
	}
	previous := int64(1<<63 - 1)
	total := 0
	next = ""
	for {
		path := "/v1/admin/audit?limit=7"
		if next != "" {
			path += "&cursor=" + next
		}
		items, cursor := pageItems(t, a, cookie, path, "audit")
		for _, item := range items {
			id, err := strconv.ParseInt(item["id"].(string), 10, 64)
			if err != nil || id >= previous {
				t.Fatal("audit IDs must be numerically ordered and never rounded", err)
			}
			previous = id
			total++
		}
		next = cursor
		if next == "" {
			break
		}
	}
	if total < 25 {
		t.Fatal("audit pagination omitted records")
	}
}

func TestAdministratorRevokesPhoneAndCancelsItsPushJobs(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a, admin, tokens, cookie := managementFixture(t, s)
	g, m, b := testutil.Devices(t, s)
	input := store.EndpointInput{BindingID: b.ID, Platform: "umeng", AppID: a.Config.Umeng.AppKey,
		Token: "synthetic-token-1234567890", Enabled: true, Consent: true}
	endpoint, err := s.RegisterEndpoint(ctx, m, input, a.Config)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"queued", "leased"} {
		id, _ := security.ID()
		job, err := s.QueuePush(ctx, g, store.Envelope{Version: 1, BindingID: b.ID, NotificationID: id,
			ExpiresAt: time.Now().Unix() + 600, Ciphertext: base64.StdEncoding.EncodeToString(make([]byte, 80))}, endpoint.Revision, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Pool.Exec(ctx, "UPDATE push_jobs SET status=$2 WHERE id=$1", job, state); err != nil {
			t.Fatal(err)
		}
	}
	path := "/v1/admin/users/" + m.AccountID + "/installations/" + m.ID
	expectStatus(t, webRequest(t, a, "DELETE", path, nil, csrfHeaders(a, tokens.CSRF), cookie), 200, "")
	if _, err = s.Device(ctx, m.Token, "mobile"); err != store.ErrDenied {
		t.Fatal("revoked phone credential remains usable", err)
	}
	var cancelled int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM push_jobs WHERE binding_id=$1 AND status='cancelled'", b.ID).Scan(&cancelled); err != nil || cancelled != 2 {
		t.Fatal("revocation left queued/leased notifications", err)
	}
	var enabled bool
	if err = s.Pool.QueryRow(ctx, "SELECT enabled OR consent FROM push_endpoints WHERE binding_id=$1", b.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("revocation left endpoint consent", err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='device.revoke' AND actor_id=$1 AND account_id=$2 AND target_id=$3", admin.ID, m.AccountID, m.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("device revocation has no administrator/account audit", err)
	}
	if _, err = s.Device(ctx, g.Token, "gateway"); err != nil {
		t.Fatal("phone revocation also revoked its desktop", err)
	}
}
