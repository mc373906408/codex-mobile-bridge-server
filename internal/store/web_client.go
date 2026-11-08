package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

func (s *Store) AuthenticateWebClient(ctx context.Context, token string) (Principal, error) {
	p := Principal{Web: true}
	err := s.Pool.QueryRow(ctx, `SELECT a.id,a.login,a.verified,a.administrator,a.disabled,a.created_at,s.id,s.csrf
		FROM sessions s JOIN accounts a ON a.id=s.account_id
		WHERE s.web_digest=$1 AND s.purpose='web_client' AND s.web_expires_at>now()
		AND s.refresh_expires_at>now() AND s.revoked_at IS NULL
		AND a.verified AND NOT a.disabled AND NOT a.credentials_change_required`, security.Digest(token)).Scan(
		&p.ID, &p.Login, &p.Verified, &p.Administrator, &p.Disabled, &p.CreatedAt, &p.SessionID, &p.CSRF)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return p, err
}

func (s *Store) WebClientDeadline(ctx context.Context, session, account string) (time.Time, error) {
	var deadline time.Time
	err := s.Pool.QueryRow(ctx, `SELECT LEAST(s.web_expires_at,s.refresh_expires_at)
		FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE s.id=$1 AND s.account_id=$2
		AND s.purpose='web_client' AND s.revoked_at IS NULL AND s.web_expires_at>now()
		AND s.refresh_expires_at>now() AND a.verified AND NOT a.disabled AND NOT a.credentials_change_required`, session, account).Scan(&deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return deadline, err
}
