package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	application "diplom/internal/application/order"
	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ application.Repository = (*OrderRepository)(nil)

type OrderRepository struct {
	pool *pgxpool.Pool
}

func NewOrderRepository(pool *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{pool: pool}
}

func (r *OrderRepository) Add(ctx context.Context, order domain.Order) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := domain.Restore(order.Number(), order.UserID(), order.Status(), order.UploadedAt()); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(order.Number()))
	result, err := r.pool.Exec(ctx, `
		INSERT INTO orders (number_hash, number, user_id, status, uploaded_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (number_hash) DO NOTHING`,
		hash[:], string(order.Number()), string(order.UserID()), string(order.Status()), order.UploadedAt(),
	)
	if err != nil {
		return fmt.Errorf("insert order: %w", err)
	}
	if result.RowsAffected() != 0 {
		return nil
	}

	// A fixed-size key supports numbers longer than PostgreSQL's btree entry
	// limit. Compare the original number so a hash collision stays an internal
	// error rather than being mistaken for an order owned by another user.
	var existingNumber string
	if err := r.pool.QueryRow(ctx,
		"SELECT number FROM orders WHERE number_hash = $1", hash[:],
	).Scan(&existingNumber); err != nil {
		return fmt.Errorf("read conflicting order: %w", err)
	}
	if existingNumber != string(order.Number()) {
		return errors.New("order number hash collision")
	}
	return domain.ErrAlreadyExists
}

func (r *OrderRepository) GetByNumber(ctx context.Context, number domain.Number) (domain.Order, error) {
	hash := sha256.Sum256([]byte(number))
	order, err := scanOrder(r.pool.QueryRow(ctx, `
		SELECT number, user_id, status, uploaded_at
		FROM orders WHERE number_hash = $1 AND number = $2`, hash[:], string(number)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, application.ErrNotFound
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order by number: %w", err)
	}
	return order, nil
}

func (r *OrderRepository) ListByUser(ctx context.Context, userID user.ID) ([]domain.Order, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT number, user_id, status, uploaded_at
		FROM orders WHERE user_id = $1 ORDER BY uploaded_at DESC, number`, string(userID))
	if err != nil {
		return nil, fmt.Errorf("list orders by user: %w", err)
	}
	defer rows.Close()

	orders := make([]domain.Order, 0)
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("read stored order: %w", err)
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read user orders: %w", err)
	}
	return orders, nil
}

func scanOrder(row pgx.Row) (domain.Order, error) {
	var number, userID, status string
	var uploadedAt time.Time
	if err := row.Scan(&number, &userID, &status, &uploadedAt); err != nil {
		return domain.Order{}, err
	}
	return domain.Restore(domain.Number(number), user.ID(userID), domain.Status(status), uploadedAt)
}
