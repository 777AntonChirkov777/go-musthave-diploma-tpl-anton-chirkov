package balance

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

const maxSumIntegerDigits = 18

var (
	ErrInvalidSum    = errors.New("withdrawal sum must be a positive number with at most two decimal places and below 10^18")
	ErrInvalidAmount = errors.New("amount must be a nonnegative number of hundredths")
)

var hundred = big.NewInt(100)

type Amount struct {
	hundredths *big.Int
}

func ParseSum(text string) (Amount, error) {
	mantissa, exponentText, hasExponent := strings.Cut(strings.ToLower(text), "e")
	integerPart, fractionPart, hasFraction := strings.Cut(mantissa, ".")
	if !isDigits(integerPart) || (len(integerPart) > 1 && integerPart[0] == '0') {
		return Amount{}, ErrInvalidSum
	}
	if hasFraction && !isDigits(fractionPart) {
		return Amount{}, ErrInvalidSum
	}
	exponent := int64(0)
	if hasExponent {
		parsed, ok := parseExponent(exponentText)
		if !ok {
			return Amount{}, ErrInvalidSum
		}
		exponent = parsed
	}
	exponent -= int64(len(fractionPart))

	digits := strings.TrimLeft(integerPart+fractionPart, "0")
	if digits == "" {
		return Amount{}, ErrInvalidSum
	}
	significant := strings.TrimRight(digits, "0")
	exponent += int64(len(digits) - len(significant))
	if exponent < -2 || int64(len(significant))+exponent > maxSumIntegerDigits {
		return Amount{}, ErrInvalidSum
	}
	hundredths, ok := new(big.Int).SetString(significant+strings.Repeat("0", int(exponent+2)), 10)
	if !ok {
		return Amount{}, ErrInvalidSum
	}
	return Amount{hundredths: hundredths}, nil
}

func parseExponent(text string) (int64, bool) {
	sign := int64(1)
	switch {
	case strings.HasPrefix(text, "+"):
		text = text[1:]
	case strings.HasPrefix(text, "-"):
		sign = -1
		text = text[1:]
	}
	if !isDigits(text) {
		return 0, false
	}
	if text = strings.TrimLeft(text, "0"); text == "" {
		return 0, true
	}
	if len(text) > maxSumIntegerDigits {
		return 0, false
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return sign * value, true
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

func AmountFromHundredths(value *big.Int) (Amount, error) {
	if value == nil || value.Sign() < 0 {
		return Amount{}, ErrInvalidAmount
	}
	return Amount{hundredths: new(big.Int).Set(value)}, nil
}

func (a Amount) Hundredths() *big.Int {
	if a.hundredths == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(a.hundredths)
}

func (a Amount) Compact() string {
	return strings.TrimSuffix(strings.TrimRight(a.String(), "0"), ".")
}

func (a Amount) String() string {
	units, cents := new(big.Int).QuoRem(a.Hundredths(), hundred, new(big.Int))
	return fmt.Sprintf("%s.%02d", units, cents.Int64())
}

func (a Amount) isPositive() bool {
	return a.hundredths != nil && a.hundredths.Sign() > 0
}
