package api

import (
	"net/http"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/push"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func (a *Server) deviceRoutes() {
	a.handle("GET /v1/gateways", a.gateways)
	a.handle("POST /v1/gateways/enroll", func(w http.ResponseWriter, r *http.Request) error { return a.enroll(w, r, "gateway") })
	a.handle("POST /v1/installations/enroll", func(w http.ResponseWriter, r *http.Request) error { return a.enroll(w, r, "mobile") })
	a.handle("DELETE /v1/gateways/{id}", func(w http.ResponseWriter, r *http.Request) error { return a.revokeDevice(w, r, "gateways") })
	a.handle("DELETE /v1/installations/{id}", func(w http.ResponseWriter, r *http.Request) error { return a.revokeDevice(w, r, "installations") })
	a.handle("POST /v1/pairings", a.newPairing)
	a.handle("POST /v1/pairings/{id}/join", a.joinPairing)
	a.handle("GET /v1/bindings", a.bindings)
	a.handle("GET /v1/gateway/bindings", a.gatewayBindings)
	a.handle("GET /v1/bindings/{id}", a.mobileBinding)
	a.handle("POST /v1/bindings/{id}/approve", a.approveBinding)
	a.handle("DELETE /v1/bindings/{id}", a.revokeBinding)
	a.handle("GET /v1/ws/gateway", a.gatewaySocket)
	a.handle("GET /v1/ws/mobile", a.mobileSocket)
	a.handle("POST /v1/push/endpoints", a.endpoint)
	a.handle("POST /v1/push/jobs", a.pushJob)
	a.handle("POST /v1/push/ack", a.ack)
	a.handle("POST /v1/push/cancel", a.cancelPush)
}
func (a *Server) enroll(w http.ResponseWriter, r *http.Request, role string) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	var in struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Key  string `json:"control_public_key"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if in.ID != "" {
		if err = validID(in.ID); err != nil {
			return err
		}
	}
	key, err := store.PublicKey(in.Key)
	if err != nil {
		return bad("invalid_public_key")
	}
	max := a.Config.MaxInstallations
	if role == "gateway" {
		max = a.Config.MaxGateways
		if len(in.Name) < 1 || len(in.Name) > 100 {
			return bad("invalid_name")
		}
	}
	d, err := a.Store.Enroll(r.Context(), p, role, in.ID, in.Name, key, max)
	if err != nil {
		return err
	}
	writeJSON(w, 200, d)
	return nil
}
func (a *Server) gateways(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	rows, err := a.Store.Pool.Query(r.Context(), "SELECT id,name,control_public_key,revoked_at FROM gateways WHERE account_id=$1 ORDER BY created_at LIMIT 100", p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name string
		var key []byte
		var revoked *time.Time
		if err = rows.Scan(&id, &name, &key, &revoked); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "name": name, "control_public_key": key, "revoked_at": revoked})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"gateways": items})
	return nil
}
func (a *Server) revokeDevice(w http.ResponseWriter, r *http.Request, table string) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	tag, err := a.Store.Pool.Exec(r.Context(), "UPDATE "+table+" SET revoked_at=now() WHERE id=$1 AND account_id=$2", id, p.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return store.ErrDenied
	}
	a.Hub.CloseDevice(id)
	writeJSON(w, 200, map[string]bool{"revoked": true})
	return nil
}
func (a *Server) newPairing(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "gateway", true)
	if err != nil {
		return err
	}
	if err = a.limit(r, "pairing", d.ID, 12, time.Hour); err != nil {
		return err
	}
	p, err := a.Store.NewPairing(r.Context(), d)
	if err != nil {
		return err
	}
	writeJSON(w, 200, p)
	return nil
}
func (a *Server) joinPairing(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "mobile", true)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	if err = a.limit(r, "pairing_join", d.ID, 20, time.Hour); err != nil {
		return err
	}
	b, err := a.Store.JoinPairing(r.Context(), id, d)
	if err != nil {
		return err
	}
	writeJSON(w, 200, b)
	return nil
}
func (a *Server) bindings(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	rows, err := a.Store.Pool.Query(r.Context(), `SELECT b.id,b.gateway_id,b.installation_id,b.status,g.control_public_key,i.control_public_key,p.revision,COALESCE(p.enabled AND p.consent,false)
		FROM bindings b JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
		LEFT JOIN push_endpoints p ON p.binding_id=b.id WHERE g.account_id=$1 ORDER BY b.created_at LIMIT 200`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]store.Binding, 0)
	for rows.Next() {
		var b store.Binding
		if err = rows.Scan(&b.ID, &b.GatewayID, &b.InstallationID, &b.Status, &b.GatewayPublicKey, &b.MobilePublicKey, &b.EndpointRevision, &b.PushEnabled); err != nil {
			return err
		}
		items = append(items, b)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"bindings": items})
	return nil
}
func (a *Server) approveBinding(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "gateway", true)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	var in struct {
		Key string `json:"mobile_public_key"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	key, err := store.PublicKey(in.Key)
	if err != nil {
		return bad("invalid_public_key")
	}
	if err = a.Store.ApproveBinding(r.Context(), id, d, key); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]bool{"approved": true})
	return nil
}

func (a *Server) gatewayBindings(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "gateway", false)
	if err != nil {
		return err
	}
	rows, err := a.Store.Pool.Query(r.Context(), `SELECT b.id,b.gateway_id,b.installation_id,b.status,g.control_public_key,i.control_public_key,p.revision,COALESCE(p.enabled AND p.consent,false)
		FROM bindings b JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
		LEFT JOIN push_endpoints p ON p.binding_id=b.id WHERE b.gateway_id=$1 ORDER BY b.created_at DESC LIMIT 200`, d.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]store.Binding, 0)
	for rows.Next() {
		var b store.Binding
		if err = rows.Scan(&b.ID, &b.GatewayID, &b.InstallationID, &b.Status, &b.GatewayPublicKey, &b.MobilePublicKey, &b.EndpointRevision, &b.PushEnabled); err != nil {
			return err
		}
		items = append(items, b)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"bindings": items})
	return nil
}

func (a *Server) mobileBinding(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "mobile", false)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	b, err := a.Store.Binding(r.Context(), id, d, true)
	if err != nil {
		return err
	}
	writeJSON(w, 200, b)
	return nil
}
func (a *Server) revokeBinding(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	if err = a.Store.RevokeBinding(r.Context(), id, p.ID); err != nil {
		return err
	}
	a.Hub.CloseBinding(id)
	writeJSON(w, 200, map[string]bool{"revoked": true})
	return nil
}
func (a *Server) gatewaySocket(w http.ResponseWriter, r *http.Request) error {
	d, token, err := a.device(r, "gateway", true)
	if err != nil {
		return err
	}
	a.Hub.Serve(w, r, d, store.Binding{}, token)
	return nil
}
func (a *Server) mobileSocket(w http.ResponseWriter, r *http.Request) error {
	d, token, err := a.device(r, "mobile", true)
	if err != nil {
		return err
	}
	id := r.Header.Get("X-Bridge-Binding")
	if err = validID(id); err != nil {
		return err
	}
	b, err := a.Store.Binding(r.Context(), id, d, true)
	if err != nil {
		return err
	}
	a.Hub.Serve(w, r, d, b, token)
	return nil
}
func (a *Server) endpoint(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "mobile", false)
	if err != nil {
		return err
	}
	var in store.EndpointInput
	if err = body(w, r, &in); err != nil {
		return err
	}
	if err = validID(in.BindingID); err != nil {
		return err
	}
	result, err := a.Store.RegisterEndpoint(r.Context(), d, in, a.Config)
	if err != nil {
		return err
	}
	writeJSON(w, 200, result)
	return nil
}
func (a *Server) pushJob(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "gateway", true)
	if err != nil {
		return err
	}
	var in struct {
		Envelope store.Envelope `json:"envelope"`
		Revision string         `json:"endpoint_revision"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if err = validID(in.Revision); err != nil {
		return err
	}
	if _, err = push.APNSPayload(in.Envelope); err != nil {
		return bad("payload_limit")
	}
	if err = a.limit(r, "push", d.ID, 600, time.Hour); err != nil {
		return err
	}
	id, err := a.Store.QueuePush(r.Context(), d, in.Envelope, in.Revision, a.Config.Paid())
	if err != nil {
		return err
	}
	writeJSON(w, 202, map[string]string{"id": id})
	return nil
}
func (a *Server) ack(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "mobile", false)
	if err != nil {
		return err
	}
	var in struct {
		BindingID string   `json:"binding_id"`
		IDs       []string `json:"notification_ids"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if err = validID(in.BindingID); err != nil {
		return err
	}
	if err = a.Store.Acknowledge(r.Context(), d, in.BindingID, in.IDs); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]bool{"acknowledged": true})
	return nil
}
func (a *Server) cancelPush(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "gateway", false)
	if err != nil {
		return err
	}
	var in struct {
		BindingID string   `json:"binding_id"`
		IDs       []string `json:"notification_ids"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if err = validID(in.BindingID); err != nil {
		return err
	}
	if err = a.Store.CancelPush(r.Context(), d, in.BindingID, in.IDs); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]bool{"cancelled": true})
	return nil
}
