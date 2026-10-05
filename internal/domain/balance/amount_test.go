package balance_test

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"diplom/internal/domain/balance"
)

func TestParseSumAcceptsExactHundredths(t *testing.T) {
	for _, tc := range []struct {
		text       string
		hundredths string
		formatted  string
	}{
		{text: "751", hundredths: "75100", formatted: "751.00"},
		{text: "751.5", hundredths: "75150", formatted: "751.50"},
		{text: "751.50", hundredths: "75150", formatted: "751.50"},
		{text: "7.51e2", hundredths: "75100", formatted: "751.00"},
		{text: "75100e-2", hundredths: "75100", formatted: "751.00"},
		{text: "1E2", hundredths: "10000", formatted: "100.00"},
		{text: "1e+2", hundredths: "10000", formatted: "100.00"},
		{text: "0.01", hundredths: "1", formatted: "0.01"},
		{text: "0.10000", hundredths: "10", formatted: "0.10"},
		{text: "1e0000000000000000000002", hundredths: "10000", formatted: "100.00"},
		{text: "999999999999999999.99", hundredths: "99999999999999999999", formatted: "999999999999999999.99"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := balance.ParseSum(tc.text)
			if err != nil {
				t.Fatalf("ParseSum(%q) error = %v", tc.text, err)
			}
			if got.Hundredths().String() != tc.hundredths || got.String() != tc.formatted {
				t.Fatalf("ParseSum(%q) = %s hundredths, %q; want %s, %q", tc.text, got.Hundredths(), got.String(), tc.hundredths, tc.formatted)
			}
		})
	}
}

func TestParseSumRejectsInvalidValues(t *testing.T) {
	for _, text := range []string{
		"",
		"0",
		"-0",
		"-1",
		"0.00",
		"0e5",
		"0.001",
		"1e-3",
		"751.555",
		"1e18",
		"1000000000000000000",
		"999999999999999999.999",
		"1e1000000000",
		"1e-99999999999999999999",
		"1e99999999999999999999",
		"+1",
		".5",
		"5.",
		"01",
		"1e",
		"1e+",
		"1.5.5",
		"1e5e5",
		"NaN",
		"Infinity",
		" 1",
		"1 ",
		"0x10",
		"\"751\"",
	} {
		t.Run(text, func(t *testing.T) {
			if got, err := balance.ParseSum(text); !errors.Is(err, balance.ErrInvalidSum) {
				t.Fatalf("ParseSum(%q) = %s, %v; want ErrInvalidSum", text, got, err)
			}
		})
	}
}

func TestParseSumHugeExponentIsCheap(t *testing.T) {
	allocations := testing.AllocsPerRun(10, func() {
		if _, err := balance.ParseSum("1e1000000000"); !errors.Is(err, balance.ErrInvalidSum) {
			t.Fatal(err)
		}
	})
	if allocations > 10 {
		t.Fatalf("ParseSum with a huge exponent made %v allocations", allocations)
	}
	started := time.Now()
	if _, err := balance.ParseSum("0." + strings.Repeat("0", 1<<20) + "1e1048578"); err != nil {
		t.Fatalf("long exact mantissa error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("ParseSum on a 1 MiB mantissa took %v", elapsed)
	}
}

func TestAmountConversions(t *testing.T) {
	for _, tc := range []struct {
		text    string
		compact string
	}{
		{text: "500.25", compact: "500.25"},
		{text: "500.50", compact: "500.5"},
		{text: "0.1", compact: "0.1"},
		{text: "0.01", compact: "0.01"},
		{text: "42", compact: "42"},
		{text: "100", compact: "100"},
		{text: "7.51e2", compact: "751"},
		{text: "12345678901234567.89", compact: "12345678901234567.89"},
		{text: "999999999999999999.99", compact: "999999999999999999.99"},
	} {
		amount, err := balance.ParseSum(tc.text)
		if err != nil {
			t.Fatal(err)
		}
		if amount.Compact() != tc.compact {
			t.Fatalf("Compact(%s) = %q, want %q", tc.text, amount.Compact(), tc.compact)
		}
	}
	var zero balance.Amount
	if zero.Compact() != "0" || zero.String() != "0.00" || zero.Hundredths().Sign() != 0 {
		t.Fatalf("zero amount = %q, %q, %s", zero.Compact(), zero.String(), zero.Hundredths())
	}
}

func TestAmountFromHundredths(t *testing.T) {
	source := big.NewInt(75825)
	amount, err := balance.AmountFromHundredths(source)
	if err != nil {
		t.Fatal(err)
	}
	source.SetInt64(1)
	copied := amount.Hundredths()
	copied.SetInt64(2)
	if amount.String() != "758.25" {
		t.Fatalf("amount shares caller-owned integers: %s", amount)
	}
	if zero, err := balance.AmountFromHundredths(big.NewInt(0)); err != nil || zero.String() != "0.00" {
		t.Fatalf("zero hundredths = %s, %v", zero, err)
	}
	for _, value := range []*big.Int{nil, big.NewInt(-1)} {
		if _, err := balance.AmountFromHundredths(value); !errors.Is(err, balance.ErrInvalidAmount) {
			t.Fatalf("AmountFromHundredths(%v) error = %v, want ErrInvalidAmount", value, err)
		}
	}
}
