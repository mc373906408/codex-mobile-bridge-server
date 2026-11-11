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

type AdminTopup struct {
	RequestID         string     `json:"request_id"`
	AccountID         string     `json:"account_id"`
	Days              int        `json:"days"`
	Note              string     `json:"note"`
	PreviousExpiresAt *time.Time `json:"previous_expires_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

func (s *Store) Topup(ctx context.Context, actor Principal, account, requestID string, days int, note string) (AdminTopup, error) {
	result := AdminTopup{RequestID: requestID, AccountID: account, Days: days, Note: note}
	if _, err := security.IDBytes(requestID); err != nil || days < 1 || days > 3650 || len(note) > 1000 || !utf8.ValidString(note) || strings.ContainsRune(note, '\x00') {
		return result, ErrDenied
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The actor/target account locks serialize grants with redemptions and revocations.
		if err := AuthorizeAdministration(ctx, tx, actor, account); err != nil {
			return err
		}
		var previous AdminTopup
		err := tx.QueryRow(ctx, `SELECT request_id,account_id,days,note,previous_expires_at,expires_at,created_at
			FROM admin_topups WHERE actor_id=$1 AND request_id=$2`, actor.ID, requestID).
			Scan(&previous.RequestID, &previous.AccountID, &previous.Days, &previous.Note, &previous.PreviousExpiresAt, &previous.ExpiresAt, &previous.CreatedAt)
		if err == nil {
			if previous.AccountID != account || previous.Days != days || previous.Note != note {
				return ErrConflict
			}
			result = previous
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var eligible bool
		if err = tx.QueryRow(ctx, `SELECT verified AND NOT disabled AND NOT administrator FROM accounts WHERE id=$1`, account).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return ErrDenied
		}
		var now time.Time
		if err = tx.QueryRow(ctx, "SELECT expires_at,clock_timestamp() FROM entitlements WHERE account_id=$1 FOR UPDATE", account).Scan(&result.PreviousExpiresAt, &now); err != nil {
			return err
		}
		base := now
		if result.PreviousExpiresAt != nil && result.PreviousExpiresAt.After(base) {
			base = *result.PreviousExpiresAt
		}
		result.ExpiresAt = base.Add(time.Duration(days) * 24 * time.Hour)
		if _, err = tx.Exec(ctx, "UPDATE entitlements SET expires_at=$2 WHERE account_id=$1", account, result.ExpiresAt); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `INSERT INTO admin_topups(actor_id,request_id,account_id,days,note,previous_expires_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`, actor.ID, requestID, account, days, note, result.PreviousExpiresAt, result.ExpiresAt).Scan(&result.CreatedAt); err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: actor.ID, AccountID: account, SessionID: actor.SessionID,
			Action: "entitlement.topup", TargetID: account, Before: map[string]any{"expires_at": result.PreviousExpiresAt},
			After: map[string]any{"expires_at": result.ExpiresAt, "days": days, "note": note, "request_id": requestID}})
	})
	return result, err
}
