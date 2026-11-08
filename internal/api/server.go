package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/relay"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

type Server struct {
	Config         config.Config
	Store          *store.Store
	Hub            *relay.Hub
	mux            *http.ServeMux
	dummyHash      string
	bootstrapSlots chan struct{}
}

type failure struct {
	Status int
	Code   string
}

func (e failure) Error() string { return e.Code }
func bad(code string) error     { return failure{400, code} }

func New(c config.Config, s *store.Store) (*Server, error) {
	dummy, err := security.Password("unused-password-for-constant-work")
	if err != nil {
		return nil, err
	}
	a := &Server{Config: c, Store: s, Hub: relay.New(s, c), mux: http.NewServeMux(), dummyHash: dummy, bootstrapSlots: make(chan struct{}, max(1, c.MaxConnections))}
	a.routes()
	return a, nil
}

func (a *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u, _ := url.Parse(a.Config.Origin)
	webHost := ""
	if a.Config.WebOrigin != "" {
		v, _ := url.Parse(a.Config.WebOrigin)
		webHost = v.Host
	}
	isWeb := webHost != "" && r.Host == webHost
	if r.Host != u.Host && !isWeb {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if !a.Config.Development {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		if !a.secureRequest(r) {
			a.respondError(w, failure{400, "https_required"})
			return
		}
	}
	if isWeb {
		if err := a.serveWebClient(w, r); err != nil {
			a.respondError(w, err)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.RawQuery != "" && !adminListQuery(r) {
		a.respondError(w, bad("query_not_allowed"))
		return
	}
	a.mux.ServeHTTP(w, r)
}

func (a *Server) handle(pattern string, handler func(http.ResponseWriter, *http.Request) error) {
	a.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if err := handler(w, r); err != nil {
			a.respondError(w, err)
		}
	})
}
func (a *Server) respondError(w http.ResponseWriter, err error) {
	status, code := 500, "unavailable"
	var f failure
	switch {
	case errors.As(err, &f):
		status, code = f.Status, f.Code
	case errors.Is(err, store.ErrDenied):
		status, code = 403, "access_denied"
	case errors.Is(err, store.ErrConflict):
		status, code = 409, "state_conflict"
	case errors.Is(err, store.ErrNotFound) || errors.Is(err, pgx.ErrNoRows):
		status, code = 404, "not_found"
	}
	writeJSON(w, status, map[string]any{"code": code, "error": code})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func body(w http.ResponseWriter, r *http.Request, out any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return bad("json_required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return bad("invalid_body")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return bad("invalid_body")
	}
	return nil
}

func bearer(r *http.Request) (string, error) {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") || len(value) != 50 {
		return "", failure{401, "authentication_required"}
	}
	return value[7:], nil
}
func (a *Server) cookieName() string {
	if a.Config.Development {
		return "bridge_dev_session"
	}
	return "__Host-bridge_session"
}

func (a *Server) principal(r *http.Request) (store.Principal, error) {
	token, web := "", false
	if r.Header.Get("Authorization") != "" {
		var err error
		token, err = bearer(r)
		if err != nil {
			return store.Principal{}, err
		}
	} else {
		cookie, err := r.Cookie(a.cookieName())
		if err != nil {
			return store.Principal{}, failure{401, "authentication_required"}
		}
		token, web = cookie.Value, true
	}
	p, err := a.Store.Authenticate(r.Context(), token, web)
	if errors.Is(err, store.ErrDenied) {
		return p, failure{401, "authentication_required"}
	}
	if err != nil {
		return p, err
	}
	if web && r.Method != http.MethodGet && r.Method != http.MethodHead {
		if r.Header.Get("Origin") != a.Config.Origin || r.Header.Get("X-CSRF-Token") != p.CSRF {
			return p, failure{403, "csrf_required"}
		}
	}
	return p, nil
}
func (a *Server) administrator(r *http.Request) (store.Principal, error) {
	p, err := a.principal(r)
	if err == nil && !p.Administrator {
		err = store.ErrDenied
	}
	return p, err
}

func (a *Server) device(r *http.Request, role string, cloud bool) (store.Device, string, error) {
	token, err := bearer(r)
	if err != nil {
		return store.Device{}, "", err
	}
	d, err := a.Store.Device(r.Context(), token, role)
	if err != nil {
		return d, token, err
	}
	if cloud {
		e, err := a.Store.Entitlement(r.Context(), d.AccountID, a.Config.Paid())
		if err != nil {
			return d, token, err
		}
		if !e.Active {
			return d, token, failure{402, "entitlement_required"}
		}
	}
	return d, token, nil
}

func (a *Server) limit(r *http.Request, action, identity string, count int, window time.Duration) error {
	ip := a.clientIP(r)
	allowed, err := a.Store.RateLimit(r.Context(), action+":ip:"+ip, count*4, window)
	if err != nil {
		return err
	}
	if !allowed {
		return failure{429, "rate_limited"}
	}
	if identity != "" {
		allowed, err = a.Store.RateLimit(r.Context(), action+":identity:"+identity, count, window)
		if err != nil {
			return err
		}
		if !allowed {
			return failure{429, "rate_limited"}
		}
	}
	return nil
}

func (a *Server) trustedProxy(ip netip.Addr) bool {
	for _, prefix := range a.Config.TrustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *Server) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	peer = peer.Unmap()
	// Container readiness probes have no credentials and never expose account APIs.
	if peer.IsLoopback() && r.Method == http.MethodGet && r.URL.RawQuery == "" && r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "" &&
		(r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
		return true
	}
	proto := r.Header.Values("X-Forwarded-Proto")
	return a.trustedProxy(peer) && len(proto) == 1 && proto[0] == "https"
}

func (a *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	peer = peer.Unmap()
	if !a.trustedProxy(peer) {
		return peer.String()
	}
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(chain) > 16 {
		return peer.String()
	}
	for i := len(chain) - 1; i >= 0; i-- {
		next, e := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if e != nil {
			return peer.String()
		}
		peer = next.Unmap()
		if !a.trustedProxy(peer) {
			break
		}
	}
	return peer.String()
}

var username = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,63}$`)

func NormalizeLogin(value string, hosted bool) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if hosted {
		address, err := mail.ParseAddress(value)
		if err != nil || address.Address != value || len(value) > 254 || !strings.Contains(value, "@") {
			return "", bad("invalid_email")
		}
		for _, c := range value {
			if c > 127 {
				return "", bad("invalid_email")
			}
		}
	} else if !username.MatchString(value) {
		return "", bad("invalid_username")
	}
	return value, nil
}

func NormalizeAdministratorLogin(value string) (string, error) {
	login, err := NormalizeLogin(value, false)
	if err == nil {
		return login, nil
	}
	// Preserve existing email-named administrators during upgrades.
	return NormalizeLogin(value, true)
}
func validID(value string) error {
	_, err := security.IDBytes(value)
	if err != nil {
		return bad("invalid_identifier")
	}
	return nil
}

func (a *Server) routes() {
	a.handle("GET /healthz", func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return nil
	})
	a.handle("GET /readyz", func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.Store.Pool.Ping(ctx); err != nil {
			return failure{503, "not_ready"}
		}
		writeJSON(w, 200, map[string]bool{"ready": true})
		return nil
	})
	a.handle("GET /v1/capabilities", a.capabilities)
	a.handle("POST /v1/auth/registration-code", a.requestRegistrationCode)
	a.handle("POST /v1/auth/registration-verify", a.verifyRegistrationCode)
	a.handle("POST /v1/auth/register", a.register)
	a.handle("POST /v1/auth/login", a.login)
	a.handle("POST /v1/admin/auth/setup", a.finishAdministratorSetup)
	a.handle("POST /v1/auth/refresh", a.refresh)
	a.handle("POST /v1/auth/logout", a.logout)
	a.handle("POST /v1/auth/forgot", a.forgot)
	a.handle("POST /v1/auth/reset", a.reset)
	a.handle("GET /v1/auth/sessions", a.sessions)
	a.handle("DELETE /v1/auth/sessions/{id}", a.revokeSession)
	a.handle("GET /v1/account", a.account)
	a.handle("DELETE /v1/account", a.deleteAccount)
	a.handle("GET /v1/account/entitlement", a.entitlement)
	a.handle("POST /v1/account/redeem", a.redeem)
	a.deviceRoutes()
	a.handle("GET /v1/ws/web-gateway", a.webGatewaySocket)
	a.adminRoutes()
	a.webRoutes()
}

func (a *Server) capabilities(w http.ResponseWriter, r *http.Request) error {
	login := "username"
	verificationMethods := []string{}
	registrationFlow := ""
	redemptionCodeLength := 0
	redemptionBatchLimit := 0
	if a.Config.Paid() {
		login = "email"
		verificationMethods = []string{"code"}
		registrationFlow = "email_code_password"
		redemptionCodeLength = security.RedemptionCodeLength
		redemptionBatchLimit = 1
	}
	writeJSON(w, 200, map[string]any{"mode": a.Config.Mode, "login": login, "registration": a.Config.Paid(), "redemption": a.Config.Paid(),
		"email_verification_methods": verificationMethods, "email_verification_code_length": 6,
		"registration_flow":      registrationFlow,
		"redemption_code_length": redemptionCodeLength,
		"redemption_batch_limit": redemptionBatchLimit,
		"redemption_web":         false, "web_home": "/", "web_console": "/admin",
		"administrator_login": "username", "administrator_initial_setup": true, "web_languages": []string{"zh-CN", "en"},
		"web_access":     "administrator_only",
		"web_client":     map[string]any{"enabled": a.Config.WebOrigin != "", "origin": a.Config.WebOrigin, "protocol": "bridge-web-proxy-v1"},
		"password_reset": a.Config.MailEnabled(), "entitlement_required": a.Config.Paid(), "relay_protocols": []int{2},
		"control_noise": "Noise_KK_25519_ChaChaPoly_BLAKE2s", "preview_noise": "Noise_K_25519_ChaChaPoly_BLAKE2s",
		"pairing_method":   "desktop_approval",
		"device_discovery": "account_directory_v1", "binding_access": "desktop_switch_v1",
		"p2p":  map[string]any{"protocol": "easytier-v1", "enabled": len(a.Config.P2PBootstrap) > 0, "bootstrap": a.Config.P2PBootstrap},
		"push": map[string]any{"umeng": a.Config.UmengEnabled(), "apns": a.Config.APNSEnabled(), "umeng_app_id": a.Config.Umeng.AppKey, "apns_app_id": a.Config.APNS.Topic}})
	return nil
}
