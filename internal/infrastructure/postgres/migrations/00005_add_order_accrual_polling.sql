-- +goose Up
ALTER TABLE orders ADD COLUMN next_check_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE orders ADD COLUMN check_attempts INTEGER NOT NULL DEFAULT 0 CHECK (check_attempts >= 0);
ALTER TABLE orders ADD COLUMN check_backoff TEXT NOT NULL DEFAULT 'long' CHECK (check_backoff IN ('short', 'long'));
CREATE INDEX orders_pending_next_check_idx ON orders (next_check_at) WHERE status IN ('NEW', 'PROCESSING');

-- +goose Down
DROP INDEX orders_pending_next_check_idx;
ALTER TABLE orders DROP COLUMN check_backoff;
ALTER TABLE orders DROP COLUMN check_attempts;
ALTER TABLE orders DROP COLUMN next_check_at;
