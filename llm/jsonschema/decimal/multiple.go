package decimal

import "math/big"

// MultipleOf reports whether n/divisor is integral. Nonpositive divisors always
// return false, including for a zero instance. Negative instances use magnitude.
func (n Number) MultipleOf(divisor Number) bool {
	if divisor.sign <= 0 {
		return false
	}
	if n.sign == 0 {
		return true
	}
	// A smaller normalized exponent would require the instance coefficient to
	// contain a factor of ten. Normalization has removed all such factors.
	if n.exponent.Cmp(&divisor.exponent) < 0 {
		return false
	}
	if divisor.coefficient == "1" {
		return true
	}
	a, u, v := factor(n.coefficient)
	b, r, s := factor(divisor.coefficient)
	if new(big.Int).Rem(a, b).Sign() != 0 {
		return false
	}
	d := new(big.Int).Sub(&n.exponent, &divisor.exponent)
	two := new(big.Int).Add(d, big.NewInt(u-r))
	five := new(big.Int).Add(d, big.NewInt(v-s))
	return two.Sign() >= 0 && five.Sign() >= 0
}

// factor removes only 2 and 5. All integer receivers here are fresh and sized
// by encoded coefficients. Each successful iteration shrinks the coefficient,
// so no loop, power, allocation or conversion follows exponent magnitude.
func factor(coefficient string) (core *big.Int, twos, fives int64) {
	core, _ = new(big.Int).SetString(coefficient, 10)
	zeros := core.TrailingZeroBits()
	twos = int64(zeros)
	core.Rsh(core, zeros)
	five := big.NewInt(5)
	quotient, remainder := new(big.Int), new(big.Int)
	for {
		quotient.QuoRem(core, five, remainder)
		if remainder.Sign() != 0 {
			break
		}
		core.Set(quotient)
		fives++
	}
	return
}
