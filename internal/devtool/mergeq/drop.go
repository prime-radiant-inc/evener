package mergeq

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DropClass is what a queue drop was classified as. The queue reports why it
// removed an entry, and that reason — not the wording of any check message —
// is what decides the class, so a green entry dropped by a batch rebuild is
// not read as a failed pull request.
type DropClass int8

const (
	// DropChecksFailed: a required check failed in the merge group.
	DropChecksFailed DropClass = iota
	// DropChecksTimedOut: the queue gave up waiting for checks.
	DropChecksTimedOut
	// DropReasonUnavailable: the queue reported no reason.
	DropReasonUnavailable
	// DropReasonOther: the queue reported a reason this tool has no advice
	// for; the reason itself is carried through.
	DropReasonOther
	// DropEventStale: the latest removal event predates this drop, so it
	// explains a prior one.
	DropEventStale
	// DropReasonUnreadable: the removal event could not be read at all.
	DropReasonUnreadable
)

// String names the class for a diagnostic.
func (c DropClass) String() string {
	switch c {
	case DropChecksFailed:
		return "failed_checks"
	case DropChecksTimedOut:
		return "checks_timed_out"
	case DropReasonUnavailable:
		return "reason_unavailable"
	case DropReasonOther:
		return "reason_other"
	case DropEventStale:
		return "event_stale"
	case DropReasonUnreadable:
		return "reason_unreadable"
	}
	return "unknown"
}

// RunRef names this pull request's merge-group run: the run itself when one
// resolves, otherwise the queue branch prefix the operator can list, otherwise
// the run generically (the status read supplied no base branch).
type RunRef struct {
	// Run is the resolved run, nil when none resolved.
	Group *GroupRun
	// Prefix is the queue branch prefix, empty when no base was read.
	Prefix string
}

// String renders the reference inside a drop reason. Every drop branch names
// this pull request's merge-group run, so the operator can go straight to it
// instead of hunting through the Actions API.
func (r RunRef) String() string {
	if r.Group != nil {
		conclusion := r.Group.Conclusion
		if conclusion == "" {
			conclusion = "unknown"
		}
		return fmt.Sprintf("merge-group run %d (%s): %s", r.Group.DatabaseID, conclusion, r.Group.URL)
	}
	if r.Prefix != "" {
		return "the merge-group run for queue branch prefix " + r.Prefix
	}
	return genericRun
}

// dropOutcome decides the outcome line for a queue drop: the class of its
// removal reason, the reason as the operator should read it, and the
// merge-group run it names. Every branch names the run, including the one where
// the removal event could not be read at all: the operator's next step is the
// same either way.
func dropOutcome(ctx context.Context, opts Options, st Status, c Client, now func() time.Time) (Outcome, *ExitError) {
	// Both diagnostic reads share ONE finite allowance. The drop has already been
	// observed -- the status read is what saw the queue entry gone -- so the two
	// reads that only explain it must not be able to hold the command past its own
	// limit: on the operator's signal-only context they could stall indefinitely,
	// after `--wait` and after the final observation's allowance, and the run would
	// never report the drop it had already decided. Whatever the reads do not
	// produce in that allowance is answered by the fallbacks below, which is the
	// same answer an unreadable reason already gets.
	readCtx, stopReads := context.WithTimeout(ctx, dropDiagnosticAllowance)
	defer stopReads()
	ref := mergeGroupRef(readCtx, opts, st.Base, c)
	event, err := c.QueueDrop(readCtx, opts.Repo, opts.PR)
	// The context is checked whether or not the read returned an error, and it is
	// checked before the error is interpreted. A ctx whose deadline expired or
	// whose cancellation arrived while a descendant held the output pipe can still
	// hand back a complete, valid answer -- the drain bound lets the call return
	// the bytes it received. Deciding a drop from that answer prints
	// `MERGEQ dropped` with re-enqueue advice for a PR the operator cancelled, and
	// exits 1, while the launcher reports 130 for the same invocation. The
	// distinction between "the operator stopped asking" and "GitHub said this" is
	// the whole point of this branch, so it is asked first and on both paths.
	//
	// The run's OWN context is asked, not the reads': the allowance above is what
	// bounds the diagnostics, and a spent allowance is an unread reason, not an
	// interrupt. Asking the read context here would report an operator cancellation
	// for a run nobody cancelled.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Outcome{}, &ExitError{Kind: Interrupted, Detail: ctxErr.Error()}
	}
	// The read is not an interrupt, so whatever it produced is a drop and the run
	// it names is the operator's next step either way. The three cases are decided
	// into values here and leave through the single return below.
	//
	// Every case that reaches this point discards the read's error on purpose: a
	// reason that could not be read is a drop whose class says so, not a failure
	// of the command, and returning the error instead would turn a queue outcome
	// the operator needs into a lookup failure that exits 2. Writing it as one
	// return rather than a nil error returned from inside `if err != nil` is what
	// makes that a stated design instead of a suppression: `nilerr` has no
	// nil-returning branch under a live error to object to, so nothing here needs
	// a //nolint and none is used.
	class := DropReasonUnavailable
	reason := "the queue removal reason was unavailable; see " + ref.String() + " before re-enqueueing"
	switch {
	case err != nil:
		class = DropReasonUnreadable
		reason = "could not read the queue removal reason; see " + ref.String() + " before re-enqueueing"
	case event != nil:
		class, reason = classifyDropReason(event.Reason, ref)
		if now().Sub(event.CreatedAt) > stalenessWindow {
			class = DropEventStale
			reason = "the latest queue removal event predates this drop; see " + ref.String() + " before re-enqueueing"
		}
	}
	return Outcome{
		Kind:      Dropped,
		PRState:   st.State,
		DropClass: class,
		Reason:    reason,
		Run:       ref,
	}, nil
}

// classifyDropReason classifies the queue's own removal reason, and returns
// the advice that reason calls for. Anything the queue reports is read whole:
// a reason this tool does not recognize is passed through rather than being
// treated as its most familiar sibling. Every reason names the run, so the
// operator's next step is the same whichever branch they are in.
func classifyDropReason(reason string, ref RunRef) (DropClass, string) {
	switch reason {
	case "failed_checks":
		return DropChecksFailed, "a required check failed in the merge group; see " + ref.String() + " and re-enqueue when the failed lanes are unrelated to your change or look transient"
	case "checks_timed_out":
		return DropChecksTimedOut, "the queue timed out waiting for checks; confirm " + ref.String() + " completed green, then re-enqueue the same head"
	case "":
		return DropReasonUnavailable, "the queue removal reason was unavailable; see " + ref.String() + " before re-enqueueing"
	}
	return DropReasonOther, "queue removal reason: " + reason + " — see " + ref.String() + " before re-enqueueing"
}

// mergeGroupRef names how to reach this pull request's merge-group run.
//
// A merge-group run's headBranch is gh-readonly-queue/<base>/pr-<N>-<base tip>:
// GitHub puts the base tip the queue tested on top of, not the pull request
// head, so the exact branch cannot be built before the run exists and the
// suffix is matched on its prefix. It never fails the run: a lookup that
// errors or finds nothing falls back to the prefix, and a base the status read
// did not supply falls back to the generic run.
func mergeGroupRef(ctx context.Context, opts Options, base string, c Client) RunRef {
	if base == "" || base == absent || base == "null" {
		return RunRef{}
	}
	prefix := queuePrefix(base, opts.PR)
	runs, err := c.MergeGroupRuns(ctx, opts.Repo)
	if err != nil {
		return RunRef{Prefix: prefix}
	}
	for i := range runs {
		if strings.HasPrefix(runs[i].HeadBranch, prefix) {
			return RunRef{Group: &runs[i]}
		}
	}
	return RunRef{Prefix: prefix}
}
