-- +goose Up
CREATE TYPE order_status AS ENUM ('NEW', 'PROCESSING', 'INVALID', 'PROCESSED');

CREATE TABLE orders (
    number_hash BYTEA NOT NULL PRIMARY KEY CHECK (octet_length(number_hash) = 32),
    number VARCHAR(255) NOT NULL CHECK (number ~ '^[0-9]+$'),
    user_id VARCHAR(32) NOT NULL REFERENCES users(id),
    status order_status NOT NULL,
    uploaded_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX orders_user_uploaded_at_idx ON orders (user_id, uploaded_at);

-- +goose Down
DROP TABLE orders;
DROP TYPE order_status;
