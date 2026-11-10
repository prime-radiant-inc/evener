package server

import (
	"maps"

	"primeradiant.com/evener/appwire"
)

// stampAppNotificationTarget re-addresses params to the fanout target the
// server is committing them under. The projector already stamps its own view
// of threadId/ref; the server overwrites both with the authoritative target
// (ref remapping, descendant fanout). Params are either structs implementing
// appwire.NotificationTargeted or map[string]any carrying threadId/ref keys;
// anything else passes through untouched.
func stampAppNotificationTarget(params any, threadID, ref string) any {
	switch p := params.(type) {
	case appwire.NotificationTargeted:
		return p.WithNotificationTarget(threadID, ref)
	case map[string]any:
		// Copy rather than mutate: the caller's map must stay untouched so a
		// future multi-target fanout of one params value cannot make every
		// notification share the last target stamped.
		out := make(map[string]any, len(p))
		maps.Copy(out, p)
		out["threadId"] = threadID
		out["ref"] = ref
		return out
	default:
		return params
	}
}

// stampFailureCountOnStatusChange rides the session's running failure count
// along on every thread/status/changed (kata 12rq).
//
// The count is otherwise snapshot-only, refreshed by thread/read — so a client
// that attached while the session was clean would keep showing nothing however
// many failures followed, which is exactly the watcher the figure exists for.
// A status transition is a turn boundary, the only moment the count can have
// moved, so this refreshes it precisely when there is something to say and
// never polls.
//
// It happens HERE, at the server's single notification egress, rather than in
// the projector: the projector maps events to notifications and holds no
// session handle, while the pull callback is the server's.
//
// An unmeasured count is left off entirely. Absence on a notification means
// "no update" — a zero here would let a client that never measured anything
// start claiming a clean run.
func (s *Server) stampFailureCountOnStatusChange(method string, params any) any {
	if method != appwire.NotifyThreadStatusChanged {
		return params
	}
	status, ok := params.(appwire.ThreadStatusChangedParams)
	if !ok {
		return params
	}
	count, measured := s.envelopeFailedToolCalls()
	if !measured {
		return params
	}
	status.FailedToolCalls = &count
	s.mu.Lock()
	pushed := count
	s.appPushedFailedToolCalls = &pushed
	s.mu.Unlock()
	return status
}

// failureCountMovedLocked reports that a running execution's failure count
// moved past what the root's statuses last carried. The count otherwise
// reaches clients only at a status transition, so a long execution with
// failing tools would show none of them until it ended (kata 895d): while an
// execution is published, the move is pushed as its own active status, which
// carries the count. Callers hold s.mu.
func (s *Server) failureCountMovedLocked() bool {
	current := s.appEnvelope.FailedToolCalls
	if !s.processing || s.appActiveTurnID == "" || current == nil {
		return false
	}
	return s.appPushedFailedToolCalls == nil || *s.appPushedFailedToolCalls != *current
}

// stampAskPendingOnStatusChange rides the pending-ask flag along on every
// thread/status/changed (#1613).
//
// The flag is otherwise snapshot-only, refreshed by thread/read, and the
// clients cannot derive it: the reducer is forbidden from recomputing this
// thread field from item lifecycle (appwire-client/typescript/reducer.test.ts,
// "askPending is wire-authoritative"), because the ask dock owns its own,
// separate in-tool signal. So after the user answers, every other client keeps
// saying "question waiting" until it rereads.
//
// It happens HERE, at the server's single notification egress, for the reason
// the two stampers around it give: the projector maps events to notifications
// and holds no session handle, and the flag lives on the envelope.
//
// envelopeAskPending has no unmeasured case, so the stamp always sets the
// field: false is a real clear, not an absence. It has to be — the client's
// absent-means-no-update rule (reducer.ts) treats an omitted field as "keep
// the prior value", so a client showing "question waiting" would never see
// it clear if this only stamped true and left false off.
func (s *Server) stampAskPendingOnStatusChange(method string, params any) any {
	if method != appwire.NotifyThreadStatusChanged {
		return params
	}
	status, ok := params.(appwire.ThreadStatusChangedParams)
	if !ok {
		return params
	}
	pending := s.envelopeAskPending()
	status.AskPending = &pending
	return status
}

// envelopeAskPending reads the pending-ask flag out of the thread envelope the
// snapshot is built from, so a notification and the snapshot cannot disagree.
//
// There is no unmeasured state to skip, which is what makes this simpler than
// the failure count above: the flag is whether the envelope holds a pending
// question, refreshed whenever the ask facet is sampled, and appThread derives
// every snapshot's flag the same way. Wire absence therefore means an old
// daemon, which is exactly what the client's absent-means-no-update rule is
// for. The bridge samples the facet BEFORE the projection commit that emits
// this notification (bridge.go), so the value stamped here is the one this
// event produced.
func (s *Server) envelopeAskPending() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appEnvelope.PendingQuestion != nil
}

// stampCapabilitiesOnStatusChange rides the action set that goes with the
// announced status along on every thread/status/changed (kata 06t8).
//
// The set is otherwise snapshot-only, refreshed by thread/read — and Send is
// the one flag defined by whether a turn is in flight (appCapabilities; Steer,
// Interrupt and Queue are harness support alone and the client applies the
// status). So a client that hydrated while the session was idle, or while it
// was a cold exited session the hub answered from the past index, holds
// send=true (and, from the past index, steer/interrupt/queue=false) for the
// whole turn its own send starts: the composer knows the turn is live (it has
// the status change and turn/started) and still renders a live Send until a
// reload.
// Every capability change is a status transition, so this refreshes them
// exactly when they can have moved and never polls — the same shape
// stampFailureCountOnStatusChange already uses for the failure count.
//
// It happens HERE, at the server's single notification egress, rather than in
// the projector, for the same reason that one does: the projector maps events
// to notifications and holds no session handle.
//
// The set is computed FROM THE ANNOUNCED STATUS, not from the server's ambient
// processing flag. The two are written by different goroutines — the projector
// emits this notification from the event bridge while SetProcessing is the
// session loop's — so reading the ambient flag here would be a race that
// silently publishes the old status's capabilities. Deriving them from the
// status in hand makes the frame self-consistent by construction: a client
// applying both fields of one notification can never hold a status and a
// capability set that disagree.
func (s *Server) stampCapabilitiesOnStatusChange(method string, params any) any {
	if method != appwire.NotifyThreadStatusChanged {
		return params
	}
	status, ok := params.(appwire.ThreadStatusChangedParams)
	if !ok {
		return params
	}
	// A daemon announcing its own close is describing a thread it is about to
	// stop running, and what that thread can still be asked to do is the HUB's
	// to say: it answers an exited session's read from the past index and
	// resumes it on the next send, advertising Send there
	// (cmd/evener-hub/app_threadread.go's pastThreadCapabilities). This daemon's
	// own all-false set would take the follow-up composer away from a session
	// the hub would happily wake — the same wedge in the other direction. So
	// the close frame leaves the field empty and the hub fills it in with its
	// own answer as it relays the frame (app_relay.go's
	// stampClosedThreadCapabilities): a client is never left reading a set the
	// departing daemon cut for a turn that is over (kata pk2d).
	if status.Status.Type == appwire.ThreadStatusClosed {
		return params
	}
	capabilities := s.appCapabilities(status.Status.Type, status.Status.Type == appwire.ThreadStatusActive)
	status.Capabilities = &capabilities
	return status
}

// envelopeFailedToolCalls reads the running failure count out of the
// materialized envelope. The bridge refreshes the failures facet BEFORE the
// commit that projects the event being stamped, so the figure a notification
// carries is at least as new as the event it rides on.
func (s *Server) envelopeFailedToolCalls() (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.appEnvelope.FailedToolCalls == nil {
		return 0, false
	}
	return *s.appEnvelope.FailedToolCalls, true
}
