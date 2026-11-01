package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func webRequest(t *testing.T, a *Server, method, path string, value any, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if value != nil {
		var err error
		data, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, a.Config.Origin+path, bytes.NewReader(data))
	if value != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func csrfHeaders(a *Server, token string) map[string]string {
	return map[string]string{"Origin": a.Config.Origin, "X-CSRF-Token": token}
}

func responseCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge > 0 {
			return cookie
		}
	}
	t.Fatal("expected a live cookie", name)
	return nil
}

func expectStatus(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("expected HTTP %d, got %d", status, w.Code)
	}
	if code != "" {
		var value map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil || value["code"] != code {
			t.Fatalf("expected error %s", code)
		}
	}
}

func TestAdministratorFirstWebLoginWithoutCodeRequiresNewPassword(t *testing.T) {
	for _, mode := range []string{"selfhost", "hosted"} {
		t.Run(mode, func(t *testing.T) {
			s := testutil.Database(t)
			ctx := context.Background()
			c := testutil.Config()
			c.Mode = mode
			if err := s.Bootstrap(ctx, "admin", "admin"); err != nil {
				t.Fatal(err)
			}
			original, err := s.AccountByLogin(ctx, "admin")
			if err != nil || !original.CredentialsChangeRequired || !security.VerifyPassword("admin", original.PasswordHash) {
				t.Fatal("default administrator must be setup-only", err)
			}
			a, err := New(c, s)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Hub.Shutdown)
			guest := responseCookie(t, webRequest(t, a, "GET", "/admin", nil, nil), "__Host-bridge_guest")
			headers := csrfHeaders(a, guest.Value)
			login := map[string]string{"login": "admin", "password": "admin"}
			w := webRequest(t, a, "POST", "/web/login", login, headers, guest)
			expectStatus(t, w, 200, "")
			if !strings.Contains(w.Body.String(), `"credentials_change_required":true`) || strings.Contains(w.Body.String(), `"authenticated":true`) {
				t.Fatal("default login must not grant a regular session")
			}
			setupCookie := responseCookie(t, w, a.setupCookieName())
			if !setupCookie.Secure || !setupCookie.HttpOnly || setupCookie.SameSite != http.SameSiteStrictMode || setupCookie.MaxAge != 600 {
				t.Fatal("setup cookie must be restricted and expire after ten minutes")
			}
			legacy := map[string]string{"login": "admin", "password": "admin", "setup_code": "unused-cached-form-field"}
			expectStatus(t, webRequest(t, a, "POST", "/web/login", legacy, headers, guest), 200, "")
			var count int
			if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 0 {
				t.Fatal("setup must not create account or device authority", err)
			}
			for _, path := range []string{"/v1/admin/status", "/v1/admin/users", "/v1/account"} {
				expectStatus(t, webRequest(t, a, "GET", path, nil, nil, setupCookie), 401, "authentication_required")
				expectStatus(t, webRequest(t, a, "GET", path, nil, map[string]string{"Authorization": "Bearer " + setupCookie.Value}), 401, "authentication_required")
			}
			page := webRequest(t, a, "GET", "/admin/setup", nil, nil, setupCookie)
			expectStatus(t, page, 200, "")
			if !strings.Contains(page.Body.String(), `id="administrator-setup"`) || strings.Contains(page.Body.String(), `id="users"`) {
				t.Fatal("setup page must not render administration controls")
			}
			setup, err := s.AdministratorSetup(ctx, setupCookie.Value)
			if err != nil {
				t.Fatal(err)
			}
			change := map[string]string{"login": "operator", "password": "different-test-password-123"}
			expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", change, nil, setupCookie), 403, "csrf_required")
			expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", change, csrfHeaders(a, "wrong"), setupCookie), 403, "csrf_required")
			change["password"] = "admin"
			expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", change, csrfHeaders(a, setup.CSRF), setupCookie), 400, "password_change_required")
			change["password"] = "short"
			expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", change, csrfHeaders(a, setup.CSRF), setupCookie), 400, "invalid_password")
			testutil.Account(t, s, "already-taken")
			change["login"], change["password"] = "already-taken", "different-test-password-123"
			expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", change, csrfHeaders(a, setup.CSRF), setupCookie), 409, "state_conflict")
			change["login"] = "operator"
			expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", change, csrfHeaders(a, setup.CSRF), setupCookie), 200, "")
			expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", change, csrfHeaders(a, setup.CSRF), setupCookie), 401, "administrator_setup_required")
			account, err := s.AccountByLogin(ctx, "operator")
			if err != nil || account.ID != original.ID || account.CredentialsChangeRequired || !account.Administrator {
				t.Fatal("credential changes must preserve account identity and require a fresh login", err)
			}
			if err := s.Bootstrap(ctx, "admin", "admin"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AccountByLogin(ctx, "admin"); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("startup must not recreate default access after a username change", err)
			}
			login = map[string]string{"login": "operator", "password": "different-test-password-123"}
			w = webRequest(t, a, "POST", "/web/login", login, headers, guest)
			expectStatus(t, w, 200, "")
			cookie := responseCookie(t, w, a.cookieName())
			page = webRequest(t, a, "GET", "/admin", nil, nil, cookie)
			expectStatus(t, page, 200, "")
			if !strings.Contains(page.Body.String(), `id="users"`) || !strings.Contains(page.Body.String(), "operator") {
				t.Fatal("only a fully initialized administrator may access the workspace")
			}
		})
	}
}

func TestOrdinaryUsersCannotLoginOrReuseWebsiteSessions(t *testing.T) {
	for _, mode := range []string{"selfhost", "hosted"} {
		t.Run(mode, func(t *testing.T) {
			s := testutil.Database(t)
			ctx := context.Background()
			c := testutil.Config()
			c.Mode = mode
			login := "ordinary-user"
			if mode == "hosted" {
				login = "ordinary@example.test"
			}
			ordinary := testutil.Account(t, s, login)
			tokens, err := s.NewSession(ctx, ordinary, c, false)
			if err != nil {
				t.Fatal("client/API login must remain available", err)
			}
			if _, err := s.NewSession(ctx, ordinary, c, true); !errors.Is(err, store.ErrDenied) {
				t.Fatal("the store must reject ordinary website session creation", err)
			}
			a, err := New(c, s)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Hub.Shutdown)
			guestPage := webRequest(t, a, "GET", "/admin", nil, nil)
			expectStatus(t, guestPage, 200, "")
			for _, forbidden := range []string{`id="register"`, `id="mail"`, `type="email"`} {
				if strings.Contains(guestPage.Body.String(), forbidden) {
					t.Fatal("the website must not expose ordinary account login or registration", forbidden)
				}
			}
			guest := responseCookie(t, guestPage, "__Host-bridge_guest")
			credentials := map[string]string{"login": login, "password": "test-only-password-123"}
			expectStatus(t, webRequest(t, a, "POST", "/web/login", credentials, csrfHeaders(a, guest.Value), guest), 403, "administrator_web_only")
			expectStatus(t, webRequest(t, a, "POST", "/v1/auth/login", credentials, nil), 200, "")
			bearer := map[string]string{"Authorization": "Bearer " + tokens.Access}
			expectStatus(t, webRequest(t, a, "GET", "/v1/account", nil, bearer), 200, "")
			expectStatus(t, webRequest(t, a, "GET", "/admin", nil, bearer), 403, "administrator_web_only")
			expectStatus(t, webRequest(t, a, "GET", "/v1/admin/users", nil, bearer), 403, "access_denied")

			legacy, _ := security.Token()
			if _, err := s.Pool.Exec(ctx, "UPDATE sessions SET web_digest=$2,web_expires_at=now()+interval '1 hour' WHERE access_digest=$1", security.Digest(tokens.Access), security.Digest(legacy)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Authenticate(ctx, legacy, true); !errors.Is(err, store.ErrDenied) {
				t.Fatal("pre-upgrade ordinary website sessions must be denied", err)
			}
			cookie := &http.Cookie{Name: a.cookieName(), Value: legacy}
			for _, path := range []string{"/", "/admin"} {
				page := webRequest(t, a, "GET", path, nil, nil, cookie)
				expectStatus(t, page, 200, "")
				if strings.Contains(page.Body.String(), `id="users"`) || (path == "/admin" && !strings.Contains(page.Body.String(), `id="login"`)) {
					t.Fatal("a stale ordinary cookie must never render an authenticated console")
				}
			}
			expectStatus(t, webRequest(t, a, "GET", "/v1/account", nil, nil, cookie), 401, "authentication_required")
			expectStatus(t, webRequest(t, a, "GET", "/v1/admin/users", nil, nil, cookie), 401, "authentication_required")
			expectStatus(t, webRequest(t, a, "GET", "/v1/account", nil, bearer), 200, "")
			for _, path := range []string{"/verify", "/reset"} {
				expectStatus(t, webRequest(t, a, "GET", path, nil, nil), 200, "")
			}
		})
	}
}

func TestAdministratorAPILoginReturnsOnlyRestrictedSetupToken(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	if err := s.Bootstrap(ctx, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	c := testutil.Config()
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	w := webRequest(t, a, "POST", "/v1/auth/login", map[string]string{"login": "admin", "password": "admin"}, nil)
	expectStatus(t, w, 200, "")
	var result struct {
		Required bool   `json:"credentials_change_required"`
		Token    string `json:"setup_token"`
		Access   string `json:"access_token"`
		Refresh  string `json:"refresh_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Required || len(result.Token) != 43 || result.Access != "" || result.Refresh != "" {
		t.Fatal("default API login must not provide account authority", err)
	}
	headers := map[string]string{"Authorization": "Bearer " + result.Token}
	expectStatus(t, webRequest(t, a, "GET", "/v1/account", nil, headers), 401, "authentication_required")
	expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", map[string]string{"login": "admin", "password": "changed-test-password-123"}, headers), 200, "")
	expectStatus(t, webRequest(t, a, "POST", "/v1/admin/auth/setup", map[string]string{"login": "admin", "password": "another-test-password-123"}, headers), 401, "administrator_setup_required")
}

func TestHostedEmailRegistrationAndVerificationStayClientOnly(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	c.Mode = "hosted"
	c.SMTP.Address, c.SMTP.Host, c.SMTP.From = "smtp.example.test:465", "smtp.example.test", "bridge@example.test"
	c.SMTP.TLS = "implicit"
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	credentials := map[string]string{"login": "client@example.test", "password": "client-test-password-123"}
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/register", credentials, nil), 202, "")
	account, err := s.AccountByLogin(ctx, credentials["login"])
	if err != nil || account.Verified || account.Administrator {
		t.Fatal("email registration must create an unverified ordinary account", err)
	}
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/login", credentials, nil), 401, "invalid_credentials")
	var mailID string
	var encrypted []byte
	if err = s.Pool.QueryRow(ctx, "SELECT id,encrypted_message FROM mail_outbox WHERE account_id=$1 AND purpose='verify'", account.ID).Scan(&mailID, &encrypted); err != nil {
		t.Fatal(err)
	}
	data, err := s.Vault.Open("mail:"+mailID, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	var mail store.MailMessage
	if err = json.Unmarshal(data, &mail); err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(mail.Body, "#token=", 2)
	if len(parts) != 2 {
		t.Fatal("verification email must contain a one-time fragment link")
	}
	token := strings.SplitN(parts[1], "\r\n", 2)[0]
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/verify", map[string]string{"token": token}, nil), 200, "")
	w := webRequest(t, a, "POST", "/v1/auth/login", credentials, nil)
	expectStatus(t, w, 200, "")
	var tokens store.Tokens
	if err = json.Unmarshal(w.Body.Bytes(), &tokens); err != nil || len(tokens.Access) != 43 || tokens.Account.Administrator {
		t.Fatal("verified ordinary users must receive only client account authority", err)
	}
	guest := responseCookie(t, webRequest(t, a, "GET", "/admin", nil, nil), "__Host-bridge_guest")
	expectStatus(t, webRequest(t, a, "POST", "/web/login", credentials, csrfHeaders(a, guest.Value), guest), 403, "administrator_web_only")
	expectStatus(t, webRequest(t, a, "GET", "/admin", nil, map[string]string{"Authorization": "Bearer " + tokens.Access}), 403, "administrator_web_only")
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/register", map[string]any{"login": "other@example.test", "password": credentials["password"], "administrator": true}, nil), 400, "invalid_body")
}
