package hubcore

import "errors"

// ErrWriteApplied marks a store failure that happened after the write
// landed: the file carries the new state and the store adopted it, so only
// the step after it failed. A caller that announces applied writes to other
// clients (the hub broadcasts the canonical state) must announce one of
// these too, or every other client stays on the pre-write revision. Wrap it
// around a failure that happened after a durable write already landed
// (fmt.Errorf("%w: %w", ErrWriteApplied, err)), so one errors.Is answers the
// question for every caller that wraps it.
var ErrWriteApplied = errors.New("the write landed before this failed")
