ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS oauth_billing_mode BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE accounts
SET oauth_billing_mode = TRUE
WHERE type = 'oauth';
