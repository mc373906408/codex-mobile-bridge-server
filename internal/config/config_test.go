package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func environment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"BRIDGE_MODE", "BRIDGE_LISTEN", "BRIDGE_ORIGIN", "DATABASE_URL", "BRIDGE_DEVELOPMENT",
		"BRIDGE_ADMIN_LOGIN", "BRIDGE_ADMIN_PASSWORD", "BRIDGE_ADMIN_SETUP_TOKEN", "BRIDGE_DATA_KEY", "BRIDGE_TRUSTED_PROXIES", "SMTP_ADDRESS", "SMTP_HOST",
		"SMTP_USER", "SMTP_FROM", "SMTP_TLS", "SMTP_PASSWORD", "UMENG_APP_KEY", "UMENG_APP_MASTER_SECRET", "UMENG_MESSAGE_SECRET",
		"UMENG_PRODUCTION", "APNS_TEAM_ID", "APNS_KEY_ID", "APNS_TOPIC", "APNS_PRIVATE_KEY", "APNS_SANDBOX", "BRIDGE_MAX_GATEWAYS",
		"BRIDGE_MAX_INSTALLATIONS", "BRIDGE_MAX_CONNECTIONS", "BRIDGE_MAX_PER_GATEWAY", "BRIDGE_BYTES_PER_SECOND"} {
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", "")
	}
	t.Setenv("BRIDGE_ORIGIN", "https://relay.example.test")
	t.Setenv("DATABASE_URL", "postgres://local-test@127.0.0.1/test")
	t.Setenv("BRIDGE_DATA_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
}

func TestIndependentSelfhostAndHostedSMTPRequirement(t *testing.T) {
	environment(t)
	c, err := Load()
	if err != nil || c.Paid() || c.MailEnabled() || c.UmengEnabled() || c.APNSEnabled() {
		t.Fatal("selfhost must start without official services or vendors", err)
	}
	t.Setenv("BRIDGE_MODE", "hosted")
	if _, err := Load(); err == nil {
		t.Fatal("hosted accounts require verifiable email delivery")
	}
	t.Setenv("SMTP_ADDRESS", "smtp.example.test:587")
	t.Setenv("SMTP_HOST", "smtp.example.test")
	t.Setenv("SMTP_FROM", "Bridge <bridge@example.test>")
	if c, err := Load(); err != nil || !c.Paid() || !c.MailEnabled() {
		t.Fatal(err)
	}
	t.Setenv("SMTP_TLS", "none")
	if _, err := Load(); err == nil {
		t.Fatal("SMTP certificate/TLS verification cannot be disabled")
	}
}

func TestSecretFilesAndConflictingSources(t *testing.T) {
	environment(t)
	file := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(file, []byte("postgres://test-only@127.0.0.1/isolated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", file)
	if c, err := Load(); err != nil || c.DatabaseURL != "postgres://test-only@127.0.0.1/isolated" {
		t.Fatal("database credentials must support mounted secrets", err)
	}
	t.Setenv("DATABASE_URL", "postgres://other-secret@127.0.0.1/other")
	if _, err := Load(); err == nil || strings.Contains(err.Error(), "other-secret") {
		t.Fatal("ambiguous secret sources must fail without leaking either value", err)
	}
}

func TestUmengClientSecretCannotBeUsedForServerSigning(t *testing.T) {
	environment(t)
	t.Setenv("UMENG_APP_KEY", "test-app")
	t.Setenv("UMENG_MESSAGE_SECRET", "test-client-secret")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "UMENG_APP_MASTER_SECRET") || strings.Contains(err.Error(), "test-client-secret") {
		t.Fatal("do not silently sign HTTP requests with Android Message Secret", err)
	}
	t.Setenv("UMENG_MESSAGE_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("partial provider credentials must fail")
	}
	t.Setenv("UMENG_APP_MASTER_SECRET", "local-test-master-secret")
	if c, err := Load(); err != nil || !c.UmengEnabled() {
		t.Fatal(err)
	}
}

func TestOriginAndProxyConfigurationFailClosed(t *testing.T) {
	environment(t)
	for _, origin := range []string{"http://relay.example.test", "https://relay.example.test/", "https://user:pass@relay.example.test",
		"https://relay.example.test?next=other", "https://RELAY.example.test", "https://relay.example.test:443",
		"https://relay.example.test:08443", "https://relay.example.test:0", "https://relay.example.test:65536"} {
		t.Setenv("BRIDGE_ORIGIN", origin)
		if _, err := Load(); err == nil {
			t.Fatal("non-canonical/insecure origin accepted", origin)
		}
	}
	t.Setenv("BRIDGE_ORIGIN", "https://relay.example.test:8443")
	t.Setenv("BRIDGE_TRUSTED_PROXIES", "127.0.0.1/32,::1/128")
	if c, err := Load(); err != nil || len(c.TrustedProxies) != 2 {
		t.Fatal(err)
	}
	t.Setenv("BRIDGE_TRUSTED_PROXIES", "proxy.example.test")
	if _, err := Load(); err == nil {
		t.Fatal("trusted proxies must be explicit CIDRs, not arbitrary hostnames")
	}
}

func TestDevelopmentAndBootstrapRestrictions(t *testing.T) {
	environment(t)
	t.Setenv("BRIDGE_DEVELOPMENT", "true")
	t.Setenv("BRIDGE_ORIGIN", "http://127.0.0.1:8080")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRIDGE_LISTEN", "0.0.0.0:8080")
	if _, err := Load(); err == nil {
		t.Fatal("development HTTP must never bind a public interface")
	}
	t.Setenv("BRIDGE_LISTEN", "127.0.0.1:8080")
	t.Setenv("BRIDGE_ADMIN_LOGIN", "local-admin")
	if _, err := Load(); err == nil {
		t.Fatal("bootstrap credentials must be configured together")
	}
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "too-short")
	if _, err := Load(); err == nil {
		t.Fatal("weak bootstrap credentials must fail")
	}
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "local-test-admin-password")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultAdministratorNeedsNoManualCodeInEitherMode(t *testing.T) {
	environment(t)
	if c, err := Load(); err != nil || c.AdminLogin != "admin" || c.AdminPassword != "admin" {
		t.Fatal("HTTPS selfhost must support setup-only admin/admin without a manual code", err)
	}
	t.Setenv("BRIDGE_ADMIN_SETUP_TOKEN", "ignored-legacy-value")
	t.Setenv("BRIDGE_ADMIN_SETUP_TOKEN_FILE", filepath.Join(t.TempDir(), "no-longer-required"))
	if _, err := Load(); err != nil {
		t.Fatal("legacy initialization-code settings must not prevent an upgrade", err)
	}
	t.Setenv("BRIDGE_MODE", "hosted")
	t.Setenv("SMTP_ADDRESS", "smtp.example.test:465")
	t.Setenv("SMTP_HOST", "smtp.example.test")
	t.Setenv("SMTP_FROM", "bridge@example.test")
	t.Setenv("SMTP_TLS", "implicit")
	if c, err := Load(); err != nil || c.AdminLogin != "admin" || !c.Paid() {
		t.Fatal("hosted administrators must not require an email login", err)
	}
}

func TestSMTPSecretFileAndEmptyFileFailClosed(t *testing.T) {
	environment(t)
	file := filepath.Join(t.TempDir(), "smtp-password")
	password := "test-only-smtp-password"
	if err := os.WriteFile(file, []byte(password+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SMTP_PASSWORD_FILE", file)
	if c, err := Load(); err != nil || c.SMTP.Password != password {
		t.Fatal("SMTP_PASSWORD_FILE must read a file rather than treat its path as a password", err)
	}
	t.Setenv("SMTP_PASSWORD", "conflicting-test-password")
	if _, err := Load(); err == nil || strings.Contains(err.Error(), password) {
		t.Fatal("ambiguous SMTP sources must fail without credential disclosure", err)
	}
	t.Setenv("SMTP_PASSWORD", "")
	if err := os.WriteFile(file, []byte(" \r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("an explicitly mounted, empty secret must not silently disable protection")
	}
}
