package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

var ErrCampaignOpen = errors.New("registration campaign already open")
var ErrCampaignState = errors.New("registration campaign rules are locked")
var ErrCampaignInput = errors.New("invalid registration campaign")

type CampaignInput struct {
	Name      string     `json:"name"`
	Days      int        `json:"days"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
	MaxGrants *int       `json:"max_grants"`
}

type RegistrationCampaign struct {
	CampaignInput
	ID           string     `json:"id"`
	State        string     `json:"state"`
	GrantedCount int        `json:"granted_count"`
	ActivatedAt  *time.Time `json:"activated_at"`
	EndedAt      *time.Time `json:"ended_at"`
	EndReason    *string    `json:"end_reason"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const campaignColumns = `id::text,name,days,starts_at,ends_at,max_grants,state,granted_count,activated_at,ended_at,end_reason,created_at,updated_at`

func scanCampaign(row pgx.Row) (c RegistrationCampaign, err error) {
	err = row.Scan(&c.ID, &c.Name, &c.Days, &c.StartsAt, &c.EndsAt, &c.MaxGrants, &c.State, &c.GrantedCount, &c.ActivatedAt, &c.EndedAt, &c.EndReason, &c.CreatedAt, &c.UpdatedAt)
	return
}

func validateCampaign(in *CampaignInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 120 || strings.ContainsAny(in.Name, "\x00\r\n") || in.Days < 1 || in.Days > 3650 || (in.MaxGrants != nil && (*in.MaxGrants < 1 || int64(*in.MaxGrants) > 2147483647)) {
		return ErrCampaignInput
	}
	for _, stamp := range []*time.Time{in.StartsAt, in.EndsAt} {
		if stamp != nil && (stamp.Year() < 1 || stamp.Year() > 9999) {
			return ErrCampaignInput
		}
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.EndsAt.After(*in.StartsAt) {
		return ErrCampaignInput
	}
	return nil
}

// Serialize selection across replacement of the current row, including an empty slot.
// Account locks always precede this lock. No campaign path locks an existing recipient.
func lockCampaigns(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(607843093)")
	return err
}

func databaseTime(ctx context.Context, tx pgx.Tx) (now time.Time, err error) {
	err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now)
	return
}

func endCampaign(ctx context.Context, tx pgx.Tx, c *RegistrationCampaign, reason string, now time.Time, actor Principal) error {
	before := *c
	ended := now
	if reason == "expired" && c.EndsAt != nil {
		ended = *c.EndsAt
	}
	c.State, c.EndReason, c.EndedAt, c.UpdatedAt = "ended", &reason, &ended, now
	if _, err := tx.Exec(ctx, `UPDATE registration_campaigns SET state='ended',end_reason=$2,ended_at=$3,updated_at=$4 WHERE id=$1`, c.ID, reason, ended, now); err != nil {
		return err
	}
	return RecordAudit(ctx, tx, AuditEvent{ActorID: actor.ID, SessionID: actor.SessionID, Action: "campaign.end", TargetID: c.ID,
		Before: map[string]any{"state": before.State}, After: map[string]any{"state": c.State, "reason": reason, "granted_count": c.GrantedCount}})
}

func settleCampaign(ctx context.Context, tx pgx.Tx, c *RegistrationCampaign, now time.Time) error {
	if c.State != "enabled" {
		return nil
	}
	reason := ""
	if c.EndsAt != nil && !now.Before(*c.EndsAt) {
		reason = "expired"
	}
	if c.MaxGrants != nil && c.GrantedCount >= *c.MaxGrants {
		reason = "exhausted"
	}
	if reason == "" {
		return nil
	}
	return endCampaign(ctx, tx, c, reason, now, Principal{})
}

func (s *Store) CreateRegistrationCampaign(ctx context.Context, actor Principal, in CampaignInput) (out RegistrationCampaign, err error) {
	if err = validateCampaign(&in); err != nil {
		return
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := AuthorizeAdministration(ctx, tx, actor, actor.ID); err != nil {
			return err
		}
		if err := lockCampaigns(ctx, tx); err != nil {
			return err
		}
		current, err := scanCampaign(tx.QueryRow(ctx, "SELECT "+campaignColumns+" FROM registration_campaigns WHERE state<>'ended' FOR UPDATE"))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		now, e := databaseTime(ctx, tx)
		if e != nil {
			return e
		}
		if err == nil {
			if err = settleCampaign(ctx, tx, &current, now); err != nil {
				return err
			}
			if current.State != "ended" {
				return ErrCampaignOpen
			}
		}
		id, err := security.ID()
		if err != nil {
			return err
		}
		out, err = scanCampaign(tx.QueryRow(ctx, `INSERT INTO registration_campaigns(id,name,days,starts_at,ends_at,max_grants,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$7) RETURNING `+campaignColumns, id, in.Name, in.Days, in.StartsAt, in.EndsAt, in.MaxGrants, now))
		if err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: actor.ID, SessionID: actor.SessionID, Action: "campaign.create", TargetID: id, After: map[string]any{"campaign": out}})
	})
	return
}

func (s *Store) ChangeRegistrationCampaign(ctx context.Context, actor Principal, id, action string, in CampaignInput) (out RegistrationCampaign, err error) {
	if action == "edit" {
		if err = validateCampaign(&in); err != nil {
			return
		}
	}
	// A business conflict after automatic expiry must still persist the expiry event.
	var conflict error
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := AuthorizeAdministration(ctx, tx, actor, actor.ID); err != nil {
			return err
		}
		if err := lockCampaigns(ctx, tx); err != nil {
			return err
		}
		var err error
		out, err = scanCampaign(tx.QueryRow(ctx, "SELECT "+campaignColumns+" FROM registration_campaigns WHERE id=$1 FOR UPDATE", id))
		if err != nil {
			return err
		}
		now, err := databaseTime(ctx, tx)
		if err != nil {
			return err
		}
		if err = settleCampaign(ctx, tx, &out, now); err != nil {
			return err
		}
		before := out
		switch action {
		case "stop":
			if out.State == "ended" {
				return nil
			}
			return endCampaign(ctx, tx, &out, "stopped", now, actor)
		case "activate":
			if out.State == "enabled" {
				return nil
			}
			if out.State != "draft" {
				conflict = ErrCampaignState
				return nil
			}
			if out.EndsAt != nil && !out.EndsAt.After(now) {
				return ErrCampaignInput
			}
			if out.StartsAt == nil {
				out.StartsAt = &now
			}
			out.State, out.ActivatedAt = "enabled", &now
		case "edit":
			if out.State != "draft" {
				conflict = ErrCampaignState
				return nil
			}
			out.CampaignInput = in
		default:
			return ErrCampaignInput
		}
		out.UpdatedAt = now
		_, err = tx.Exec(ctx, `UPDATE registration_campaigns SET name=$2,days=$3,starts_at=$4,ends_at=$5,max_grants=$6,state=$7,activated_at=$8,updated_at=$9 WHERE id=$1`,
			id, out.Name, out.Days, out.StartsAt, out.EndsAt, out.MaxGrants, out.State, out.ActivatedAt, now)
		if err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: actor.ID, SessionID: actor.SessionID, Action: "campaign." + action, TargetID: id, Before: map[string]any{"campaign": before}, After: map[string]any{"campaign": out}})
	})
	if err == nil {
		err = conflict
	}
	return
}

// Called only for the new verified account inside CompleteRegistration's transaction.
func grantRegistrationTrial(ctx context.Context, tx pgx.Tx, account string) error {
	if err := lockCampaigns(ctx, tx); err != nil {
		return err
	}
	c, err := scanCampaign(tx.QueryRow(ctx, "SELECT "+campaignColumns+" FROM registration_campaigns WHERE state<>'ended' FOR UPDATE"))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return err
	}
	if err = settleCampaign(ctx, tx, &c, now); err != nil {
		return err
	}
	if c.State != "enabled" || c.StartsAt == nil || now.Before(*c.StartsAt) {
		return nil
	}
	id, err := security.ID()
	if err != nil {
		return err
	}
	expires := now.Add(time.Duration(c.Days) * 24 * time.Hour)
	if _, err = tx.Exec(ctx, `INSERT INTO registration_campaign_grants(id,campaign_id,account_id,days,granted_at,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, id, c.ID, account, c.Days, now, expires); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE entitlements SET expires_at=$2 WHERE account_id=$1", account, expires); err != nil {
		return err
	}
	c.GrantedCount++
	if _, err = tx.Exec(ctx, "UPDATE registration_campaigns SET granted_count=$2,updated_at=$3 WHERE id=$1", c.ID, c.GrantedCount, now); err != nil {
		return err
	}
	if err = RecordAudit(ctx, tx, AuditEvent{AccountID: account, Action: "entitlement.registration_trial", TargetID: id,
		Before: map[string]any{"expires_at": nil}, After: map[string]any{"campaign_id": c.ID, "days": c.Days, "granted_at": now, "expires_at": expires}}); err != nil {
		return err
	}
	return settleCampaign(ctx, tx, &c, now)
}
