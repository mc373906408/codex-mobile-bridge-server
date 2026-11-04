package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestEmailVerificationIsCodeOnly(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	c.Mode = "hosted"
	c.SMTP.Address = "smtp.example.test:465"
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	w := webRequest(t, a, "GET", "/v1/capabilities", nil, nil)
	expectStatus(t, w, 200, "")
	var capability struct {
		Methods []string `json:"email_verification_methods"`
		Length  int      `json:"email_verification_code_length"`
		Flow    string   `json:"registration_flow"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &capability); err != nil || len(capability.Methods) != 1 || capability.Methods[0] != "code" || capability.Length != 6 || capability.Flow != "email_code_password" {
		t.Fatal("hosted capabilities must advertise only six-digit codes", err)
	}
	expectStatus(t, webRequest(t, a, "GET", "/verify", nil, nil), 404, "")
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/verify", map[string]string{"token": "retired-verification-link"}, nil), 404, "")
	expectStatus(t, webRequest(t, a, "GET", "/reset", nil, nil), 200, "")
	credentials := map[string]string{"login": "codes@example.test", "password": "registration-test-password-123"}
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/register", credentials, nil), 400, "invalid_registration_token")
	w = webRequest(t, a, "POST", "/v1/auth/registration-code", map[string]string{"login": credentials["login"]}, nil)
	expectStatus(t, w, 202, "")
	var receipt struct {
		Accepted bool   `json:"accepted"`
		Method   string `json:"verification_method"`
		Expiry   int    `json:"expires_in"`
		Retry    int    `json:"retry_after"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || !receipt.Accepted || receipt.Method != "code" || receipt.Expiry != 600 || receipt.Retry != 60 {
		t.Fatal("email-first registration must issue a code receipt, never a link", err)
	}
	if _, err = s.AccountByLogin(ctx, credentials["login"]); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("requesting a code must not create an account", err)
	}
	code := testutil.RegistrationCode(t, s, credentials["login"])
	for _, invalid := range []string{"12345", "abcdef", "1234567"} {
		expectStatus(t, webRequest(t, a, "POST", "/v1/auth/registration-verify", map[string]string{"login": credentials["login"], "code": invalid}, nil), 400, "invalid_verification_code")
	}
	w = webRequest(t, a, "POST", "/v1/auth/registration-verify", map[string]string{"login": credentials["login"], "code": code}, nil)
	expectStatus(t, w, 200, "")
	var proof store.RegistrationProof
	if err = json.Unmarshal(w.Body.Bytes(), &proof); err != nil || !proof.Verified || len(proof.Token) != 43 || proof.ExpiresIn != 600 {
		t.Fatal("verification must return only a short-lived registration proof", err)
	}
	if _, err = s.AccountByLogin(ctx, credentials["login"]); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("email verification must not create an account or set a password", err)
	}
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/registration-verify", map[string]string{"login": credentials["login"], "code": code}, nil), 400, "invalid_verification_code")
	w = webRequest(t, a, "POST", "/v1/auth/register", map[string]string{"login": credentials["login"], "password": credentials["password"], "registration_token": proof.Token}, nil)
	expectStatus(t, w, 201, "")
	if w.Body.String() != "{\"registered\":true}\n" {
		t.Fatal("registration must not sign in or grant device authority")
	}
	account, err := s.AccountByLogin(ctx, credentials["login"])
	if err != nil || !account.Verified || account.Administrator {
		t.Fatal("password completion must create a verified ordinary account", err)
	}
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/register", map[string]string{"login": credentials["login"], "password": credentials["password"], "registration_token": proof.Token}, nil), 400, "invalid_registration_token")
	for _, endpoint := range []string{"registration-code", "registration-verify"} {
		expectStatus(t, webRequest(t, a, "POST", "/v1/auth/"+endpoint, credentials, nil), 400, "invalid_body")
	}
	for _, endpoint := range []string{"verification", "verify-code"} {
		in := map[string]string{"login": credentials["login"], "code": code}
		expectStatus(t, webRequest(t, a, "POST", "/v1/auth/"+endpoint, in, nil), 404, "")
	}
}

func TestEmailCodeResendSharesPersistentLimitsAndDoesNotEnumerate(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	c.Mode = "hosted"
	c.SMTP.Address = "smtp.example.test:465"
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	login := "cooldown@example.test"
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/registration-code", map[string]string{"login": login}, nil), 202, "")
	oldCode := testutil.RegistrationCode(t, s, login)
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/registration-code", map[string]string{"login": login}, nil), 429, "rate_limited")
	if fresh := testutil.RegistrationCode(t, s, login); fresh != oldCode {
		t.Fatal("cooldown rejection must not send or replace a code")
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE rate_limits SET window_start=now()-interval '61 seconds'"); err != nil {
		t.Fatal(err)
	}
	w := webRequest(t, a, "POST", "/v1/auth/registration-code", map[string]string{"login": login}, nil)
	expectStatus(t, w, 202, "")
	if fresh := testutil.RegistrationCode(t, s, login); fresh == oldCode {
		t.Fatal("resend must invalidate the old code")
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE rate_limits SET window_start=now()-interval '61 seconds'"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, webRequest(t, a, "POST", "/v1/auth/registration-code", map[string]string{"login": login}, nil), 429, "rate_limited")
	if _, err = s.Pool.Exec(ctx, "UPDATE rate_limits SET window_start=now()-interval '2 hours'"); err != nil {
		t.Fatal(err)
	}
	testutil.Account(t, s, "existing@example.test")
	missing := webRequest(t, a, "POST", "/v1/auth/registration-code", map[string]string{"login": "existing@example.test"}, nil)
	expectStatus(t, missing, 202, "")
	if missing.Body.String() != w.Body.String() {
		t.Fatal("resend receipts must not disclose whether an account exists")
	}
}

func TestSelfhostHasNoPublicEmailCodeFlow(t *testing.T) {
	s := testutil.Database(t)
	a, err := New(testutil.Config(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	for _, endpoint := range []string{"register", "registration-code", "registration-verify"} {
		expectStatus(t, webRequest(t, a, "POST", "/v1/auth/"+endpoint, map[string]string{"login": "local"}, nil), 404, "registration_disabled")
	}
	for _, endpoint := range []string{"verification", "verify-code"} {
		expectStatus(t, webRequest(t, a, "POST", "/v1/auth/"+endpoint, map[string]string{"login": "local"}, nil), 404, "")
	}
	w := webRequest(t, a, "GET", "/v1/capabilities", nil, nil)
	expectStatus(t, w, 200, "")
	var capability struct {
		Methods []string `json:"email_verification_methods"`
		Flow    string   `json:"registration_flow"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &capability); err != nil || len(capability.Methods) != 0 || capability.Flow != "" {
		t.Fatal("selfhost must not advertise email registration codes", err)
	}
}
