package mergeq

import (
	"context"
	"errors"
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
	enqueued   []enqueuedPull
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

func (f *fakeClient) Enqueue(_ context.Context, _ string, prID string, jump bool) error {
	if f.enqueueErr != nil {
		return f.enqueueErr
	}
	f.enqueued = append(f.enqueued, enqueuedPull{prID: prID, jump: jump})
	return nil
}

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

func (c *cancellingClient) Enqueue(context.Context, string, string, bool) error { return nil }

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
	for i, had := range c.duringWait {
		if !had {
			t.Fatalf("poll read %d of %d was issued with a context carrying no deadline: the wait's budget bounds only the sleeps, so one stalled gh call holds the run past the --wait the caller asked for", i+1, len(c.duringWait))
		}
	}
	if c.overBudget {
		t.Fatal("a read was given a budget reaching past the end of the wait, so the limit the caller set is not the limit the reads obey")
	}
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
	overBudget bool
}

func (c *deadlineRecordingClient) record(ctx context.Context) {
	c.reads++
	if !c.waited {
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
	return c.status, nil
}

func (c *deadlineRecordingClient) MergedCommit(ctx context.Context, _, _ string) (string, bool, error) {
	c.record(ctx)
	return "", false, nil
}

func (c *deadlineRecordingClient) Enqueue(context.Context, string, string, bool) error { return nil }

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
	if c.observedAt != finalObservation {
		t.Fatalf("the cancelling read was %v, want the final observation: the case has to cancel on the read whose reply commits the run to a verdict, or it proves nothing about it", c.observedAt)
	}
	// The wait reported where the PR entered the queue, which is not a terminal
	// verdict: it is the progress line the script always printed.
	if !strings.Contains(stdout.String(), "MERGEQ queued position=1") {
		t.Fatalf("the wait printed no queued progress line (stdout %q): the run did not reach the wait this case is about", stdout.String())
	}
	// And no terminal verdict: no MERGEQ line other than that progress line.
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if strings.HasPrefix(line, "MERGEQ ") && line != "MERGEQ queued position=1" {
			t.Fatalf("a cancelled wait printed the terminal verdict %q: the reply arrived after the operator cancelled, and reporting it says a wait they stopped reached its own conclusion", line)
		}
	}
}

// observing is the read a lateReplyClient cancelled on.
type observing int

const (
	// initialRead is the status read before the wait, which places the PR in the
	// queue.
	initialRead observing = iota
	// finalObservation is the read made after the wait's budget is spent, whose
	// reply is what a run would report as the outcome.
	finalObservation
	// queueRead is a poll's status read inside the wait.
	queueRead
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
	c.observedAt = finalObservation
	c.cancel()
	return Status{ID: "PR_6499", State: mergedState, MergeCommit: new(string)}, nil
}

func (c *lateReplyClient) MergedCommit(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

func (c *lateReplyClient) Enqueue(context.Context, string, string, bool) error { return nil }

func (c *lateReplyClient) QueueDrop(context.Context, string, string) (*DropEvent, error) {
	return nil, nil
}

func (c *lateReplyClient) MergeGroupRuns(context.Context, string) ([]GroupRun, error) {
	return nil, nil
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
