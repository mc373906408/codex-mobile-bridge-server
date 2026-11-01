ALTER TABLE accounts ADD COLUMN credentials_change_required boolean NOT NULL DEFAULT false;

CREATE TABLE administrator_setup_sessions (
    digest bytea PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    password_hash text NOT NULL,
    csrf text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX administrator_setup_expiry ON administrator_setup_sessions(expires_at);
