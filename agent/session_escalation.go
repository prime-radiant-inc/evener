package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/sandbox"
)

// M7 — in-UI sandbox-exemption escalation.
//
// This is a NEW human-gated approval primitive, the inverse of ask_user on every
// axis. ask_user is model-initiated, ends the turn, is visible in the transcript,
// and is answered by the user's next input. An escalation is HARNESS-initiated (a
// reaction to a typed sandbox denial, no tool call), BLOCKS mid-tool, is INVISIBLE
// to the model (never a schema.Turn), and is answered by a NEW resolve request that
// unblocks the specific waiting tool-exec goroutine.
//
// The four walls, each defended here:
//   - Not triggerable: only escalateOnSandboxDenial raises one, off a typed denial.
//     No tool exposes it to the model.
//   - Not approvable by the model: the only resolver is ResolveSandboxEscalation,
//     driven by the UI's out-of-band request; it is never advertised as a tool.
//   - Not observable: escalateOnSandboxDenial never appends to s.history — only the
//     final tool result (approved re-run OR typed denial) enters the model context.
//   - Not replayable: the pending map is never persisted, so a crashed/resumed
//     session has no pending escalation; the interrupted call reads as an IsError
//     orphan-repair placeholder, exactly like an interrupted ask_user.

// ctxEscalationGrantKey carries a single granted absolute path on the context of
// one approved re-dispatch. It is a per-invocation grant: it lives only on that
// one call's context, never on the session and never on the resolved policy, so it
// cannot leak to any later call.
type ctxEscalationGrantKey struct{}

// withInvocationGrant returns ctx carrying a single granted path for one tool
// re-dispatch. The execenv layer consults it for exactly that call.
func withInvocationGrant(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, ctxEscalationGrantKey{}, path)
}

// invocationGrant returns the path granted on ctx for the current re-dispatch, if
// any. A grant is present only inside an approved re-run.
func invocationGrant(ctx context.Context) (string, bool) {
	p, ok := ctx.Value(ctxEscalationGrantKey{}).(string)
	return p, ok && p != ""
}

// SetSubscriberCountFunc injects the "is a human actually watching this thread"
// probe. The daemon wires it to appserver.SubscriberCount(threadID); a session with
// no server attached leaves it nil, which reads as zero (never blocks a card no one
// can answer).
func (s *Session) SetSubscriberCountFunc(f func() int) {
	s.mu.Lock()
	s.subscriberCountFn = f
	s.mu.Unlock()
}

// subscriberCount reports the live AppWire subscriber count, or 0 when no probe is
// wired.
func (s *Session) subscriberCount() int {
	s.mu.Lock()
	f := s.subscriberCountFn
	s.mu.Unlock()
	if f == nil {
		return 0
	}
	return f()
}

// escalationAllowed reports whether a denial is eligible for human escalation. It
// mirrors ask_user's root-only interactive gate (NonInteractive || subagent), adds
// the reconciliation zero-subscriber rule, and refuses to escalate a SENSITIVE
// denial: a masked credential/denylist path can never be granted (that would relax
// the immutable secrets floor), and its path cannot even be shown by basename, so a
// human could not meaningfully approve it. Such denials stay final.
func (s *Session) escalationAllowed(denied *sandbox.DeniedError) bool {
	if s.cfg.NonInteractive || s.isSubagentSession() {
		return false
	}
	if denied.Sensitive {
		return false
	}
	if s.subscriberCount() == 0 {
		return false
	}
	return true
}

// escalateOnSandboxDenial is the primitive's chokepoint. Given a tool result that
// MAY carry a typed sandbox denial and a rerun closure that re-dispatches the same
// invocation, it decides:
//
//   - not a sandbox denial, or not escalatable → return res unchanged (final);
//   - escalatable → register a pending escalation, emit the human-facing approval
//     card, and BLOCK the tool-exec goroutine until a human resolves it or the turn
//     is interrupted / the session closes.
//
// On approve it calls rerun with the granted path threaded on the context (the
// grant is per-invocation and consumed by the execenv layer). On deny / interrupt /
// close it returns the original typed denial, exactly as a non-interactive session
// already does. It NEVER touches s.history.
func (s *Session) escalateOnSandboxDenial(ctx context.Context, res tool.ExecResult, rerun func(context.Context) tool.ExecResult) tool.ExecResult {
	denied, ok := sandbox.AsDenied(res.Err)
	if !ok || !s.escalationAllowed(denied) {
		return res
	}

	id := newEscalationID()
	ch := make(chan sandbox.EscalationDecision, 1)

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return res // a session already tearing down denies rather than blocks
	}
	if s.pendingEscalations == nil {
		s.pendingEscalations = map[string]chan sandbox.EscalationDecision{}
	}
	s.pendingEscalations[id] = ch
	s.mu.Unlock()

	// Remove the waiter on every exit path (resolve, interrupt, close). Idempotent
	// with ResolveSandboxEscalation's delete and cancelAllEscalations's clear.
	defer func() {
		s.mu.Lock()
		delete(s.pendingEscalations, id)
		s.mu.Unlock()
	}()

	// Emit the card BEFORE blocking so RecordAppEvent → Broadcast pushes it to the
	// human, then the goroutine waits. The payload carries only the redacted denial
	// — never file contents, never a sensitive path.
	req := sandbox.NewEscalationRequest(id, denied)
	s.emit(events.EventSandboxEscalationRequested, escalationRequestedData(req))

	select {
	case d := <-ch:
		if d.Approve {
			return rerun(withInvocationGrant(ctx, denied.Path))
		}
		return res
	case <-ctx.Done():
		return res
	}
}

// ResolveSandboxEscalation delivers a human decision to the blocked tool-exec
// goroutine for id. It is the ONLY resolver — the daemon's UI resolve handler calls
// it, never the model. Resolving an unknown or already-resolved id is a clean error
// (no panic, no block), so a double-click or a stale card cannot double-approve.
func (s *Session) ResolveSandboxEscalation(id string, approve bool) error {
	s.mu.Lock()
	ch, ok := s.pendingEscalations[id]
	if ok {
		delete(s.pendingEscalations, id)
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("sandbox escalation %q is not pending (unknown or already resolved)", id)
	}
	ch <- sandbox.EscalationDecision{Approve: approve} // buffered(1); exactly one send
	return nil
}

// cancelAllEscalations denies every pending escalation. Called from Close so a
// blocked tool-exec goroutine unblocks (returning the typed denial) rather than
// leaking. Turn-interrupt cancellation is handled by the ctx.Done() arm of the
// select; this covers teardown, where the ctx may outlive the decision to stop.
func (s *Session) cancelAllEscalations() {
	s.mu.Lock()
	pending := s.pendingEscalations
	s.pendingEscalations = nil
	s.mu.Unlock()
	for _, ch := range pending {
		ch <- sandbox.EscalationDecision{Approve: false}
	}
}

// escalationRequestedData maps the wire-agnostic request to the event payload the
// projector reads.
func escalationRequestedData(req sandbox.EscalationRequest) events.SandboxEscalationRequestedData {
	return events.SandboxEscalationRequestedData{
		EscalationID: req.ID,
		Mode:         req.Mode.String(),
		Tool:         req.Tool,
		Kind:         string(req.Kind),
		DeniedPath:   req.DeniedPath,
		Command:      req.Command,
		OutputSoFar:  req.OutputSoFar,
		PartiallyRan: req.PartiallyRan,
	}
}

// escalationSeq guarantees id uniqueness by construction: a process-monotonic
// counter, so two ids can never collide even in the (Linux-impossible) event that
// crypto/rand degenerates. The random suffix keeps the id unguessable.
var escalationSeq atomic.Uint64

// newEscalationID mints a unique, unguessable opaque handle for one escalation.
func newEscalationID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("esc_%d_%s", escalationSeq.Add(1), hex.EncodeToString(b[:]))
}
