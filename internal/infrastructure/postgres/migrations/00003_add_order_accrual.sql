-- +goose Up
ALTER TABLE orders
    ADD COLUMN accrual DOUBLE PRECISION
    CHECK (accrual >= 0 AND accrual < 'Infinity'::DOUBLE PRECISION);

-- +goose Down
ALTER TABLE orders DROP COLUMN accrual;
