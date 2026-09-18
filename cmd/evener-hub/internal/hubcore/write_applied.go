package hubcore

import "errors"

// ErrWriteApplied marks a failure that happened after a write already
// landed: the file or config carries the new state, so only a step after it
// failed. A caller that announces applied writes to other clients must
// announce one of these too, or every other client stays on the pre-write
// state. hub.writeApplied (cmd/evener-hub/app_write_applied.go) wraps a
// failure in this sentinel for the hub's own auth/instance/launch writes
// without changing the failure's own message or wire class, and
// hub.writeDidApply reads it back with errors.Is; KeybindingsPostRenameError
// answers the same check for the keybindings store.
var ErrWriteApplied = errors.New("the write landed before this failed")
