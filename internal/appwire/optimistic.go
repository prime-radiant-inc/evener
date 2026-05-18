package appwire

// PendingCoordinator is the callback hook the client uses to inform a
// renderer that an outgoing optimistic request was issued. The
// coordinator is responsible for drawing the pending visual, scheduling
// the event-arrival timeout, and reconciling authoritative events
// through tryReconcile in the renderer's own notification path.
//
// Set via Client.SetPendingCoordinator. When the coordinator is nil,
// the Turn* methods pass through unchanged.
type PendingCoordinator interface {
	// Register is called immediately before the JSON-RPC request is
	// issued. The returned PendingHandle gives the client a way to
	// signal RPC-level failure (network error, hub Unavailable).
	// The coordinator owns the timeout, the reconciliation, and the
	// authoritative confirmation lifecycle.
	Register(method, text string) PendingHandle
}

// PendingHandle is the per-call lifecycle handle returned by
// PendingCoordinator.Register.
type PendingHandle interface {
	// Fail marks the pending entry as failed if it has not already
	// been reconciled. Idempotent.
	Fail(reason string)
}
