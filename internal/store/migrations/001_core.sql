CREATE TABLE accounts (
    id uuid PRIMARY KEY,
    login text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    verified boolean NOT NULL DEFAULT false,
    administrator boolean NOT NULL DEFAULT false,
    disabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
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
    purpose text NOT NULL CHECK (purpose IN ('verify', 'reset')),
    expires_at timestamptz NOT NULL,
    used_at timestamptz
);
CREATE TABLE mail_outbox (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts ON DELETE CASCADE,
    encrypted_message bytea NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'leased', 'sent', 'failed')),
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_id uuid,
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_ready ON mail_outbox(available_at) WHERE status IN ('queued','leased');
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
    control_public_key bytea NOT NULL CHECK (octet_length(control_public_key) = 32),
    token_digest bytea NOT NULL UNIQUE,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX installations_account ON installations(account_id);
CREATE TABLE pairings (
    id uuid PRIMARY KEY,
    gateway_id uuid NOT NULL REFERENCES gateways ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE bindings (
    id uuid PRIMARY KEY,
    gateway_id uuid NOT NULL REFERENCES gateways ON DELETE CASCADE,
    installation_id uuid NOT NULL REFERENCES installations ON DELETE CASCADE,
    pairing_id uuid REFERENCES pairings ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','revoked')),
    approved_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX live_binding ON bindings(gateway_id,installation_id) WHERE status <> 'revoked';
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
    actor_id uuid REFERENCES accounts ON DELETE SET NULL,
    action text NOT NULL,
    target_id uuid,
    created_at timestamptz NOT NULL DEFAULT now()
);
