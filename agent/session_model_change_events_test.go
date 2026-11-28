package agent

// Tests for Task 4: SetModel and SetReasoningEffort emit session change
// events after the state change commits (agent/events/events.go
// EventModelChanged, EventReasoningEffortChanged).

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm/registry"
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

	if err := sess.SetModel("", "gpt-4.1-mini"); err != nil {
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

// TestSetModel_ModelChangedEventCarriesNewDefaultEffort verifies that the
// switch event names the effort an unset session runs at on the NEW model, so
// a client's "(default)" readout follows the switch instead of naming the
// previous model's default.
func TestSetModel_ModelChangedEventCarriesNewDefaultEffort(t *testing.T) {
	t.Parallel()
	instance := lunarouteInstance(map[string]registry.Model{
		"plain-model":   {Caps: registry.Caps{Reasoning: new(true), EffortValues: []string{"low", "medium", "high"}}},
		"stated-model":  {Caps: registry.Caps{Reasoning: new(true), EffortValues: []string{"low", "medium", "high", "max"}, DefaultEffort: new("max")}},
		"thinker-model": {Caps: registry.Caps{Reasoning: new(true), EffortValues: []string{"high", "max"}}},
	})
	cases := []struct {
		model string
		want  string
	}{
		{model: "stated-model", want: "max"},
		{model: "thinker-model", want: "high"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			sess := newSession(t,
				withProfile(resolveTestProfile("lunaroute", instance, "plain-model")),
				withAdapter(&fakeAdapter{name: "lunaroute"}),
				withConfig(SessionConfig{
					testOnly: testConfig{skipGitSnapshot: true},
				}),
			)
			if err := sess.SetModel("", tc.model); err != nil {
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
			if found.NewModel != tc.model {
				t.Fatalf("NewModel = %q, want %q", found.NewModel, tc.model)
			}
			if found.DefaultReasoningEffort != tc.want {
				t.Fatalf("DefaultReasoningEffort = %q, want %q: the new model's unset effort", found.DefaultReasoningEffort, tc.want)
			}
		})
	}
}

// TestSetModel_FailedSwitch_DoesNotEmitModelChangedEvent verifies that a
// rejected SetModel call (unknown instance) emits no EventModelChanged and no
// EventNextModelChanged: a pick that never passed its own validation was
// never accepted, so no publication may claim it.
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

	if err := sess.SetModel("", "bogus/some-model"); err == nil {
		t.Fatal("expected error from unknown instance ref")
	}

	// Drain every buffered event, not just the first: the channel's first
	// event is the session start, so a single read would pass even if the
	// rejected pick had announced itself behind it.
	for {
		select {
		case ev := <-sess.Events():
			if _, ok := ev.Data.(events.ModelChangedData); ok {
				t.Fatal("EventModelChanged emitted for a failed SetModel")
			}
			if _, ok := ev.Data.(events.NextModelChangedData); ok {
				t.Fatal("EventNextModelChanged emitted for a failed SetModel")
			}
		default:
			return
		}
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
