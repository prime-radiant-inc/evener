package server

import (
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
)

// Bridge reads events from a session event channel, records appwire
// notifications, and updates server status.
// It blocks until the events channel is closed.
func Bridge(srv *Server, eventCh <-chan events.SessionEvent) {
	BridgeWithObserver(srv, eventCh, nil)
}

// BridgeWithObserver behaves like Bridge and also invokes observer for every
// event before broadcasting it. This is used to tee raw NDJSON event logs.
//
// The channel form is for callers that already hold one. The daemon does NOT
// use it: it registers through agent.Session.ConsumeEventsLossless so its feed
// cannot drop, and applies each event with BridgeEvent.
func BridgeWithObserver(srv *Server, eventCh <-chan events.SessionEvent, observer func(events.SessionEvent)) {
	for ev := range eventCh {
		BridgeEvent(srv, ev, observer)
	}
}

// BridgeEvent applies one session event to the server: the observer tee, then
// the status write, the envelope refresh, and the projection commit.
//
// Everything it does is bounded, in-memory work plus at most one jobs.jsonl
// read per turn, and that is load-bearing rather than incidental. This runs on
// the daemon's authoritative consumer, whose feed now blocks its emitter rather
// than dropping, so anything slow in here becomes a stall on the session loop.
// The observer is called through a non-blocking tee for exactly this reason
// (see cmd/serf's verboseEventTee); do not add an unbounded wait here.
//
// THE LOCK RULE, which is wider than "do not block" and is the part that will
// bite someone: because the feed blocks its emitter, a session goroutine that
// emits WHILE HOLDING ANY LOCK THIS FUNCTION ACQUIRES deadlocks the pair once
// the 256-deep buffer fills. Before losslessness that was a dropped event and
// nobody noticed.
//
// So: EMIT WITH NO LOCK HELD. agent/session_prompts.go is the worked example —
// renderSystemPrompt returns its diagnostic instead of emitting it, precisely
// because three of its callers hold s.mu.
//
// DO NOT READ THE LIST BELOW AS COMPLETE. The reachable set spans at least four
// packages and two module boundaries, and an earlier version of this comment
// named six locks and got one of them wrong; the useful statement is the rule,
// not the inventory. What is verified:
//
//   - Taken on EVERY accepted event, not per facet: Server.mu (below, via
//     acceptsSessionEvent and applySessionEventStatus) and appserver's
//     projectionMu, deliveryMu and mu, plus Subscriptions.mu, Notifier.mu,
//     Connection.sendMu and appTurnSnapshot.mu inside the commit. That is the
//     bulk of the exposure, and it does not shrink by adding fewer facets.
//   - Outside this package AND outside agent/: cmd/serf/serve.go's currentMu,
//     which every facet sample passes through because the envelope source
//     resolves the live session per call. Anyone auditing from here alone will
//     not see it.
//   - Inside agent/ and its subsystems: Session.mu, jobManager.mu,
//     jobstore.Store.mu, jobstore.OutputStore.mu, the task store's, the goal
//     store's, transcript.Writer.mu, contextmgr.Manager.mu, tool.Registry.mu,
//     clientMutationStore.stateMu, the MCP connection's, and the sync.Once
//     guards around the lazily built stores.
//
// One correction worth keeping, because it is the kind of detail that makes an
// inventory worse than useless: the jobs.jsonl read behind DetailedStatus runs
// under jobstore.Store.mu, NOT jobManager.mu — listWithError calls store.Load()
// BEFORE taking jm.mu (agent/jobs.go). An earlier version of this comment named
// the lock that does not do the I/O.
//
// `go vet` cannot see any of this and no fixture reaches it: the default
// suite's scripted provider never fills the buffer. What DOES catch a violation
// is the emit path itself, and that was audited rather than assumed (kata
// cb1k). Intersect the two sets — the locks this consumer takes inside agent/,
// and the locks agent code can hold across an emit — and exactly two survive:
// Session.mu and jobManager.mu. The emit path re-acquires BOTH (emit →
// activeCausalProvenance → Session.mu; emitWithProvenance →
// jobManager.onSessionEvent → jobManager.mu), so on non-reentrant mutexes a
// violation self-deadlocks on its first execution rather than wedging in
// production. Every other lock reachable from here either lives in a package
// that cannot call back into agent (jobstore, transcript, task, goal,
// contextmgr, tool, mcp) or is held only over straight-line code that emits
// nothing. agent/session_emit_lock_guard_test.go pins those two guards, which
// are incidental to what emit and onSessionEvent are actually for and would
// otherwise be deleted by a plausible refactor without a single test failing.
//
// So the recommendation that used to close this comment — wrap Session.mu in an
// owner-tracked mutex and assert at sendEvent's blocking branch — is RETRACTED.
// It buys nothing: emit-under-Session.mu is already an immediate self-deadlock
// by construction, and the assert would sit in a branch no test reaches.
//
// What is NOT closed is the other direction, and it is where the next incident
// comes from. Four locks are held across live emits TODAY —
// Session.queueEventsMu (agent/session_client_mutation_queue.go's
// reflectDurableInputQueue, agent/session_queue.go's popSteeringHead),
// queuePersistMu (persistQueuesSnapshot), responseSideEffectsMu
// (agent/session_tools.go's TOOL_CALL_END and its output-delta chunk loop, the
// highest-volume emit in the system) and subagent.mu (agent/job_delegate.go).
// They are safe only because THIS consumer does not take them, which is a
// property of cmd/serf's liveThreadEnvelopeSource, not of anything in agent.
// Adding an envelope facet that samples session state under any of those four
// wedges the daemon on the next large tool output, and nothing anywhere would
// say so first.
func BridgeEvent(srv *Server, ev events.SessionEvent, observer func(events.SessionEvent)) {
	if observer != nil {
		observer(ev)
	}
	// A cheap early-out for an event the daemon does not serve. It is not
	// the guard: applySessionEventStatus and RecordAppEvent each re-test
	// acceptance under the lock they mutate, because this one runs unlocked
	// and an identity can be replaced between it and them.
	if !srv.acceptsSessionEvent(ev.SessionID) {
		return
	}
	srv.applySessionEventStatus(ev)
	// Sample the envelope facets this event can have moved, before the
	// commit that publishes the event announcing them. Same ordering, and
	// for the same reason, as applySessionEventStatus above.
	srv.refreshThreadEnvelopeForEvent(ev)
	srv.RecordAppEvent(ev)
}

// applySessionEventStatus writes the status an event announces, before the
// projection commit that publishes the announcement.
//
// Two orderings ride on this one critical section.
//
// Status before notification. The two reach a client by different routes: the
// notification arrives on the subscription, the status it describes is read
// back from thread/read and /status. Projecting first leaves a window where a
// client that reduces thread/started and immediately re-reads sees the
// PREVIOUS session's model, profile and state — and across /clear that window
// spans a whole identity, because ReplaceAppIdentity has already moved
// status.SessionID to the replacement while the rest still describes what it
// replaced.
//
// Acceptance with application. The bridge tests acceptance unlocked, so an
// event admitted a moment before an identity replacement would otherwise write
// the REPLACED session's metadata on top of the replacement's — a straggling
// SESSION_START from the session /clear just retired renaming the thread that
// retired it, with nothing to correct it afterwards. Re-testing here, in the
// same hold as the writes, drops it instead.
//
// It takes only s.mu and notifies nothing: the single notification egress stays
// RecordAppEvent's projection commit, which acquires projectionMu first.
func (s *Server) applySessionEventStatus(ev events.SessionEvent) {
	effect := sessionEventStatusEffect(ev)
	if effect == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.acceptsSessionEventLocked(ev.SessionID) {
		return
	}
	effect(s)
}

// sessionEventStatusEffect returns the status write an event announces, or nil
// when it announces none.
//
// Choosing the effect before the lock keeps every event that has none -- which
// is nearly all of them, every token delta included -- off s.mu entirely. The
// daemon drains through its authoritative consumer, whose feed BLOCKS its
// emitter rather than dropping (see BridgeEvent), so anything that slows the
// drain is backpressure on the session loop, and a drain that stops is a
// session that can no longer even be closed.
//
// This is deliberately one switch rather than a kind test in front of the
// existing one: a second list of the status-bearing kinds would drift from this
// one silently, and the drift would look like an event that simply stopped
// updating status.
func sessionEventStatusEffect(ev events.SessionEvent) func(*Server) {
	switch ev.Kind {
	case events.EventSessionStart:
		d, ok := ev.Data.(events.SessionStartData)
		if !ok {
			return nil
		}
		return func(s *Server) {
			s.updateSessionInfoLocked(ev.SessionID, d.Model, d.Profile)
			s.status.State = d.State
			if d.State == "" {
				s.status.State = string(agent.SessionIdle)
			}
		}
	case events.EventAssistantTextEnd:
		return func(s *Server) { s.status.Turns++ }
	case events.EventSessionEnd:
		d, ok := ev.Data.(events.SessionEndData)
		if ok && d.Interrupted {
			// An interrupted end closes nothing: the session stays live and the
			// cancelled turn is still unwinding. The event is still projected by
			// the caller — only its status effects are skipped.
			return nil
		}
		return func(s *Server) {
			s.setProcessingLocked(false)
			s.status.State = string(agent.SessionClosed)
			if ok && d.State != "" {
				s.status.State = d.State
			}
		}
	default:
		return nil
	}
}
