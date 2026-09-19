The stats package in repo/stats/stats.go exists but is empty. Add an
exported function Median(xs []float64) float64 to package stats that
returns the median of xs (the middle element of the sorted slice for odd
length, the mean of the two middle elements for even length, and 0 for an
empty slice). The repository's own tests must keep passing.
