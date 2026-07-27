-- API key accounts with an explicit upstream URL are candidates for the
-- Sub2API billing declaration probe. Accounts named with the lowercase
-- "free" marker remain excluded. This migration does not perform I/O; the
-- regular bounded runner will issue the first probe.
UPDATE accounts
SET extra = jsonb_set(
        COALESCE(extra, '{}'::jsonb),
        '{upstream_billing_probe_enabled}',
        to_jsonb(
            COALESCE(credentials ->> 'api_key', '') <> ''
            AND COALESCE(credentials ->> 'base_url', '') <> ''
            AND name NOT LIKE '%free%'
        ),
        true
    ),
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND type = 'apikey';
