package config

import "testing"

func TestWebOriginMustBeSeparateCanonicalHTTPS(t *testing.T) {
	c := Config{Mode: "selfhost", Origin: "https://relay.example.test:8787", Listen: "127.0.0.1:8080", DataKey: make([]byte, 32), DatabaseURL: "synthetic"}
	for _, origin := range []string{"", "https://relay.example.test:18787"} {
		c.WebOrigin = origin
		if err := c.Validate(); err != nil {
			t.Fatal(origin, err)
		}
	}
	for _, origin := range []string{c.Origin, "http://relay.example.test:18787", "https://relay.example.test:18787/", "https://Relay.example.test:18787", "https://relay.example.test:18787?token=no", "https://user@relay.example.test:18787"} {
		c.WebOrigin = origin
		if c.Validate() == nil {
			t.Fatal("accepted", origin)
		}
	}
}
