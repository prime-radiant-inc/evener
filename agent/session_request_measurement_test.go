package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/schema"
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
