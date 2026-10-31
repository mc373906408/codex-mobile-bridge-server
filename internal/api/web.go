package api

import (
	"embed"
	"errors"
	"html/template"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

//go:embed web/*
var webFiles embed.FS
var pageTemplate = template.Must(template.ParseFS(webFiles, "web/index.html"))

func (a *Server) webRoutes() {
	a.handle("GET /{$}", a.page)
	a.handle("GET /verify", a.page)
	a.handle("GET /reset", a.page)
	a.handle("POST /web/login", a.webLogin)
	a.mux.HandleFunc("GET /assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name != "app.js" && name != "style.css" {
			http.NotFound(w, r)
			return
		}
		data, err := webFiles.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if name == "app.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = w.Write(data)
	})
}

func (a *Server) page(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	authenticated := err == nil
	csrf := p.CSRF
	if !authenticated {
		csrf, err = security.Token()
		if err != nil {
			return err
		}
		name := "__Host-bridge_guest"
		if a.Config.Development {
			name = "bridge_dev_guest"
		}
		http.SetCookie(w, &http.Cookie{Name: name, Value: csrf, Path: "/", MaxAge: 1800, HttpOnly: true, Secure: !a.Config.Development, SameSite: http.SameSiteStrictMode})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return pageTemplate.Execute(w, map[string]any{"Mode": a.Config.Mode, "Hosted": a.Config.Paid(), "Authenticated": authenticated,
		"Account": p.Account, "CSRF": csrf, "Verify": r.URL.Path == "/verify", "Reset": r.URL.Path == "/reset"})
}
func (a *Server) webLogin(w http.ResponseWriter, r *http.Request) error {
	name := "__Host-bridge_guest"
	if a.Config.Development {
		name = "bridge_dev_guest"
	}
	guest, err := r.Cookie(name)
	if err != nil || r.Header.Get("Origin") != a.Config.Origin || len(guest.Value) != 43 || guest.Value != r.Header.Get("X-CSRF-Token") {
		return failure{403, "csrf_required"}
	}
	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	login, err := NormalizeLogin(in.Login, a.Config.Paid())
	if err != nil {
		return failure{401, "invalid_credentials"}
	}
	if err = a.limit(r, "login", login, 8, 15*time.Minute); err != nil {
		return err
	}
	account, lookup := a.Store.AccountByLogin(r.Context(), login)
	hash := account.PasswordHash
	if lookup != nil {
		hash = a.dummyHash
	}
	valid := security.VerifyPassword(in.Password, hash)
	if lookup != nil && !errors.Is(lookup, pgx.ErrNoRows) {
		return lookup
	}
	if lookup != nil || !valid || account.Disabled || !account.Verified {
		return failure{401, "invalid_credentials"}
	}
	tokens, err := a.Store.NewSession(r.Context(), account, a.Config, true)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(), Value: tokens.Web, Path: "/", MaxAge: 12 * 3600, HttpOnly: true, Secure: !a.Config.Development, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"authenticated": true})
	return nil
}
