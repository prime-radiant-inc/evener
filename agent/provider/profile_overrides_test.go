package provider

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestWithCommunicateOutputSchema(t *testing.T) {
	t.Run("nil profile", func(t *testing.T) {
		if got := WithCommunicateOutputSchema(nil, map[string]any{"type": "string"}); got != nil {
			t.Errorf("got non-nil for nil profile")
		}
	})

	t.Run("empty schema", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		before, _ := json.Marshal(p.ToolDefinitions())
		q := WithCommunicateOutputSchema(p, map[string]any{})
		after, _ := json.Marshal(q.ToolDefinitions())
		if !reflect.DeepEqual(before, after) {
			t.Error("profile changed with empty schema")
		}
	})

	t.Run("valid schema", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		schema := map[string]any{"type": "string", "description": "custom"}
		q := WithCommunicateOutputSchema(p, schema)
		def := findToolDef(q, "communicate")
		if def == nil {
			t.Fatal("communicate tool missing")
		}
		params := def.Parameters
		props := params["properties"].(map[string]any)
		out := props["output"].(map[string]any)
		if out["type"] != "string" {
			t.Errorf("output.type = %v, want string", out["type"])
		}
		if out["description"] != "custom" {
			t.Errorf("output.description = %v", out["description"])
		}
		req := toStringSlice(params["required"])
		found := false
		for _, r := range req {
			if r == "output" {
				found = true
			}
		}
		if !found {
			t.Error("output not in required")
		}
	})

	t.Run("no communicate tool", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "shell"}}}
		q := WithCommunicateOutputSchema(p, map[string]any{"type": "string"})
		if len(q.toolDefs) != 1 || q.toolDefs[0].Name != "shell" {
			t.Error("profile mutated unexpectedly")
		}
	})

	t.Run("communicate no parameters", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "communicate"}}}
		q := WithCommunicateOutputSchema(p, map[string]any{"type": "string"})
		if q.toolDefs[0].Parameters != nil {
			t.Error("parameters mutated unexpectedly")
		}
	})

	t.Run("communicate no properties", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "communicate", Parameters: map[string]any{"type": "object"}}}}
		q := WithCommunicateOutputSchema(p, map[string]any{"type": "string"})
		if q.toolDefs[0].Parameters["properties"] != nil {
			t.Error("properties mutated unexpectedly")
		}
	})

	t.Run("marshal error in parameters", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "communicate", Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"output": map[string]any{}},
			"bad":        make(chan int),
		}}}}
		q := WithCommunicateOutputSchema(p, map[string]any{"type": "string"})
		if q.toolDefs[0].Parameters["bad"] == nil {
			t.Error("parameters were mutated despite marshal error")
		}
	})

	t.Run("deep copy schema error", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "communicate", Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"output": map[string]any{}},
		}}}}
		q := WithCommunicateOutputSchema(p, map[string]any{"bad": make(chan int)})
		if q.toolDefs[0].Parameters["properties"] == nil {
			t.Error("parameters were mutated despite deep copy error")
		}
	})
}

func TestWithAllowedDecisions(t *testing.T) {
	t.Run("nil profile", func(t *testing.T) {
		if got := WithAllowedDecisions(nil, []string{"a"}); got != nil {
			t.Errorf("got non-nil for nil profile")
		}
	})

	t.Run("empty decisions", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		before, _ := json.Marshal(p.ToolDefinitions())
		q := WithAllowedDecisions(p, []string{})
		after, _ := json.Marshal(q.ToolDefinitions())
		if !reflect.DeepEqual(before, after) {
			t.Error("profile changed with empty decisions")
		}
	})

	t.Run("valid decisions", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		q := WithAllowedDecisions(p, []string{"continue", "stop"})
		def := findToolDef(q, "communicate")
		if def == nil {
			t.Fatal("communicate tool missing")
		}
		params := def.Parameters
		props := params["properties"].(map[string]any)
		out := props["output"].(map[string]any)
		outProps := out["properties"].(map[string]any)
		decision := outProps["decision"].(map[string]any)
		if decision["type"] != "string" {
			t.Errorf("decision.type = %v", decision["type"])
		}
		enum := toStringSlice(decision["enum"])
		if len(enum) != 2 || enum[0] != "continue" || enum[1] != "stop" {
			t.Errorf("decision.enum = %v", enum)
		}
		outReq := toStringSlice(out["required"])
		foundDecision := false
		for _, r := range outReq {
			if r == "decision" {
				foundDecision = true
			}
		}
		if !foundDecision {
			t.Error("decision not in output.required")
		}
		if out["description"] != "Structured output." {
			t.Errorf("output.description = %q", out["description"])
		}
		topReq := toStringSlice(params["required"])
		foundOutput := false
		for _, r := range topReq {
			if r == "output" {
				foundOutput = true
			}
		}
		if !foundOutput {
			t.Error("output not in top-level required")
		}
	})

	t.Run("no communicate tool", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "shell"}}}
		q := WithAllowedDecisions(p, []string{"a"})
		if len(q.toolDefs) != 1 || q.toolDefs[0].Name != "shell" {
			t.Error("profile mutated unexpectedly")
		}
	})

	t.Run("communicate no output property", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "communicate", Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
		}}}}
		q := WithAllowedDecisions(p, []string{"a"})
		props := q.toolDefs[0].Parameters["properties"].(map[string]any)
		if _, ok := props["decision"]; ok {
			t.Error("decision should not be added when output property is missing")
		}
	})

	t.Run("communicate output has no properties", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "communicate", Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"output": map[string]any{"type": "object"}},
		}}}}
		q := WithAllowedDecisions(p, []string{"a"})
		out := q.toolDefs[0].Parameters["properties"].(map[string]any)["output"].(map[string]any)
		if _, ok := out["decision"]; ok {
			t.Error("decision should not be added when output has no properties")
		}
	})

	t.Run("marshal error in parameters", func(t *testing.T) {
		p := &Profile{toolDefs: []llm.ToolDefinition{{Name: "communicate", Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"output": map[string]any{"type": "object", "properties": map[string]any{}}},
			"bad":        make(chan int),
		}}}}
		q := WithAllowedDecisions(p, []string{"a"})
		if q.toolDefs[0].Parameters["bad"] == nil {
			t.Error("parameters were mutated despite marshal error")
		}
	})
}

func TestDecidePrefixAction(t *testing.T) {
	tests := []struct {
		name         string
		behaviorTag  string
		instanceName string
		prefix       string
		want         prefixAction
	}{
		{"openrouter self strip", "openrouter", "openrouter", "openrouter", prefixActionStrip},
		{"openrouter switch ollama", "openrouter", "openrouter", "ollama", prefixActionSwitch},
		{"openrouter switch kimi", "openrouter", "openrouter", "kimi", prefixActionSwitch},
		{"openrouter keep anthropic", "openrouter", "openrouter", "anthropic", prefixActionKeep},
		{"minimax self keep", "minimax", "minimax", "minimax", prefixActionKeep},
		{"minimax other switch", "minimax", "minimax", "openai", prefixActionSwitch},
		{"openai self strip", "openai", "openai", "openai", prefixActionStrip},
		{"openai other switch", "openai", "openai", "anthropic", prefixActionSwitch},
		{"kimi self strip", "kimi", "kimi", "kimi", prefixActionStrip},
		{"kimi other switch", "kimi", "kimi", "anthropic", prefixActionSwitch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decidePrefixAction(tt.behaviorTag, tt.instanceName, tt.prefix)
			if got != tt.want {
				t.Errorf("decidePrefixAction(%q, %q, %q) = %v, want %v", tt.behaviorTag, tt.instanceName, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestCrossProviderRef(t *testing.T) {
	p := NewOpenAIProfile("gpt-4")
	if p.CrossProviderRef("gpt-4") {
		t.Error("bare model should not be cross-provider")
	}
	if !p.CrossProviderRef("anthropic/claude-sonnet") {
		t.Error("different provider prefix should be cross-provider")
	}
	if p.CrossProviderRef("openai/gpt-4") {
		t.Error("self-prefix should not be cross-provider")
	}
}

func TestRebuildOnSameProviderChange(t *testing.T) {
	tests := []struct {
		tag  string
		want bool
	}{
		{"kimi", true},
		{"glm", true},
		{"openrouter", true},
		{"ollama", true},
		{"openrouter-anthropic", true},
		{"openai", false},
		{"anthropic", false},
		{"minimax", false},
		{"google", false},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			if got := rebuildOnSameProviderChange(tt.tag); got != tt.want {
				t.Errorf("rebuildOnSameProviderChange(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}

func TestProfileGetters(t *testing.T) {
	p := NewOpenAIProfile("gpt-4")
	if p.ToolNameMap() == nil {
		t.Error("ToolNameMap should not be nil for openai")
	}
	if !p.SupportsParallelToolCalls() {
		t.Error("SupportsParallelToolCalls should be true")
	}
	if len(p.ProjectDocFiles()) == 0 {
		t.Error("ProjectDocFiles should not be empty")
	}
	if !p.SupportsReasoning() {
		t.Error("SupportsReasoning should be true")
	}
	if !p.SupportsStreaming() {
		t.Error("SupportsStreaming should be true")
	}
	if !p.SupportsWebSearch() {
		t.Error("SupportsWebSearch should be true")
	}
	if p.DefaultCommandTimeoutMS() != 120000 {
		t.Errorf("DefaultCommandTimeoutMS = %d", p.DefaultCommandTimeoutMS())
	}
	if p.KnowledgeCutoff() != "2025-06-01" {
		t.Errorf("KnowledgeCutoff = %q", p.KnowledgeCutoff())
	}

	// CheapModel fallback for openai behaviorTag
	if got := p.CheapModel(); got != "gpt-4.1-nano" {
		t.Errorf("CheapModel fallback = %q, want gpt-4.1-nano", got)
	}

	// ConfiguredCheapModel with empty cheapModel
	if got := (&Profile{}).ConfiguredCheapModel(); got != "" {
		t.Errorf("ConfiguredCheapModel empty = %q", got)
	}

	// CheapProvider with nil profile
	var nilProfile *Profile
	if got := nilProfile.CheapProvider(); got != "" {
		t.Errorf("CheapProvider nil = %q", got)
	}

	// CheapModelRefString with nil profile
	if got := nilProfile.CheapModelRefString(); got != "" {
		t.Errorf("CheapModelRefString nil = %q", got)
	}
}

func TestWithProviderID(t *testing.T) {
	t.Run("nil profile", func(t *testing.T) {
		if got := WithProviderID(nil, "x"); got != nil {
			t.Errorf("got non-nil for nil profile")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		q := WithProviderID(p, "")
		if q.ID() != p.ID() {
			t.Errorf("ID changed from %q to %q", p.ID(), q.ID())
		}
	})

	t.Run("valid name", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		q := WithProviderID(p, "custom-id")
		if q.ID() != "custom-id" {
			t.Errorf("ID = %q, want custom-id", q.ID())
		}
		if q.BehaviorTag() != p.BehaviorTag() {
			t.Errorf("BehaviorTag changed")
		}
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		q := WithProviderID(p, "  spaced  ")
		if q.ID() != "spaced" {
			t.Errorf("ID = %q, want spaced", q.ID())
		}
	})
}

func TestWithCheapModel(t *testing.T) {
	t.Run("nil profile", func(t *testing.T) {
		if got := WithCheapModel(nil, "x"); got != nil {
			t.Errorf("got non-nil for nil profile")
		}
	})

	t.Run("empty ref", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		q := WithCheapModel(p, "")
		if q.ConfiguredCheapModel() != p.ConfiguredCheapModel() {
			t.Error("cheap model changed")
		}
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		q := WithCheapModel(p, "  model  ")
		if q.ConfiguredCheapModel() != "model" {
			t.Errorf("CheapModel = %q, want model", q.ConfiguredCheapModel())
		}
	})
}

func TestWithContextWindow(t *testing.T) {
	t.Run("nil profile", func(t *testing.T) {
		if got := WithContextWindow(nil, 100); got != nil {
			t.Errorf("got non-nil for nil profile")
		}
	})

	t.Run("zero or negative", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		q := WithContextWindow(p, 0)
		if q.ContextWindowSize() != p.ContextWindowSize() {
			t.Error("context window changed for n=0")
		}
		r := WithContextWindow(p, -5)
		if r.ContextWindowSize() != p.ContextWindowSize() {
			t.Error("context window changed for n=-5")
		}
	})

	t.Run("valid window", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-4")
		q := WithContextWindow(p, 256000)
		if q.ContextWindowSize() != 256000 {
			t.Errorf("ContextWindowSize = %d, want 256000", q.ContextWindowSize())
		}
	})
}

func TestDeepCopyJSONMap(t *testing.T) {
	t.Run("valid map", func(t *testing.T) {
		orig := map[string]any{
			"a":      "b",
			"nested": map[string]any{"c": 1},
		}
		cp, err := deepCopyJSONMap(orig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// JSON round-trip converts ints to float64, so compare via JSON
		origJSON, _ := json.Marshal(orig)
		cpJSON, _ := json.Marshal(cp)
		if !reflect.DeepEqual(origJSON, cpJSON) {
			t.Errorf("copy not equal to original after JSON round-trip")
		}
		// Mutate copy and verify original is unaffected
		cp["a"] = "changed"
		if orig["a"] != "b" {
			t.Error("original was mutated")
		}
		nested := cp["nested"].(map[string]any)
		nested["c"] = 2
		origNested := orig["nested"].(map[string]any)
		if origNested["c"] != 1 {
			t.Error("original nested was mutated")
		}
	})

	t.Run("unmarshalable value", func(t *testing.T) {
		bad := map[string]any{"key": make(chan int)}
		_, err := deepCopyJSONMap(bad)
		if err == nil {
			t.Fatal("expected error for unmarshalable value")
		}
	})
}

func TestToStringSlice(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"int", 42, nil},
		{"[]string", []string{"a", "b"}, []string{"a", "b"}},
		{"[]any strings", []any{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"[]any mixed", []any{"a", 42, "b", nil}, []string{"a", "b"}},
		{"[]any empty", []any{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toStringSlice(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("toStringSlice(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
