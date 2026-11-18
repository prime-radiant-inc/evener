package dev

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/internal/devtool/mergeq"
)

// stubQueue is the data seam the subcommand's contract is pinned through: it
// answers the same questions GitHub would, and the real argument parsing runs
// against it. Nothing stands in for `gh` on PATH — the decisions and their
// client are both in-process.
type stubQueue struct {
	status     mergeq.Status
	statuses   []mergeq.Status
	drop       *mergeq.DropEvent
	runs       []mergeq.GroupRun
	enqueueErr error
	enqueued   []string
}

func (s *stubQueue) Status(context.Context, string, string) (mergeq.Status, error) {
	if len(s.statuses) == 0 {
		return s.status, nil
	}
	st := s.statuses[0]
	if len(s.statuses) > 1 {
		s.statuses = s.statuses[1:]
	}
	return st, nil
}

func (s *stubQueue) MergedCommit(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

func (s *stubQueue) Enqueue(_ context.Context, _, prID string, _ bool) error {
	if s.enqueueErr != nil {
		return s.enqueueErr
	}
	s.enqueued = append(s.enqueued, prID)
	return nil
}

func (s *stubQueue) QueueDrop(context.Context, string, string) (*mergeq.DropEvent, error) {
	return s.drop, nil
}

func (s *stubQueue) MergeGroupRuns(context.Context, string) ([]mergeq.GroupRun, error) {
	return s.runs, nil
}

// pausedClock is the wait's clock with no wall time in it: Sleep advances Now,
// so a wait that must time out costs nothing and fires exactly.
func pausedClock() mergeq.Clock {
	var now time.Time
	return mergeq.Clock{
		Now:   func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
}

// entry is a queued PR's queue entry.
func entry(position int64) *mergeq.QueueEntry {
	return &mergeq.QueueEntry{Position: position, State: "QUEUED"}
}

// TestMergeEnqueueSubcommandExitCodes pins the contract the launcher relies on:
// real arguments in, the documented exit code out, and the outcome line on the
// last line of stdout for every ending.
func TestMergeEnqueueSubcommandExitCodes(t *testing.T) {
	t.Parallel()
	sha := "abcdef0123456789abcdef0123456789abcdef01"
	cases := []struct {
		name string
		args []string
		q    *stubQueue
		code int
	}{
		{
			name: "queued",
			args: []string{"--pr", "6499"},
			q: &stubQueue{statuses: []mergeq.Status{
				{ID: "PR_6499", State: "OPEN"},
				{ID: "PR_6499", State: "OPEN", Entry: entry(2)},
			}},
			code: 0,
		},
		{
			name: "merged",
			args: []string{"--pr", "6499"},
			q:    &stubQueue{status: mergeq.Status{ID: "PR_6499", State: "MERGED", MergeCommit: &sha}},
			code: 0,
		},
		{
			name: "dropped",
			args: []string{"--pr", "6499", "--wait", "1"},
			q: &stubQueue{statuses: []mergeq.Status{
				{ID: "PR_6499", State: "OPEN", Base: "main", Entry: entry(1)},
				{ID: "PR_6499", State: "OPEN", Base: "main"},
			}, drop: &mergeq.DropEvent{CreatedAt: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC), Reason: "failed_checks"}},
			code: 1,
		},
		{
			name: "timed out",
			args: []string{"--pr", "6499", "--wait", "1"},
			q:    &stubQueue{status: mergeq.Status{ID: "PR_6499", State: "OPEN", Entry: entry(1)}},
			code: 1,
		},
		{
			name: "refused",
			args: []string{"--pr", "6499"},
			q:    &stubQueue{enqueueErr: errors.New("HTTP 422")},
			code: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr strings.Builder
			if code := runMergeEnqueue(tc.args, tc.q, pausedClock(), &stdout, &stderr); code != tc.code {
				t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", code, tc.code, stdout.String(), stderr.String())
			}
			if tc.code == 2 {
				if stdout.Len() != 0 || stderr.Len() == 0 {
					t.Fatalf("a refusal belongs on stderr only: stdout %q, stderr %q", stdout.String(), stderr.String())
				}
				return
			}
			// The outcome is the last line whatever came before it: a waiting
			// run prints the queued position first, and the operator reads the
			// verdict from the final line.
			if _, _, ok := strings.Cut(strings.TrimSpace(stdout.String()), "MERGEQ "); !ok {
				t.Fatalf("stdout lacks the outcome marker: %q", stdout.String())
			}
			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			if last := lines[len(lines)-1]; !strings.HasPrefix(last, "MERGEQ ") {
				t.Fatalf("last line = %q, want the outcome line", last)
			}
		})
	}
}

// TestMergeEnqueueSubcommandRejectsBadArguments pins usage handling: a missing
// --pr, a negative --wait and a stray positional are exit 2 with nothing on
// stdout, before any client is touched.
func TestMergeEnqueueSubcommandRejectsBadArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"--wait", "5"},
		{"--pr", "6499", "--wait", "-1"},
		// A wait has no sign. These two are also pinned at the parser by
		// TestMergeEnqueueRejectsASignedWait, because the stub's empty answer
		// reaches exit 2 on its own and so cannot tell the two parsers apart.
		{"--pr", "6499", "--wait", "+1"},
		{"--pr", "6499", "--wait", "-0"},
		{"--pr", "6499", "--wait", "nope"},
		{"--pr", "6499", "stray"},
	} {
		var stdout, stderr strings.Builder
		if code := runMergeEnqueue(args, &stubQueue{}, pausedClock(), &stdout, &stderr); code != 2 {
			t.Fatalf("runMergeEnqueue(%v) = %d, want 2", args, code)
		}
		if stdout.Len() != 0 {
			t.Fatalf("runMergeEnqueue(%v) wrote stdout %q on a usage error", args, stdout.String())
		}
	}
}

// TestMergeEnqueueRejectsASignedWait pins that a wait is an unsigned decimal
// number. strconv.Atoi accepts a leading sign, so "--wait +1" read as one second
// and "--wait -0" as no wait at all: both are values the caller cannot have
// meant, and both must be usage errors instead.
//
// The oracle is the parser, not the subcommand's exit code. A run with no
// status to report reaches exit 2 for its own reason, so a table entry in
// TestMergeEnqueueSubcommandRejectsBadArguments would pass against either
// parser.
func TestMergeEnqueueRejectsASignedWait(t *testing.T) {
	t.Parallel()
	for _, signed := range []string{"+1", "-0", "-1"} {
		if wait, err := parseWaitSeconds(signed); err == nil {
			t.Fatalf("--wait %s = %v, want a usage error: a wait has no sign", signed, wait)
		}
	}
}

// TestMergeEnqueueReadsWaitAsDecimal pins issue #5304's fix at this entry
// point: "--wait 08" is eight seconds, not an octal parse error. Go's flag.Int
// would reject it, so the flag is parsed as a decimal integer.
func TestMergeEnqueueReadsWaitAsDecimal(t *testing.T) {
	t.Parallel()
	merged := "abcdef0123456789abcdef0123456789abcdef01"
	q := &stubQueue{status: mergeq.Status{ID: "PR_6499", State: "MERGED", MergeCommit: &merged}}
	var stdout, stderr strings.Builder
	if code := runMergeEnqueue([]string{"--pr", "6499", "--wait", "08"}, q, pausedClock(), &stdout, &stderr); code != 0 {
		t.Fatalf("--wait 08 = exit %d, want 0 (stderr %q)", code, stderr.String())
	}
}

// TestMergeEnqueueRejectsWaitBeyondTheLargestDuration pins the bound at the
// edge: one second past the largest duration a wait fits is a usage error, and
// the largest that fits is not. Without the bound the conversion wraps
// negative, the no-wait branch runs, and a queued PR reports success it does
// not have.
//
// The accepted case is checked by the duration it produces rather than by
// running the wait: 9.2 billion seconds is a real wait, and a clock that
// advanced it would only prove the fake advanced.
func TestMergeEnqueueRejectsWaitBeyondTheLargestDuration(t *testing.T) {
	t.Parallel()
	if _, err := parseWaitSeconds("9223372037"); err == nil {
		t.Fatal("--wait 9223372037 was accepted; it overflows a duration")
	}
	wait, err := parseWaitSeconds("9223372036")
	if err != nil {
		t.Fatalf("--wait 9223372036 = %v, want the largest whole-second wait", err)
	}
	// Compared as whole seconds: unconvert rejects converting the constant and
	// durationcheck rejects multiplying durations, and both are right, so the
	// assertion is stated in the unit the flag takes.
	if got := int64(wait / time.Second); got != math.MaxInt64/int64(time.Second) {
		t.Fatalf("--wait 9223372036 = %d seconds, want %d", got, math.MaxInt64/int64(time.Second))
	}
}
