package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func Mail(t *testing.T, s *store.Store, accountID, purpose string) store.MailMessage {
	t.Helper()
	var id string
	var encrypted []byte
	if err := s.Pool.QueryRow(context.Background(), `SELECT id,encrypted_message FROM mail_outbox
		WHERE account_id=$1 AND purpose=$2 AND status='queued' ORDER BY created_at DESC,id DESC LIMIT 1`, accountID, purpose).Scan(&id, &encrypted); err != nil {
		t.Fatal("test email unavailable", err)
	}
	data, err := s.Vault.Open("mail:"+id, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	var message store.MailMessage
	if err = json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, data) {
		t.Fatal("email body stored without encryption")
	}
	return message
}

func RegistrationCode(t *testing.T, s *store.Store, login string) string {
	t.Helper()
	var id string
	var encrypted []byte
	if err := s.Pool.QueryRow(context.Background(), `SELECT j.id,j.encrypted_message FROM mail_outbox j
		JOIN registration_challenges c ON c.id=j.registration_id WHERE c.login=$1 AND j.status='queued'
		ORDER BY j.created_at DESC,j.id DESC LIMIT 1`, login).Scan(&id, &encrypted); err != nil {
		t.Fatal("test registration email unavailable", err)
	}
	data, err := s.Vault.Open("mail:"+id, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	var message store.MailMessage
	if err = json.Unmarshal(data, &message); err != nil || bytes.Contains(encrypted, data) {
		t.Fatal("registration email must be encrypted", err)
	}
	if message.To != login {
		t.Fatal("registration code delivered to the wrong test email")
	}
	return emailCode(t, message)
}

func emailCode(t *testing.T, message store.MailMessage) string {
	t.Helper()
	if strings.Contains(message.Body, "://") || strings.Contains(message.Body, "#token=") || strings.Contains(message.Body, "/verify") {
		t.Fatal("verification email must not contain a link")
	}
	matches := regexp.MustCompile(`(?m)^([0-9]{6})\r?$`).FindStringSubmatch(message.Body)
	if len(matches) != 2 {
		t.Fatal("verification email must contain a six-digit code")
	}
	return matches[1]
}
