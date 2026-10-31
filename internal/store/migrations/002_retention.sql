ALTER TABLE mail_outbox ADD COLUMN purpose text NOT NULL DEFAULT 'verify' CHECK (purpose IN ('verify','reset'));
ALTER TABLE mail_outbox ADD COLUMN expires_at timestamptz NOT NULL DEFAULT (now() + interval '1 hour');
ALTER TABLE mail_outbox DROP CONSTRAINT mail_outbox_status_check;
ALTER TABLE mail_outbox ADD CONSTRAINT mail_outbox_status_check CHECK (status IN ('queued','leased','sent','failed','cancelled'));
CREATE INDEX mail_account_purpose ON mail_outbox(account_id,purpose) WHERE status IN ('queued','leased');
CREATE INDEX pairings_expiry ON pairings(expires_at);
CREATE INDEX bindings_status ON bindings(status,created_at);
