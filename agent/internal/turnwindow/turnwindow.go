// Package turnwindow parses the turn-window grammar shared by read_transcript
// and doctor transcript, so a range written for one selects the same turns in
// the other.
package turnwindow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Grammar is the accepted range syntax, for messages that name it.
const Grammar = "N-M | last:N | start:N"

// ErrMalformed is wrapped by Parse for a syntactically malformed spec.
var ErrMalformed = errors.New("malformed range")

// Parse resolves spec to inclusive [start, end] turn numbers over count turns,
// clamped to valid bounds. An empty turn list is not malformed; it yields
// (0, -1). Callers give the empty spec their own default before calling.
//
// Grammar:
//   - "last:N"   → the last N turns (N must be a positive integer).
//   - "start:N"  → the first N turns (N must be a positive integer).
//   - "N-M"      → turns N..M inclusive (N, M non-negative integers).
//
// "N-M" with N > M is syntactically valid; it clamps to an empty range rather
// than erroring.
func Parse(spec string, count int) (start, end int, err error) {
	if count <= 0 {
		return 0, -1, nil
	}
	last := count - 1

	switch {
	case strings.HasPrefix(spec, "last:"):
		n, ok := ParsePositiveInt(strings.TrimPrefix(spec, "last:"))
		if !ok {
			return 0, 0, fmt.Errorf("%w: %q", ErrMalformed, spec)
		}
		start, end = clamp(count-n, last, count)
		return start, end, nil

	case strings.HasPrefix(spec, "start:"):
		n, ok := ParsePositiveInt(strings.TrimPrefix(spec, "start:"))
		if !ok {
			return 0, 0, fmt.Errorf("%w: %q", ErrMalformed, spec)
		}
		start, end = clamp(0, n-1, count)
		return start, end, nil

	case strings.Contains(spec, "-"):
		lo, hi, ok := ParseDash(spec)
		if !ok {
			return 0, 0, fmt.Errorf("%w: %q", ErrMalformed, spec)
		}
		start, end = clamp(lo, hi, count)
		return start, end, nil

	default:
		return 0, 0, fmt.Errorf("%w: %q", ErrMalformed, spec)
	}
}

// clamp clamps [lo, hi] to [0, count-1] and returns it as inclusive bounds. A
// resulting lo > hi denotes an empty selection.
func clamp(lo, hi, count int) (start, end int) {
	if lo < 0 {
		lo = 0
	}
	if hi > count-1 {
		hi = count - 1
	}
	if hi < 0 {
		hi = 0
	}
	if lo > count-1 {
		lo = count - 1
	}
	return lo, hi
}

// ParsePositiveInt parses s as a strictly positive base-10 integer.
func ParsePositiveInt(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ParseDash parses an "N-M" spec into non-negative integers N and M. Both
// operands must be present and non-negative; either side missing or
// non-numeric is rejected.
func ParseDash(spec string) (lo, hi int, ok bool) {
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(parts[0])
	hi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || lo < 0 || hi < 0 {
		return 0, 0, false
	}
	return lo, hi, true
}
