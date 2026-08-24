package llm

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// TestJsonInt64FloatFallback covers the json.Number Int64 failure that
// succeeds as Float64 (line 108).
func TestJsonInt64FloatFallback(t *testing.T) {
	n := json.Number("1.5")
	got, ok := jsonInt64(n)
	if !ok || got != 1 {
		t.Fatalf("jsonInt64(1.5) = (%d, %t), want (1, true)", got, ok)
	}
}

// TestJsonInt64FloatOverflow covers the float64 overflow path (lines 116-117).
func TestJsonInt64FloatOverflow(t *testing.T) {
	n := math.MaxFloat64
	got, ok := jsonInt64(n)
	if ok {
		t.Fatalf("jsonInt64(MaxFloat64) = (%d, true), want (0, false)", got)
	}
}

// TestJsonInt64Int64Overflow covers the int64 overflow path (lines 120-121).
func TestJsonInt64Int64Overflow(t *testing.T) {
	maxSecs := int64(math.MaxInt64) / int64(time.Second)
	n := maxSecs + 1
	got, ok := jsonInt64(n)
	if ok {
		t.Fatalf("jsonInt64(overflow) = (%d, true), want (0, false)", got)
	}
}

// TestJsonInt64Int64NegativeOverflow covers the negative int64 overflow
// (line 123).
func TestJsonInt64Int64NegativeOverflow(t *testing.T) {
	maxSecs := int64(math.MaxInt64) / int64(time.Second)
	n := -maxSecs - 1
	got, ok := jsonInt64(n)
	if ok {
		t.Fatalf("jsonInt64(-overflow) = (%d, true), want (0, false)", got)
	}
}

// TestJsonInt64JsonNumberOverflow covers the json.Number overflow path
// (lines 110-111).
func TestJsonInt64JsonNumberOverflow(t *testing.T) {
	maxSecs := int64(math.MaxInt64) / int64(time.Second)
	n := json.Number("99999999999999999999999")
	got, ok := jsonInt64(n)
	if ok {
		t.Fatalf("jsonInt64(huge) = (%d, true), want (0, false)", got)
	}
	_ = maxSecs
}
