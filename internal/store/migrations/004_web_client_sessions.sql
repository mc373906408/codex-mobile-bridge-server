ALTER TABLE sessions ADD COLUMN purpose text NOT NULL DEFAULT 'native'
    CHECK (purpose IN ('native', 'admin', 'web_client'));
UPDATE sessions SET purpose='admin' WHERE web_digest IS NOT NULL;
