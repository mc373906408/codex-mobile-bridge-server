package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestAccountWebLoginScopeAndLogout(t *testing.T) {
	s := testutil.Database(t)
	c := testutil.Config()
	c.WebOrigin = "https://relay.example.test:18787"
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	account := testutil.Account(t, s, "web-user")
	other := testutil.Account(t, s, "other-user")
	p, _ := testutil.Principal(t, s, account)
	op, _ := testutil.Principal(t, s, other)
	key := append([]byte{1}, make([]byte, 31)...)
	own, err := s.Enroll(context.Background(), p, "gateway", "", "Own desktop", key, 8)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.Enroll(context.Background(), op, "gateway", "", "Other desktop", key, 8)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, origin, path, body, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", origin)
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	first := call("GET", c.WebOrigin, "/_bridge/session", "", "", nil)
	var guest struct {
		CSRF string `json:"csrf"`
	}
	if err = json.Unmarshal(first.Body.Bytes(), &guest); err != nil {
		t.Fatal(err)
	}
	logged := call("POST", c.WebOrigin, "/_bridge/login", `{"login":"web-user","password":"test-only-password-123"}`, guest.CSRF, first.Result().Cookies()[0])
	if logged.Code != 200 {
		t.Fatal(logged.Code, logged.Body.String())
	}
	var auth struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(logged.Body.Bytes(), &auth)
	cookie := logged.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie boundary", cookie.Name)
	}
	for _, token := range []string{"access_token", "refresh_token", "device_token"} {
		if strings.Contains(logged.Body.String(), token) {
			t.Fatal("credential escaped")
		}
	}
	directory := call("GET", c.WebOrigin, "/_bridge/gateways", "", "", cookie)
	if directory.Code != 200 || !strings.Contains(directory.Body.String(), own.ID) || strings.Contains(directory.Body.String(), foreign.ID) {
		t.Fatal("directory boundary", directory.Code, directory.Body.String())
	}
	if w := call("GET", c.WebOrigin, "/d/"+foreign.ID+"/", "", "", cookie); w.Code != 404 {
		t.Fatal("foreign computer", w.Code)
	}
	if w := call("GET", c.WebOrigin, "/d/"+own.ID+"/", "", "", cookie); w.Code != 503 {
		t.Fatal("offline computer", w.Code)
	}
	if w := call("POST", c.WebOrigin, "/_bridge/logout", `{}`, "wrong", cookie); w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	cross := httptest.NewRequest("POST", c.WebOrigin+"/_bridge/logout", strings.NewReader(`{}`))
	cross.AddCookie(cookie)
	cross.Header.Set("Origin", c.Origin)
	cross.Header.Set("X-CSRF-Token", auth.CSRF)
	result := httptest.NewRecorder()
	a.ServeHTTP(result, cross)
	if result.Code != 403 {
		t.Fatal("cross-port origin accepted")
	}
	for _, path := range []string{"/admin", "/v1/account", "/web/login"} {
		if w := call("GET", c.WebOrigin, path, "", "", cookie); w.Code != 404 {
			t.Fatal("native route on Web host", path, w.Code)
		}
	}
	if w := call("POST", c.WebOrigin, "/_bridge/logout", `{}`, auth.CSRF, cookie); w.Code != 200 {
		t.Fatal("logout", w.Code, w.Body.String())
	}
	if w := call("GET", c.WebOrigin, "/_bridge/gateways", "", "", cookie); w.Code != 401 {
		t.Fatal("revoked cookie accepted", w.Code)
	}
	if _, err = s.Authenticate(context.Background(), cookie.Value, true); err != store.ErrDenied {
		t.Fatal("browser escalated to admin", err)
	}
}

func TestWebSessionTokensCannotAuthenticateNativeOrAdmin(t *testing.T) {
	s := testutil.Database(t)
	a := testutil.Account(t, s, "session-purpose")
	ctx := context.Background()
	token, err := s.NewWebClientSession(ctx, a, testutil.Config())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateWebClient(ctx, token.Web); err != nil {
		t.Fatal(err)
	}
	for _, web := range []bool{false, true} {
		for _, value := range []string{token.Access, token.Web} {
			if _, err = s.Authenticate(ctx, value, web); err != store.ErrDenied {
				t.Fatal("purpose crossover", web, err)
			}
		}
	}
	if _, err = s.Refresh(ctx, token.Refresh, testutil.Config()); err != store.ErrDenied {
		t.Fatal("Web session refreshed as native", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE sessions SET web_expires_at=now()-interval '1 second' WHERE web_digest IS NOT NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateWebClient(ctx, token.Web); err != store.ErrDenied {
		t.Fatal("expired cookie accepted", err)
	}
}

func TestHostedWebChecksEntitlementAndRevocationBeforeDesktop(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	account := testutil.Account(t, s, "hosted-web@example.test")
	p, _ := testutil.Principal(t, s, account)
	g, err := s.Enroll(ctx, p, "gateway", "", "Hosted", append([]byte{1}, make([]byte, 31)...), 8)
	if err != nil {
		t.Fatal(err)
	}
	c := testutil.Config()
	c.Mode = "hosted"
	c.WebOrigin = "https://relay.example.test:18787"
	app, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Hub.Shutdown()
	tokens, err := s.NewWebClientSession(ctx, account, c)
	if err != nil {
		t.Fatal(err)
	}
	request := func() int {
		r := httptest.NewRequest("GET", c.WebOrigin+"/d/"+g.ID+"/", nil)
		r.AddCookie(&http.Cookie{Name: app.clientCookieName(false), Value: tokens.Web})
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w.Code
	}
	if code := request(); code != 402 {
		t.Fatal("unentitled Web", code)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE entitlements SET expires_at=now()+interval '1 day' WHERE account_id=$1", account.ID); err != nil {
		t.Fatal(err)
	}
	if code := request(); code != 503 {
		t.Fatal("active account must reach desktop check", code)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE accounts SET disabled=true WHERE id=$1", account.ID); err != nil {
		t.Fatal(err)
	}
	if code := request(); code != 303 {
		t.Fatal("disabled session must return to login", code)
	}
}
