package hub

import (
	"errors"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A hub write that applied is announced to every client, whatever the step
// after it did. The clients hold no record of what they sent: the broadcast
// is the only thing that tells them a list they are showing is stale, so a
// write that reached the config and then hit a failing rollback or a failing
// re-read must still be announced - otherwise the list every other client
// shows stays wrong until a reconnect or an unrelated notification.
//
// The error still goes back to the caller that asked: the two are independent.
// The caller sees what failed, and everyone else learns the write landed.

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
