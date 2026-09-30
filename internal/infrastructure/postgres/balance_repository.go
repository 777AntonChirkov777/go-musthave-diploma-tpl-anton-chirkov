package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"time"

	application "diplom/internal/application/balance"
	domain "diplom/internal/domain/balance"
	"diplom/internal/domain/order"
	"diplom/internal/domain/user"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ application.Repository = (*BalanceRepository)(nil)

const balanceQuery = `
	WITH accrued AS (
		SELECT COALESCE(SUM(accrual), 0) AS total
		FROM orders
		WHERE user_id = $1 AND status = 'PROCESSED' AND accrual IS NOT NULL
	), withdrawn AS (
		SELECT COALESCE(SUM(amount), 0) AS total
		FROM withdrawals
		WHERE user_id = $1
	)
	SELECT accrued.total - withdrawn.total, withdrawn.total FROM accrued, withdrawn`

type BalanceRepository struct {
	pool *pgxpool.Pool
}

func NewBalanceRepository(pool *pgxpool.Pool) *BalanceRepository {
	return &BalanceRepository{pool: pool}
}

func (r *BalanceRepository) GetBalance(ctx context.Context, userID user.ID) (domain.Balance, error) {
	current, withdrawn, err := scanBalance(r.pool.QueryRow(ctx, balanceQuery, string(userID)))
	if err != nil {
		return domain.Balance{}, fmt.Errorf("query balance: %w", err)
	}
	result := domain.Balance{}
	if result.Current, err = amountFromNumeric(current); err != nil {
		return domain.Balance{}, fmt.Errorf("read current balance: %w", err)
	}
	if result.Withdrawn, err = amountFromNumeric(withdrawn); err != nil {
		return domain.Balance{}, fmt.Errorf("read withdrawn total: %w", err)
	}
	return result, nil
}

func (r *BalanceRepository) Withdraw(ctx context.Context, withdrawal domain.Withdrawal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := domain.NewWithdrawal(withdrawal.Number(), withdrawal.UserID(), withdrawal.Sum(), withdrawal.ProcessedAt()); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(withdrawal.Number()))
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin withdrawal: %w", err)
	}
	defer tx.Rollback(ctx)

	var locked int
	if err := tx.QueryRow(ctx,
		"SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE", string(withdrawal.UserID()),
	).Scan(&locked); err != nil {
		return fmt.Errorf("lock withdrawal owner: %w", err)
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO withdrawals (number_hash, number, user_id, amount, processed_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (number_hash) DO NOTHING`,
		hash[:], string(withdrawal.Number()), string(withdrawal.UserID()), numericFromAmount(withdrawal.Sum()), withdrawal.ProcessedAt(),
	)
	if err != nil {
		return fmt.Errorf("insert withdrawal: %w", err)
	}
	if result.RowsAffected() == 0 {
		var existingNumber string
		if err := tx.QueryRow(ctx,
			"SELECT number FROM withdrawals WHERE number_hash = $1", hash[:],
		).Scan(&existingNumber); err != nil {
			return fmt.Errorf("read conflicting withdrawal: %w", err)
		}
		if existingNumber != string(withdrawal.Number()) {
			return errors.New("withdrawal number hash collision")
		}
		return domain.ErrAlreadyWithdrawn
	}
	current, _, err := scanBalance(tx.QueryRow(ctx, balanceQuery, string(withdrawal.UserID())))
	if err != nil {
		return fmt.Errorf("check balance: %w", err)
	}
	if current.Int.Sign() < 0 {
		return domain.ErrInsufficientFunds
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit withdrawal: %w", err)
	}
	return nil
}

func (r *BalanceRepository) ListWithdrawals(ctx context.Context, userID user.ID) ([]domain.Withdrawal, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT number, user_id, amount, processed_at
		FROM withdrawals WHERE user_id = $1 ORDER BY processed_at DESC, number`, string(userID))
	if err != nil {
		return nil, fmt.Errorf("list withdrawals by user: %w", err)
	}
	defer rows.Close()

	withdrawals := make([]domain.Withdrawal, 0)
	for rows.Next() {
		var number, owner string
		var amount pgtype.Numeric
		var processedAt time.Time
		if err := rows.Scan(&number, &owner, &amount, &processedAt); err != nil {
			return nil, fmt.Errorf("scan withdrawal: %w", err)
		}
		sum, err := amountFromNumeric(amount)
		if err != nil {
			return nil, fmt.Errorf("read withdrawal amount: %w", err)
		}
		withdrawal, err := domain.NewWithdrawal(order.Number(number), user.ID(owner), sum, processedAt)
		if err != nil {
			return nil, fmt.Errorf("read stored withdrawal: %w", err)
		}
		withdrawals = append(withdrawals, withdrawal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read user withdrawals: %w", err)
	}
	return withdrawals, nil
}

func scanBalance(row pgx.Row) (pgtype.Numeric, pgtype.Numeric, error) {
	var current, withdrawn pgtype.Numeric
	if err := row.Scan(&current, &withdrawn); err != nil {
		return pgtype.Numeric{}, pgtype.Numeric{}, err
	}
	for _, value := range []pgtype.Numeric{current, withdrawn} {
		if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite || value.Int == nil {
			return pgtype.Numeric{}, pgtype.Numeric{}, errors.New("balance is not a finite number")
		}
	}
	return current, withdrawn, nil
}

func numericFromAmount(amount domain.Amount) pgtype.Numeric {
	return pgtype.Numeric{Int: amount.Hundredths(), Exp: -2, Valid: true}
}

func amountFromNumeric(value pgtype.Numeric) (domain.Amount, error) {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite || value.Int == nil {
		return domain.Amount{}, errors.New("amount is not a finite number")
	}
	hundredths := new(big.Int).Set(value.Int)
	switch scale := int64(value.Exp) + 2; {
	case scale > 0:
		hundredths.Mul(hundredths, new(big.Int).Exp(big.NewInt(10), big.NewInt(scale), nil))
	case scale < 0:
		remainder := new(big.Int)
		hundredths.QuoRem(hundredths, new(big.Int).Exp(big.NewInt(10), big.NewInt(-scale), nil), remainder)
		if remainder.Sign() != 0 {
			return domain.Amount{}, errors.New("amount has more than two decimal places")
		}
	}
	return domain.AmountFromHundredths(hundredths)
}
