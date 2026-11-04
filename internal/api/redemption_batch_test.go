package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestAccountCannotRedeemTwoCodesFromOneBatchAcrossSessions(t *testing.T) {
	s := testutil.Database(t)
	c := testutil.Config()
	c.Mode = "hosted"
	app, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Hub.Shutdown)
	account := testutil.Account(t, s, "api-batch-user")
	_, firstToken := testutil.Principal(t, s, account)
	_, secondToken := testutil.Principal(t, s, account)
	codes, err := s.GenerateCodes(context.Background(), account.ID, 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := security.ID()
	first := webRequest(t, app, "POST", "/v1/account/redeem", map[string]any{"code": codes[0].Code, "request_id": id}, map[string]string{"Authorization": "Bearer " + firstToken.Access})
	expectStatus(t, first, 200, "")
	id2, _ := security.ID()
	denied := webRequest(t, app, "POST", "/v1/account/redeem", map[string]any{"code": codes[1].Code, "request_id": id2}, map[string]string{"Authorization": "Bearer " + secondToken.Access})
	expectStatus(t, denied, 409, "redemption_batch_already_used")
	retry := webRequest(t, app, "POST", "/v1/account/redeem", map[string]any{"code": codes[0].Code, "request_id": id}, map[string]string{"Authorization": "Bearer " + secondToken.Access})
	expectStatus(t, retry, 200, "")
	var before, after store.Redemption
	if json.Unmarshal(first.Body.Bytes(), &before) != nil || json.Unmarshal(retry.Body.Bytes(), &after) != nil || before != after {
		t.Fatal("same-code receipt changed across sessions")
	}
}
