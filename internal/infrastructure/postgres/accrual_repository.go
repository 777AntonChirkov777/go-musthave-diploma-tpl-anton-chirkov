package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	accrualapp "diplom/internal/application/accrual"
	domain "diplom/internal/domain/order"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ accrualapp.Repository = (*AccrualRepository)(nil)

type AccrualRepository struct {
	pool *pgxpool.Pool
}

func NewAccrualRepository(pool *pgxpool.Pool) *AccrualRepository {
	return &AccrualRepository{pool: pool}
}

func (r *AccrualRepository) ClaimDue(ctx context.Context, now, leaseUntil time.Time, limit int) ([]accrualapp.Pending, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		WITH due AS (
			SELECT number_hash, next_check_at FROM orders
			WHERE status IN ('NEW', 'PROCESSING') AND next_check_at <= $1
			ORDER BY next_check_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		), claimed AS (
			UPDATE orders SET next_check_at = $2
			FROM due
			WHERE orders.number_hash = due.number_hash
			RETURNING orders.number, orders.status, orders.check_attempts, orders.check_backoff, due.next_check_at AS due_at
		)
		SELECT number, status, check_attempts, check_backoff FROM claimed ORDER BY due_at`,
		now, leaseUntil, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("claim due orders: %w", err)
	}
	defer rows.Close()

	claimed := make([]accrualapp.Pending, 0)
	for rows.Next() {
		pending, err := scanPending(rows)
		if err != nil {
			return nil, fmt.Errorf("read claimed order: %w", err)
		}
		claimed = append(claimed, pending)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due orders: %w", err)
	}
	return claimed, nil
}

func (r *AccrualRepository) Claim(ctx context.Context, number domain.Number, now, leaseUntil time.Time) (accrualapp.Pending, bool, error) {
	hash := sha256.Sum256([]byte(number))
	pending, err := scanPending(r.pool.QueryRow(ctx, `
		UPDATE orders SET next_check_at = $4
		WHERE number_hash = $1 AND number = $2
		  AND status IN ('NEW', 'PROCESSING') AND next_check_at <= $3
		RETURNING number, status, check_attempts, check_backoff`,
		hash[:], string(number), now, leaseUntil,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return accrualapp.Pending{}, false, nil
	}
	if err != nil {
		return accrualapp.Pending{}, false, fmt.Errorf("claim order: %w", err)
	}
	return pending, true, nil
}

func (r *AccrualRepository) Save(ctx context.Context, update accrualapp.Update) error {
	if err := accrualapp.ValidateUpdate(update); err != nil {
		return fmt.Errorf("save accrual result: %w", err)
	}
	hash := sha256.Sum256([]byte(update.Number))
	if _, err := r.pool.Exec(ctx, `
		UPDATE orders
		SET status = $3::text::order_status, accrual = $4, check_attempts = $5, check_backoff = $7, next_check_at = $6
		WHERE number_hash = $1 AND number = $2
		  AND status IN ('NEW', 'PROCESSING')
		  AND NOT (status = 'PROCESSING' AND $3::text = 'NEW')`,
		hash[:], string(update.Number), string(update.Status), update.Accrual, update.Attempts, update.NextCheckAt, string(update.Backoff),
	); err != nil {
		return fmt.Errorf("save accrual result: %w", err)
	}
	return nil
}

func scanPending(row pgx.Row) (accrualapp.Pending, error) {
	var number, status, backoff string
	var attempts int
	if err := row.Scan(&number, &status, &attempts, &backoff); err != nil {
		return accrualapp.Pending{}, err
	}
	return accrualapp.Pending{Number: domain.Number(number), Status: domain.Status(status), Attempts: attempts, Backoff: accrualapp.Backoff(backoff)}, nil
}
