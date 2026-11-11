package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestSponsorshipImageIsPublicAndOnlyBelongsToOfficialHostedService(t *testing.T) {
	for _, item := range []struct {
		mode, origin string
		enabled      bool
	}{
		{"hosted", officialSponsorshipOrigin, true}, {"selfhost", officialSponsorshipOrigin, false}, {"hosted", "https://custom.example.test", false},
	} {
		c := testutil.Config()
		c.Mode, c.Origin = item.mode, item.origin
		a, err := New(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Hub.Shutdown)
		w := webRequest(t, a, "GET", "/v1/sponsorship", nil, nil)
		if !item.enabled {
			expectStatus(t, w, 404, "sponsorship_unavailable")
			continue
		}
		expectStatus(t, w, 200, "")
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("purchase image may become stale")
		}
		var value map[string]string
		if err = json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if len(value) != 1 || !strings.HasPrefix(value["image"], "data:image/png;base64,") {
			t.Fatal("unexpected public image shape")
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value["image"], "data:image/png;base64,"))
		if err != nil {
			t.Fatal(err)
		}
		config, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil || config.Width != 320 || config.Height != 320 {
			t.Fatal("invalid QR image", config, err)
		}
		expected, err := webFiles.ReadFile("web/sponsorship.png")
		if err != nil || !bytes.Equal(raw, expected) {
			t.Fatal("service did not deliver its own purchase image", err)
		}
	}
}
