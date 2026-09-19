package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

var (
	ErrNotFound           = errors.New("order not found")
	ErrOwnedByAnotherUser = errors.New("order belongs to another user")
)

type Repository interface {
	Add(context.Context, domain.Order) error
	GetByNumber(context.Context, domain.Number) (domain.Order, error)
	ListByUser(context.Context, user.ID) ([]domain.Order, error)
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

func (s *Service) Submit(ctx context.Context, userID user.ID, rawNumber string) (domain.Order, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, false, err
	}
	if err := user.ValidateID(userID); err != nil {
		return domain.Order{}, false, err
	}
	number, err := domain.ParseNumber(rawNumber)
	if err != nil {
		return domain.Order{}, false, err
	}
	submitted, err := domain.New(number, userID, s.now())
	if err != nil {
		return domain.Order{}, false, err
	}
	if err := s.repository.Add(ctx, submitted); err != nil {
		if !errors.Is(err, domain.ErrAlreadyExists) {
			return domain.Order{}, false, fmt.Errorf("add order: %w", err)
		}
		existing, err := s.repository.GetByNumber(ctx, number)
		if err != nil {
			return domain.Order{}, false, fmt.Errorf("get existing order: %w", err)
		}
		if existing.UserID() != userID {
			return domain.Order{}, false, ErrOwnedByAnotherUser
		}
		return existing, false, nil
	}
	return submitted, true, nil
}

func (s *Service) List(ctx context.Context, userID user.ID) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := user.ValidateID(userID); err != nil {
		return nil, err
	}
	orders, err := s.repository.ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	return orders, nil
}
