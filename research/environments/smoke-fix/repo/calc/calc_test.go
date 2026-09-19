package calc

import "testing"

func TestAverage(t *testing.T) {
	got := Average([]float64{1, 2, 3})
	if got != 2 {
		t.Fatalf("Average([1,2,3]) = %v, want 2", got)
	}
	if Average(nil) != 0 {
		t.Fatal("Average(nil) must be 0")
	}
}
