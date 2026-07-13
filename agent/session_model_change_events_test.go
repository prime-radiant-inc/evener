package agent

// Tests for Task 4: SetModel and SetReasoningEffort emit session change
// events after the state change commits (agent/events/events.go
// EventModelChanged, EventReasoningEffortChanged).

import (
	"testing"

	"primeradiant.com/serf/agent/events"
)

// TestSetModel_EmitsModelChangedEvent verifies that a successful SetModel
// switch emits EventModelChanged carrying old + new provider/model and the
// NEW profile's ReasoningEffortLevels()/SupportsReasoning().
func TestSetModel_EmitsModelChangedEvent(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("gpt-4.1-mini"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	var found *events.ModelChangedData
drain:
	for {
		select {
		case ev := <-sess.Events():
			if d, ok := ev.Data.(events.ModelChangedData); ok {
				found = &d
			}
		default:
			break drain
		}
	}

	if found == nil {
		t.Fatal("no EventModelChanged emitted")
	}
	if found.OldProvider != "openai" || found.OldModel != "gpt-5.4" {
		t.Fatalf("old provider/model = %s/%s, want openai/gpt-5.4", found.OldProvider, found.OldModel)
	}
	if found.NewProvider != "openai" || found.NewModel != "gpt-4.1-mini" {
		t.Fatalf("new provider/model = %s/%s, want openai/gpt-4.1-mini", found.NewProvider, found.NewModel)
	}
	newProfile := sess.currentProfile()
	if found.SupportsReasoning != newProfile.SupportsReasoning() {
		t.Fatalf("SupportsReasoning = %v, want %v", found.SupportsReasoning, newProfile.SupportsReasoning())
	}
	wantLevels := newProfile.ReasoningEffortLevels()
	if len(found.ReasoningEffortLevels) != len(wantLevels) {
		t.Fatalf("ReasoningEffortLevels = %v, want %v", found.ReasoningEffortLevels, wantLevels)
	}
}

// TestSetModel_FailedSwitch_DoesNotEmitModelChangedEvent verifies that a
// rejected SetModel call (unknown instance) emits no EventModelChanged.
func TestSetModel_FailedSwitch_DoesNotEmitModelChangedEvent(t *testing.T) {
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
		t.Fatal("expected error from unknown instance ref")
	}

	select {
	case ev := <-sess.Events():
		if _, ok := ev.Data.(events.ModelChangedData); ok {
			t.Fatal("EventModelChanged emitted for a failed SetModel")
		}
	default:
	}
}

// TestSetReasoningEffort_EmitsReasoningEffortChangedEvent verifies that
// SetReasoningEffort emits EventReasoningEffortChanged carrying the
// normalized effort value.
func TestSetReasoningEffort_EmitsReasoningEffortChangedEvent(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	sess.SetReasoningEffort("high")

	var found *events.ReasoningEffortChangedData
drain:
	for {
		select {
		case ev := <-sess.Events():
			if d, ok := ev.Data.(events.ReasoningEffortChangedData); ok {
				found = &d
			}
		default:
			break drain
		}
	}

	if found == nil {
		t.Fatal("no EventReasoningEffortChanged emitted")
	}
	if found.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want %q", found.ReasoningEffort, "high")
	}
}
