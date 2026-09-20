package order

import (
	"errors"
	"fmt"
	"math"
	"time"

	"diplom/internal/domain/user"
)

type Status string

const (
	StatusNew        Status = "NEW"
	StatusProcessing Status = "PROCESSING"
	StatusInvalid    Status = "INVALID"
	StatusProcessed  Status = "PROCESSED"
)

var (
	ErrAlreadyExists     = errors.New("order already exists")
	ErrInvalidUploadedAt = errors.New("order upload time must not be zero")
	ErrInvalidStatus     = errors.New("invalid order status")
	ErrInvalidAccrual    = errors.New("order accrual must be finite and nonnegative")
)

type Order struct {
	number     Number
	userID     user.ID
	status     Status
	uploadedAt time.Time
	accrual    float64
	hasAccrual bool
}

func New(number Number, userID user.ID, uploadedAt time.Time) (Order, error) {
	return Restore(number, userID, StatusNew, uploadedAt)
}

func Restore(number Number, userID user.ID, status Status, uploadedAt time.Time) (Order, error) {
	if _, err := ParseNumber(string(number)); err != nil {
		return Order{}, fmt.Errorf("restore order: %w", err)
	}
	if err := user.ValidateID(userID); err != nil {
		return Order{}, fmt.Errorf("restore order: %w", err)
	}
	if uploadedAt.IsZero() {
		return Order{}, ErrInvalidUploadedAt
	}
	switch status {
	case StatusNew, StatusProcessing, StatusInvalid, StatusProcessed:
	default:
		return Order{}, ErrInvalidStatus
	}
	return Order{
		number:     number,
		userID:     userID,
		status:     status,
		uploadedAt: uploadedAt,
	}, nil
}

func (o Order) Number() Number        { return o.number }
func (o Order) UserID() user.ID       { return o.userID }
func (o Order) Status() Status        { return o.status }
func (o Order) UploadedAt() time.Time { return o.uploadedAt }

// Accrual distinguishes an unknown reward from a calculated reward of zero.
func (o Order) Accrual() (float64, bool) { return o.accrual, o.hasAccrual }

func (o Order) WithAccrual(value float64) (Order, error) {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return Order{}, ErrInvalidAccrual
	}
	o.accrual = value
	o.hasAccrual = true
	return o, nil
}
