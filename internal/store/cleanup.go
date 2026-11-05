package store

import (
	"context"
	"log/slog"
	"time"
)

// Bounded deletion retains push IDs past their maximum replay window. Accounting
// and audit history are intentionally not pruned by the runtime.
func (s *Store) Cleanup(ctx context.Context) error {
	statements := []string{
		`DELETE FROM administrator_setup_sessions WHERE digest IN (SELECT digest FROM administrator_setup_sessions WHERE expires_at<now() LIMIT 1000)`,
		`DELETE FROM refresh_results WHERE old_digest IN (SELECT old_digest FROM refresh_results WHERE expires_at<now() LIMIT 1000)`,
		`DELETE FROM push_jobs WHERE id IN (SELECT id FROM push_jobs WHERE expires_at<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM push_acks WHERE ctid IN (SELECT ctid FROM push_acks WHERE expires_at<now() LIMIT 1000)`,
		`DELETE FROM mail_outbox WHERE id IN (SELECT id FROM mail_outbox WHERE expires_at<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM email_tokens WHERE digest IN (SELECT digest FROM email_tokens WHERE expires_at<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM registration_challenges WHERE login IN (SELECT login FROM registration_challenges WHERE GREATEST(expires_at,registration_expires_at)<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM rate_limits WHERE key IN (SELECT key FROM rate_limits WHERE window_start<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM sessions WHERE id IN (SELECT s.id FROM sessions s WHERE s.refresh_expires_at<now()-interval '7 days'
			AND NOT EXISTS (SELECT 1 FROM gateways g WHERE g.session_id=s.id)
			AND NOT EXISTS (SELECT 1 FROM installations i WHERE i.session_id=s.id) LIMIT 1000)`,
	}
	for _, sql := range statements {
		if _, err := s.Pool.Exec(ctx, sql); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			call, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := s.Cleanup(call); err != nil {
				slog.Error("database cleanup failed", "operation", "bounded_retention", "retry_seconds", 60)
			}
			cancel()
		}
	}
}
