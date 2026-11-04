package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuthRotationResetAndEmailFirstRegistration(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	c.Mode = "hosted"
	c.SMTP.Address = "smtp.example.test:587"
	hash, _ := security.Password("test-only-password-123")
	login := "account@example.test"
	proof := signupProof(t, s, login)
	noSignupAccount(t, s, login)
	a, err := s.CompleteRegistration(ctx, login, proof.Token, hash, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE accounts SET verified=false WHERE id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NewSession(ctx, a, c, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("unverified persisted account accepted despite stale verified snapshot", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE accounts SET verified=true WHERE id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	var mailID string
	var encrypted, data []byte
	var message store.MailMessage
	first, err := s.NewSession(ctx, a, c, false)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := s.Refresh(ctx, first.Refresh, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, first.Access, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("old access token accepted")
	}
	if _, err = s.Refresh(ctx, first.Refresh, c); !errors.Is(err, store.ErrDenied) {
		t.Fatal("refresh reuse accepted")
	}
	if _, err = s.Authenticate(ctx, rotated.Access, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("refresh family not revoked")
	}
	_, fresh := testutil.Principal(t, s, a)
	if err = s.RequestPasswordReset(ctx, a.Login, c); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT id,encrypted_message FROM mail_outbox WHERE account_id=$1 AND purpose='reset'", a.ID).Scan(&mailID, &encrypted); err != nil {
		t.Fatal(err)
	}
	data, _ = s.Vault.Open("mail:"+mailID, encrypted)
	_ = json.Unmarshal(data, &message)
	reset := strings.Split(strings.Split(message.Body, "#token=")[1], "\r\n")[0]
	nextHash, _ := security.Password("another-test-password-123")
	if _, err = s.ConsumePasswordResetToken(ctx, reset, nextHash); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, fresh.Access, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("reset did not revoke sessions")
	}
	if _, err = s.NewSession(ctx, a, c, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("stale password login race")
	}
	if _, err = s.ConsumePasswordResetToken(ctx, reset, nextHash); !errors.Is(err, store.ErrDenied) {
		t.Fatal("reset reused")
	}
	if err = s.RequestPasswordReset(ctx, a.Login, c); err != nil {
		t.Fatal(err)
	}
	message = testutil.Mail(t, s, a.ID, "reset")
	reset = strings.Split(strings.Split(message.Body, "#token=")[1], "\r\n")[0]
	_, _ = s.Pool.Exec(ctx, "UPDATE email_tokens SET expires_at=now()-interval '1 second' WHERE account_id=$1", a.ID)
	if _, err = s.ConsumePasswordResetToken(ctx, reset, nextHash); !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired reset accepted")
	}
}

func TestConcurrentRedemptionsAndUTC(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a := testutil.Account(t, s, "redeemer")
	var codes []store.GeneratedCode
	for range 3 {
		batch, err := s.GenerateCodes(ctx, a.ID, 7, 1)
		if err != nil {
			t.Fatal(err)
		}
		codes = append(codes, batch[0])
	}
	var err error
	request, _ := security.ID()
	var wg sync.WaitGroup
	outcomes := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Redeem(ctx, a.ID, codes[0].Code, request); outcomes <- err }()
	}
	wg.Wait()
	close(outcomes)
	for err := range outcomes {
		if err != nil {
			t.Fatal(err)
		}
	}
	e, err := s.Entitlement(ctx, a.ID, true)
	if err != nil || !e.Active {
		t.Fatal(err)
	}
	var count int
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM redemptions WHERE account_id=$1", a.ID).Scan(&count)
	if count != 1 {
		t.Fatal("concurrent duplicate redemption", count)
	}
	baseline := *e.ExpiresAt
	request2, _ := security.ID()
	if _, err = s.Redeem(ctx, a.ID, codes[1].Code, request2); err != nil {
		t.Fatal(err)
	}
	e, _ = s.Entitlement(ctx, a.ID, true)
	if e.ExpiresAt.Sub(baseline) != 7*24*time.Hour {
		t.Fatal("did not extend existing entitlement")
	}
	if _, err = s.Redeem(ctx, a.ID, codes[2].Code, request2); !errors.Is(err, store.ErrConflict) {
		t.Fatal("idempotency key substitution accepted", err)
	}
	_, _ = s.Pool.Exec(ctx, "UPDATE entitlements SET expires_at=now()-interval '1 day' WHERE account_id=$1", a.ID)
	request3, _ := security.ID()
	before := time.Now().UTC()
	if _, err = s.Redeem(ctx, a.ID, codes[2].Code, request3); err != nil {
		t.Fatal(err)
	}
	e, _ = s.Entitlement(ctx, a.ID, true)
	if e.ExpiresAt.Before(before.Add(7*24*time.Hour-time.Second)) || e.ExpiresAt.After(time.Now().UTC().Add(7*24*time.Hour+time.Second)) {
		t.Fatal("expired account did not start at server UTC")
	}
	request4, _ := security.ID()
	if _, err = s.Redeem(ctx, a.ID, codes[0].Code, request4); err != nil {
		t.Fatal("same code same account must return original result", err)
	}
	other := testutil.Account(t, s, "other")
	if _, err = s.Redeem(ctx, other.ID, codes[0].Code, request4); !errors.Is(err, store.ErrDenied) {
		t.Fatal("code shared across accounts")
	}
}

func TestBindingAccountIsolationAndRevocation(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	g, m, b := testutil.Devices(t, s)
	other := testutil.Account(t, s, "outsider")
	p, _ := testutil.Principal(t, s, other)
	outsider, err := s.Enroll(ctx, p, "mobile", "", "", append([]byte{3}, make([]byte, 31)...), 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Binding(ctx, b.ID, outsider, true); !errors.Is(err, store.ErrDenied) {
		t.Fatal("cross-account binding")
	}
	if err = s.RevokeBinding(ctx, b.ID, g.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Binding(ctx, b.ID, m, true); !errors.Is(err, store.ErrDenied) {
		t.Fatal("revoked binding")
	}
	_, _ = s.Pool.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE id=$1", g.SessionID)
	if _, err = s.Device(ctx, g.Token, "gateway"); !errors.Is(err, store.ErrDenied) {
		t.Fatal("revoked session still authorizes a gateway")
	}
}
