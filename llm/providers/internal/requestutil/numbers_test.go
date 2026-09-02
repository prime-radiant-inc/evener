package requestutil

import (
	"encoding/json"
	"testing"
)

func TestPositiveInt(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  int
	}{
		{name: "int", value: 7, want: 7},
		{name: "int64", value: int64(8), want: 8},
		{name: "float64", value: float64(9), want: 9},
		{name: "json number", value: json.Number("10"), want: 10},
		{name: "zero", value: 0},
		{name: "negative", value: -1},
		{name: "fraction below one", value: 0.5},
		{name: "non-integral json number", value: json.Number("1.5")},
		{name: "unknown", value: "11"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := PositiveInt(test.value); got != test.want {
				t.Fatalf("PositiveInt(%v) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestPositivePointerInt(t *testing.T) {
	if got := PositivePointerInt(nil); got != 0 {
		t.Fatalf("PositivePointerInt(nil) = %d, want 0", got)
	}
	for _, test := range []struct {
		value int
		want  int
	}{
		{value: -1, want: 0},
		{value: 0, want: 0},
		{value: 12, want: 12},
	} {
		if got := PositivePointerInt(&test.value); got != test.want {
			t.Fatalf("PositivePointerInt(%d) = %d, want %d", test.value, got, test.want)
		}
	}
}

func TestMinPositiveInt(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []int
		want   int
	}{
		{name: "empty"},
		{name: "no positive", values: []int{0, -2}},
		{name: "one positive", values: []int{4}, want: 4},
		{name: "mixed", values: []int{9, 0, 3, -1, 5}, want: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := MinPositiveInt(test.values...); got != test.want {
				t.Fatalf("MinPositiveInt(%v) = %d, want %d", test.values, got, test.want)
			}
		})
	}
}
