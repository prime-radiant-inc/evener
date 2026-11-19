package mergeq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeClock is the wait's clock with no wall time in it: Sleep advances Now,
// so a wait that must time out costs nothing and fires exactly.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) clock() Clock {
	return Clock{
		Now:   func() time.Time { return c.now },
		Sleep: func(_ context.Context, d time.Duration) bool { c.now = c.now.Add(d); return true },
	}
}

// fakeClient answers the queue's questions from supplied data. It is the data
// seam the decisions are tested through: the same values GitHub would answer
// with, with no `gh` anywhere.
type fakeClient struct {
	// statuses is the queue of status reads; the last one repeats once the
	// queue is down to one entry.
	statuses []Status
	// statusCalls counts the reads, so a client can tell the starting lookup
	// from the reconciliation read that follows the mutation.
	statusCalls int
	// statusErr, when set, is returned by every status read.
	statusErr error
	// mergedSHA and merged say what the PR's own state reports; mergedErr
	// makes the read fail.
	mergedSHA string
	merged    bool
	mergedErr error
	// mergedFromCall makes the merge visible only from the Nth merge lookup on
	// (1-based), so a test can place the merge after the last poll. mergedCalls
	// counts the lookups either way.
	mergedFromCall int
	mergedCalls    int
	// enqueueErr makes the enqueue call fail. enqueued records the pulls it
	// was asked to enqueue, in order.
	enqueueErr error
	// enqueueEntry is the queue entry the enqueue reply names, which is nil when
	// the reply names none.
	enqueueEntry *QueueEntry
	enqueued     []enqueuedPull
	// drop/ dropErr answer the removal-event read; drop nil means none.
	drop    *DropEvent
	dropErr error
	// runs and runsErr answer the merge-group run list.
	runs    []GroupRun
	runsErr error
	// runListCalls counts run-list reads, so a test can assert a lookup never
	// happened.
	runListCalls int
}

type enqueuedPull struct {
	prID string
	jump bool
}

func (f *fakeClient) Status(_ context.Context, _, _ string) (Status, error) {
	f.statusCalls++
	if f.statusErr != nil {
		return Status{}, f.statusErr
	}
	if len(f.statuses) == 0 {
		return Status{}, errors.New("fakeClient: no status supplied")
	}
	st := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return st, nil
}

func (f *fakeClient) MergedCommit(context.Context, string, string) (string, bool, error) {
	f.mergedCalls++
	if f.mergedErr != nil {
		return "", false, f.mergedErr
	}
	if f.mergedFromCall > 0 {
		return f.mergedSHA, f.mergedCalls >= f.mergedFromCall, nil
	}
	return f.mergedSHA, f.merged, nil
}

func (f *fakeClient) Enqueue(_ context.Context, _ string, prID string, jump bool) (*QueueEntry, error) {
	if f.enqueueErr != nil {
		return nil, f.enqueueErr
	}
	f.enqueued = append(f.enqueued, enqueuedPull{prID: prID, jump: jump})
	return f.enqueueEntry, nil
}

// StatusCalls reports how many status reads have been served.
func (f *fakeClient) StatusCalls() int { return f.statusCalls }

func (f *fakeClient) QueueDrop(context.Context, string, string) (*DropEvent, error) {
	return f.drop, f.dropErr
}

func (f *fakeClient) MergeGroupRuns(context.Context, string) ([]GroupRun, error) {
	f.runListCalls++
	return f.runs, f.runsErr
}

// opts is the request every test makes unless it says otherwise.
func opts(wait time.Duration) Options {
	return Options{Repo: "prime-radiant-inc/evener", PR: "6499", Wait: wait}
}

// queueEntry is a queued PR's entry.
func queueEntry(position int64) *QueueEntry {
	return &QueueEntry{Position: position, State: "QUEUED"}
}

// TestCancelledClientCallIsAnInterruptNotAFailure pins the other half of the
// interrupt: a signal that lands while `gh` is answering. The call fails with
// whatever its own cancellation looks like, and reporting that as a lookup
// failure (exit 2) tells the operator GitHub broke when they stopped the wait.
// The context decides, so the same error is a failure when nothing cancelled.
func TestCancelledClientCallIsAnInterruptNotAFailure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &cancellingClient{cancel: cancel}
	var stdout, stderr strings.Builder
	code := Run(ctx, opts(time.Minute), c, (&fakeClock{now: time.Now()}).clock(), &stdout, &stderr)
	if code != InterruptedExit {
		t.Fatalf("Run = exit %d, want %d (stdout %q, stderr %q)", code, InterruptedExit, stdout.String(), stderr.String())
	}
	// The exit code is the whole claim: 130 is the interrupt and 2 is the
	// refusal, so a cancelled lookup reported as a failure fails above. The
	// diagnostic's wording is deliberately not asserted here.
	if stderr.Len() == 0 {
		t.Fatal("an interrupted run wrote no diagnostic")
	}
}

// cancellingClient ends the context the way a delivered signal would, then
// fails the call the way `gh` does when it is killed.
type cancellingClient struct{ cancel context.CancelFunc }

func (c *cancellingClient) Status(context.Context, string, string) (Status, error) {
	c.cancel()
	return Status{}, errors.New("signal: killed")
}

func (c *cancellingClient) MergedCommit(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

func (c *cancellingClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	return nil, nil
}

func (c *cancellingClient) QueueDrop(context.Context, string, string) (*DropEvent, error) {
	return nil, nil
}

func (c *cancellingClient) MergeGroupRuns(context.Context, string) ([]GroupRun, error) {
	return nil, nil
}

// TestDroppedBranchesNameThisPRsMergeGroupRun pins the defect #6505 fixed and
// #6514 landed: every drop names the merge-group run the operator should read,
// resolved by the queue branch prefix because the branch carries the base tip,
// never the PR head. It is parametrized over the reason so one assertion covers
// every branch of the drop decision.
func TestDroppedBranchesNameThisPRsMergeGroupRun(t *testing.T) {
	t.Parallel()
	const (
		queueBranch = "gh-readonly-queue/main/pr-6499-114743f798cd16a42e0522a247157273e1203c28"
		runURL      = "https://github.com/prime-radiant-inc/evener/actions/runs/123456789"
	)
	buildTime := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		event *DropEvent
		err   error
		class DropClass
	}{
		{name: "failed_checks", event: &DropEvent{CreatedAt: buildTime.Add(-time.Minute), Reason: "failed_checks"}, class: DropChecksFailed},
		{name: "checks_timed_out", event: &DropEvent{CreatedAt: buildTime.Add(-time.Minute), Reason: "checks_timed_out"}, class: DropChecksTimedOut},
		{name: "unavailable reason", event: &DropEvent{CreatedAt: buildTime.Add(-time.Minute)}, class: DropReasonUnavailable},
		{name: "unknown reason", event: &DropEvent{CreatedAt: buildTime.Add(-time.Minute), Reason: "merge_conflict"}, class: DropReasonOther},
		{name: "stale event", event: &DropEvent{CreatedAt: buildTime.Add(-time.Hour), Reason: "failed_checks"}, class: DropEventStale},
		{name: "unreadable event", err: errors.New("HTTP 500"), class: DropReasonUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := &fakeClock{now: buildTime}
			c := &fakeClient{
				statuses: []Status{
					{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(1)},
					{ID: "PR_6499", State: "OPEN", Base: "main"},
				},
				drop:    tc.event,
				dropErr: tc.err,
				runs:    []GroupRun{{DatabaseID: 123456789, URL: runURL, Conclusion: "failure", HeadBranch: queueBranch}},
			}
			out, exitErr := Execute(context.Background(), opts(time.Minute), c, clock.clock())
			if exitErr != nil {
				t.Fatalf("Execute: %v", exitErr)
			}
			if out.Kind != Dropped {
				t.Fatalf("Kind = %v, want Dropped", out.Kind)
			}
			if out.DropClass != tc.class {
				t.Fatalf("DropClass = %v, want %v", out.DropClass, tc.class)
			}
			// The resolution the pre-#6514 suite could not see: the run, found
			// by prefix, is named. If the run is not resolved the fallback names
			// the prefix instead, so a run here is the strong claim.
			if out.Run.Group == nil {
				t.Fatalf("drop named no resolved run; fallback was %q", out.Run.String())
			}
			if out.Run.Group.DatabaseID != 123456789 || out.Run.Group.URL != runURL {
				t.Fatalf("drop named run %d (%s), want %d (%s)", out.Run.Group.DatabaseID, out.Run.Group.URL, 123456789, runURL)
			}
			// The operator reads Reason, not Run: a drop line without the run in
			// it sends them hunting through the Actions API, and asserting only
			// the structured field would pass while the printed line lost it
			// (the deleted shell suite checked those lines carried the URL).
			if !strings.Contains(out.Reason, runURL) {
				t.Fatalf("drop reason = %q, want it to name the run at %s", out.Reason, runURL)
			}
		})
	}
}

// TestDropResolvesTheBaseTipRunAndNotThePRHead pins what #6514's base-tip
// finding was about: the queue branch carries the base tip the queue tested on
// top of, not the PR head, so the run is reached by branch prefix alone. A run
// for another PR must not be picked just for being first in the list, and the
// lookup is given no commit SHA to match on — the head would be the wrong one,
// and a push to the queued PR between the drop and its detection would
// redirect the lookup to a head that never got a merge-group run.
func TestDropResolvesTheBaseTipRunAndNotThePRHead(t *testing.T) {
	t.Parallel()
	const (
		baseTipSHA = "114743f798cd16a42e0522a247157273e1203c28"
		wantID     = int64(123456789)
		otherID    = int64(987654321)
	)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(1)},
			{ID: "PR_6499", State: "OPEN", Base: "main"},
		},
		drop: &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"},
		runs: []GroupRun{
			// Newest first, another PR: the prefix must select this PR's row.
			{DatabaseID: otherID, URL: "https://example.invalid/other", HeadBranch: "gh-readonly-queue/main/pr-6500-" + baseTipSHA},
			// This PR, the base tip the queue tested on top of. Nothing in the
			// lookup knows this SHA; it is found by prefix.
			{DatabaseID: wantID, URL: "https://example.invalid/want", HeadBranch: "gh-readonly-queue/main/pr-6499-" + baseTipSHA},
		},
	}
	// A drop is only reachable across a wait, so the request asks for one.
	out, exitErr := Execute(context.Background(), opts(time.Minute), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Kind != Dropped {
		t.Fatalf("Kind = %v, want Dropped", out.Kind)
	}
	if out.Run.Group == nil || out.Run.Group.DatabaseID != wantID {
		t.Fatalf("drop named %q, want the base-tip run %d", out.Run.String(), wantID)
	}
	if out.Run.Group.DatabaseID == otherID {
		t.Fatalf("drop named another PR's run, which only happened to be first in the list")
	}
}

// TestDropWithoutABaseSkipsTheRunLookup pins the fallbacks: a status read that
// supplied no base has no prefix to filter by, so no run list is issued and the
// reason names the run generically. #6514's suite asserted on the argv of that
// skipped call; here the absence of the call is the assertion.
func TestDropWithoutABaseSkipsTheRunLookup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	for _, base := range []string{"", "-"} {
		t.Run("base="+base, func(t *testing.T) {
			t.Parallel()
			c := &fakeClient{
				statuses: []Status{
					{ID: "PR_6499", State: "OPEN", Base: base, Entry: queueEntry(1)},
					{ID: "PR_6499", State: "OPEN", Base: base},
				},
				drop: &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"},
			}
			out, exitErr := Execute(context.Background(), opts(time.Minute), c, (&fakeClock{now: now}).clock())
			if exitErr != nil {
				t.Fatalf("Execute: %v", exitErr)
			}
			if out.Kind != Dropped {
				t.Fatalf("Kind = %v, want Dropped", out.Kind)
			}
			if c.runListCalls != 0 {
				t.Fatalf("run list was read %d time(s) with no base to filter by", c.runListCalls)
			}
			if out.Run.Prefix != "" {
				t.Fatalf("drop carries a queue branch prefix with no base: %q", out.Run.Prefix)
			}
			if out.Run.Group != nil {
				t.Fatalf("drop resolved a run with no base to filter by: %d", out.Run.Group.DatabaseID)
			}
		})
	}
}

// TestRunListFailureFallsBackToThePrefix pins the other fallback: a run lookup
// that fails does not fail the run, and the reason names the prefix so the
// operator can list the run themselves.
func TestRunListFailureFallsBackToThePrefix(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	const prefix = "gh-readonly-queue/main/pr-6499-"
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(1)},
			{ID: "PR_6499", State: "OPEN", Base: "main"},
		},
		drop:    &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"},
		runsErr: errors.New("HTTP 500"),
	}
	out, exitErr := Execute(context.Background(), opts(time.Minute), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Run.Group != nil {
		t.Fatalf("drop resolved run %d from a lookup that failed", out.Run.Group.DatabaseID)
	}
	if out.Run.Prefix != prefix {
		t.Fatalf("drop prefix = %q, want %q", out.Run.Prefix, prefix)
	}
	// The operator-facing reason must carry the prefix too, or the advice sends
	// them looking for a run it does not name. Asserted against Reason, the
	// renderer's own input, rather than through outcomeLine: the sentence's
	// wording is not the contract, the fact that the prefix reaches it is.
	if !strings.Contains(out.Reason, prefix) {
		t.Fatalf("drop reason = %q, want it to carry the queue branch prefix %q", out.Reason, prefix)
	}
}

// TestEmptyRunListFallsBackToThePrefix pins the same fallback for the other
// way a lookup can name nothing: it succeeded and returned no run for this
// base tip yet. The queue can drop a PR before its run is listed, and the
// operator still needs the prefix to find the run once it appears.
func TestEmptyRunListFallsBackToThePrefix(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	const prefix = "gh-readonly-queue/main/pr-6499-"
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(1)},
			{ID: "PR_6499", State: "OPEN", Base: "main"},
		},
		drop: &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"},
		// A run list that answered, with another PR's run in it: this PR's
		// base tip has none yet.
		runs: []GroupRun{{
			DatabaseID: 555,
			URL:        "https://example.invalid/other",
			Conclusion: "failure",
			HeadBranch: "gh-readonly-queue/main/pr-6500-deadbeef",
		}},
	}
	out, exitErr := Execute(context.Background(), opts(time.Minute), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Run.Group != nil {
		t.Fatalf("drop resolved another PR's run: %d", out.Run.Group.DatabaseID)
	}
	if out.Run.Prefix != prefix {
		t.Fatalf("drop prefix = %q, want %q", out.Run.Prefix, prefix)
	}
	if !strings.Contains(out.Reason, prefix) {
		t.Fatalf("drop reason = %q, want it to carry the queue branch prefix %q", out.Reason, prefix)
	}
}

// TestSecondRunForThisPRIsTheOneNamed pins the most-recent rule: one PR can be
// queued more than once on different base tips, and the entry that dropped is
// the newer run's.
func TestSecondRunForThisPRIsTheOneNamed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(1)},
			{ID: "PR_6499", State: "OPEN", Base: "main"},
		},
		drop: &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"},
		runs: []GroupRun{
			{DatabaseID: 111222333, URL: "https://example.invalid/new", Conclusion: "failure", HeadBranch: "gh-readonly-queue/main/pr-6499-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{DatabaseID: 999888777, URL: "https://example.invalid/old", Conclusion: "success", HeadBranch: "gh-readonly-queue/main/pr-6499-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	out, exitErr := Execute(context.Background(), opts(time.Minute), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Run.Group == nil || out.Run.Group.DatabaseID != 111222333 {
		t.Fatalf("drop named %q, want the most recent run 111222333", out.Run.String())
	}
	if out.Run.Group.DatabaseID == 999888777 {
		t.Fatalf("drop named the older run 999888777")
	}
}

// TestRunWithoutAConclusionStillNamesItsIdentity pins the shape a run has while
// it is still going: `gh run list` reports no conclusion yet, so the drop names
// the run's id and URL with no verdict. A lookup that resolved must never fall
// back to the prefix just because the conclusion is empty.
func TestRunWithoutAConclusionStillNamesItsIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	const (
		runID  = int64(444555666)
		runURL = "https://example.invalid/runs/444555666"
	)
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(1)},
			{ID: "PR_6499", State: "OPEN", Base: "main"},
		},
		drop: &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"},
		runs: []GroupRun{{
			DatabaseID: runID,
			URL:        runURL,
			// Conclusion deliberately empty: the run is still in progress.
			HeadBranch: "gh-readonly-queue/main/pr-6499-deadbeef",
		}},
	}
	out, exitErr := Execute(context.Background(), opts(time.Minute), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Run.Group == nil {
		t.Fatalf("drop named no run for a matched entry; fallback was %q", out.Run.String())
	}
	if out.Run.Group.DatabaseID != runID || out.Run.Group.URL != runURL {
		t.Fatalf("drop named run %d (%s), want %d (%s)", out.Run.Group.DatabaseID, out.Run.Group.URL, runID, runURL)
	}
	// The operator-facing reason must carry the same identity, since that is
	// what they actually read.
	if !strings.Contains(out.Reason, runURL) {
		t.Fatalf("drop reason = %q, want it to name %s", out.Reason, runURL)
	}
}

// TestDropWithoutAClassifiableReasonStillNamesTheRun pins that an unreadable or
// absent reason does not lose the run: the operator gets the run either way.
func TestDropWithoutAClassifiableReasonStillNamesTheRun(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(1)},
			{ID: "PR_6499", State: "OPEN", Base: "main"},
		},
		dropErr: errors.New("HTTP 500"),
		runs:    []GroupRun{{DatabaseID: 42, URL: "https://example.invalid/42", HeadBranch: "gh-readonly-queue/main/pr-6499-deadbeef"}},
	}
	out, _ := Execute(context.Background(), opts(time.Minute), c, (&fakeClock{now: now}).clock())
	if out.Run.Group == nil || out.Run.Group.DatabaseID != 42 {
		t.Fatalf("drop after an unreadable removal event named %q, want run 42", out.Run.String())
	}
	if out.DropClass != DropReasonUnreadable {
		t.Fatalf("DropClass = %v, want DropReasonUnreadable", out.DropClass)
	}
}

// TestEnqueueReportsThePositionAndSkipsTheRunLookup pins the enqueue path: a PR
// that is not queued is enqueued, the position is reported, and no drop happens
// so no run is looked up.
func TestEnqueueReportsThePositionAndSkipsTheRunLookup(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN"},
			{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)},
		},
	}
	out, exitErr := Execute(context.Background(), opts(0), c, SystemClock)
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Kind != Queued || out.Position != 1 {
		t.Fatalf("outcome = %+v, want Queued at position 1", out)
	}
	if len(c.enqueued) != 1 || c.enqueued[0] != (enqueuedPull{prID: "PR_6499", jump: false}) {
		t.Fatalf("enqueued = %+v, want this PR once with no jump", c.enqueued)
	}
	if c.runListCalls != 0 {
		t.Fatalf("run list was read %d time(s) on the enqueue path", c.runListCalls)
	}
}

// TestJumpRidesOnTheEnqueueCall pins --jump's one legal effect: the enqueue
// call carries the flag. It is a value on that call, not the words a fake
// logged.
func TestJumpRidesOnTheEnqueueCall(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN"},
			{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)},
		},
	}
	req := opts(0)
	req.Jump = true
	if _, exitErr := Execute(context.Background(), req, c, SystemClock); exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if len(c.enqueued) != 1 || !c.enqueued[0].jump {
		t.Fatalf("enqueued = %+v, want this PR once with the jump flag", c.enqueued)
	}
}

// TestJumpOnAQueuedPRIsRefused pins --jump's refusal: a PR already queued gets
// no enqueue call to carry the jump, so the run ends at exit 2 with the
// position it is already at.
func TestJumpOnAQueuedPRIsRefused(t *testing.T) {
	t.Parallel()
	c := &fakeClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(3)}}}
	req := opts(0)
	req.Jump = true
	_, exitErr := Execute(context.Background(), req, c, SystemClock)
	if exitErr == nil {
		t.Fatal("--jump on a queued PR must be refused")
	}
	if exitErr.Kind != JumpRefused || exitErr.Position != 3 {
		t.Fatalf("refusal = %+v, want JumpRefused at position 3", exitErr)
	}
	if len(c.enqueued) != 0 {
		t.Fatalf("a queued PR was enqueued again: %+v", c.enqueued)
	}
}

// TestEnqueueThatLeavesNoEntryIsRefused pins the post-enqueue verification: the
// call returning is not the same as the PR being queued.
func TestEnqueueThatLeavesNoEntryIsRefused(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN"},
			{ID: "PR_6499", State: "OPEN"},
		},
	}
	_, exitErr := Execute(context.Background(), opts(0), c, SystemClock)
	if exitErr == nil || exitErr.Kind != EnqueueLeftNoEntry {
		t.Fatalf("refusal = %+v, want EnqueueLeftNoEntry", exitErr)
	}
	if exitErr.PRState != "OPEN" {
		t.Fatalf("refusal PRState = %q, want OPEN", exitErr.PRState)
	}
}

// TestEnqueueFailureIsRefusedWithGhsOutput pins the class of a failed enqueue
// call, and that gh's own text survives into the diagnostic.
func TestEnqueueFailureIsRefusedWithGhsOutput(t *testing.T) {
	t.Parallel()
	const ghSays = "HTTP 422: Pull request is not mergeable"
	c := &fakeClient{
		statuses:   []Status{{ID: "PR_6499", State: "OPEN"}},
		enqueueErr: errors.New(ghSays),
	}
	_, exitErr := Execute(context.Background(), opts(0), c, SystemClock)
	if exitErr == nil || exitErr.Kind != EnqueueFailed {
		t.Fatalf("refusal = %+v, want EnqueueFailed", exitErr)
	}
	if exitErr.Detail != ghSays {
		t.Fatalf("refusal Detail = %q, want gh's own text %q", exitErr.Detail, ghSays)
	}
}

// TestLookupFailureIsRefused pins the class of a failed status read.
func TestLookupFailureIsRefused(t *testing.T) {
	t.Parallel()
	c := &fakeClient{statusErr: errors.New("HTTP 500")}
	_, exitErr := Execute(context.Background(), opts(0), c, SystemClock)
	if exitErr == nil || exitErr.Kind != LookupFailed {
		t.Fatalf("refusal = %+v, want LookupFailed", exitErr)
	}
}

// TestMergedStateEndsTheRunWithoutAnEnqueue pins that the PR's own state is
// what decides a merge: a MERGED pull request is reported, not enqueued.
func TestMergedStateEndsTheRunWithoutAnEnqueue(t *testing.T) {
	t.Parallel()
	sha := "abcdef0123456789abcdef0123456789abcdef01"
	c := &fakeClient{
		statuses: []Status{{ID: "PR_6499", State: "MERGED", MergeCommit: &sha}},
	}
	out, exitErr := Execute(context.Background(), opts(time.Minute), c, SystemClock)
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Kind != Merged || out.SHA != sha {
		t.Fatalf("outcome = %+v, want Merged with sha %s", out, sha)
	}
	if len(c.enqueued) != 0 {
		t.Fatalf("a merged PR was enqueued: %+v", c.enqueued)
	}
}

// TestMergedCommitWinsOverAStaleQueueSnapshot pins #6480's finding: the queue
// snapshot can lag a merge, so a verdict read from it alone reports a PR that
// already merged. The PR's own state decides.
//
// The merge becomes visible only on the final observation, after the last
// scheduled poll: the status reads stay OPEN with a queue entry for the whole
// wait, and the merge lookup answers merged only once the deadline has passed.
// That is the case a first-read merge cannot stand in for -- with the merge
// visible up front the run exits before polling, and deleting the final
// observation would still pass.
func TestMergedCommitWinsOverAStaleQueueSnapshot(t *testing.T) {
	t.Parallel()
	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &fakeClient{
		// Every read keeps the PR queued, so the run only ends by the merge
		// lookup: the queue snapshot never reports it.
		statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}},
		// A 90s wait makes one merge lookup before enqueueing, one after each
		// of three 30s polls, and one more on the deadline, so the fifth is the
		// final observation.
		mergedFromCall: 5,
		mergedSHA:      sha,
	}
	out, exitErr := Execute(context.Background(), opts(90*time.Second), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Kind != Merged || out.SHA != sha {
		t.Fatalf("outcome = %+v, want Merged with sha %s (a merge first visible after the last poll is not a timeout)", out, sha)
	}
	if c.mergedCalls != 5 {
		t.Fatalf("merge lookup ran %d time(s), want five (one up front, three polls, one final observation)", c.mergedCalls)
	}
}

// TestAnExplicitMutationRefusalIsNotReconciled pins which enqueue failures are
// reconciled against a fresh read, and which are answers.
//
// Reconciling is right for a reply that was lost and for a call whose own
// allowance expired: in both, GitHub may have accepted the mutation before the
// answer was lost, so a refusal would report an enqueue that had already landed.
// A failure the call itself reported is neither. Reconciling it reads the queue
// looking for this invocation's work, and finds whatever is there -- including an
// entry another operator made for the same pull request after the initial
// lookup. That entry then turns a refused mutation into reported success.
//
// The ordering is what makes the difference: the enqueue context's error has to
// be read before the cancel, because cancelling sets it to Canceled whatever it
// was, and a check that runs afterwards calls every failure uncertain.
func TestAnExplicitMutationRefusalIsNotReconciled(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		enqueueErr: errors.New("gh: the pull request is already in the merge queue and --jump was refused"),
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN"},
			// Another operator queued it after the lookup above. Reconciling an
			// explicit refusal against this read reports their entry as the work
			// of an invocation GitHub refused.
			{ID: "PR_6499", State: "OPEN", Entry: queueEntry(3)},
		},
	}
	out, exitErr := Execute(context.Background(), opts(0), c, SystemClock)
	if exitErr == nil {
		t.Fatalf("outcome = %+v with no error: an enqueue GitHub refused was reported as success, from another operator's queue entry", out)
	}
	if exitErr.Kind != EnqueueFailed {
		t.Fatalf("kind = %v, want EnqueueFailed: the call reported a refusal, which is an answer and not something to reconcile", exitErr.Kind)
	}
}

// TestAnExpiredMutationIsReconciled pins the other side of the same rule: a call
// whose own allowance ran out is not a refusal, because the mutation may have
// landed before the allowance did. It is read back and reported as what the queue
// says.
func TestAnExpiredMutationIsReconciled(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	// The stalled call also moves the clock past the deadline, which is what the
	// wait looks like from the run's side afterwards: the caller's budget is spent
	// and the confirmation draws the post-deadline allowance instead.
	c := &expiringEnqueueClient{now: &now}
	c.statuses = []Status{
		{ID: "PR_6499", State: "OPEN"},
		{ID: "PR_6499", State: mergedState},
	}
	// One millisecond of wait: the mutation's allowance is what is left of it, and
	// the stalled call is what spends it, so the outcome is the real ending rather
	// than an error built to look like it.
	out, exitErr := Execute(context.Background(), opts(time.Millisecond), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the queue read back and reported: a call that ran out of its own allowance may have landed, so it is not a refusal", exitErr)
	}
	if out.Kind != Merged {
		t.Fatalf("outcome = %+v, want Merged: the enqueue expired and the read that followed found the pull request merged", out)
	}
}

// expiringEnqueueClient is a client whose enqueue call spends its whole
// allowance, the way a stalled gh call does, and returns the context's error.
type expiringEnqueueClient struct {
	fakeClient
	// now is the run's clock, moved past the deadline when the call gives up, so
	// the read that follows starts with the wait already spent.
	now *time.Time
}

func (c *expiringEnqueueClient) Enqueue(ctx context.Context, _, _ string, _ bool) (*QueueEntry, error) {
	<-ctx.Done()
	*c.now = c.now.Add(time.Hour)
	return nil, ctx.Err()
}

// TestWaitingRunReportsTheQueuedPositionFirst pins the progress line a waiting
// run prints before it waits: the operator sees where the PR entered the queue,
// and the outcome line is still the last one.
func TestWaitingRunReportsTheQueuedPositionFirst(t *testing.T) {
	t.Parallel()
	var progress []Outcome
	req := opts(time.Minute)
	req.Progress = func(out Outcome) { progress = append(progress, out) }
	c := &fakeClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(4)}}}
	out, exitErr := Execute(context.Background(), req, c, (&fakeClock{now: time.Now()}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if len(progress) != 1 || progress[0].Kind != Queued || progress[0].Position != 4 {
		t.Fatalf("progress = %+v, want one Queued outcome at position 4", progress)
	}
	if out.Kind != TimedOut {
		t.Fatalf("outcome = %+v, want TimedOut", out)
	}
}

// TestTimeoutReportsThePositionAndState pins the timeout verdict and the wait's
// bound: the clock advances by the requested wait and no further, and the
// outcome is a timeout, not a drop.
func TestTimeoutReportsThePositionAndState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	c := &fakeClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}}}
	out, exitErr := Execute(context.Background(), opts(90*time.Second), c, clock.clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if out.Kind != TimedOut || out.Position != 1 || out.QueueState != "QUEUED" {
		t.Fatalf("outcome = %+v, want TimedOut at position 1 in QUEUED", out)
	}
	if got := clock.now.Sub(now); got != 90*time.Second {
		t.Fatalf("wait ran %v, want exactly the requested 90s", got)
	}
	if c.runListCalls != 0 {
		t.Fatalf("run list was read %d time(s) on the timeout path", c.runListCalls)
	}
}

// TestWaitSleepsAtMostThePollInterval pins the poll's pacing: a long wait is
// bounded to one poll interval per sleep, so the last sleep is the remaining
// time rather than a full interval past the deadline.
//
// It also pins what a cancellation must do to that sleep: the wait wakes on the
// context, reports an interrupt, and never takes the timeout branch. That is
// the difference between an interrupted operator and one whose wait expired,
// and the two are both exit codes a caller acts on differently.
func TestCancelledWaitStopsPollingAndReportsAnInterrupt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var slept []time.Duration
	clock := Clock{
		// The fake ends the context when a poll would sleep, standing in for a
		// signal arriving while the helper waits.
		Sleep: func(_ context.Context, d time.Duration) bool {
			slept = append(slept, d)
			cancel()
			return false
		},
		Now: func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) },
	}
	c := &fakeClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}}}
	var stdout, stderr strings.Builder
	if code := Run(ctx, opts(time.Minute), c, clock, &stdout, &stderr); code != InterruptedExit {
		t.Fatalf("Run = exit %d, want %d (stdout %q, stderr %q)", code, InterruptedExit, stdout.String(), stderr.String())
	}
	if len(slept) != 1 {
		t.Fatalf("polls = %v, want the wait to stop at the first cancelled sleep", slept)
	}
	if strings.Contains(stdout.String(), "MERGEQ timeout") {
		t.Fatalf("an interrupted wait printed the timeout verdict: %q", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("an interrupted wait wrote no diagnostic")
	}
}

func TestWaitSleepsAtMostThePollInterval(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	var sleeps []time.Duration
	clock := Clock{
		Now: func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool {
			sleeps = append(sleeps, d)
			now = now.Add(d)
			return true
		},
	}
	c := &fakeClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}}}
	if _, exitErr := Execute(context.Background(), opts(75*time.Second), c, clock); exitErr != nil {
		t.Fatalf("Execute: %v", exitErr)
	}
	if len(sleeps) != 3 {
		t.Fatalf("sleeps = %v, want three (30s, 30s, 15s)", sleeps)
	}
	if sleeps[0] != 30*time.Second || sleeps[1] != 30*time.Second || sleeps[2] != 15*time.Second {
		t.Fatalf("sleeps = %v, want [30s 30s 15s]", sleeps)
	}
}

// TestWaitBudgetBoundsTheReadsItMakes pins the half of the wait a sleep bound
// cannot cover: the reads.
//
// `--wait 60` promises the run is over in about a minute. Bounding only the
// sleeps spends that promise on the polling cadence and leaves each gh call free
// to hold the run for as long as GitHub takes -- the production context carries
// no deadline of its own, and the drain bound does not start while the child is
// still running under a live context. So a read stalled at the end of the budget
// kept the run past its own limit, and the timeout the caller was waiting for
// arrived late or not at all.
//
// The assertion is on the budget each read RECEIVES, not on waiting one out: a
// read that carries a deadline is bounded, and one that carries none is the
// unbounded call this finding is about. Waiting for a stalled read to expire
// would make the case cost the allowance it is checking.
func TestWaitBudgetBoundsTheReadsItMakes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &deadlineRecordingClient{status: Status{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}}
	clock := Clock{
		Now: func() time.Time { return now },
		// The sleep spends the whole budget, so a poll that follows has nothing
		// left of it: the reads must still be bounded by what the wait allows.
		Sleep: func(_ context.Context, d time.Duration) bool {
			now = now.Add(d)
			c.waited = true
			return true
		},
	}
	var stdout, stderr strings.Builder
	// The run's own context is never cancelled and carries no deadline, which is
	// the production shape: a signal cancels it, nothing else does.
	_ = Run(context.Background(), opts(60*time.Second), c, clock, &stdout, &stderr)
	// Only the reads made DURING the wait are the claim. The read that places the
	// PR in the queue happens before the budget exists and has nothing to be
	// bounded by, so it is not part of the finding.
	if len(c.duringWait) == 0 {
		t.Fatalf("no read was made during the wait, so this case did not reach the path it exists to test (reads %d, stdout %q)", c.reads, stdout.String())
	}
	// A read handed an already-expired context never reaches gh. Counting one as an
	// observation is how the loop's last poll hid: the fake answered, the count
	// looked right, and the real command would have failed before contacting
	// anything -- leaving the run one recheck short of the wait it was given.
	if c.expiredReads != 0 {
		t.Fatalf("%d read(s) were issued on a context that was already over, so those gh calls could never have started: the run reached its verdict having made fewer observations than the wait allowed, and a merge in that gap is reported as still queued", c.expiredReads)
	}
	for i, had := range c.duringWait {
		if !had {
			t.Fatalf("poll read %d of %d was issued with a context carrying no deadline: the wait's budget bounds only the sleeps, so one stalled gh call holds the run past the --wait the caller asked for", i+1, len(c.duringWait))
		}
	}
	if c.overBudget {
		t.Fatal("a read was given a budget reaching past the end of the wait, so the limit the caller set is not the limit the reads obey")
	}
}

// TestARefusalAcrossTheDeadlineIsStillARefusal pins that an expired context does
// not launder a definite no into an unknown.
//
// gh can return the refusal just before the budget expires, with the output drain
// crossing it afterwards, so the call's context is expired while its answer is a
// definite refusal. Treating that as uncertain sends the run to reconcile, and an
// entry another operator made for the same pull request then turns a refused
// `--jump` into reported success -- the truthfulness bug this is about, not a
// timeout one. The refusal outranks the expired context.
// TestAWrappedRefusalIsStillAnAnswer pins the shape outcomeUnknown's type test
// has to keep, now that it asks with errors.AsType.
//
// The refusal is what decides whether the run reconciles, and a refusal reaches
// it wrapped: Run's clients wrap client errors with the call that failed, and so
// does the gh client itself for the detail it carries. A type test that only saw
// an unwrapped *RefusalError would classify a wrapped refusal as an unknown
// outcome, spend a read looking for this invocation's work, and find another
// operator's queue entry for the same pull request -- reporting a refused --jump
// as this invocation's success. That is the exact misreport the discriminated
// error types exist to prevent, so matching through wrapping is load-bearing and
// is pinned here rather than left to the errors.AsType rewrite's reputation.
// TestReadStatusKeepsAnAnswerItsOwnAllowanceOutlived pins the distinction
// readStatus has to make, at the place it makes it.
//
// The read context bounds how long the run waits. The invocation context says
// whether the operator stopped it. They expire for different reasons and only
// one of them makes the answer untrue, so the function takes both.
//
// A completed reply can arrive and then have a descendant hold the output pipe
// until the read's own allowance runs out, so `Status` returns a decodable
// status AND an expired read context together. Asking the read context makes
// that Interrupted: the run denies an answer it received and reports a lookup
// failure for a PR it was just told was queued, with nobody having cancelled
// anything. Asking the invocation context keeps the answer, which is what the
// allowance was always meant to bound.
//
// Tested here rather than through Execute because the window is the read
// context's own expiry and a fake clock cannot expire a real context: the two
// are separate machinery, and a case built on the clock would assert the fake's
// arithmetic rather than this branch.
// TestAConfirmedEnqueueThatIsGoneIsADropNotAFailure pins what the mutation's own
// reply is worth.
//
// The enqueue mutation asks for the new queue entry, so a reply that performed
// the write carries the position GitHub put the PR in. That reply used to be
// discarded, and the only record left was the confirmation read. A concurrent
// push can rebuild the merge group between the mutation and that read, which
// removes the entry: the read then finds none, and the run reported
// "no queue entry after enqueueing" -- exit 2, an enqueue failure -- for a
// mutation GitHub itself had confirmed. The drop's own reason was never read,
// so the operator also lost the diagnostics that say why.
//
// With the entry kept, a confirmation that finds it gone is what it is: the
// queue took the entry and removed it. That is a drop, reported with its reason.
func TestAConfirmedEnqueueThatIsGoneIsADropNotAFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	// The mutation's reply named position 2. The confirmation reads after it find
	// the PR open with no entry, which is the push-rebuild case.
	c := &fakeClient{
		enqueueEntry: queueEntry(2),
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main"},
			{ID: "PR_6499", State: "OPEN", Base: "main"},
		},
		drop: &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"},
	}
	out, exitErr := Execute(context.Background(), opts(0), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the drop reported: the mutation's reply named the entry GitHub put the PR in, so a read that finds it gone is the queue having removed it, not a failed enqueue", exitErr)
	}
	if out.Kind != Dropped {
		t.Fatalf("outcome = %+v, want Dropped", out)
	}
	if out.DropClass != DropChecksFailed {
		t.Fatalf("DropClass = %v, want %v: the queue's own removal reason was available and explains the drop", out.DropClass, DropChecksFailed)
	}

	// The other direction, so this is not a licence to call any absence a drop: a
	// reply that named no entry and a read that finds none is still an enqueue
	// failure.
	silent := &fakeClient{
		statuses: []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main"},
			{ID: "PR_6499", State: "OPEN", Base: "main"},
		},
	}
	if _, exitErr := Execute(context.Background(), opts(0), silent, (&fakeClock{now: now}).clock()); exitErr == nil {
		t.Fatal("an enqueue that was never confirmed and left no entry was reported as success")
	} else if exitErr.Kind != EnqueueLeftNoEntry {
		t.Fatalf("kind = %v, want EnqueueLeftNoEntry: no reply named an entry and no read found one, so nothing established the enqueue", exitErr.Kind)
	}
}

func TestReadStatusKeepsAnAnswerItsOwnAllowanceOutlived(t *testing.T) {
	t.Parallel()
	// The read's allowance is already spent when the call returns: expired before
	// the read starts, which is the state an overrun leaves behind.
	spent, stopSpent := context.WithTimeout(context.Background(), -time.Second)
	defer stopSpent()
	c := &fakeClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(6)}}}

	// The invocation is live, so the answer stands.
	st, exitErr := readStatus(spent, context.Background(), opts(0), c)
	if exitErr != nil {
		t.Fatalf("readStatus: %v, want the decoded status: the read's own allowance expired, the reply arrived before it did, and nobody stopped the invocation", exitErr)
	}
	if st.Entry == nil || st.Entry.Position != 6 {
		t.Fatalf("status = %+v, want position 6 from the reply the read returned", st)
	}

	// The invocation is stopped, so the answer does not stand. This is the
	// direction that keeps the fix from becoming a licence to ignore a signal.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, exitErr := readStatus(context.Background(), ctx, opts(0), c); exitErr == nil {
		t.Fatal("a cancelled invocation reported the decoded status as its outcome")
	} else if exitErr.Kind != Interrupted {
		t.Fatalf("kind = %v, want Interrupted: the operator stopped the invocation, and a stop outranks a reply that arrived behind it", exitErr.Kind)
	}
}

func TestAWrappedRefusalIsStillAnAnswer(t *testing.T) {
	t.Parallel()
	refusal := &RefusalError{Detail: "the pull request is already in the merge queue, so --jump was refused"}
	if outcomeUnknown(refusal, false) {
		t.Fatal("a bare refusal was classified as an unknown outcome")
	}
	for _, wrapped := range []error{
		fmt.Errorf("enqueue: %w", refusal),
		fmt.Errorf("enqueue: %w: exit status 1", refusal),
		fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", refusal)),
	} {
		if outcomeUnknown(wrapped, false) {
			t.Fatalf("error = %v, classified as an unknown outcome: the refusal is wrapped, not absent, and reconciling it is what reports another operator's queue entry as this invocation's work", wrapped)
		}
		// And the expiry flag does not override it: the two are true at once when
		// a refusal arrives just before the allowance runs out.
		if outcomeUnknown(wrapped, true) {
			t.Fatalf("error = %v with an expired allowance, classified as an unknown outcome: the call answered no before its budget ran out, and the answer is an answer", wrapped)
		}
	}
	// The other direction still holds, so this test cannot be satisfied by a
	// classifier that calls everything an answer.
	if !outcomeUnknown(ErrOutcomeUnknown, false) {
		t.Fatal("ErrOutcomeUnknown was not classified as an unknown outcome")
	}
	if !outcomeUnknown(fmt.Errorf("enqueue: %w", ErrOutcomeUnknown), false) {
		t.Fatal("a wrapped ErrOutcomeUnknown was not classified as an unknown outcome")
	}
}

func TestARefusalAcrossTheDeadlineIsStillARefusal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &refusingEnqueueClient{now: &now, spend: time.Second}
	c.statuses = []Status{
		{ID: "PR_6499", State: "OPEN"},
		// Another operator's entry: the reconciliation's fresh read finds this and
		// would report it as this invocation's success.
		{ID: "PR_6499", State: "OPEN", Entry: queueEntry(3)},
	}
	clock := Clock{
		Now:   func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
	out, exitErr := Execute(context.Background(), opts(time.Second), c, clock)
	if exitErr == nil {
		t.Fatalf("outcome = %+v with no error: a refused --jump was reported as success because another operator had queued the PR", out)
	}
	if exitErr.Kind != EnqueueFailed {
		t.Fatalf("kind = %v, want EnqueueFailed: the call returned a refusal, which is an answer even when the allowance expired while its output drained", exitErr.Kind)
	}
	// The answer is not just the code. A reconciled refusal spends a read looking
	// for this invocation's work and would find the other operator's entry; the
	// refused invocation must not read the queue at all after the mutation.
	if c.reads != 1 {
		t.Fatalf("%d status read(s) followed the refusal, want none: a refusal is an answer, and reconciling it is what let another operator's queue entry be reported as this invocation's success", c.reads-1)
	}
}

// refusingEnqueueClient returns a definitive refusal AFTER spending the whole
// wait, which is the drain crossing the deadline on a call that answered no.
type refusingEnqueueClient struct {
	fakeClient
	now   *time.Time
	spend time.Duration
	// reads counts the status reads, which is how the case sees whether the run
	// went on to reconcile a refusal it already had an answer for.
	reads int
}

func (c *refusingEnqueueClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	c.reads++
	return c.fakeClient.Status(ctx, repo, pr)
}

func (c *refusingEnqueueClient) Enqueue(ctx context.Context, _, _ string, _ bool) (*QueueEntry, error) {
	// The call waits out its allowance and THEN reports the refusal, which is the
	// reviewer's interleaving: gh answered no just before the deadline and the
	// drain crossed it, so the context is expired while the answer is definite.
	// The clock is advanced first so the post-deadline allowance is still there for
	// a reconciliation read to spend -- if the run wrongly reconciles.
	*c.now = c.now.Add(c.spend - time.Millisecond)
	<-ctx.Done()
	return nil, &RefusalError{Detail: "the pull request is already in the merge queue, so --jump was refused"}
}

// TestAStalledStartingLookupIsBounded pins that the first reads are bounded too.
//
// The starting status and merge probes ran on the run's signal-only context,
// which carries no deadline: a stalled `gh pr view` held a `--wait 570`
// invocation for as long as GitHub took, before the wait had even started, so
// the number the operator asked for bounded nothing. They are given a finite
// allowance now, and an allowance that expires is a lookup failure (exit 2)
// rather than an interrupt -- the operator stopped nothing.
//
// The assertion is on the deadline the read RECEIVES, not on waiting one out:
// waiting a real allowance to expire would spend the suite's half-minute proving
// that a timer fires, and the finding is about which budget applies.
func TestAStalledStartingLookupIsBounded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := Clock{
		Now: func() time.Time { return now },
		// The wait is never entered: the starting read answers first, so this fake
		// only has to keep a real hour from being waited out.
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
	var stdout, stderr strings.Builder
	c := &stalledStartingClient{}
	_ = Run(context.Background(), opts(time.Hour), c, clock, &stdout, &stderr)
	if !c.bounded {
		t.Fatal("the first status read was issued on a context carrying no deadline: a stalled gh pr view holds the whole invocation before the wait it was asked for even starts")
	}
	// The run's own cancellation is still the interrupt, and the allowance is not
	// allowed to become one: an expired starting read is exit 2, not 130.
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := &cancelledStartingClient{cancel: cancel}
	if code := Run(cancelCtx, opts(time.Hour), cancelled, clock, &stdout, &stderr); code != InterruptedExit {
		t.Fatalf("Run = exit %d (stderr %q), want %d: a starting read ended by the operator's signal is not a lookup failure", code, stderr.String(), InterruptedExit)
	}
}

// stalledStartingClient records whether its first read carried a deadline, which
// is what tells a bounded read from one that can hold the run forever.
type stalledStartingClient struct {
	fakeClient
	bounded bool
	// seen says the first read has been recorded. Only that one is the starting
	// lookup: the wait's own polls also reach Status, and one of them overwriting
	// the answer hid an unbounded starting read on the first run of this case.
	seen bool
}

func (c *stalledStartingClient) Status(ctx context.Context, _, _ string) (Status, error) {
	if !c.seen {
		c.seen = true
		_, c.bounded = ctx.Deadline()
	}
	return Status{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}, nil
}

// cancelledStartingClient cancels the run on its first read, which is the
// signal arriving while the starting lookup is in flight.
type cancelledStartingClient struct {
	fakeClient
	cancel func()
}

func (c *cancelledStartingClient) Status(ctx context.Context, _, _ string) (Status, error) {
	c.cancel()
	<-ctx.Done()
	return Status{}, ctx.Err()
}

// TestTheConfirmationSpendsThePostDeadlineAllowance pins that a budget boundary
// is not a verdict.
//
// An enqueue that succeeds just before the deadline leaves the confirmation less
// time than the read needs. Reporting the queue unread there loses the only read
// that could confirm an accepted mutation, while the same read starting a moment
// later would have had a full allowance: the outcome depends on a boundary
// nobody asked about. The confirmation therefore draws on the same shared
// post-deadline allowance the wait's last reads use.
func TestTheConfirmationSpendsThePostDeadlineAllowance(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	// The mutation spends the whole wait, so the confirmation starts with nothing
	// left of the caller's budget.
	c := &spendingEnqueueClient{now: &now, spend: time.Second}
	c.statuses = []Status{
		{ID: "PR_6499", State: "OPEN"},
		{ID: "PR_6499", State: "OPEN", Entry: queueEntry(2)},
	}
	// The clock has to read the same variable the client advances: a fakeClock
	// value copies the instant, so the run would see a clock that never moved and
	// the case would pass whatever the confirmation did.
	clock := Clock{
		Now:   func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
	out, exitErr := Execute(context.Background(), opts(time.Second), c, clock)
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the confirmation to draw on the post-deadline allowance and report where the accepted enqueue landed", exitErr)
	}
	// The mutation spent the whole wait, so the run's verdict is a timeout -- but
	// throws the confirmation away. The position is what proves the confirmation
	// read happened.
	if out.Kind != TimedOut || out.Position != 2 {
		t.Fatalf("outcome = %+v, want TimedOut at position 2: the enqueue was accepted, the confirmation read confirmed it, and only the wait had run out", out)
	}
}

// spendingEnqueueClient spends its whole wait inside the enqueue call, the way a
// mutation that answers just before the deadline does, and then lets the reads
// that follow answer.
type spendingEnqueueClient struct {
	fakeClient
	now   *time.Time
	spend time.Duration
}

func (c *spendingEnqueueClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	*c.now = c.now.Add(c.spend)
	return nil, nil
}

// TestAConfirmationThatStartsBeforeTheDeadlineStillGetsTheAllowance pins the
// half of the rule the previous case could not see: the confirmation is not stuck
// with the fraction of the wait the mutation happened to leave.
//
// An enqueue accepted a moment before expiry leaves, say, a millisecond of the
// caller's wait. The read then got that millisecond and nothing else, so it
// failed -- while the identical read starting a moment later, after the deadline,
// drew the whole post-deadline allowance. Whether an accepted enqueue is
// confirmed must not depend on which side of a boundary the mutation landed on.
//
// The assertion is on the budget the read RECEIVES, because that is the whole of
// the finding and waiting the allowance out would spend the suite's time proving
// that a timer fires. A run that honours only the leftover hands the read
// about a millisecond; one that honours the shared cutoff hands it the allowance.
func TestAConfirmationThatStartsBeforeTheDeadlineStillGetsTheAllowance(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &confirmationBudgetClient{now: &now}
	c.statuses = []Status{
		{ID: "PR_6499", State: "OPEN"},
		{ID: "PR_6499", State: "OPEN", Entry: queueEntry(6)},
	}
	clock := Clock{
		Now:   func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
	// A two-second wait and a mutation that spends all but a millisecond of it:
	// the confirmation starts BEFORE the deadline, with 1ms of the wait left.
	c.spend = 2*time.Second - time.Millisecond
	if _, exitErr := Execute(context.Background(), opts(2*time.Second), c, clock); exitErr != nil {
		t.Fatalf("Execute: %v, want the confirmation to answer", exitErr)
	}
	if c.confirmationBudget <= time.Second {
		t.Fatalf("the confirmation was handed %v of budget, want the shared post-deadline allowance: a mutation accepted just before expiry leaves it a sliver of the wait, and the same read one moment later would have had the whole allowance", c.confirmationBudget)
	}
}

// confirmationBudgetClient records the budget the read after the mutation
// receives, read back from the deadline the run builds its context with.
type confirmationBudgetClient struct {
	fakeClient
	now *time.Time
	// spend is what the mutation costs, measured on the run's clock.
	spend time.Duration
	// enqueueCalled says the mutation has run, so the reads being seen are the
	// confirmation and the wait's own polls rather than the starting lookup.
	enqueueCalled bool
	// confirmationBudget is the time the FIRST read after the mutation was given,
	// as the run's context reports it.
	confirmationBudget time.Duration
	confirmationReads  int
}

func (c *confirmationBudgetClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	*c.now = c.now.Add(c.spend)
	c.enqueueCalled = true
	return nil, nil
}

func (c *confirmationBudgetClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if c.enqueueCalled {
		c.confirmationReads++
		if c.confirmationReads == 1 {
			// The context's deadline is wall-clock, and the run built it from the
			// budget it computed: the distance to it is that budget.
			if deadline, ok := ctx.Deadline(); ok {
				c.confirmationBudget = time.Until(deadline)
			}
		}
	}
	return c.fakeClient.Status(ctx, repo, pr)
}

// TestANoWaitMutationCostsTheConfirmationItsAllowance pins the `--wait 0` half of
// the same finding.
//
// With no wait there is no wait deadline for the post-deadline cutoff to hang
// off, so it was anchored to the instant BEFORE the mutation ran. A mutation that
// spends the allowance then leaves the reconciliation read an already-expired
// context: the read never starts, the run cannot learn whether the enqueue
// landed, and it reports failure for work that may have succeeded. The cutoff now
// starts when the reconciliation read starts.
//
// The mutation's ending here is a lost reply rather than an expired context, and
// that is deliberate. Both are the unknown-outcome branch, and this case is about
// the confirmation's budget on that branch. Driving it through a genuinely
// expired mutation would cost a real enqueue allowance -- thirty seconds of
// suite time for a branch this reaches directly.
func TestANoWaitMutationCostsTheConfirmationItsAllowance(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &spentAllowanceClient{now: &now}
	c.statuses = []Status{
		{ID: "PR_6499", State: "OPEN"},
		{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)},
	}
	// The clock has to read the same variable the fake advances. fakeClock copies
	// the instant into a field of its own, so a run given its clock would see a
	// clock that never moved -- and the pre-fix budget, anchored to the deadline,
	// would then look exactly like the fixed one. That is how this case passed
	// against the mutant on its first run.
	clock := Clock{
		Now:   func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
	out, exitErr := Execute(context.Background(), opts(0), c, clock)
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the confirmation read to happen and report where the enqueue landed: with no wait, the only limit that read has is the allowance, and it must be counted from when the read starts", exitErr)
	}
	if out.Kind != Queued || out.Position != 1 {
		t.Fatalf("outcome = %+v, want Queued at position 1: the mutation's ending was unknown, the read that followed found the entry", out)
	}
}

// spentAllowanceClient spends more than a whole post-deadline allowance inside
// the mutation, and then reports its outcome as unknown -- the shape of a
// mutation that ate the allowance and left nothing for its own reconciliation.
type spentAllowanceClient struct {
	fakeClient
	now           *time.Time
	enqueueCalled bool
}

func (c *spentAllowanceClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	*c.now = c.now.Add(2 * finalObservationAllowance)
	c.enqueueCalled = true
	return nil, fmt.Errorf("gh: %w", ErrIncompleteReply)
}

func (c *spentAllowanceClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if c.enqueueCalled {
		// A read handed an already-expired context never starts, which is what the
		// real gh call does too: refuse rather than answer.
		if err := ctx.Err(); err != nil {
			return Status{}, err
		}
	}
	return c.fakeClient.Status(ctx, repo, pr)
}

// TestAStalledRecheckKeepsTheConfirmedPosition pins that a recheck which never
// answers does not throw away an answer that already arrived.
//
// The first post-deadline poll confirms the PR is still queued at a known
// position. If the follow-up recheck then stalls, the run still has this
// instant's queue -- read after the deadline -- so reporting exit 2 ("nothing was
// decided") denies what it saw and stops the documented repeat-on-timeout
// workflow. The verdict is `MERGEQ timeout`, which is exit 1, with the position
// that read confirmed.
func TestAStalledRecheckKeepsTheConfirmedPosition(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &stallAfterFirstPostDeadlineClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(4)}}}
	c.now = &now
	clock := Clock{
		Now: func() time.Time { return now },
		// The sleep spends the wait, so the next read is a post-deadline poll.
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); c.waited = true; return true },
	}
	var stdout, stderr strings.Builder
	if code := Run(context.Background(), opts(time.Second), c, clock, &stdout, &stderr); code != 1 {
		t.Fatalf("Run = exit %d (stdout %q, stderr %q), want 1: the queue was confirmed after the deadline, so this is the repeat-on-timeout verdict and not a launcher failure", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "position=4") {
		t.Fatalf("stdout %q does not name the position the post-deadline read confirmed: the confirmed answer was discarded", stdout.String())
	}
}

// TestAFailedRecheckKeepsTheConfirmedPosition pins the other ending of the same
// recheck: it does not run out, it FAILS.
//
// A connection reset while rechecking costs the run a newer observation. It does
// not unlearn the position the poll before it confirmed, which was read after the
// deadline and is therefore this instant's queue. Reporting exit 2 there says
// nothing was decided, stops the documented repeat-on-timeout workflow, and
// differs from the identical read merely expiring -- which is exit 1 -- for a
// reason the operator cannot see or act on.
func TestAFailedRecheckKeepsTheConfirmedPosition(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &failingRecheckClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(4)}}}
	c.now = &now
	clock := Clock{
		Now: func() time.Time { return now },
		// The sleep spends the wait, so the poll that follows is a post-deadline
		// read: the position it confirms is this instant's queue.
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); c.waited = true; return true },
	}
	var stdout, stderr strings.Builder
	if code := Run(context.Background(), opts(time.Second), c, clock, &stdout, &stderr); code != 1 {
		t.Fatalf("Run = exit %d (stdout %q, stderr %q), want 1: the post-deadline poll confirmed position 4, and a recheck that failed afterwards cannot unread it", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "position=4") {
		t.Fatalf("stdout %q does not name the position the post-deadline poll confirmed: a transient failure on the recheck discarded an answer the run had already received", stdout.String())
	}
}

// failingRecheckClient answers the post-deadline poll and then fails every read
// after it, the way a connection reset does.
type failingRecheckClient struct {
	fakeClient
	now *time.Time
	// waited says the run has entered its wait, so the reads being seen are the
	// ones the finding is about rather than the starting lookup.
	waited   bool
	answered bool
}

func (c *failingRecheckClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if !c.waited {
		return c.fakeClient.Status(ctx, repo, pr)
	}
	if c.answered {
		return Status{}, errors.New("read tcp: connection reset by peer")
	}
	c.answered = true
	return c.fakeClient.Status(ctx, repo, pr)
}

// TestAnInterruptedRecheckIsNotATimeout pins that the operator's stop outranks a
// position the run confirmed after the deadline.
//
// The first post-deadline poll confirms the PR is still queued at a known
// position. The recheck after it is interrupted -- a SIGINT, the operator ending
// the wait -- and the run has that position in hand. The fallback below the
// interrupt check reports the confirmed position and exits 1, which the
// documented workflow tells the caller to repeat by running the command again.
// That is the wrong instruction for a wait the operator just cancelled: they
// ended it, they did not ask to be told to run it. Exit 130 is the code that
// says the wait was stopped rather than lost, and every other interrupt path
// already returns it.
//
// The fallback is still right for the two endings beside this one: a recheck
// that FAILS or runs out while the invocation is genuinely live leaves the run
// with the confirmed position and no newer answer, which is a timeout.
func TestAnInterruptedRecheckIsNotATimeout(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &interruptedRecheckClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(4)}}}
	c.now = &now
	c.cancel = cancel
	clock := Clock{
		Now: func() time.Time { return now },
		// The sleep spends the wait, so the next read is a post-deadline poll.
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); c.waited = true; return true },
	}
	var stdout, stderr strings.Builder
	if code := Run(ctx, opts(time.Second), c, clock, &stdout, &stderr); code != InterruptedExit {
		t.Fatalf("Run = exit %d (stdout %q, stderr %q), want %d: the operator stopped the final recheck, and reporting a timeout at exit 1 tells the caller to repeat a wait they cancelled", code, stdout.String(), stderr.String(), InterruptedExit)
	}
	if strings.Contains(stdout.String(), "timeout") {
		t.Fatalf("stdout %q reports a timeout: the run did not time out, it was stopped, and the confirmed position must not be printed as one", stdout.String())
	}
}

// interruptedRecheckClient answers the post-deadline poll and then cancels the
// run's context during the recheck, which is what a signal landing there does.
type interruptedRecheckClient struct {
	fakeClient
	now *time.Time
	// waited says the run has entered its wait, so the reads being seen are the
	// ones the finding is about rather than the starting lookup.
	waited   bool
	answered bool
	cancel   context.CancelFunc
}

func (c *interruptedRecheckClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if !c.waited {
		return c.fakeClient.Status(ctx, repo, pr)
	}
	if c.answered {
		c.cancel()
		return Status{}, context.Canceled
	}
	c.answered = true
	return c.fakeClient.Status(ctx, repo, pr)
}

// stallAfterFirstPostDeadlineClient answers the post-deadline poll, then reports
// its allowance as spent on every read after it, which is a recheck that never
// completes.
type stallAfterFirstPostDeadlineClient struct {
	fakeClient
	now *time.Time
	// waited says the run has entered its wait, so the reads being seen are the
	// ones the finding is about. Without it the STARTING read would set the
	// fixture's state, and every read after it would stall on a real thirty-second
	// context: the case would cost a minute and never reach the branch it names.
	waited   bool
	answered bool
}

// TestAPostDeadlineConfirmationKeepsItsPosition pins that an answer learned by
// the confirmation is not thrown away by a later recheck.
//
// The confirmation is the read that follows an enqueue. When it starts after the
// deadline it is the same observation a post-deadline poll makes -- this instant's
// queue -- and the timeout verdict is built from exactly that observation. The
// wait loop's own flag was the only thing that could say so, and a run whose
// confirmation had done the reading never set it. A stalled recheck then discarded
// a confirmed position and answered exit 2, where the same status from the loop
// would have printed `MERGEQ timeout` at exit 1 -- the documented retry path.
func TestAPostDeadlineConfirmationKeepsItsPosition(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &stallAfterConfirmationClient{statuses: []Status{
		{ID: "PR_6499", State: "OPEN"},
		{ID: "PR_6499", State: "OPEN", Entry: queueEntry(7)},
	}}
	c.now = &now
	// The mutation spends the whole wait, so the confirmation is a post-deadline
	// read -- the shape the finding names. It answers with position 7.
	c.spend = 2 * time.Second
	c.stall = finalObservationAllowance
	clock := Clock{
		Now:   func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
	var stdout, stderr strings.Builder
	if code := Run(context.Background(), opts(2*time.Second), c, clock, &stdout, &stderr); code != 1 {
		t.Fatalf("Run = exit %d (stdout %q, stderr %q), want 1: the confirmation read the queue after the deadline and confirmed position 7, so this is the repeat-on-timeout verdict and not a launcher failure", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "position=7") {
		t.Fatalf("stdout %q does not name the position the post-deadline confirmation read: the confirmed answer was discarded", stdout.String())
	}
}

// TestAConfirmationThatAnswersAfterTheDeadlineIsThisInstantsQueue pins the
// timestamp the post-deadline decision is made from.
//
// The rule is about the OBSERVATION, not the request that produced it. A
// confirmation that starts a moment before the deadline and answers a moment
// after it read the queue after the deadline: its answer is this instant's queue,
// and it is the timeout verdict. Reading the clock before the call instead calls
// that answer a read of the wait, so the run holds a confirmed position and
// reports that the queue was never read.
//
// The mutation here answers inside the wait; the confirmation then spends the
// REST of the wait and the shared allowance answering. It started before the
// deadline and answered after it, which is the shape the finding names.
func TestAConfirmationThatAnswersAfterTheDeadlineIsThisInstantsQueue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &slowConfirmationClient{statuses: []Status{
		{ID: "PR_6499", State: "OPEN"},
		{ID: "PR_6499", State: "OPEN", Entry: queueEntry(5)},
	}}
	c.now = &now
	// The confirmation answers position 5 and takes long enough that the clock is
	// past the deadline when it does.
	c.answerAfter = 2 * finalObservationAllowance
	clock := Clock{
		Now:   func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
	var stdout, stderr strings.Builder
	if code := Run(context.Background(), opts(time.Second), c, clock, &stdout, &stderr); code != 1 {
		t.Fatalf("Run = exit %d (stdout %q, stderr %q), want 1: the confirmation read position 5 after the deadline, so this is the repeat-on-timeout verdict and not a lookup failure", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "position=5") {
		t.Fatalf("stdout %q does not name the position the confirmation read: the answer arrived after the deadline and the run read the clock before the call, so a confirmed position was reported as never read", stdout.String())
	}
}

// slowConfirmationClient answers the confirmation after moving its clock past
// the deadline, which is a read whose request started inside the wait and whose
// answer arrived outside it.
type slowConfirmationClient struct {
	fakeClient
	now *time.Time
	// answerAfter is how long the confirmation appears to take, measured on the
	// run's own clock.
	answerAfter   time.Duration
	enqueueCalled bool
}

func (c *slowConfirmationClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	c.enqueueCalled = true
	return nil, nil
}

func (c *slowConfirmationClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if !c.enqueueCalled {
		return c.fakeClient.Status(ctx, repo, pr)
	}
	c.enqueueCalled = false
	// The answer is produced now, and the clock then moves past the deadline
	// before the run reads it: the observation is after the deadline even though
	// the call started before it.
	st, err := c.fakeClient.Status(ctx, repo, pr)
	*c.now = c.now.Add(c.answerAfter)
	return st, err
}

// stallAfterConfirmationClient spends the whole wait in the mutation, answers the
// confirmation, and then lets every later read run out of its allowance.
type stallAfterConfirmationClient struct {
	fakeClient
	now   *time.Time
	spend time.Duration
	stall time.Duration
	// enqueueCalled separates the starting lookup from the confirmation.
	enqueueCalled bool
	// confirmed says the confirmation has answered, so a read after it is the
	// recheck the finding is about.
	confirmed bool
}

func (c *stallAfterConfirmationClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	*c.now = c.now.Add(c.spend)
	c.enqueueCalled = true
	return nil, nil
}

func (c *stallAfterConfirmationClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if !c.enqueueCalled {
		return c.fakeClient.Status(ctx, repo, pr)
	}
	if !c.confirmed {
		c.confirmed = true
		// The confirmation answers, and it leaves the shared allowance spent: the
		// recheck after the wait loop then has nothing and stalls on what is left,
		// which is the ending the finding describes.
		*c.now = c.now.Add(c.stall - time.Millisecond)
		return c.fakeClient.Status(ctx, repo, pr)
	}
	<-ctx.Done()
	return Status{}, ctx.Err()
}

func (c *stallAfterFirstPostDeadlineClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if !c.waited {
		return c.fakeClient.Status(ctx, repo, pr)
	}
	if c.answered {
		// The recheck waits out its own allowance, which is the ending observe()
		// classifies as expired rather than cancelled. An error returned without
		// waiting would look like a failed lookup and never reach the branch this
		// case is about.
		<-ctx.Done()
		return Status{}, ctx.Err()
	}
	c.answered = true
	// Leave the recheck a millisecond of the shared allowance: the poll straddled
	// the deadline late, so almost all of the post-deadline allowance is gone by
	// the time the recheck starts. A real expired context rather than a stand-in,
	// and it costs a millisecond rather than an interval.
	*c.now = c.now.Add(finalObservationAllowance - time.Millisecond)
	return c.fakeClient.Status(ctx, repo, pr)
}

// TestAPollThatAnswersAfterTheDeadlineIsThisInstantsQueue pins the same rule one
// place over: the wait loop's own poll.
//
// The last poll of a wait is scheduled to land on the deadline, and the read it
// makes finishes a moment later -- outside the wait the caller asked for. Reading
// the deadline before the call calls that a read of the wait, so the run ends
// with no post-deadline observation and reports exit 2 ("the queue could not be
// read") for a position it has in hand.
func TestAPollThatAnswersAfterTheDeadlineIsThisInstantsQueue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &slowPollClient{statuses: []Status{
		{ID: "PR_6499", State: "OPEN"},
		{ID: "PR_6499", State: "OPEN", Entry: queueEntry(9)},
	}}
	c.now = &now
	clock := Clock{
		Now: func() time.Time { return now },
		// The sleep spends the wait exactly, so the poll that follows starts ON the
		// deadline and answers just after it.
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); c.waited = true; return true },
	}
	var stdout, stderr strings.Builder
	if code := Run(context.Background(), opts(time.Second), c, clock, &stdout, &stderr); code != 1 {
		t.Fatalf("Run = exit %d (stdout %q, stderr %q), want 1: the poll answered position 9 after the deadline, so this is the repeat-on-timeout verdict", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "position=9") {
		t.Fatalf("stdout %q does not name the position the straddling poll read: the answer arrived after the deadline and was recorded as a read of the wait", stdout.String())
	}
}

// slowPollClient answers the wait's poll a moment after the deadline has passed,
// which is a poll scheduled on the deadline and answered after it.
type slowPollClient struct {
	fakeClient
	now    *time.Time
	waited bool
}

func (c *slowPollClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if !c.waited {
		return c.fakeClient.Status(ctx, repo, pr)
	}
	st, err := c.fakeClient.Status(ctx, repo, pr)
	*c.now = c.now.Add(time.Millisecond)
	return st, err
}

// TestTheWaitSpendsOnePostDeadlineAllowance pins how long a run may keep reading
// after the wait its caller asked for has run out.
//
// The reads that cannot draw on the wait get a post-deadline allowance, because
// the queue can finish in the moment after the deadline and a run that stopped
// looking at the deadline would miss that merge. Granting it to EACH of them is
// what made the limit untrue: the poll that straddled the deadline rounded its
// spent budget up to a fresh interval, and the recheck after the loop started
// another one, so `--wait 1` could go on reading for about a minute. One
// allowance bounds both, and the run's last read is inside it.
//
// The assertion is on the deadlines the reads RECEIVE, because that is what the
// wait controls: waiting a real allowance out would spend the suite's time
// proving that a timer fires.
func TestTheWaitSpendsOnePostDeadlineAllowance(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	start := now
	c := &deadlineRecordingClient{status: Status{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}}
	c.now = &now
	// Each read takes the whole allowance it is given, which is the stall the
	// finding describes: a run that grants a fresh allowance per observation then
	// goes on for a whole extra interval, and one that shares it does not.
	c.stall = finalObservationAllowance
	clock := Clock{
		Now: func() time.Time { return now },
		// Every sleep spends what it is given, so the wait's second is spent by the
		// first sleep and every read after it draws on the post-deadline allowance.
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); c.waited = true; return true },
	}
	var stdout, stderr strings.Builder
	_ = Run(context.Background(), opts(time.Second), c, clock, &stdout, &stderr)
	if len(c.duringWait) == 0 {
		t.Fatalf("no read was made during the wait, so this case did not reach the path it exists to test (reads %d, stdout %q)", c.reads, stdout.String())
	}
	// One allowance past the deadline, and no further. Since every read spends its
	// whole budget, the clock's final position IS how long the run got to keep
	// reading: a second allowance shows up here as another interval.
	if limit := time.Second + finalObservationAllowance; now.Sub(start) > limit {
		t.Fatalf("the run went on for %v against a --wait of %v, want at most %v: each post-deadline read starting its own allowance is how --wait 1 spent about a minute reading",
			now.Sub(start), time.Second, limit)
	}
}

// TestThePostEnqueueConfirmationIsBounded pins that the read which decides
// whether an enqueue landed carries a deadline.
//
// Both paths that refresh after the enqueue used the run's signal-only context,
// which carries no deadline at all, and the wait's own deadline was created only
// after that refresh returned. A `gh` call that stalls there therefore held the
// command for as long as GitHub took, on top of a --wait that had already
// started: the caller's limit bounded everything except the read that decided
// whether their PR was in the queue. `--wait 0` is the sharpest shape, because
// there is no wait at all and nothing else that could end the run.
//
// The assertion is on the deadline the read RECEIVES, not on a stall played out
// against the clock: waiting for a real allowance to expire would spend the
// suite's time proving that a timer fires.
func TestThePostEnqueueConfirmationIsBounded(t *testing.T) {
	t.Parallel()
	for _, wait := range []time.Duration{0, 60 * time.Second} {
		t.Run(fmt.Sprintf("wait=%s", wait), func(t *testing.T) {
			t.Parallel()
			c := &confirmationRecordingClient{statuses: []Status{
				{ID: "PR_6499", State: "OPEN"},
				{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)},
			}}
			now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
			clock := Clock{
				Now: func() time.Time { return now },
				// The sleep spends the budget, so a wait asked for one round of
				// polling ends rather than spinning: this case is about the deadline
				// the confirmation read carries, not about how long a wait runs.
				Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
			}
			_, _ = Execute(context.Background(), opts(wait), c, clock)
			if c.afterEnqueue == 0 {
				t.Fatalf("no status read followed the enqueue, so this case did not reach the read it exists to bound (reads %d)", c.reads)
			}
			if !c.firstAfterEnqueueBounded {
				t.Fatal("the status read that decides whether the enqueue landed was issued on a context carrying no deadline: a stalled gh call there holds the command past the limit the caller set, and the enqueue is never reported either way")
			}
		})
	}
}

// confirmationRecordingClient answers a PR as unqueued, then queued, and records
// whether the read AFTER the enqueue call carried a deadline.
type confirmationRecordingClient struct {
	statuses []Status
	reads    int
	// afterEnqueue counts the reads made once the enqueue call has run.
	afterEnqueue int
	// firstAfterEnqueueBounded says whether the FIRST read after the enqueue
	// carried a deadline. Only that one is the confirmation. The wait's own polls
	// also carry deadlines, so a client that recorded "any read after the
	// enqueue" would answer yes from a poll and hide an unbounded confirmation --
	// which is how this case passed against the mutant on its first run.
	firstAfterEnqueueBounded bool
	enqueueCalled            bool
}

func (c *confirmationRecordingClient) Status(ctx context.Context, _, _ string) (Status, error) {
	c.reads++
	if c.enqueueCalled {
		c.afterEnqueue++
		if c.afterEnqueue == 1 {
			_, c.firstAfterEnqueueBounded = ctx.Deadline()
		}
	}
	if len(c.statuses) == 0 {
		return Status{}, errors.New("confirmationRecordingClient: no status supplied")
	}
	st := c.statuses[0]
	if len(c.statuses) > 1 {
		c.statuses = c.statuses[1:]
	}
	return st, nil
}

func (c *confirmationRecordingClient) MergedCommit(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

func (c *confirmationRecordingClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	c.enqueueCalled = true
	return nil, nil
}

func (c *confirmationRecordingClient) QueueDrop(context.Context, string, string) (*DropEvent, error) {
	return nil, nil
}

func (c *confirmationRecordingClient) MergeGroupRuns(context.Context, string) ([]GroupRun, error) {
	return nil, nil
}

// deadlineRecordingClient records the budget every read is given, and answers as
// a PR that stays queued.
type deadlineRecordingClient struct {
	status Status
	reads  int
	// waited is set once the run has entered its wait, so the reads recorded
	// after it are the ones the wait makes.
	waited bool
	// duringWait records, per read made after the wait began, whether that read
	// carried a deadline.
	duringWait []bool
	// expiredReads counts reads the run issued on a context that was already over:
	// calls that could never reach GitHub. They are excluded from duringWait and
	// asserted separately, because a fake that counted them made a run with one
	// fewer real observation look correct.
	expiredReads int
	overBudget   bool
	// now and stall model a read that takes its whole allowance: each read moves
	// the run's clock on by stall. It is what makes the post-deadline allowance
	// observable at all, because a read that answers instantly leaves the next one
	// the full allowance and a run that starts two allowances then looks exactly
	// like one that shares a single one.
	now   *time.Time
	stall time.Duration
}

func (c *deadlineRecordingClient) record(ctx context.Context) {
	c.reads++
	if !c.waited {
		return
	}
	// A read whose context is already expired is a call that never starts, not an
	// observation. Counting it is what hid the loop's last poll being handed a
	// spent budget: the fake answered, the count looked right, and the real gh
	// would have failed before contacting anything. Every read this fake records
	// is therefore one that began with a live context.
	if ctx.Err() != nil {
		c.expiredReads++
		return
	}
	deadline, ok := ctx.Deadline()
	c.duringWait = append(c.duringWait, ok)
	// A budget reaching past what the wait has left is not the wait's budget: it
	// is the absence of one wearing a deadline.
	if ok && time.Until(deadline) > 2*pollInterval {
		c.overBudget = true
	}
}

func (c *deadlineRecordingClient) Status(ctx context.Context, _, _ string) (Status, error) {
	c.record(ctx)
	// The stall lands on the status read, which is the last read of an
	// observation: one observe() therefore spends one allowance, which is the
	// unit the finding is about. Charging it per read would count the merge
	// lookup as well and make one observation look like two allowances.
	//
	// Only after the wait has begun: the reads before it are not bounded by the
	// wait, and advancing the clock there would move the deadline the loop reads.
	if c.now != nil && c.waited {
		*c.now = c.now.Add(c.stall)
	}
	return c.status, nil
}

func (c *deadlineRecordingClient) MergedCommit(ctx context.Context, _, _ string) (string, bool, error) {
	c.record(ctx)
	return "", false, nil
}

func (c *deadlineRecordingClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	return nil, nil
}

func (c *deadlineRecordingClient) QueueDrop(context.Context, string, string) (*DropEvent, error) {
	return nil, nil
}

func (c *deadlineRecordingClient) MergeGroupRuns(context.Context, string) ([]GroupRun, error) {
	return nil, nil
}

// TestACompleteReplyAfterTheSignalIsNotAQueueVerdict pins the other half: a
// cancelled wait whose reply did arrive.
//
// gh writes its JSON and exits, and a descendant it left behind holds the output
// pipe open until the drain bound. The reply is complete and valid, so a run that
// interprets it reports the verdict it describes -- here, a merge -- for a wait
// the operator stopped, while the launcher reports 130 for the same invocation.
// The read is allowed to finish, and the answer to it is not used.
func TestACompleteReplyAfterTheSignalIsNotAQueueVerdict(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := Clock{
		Now: func() time.Time { return now },
		// The sleep spends the budget, so the read that follows is the last
		// observation and the one whose reply decides the run.
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d); return true },
	}
	c := &lateReplyClient{cancel: cancel}
	var stdout, stderr strings.Builder
	if code := Run(ctx, opts(30*time.Second), c, clock, &stdout, &stderr); code != InterruptedExit {
		t.Fatalf("Run = exit %d, want %d: the reply arrived after the operator cancelled, and reporting its verdict says a wait they stopped reached its own conclusion (stdout %q)", code, InterruptedExit, stdout.String())
	}
	// The run has to have REACHED the final observation. A client that cancels on
	// the initial status read never enters the wait, and the case then passes
	// without exercising the path it names -- which is what an earlier version of
	// this test did.
	if c.observedAt == noLateRead {
		t.Fatal("the run never made a read after the initial one, so no reply arrived late and the case proves nothing about the one it exists to reject")
	}
	// The wait reported where the PR entered the queue, which is not a terminal
	// verdict: it is the progress line the script always printed.
	if !strings.Contains(stdout.String(), "MERGEQ queued position=1") {
		t.Fatalf("the wait printed no queued progress line (stdout %q): the run did not reach the wait this case is about", stdout.String())
	}
	// And no terminal verdict: no MERGEQ line other than that progress line.
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		if strings.HasPrefix(line, "MERGEQ ") && line != "MERGEQ queued position=1" {
			t.Fatalf("a cancelled wait printed the terminal verdict %q: the reply arrived after the operator cancelled, and reporting it says a wait they stopped reached its own conclusion", line)
		}
	}
}

// observing names the read a lateReplyClient cancelled on.
//
// There is one name and not one per read: the client cannot see where the run
// is, so a fake that labelled a read "a poll" or "the final observation" would
// be inventing the very distinction the case is meant to prove, and an assertion
// on that label would only read its own bookkeeping back. What the client knows
// is that the first read must answer normally -- it is what places the PR in the
// queue -- and that a later read is the one whose reply the run must not use.
// The zero value is "no later read happened", which is the shape a client that
// cancelled on the first read leaves behind.
type observing int

const (
	// noLateRead is a client that never reached a read after the first: the
	// vacuous shape this case exists to rule out.
	noLateRead observing = iota
	// lateRead is a read made after the one that placed the PR in the queue. The
	// name says no more than the client can see: the run is waiting by then, and
	// the reply this read delivers is the one the run must not report.
	lateRead
)

// lateReplyClient answers the pre-wait reads normally, then delivers one
// complete reply after cancelling the run, saying the PR merged -- the verdict a
// run that trusted the late reply would print in place of the interrupt.
type lateReplyClient struct {
	cancel context.CancelFunc
	// statusCalls counts every status reply, so the client knows which read it is
	// answering.
	statusCalls int
	// observedAt records the read that delivered the late reply.
	observedAt observing
}

func (c *lateReplyClient) Status(context.Context, string, string) (Status, error) {
	c.statusCalls++
	// The first read places the PR in the queue, so the run enters its wait at
	// all. Cancelling here instead would test nothing the case names.
	if c.statusCalls == 1 {
		return Status{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}, nil
	}
	// Every later status read is the poll's or the final observation's. The latter
	// is the one that decides a timeout, so it is the one that cancels and then
	// answers in full.
	c.observedAt = lateRead
	c.cancel()
	return Status{ID: "PR_6499", State: mergedState, MergeCommit: new(string)}, nil
}

func (c *lateReplyClient) MergedCommit(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

func (c *lateReplyClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	return nil, nil
}

func (c *lateReplyClient) QueueDrop(context.Context, string, string) (*DropEvent, error) {
	return nil, nil
}

func (c *lateReplyClient) MergeGroupRuns(context.Context, string) ([]GroupRun, error) {
	return nil, nil
}

// TestAPollWhoseStatusQueryExhaustsItsBudgetStillGetsTheFinalObservation pins
// the read site M1 is about: the status query inside a poll.
//
// The poll's own allowance bounds the status read, and a descendant holding the
// output pipe can spend it after `gh pr view` has already answered that the PR is
// not merged. The read then produces no status, and the run used to report that
// as a lookup failure (exit 2) even though the invocation was live and the final
// observation had an allowance of its own waiting. The merge that landed in that
// window was never observed, and the caller was told nothing was decided.
//
// The poll keeps a sliver of the caller's wait, which is what makes the poll --
// not the shared post-deadline allowance -- the budget that runs out. The status
// read spends it, and the merge is visible to the observation that follows.
func TestAPollWhoseStatusQueryExhaustsItsBudgetStillGetsTheFinalObservation(t *testing.T) {
	t.Parallel()
	sha := "abcdef0123456789abcdef0123456789abcdef01"
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &pollBudgetClient{sha: sha, now: &now}
	c.statuses = []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(3)}}
	clock := Clock{
		Now: func() time.Time { return now },
		// The sleep leaves a millisecond of the caller's wait, so the poll reads
		// on that instead of drawing the post-deadline allowance.
		Sleep: func(_ context.Context, d time.Duration) bool { now = now.Add(d - time.Millisecond); return true },
	}
	out, exitErr := Execute(context.Background(), opts(400*time.Millisecond), c, clock)
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the merge the final observation read: a poll that spent its own allowance on a live invocation is not the end of the run", exitErr)
	}
	if out.Kind != Merged || out.SHA != sha {
		t.Fatalf("outcome = %+v, want Merged with sha %s: the final observation had its own allowance and read the merge", out, sha)
	}
}

// pollBudgetClient spends the poll's own allowance inside the status query after
// `gh pr view` has already answered, and reports the merge to the observation
// that follows -- the shape of a read whose pipes were held past its budget.
type pollBudgetClient struct {
	fakeClient
	now *time.Time
	// waited says the wait has begun, so the read being seen is the poll rather
	// than the starting lookup.
	waited bool
	// spent says the poll's status query has already exhausted its budget, which
	// is what makes the merge visible to the next observation.
	spent bool
	sha   string
}

func (c *pollBudgetClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if !c.waited || c.spent {
		return c.fakeClient.Status(ctx, repo, pr)
	}
	// The allowance runs out under a live invocation: gh answered nothing before
	// the descendant holding the pipe outlived the budget.
	c.spent = true
	*c.now = c.now.Add(time.Second)
	<-ctx.Done()
	return Status{}, ctx.Err()
}

func (c *pollBudgetClient) MergedCommit(context.Context, string, string) (string, bool, error) {
	if !c.waited {
		// The starting probe answers "not merged" before the wait begins.
		c.waited = true
		return "", false, nil
	}
	// The poll's own probe answers before its allowance is spent, so the status
	// query is the read that runs out. Once it has, the merge is what the final
	// observation sees.
	return c.sha, c.spent, nil
}

// TestAMergeThatOutlivedItsReadAllowanceIsStillAMerge pins the other two read
// sites M2 names, where a decoded MERGED result was discarded because the read's
// own allowance had expired.
//
// Both are M1's rule with the answer PRESENT instead of absent: a usable answer
// outranks an expired read allowance, and only an operator cancellation outranks
// the answer.
//
//   - mergedOutcome checked the read context, so a complete MERGED reply whose
//     pipe a descendant held past the poll's allowance was thrown away and the
//     run went on to report a timeout it had already been told not to.
//   - startupEnding turned a nil error into the lookup failure whenever the
//     starting allowance had expired, which discarded an initial status that
//     reported MERGED before Execute ever reached its merge check.
//
// The allowance is already spent when the call returns, which is the state an
// overrun leaves behind. Asserting the deadline the read RECEIVES rather than
// waiting one out is the same choice TestAStalledStartingLookupIsBounded makes:
// waiting a real allowance would spend the suite's half-minute proving a timer
// fires, and the finding is about which context decides.
func TestAMergeThatOutlivedItsReadAllowanceIsStillAMerge(t *testing.T) {
	t.Parallel()
	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	c := &fakeClient{mergedSHA: sha, merged: true}
	spent, stopSpent := context.WithTimeout(context.Background(), -time.Second)
	defer stopSpent()

	// The merge probe: the invocation is live, so the decoded merge stands.
	out, merged, exitErr := mergedOutcome(spent, context.Background(), opts(0), c)
	if exitErr != nil {
		t.Fatalf("mergedOutcome: %v, want the decoded merge: the read's own allowance expired, the reply arrived before it did, and nobody stopped the invocation", exitErr)
	}
	if !merged || out.Kind != Merged || out.SHA != sha {
		t.Fatalf("mergedOutcome = %+v merged=%v, want Merged with sha %s", out, merged, sha)
	}

	// The same probe with the invocation cancelled: the stop outranks the answer.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, exitErr := mergedOutcome(spent, cancelled, opts(0), c); exitErr == nil || exitErr.Kind != Interrupted {
		t.Fatalf("mergedOutcome under a cancelled invocation = %+v, want Interrupted: the operator's stop outranks the answer", exitErr)
	}

	// The starting status: a read that answered is not a lookup failure just
	// because the starting allowance expired alongside it.
	if exitErr := startupEnding(context.Background(), nil); exitErr != nil {
		t.Fatalf("startupEnding = %+v, want no refusal: an answer outranks the expired starting allowance", exitErr)
	}
	// And an unanswered read is still the lookup failure, not a merge.
	if exitErr := startupEnding(context.Background(), startupNotRead()); exitErr == nil || exitErr.Kind != LookupFailed {
		t.Fatalf("startupEnding with no answer = %+v, want LookupFailed", exitErr)
	}
}

// TestAConfirmedEnqueueWhoseRefreshFailsIsStillQueued pins the confirming-write
// medium: when the mutation's reply names a queue entry and the read after it
// fails, the run knows GitHub performed the enqueue.
//
// The position is the operator's whole question, and discarding it for the read's
// failure tells them nothing was decided about a write that landed -- and invites
// a retried `--jump`, which the queue then refuses because the PR is already
// there.
func TestAConfirmedEnqueueWhoseRefreshFailsIsStillQueued(t *testing.T) {
	t.Parallel()
	// The mutation reports the entry and an unknown ending, the shape a
	// field-resolution error leaves; the reconciliation read then fails.
	c := &confirmedRefreshFailedClient{}
	c.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main"}}
	out, exitErr := Execute(context.Background(), opts(0), c, (&fakeClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the position the mutation's reply named: GitHub confirmed the write and only the refresh failed", exitErr)
	}
	if out.Kind != Queued || out.Position != 5 {
		t.Fatalf("outcome = %+v, want Queued at position 5: that is the entry the mutation returned", out)
	}

	// The other direction, so this is not a licence to report any failure as
	// queued: a mutation whose reply named no entry leaves nothing confirmed, and
	// the failure stands.
	unconfirmed := &refreshFailedClient{}
	unconfirmed.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main"}}
	unconfirmed.enqueueErr = fmt.Errorf("gh: %w", ErrIncompleteReply)
	if _, exitErr := Execute(context.Background(), opts(0), unconfirmed, (&fakeClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}).clock()); exitErr == nil || exitErr.Kind != LookupFailed {
		t.Fatalf("refusal = %+v, want LookupFailed: no reply named an entry, so nothing established the enqueue", exitErr)
	}

	// A WAITING run keeps the position and spends its budget trying to read a
	// newer answer. Every read fails, so the wait runs out with the mutation's
	// reply as the last thing the run knows: the timeout verdict names the
	// position GitHub confirmed, at exit 1, and says the entry's state is
	// unavailable rather than printing an empty one.
	waiting := &confirmedRefreshFailedClient{}
	waiting.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main"}}
	waited, exitErr := Execute(context.Background(), opts(30*time.Second), waiting, (&fakeClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the timeout verdict at the confirmed position: the mutation's reply named the entry and no read answered afterwards", exitErr)
	}
	if waited.Kind != TimedOut || waited.Position != 5 || waited.QueueState != absent {
		t.Fatalf("outcome = %+v, want TimedOut at position 5 and the state %q: the position is GitHub's own answer and the state beside it is not", waited, absent)
	}

	// And an operator cancellation outranks the confirmed position: they stopped
	// the wait and did not ask for a verdict.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := &confirmedRefreshCancelledClient{cancel: cancel}
	cancelled.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main"}}
	if _, exitErr := Execute(ctx, opts(0), cancelled, (&fakeClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}).clock()); exitErr == nil || exitErr.Kind != Interrupted {
		t.Fatalf("refusal = %+v, want Interrupted: the operator's stop outranks the confirmed position", exitErr)
	}
	// The same stop with a wait asked for, on a live context of its own: the stop
	// arrives while the confirmation runs, and the confirmed position must not turn
	// a cancelled wait into a timeout verdict.
	waitCtx, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	cancelledWait := &confirmedRefreshCancelledClient{cancel: cancelWait}
	cancelledWait.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main"}}
	if _, exitErr := Execute(waitCtx, opts(30*time.Second), cancelledWait, (&fakeClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}).clock()); exitErr == nil || exitErr.Kind != Interrupted {
		t.Fatalf("refusal = %+v, want Interrupted: a stop during the confirmation outranks the confirmed position even when a wait was asked for", exitErr)
	}
}

// TestAConfirmedEnqueueKeepsWaitingForTheOutcome pins what a waiting run does
// when the enqueue is confirmed and the confirmation read fails: it waits.
//
// The position GitHub named is a write that landed, not a verdict. A waiting run
// asked to be told when the queue finished, the wait budget is almost all still
// there, and the queue can still merge or drop this pull request. Answering
// `MERGEQ timeout` from the mutation's reply alone skips every poll that could
// have observed either.
func TestAConfirmedEnqueueKeepsWaitingForTheOutcome(t *testing.T) {
	t.Parallel()
	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	t.Run("merge", func(t *testing.T) {
		t.Parallel()
		fc := &fakeClock{now: start}
		c := &confirmedThenRefreshedClient{mergedSHA: sha, mergedFromCall: 2}
		c.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main"}}
		out, exitErr := Execute(context.Background(), opts(90*time.Second), c, fc.clock())
		if exitErr != nil {
			t.Fatalf("Execute: %v", exitErr)
		}
		if out.Kind != Merged || out.SHA != sha {
			t.Fatalf("outcome = %+v, want Merged with sha %s: the wait had 90 seconds left and the queue merged the pull request inside it", out, sha)
		}
	})

	// The mutation reported no ending of its own and still named the entry, so the
	// confirmation is the read that failed. The wait is kept here too.
	t.Run("merge after a mutation that reported no failure", func(t *testing.T) {
		t.Parallel()
		fc := &fakeClock{now: start}
		c := &confirmedThenRefreshedClient{mergedSHA: sha, mergedFromCall: 2}
		c.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main"}}
		c.mutationReportedNoError = true
		out, exitErr := Execute(context.Background(), opts(90*time.Second), c, fc.clock())
		if exitErr != nil {
			t.Fatalf("Execute: %v", exitErr)
		}
		if out.Kind != Merged || out.SHA != sha {
			t.Fatalf("outcome = %+v, want Merged with sha %s: the mutation named the entry, only the confirmation failed, and the wait had 90 seconds left", out, sha)
		}
	})

	t.Run("drop", func(t *testing.T) {
		t.Parallel()
		fc := &fakeClock{now: start}
		c := &confirmedThenRefreshedClient{}
		c.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main"}}
		c.drop = &DropEvent{CreatedAt: start.Add(-time.Minute), Reason: "failed_checks"}
		out, exitErr := Execute(context.Background(), opts(90*time.Second), c, fc.clock())
		if exitErr != nil {
			t.Fatalf("Execute: %v, want the drop the poll observed", exitErr)
		}
		if out.Kind != Dropped || out.PRState != "OPEN" {
			t.Fatalf("outcome = %+v, want Dropped: the poll after the confirmation read the entry gone, and a confirmed enqueue that leaves no entry is a drop", out)
		}
	})

	// A fresh status outranks the mutation's reply: the poll answers with the
	// position the queue reports now, and the verdict is the timeout at that
	// position and state rather than at the position the mutation named.
	t.Run("fresh status", func(t *testing.T) {
		t.Parallel()
		fc := &fakeClock{now: start}
		c := &confirmedThenRefreshedClient{}
		c.statuses = []Status{
			{ID: "PR_6499", State: "OPEN", Base: "main"},
			{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(2)},
		}
		out, exitErr := Execute(context.Background(), opts(90*time.Second), c, fc.clock())
		if exitErr != nil {
			t.Fatalf("Execute: %v", exitErr)
		}
		if out.Kind != TimedOut || out.Position != 2 || out.QueueState != "QUEUED" {
			t.Fatalf("outcome = %+v, want TimedOut at position 2 in QUEUED: that is the read the poll made, and it is newer than the mutation's reply", out)
		}
	})
}

// confirmedThenRefreshedClient answers the mutation with the entry its reply can
// name and an unknown ending, fails the confirmation read, and then answers every
// later read from the supplied data -- the shape of an enqueue that landed whose
// first refresh hit a connection reset.
type confirmedThenRefreshedClient struct {
	fakeClient
	// mutationReportedNoError makes the mutation return the entry with a nil
	// error, so the confirmation is the only read that failed. The zero value is
	// the entry beside an unknown ending, which is what a reply GitHub applied and
	// then failed to resolve looks like.
	mutationReportedNoError bool
	// reads counts status reads, so the client can tell the starting lookup and
	// the confirmation from the polls that follow.
	reads int
}

func (c *confirmedThenRefreshedClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	if c.mutationReportedNoError {
		return &QueueEntry{Position: 5}, nil
	}
	return &QueueEntry{Position: 5}, fmt.Errorf("gh: %w", ErrIncompleteReply)
}

func (c *confirmedThenRefreshedClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	c.reads++
	if c.reads == 2 {
		// The confirmation.
		return Status{}, errors.New("read tcp: connection reset by peer")
	}
	return c.fakeClient.Status(ctx, repo, pr)
}

// confirmedRefreshFailedClient answers the mutation with the entry its reply can
// name -- the position, because that is all the mutation asks for -- and an
// unknown ending, then fails the reconciliation read.
type confirmedRefreshFailedClient struct{ fakeClient }

func (c *confirmedRefreshFailedClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	return &QueueEntry{Position: 5}, fmt.Errorf("gh: %w", ErrIncompleteReply)
}

func (c *confirmedRefreshFailedClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if c.StatusCalls() > 0 {
		return Status{}, errors.New("read tcp: connection reset by peer")
	}
	return c.fakeClient.Status(ctx, repo, pr)
}

// refreshFailedClient fails the reconciliation read after a mutation that named
// no entry.
type refreshFailedClient struct{ fakeClient }

func (c *refreshFailedClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	return nil, c.enqueueErr
}

func (c *refreshFailedClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if c.StatusCalls() > 0 {
		return Status{}, errors.New("read tcp: connection reset by peer")
	}
	return c.fakeClient.Status(ctx, repo, pr)
}

// confirmedRefreshCancelledClient cancels the invocation while the reconciliation
// read runs, after the mutation named an entry.
type confirmedRefreshCancelledClient struct {
	fakeClient
	cancel func()
}

func (c *confirmedRefreshCancelledClient) Enqueue(context.Context, string, string, bool) (*QueueEntry, error) {
	return &QueueEntry{Position: 5, State: "QUEUED"}, nil
}

func (c *confirmedRefreshCancelledClient) Status(ctx context.Context, repo, pr string) (Status, error) {
	if c.StatusCalls() > 0 {
		c.cancel()
		<-ctx.Done()
		return Status{}, ctx.Err()
	}
	return c.fakeClient.Status(ctx, repo, pr)
}

// TestASupplementalProbeThatTimesOutKeepsTheStatusItSupplemented pins the startup
// medium: the status read answers, and the `gh pr view` probe that only upgrades
// a queue read into a merge then spends the starting allowance.
//
// The probe is supplemental to the status, so its ending cannot discard the
// position the run was given. `--wait 0` used to exit 2 there instead of naming
// where the PR is queued, for a read that succeeded.
//
// The allowance is already spent when the probe returns, which is the state an
// overrun leaves behind. Driving startupProbeEnding with that ending asserts the
// deadline rule rather than waiting a real thirty-second allowance out to prove a
// timer fires -- the same choice TestAStalledStartingLookupIsBounded and
// TestReadStatusKeepsAnAnswerItsOwnAllowanceOutlived make.
func TestASupplementalProbeThatTimesOutKeepsTheStatusItSuppressed(t *testing.T) {
	t.Parallel()
	// refusalFor answered the probe's own read context: an inner spent allowance
	// arrives as Interrupted with the invocation still live.
	spent := &ExitError{Kind: Interrupted, Detail: context.DeadlineExceeded.Error()}
	if got := startupProbeEnding(context.Background(), spent); got != nil {
		t.Fatalf("startupProbeEnding = %+v, want no ending: the probe only supplements a status that already answered", got)
	}

	// The operator's stop is not the allowance, and it outranks the answer.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := startupProbeEnding(cancelled, spent); got == nil || got.Kind != Interrupted {
		t.Fatalf("startupProbeEnding under a cancelled invocation = %+v, want Interrupted: the operator's stop outranks the status the probe supplements", got)
	}

	// A real defect still passes through: this drops the allowance's ending, not
	// every failure the probe can report.
	defect := &ExitError{Kind: LookupFailed, Detail: "decode pr view"}
	if got := startupProbeEnding(context.Background(), defect); got != defect {
		t.Fatalf("startupProbeEnding = %+v, want the defect it was given", got)
	}

	// And a probe that answered leaves nothing behind.
	if got := startupProbeEnding(context.Background(), nil); got != nil {
		t.Fatalf("startupProbeEnding = %+v, want no ending", got)
	}
}

// TestASupplementalProbeKeepsThePositionEndToEnd is the same finding driven
// through Execute: the status answers, the probe returns the allowance's ending,
// and `--wait 0` reports the position rather than exit 2. A decoded merge still
// wins over the status it supplements.
func TestASupplementalProbeKeepsThePositionEndToEnd(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := &startupProbeClient{}
	c.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(4)}}
	out, exitErr := Execute(context.Background(), opts(0), c, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the position the status read reported: the probe that supplements it could not be read", exitErr)
	}
	if out.Kind != Queued || out.Position != 4 {
		t.Fatalf("outcome = %+v, want Queued at position 4", out)
	}

	// A decoded merge still wins over the status it supplements.
	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	merged := &startupProbeClient{sha: sha, mergeWins: true}
	merged.statuses = []Status{{ID: "PR_6499", State: "OPEN", Base: "main", Entry: queueEntry(4)}}
	out, exitErr = Execute(context.Background(), opts(0), merged, (&fakeClock{now: now}).clock())
	if exitErr != nil {
		t.Fatalf("Execute: %v, want the merge the probe read", exitErr)
	}
	if out.Kind != Merged || out.SHA != sha {
		t.Fatalf("outcome = %+v, want Merged with sha %s", out, sha)
	}
}

// startupProbeClient answers Status, then reports the starting allowance's own
// ending from the merge probe -- the shape a stalled drain leaves.
type startupProbeClient struct {
	fakeClient
	sha       string
	mergeWins bool
}

func (c *startupProbeClient) MergedCommit(context.Context, string, string) (string, bool, error) {
	if c.mergeWins {
		return c.sha, true, nil
	}
	// No answer, and the allowance spent: mergedOutcome reports the read context's
	// ending for this, and the invocation is still live.
	return "", false, context.DeadlineExceeded
}

// TestRunExitCodes pins the contract the launcher's callers depend on: 0 for
// queued and merged, 1 for drop and timeout, 2 for every refusal.
func TestRunExitCodes(t *testing.T) {
	t.Parallel()
	sha := "abcdef0123456789abcdef0123456789abcdef01"
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		c    Client
		wait time.Duration
		code int
		// marker is the documented outcome marker this ending prints, and
		// fields are the machine-readable values that must appear on the same
		// line. They are the contract a caller parses; the prose around them is
		// not asserted.
		marker string
		fields []string
	}{
		{
			name: "queued",
			c: &fakeClient{statuses: []Status{
				{ID: "PR_6499", State: "OPEN"},
				{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)},
			}},
			code:   0,
			marker: "MERGEQ queued",
			fields: []string{"position=1"},
		},
		{
			name:   "merged",
			c:      &fakeClient{statuses: []Status{{ID: "PR_6499", State: "MERGED", MergeCommit: &sha}}},
			code:   0,
			marker: "MERGEQ merged",
			fields: []string{"sha=" + sha},
		},
		{
			name: "dropped",
			c: &fakeClient{
				statuses: []Status{
					{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)},
					{ID: "PR_6499", State: "OPEN", Base: "main"},
				},
				drop: &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"},
				runs: []GroupRun{{
					DatabaseID: 987654321,
					URL:        "https://example.invalid/runs/987654321",
					Conclusion: "failure",
					HeadBranch: "gh-readonly-queue/main/pr-6499-deadbeef",
				}},
			},
			wait:   time.Minute,
			code:   1,
			marker: "MERGEQ dropped",
			fields: []string{"state=OPEN", "987654321", "https://example.invalid/runs/987654321"},
		},
		{
			name:   "timed out",
			c:      &fakeClient{statuses: []Status{{ID: "PR_6499", State: "OPEN", Entry: queueEntry(1)}}},
			wait:   time.Minute,
			code:   1,
			marker: "MERGEQ timeout",
			fields: []string{"position=1", "state=QUEUED"},
		},
		{
			name: "refused",
			c:    &fakeClient{statusErr: errors.New("HTTP 500")},
			code: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr strings.Builder
			code := Run(context.Background(), opts(tc.wait), tc.c, (&fakeClock{now: now}).clock(), &stdout, &stderr)
			if code != tc.code {
				t.Fatalf("Run = exit %d, want %d (stdout %q, stderr %q)", code, tc.code, stdout.String(), stderr.String())
			}
			if code == 2 {
				if stdout.Len() != 0 || stderr.Len() == 0 {
					t.Fatalf("a refusal belongs on stderr only: stdout %q, stderr %q", stdout.String(), stderr.String())
				}
				return
			}
			if stderr.Len() != 0 {
				t.Fatalf("an outcome run wrote stderr %q", stderr.String())
			}
			// The last line is always the outcome, and it carries this ending's
			// own marker and fields: a waiting run prints the queued position
			// first, and a caller reads the verdict from the final line. A
			// renderer that printed the wrong marker, or dropped a field, must
			// fail here even when the exit code is right.
			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			last := lines[len(lines)-1]
			if !strings.HasPrefix(last, tc.marker) {
				t.Fatalf("last line = %q, want the %q outcome line", last, tc.marker)
			}
			for _, field := range tc.fields {
				if !strings.Contains(last, field) {
					t.Fatalf("last line = %q, want it to carry %q", last, field)
				}
			}
		})
	}
}
