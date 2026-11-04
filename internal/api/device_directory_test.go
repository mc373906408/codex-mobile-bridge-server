package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestLoginDiscoversDevicesWithoutAMobilePairingRequest(t *testing.T) {
	s := testutil.Database(t)
	c := testutil.Config()
	a, err := New(c, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Hub.Shutdown)
	account := testutil.Account(t, s, "request-api")
	p, _ := testutil.Principal(t, s, account)
	g, err := s.Enroll(context.Background(), p, "gateway", "", "Desktop", append([]byte{1}, make([]byte, 31)...), 8)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Enroll(context.Background(), p, "mobile", "", "Phone", append([]byte{2}, make([]byte, 31)...), 16)
	if err != nil {
		t.Fatal(err)
	}
	gh, mh := map[string]string{"Authorization": "Bearer " + g.Token}, map[string]string{"Authorization": "Bearer " + m.Token}
	expectStatus(t, webRequest(t, a, "POST", "/v1/pairings", map[string]string{}, gh), 404, "")
	expectStatus(t, webRequest(t, a, "GET", "/v1/mobile/bindings", nil, nil), 401, "")
	expectStatus(t, webRequest(t, a, "GET", "/v1/mobile/bindings", nil, gh), 403, "")
	w := webRequest(t, a, "GET", "/v1/mobile/bindings", nil, mh)
	expectStatus(t, w, 200, "")
	var directory struct {
		Bindings []store.Binding `json:"bindings"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &directory); err != nil || len(directory.Bindings) != 1 {
		t.Fatal("login did not discover the desktop", err)
	}
	b := directory.Bindings[0]
	id := b.ID
	for _, legacy := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/v1/pairing-requests", 404},
		{"GET", "/v1/pairing-requests/" + id, 404},
		{"DELETE", "/v1/pairing-requests/" + id, 404},
		{"POST", "/v1/bindings/" + id + "/approve", 404},
		{"DELETE", "/v1/gateway/bindings/" + id, 405},
	} {
		for _, headers := range []map[string]string{gh, mh} {
			expectStatus(t, webRequest(t, a, legacy.method, legacy.path, map[string]string{}, headers), legacy.status, "")
		}
	}
	if b.Status != "pending" || b.GatewayID != g.ID || b.GatewayName != "Desktop" || b.AccessRevision != 0 {
		t.Fatal("discovery granted authority or lost device identity")
	}
	expectStatus(t, webRequest(t, a, "GET", "/v1/gateway/bindings/"+id, nil, gh), 200, "")
	path := "/v1/gateway/bindings/" + id + "/access"
	in := map[string]any{"allowed": true, "expected_revision": 0, "mobile_public_key": base64.StdEncoding.EncodeToString(m.PublicKey)}
	expectStatus(t, webRequest(t, a, "POST", path, in, mh), 403, "")
	expectStatus(t, webRequest(t, a, "POST", path, in, gh), 200, "")
	expectStatus(t, webRequest(t, a, "POST", path, in, gh), 409, "state_conflict")
	in["allowed"] = false
	w = webRequest(t, a, "POST", path, in, gh)
	expectStatus(t, w, 200, "")
	if err = json.Unmarshal(w.Body.Bytes(), &b); err != nil || b.Status != "disabled" || b.AccessRevision != 2 {
		t.Fatal("closing did not fence the prior opening")
	}
	expectStatus(t, webRequest(t, a, "GET", "/v1/bindings/"+id, nil, mh), 200, "")
	in["allowed"], in["expected_revision"] = true, 2
	expectStatus(t, webRequest(t, a, "POST", path, in, gh), 200, "")
}
