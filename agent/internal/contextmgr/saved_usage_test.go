package contextmgr

import (
	"slices"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// identifiedHistory returns n assistant turns of chars bytes each, every turn
// carrying a distinct TurnID derived from label. A durable transcript entry has
// such a TurnID; this fixture keeps only that identity so two histories of the
// same length differ exactly in which turns they hold.
func identifiedHistory(n, chars int, label string) []schema.Turn {
	history := textHistory(n, chars)
	for i := range history {
		history[i].TurnID = label + "-" + strconv.Itoa(i)
	}
	return history
}

// TestPrefixAnchorSurvivesRestoreIdentity pins the anchor's stability across a
// restore: a live turn carries the identity its producer stamped, while the
// restored transcript entry additionally carries the writer's TurnID. The anchor
// must not change between the two, or every resume would drop a valid count.
func TestPrefixAnchorSurvivesRestoreIdentity(t *testing.T) {
	live := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("hi"), ConversationInputIDs: []string{"input_1"}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer"), ResponseID: "resp_1"},
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("call_1", "read_file", "body", false)},
	}
	restored := slices.Clone(live)
	for i := range restored {
		restored[i].TurnID = "t_" + strconv.Itoa(i)
		restored[i].Format = schema.TurnFormatIdentity
	}
	for i := range live {
		if got, want := PrefixAnchor(restored, i+1), PrefixAnchor(live, i+1); got != want {
			t.Fatalf("anchor at %d after restore = %q, want %q: the transcript's TurnID must not change the measured prefix's identity", i+1, got, want)
		}
	}
}

// TestSavedInputUsageRejectsAShorterPrefix pins the half of the durable-prefix
// rule main can enforce: a saved count belongs to the turns the provider
// actually received, so a truncated history must be priced by the char/4
// fallback. Both readers are checked: EstimateUsage takes the status_profile.go
// path, Pressure the direct path in pressure.go.
func TestSavedInputUsageRejectsAShorterPrefix(t *testing.T) {
	const saved = 600
	profile := testProfile("openai", "gpt-5.2", 1000)
	history := identifiedHistory(6, 40, "measured")
	cm := NewManager(profile, nil, nil)
	if !cm.RecordInputTokensFor(profile, saved, len(history), PrefixAnchor(history, len(history))) {
		t.Fatal("the manager's own profile was refused")
	}
	if got := cm.EstimateUsage(history, 0).Used; got != saved {
		t.Fatalf("EstimateUsage on the measured prefix = %d, want %d", got, saved)
	}

	truncated := history[:3]
	est := cm.EstimateUsage(truncated, 0)
	if est.Used != 30 {
		t.Fatalf("EstimateUsage on a truncated prefix = %d, want 30 (three 40-char turns): the saved count belongs to turns this history does not hold", est.Used)
	}
	if got, want := cm.Pressure(truncated, 0), float64(est.Used)/float64(est.Window); got != want {
		t.Fatalf("Pressure on a truncated prefix = %v, want %v", got, want)
	}
}

// TestSavedInputUsageRejectsARewrittenPrefixOfTheSameLength pins the gap the
// length check alone cannot see: a count belongs to the turns the provider
// actually received, so a prefix rewritten to the same (or a greater) length
// must be priced by the char/4 fallback. The prefix anchor, not the length, is
// what distinguishes the two histories. Both readers are checked: EstimateUsage
// takes the status_profile.go path, Pressure the direct path in pressure.go.
func TestSavedInputUsageRejectsARewrittenPrefixOfTheSameLength(t *testing.T) {
	const saved = 600
	profile := testProfile("openai", "gpt-5.2", 1000)
	measured := identifiedHistory(6, 40, "measured")
	cm := NewManager(profile, nil, nil)
	if !cm.RecordInputTokensFor(profile, saved, len(measured), PrefixAnchor(measured, len(measured))) {
		t.Fatal("the manager's own profile was refused")
	}
	if got := cm.EstimateUsage(measured, 0).Used; got != saved {
		t.Fatalf("EstimateUsage on the measured prefix = %d, want %d", got, saved)
	}

	rewritten := identifiedHistory(6, 40, "rewritten")
	est := cm.EstimateUsage(rewritten, 0)
	if est.Used != 60 {
		t.Fatalf("EstimateUsage on a same-length rewritten prefix = %d, want 60 (six 40-char turns): the saved count belongs to other turns", est.Used)
	}
	if got, want := cm.Pressure(rewritten, 0), float64(est.Used)/float64(est.Window); got != want {
		t.Fatalf("Pressure on a same-length rewritten prefix = %v, want %v", got, want)
	}

	longer := append(slices.Clone(rewritten), identifiedHistory(1, 40, "tail")...)
	if got := cm.EstimateUsage(longer, 0).Used; got != 70 {
		t.Fatalf("EstimateUsage on a longer rewritten history = %d, want 70 (seven 40-char turns): the saved count covers turns this history does not hold", got)
	}

	// A history that still starts with the measured turns keeps the count and
	// prices only the turns appended after it.
	appended := append(slices.Clone(measured), identifiedHistory(2, 40, "tail")...)
	if got := cm.EstimateUsage(appended, 0).Used; got != saved+20 {
		t.Fatalf("EstimateUsage on the measured prefix plus two turns = %d, want %d", got, saved+20)
	}
}

// TestSavedInputUsageNeedsTheAnchorInViewOrInTheMemo pins the suffix path the
// session's ContextStatus takes: it copies only the turns the memo has not
// walked, so the anchor turn can sit outside the copy. Without a memo for the
// same revision the manager must refuse to answer rather than charge a count it
// could not check; once the memo exists it may answer from it.
func TestSavedInputUsageNeedsTheAnchorInViewOrInTheMemo(t *testing.T) {
	const saved = 600
	profile := testProfile("openai", "gpt-5.2", 1000)
	history := identifiedHistory(6, 40, "measured")
	cm := NewManager(profile, nil, nil)
	if !cm.RecordInputTokensFor(profile, saved, 3, PrefixAnchor(history, 3)) {
		t.Fatal("the manager's own profile was refused")
	}

	// The copy starts exactly at the measured prefix's end, so the anchor turn
	// is outside it and no memo for this revision exists.
	if metrics, _, _, ok := cm.EstimateStatusForHistory(history[3:], len(history), 0, 7); ok {
		t.Fatalf("answered %+v with the anchor turn outside the copy and no memo", metrics)
	}

	// A full copy verifies the anchor, and the answer stands.
	metrics, _, _, ok := cm.EstimateStatusForHistory(history, len(history), 0, 7)
	if !ok || metrics.Used != saved+30 {
		t.Fatalf("full history = (%+v, ok=%v), want Used %d", metrics, ok, saved+30)
	}

	// The memo now vouches for the same revision, so a nil copy can answer.
	metrics, _, _, ok = cm.EstimateStatusForHistory(nil, len(history), 0, 7)
	if !ok || metrics.Used != saved+30 {
		t.Fatalf("nil history after the memo = (%+v, ok=%v), want Used %d", metrics, ok, saved+30)
	}
}

// TestSavedInputUsageSurvivesReopenForTheSamePrefixAndTarget pins the calls the
// restore path makes (agent/session_init.go seeds meta.LastInputTokens with
// RecordInputTokens at the persisted measured length and anchor): the saved
// count prices the durable prefix, and the turns appended after it are
// estimated on top. A manager that was never seeded must hold no count at all
// and price by estimate, so adding persistence carelessly cannot make a fresh
// manager inherit a stale saved count. It does not drive the session restore
// itself.
func TestSavedInputUsageSurvivesReopenForTheSamePrefixAndTarget(t *testing.T) {
	const saved = 600
	profile := testProfile("openai", "gpt-5.2", 1000)
	prefix := identifiedHistory(6, 40, "measured")

	reopened := NewManager(profile, nil, nil)
	if got := reopened.LastInputTokens(); got != 0 {
		t.Fatalf("fresh manager LastInputTokens = %d, want 0", got)
	}
	if got := reopened.EstimateUsage(prefix, 0).Used; got != 60 {
		t.Fatalf("fresh manager estimate = %d, want 60 (six 40-char turns)", got)
	}

	reopened.RecordInputTokens(saved, len(prefix), PrefixAnchor(prefix, len(prefix)))
	if got := reopened.LastInputTokens(); got != saved {
		t.Fatalf("seeded LastInputTokens = %d, want %d", got, saved)
	}
	appended := append(slices.Clone(prefix), identifiedHistory(2, 40, "tail")...)
	est := reopened.EstimateUsage(appended, 0)
	if est.Used != saved+20 {
		t.Fatalf("EstimateUsage after reopen = %d, want %d (saved count plus two appended turns)", est.Used, saved+20)
	}
	if got, want := reopened.Pressure(appended, 0), float64(est.Used)/float64(est.Window); got != want {
		t.Fatalf("Pressure after reopen = %v, want %v", got, want)
	}
}

// TestSavedInputUsageRejectedAfterTargetChange covers the target half of the
// same rule: a count is only valid for the instance, model, protocol, and
// estimator row that produced it. When a fallback recipient answers and the
// manager's target changes with it, the new target must price from its own
// estimate rather than inherit the configured recipient's count, and switching
// back must not resurrect the discarded count. This covers SetProfile only:
// the restore seed in agent/session_init.go calls RecordInputTokens directly
// and bypasses the target check.
func TestSavedInputUsageRejectedAfterTargetChange(t *testing.T) {
	const saved = 600
	configured := testProfile("openai", "gpt-5.2", 1000)
	prefix := textHistory(6, 40)
	// The estimator reads more of a resolved row than its identity. This row
	// keeps the configured instance, model, and protocol and differs only in a
	// cap the estimator bills against, so the deep comparison has to reject it.
	row := configured.Resolved()
	highDetail := "high"
	row.Caps.ImageDetail = &highDetail
	for _, tc := range []struct {
		name    string
		profile *provider.Profile
	}{
		{name: "different instance", profile: testProfile("anthropic", "claude-opus-4-6", 1000)},
		{name: "different model", profile: testProfile("openai", "gpt-5.2-mini", 1000)},
		{name: "different protocol", profile: provider.FromResolved(registry.Resolved{
			Instance: "openai", ModelID: "gpt-5.2", Protocol: registry.ProtocolOpenAIChat,
		}, nil)},
		{name: "different estimator row", profile: provider.FromResolved(row, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := NewManager(configured, nil, nil)
			cm.RecordInputTokens(saved, len(prefix), PrefixAnchor(prefix, len(prefix)))
			cm.SetProfile(tc.profile)
			if got := cm.LastInputTokens(); got != 0 {
				t.Fatalf("LastInputTokens = %d after target change, want 0", got)
			}
			want := NewManager(tc.profile, nil, nil).EstimateUsage(prefix, 0)
			if got := cm.EstimateUsage(prefix, 0).Used; got != want.Used {
				t.Fatalf("EstimateUsage under the new target = %d, want its own estimate %d: a count from another recipient is not conservative", got, want.Used)
			}
			if got, wantPressure := cm.Pressure(prefix, 0), float64(want.Used)/float64(want.Window); got != wantPressure {
				t.Fatalf("Pressure under the new target = %v, want %v", got, wantPressure)
			}
			cm.SetProfile(configured)
			if got := cm.LastInputTokens(); got != 0 {
				t.Fatalf("LastInputTokens = %d after switching back, want 0: the discarded count must not return", got)
			}
		})
	}
}
