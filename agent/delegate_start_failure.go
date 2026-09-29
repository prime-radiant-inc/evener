package agent

import "errors"

// isTransientStartFailure reports whether a delegate start or restore failed
// for a reason that clears on its own: the target was busy (a race with a
// reservation, a stop or a reclamation), or retirement had closed admission.
// Those are retried as they stand. Any other failure is permanent for the
// purposes of giving up: the attention drive counts it toward handing the
// attention to the root, and the owed-start path fails the start.
func isTransientStartFailure(err error) bool {
	return errors.Is(err, errDelegateTargetBusy) || errors.Is(err, ErrRetirementUnavailable)
}
