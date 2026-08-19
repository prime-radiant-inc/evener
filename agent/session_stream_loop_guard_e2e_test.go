package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The ported consumeModelStream tests all drive one function call in
// isolation. These drive whole turns through ProcessInput instead, which is
// the only way to prove the four things the unit level cannot see: that the
// tool calls streamed before a tier-1 trip actually dispatch, that the pending
// nudge reaches message history as a steering turn positioned after those tool
// results, that the NEXT request carries it, and that a session survives a
// tier-2 hard stop and accepts a later turn normally.

// loopProbeTools are three registered no-op tools whose names form the k=3
// cycle the primary captured incident had. Real tool names (rather than
// communicate/manage_worktree) keep dispatch under the test's control: the
// result tool would end the turn, which is not what a runaway does.
var loopProbeTools = []string{"probe_alpha", "probe_beta", "probe_gamma"}

// registerLoopProbeTools registers loopProbeTools as no-ops so the calls a
// tripping round streams actually dispatch and produce tool results.
func registerLoopProbeTools(sess *Session) {
	for _, name := range loopProbeTools {
		sess.RegisterTool(name, "no-op probe for the stream loop guard", map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}, func(context.Context, any) (any, error) { return "ok", nil })
	}
}

// scriptedRounds answers each successive Stream call with the next script in
// the list, repeating the last one once the list runs out. The counter is the
// per-call sequencer these turn-level tests need: one adapter serves every
// round of every turn, and which round it is decides what the model "says".
type scriptedRounds struct {
	mu     sync.Mutex
	n      int
	rounds []func(*llm.ChanStream)
}

func (r *scriptedRounds) stream(st *llm.ChanStream) {
	r.mu.Lock()
	i := r.n
	r.n++
	r.mu.Unlock()
	if i >= len(r.rounds) {
		i = len(r.rounds) - 1
	}
	r.rounds[i](st)
}

func (r *scriptedRounds) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// streamCycleTrip scripts one runaway response: the loopProbeTools cycle,
// repeated past the 15-call trip point. It sends 20 calls, so a guard that
// failed to stop the stream would dispatch 20 rather than 15 — the count is
// the assertion that the cut happened at the right place, not merely that it
// happened. idPrefix keeps tool-call IDs unique across rounds within one
// session's history.
func streamCycleTrip(idPrefix string) func(*llm.ChanStream) {
	return func(st *llm.ChanStream) {
		for i := range 20 {
			name := loopProbeTools[i%len(loopProbeTools)]
			id := idPrefix + "_" + itoaTest(i)
			args := []byte(`{"probe":"` + name + `"}`)
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: id, Name: name, Type: "function"}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: id, Arguments: args}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: args}})
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
	}
}

// loopGuardSession wires a scripted-round adapter into a transcript-backed
// session with the probe tools registered.
func loopGuardSession(t *testing.T, rounds *scriptedRounds, fallbacks ...string) (*Session, *scriptedStreamAdapter) {
	t.Helper()
	a := &scriptedStreamAdapter{
		provider: "openai",
		script:   map[string]func(*llm.ChanStream){"primary": rounds.stream},
	}
	sess := settlementSession(t, a, fallbacks...)
	registerLoopProbeTools(sess)
	drainSessionEvents(sess)
	return sess, a
}

// turnIndexOfSteeringKind returns the history index of the first steering turn
// with the given kind, or -1.
func turnIndexOfSteeringKind(history []schema.Turn, kind string) int {
	for i, turn := range history {
		if turn.Kind == schema.TurnSteering && turn.SteeringKind == kind {
			return i
		}
	}
	return -1
}

// countToolResults counts tool-result content parts whose call ID carries the
// given round prefix, across every tool-results turn in history. The prefix
// scopes the count to one scripted round, so the recovery round's own
// communicate result never inflates it.
func countToolResults(history []schema.Turn, idPrefix string) int {
	n := 0
	for _, turn := range history {
		if turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && strings.HasPrefix(part.ToolResult.ToolCallID, idPrefix+"_") {
				n++
			}
		}
	}
	return n
}

// TestProcessInput_StreamLoopGuard_TripDispatchesNudgesAndRecovers is the
// end-to-end shape the whole guard exists to produce: round 1 runs away, the
// guard cuts it at a legal boundary, the calls it already streamed still
// dispatch, the nudge lands as a steering turn AFTER those results, the next
// request carries it, and round 2 completes cleanly with the streak back to
// zero.
func TestProcessInput_StreamLoopGuard_TripDispatchesNudgesAndRecovers(t *testing.T) {
	rounds := &scriptedRounds{rounds: []func(*llm.ChanStream){
		streamCycleTrip("r1"),
		streamCommunicate("stopped repeating, here is the answer"),
	}}
	sess, a := loopGuardSession(t, rounds)

	out, err := sess.ProcessInput(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "stopped repeating") {
		t.Fatalf("ProcessInput output = %q, want the round-2 answer", out)
	}
	if got := rounds.count(); got != 2 {
		t.Fatalf("model called %d times, want exactly 2 (the tripped round, then the recovery round)", got)
	}

	history := sessionHistory(sess)

	// The 15 calls that streamed before the cut must have dispatched, not been
	// discarded: a guard that threw the response away would leave zero.
	if got := countToolResults(history, "r1"); got != 15 {
		t.Fatalf("dispatched tool results = %d, want 15 (the calls streamed before the trip, all of them, and none of the 5 the fixture sent afterwards)", got)
	}

	steerIdx := turnIndexOfSteeringKind(history, events.SteeringKindStreamLoop)
	if steerIdx < 0 {
		t.Fatal("no SteeringKindStreamLoop turn in history: the trip's nudge never reached the model")
	}
	// The nudge must sit after the tripped round's OWN results, never between
	// the tool_use turn that carried those calls and their tool_result turn:
	// providers that require the two adjacent would reject the next request.
	tripResultsIdx := -1
	for i, turn := range history {
		if turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && strings.HasPrefix(part.ToolResult.ToolCallID, "r1_") {
				tripResultsIdx = i
			}
		}
	}
	if tripResultsIdx < 0 {
		t.Fatal("no TurnToolResults turn carries the tripped round's results")
	}
	if steerIdx < tripResultsIdx {
		t.Fatalf("stream-loop steering turn at index %d, tripped round's tool results at %d: the nudge must land AFTER the tool results, never between a tool_use turn and its tool_result", steerIdx, tripResultsIdx)
	}

	nudge := history[steerIdx].Message.Text()
	if !containsAll(nudge, loopProbeTools[0], loopProbeTools[1], loopProbeTools[2]) {
		t.Fatalf("nudge = %q, want it to name the repeated calls", nudge)
	}

	// The next request must actually carry the nudge, not merely have it sitting
	// in history behind some filter.
	reqs := a.Requests()
	if len(reqs) < 2 {
		t.Fatalf("adapter saw %d requests, want at least 2", len(reqs))
	}
	if next := modelVisibleText(reqs[1]); !strings.Contains(next, nudge) {
		t.Fatalf("second request does not carry the nudge.\nnudge: %q\nrequest text: %q", nudge, next)
	}

	sess.mu.Lock()
	streak := sess.streamLoopTripStreak
	pending := sess.pendingStreamLoopNudge
	sess.mu.Unlock()
	if streak != 0 {
		t.Fatalf("streamLoopTripStreak = %d after the clean recovery round, want 0", streak)
	}
	if pending != "" {
		t.Fatalf("pendingStreamLoopNudge = %q after delivery, want empty", pending)
	}
}

// TestProcessInput_StreamLoopGuard_HardStopEndsTurnAndSessionSurvives covers
// the tier-2 path at turn level. Two consecutive tripping rounds inside ONE
// turn must surface the hard stop out of ProcessInput itself rather than being
// swallowed. The second turn is the real point: it opens with another tripping
// round, and it must get a tier-1 nudge and recover — which is only possible
// if the streak was zeroed at turn start. Without that reset the streak is
// still 2 from the hard stop, the first trip of turn 2 escalates straight to
// tier 2, and the session is permanently one trip away from failing every
// turn.
func TestProcessInput_StreamLoopGuard_HardStopEndsTurnAndSessionSurvives(t *testing.T) {
	rounds := &scriptedRounds{rounds: []func(*llm.ChanStream){
		streamCycleTrip("t1r1"),
		streamCycleTrip("t1r2"),
		streamCycleTrip("t2r1"),
		streamCommunicate("recovered on the second turn"),
	}}
	sess, _ := loopGuardSession(t, rounds)

	_, err := sess.ProcessInput(context.Background(), "go", nil)
	if err == nil {
		t.Fatal("ProcessInput returned nil after two consecutive tripping rounds, want the hard-stop error")
	}
	var loopStop *streamLoopHardStopError
	if !errors.As(err, &loopStop) {
		t.Fatalf("ProcessInput error = %v, want it to wrap *streamLoopHardStopError", err)
	}
	var le llm.Error
	if !errors.As(err, &le) {
		t.Fatalf("ProcessInput error %v does not satisfy llm.Error", err)
	}
	if le.Retryable() {
		t.Fatal("hard-stop error is Retryable() == true, want false")
	}

	// The session must still be usable. Turn 2 trips once more, which is a
	// FIRST trip again, so it earns a nudge and recovers.
	out, err := sess.ProcessInput(context.Background(), "try again", nil)
	if err != nil {
		t.Fatalf("second ProcessInput after a hard stop: %v (a hard stop must end its turn, not the session)", err)
	}
	if !strings.Contains(out, "recovered on the second turn") {
		t.Fatalf("second turn output = %q, want the recovery answer", out)
	}
	if got := rounds.count(); got != 4 {
		t.Fatalf("model called %d times, want exactly 4 (two tripping rounds, then a trip and a recovery)", got)
	}

	sess.mu.Lock()
	streak := sess.streamLoopTripStreak
	sess.mu.Unlock()
	if streak != 0 {
		t.Fatalf("streamLoopTripStreak = %d after the recovery round, want 0", streak)
	}
}

// TestProcessInput_StreamLoopGuard_TurnStartResetsStreakAndNudge observes the
// reset directly rather than through its consequences. The adapter's script
// runs after processOneInput's turn-start block, so reading the two fields
// from inside the script is a read at exactly the moment the round loop is
// about to begin.
func TestProcessInput_StreamLoopGuard_TurnStartResetsStreakAndNudge(t *testing.T) {
	var sess *Session
	var streakAtEntry int
	var nudgeAtEntry string
	rounds := &scriptedRounds{rounds: []func(*llm.ChanStream){
		func(st *llm.ChanStream) {
			sess.mu.Lock()
			streakAtEntry = sess.streamLoopTripStreak
			nudgeAtEntry = sess.pendingStreamLoopNudge
			sess.mu.Unlock()
			streamCommunicate("fine")(st)
		},
	}}
	sess, _ = loopGuardSession(t, rounds)

	// State left over from an earlier turn that ended in a hard stop.
	sess.mu.Lock()
	sess.streamLoopTripStreak = 2
	sess.pendingStreamLoopNudge = "stale nudge from a turn that is over"
	sess.mu.Unlock()

	if _, err := sess.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if streakAtEntry != 0 {
		t.Fatalf("streamLoopTripStreak at round-loop entry = %d, want 0 (a hard stop returns before the clean-completion reset can run, so the turn start has to do it)", streakAtEntry)
	}
	if nudgeAtEntry != "" {
		t.Fatalf("pendingStreamLoopNudge at round-loop entry = %q, want empty (a nudge that missed its own turn lands out of context in an unrelated one)", nudgeAtEntry)
	}
	if _, ok := findSteeringTurnByKind(sess, events.SteeringKindStreamLoop); ok {
		t.Fatal("a stale nudge was delivered as a steering turn in an unrelated turn")
	}
}

// TestProcessInput_StreamLoopGuard_HardStopSkipsModelFallbacks is the
// fallback-ineligibility fix proven end to end. A hard stop classifies as
// ErrorClassPermanent, which modelFallbackEligible otherwise reads as "walk to
// the next model" — re-running the exact request that produced the runaway
// against every configured fallback, which is precisely what the hard stop
// exists to prevent.
func TestProcessInput_StreamLoopGuard_HardStopSkipsModelFallbacks(t *testing.T) {
	rounds := &scriptedRounds{rounds: []func(*llm.ChanStream){
		streamCycleTrip("r1"),
		streamCycleTrip("r2"),
	}}
	sess, a := loopGuardSession(t, rounds, "fallback-b", "fallback-c")

	_, err := sess.ProcessInput(context.Background(), "go", nil)
	if err == nil {
		t.Fatal("ProcessInput returned nil, want the hard-stop error")
	}
	// Assert the turn ended for the reason under test. A round loop that
	// simply ran out of rounds also returns an error, which would let this
	// test pass without a guard at all.
	var loopStop *streamLoopHardStopError
	if !errors.As(err, &loopStop) {
		t.Fatalf("ProcessInput error = %v, want it to wrap *streamLoopHardStopError", err)
	}

	for _, model := range a.Models() {
		if model != "primary" {
			t.Fatalf("the fallback chain walked to %q after a loop-guard hard stop (models: %v); the hard stop must not be fallback-eligible", model, a.Models())
		}
	}
}

// TestModelFallbackEligible_StreamLoopHardStop is the narrow unit mirror of
// TestModelFallbackEligible_ProviderUnhealthy: the marker type must be
// excluded before llm.Classify runs, since the wrapped cause classifies
// permanent and permanent is otherwise eligible.
func TestModelFallbackEligible_StreamLoopHardStop(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"Bare", &streamLoopHardStopError{cause: llm.NewPermanentStreamError("openai", "loop guard: cycle", nil)}},
		{"Wrapped", errors.Join(errors.New("provider error"), &streamLoopHardStopError{cause: llm.NewPermanentStreamError("openai", "loop guard: ceiling", nil)})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if llm.Classify(tc.err) != llm.ErrorClassPermanent {
				t.Fatalf("Classify(%v) = %v, want ErrorClassPermanent (the arm under test is only load-bearing because permanent is otherwise eligible)", tc.err, llm.Classify(tc.err))
			}
			if modelFallbackEligible(tc.err, llm.DefaultRetryPolicy()) {
				t.Fatalf("modelFallbackEligible(%v) = true, want false", tc.err)
			}
		})
	}
}
