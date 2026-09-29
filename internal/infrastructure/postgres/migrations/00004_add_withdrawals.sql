-- +goose Up
ALTER TABLE orders DROP CONSTRAINT orders_accrual_check;
ALTER TABLE orders ALTER COLUMN accrual TYPE NUMERIC(20, 2) USING accrual::NUMERIC(20, 2);
ALTER TABLE orders ADD CONSTRAINT orders_accrual_check CHECK (accrual >= 0 AND accrual <> 'NaN'::NUMERIC);

CREATE TABLE withdrawals (
    number_hash BYTEA PRIMARY KEY CHECK (octet_length(number_hash) = 32),
    number TEXT NOT NULL CHECK (number ~ '^[0-9]+$'),
    user_id TEXT NOT NULL REFERENCES users(id),
    amount NUMERIC(20, 2) NOT NULL CHECK (amount > 0 AND amount <> 'NaN'::NUMERIC),
    processed_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX withdrawals_user_processed_at_idx ON withdrawals (user_id, processed_at);

-- +goose Down
DROP TABLE withdrawals;
ALTER TABLE orders DROP CONSTRAINT orders_accrual_check;
ALTER TABLE orders ALTER COLUMN accrual TYPE DOUBLE PRECISION USING accrual::DOUBLE PRECISION;
ALTER TABLE orders ADD CONSTRAINT orders_accrual_check CHECK (accrual >= 0 AND accrual < 'Infinity'::DOUBLE PRECISION);
