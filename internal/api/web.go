package api

import (
	"embed"
	"html/template"
	"net/http"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

//go:embed web/*
var webFiles embed.FS
var pageTemplate = template.Must(template.ParseFS(webFiles, "web/index.html"))
var adminTemplate = template.Must(template.ParseFS(webFiles, "web/admin.html"))

func (a *Server) webRoutes() {
	a.handle("GET /{$}", a.page)
	a.handle("GET /reset", a.page)
	a.handle("GET /admin", a.page)
	a.handle("GET /admin/setup", a.page)
	a.handle("POST /web/login", a.webLogin)
	a.mux.HandleFunc("GET /assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name != "app.js" && name != "style.css" && name != "locale_zh-CN.json" && name != "locale_en.json" {
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
		} else if name == "style.css" {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		_, _ = w.Write(data)
	})
}

func (a *Server) page(w http.ResponseWriter, r *http.Request) error {
	language, text := pageLanguage(w, r, a.Config.Development)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Path == "/" || r.URL.Path == "/reset" {
		return pageTemplate.Execute(w, map[string]any{"Mode": a.Config.Mode, "Hosted": a.Config.Paid(),
			"Home": r.URL.Path == "/", "Reset": r.URL.Path == "/reset",
			"Language": language, "Text": text})
	}
	if r.URL.Path == "/admin/setup" {
		setup, _, err := a.setupPrincipal(r)
		if err != nil {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return nil
		}
		return pageTemplate.Execute(w, map[string]any{"Mode": a.Config.Mode, "Hosted": a.Config.Paid(), "Setup": true,
			"SetupLogin": setup.Login, "CSRF": setup.CSRF, "Language": language, "Text": text})
	}
	p, err := a.principal(r)
	authenticated := err == nil
	if authenticated && !p.Administrator {
		return failure{403, "administrator_web_only"}
	}
	if authenticated {
		return adminTemplate.Execute(w, map[string]any{"Mode": a.Config.Mode, "Hosted": a.Config.Paid(),
			"Account": p.Account, "CSRF": p.CSRF, "Language": language, "Text": text})
	}
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
	return pageTemplate.Execute(w, map[string]any{"Mode": a.Config.Mode, "Hosted": a.Config.Paid(), "Login": true, "CSRF": csrf,
		"Changed": r.URL.Query().Get("changed") == "1", "Language": language, "Text": text})
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
	var in loginInput
	if err = body(w, r, &in); err != nil {
		return err
	}
	account, err := a.passwordAccount(r, in)
	if err != nil {
		return err
	}
	if !account.Administrator {
		return failure{403, "administrator_web_only"}
	}
	if account.CredentialsChangeRequired {
		setup, err := a.Store.NewAdministratorSetup(r.Context(), account)
		if err != nil {
			return err
		}
		http.SetCookie(w, &http.Cookie{Name: a.cookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
			Secure: !a.Config.Development, SameSite: http.SameSiteStrictMode})
		http.SetCookie(w, &http.Cookie{Name: a.setupCookieName(), Value: setup.Token, Path: "/", MaxAge: 600, HttpOnly: true,
			Secure: !a.Config.Development, SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]bool{"authenticated": false, "credentials_change_required": true})
		return nil
	}
	tokens, err := a.Store.NewSession(r.Context(), account, a.Config, true)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(), Value: tokens.Web, Path: "/", MaxAge: 12 * 3600, HttpOnly: true, Secure: !a.Config.Development, SameSite: http.SameSiteStrictMode})
	a.clearSetupCookie(w)
	writeJSON(w, 200, map[string]bool{"authenticated": true})
	return nil
}
