package user_test

import (
	"errors"
	"strings"
	"testing"

	"diplom/internal/domain/user"
)

func TestCredentials(t *testing.T) {
	for _, tc := range []struct {
		login, password string
		valid           bool
	}{
		{"alice", "a", true},
		{"логин", "пароль", true},
		{" alice ", " ", true},
		{"", "password", false},
		{" \t\n", "password", false},
		{"alice", "", false},
		{"ali\x00ce", "password", false},
		{strings.Repeat("я", 255), "password", true},
		{strings.Repeat("я", 256), "password", false},
	} {
		err := user.ValidateCredentials(tc.login, tc.password)
		if tc.valid && err != nil || !tc.valid && !errors.Is(err, user.ErrInvalidCredentials) {
			t.Errorf("ValidateCredentials(%q, %q) = %v, want valid %t", tc.login, tc.password, err, tc.valid)
		}
	}
}

func TestNewUser(t *testing.T) {
	got, err := user.New("id", " Alice ", "stored hash")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != "id" || got.Login() != " Alice " || got.PasswordHash() != "stored hash" {
		t.Fatalf("user does not preserve supplied values: %+v", got)
	}
	if _, err := user.New("", "alice", "hash"); !errors.Is(err, user.ErrInvalidID) {
		t.Errorf("empty ID error = %v", err)
	}
	if _, err := user.New("id", "alice", ""); !errors.Is(err, user.ErrInvalidCredentials) {
		t.Errorf("empty hash error = %v", err)
	}
}
