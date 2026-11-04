package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Lock accounts before resources, as enrollment and access changes do. Authorization
// is rechecked after the lock; a stale administrator principal cannot mutate another account.
func authorizeAccountMutation(ctx context.Context, tx pgx.Tx, actor Principal, account string) error {
	rows, err := tx.Query(ctx, "SELECT id FROM accounts WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE", []string{actor.ID, account})
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		found = found || id == account
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT a.verified AND NOT a.disabled AND NOT a.credentials_change_required
		AND (a.id=$3 OR a.administrator) AND s.revoked_at IS NULL AND s.refresh_expires_at>clock_timestamp()
		AND (CASE WHEN $4 THEN s.web_expires_at ELSE s.access_expires_at END)>clock_timestamp()
		FROM accounts a JOIN sessions s ON s.account_id=a.id WHERE a.id=$1 AND s.id=$2`, actor.ID, actor.SessionID, account, actor.Web).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !allowed {
		return ErrDenied
	}
	return err
}

func RevokeAccountSessions(ctx context.Context, tx pgx.Tx, actor Principal, account, cause string) error {
	rows, err := tx.Query(ctx, "UPDATE sessions SET revoked_at=clock_timestamp() WHERE account_id=$1 AND revoked_at IS NULL RETURNING id", account)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = RecordAudit(ctx, tx, AuditEvent{ActorID: actor.ID, AccountID: account, SessionID: actor.SessionID,
			Action: "session.revoke", TargetID: id, Before: map[string]any{"revoked": false},
			After: map[string]any{"revoked": true, "cause": cause}}); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE push_jobs SET status='cancelled' WHERE status IN ('queued','leased') AND binding_id IN
		(SELECT b.id FROM bindings b JOIN gateways g ON g.id=b.gateway_id WHERE g.account_id=$1)`, account)
	return err
}

func AuthorizeAdministration(ctx context.Context, tx pgx.Tx, actor Principal, account string) error {
	if err := authorizeAccountMutation(ctx, tx, actor, account); err != nil {
		return err
	}
	var admin bool
	if err := tx.QueryRow(ctx, "SELECT administrator FROM accounts WHERE id=$1", actor.ID).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return ErrDenied
	}
	return nil
}

func revokeBindingTx(ctx context.Context, tx pgx.Tx, actor Principal, account, id, cause string) error {
	var status string
	var revision int64
	err := tx.QueryRow(ctx, `SELECT b.status,b.access_revision FROM bindings b
		JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
		WHERE b.id=$1 AND g.account_id=$2 AND i.account_id=$2 FOR UPDATE OF b`, id, account).Scan(&status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil || status == "revoked" {
		return err
	}
	var pushEnabled, pushConsent bool
	err = tx.QueryRow(ctx, "SELECT enabled,consent FROM push_endpoints WHERE binding_id=$1 FOR UPDATE", id).Scan(&pushEnabled, &pushConsent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE bindings SET status='revoked',revoked_at=clock_timestamp(),access_revision=access_revision+1 WHERE id=$1", id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE push_endpoints SET enabled=false,consent=false WHERE binding_id=$1", id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE push_jobs SET status='cancelled' WHERE binding_id=$1 AND status IN ('queued','leased')", id); err != nil {
		return err
	}
	return RecordAudit(ctx, tx, AuditEvent{ActorID: actor.ID, AccountID: account, SessionID: actor.SessionID,
		Action: "binding.revoke", TargetID: id, Before: map[string]any{"status": status, "access_revision": revision, "push_enabled": pushEnabled, "push_consent": pushConsent},
		After: map[string]any{"status": "revoked", "access_revision": revision + 1, "push_enabled": false, "push_consent": false, "cause": cause}})
}

// RevokeResource serves both self-service and explicit administrator operations.
// kind is selected by a registered route, never used as a caller-provided SQL identifier.
func (s *Store) RevokeResource(ctx context.Context, actor Principal, account, kind, id string) error {
	if kind != "gateway" && kind != "mobile" && kind != "binding" && kind != "session" {
		return ErrDenied
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := authorizeAccountMutation(ctx, tx, actor, account); err != nil {
			return err
		}
		if kind == "binding" {
			return revokeBindingTx(ctx, tx, actor, account, id, "explicit")
		}
		table, action := "sessions", "session.revoke"
		if kind == "gateway" {
			table, action = "gateways", "device.revoke"
		} else if kind == "mobile" {
			table, action = "installations", "device.revoke"
		}
		var revoked bool
		err := tx.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM "+table+" WHERE id=$1 AND account_id=$2 FOR UPDATE", id, account).Scan(&revoked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil || revoked {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE "+table+" SET revoked_at=clock_timestamp() WHERE id=$1", id); err != nil {
			return err
		}
		if kind != "session" {
			column := "gateway_id"
			if kind == "mobile" {
				column = "installation_id"
			}
			rows, err := tx.Query(ctx, "SELECT id FROM bindings WHERE "+column+"=$1 ORDER BY id", id)
			if err != nil {
				return err
			}
			var bindings []string
			for rows.Next() {
				var binding string
				if err = rows.Scan(&binding); err != nil {
					rows.Close()
					return err
				}
				bindings = append(bindings, binding)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			for _, binding := range bindings {
				if err = revokeBindingTx(ctx, tx, actor, account, binding, kind+"_revoked"); err != nil {
					return err
				}
			}
		} else if _, err = tx.Exec(ctx, `UPDATE push_jobs SET status='cancelled' WHERE status IN ('queued','leased') AND binding_id IN
			(SELECT b.id FROM bindings b JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
			WHERE g.session_id=$1 OR i.session_id=$1)`, id); err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: actor.ID, AccountID: account, SessionID: actor.SessionID,
			Action: action, TargetID: id, Before: map[string]any{"revoked": false}, After: map[string]any{"revoked": true, "kind": kind}})
	})
}
