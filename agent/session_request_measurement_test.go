package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/provider"
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

// prepareRoundRequest builds one real model request and returns the resident
// history the round had when it was prepared: the prefix the provider's input
// count describes.
func prepareRoundRequest(t *testing.T, sess *Session) []schema.Turn {
	t.Helper()
	var timings events.RoundTimings
	if _, _, _, _, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, &timings); err != nil {
		t.Fatalf("prepare model request: %v", err)
	}
	return currentHistory(t, sess)
}

// meterWithNoSavedCount returns what a session that never recorded an input
// count reads over turns: the char/4 fallback.
func meterWithNoSavedCount(profile *provider.Profile, turns []schema.Turn) int {
	return contextmgr.NewManager(profile, nil, nil).EstimateUsage(turns, 0).Used
}

// TestRequestMeasurementAnchorsTheCountToTheDispatchedPrefix pins which history
// a live reply's input count belongs to: the prefix the request was prepared
// from, not whatever history holds when the reply settles. A turn accepted while
// the call is in flight was not part of the request, so the count must not claim
// to cover it — the reader prices it as a delta instead.
func TestRequestMeasurementAnchorsTheCountToTheDispatchedPrefix(t *testing.T) {
	sess := newSession(t)
	profile := sess.currentProfile()
	sess.mu.Lock()
	sess.history = pricedTurns(3, 40)
	sess.mu.Unlock()
	dispatched := prepareRoundRequest(t, sess)
	if len(dispatched) < 2 {
		t.Fatalf("fixture prepared a request over %d turns; the append proof needs a multi-turn prefix", len(dispatched))
	}

	appended := pricedTurns(2, 40)
	sess.mu.Lock()
	sess.history = append(sess.history, appended...)
	sess.mu.Unlock()

	sess.recordResponseUsage(llm.Response{Usage: llm.Usage{InputTokens: 600}}, llm.Request{}, profile)

	if got := sess.contextMgr.LastInputTokens(); got != 600 {
		t.Fatalf("LastInputTokens = %d, want 600: an ordinary append keeps the count", got)
	}
	want := 600 + meterWithNoSavedCount(profile, appended)
	if got := sess.contextMgr.EstimateUsage(currentHistory(t, sess), 0).Used; got != want {
		t.Fatalf("context meter after two turns landed mid-flight = %d, want %d (the saved count plus the appended delta): the count was anchored to the post-append history", got, want)
	}
}

// TestRequestMeasurementDropsTheCountWhenHistoryIsRewrittenInFlight pins the
// snapshot half of compaction safety: a fold or repair that rewrites history
// while the reply streams makes the pre-rewrite count describe a conversation
// that no longer exists. Pairing it with the rewritten prefix would make the
// context meter price the new history from an unrelated count.
func TestRequestMeasurementDropsTheCountWhenHistoryIsRewrittenInFlight(t *testing.T) {
	sess := newSession(t)
	profile := sess.currentProfile()
	sess.mu.Lock()
	sess.history = pricedTurns(4, 40)
	sess.mu.Unlock()
	dispatched := prepareRoundRequest(t, sess)
	if len(dispatched) < 2 {
		t.Fatalf("fixture prepared a request over %d turns; the rewrite proof needs a multi-turn prefix", len(dispatched))
	}

	// Rewrite an EARLIER turn in place and keep the measured boundary turn
	// where it is: the length and the prefix anchor both still hold, so only
	// the rewrite marker itself can tell the count no longer applies.
	rewritten := append([]schema.Turn{}, dispatched...)
	rewritten[0] = schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("y", 40)))
	sess.mu.Lock()
	sess.history = rewritten
	sess.bumpHistoryRevisionLocked()
	sess.mu.Unlock()

	sess.recordResponseUsage(llm.Response{Usage: llm.Usage{InputTokens: 600}}, llm.Request{}, profile)

	if got := sess.contextMgr.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens = %d, want 0: a count measured before the rewrite must not be charged to the rewritten history", got)
	}
	if got, want := sess.contextMgr.EstimateUsage(rewritten, 0).Used, meterWithNoSavedCount(profile, rewritten); got != want {
		t.Fatalf("context meter after the rewrite = %d, want the %d a session with no saved count reads", got, want)
	}
}

// TestRequestMeasurementDropsTheCountWhenRequestOverheadChanges pins the other
// half of the request shape: a saved count covers the system prompt and tool
// definitions the request was built with. When either changes while the call is
// in flight, the count belongs to a smaller (or different) request and must not
// be charged to the shape the session will send next.
func TestRequestMeasurementDropsTheCountWhenRequestOverheadChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Session)
	}{
		{
			name: "system prompt",
			change: func(s *Session) {
				s.mu.Lock()
				s.cachedSystemPrompt += "\n\ncontext that joined mid-flight"
				s.mu.Unlock()
			},
		},
		{
			name: "tool definitions",
			change: func(s *Session) {
				s.registerTool("mid_flight_tool", "advertised while the reply streamed", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
					return nil, nil
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession(t)
			profile := sess.currentProfile()
			prepareRoundRequest(t, sess)
			tc.change(sess)

			sess.recordResponseUsage(llm.Response{Usage: llm.Usage{InputTokens: 600}}, llm.Request{}, profile)

			if got := sess.contextMgr.LastInputTokens(); got != 0 {
				t.Fatalf("LastInputTokens = %d, want 0: the count was measured against a different request shape than the session now sends", got)
			}
		})
	}
}

// TestRequestMeasurementDropsTheCountAfterAModelSwitch is the target half of
// the same rule: the count belongs to the model, instance, and protocol that
// produced it, so a switch during the call discards it.
func TestRequestMeasurementDropsTheCountAfterAModelSwitch(t *testing.T) {
	sess := newSession(t)
	profile := sess.currentProfile()
	prepareRoundRequest(t, sess)
	sess.contextMgr.SetProfile(NewOpenAIProfile("gpt-5.2-mini"))

	sess.recordResponseUsage(llm.Response{Usage: llm.Usage{InputTokens: 600}}, llm.Request{}, profile)

	if got := sess.contextMgr.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens = %d, want 0: a count from another target was recorded", got)
	}
}

// TestFoldPublicationClearsTheRecordedInputCount pins the publication half: a
// count recorded before a fold that rewrote history describes the pre-fold
// prefix, so the fold drops it in the same lock hold that publishes.
func TestFoldPublicationClearsTheRecordedInputCount(t *testing.T) {
	sess := compactAtSession(t, 0)
	// Pressure above the default 0.90 trigger, so the request-time fold really
	// rewrites history instead of publishing it unchanged.
	forcePressureAbove(t, sess, 0.91)
	if got := sess.contextMgr.LastInputTokens(); got == 0 {
		t.Fatal("fixture recorded no input count, so this proof is vacuous")
	}

	var timings events.RoundTimings
	if _, _, _, _, _, _, err := sess.prepareModelRequestWithError(context.Background(), 1, &timings); err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	folded := currentHistory(t, sess)
	if !hasCheckpoint(folded) {
		t.Fatal("fixture published no checkpoint, so no fold rewrote history")
	}
	if got := sess.contextMgr.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens after a fold that rewrote history = %d, want 0: the pre-fold count outlived the publication", got)
	}
	profile := sess.currentProfile()
	if got, want := sess.contextMgr.EstimateUsage(folded, 0).Used, meterWithNoSavedCount(profile, folded); got != want {
		t.Fatalf("context meter after the fold = %d, want the %d a session with no saved count reads", got, want)
	}
}

// TestRequestMeasurementSurvivesAnUnchangedRequestTimeFold pins the guard: the
// default strategy folds before EVERY request, and a fold that finds nothing to
// fold still publishes the same history. Clearing the saved count there would
// wipe the provider's measurement before every request, so the full-history
// estimate and the pressure meter would fall back to char/4 each round.
func TestRequestMeasurementSurvivesAnUnchangedRequestTimeFold(t *testing.T) {
	sess := compactAtSession(t, 0)
	history := currentHistory(t, sess)
	sess.contextMgr.RecordInputTokens(900, len(history), "")

	var timings events.RoundTimings
	_, _, _, req, _, _, err := sess.prepareModelRequestWithError(context.Background(), 1, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	presented := currentHistory(t, sess)
	if hasCheckpoint(presented) || !reflect.DeepEqual(history, presented) {
		t.Fatalf("fixture rewrote history (checkpoint=%v), so the unchanged-publication proof is vacuous", hasCheckpoint(presented))
	}
	if got := sess.contextMgr.LastInputTokens(); got != 900 {
		t.Fatalf("LastInputTokens after an unchanged request-time fold = %d, want the saved 900", got)
	}
	if got := req.FullHistoryInputTokensEstimate; got < 900 {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want at least the saved 900: the count was wiped before the request was built", got)
	}
}

// TestRequestMeasurementRecordsLiveTurns drives the whole pipeline twice: each
// round's count is recorded against the prefix it dispatched, so the reply the
// round appended afterwards is priced on top of it, and the persisted meta names
// that prefix rather than the post-reply history.
func TestRequestMeasurementRecordsLiveTurns(t *testing.T) {
	sess := newSession(t, withStateDir(t.TempDir()), withSteps(
		func(llm.Request) llm.Response {
			resp := agenttest.FinalResponse("first reply")
			resp.Usage.InputTokens = 600
			return resp
		},
		func(llm.Request) llm.Response {
			resp := agenttest.FinalResponse("second reply")
			resp.Usage.InputTokens = 5000
			return resp
		},
	))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter; only fires on a genuine hang.
	defer cancel()

	for _, want := range []int{600, 5000} {
		if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		if got := sess.contextMgr.LastInputTokens(); got != want {
			t.Fatalf("LastInputTokens = %d, want %d", got, want)
		}
		resident := currentHistory(t, sess)
		if got := sess.contextMgr.EstimateUsage(resident, 0).Used; got <= want {
			t.Fatalf("context meter = %d, want more than the saved %d: the reply appended after the dispatched prefix was not priced", got, want)
		}
	}

	resident := currentHistory(t, sess)
	meta := sess.Meta()
	if meta.LastInputTokens != 5000 {
		t.Fatalf("meta LastInputTokens = %d, want 5000", meta.LastInputTokens)
	}
	if meta.LastInputMeasuredLen == nil {
		t.Fatal("meta persisted no measured length")
	}
	if *meta.LastInputMeasuredLen >= len(resident) {
		t.Fatalf("meta LastInputMeasuredLen = %d over a %d-turn history: the persisted prefix covers the reply appended after dispatch", *meta.LastInputMeasuredLen, len(resident))
	}
	if meta.LastInputAnchor == "" {
		t.Fatal("meta persisted no prefix anchor")
	}
}
