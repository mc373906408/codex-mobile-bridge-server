package api

import (
	"errors"
	"net/http"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func (a *Server) campaignAdministrator(r *http.Request) (store.Principal, error) {
	p, err := a.administrator(r)
	if err == nil && !a.Config.Paid() {
		err = failure{404, "registration_campaigns_disabled"}
	}
	return p, err
}

func campaignError(err error) error {
	switch {
	case errors.Is(err, store.ErrCampaignInput):
		return bad("invalid_registration_campaign")
	case errors.Is(err, store.ErrCampaignOpen):
		return failure{409, "registration_campaign_already_open"}
	case errors.Is(err, store.ErrCampaignState):
		return failure{409, "registration_campaign_locked"}
	default:
		return err
	}
}

func (a *Server) adminCampaigns(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.campaignAdministrator(r); err != nil {
		return err
	}
	// Reads derive elapsed states without requiring a background job or changing history.
	return a.listPage(w, r, "campaigns", "registration-campaigns", `SELECT c.id::text,c.name,c.days,c.starts_at,c.ends_at,c.max_grants,c.state,
		CASE WHEN c.state='ended' OR (c.state='enabled' AND c.ends_at<=statement_timestamp()) THEN 'ended'
		 WHEN c.state='draft' THEN 'draft' WHEN c.starts_at>statement_timestamp() THEN 'scheduled' ELSE 'running' END AS status,
		CASE WHEN c.state='enabled' AND c.ends_at<=statement_timestamp() THEN 'expired' ELSE c.end_reason END AS end_reason,
		c.activated_at,CASE WHEN c.state='enabled' AND c.ends_at<=statement_timestamp() THEN c.ends_at ELSE c.ended_at END AS ended_at,
		c.granted_count,c.granted_count::bigint*c.days AS granted_days,
		CASE WHEN c.max_grants IS NULL THEN NULL ELSE c.max_grants-c.granted_count END AS remaining_grants,c.created_at,c.updated_at
		FROM registration_campaigns c WHERE true`, "c.created_at", "c.id")
}

func (a *Server) adminCreateCampaign(w http.ResponseWriter, r *http.Request) error {
	p, err := a.campaignAdministrator(r)
	if err != nil {
		return err
	}
	in := store.CampaignInput{Days: 7}
	if err = body(w, r, &in); err != nil {
		return err
	}
	c, err := a.Store.CreateRegistrationCampaign(r.Context(), p, in)
	if err != nil {
		return campaignError(err)
	}
	writeJSON(w, 201, c)
	return nil
}

func (a *Server) adminChangeCampaign(w http.ResponseWriter, r *http.Request, action string) error {
	p, err := a.campaignAdministrator(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	var in store.CampaignInput
	if action == "edit" {
		err = body(w, r, &in)
	} else {
		err = body(w, r, &struct{}{})
	}
	if err != nil {
		return err
	}
	c, err := a.Store.ChangeRegistrationCampaign(r.Context(), p, id, action, in)
	if err != nil {
		return campaignError(err)
	}
	writeJSON(w, 200, c)
	return nil
}

func (a *Server) adminCampaignGrants(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.campaignAdministrator(r); err != nil {
		return err
	}
	id := r.PathValue("id")
	if err := validID(id); err != nil {
		return err
	}
	var exists bool
	if err := a.Store.Pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM registration_campaigns WHERE id=$1)", id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return store.ErrNotFound
	}
	return a.listPage(w, r, "grants", "registration-campaign-grants:"+id, `SELECT g.id::text,g.campaign_id::text,g.account_id::text,a.login,g.days,g.granted_at,g.expires_at
		FROM registration_campaign_grants g JOIN accounts a ON a.id=g.account_id WHERE g.campaign_id=$1`, "g.granted_at", "g.id", id)
}
