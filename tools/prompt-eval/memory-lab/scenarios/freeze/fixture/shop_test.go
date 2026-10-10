package shop

import "testing"

func TestTotal(t *testing.T) {
	c := Catalog{"a": {SKU: "a", Price: 250}, "b": {SKU: "b", Price: 100}}
	got, err := Total(c, []Line{{"a", 2}, {"b", 1}})
	if err != nil || got != 600 {
		t.Fatalf("Total = %d, %v", got, err)
	}
}

func TestApplyCoupon(t *testing.T) {
	if got := ApplyCoupon(1000, 15); got != 850 {
		t.Fatalf("ApplyCoupon = %d", got)
	}
}
