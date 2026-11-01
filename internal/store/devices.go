package store

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

type Device struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	SessionID string `json:"-"`
	Role      string `json:"role"`
	Name      string `json:"name,omitempty"`
	PublicKey []byte `json:"control_public_key"`
	Token     string `json:"device_token,omitempty"`
}

type Pairing struct {
	ID        string    `json:"id"`
	GatewayID string    `json:"gateway_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Binding struct {
	ID               string  `json:"id"`
	GatewayID        string  `json:"gateway_id"`
	InstallationID   string  `json:"installation_id"`
	Status           string  `json:"status"`
	GatewayPublicKey []byte  `json:"gateway_public_key"`
	MobilePublicKey  []byte  `json:"mobile_public_key"`
	EndpointRevision *string `json:"endpoint_revision,omitempty"`
	PushEnabled      bool    `json:"push_enabled"`
}

func PublicKey(value string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) != 32 || subtle.ConstantTimeCompare(b, make([]byte, 32)) == 1 {
		return nil, ErrDenied
	}
	return b, nil
}

func (s *Store) Enroll(ctx context.Context, p Principal, role, id, name string, key []byte, max int) (Device, error) {
	d := Device{ID: id, AccountID: p.ID, SessionID: p.SessionID, Role: role, Name: name, PublicKey: key}
	var err error
	if d.ID == "" {
		d.ID, err = security.ID()
		if err != nil {
			return d, err
		}
	}
	d.Token, err = security.Token()
	if err != nil {
		return d, err
	}
	table := "gateways"
	if role == "mobile" {
		table = "installations"
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, "SELECT verified AND NOT disabled AND NOT credentials_change_required FROM accounts WHERE id=$1 FOR UPDATE", p.ID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return ErrDenied
		}
		if err := tx.QueryRow(ctx, "SELECT revoked_at IS NULL AND refresh_expires_at>now() FROM sessions WHERE id=$1 AND account_id=$2 FOR UPDATE", p.SessionID, p.ID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return ErrDenied
		}
		var owner string
		var oldKey []byte
		var revokedAt *time.Time
		err := tx.QueryRow(ctx, "SELECT account_id,control_public_key,revoked_at FROM "+table+" WHERE id=$1 FOR UPDATE", d.ID).Scan(&owner, &oldKey, &revokedAt)
		if err == nil {
			if owner != p.ID || revokedAt != nil || subtle.ConstantTimeCompare(oldKey, key) != 1 {
				return ErrDenied
			}
			_, err = tx.Exec(ctx, "UPDATE "+table+" SET token_digest=$2,session_id=$3 WHERE id=$1", d.ID, security.Digest(d.Token), p.SessionID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE account_id=$1 AND revoked_at IS NULL", p.ID).Scan(&count); err != nil {
			return err
		}
		if count >= max {
			return ErrConflict
		}
		if role == "gateway" {
			_, err = tx.Exec(ctx, "INSERT INTO gateways(id,account_id,session_id,name,control_public_key,token_digest) VALUES($1,$2,$3,$4,$5,$6)", d.ID, p.ID, p.SessionID, name, key, security.Digest(d.Token))
		} else {
			_, err = tx.Exec(ctx, "INSERT INTO installations(id,account_id,session_id,control_public_key,token_digest) VALUES($1,$2,$3,$4,$5)", d.ID, p.ID, p.SessionID, key, security.Digest(d.Token))
		}
		return err
	})
	return d, err
}

func (s *Store) Device(ctx context.Context, token, role string) (Device, error) {
	d := Device{Role: role}
	table := "gateways"
	name := "d.name"
	if role == "mobile" {
		table = "installations"
		name = "''"
	}
	err := s.Pool.QueryRow(ctx, `SELECT d.id,d.account_id,d.session_id,`+name+`,d.control_public_key
		FROM `+table+` d JOIN accounts a ON a.id=d.account_id JOIN sessions s ON s.id=d.session_id
		WHERE d.token_digest=$1 AND d.revoked_at IS NULL AND a.verified AND NOT a.disabled AND NOT a.credentials_change_required
		AND s.revoked_at IS NULL AND s.refresh_expires_at>now()`, security.Digest(token)).Scan(&d.ID, &d.AccountID, &d.SessionID, &d.Name, &d.PublicKey)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return d, err
}

func (s *Store) NewPairing(ctx context.Context, d Device) (Pairing, error) {
	p := Pairing{GatewayID: d.ID}
	var err error
	p.ID, err = security.ID()
	if err != nil {
		return p, err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT id FROM gateways WHERE id=$1 FOR UPDATE", d.ID); err != nil {
			return err
		}
		// Only one pairing window per desktop. The secret never leaves that desktop.
		if _, err := tx.Exec(ctx, "UPDATE pairings SET expires_at=now() WHERE gateway_id=$1 AND expires_at>now()", d.ID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, "INSERT INTO pairings(id,gateway_id,expires_at) VALUES($1,$2,now()+interval '5 minutes') RETURNING expires_at", p.ID, d.ID).Scan(&p.ExpiresAt)
	})
	return p, err
}

func (s *Store) JoinPairing(ctx context.Context, pairingID string, mobile Device) (Binding, error) {
	var b Binding
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// A lost join/approval response may be retried, but never grants a different identity.
		var existingID string
		if err := tx.QueryRow(ctx, "SELECT id FROM bindings WHERE pairing_id=$1 AND installation_id=$2 AND status='active'", pairingID, mobile.ID).Scan(&existingID); err == nil {
			b.ID = existingID
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var accountID string
		var consumed *time.Time
		err := tx.QueryRow(ctx, `SELECT p.gateway_id,g.account_id,p.consumed_at FROM pairings p JOIN gateways g ON g.id=p.gateway_id
			WHERE p.id=$1 AND p.expires_at>now() AND g.revoked_at IS NULL FOR UPDATE OF p`, pairingID).Scan(&b.GatewayID, &accountID, &consumed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil {
			return err
		}
		if accountID != mobile.AccountID || consumed != nil {
			return ErrDenied
		}
		var candidates int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM bindings WHERE pairing_id=$1 AND status='pending'", pairingID).Scan(&candidates); err != nil {
			return err
		}
		if candidates >= 32 {
			return ErrConflict
		}
		err = tx.QueryRow(ctx, "SELECT id,pairing_id FROM bindings WHERE gateway_id=$1 AND installation_id=$2 AND status<>'revoked' FOR UPDATE", b.GatewayID, mobile.ID).Scan(&b.ID, new(*string))
		if err == nil {
			var matches bool
			if err = tx.QueryRow(ctx, "SELECT pairing_id=$2 AND status='pending' FROM bindings WHERE id=$1", b.ID, pairingID).Scan(&matches); err != nil {
				return err
			}
			if !matches {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		b.ID, err = security.ID()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO bindings(id,gateway_id,installation_id,pairing_id) VALUES($1,$2,$3,$4)", b.ID, b.GatewayID, mobile.ID, pairingID)
		return err
	})
	if err != nil {
		return b, err
	}
	return s.Binding(ctx, b.ID, mobile, true)
}

func (s *Store) Binding(ctx context.Context, id string, d Device, allowPending bool) (Binding, error) {
	var b Binding
	err := s.Pool.QueryRow(ctx, `SELECT b.id,b.gateway_id,b.installation_id,b.status,g.control_public_key,i.control_public_key,
		e.revision,COALESCE(e.enabled AND e.consent,false)
		FROM bindings b JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
		JOIN sessions gs ON gs.id=g.session_id JOIN sessions ms ON ms.id=i.session_id
		LEFT JOIN pairings p ON p.id=b.pairing_id LEFT JOIN push_endpoints e ON e.binding_id=b.id
		WHERE b.id=$1 AND g.account_id=$2 AND i.account_id=$2 AND g.revoked_at IS NULL AND i.revoked_at IS NULL
		AND gs.revoked_at IS NULL AND ms.revoked_at IS NULL AND gs.refresh_expires_at>now() AND ms.refresh_expires_at>now()
		AND (b.status='active' OR ($3 AND b.status='pending' AND p.expires_at>now() AND p.consumed_at IS NULL))`, id, d.AccountID, allowPending).Scan(
		&b.ID, &b.GatewayID, &b.InstallationID, &b.Status, &b.GatewayPublicKey, &b.MobilePublicKey, &b.EndpointRevision, &b.PushEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	if err == nil && ((d.Role == "gateway" && b.GatewayID != d.ID) || (d.Role == "mobile" && b.InstallationID != d.ID)) {
		err = ErrDenied
	}
	return b, err
}

func (s *Store) ApproveBinding(ctx context.Context, id string, d Device, mobileKey []byte) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var bindingStatus string
		var key []byte
		var pairingID string
		var pairingValid bool
		err := tx.QueryRow(ctx, `SELECT b.status,i.control_public_key,p.id,p.expires_at>now() AND p.consumed_at IS NULL
			FROM bindings b JOIN installations i ON i.id=b.installation_id JOIN gateways g ON g.id=b.gateway_id
			JOIN sessions ms ON ms.id=i.session_id JOIN sessions gs ON gs.id=g.session_id
			JOIN pairings p ON p.id=b.pairing_id WHERE b.id=$1 AND b.gateway_id=$2 AND i.account_id=$3 AND g.account_id=$3
			AND g.revoked_at IS NULL AND i.revoked_at IS NULL AND ms.revoked_at IS NULL AND gs.revoked_at IS NULL
			AND ms.refresh_expires_at>now() AND gs.refresh_expires_at>now() FOR UPDATE OF b,p`, id, d.ID, d.AccountID).Scan(&bindingStatus, &key, &pairingID, &pairingValid)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(key, mobileKey) != 1 {
			return ErrDenied
		}
		if bindingStatus == "active" {
			return nil
		}
		if bindingStatus != "pending" || !pairingValid {
			return ErrDenied
		}
		if _, err = tx.Exec(ctx, "UPDATE bindings SET status='active',approved_at=now() WHERE id=$1", id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE pairings SET consumed_at=now() WHERE id=$1", pairingID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE bindings SET status='revoked',revoked_at=now() WHERE pairing_id=$1 AND status='pending'", pairingID)
		return err
	})
}

func (s *Store) RevokeBinding(ctx context.Context, id, accountID string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE bindings SET status='revoked',revoked_at=now() WHERE id=$1 AND gateway_id IN (SELECT id FROM gateways WHERE account_id=$2)", id, accountID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDenied
		}
		if _, err = tx.Exec(ctx, "UPDATE push_endpoints SET enabled=false,consent=false WHERE binding_id=$1", id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE push_jobs SET status='cancelled' WHERE binding_id=$1 AND status IN ('queued','leased')", id)
		return err
	})
}
