package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

type AdministratorSetup struct {
	Token        string `json:"setup_token"`
	AccountID    string `json:"-"`
	Login        string `json:"login"`
	CSRF         string `json:"-"`
	ExpiresIn    int64  `json:"expires_in"`
	PasswordHash string `json:"-"`
}

func (s *Store) NewAdministratorSetup(ctx context.Context, account Account) (AdministratorSetup, error) {
	setup := AdministratorSetup{AccountID: account.ID, Login: account.Login, ExpiresIn: 600}
	var err error
	setup.Token, err = security.Token()
	if err != nil {
		return setup, err
	}
	setup.CSRF, err = security.Token()
	if err != nil {
		return setup, err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(607843093)"); err != nil {
			return err
		}
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT administrator AND verified AND NOT disabled AND credentials_change_required
			AND password_hash=$2 FROM accounts WHERE id=$1 FOR UPDATE`, account.ID, account.PasswordHash).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return ErrDenied
		}
		if _, err := tx.Exec(ctx, "DELETE FROM administrator_setup_sessions WHERE account_id=$1 AND expires_at<=now()", account.ID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM administrator_setup_sessions WHERE account_id=$1", account.ID).Scan(&count); err != nil {
			return err
		}
		if count >= 8 {
			return ErrConflict
		}
		_, err := tx.Exec(ctx, `INSERT INTO administrator_setup_sessions(digest,account_id,password_hash,csrf,expires_at)
			VALUES($1,$2,$3,$4,now()+interval '10 minutes')`, security.Digest(setup.Token), account.ID, account.PasswordHash, setup.CSRF)
		return err
	})
	return setup, err
}

func (s *Store) AdministratorSetup(ctx context.Context, token string) (AdministratorSetup, error) {
	var setup AdministratorSetup
	err := s.Pool.QueryRow(ctx, `SELECT s.account_id,a.login,s.csrf,a.password_hash FROM administrator_setup_sessions s
		JOIN accounts a ON a.id=s.account_id WHERE s.digest=$1 AND s.expires_at>now()
		AND a.administrator AND a.verified AND NOT a.disabled AND a.credentials_change_required
		AND a.password_hash=s.password_hash`, security.Digest(token)).Scan(&setup.AccountID, &setup.Login, &setup.CSRF, &setup.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return setup, err
}

func (s *Store) FinishAdministratorSetup(ctx context.Context, token, login, passwordHash string) (string, error) {
	var accountID string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(607843093)"); err != nil {
			return err
		}
		var previousHash string
		err := tx.QueryRow(ctx, `SELECT account_id,password_hash FROM administrator_setup_sessions
			WHERE digest=$1 AND expires_at>now() FOR UPDATE`, security.Digest(token)).Scan(&accountID, &previousHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil {
			return err
		}
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT administrator AND verified AND NOT disabled AND credentials_change_required
			AND password_hash=$2 FROM accounts WHERE id=$1 FOR UPDATE`, accountID, previousHash).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return ErrDenied
		}
		if _, err := tx.Exec(ctx, "UPDATE accounts SET login=$2,password_hash=$3,credentials_change_required=false WHERE id=$1", accountID, login, passwordHash); err != nil {
			return err
		}
		if err := invalidateAdministratorCredentials(ctx, tx, accountID); err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: accountID, AccountID: accountID, Action: "admin.credentials_change", TargetID: accountID,
			Before: map[string]any{"credentials_change_required": true}, After: map[string]any{"credentials_change_required": false}})
	})
	var conflict *pgconn.PgError
	if errors.As(err, &conflict) && conflict.Code == "23505" {
		err = ErrConflict
	}
	return accountID, err
}

// This operation is only exposed by the local server CLI, never an HTTP route.
func (s *Store) ResetAdministrator(ctx context.Context) error {
	hash, err := security.DefaultAdministratorPassword()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(607843093)"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT id FROM accounts WHERE administrator ORDER BY id LIMIT 2 FOR UPDATE")
		if err != nil {
			return err
		}
		ids := make([]string, 0, 2)
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
		if len(ids) != 1 {
			return ErrConflict
		}
		id := ids[0]
		if _, err = tx.Exec(ctx, "UPDATE accounts SET login='admin',password_hash=$2,verified=true,disabled=false,credentials_change_required=true WHERE id=$1", id, hash); err != nil {
			return err
		}
		if err = invalidateAdministratorCredentials(ctx, tx, id); err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{AccountID: id, Action: "admin.bootstrap_reset", TargetID: id,
			After: map[string]any{"credentials_change_required": true, "disabled": false}})
	})
}

func invalidateAdministratorCredentials(ctx context.Context, tx pgx.Tx, accountID string) error {
	if err := RevokeAccountSessions(ctx, tx, Principal{}, accountID, "administrator_credentials_change"); err != nil {
		return err
	}
	for _, query := range []string{
		"DELETE FROM administrator_setup_sessions WHERE account_id=$1",
		"UPDATE email_tokens SET used_at=now() WHERE account_id=$1 AND used_at IS NULL",
		"UPDATE mail_outbox SET status='cancelled' WHERE account_id=$1 AND status IN ('queued','leased')",
	} {
		if _, err := tx.Exec(ctx, query, accountID); err != nil {
			return err
		}
	}
	return nil
}
