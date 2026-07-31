package agent

import (
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// EnvelopeSampling is every Session read the daemon's AUTHORITATIVE event
// consumer performs, and the only ones it may perform.
//
// The daemon materializes its thread envelope by sampling live session state at
// the moment an event says it moved (server/thread_envelope.go's facetsByEvent,
// implemented by cmd/serf's liveThreadEnvelopeSource). That sampling runs ON THE
// DRAIN GOROUTINE, inside the same call that is consuming the session's event
// stream.
//
// THE RULE, and it is a deadlock rule rather than a style one:
//
//	A method on this interface may take Session.mu. It may take no other lock
//	that lives in package agent.
//
// Since the feed became lossless, an emitter whose buffer is full WAITS
// (session_events.go's sendEvent). Four locks are held across live emits today
// on purpose — responseSideEffectsMu around a tool call's side-effect bundle,
// queueEventsMu around a queue mutation and its announcement, queuePersistMu
// around the snapshot-then-write, and subagent.mu around a delegate handoff. If
// a sample taken here blocks on one of them, the consumer stops draining, the
// buffer fills, and the emitter blocks holding that same lock. Neither side can
// move, and because the blocked send holds eventsMu.RLock the session can no
// longer even be CLOSED (Close needs eventsMu.Lock).
//
// Session.mu is the single exception, and it is safe by construction rather than
// by convention: the emit path re-acquires it (emit -> activeCausalProvenance),
// so agent code cannot hold it across an emit without self-deadlocking on the
// first execution. session_emit_lock_guard_test.go pins that. No other lock in
// this package has anything equivalent, which is why the rule is "mu and
// nothing else" rather than a list of the four.
//
// WHY THIS IS A NAMED INTERFACE and not just *Session. The rule constrains a
// call graph inside agent, but the person who breaks it is adding an envelope
// facet in server/ and cmd/serf/, where none of the above is visible. Typing the
// daemon's window as this interface means a new facet cannot sample anything new
// without adding a method HERE — in the file that states the rule, next to
// session_envelope_sampling_test.go, which proves it for every method the
// interface declares. A type assertion back to *Session still escapes it; what
// that costs is that escaping becomes a written decision instead of an omission.
//
// The cb1k audit established the other half of the picture: the locks the
// consumer reaches OUTSIDE agent (jobstore's, the transcript writer's, the task
// and goal stores') cannot be held across an emit at all, because those packages
// invoke no caller-supplied callback under them. So constraining this surface
// closes the direction that was left open.
type EnvelopeSampling interface {
	ContextPressure() float64
	ContextMetrics() ContextMetrics
	DetailedStatus() DetailedStatus
	ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation)
	TasksWithError() ([]task.Task, error)
	GoalStatus() (status string, iterations int, ok bool)
	WorkMillisSnapshot() int64
	CumulativeUsageSnapshot() llm.Usage
	ActiveTurnStartedAtMillis() int64
	FailedToolCallsSnapshot() (count int, measured bool)
	HasPendingAsk() bool
	PendingEscalations() []events.SandboxEscalationRequestedData
	ReasoningEffort() string
	Profile() *provider.Profile
	Meta() schema.SessionMeta
}

var _ EnvelopeSampling = (*Session)(nil)
