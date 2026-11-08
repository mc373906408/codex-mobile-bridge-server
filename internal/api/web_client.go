package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/webproxy"
)

func (a *Server) clientCookieName(guest bool) string {
	name := "__Host-bridge_web_client"
	if a.Config.Development {
		name = "bridge_dev_web_client"
	}
	if guest {
		name += "_guest"
	}
	return name
}
func (a *Server) setClientCookie(w http.ResponseWriter, guest bool, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: a.clientCookieName(guest), Value: token, Path: "/", MaxAge: age, Secure: !a.Config.Development, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}
func (a *Server) clientPrincipal(r *http.Request) (store.Principal, error) {
	cookie, err := r.Cookie(a.clientCookieName(false))
	if err != nil {
		return store.Principal{}, failure{401, "authentication_required"}
	}
	p, err := a.Store.AuthenticateWebClient(r.Context(), cookie.Value)
	if errors.Is(err, store.ErrDenied) {
		return p, failure{401, "authentication_required"}
	}
	if err == nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		err = a.clientCSRF(r, p.CSRF)
	}
	return p, err
}
func (a *Server) clientCSRF(r *http.Request, token string) error {
	if r.Header.Get("Origin") != a.Config.WebOrigin || len(token) != 43 || subtle.ConstantTimeCompare([]byte(token), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		return failure{403, "csrf_required"}
	}
	return nil
}
func (a *Server) serveWebClient(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return failure{405, "method_not_allowed"}
	}
	if r.Method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		return bad("get_body_not_allowed")
	}
	if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != a.Config.WebOrigin {
		return failure{403, "origin_required"}
	}
	if r.URL.RawQuery != "" && !strings.HasPrefix(path, "/d/") {
		return bad("query_not_allowed")
	}
	if r.Method == http.MethodGet && (path == "/" || path == "/_bridge/app.js" || path == "/_bridge/style.css" || path == "/_bridge/icon.png") {
		file, kind := "web_client.html", "text/html; charset=utf-8"
		if path == "/_bridge/app.js" {
			file, kind = "web_client.js", "text/javascript; charset=utf-8"
		}
		if path == "/_bridge/style.css" {
			file, kind = "web_client.css", "text/css; charset=utf-8"
		}
		if path == "/_bridge/icon.png" {
			file, kind = "web_client_icon.png", "image/png"
		}
		data, err := webFiles.ReadFile("web/" + file)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", kind)
		_, err = w.Write(data)
		return err
	}
	if path == "/_bridge/session" && r.Method == http.MethodGet {
		p, err := a.clientPrincipal(r)
		if err == nil {
			writeJSON(w, 200, map[string]any{"authenticated": true, "login": p.Login, "csrf": p.CSRF})
			return nil
		}
		var f failure
		if !errors.As(err, &f) || f.Status != 401 {
			return err
		}
		token, err := security.Token()
		if err != nil {
			return err
		}
		a.setClientCookie(w, true, token, 1800)
		writeJSON(w, 200, map[string]any{"authenticated": false, "csrf": token})
		return nil
	}
	if path == "/_bridge/login" && r.Method == http.MethodPost {
		guest, err := r.Cookie(a.clientCookieName(true))
		if err != nil {
			return failure{403, "csrf_required"}
		}
		if err = a.clientCSRF(r, guest.Value); err != nil {
			return err
		}
		var in loginInput
		if err = body(w, r, &in); err != nil {
			return err
		}
		account, err := a.passwordAccount(r, in)
		if err != nil {
			return err
		}
		if account.CredentialsChangeRequired {
			return failure{403, "credentials_change_required"}
		}
		tokens, err := a.Store.NewWebClientSession(r.Context(), account, a.Config)
		if err != nil {
			return err
		}
		a.setClientCookie(w, false, tokens.Web, 12*3600)
		a.setClientCookie(w, true, "", -1)
		writeJSON(w, 200, map[string]any{"authenticated": true, "csrf": tokens.CSRF})
		return nil
	}
	p, err := a.clientPrincipal(r)
	if err != nil {
		if r.Method == http.MethodGet && strings.HasPrefix(path, "/d/") && strings.HasSuffix(path, "/") {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return nil
		}
		return err
	}
	if (path == "/_bridge/logout" || strings.HasPrefix(path, "/d/") && strings.HasSuffix(path, "/api/logout")) && r.Method == http.MethodPost {
		if err = a.Store.RevokeResource(r.Context(), p, p.ID, "session", p.SessionID); err != nil {
			return err
		}
		a.Hub.CloseSession(p.SessionID)
		a.setClientCookie(w, false, "", -1)
		writeJSON(w, 200, map[string]bool{"logged_out": true})
		return nil
	}
	if path == "/_bridge/gateways" && r.Method == http.MethodGet {
		e, err := a.Store.Entitlement(r.Context(), p.ID, a.Config.Paid())
		if err != nil {
			return err
		}
		rows, err := a.Store.Pool.Query(r.Context(), "SELECT id,name FROM gateways WHERE account_id=$1 AND revoked_at IS NULL ORDER BY created_at LIMIT 100", p.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, name string
			if err = rows.Scan(&id, &name); err != nil {
				return err
			}
			items = append(items, map[string]any{"id": id, "name": name, "online": a.Hub.Web.Online(id), "desktop_online": a.Hub.GatewayOnline(id)})
		}
		if err = rows.Err(); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"gateways": items, "entitlement": e})
		return nil
	}
	if !strings.HasPrefix(path, "/d/") {
		return failure{404, "not_found"}
	}
	parts := strings.SplitN(strings.TrimPrefix(path, "/d/"), "/", 2)
	if len(parts) != 2 || validID(parts[0]) != nil || r.URL.EscapedPath() != path || strings.Contains(path, "\\") || strings.Contains(path, "//") {
		return bad("invalid_path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return bad("invalid_path")
		}
	}
	if len(r.URL.RequestURI()) > 8192 {
		return bad("invalid_path")
	}
	if r.Header.Get("Upgrade") != "" {
		return failure{400, "http_proxy_only"}
	}
	deadline, err := a.Store.WebClientDeadline(r.Context(), p.SessionID, p.ID)
	if err != nil {
		return err
	}
	e, err := a.Store.Entitlement(r.Context(), p.ID, a.Config.Paid())
	if err != nil {
		return err
	}
	if !e.Active {
		return failure{402, "entitlement_required"}
	}
	if e.Required && e.ExpiresAt != nil {
		entitlementDeadline := time.Now().Add(e.ExpiresAt.Sub(e.ServerTime))
		if entitlementDeadline.Before(deadline) {
			deadline = entitlementDeadline
		}
	}
	var own bool
	if err = a.Store.Pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM gateways WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL)", parts[0], p.ID).Scan(&own); err != nil {
		return err
	}
	if !own {
		return failure{404, "not_found"}
	}
	upstream := "/" + parts[1]
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	err = a.Hub.Web.Proxy(w, r, p, parts[0], upstream, a.Config.WebOrigin, deadline)
	if errors.Is(err, webproxy.ErrOffline) {
		return failure{503, "gateway_web_offline"}
	}
	if errors.Is(err, webproxy.ErrCapacity) {
		return failure{429, "web_capacity"}
	}
	if errors.Is(err, webproxy.ErrBodyTooLarge) {
		return failure{413, "body_too_large"}
	}
	if err != nil {
		return failure{502, "gateway_request_failed"}
	}
	return err
}

func (a *Server) webGatewaySocket(w http.ResponseWriter, r *http.Request) error {
	if a.Config.WebOrigin == "" {
		return failure{404, "web_disabled"}
	}
	d, token, err := a.device(r, "gateway", true)
	if err != nil {
		return err
	}
	a.Hub.Web.Serve(w, r, d, token)
	return nil
}
