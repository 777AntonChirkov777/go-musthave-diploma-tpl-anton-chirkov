// Package memory provides volatile persistence adapters for development.
package memory

import (
	"context"
	"sort"
	"sync"

	application "diplom/internal/application/order"
	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

var _ application.Repository = (*OrderRepository)(nil)

type OrderRepository struct {
	mu     sync.RWMutex
	orders map[domain.Number]domain.Order
}

func NewOrderRepository() *OrderRepository {
	return &OrderRepository{orders: make(map[domain.Number]domain.Order)}
}

func (r *OrderRepository) Add(ctx context.Context, order domain.Order) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := r.orders[order.Number()]; exists {
		return domain.ErrAlreadyExists
	}
	if r.orders == nil {
		r.orders = make(map[domain.Number]domain.Order)
	}
	r.orders[order.Number()] = order
	return nil
}

func (r *OrderRepository) GetByNumber(ctx context.Context, number domain.Number) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	order, exists := r.orders[number]
	if !exists {
		return domain.Order{}, application.ErrNotFound
	}
	return order, nil
}

func (r *OrderRepository) ListByUser(ctx context.Context, userID user.ID) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	if err := ctx.Err(); err != nil {
		r.mu.RUnlock()
		return nil, err
	}
	orders := make([]domain.Order, 0)
	for _, order := range r.orders {
		if order.UserID() == userID {
			orders = append(orders, order)
		}
	}
	r.mu.RUnlock()
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].UploadedAt().Equal(orders[j].UploadedAt()) {
			return orders[i].Number() < orders[j].Number()
		}
		return orders[i].UploadedAt().After(orders[j].UploadedAt())
	})
	return orders, nil
}
