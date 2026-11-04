package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// AuditEvent contains identities and state transitions, never credentials or request bodies.
// Identity columns deliberately survive account/device deletion.
type AuditEvent struct {
	ActorID, AccountID, SessionID, DeviceID string
	Action, TargetID                        string
	Before, After                           map[string]any
}

func RecordAudit(ctx context.Context, tx pgx.Tx, event AuditEvent) error {
	details, err := json.Marshal(map[string]any{"before": event.Before, "after": event.After})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_id,account_id,session_id,device_id,action,target_id,details)
		VALUES(NULLIF($1,'')::uuid,NULLIF($2,'')::uuid,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,$5,NULLIF($6,'')::uuid,$7)`,
		event.ActorID, event.AccountID, event.SessionID, event.DeviceID, event.Action, event.TargetID, details)
	return err
}

func deviceAudit(d Device, action, target string, before, after map[string]any) AuditEvent {
	return AuditEvent{ActorID: d.AccountID, AccountID: d.AccountID, SessionID: d.SessionID,
		DeviceID: d.ID, Action: action, TargetID: target, Before: before, After: after}
}
