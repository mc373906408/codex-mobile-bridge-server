package api

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestPhoneWithdrawsPushWithoutDesktopAccess(t *testing.T) {
	for _, change := range []string{"binding_disabled", "gateway_session_revoked", "gateway_revoked", "hosted_expired"} {
		t.Run(change, func(t *testing.T) {
			s := testutil.Database(t)
			ctx := context.Background()
			c := testutil.Config()
			c.Umeng.AppKey, c.Umeng.MasterSecret = "synthetic-app", "synthetic-secret"
			g, m, b := testutil.Devices(t, s)
			a, err := New(c, s)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Hub.Shutdown)
			headers := map[string]string{"Authorization": "Bearer " + m.Token}
			input := store.EndpointInput{BindingID: b.ID, Platform: "umeng", AppID: c.Umeng.AppKey,
				Token: "synthetic-token-1234567890", Enabled: true, Consent: true}
			expectStatus(t, webRequest(t, a, "POST", "/v1/push/endpoints", input, headers), 200, "")
			endpoint, err := s.RegisterEndpoint(ctx, m, input, c)
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{"queued", "leased"} {
				notification, err := security.ID()
				if err != nil {
					t.Fatal(err)
				}
				job, err := s.QueuePush(ctx, g, store.Envelope{Version: 1, BindingID: b.ID,
					NotificationID: notification, ExpiresAt: time.Now().Unix() + 600,
					Ciphertext: base64.StdEncoding.EncodeToString(make([]byte, 80))}, endpoint.Revision, false)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Pool.Exec(ctx, "UPDATE push_jobs SET status=$2 WHERE id=$1", job, state); err != nil {
					t.Fatal(err)
				}
			}
			switch change {
			case "binding_disabled":
				access := map[string]any{"allowed": false, "expected_revision": b.AccessRevision,
					"mobile_public_key": base64.StdEncoding.EncodeToString(m.PublicKey)}
				expectStatus(t, webRequest(t, a, "POST", "/v1/gateway/bindings/"+b.ID+"/access", access,
					map[string]string{"Authorization": "Bearer " + g.Token}), 200, "")
			case "gateway_session_revoked":
				_, err = s.Pool.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE id=$1", g.SessionID)
			case "gateway_revoked":
				_, err = s.Pool.Exec(ctx, "UPDATE gateways SET revoked_at=now() WHERE id=$1", g.ID)
			case "hosted_expired":
				a.Config.Mode = "hosted"
				_, err = s.Pool.Exec(ctx, "UPDATE entitlements SET expires_at=now()-interval '1 day' WHERE account_id=$1", g.AccountID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if w := webRequest(t, a, "POST", "/v1/push/endpoints", input, headers); w.Code == 200 {
				t.Fatal("disabled access still enabled push")
			}
			input.Enabled, input.Consent = false, false
			expectStatus(t, webRequest(t, a, "POST", "/v1/push/endpoints", input, headers), 200, "")
			expectStatus(t, webRequest(t, a, "POST", "/v1/push/endpoints", input, headers), 200, "")
			var consent, enabled bool
			if err = s.Pool.QueryRow(ctx, "SELECT consent,enabled FROM push_endpoints WHERE binding_id=$1", b.ID).Scan(&consent, &enabled); err != nil || consent || enabled {
				t.Fatal("withdrawal was not persisted", err)
			}
			var cancelled int
			if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM push_jobs WHERE binding_id=$1 AND status='cancelled'", b.ID).Scan(&cancelled); err != nil || cancelled != 2 {
				t.Fatal("withdrawal left queued or leased jobs eligible", err)
			}
			if change == "binding_disabled" {
				current, err := s.DirectoryBinding(ctx, b.ID, g)
				if err != nil {
					t.Fatal(err)
				}
				restored, err := s.SetBindingAccess(ctx, b.ID, g, m.PublicKey, true, current.AccessRevision, false)
				if err != nil || restored.PushEnabled {
					t.Fatal("restoring access revived withdrawn consent", err)
				}
			}
		})
	}
}

func TestPushWithdrawalRejectsOtherPhonesAndExpiredCaller(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	g, m, b := testutil.Devices(t, s)
	a, err := New(testutil.Config(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	owner, err := s.AccountByLogin(ctx, "devices")
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []store.Account{owner, testutil.Account(t, s, "outsider")} {
		p, _ := testutil.Principal(t, s, account)
		other, err := s.Enroll(ctx, p, "mobile", "", "Other phone", append([]byte{7}, make([]byte, 31)...), 16)
		if err != nil {
			t.Fatal(err)
		}
		expectStatus(t, webRequest(t, a, "POST", "/v1/push/endpoints", store.EndpointInput{BindingID: b.ID},
			map[string]string{"Authorization": "Bearer " + other.Token}), 403, "")
	}
	expectStatus(t, webRequest(t, a, "POST", "/v1/push/endpoints", store.EndpointInput{BindingID: b.ID},
		map[string]string{"Authorization": "Bearer " + g.Token}), 403, "")
	if _, err = s.Pool.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE id=$1", m.SessionID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, webRequest(t, a, "POST", "/v1/push/endpoints", store.EndpointInput{BindingID: b.ID},
		map[string]string{"Authorization": "Bearer " + m.Token}), 403, "")
}
