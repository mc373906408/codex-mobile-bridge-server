package store

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

type Envelope struct {
	Version        int    `json:"v"`
	NotificationID string `json:"notification_id"`
	BindingID      string `json:"binding_id"`
	ExpiresAt      int64  `json:"expires_at"`
	Ciphertext     string `json:"ciphertext"`
}

func (e Envelope) Validate(now time.Time) error {
	if e.Version != 1 {
		return ErrDenied
	}
	if _, err := security.IDBytes(e.NotificationID); err != nil {
		return ErrDenied
	}
	if _, err := security.IDBytes(e.BindingID); err != nil {
		return ErrDenied
	}
	if e.ExpiresAt <= now.Unix() || e.ExpiresAt > now.Add(time.Hour).Unix() {
		return ErrDenied
	}
	data, err := base64.StdEncoding.DecodeString(e.Ciphertext)
	if err != nil || len(data) < 48 || len(data) > 1072 {
		return ErrDenied
	}
	return nil
}

type EndpointInput struct {
	BindingID string `json:"binding_id"`
	Platform  string `json:"platform"`
	AppID     string `json:"app_id"`
	Token     string `json:"token"`
	Enabled   bool   `json:"enabled"`
	Consent   bool   `json:"consent"`
}

type Endpoint struct {
	ID         string `json:"id"`
	Revision   string `json:"revision"`
	Registered bool   `json:"registered"`
}

var umengToken = regexp.MustCompile(`^[A-Za-z0-9_-]{20,256}$`)
var apnsToken = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func (s *Store) RegisterEndpoint(ctx context.Context, d Device, in EndpointInput, c config.Config) (Endpoint, error) {
	var out Endpoint
	if _, err := s.Binding(ctx, in.BindingID, d, false); err != nil {
		return out, err
	}
	if !in.Enabled || !in.Consent {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "UPDATE push_endpoints SET enabled=false,consent=false WHERE binding_id=$1", in.BindingID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "UPDATE push_jobs SET status='cancelled' WHERE binding_id=$1 AND status IN ('queued','leased')", in.BindingID)
			return err
		})
		return out, err
	}
	if (in.Platform == "umeng" && (!c.UmengEnabled() || in.AppID != c.Umeng.AppKey || !umengToken.MatchString(in.Token))) ||
		(in.Platform == "apns" && (!c.APNSEnabled() || in.AppID != c.APNS.Topic || !apnsToken.MatchString(in.Token))) ||
		(in.Platform != "umeng" && in.Platform != "apns") {
		return out, ErrDenied
	}
	out.ID, _ = security.ID()
	out.Revision, _ = security.ID()
	out.Registered = true
	if out.ID == "" || out.Revision == "" {
		return out, errors.New("randomness unavailable")
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT a.verified AND NOT a.disabled AND (NOT $2 OR e.expires_at>clock_timestamp())
			FROM accounts a JOIN entitlements e ON e.account_id=a.id WHERE a.id=$1 FOR UPDATE OF a`, d.AccountID, c.Paid()).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return ErrDenied
		}
		var active bool
		if err := tx.QueryRow(ctx, "SELECT status='active' FROM bindings WHERE id=$1 AND installation_id=$2 FOR UPDATE", in.BindingID, d.ID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrDenied
		}
		var oldID, oldPlatform, oldApp, oldRevision string
		var oldCipher []byte
		var oldEnabled, oldConsent bool
		err := tx.QueryRow(ctx, "SELECT id,platform,app_id,revision,token_ciphertext,enabled,consent FROM push_endpoints WHERE binding_id=$1 FOR UPDATE", in.BindingID).Scan(&oldID, &oldPlatform, &oldApp, &oldRevision, &oldCipher, &oldEnabled, &oldConsent)
		if err == nil {
			out.ID = oldID
			oldToken, err := s.Vault.Open("push-token:"+oldID, oldCipher)
			if err != nil {
				return err
			}
			if oldEnabled && oldConsent && oldPlatform == in.Platform && oldApp == in.AppID && string(oldToken) == in.Token {
				out.Revision = oldRevision
				return nil
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		sealed, err := s.Vault.Seal("push-token:"+out.ID, []byte(in.Token))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO push_endpoints(id,binding_id,platform,app_id,token_ciphertext,revision,enabled,consent,direct_until)
			VALUES($1,$2,$3,$4,$5,$6,true,true,now()+interval '45 seconds') ON CONFLICT(binding_id) DO UPDATE SET
			platform=EXCLUDED.platform,app_id=EXCLUDED.app_id,token_ciphertext=EXCLUDED.token_ciphertext,revision=EXCLUDED.revision,
			enabled=true,consent=true,direct_until=EXCLUDED.direct_until`, out.ID, in.BindingID, in.Platform, in.AppID, sealed, out.Revision); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE push_jobs SET status='cancelled' WHERE binding_id=$1 AND status IN ('queued','leased')", in.BindingID)
		return err
	})
	return out, err
}

func (s *Store) QueuePush(ctx context.Context, d Device, e Envelope, revision string, paid bool) (string, error) {
	var id string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var eligible bool
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT a.verified AND NOT a.disabled AND (NOT $2 OR x.expires_at>clock_timestamp()),clock_timestamp()
			FROM accounts a JOIN entitlements x ON x.account_id=a.id WHERE a.id=$1 FOR UPDATE OF a`, d.AccountID, paid).Scan(&eligible, &now); err != nil {
			return err
		}
		if !eligible {
			return ErrDenied
		}
		if err := e.Validate(now); err != nil {
			return err
		}
		var endpointID, currentRevision string
		var directUntil time.Time
		err := tx.QueryRow(ctx, `SELECT p.id,p.revision,p.direct_until FROM push_endpoints p JOIN bindings b ON b.id=p.binding_id
			JOIN installations i ON i.id=b.installation_id JOIN sessions ms ON ms.id=i.session_id
			WHERE b.id=$1 AND b.gateway_id=$2 AND b.status='active' AND p.enabled AND p.consent AND i.revoked_at IS NULL
			AND ms.revoked_at IS NULL AND ms.refresh_expires_at>now() FOR UPDATE OF p`, e.BindingID, d.ID).Scan(&endpointID, &currentRevision, &directUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil {
			return err
		}
		if currentRevision != revision {
			return ErrConflict
		}
		var existing Envelope
		err = tx.QueryRow(ctx, "SELECT id,ciphertext,extract(epoch FROM expires_at)::bigint FROM push_jobs WHERE binding_id=$1 AND notification_id=$2", e.BindingID, e.NotificationID).Scan(&id, &existing.Ciphertext, &existing.ExpiresAt)
		if err == nil {
			if existing.Ciphertext != e.Ciphertext || existing.ExpiresAt != e.ExpiresAt {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var pending int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM push_jobs j JOIN bindings b ON b.id=j.binding_id
			WHERE b.gateway_id=$1 AND j.status IN ('queued','leased')`, d.ID).Scan(&pending); err != nil {
			return err
		}
		if pending >= 2048 {
			return ErrConflict
		}
		id, err = security.ID()
		if err != nil {
			return err
		}
		available := now
		if directUntil.After(available) {
			available = directUntil
		}
		_, err = tx.Exec(ctx, `INSERT INTO push_jobs(id,binding_id,notification_id,endpoint_id,endpoint_revision,ciphertext,expires_at,available_at,status)
			VALUES($1,$2,$3,$4,$5,$6,to_timestamp($7),$8,CASE WHEN EXISTS(SELECT 1 FROM push_acks WHERE binding_id=$2 AND notification_id=$3 AND expires_at>now()) THEN 'acked' ELSE 'queued' END)`,
			id, e.BindingID, e.NotificationID, endpointID, revision, e.Ciphertext, e.ExpiresAt, available)
		return err
	})
	return id, err
}

func (s *Store) Acknowledge(ctx context.Context, d Device, bindingID string, ids []string) error {
	if _, err := s.Binding(ctx, bindingID, d, false); err != nil {
		return err
	}
	if len(ids) > 100 {
		return ErrDenied
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE push_endpoints SET direct_until=now()+interval '45 seconds' WHERE binding_id=$1 AND enabled AND consent", bindingID); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := security.IDBytes(id); err != nil {
				return ErrDenied
			}
			if _, err := tx.Exec(ctx, `INSERT INTO push_acks(binding_id,notification_id,expires_at) VALUES($1,$2,now()+interval '1 hour')
				ON CONFLICT(binding_id,notification_id) DO NOTHING`, bindingID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE push_jobs SET status='acked' WHERE binding_id=$1 AND notification_id=$2 AND status IN ('queued','leased','accepted')", bindingID, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) CancelPush(ctx context.Context, d Device, bindingID string, ids []string) error {
	if _, err := s.Binding(ctx, bindingID, d, false); err != nil {
		return err
	}
	if len(ids) > 100 {
		return ErrDenied
	}
	if len(ids) == 0 {
		_, err := s.Pool.Exec(ctx, "UPDATE push_jobs SET status='cancelled' WHERE binding_id=$1 AND status IN ('queued','leased')", bindingID)
		return err
	}
	for _, id := range ids {
		if _, err := security.IDBytes(id); err != nil {
			return ErrDenied
		}
	}
	_, err := s.Pool.Exec(ctx, "UPDATE push_jobs SET status='cancelled' WHERE binding_id=$1 AND notification_id=ANY($2::uuid[]) AND status IN ('queued','leased')", bindingID, ids)
	return err
}
