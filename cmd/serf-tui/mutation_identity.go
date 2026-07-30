package main

import "primeradiant.com/serf/identifier"

// newClientMutationID mints the identity every retry-safe turn mutation must
// carry (appwire.ValidateMutationParams gates turn/start, steer, interrupt,
// queue, drainAsSteer, promoteQueuedAsSteer and cancelQueued on it).
//
// The daemon keys its mutation journal on this value: resending the same id
// replays the recorded outcome instead of applying the mutation twice. So the
// id must be minted once per user ACTION, not once per transport attempt —
// callers generate it when building the tea.Cmd, outside the returned closure,
// so a command that ran twice would still be one mutation.
//
// Unlike the web client the TUI needs no outbox behind this. That machinery
// exists because a browser tab can reload mid-mutation and must recover its
// identity from storage; a TUI talking to a local daemon dies with its
// mutations, so a fresh id per action is the whole requirement.
//
// identifier.NewSessionID is the hub's own choice for this
// (cmd/serf-hub/web_session.go, app_threadlifecycle.go): an unprefixed UUIDv7
// payload. Matching it keeps one convention rather than two, though the name
// reads oddly for a mutation and a dedicated identifier domain would read
// better across all three call sites.
func newClientMutationID() (string, error) {
	return identifier.NewSessionID()
}
