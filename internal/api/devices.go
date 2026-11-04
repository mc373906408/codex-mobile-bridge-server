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
	a.handle("GET /v1/bindings", a.bindings)
	a.handle("GET /v1/gateway/bindings", a.gatewayBindings)
	a.handle("GET /v1/mobile/bindings", a.mobileBindings)
	a.handle("GET /v1/gateway/bindings/{id}", a.gatewayBinding)
	a.handle("POST /v1/gateway/bindings/{id}/access", a.bindingAccess)
	a.handle("GET /v1/bindings/{id}", a.mobileBinding)
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
	}
	if role == "mobile" && in.Name == "" {
		in.Name = "Phone"
	}
	if len(in.Name) < 1 || len(in.Name) > 100 {
		return bad("invalid_name")
	}
	d, err := a.Store.Enroll(r.Context(), p, role, in.ID, in.Name, key, max)
	if err != nil {
		return err
	}
	a.Hub.CloseDevice(d.ID)
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
		items = append(items, map[string]any{"id": id, "name": name, "control_public_key": key, "revoked_at": revoked, "online": revoked == nil && a.Hub.GatewayOnline(id)})
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
	role := "gateway"
	if table == "installations" {
		role = "mobile"
	}
	if err = a.Store.RevokeResource(r.Context(), p, p.ID, role, id); err != nil {
		return err
	}
	a.Hub.CloseDevice(id)
	writeJSON(w, 200, map[string]bool{"revoked": true})
	return nil
}
func (a *Server) gatewayBinding(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "gateway", false)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	b, err := a.Store.DirectoryBinding(r.Context(), id, d)
	if err != nil {
		return err
	}
	b.GatewayOnline = !b.DeviceRevoked && a.Hub.GatewayOnline(b.GatewayID)
	writeJSON(w, 200, b)
	return nil
}
func (a *Server) bindings(w http.ResponseWriter, r *http.Request) error {
	p, err := a.principal(r)
	if err != nil {
		return err
	}
	items, err := a.Store.AccountBindings(r.Context(), p.ID)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].GatewayOnline = !items[i].DeviceRevoked && a.Hub.GatewayOnline(items[i].GatewayID)
	}
	writeJSON(w, 200, map[string]any{"bindings": items})
	return nil
}
func (a *Server) gatewayBindings(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "gateway", false)
	if err != nil {
		return err
	}
	items, err := a.Store.DeviceBindings(r.Context(), d)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].GatewayOnline = !items[i].DeviceRevoked && a.Hub.GatewayOnline(items[i].GatewayID)
	}
	writeJSON(w, 200, map[string]any{"bindings": items})
	return nil
}

func (a *Server) mobileBindings(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "mobile", false)
	if err != nil {
		return err
	}
	if err = a.Store.TouchInstallation(r.Context(), d); err != nil {
		return err
	}
	items, err := a.Store.DeviceBindings(r.Context(), d)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].GatewayOnline = !items[i].DeviceRevoked && a.Hub.GatewayOnline(items[i].GatewayID)
	}
	writeJSON(w, 200, map[string]any{"bindings": items})
	return nil
}

func (a *Server) bindingAccess(w http.ResponseWriter, r *http.Request) error {
	d, _, err := a.device(r, "gateway", false)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	var in struct {
		Allowed  *bool  `json:"allowed"`
		Revision *int64 `json:"expected_revision"`
		Key      string `json:"mobile_public_key"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	if in.Allowed == nil || in.Revision == nil || *in.Revision < 0 {
		return bad("invalid_access_revision")
	}
	key, err := store.PublicKey(in.Key)
	if err != nil {
		return bad("invalid_public_key")
	}
	b, err := a.Store.SetBindingAccess(r.Context(), id, d, key, *in.Allowed, *in.Revision, a.Config.Paid())
	if err != nil {
		return err
	}
	if !*in.Allowed {
		a.Hub.CloseBinding(id)
	}
	writeJSON(w, 200, b)
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
	b, err := a.Store.DirectoryBinding(r.Context(), id, d)
	if err != nil {
		return err
	}
	b.GatewayOnline = !b.DeviceRevoked && a.Hub.GatewayOnline(b.GatewayID)
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
	if err = a.Store.RevokeResource(r.Context(), p, p.ID, "binding", id); err != nil {
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
