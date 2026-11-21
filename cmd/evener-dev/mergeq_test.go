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
	// calls names every client method invoked, in order, so a test can tell
	// a run that never reached the client from one whose answers happened to
	// end the same way.
	calls []string
}

func (s *stubQueue) Status(context.Context, string, string) (mergeq.Status, error) {
	s.calls = append(s.calls, "Status")
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
	s.calls = append(s.calls, "MergedCommit")
	return "", false, nil
}

// Enqueue keeps the two things the subcommand's contract is pinned through --
// which PR was asked for, and the error it was told to report -- and answers the
// entry with nil. The entry is the mutation's own reply, which these tests do not
// exercise: what they check is the exit code the command derives from the error,
// and a nil entry is the reply that named none.
func (s *stubQueue) Enqueue(_ context.Context, _, prID string, _ bool) (*mergeq.QueueEntry, error) {
	s.calls = append(s.calls, "Enqueue")
	if s.enqueueErr != nil {
		return nil, s.enqueueErr
	}
	s.enqueued = append(s.enqueued, prID)
	return nil, nil
}

func (s *stubQueue) QueueDrop(context.Context, string, string) (*mergeq.DropEvent, error) {
	s.calls = append(s.calls, "QueueDrop")
	return s.drop, nil
}

func (s *stubQueue) MergeGroupRuns(context.Context, string) ([]mergeq.GroupRun, error) {
	s.calls = append(s.calls, "MergeGroupRuns")
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
// stdout, before any client is touched. The exit code and the empty stdout
// cannot show that last part on their own: a run that reached the empty stub
// gets no status to report and exits 2 with nothing on stdout as well. The
// stub's call record is the oracle for it.
func TestMergeEnqueueSubcommandRejectsBadArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"--wait", "5"},
		{"--pr", "6499", "--wait", "-1"},
		// A wait has no sign. TestMergeEnqueueRejectsASignedWait also pins
		// these two at the parser itself.
		{"--pr", "6499", "--wait", "+1"},
		{"--pr", "6499", "--wait", "-0"},
		{"--pr", "6499", "--wait", "nope"},
		{"--pr", "6499", "stray"},
	} {
		var stdout, stderr strings.Builder
		q := &stubQueue{}
		if code := runMergeEnqueue(args, q, pausedClock(), &stdout, &stderr); code != 2 {
			t.Fatalf("runMergeEnqueue(%v) = %d, want 2", args, code)
		}
		if stdout.Len() != 0 {
			t.Fatalf("runMergeEnqueue(%v) wrote stdout %q on a usage error", args, stdout.String())
		}
		if len(q.calls) != 0 {
			t.Fatalf("runMergeEnqueue(%v) called the client %v on a usage error, want no calls", args, q.calls)
		}
	}
}

// TestMergeEnqueueRejectsASignedWait pins that a wait is an unsigned decimal
// number. strconv.Atoi accepts a leading sign, so "--wait +1" read as one second
// and "--wait -0" as no wait at all: both are values the caller cannot have
// meant, and both must be usage errors instead.
//
// The oracle is the parser itself, so the refusal is pinned where it is made
// and not only through the subcommand that reports it.
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
	// A PR that stays queued, so the wait runs to its deadline and the run
	// reports a timeout. That is what makes the parse observable: read `08` as
	// zero and the no-wait branch reports "queued" at exit 0 without sleeping at
	// all. A case starting from a merged PR returns before the wait is reached,
	// so it passes whichever way the seconds are read -- which is why this case
	// starts queued.
	q := &stubQueue{status: mergeq.Status{ID: "PR_6499", State: "OPEN", Entry: entry(1)}}
	// The clock records what the wait consumed rather than asserting on the exit
	// alone: eight seconds is the claim, and a narrower number would still time
	// out eventually.
	var now time.Time
	var slept time.Duration
	clock := mergeq.Clock{
		Now: func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool {
			now = now.Add(d)
			slept += d
			return true
		},
	}
	var stdout, stderr strings.Builder
	if code := runMergeEnqueue([]string{"--pr", "6499", "--wait", "08"}, q, clock, &stdout, &stderr); code != 1 {
		t.Fatalf("--wait 08 = exit %d, want 1: eight seconds of a PR that never moves is a timeout, and an exit of 0 means the seconds were read as zero (stderr %q)", code, stderr.String())
	}
	// Eight seconds, not one and not eighty. The poll interval shortens the last
	// sleep to land on the deadline, so the total is the wait itself.
	if slept != 8*time.Second {
		t.Fatalf("--wait 08 consumed %v, want 8s: the flag is decimal seconds", slept)
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

// TestMergeEnqueueUsageNamesTheRunnableBinary pins that both lines the --wait
// parse error prints, the diagnostic and the usage line, name `evener-dev`, the
// executable that dispatches merge-enqueue. Plain `evener` does not dispatch it.
func TestMergeEnqueueUsageNamesTheRunnableBinary(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	if code := runMergeEnqueue([]string{"--pr", "6499", "--wait", "5x"}, &stubQueue{}, pausedClock(), &stdout, &stderr); code != 2 {
		t.Fatalf("runMergeEnqueue(--wait 5x) = %d, want 2", code)
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr has %d lines, want the diagnostic and the usage line: %q", len(lines), stderr.String())
	}
	for _, line := range lines {
		if !strings.Contains(line, "evener-dev") {
			t.Fatalf("stderr line %q does not name the evener-dev executable that dispatches merge-enqueue", line)
		}
	}
}
