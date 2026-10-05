-- +goose Up
ALTER TABLE orders
    ADD COLUMN accrual NUMERIC(20, 2) NULL
    CHECK (accrual >= 0 AND accrual <> 'NaN'::NUMERIC);

CREATE TABLE withdrawals (
    number_hash BYTEA NOT NULL PRIMARY KEY CHECK (octet_length(number_hash) = 32),
    number VARCHAR(255) NOT NULL CHECK (number ~ '^[0-9]+$'),
    user_id VARCHAR(32) NOT NULL REFERENCES users(id),
    amount NUMERIC(20, 2) NOT NULL CHECK (amount > 0 AND amount <> 'NaN'::NUMERIC),
    processed_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX withdrawals_user_processed_at_idx ON withdrawals (user_id, processed_at);

-- +goose Down
DROP TABLE withdrawals;
ALTER TABLE orders DROP COLUMN accrual;
