package store_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func emailConfig() config.Config {
	c := testutil.Config()
	c.Mode = "hosted"
	c.SMTP.Address = "smtp.example.test:465"
	return c
}

func signupProof(t *testing.T, s *store.Store, login string) store.RegistrationProof {
	t.Helper()
	c := emailConfig()
	if err := s.RequestRegistrationCode(context.Background(), login, c); err != nil {
		t.Fatal(err)
	}
	proof, err := s.VerifyRegistrationCode(context.Background(), login, testutil.RegistrationCode(t, s, login), c)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func noSignupAccount(t *testing.T, s *store.Store, login string) {
	t.Helper()
	if _, err := s.AccountByLogin(context.Background(), login); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("email proof must not create an account before password completion", err)
	}
}

func TestRegistrationCreatesNoAccountUntilEmailProofAndPassword(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := emailConfig()
	login := "signup@example.test"
	if err := s.RequestRegistrationCode(ctx, login, c); err != nil {
		t.Fatal(err)
	}
	noSignupAccount(t, s, login)
	code := testutil.RegistrationCode(t, s, login)
	var digest []byte
	if err := s.Pool.QueryRow(ctx, "SELECT digest FROM registration_challenges WHERE login=$1", login).Scan(&digest); err != nil || len(digest) != 32 || bytes.Equal(digest, []byte(code)) {
		t.Fatal("database must store a keyed code digest", err)
	}
	if _, err := s.CompleteRegistration(ctx, login, strings.Repeat("t", 43), "test-only-hash", c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("password submission must require verified email proof", err)
	}
	proof, err := s.VerifyRegistrationCode(ctx, login, code, c)
	if err != nil || !proof.Verified || len(proof.Token) != 43 || proof.ExpiresIn != 600 {
		t.Fatal("verification must return a short-lived registration proof", err)
	}
	noSignupAccount(t, s, login)
	var pendingEntitlements, sessions, gateways int
	if err = s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM entitlements),(SELECT count(*) FROM sessions),(SELECT count(*) FROM gateways)").Scan(&pendingEntitlements, &sessions, &gateways); err != nil || pendingEntitlements != 0 || sessions != 0 || gateways != 0 {
		t.Fatal("verification must not grant an account, session or device authority", err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT registration_digest FROM registration_challenges WHERE login=$1", login).Scan(&digest); err != nil || !bytes.Equal(digest, security.Digest(proof.Token)) || bytes.Contains(digest, []byte(proof.Token)) {
		t.Fatal("registration proof must be stored only as a digest", err)
	}
	if _, err = s.VerifyRegistrationCode(ctx, login, code, c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("a verified code must not issue a second proof", err)
	}
	a, err := s.CompleteRegistration(ctx, login, proof.Token, "test-only-password-hash", c)
	if err != nil || !a.Verified || a.Administrator || a.Login != login || a.PasswordHash != "test-only-password-hash" {
		t.Fatal("verified proof and password must create an ordinary verified account", err)
	}
	if _, err = s.CompleteRegistration(ctx, login, proof.Token, "replacement-must-not-be-stored", c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("registration proof must be single-use", err)
	}
	after, err := s.AccountByLogin(ctx, login)
	if err != nil || after.PasswordHash != a.PasswordHash {
		t.Fatal("replaying registration must never change a password", err)
	}
}

func TestRegistrationCodesExpireAndCommitFailedAttempts(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := emailConfig()
	for _, login := range []string{"expired@example.test", "attempts@example.test"} {
		if err := s.RequestRegistrationCode(ctx, login, c); err != nil {
			t.Fatal(err)
		}
		code := testutil.RegistrationCode(t, s, login)
		if login == "expired@example.test" {
			if _, err := s.Pool.Exec(ctx, "UPDATE registration_challenges SET expires_at=now()-interval '1 second' WHERE login=$1", login); err != nil {
				t.Fatal(err)
			}
		} else {
			wrong := "000000"
			if code == wrong {
				wrong = "000001"
			}
			for range 5 {
				if _, err := s.VerifyRegistrationCode(ctx, login, wrong, c); !errors.Is(err, store.ErrDenied) {
					t.Fatal("wrong code must be rejected", err)
				}
			}
			var attempts int
			if err := s.Pool.QueryRow(ctx, "SELECT attempts FROM registration_challenges WHERE login=$1", login).Scan(&attempts); err != nil || attempts != 5 {
				t.Fatal("failed code attempts must persist", err)
			}
		}
		if _, err := s.VerifyRegistrationCode(ctx, login, code, c); !errors.Is(err, store.ErrDenied) {
			t.Fatal("expired or exhausted codes must not verify", err)
		}
		noSignupAccount(t, s, login)
	}
}

func TestRegistrationResendInvalidatesCodesAndProofs(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := emailConfig()
	login := "resend@example.test"
	if err := s.RequestRegistrationCode(ctx, login, c); err != nil {
		t.Fatal(err)
	}
	oldCode := testutil.RegistrationCode(t, s, login)
	if err := s.RequestRegistrationCode(ctx, login, c); err != nil {
		t.Fatal(err)
	}
	freshCode := testutil.RegistrationCode(t, s, login)
	if oldCode == freshCode {
		t.Fatal("resend must rotate the previous code")
	}
	if _, err := s.VerifyRegistrationCode(ctx, login, oldCode, c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("resend must invalidate the previous code", err)
	}
	proof, err := s.VerifyRegistrationCode(ctx, login, freshCode, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestRegistrationCode(ctx, login, c); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteRegistration(ctx, login, proof.Token, "not-to-store", c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("resend must invalidate an outstanding password-stage proof", err)
	}
	noSignupAccount(t, s, login)
}

func TestRegistrationProofIsBoundToEmailOriginAndExpiry(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := emailConfig()
	proof := signupProof(t, s, "scope@example.test")
	if _, err := s.CompleteRegistration(ctx, "other@example.test", proof.Token, "not-to-store", c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("proof must not cross email scopes", err)
	}
	other := c
	other.Origin = "https://other.example.test"
	if _, err := s.CompleteRegistration(ctx, "scope@example.test", proof.Token, "not-to-store", other); !errors.Is(err, store.ErrDenied) {
		t.Fatal("proof must not cross exact server origins", err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE registration_challenges SET registration_expires_at=now()-interval '1 second' WHERE login=$1", "scope@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteRegistration(ctx, "scope@example.test", proof.Token, "not-to-store", c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired email proof must not create an account", err)
	}
	noSignupAccount(t, s, "scope@example.test")
	noSignupAccount(t, s, "other@example.test")
}

func TestRegistrationVerificationAndCompletionAreSingleUseUnderConcurrency(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := emailConfig()
	login := "concurrent@example.test"
	if err := s.RequestRegistrationCode(ctx, login, c); err != nil {
		t.Fatal(err)
	}
	code := testutil.RegistrationCode(t, s, login)
	proofs := make(chan store.RegistrationProof, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proof, err := s.VerifyRegistrationCode(ctx, login, code, c)
			if err == nil {
				proofs <- proof
			} else if !errors.Is(err, store.ErrDenied) {
				t.Error("concurrent verification failed unexpectedly", err)
			}
		}()
	}
	wg.Wait()
	close(proofs)
	if len(proofs) != 1 {
		t.Fatal("a code must issue exactly one proof")
	}
	proof := <-proofs
	results := make(chan bool, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CompleteRegistration(ctx, login, proof.Token, "test-only-concurrent-hash", c)
			results <- err == nil
			if err != nil && !errors.Is(err, store.ErrDenied) {
				t.Error("concurrent completion failed unexpectedly", err)
			}
		}()
	}
	wg.Wait()
	close(results)
	var completed int
	for success := range results {
		if success {
			completed++
		}
	}
	if completed != 1 {
		t.Fatal("a proof must create exactly one account")
	}
}

func TestRegistrationNeverAdoptsAnUnverifiedAccount(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := emailConfig()
	login := "existing-pending@example.test"
	proof := signupProof(t, s, login)
	existing := testutil.Account(t, s, login)
	if _, err := s.Pool.Exec(ctx, "UPDATE accounts SET verified=false WHERE id=$1", existing.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestRegistrationCode(ctx, login, c); err != nil {
		t.Fatal("existing accounts must receive the same accepted response", err)
	}
	if _, err := s.CompleteRegistration(ctx, login, proof.Token, "replacement-must-not-be-stored", c); !errors.Is(err, store.ErrConflict) {
		t.Fatal("signup must not activate an existing unverified account", err)
	}
	after, err := s.AccountByLogin(ctx, login)
	if err != nil || after.Verified || after.ID != existing.ID || after.PasswordHash != existing.PasswordHash {
		t.Fatal("registration changed existing account credentials", err)
	}
}

func TestRegistrationNeverOverwritesExistingOrDisabledAccounts(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := emailConfig()
	existing := testutil.Account(t, s, "existing@example.test")
	if err := s.RequestRegistrationCode(ctx, existing.Login, c); err != nil {
		t.Fatal("known account must get the same accepted send outcome", err)
	}
	var mailCount int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM registration_challenges WHERE login=$1", existing.Login).Scan(&mailCount); err != nil || mailCount != 0 {
		t.Fatal("registration must not issue a code for a verified account", err)
	}
	proof := signupProof(t, s, "blocked@example.test")
	blocked := testutil.Account(t, s, "blocked@example.test")
	if _, err := s.CompleteRegistration(ctx, blocked.Login, proof.Token, "not-to-store", c); !errors.Is(err, store.ErrConflict) {
		t.Fatal("an account created during signup must not be overwritten", err)
	}
	after, err := s.AccountByLogin(ctx, blocked.Login)
	if err != nil || after.PasswordHash != blocked.PasswordHash {
		t.Fatal("concurrent account must retain its credentials", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE accounts SET verified=false,disabled=true WHERE id=$1", blocked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteRegistration(ctx, blocked.Login, proof.Token, "not-to-store", c); !errors.Is(err, store.ErrConflict) {
		t.Fatal("signup must never reactivate a disabled account", err)
	}
}
