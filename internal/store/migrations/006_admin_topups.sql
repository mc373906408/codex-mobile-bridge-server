CREATE TABLE admin_topups (
    actor_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    request_id uuid NOT NULL,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    days integer NOT NULL CHECK (days BETWEEN 1 AND 3650),
    note text NOT NULL DEFAULT '' CHECK (octet_length(note) <= 1000),
    previous_expires_at timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (actor_id, request_id)
);
CREATE INDEX admin_topups_account ON admin_topups(account_id, created_at DESC);
