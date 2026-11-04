package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestAdministratorConsoleSeparatesHomeAndClientRedemption(t *testing.T) {
	for _, mode := range []string{"selfhost", "hosted"} {
		t.Run(mode, func(t *testing.T) {
			s := testutil.Database(t)
			ctx := context.Background()
			c := testutil.Config()
			c.Mode = mode
			if err := s.Bootstrap(ctx, "operator", "console-test-password-123"); err != nil {
				t.Fatal(err)
			}
			admin, err := s.AccountByLogin(ctx, "operator")
			if err != nil {
				t.Fatal(err)
			}
			setup, err := s.NewAdministratorSetup(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := security.Password("completed-console-password-123")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.FinishAdministratorSetup(ctx, setup.Token, "operator", hash); err != nil {
				t.Fatal(err)
			}
			admin, err = s.AccountByLogin(ctx, "operator")
			if err != nil {
				t.Fatal(err)
			}
			tokens, err := s.NewSession(ctx, admin, c, true)
			if err != nil {
				t.Fatal(err)
			}
			a, err := New(c, s)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Hub.Shutdown)
			cookie := &http.Cookie{Name: a.cookieName(), Value: tokens.Web}
			page := webRequest(t, a, "GET", "/admin", nil, nil, cookie)
			expectStatus(t, page, 200, "")
			for _, fragment := range []string{`data-admin="true"`, `class="admin-shell"`, `class="admin-nav"`, `id="users"`, `id="devices"`, `id="sessions"`, `id="audit"`} {
				if !strings.Contains(page.Body.String(), fragment) {
					t.Fatal("console is missing its management layout", fragment)
				}
			}
			for _, fragment := range []string{`class="intro"`, `id="login"`, `id="redeem"`, `id="entitlement"`} {
				if strings.Contains(page.Body.String(), fragment) {
					t.Fatal("console must not contain landing, login or redemption widgets", fragment)
				}
			}
			if c.Paid() != strings.Contains(page.Body.String(), `id="codes"`) || c.Paid() != strings.Contains(page.Body.String(), `href="#code-management"`) {
				t.Fatal("code issuance must have its own hosted-only management section")
			}
			for _, cookies := range [][]*http.Cookie{nil, {cookie}} {
				home := webRequest(t, a, "GET", "/", nil, nil, cookies...)
				expectStatus(t, home, 200, "")
				if !strings.Contains(home.Body.String(), `id="home-welcome"`) || strings.Contains(home.Body.String(), `data-admin="true"`) || strings.Contains(home.Body.String(), `id="users"`) {
					t.Fatal("homepage must remain separate even with an administrator cookie")
				}
			}
			requestID, _ := security.ID()
			input := map[string]string{"code": "test-only-code", "request_id": requestID}
			expectStatus(t, webRequest(t, a, "POST", "/v1/account/redeem", input, csrfHeaders(a, tokens.CSRF), cookie), 403, "redemption_client_only")
			client := testutil.Account(t, s, "client@example.test")
			clientTokens, err := s.NewSession(ctx, client, c, false)
			if err != nil {
				t.Fatal(err)
			}
			if c.Paid() {
				batch := map[string]int{"days": 7, "count": 2}
				expectStatus(t, webRequest(t, a, "POST", "/v1/admin/codes", batch, nil, cookie), 403, "csrf_required")
				bearer := map[string]string{"Authorization": "Bearer " + clientTokens.Access}
				expectStatus(t, webRequest(t, a, "POST", "/v1/admin/codes", batch, bearer), 403, "access_denied")
				generated := webRequest(t, a, "POST", "/v1/admin/codes", batch, csrfHeaders(a, tokens.CSRF), cookie)
				expectStatus(t, generated, 201, "")
				var result struct {
					Codes      []store.GeneratedCode `json:"codes"`
					ExportOnce bool                  `json:"export_once"`
				}
				if err := json.Unmarshal(generated.Body.Bytes(), &result); err != nil || !result.ExportOnce || len(result.Codes) != 2 {
					t.Fatal("administrator generation must return a private one-time export")
				}
				for _, code := range result.Codes {
					if len(code.Code) != security.RedemptionCodeLength || code.Days != 7 {
						t.Fatal("generated code shape is invalid")
					}
				}
				listed := webRequest(t, a, "GET", "/v1/admin/codes", nil, nil, cookie)
				expectStatus(t, listed, 200, "")
				for _, code := range result.Codes {
					if strings.Contains(listed.Body.String(), code.Code) {
						t.Fatal("the list must never re-export code plaintext")
					}
				}
				expectStatus(t, webRequest(t, a, "GET", "/v1/admin/codes", nil, bearer), 403, "access_denied")
				input["code"] = result.Codes[0].ID
				expectStatus(t, webRequest(t, a, "POST", "/v1/account/redeem", input, bearer), 400, "invalid_redemption")
				input["code"] = result.Codes[0].Code
				expectStatus(t, webRequest(t, a, "POST", "/v1/account/redeem", input, bearer), 200, "")
				expectStatus(t, webRequest(t, a, "DELETE", "/v1/admin/codes/"+result.Codes[0].ID, nil, csrfHeaders(a, tokens.CSRF), cookie), 409, "state_conflict")
				expectStatus(t, webRequest(t, a, "DELETE", "/v1/admin/codes/"+result.Codes[1].ID, nil, csrfHeaders(a, tokens.CSRF), cookie), 200, "")
				input["code"] = result.Codes[1].Code
				input["request_id"], _ = security.ID()
				expectStatus(t, webRequest(t, a, "POST", "/v1/account/redeem", input, bearer), 400, "invalid_redemption")
				expectStatus(t, webRequest(t, a, "GET", "/v1/admin/redemptions", nil, nil, cookie), 200, "")
				expectStatus(t, webRequest(t, a, "GET", "/v1/admin/redemptions", nil, bearer), 403, "access_denied")
			} else {
				expectStatus(t, webRequest(t, a, "POST", "/v1/account/redeem", input, map[string]string{"Authorization": "Bearer " + clientTokens.Access}), 404, "redemption_disabled")
			}
		})
	}
}

func TestPublicHomepageDoesNotLoadManagementOrRedemption(t *testing.T) {
	a, err := New(testutil.Config(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	w := webRequest(t, a, "GET", "/", nil, nil)
	expectStatus(t, w, 200, "")
	if !strings.Contains(w.Body.String(), `id="home-welcome"`) || strings.Contains(w.Body.String(), `id="login"`) {
		t.Fatal("the homepage must not be a login or management panel")
	}
	js, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/account/redeem", "/v1/account/entitlement"} {
		if strings.Contains(string(js), path) {
			t.Fatal("browser assets must not call personal service-redemption APIs", path)
		}
	}
}
