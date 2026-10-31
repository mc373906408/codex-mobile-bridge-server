package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/push"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

type MailSender interface {
	Send(context.Context, store.MailMessage) error
}
type Worker struct {
	Store  *store.Store
	Config config.Config
	Push   push.Sender
	Mail   MailSender
}

type Job struct {
	ID, LeaseID string
	Attempts    int
	Delivery    push.Delivery
	DirectUntil time.Time
}

var ErrDirectLease = errors.New("direct delivery lease active")

func (w *Worker) ClaimPush(ctx context.Context) (Job, error) {
	var job Job
	lease, err := security.ID()
	if err != nil {
		return job, err
	}
	job.LeaseID = lease
	err = w.Store.Pool.QueryRow(ctx, `UPDATE push_jobs SET status='leased',lease_id=$1,lease_until=now()+interval '60 seconds',attempts=attempts+1
		WHERE id=(SELECT id FROM push_jobs WHERE ((status='queued' AND available_at<=now()) OR (status='leased' AND lease_until<now()))
		ORDER BY available_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id,binding_id,notification_id,ciphertext,extract(epoch FROM expires_at)::bigint,attempts`, lease).Scan(
		&job.ID, &job.Delivery.Envelope.BindingID, &job.Delivery.Envelope.NotificationID, &job.Delivery.Envelope.Ciphertext, &job.Delivery.Envelope.ExpiresAt, &job.Attempts)
	job.Delivery.Envelope.Version = 1
	return job, err
}

func (w *Worker) Eligible(ctx context.Context, job *Job) error {
	var token []byte
	var endpointID string
	err := w.Store.Pool.QueryRow(ctx, `SELECT p.platform,p.app_id,p.token_ciphertext,p.id,p.direct_until FROM push_jobs j
		JOIN push_endpoints p ON p.id=j.endpoint_id JOIN bindings b ON b.id=j.binding_id
		JOIN gateways g ON g.id=b.gateway_id JOIN installations i ON i.id=b.installation_id
		JOIN accounts a ON a.id=g.account_id JOIN entitlements e ON e.account_id=a.id
		JOIN sessions gs ON gs.id=g.session_id JOIN sessions ms ON ms.id=i.session_id
		WHERE j.id=$1 AND j.status='leased' AND j.lease_id=$2 AND j.lease_until>clock_timestamp() AND j.expires_at>clock_timestamp()
		AND j.attempts<=6 AND b.status='active' AND g.revoked_at IS NULL AND i.revoked_at IS NULL AND p.enabled AND p.consent
		AND gs.revoked_at IS NULL AND ms.revoked_at IS NULL AND gs.refresh_expires_at>now() AND ms.refresh_expires_at>now()
		AND p.revision=j.endpoint_revision AND a.verified AND NOT a.disabled
		AND (NOT $3 OR e.expires_at>clock_timestamp())
		AND NOT EXISTS(SELECT 1 FROM push_acks WHERE binding_id=j.binding_id AND notification_id=j.notification_id AND expires_at>now())`, job.ID, job.LeaseID, w.Config.Paid()).Scan(
		&job.Delivery.Platform, &job.Delivery.AppID, &token, &endpointID, &job.DirectUntil)
	if err != nil {
		return err
	}
	if job.DirectUntil.After(time.Now()) {
		return ErrDirectLease
	}
	decoded, err := w.Store.Vault.Open("push-token:"+endpointID, token)
	if err != nil {
		return err
	}
	job.Delivery.Token = string(decoded)
	return nil
}

func (w *Worker) StepPush(ctx context.Context) (bool, error) {
	job, err := w.ClaimPush(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = w.Eligible(ctx, &job); err != nil {
		if errors.Is(err, ErrDirectLease) {
			_, err = w.Store.Pool.Exec(ctx, `UPDATE push_jobs SET status='queued',available_at=$3,attempts=GREATEST(0,attempts-1),lease_id=NULL,lease_until=NULL
				WHERE id=$1 AND lease_id=$2 AND status='leased'`, job.ID, job.LeaseID, job.DirectUntil)
			return true, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return true, err
		}
		_, err = w.Store.Pool.Exec(ctx, `UPDATE push_jobs SET status=CASE WHEN expires_at<=now() THEN 'expired' WHEN attempts>6 THEN 'failed' ELSE 'cancelled' END,
			lease_id=NULL,lease_until=NULL WHERE id=$1 AND lease_id=$2 AND status='leased'`, job.ID, job.LeaseID)
		return true, err
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	result := w.Push.Send(call, job.Delivery)
	cancel()
	status := "queued"
	if result.Accepted {
		status = "accepted"
	} else if result.Permanent || job.Attempts >= 6 {
		status = "failed"
	}
	delay := time.Duration(5*(1<<min(job.Attempts-1, 5))) * time.Second
	err = pgx.BeginFunc(ctx, w.Store.Pool, func(tx pgx.Tx) error {
		if result.InvalidToken {
			if _, err := tx.Exec(ctx, `UPDATE push_endpoints p SET enabled=false FROM push_jobs j
				WHERE j.id=$1 AND j.lease_id=$2 AND j.status='leased' AND p.id=j.endpoint_id AND p.revision=j.endpoint_revision`, job.ID, job.LeaseID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE push_jobs SET status=$3,last_code=$4,available_at=now()+$5::interval,lease_id=NULL,lease_until=NULL
			WHERE id=$1 AND lease_id=$2 AND status='leased'`, job.ID, job.LeaseID, status, result.Code, store.Interval(delay))
		return err
	})
	return true, err
}

func (w *Worker) StepMail(ctx context.Context) (bool, error) {
	if w.Mail == nil {
		return false, nil
	}
	id, err := security.ID()
	if err != nil {
		return false, err
	}
	var jobID string
	var encrypted []byte
	var attempts int
	err = w.Store.Pool.QueryRow(ctx, `UPDATE mail_outbox SET status='leased',lease_id=$1,lease_until=now()+interval '60 seconds',attempts=attempts+1
		WHERE id=(SELECT id FROM mail_outbox WHERE (status='queued' AND available_at<=now()) OR (status='leased' AND lease_until<now())
		ORDER BY available_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,encrypted_message,attempts`, id).Scan(&jobID, &encrypted, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var valid bool
	if err = w.Store.Pool.QueryRow(ctx, `SELECT j.expires_at>clock_timestamp() AND a.verified IS NOT NULL AND NOT a.disabled
		FROM mail_outbox j JOIN accounts a ON a.id=j.account_id WHERE j.id=$1 AND j.lease_id=$2 AND j.status='leased'`, jobID, id).Scan(&valid); err != nil {
		return true, err
	}
	if !valid {
		_, err = w.Store.Pool.Exec(ctx, "UPDATE mail_outbox SET status='cancelled',lease_id=NULL,lease_until=NULL WHERE id=$1 AND lease_id=$2 AND status='leased'", jobID, id)
		return true, err
	}
	data, err := w.Store.Vault.Open("mail:"+jobID, encrypted)
	var message store.MailMessage
	if err == nil {
		err = json.Unmarshal(data, &message)
	}
	if err == nil {
		call, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = w.Mail.Send(call, message)
		cancel()
	}
	status := "queued"
	if err == nil {
		status = "sent"
	} else if attempts >= 6 {
		status = "failed"
	}
	_, err = w.Store.Pool.Exec(ctx, "UPDATE mail_outbox SET status=$3,lease_id=NULL,lease_until=NULL,available_at=now()+interval '1 minute' WHERE id=$1 AND lease_id=$2 AND status='leased'", jobID, id, status)
	return true, err
}

func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(mail bool) {
			defer wg.Done()
			for ctx.Err() == nil {
				var worked bool
				var err error
				if mail {
					worked, err = w.StepMail(ctx)
				} else {
					worked, err = w.StepPush(ctx)
				}
				if !worked || err != nil {
					if err != nil {
						slog.Warn("worker operation failed", "queue", map[bool]string{true: "mail", false: "push"}[mail])
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
				}
			}
		}(i == 4)
	}
	wg.Wait()
}
