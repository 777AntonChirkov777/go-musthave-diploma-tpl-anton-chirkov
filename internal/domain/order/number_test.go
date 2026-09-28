package order_test

import (
	"errors"
	"strings"
	"testing"

	"diplom/internal/domain/order"
)

func TestParseNumber(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "odd length", value: "12345678903", valid: true},
		{name: "even length", value: "4532015112830366", valid: true},
		{name: "single zero", value: "0", valid: true},
		{name: "leading zeros", value: "00012345678903", valid: true},
		{name: "larger than uint64", value: strings.Repeat("18", 12), valid: true},
		{name: "thousands of digits", value: strings.Repeat("18", 2048), valid: true},
		{name: "empty"},
		{name: "single nonzero digit", value: "1"},
		{name: "invalid odd checksum", value: "12345678904"},
		{name: "invalid even checksum", value: "4532015112830367"},
		{name: "invalid long checksum", value: strings.Repeat("18", 2047) + "19"},
		{name: "letters", value: "1234567890a"},
		{name: "full width digits", value: "１２３４５６７８９０３"},
		{name: "Arabic zero", value: "٠"},
		{name: "leading space", value: " 12345678903"},
		{name: "trailing newline", value: "12345678903\n"},
		{name: "embedded tab", value: "12345\t678903"},
		{name: "plus sign", value: "+12345678903"},
		{name: "minus sign", value: "-12345678903"},
		{name: "decimal separator", value: "1234567890.3"},
		{name: "null byte", value: "12345678903\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := order.ParseNumber(tc.value)
			if tc.valid {
				if err != nil || string(got) != tc.value {
					t.Fatalf("ParseNumber must preserve all %d digits: length %d, error %v", len(tc.value), len(got), err)
				}
			} else if got != "" || !errors.Is(err, order.ErrInvalidNumber) {
				t.Fatalf("ParseNumber = %q, %v, want empty number and ErrInvalidNumber", got, err)
			}
		})
	}
}
