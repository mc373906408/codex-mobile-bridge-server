package api

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func (a *Server) adminRoutes() {
	a.handle("GET /v1/admin/users", a.adminUsers)
	a.handle("POST /v1/admin/users", a.adminCreateUser)
	a.handle("POST /v1/admin/users/{id}/disable", a.adminDisable)
	a.handle("POST /v1/admin/users/{id}/reset", a.adminReset)
	a.handle("POST /v1/admin/users/{id}/revoke-entitlement", a.adminRevokeEntitlement)
	a.handle("POST /v1/admin/codes", a.adminCodes)
	a.handle("GET /v1/admin/codes", a.adminListCodes)
	a.handle("DELETE /v1/admin/codes/{id}", a.adminRevokeCode)
	a.handle("GET /v1/admin/redemptions", a.adminRedemptions)
	a.handle("GET /v1/admin/audit", a.adminAudit)
	a.handle("GET /v1/admin/status", a.adminStatus)
}
func (a *Server) adminUsers(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.administrator(r); err != nil {
		return err
	}
	rows, err := a.Store.Pool.Query(r.Context(), `SELECT a.id,a.login,a.verified,a.administrator,a.disabled,e.expires_at
		FROM accounts a JOIN entitlements e ON e.account_id=a.id ORDER BY a.created_at DESC LIMIT 200`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, login string
		var verified, admin, disabled bool
		var expiry *time.Time
		if err = rows.Scan(&id, &login, &verified, &admin, &disabled, &expiry); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "login": login, "verified": verified, "administrator": admin, "disabled": disabled, "expires_at": expiry})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"users": items})
	return nil
}
func (a *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) error {
	p, err := a.administrator(r)
	if err != nil {
		return err
	}
	if a.Config.Paid() {
		return bad("hosted_users_require_email_verification")
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
	hash, err := security.Password(in.Password)
	if err != nil {
		return bad("invalid_password")
	}
	user, err := a.Store.CreateAccount(r.Context(), login, hash, true, a.Config, p.ID)
	if err != nil {
		return err
	}
	writeJSON(w, 201, user)
	return nil
}
func (a *Server) adminDisable(w http.ResponseWriter, r *http.Request) error {
	p, err := a.administrator(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	var in struct {
		Disabled bool `json:"disabled"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if id == p.ID && in.Disabled {
		return bad("cannot_disable_current_administrator")
	}
	err = pgx.BeginFunc(r.Context(), a.Store.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), "UPDATE accounts SET disabled=$2 WHERE id=$1", id, in.Disabled)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return store.ErrNotFound
		}
		return store.Audit(r.Context(), tx, p.ID, "account.disable", id)
	})
	if err != nil {
		return err
	}
	a.Hub.CloseAccount(id)
	writeJSON(w, 200, map[string]bool{"updated": true})
	return nil
}
func (a *Server) adminReset(w http.ResponseWriter, r *http.Request) error {
	p, err := a.administrator(r)
	if err != nil {
		return err
	}
	if a.Config.Paid() {
		return bad("use_verified_email_reset")
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	var in struct {
		Password string `json:"password"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	hash, err := security.Password(in.Password)
	if err != nil {
		return bad("invalid_password")
	}
	err = pgx.BeginFunc(r.Context(), a.Store.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), "UPDATE accounts SET password_hash=$2 WHERE id=$1", id, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return store.ErrNotFound
		}
		if _, err = tx.Exec(r.Context(), "UPDATE sessions SET revoked_at=now() WHERE account_id=$1", id); err != nil {
			return err
		}
		return store.Audit(r.Context(), tx, p.ID, "account.reset", id)
	})
	if err != nil {
		return err
	}
	a.Hub.CloseAccount(id)
	writeJSON(w, 200, map[string]bool{"reset": true})
	return nil
}
func (a *Server) adminRevokeEntitlement(w http.ResponseWriter, r *http.Request) error {
	p, err := a.administrator(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	err = pgx.BeginFunc(r.Context(), a.Store.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), "UPDATE entitlements SET expires_at=now() WHERE account_id=$1", id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return store.ErrNotFound
		}
		return store.Audit(r.Context(), tx, p.ID, "entitlement.revoke", id)
	})
	if err != nil {
		return err
	}
	a.Hub.CloseAccount(id)
	writeJSON(w, 200, map[string]bool{"revoked": true})
	return nil
}
func (a *Server) adminCodes(w http.ResponseWriter, r *http.Request) error {
	p, err := a.administrator(r)
	if err != nil {
		return err
	}
	if !a.Config.Paid() {
		return failure{404, "redemption_disabled"}
	}
	var in struct {
		Days  int `json:"days"`
		Count int `json:"count"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	codes, err := a.Store.GenerateCodes(r.Context(), p.ID, in.Days, in.Count)
	if err != nil {
		return bad("invalid_code_batch")
	}
	writeJSON(w, 201, map[string]any{"codes": codes, "export_once": true})
	return nil
}
func (a *Server) adminListCodes(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.administrator(r); err != nil {
		return err
	}
	rows, err := a.Store.Pool.Query(r.Context(), "SELECT id,days,created_at,used_at,revoked_at FROM redemption_codes ORDER BY created_at DESC LIMIT 200")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id string
		var days int
		var created time.Time
		var used, revoked *time.Time
		if err = rows.Scan(&id, &days, &created, &used, &revoked); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "days": days, "created_at": created, "used_at": used, "revoked_at": revoked})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"codes": items})
	return nil
}
func (a *Server) adminRevokeCode(w http.ResponseWriter, r *http.Request) error {
	p, err := a.administrator(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	err = pgx.BeginFunc(r.Context(), a.Store.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), "UPDATE redemption_codes SET revoked_at=now() WHERE id=$1 AND used_at IS NULL", id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return store.ErrConflict
		}
		return store.Audit(r.Context(), tx, p.ID, "code.revoke", id)
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
	return nil
}
func (a *Server) adminRedemptions(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.administrator(r); err != nil {
		return err
	}
	rows, err := a.Store.Pool.Query(r.Context(), "SELECT id,account_id,code_id,days,expires_at,created_at FROM redemptions ORDER BY created_at DESC LIMIT 200")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, account, code string
		var days int
		var expiry, created time.Time
		if err = rows.Scan(&id, &account, &code, &days, &expiry, &created); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "account_id": account, "code_id": code, "days": days, "expires_at": expiry, "created_at": created})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"redemptions": items})
	return nil
}
func (a *Server) adminAudit(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.administrator(r); err != nil {
		return err
	}
	rows, err := a.Store.Pool.Query(r.Context(), "SELECT actor_id,action,target_id,created_at FROM audit_log ORDER BY id DESC LIMIT 200")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var actor, target *string
		var action string
		var created time.Time
		if err = rows.Scan(&actor, &action, &target, &created); err != nil {
			return err
		}
		items = append(items, map[string]any{"actor_id": actor, "action": action, "target_id": target, "created_at": created})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"audit": items})
	return nil
}
func (a *Server) adminStatus(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.administrator(r); err != nil {
		return err
	}
	g, m := a.Hub.Status()
	var queued, leased int
	if err := a.Store.Pool.QueryRow(r.Context(), "SELECT count(*) FILTER(WHERE status='queued'),count(*) FILTER(WHERE status='leased') FROM push_jobs").Scan(&queued, &leased); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"gateways_online": g, "mobiles_online": m, "push_queued": queued, "push_leased": leased, "capacity_verified": false})
	return nil
}
