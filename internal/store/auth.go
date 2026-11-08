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
	Web       bool
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
		if _, err = tx.Exec(ctx, "INSERT INTO entitlements(account_id) VALUES($1)", id); err != nil {
			return err
		}
		return Audit(ctx, tx, id, "admin.bootstrap", id)
	})
}

func (s *Store) CreateAccount(ctx context.Context, login, hash, actor string) (Account, error) {
	id, err := security.ID()
	if err != nil {
		return Account{}, err
	}
	var a Account
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "INSERT INTO accounts(id,login,password_hash,verified) VALUES($1,$2,$3,true) ON CONFLICT(login) DO NOTHING RETURNING "+accountColumns, id, login, hash)
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
			return RecordAudit(ctx, tx, AuditEvent{ActorID: actor, AccountID: id, Action: "account.create", TargetID: id,
				After: map[string]any{"verified": true, "disabled": false}})
		}
		return nil
	})
	return a, err
}

func (s *Store) queuePasswordReset(ctx context.Context, tx pgx.Tx, a Account, c config.Config) error {
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
	ttl := 30 * time.Minute
	// Only this encrypted outbox contains the bearer link; token rows contain digests.
	message := MailMessage{To: a.Login, Subject: "Reset your Bridge password",
		Body: c.Origin + "/reset#token=" + token + "\r\n\r\nThis link expires. If you did not request it, ignore this message.\r\n"}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	sealed, err := s.Vault.Seal("mail:"+id, data)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE email_tokens SET used_at=now() WHERE account_id=$1 AND purpose='reset' AND used_at IS NULL", a.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO email_tokens(digest,account_id,purpose,expires_at) VALUES($1,$2,'reset',now()+$3::interval)", security.Digest(token), a.ID, Interval(ttl)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE mail_outbox SET status='cancelled' WHERE account_id=$1 AND purpose='reset' AND status IN ('queued','leased')", a.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO mail_outbox(id,account_id,encrypted_message,purpose,expires_at) VALUES($1,$2,$3,'reset',now()+$4::interval)", id, a.ID, sealed, Interval(ttl))
	return err
}

func (s *Store) RequestPasswordReset(ctx context.Context, login string, c config.Config) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		a, err := scanAccount(tx.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE login=$1 FOR UPDATE", login))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.Disabled || a.CredentialsChangeRequired || !a.Verified {
			return nil
		}
		return s.queuePasswordReset(ctx, tx, a, c)
	})
}

func (s *Store) ConsumePasswordResetToken(ctx context.Context, token, passwordHash string) (string, error) {
	var accountID string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT account_id FROM email_tokens WHERE digest=$1 AND purpose='reset'", security.Digest(token)).Scan(&accountID); err != nil {
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
		tag, err := tx.Exec(ctx, "UPDATE email_tokens SET used_at=now() WHERE digest=$1 AND purpose='reset' AND used_at IS NULL AND expires_at>now()", security.Digest(token))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDenied
		}
		_, err = tx.Exec(ctx, "UPDATE accounts SET password_hash=$2 WHERE id=$1", accountID, passwordHash)
		if err != nil {
			return err
		}
		if err = RevokeAccountSessions(ctx, tx, Principal{Account: Account{ID: accountID}}, accountID, "password_reset"); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE email_tokens SET used_at=now() WHERE account_id=$1 AND purpose='reset' AND used_at IS NULL", accountID); err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: accountID, AccountID: accountID, Action: "account.password_reset", TargetID: accountID,
			After: map[string]any{"sessions_revoked": true}})
	})
	return accountID, err
}

func (s *Store) NewSession(ctx context.Context, a Account, c config.Config, web bool) (Tokens, error) {
	purpose := "native"
	if web {
		purpose = "admin"
	}
	return s.newSession(ctx, a, c, purpose)
}

func (s *Store) NewWebClientSession(ctx context.Context, a Account, c config.Config) (Tokens, error) {
	return s.newSession(ctx, a, c, "web_client")
}

func (s *Store) newSession(ctx context.Context, a Account, c config.Config, purpose string) (Tokens, error) {
	web, admin := purpose != "native", purpose == "admin"
	refreshTTL := c.RefreshTTL
	if purpose == "web_client" && refreshTTL > 12*time.Hour {
		refreshTTL = 12 * time.Hour
	}
	if a.CredentialsChangeRequired || (admin && !a.Administrator) {
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
		if err := tx.QueryRow(ctx, "SELECT verified AND NOT disabled AND NOT credentials_change_required AND password_hash=$2 AND (NOT $3::boolean OR administrator) FROM accounts WHERE id=$1 FOR UPDATE", a.ID, a.PasswordHash, admin).Scan(&enabled); err != nil {
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
		_, err := tx.Exec(ctx, `INSERT INTO sessions(id,account_id,access_digest,refresh_digest,web_digest,csrf,access_expires_at,refresh_expires_at,web_expires_at,purpose)
			VALUES($1,$2,$3,$4,$5,$6,now()+$7::interval,now()+$8::interval,$9,$10)`,
			id, a.ID, security.Digest(t.Access), security.Digest(t.Refresh), webDigest, t.CSRF, Interval(c.AccessTTL), Interval(refreshTTL), webExpires, purpose)
		if err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: a.ID, AccountID: a.ID, SessionID: id, Action: "session.create", TargetID: id,
			After: map[string]any{"web": web, "purpose": purpose, "revoked": false}})
	})
	return t, err
}

func (s *Store) Authenticate(ctx context.Context, token string, web bool) (Principal, error) {
	p := Principal{Web: web}
	column, expiry := "access_digest", "access_expires_at"
	if web {
		column, expiry = "web_digest", "web_expires_at"
	}
	err := s.Pool.QueryRow(ctx, `SELECT a.id,a.login,a.password_hash,a.verified,a.administrator,a.disabled,a.created_at,s.id,s.csrf
		FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE s.`+column+`=$1 AND s.`+expiry+`>now()
		AND s.revoked_at IS NULL AND s.refresh_expires_at>now() AND a.verified AND NOT a.disabled AND NOT a.credentials_change_required
		AND s.purpose <> 'web_client' AND (NOT $2::boolean OR (a.administrator AND s.purpose='admin'))`, security.Digest(token), web).Scan(
		&p.ID, &p.Login, &p.PasswordHash, &p.Verified, &p.Administrator, &p.Disabled, &p.CreatedAt, &p.SessionID, &p.CSRF)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return p, err
}

func (s *Store) Refresh(ctx context.Context, token string, c config.Config, requestIDs ...string) (Tokens, error) {
	requestID := ""
	if len(requestIDs) == 1 {
		requestID = requestIDs[0]
	} else {
		requestID, _ = security.ID()
	}
	return s.refreshRecoverable(ctx, token, requestID, c)
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
