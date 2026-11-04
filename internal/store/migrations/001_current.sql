CREATE TABLE accounts (
    id uuid PRIMARY KEY,
    login text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    verified boolean NOT NULL DEFAULT false,
    administrator boolean NOT NULL DEFAULT false,
    disabled boolean NOT NULL DEFAULT false,
    credentials_change_required boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE administrator_setup_sessions (
    digest bytea PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    password_hash text NOT NULL,
    csrf text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX administrator_setup_expiry ON administrator_setup_sessions(expires_at);
CREATE TABLE entitlements (
    account_id uuid PRIMARY KEY REFERENCES accounts ON DELETE CASCADE,
    expires_at timestamptz
);
CREATE TABLE sessions (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    access_digest bytea NOT NULL UNIQUE,
    refresh_digest bytea NOT NULL UNIQUE,
    web_digest bytea UNIQUE,
    csrf text NOT NULL,
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz NOT NULL,
    web_expires_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_account ON sessions(account_id);
CREATE TABLE spent_refresh_tokens (
    digest bytea PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES sessions ON DELETE CASCADE
);
CREATE TABLE email_tokens (
    digest bytea PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    purpose text NOT NULL CHECK (purpose = 'reset'),
    expires_at timestamptz NOT NULL,
    used_at timestamptz
);
CREATE TABLE registration_challenges (
    login text PRIMARY KEY,
    origin text NOT NULL,
    id uuid NOT NULL UNIQUE,
    digest bytea NOT NULL CHECK (octet_length(digest) = 32),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    expires_at timestamptz NOT NULL,
    verified_at timestamptz,
    registration_digest bytea UNIQUE CHECK (octet_length(registration_digest) = 32),
    registration_expires_at timestamptz,
    used_at timestamptz,
    issued_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((verified_at IS NULL AND registration_digest IS NULL AND registration_expires_at IS NULL)
        OR (verified_at IS NOT NULL AND registration_digest IS NOT NULL AND registration_expires_at IS NOT NULL))
);
CREATE INDEX registration_challenge_expiry ON registration_challenges(expires_at);
CREATE TABLE mail_outbox (
    id uuid PRIMARY KEY,
    account_id uuid REFERENCES accounts ON DELETE CASCADE,
    registration_id uuid REFERENCES registration_challenges(id) ON DELETE CASCADE,
    encrypted_message bytea NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('reset','register')),
    expires_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'leased', 'sent', 'failed', 'cancelled')),
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_id uuid,
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((purpose = 'register' AND account_id IS NULL AND registration_id IS NOT NULL)
        OR (purpose = 'reset' AND account_id IS NOT NULL AND registration_id IS NULL))
);
CREATE INDEX mail_ready ON mail_outbox(available_at) WHERE status IN ('queued','leased');
CREATE INDEX mail_account_purpose ON mail_outbox(account_id,purpose) WHERE status IN ('queued','leased');
CREATE INDEX mail_registration ON mail_outbox(registration_id) WHERE status IN ('queued','leased');
CREATE TABLE redemption_codes (
    id uuid PRIMARY KEY,
    digest bytea NOT NULL UNIQUE,
    days integer NOT NULL CHECK (days BETWEEN 1 AND 3650),
    revoked_at timestamptz,
    used_by uuid REFERENCES accounts ON DELETE SET NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE redemptions (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    code_id uuid NOT NULL UNIQUE REFERENCES redemption_codes,
    days integer NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE redeem_requests (
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    request_id uuid NOT NULL,
    code_digest bytea NOT NULL,
    redemption_id uuid NOT NULL REFERENCES redemptions ON DELETE CASCADE,
    PRIMARY KEY(account_id, request_id)
);
CREATE TABLE gateways (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions ON DELETE CASCADE,
    name text NOT NULL,
    control_public_key bytea NOT NULL CHECK (octet_length(control_public_key) = 32),
    token_digest bytea NOT NULL UNIQUE,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gateways_account ON gateways(account_id);
CREATE TABLE installations (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions ON DELETE CASCADE,
    name text NOT NULL DEFAULT 'Phone',
    last_seen_at timestamptz,
    control_public_key bytea NOT NULL CHECK (octet_length(control_public_key) = 32),
    token_digest bytea NOT NULL UNIQUE,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX installations_account ON installations(account_id);
CREATE TABLE bindings (
    id uuid PRIMARY KEY,
    gateway_id uuid NOT NULL REFERENCES gateways ON DELETE CASCADE,
    installation_id uuid NOT NULL REFERENCES installations ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','disabled','revoked')),
    access_revision bigint NOT NULL DEFAULT 0 CHECK (access_revision >= 0),
    approved_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX live_binding ON bindings(gateway_id,installation_id) WHERE status <> 'revoked';
CREATE INDEX bindings_status ON bindings(status,created_at);
CREATE TABLE push_endpoints (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL UNIQUE REFERENCES bindings ON DELETE CASCADE,
    platform text NOT NULL CHECK (platform IN ('umeng','apns')),
    app_id text NOT NULL,
    token_ciphertext bytea NOT NULL,
    revision uuid NOT NULL,
    enabled boolean NOT NULL,
    consent boolean NOT NULL,
    direct_until timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE push_jobs (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL REFERENCES bindings ON DELETE CASCADE,
    notification_id uuid NOT NULL,
    endpoint_id uuid NOT NULL REFERENCES push_endpoints ON DELETE CASCADE,
    endpoint_revision uuid NOT NULL,
    ciphertext text NOT NULL,
    expires_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','leased','accepted','acked','cancelled','expired','failed')),
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL,
    lease_id uuid,
    lease_until timestamptz,
    last_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(binding_id,notification_id)
);
CREATE INDEX push_ready ON push_jobs(available_at) WHERE status IN ('queued','leased');
CREATE TABLE push_acks (
    binding_id uuid NOT NULL REFERENCES bindings ON DELETE CASCADE,
    notification_id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY(binding_id,notification_id)
);
CREATE TABLE rate_limits (
    key bytea PRIMARY KEY,
    window_start timestamptz NOT NULL,
    attempts integer NOT NULL
);
CREATE TABLE audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id uuid,
    account_id uuid,
    session_id uuid,
    device_id uuid,
    action text NOT NULL,
    target_id uuid,
    details jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details)='object' AND octet_length(details::text)<=4096),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_account_page ON audit_log(account_id,id DESC);
CREATE INDEX accounts_admin_page ON accounts(created_at DESC,id DESC);
CREATE INDEX gateways_admin_page ON gateways(account_id,created_at DESC,id DESC);
CREATE INDEX installations_admin_page ON installations(account_id,created_at DESC,id DESC);
CREATE INDEX sessions_admin_page ON sessions(account_id,created_at DESC,id DESC);
