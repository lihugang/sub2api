ALTER TABLE users
    ADD COLUMN IF NOT EXISTS model_routing_notice_mode VARCHAR(16) NOT NULL DEFAULT 'color';

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_model_routing_notice_mode_check;

ALTER TABLE users
    ADD CONSTRAINT users_model_routing_notice_mode_check
    CHECK (model_routing_notice_mode IN ('disabled', 'plain', 'color'));
