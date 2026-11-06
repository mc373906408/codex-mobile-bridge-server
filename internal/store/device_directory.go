package store

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Enrollment already holds the account lock, so simultaneous logins cannot duplicate pairs.
func discoverDevices(ctx context.Context, tx pgx.Tx, d Device) error {
	rows, err := tx.Query(ctx, `INSERT INTO bindings(id,gateway_id,installation_id)
		SELECT gen_random_uuid(),g.id,i.id FROM gateways g JOIN installations i ON i.account_id=g.account_id
		WHERE g.account_id=$1 AND g.revoked_at IS NULL AND i.revoked_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM bindings b WHERE b.gateway_id=g.id AND b.installation_id=i.id) RETURNING id`, d.AccountID)
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
		if err = RecordAudit(ctx, tx, deviceAudit(d, "binding.discover", id, nil, map[string]any{"status": "pending", "access_revision": 0})); err != nil {
			return err
		}
	}
	return nil
}

const directoryColumns = `b.id,b.gateway_id,b.installation_id,b.status,g.control_public_key,i.control_public_key,
	e.revision,COALESCE(b.status='active' AND e.enabled AND e.consent,false),i.name,g.name,b.access_revision,
	(g.revoked_at IS NOT NULL OR i.revoked_at IS NOT NULL),
	COALESCE(i.last_seen_at>clock_timestamp()-interval '12 seconds' AND i.revoked_at IS NULL
	AND ms.revoked_at IS NULL AND ms.refresh_expires_at>clock_timestamp(),false),
	COALESCE(ms.revoked_at IS NULL AND ms.refresh_expires_at>clock_timestamp()
	AND gs.revoked_at IS NULL AND gs.refresh_expires_at>clock_timestamp(),false)`

const directoryJoins = ` FROM bindings b JOIN gateways g ON g.id=b.gateway_id
	JOIN installations i ON i.id=b.installation_id JOIN sessions ms ON ms.id=i.session_id
	JOIN sessions gs ON gs.id=g.session_id
	LEFT JOIN push_endpoints e ON e.binding_id=b.id `

func scanDirectory(row pgx.Row) (Binding, error) {
	var b Binding
	err := row.Scan(&b.ID, &b.GatewayID, &b.InstallationID, &b.Status, &b.GatewayPublicKey, &b.MobilePublicKey,
		&b.EndpointRevision, &b.PushEnabled, &b.DeviceName, &b.GatewayName, &b.AccessRevision, &b.DeviceRevoked, &b.MobileOnline, &b.SessionsValid)
	return b, err
}

// Directory reads retain offline/disabled entries and never create or approve bindings.
func (s *Store) DeviceBindings(ctx context.Context, d Device) ([]Binding, error) {
	column := "b.gateway_id"
	if d.Role == "mobile" {
		column = "b.installation_id"
	} else if d.Role != "gateway" {
		return nil, ErrDenied
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ON (b.gateway_id,b.installation_id) `+directoryColumns+directoryJoins+
		`WHERE `+column+`=$1 AND g.account_id=$2 AND i.account_id=$2
		ORDER BY b.gateway_id,b.installation_id,(b.status='revoked'),b.created_at DESC,b.id LIMIT 200`, d.ID, d.AccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Binding, 0)
	for rows.Next() {
		b, err := scanDirectory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}

func (s *Store) DirectoryBinding(ctx context.Context, id string, d Device) (Binding, error) {
	b, err := scanDirectory(s.Pool.QueryRow(ctx, `SELECT `+directoryColumns+directoryJoins+
		`WHERE b.id=$1 AND g.account_id=$2 AND i.account_id=$2`, id, d.AccountID))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	if err == nil && (d.Role == "gateway" && b.GatewayID != d.ID || d.Role == "mobile" && b.InstallationID != d.ID || d.Role != "mobile" && d.Role != "gateway") {
		err = ErrDenied
	}
	return b, err
}

func (s *Store) AccountBindings(ctx context.Context, accountID string) ([]Binding, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+directoryColumns+directoryJoins+
		`WHERE g.account_id=$1 AND i.account_id=$1 ORDER BY b.created_at DESC,b.id LIMIT 200`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Binding, 0)
	for rows.Next() {
		b, err := scanDirectory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}

func (s *Store) TouchInstallation(ctx context.Context, d Device) error {
	if d.Role != "mobile" {
		return ErrDenied
	}
	_, err := s.Pool.Exec(ctx, `UPDATE installations SET last_seen_at=clock_timestamp()
		WHERE id=$1 AND account_id=$2 AND session_id=$3 AND revoked_at IS NULL`, d.ID, d.AccountID, d.SessionID)
	return err
}

// Closing always advances the fence. An old opening can never override a close,
// including when the earlier opening reached the server before the close did.
func (s *Store) SetBindingAccess(ctx context.Context, id string, d Device, key []byte, allowed bool, expected int64, paid bool) (Binding, error) {
	if d.Role != "gateway" || expected < 0 {
		return Binding{}, ErrDenied
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var eligible bool
		err := tx.QueryRow(ctx, `SELECT a.verified AND NOT a.disabled AND NOT a.credentials_change_required
			AND (NOT $2 OR COALESCE(e.expires_at>clock_timestamp(),false)) FROM accounts a JOIN entitlements e ON e.account_id=a.id
			WHERE a.id=$1 FOR UPDATE OF a`, d.AccountID, allowed && paid).Scan(&eligible)
		if err != nil {
			return err
		}
		if !eligible {
			return ErrDenied
		}
		var status string
		var revision int64
		var mobileKey []byte
		var liveMobile bool
		err = tx.QueryRow(ctx, `SELECT b.status,b.access_revision,i.control_public_key,
			i.revoked_at IS NULL AND ms.revoked_at IS NULL AND ms.refresh_expires_at>clock_timestamp()
			FROM bindings b JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
			JOIN sessions gs ON gs.id=g.session_id JOIN sessions ms ON ms.id=i.session_id
			WHERE b.id=$1 AND b.gateway_id=$2 AND g.account_id=$3 AND i.account_id=$3
			AND g.revoked_at IS NULL AND g.session_id=$4 AND gs.revoked_at IS NULL AND gs.refresh_expires_at>clock_timestamp()
			FOR UPDATE OF b`, id, d.ID, d.AccountID, d.SessionID).Scan(&status, &revision, &mobileKey, &liveMobile)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil {
			return err
		}
		if status == "revoked" || subtle.ConstantTimeCompare(key, mobileKey) != 1 || allowed && !liveMobile {
			return ErrDenied
		}
		if expected > revision || allowed && expected != revision {
			return ErrConflict
		}
		next := "disabled"
		if allowed {
			next = "active"
		}
		if _, err = tx.Exec(ctx, `UPDATE bindings SET status=$2,access_revision=access_revision+1,
			approved_at=CASE WHEN $3 THEN COALESCE(approved_at,clock_timestamp()) ELSE approved_at END WHERE id=$1`, id, next, allowed); err != nil {
			return err
		}
		if !allowed {
			_, err = tx.Exec(ctx, `UPDATE push_jobs SET status='cancelled' WHERE binding_id=$1 AND status IN ('queued','leased')`, id)
		}
		if err != nil {
			return err
		}
		return RecordAudit(ctx, tx, deviceAudit(d, "binding.access", id,
			map[string]any{"status": status, "access_revision": revision}, map[string]any{"status": next, "access_revision": revision + 1}))
	})
	if err != nil {
		return Binding{}, err
	}
	return s.DirectoryBinding(ctx, id, d)
}
