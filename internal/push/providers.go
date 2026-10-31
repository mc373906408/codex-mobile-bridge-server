package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/md5" // Umeng's HTTP signature requires MD5; this is not used for passwords or tokens.
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

const GenericTitle = "Bridge"
const GenericBody = "A task update is available. Open Bridge to view it."
const APNSEnvelopeKey = "bridge"
const UmengEnvelopeKey = "bridge_preview"

type Delivery struct {
	Platform, Token, AppID string
	Envelope               store.Envelope
}
type Result struct {
	Code                              string
	Accepted, InvalidToken, Permanent bool
}
type Sender interface {
	Send(context.Context, Delivery) Result
}

type Providers struct {
	config  config.Config
	client  *http.Client
	key     *ecdsa.PrivateKey
	mu      sync.Mutex
	jwt     string
	jwtTime time.Time
}

func NewProviders(c config.Config) (*Providers, error) {
	p := &Providers{config: c, client: &http.Client{Timeout: 15 * time.Second,
		Transport:     &http.Transport{ForceAttemptHTTP2: true, MaxIdleConns: 32, IdleConnTimeout: time.Minute},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if c.APNSEnabled() {
		block, _ := pem.Decode([]byte(c.APNS.PrivateKey))
		if block == nil {
			return nil, errors.New("invalid APNs private key")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid APNs private key")
		}
		var ok bool
		p.key, ok = key.(*ecdsa.PrivateKey)
		if !ok || p.key.Curve.Params().BitSize != 256 {
			return nil, errors.New("APNs requires a P-256 signing key")
		}
	}
	return p, nil
}

func APNSPayload(e store.Envelope) ([]byte, error) {
	data, err := json.Marshal(map[string]any{"aps": map[string]any{"alert": map[string]string{"title": GenericTitle, "body": GenericBody},
		"mutable-content": 1, "sound": "default"}, APNSEnvelopeKey: e})
	if err != nil {
		return nil, err
	}
	if len(data) > 4096 {
		return nil, errors.New("APNs payload exceeds 4096 bytes")
	}
	return data, nil
}

func (p *Providers) Send(ctx context.Context, d Delivery) Result {
	if _, err := APNSPayload(d.Envelope); err != nil {
		return Result{Code: "payload_limit", Permanent: true}
	}
	switch d.Platform {
	case "apns":
		if !p.config.APNSEnabled() || d.AppID != p.config.APNS.Topic {
			return Result{Code: "platform_disabled", Permanent: true}
		}
		return p.apns(ctx, d)
	case "umeng":
		if !p.config.UmengEnabled() || d.AppID != p.config.Umeng.AppKey {
			return Result{Code: "platform_disabled", Permanent: true}
		}
		return p.umeng(ctx, d)
	default:
		return Result{Code: "platform_disabled", Permanent: true}
	}
}

func (p *Providers) authToken() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jwt != "" && time.Since(p.jwtTime) < 45*time.Minute {
		return p.jwt, nil
	}
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": p.config.APNS.KeyID})
	p.jwtTime = time.Now()
	body, _ := json.Marshal(map[string]any{"iss": p.config.APNS.TeamID, "iat": p.jwtTime.Unix()})
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	hash := sha256.Sum256([]byte(encoded))
	r, s, err := ecdsa.Sign(rand.Reader, p.key, hash[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	p.jwt = encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
	return p.jwt, nil
}

func (p *Providers) apns(ctx context.Context, d Delivery) Result {
	data, err := APNSPayload(d.Envelope)
	if err != nil {
		return Result{Code: "payload_limit", Permanent: true}
	}
	auth, err := p.authToken()
	if err != nil {
		return Result{Code: "credentials", Permanent: true}
	}
	host := "https://api.push.apple.com"
	if p.config.APNS.Sandbox {
		host = "https://api.sandbox.push.apple.com"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/3/device/"+d.Token, bytes.NewReader(data))
	if err != nil {
		return Result{Code: "request", Permanent: true}
	}
	request.Header.Set("authorization", "bearer "+auth)
	request.Header.Set("apns-topic", d.AppID)
	request.Header.Set("apns-push-type", "alert")
	request.Header.Set("apns-priority", "10")
	request.Header.Set("apns-expiration", strconv.FormatInt(d.Envelope.ExpiresAt, 10))
	request.Header.Set("apns-collapse-id", d.Envelope.NotificationID)
	request.Header.Set("apns-id", d.Envelope.NotificationID)
	response, err := p.client.Do(request)
	if err != nil {
		return Result{Code: "result_unknown"}
	}
	defer response.Body.Close()
	if response.StatusCode == 200 {
		return Result{Code: "accepted", Accepted: true}
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body)
	if response.StatusCode == 410 || body.Reason == "BadDeviceToken" || body.Reason == "DeviceTokenNotForTopic" {
		return Result{Code: "invalid_token", InvalidToken: true, Permanent: true}
	}
	if response.StatusCode == 429 || response.StatusCode >= 500 {
		return Result{Code: "temporary"}
	}
	return Result{Code: "provider_rejected", Permanent: true}
}

func (p *Providers) umeng(ctx context.Context, d Delivery) Result {
	const endpoint = "https://msgapi.umeng.com/api/send"
	envelope, err := json.Marshal(d.Envelope)
	if err != nil {
		return Result{Code: "payload", Permanent: true}
	}
	// UMessage.extra is a string map, unlike APNs' dictionary-valued userInfo.
	data, err := json.Marshal(map[string]any{
		"appkey": d.AppID, "timestamp": strconv.FormatInt(time.Now().Unix(), 10), "type": "unicast", "device_tokens": d.Token,
		"production_mode": strconv.FormatBool(p.config.Umeng.Production),
		"payload": map[string]any{"display_type": "notification",
			"body":  map[string]any{"ticker": GenericTitle, "title": GenericTitle, "text": GenericBody, "after_open": "go_app"},
			"extra": map[string]string{UmengEnvelopeKey: string(envelope)}},
		"policy": map[string]string{"expire_time": time.Unix(d.Envelope.ExpiresAt, 0).In(time.FixedZone("Umeng", 8*3600)).Format("2006-01-02 15:04:05")},
	})
	if err != nil {
		return Result{Code: "payload", Permanent: true}
	}
	signing := make([]byte, 0, len("POST"+endpoint)+len(data)+len(p.config.Umeng.MasterSecret))
	signing = append(signing, []byte("POST"+endpoint)...)
	signing = append(signing, data...)
	signing = append(signing, []byte(p.config.Umeng.MasterSecret)...)
	signature := md5.Sum(signing)
	// Signing uses a separate slice so the secret is never appended to the HTTP body.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?sign="+hex.EncodeToString(signature[:]), bytes.NewReader(data))
	if err != nil {
		return Result{Code: "request", Permanent: true}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return Result{Code: "result_unknown"}
	}
	defer response.Body.Close()
	if response.StatusCode == 429 || response.StatusCode >= 500 {
		return Result{Code: "temporary"}
	}
	var body struct {
		Ret  string `json:"ret"`
		Data struct {
			ID    string          `json:"msg_id"`
			Error json.RawMessage `json:"error_code"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body); err != nil {
		return Result{Code: "result_unknown"}
	}
	if response.StatusCode == 200 && body.Ret == "SUCCESS" && body.Data.ID != "" {
		return Result{Code: "accepted", Accepted: true}
	}
	code := string(body.Data.Error)
	if code == `"2010"` || code == "2010" {
		return Result{Code: "invalid_token", InvalidToken: true, Permanent: true}
	}
	return Result{Code: "provider_rejected", Permanent: true}
}
