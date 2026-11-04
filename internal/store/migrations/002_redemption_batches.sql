CREATE TABLE redemption_batches (
    id uuid PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE redemption_codes ADD COLUMN batch_id uuid;
-- Before batch IDs, one GenerateCodes transaction gave every code the same now().
INSERT INTO redemption_batches(id,created_at)
    SELECT min(id::text)::uuid,created_at FROM redemption_codes GROUP BY created_at;
UPDATE redemption_codes c SET batch_id=b.id
    FROM redemption_batches b WHERE b.created_at=c.created_at;
ALTER TABLE redemption_codes ALTER COLUMN batch_id SET NOT NULL;
ALTER TABLE redemption_codes ADD CONSTRAINT redemption_codes_batch_fk
    FOREIGN KEY(batch_id) REFERENCES redemption_batches;
ALTER TABLE redemption_codes ADD CONSTRAINT redemption_codes_id_batch_unique UNIQUE(id,batch_id);
CREATE INDEX redemption_codes_batch ON redemption_codes(batch_id);

ALTER TABLE redemptions ADD COLUMN batch_id uuid;
UPDATE redemptions r SET batch_id=c.batch_id FROM redemption_codes c WHERE c.id=r.code_id;
ALTER TABLE redemptions ALTER COLUMN batch_id SET NOT NULL;
ALTER TABLE redemptions ADD CONSTRAINT redemptions_code_batch_fk
    FOREIGN KEY(code_id,batch_id) REFERENCES redemption_codes(id,batch_id);
ALTER TABLE redemptions ADD CONSTRAINT redemptions_account_batch_unique UNIQUE(account_id,batch_id);
