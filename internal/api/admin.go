package api

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func (a *Server) adminRoutes() {
	a.handle("GET /v1/admin/registration-campaigns", a.adminCampaigns)
	a.handle("POST /v1/admin/registration-campaigns", a.adminCreateCampaign)
	a.handle("PATCH /v1/admin/registration-campaigns/{id}", func(w http.ResponseWriter, r *http.Request) error { return a.adminChangeCampaign(w, r, "edit") })
	a.handle("POST /v1/admin/registration-campaigns/{id}/activate", func(w http.ResponseWriter, r *http.Request) error { return a.adminChangeCampaign(w, r, "activate") })
	a.handle("POST /v1/admin/registration-campaigns/{id}/stop", func(w http.ResponseWriter, r *http.Request) error { return a.adminChangeCampaign(w, r, "stop") })
	a.handle("GET /v1/admin/registration-campaigns/{id}/grants", a.adminCampaignGrants)
	a.handle("GET /v1/admin/users", a.adminUsers)
	a.handle("POST /v1/admin/users", a.adminCreateUser)
	a.handle("POST /v1/admin/users/{id}/disable", a.adminDisable)
	a.handle("POST /v1/admin/users/{id}/reset", a.adminReset)
	a.handle("POST /v1/admin/users/{id}/revoke-entitlement", a.adminRevokeEntitlement)
	a.handle("POST /v1/admin/users/{id}/topups", a.adminTopup)
	a.handle("GET /v1/admin/users/{account}/devices", a.adminDevices)
	a.handle("GET /v1/admin/users/{account}/bindings", a.adminBindings)
	a.handle("GET /v1/admin/users/{account}/sessions", a.adminSessions)
	a.handle("DELETE /v1/admin/users/{account}/gateways/{id}", func(w http.ResponseWriter, r *http.Request) error { return a.adminRevokeResource(w, r, "gateway") })
	a.handle("DELETE /v1/admin/users/{account}/installations/{id}", func(w http.ResponseWriter, r *http.Request) error { return a.adminRevokeResource(w, r, "mobile") })
	a.handle("DELETE /v1/admin/users/{account}/bindings/{id}", func(w http.ResponseWriter, r *http.Request) error { return a.adminRevokeResource(w, r, "binding") })
	a.handle("DELETE /v1/admin/users/{account}/sessions/{id}", func(w http.ResponseWriter, r *http.Request) error { return a.adminRevokeResource(w, r, "session") })
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
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(search) > 254 || !utf8.ValidString(search) || strings.ContainsAny(search, "\x00\r\n") || len(r.URL.Query()["q"]) > 1 {
		return bad("invalid_search")
	}
	return a.listPage(w, r, "users", "users:"+search, `SELECT a.id::text,a.login,a.verified,a.administrator,a.disabled,e.expires_at,a.created_at
		FROM accounts a JOIN entitlements e ON e.account_id=a.id
		WHERE ($1='' OR strpos(lower(a.login),$1)>0 OR strpos(a.id::text,$1)>0)`, "a.created_at", "a.id", search)
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
	user, err := a.Store.CreateAccount(r.Context(), login, hash, p.ID)
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
		Disabled *bool `json:"disabled"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if in.Disabled == nil {
		return bad("invalid_body")
	}
	if id == p.ID && *in.Disabled {
		return bad("cannot_disable_current_administrator")
	}
	err = pgx.BeginFunc(r.Context(), a.Store.Pool, func(tx pgx.Tx) error {
		if err := store.AuthorizeAdministration(r.Context(), tx, p, id); err != nil {
			return err
		}
		var before bool
		if err := tx.QueryRow(r.Context(), "SELECT disabled FROM accounts WHERE id=$1", id).Scan(&before); err != nil {
			return err
		}
		if before == *in.Disabled {
			return nil
		}
		if _, err := tx.Exec(r.Context(), "UPDATE accounts SET disabled=$2 WHERE id=$1", id, *in.Disabled); err != nil {
			return err
		}
		if *in.Disabled {
			if _, err := tx.Exec(r.Context(), `UPDATE push_jobs SET status='cancelled' WHERE status IN ('queued','leased')
				AND binding_id IN (SELECT b.id FROM bindings b JOIN gateways g ON g.id=b.gateway_id WHERE g.account_id=$1)`, id); err != nil {
				return err
			}
		}
		return store.RecordAudit(r.Context(), tx, store.AuditEvent{ActorID: p.ID, AccountID: id, SessionID: p.SessionID,
			Action: "account.disable", TargetID: id, Before: map[string]any{"disabled": before}, After: map[string]any{"disabled": *in.Disabled}})
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
		if err := store.AuthorizeAdministration(r.Context(), tx, p, id); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), "UPDATE accounts SET password_hash=$2 WHERE id=$1", id, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return store.ErrNotFound
		}
		if err = store.RevokeAccountSessions(r.Context(), tx, p, id, "administrator_password_reset"); err != nil {
			return err
		}
		return store.RecordAudit(r.Context(), tx, store.AuditEvent{ActorID: p.ID, AccountID: id, SessionID: p.SessionID,
			Action: "account.reset", TargetID: id, After: map[string]any{"sessions_revoked": true}})
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
		if err := store.AuthorizeAdministration(r.Context(), tx, p, id); err != nil {
			return err
		}
		var before, after *time.Time
		if err := tx.QueryRow(r.Context(), "SELECT expires_at FROM entitlements WHERE account_id=$1 FOR UPDATE", id).Scan(&before); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), "UPDATE entitlements SET expires_at=now() WHERE account_id=$1", id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return store.ErrNotFound
		}
		if err = tx.QueryRow(r.Context(), "SELECT expires_at FROM entitlements WHERE account_id=$1", id).Scan(&after); err != nil {
			return err
		}
		return store.RecordAudit(r.Context(), tx, store.AuditEvent{ActorID: p.ID, AccountID: id, SessionID: p.SessionID,
			Action: "entitlement.revoke", TargetID: id, Before: map[string]any{"expires_at": before}, After: map[string]any{"expires_at": after}})
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
	return a.listPage(w, r, "codes", "codes", "SELECT c.id::text,c.batch_id::text,c.days,c.created_at,c.used_at,c.revoked_at FROM redemption_codes c WHERE true", "c.created_at", "c.id")
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
		return store.RecordAudit(r.Context(), tx, store.AuditEvent{ActorID: p.ID, AccountID: p.ID, SessionID: p.SessionID,
			Action: "code.revoke", TargetID: id, After: map[string]any{"revoked": true}})
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
	return a.listPage(w, r, "redemptions", "redemptions", "SELECT d.id::text,d.account_id::text,d.code_id::text,d.batch_id::text,d.days,d.expires_at,d.created_at FROM redemptions d WHERE true", "d.created_at", "d.id")
}
func (a *Server) adminAudit(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.administrator(r); err != nil {
		return err
	}
	account := r.URL.Query().Get("account_id")
	if account != "" && validID(account) != nil || len(r.URL.Query()["account_id"]) > 1 {
		return bad("invalid_identifier")
	}
	return a.listPage(w, r, "audit", "audit:"+account, `SELECT a.id::text,a.actor_id::text,a.account_id::text,a.session_id::text,
		a.device_id::text,a.action,a.target_id::text,a.details,a.created_at FROM audit_log a WHERE ($1='' OR a.account_id=NULLIF($1,'')::uuid)`, "", "a.id", account)
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
