package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Round-7 R1, pinned as reds: the fence's specified placement — "comparing
// immediately before clearAskPending" — is two lock acquisitions with a
// preemptible gap. clearAskPending takes s.mu itself (agent/session_tools_ask.go),
// so any comparison that authorizes it runs in a PREVIOUS acquisition, and the
// model goroutine, live mid-turn, can append an ask between the comparison and
// the clear (the grounded append window: start is refused while a turn is
// active, so the answer arrives through queue or steer while the model can
// still ask again). Both reds below perform the spec'd comparison, land the
// append in the gap, run the spec'd clear, and assert the promised protection
// (spec 4: a mismatch is refused, the appended ask is "never admitted as an
// answer" and must survive). Both fail today because the clear is
// unconditional: nothing between the comparison and clearAskPending
// re-validates the set.
//
// The primitives used are the real ones, in the real call order: the fence's
// comparison reads the same state PendingAskCalls() serves, the append is
// appendAskPendingLocked (the live ask_user Exec's own mutation), and the
// clears are the two sites' own clear primitives.

// fenceComparisonSample stands in for the comparison the spec'd fence performs
// immediately before clearAskPending: it reads the pending set in its own lock
// acquisition and passes when the pair still matches what the answering side
// read.
func fenceComparisonSample(s *Session) *appwirePendingAskFenceSample {
	state := s.PendingAskCalls()
	return &appwirePendingAskFenceSample{revision: state.Revision, calls: len(state.Calls)}
}

type appwirePendingAskFenceSample struct {
	revision uint64
	calls    int
}

// armLiveAskExecution marks the session as executing turn-live, the state a
// real turn holds while its rounds can append asks (appendAskPendingLocked
// stamps the executing turn's id onto each appended question).
func armLiveAskExecution(s *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execution.turnID = "turn-live"
}

// appendAsk posts one more question to the pending set — the model goroutine's
// own mid-turn mutation.
func appendAsk(s *Session, question string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendAskPendingLocked([]askQuestion{{Question: question, Options: []string{"yes", "no"}}}, []byte(`{"questions":[{}]}`), "call-"+question)
}

// TestAskFenceSteerClearSiteComparisonGap is the steer site's red
// (clearAskPendingForResolvingSteer, agent/session_queue.go deliverSteering):
// the answering check steeringAnswersAsk evaluated the steer against the
// pre-append set, a fence comparison placed immediately before the clear has
// already passed on the same stale pair, and the clear then wipes an ask that
// arrived after both.
func TestAskFenceSteerClearSiteComparisonGap(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	armLiveAskExecution(s)
	appendAsk(s, "Q1")

	// The spec'd comparison: one lock acquisition, pair still matches.
	fence := fenceComparisonSample(s)
	if fence.revision != 1 || fence.calls != 1 {
		t.Fatalf("fence sample = %+v, want revision 1 with 1 call", fence)
	}

	// The append lands between the comparison and the clear.
	appendAsk(s, "Q2")
	if current := s.PendingAskCalls(); current.Revision == fence.revision {
		t.Fatalf("append did not race into the comparison-to-clear gap: revision still %d", current.Revision)
	}

	// The steer site's clear: a user-sourced steer that answers the ask.
	steer := schema.NewTurn(schema.TurnSteering, llm.Message{Role: llm.RoleUser,
		Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "delayed human answer to Q1"}}})
	steer.SteeringSource = events.SteeringSourceUser
	s.clearAskPendingForResolvingSteer(steer)

	// Promised: the stale answer is refused — the ask appended after the
	// comparison survives. RED today: the clear wiped the whole set.
	if count := s.askPendingCount(); count != 2 {
		t.Fatalf("stale answer cleared the pending set through the comparison-to-clear gap: %d questions survive, want 2 (the refused answer may clear nothing)", count)
	}
}

// TestAskFenceQueueClearSiteComparisonGap is the queue site's red
// (processOneInput, agent/session_lifecycle.go): the admission samples the
// pending turns the durable answer will name (turn.AnswerToTurnIDs =
// pendingAskTurnIDs()), the answer turn is appended durably, and the clear
// runs after — two lock acquisitions around a durable append. The red also
// pins the name-capture divergence the gap produces: the durable answer names
// only the asks that existed at the sample, yet the clear removes the appended
// ask too.
func TestAskFenceQueueClearSiteComparisonGap(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	armLiveAskExecution(s)
	appendAsk(s, "Q1")

	// The queue site's admission sample: one lock acquisition.
	names := s.pendingAskTurnIDs()
	if len(names) != 1 || names[0] != "turn-live" {
		t.Fatalf("admission sample = %v, want exactly the live turn's ask", names)
	}
	fence := fenceComparisonSample(s)
	if fence.revision != 1 || fence.calls != 1 {
		t.Fatalf("fence sample = %+v, want revision 1 with 1 call", fence)
	}

	// The append lands between the sample and the clear — inside the very
	// window the durable answer occupies (the append at
	// session_lifecycle.go:2332 precedes the clear at :2344).
	appendAsk(s, "Q2")

	// The clear the fence comparison authorizes.
	s.clearAskPending()

	// Promised: the appended ask survives the stale answer. RED today.
	if count := s.askPendingCount(); count != 2 {
		t.Fatalf("stale answer cleared the pending set through the sample-to-clear gap: %d questions survive, want 2 (the refused answer may clear nothing)", count)
	}
	// And the durable evidence of the divergence: the answer names a strict
	// subset of what its clear removed. (Both questions are gone while the
	// sampled names held only the first.)
	if survivors := s.pendingAskTurnIDs(); len(survivors) != 0 {
		t.Fatalf("pending turns remain: %v", survivors)
	}
}
