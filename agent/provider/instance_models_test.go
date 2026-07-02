package provider

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func testBool(v bool) *bool { return &v }

func lunarouteModels() map[string]providercfg.ModelConfig {
	return map[string]providercfg.ModelConfig{
		"glm-5.2-nvfp4": {
			ContextWindow:   1_048_576,
			MaxOutputTokens: 131_072,
			Reasoning:       testBool(true),
			ThinkingLevels: map[string]string{
				"minimal": "high", "low": "high", "medium": "high", "high": "high", "xhigh": "max",
			},
		},
		"tiny-chat": {
			ContextWindow: 32_000,
			Reasoning:     testBool(false),
		},
	}
}

func TestNewOpenAICompatProfile_InstanceModelConfig(t *testing.T) {
	p := newOpenAICompatProfile("openai-compatible", "glm-5.2-nvfp4", 0, lunarouteModels())
	if got := p.ContextWindowSize(); got != 1_048_576 {
		t.Errorf("ContextWindowSize = %d, want 1048576 (instance config beats catalog/default)", got)
	}
	wantLevels := []string{"minimal", "low", "medium", "high", "xhigh"}
	if got := p.ReasoningEffortLevels(); !reflect.DeepEqual(got, wantLevels) {
		t.Errorf("ReasoningEffortLevels = %v, want %v (thinking_levels keys, rank order)", got, wantLevels)
	}
	if !p.SupportsReasoning() {
		t.Error("SupportsReasoning = false, want true")
	}
}

func TestNewOpenAICompatProfile_InstanceModelReasoningOff(t *testing.T) {
	p := newOpenAICompatProfile("openai-compatible", "tiny-chat", 0, lunarouteModels())
	if p.SupportsReasoning() {
		t.Error("SupportsReasoning = true, want false (reasoning = false in config)")
	}
	if got := p.ReasoningEffortLevels(); len(got) != 0 {
		t.Errorf("ReasoningEffortLevels = %v, want empty for a non-reasoning model", got)
	}
	if got := p.ContextWindowSize(); got != 32_000 {
		t.Errorf("ContextWindowSize = %d, want 32000", got)
	}
}

func TestNewOpenAICompatProfile_UnknownModelKeepsDefaults(t *testing.T) {
	withModels := newOpenAICompatProfile("openai-compatible", "some-other-model", 0, lunarouteModels())
	without := newOpenAICompatProfile("openai-compatible", "some-other-model", 0, nil)
	if got, want := withModels.ReasoningEffortLevels(), without.ReasoningEffortLevels(); !reflect.DeepEqual(got, want) {
		t.Errorf("levels with unrelated models table = %v, want same as without (%v)", got, want)
	}
	if got, want := withModels.ContextWindowSize(), without.ContextWindowSize(); got != want {
		t.Errorf("context with unrelated models table = %d, want %d", got, want)
	}
}

// A runtime model switch re-resolves the new model against the SAME instance
// model table — and switching back restores the original shape.
func TestWithModel_CarriesInstanceModels(t *testing.T) {
	p := newOpenAICompatProfile("openai-compatible", "glm-5.2-nvfp4", 0, lunarouteModels())
	tiny := p.WithModel("tiny-chat")
	if tiny.SupportsReasoning() {
		t.Error("tiny-chat after WithModel: SupportsReasoning = true, want false")
	}
	if got := tiny.ContextWindowSize(); got != 32_000 {
		t.Errorf("tiny-chat ContextWindowSize = %d, want 32000", got)
	}
	back := tiny.WithModel("glm-5.2-nvfp4")
	wantLevels := []string{"minimal", "low", "medium", "high", "xhigh"}
	if got := back.ReasoningEffortLevels(); !reflect.DeepEqual(got, wantLevels) {
		t.Errorf("switch-back ReasoningEffortLevels = %v, want %v", got, wantLevels)
	}
	if got := back.ContextWindowSize(); got != 1_048_576 {
		t.Errorf("switch-back ContextWindowSize = %d, want 1048576", got)
	}
}

// The task_list tool's effort enum must reflect the configured levels, not the
// provider default — a stale enum teaches the model levels the clamp rejects.
func TestNewOpenAICompatProfile_TaskListEnumMatchesConfiguredLevels(t *testing.T) {
	p := newOpenAICompatProfile("openai-compatible", "glm-5.2-nvfp4", 0, lunarouteModels())
	def := findToolDef(p, "task_list")
	if def == nil {
		t.Fatal("profile has no task_list tool")
	}
	enum := effortEnumFromTaskList(t, *def)
	want := []string{"minimal", "low", "medium", "high", "xhigh"}
	if !reflect.DeepEqual(enum, want) {
		t.Errorf("task_list effort enum = %v, want %v", enum, want)
	}
}

// Live /models enrichment must not clobber explicitly-configured levels or
// context window — user config is authoritative over provider defaults.
func TestWithLiveModelInfo_DoesNotClobberInstanceModelConfig(t *testing.T) {
	p := newOpenAICompatProfile("openai-compatible", "glm-5.2-nvfp4", 0, lunarouteModels())
	live := p.WithLiveModelInfo(llm.ModelInfo{
		ContextWindow:         128_000,
		ReasoningEffortLevels: []string{"low", "medium", "high"},
	})
	wantLevels := []string{"minimal", "low", "medium", "high", "xhigh"}
	if got := live.ReasoningEffortLevels(); !reflect.DeepEqual(got, wantLevels) {
		t.Errorf("live enrichment clobbered configured levels: %v, want %v", got, wantLevels)
	}
	if got := live.ContextWindowSize(); got != 1_048_576 {
		t.Errorf("live enrichment clobbered configured context: %d, want 1048576", got)
	}

	// A model without instance config still takes live info.
	q := newOpenAICompatProfile("openai-compatible", "some-other-model", 0, lunarouteModels())
	qLive := q.WithLiveModelInfo(llm.ModelInfo{
		ContextWindow:         262_144,
		ReasoningEffortLevels: []string{"low", "high"},
	})
	if got := qLive.ContextWindowSize(); got != 262_144 {
		t.Errorf("unconfigured model ignored live context: %d, want 262144", got)
	}
	if got := qLive.ReasoningEffortLevels(); !reflect.DeepEqual(got, []string{"low", "high"}) {
		t.Errorf("unconfigured model ignored live levels: %v", got)
	}
}

func TestResolveProfileFromConfig_PassesInstanceModels(t *testing.T) {
	cfg := providercfg.Config{
		Default: "lunaroute",
		Instances: []providercfg.InstanceConfig{
			{
				Name:     "lunaroute",
				Type:     "openai",
				APIStyle: providercfg.StyleChatCompletions,
				BaseURL:  "https://gw.example.com/v1",
				Models:   lunarouteModels(),
			},
			{
				Name:   "zai",
				Type:   "glm",
				Models: lunarouteModels(),
			},
		},
	}
	for _, ref := range []string{"lunaroute/glm-5.2-nvfp4", "zai/glm-5.2-nvfp4"} {
		p, err := ResolveProfileFromConfig(cfg, ref)
		if err != nil {
			t.Fatalf("ResolveProfileFromConfig(%s): %v", ref, err)
		}
		wantLevels := []string{"minimal", "low", "medium", "high", "xhigh"}
		if got := p.ReasoningEffortLevels(); !reflect.DeepEqual(got, wantLevels) {
			t.Errorf("%s: ReasoningEffortLevels = %v, want %v", ref, got, wantLevels)
		}
		if got := p.ContextWindowSize(); got != 1_048_576 {
			t.Errorf("%s: ContextWindowSize = %d, want 1048576", ref, got)
		}
	}
}

// effortEnumFromTaskList digs the reasoning-effort enum out of the task_list
// tool's JSON-schema parameters.
func effortEnumFromTaskList(t *testing.T, def llm.ToolDefinition) []string {
	t.Helper()
	params := def.Parameters
	var enum []string
	var walk func(v any)
	walk = func(v any) {
		if enum != nil {
			// task_list embeds the enum twice (append and update items); the
			// first hit is enough.
			return
		}
		switch node := v.(type) {
		case map[string]any:
			for k, child := range node {
				if k == "reasoning_effort" {
					if m, ok := child.(map[string]any); ok {
						switch raw := m["enum"].(type) {
						case []any:
							for _, item := range raw {
								if s, ok := item.(string); ok {
									enum = append(enum, s)
								}
							}
							return
						case []string:
							enum = append(enum, raw...)
							return
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(params)
	if enum == nil {
		t.Fatalf("no reasoning_effort enum found in task_list parameters: %v", params)
	}
	return enum
}
