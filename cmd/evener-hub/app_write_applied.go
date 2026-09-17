package hub

import (
	"errors"
	"fmt"

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
// The error still goes back to the caller that asked: the two are independent.
// The caller sees what failed, and everyone else learns the write landed.

// writeApplied marks err as reporting a failure that followed a write which
// stands. Wrapping hubcore.ErrWriteApplied keeps the message and the wire
// class of what it wraps: appserver's router resolves a WireError through
// Unwrap, and writeDidApply below reads the same sentinel back out - the same
// one the keybindings and transcript-display stores wrap their own
// applied-then-failed errors in, so one answer serves the hub's own writes
// and theirs alike.
func writeApplied(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", hubcore.ErrWriteApplied, err)
}

// writeDidApply answers the handlers' one question: is there a change the
// other clients need to hear about? A nil error is the ordinary applied
// write; hubcore.ErrWriteApplied marks a hub write or a hubcore store's
// (keybindings, transcript-display) that landed before a later step failed.
func writeDidApply(err error) bool {
	return err == nil || errors.Is(err, hubcore.ErrWriteApplied)
}
