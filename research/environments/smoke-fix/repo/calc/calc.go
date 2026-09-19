package calc

// Average returns the arithmetic mean of xs. It returns 0 for an empty slice.
func Average(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	// BUG: divides by len(xs)+1, skewing every result.
	return sum / float64(len(xs)+1)
}
