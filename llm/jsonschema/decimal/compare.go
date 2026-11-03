package decimal

import (
	"math/big"
	"strings"
)

// Cmp returns -1, 0, or +1 according to the exact ordering of n and other.
func (n Number) Cmp(other Number) int {
	if n.sign < other.sign {
		return -1
	}
	if n.sign > other.sign {
		return 1
	}
	if n.sign == 0 {
		return 0
	}
	// Decimal order uses exponent-sized arithmetic, not exponent-sized padding.
	order := new(big.Int).Add(&n.exponent, big.NewInt(int64(len(n.coefficient))))
	otherOrder := new(big.Int).Add(&other.exponent, big.NewInt(int64(len(other.coefficient))))
	if cmp := order.Cmp(otherOrder); cmp != 0 {
		return n.sign * cmp
	}
	// Normalized coefficients have no trailing zeros. If one is a proper prefix,
	// the longer coefficient's suffix contains a nonzero digit, so lexical order
	// matches zero-padding.
	return n.sign * strings.Compare(n.coefficient, other.coefficient)
}
