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
	//
	// The entry GitHub put the pull request in is returned when the reply names
	// one, and nil when it does not. It is an observation the run cannot get any
	// other way: the confirmation read may find the entry already gone, because a
	// concurrent push rebuilt the merge group between the mutation and the read,
	// and without this the run reports "no queue entry after enqueueing" for an
	// enqueue GitHub itself confirmed performing. With it, a gone entry is what it
	// is -- a drop the queue decided -- and is reported with its diagnostics
	// rather than as a failed mutation.
	Enqueue(ctx context.Context, repo, prID string, jump bool) (*QueueEntry, error)
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
	st, exitErr := readStatus(startCtx, ctx, opts, c)
	if exitErr == nil && st.State != mergedState {
		var out Outcome
		var merged bool
		out, merged, probeErr := mergedOutcome(startCtx, ctx, opts, c)
		if probeErr == nil && merged {
			stopStart()
			return out, nil
		}
		// The status above ANSWERED, and the probe is supplemental to it: `gh pr
		// view` only upgrades a queue read into a merge. Letting a probe that spent
		// the allowance replace a decoded status discards the position the run was
		// given -- `--wait 0` then exits 2 instead of naming where the PR is
		// queued, for a read that succeeded.
		exitErr = startupProbeEnding(ctx, probeErr)
	}
	stopStart()
	if exitErr := startupEnding(ctx, exitErr); exitErr != nil {
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
	// known is what this run has learned about the queue, recorded as it is
	// learned. Everything the exit path decides reads it, rather than the last
	// thing that happened: an observed answer is not less true because a later
	// read failed.
	var known queueKnowledge
	if st.Entry == nil {
		// The mutation is bounded too. On the signal-only context it had no limit at
		// all, so a stalled call blocked the confirmation from starting and held the
		// command for as long as GitHub took. It spends the same caller budget as
		// everything else here: a mutation that eats the whole --wait leaves the
		// confirmation with nothing, which it reports rather than spends twice.
		enqueueCtx, stopEnqueue := context.WithTimeout(ctx, remainingAllowance(opts, clock, deadline))
		// The entry GitHub put the PR in, when the mutation's own reply named one.
		// It is the only record of the enqueue if a concurrent push rebuilds the
		// merge group before the confirmation runs, and the confirmation then finds
		// no entry without the mutation having failed.
		enqueueEntry, enqueueErr := c.Enqueue(enqueueCtx, opts.Repo, st.ID, opts.Jump)
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
			// Which of the three endings this is, asked once: an answer, an
			// unknown, or a failure the call stated.
			if !outcomeUnknown(enqueueErr, enqueueExpired) {
				return Outcome{}, refusalFor(ctx, EnqueueFailed, enqueueErr)
			}
			var confirmed bool
			if st, confirmed, exitErr = confirmEnqueue(ctx, opts, c, clock, deadline, post); exitErr != nil {
				// The mutation's own reply named a queue entry, so GitHub confirmed
				// the enqueue even though the refresh could not be read. Reporting a
				// lookup failure here discards that confirmation and invites the
				// operator to repeat a write that already landed.
				if enqueueEntry != nil && exitErr.Kind != Interrupted {
					return confirmedButUnrefreshed(opts.Wait, *enqueueEntry)
				}
				return Outcome{}, exitErr
			}
			known.observe(st, confirmed)
			if st.State == mergedState {
				return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
			}
			if st.Entry == nil {
				if enqueueEntry == nil {
					// No entry then and none now: the mutation did not land, and the
					// honest report is that the enqueue could not be confirmed.
					return Outcome{}, refusalFor(ctx, EnqueueFailed, enqueueErr)
				}
				// The mutation's reply named an entry and the read finds none, so the
				// queue took the entry and removed it: that is a drop, not a failed
				// mutation, and the drop diagnostics are what the operator needs.
				return dropOutcome(ctx, opts, st, c, clock.Now)
			}
		} else {
			var confirmed bool
			if st, confirmed, exitErr = confirmEnqueue(ctx, opts, c, clock, deadline, post); exitErr != nil {
				// Same rule on the branch where the mutation reported no error: when
				// its reply named an entry, the enqueue is confirmed and only the
				// refresh failed. That is "queued, current state unverified", not
				// "nothing was read".
				if enqueueEntry != nil && exitErr.Kind != Interrupted {
					return confirmedButUnrefreshed(opts.Wait, *enqueueEntry)
				}
				return Outcome{}, exitErr
			}
			known.observe(st, confirmed)
		}
		if st.State == mergedState {
			return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
		}
		if st.Entry == nil {
			if enqueueEntry == nil {
				return Outcome{}, &ExitError{Kind: EnqueueLeftNoEntry, PRState: st.State}
			}
			// The enqueue landed and the entry is gone before this run could read it:
			// a drop, with the diagnostics the queue's own reason supplies.
			return dropOutcome(ctx, opts, st, c, clock.Now)
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
		// When the answer arrived, not when the poll started: the deadline applies
		// to the observation. A poll that straddled it read this instant's queue,
		// and calling that a read of the wait leaves the run with no observation to
		// build its timeout verdict from.
		known.observe(read, deadline.Sub(clock.Now()) <= 0)
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
			return known.ending()
		}
		if exitErr != nil {
			// The operator's stop outranks the confirmed position. The position is
			// real, but the run did not reach a verdict: it was ended. Reporting
			// `MERGEQ timeout` at exit 1 there tells a caller to repeat a wait they
			// just cancelled, and the two codes mean different things to it --
			// "the queue ended this" versus "I stopped it". It also disagrees with
			// every other interrupt path, which returns 130.
			if exitErr.Kind == Interrupted {
				return Outcome{}, exitErr
			}
			// Same rule as the loop's poll for a failure the invocation survived: a
			// transient failure here costs the run a newer observation, and the
			// confirmed position is still this instant's queue as far as the run can
			// tell. A connection reset must not stop the documented
			// repeat-on-timeout workflow for a run that already read the position.
			//
			// An allowance that ran out is the other surviving ending, and it does not
			// reach here: observe() reports it as expired, handled above.
			if known.answered {
				return known.ending()
			}
			return Outcome{}, exitErr
		}
		if merged {
			return out, nil
		}
		known.observe(read, true)
		st = read
	}
	// An allowance with nothing left is not a fresh one: a read handed it would be
	// a call that never starts. What the run has then is a position from before
	// the deadline, and exit 2 is the code for "the queue could not be read".
	timedOut, notRead := known.ending()
	if notRead != nil {
		return Outcome{}, notRead
	}
	if st.State == mergedState {
		return Outcome{Kind: Merged, SHA: st.MergeSHA()}, nil
	}
	if st.Entry == nil {
		return dropOutcome(ctx, opts, st, c, clock.Now)
	}
	return timedOut, nil
}

// queueKnowledge is what one run knows about the queue, and the only thing its
// exit path reads.
//
// A run can end three ways and only two of them are decided here. A refusal is
// GitHub's own no, and it ends the run where it is reported. Queued: a read
// confirmed a position, and that survives any later read failing, because the
// queue's answer at that instant was given. Unknown: no read completed, so
// whether the pull request is in the queue was never learned, and the run must
// say so rather than name the past as the present.
//
// It is one value rather than a flag per read because the run learns this fact in
// four places -- the confirmation after an enqueue, the wait's polls, the poll
// that straddles the deadline, and the recheck after the loop -- and each of them
// used to carry its own boolean. Two booleans meaning one fact can disagree, and
// the exit path then chose between exit 1 and exit 2 by consulting whichever it
// happened to reach first: a position an earlier read had confirmed was thrown
// away because the read after it failed.
type queueKnowledge struct {
	// answered says a read completed after the wait ran out. Only such a read is
	// this instant's queue: one from before the deadline is the queue of an
	// earlier instant, and printing its position would report the past as now.
	answered bool
	// st is the latest answer, which is what the timeout verdict names.
	st Status
}

// observe records one completed read. afterDeadline is when the ANSWER arrived,
// not when the call started: a confirmation that started before the deadline and
// answered after it is a post-deadline observation, and reading the clock before
// the call recorded the intent of the read instead of its result -- so an
// enqueue accepted first and confirmed second was reported as never confirmed.
func (k *queueKnowledge) observe(st Status, afterDeadline bool) {
	k.st = st
	if afterDeadline {
		k.answered = true
	}
}

// ending is what a run whose wait is over reports: the timeout verdict, at the
// position the last answer named, when a read completed after the deadline, and
// the lookup failure when none did.
//
// This is the one predicate the exit path asks. Every read that can end the wait
// -- the confirmation, the poll that straddles the deadline, the recheck after
// the loop -- ends by asking it, so a run that has answered the question once
// cannot be made to answer it again.
func (k *queueKnowledge) ending() (Outcome, *ExitError) {
	if !k.answered {
		return Outcome{}, lookupNotRead()
	}
	return timedOutAt(k.st), nil
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
// cancellation, so the run's own context is asked first; an allowance that
// expired under a live invocation becomes the lookup failure, because the run
// reached no queue answer and the caller has to know the difference between "I
// stopped this" and "this could not be read".
//
// The allowance is not asked about separately, and that is the fix: it used to
// turn a nil error into the lookup failure whenever it had expired, which threw
// away an answer that arrived just before it did. A nil error IS an answer -- a
// decoded status, or a merge the probe read -- and a usable answer outranks an
// expired allowance. Only an operator cancellation outranks the answer, and that
// is the invocation check above.
func startupEnding(ctx context.Context, exitErr *ExitError) *ExitError {
	if ctx.Err() != nil {
		return &ExitError{Kind: Interrupted, Detail: ctx.Err().Error()}
	}
	if exitErr == nil {
		return nil
	}
	if exitErr.Kind != Interrupted {
		return exitErr
	}
	// refusalFor answered the starting allowance, not the operator: the run's own
	// context is live, so what ended this read was the allowance.
	return startupNotRead()
}

// startupProbeEnding is what the supplemental merge probe's ending does to the
// status it supplements.
//
// The probe upgrades a queue read into a merge; it does not replace one. So a
// probe that spent the starting allowance is dropped, because the run already has
// an answer and this read only failed to add to it. It arrives here as
// Interrupted: refusalFor answered the probe's own read context, and an inner
// read reports its own spent budget that way.
//
// The operator's cancellation is NOT dropped, and that is why the invocation is
// asked rather than only the kind: a stopped run reports 130 whatever it read
// first. A nil error passes through, and so does an ending that carries a real
// defect -- a malformed reply is worth reporting even beside a good status,
// because it is the probe saying it could not do its job.
func startupProbeEnding(ctx context.Context, probeErr *ExitError) *ExitError {
	if probeErr == nil {
		return nil
	}
	if probeErr.Kind == Interrupted && ctx.Err() == nil {
		return nil
	}
	return probeErr
}

// confirmedButUnrefreshed reports the position the mutation's own reply named when
// the read after it failed.
//
// The mutation is the observation that matters: GitHub answered with the queue
// entry it created, which is a confirmed write. What failed is the refresh, and a
// refresh that could not be read does not unconfirm the write. Reporting the
// lookup failure instead discards a position the run holds and tells the operator
// nothing was decided about an enqueue that landed -- the sharpest case being a
// retried `--jump`, which the queue then refuses because the PR is already there.
//
// The verdict depends on what was asked for, and neither branch invents a state
// the mutation did not report:
//
//   - `--wait 0` asked where the PR went, and the mutation's reply answered:
//     Queued at that position, exit 0.
//   - A waiting run asked to be told when the queue finished, and this run cannot
//     read the queue any further. Reporting Queued there would claim a wait that
//     never ran, so it reports the timeout verdict at the confirmed position --
//     exit 1, which the documented workflow repeats. That is the honest report:
//     the PR is queued at N and nothing newer could be read.
//
// A cancellation never reaches here: the caller checks Interrupted first, because
// an operator who stopped the wait did not ask for a verdict.
func confirmedButUnrefreshed(wait time.Duration, entry QueueEntry) (Outcome, *ExitError) {
	if wait <= 0 {
		return Outcome{Kind: Queued, Position: entry.Position}, nil
	}
	return timedOutAt(Status{Entry: &entry}), nil
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
// expired reports that a read ended because its budget ran out and produced
// nothing usable, rather than because the operator cancelled. The two are
// different endings and the caller is what knows which one this point in the
// wait can act on: a spent poll budget is not the end of the run, because the
// final observation has an allowance of its own.
//
// A usable answer outranks the spent allowance. The allowance bounds how long
// the run waits for a read; it does not unread a reply that arrived before it
// ran out. Only an operator cancellation outranks the answer, and that is asked
// of the INVOCATION context above.
func observe(ctx context.Context, opts Options, c Client, budget time.Duration) (out Outcome, merged bool, st Status, exitErr *ExitError, expired bool) {
	readCtx, stop := context.WithTimeout(ctx, budget)
	defer stop()
	out, merged, exitErr = mergedOutcome(readCtx, ctx, opts, c)
	if exitErr == nil && !merged {
		st, exitErr = readStatus(readCtx, ctx, opts, c)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, Status{}, &ExitError{Kind: Interrupted, Detail: err.Error()}, false
	}
	if exitErr == nil {
		return out, merged, st, nil, false
	}
	// The invocation is live, so nothing here is the operator's doing. What is
	// left is the read's own allowance, and a read that exhausted it without
	// producing an answer is a budget expiry: the run is not over, because the
	// final observation draws on an allowance of its own. Reporting it as the
	// run-ending lookup failure skipped that observation and exited 2 while the
	// allowance was still there to spend.
	//
	// refusalFor already answered the invocation's cancellation above, so an
	// Interrupted reaching this point is the budget's too.
	if errors.Is(readCtx.Err(), context.DeadlineExceeded) || exitErr.Kind == Interrupted {
		return Outcome{}, false, Status{}, nil, true
	}
	return out, merged, st, exitErr, false
}

// readStatus reads the status, turning a failed read into the run-ending
// refusal the script reported.
//
// The INVOCATION context is asked after the read and before the status is
// interpreted, on the success path as well as the failure one. gh writes its
// reply, exits, and leaves a descendant holding the output pipe open until the
// drain bound, so a cancelled read still returns a complete, valid answer -- and
// printing "MERGEQ queued position=1" for a wait the operator stopped tells them
// the run reached its own conclusion. The status is thrown away with that
// context.
//
// What is NOT thrown away is an answer that arrived while only this READ's own
// allowance expired. The allowance bounds how long the run waits for a read; it
// does not unread a reply that arrived before it ran out. Discarding one there
// cost the run the only observation it had: a complete confirmation showing the
// PR queued, with a descendant holding the pipe past the allowance and the
// operator still waiting, was turned into Interrupted and reported as a lookup
// failure. The two contexts are separate arguments for exactly that reason --
// `readCtx` bounds the wait, `ctx` is the invocation, and only the invocation
// says the operator stopped something.
func readStatus(readCtx, ctx context.Context, opts Options, c Client) (Status, *ExitError) {
	st, err := c.Status(readCtx, opts.Repo, opts.PR)
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
//
// It also reports whether the wait had already run out when the ANSWER arrived,
// which is what makes it a post-deadline observation the caller can use as its
// timeout verdict. The clock is read after the call returns, not before it
// starts: the deadline applies to the observation, not to the request that
// produced it. Reading it first calls a read that started before the deadline
// and answered after it a read of the wait, and its answer -- a real position,
// read after the deadline -- was then not allowed to carry the timeout verdict.
// The enqueue was accepted and confirmed, and the run reported that nothing had
// been read.
func confirmEnqueue(ctx context.Context, opts Options, c Client, clock Clock, deadline time.Time, post *postDeadline) (Status, bool, *ExitError) {
	readCtx, stop := context.WithTimeout(ctx, post.budget(clock, deadline, opts.Wait))
	defer stop()
	st, exitErr := readStatus(readCtx, ctx, opts, c)
	if exitErr != nil && exitErr.Kind == Interrupted && ctx.Err() == nil {
		// refusalFor answered the budget, not the operator: the run's own context
		// is still live, so what ended this read is the allowance.
		return Status{}, false, &ExitError{
			Kind:   LookupFailed,
			Detail: "the queue state after enqueueing did not complete within the wait's allowance, so whether the pull request is queued was not read",
		}
	}
	return st, deadline.Sub(clock.Now()) <= 0, exitErr
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
	if p.at.IsZero() {
		if wait > 0 {
			// A waiting run's cutoff hangs off the wait's deadline, so the
			// confirmation is never handed only the fraction of the wait the
			// mutation happened to leave. An enqueue accepted just before expiry
			// is exactly the one whose confirmation has no caller budget left,
			// and the same read starting a moment later would have had the whole
			// allowance.
			p.at = deadline.Add(finalObservationAllowance)
		} else {
			// A run with no wait has no wait deadline to hang it off. The deadline
			// it is handed is the instant BEFORE the mutation ran, so anchoring the
			// cutoff there leaves a mutation that spent the allowance an
			// already-expired confirmation -- and an expired mutation is precisely
			// the one that has to be reconciled. Count it from when the read starts.
			p.at = clock.Now().Add(finalObservationAllowance)
		}
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

// outcomeUnknown reports whether a failed enqueue call ended without
// establishing whether GitHub performed the mutation, which is the only ending
// that is read back.
//
// Three endings, and the state read is right for exactly one of them. A refusal
// is GitHub's answer: reconciling it spends a read looking for this invocation's
// work and finds whatever is there, including an entry another operator made for
// the same pull request, which turns a refused --jump into reported success. A
// failure the call stated is the same kind of answer and takes the same path. A
// call that was cut off, or whose own allowance expired, may have been accepted
// before it ended, so only that one is uncertain.
//
// The discriminated error types are asked first because the two can be true at
// once: gh can return the refusal just before the budget expires, with the output
// drain crossing it afterwards. The context then says "expired" while the
// mutation's answer was a definite no.
//
// A body-less failure -- a reset connection, a killed process, a wrapper's own
// diagnostic -- is an unknown outcome, reported as ErrOutcomeUnknown by the
// client. It used to be indistinguishable from a failure the call stated, and the
// run reported GitHub having accepted an enqueue as a refusal.
func outcomeUnknown(err error, expired bool) bool {
	if _, ok := errors.AsType[*RefusalError](err); ok {
		return false
	}
	return errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrIncompleteReply) || expired
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
//
// Two contexts, for the reason readStatus gives. `readCtx` bounds how long the
// run waits for the answer and `ctx` is the invocation, and only the invocation
// says the operator stopped something. A MERGED reply that arrived while the
// read's own allowance was expiring is an answer -- the entire point of the read
// -- and discarding it here reported a timeout after the run had been told the
// pull request merged. A reply that never arrived is still the read failing.
func mergedOutcome(readCtx, ctx context.Context, opts Options, c Client) (Outcome, bool, *ExitError) {
	sha, merged, err := c.MergedCommit(readCtx, opts.Repo, opts.PR)
	// Asked before the answer is used: a cancelled read can still deliver the
	// complete reply it received before the signal, and a merge reported from it
	// ends a wait the operator stopped.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Outcome{}, false, &ExitError{Kind: Interrupted, Detail: ctxErr.Error()}
	}
	if err == nil && merged {
		// The answer wins, whether or not the allowance expired while the pipe
		// drained. Only the invocation outranks it.
		return Outcome{Kind: Merged, SHA: sha}, true, nil
	}
	if err != nil {
		// A read that produced no answer and spent its allowance is the budget
		// expiring, not a state the run learned: report it as the read's ending so
		// the caller can spend the allowance it keeps for exactly this, rather than
		// as a verdict about a pull request nobody read.
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return Outcome{}, false, &ExitError{Kind: Interrupted, Detail: readCtx.Err().Error()}
		}
		// A cancelled probe must not fall through to a queue verdict: the run
		// would report "queued position=N" (exit 0) for a PR whose state it
		// never read.
		if exitErr := refusalFor(ctx, LookupFailed, err); exitErr.Kind == Interrupted {
			return Outcome{}, false, exitErr
		}
	}
	return Outcome{}, false, nil
}

// queuePrefix is the prefix every merge-group run's branch for this PR carries.
func queuePrefix(base, pr string) string {
	return fmt.Sprintf("gh-readonly-queue/%s/pr-%s-", base, pr)
}
