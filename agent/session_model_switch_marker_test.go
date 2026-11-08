package agent

// Tests for Task 5: the persisted model-switch marker turn (N5) and the
// pre-refresh knowledge-cutoff recompute (G15).
//
// (a) a successful switch appends a TurnModelSwitch turn reading
//     "Switched model: <old> → <new>";
// (c) expandHistory excludes the marker turn from model requests;
// (d) the marker carries a warning line when estimated context usage
//     exceeds the new profile's compaction threshold, and another when the
//     switch dropped now-invalid cfg.ModelFallbacks entries;
// (e) the cached system prompt reflects the new model's knowledge cutoff
//     after a switch.
//
// (b), the live-projector systemMessage echo, is covered in
// internal/appprojector/appwire_projection_test.go
// (TestProject_ModelChanged); ProjectTurn's TurnModelSwitch case is covered
// in internal/apptranscript/apptranscript_test.go.

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// tinyWindowResolver maps "tiny/<model>" to an openai profile whose context
// window is overridden down to a handful of tokens, so even the base system
// prompt alone pushes estimated pressure over the compaction threshold —
// deterministic, no need to seed a huge synthetic history.
func tinyWindowResolver(ref string) (*provider.Profile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	switch parts[0] {
	case "openai":
		return NewOpenAIProfile(parts[1]), nil
	case "tiny":
		return WithContextWindow(NewOpenAIProfile(parts[1]), 100), nil
	}
	return nil, nil
}

// assumedWindowResolver maps "openai/<model>" to the catalog profile and
// "gw/<model>" to an openai-compatible custom gateway whose model row declares
// no context window — the unknown-window target a switch now budgets against
// contextmgr.AssumedContextWindow (issue #4598).
func assumedWindowResolver(ref string) (*provider.Profile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	switch parts[0] {
	case "openai":
		return NewOpenAIProfile(parts[1]), nil
	case "gw":
		return newOpenAICompatProfile("gw", parts[1], 0), nil
	}
	return nil, nil
}

// declaredWindowResolver maps "openai/<model>" to the catalog profile and
// "win/<model>" to a profile whose context window is declared at 4096 — a known
// window small enough that the base system prompt alone crosses its 0.80
// compaction threshold, with a window figure (4096) the rendered percentage
// cannot collide with.
func declaredWindowResolver(ref string) (*provider.Profile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	switch parts[0] {
	case "openai":
		return NewOpenAIProfile(parts[1]), nil
	case "win":
		return WithContextWindow(NewOpenAIProfile(parts[1]), 4096), nil
	}
	return nil, nil
}

// lastMarkerTurn returns the most recently appended TurnModelSwitch turn, or
// fails the test if none exists.
func lastMarkerTurn(t *testing.T, sess *Session) schema.Turn {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, h := range slices.Backward(sess.history) {
		if h.Kind == schema.TurnModelSwitch {
			return h
		}
	}
	t.Fatal("no TurnModelSwitch turn in history")
	return schema.Turn{}
}

// TestSetModel_AppendsMarkerTurnOnSuccess verifies (a): a successful switch
// appends a TurnModelSwitch turn reading "Switched model: <old> → <new>",
// with no warning lines when nothing warrants one.
func TestSetModel_AppendsMarkerTurnOnSuccess(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withConfig(SessionConfig{
			ResolveProfile: testResolver,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("anthropic/claude-opus-4-6"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	marker := lastMarkerTurn(t, sess)
	want := "Switched model: openai/gpt-5.4 → anthropic/claude-opus-4-6"
	if got := marker.Message.Text(); got != want {
		t.Fatalf("marker text = %q, want %q", got, want)
	}
}

// TestSetModel_PersistsModelSwitchIdentity exercises the durable semantic record,
// including two transitions whose intermediate model is absent from meta.json.
func TestSetModel_PersistsModelSwitchIdentity(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withConfig(SessionConfig{StateDir: t.TempDir(), ResolveProfile: testResolver, testOnly: testConfig{skipGitSnapshot: true}}),
	)
	for _, model := range []string{"openai/gpt-5.2", "anthropic/claude-opus-4-6"} {
		if err := sess.SetModel(model); err != nil {
			t.Fatal(err)
		}
	}
	data, err := readTranscriptFull(context.Background(), sess.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	var got []schema.ModelSwitchInfo
	for _, entry := range data.Entries {
		if entry.Turn.Kind != schema.TurnModelSwitch {
			continue
		}
		if entry.Turn.ModelSwitch == nil {
			t.Fatal("persisted model switch has no structured identity")
		}
		got = append(got, *entry.Turn.ModelSwitch)
	}
	want := []schema.ModelSwitchInfo{
		{OldProvider: "openai", OldModel: "gpt-5.4", NewProvider: "openai", NewModel: "gpt-5.2"},
		{OldProvider: "openai", OldModel: "gpt-5.2", NewProvider: "anthropic", NewModel: "claude-opus-4-6"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("persisted transitions = %+v, want %+v", got, want)
	}
	if marker := lastMarkerTurn(t, sess); marker.ModelSwitch == nil || *marker.ModelSwitch != want[1] {
		t.Fatalf("live history transition = %+v, want %+v", marker.ModelSwitch, want[1])
	}
}

// TestSetModel_FailedSwitchAppendsNoMarker verifies a rejected switch never
// appends a marker turn.
func TestSetModel_FailedSwitchAppendsNoMarker(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			ResolveProfile: unknownInstanceResolver,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("bogus/some-model"); err == nil {
		t.Fatal("SetModel with unknown instance = nil error, want non-nil")
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnModelSwitch {
			t.Fatalf("marker turn appended after a rejected switch: %+v", turn)
		}
	}
}

// TestExpandHistory_ExcludesModelSwitchMarker verifies (c): the marker turn
// is presentational only and never reaches expandHistory's output.
func TestExpandHistory_ExcludesModelSwitchMarker(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		schema.NewTurn(schema.TurnModelSwitch, llm.System("Switched model: openai/gpt-5.4 → anthropic/claude-opus-4-6")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("hi")),
	}
	out := expandHistory(turns, replayScope{})
	if len(out) != 2 {
		t.Fatalf("expandHistory returned %d messages, want 2 (marker excluded): %+v", len(out), out)
	}
	for _, m := range out {
		if strings.Contains(m.Text(), "Switched model:") {
			t.Fatalf("expandHistory leaked the marker text into a model message: %+v", m)
		}
	}
}

// TestSetModel_MarkerWarnsOnContextWindowShrink verifies (d): when estimated
// context usage exceeds the new profile's compaction threshold, the marker
// carries a warning line.
func TestSetModel_MarkerWarnsOnContextWindowShrink(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "tiny"}), // non-enumerable: fails open
		withConfig(SessionConfig{
			ResolveProfile: tinyWindowResolver,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("tiny/whatever"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	marker := lastMarkerTurn(t, sess)
	text := marker.Message.Text()
	if !strings.HasPrefix(text, "Switched model: openai/gpt-5.4 → openai/whatever") {
		t.Fatalf("marker text = %q, want it to start with the switch line", text)
	}
	if !strings.Contains(text, "compaction threshold") {
		t.Fatalf("marker text = %q, want a context-window-shrink warning line", text)
	}
}

// TestSetModel_MarkerIdentifiesAssumedWindow pins the fast-follow for #4598's
// reviewed LOW: an unknown window budgets against the assumed 128K, so a switch
// above roughly 102K tokens composes the context-warning line against an
// assumption. Left undisclosed, the percentage reads as a fact about the new
// model's real window; the marker must name the assumed window it was
// calculated against.
func TestSetModel_MarkerIdentifiesAssumedWindow(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "gw"}),
		withConfig(SessionConfig{
			ResolveProfile: assumedWindowResolver,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)
	target, err := assumedWindowResolver("gw/deepseek-4.1-flash")
	if err != nil || target == nil {
		t.Fatalf("resolve fixture target: %v", err)
	}
	if target.ContextWindowSize() != 0 {
		t.Fatalf("fixture target window = %d, want 0 (unknown)", target.ContextWindowSize())
	}
	// ~150K tokens by the char/4 estimate ("token " counts ~1.5): past the
	// assumed 128K window's 0.80 checkpoint threshold, so the switch warns.
	sess.history = []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("token ", 100_000)))}

	if err := sess.SetModel("gw/deepseek-4.1-flash"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	marker := lastMarkerTurn(t, sess).Message.Text()
	// A warning line fired, so the marker is more than the bare switch line.
	if strings.Count(marker, "\n") < 1 {
		t.Fatalf("marker = %q, want a context warning line", marker)
	}
	if !strings.Contains(marker, strconv.Itoa(contextmgr.AssumedContextWindow)) {
		t.Fatalf("marker = %q, want the warning to identify the assumed %d-token window its percentage is against", marker, contextmgr.AssumedContextWindow)
	}
}

// TestSetModel_KnownWindowMarkerStaysUnassumed replaces the known-window
// full-copy golden (review of #4633): the old test compared the whole marker
// against hardcoded prose, so a wording edit broke it with no behavior change.
// A switch to a model with a declared window now drives the same marker path
// behaviorally, and each claim is a structured fact around the marker, not its
// prose:
//   - the switch turn's structured identity names both models;
//   - the context metrics the marker budgeted against report the declared
//     4096-token window and Assumed == false, so the disclosure stays off;
//   - estimated usage crossed the 0.80 checkpoint threshold, so the marker
//     carries the warning line with the percentage derived from that estimate;
//   - the marker does not print the assumed token figure the unknown-window
//     branch adds — the one clause that must stay absent for a known window.
//
// The last claim is an absence, so it is mutation-checked: forcing the assumed
// branch in SetModel makes this test fail. The figure is the window the metrics
// report, not hardcoded copy, so a reworded warning still passes.
func TestSetModel_KnownWindowMarkerStaysUnassumed(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "win"}),
		withConfig(SessionConfig{
			ResolveProfile: declaredWindowResolver,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)

	// The turn before the switch must survive it: the marker is appended, not a
	// replacement for history.
	prior := schema.NewTurn(schema.TurnUserInput, llm.User("before the switch"))
	sess.history = []schema.Turn{prior}

	if err := sess.SetModel("win/gpt-5.5"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	marker := lastMarkerTurn(t, sess)
	// The turn's machine identity, not its rendered line, names the models.
	wantIdentity := schema.ModelSwitchInfo{
		OldProvider: "openai", OldModel: "gpt-5.4",
		NewProvider: "openai", NewModel: "gpt-5.5",
	}
	if marker.ModelSwitch == nil || *marker.ModelSwitch != wantIdentity {
		t.Fatalf("marker identity = %v, want %+v", marker.ModelSwitch, wantIdentity)
	}
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if len(history) != 2 {
		t.Fatalf("history after switch = %d turns, want 2 (the prior turn plus the marker): %+v", len(history), history)
	}
	if history[0].Kind != schema.TurnUserInput || history[0].Message.Text() != "before the switch" {
		t.Fatalf("switch did not preserve the pre-switch turn: %+v", history[0])
	}

	// The window the marker budgeted against is the declared one, reported as
	// NOT assumed — measured with the same history and prompt the marker saw
	// (the marker turn itself is recorded after that estimate).
	metrics := sess.contextMgr.EstimateUsage([]schema.Turn{prior}, len(sess.cachedSystemPrompt))
	if metrics.Assumed {
		t.Fatalf("context metrics = %+v, want a declared window, not the unknown-window assumption", metrics)
	}
	if metrics.Window != 4096 {
		t.Fatalf("context metrics window = %d, want the declared 4096", metrics.Window)
	}
	// Estimated usage crossed the threshold, so a warning line fired, with the
	// percentage the estimate produces.
	pressure := float64(metrics.Used) / float64(metrics.Window)
	if pressure < sess.contextMgr.CheckpointThreshold {
		t.Fatalf("estimated pressure %.3f < threshold %.2f; no warning would fire", pressure, sess.contextMgr.CheckpointThreshold)
	}
	text := marker.Message.Text()
	if strings.Count(text, "\n") < 1 {
		t.Fatalf("marker = %q, want a warning line beyond the bare switch line", text)
	}
	if wantPct := strconv.Itoa(int(pressure*100 + 0.5)); !strings.Contains(text, wantPct) {
		t.Fatalf("marker = %q, want the ~%s%% warning percentage the estimate produces", text, wantPct)
	}
	// A known window must not disclose an assumed token figure — the clause the
	// forced-assumed mutation adds. The figure is the window the metrics report,
	// so no prose is pinned.
	if assumedFigure := strconv.Itoa(metrics.Window); strings.Contains(text, assumedFigure) {
		t.Fatalf("marker = %q, want no assumed-window disclosure for a known window (found window figure %s)", text, assumedFigure)
	}
}

// TestSetModel_MarkerWarnsOnDroppedFallbacks verifies (d): a cross-tag switch
// that drops now-invalid cfg.ModelFallbacks entries names them in a warning
// line, consuming Task 2's DroppedModelFallbacksFromLastSwitch.
func TestSetModel_MarkerWarnsOnDroppedFallbacks(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withConfig(SessionConfig{
			ResolveProfile: testResolver,
			ModelFallbacks: []string{"openai/gpt-4.1-mini"},
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("anthropic/claude-opus-4-6"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	dropped := sess.DroppedModelFallbacksFromLastSwitch()
	if len(dropped) != 1 || dropped[0] != "openai/gpt-4.1-mini" {
		t.Fatalf("DroppedModelFallbacksFromLastSwitch() = %v, want [openai/gpt-4.1-mini]", dropped)
	}

	marker := lastMarkerTurn(t, sess)
	text := marker.Message.Text()
	if !strings.Contains(text, "openai/gpt-4.1-mini") {
		t.Fatalf("marker text = %q, want it to name the dropped fallback", text)
	}
}

// TestSetModel_RecomputesKnowledgeCutoffBeforePromptRefresh verifies (e) and
// G15: after a switch, the cached system prompt reflects the NEW model's
// knowledge cutoff, not the launch model's.
func TestSetModel_RecomputesKnowledgeCutoffBeforePromptRefresh(t *testing.T) {
	t.Parallel()
	launch := NewOpenAIProfile("gpt-5.4")
	switched := newAnthropicProfile("claude-opus-4-6")
	launchCutoff, switchedCutoff := launch.KnowledgeCutoff(), switched.KnowledgeCutoff()
	if launchCutoff == "" || switchedCutoff == "" || launchCutoff == switchedCutoff {
		t.Fatalf("fixture models must carry two different cutoffs: %q and %q", launchCutoff, switchedCutoff)
	}
	sess := newSession(t,
		withProfile(launch),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withConfig(SessionConfig{
			ResolveProfile: testResolver,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)

	if got := sess.envInfo.KnowledgeCutoff; got != launchCutoff {
		t.Fatalf("launch KnowledgeCutoff = %q, want %q", got, launchCutoff)
	}
	if !strings.Contains(sess.cachedSystemPrompt, launchCutoff) {
		t.Fatalf("launch cachedSystemPrompt does not mention %s:\n%s", launchCutoff, sess.cachedSystemPrompt)
	}

	if err := sess.SetModel("anthropic/claude-opus-4-6"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	if got := sess.envInfo.KnowledgeCutoff; got != switchedCutoff {
		t.Fatalf("post-switch KnowledgeCutoff = %q, want %q", got, switchedCutoff)
	}
	if !strings.Contains(sess.cachedSystemPrompt, switchedCutoff) {
		t.Fatalf("post-switch cachedSystemPrompt does not mention the new cutoff %s:\n%s", switchedCutoff, sess.cachedSystemPrompt)
	}
	if strings.Contains(sess.cachedSystemPrompt, launchCutoff) {
		t.Fatalf("post-switch cachedSystemPrompt still mentions the launch model's cutoff %s:\n%s", launchCutoff, sess.cachedSystemPrompt)
	}
}
