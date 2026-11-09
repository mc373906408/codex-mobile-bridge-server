package api

import (
	"context"
	"encoding/json"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestRegistrationCampaignAdministrationLifecycle(t *testing.T) {
	s := testutil.Database(t)
	a, _, tokens, cookie := managementFixture(t, s)
	a.Config.Mode = "hosted"
	base := "/v1/admin/registration-campaigns"
	headers := csrfHeaders(a, tokens.CSRF)
	rows, _ := pageItems(t, a, cookie, base, "campaigns")
	if len(rows) != 1 || rows[0]["status"] != "draft" {
		t.Fatal(rows)
	}
	id := rows[0]["id"].(string)
	for _, input := range []map[string]any{{"name": "bad", "days": 0}, {"name": "bad", "days": 3651}, {"name": "bad", "days": 7, "max_grants": 0}, {"name": "bad", "days": 7, "starts_at": "2030-01-02T00:00:00Z", "ends_at": "2030-01-01T00:00:00Z"}} {
		expectStatus(t, webRequest(t, a, "PATCH", base+"/"+id, input, headers, cookie), 400, "invalid_registration_campaign")
	}
	in := map[string]any{"name": "Trial", "days": 7, "starts_at": "2090-01-01T00:00:00Z", "max_grants": 2}
	expectStatus(t, webRequest(t, a, "PATCH", base+"/"+id, in, headers, cookie), 200, "")
	for range 2 {
		expectStatus(t, webRequest(t, a, "POST", base+"/"+id+"/activate", map[string]any{}, headers, cookie), 200, "")
	}
	rows, _ = pageItems(t, a, cookie, base, "campaigns")
	if rows[0]["status"] != "scheduled" || rows[0]["remaining_grants"] != float64(2) || rows[0]["granted_days"] != float64(0) {
		t.Fatal(rows)
	}
	expectStatus(t, webRequest(t, a, "PATCH", base+"/"+id, in, headers, cookie), 409, "registration_campaign_locked")
	expectStatus(t, webRequest(t, a, "POST", base, in, headers, cookie), 409, "registration_campaign_already_open")
	for range 2 {
		expectStatus(t, webRequest(t, a, "POST", base+"/"+id+"/stop", map[string]any{}, headers, cookie), 200, "")
	}
	rows, _ = pageItems(t, a, cookie, base, "campaigns")
	if rows[0]["status"] != "ended" || rows[0]["end_reason"] != "stopped" {
		t.Fatal(rows)
	}
	expectStatus(t, webRequest(t, a, "POST", base+"/"+id+"/activate", map[string]any{}, headers, cookie), 409, "registration_campaign_locked")
	expectStatus(t, webRequest(t, a, "POST", base, map[string]any{"name": "Next"}, headers, cookie), 201, "")
	rows, cursor := pageItems(t, a, cookie, base+"?limit=1", "campaigns")
	if len(rows) != 1 || rows[0]["days"] != float64(7) || cursor == "" {
		t.Fatal(rows, cursor)
	}
	older, _ := pageItems(t, a, cookie, base+"?limit=1&cursor="+cursor, "campaigns")
	if len(older) != 1 || older[0]["id"] != id {
		t.Fatal(older)
	}
	var n int
	if err := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE target_id=$1 AND action IN ('campaign.activate','campaign.end')", id).Scan(&n); err != nil || n != 2 {
		t.Fatal("idempotent actions duplicated changes", n, err)
	}
	for _, lang := range []string{"en", "zh-CN"} {
		w := webRequest(t, a, "GET", "/admin?lang="+lang, nil, nil, cookie)
		expectStatus(t, w, 200, "")
		if !strings.Contains(w.Body.String(), `id="campaign-management"`) || strings.Contains(w.Body.String(), "<no value>") {
			t.Fatal("missing campaign interface", lang)
		}
	}
	a.Config.Mode = "selfhost"
	for _, route := range []struct{ method, path string }{{"GET", base}, {"POST", base}, {"PATCH", base + "/" + id}, {"POST", base + "/" + id + "/activate"}, {"POST", base + "/" + id + "/stop"}, {"GET", base + "/" + id + "/grants"}} {
		expectStatus(t, webRequest(t, a, route.method, route.path, map[string]any{}, headers, cookie), 404, "registration_campaigns_disabled")
	}
	w := webRequest(t, a, "GET", "/admin", nil, nil, cookie)
	expectStatus(t, w, 200, "")
	if strings.Contains(w.Body.String(), "campaign-management") || strings.Contains(w.Body.String(), "/assets/campaigns.js") {
		t.Fatal("selfhost exposes campaign UI")
	}
}

func TestRegistrationTrialUsesExistingCloudEligibilityAndResponse(t *testing.T) {
	s := testutil.Database(t)
	a, admin, tokens, cookie := managementFixture(t, s)
	ctx := context.Background()
	a.Config.Mode = "hosted"
	a.Config.SMTP.Address = "smtp.example.test:465"
	a.Config.P2PBootstrap = []string{"tcp://relay.example.test:443"}
	rows, _ := pageItems(t, a, cookie, "/v1/admin/registration-campaigns", "campaigns")
	id := rows[0]["id"].(string)
	if _, err := s.ChangeRegistrationCampaign(ctx, admin, id, "activate", store.CampaignInput{}); err != nil {
		t.Fatal(err)
	}
	const login = "cloud-trial@example.test"
	if err := s.RequestRegistrationCode(ctx, login, a.Config); err != nil {
		t.Fatal(err)
	}
	proof, err := s.VerifyRegistrationCode(ctx, login, testutil.RegistrationCode(t, s, login), a.Config)
	if err != nil {
		t.Fatal(err)
	}
	w := webRequest(t, a, "POST", "/v1/auth/register", map[string]any{"login": login, "password": "synthetic-cloud-password-123", "registration_token": proof.Token}, nil)
	expectStatus(t, w, 201, "")
	var receipt map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || len(receipt) != 1 || receipt["registered"] != true || len(w.Result().Cookies()) != 0 {
		t.Fatal("registration wire contract changed", receipt, err)
	}
	account, err := s.AccountByLogin(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	p, clientTokens := testutil.Principal(t, s, account)
	expectStatus(t, webRequest(t, a, "GET", "/v1/account/entitlement", nil, map[string]string{"Authorization": "Bearer " + clientTokens.Access}), 200, "")
	g, err := s.Enroll(ctx, p, "gateway", "", "Trial desktop", append([]byte{1}, make([]byte, 31)...), 8)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Enroll(ctx, p, "mobile", "", "Trial phone", append([]byte{2}, make([]byte, 31)...), 16)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := s.DeviceBindings(ctx, m)
	if err != nil || len(bindings) != 1 || bindings[0].Status != "pending" {
		t.Fatal("trial approved a device", bindings, err)
	}
	b, err := s.SetBindingAccess(ctx, bindings[0].ID, g, m.PublicKey, true, bindings[0].AccessRevision, true)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/gateway/bindings/" + b.ID + "/p2p"
	deviceHeaders := map[string]string{"Authorization": "Bearer " + g.Token}
	expectStatus(t, webRequest(t, a, "GET", path, nil, deviceHeaders), 200, "")
	expectStatus(t, webRequest(t, a, "POST", "/v1/admin/registration-campaigns/"+id+"/stop", map[string]any{}, csrfHeaders(a, tokens.CSRF), cookie), 200, "")
	expectStatus(t, webRequest(t, a, "GET", path, nil, deviceHeaders), 200, "")
	if _, err = s.Pool.Exec(ctx, "UPDATE entitlements SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1", account.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, webRequest(t, a, "GET", path, nil, deviceHeaders), 402, "entitlement_required")
}

func TestRegistrationCampaignGuardsAndScopedGrantPages(t *testing.T) {
	s := testutil.Database(t)
	a, _, tokens, cookie := managementFixture(t, s)
	a.Config.Mode = "hosted"
	ctx := context.Background()
	base := "/v1/admin/registration-campaigns"
	headers := csrfHeaders(a, tokens.CSRF)
	rows, _ := pageItems(t, a, cookie, base, "campaigns")
	id := rows[0]["id"].(string)
	path := base + "/" + id
	expectStatus(t, webRequest(t, a, "GET", base, nil, nil), 401, "")
	user := testutil.Account(t, s, "not-admin")
	_, ut := testutil.Principal(t, s, user)
	expectStatus(t, webRequest(t, a, "GET", base, nil, map[string]string{"Authorization": "Bearer " + ut.Access}), 403, "access_denied")
	expectStatus(t, webRequest(t, a, "POST", path+"/activate", map[string]any{}, nil, cookie), 403, "csrf_required")
	badOrigin := csrfHeaders(a, tokens.CSRF)
	badOrigin["Origin"] = "https://foreign.example.test"
	expectStatus(t, webRequest(t, a, "POST", path+"/activate", map[string]any{}, badOrigin, cookie), 403, "")
	for i := range 3 {
		login := []string{"first", "second", "third"}[i]
		recipient := testutil.Account(t, s, login)
		if _, err := s.Pool.Exec(ctx, `INSERT INTO registration_campaign_grants(id,campaign_id,account_id,days,granted_at,expires_at)
     VALUES(gen_random_uuid(),$1,$2,7,now(),now()+interval '7 days')`, id, recipient.ID); err != nil {
			t.Fatal(err)
		}
	}
	first, cursor := pageItems(t, a, cookie, path+"/grants?limit=2", "grants")
	if len(first) != 2 || cursor == "" {
		t.Fatal(first, cursor)
	}
	rest, _ := pageItems(t, a, cookie, path+"/grants?limit=2&cursor="+cursor, "grants")
	if len(rest) != 1 || rest[0]["id"] == first[0]["id"] || rest[0]["id"] == first[1]["id"] {
		t.Fatal(rest)
	}
	if first[0]["login"] == nil || first[0]["granted_at"] == nil || first[0]["expires_at"] == nil {
		t.Fatal(first)
	}
	expectStatus(t, webRequest(t, a, "GET", base+"?cursor="+cursor, nil, nil, cookie), 400, "invalid_page")
	expectStatus(t, webRequest(t, a, "GET", path+"/grants?limit=0", nil, nil, cookie), 400, "invalid_page")
	expectStatus(t, webRequest(t, a, "POST", path+"/stop", map[string]any{}, headers, cookie), 200, "")
	expectStatus(t, webRequest(t, a, "POST", base, map[string]any{"name": "next", "days": 7}, headers, cookie), 201, "")
	rows, _ = pageItems(t, a, cookie, base, "campaigns")
	expectStatus(t, webRequest(t, a, "GET", base+"/"+rows[0]["id"].(string)+"/grants?cursor="+cursor, nil, nil, cookie), 400, "invalid_page")
	if _, err := s.Pool.Exec(ctx, "UPDATE registration_campaign_grants SET days=8 WHERE campaign_id=$1", id); err == nil {
		t.Fatal("grant history mutable")
	}
	if _, err := s.Pool.Exec(ctx, "DELETE FROM registration_campaign_grants WHERE campaign_id=$1", id); err == nil {
		t.Fatal("grant history deletable")
	}
	// The ordinary service API keeps its existing contract.
	e, err := s.Entitlement(ctx, user.ID, true)
	if err != nil || e.Active {
		t.Fatal(e, err)
	}
}
