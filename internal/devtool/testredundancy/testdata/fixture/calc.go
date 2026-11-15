// Package calc is the fixture package test-redundancy's tests profile.
package calc

// Abs returns the absolute value of x.
func Abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Sign returns -1, 0 or 1 by the sign of x.
func Sign(x int) int {
	if x < 0 {
		return -1
	}
	if x > 0 {
		return 1
	}
	return 0
}
