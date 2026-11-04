package store

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

const RegistrationTTL = 10 * time.Minute
const EmailCodeTTL = 10 * time.Minute
const EmailCodeRetry = time.Minute

func emailVerificationMessage(login, code string) MailMessage {
	return MailMessage{To: login, Subject: "Bridge email verification code / 邮箱验证码",
		Body: "Your Bridge verification code / 您的邮箱验证码：\r\n\r\n" + code +
			"\r\n\r\nValid for 10 minutes. Do not share this code. If you did not request it, ignore this email." +
			"\r\n10 分钟内有效，请勿分享。若非本人申请，请忽略此邮件。\r\n"}
}

type RegistrationProof struct {
	Verified  bool   `json:"verified"`
	Token     string `json:"registration_token"`
	ExpiresIn int    `json:"expires_in"`
}

func registrationReady(ctx context.Context, tx pgx.Tx, login string) (bool, error) {
	// Serialize resend, verification and completion even before an account exists.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "registration:"+login); err != nil {
		return false, err
	}
	var exists bool
	err := tx.QueryRow(ctx, "SELECT true FROM accounts WHERE login=$1 FOR UPDATE", login).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func (s *Store) RequestRegistrationCode(ctx context.Context, login string, c config.Config) error {
	if !c.Paid() || !c.MailEnabled() {
		return ErrDenied
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		ready, err := registrationReady(ctx, tx, login)
		if err != nil || !ready {
			return err
		}
		var id string
		var previous []byte
		err = tx.QueryRow(ctx, "SELECT id,digest FROM registration_challenges WHERE login=$1 FOR UPDATE", login).Scan(&id, &previous)
		if errors.Is(err, pgx.ErrNoRows) {
			id, err = security.ID()
		}
		if err != nil {
			return err
		}
		var code string
		var digest []byte
		for range 8 {
			code, err = security.NewEmailCode()
			if err != nil {
				return err
			}
			digest, err = security.EmailCodeDigest(c.DataKey, "registration:"+c.Origin+":"+id+":"+login, code)
			if err != nil {
				return err
			}
			if !hmac.Equal(previous, digest) {
				break
			}
		}
		if hmac.Equal(previous, digest) {
			return errors.New("cannot rotate registration code")
		}
		mailID, err := security.ID()
		if err != nil {
			return err
		}
		message := emailVerificationMessage(login, code)
		data, err := json.Marshal(message)
		if err != nil {
			return err
		}
		sealed, err := s.Vault.Seal("mail:"+mailID, data)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO registration_challenges(login,id,digest,expires_at,origin) VALUES($1,$2,$3,now()+$4::interval,$5)
			ON CONFLICT(login) DO UPDATE SET origin=excluded.origin,digest=excluded.digest,attempts=0,expires_at=excluded.expires_at,
			verified_at=NULL,registration_digest=NULL,registration_expires_at=NULL,used_at=NULL,issued_at=now()`, login, id, digest, Interval(EmailCodeTTL), c.Origin); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE mail_outbox SET status='cancelled' WHERE registration_id=$1 AND status IN ('queued','leased')", id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO mail_outbox(id,registration_id,encrypted_message,purpose,expires_at)
			VALUES($1,$2,$3,'register',now()+$4::interval)`, mailID, id, sealed, Interval(EmailCodeTTL))
		return err
	})
}

func (s *Store) VerifyRegistrationCode(ctx context.Context, login, code string, c config.Config) (RegistrationProof, error) {
	if !c.Paid() || !security.ValidEmailCode(code) {
		return RegistrationProof{}, ErrDenied
	}
	var proof RegistrationProof
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		ready, err := registrationReady(ctx, tx, login)
		if err != nil || !ready {
			return err
		}
		var id, origin string
		var digest []byte
		var attempts int
		var live bool
		err = tx.QueryRow(ctx, `SELECT id,origin,digest,attempts,verified_at IS NULL AND used_at IS NULL AND expires_at>clock_timestamp()
			FROM registration_challenges WHERE login=$1 FOR UPDATE`, login).Scan(&id, &origin, &digest, &attempts, &live)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !live || attempts >= 5 || origin != c.Origin {
			return nil
		}
		actual, err := security.EmailCodeDigest(c.DataKey, "registration:"+c.Origin+":"+id+":"+login, code)
		if err != nil {
			return err
		}
		if !hmac.Equal(digest, actual) {
			// A wrong guess must commit its attempt count rather than roll it back.
			_, err = tx.Exec(ctx, "UPDATE registration_challenges SET attempts=attempts+1 WHERE login=$1", login)
			return err
		}
		token, err := security.Token()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE registration_challenges SET verified_at=clock_timestamp(),registration_digest=$2,
			registration_expires_at=clock_timestamp()+$3::interval WHERE login=$1`, login, security.Digest(token), Interval(RegistrationTTL)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE mail_outbox SET status='cancelled' WHERE registration_id=$1 AND status IN ('queued','leased')", id); err != nil {
			return err
		}
		proof = RegistrationProof{Verified: true, Token: token, ExpiresIn: int(RegistrationTTL.Seconds())}
		return nil
	})
	if err != nil {
		return RegistrationProof{}, err
	}
	if !proof.Verified {
		return RegistrationProof{}, ErrDenied
	}
	return proof, nil
}

func (s *Store) CompleteRegistration(ctx context.Context, login, token, hash string, c config.Config) (Account, error) {
	if !c.Paid() || len(token) != 43 || hash == "" {
		return Account{}, ErrDenied
	}
	var a Account
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		ready, err := registrationReady(ctx, tx, login)
		if err != nil {
			return err
		}
		var digest []byte
		var origin string
		var live bool
		err = tx.QueryRow(ctx, `SELECT origin,registration_digest,verified_at IS NOT NULL AND used_at IS NULL
			AND registration_expires_at>clock_timestamp() FROM registration_challenges WHERE login=$1 FOR UPDATE`, login).Scan(&origin, &digest, &live)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil {
			return err
		}
		if !live || origin != c.Origin || !hmac.Equal(digest, security.Digest(token)) {
			return ErrDenied
		}
		if !ready {
			return ErrConflict
		}
		id, err := security.ID()
		if err != nil {
			return err
		}
		a, err = scanAccount(tx.QueryRow(ctx, `INSERT INTO accounts(id,login,password_hash,verified) VALUES($1,$2,$3,true)
			ON CONFLICT(login) DO NOTHING
			RETURNING `+accountColumns, id, login, hash))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO entitlements(account_id) VALUES($1)", a.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE registration_challenges SET used_at=clock_timestamp() WHERE login=$1", login); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE mail_outbox SET status='cancelled' WHERE status IN ('queued','leased')
			AND registration_id=(SELECT id FROM registration_challenges WHERE login=$1)`, login)
		if err != nil {
			return err
		}
		return RecordAudit(ctx, tx, AuditEvent{ActorID: a.ID, AccountID: a.ID, Action: "account.create", TargetID: a.ID,
			After: map[string]any{"verified": true, "disabled": false, "registration": "email_proof"}})
	})
	return a, err
}
