package mergeq

import (
	"fmt"
	"strconv"
)

// outcomeLine renders an outcome as its one documented line and the exit code
// it stands for. The markers are the script's contract: callers and operators
// read them, so they are built here once rather than in the launcher.
//
// A queued or merged run exits 0; a drop or a timeout exits 1.
func outcomeLine(out Outcome) (string, int) {
	switch out.Kind {
	case Merged:
		return "MERGEQ merged sha=" + out.SHA, 0
	case Queued:
		return "MERGEQ queued position=" + strconv.FormatInt(out.Position, 10), 0
	case TimedOut:
		return "MERGEQ timeout position=" + strconv.FormatInt(out.Position, 10) + " state=" + out.QueueState, 1
	case Dropped:
		return "MERGEQ dropped: PR left the queue without merging (state=" + out.PRState + "); " + out.Reason, 1
	}
	return "MERGEQ unknown outcome", 2
}

// refusalLine renders a run-ending refusal for stderr.
func refusalLine(err *ExitError) string {
	switch err.Kind {
	case EnqueueLeftNoEntry:
		return "MERGEQ enqueue failed: no queue entry after enqueueing (state=" + err.PRState + ")"
	case JumpRefused:
		return "MERGEQ jump refused: PR is already queued at position " +
			strconv.FormatInt(err.Position, 10) + "; remove it from the queue first"
	case Interrupted:
		return "MERGEQ interrupted: the wait was cancelled before the queue finished"
	case LookupFailed, EnqueueFailed:
		return "MERGEQ " + err.Kind.String() + ": " + err.Detail
	}
	return fmt.Sprintf("MERGEQ %s: %s", err.Kind, err.Detail)
}
