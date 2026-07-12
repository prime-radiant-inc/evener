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
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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

// lastMarkerTurn returns the most recently appended TurnModelSwitch turn, or
// fails the test if none exists.
func lastMarkerTurn(t *testing.T, sess *Session) schema.Turn {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for i := len(sess.history) - 1; i >= 0; i-- {
		if sess.history[i].Kind == schema.TurnModelSwitch {
			return sess.history[i]
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
			NoProjectPrompts: true,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
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

// TestSetModel_FailedSwitchAppendsNoMarker verifies a rejected switch never
// appends a marker turn.
func TestSetModel_FailedSwitchAppendsNoMarker(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   unknownInstanceResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
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
			NoProjectPrompts: true,
			ResolveProfile:   tinyWindowResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
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
			NoProjectPrompts: true,
			ResolveProfile:   testResolver,
			ModelFallbacks:   []string{"openai/gpt-4.1-mini"},
			testOnly:         testConfig{skipGitSnapshot: true},
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
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")), // knowledge cutoff 2025-06-01
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "anthropic"}), // knowledge cutoff 2025-04-01
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	if got := sess.envInfo.KnowledgeCutoff; got != "2025-06-01" {
		t.Fatalf("launch KnowledgeCutoff = %q, want 2025-06-01", got)
	}
	if !strings.Contains(sess.cachedSystemPrompt, "2025-06-01") {
		t.Fatalf("launch cachedSystemPrompt does not mention 2025-06-01:\n%s", sess.cachedSystemPrompt)
	}

	if err := sess.SetModel("anthropic/claude-opus-4-6"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	if got := sess.envInfo.KnowledgeCutoff; got != "2025-04-01" {
		t.Fatalf("post-switch KnowledgeCutoff = %q, want 2025-04-01", got)
	}
	if !strings.Contains(sess.cachedSystemPrompt, "2025-04-01") {
		t.Fatalf("post-switch cachedSystemPrompt does not mention the new cutoff 2025-04-01:\n%s", sess.cachedSystemPrompt)
	}
	if strings.Contains(sess.cachedSystemPrompt, "2025-06-01") {
		t.Fatalf("post-switch cachedSystemPrompt still mentions the launch model's cutoff 2025-06-01:\n%s", sess.cachedSystemPrompt)
	}
}
