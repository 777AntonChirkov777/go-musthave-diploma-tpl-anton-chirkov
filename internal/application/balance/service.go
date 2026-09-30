package balance

import (
	"context"
	"fmt"
	"time"

	domain "diplom/internal/domain/balance"
	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

type Repository interface {
	GetBalance(context.Context, user.ID) (domain.Balance, error)
	Withdraw(context.Context, domain.Withdrawal) error
	ListWithdrawals(context.Context, user.ID) ([]domain.Withdrawal, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) Balance(ctx context.Context, userID user.ID) (domain.Balance, error) {
	if err := ctx.Err(); err != nil {
		return domain.Balance{}, err
	}
	if err := user.ValidateID(userID); err != nil {
		return domain.Balance{}, err
	}
	current, err := s.repository.GetBalance(ctx, userID)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("get balance: %w", err)
	}
	return current, nil
}

func (s *Service) Withdraw(ctx context.Context, userID user.ID, rawOrder, rawSum string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := user.ValidateID(userID); err != nil {
		return err
	}
	number, err := order.ParseNumber(rawOrder)
	if err != nil {
		return err
	}
	sum, err := domain.ParseSum(rawSum)
	if err != nil {
		return err
	}
	withdrawal, err := domain.NewWithdrawal(number, userID, sum, s.now())
	if err != nil {
		return err
	}
	if err := s.repository.Withdraw(ctx, withdrawal); err != nil {
		return fmt.Errorf("withdraw: %w", err)
	}
	return nil
}

func (s *Service) Withdrawals(ctx context.Context, userID user.ID) ([]domain.Withdrawal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := user.ValidateID(userID); err != nil {
		return nil, err
	}
	withdrawals, err := s.repository.ListWithdrawals(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list withdrawals: %w", err)
	}
	return withdrawals, nil
}
