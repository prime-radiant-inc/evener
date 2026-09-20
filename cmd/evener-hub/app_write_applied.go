package hub

import (
	"errors"
	"sync"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// The error still goes back to the caller that asked, independent of the
// broadcast: the caller sees what failed, and everyone else learns the write
// landed (hubcore.ErrWriteApplied's own doc has the rule).

// appliedWrites records whether a write that other clients need to hear about
// landed during the current mutation of ONE controller. It is the hub's own
// form of the plugin store's post-commit hook (#1733): the write primitive
// marks it the instant its own write succeeds (markApplied), so no caller has
// to remember to put a marker on the error it eventually returns - forgetting
// is how #1543 happened.
//
// The mark is captured per call, never read across calls: the mutation clears
// it when it takes the controller's lock (resetApplied) and folds it onto the
// error it is about to return while that lock is still held (captureApplied),
// so the applied answer rides the error of the call that made the write and no
// concurrent mutation can reset or steal it. A rollback that puts the prior
// state back clears it again, because a write that was undone leaves nothing
// to announce. The mutex guards the flag against the direct/peeking readers
// that run without the controller's own lock.
type appliedWrites struct {
	mu    sync.Mutex
	wrote bool
}

// resetApplied clears the record: the next takeApplied answers false unless a
// write primitive marks it again.
func (a *appliedWrites) resetApplied() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.wrote = false
}

// markApplied records that a write landed. Called by the write primitives
// themselves, immediately after their own write succeeds.
func (a *appliedWrites) markApplied() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.wrote = true
}

// peekApplied reports whether a write landed without clearing the record, for
// a controller that has to make one decision of its own (a rename whose config
// write stood) before the RPC layer takes it.
func (a *appliedWrites) peekApplied() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wrote
}

// takeApplied reports whether a write landed and clears the record, so the
// mutation that produced it is announced exactly once.
func (a *appliedWrites) takeApplied() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	wrote := a.wrote
	a.wrote = false
	return wrote
}

// appliedError marks err as reporting a failure that followed a write which
// stands, without changing err's own message or wire class: Error() returns
// it verbatim, and Unwrap exposes both err (so appserver's router still
// resolves a wrapped WireError through errors.AsType) and
// hubcore.ErrWriteApplied (so writeDidApply's errors.Is finds it) -
// KeybindingsPostRenameError's shape.
type appliedError struct{ err error }

func (e *appliedError) Error() string   { return e.err.Error() }
func (e *appliedError) Unwrap() []error { return []error{e.err, hubcore.ErrWriteApplied} }

// writeApplied marks err as reporting a failure that followed a write which
// stands.
func writeApplied(err error) error {
	if err == nil {
		return nil
	}
	return &appliedError{err: err}
}

// writeDidApply answers the handlers' one question: is there a change the
// other clients need to hear about? A nil error is the ordinary applied
// write; hubcore.ErrWriteApplied marks one that landed before a later step
// failed.
func writeDidApply(err error) bool {
	return err == nil || errors.Is(err, hubcore.ErrWriteApplied)
}
