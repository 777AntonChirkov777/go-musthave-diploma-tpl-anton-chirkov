-- +goose Up
CREATE TYPE order_check_backoff AS ENUM ('short', 'long');

ALTER TABLE orders ADD COLUMN next_check_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE orders ADD COLUMN check_attempts INTEGER NOT NULL DEFAULT 0 CHECK (check_attempts >= 0);
ALTER TABLE orders ADD COLUMN check_backoff order_check_backoff NOT NULL DEFAULT 'long';
CREATE INDEX orders_pending_next_check_idx ON orders (next_check_at) WHERE status IN ('NEW', 'PROCESSING');

-- +goose Down
DROP INDEX orders_pending_next_check_idx;
ALTER TABLE orders DROP COLUMN check_backoff;
ALTER TABLE orders DROP COLUMN check_attempts;
ALTER TABLE orders DROP COLUMN next_check_at;
DROP TYPE order_check_backoff;
