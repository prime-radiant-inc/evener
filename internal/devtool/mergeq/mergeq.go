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
	"errors"
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
	// finalObservationAllowance bounds the observation made after the wait's
	// budget has run out. The loop's reads are bounded by what is left of the
	// budget, so the read that catches a merge landing just after the deadline has
	// none left to inherit; it gets the same allowance a poll gets, because that
	// is this client's existing notion of how long one read may take. A bound is
	// what makes the wait's limit a limit: without it a stalled gh call holds the
	// run for as long as GitHub takes to answer, past the --wait the caller asked
	// for.
	finalObservationAllowance = pollInterval
	// startupAllowance bounds the two reads that place the pull request before
	// anything is decided: its state, and whether it has already merged.
	//
	// They ran on the run's signal-only context, which carries no deadline at
	// all, so a stalled `gh pr view` held a `--wait 570` invocation for as long as
	// GitHub took -- before the wait had even started, so the number the operator
	// asked for bounded nothing. The allowance is a read's notion of bounded, the
	// same one every other observation uses.
	startupAllowance = pollInterval
	// dropDiagnosticAllowance bounds the two reads that only EXPLAIN a drop the
	// status read already observed: the merge-group run and the removal reason.
	// They share one allowance, because neither is what decided the run -- the
	// drop was decided before either was asked -- and a diagnostic that can hold
	// the command past its own limit is a wait with no limit.
	dropDiagnosticAllowance = pollInterval
	// stalenessWindow is how old a queue removal event may be and still
	// explain the drop it is read for. An older one is a prior drop's event,
	// not this drop's reason.
	stalenessWindow = 10 * time.Minute
	// genericRun names the merge-group run when the status read supplied no
	// base branch to build the queue branch prefix from.
	genericRun = "the merge-group run for this PR"
	// InterruptedExit is the status a cancelled wait ends the run with:
	// 128+SIGINT, which is what a shell reports for an interrupted command. An
	// interrupt is never the timeout verdict, whose exit code means "still
	// queued, repeat the command".
	InterruptedExit = 130
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
	Now func() time.Time
	// Sleep waits for d, or until ctx ends, and reports false when the context
	// ended first. A cancelled wait abandons its poll instead of finishing the
	// interval: an operator's signal must land at once, not up to a poll
	// interval later.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// SystemClock is the real clock.
var SystemClock = Clock{Now: time.Now, Sleep: sleepContext}

// sleepContext is SystemClock's Sleep: a timer a cancelled context cuts short.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

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
	// Interrupted: the wait's context ended before the PR merged or left the
	// queue.
	Interrupted
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
	case Interrupted:
		return "interrupted"
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
//
// An interrupted wait is neither: it exits InterruptedExit with a diagnostic on
// stderr, because 1 means "still queued, repeat the command" and 2 means the
// queue refused. A signal must not be read as either.
func Run(ctx context.Context, opts Options, c Client, clock Clock, stdout, stderr io.Writer) int {
	opts.Progress = func(out Outcome) {
		line, _ := outcomeLine(out)
		_, _ = fmt.Fprintln(stdout, line)
	}
	out, err := Execute(ctx, opts, c, clock)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, refusalLine(err))
		if err.Kind == Interrupted {
			return InterruptedExit
		}
		return 2
	}
	line, code := outcomeLine(out)
	_, _ = fmt.Fprintln(stdout, line)
	return code
}

// Execute runs one invocation to an outcome, or to the refusal that ended it.
func Execute(ctx context.Context, opts Options, c Client, clock Clock) (Outcome, *ExitError) {
	// A wait the operator already stopped does not start: no request goes out
	// and no queue is touched.
	if err := ctx.Err(); err != nil {
		return Outcome{}, &ExitError{Kind: Interrupted, Detail: err.Error()}
	}
	// The starting lookups are bounded, and they are the only reads before the
	// caller's budget exists. On the signal-only context a stalled `gh pr view`
	// held a `--wait 570` invocation for as long as GitHub took, before the wait
	// even began: the number the operator asked for bounded nothing, because the
	// thing it was meant to bound had not started.
	//
	// An allowance that expires here is neither a cancellation nor a queue
	// verdict: the run never learned anything. That is the lookup failure exit 2
	// is for, and the operator's signal stays the interrupt it was.
	startCtx, stopStart := context.WithTimeout(ctx, startupAllowance)
	st, exitErr := readStatus(startCtx, opts, c)
	if exitErr == nil && st.State != mergedState {
		var out Outcome
		var merged bool
		out, merged, exitErr = mergedOutcome(startCtx, opts, c)
		if exitErr == nil && merged {
			stopStart()
			return out, nil
		}
	}
	// The ending is read before the cancel for the same reason the enqueue's is:
	// cancelling sets the context's error whatever it was, so a check that runs
	// afterwards sees Canceled and calls an expired allowance a cancellation.
	startExpired := errors.Is(startCtx.Err(), context.DeadlineExceeded)
	stopStart()
	if exitErr = startupEnding(ctx, startExpired, exitErr); exitErr != nil {
		return Outcome{}, exitErr
	}
	if st.State == mergedState {
		return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
	}
	// The caller's limit for the whole invocation, established BEFORE the enqueue
	// is confirmed. It used to be created after that confirmation returned, which
	// is a limit that starts after the thing it limits: a stalled refresh made the
	// wait wait, on top of a --wait that had already begun. Everything below --
	// the confirmation reads and the wait itself -- spends what is left of this.
	deadline := clock.Now().Add(opts.Wait)
	// Every read that cannot draw on the wait spends the same post-deadline
	// allowance, the confirmation included. It is created here so the
	// confirmation and the wait below cannot each mint one of their own.
	post := &postDeadline{}
	if st.Entry == nil {
		// The mutation is bounded too. On the signal-only context it had no limit at
		// all, so a stalled call blocked the confirmation from starting and held the
		// command for as long as GitHub took. It spends the same caller budget as
		// everything else here: a mutation that eats the whole --wait leaves the
		// confirmation with nothing, which it reports rather than spends twice.
		enqueueCtx, stopEnqueue := context.WithTimeout(ctx, remainingAllowance(opts, clock, deadline))
		enqueueErr := c.Enqueue(enqueueCtx, opts.Repo, st.ID, opts.Jump)
		// The ending is read BEFORE the cancel below, because cancelling sets the
		// context's error to Canceled whatever it was: asked afterwards, every
		// failure looked like an expired allowance. That is not a cosmetic
		// difference. A live parent plus a non-nil child error meant "uncertain",
		// so an explicit refusal -- GitHub declining this invocation's --jump --
		// was reconciled against a queue read, and another operator's entry for
		// the same pull request made the read find an entry and report success
		// for a mutation that was refused.
		enqueueExpired := errors.Is(enqueueCtx.Err(), context.DeadlineExceeded)
		stopEnqueue()
		if enqueueErr != nil {
			// A reply lost to the drain bound is not a refusal: GitHub may have
			// accepted the mutation before the answer was cut off, so calling it
			// a failure would report an enqueue that already landed. Read the
			// state once and let it decide. An explicit refusal, and a
			// cancellation, keep their own paths below and above.
			//
			// A mutation whose own deadline expired is the same question with the
			// same answer: the call may have been accepted before the allowance ran
			// out, so it is reconciled with a bounded read rather than reported as a
			// refusal. A cancellation is not uncertain -- the operator stopped it --
			// and refusalFor below is what tells that apart.
			//
			// Only the endings that leave the outcome unknown are reconciled.
			// Anything else the call reported is a refusal it stated, and a
			// refusal is an answer: reconciling it spends a read looking for a
			// queue entry that a different invocation may have put there.
			//
			// A RefusalError is checked first and outranks the expired context,
			// because the two can be true at once: gh can return the refusal just
			// before the budget expires, with the drain crossing it afterwards.
			// The context then says "expired" while the mutation's answer was a
			// definite no.
			var refusal *RefusalError
			uncertain := !errors.As(enqueueErr, &refusal) &&
				(errors.Is(enqueueErr, ErrIncompleteReply) || enqueueExpired)
			if !uncertain {
				return Outcome{}, refusalFor(ctx, EnqueueFailed, enqueueErr)
			}
			if st, exitErr = confirmEnqueue(ctx, opts, c, clock, deadline, post); exitErr != nil {
				return Outcome{}, exitErr
			}
			if st.State == mergedState {
				return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
			}
			if st.Entry == nil {
				// Still absent after a fresh read: the mutation did not land, and
				// the honest report is that the enqueue could not be confirmed.
				return Outcome{}, refusalFor(ctx, EnqueueFailed, enqueueErr)
			}
		} else if st, exitErr = confirmEnqueue(ctx, opts, c, clock, deadline, post); exitErr != nil {
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

	// deadline was established before the enqueue, so the wait and the
	// confirmation share one budget rather than each starting its own.
	//
	// post is the shared post-deadline allowance the confirmation above may have
	// already spent part of; the poll that finds the wait spent and the recheck
	// after the loop draw on what is left of it.
	var out Outcome
	var read Status
	var merged, expired bool
	// answeredAfterDeadline says the status in `st` was read on the post-deadline
	// allowance, so it is this instant's queue. The timeout verdict states a
	// position and a state, and a position from before the deadline is the queue
	// of an earlier instant: reporting it would name the past as the present.
	var answeredAfterDeadline bool
	for {
		if err := ctx.Err(); err != nil {
			return Outcome{}, &ExitError{Kind: Interrupted, Detail: err.Error()}
		}
		left := deadline.Sub(clock.Now())
		if left <= 0 {
			break
		}
		// A cancelled poll is not a timeout: the operator stopped the wait, so
		// the run says so instead of reporting where the PR still is.
		if !clock.Sleep(ctx, min(left, pollInterval)) {
			return Outcome{}, &ExitError{Kind: Interrupted, Detail: ctx.Err().Error()}
		}
		// The reads that follow are capped at what is left of the wait, never
		// rounded up to a full interval past it.
		//
		// A sleep that spends the last of the budget leaves this poll nothing to
		// read with, and a zero budget builds an ALREADY-EXPIRED context: the gh
		// call never starts, so the run reaches its verdict having made one fewer
		// real observation than the wait allowed, and a merge that became visible
		// in that gap is reported as still queued. That poll draws on the
		// post-deadline allowance instead, the same one the recheck after the loop
		// spends.
		//
		// The regression test could not see this because its fake counted the call
		// anyway. A read that never starts and a read that answers look identical to
		// a fake that does not look at its context.
		budget := deadline.Sub(clock.Now())
		afterDeadline := budget <= 0
		if budget <= 0 {
			if budget = post.take(clock, deadline); budget <= 0 {
				break
			}
		}
		out, merged, read, exitErr, expired = observe(ctx, opts, c, budget)
		if expired {
			break
		}
		if exitErr != nil {
			return Outcome{}, exitErr
		}
		if merged {
			return out, nil
		}
		answeredAfterDeadline = afterDeadline
		st = read
		if st.State == mergedState {
			return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
		}
		if st.Entry == nil {
			// The run's own context, not the wait's budget: observing the drop is
			// what ended the wait, so there is no budget left for it to spend. What
			// decides the reason read is dropOutcome, which reads the context to tell
			// "the operator stopped asking" from "GitHub said this".
			return dropOutcome(ctx, opts, st, c, clock.Now)
		}
	}
	// The loop's last read lands on the deadline, and the queue can finish in
	// the moment after it. A timeout verdict printed from that read reports a
	// merge that already happened, so observe once more before calling it.
	//
	// The poll above that straddled the deadline and this recheck share ONE
	// allowance, so the pair costs one interval rather than two: a fresh timer
	// per observation is what let a `--wait 1` keep reading for about a minute
	// after the second it asked for.
	if recheck := post.take(clock, deadline); recheck > 0 {
		out, merged, read, exitErr, expired = observe(ctx, opts, c, recheck)
		if expired {
			// A recheck that ran out is not a reason to throw away an answer that
			// already arrived. The poll before this one confirmed the PR is still
			// queued at a known position, read on the post-deadline allowance, so
			// the run has this instant's queue: reporting exit 2 there told the
			// operator nothing was decided while the documented repeat-on-timeout
			// workflow was the right one. Only a run with no post-deadline answer
			// at all has nothing to report.
			if !answeredAfterDeadline {
				return Outcome{}, lookupNotRead()
			}
			return timedOutAt(st), nil
		}
		if exitErr != nil {
			return Outcome{}, exitErr
		}
		if merged {
			return out, nil
		}
		st = read
		answeredAfterDeadline = true
	}
	// An allowance with nothing left is not a fresh one: a read handed it would be
	// a call that never starts. What the run has then is a position from before
	// the deadline, and exit 2 is the code for "the queue could not be read".
	if !answeredAfterDeadline {
		return Outcome{}, lookupNotRead()
	}
	if st.State == mergedState {
		return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
	}
	if st.Entry == nil {
		return dropOutcome(ctx, opts, st, c, clock.Now)
	}
	return timedOutAt(st), nil
}

// timedOutAt is the timeout verdict for a status read after the deadline: the
// queue position and state as of that read, which is what a caller repeats on.
func timedOutAt(st Status) Outcome {
	if st.Entry == nil {
		return Outcome{Kind: TimedOut}
	}
	return Outcome{Kind: TimedOut, Position: st.Entry.Position, QueueState: st.Entry.State}
}

// lookupNotRead is the refusal for a run that reached its deadline without a
// read that completed after it.
//
// The timeout verdict states a queue position and a state, and no read after the
// deadline supplies either: printing the last position seen would report this
// instant's queue from an earlier one. What the run knows is that the queue could
// not be read, which is the lookup failure exit 2 is for.
func lookupNotRead() *ExitError {
	return &ExitError{
		Kind:   LookupFailed,
		Detail: "the final observation of the queue did not complete within the wait's allowance, so whether the pull request is still queued was not read",
	}
}

// startupEnding turns what the starting lookups reported into the run-ending
// refusal, telling the operator's signal apart from the allowance running out.
//
// The two endings are the same context error to a reader that only asks whether
// the context is done, and they are different codes: 130 says the operator
// stopped it and 2 says nothing was decided. refusalFor already answers the
// cancellation, so an Interrupted it reports is kept; an allowance that expired
// under a live parent becomes the lookup failure, because the run reached no
// queue answer and the caller has to know the difference between "I stopped
// this" and "this could not be read".
func startupEnding(ctx context.Context, startExpired bool, exitErr *ExitError) *ExitError {
	if ctx.Err() != nil {
		return &ExitError{Kind: Interrupted, Detail: ctx.Err().Error()}
	}
	if exitErr == nil {
		// A read that reported no failure on an expired allowance never answered:
		// the context ended while it was running and it returned no error.
		if startExpired {
			return startupNotRead()
		}
		return nil
	}
	if exitErr.Kind != Interrupted {
		return exitErr
	}
	return startupNotRead()
}

// startupNotRead is the refusal for a run whose first lookups could not answer
// within their allowance.
func startupNotRead() *ExitError {
	return &ExitError{
		Kind:   LookupFailed,
		Detail: "reading the pull request did not complete within the starting allowance, so its queue state was not read",
	}
}

// observe makes one bounded pass over the pull request: whether it has merged,
// and its queue status when it has not.
//
// budget bounds BOTH reads, so the wait's limit is a limit. The production
// context is cancelled by a signal and carries no deadline of its own, and the
// drain bound does not start while the child is still running under a live
// context, so an unbounded read is a gh call that holds the run for however long
// GitHub takes to answer -- past the --wait the caller asked for, with the
// timeout the caller is waiting for arriving late or not at all.
//
// The run's own context is asked AGAIN after the reads, before any of what they
// returned is interpreted. A cancelled run that received a complete reply has
// still been cancelled: a descendant holding the output pipe open makes the
// reply land up to a drain grace after the signal, and a queue verdict printed
// from that reply tells the operator a wait they stopped ran to its own
// conclusion. The read is allowed to finish; the answer to it is not used.
//
// expired reports that a read ended because its budget ran out rather than
// because the operator cancelled. The two are different endings and the caller
// is what knows which one this point in the wait can act on.
func observe(ctx context.Context, opts Options, c Client, budget time.Duration) (out Outcome, merged bool, st Status, exitErr *ExitError, expired bool) {
	readCtx, stop := context.WithTimeout(ctx, budget)
	defer stop()
	out, merged, exitErr = mergedOutcome(readCtx, opts, c)
	if exitErr == nil && !merged {
		st, exitErr = readStatus(readCtx, opts, c)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, Status{}, &ExitError{Kind: Interrupted, Detail: err.Error()}, false
	}
	// refusalFor answered an interrupt from the read's context. With the run's
	// own context still live, that context is this function's budget, so the
	// ending is the budget's and not the operator's.
	if exitErr != nil && exitErr.Kind == Interrupted {
		return Outcome{}, false, Status{}, nil, true
	}
	return out, merged, st, exitErr, false
}

// readStatus reads the status, turning a failed read into the run-ending
// refusal the script reported.
//
// The context is asked after the read and before the status is interpreted, on
// the success path as well as the failure one. gh writes its reply, exits, and
// leaves a descendant holding the output pipe open until the drain bound, so a
// cancelled read still returns a complete, valid answer -- and printing
// "MERGEQ queued position=1" for a wait the operator stopped tells them the run
// reached its own conclusion. The status is thrown away with the context.
func readStatus(ctx context.Context, opts Options, c Client) (Status, *ExitError) {
	st, err := c.Status(ctx, opts.Repo, opts.PR)
	if err != nil {
		return Status{}, refusalFor(ctx, LookupFailed, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Status{}, &ExitError{Kind: Interrupted, Detail: ctxErr.Error()}
	}
	return st, nil
}

// confirmEnqueue reads the status once after an enqueue, bounded by whatever the
// caller's budget has left AT THE MOMENT THE READ STARTS.
//
// The refresh that follows an enqueue is what decides whether the PR is queued,
// so it is part of the answer and not a diagnostic. On the signal-only context
// it had no limit: a stalled gh call there held the command for however long
// GitHub took, past the --wait the caller asked for, and the enqueue that had
// already landed was never reported. `--wait 0` is the sharpest case, because
// there is no wait at all to end and nothing else to bound it.
//
// The budget is computed here and not by the caller, and that ordering is the
// finding this fixes: a budget calculated before the mutation was handed to a
// read that started after it returned, so a 50-second mutation against `--wait
// 60` left the confirmation with almost a full minute of its own. A limit is
// what is left when the thing it limits begins.
//
// An allowance that expires is not a cancellation: the operator did not stop
// anything, and exit 130 would say they had. The run does not know whether the
// enqueue landed, which is a lookup failure -- exit 2 -- and says so.
func confirmEnqueue(ctx context.Context, opts Options, c Client, clock Clock, deadline time.Time, post *postDeadline) (Status, *ExitError) {
	readCtx, stop := context.WithTimeout(ctx, post.budget(clock, deadline, opts.Wait))
	defer stop()
	st, exitErr := readStatus(readCtx, opts, c)
	if exitErr != nil && exitErr.Kind == Interrupted && ctx.Err() == nil {
		// refusalFor answered the budget, not the operator: the run's own context
		// is still live, so what ended this read is the allowance.
		return Status{}, &ExitError{
			Kind:   LookupFailed,
			Detail: "the queue state after enqueueing did not complete within the wait's allowance, so whether the pull request is queued was not read",
		}
	}
	return st, exitErr
}

// postDeadline is the ONE allowance the reads that cannot draw on the wait are
// bounded by: the confirmation that follows an enqueue, the poll that finds the
// wait spent, and the recheck after the loop.
//
// It is shared rather than granted per read because a fresh interval each time is
// how `--wait 1` spent about a minute reading after the second it asked for. It
// is an instant, not a duration, so a read's budget is what remains of it.
//
// The confirmation draws on it too, and that is the other half of the same rule.
// An enqueue that succeeds just before the deadline leaves the confirmation less
// time than it needs, and reporting the queue unread there wastes the only read
// that could have confirmed an accepted mutation -- while the same read starting
// one moment later would have got a full allowance. A budget boundary is not a
// verdict.
type postDeadline struct {
	// at is when the allowance ends. Zero means not yet established.
	at time.Time
}

// budget is what a read starting now may spend: what is left of the caller's
// wait, or what is left of the post-deadline allowance when that is spent.
//
// A read that would get nothing is reported as zero, which the caller must treat
// as "the queue could not be read" rather than handing it to a context builder:
// a zero-length budget builds an already-expired context, and a read handed one
// never starts.
func (p *postDeadline) budget(clock Clock, deadline time.Time, wait time.Duration) time.Duration {
	if wait <= 0 {
		return p.take(clock, deadline)
	}
	if left := deadline.Sub(clock.Now()); left > 0 {
		return left
	}
	return p.take(clock, deadline)
}

// take is what is left of the post-deadline allowance, establishing it from the
// wait's deadline the first time it is asked for. It returns zero once spent.
func (p *postDeadline) take(clock Clock, deadline time.Time) time.Duration {
	if p.at.IsZero() {
		p.at = deadline.Add(finalObservationAllowance)
	}
	left := p.at.Sub(clock.Now())
	if left <= 0 {
		return 0
	}
	return left
}

// remainingAllowance is how much of the caller's budget is left for a read that
// is about to start.
//
// A run that asked for no wait has no caller budget to lend, so its reads get the
// allowance the other outcome-deciding reads use. A run that did ask for one gets
// what remains of it.
//
// A budget with nothing left falls back to that same allowance rather than
// returning zero. A zero-length budget builds an ALREADY-EXPIRED context, and a
// read handed one never starts: it would report "did not complete within the
// allowance" for a call that was never made. That is the defect the wait loop had
// in its last poll, one place over, so it is not repeated here. The final
// observation sets this precedent explicitly -- it gets an allowance of its own
// because "the wait's budget has nothing left to lend it".
func remainingAllowance(opts Options, clock Clock, deadline time.Time) time.Duration {
	if opts.Wait <= 0 {
		return finalObservationAllowance
	}
	left := deadline.Sub(clock.Now())
	if left <= 0 {
		return finalObservationAllowance
	}
	return left
}

// refusalFor turns a failed client call into the run-ending refusal.
//
// A cancelled call is an interrupt, not a failure: `gh` returns whatever its
// own cancellation looks like, and reporting that as "lookup failed" (exit 2)
// tells the operator GitHub broke when in fact they stopped the wait. The
// context is what distinguishes the two, and it is checked here rather than at
// each call site so every call gets the same answer.
func refusalFor(ctx context.Context, kind ErrorKind, err error) *ExitError {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &ExitError{Kind: Interrupted, Detail: ctxErr.Error()}
	}
	return &ExitError{Kind: kind, Detail: err.Error()}
}

// mergedOutcome reports the merged outcome when the pull request's own state
// says MERGED, and false when it does not (or cannot be read, which the queue
// read that follows surfaces as a real failure).
func mergedOutcome(ctx context.Context, opts Options, c Client) (Outcome, bool, *ExitError) {
	sha, merged, err := c.MergedCommit(ctx, opts.Repo, opts.PR)
	// Asked before the answer is used, for the reason readStatus gives: a
	// cancelled read can still deliver the complete reply it received before the
	// signal, and a merge reported from it ends a wait the operator stopped.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Outcome{}, false, &ExitError{Kind: Interrupted, Detail: ctxErr.Error()}
	}
	if err != nil || !merged {
		// A cancelled probe must not fall through to a queue verdict: the run
		// would report "queued position=N" (exit 0) for a PR whose state it
		// never read.
		if err != nil {
			if exitErr := refusalFor(ctx, LookupFailed, err); exitErr.Kind == Interrupted {
				return Outcome{}, false, exitErr
			}
		}
		return Outcome{}, false, nil
	}
	return Outcome{Kind: Merged, SHA: sha}, true, nil
}

// queuePrefix is the prefix every merge-group run's branch for this PR carries.
func queuePrefix(base, pr string) string {
	return fmt.Sprintf("gh-readonly-queue/%s/pr-%s-", base, pr)
}
