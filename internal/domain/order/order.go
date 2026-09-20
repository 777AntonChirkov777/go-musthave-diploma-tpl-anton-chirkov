package order

import (
	"errors"
	"fmt"
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
)

type Order struct {
	number     Number
	userID     user.ID
	status     Status
	uploadedAt time.Time
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
