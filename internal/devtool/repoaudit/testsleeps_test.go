package repoaudit

import "testing"

func TestCountSleepsCountsCallsToTimeSleep(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"calls", `package x
import "time"
func TestA(t *testing.T) {
	time.Sleep(time.Millisecond)
	for range 3 { time.Sleep(2 * time.Millisecond) }
}`, 2},
		{"comments and strings are not calls", `package x
import "time"
// time.Sleep(time.Second) would flake.
var s = "time.Sleep(time.Second)"
var d = time.Second`, 0},
		{"a synctest file", `package x
import (
	"testing/synctest"
	"time"
)
func TestA(t *testing.T) { synctest.Test(t, func(*testing.T) { time.Sleep(time.Hour) }) }`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := countSleeps("x_test.go", []byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("countSleeps = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestJudgeSleepsRatchetsDownward pins the ratchet: a count above the baseline
// fails, an equal count is silent, and a lower count passes with a note to
// lower the baseline.
func TestJudgeSleepsRatchetsDownward(t *testing.T) {
	cases := []struct {
		name                    string
		count, baseline         int
		wantFindings, wantNotes int
	}{
		{"above", 5, 4, 1, 0},
		{"equal", 4, 4, 0, 0},
		{"below", 3, 4, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := runCheck(check{"test-sleeps", func(a *audit) { judgeSleeps(a, tc.count, tc.baseline) }})
			if len(a.findings) != tc.wantFindings || len(a.notes) != tc.wantNotes {
				t.Fatalf("findings %q, notes %q; want %d finding(s) and %d note(s)",
					a.findings, a.notes, tc.wantFindings, tc.wantNotes)
			}
		})
	}
}
