package agent

// Tests for Task 4: SetModel and SetReasoningEffort emit session change
// events after the state change commits (agent/events/events.go
// EventModelChanged, EventReasoningEffortChanged).

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
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
			testOnly: testConfig{skipGitSnapshot: true},
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
			ResolveProfile: unknownInstanceResolver,
			testOnly:       testConfig{skipGitSnapshot: true},
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

// TestSetModelWithMutationID_RefusedAtRestLeavesADurableOutcome pins that a
// pick refused on the at-rest path records its rejection in snapshot state. A
// pick is admitted as pending before it is validated, so a refusal that only
// returned an error would leave a reconnecting client displaying a pending pick
// the session had already refused.
func TestSetModelWithMutationID_RefusedAtRestLeavesADurableOutcome(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			ResolveProfile: unknownInstanceResolver,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModelWithMutationID("bogus/some-model", "cm-refused-at-rest"); err == nil {
		t.Fatal("expected error from unknown instance ref")
	}

	state, ok := sess.ModelPickState()
	if !ok {
		t.Fatal("no model pick recorded after a refused pick; a reconnecting client would show it pending forever")
	}
	if state.Outcome != appwire.ModelPickRejected {
		t.Fatalf("outcome = %q, want %q: a refused pick must not stay pending", state.Outcome, appwire.ModelPickRejected)
	}
	if state.ClientMutationID != "cm-refused-at-rest" {
		t.Fatalf("clientMutationId = %q, want %q", state.ClientMutationID, "cm-refused-at-rest")
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
			testOnly: testConfig{skipGitSnapshot: true},
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
