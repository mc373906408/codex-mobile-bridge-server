package store_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestAuditTracksDeviceAccessAndConsentWithoutSecretsOrPollingNoise(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	g, m, b := testutil.Devices(t, s)
	c := testutil.Config()
	c.Umeng.AppKey, c.Umeng.MasterSecret = "synthetic-app", "synthetic-master-secret"
	input := store.EndpointInput{BindingID: b.ID, Platform: "umeng", AppID: c.Umeng.AppKey,
		Token: "synthetic-token-1234567890", Enabled: true, Consent: true}
	for range 2 {
		if _, err := s.RegisterEndpoint(ctx, m, input, c); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeviceBindings(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetBindingAccess(ctx, b.ID, g, m.PublicKey, false, b.AccessRevision, false); err != nil {
		t.Fatal(err)
	}
	input.Enabled, input.Consent = false, false
	for range 2 {
		if _, err := s.RegisterEndpoint(ctx, m, input, c); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.Pool.Query(ctx, "SELECT action,actor_id::text,account_id::text,device_id::text,session_id::text,details FROM audit_log ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int{}
	var encoded strings.Builder
	for rows.Next() {
		var action string
		var actor, account, device, session *string
		var details map[string]any
		if err := rows.Scan(&action, &actor, &account, &device, &session, &details); err != nil {
			t.Fatal(err)
		}
		counts[action]++
		data, _ := json.Marshal(details)
		encoded.Write(data)
		if action == "binding.access" || action == "push.consent" || action == "device.enroll" || action == "binding.discover" {
			if actor == nil || *actor != g.AccountID || account == nil || *account != g.AccountID || device == nil || session == nil {
				t.Fatal("device transition lacks actor/account/device/session attribution", action)
			}
		}
		if action == "push.consent" && details["after"].(map[string]any)["enabled"] == false {
			if details["before"].(map[string]any)["consent"] != true || details["after"].(map[string]any)["consent"] != false {
				t.Fatal("withdrawal lacks its consent transition")
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if counts["device.enroll"] != 2 || counts["binding.discover"] != 1 || counts["binding.access"] != 2 || counts["push.consent"] != 2 || counts["session.create"] != 2 {
		t.Fatal("audit has missing transitions or logs no-op polling", counts)
	}
	for _, secret := range []string{g.Token, m.Token, input.Token, c.Umeng.MasterSecret, base64.StdEncoding.EncodeToString(g.PublicKey)} {
		if strings.Contains(encoded.String(), secret) {
			t.Fatal("audit retained secret payload")
		}
	}
}

func TestAuditFailureRollsBackEnrollmentAndConsent(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	g, m, b := testutil.Devices(t, s)
	owner, err := s.AccountByLogin(ctx, "devices")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := testutil.Principal(t, s, owner)
	if _, err = s.Pool.Exec(ctx, `CREATE FUNCTION reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$;
		CREATE TRIGGER reject_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Enroll(ctx, p, "mobile", "", "Must roll back", append([]byte{9}, make([]byte, 31)...), 16); err == nil {
		t.Fatal("enrollment succeeded without an audit")
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM installations WHERE account_id=$1", g.AccountID).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed enrollment left a device or discovered binding", err)
	}
	c := testutil.Config()
	c.Umeng.AppKey, c.Umeng.MasterSecret = "synthetic-app", "synthetic-secret"
	_, err = s.RegisterEndpoint(ctx, m, store.EndpointInput{BindingID: b.ID, Platform: "umeng", AppID: c.Umeng.AppKey,
		Token: "synthetic-token-1234567890", Enabled: true, Consent: true}, c)
	if err == nil {
		t.Fatal("notification permission enabled without audit")
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM push_endpoints WHERE binding_id=$1", b.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure left a registered endpoint", err)
	}
}
