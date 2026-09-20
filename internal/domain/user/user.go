package user

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidCredentials = errors.New("invalid login or password")
	ErrLoginTaken         = errors.New("login already taken")
)

type User struct {
	id           ID
	login        string
	passwordHash string
}

func New(id ID, login, passwordHash string) (User, error) {
	if err := ValidateID(id); err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	if err := ValidateCredentials(login, passwordHash); err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return User{id: id, login: login, passwordHash: passwordHash}, nil
}

func ValidateCredentials(login, password string) error {
	if strings.TrimSpace(login) == "" || strings.ContainsRune(login, '\x00') || utf8.RuneCountInString(login) > 255 || password == "" {
		return ErrInvalidCredentials
	}
	return nil
}

func (u User) ID() ID               { return u.id }
func (u User) Login() string        { return u.login }
func (u User) PasswordHash() string { return u.passwordHash }
