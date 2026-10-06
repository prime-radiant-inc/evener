package shop

// ApplyCoupon takes percent off total, rounding down.
func ApplyCoupon(total, percent int) int {
	return total - total*percent/100
}
