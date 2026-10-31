package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func envelope() store.Envelope {
	return store.Envelope{Version: 1, NotificationID: "e22e4bce-551a-4ad9-93fa-3b266756b0ad",
		BindingID: "a75e8da3-7b36-4e1c-b6f0-91e06f1c9d44", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		Ciphertext: strings.Repeat("A", 1440)}
}

func TestAPNSPayloadContractAndWholeJSONLimit(t *testing.T) {
	e := envelope()
	data, err := APNSPayload(e)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		APS struct {
			Alert   map[string]string `json:"alert"`
			Mutable int               `json:"mutable-content"`
		} `json:"aps"`
		Envelope store.Envelope `json:"bridge"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Envelope != e || payload.APS.Mutable != 1 ||
		payload.APS.Alert["title"] != GenericTitle || payload.APS.Alert["body"] != GenericBody {
		t.Fatal("APNs must contain a dictionary envelope and generic outer alert", err)
	}
	e.Ciphertext = ""
	base, _ := APNSPayload(e)
	e.Ciphertext = strings.Repeat("A", 4096-len(base))
	if data, err := APNSPayload(e); err != nil || len(data) != 4096 {
		t.Fatal("4096-byte complete payload should fit", err)
	}
	e.Ciphertext += "A"
	if _, err := APNSPayload(e); err == nil {
		t.Fatal("the complete JSON, not only ciphertext, must fit in 4096 bytes")
	}
}

func TestUmengStringEnvelopeAndAppMasterSignature(t *testing.T) {
	c := config.Config{Umeng: config.Umeng{AppKey: "test-app", MasterSecret: "local-test-app-master-secret", Production: true}}
	p, err := NewProviders(c)
	if err != nil {
		t.Fatal(err)
	}
	d := Delivery{Platform: "umeng", AppID: c.Umeng.AppKey, Token: "local-test-device-token", Envelope: envelope()}
	p.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.Host != "msgapi.umeng.com" || r.URL.Path != "/api/send" {
			t.Fatal("provider destination must remain fixed")
		}
		hash := md5.Sum(append(append([]byte("POSThttps://msgapi.umeng.com/api/send"), body...), []byte(c.Umeng.MasterSecret)...))
		if r.URL.Query().Get("sign") != hex.EncodeToString(hash[:]) || strings.Contains(string(body), c.Umeng.MasterSecret) {
			t.Fatal("sign exactly the transmitted body with App Master Secret, without transmitting it")
		}
		var payload struct {
			Production string `json:"production_mode"`
			Payload    struct {
				Display string            `json:"display_type"`
				Body    map[string]string `json:"body"`
				Extra   map[string]string `json:"extra"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal("UMessage.extra requires string values", err)
		}
		var got store.Envelope
		if err := json.Unmarshal([]byte(payload.Payload.Extra[UmengEnvelopeKey]), &got); err != nil || got != d.Envelope {
			t.Fatal("Android must receive the same bridge_preview envelope", err)
		}
		if len(payload.Payload.Extra) != 1 || payload.Production != "true" || payload.Payload.Display != "notification" ||
			payload.Payload.Body["title"] != GenericTitle || payload.Payload.Body["text"] != GenericBody ||
			payload.Payload.Body["after_open"] != "go_app" {
			t.Fatal("vendor-visible content must remain generic and non-navigating")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ret":"SUCCESS","data":{"msg_id":"local-test-id"}}`))}, nil
	})
	if result := p.Send(context.Background(), d); !result.Accepted || result.Code != "accepted" {
		t.Fatal(result)
	}
	if p.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("provider redirects must not be followed")
	}
}

func TestProviderFailuresDoNotBecomeDeliveryReceipts(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       Result
	}{
		{"invalid token", `{"ret":"FAIL","data":{"error_code":"2010"}}`, 200, Result{Code: "invalid_token", InvalidToken: true, Permanent: true}},
		{"rate limit", `{}`, 429, Result{Code: "temporary"}},
		{"server error", `{}`, 503, Result{Code: "temporary"}},
		{"ambiguous body", `not-json`, 200, Result{Code: "result_unknown"}},
		{"missing acceptance id", `{"ret":"SUCCESS","data":{}}`, 200, Result{Code: "provider_rejected", Permanent: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, _ := NewProviders(config.Config{Umeng: config.Umeng{AppKey: "app", MasterSecret: "test-only"}})
			p.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			if got := p.Send(context.Background(), Delivery{Platform: "umeng", AppID: "app", Envelope: envelope()}); got != test.want {
				t.Fatal(got)
			}
		})
	}
	p, _ := NewProviders(config.Config{Umeng: config.Umeng{AppKey: "app", MasterSecret: "test-only"}})
	p.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("lost response") })
	if got := p.Send(context.Background(), Delivery{Platform: "umeng", AppID: "app", Envelope: envelope()}); got.Code != "result_unknown" || got.Accepted {
		t.Fatal(got)
	}
}

func TestAPNSHeadersShareNotificationID(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	c := config.Config{APNS: config.APNS{TeamID: "TESTTEAM", KeyID: "TESTKEY", Topic: "dev.waken.bridgeMobile",
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), Sandbox: true}}
	p, err := NewProviders(c)
	if err != nil {
		t.Fatal(err)
	}
	d := Delivery{Platform: "apns", AppID: c.APNS.Topic, Token: strings.Repeat("a", 64), Envelope: envelope()}
	p.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.sandbox.push.apple.com" || r.URL.Path != "/3/device/"+d.Token ||
			r.Header.Get("apns-id") != d.Envelope.NotificationID || r.Header.Get("apns-collapse-id") != d.Envelope.NotificationID ||
			r.Header.Get("apns-expiration") != strconv.FormatInt(d.Envelope.ExpiresAt, 10) ||
			r.Header.Get("apns-topic") != d.AppID || r.Header.Get("apns-push-type") != "alert" ||
			!strings.HasPrefix(r.Header.Get("authorization"), "bearer ") {
			t.Fatal("APNs identity, expiry, topic, and sandbox headers do not match")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if got := p.Send(context.Background(), d); !got.Accepted {
		t.Fatal(got)
	}
	d.AppID = "wrong.app"
	if got := p.Send(context.Background(), d); got.Code != "platform_disabled" || !got.Permanent {
		t.Fatal("cannot choose an arbitrary provider application", got)
	}
}
