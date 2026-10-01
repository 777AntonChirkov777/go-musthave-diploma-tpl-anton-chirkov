package balance

import (
	"errors"
	"time"

	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

var (
	ErrInsufficientFunds  = errors.New("insufficient loyalty balance")
	ErrAlreadyWithdrawn   = errors.New("order number has already been paid with loyalty points")
	ErrInvalidProcessedAt = errors.New("withdrawal processing time must not be zero")
)

type Balance struct {
	Current   Amount
	Withdrawn Amount
}

type Withdrawal struct {
	number      order.Number
	userID      user.ID
	sum         Amount
	processedAt time.Time
}

func NewWithdrawal(number order.Number, userID user.ID, sum Amount, processedAt time.Time) (Withdrawal, error) {
	if _, err := order.ParseNumber(string(number)); err != nil {
		return Withdrawal{}, err
	}
	if err := user.ValidateID(userID); err != nil {
		return Withdrawal{}, err
	}
	if !sum.isPositive() {
		return Withdrawal{}, ErrInvalidSum
	}
	if processedAt.IsZero() {
		return Withdrawal{}, ErrInvalidProcessedAt
	}
	return Withdrawal{
		number:      number,
		userID:      userID,
		sum:         Amount{hundredths: sum.Hundredths()},
		processedAt: processedAt,
	}, nil
}

func (w Withdrawal) Number() order.Number   { return w.number }
func (w Withdrawal) UserID() user.ID        { return w.userID }
func (w Withdrawal) Sum() Amount            { return Amount{hundredths: w.sum.Hundredths()} }
func (w Withdrawal) ProcessedAt() time.Time { return w.processedAt }
