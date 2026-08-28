package llm

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func resolved(protocol string, caps registry.Caps) registry.Resolved {
	if caps.Fields == nil {
		caps.Fields = registry.Baseline(protocol)
	}
	return registry.Resolved{Protocol: protocol, Caps: caps}
}

func TestShapeRequest_ReasoningOffAndClamp(t *testing.T) {
	req := Request{ReasoningEffort: new("high")}
	got := ShapeRequest(req, resolved(registry.ProtocolOpenAIChat, registry.Caps{Reasoning: new(false)}))
	if got.ReasoningEffort != nil {
		t.Fatal("reasoning = false must clear the effort")
	}
	got = ShapeRequest(req, resolved(registry.ProtocolOpenAIChat, registry.Caps{EffortValues: []string{"low", "medium"}}))
	if got.ReasoningEffort == nil || *got.ReasoningEffort != "medium" {
		t.Fatalf("effort must clamp to the ladder, got %v", got.ReasoningEffort)
	}
	got = ShapeRequest(req, resolved(registry.ProtocolOpenAIChat, registry.Caps{}))
	if got.ReasoningEffort == nil || *got.ReasoningEffort != "high" {
		t.Fatal("an empty ladder passes the effort through")
	}
	got = ShapeRequest(Request{}, resolved(registry.ProtocolAnthropic, registry.Caps{ThinkingAlwaysOn: new(true), EffortValues: []string{"low", "high"}}))
	if got.ReasoningEffort != nil {
		t.Fatal("ShapeRequest never adds an effort the caller did not set")
	}
}

func TestShapeRequest_MaxTokensAndSampling(t *testing.T) {
	req := Request{Temperature: new(0.2), TopP: new(0.9), StopSequences: []string{"a", "b"}}
	got := ShapeRequest(req, resolved(registry.ProtocolOpenAIChat, registry.Caps{MaxOutputTokens: new(4096)}))
	if got.MaxTokens == nil || *got.MaxTokens != 4096 || got.Temperature == nil || got.TopP == nil || len(got.StopSequences) != 2 {
		t.Fatalf("defaults: %+v", got)
	}
	req.MaxTokens = new(10)
	if got := ShapeRequest(req, resolved(registry.ProtocolOpenAIChat, registry.Caps{MaxOutputTokens: new(4096)})); *got.MaxTokens != 10 {
		t.Fatal("a caller's max tokens is kept")
	}
	got = ShapeRequest(req, resolved(registry.ProtocolOpenAIChat, registry.Caps{Sampling: new(false)}))
	if got.Temperature != nil || got.TopP != nil {
		t.Fatal("sampling = false drops temperature and top_p")
	}
	fields := registry.Baseline(registry.ProtocolOpenAIChat)
	fields["temperature"], fields["stop"] = false, false
	got = ShapeRequest(req, resolved(registry.ProtocolOpenAIChat, registry.Caps{Fields: fields}))
	if got.Temperature != nil || got.TopP == nil || got.StopSequences != nil {
		t.Fatalf("fields gate the request-level values: %+v", got)
	}
	gfields := registry.Baseline(registry.ProtocolGoogle)
	gfields["generationConfig.topP"] = false
	got = ShapeRequest(req, resolved(registry.ProtocolGoogle, registry.Caps{Fields: gfields}))
	if got.TopP != nil || got.Temperature == nil || len(got.StopSequences) != 2 {
		t.Fatalf("google paths: %+v", got)
	}
	got = ShapeRequest(req, resolved(registry.ProtocolOpenAIResponses, registry.Caps{}))
	if got.StopSequences != nil {
		t.Fatal("the Responses API has no stop parameter")
	}
	got = ShapeRequest(req, resolved(registry.ProtocolAnthropic, registry.Caps{MaxStopSequences: new(1)}))
	if !reflect.DeepEqual(got.StopSequences, []string{"a"}) {
		t.Fatalf("max_stop_sequences truncates: %v", got.StopSequences)
	}
}

func TestShapeRequest_PromptCacheGates(t *testing.T) {
	req := Request{PromptCacheKey: "k", PromptCacheRetention: "24h"}
	fields := registry.Baseline(registry.ProtocolOpenAIResponses)
	fields["prompt_cache_key"] = true
	got := ShapeRequest(req, resolved(registry.ProtocolOpenAIResponses, registry.Caps{Fields: fields}))
	if got.PromptCacheKey != "k" || got.PromptCacheRetention != "" {
		t.Fatalf("two independent gates: %+v", got)
	}
}

func TestShapeRequest_DoesNotMutateInput(t *testing.T) {
	req := Request{Temperature: new(0.2), ReasoningEffort: new("max")}
	_ = ShapeRequest(req, resolved(registry.ProtocolOpenAIChat, registry.Caps{Sampling: new(false), EffortValues: []string{"low"}}))
	if *req.Temperature != 0.2 || *req.ReasoningEffort != "max" {
		t.Fatal("ShapeRequest must not write through the caller's pointers")
	}
}

// registry validates default_effort against its own copy of the effort
// vocabulary, because llm imports registry and cannot be imported back. This
// is the seam that keeps the two lists honest: every level a session accepts
// must parse as a default, and a value the session would reject must not.
func TestDefaultEffortVocabularyMatchesTheSessionVocabulary(t *testing.T) {
	for _, level := range append(ReasoningEffortVocabulary(), ReasoningEffortNone) {
		if err := ValidateReasoningEffort(level); err != nil {
			t.Fatalf("fixture: %q is not a session level: %v", level, err)
		}
		if _, err := registry.ParseConfig([]byte("[providers.x]\ndefault_effort = \"" + level + "\"\n")); err != nil {
			t.Errorf("default_effort = %q: %v", level, err)
		}
	}
	// The disable aliases normalize to "none" before they reach a session, so
	// they are not levels; nor is a retired or misspelled tier.
	for _, bad := range []string{"off", "null", "false", "0", "ultra", "High", ""} {
		if _, err := registry.ParseConfig([]byte("[providers.x]\ndefault_effort = \"" + bad + "\"\n")); err == nil {
			t.Errorf("default_effort = %q parsed, want a refusal", bad)
		}
	}
}
