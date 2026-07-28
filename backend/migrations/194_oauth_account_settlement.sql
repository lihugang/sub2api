ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS oauth_settlement_cost NUMERIC(20,10);

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS record_type VARCHAR(32) NOT NULL DEFAULT 'request';

ALTER TABLE usage_logs
    ALTER COLUMN api_key_id DROP NOT NULL;

UPDATE accounts
SET rate_multiplier = 0
WHERE type = 'oauth' AND rate_multiplier IS DISTINCT FROM 0;

CREATE OR REPLACE FUNCTION force_oauth_account_rate_multiplier_zero()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.type = 'oauth' THEN
        NEW.rate_multiplier := 0;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS accounts_force_oauth_rate_multiplier_zero ON accounts;
CREATE TRIGGER accounts_force_oauth_rate_multiplier_zero
    BEFORE INSERT OR UPDATE OF type, rate_multiplier ON accounts
    FOR EACH ROW EXECUTE FUNCTION force_oauth_account_rate_multiplier_zero();

CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_logs_oauth_settlement_once
    ON usage_logs (account_id, user_id)
    WHERE record_type = 'oauth_settlement';
