package contextmgr

import (
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestSavedInputUsageRejectsAShorterPrefix pins the half of the durable-prefix
// rule main can enforce: a saved count belongs to the turns the provider
// actually received, so a truncated history must be priced by the char/4
// fallback. Both readers are checked: EstimateUsage takes the status_profile.go
// path, Pressure the direct path in pressure.go.
func TestSavedInputUsageRejectsAShorterPrefix(t *testing.T) {
	const saved = 600
	profile := testProfile("openai", "gpt-5.2", 1000)
	history := textHistory(6, 40)
	cm := NewManager(profile, nil, nil)
	if !cm.RecordInputTokensFor(profile, saved, len(history), "") {
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

// TestSavedInputUsageSurvivesReopenForTheSamePrefixAndTarget pins the calls the
// restore path makes (agent/session_init.go seeds meta.LastInputTokens with
// RecordInputTokens at the restored history's length): the saved count prices
// the durable prefix, and the turns appended after it are estimated on top. A
// manager that was never seeded must hold no count at all and price by
// estimate, so adding persistence carelessly cannot make a fresh manager
// inherit a stale saved count. It does not drive the session restore itself.
func TestSavedInputUsageSurvivesReopenForTheSamePrefixAndTarget(t *testing.T) {
	const saved = 600
	profile := testProfile("openai", "gpt-5.2", 1000)
	prefix := textHistory(6, 40)

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
	appended := append(slices.Clone(prefix), textHistory(2, 40)...)
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

// rewrittenHistory is six 40-char assistant turns with different bytes from
// textHistory's: the same length and shape, so only the anchor can tell them
// apart.
func rewrittenHistory(n, chars int) []schema.Turn {
	history := make([]schema.Turn, 0, n)
	for range n {
		history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("y", chars))))
	}
	return history
}

// TestSavedInputUsageRejectsARewrittenPrefixOfTheSameLength pins the half of the
// prefix rule the length check cannot enforce (issue #7154): a history rewritten
// to the same length is not the history the provider received, so it must be
// priced by char/4. Both readers are checked: EstimateUsage takes the
// status_profile.go path, Pressure the direct path in pressure.go.
func TestSavedInputUsageRejectsARewrittenPrefixOfTheSameLength(t *testing.T) {
	const saved = 600
	profile := testProfile("openai", "gpt-5.2", 1000)
	measured := textHistory(6, 40)
	cm := NewManager(profile, nil, nil)
	if !cm.RecordInputTokensFor(profile, saved, len(measured), PrefixAnchor(measured, len(measured))) {
		t.Fatal("the manager's own profile was refused")
	}
	if got := cm.EstimateUsage(measured, 0).Used; got != saved {
		t.Fatalf("EstimateUsage on the measured prefix = %d, want %d", got, saved)
	}

	rewritten := rewrittenHistory(6, 40)
	est := cm.EstimateUsage(rewritten, 0)
	if est.Used != 60 {
		t.Fatalf("EstimateUsage on a same-length rewrite = %d, want 60 (six 40-char turns): the saved count belongs to turns this history no longer holds", est.Used)
	}
	if got, want := cm.Pressure(rewritten, 0), float64(est.Used)/float64(est.Window); got != want {
		t.Fatalf("Pressure on a same-length rewrite = %v, want %v", got, want)
	}
}

// TestRestoreMeasurementRequiresTheSavedTargetAndPrefix pins the restore seed: a
// saved count is reused only when the resume restored the target that produced
// it and the history still carries the measured prefix. A different target, a
// rewritten prefix, a shorter history, or a meta missing any part of the
// measurement seeds nothing.
func TestRestoreMeasurementRequiresTheSavedTargetAndPrefix(t *testing.T) {
	const saved = 600
	profile := testProfile("openai", "gpt-5.2", 1000)
	history := textHistory(6, 40)
	anchor := PrefixAnchor(history, len(history))
	target := measurementTarget(profile)

	t.Run("matching target and prefix", func(t *testing.T) {
		cm := NewManager(profile, nil, nil)
		if !cm.RestoreInputMeasurement(saved, len(history), anchor, target, history) {
			t.Fatal("the saved measurement was refused")
		}
		if got := cm.LastInputTokens(); got != saved {
			t.Fatalf("LastInputTokens after restore = %d, want %d", got, saved)
		}
		appended := append(slices.Clone(history), textHistory(2, 40)...)
		if got := cm.EstimateUsage(appended, 0).Used; got != saved+20 {
			t.Fatalf("EstimateUsage after restore = %d, want %d (saved count plus two appended turns)", got, saved+20)
		}
	})

	rewritten := rewrittenHistory(6, 40)
	for _, tc := range []struct {
		name        string
		profile     *provider.Profile
		tokens      int
		measuredLen int
		anchor      string
		target      string
		history     []schema.Turn
	}{
		{name: "different instance", profile: testProfile("anthropic", "claude-opus-4-6", 1000), tokens: saved, measuredLen: 6, anchor: anchor, target: target, history: history},
		{name: "different model", profile: testProfile("openai", "gpt-5.2-mini", 1000), tokens: saved, measuredLen: 6, anchor: anchor, target: target, history: history},
		{name: "different protocol", profile: provider.FromResolved(registry.Resolved{
			Instance: "openai", ModelID: "gpt-5.2", Protocol: registry.ProtocolOpenAIChat,
		}, nil), tokens: saved, measuredLen: 6, anchor: anchor, target: target, history: history},
		{name: "rewritten prefix", profile: profile, tokens: saved, measuredLen: 6, anchor: anchor, target: target, history: rewritten},
		{name: "history shorter than the measurement", profile: profile, tokens: saved, measuredLen: 6, anchor: anchor, target: target, history: history[:3]},
		{name: "missing anchor", profile: profile, tokens: saved, measuredLen: 6, anchor: "", target: target, history: history},
		{name: "missing target", profile: profile, tokens: saved, measuredLen: 6, anchor: anchor, target: "", history: history},
		{name: "measured over no turns", profile: profile, tokens: saved, measuredLen: 0, anchor: "", target: target, history: history},
		{name: "empty measured prefix with an anchor", profile: profile, tokens: saved, measuredLen: 0, anchor: anchor, target: target, history: history},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := NewManager(tc.profile, nil, nil)
			if cm.RestoreInputMeasurement(tc.tokens, tc.measuredLen, tc.anchor, tc.target, tc.history) {
				t.Fatal("seeded a count that does not describe this target and prefix")
			}
			if got := cm.LastInputTokens(); got != 0 {
				t.Fatalf("LastInputTokens = %d, want 0", got)
			}
		})
	}
}

// TestPrefixAnchorHandlesAnEmptyPrefix pins the empty case: no measured turn has
// no anchor, and a count taken over no turns stays a usable baseline, exactly as
// it was before anchors existed.
func TestPrefixAnchorHandlesAnEmptyPrefix(t *testing.T) {
	if got := PrefixAnchor(nil, 0); got != "" {
		t.Fatalf("PrefixAnchor(nil, 0) = %q, want empty", got)
	}
	if got := PrefixAnchor(textHistory(2, 10), 3); got != "" {
		t.Fatalf("PrefixAnchor past the end = %q, want empty", got)
	}
	profile := testProfile("openai", "gpt-5.2", 1000)
	cm := NewManager(profile, nil, nil)
	cm.RecordInputTokens(100, 0, PrefixAnchor(nil, 0))
	if got := cm.EstimateUsage(textHistory(2, 40), 0).Used; got != 120 {
		t.Fatalf("EstimateUsage over a count measured on no turns = %d, want 120 (100 plus two 40-char turns)", got)
	}
}
