package decimal

import "math/big"

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
	// Equal orders need only a virtual zero-padded comparison of encoded digits.
	for i := 0; i < len(n.coefficient) || i < len(other.coefficient); i++ {
		a, b := byte('0'), byte('0')
		if i < len(n.coefficient) {
			a = n.coefficient[i]
		}
		if i < len(other.coefficient) {
			b = other.coefficient[i]
		}
		if a < b {
			return -n.sign
		}
		if a > b {
			return n.sign
		}
	}
	return 0
}
