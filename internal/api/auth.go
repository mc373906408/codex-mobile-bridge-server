package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func (a *Server) register(w http.ResponseWriter, r *http.Request) error {
	if !a.Config.Paid() {
		return failure{404, "registration_disabled"}
	}
	var in struct {
		Login             string `json:"login"`
		Password          string `json:"password"`
		RegistrationToken string `json:"registration_token"`
	}
	if err := body(w, r, &in); err != nil {
		return err
	}
	login, err := NormalizeLogin(in.Login, true)
	if err != nil {
		return err
	}
	if len(in.RegistrationToken) != 43 {
		return bad("invalid_registration_token")
	}
	if err = a.limit(r, "register", login, 3, time.Hour); err != nil {
		return err
	}
	hash, err := security.Password(in.Password)
	if err != nil {
		return bad("invalid_password")
	}
	account, err := a.Store.CompleteRegistration(r.Context(), login, in.RegistrationToken, hash, a.Config)
	if errors.Is(err, store.ErrDenied) {
		return bad("invalid_registration_token")
	}
	if err != nil {
		return err
	}
	a.Hub.CloseAccount(account.ID)
	writeJSON(w, 201, map[string]bool{"registered": true})
	return nil
}

func (a *Server) login(w http.ResponseWriter, r *http.Request) error {
	var in loginInput
	if err := body(w, r, &in); err != nil {
		return err
	}
	account, err := a.passwordAccount(r, in)
	if err != nil {
		return err
	}
	if account.CredentialsChangeRequired {
		setup, err := a.Store.NewAdministratorSetup(r.Context(), account)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"credentials_change_required": true, "setup_token": setup.Token, "expires_in": setup.ExpiresIn})
		return nil
	}
	tokens, err := a.Store.NewSession(r.Context(), account, a.Config, false)
	if err != nil {
		return err
	}
	writeJSON(w, 200, tokens)
	return nil
}

func (a *Server) refresh(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"refresh_token"`
	}
	if err := body(w, r, &in); err != nil {
		return err
	}
	if err := a.limit(r, "refresh", "", 60, time.Minute); err != nil {
		return err
	}
	if len(in.Token) != 43 {
		return failure{401, "invalid_refresh_token"}
	}
	tokens, err := a.Store.Refresh(r.Context(), in.Token, a.Config)
	if errors.Is(err, store.ErrDenied) {
		return failure{401, "invalid_refresh_token"}
	}
	if err != nil {
		return err
	}
	writeJSON(w, 200, tokens)
	return nil
}

func (a *Server) logout(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	err = a.Store.RevokeResource(r.Context(), p, p.ID, "session", p.SessionID)
	if err != nil {
		return err
	}
	a.Hub.CloseSession(p.SessionID)
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: !a.Config.Development, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"logged_out": true})
	return nil
}

func (a *Server) forgot(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Login string `json:"login"`
	}
	if err := body(w, r, &in); err != nil {
		return err
	}
	login, normalizeErr := NormalizeLogin(in.Login, a.Config.Paid())
	if err := a.limit(r, "mail", login, 3, time.Hour); err != nil {
		return err
	}
	if a.Config.Paid() && normalizeErr == nil {
		if err := a.Store.RequestPasswordReset(r.Context(), login, a.Config); err != nil {
			return err
		}
	}
	writeJSON(w, 202, map[string]bool{"accepted": true})
	return nil
}
func (a *Server) reset(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := body(w, r, &in); err != nil {
		return err
	}
	if err := a.limit(r, "reset", "", 10, time.Hour); err != nil {
		return err
	}
	if len(in.Token) != 43 {
		return bad("invalid_email_token")
	}
	hash, err := security.Password(in.Password)
	if err != nil {
		return bad("invalid_password")
	}
	id, err := a.Store.ConsumePasswordResetToken(r.Context(), in.Token, hash)
	if errors.Is(err, store.ErrDenied) {
		return bad("invalid_email_token")
	}
	if err != nil {
		return err
	}
	a.Hub.CloseAccount(id)
	writeJSON(w, 200, map[string]bool{"reset": true, "re_pairing_keys_not_recovered": true})
	return nil
}

func (a *Server) account(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	writeJSON(w, 200, p.Account)
	return nil
}
func (a *Server) deleteAccount(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	var in struct {
		Password string `json:"password"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if err = a.limit(r, "delete", p.ID, 3, time.Hour); err != nil {
		return err
	}
	if !security.VerifyPassword(in.Password, p.PasswordHash) {
		return store.ErrDenied
	}
	err = pgx.BeginFunc(r.Context(), a.Store.Pool, func(tx pgx.Tx) error {
		if err := store.RecordAudit(r.Context(), tx, store.AuditEvent{ActorID: p.ID, AccountID: p.ID, SessionID: p.SessionID,
			Action: "account.delete", TargetID: p.ID, After: map[string]any{"deleted": true}}); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), "DELETE FROM accounts WHERE id=$1 AND password_hash=$2", p.ID, p.PasswordHash)
		if err == nil && tag.RowsAffected() != 1 {
			return store.ErrDenied
		}
		return err
	})
	if err != nil {
		return err
	}
	a.Hub.CloseAccount(p.ID)
	writeJSON(w, 200, map[string]bool{"deleted": true})
	return nil
}
func (a *Server) entitlement(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	e, err := a.Store.Entitlement(r.Context(), p.ID, a.Config.Paid())
	if err != nil {
		return err
	}
	writeJSON(w, 200, e)
	return nil
}
func (a *Server) redeem(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	if r.Header.Get("Authorization") == "" {
		return failure{403, "redemption_client_only"}
	}
	if !a.Config.Paid() {
		return failure{404, "redemption_disabled"}
	}
	var in struct {
		Code      string `json:"code"`
		RequestID string `json:"request_id"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if len(in.Code) > 128 || validID(in.RequestID) != nil {
		return bad("invalid_redemption")
	}
	if err = a.limit(r, "redeem", p.ID, 10, time.Hour); err != nil {
		return err
	}
	result, err := a.Store.Redeem(r.Context(), p.ID, in.Code, in.RequestID)
	if errors.Is(err, store.ErrBatchRedeemed) {
		return failure{409, "redemption_batch_already_used"}
	}
	if errors.Is(err, store.ErrDenied) {
		return bad("invalid_redemption")
	}
	if err != nil {
		return err
	}
	writeJSON(w, 200, result)
	return nil
}
func (a *Server) sessions(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	rows, err := a.Store.Pool.Query(r.Context(), "SELECT id,created_at,refresh_expires_at,revoked_at FROM sessions WHERE account_id=$1 ORDER BY created_at DESC LIMIT 100", p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id string
		var created, expires time.Time
		var revoked *time.Time
		if err = rows.Scan(&id, &created, &expires, &revoked); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "created_at": created, "expires_at": expires, "revoked_at": revoked, "current": id == p.SessionID})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"sessions": items})
	return nil
}
func (a *Server) revokeSession(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	if err = a.Store.RevokeResource(r.Context(), p, p.ID, "session", id); err != nil {
		return err
	}
	a.Hub.CloseSession(id)
	writeJSON(w, 200, map[string]bool{"revoked": true})
	return nil
}
