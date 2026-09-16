package hub

import (
	"errors"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A hub write that applied is announced to every client, whatever the step
// after it did. The clients hold no record of what they sent: since D11
// (#1502) and #1532 the broadcast is the only thing that tells them a list
// they are showing is stale, so a write that reached the config or the plugin
// store and then hit a failing rollback or a failing listing must still be
// announced — otherwise the list every other client shows stays wrong until a
// reconnect or an unrelated notification.
//
// The error still goes back to the caller that asked: the two are independent,
// which is what appliedWriteError carries. The caller sees what failed, and
// everyone else learns the write landed.
type appliedWriteError struct{ err error }

func (e appliedWriteError) Error() string { return e.err.Error() }

func (e appliedWriteError) Unwrap() error { return e.err }

// writeApplied marks err as reporting a failure that followed a write which
// stands. Wrapping keeps the message and the wire class of what it wraps:
// appserver's router resolves a WireError through Unwrap.
func writeApplied(err error) error {
	if err == nil {
		return nil
	}
	return appliedWriteError{err}
}

// storeWriteError marks a store failure that landed its write, so the one
// predicate below answers for the hub's own writes and for the state stores
// (hubcore.ErrWriteApplied) and the plugin store (plugins.ErrStoreChanged)
// alike. The wire class of what it wraps is preserved.
func storeWriteError(err error) error {
	if errors.Is(err, hubcore.ErrWriteApplied) {
		return writeApplied(err)
	}
	return err
}

// writeDidApply answers the handlers' one question: is there a change the
// other clients need to hear about? A nil error is the ordinary applied write;
// a marked error is one that applied and then failed.
func writeDidApply(err error) bool {
	if err == nil {
		return true
	}
	_, applied := errors.AsType[appliedWriteError](err)
	return applied
}
