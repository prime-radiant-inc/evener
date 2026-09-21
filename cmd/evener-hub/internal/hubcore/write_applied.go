package hubcore

import "errors"

// ErrWriteApplied marks a failure that happened after a write already
// landed: the file or config carries the new state, so only a step after it
// failed. A caller that announces applied writes to other clients must
// announce one of these too, or every other client stays on the pre-write
// state. KeybindingsPostRenameError answers the check for the keybindings
// store, and hub.writeApplied (cmd/evener-hub/app_write_applied.go) wraps a
// failure in this sentinel without changing the failure's own message or wire
// class, which hub.writeDidApply reads back with errors.Is. The hub's instance
// controller derives that wrap from the applied-write record the providers.toml
// write primitive sets and folds onto the mutation's error while its lock is
// held (appliedWrites, same file), and its auth controller derives it per call
// from credentialWrite's success, so a new error path cannot forget the marker.
// The remaining direct writes (the agents-doc save, launch's SetLayer/TrustRepo)
// wrap at their single post-write site.
var ErrWriteApplied = errors.New("the write landed before this failed")
