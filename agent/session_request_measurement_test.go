package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

// pricedTurns returns n user turns of chars bytes each, so the char/4 fallback
// prices a known amount and a saved count is easy to tell apart from it.
func pricedTurns(n, chars int) []schema.Turn {
	turns := make([]schema.Turn, n)
	for i := range turns {
		turns[i] = schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("x", chars)))
	}
	return turns
}

// TestRecordResponseUsageDropsAMeasurementAcrossAMidStreamRewrite pins which
// history a response's input count may be recorded against. The count describes
// the request's snapshot; a fold that publishes while the reply streams rewrites
// history, and pairing the pre-fold count with the post-fold length and anchor
// would make the context meter jump back to the old usage.
func TestRecordResponseUsageDropsAMeasurementAcrossAMidStreamRewrite(t *testing.T) {
	sess := newSession(t)
	profile := sess.currentProfile()
	pre := pricedTurns(4, 40)
	// The fold rewrites the earlier turns but leaves the measured boundary turn
	// at the same index, so the length and the anchor both still hold: only the
	// revision says the conversation was rewritten under the reply.
	folded := append(pricedTurns(3, 40), pre[3])

	sess.mu.Lock()
	sess.history = pre
	sess.historyRevision = 7
	sess.requestMeasurement = requestMeasurement{
		ok:       true,
		length:   len(pre),
		anchor:   contextmgr.PrefixAnchor(pre, len(pre)),
		revision: 7,
	}
	sess.mu.Unlock()

	// A fold publishes while the reply is in flight.
	sess.mu.Lock()
	sess.history = folded
	sess.historyRevision++
	sess.mu.Unlock()

	sess.recordResponseUsage(llm.Response{Usage: llm.Usage{InputTokens: 600}}, llm.Request{}, profile)

	if got := sess.contextMgr.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens = %d, want 0: a pre-fold count must not be charged to the rewritten history", got)
	}
	fresh := contextmgr.NewManager(profile, nil, nil)
	if got, want := sess.contextMgr.EstimateUsage(folded, 0).Used, fresh.EstimateUsage(folded, 0).Used; got != want {
		t.Fatalf("context meter after the rewrite = %d, want the %d a session with no saved count reads", got, want)
	}
}

// TestRecordResponseUsageKeepsAMeasurementAcrossAppends is the other half: an
// ordinary append during the reply leaves the measured prefix intact, so the
// count is recorded and the appended turns are priced as a delta on top of it.
func TestRecordResponseUsageKeepsAMeasurementAcrossAppends(t *testing.T) {
	sess := newSession(t)
	profile := sess.currentProfile()
	pre := pricedTurns(4, 40)
	appended := pricedTurns(2, 40)

	sess.mu.Lock()
	sess.history = pre
	sess.historyRevision = 7
	sess.requestMeasurement = requestMeasurement{
		ok:       true,
		length:   len(pre),
		anchor:   contextmgr.PrefixAnchor(pre, len(pre)),
		revision: 7,
	}
	sess.history = append(append([]schema.Turn{}, pre...), appended...)
	sess.mu.Unlock()

	sess.recordResponseUsage(llm.Response{Usage: llm.Usage{InputTokens: 600}}, llm.Request{}, profile)

	if got := sess.contextMgr.LastInputTokens(); got != 600 {
		t.Fatalf("LastInputTokens = %d, want 600: growing history keeps the count", got)
	}
	fresh := contextmgr.NewManager(profile, nil, nil)
	want := 600 + fresh.EstimateUsage(appended, 0).Used
	if got := sess.contextMgr.EstimateUsage(sess.history, 0).Used; got != want {
		t.Fatalf("context meter with two appended turns = %d, want %d (the saved count plus the appended delta)", got, want)
	}
}

// TestRequestMeasurementCoversTurnsAppendedByAPreDispatchRebuild pins the other
// append site: the final-dispatch skill reload appends a typed notification to
// resident history and rebuilds the request before it goes out. The provider's
// input count therefore covers the appended turn, and a measurement still
// pointing at the pre-rebuild snapshot prices it a second time as a delta.
func TestRequestMeasurementCoversTurnsAppendedByAPreDispatchRebuild(t *testing.T) {
	sess := newSession(t, withStateDir(t.TempDir()))
	profile := sess.currentProfile()
	if sess.skillAvailableToSession(skill.GardeningMemorySkillName) {
		t.Fatal("fixture needs a session that cannot follow the gardening-memory skill")
	}

	sess.mu.Lock()
	sess.history = append([]schema.Turn{}, pricedTurns(3, 40)...)
	// An obligation whose skill this session cannot follow appends a typed
	// failure notification and sets appendedNotifications, so the final
	// dispatch rebuilds the request through rebuildForAppendedSkillTurns.
	sess.skillLifecycle.Obligations = append(sess.skillLifecycle.Obligations, schema.SkillDeliveryObligation{
		InvocationID: "inv-7154-rebuild",
		Identity:     schema.SkillContentIdentity{Name: skill.GardeningMemorySkillName},
		Route:        "model_tool",
	})
	sess.skillLifecycle.Revision++
	before := len(sess.history)
	sess.mu.Unlock()

	var timings events.RoundTimings
	if _, _, _, _, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, &timings); err != nil {
		t.Fatalf("prepare model request: %v", err)
	}

	sess.mu.Lock()
	resident := append([]schema.Turn{}, sess.history...)
	sess.mu.Unlock()
	if len(resident) <= before {
		t.Fatalf("history has %d turns, want more than the %d it held before the request: the delivery notification never landed, so this proof is vacuous", len(resident), before)
	}
	// Positive control by outcome identity, not wording: the appended turn
	// carries the failed delivery outcome for the obligation.
	appended := resident[len(resident)-1]
	carried := false
	if appended.SkillState != nil {
		for _, outcome := range appended.SkillState.Outcomes {
			if outcome.InvocationID == "inv-7154-rebuild" {
				carried = true
			}
		}
	}
	if !carried {
		t.Fatalf("the appended turn carries no delivery outcome for inv-7154-rebuild: %#v", appended)
	}

	// The rebuilt request presents the whole resident history, so the count the
	// provider returns covers every resident turn and the meter reads it back
	// exactly, with no delta for the appended turn.
	sess.recordResponseUsage(llm.Response{Usage: llm.Usage{InputTokens: 1000}}, llm.Request{}, profile)
	if got := sess.contextMgr.LastInputTokens(); got != 1000 {
		t.Fatalf("LastInputTokens = %d, want 1000", got)
	}
	if got := sess.contextMgr.EstimateUsage(resident, 0).Used; got != 1000 {
		fresh := contextmgr.NewManager(profile, nil, nil)
		t.Fatalf("context meter after the rebuilt request = %d, want 1000: the appended turn was priced twice as a %d-token delta", got, fresh.EstimateUsage(resident[before:], 0).Used)
	}
}
