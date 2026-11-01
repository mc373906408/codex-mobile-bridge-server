package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

type loginInput struct {
	Login     string `json:"login"`
	Password  string `json:"password"`
	SetupCode string `json:"setup_code,omitempty"` // Ignored for cached older login forms.
}

func (a *Server) passwordAccount(r *http.Request, in loginInput) (store.Account, error) {
	login, err := NormalizeAdministratorLogin(in.Login)
	if err != nil {
		if err := a.limit(r, "login", "", 8, 15*time.Minute); err != nil {
			return store.Account{}, err
		}
		return store.Account{}, failure{401, "invalid_credentials"}
	}
	if err = a.limit(r, "login", login, 8, 15*time.Minute); err != nil {
		return store.Account{}, err
	}
	account, lookup := a.Store.AccountByLogin(r.Context(), login)
	hash := account.PasswordHash
	if lookup != nil {
		hash = a.dummyHash
	}
	valid := security.VerifyPassword(in.Password, hash)
	if lookup != nil && !errors.Is(lookup, pgx.ErrNoRows) {
		return store.Account{}, lookup
	}
	roleAllowed := account.Administrator || (strings.Contains(login, "@") == a.Config.Paid())
	if lookup != nil || !valid || account.Disabled || !account.Verified || !roleAllowed {
		return store.Account{}, failure{401, "invalid_credentials"}
	}
	if account.CredentialsChangeRequired && !account.Administrator {
		return store.Account{}, store.ErrDenied
	}
	return account, nil
}

func (a *Server) setupCookieName() string {
	if a.Config.Development {
		return "bridge_dev_setup"
	}
	return "__Host-bridge_setup"
}

func (a *Server) clearSetupCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: a.setupCookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: !a.Config.Development, SameSite: http.SameSiteStrictMode})
}

func (a *Server) setupPrincipal(r *http.Request) (store.AdministratorSetup, string, error) {
	token, web := "", false
	if r.Header.Get("Authorization") != "" {
		var err error
		token, err = bearer(r)
		if err != nil {
			return store.AdministratorSetup{}, "", err
		}
	} else {
		cookie, err := r.Cookie(a.setupCookieName())
		if err != nil || len(cookie.Value) != 43 {
			return store.AdministratorSetup{}, "", failure{401, "administrator_setup_required"}
		}
		token, web = cookie.Value, true
	}
	setup, err := a.Store.AdministratorSetup(r.Context(), token)
	if errors.Is(err, store.ErrDenied) {
		return setup, token, failure{401, "administrator_setup_required"}
	}
	if err == nil && web && r.Method != http.MethodGet && r.Method != http.MethodHead {
		if r.Header.Get("Origin") != a.Config.Origin || r.Header.Get("X-CSRF-Token") != setup.CSRF {
			return setup, token, failure{403, "csrf_required"}
		}
	}
	return setup, token, err
}

func (a *Server) finishAdministratorSetup(w http.ResponseWriter, r *http.Request) error {
	setup, token, err := a.setupPrincipal(r)
	if err != nil {
		return err
	}
	if err = a.limit(r, "administrator-setup", setup.AccountID, 8, 15*time.Minute); err != nil {
		return err
	}
	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	login, err := NormalizeLogin(in.Login, false)
	if err != nil {
		return err
	}
	if security.VerifyPassword(in.Password, setup.PasswordHash) {
		return bad("password_change_required")
	}
	hash, err := security.Password(in.Password)
	if err != nil {
		return bad("invalid_password")
	}
	id, err := a.Store.FinishAdministratorSetup(r.Context(), token, login, hash)
	if err != nil {
		return err
	}
	a.Hub.CloseAccount(id)
	a.clearSetupCookie(w)
	writeJSON(w, 200, map[string]bool{"credentials_changed": true, "sign_in_required": true})
	return nil
}
