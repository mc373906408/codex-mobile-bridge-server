package store

import (
	"context"
	"time"
)

// Bounded deletion retains push IDs past their maximum replay window. Accounting
// and audit history are intentionally not pruned by the runtime.
func (s *Store) Cleanup(ctx context.Context) error {
	statements := []string{
		`DELETE FROM push_jobs WHERE id IN (SELECT id FROM push_jobs WHERE expires_at<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM push_acks WHERE ctid IN (SELECT ctid FROM push_acks WHERE expires_at<now() LIMIT 1000)`,
		`DELETE FROM mail_outbox WHERE id IN (SELECT id FROM mail_outbox WHERE expires_at<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM email_tokens WHERE digest IN (SELECT digest FROM email_tokens WHERE expires_at<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM rate_limits WHERE key IN (SELECT key FROM rate_limits WHERE window_start<now()-interval '1 day' LIMIT 1000)`,
		`UPDATE bindings SET status='revoked',revoked_at=now() WHERE id IN (SELECT b.id FROM bindings b JOIN pairings p ON p.id=b.pairing_id WHERE b.status='pending' AND p.expires_at<now() LIMIT 1000)`,
		`DELETE FROM sessions WHERE id IN (SELECT id FROM sessions WHERE refresh_expires_at<now()-interval '7 days' LIMIT 1000)`,
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
			_ = s.Cleanup(call)
			cancel()
		}
	}
}
