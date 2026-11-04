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

type Binding struct {
	ID               string  `json:"id"`
	GatewayID        string  `json:"gateway_id"`
	InstallationID   string  `json:"installation_id"`
	Status           string  `json:"status"`
	GatewayPublicKey []byte  `json:"gateway_public_key"`
	MobilePublicKey  []byte  `json:"mobile_public_key"`
	EndpointRevision *string `json:"endpoint_revision,omitempty"`
	PushEnabled      bool    `json:"push_enabled"`
	DeviceName       string  `json:"device_name,omitempty"`
	GatewayName      string  `json:"gateway_name,omitempty"`
	AccessRevision   int64   `json:"access_revision"`
	GatewayOnline    bool    `json:"gateway_online"`
	MobileOnline     bool    `json:"mobile_online"`
	DeviceRevoked    bool    `json:"device_revoked"`
}

func PublicKey(value string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) != 32 || subtle.ConstantTimeCompare(b, make([]byte, 32)) == 1 {
		return nil, ErrDenied
	}
	return b, nil
}

func (s *Store) Enroll(ctx context.Context, p Principal, role, id, name string, key []byte, max int) (Device, error) {
	if role != "gateway" && role != "mobile" {
		return Device{}, ErrDenied
	}
	if role == "mobile" && name == "" {
		name = "Phone"
	}
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
			if role == "mobile" {
				_, err = tx.Exec(ctx, "UPDATE installations SET token_digest=$2,session_id=$3,name=$4,last_seen_at=clock_timestamp() WHERE id=$1", d.ID, security.Digest(d.Token), p.SessionID, name)
			} else {
				_, err = tx.Exec(ctx, "UPDATE gateways SET token_digest=$2,session_id=$3,name=$4 WHERE id=$1", d.ID, security.Digest(d.Token), p.SessionID, name)
			}
			if err != nil {
				return err
			}
			if err = RecordAudit(ctx, tx, deviceAudit(d, "device.reenroll", d.ID, nil, map[string]any{"role": role})); err != nil {
				return err
			}
			return discoverDevices(ctx, tx, d)
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
			_, err = tx.Exec(ctx, "INSERT INTO installations(id,account_id,session_id,control_public_key,token_digest,name,last_seen_at) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp())", d.ID, p.ID, p.SessionID, key, security.Digest(d.Token), name)
		}
		if err != nil {
			return err
		}
		if err = RecordAudit(ctx, tx, deviceAudit(d, "device.enroll", d.ID, nil, map[string]any{"role": role, "revoked": false})); err != nil {
			return err
		}
		return discoverDevices(ctx, tx, d)
	})
	return d, err
}

func (s *Store) Device(ctx context.Context, token, role string) (Device, error) {
	d := Device{Role: role}
	table := "gateways"
	name := "d.name"
	if role == "mobile" {
		table = "installations"
		name = "d.name"
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

func (s *Store) Binding(ctx context.Context, id string, d Device, allowPending bool) (Binding, error) {
	var b Binding
	err := s.Pool.QueryRow(ctx, `SELECT b.id,b.gateway_id,b.installation_id,b.status,g.control_public_key,i.control_public_key,
		e.revision,COALESCE(e.enabled AND e.consent,false),i.name,g.name,b.access_revision
		FROM bindings b JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
		JOIN sessions gs ON gs.id=g.session_id JOIN sessions ms ON ms.id=i.session_id
		LEFT JOIN push_endpoints e ON e.binding_id=b.id
		WHERE b.id=$1 AND g.account_id=$2 AND i.account_id=$2 AND g.revoked_at IS NULL AND i.revoked_at IS NULL
		AND gs.revoked_at IS NULL AND ms.revoked_at IS NULL AND gs.refresh_expires_at>now() AND ms.refresh_expires_at>now()
		AND (b.status='active' OR ($3 AND b.status='pending'))`, id, d.AccountID, allowPending).Scan(
		&b.ID, &b.GatewayID, &b.InstallationID, &b.Status, &b.GatewayPublicKey, &b.MobilePublicKey, &b.EndpointRevision, &b.PushEnabled, &b.DeviceName, &b.GatewayName, &b.AccessRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	if err == nil && ((d.Role == "gateway" && b.GatewayID != d.ID) || (d.Role == "mobile" && b.InstallationID != d.ID)) {
		err = ErrDenied
	}
	return b, err
}

func (s *Store) RevokeBinding(ctx context.Context, id, accountID string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return revokeBindingTx(ctx, tx, Principal{Account: Account{ID: accountID}}, accountID, id, "explicit")
	})
}
