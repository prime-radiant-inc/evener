package receipts

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePrice turns a price as the till exports it ("12.05", "3") into cents.
func ParsePrice(s string) (int, error) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	dollars, err := strconv.Atoi(whole)
	if err != nil {
		return 0, fmt.Errorf("price %q: %w", s, err)
	}
	cents := 0
	if frac != "" {
		if len(frac) > 2 {
			return 0, fmt.Errorf("price %q: more than two decimal places", s)
		}
		cents, err = strconv.Atoi(frac)
		if err != nil {
			return 0, fmt.Errorf("price %q: %w", s, err)
		}
	}
	return dollars*100 + cents, nil
}

// FormatCents renders cents as dollars for the printed receipt.
func FormatCents(c int) string {
	return fmt.Sprintf("$%d.%02d", c/100, c%100)
}
