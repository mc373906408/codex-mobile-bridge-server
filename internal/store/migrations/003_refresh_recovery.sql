CREATE TABLE refresh_results (
    old_digest bytea PRIMARY KEY REFERENCES spent_refresh_tokens(digest) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    request_id uuid NOT NULL,
    new_refresh_digest bytea NOT NULL,
    ciphertext bytea NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT now() + interval '5 minutes',
    UNIQUE(session_id, request_id)
);
CREATE INDEX refresh_results_expiry ON refresh_results(expires_at);
