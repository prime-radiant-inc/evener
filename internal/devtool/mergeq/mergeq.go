// Package mergeq decides a pull request's place in its base branch's GitHub
// merge queue, and what the operator should be told about it.
//
// It is the port of scripts/ops/merge-enqueue.sh's orchestration. That script
// had outgrown shell: the only way to exercise its decisions was to write a
// fake `gh` into a temp directory and put it on PATH, which
// docs/developing-evener/testing.md bans outright — a fake on PATH tests the
// fake's argument handling, and the argv a script hands it is an
// implementation restated. The script keeps its name, its arguments and its
// documented outcome lines as a launcher; the decisions live here, where a
// test supplies the data GitHub answers with (the port-on-touch move
// cmd/evener-dev/timingparse.go made for the timing parser).
//
// The contract the launcher and this package share is the script's own:
//
//	MERGEQ queued position=P            (no --wait)
//	MERGEQ merged sha=SHA               (exit 0)
//	MERGEQ dropped: <reason>            (exit 1: left the queue unmerged)
//	MERGEQ timeout position=P state=S   (exit 1: still queued after the wait)
//
// Exit 2: a usage error, or the enqueue call failed.
package mergeq

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// absent is what a field the read did not supply renders as.
	absent = "-"
	// mergedState is the pull request state that ends a run.
	mergedState = "MERGED"
	// pollInterval bounds how long one wait sleeps at a time.
	pollInterval = 30 * time.Second
	// stalenessWindow is how old a queue removal event may be and still
	// explain the drop it is read for. An older one is a prior drop's event,
	// not this drop's reason.
	stalenessWindow = 10 * time.Minute
	// genericRun names the merge-group run when the status read supplied no
	// base branch to build the queue branch prefix from.
	genericRun = "the merge-group run for this PR"
)

// Options is one run's request.
type Options struct {
	Repo string
	PR   string
	// Wait is how long to poll after enqueueing; zero means report the queue
	// position and stop.
	Wait time.Duration
	// Jump asks for the front of the queue as the PR is enqueued (admins
	// only). A PR already queued has no enqueue call to carry the jump.
	Jump bool
	// Progress, when set, receives the queued outcome a waiting run reports
	// before it starts waiting. The outcome line is the last one either way.
	Progress func(Outcome)
}

// Clock is the wait's notion of time, so a test can advance it instead of
// sleeping. Production passes SystemClock.
type Clock struct {
	Now   func() time.Time
	Sleep func(time.Duration)
}

// SystemClock is the real clock.
var SystemClock = Clock{Now: time.Now, Sleep: time.Sleep}

// Status is the pull request's own state, its queue entry and its base branch,
// read at one instant. Base is empty when the read did not supply one, and
// Entry is nil when the PR is not queued.
type Status struct {
	ID          string
	State       string
	MergeCommit *string
	Base        string
	Entry       *QueueEntry
}

// QueueEntry is a queued pull request's place in the merge queue.
type QueueEntry struct {
	Position int64
	State    string
}

// MergeSHA renders the pull request's merge commit, or "-" when it has none.
func (s Status) MergeSHA() string {
	if s.MergeCommit == nil {
		return absent
	}
	return *s.MergeCommit
}

// GroupRun is one merge-group run, as `gh run list` reports it.
type GroupRun struct {
	DatabaseID int64
	URL        string
	Conclusion string
	HeadBranch string
}

// DropEvent is the queue's own removal event for a pull request.
type DropEvent struct {
	CreatedAt time.Time
	Reason    string
}

// Client answers the questions the queue's decisions rest on. Production
// answers them with the gh CLI (GHClient); a test answers them with the data
// GitHub would, which is a data seam rather than a faked toolchain.
type Client interface {
	// Status reads the pull request's state, merge commit, base branch and
	// queue entry.
	Status(ctx context.Context, repo, pr string) (Status, error)
	// MergedCommit reports the merge commit when the pull request's own state
	// says MERGED. The queue snapshot can lag a merge by an interval or more,
	// so this, not the snapshot, decides whether a wait is over.
	MergedCommit(ctx context.Context, repo, pr string) (sha string, merged bool, err error)
	// Enqueue adds the pull request to its base branch's merge queue.
	Enqueue(ctx context.Context, repo, prID string, jump bool) error
	// QueueDrop reads the latest queue removal event, or nil when the
	// timeline holds none.
	QueueDrop(ctx context.Context, repo, pr string) (*DropEvent, error)
	// MergeGroupRuns lists the most recent merge-group runs of the repository,
	// newest first.
	MergeGroupRuns(ctx context.Context, repo string) ([]GroupRun, error)
}

// Kind is how a run ended.
type Kind int8

const (
	// Queued: the PR is in the queue and no wait was asked for.
	Queued Kind = iota
	// Merged: the PR merged.
	Merged
	// Dropped: the PR left the queue unmerged.
	Dropped
	// TimedOut: the PR was still queued when the wait ran out.
	TimedOut
)

// Outcome is what a run decided, in the terms the outcome line reports.
type Outcome struct {
	Kind Kind
	// SHA is the merge commit, for Merged.
	SHA string
	// Position is the queue position, for Queued and TimedOut.
	Position int64
	// QueueState is the queue entry's own state, for TimedOut.
	QueueState string
	// PRState is the pull request's state, for Dropped.
	PRState string
	// Reason explains a Dropped outcome to the operator.
	Reason string
	// DropClass is what a Dropped outcome was classified as.
	DropClass DropClass
	// Run names the merge-group run a Dropped outcome points at. It is the
	// structured form of what Reason's prose says, so a caller (or a test) can
	// act on the run without reading the sentence.
	Run RunRef
}

// ExitError is a run that ends with exit 2.
type ExitError struct {
	Kind     ErrorKind
	Detail   string // the gh output that failed, when the kind carries one
	PRState  string // the pull request state an enqueue left unqueued
	Position int64  // the queue position a jump was refused at
}

// ErrorKind is which refusal ended the run.
type ErrorKind int8

const (
	// LookupFailed: the pull request could not be read.
	LookupFailed ErrorKind = iota
	// EnqueueFailed: the enqueue call failed.
	EnqueueFailed
	// EnqueueLeftNoEntry: the enqueue call returned and the PR is still not
	// queued.
	EnqueueLeftNoEntry
	// JumpRefused: --jump was asked for a PR that is already queued.
	JumpRefused
)

// String names the refusal for a diagnostic.
func (k ErrorKind) String() string {
	switch k {
	case LookupFailed:
		return "lookup failed"
	case EnqueueFailed:
		return "enqueue failed"
	case EnqueueLeftNoEntry:
		return "no queue entry after enqueueing"
	case JumpRefused:
		return "jump refused"
	}
	return "unknown failure"
}

// Error implements error.
func (e *ExitError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("%s: %s", e.Kind, e.Detail))
}

// Run executes one invocation, prints its outcome, and returns the exit code
// the script's contract documents: 0 for queued or merged, 1 for a drop or a
// timeout, 2 for a refusal.
//
// Every outcome line goes to stdout, including the two that exit 1: callers
// read `MERGEQ timeout` and `MERGEQ dropped` from the stream the script has
// always written them to (AGENTS.md tells an operator to repeat the command
// while it prints the timeout line). Only a refusal is diagnostic, and that
// goes to stderr.
func Run(ctx context.Context, opts Options, c Client, clock Clock, stdout, stderr io.Writer) int {
	opts.Progress = func(out Outcome) {
		line, _ := outcomeLine(out)
		_, _ = fmt.Fprintln(stdout, line)
	}
	out, err := Execute(ctx, opts, c, clock)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, refusalLine(err))
		return 2
	}
	line, code := outcomeLine(out)
	_, _ = fmt.Fprintln(stdout, line)
	return code
}

// Execute runs one invocation to an outcome, or to the refusal that ended it.
func Execute(ctx context.Context, opts Options, c Client, clock Clock) (Outcome, *ExitError) {
	st, exitErr := readStatus(ctx, opts, c)
	if exitErr != nil {
		return Outcome{}, exitErr
	}
	if st.State == mergedState {
		return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
	}
	if out, merged := mergedOutcome(ctx, opts, c); merged {
		return out, nil
	}
	if st.Entry == nil {
		if err := c.Enqueue(ctx, opts.Repo, st.ID, opts.Jump); err != nil {
			return Outcome{}, &ExitError{Kind: EnqueueFailed, Detail: err.Error()}
		}
		if st, exitErr = readStatus(ctx, opts, c); exitErr != nil {
			return Outcome{}, exitErr
		}
		if st.State == mergedState {
			return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
		}
		if st.Entry == nil {
			return Outcome{}, &ExitError{Kind: EnqueueLeftNoEntry, PRState: st.State}
		}
	} else if opts.Jump {
		// The jump rides on the enqueue call, which a queued PR does not get.
		return Outcome{}, &ExitError{Kind: JumpRefused, Position: st.Entry.Position}
	}
	if opts.Wait <= 0 {
		return Outcome{Kind: Queued, Position: st.Entry.Position}, nil
	}
	// Waiting runs report where the PR entered the queue before they report
	// where it ended up: that is the progress line the script always printed,
	// and the outcome line is still the last one.
	if opts.Progress != nil {
		opts.Progress(Outcome{Kind: Queued, Position: st.Entry.Position})
	}

	deadline := clock.Now().Add(opts.Wait)
	for {
		left := deadline.Sub(clock.Now())
		if left <= 0 {
			break
		}
		clock.Sleep(min(left, pollInterval))
		if out, merged := mergedOutcome(ctx, opts, c); merged {
			return out, nil
		}
		if st, exitErr = readStatus(ctx, opts, c); exitErr != nil {
			return Outcome{}, exitErr
		}
		if st.State == mergedState {
			return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
		}
		if st.Entry == nil {
			return dropOutcome(ctx, opts, st, c, clock.Now)
		}
	}
	// The loop's last read lands on the deadline, and the queue can finish in
	// the moment after it. A timeout verdict printed from that read reports a
	// merge that already happened, so observe once more before calling it.
	if out, merged := mergedOutcome(ctx, opts, c); merged {
		return out, nil
	}
	if st, exitErr = readStatus(ctx, opts, c); exitErr != nil {
		return Outcome{}, exitErr
	}
	if st.State == mergedState {
		return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
	}
	if st.Entry == nil {
		return dropOutcome(ctx, opts, st, c, clock.Now)
	}
	return Outcome{Kind: TimedOut, Position: st.Entry.Position, QueueState: st.Entry.State}, nil
}

// readStatus reads the status, turning a failed read into the run-ending
// refusal the script reported.
func readStatus(ctx context.Context, opts Options, c Client) (Status, *ExitError) {
	st, err := c.Status(ctx, opts.Repo, opts.PR)
	if err != nil {
		return Status{}, &ExitError{Kind: LookupFailed, Detail: err.Error()}
	}
	return st, nil
}

// mergedOutcome reports the merged outcome when the pull request's own state
// says MERGED, and false when it does not (or cannot be read, which the queue
// read that follows surfaces as a real failure).
func mergedOutcome(ctx context.Context, opts Options, c Client) (Outcome, bool) {
	sha, merged, err := c.MergedCommit(ctx, opts.Repo, opts.PR)
	if err != nil || !merged {
		return Outcome{}, false
	}
	return Outcome{Kind: Merged, SHA: sha}, true
}

// queuePrefix is the prefix every merge-group run's branch for this PR carries.
func queuePrefix(base, pr string) string {
	return fmt.Sprintf("gh-readonly-queue/%s/pr-%s-", base, pr)
}
