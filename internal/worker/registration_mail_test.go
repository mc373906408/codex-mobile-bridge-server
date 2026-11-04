package worker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/worker"
)

type registrationMail struct {
	messages []store.MailMessage
}

func (m *registrationMail) Send(_ context.Context, message store.MailMessage) error {
	m.messages = append(m.messages, message)
	return nil
}

func TestRegistrationMailDoesNotRequireAPrecreatedAccount(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	c.Mode = "hosted"
	c.SMTP.Address = "smtp.example.test:465"
	login := "mail@example.test"
	if err := s.RequestRegistrationCode(ctx, login, c); err != nil {
		t.Fatal(err)
	}
	code := testutil.RegistrationCode(t, s, login)
	mail := &registrationMail{}
	w := worker.Worker{Store: s, Mail: mail}
	worked, err := w.StepMail(ctx)
	if err != nil || !worked || len(mail.messages) != 1 || mail.messages[0].To != login || !strings.Contains(mail.messages[0].Body, "\r\n"+code+"\r\n") {
		t.Fatal("worker must deliver an encrypted signup email before account creation", err)
	}
	var accounts int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM accounts").Scan(&accounts); err != nil || accounts != 0 {
		t.Fatal("mail delivery must not create an account", err)
	}
}

func TestRegistrationMailResendAndVerificationCancelStaleDeliveries(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	c := testutil.Config()
	c.Mode = "hosted"
	c.SMTP.Address = "smtp.example.test:465"
	login := "resend-mail@example.test"
	for range 2 {
		if err := s.RequestRegistrationCode(ctx, login, c); err != nil {
			t.Fatal(err)
		}
	}
	code := testutil.RegistrationCode(t, s, login)
	mail := &registrationMail{}
	w := worker.Worker{Store: s, Mail: mail}
	if _, err := s.VerifyRegistrationCode(ctx, login, code, c); err != nil {
		t.Fatal(err)
	}
	worked, err := w.StepMail(ctx)
	if err != nil || worked || len(mail.messages) != 0 {
		t.Fatal("cancelled signup mail must not be delivered after verification", err)
	}
}
