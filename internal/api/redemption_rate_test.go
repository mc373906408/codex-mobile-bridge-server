package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestCapabilitiesDeclareNewCodeLengthWithoutEnablingSelfhostRedemption(t *testing.T) {
	for _, mode := range []string{"hosted", "selfhost"} {
		t.Run(mode, func(t *testing.T) {
			c := testutil.Config()
			c.Mode = mode
			a, err := New(c, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Hub.Shutdown)
			response := webRequest(t, a, "GET", "/v1/capabilities", nil, nil)
			expectStatus(t, response, 200, "")
			var value struct {
				Redemption bool `json:"redemption"`
				Length     int  `json:"redemption_code_length"`
				BatchLimit int  `json:"redemption_batch_limit"`
			}
			if err = json.Unmarshal(response.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			want := 0
			if c.Paid() {
				want = security.RedemptionCodeLength
			}
			if value.Redemption != c.Paid() || value.Length != want {
				t.Fatal("capabilities must describe generation length without enabling selfhost codes")
			}
			if (c.Paid() && value.BatchLimit != 1) || (!c.Paid() && value.BatchLimit != 0) {
				t.Fatal("batch policy must be explicit only when redemption is available")
			}
		})
	}
}

func TestRedemptionGuessLimitsProtectShortCodes(t *testing.T) {
	for _, dimension := range []string{"account", "ip"} {
		t.Run(dimension, func(t *testing.T) {
			s := testutil.Database(t)
			ctx := context.Background()
			c := testutil.Config()
			c.Mode = "hosted"
			a, err := New(c, s)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Hub.Shutdown)
			attempts := 10
			accounts := 1
			if dimension == "ip" {
				attempts = 40
				accounts = 5
			}
			var principals []store.Account
			var bearers []map[string]string
			for index := range accounts {
				account := testutil.Account(t, s, fmt.Sprintf("limited-%d@example.test", index))
				tokens, err := s.NewSession(ctx, account, c, false)
				if err != nil {
					t.Fatal(err)
				}
				principals = append(principals, account)
				bearers = append(bearers, map[string]string{"Authorization": "Bearer " + tokens.Access})
			}
			requestID, _ := security.ID()
			input := map[string]string{"code": "invalid-guess", "request_id": requestID}
			for index := range attempts {
				expectStatus(t, webRequest(t, a, "POST", "/v1/account/redeem", input, bearers[index%accounts]), 400, "invalid_redemption")
			}
			codes, err := s.GenerateCodes(ctx, principals[0].ID, 7, 1)
			if err != nil {
				t.Fatal(err)
			}
			input["code"] = codes[0].Code
			expectStatus(t, webRequest(t, a, "POST", "/v1/account/redeem", input, bearers[0]), 429, "rate_limited")
			var untouched bool
			if err = s.Pool.QueryRow(ctx, "SELECT used_at IS NULL FROM redemption_codes WHERE id=$1", codes[0].ID).Scan(&untouched); err != nil || !untouched {
				t.Fatal("a rate-limited request must not consume a valid code", err)
			}
			// Advance only this test schema's persisted limits, not production state.
			if _, err = s.Pool.Exec(ctx, "UPDATE rate_limits SET window_start=now()-interval '2 hours'"); err != nil {
				t.Fatal(err)
			}
			expectStatus(t, webRequest(t, a, "POST", "/v1/account/redeem", input, bearers[0]), 200, "")
		})
	}
}
