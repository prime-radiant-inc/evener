package turnwindow

import (
	"errors"
	"testing"
)

func TestParseSelectsTurnWindows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		spec               string
		count              int
		wantStart, wantEnd int
	}{
		{"last:3", 10, 7, 9},
		{"start:2", 10, 0, 1},
		{"2-5", 10, 2, 5},
		{"5-2", 10, 0, -1},   // N > M is valid and selects nothing
		{"100-99", 3, 0, -1}, // even when both ends are past the last turn
		{"2-99", 4, 2, 3},
		{"last:3", 0, 0, -1}, // an empty transcript is the empty range
	} {
		start, end, err := Parse(tc.spec, tc.count)
		if err != nil || start != tc.wantStart || end != tc.wantEnd {
			t.Errorf("Parse(%q, %d) = %d, %d, %v; want %d, %d", tc.spec, tc.count, start, end, err, tc.wantStart, tc.wantEnd)
		}
	}
	for _, bad := range []string{"", "start:0", "last:-1", "garbage", "1-"} {
		if _, _, err := Parse(bad, 10); !errors.Is(err, ErrMalformed) {
			t.Errorf("Parse(%q) error = %v, want ErrMalformed", bad, err)
		}
	}
}

func TestClamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		lo, hi, count  int
		wantLo, wantHi int
	}{
		{-5, 100, 10, 0, 9}, // both clamped to bounds
		{3, 7, 10, 3, 7},    // in range unchanged
		{20, 30, 10, 9, 9},  // lo past end clamped to last
		{5, -3, 10, 5, 0},   // hi negative clamped to 0
		{0, -1, 2, 0, 0},
		{0, 9, 2, 0, 1},
	}
	for _, tc := range tests {
		lo, hi := clamp(tc.lo, tc.hi, tc.count)
		if lo != tc.wantLo || hi != tc.wantHi {
			t.Errorf("clamp(%d,%d,%d) = %d,%d want %d,%d", tc.lo, tc.hi, tc.count, lo, hi, tc.wantLo, tc.wantHi)
		}
	}
}

func TestParseDash(t *testing.T) {
	t.Parallel()
	if lo, hi, ok := ParseDash("3-7"); !ok || lo != 3 || hi != 7 {
		t.Fatalf("3-7 => %d,%d,%v", lo, hi, ok)
	}
	for _, bad := range []string{"nodash", "none", "-5", "3-", "a-b", "-1-2", "3"} {
		if _, _, ok := ParseDash(bad); ok {
			t.Errorf("ParseDash(%q) unexpectedly ok", bad)
		}
	}
}
