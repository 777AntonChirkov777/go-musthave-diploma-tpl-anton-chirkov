package password_test

import (
	"errors"
	"strings"
	"testing"

	"diplom/internal/infrastructure/password"
)

func TestHashUsesRandomSaltAndVerifiesExactPassword(t *testing.T) {
	hasher := password.NewHasher()
	const secret = "секрет с пробелами "
	first, err := hasher.Hash(secret)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hasher.Hash(secret)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, secret) || !strings.HasPrefix(first, "pbkdf2-sha256$600000$") {
		t.Fatalf("expected distinct salted hashes, got %q and %q", first, second)
	}
	for _, tc := range []struct {
		input string
		valid bool
	}{{secret, true}, {strings.TrimSpace(secret), false}, {"wrong", false}} {
		valid, err := hasher.Compare(first, tc.input)
		if err != nil || valid != tc.valid {
			t.Errorf("Compare valid = %t, err = %v, want %t", valid, err, tc.valid)
		}
	}
}

func TestCompareRejectsMalformedAndUnboundedHashes(t *testing.T) {
	hasher := password.NewHasher()
	const salt = "AAAAAAAAAAAAAAAAAAAAAA"
	const key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, encoded := range []string{
		"", "plaintext-password", "pbkdf2-sha512$600000$" + salt + "$" + key,
		"pbkdf2-sha256$0$" + salt + "$" + key,
		"pbkdf2-sha256$-1$" + salt + "$" + key,
		"pbkdf2-sha256$99999999999999999$" + salt + "$" + key,
		"pbkdf2-sha256$NaN$" + salt + "$" + key,
		"pbkdf2-sha256$600000$short$" + key,
		"pbkdf2-sha256$600000$" + salt + "$short",
		"pbkdf2-sha256$600000$!" + salt + "$" + key,
		"pbkdf2-sha256$600000$" + salt + "$!" + key,
		"pbkdf2-sha256$600000$" + salt + "$" + key + "$extra",
		strings.Repeat("x", 257),
	} {
		if valid, err := hasher.Compare(encoded, "password"); valid || !errors.Is(err, password.ErrInvalidHash) {
			t.Errorf("Compare(%q) = %t, %v", encoded, valid, err)
		}
	}
}

func TestCompareIndependentPBKDF2Vector(t *testing.T) {
	// Expected key generated independently using Python hashlib.pbkdf2_hmac:
	// SHA-256, password "password", salt "0123456789abcdef", 600000 iterations.
	const encoded = "pbkdf2-sha256$600000$MDEyMzQ1Njc4OWFiY2RlZg$mW18kPdKShac963vQrBoSPfRusPlaNHMlNT3m+HuAmM"
	valid, err := password.NewHasher().Compare(encoded, "password")
	if err != nil || !valid {
		t.Fatalf("Compare independent vector = %t, %v", valid, err)
	}
}
