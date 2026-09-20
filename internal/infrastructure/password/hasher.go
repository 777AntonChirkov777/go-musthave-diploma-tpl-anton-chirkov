// Package password stores salted PBKDF2-SHA256 password hashes.
package password

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	application "diplom/internal/application/user"
)

const (
	iterations = 600000
	saltSize   = 16
	keySize    = 32
)

var (
	_              application.PasswordHasher = (*Hasher)(nil)
	ErrInvalidHash                            = errors.New("invalid password hash")
)

type Hasher struct{}

func NewHasher() *Hasher { return &Hasher{} }

func (h *Hasher) Hash(password string) (string, error) {
	var salt [saltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt[:], iterations, keySize)
	if err != nil {
		return "", fmt.Errorf("derive password hash: %w", err)
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations,
		base64.RawStdEncoding.EncodeToString(salt[:]), base64.RawStdEncoding.EncodeToString(key)), nil
}

func (h *Hasher) Compare(encoded, password string) (bool, error) {
	if len(encoded) > 256 {
		return false, ErrInvalidHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false, ErrInvalidHash
	}
	work, err := strconv.Atoi(parts[1])
	// Bound database-provided parameters before allocating or deriving a key.
	if err != nil || work < iterations || work > 2000000 {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(salt) < saltSize || len(salt) > 64 {
		return false, ErrInvalidHash
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[3])
	if err != nil || len(expected) != keySize {
		return false, ErrInvalidHash
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, work, len(expected))
	if err != nil {
		return false, fmt.Errorf("derive password hash: %w", err)
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}
