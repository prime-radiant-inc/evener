package agent

// Tests for Task 4: SetModelControl and SetReasoningEffort emit session change
// events after the state change commits (agent/events/events.go
// EventModelChanged, EventReasoningEffortChanged).

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm/registry"
)

// TestSetModel_EmitsModelChangedEvent verifies that a successful SetModelControl
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

	if err := setModel(sess, "gpt-4.1-mini"); err != nil {
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
			if err := setModel(sess, tc.model); err != nil {
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
// rejected SetModelControl call (unknown instance) emits no EventModelChanged.
//
// TestSetModel_ReselectAfterDefaultTierRefreshStillEmitsModelChangedEvent pins
// the reselect path against a roster refresh that moves only the default
// reasoning tier. A Codex listing can keep a model's id, its effort ladder and
// its context window while changing the effort the model says it runs at when
// nothing is asked. Reselecting installs that refreshed profile, and the switch
// event is the only frame that carries the default tier, so a same-model check
// that ignored it left every client displaying the tier the model no longer
// defaults to.
func TestSetModel_ReselectAfterDefaultTierRefreshStillEmitsModelChangedEvent(t *testing.T) {
	instances := map[string]registry.Provider{"work": {
		Base: "openai", APIKey: "test",
		Models: map[string]registry.Model{"roster-model": {Caps: registry.Caps{
			Reasoning:     new(true),
			EffortValues:  []string{"low", "medium", "high"},
			ContextWindow: new(200000),
		}}},
	}}
	client := registryClient(t, instances)
	// The roster before the refresh: the tier the model states it runs at.
	client.Registry().ApplyLive("work", []registry.Model{{ID: "roster-model", Caps: registry.Caps{
		Reasoning:     new(true),
		EffortValues:  []string{"low", "medium", "high"},
		ContextWindow: new(200000),
		DefaultEffort: new("low"),
	}}})
	sess := newSession(t,
		withClient(client),
		withProfile(resolveClientProfile(t, client, "work/roster-model")),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)

	if got := profileDefaultReasoningEffort(sess.currentProfile()); got != "low" {
		t.Fatalf("setup: session launched on default tier %q, want %q", got, "low")
	}
	// The roster refresh a Codex listing performs: same id, same ladder, same
	// window, only the tier an unset session runs at has moved.
	client.Registry().ApplyLive("work", []registry.Model{{ID: "roster-model", Caps: registry.Caps{
		Reasoning:     new(true),
		EffortValues:  []string{"low", "medium", "high"},
		ContextWindow: new(200000),
		DefaultEffort: new("high"),
	}}})
	if err := setModel(sess, "work/roster-model"); err != nil {
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
		t.Fatal("no EventModelChanged emitted for a reselect that changed the model's default reasoning tier")
	}
	if found.DefaultReasoningEffort != "high" {
		t.Fatalf("DefaultReasoningEffort = %q, want %q: the refreshed tier", found.DefaultReasoningEffort, "high")
	}
}

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

	if err := setModel(sess, "bogus/some-model"); err == nil {
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
