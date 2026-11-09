CREATE TABLE registration_campaigns (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
    days integer NOT NULL CHECK (days BETWEEN 1 AND 3650),
    starts_at timestamptz,
    ends_at timestamptz,
    max_grants integer CHECK (max_grants > 0),
    granted_count integer NOT NULL DEFAULT 0 CHECK (granted_count >= 0),
    state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','enabled','ended')),
    activated_at timestamptz,
    ended_at timestamptz,
    end_reason text CHECK (end_reason IN ('stopped','expired','exhausted')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (starts_at IS NULL OR ends_at IS NULL OR ends_at > starts_at),
    CHECK (max_grants IS NULL OR granted_count <= max_grants),
    CHECK ((state = 'ended') = (ended_at IS NOT NULL AND end_reason IS NOT NULL)),
    CHECK (state <> 'enabled' OR (activated_at IS NOT NULL AND starts_at IS NOT NULL))
);
CREATE UNIQUE INDEX registration_campaign_single_open ON registration_campaigns ((true)) WHERE state <> 'ended';
CREATE TABLE registration_campaign_grants (
    id uuid PRIMARY KEY,
    campaign_id uuid NOT NULL REFERENCES registration_campaigns,
    account_id uuid NOT NULL UNIQUE REFERENCES accounts,
    days integer NOT NULL CHECK (days BETWEEN 1 AND 3650),
    granted_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > granted_at)
);
CREATE INDEX registration_campaign_grant_page ON registration_campaign_grants(campaign_id,granted_at DESC,id DESC);
CREATE FUNCTION preserve_registration_grant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'registration grant history is immutable';
END $$;
CREATE TRIGGER preserve_registration_grant BEFORE UPDATE OR DELETE ON registration_campaign_grants
    FOR EACH ROW EXECUTE FUNCTION preserve_registration_grant();
INSERT INTO registration_campaigns(id,name,days) VALUES(gen_random_uuid(),'邮箱注册 7 天体验',7);
