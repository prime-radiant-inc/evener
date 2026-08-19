package agent

import (
	"context"
	"fmt"
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
// ceiling.
func (g *streamLoopGuard) observeToolCall(name, args string) *loopTrip {
	_ = name
	_ = args
	return nil
}

// checkCycle checks whether the most recently appended signature completes a
// cycle of length k (1..5), repeated loopGuardCycleRepeats times
// consecutively.
func (g *streamLoopGuard) checkCycle() *loopTrip {
	return nil
}

// streamLoopNudgeText builds the tier-1 steering message for a stream-loop
// trip.
func streamLoopNudgeText(trip *loopTrip) string {
	_ = trip
	return ""
}

// streamLoopHardStopMessage builds the tier-2 error message when a second
// consecutive response trips the guard.
func streamLoopHardStopMessage(trip *loopTrip) string {
	_ = trip
	return ""
}

// describeCyclePattern names the repeated call(s) for the nudge/hard-stop
// message.
func describeCyclePattern(pattern []string) string {
	_ = pattern
	return ""
}

// deliverPendingStreamLoopNudge reads and clears s.pendingStreamLoopNudge and,
// if one was pending, appends it as a steering turn.
func (s *Session) deliverPendingStreamLoopNudge(ctx context.Context) error {
	_ = ctx
	return nil
}

var _ = fmt.Sprintf
