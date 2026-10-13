package receipts

import (
	"fmt"
	"strconv"
	"strings"
)

// Line is one exported till line: "sku,price,qty".
type Line struct {
	SKU   string
	Price int // cents
	Qty   int
}

// ParseLine reads one exported till line.
func ParseLine(s string) (Line, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return Line{}, fmt.Errorf("line %q: want sku,price,qty", s)
	}
	price, err := ParsePrice(parts[1])
	if err != nil {
		return Line{}, err
	}
	qty, err := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err != nil {
		return Line{}, fmt.Errorf("line %q: %w", s, err)
	}
	return Line{SKU: strings.TrimSpace(parts[0]), Price: price, Qty: qty}, nil
}

// Total sums the receipt lines in cents.
func Total(lines []string) (int, error) {
	total := 0
	for _, s := range lines {
		l, err := ParseLine(s)
		if err != nil {
			return 0, err
		}
		total += l.Price * l.Qty
	}
	return total, nil
}
