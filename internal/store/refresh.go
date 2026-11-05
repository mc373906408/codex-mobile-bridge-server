package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

// The session row serializes first execution, duplicate delivery and theft detection.
func (s *Store) refreshRecoverable(ctx context.Context, token, requestID string, c config.Config) (Tokens, error) {
	var result Tokens
	if _, err := security.IDBytes(requestID); err != nil {
		return result, ErrDenied
	}
	digest := security.Digest(token)
	denied := false
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Serialize lookup as well as rotation: a concurrent snapshot must see the
		// first transaction's spent-token row before selecting the session.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", token); err != nil {
			return err
		}
		var sessionID, accountID string
		var current []byte
		var accessExpiry, dbNow time.Time
		err := tx.QueryRow(ctx, `SELECT s.id,s.account_id,s.refresh_digest,s.access_expires_at,clock_timestamp()
            FROM sessions s WHERE (s.refresh_digest=$1 OR s.id=(SELECT session_id FROM spent_refresh_tokens WHERE digest=$1))
            AND s.revoked_at IS NULL AND s.refresh_expires_at>now() FOR UPDATE`, digest).Scan(&sessionID, &accountID, &current, &accessExpiry, &dbNow)
		if errors.Is(err, pgx.ErrNoRows) {
			denied = true
			return nil
		}
		if err != nil {
			return err
		}
		account, err := scanAccount(tx.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=$1", accountID))
		if err != nil {
			return err
		}
		if account.Disabled || !account.Verified || account.CredentialsChangeRequired {
			denied = true
			return nil
		}
		if !bytes.Equal(current, digest) {
			var savedID string
			var encrypted, newDigest []byte
			var expires time.Time
			err = tx.QueryRow(ctx, `SELECT request_id,ciphertext,new_refresh_digest,expires_at FROM refresh_results WHERE old_digest=$1`, digest).Scan(&savedID, &encrypted, &newDigest, &expires)
			if err == nil && savedID == requestID && dbNow.Before(expires) {
				// A delayed response from an older rotation cannot replace newer credentials.
				if !bytes.Equal(current, newDigest) || !dbNow.Before(accessExpiry) {
					denied = true
					return nil
				}
				raw, e := s.Vault.Open("refresh:"+sessionID+":"+requestID, encrypted)
				if e != nil {
					return e
				}
				defer clear(raw)
				if e = json.Unmarshal(raw, &result); e != nil {
					return e
				}
				result.Account = account
				result.ExpiresIn = int64(accessExpiry.Sub(dbNow) / time.Second)
				return nil
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			denied = true
			if _, err = tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE id=$1", sessionID); err != nil {
				return err
			}
			return RecordAudit(ctx, tx, AuditEvent{AccountID: accountID, SessionID: sessionID, Action: "session.reuse", TargetID: sessionID,
				Before: map[string]any{"revoked": false}, After: map[string]any{"revoked": true}})
		}
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM refresh_results WHERE session_id=$1 AND request_id=$2)", sessionID, requestID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			denied = true
			return nil
		}
		result = Tokens{Account: account, ExpiresIn: int64(c.AccessTTL / time.Second)}
		if result.Access, err = security.Token(); err != nil {
			return err
		}
		if result.Refresh, err = security.Token(); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO spent_refresh_tokens(digest,session_id) VALUES($1,$2)", digest, sessionID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE sessions SET access_digest=$2,refresh_digest=$3,access_expires_at=clock_timestamp()+$4::interval WHERE id=$1`,
			sessionID, security.Digest(result.Access), security.Digest(result.Refresh), Interval(c.AccessTTL)); err != nil {
			return err
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		defer clear(raw)
		encrypted, err := s.Vault.Seal("refresh:"+sessionID+":"+requestID, raw)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO refresh_results(old_digest,session_id,request_id,new_refresh_digest,ciphertext) VALUES($1,$2,$3,$4,$5)`,
			digest, sessionID, requestID, security.Digest(result.Refresh), encrypted)
		return err
	})
	if err == nil && denied {
		err = ErrDenied
	}
	return result, err
}
