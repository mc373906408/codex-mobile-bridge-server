package store

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

type Entitlement struct {
	Required   bool       `json:"required"`
	Active     bool       `json:"active"`
	ExpiresAt  *time.Time `json:"expires_at"`
	ServerTime time.Time  `json:"server_time"`
}

type Redemption struct {
	ID        string    `json:"id"`
	Days      int       `json:"days"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

var ErrBatchRedeemed = errors.New("account already redeemed this batch")

func (s *Store) Entitlement(ctx context.Context, accountID string, paid bool) (Entitlement, error) {
	e := Entitlement{Required: paid}
	var eligible bool
	err := s.Pool.QueryRow(ctx, `SELECT e.expires_at,clock_timestamp(),a.verified AND NOT a.disabled
		FROM accounts a JOIN entitlements e ON e.account_id=a.id WHERE a.id=$1`, accountID).Scan(&e.ExpiresAt, &e.ServerTime, &eligible)
	e.Active = eligible && (!paid || (e.ExpiresAt != nil && e.ExpiresAt.After(e.ServerTime)))
	return e, err
}

func CodeDigest(code string) []byte {
	return security.Digest(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", "")))
}

func (s *Store) Redeem(ctx context.Context, accountID, code, requestID string) (Redemption, error) {
	var result Redemption
	digest := CodeDigest(code)
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, "SELECT verified AND NOT disabled FROM accounts WHERE id=$1 FOR UPDATE", accountID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return ErrDenied
		}
		var previous []byte
		err := tx.QueryRow(ctx, "SELECT code_digest,redemption_id FROM redeem_requests WHERE account_id=$1 AND request_id=$2", accountID, requestID).Scan(&previous, &result.ID)
		if err == nil {
			if subtle.ConstantTimeCompare(previous, digest) != 1 {
				return ErrConflict
			}
			return scanRedemption(ctx, tx, result.ID, &result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var codeID, batchID string
		var usedBy *string
		var usedAt, revokedAt *time.Time
		if err := tx.QueryRow(ctx, "SELECT id,batch_id,days,used_by,used_at,revoked_at FROM redemption_codes WHERE digest=$1 FOR UPDATE", digest).Scan(&codeID, &batchID, &result.Days, &usedBy, &usedAt, &revokedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDenied
			}
			return err
		}
		if usedAt != nil {
			if usedBy == nil || *usedBy != accountID {
				return ErrDenied
			}
			if err := tx.QueryRow(ctx, "SELECT id FROM redemptions WHERE code_id=$1 AND account_id=$2", codeID, accountID).Scan(&result.ID); err != nil {
				return err
			}
			if err := scanRedemption(ctx, tx, result.ID, &result); err != nil {
				return err
			}
		} else {
			if revokedAt != nil {
				return ErrDenied
			}
			var claimed bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM redemptions WHERE account_id=$1 AND batch_id=$2)", accountID, batchID).Scan(&claimed); err != nil {
				return err
			}
			if claimed {
				return ErrBatchRedeemed
			}
			var current *time.Time
			var now time.Time
			if err := tx.QueryRow(ctx, "SELECT expires_at,clock_timestamp() FROM entitlements WHERE account_id=$1 FOR UPDATE", accountID).Scan(&current, &now); err != nil {
				return err
			}
			base := now
			if current != nil && current.After(base) {
				base = *current
			}
			result.ExpiresAt = base.Add(time.Duration(result.Days) * 24 * time.Hour)
			result.ID, err = security.ID()
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "UPDATE entitlements SET expires_at=$2 WHERE account_id=$1", accountID, result.ExpiresAt); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "UPDATE redemption_codes SET used_by=$2,used_at=now() WHERE id=$1", codeID, accountID); err != nil {
				return err
			}
			if err = tx.QueryRow(ctx, `INSERT INTO redemptions(id,account_id,code_id,batch_id,days,expires_at)
				VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(account_id,batch_id) DO NOTHING RETURNING created_at`,
				result.ID, accountID, codeID, batchID, result.Days, result.ExpiresAt).Scan(&result.CreatedAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrBatchRedeemed // The whole transaction rolls back, including the code and entitlement.
				}
				return err
			}
			if err = RecordAudit(ctx, tx, AuditEvent{ActorID: accountID, AccountID: accountID, Action: "entitlement.redeem", TargetID: result.ID,
				Before: map[string]any{"expires_at": current}, After: map[string]any{"expires_at": result.ExpiresAt, "days": result.Days, "batch_id": batchID}}); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "INSERT INTO redeem_requests(account_id,request_id,code_digest,redemption_id) VALUES($1,$2,$3,$4)", accountID, requestID, digest, result.ID)
		return err
	})
	return result, err
}

func scanRedemption(ctx context.Context, tx pgx.Tx, id string, out *Redemption) error {
	return tx.QueryRow(ctx, "SELECT id,days,expires_at,created_at FROM redemptions WHERE id=$1", id).Scan(&out.ID, &out.Days, &out.ExpiresAt, &out.CreatedAt)
}

type GeneratedCode struct {
	ID      string `json:"id"`
	BatchID string `json:"batch_id"`
	Code    string `json:"code"`
	Days    int    `json:"days"`
}

func (s *Store) GenerateCodes(ctx context.Context, actor string, days, count int) ([]GeneratedCode, error) {
	if days < 1 || days > 3650 || count < 1 || count > 100 {
		return nil, ErrDenied
	}
	result := make([]GeneratedCode, 0, count)
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		batch, err := security.ID()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO redemption_batches(id) VALUES($1)", batch); err != nil {
			return err
		}
		for i := 0; i < count; i++ {
			id, err := security.ID()
			if err != nil {
				return err
			}
			code, err := security.NewRedemptionCode()
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "INSERT INTO redemption_codes(id,batch_id,digest,days) VALUES($1,$2,$3,$4)", id, batch, CodeDigest(code), days); err != nil {
				return err
			}
			result = append(result, GeneratedCode{ID: id, BatchID: batch, Code: code, Days: days})
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: actor, AccountID: actor, Action: "codes.generate", TargetID: batch,
			After: map[string]any{"count": count, "days": days}})
	})
	return result, err
}
