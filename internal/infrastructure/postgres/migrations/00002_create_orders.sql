-- +goose Up
CREATE TABLE orders (
    number_hash BYTEA PRIMARY KEY CHECK (octet_length(number_hash) = 32),
    number TEXT NOT NULL CHECK (number ~ '^[0-9]+$'),
    user_id TEXT NOT NULL REFERENCES users(id),
    status TEXT NOT NULL CHECK (status IN ('NEW', 'PROCESSING', 'INVALID', 'PROCESSED')),
    uploaded_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX orders_user_uploaded_at_idx ON orders (user_id, uploaded_at);

-- +goose Down
DROP TABLE orders;
