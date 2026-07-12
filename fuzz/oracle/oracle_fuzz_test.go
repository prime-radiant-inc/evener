package oracle_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/oracle"
)

func FuzzOracleCombinators(f *testing.F) {
	for _, program := range []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12} {
		f.Add(program)
	}
	f.Fuzz(func(t *testing.T, program byte) {
		fuzzOracleProgram(t, program)
	})
}

type fuzzReporter struct {
	helpers int
	fatals  int
	message string
}

func (r *fuzzReporter) Helper() { r.helpers++ }

func (r *fuzzReporter) Fatalf(format string, args ...any) {
	r.fatals++
	r.message = format
}

func fuzzOracleProgram(t *testing.T, input byte) {
	t.Helper()
	program := input % 13
	r := &fuzzReporter{}
	wantFatal := 0

	switch program {
	case 0, 1, 2, 3:
		encCalls, decCalls := 0, 0
		enc := func(n int) (string, error) {
			encCalls++
			if program == 1 {
				return "", errors.New("encode")
			}
			return strconv.Itoa(n), nil
		}
		dec := func(s string) (int, error) {
			decCalls++
			if program == 2 {
				return 0, errors.New("decode")
			}
			n, err := strconv.Atoi(s)
			if program == 3 {
				n++
			}
			return n, err
		}
		oracle.RoundTrip(r, enc, dec, 37, func(a, b int) bool { return a == b })
		wantDecCalls := 1
		if program == 1 {
			wantDecCalls = 0
		}
		if encCalls != 1 || decCalls != wantDecCalls {
			t.Fatalf("RoundTrip call sequence: enc=%d dec=%d", encCalls, decCalls)
		}
		if program != 0 {
			wantFatal = 1
		}
	case 4, 5:
		calls := 0
		oracle.Deterministic(r, func(n int) int {
			calls++
			if program == 5 {
				return n + calls
			}
			return n * n
		}, 7, func(a, b int) bool { return a == b })
		if calls != 2 {
			t.Fatalf("Deterministic calls=%d, want 2", calls)
		}
		wantFatal = int(program - 4)
	case 6, 7:
		calls := 0
		oracle.Idempotent(r, func(s string) string {
			calls++
			if program == 7 {
				return s + "x"
			}
			return strings.TrimSpace(s)
		}, " value ", func(a, b string) bool { return a == b })
		if calls != 2 {
			t.Fatalf("Idempotent calls=%d, want 2", calls)
		}
		wantFatal = int(program - 6)
	case 8, 9:
		calls := 0
		oracle.Preserves(r, func(s string) string {
			calls++
			if program == 9 {
				return s[1:]
			}
			return reverseString(s)
		}, "abcd", func(s string) int { return len(s) })
		if calls != 1 {
			t.Fatalf("Preserves calls=%d, want 1", calls)
		}
		wantFatal = int(program - 8)
	case 10, 11:
		leftCalls, rightCalls := 0, 0
		left := func(n int) int { leftCalls++; return n * 2 }
		right := func(n int) int {
			rightCalls++
			if program == 11 {
				return n*2 + 1
			}
			return n + n
		}
		oracle.AgreesWith(r, left, right, 19, func(a, b int) bool { return a == b })
		if leftCalls != 1 || rightCalls != 1 {
			t.Fatalf("AgreesWith calls: left=%d right=%d", leftCalls, rightCalls)
		}
		wantFatal = int(program - 10)
	case 12:
		if !oracle.DeepEqual([]int{1, 2}, []int{1, 2}) || oracle.DeepEqual([]int{1, 2}, []int{2, 1}) {
			t.Fatal("DeepEqual disagrees with structural equality")
		}
		if r.helpers != 0 || r.fatals != 0 {
			t.Fatalf("DeepEqual unexpectedly used reporter: %+v", r)
		}
		return
	}

	if r.helpers != 1 {
		t.Fatalf("program %d: Helper calls=%d, want 1", program, r.helpers)
	}
	if r.fatals != wantFatal {
		t.Fatalf("program %d: Fatalf calls=%d, want %d", program, r.fatals, wantFatal)
	}
	if wantFatal == 1 && r.message == "" {
		t.Fatalf("program %d: Fatalf did not identify the violated oracle", program)
	}
}

func reverseString(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
