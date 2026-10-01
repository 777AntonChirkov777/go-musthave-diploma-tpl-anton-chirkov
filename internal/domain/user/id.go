package user

import (
	"errors"
	"strings"
)

type ID string

var ErrInvalidID = errors.New("user ID must not be empty")

func ValidateID(id ID) error {
	if strings.TrimSpace(string(id)) == "" {
		return ErrInvalidID
	}
	return nil
}
