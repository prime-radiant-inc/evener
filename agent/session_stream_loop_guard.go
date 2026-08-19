package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
)

// streamLoopGuard detects a runaway SINGLE model response mid-stream (issue
// #94): a repeating cycle of tool-call signatures, or a raw tool-call count
// far past anything a legitimate response needs. It is the shape of
// gemini-cli's LoopDetectionService, ported to Go and to evener's stream
// event model. One instance lives for exactly one consumeModelStream attempt
// -- state does not (and must not) survive past a single response, since the
// hole this closes is specifically that nothing bounds one response.
type streamLoopGuard struct {
	// sigs holds one entry per completed tool call this response, in
	// order: canonicalToolName + ":" + raw argument text. A cheap string
	// join rather than a hash, matching the existing post-dispatch
	// breaker's signature() convention (agent/internal/tool/breaker.go) --
	// byte-identical arguments share a signature, differing JSON
	// formatting does not.
	sigs []string
}

// loopGuardCycleMaxLen and loopGuardCycleRepeats are gemini-cli's own
// numbers (LoopDetectionService: cycle length k=1..5, repeated 5 times).
// Issue #94's spec calls for this exact shape: "a repeating CYCLE of length
// k=1..5 repeated 5x."
const (
	loopGuardCycleMaxLen  = 5
	loopGuardCycleRepeats = 5
)

// loopGuardRawCeiling is the backstop for shapes with no short cycle (issue
// #94: "a HIGH raw tool-call ceiling per response as backstop... do not pick
// 15" -- odysseus #3185 killed a legitimate 18-distinct-call round at that
// threshold). Chosen with a wide safety margin above the only documented
// legitimate shape (18 distinct calls) while still catching the second
// measured runaway shape (83 calls / 48 distinct signatures / max repeat 12)
// well before it completes.
const loopGuardRawCeiling = 50

// loopTripKind names which detector fired.
type loopTripKind int

const (
	loopTripCycle loopTripKind = iota
	loopTripCeiling
)

func (k loopTripKind) String() string {
	switch k {
	case loopTripCycle:
		return "cycle"
	case loopTripCeiling:
		return "ceiling"
	default:
		return "unknown"
	}
}

// loopTrip reports one guard trip: which detector fired and a human-readable
// detail naming what repeated, for the tier-1 nudge and the tier-2 hard-stop
// message alike.
type loopTrip struct {
	Kind   loopTripKind
	Detail string
}

// streamLoopHardStopError marks the tier-2 hard stop so the fallback chain can
// exclude it by type. The wrapped cause classifies as ErrorClassPermanent,
// which modelFallbackEligible would otherwise read as "try the next model" --
// re-running the exact request that produced the runaway against every
// configured fallback, the opposite of what the hard stop exists to do.
type streamLoopHardStopError struct{ cause error }

func (e *streamLoopHardStopError) Error() string { return e.cause.Error() }
func (e *streamLoopHardStopError) Unwrap() error { return e.cause }

func newStreamLoopGuard() *streamLoopGuard {
	return &streamLoopGuard{}
}

// observeToolCall records one COMPLETED tool call (StreamEventToolCallEnd --
// the only point a call's name and arguments are both final) and reports a
// trip if the running sequence now shows a cycle or has crossed the raw
// ceiling. Cycle is checked first: it is the field's dominant shape (the
// captured incident was A,B,C x76, and every consecutive-identical detector is
// structurally blind to it) and fires far earlier than the ceiling ever could.
// The ceiling is checked second, as the backstop for shapes with no short
// cycle.
func (g *streamLoopGuard) observeToolCall(name, args string) *loopTrip {
	g.sigs = append(g.sigs, name+":"+args)
	if trip := g.checkCycle(); trip != nil {
		return trip
	}
	if len(g.sigs) >= loopGuardRawCeiling {
		return &loopTrip{
			Kind:   loopTripCeiling,
			Detail: fmt.Sprintf("%d tool calls in a single response, past the %d-call ceiling", len(g.sigs), loopGuardRawCeiling),
		}
	}
	return nil
}

// checkCycle checks whether the most recently appended signature completes a
// cycle of length k (1..5), repeated loopGuardCycleRepeats times
// consecutively. For each k it looks at exactly the trailing k*R signatures
// and asks whether they consist of R back-to-back copies of the trailing k
// -- not "a cycle occurs somewhere in a wider window," which would fire on
// coincidental repetition inside a long response; the last occurrence must
// be the (r)th repeat with nothing else interleaved.
func (g *streamLoopGuard) checkCycle() *loopTrip {
	n := len(g.sigs)
	for k := 1; k <= loopGuardCycleMaxLen; k++ {
		need := k * loopGuardCycleRepeats
		if n < need {
			continue
		}
		window := g.sigs[n-need:]
		pattern := window[:k]
		matched := true
		for i := k; i < need; i++ {
			if window[i] != pattern[i%k] {
				matched = false
				break
			}
		}
		if matched {
			return &loopTrip{
				Kind:   loopTripCycle,
				Detail: describeCyclePattern(pattern),
			}
		}
	}
	return nil
}

// streamLoopNudgeText builds the tier-1 steering message for a stream-loop
// trip: it names what repeated, wrapped as a SYSTEM-REMINDER to match every
// other steering message's envelope (agent/task_reminders.go).
func streamLoopNudgeText(trip *loopTrip) string {
	var what string
	switch trip.Kind {
	case loopTripCycle:
		what = fmt.Sprintf("You just repeated the same sequence of tool calls five times in a row within one response: %s. "+
			"That response was cut off before it could repeat a sixth time.", trip.Detail)
	case loopTripCeiling:
		what = fmt.Sprintf("Your last response made %s. "+
			"That response was cut off once it crossed the limit.", trip.Detail)
	}
	return "<SYSTEM-REMINDER>\n" + what +
		" This is a signal you are stuck, not a punishment. Stop and think about why your current approach is not working, " +
		"then either try something different or report what you tried and what failed.\n" +
		"</SYSTEM-REMINDER>"
}

// streamLoopHardStopMessage builds the tier-2 error message when a second
// consecutive response trips the guard: unlike the tier-1 nudge, there is no
// next response to read it, so it explains the failure for the turn's error
// surface (emitTurnFailure) rather than steering a model that already had
// its one chance to self-correct.
func streamLoopHardStopMessage(trip *loopTrip) string {
	return fmt.Sprintf("loop guard: two responses in a row were cut off for the same reason (%s: %s); stopping rather than nudging indefinitely", trip.Kind, trip.Detail)
}

// describeCyclePattern names the repeated call(s) for the nudge/hard-stop
// message, e.g. "manage_worktree, task_list, communicate" for a k=3 cycle.
// Signatures are "name:args"; only the name is surfaced -- the arguments are
// often large or provider-internal and the tool name is what the model needs
// to recognize it is repeating itself.
func describeCyclePattern(pattern []string) string {
	names := make([]string, len(pattern))
	for i, sig := range pattern {
		name, _, found := strings.Cut(sig, ":")
		if !found {
			name = sig
		}
		names[i] = name
	}
	return strings.Join(names, ", ")
}

// deliverPendingStreamLoopNudge reads and clears s.pendingStreamLoopNudge and,
// if one was pending, appends it as a steering turn. injectPostToolSteering
// (session_tool_round.go) is the only call site: both detectors key off
// StreamEventToolCallEnd, so a trip is only ever possible once at least one
// tool call has completed, which means the round always has tool calls and
// always reaches that function. Delivering there positions the nudge after the
// tool results, so it never lands between a tool_use turn and its tool_result
// -- providers that require them adjacent would reject the next request.
func (s *Session) deliverPendingStreamLoopNudge(ctx context.Context) error {
	s.mu.Lock()
	nudge := s.pendingStreamLoopNudge
	s.pendingStreamLoopNudge = ""
	s.mu.Unlock()
	if nudge == "" {
		return nil
	}
	return s.withResponseSideEffects(ctx, func() {
		s.emit(events.EventLoopDetection, events.LoopDetectionData{Message: nudge})
		s.appendSteeringTurn(nudge, events.SteeringKindStreamLoop)
	})
}
