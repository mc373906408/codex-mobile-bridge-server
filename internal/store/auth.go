package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

type Account struct {
	ID                        string    `json:"id"`
	Login                     string    `json:"login"`
	Verified                  bool      `json:"verified"`
	Administrator             bool      `json:"administrator"`
	Disabled                  bool      `json:"disabled"`
	CredentialsChangeRequired bool      `json:"credentials_change_required"`
	CreatedAt                 time.Time `json:"created_at"`
	PasswordHash              string    `json:"-"`
}

type Principal struct {
	Account
	SessionID string
	CSRF      string
}

type Tokens struct {
	Access    string  `json:"access_token"`
	Refresh   string  `json:"refresh_token"`
	ExpiresIn int64   `json:"expires_in"`
	Account   Account `json:"account"`
	Web       string  `json:"-"`
	CSRF      string  `json:"-"`
}

type MailMessage struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Login, &a.PasswordHash, &a.Verified, &a.Administrator, &a.Disabled, &a.CreatedAt, &a.CredentialsChangeRequired)
	return a, err
}

const accountColumns = "id, login, password_hash, verified, administrator, disabled, created_at, credentials_change_required"

func (s *Store) AccountByLogin(ctx context.Context, login string) (Account, error) {
	return scanAccount(s.Pool.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE login=$1", login))
}

func (s *Store) Bootstrap(ctx context.Context, login, password string) error {
	if login == "" {
		return nil
	}
	var hash string
	var err error
	if login == "admin" && password == "admin" {
		hash, err = security.DefaultAdministratorPassword()
	} else {
		hash, err = security.Password(password)
	}
	if err != nil {
		return err
	}
	id, err := security.ID()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(607843093)"); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM accounts WHERE administrator)").Scan(&exists); err != nil {
			return err
		}
		// Changing the login or deployment settings must never recreate default access.
		if exists {
			return nil
		}
		if _, err = tx.Exec(ctx, "INSERT INTO accounts(id,login,password_hash,verified,administrator,credentials_change_required) VALUES($1,$2,$3,true,true,true)", id, login, hash); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO entitlements(account_id) VALUES($1)", id)
		return err
	})
}

func (s *Store) CreateAccount(ctx context.Context, login, hash string, verified bool, c config.Config, actor string) (Account, error) {
	id, err := security.ID()
	if err != nil {
		return Account{}, err
	}
	var a Account
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "INSERT INTO accounts(id,login,password_hash,verified) VALUES($1,$2,$3,$4) ON CONFLICT(login) DO NOTHING RETURNING "+accountColumns, id, login, hash, verified)
		a, err = scanAccount(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO entitlements(account_id) VALUES($1)", id); err != nil {
			return err
		}
		if actor != "" {
			return Audit(ctx, tx, actor, "account.create", id)
		}
		return s.queueMail(ctx, tx, a, "verify", c)
	})
	return a, err
}

func (s *Store) queueMail(ctx context.Context, tx pgx.Tx, a Account, purpose string, c config.Config) error {
	if !c.MailEnabled() {
		return nil
	}
	token, err := security.Token()
	if err != nil {
		return err
	}
	id, err := security.ID()
	if err != nil {
		return err
	}
	ttl := time.Hour
	subject := "Verify your Bridge account"
	if purpose == "reset" {
		ttl = 30 * time.Minute
		subject = "Reset your Bridge password"
	}
	// Only this encrypted outbox contains the bearer link; token rows contain digests.
	message := MailMessage{To: a.Login, Subject: subject,
		Body: c.Origin + "/" + purpose + "#token=" + token + "\r\n\r\nThis link expires. If you did not request it, ignore this message.\r\n"}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	sealed, err := s.Vault.Seal("mail:"+id, data)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE email_tokens SET used_at=now() WHERE account_id=$1 AND purpose=$2 AND used_at IS NULL", a.ID, purpose); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO email_tokens(digest,account_id,purpose,expires_at) VALUES($1,$2,$3,now()+$4::interval)", security.Digest(token), a.ID, purpose, Interval(ttl)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE mail_outbox SET status='cancelled' WHERE account_id=$1 AND purpose=$2 AND status IN ('queued','leased')", a.ID, purpose); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO mail_outbox(id,account_id,encrypted_message,purpose,expires_at) VALUES($1,$2,$3,$4,now()+$5::interval)", id, a.ID, sealed, purpose, Interval(ttl))
	return err
}

func (s *Store) RequestMail(ctx context.Context, login, purpose string, c config.Config) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		a, err := scanAccount(tx.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE login=$1 FOR UPDATE", login))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.Disabled || a.CredentialsChangeRequired || (purpose == "verify" && a.Verified) {
			return nil
		}
		return s.queueMail(ctx, tx, a, purpose, c)
	})
}

func (s *Store) ConsumeMailToken(ctx context.Context, token, purpose, passwordHash string) (string, error) {
	var accountID string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT account_id FROM email_tokens WHERE digest=$1 AND purpose=$2", security.Digest(token), purpose).Scan(&accountID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDenied
			}
			return err
		}
		var disabled bool
		if err := tx.QueryRow(ctx, "SELECT disabled FROM accounts WHERE id=$1 FOR UPDATE", accountID).Scan(&disabled); err != nil {
			return err
		}
		if disabled {
			return ErrDenied
		}
		tag, err := tx.Exec(ctx, "UPDATE email_tokens SET used_at=now() WHERE digest=$1 AND purpose=$2 AND used_at IS NULL AND expires_at>now()", security.Digest(token), purpose)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDenied
		}
		if purpose == "verify" {
			_, err = tx.Exec(ctx, "UPDATE accounts SET verified=true WHERE id=$1", accountID)
		} else {
			_, err = tx.Exec(ctx, "UPDATE accounts SET password_hash=$2 WHERE id=$1", accountID, passwordHash)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL", accountID)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE email_tokens SET used_at=now() WHERE account_id=$1 AND purpose=$2 AND used_at IS NULL", accountID, purpose)
		return err
	})
	return accountID, err
}

func (s *Store) NewSession(ctx context.Context, a Account, c config.Config, web bool) (Tokens, error) {
	if a.CredentialsChangeRequired || (web && !a.Administrator) {
		return Tokens{}, ErrDenied
	}
	t := Tokens{ExpiresIn: int64(c.AccessTTL.Seconds()), Account: a}
	id, err := security.ID()
	if err != nil {
		return t, err
	}
	for _, out := range []*string{&t.Access, &t.Refresh, &t.CSRF} {
		*out, err = security.Token()
		if err != nil {
			return t, err
		}
	}
	var webDigest any
	var webExpires any
	if web {
		t.Web, err = security.Token()
		if err != nil {
			return t, err
		}
		webDigest = security.Digest(t.Web)
		webExpires = time.Now().UTC().Add(12 * time.Hour)
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, "SELECT verified AND NOT disabled AND NOT credentials_change_required AND password_hash=$2 AND (NOT $3::boolean OR administrator) FROM accounts WHERE id=$1 FOR UPDATE", a.ID, a.PasswordHash, web).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return ErrDenied
		}
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE account_id=$1 AND revoked_at IS NULL AND refresh_expires_at>now()", a.ID).Scan(&count); err != nil {
			return err
		}
		if count >= 32 {
			return ErrConflict
		}
		_, err := tx.Exec(ctx, `INSERT INTO sessions(id,account_id,access_digest,refresh_digest,web_digest,csrf,access_expires_at,refresh_expires_at,web_expires_at)
			VALUES($1,$2,$3,$4,$5,$6,now()+$7::interval,now()+$8::interval,$9)`,
			id, a.ID, security.Digest(t.Access), security.Digest(t.Refresh), webDigest, t.CSRF, Interval(c.AccessTTL), Interval(c.RefreshTTL), webExpires)
		return err
	})
	return t, err
}

func (s *Store) Authenticate(ctx context.Context, token string, web bool) (Principal, error) {
	var p Principal
	column, expiry := "access_digest", "access_expires_at"
	if web {
		column, expiry = "web_digest", "web_expires_at"
	}
	err := s.Pool.QueryRow(ctx, `SELECT a.id,a.login,a.password_hash,a.verified,a.administrator,a.disabled,a.created_at,s.id,s.csrf
		FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE s.`+column+`=$1 AND s.`+expiry+`>now()
		AND s.revoked_at IS NULL AND s.refresh_expires_at>now() AND a.verified AND NOT a.disabled AND NOT a.credentials_change_required
		AND (NOT $2::boolean OR a.administrator)`, security.Digest(token), web).Scan(
		&p.ID, &p.Login, &p.PasswordHash, &p.Verified, &p.Administrator, &p.Disabled, &p.CreatedAt, &p.SessionID, &p.CSRF)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return p, err
}

func (s *Store) Refresh(ctx context.Context, token string, c config.Config) (Tokens, error) {
	t := Tokens{ExpiresIn: int64(c.AccessTTL.Seconds())}
	var err error
	for _, out := range []*string{&t.Access, &t.Refresh} {
		*out, err = security.Token()
		if err != nil {
			return t, err
		}
	}
	denied := false
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var id, accountID string
		err := tx.QueryRow(ctx, "SELECT id,account_id FROM sessions WHERE refresh_digest=$1 AND revoked_at IS NULL AND refresh_expires_at>now() FOR UPDATE", security.Digest(token)).Scan(&id, &accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			denied = true
			_, err = tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE id IN (SELECT session_id FROM spent_refresh_tokens WHERE digest=$1)", security.Digest(token))
			return err
		}
		if err != nil {
			return err
		}
		t.Account, err = scanAccount(tx.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=$1", accountID))
		if err != nil {
			return err
		}
		if t.Account.Disabled || !t.Account.Verified || t.Account.CredentialsChangeRequired {
			denied = true
			return nil
		}
		if _, err = tx.Exec(ctx, "INSERT INTO spent_refresh_tokens(digest,session_id) VALUES($1,$2)", security.Digest(token), id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE sessions SET access_digest=$2,refresh_digest=$3,access_expires_at=now()+$4::interval WHERE id=$1", id, security.Digest(t.Access), security.Digest(t.Refresh), Interval(c.AccessTTL))
		return err
	})
	if err == nil && denied {
		err = ErrDenied
	}
	return t, err
}

func (s *Store) RateLimit(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	var count int
	err := s.Pool.QueryRow(ctx, `INSERT INTO rate_limits(key,window_start,attempts) VALUES($1,now(),1)
		ON CONFLICT(key) DO UPDATE SET
		attempts=CASE WHEN rate_limits.window_start<now()-$2::interval THEN 1 ELSE rate_limits.attempts+1 END,
		window_start=CASE WHEN rate_limits.window_start<now()-$2::interval THEN now() ELSE rate_limits.window_start END
		RETURNING attempts`, security.Digest(key), Interval(window)).Scan(&count)
	return count <= limit, err
}
