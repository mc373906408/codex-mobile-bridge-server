package api

import (
	"net/http"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func (a *Server) adminTarget(r *http.Request) (store.Principal, string, error) {
	p, err := a.administrator(r)
	if err != nil {
		return p, "", err
	}
	account := r.PathValue("account")
	if err = validID(account); err != nil {
		return p, "", err
	}
	var exists bool
	if err = a.Store.Pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)", account).Scan(&exists); err != nil {
		return p, "", err
	}
	if !exists {
		return p, "", store.ErrNotFound
	}
	return p, account, nil
}

func (a *Server) adminDevices(w http.ResponseWriter, r *http.Request) error {
	_, account, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	role := r.URL.Query().Get("role")
	if role != "gateway" && role != "mobile" || len(r.URL.Query()["role"]) != 1 {
		return bad("invalid_device_role")
	}
	table := "gateways"
	if role == "mobile" {
		table = "installations"
	}
	return a.listPage(w, r, "devices", "devices:"+role+":"+account, `SELECT d.id::text,d.name,$2::text AS role,d.session_id::text,d.created_at,d.revoked_at
		FROM `+table+` d WHERE d.account_id=$1`, "d.created_at", "d.id", account, role)
}

func (a *Server) adminBindings(w http.ResponseWriter, r *http.Request) error {
	_, account, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	return a.listPage(w, r, "bindings", "bindings:"+account, `SELECT b.id::text,b.gateway_id::text,b.installation_id::text,
		g.name AS gateway_name,i.name AS device_name,b.status,b.access_revision,b.created_at,b.revoked_at
		FROM bindings b JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
		WHERE g.account_id=$1 AND i.account_id=$1`, "b.created_at", "b.id", account)
}

func (a *Server) adminSessions(w http.ResponseWriter, r *http.Request) error {
	p, account, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	return a.listPage(w, r, "sessions", "sessions:"+account, `SELECT s.id::text,s.created_at,s.refresh_expires_at AS expires_at,
		s.revoked_at,(s.id=$2) AS current FROM sessions s WHERE s.account_id=$1`, "s.created_at", "s.id", account, p.SessionID)
}

func (a *Server) adminRevokeResource(w http.ResponseWriter, r *http.Request, kind string) error {
	p, account, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	if err = a.Store.RevokeResource(r.Context(), p, account, kind, id); err != nil {
		return err
	}
	switch kind {
	case "gateway", "mobile":
		a.Hub.CloseDevice(id)
	case "binding":
		a.Hub.CloseBinding(id)
	case "session":
		a.Hub.CloseSession(id)
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
	return nil
}
