// Package decimal implements exact finite JSON decimals without expanding powers
// of ten. Arithmetic work and storage follow encoded mantissas and exponents.
package decimal

import (
	"fmt"
	"math/big"
	"strings"
)

// Number is an immutable decimal. Its zero value represents zero.
// A nonzero value is sign * coefficient * 10^exponent, with no leading or
// trailing coefficient zeros. No method mutates the exponent's shared limbs.
type Number struct {
	sign        int
	coefficient string
	exponent    big.Int
}

// Parse accepts exactly JSON number syntax. Exponents remain arbitrary signed
// integers, never native integers. Only encoded mantissa lengths adjust them.
func Parse(text string) (Number, error) {
	invalid := func() (Number, error) { return Number{}, fmt.Errorf("invalid JSON number %q", text) }
	if len(text) == 0 {
		return invalid()
	}
	i, sign := 0, 1
	if text[i] == '-' {
		sign = -1
		i++
		if i == len(text) {
			return invalid()
		}
	}
	start := i
	if text[i] == '0' {
		i++
	} else {
		if text[i] < '1' || text[i] > '9' {
			return invalid()
		}
		for i < len(text) && digit(text[i]) {
			i++
		}
	}
	integerEnd := i
	fracStart, fracEnd := i, i
	if i < len(text) && text[i] == '.' {
		i++
		fracStart = i
		for i < len(text) && digit(text[i]) {
			i++
		}
		fracEnd = i
		if fracStart == fracEnd {
			return invalid()
		}
	}
	expStart, expEnd := i, i
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		expStart = i
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		digitsStart := i
		for i < len(text) && digit(text[i]) {
			i++
		}
		if digitsStart == i {
			return invalid()
		}
		expEnd = i
	}
	if i != len(text) {
		return invalid()
	}
	coefficient := text[start:integerEnd] + text[fracStart:fracEnd]
	coefficient = strings.TrimLeft(coefficient, "0")
	if len(coefficient) == 0 {
		return Number{}, nil
	}
	var exponent big.Int
	if expStart != expEnd {
		if _, ok := exponent.SetString(text[expStart:expEnd], 10); !ok {
			return invalid()
		}
	}
	trimmed := strings.TrimRight(coefficient, "0")
	adjustment := len(coefficient) - len(trimmed) - (fracEnd - fracStart)
	exponent.Add(&exponent, big.NewInt(int64(adjustment)))
	return Number{sign, trimmed, exponent}, nil
}

func digit(c byte) bool { return c >= '0' && c <= '9' }

// Sign returns -1, 0, or +1.
func (n Number) Sign() int { return n.sign }

// IsInteger reports whether n is integral.
func (n Number) IsInteger() bool { return n.sign == 0 || n.exponent.Sign() >= 0 }

// Equal reports exact numeric equality.
func (n Number) Equal(other Number) bool {
	return n.sign == other.sign && n.coefficient == other.coefficient && n.exponent.Cmp(&other.exponent) == 0
}

// String returns compact canonical JSON number syntax, never implied zeros.
func (n Number) String() string {
	if n.sign == 0 {
		return "0"
	}
	prefix := ""
	if n.sign < 0 {
		prefix = "-"
	}
	if n.exponent.Sign() == 0 {
		return prefix + n.coefficient
	}
	return prefix + n.coefficient + "e" + n.exponent.String()
}
