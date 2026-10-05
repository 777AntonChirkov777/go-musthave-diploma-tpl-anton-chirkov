package order

import "errors"

type Number string

const MaxNumberLength = 255

var ErrInvalidNumber = errors.New("order number must contain 1 to 255 digits and pass the Luhn check")

func ParseNumber(value string) (Number, error) {
	if value == "" || len(value) > MaxNumberLength {
		return "", ErrInvalidNumber
	}

	checksum := 0
	double := false
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] < '0' || value[i] > '9' {
			return "", ErrInvalidNumber
		}
		digit := int(value[i] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		checksum = (checksum + digit) % 10
		double = !double
	}
	if checksum != 0 {
		return "", ErrInvalidNumber
	}
	return Number(value), nil
}
