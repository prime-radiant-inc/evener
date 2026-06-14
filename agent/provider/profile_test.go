package provider

import (
	"bytes"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func findToolDef(p *Profile, name string) *llm.ToolDefinition {
	for _, td := range p.ToolDefinitions() {
		if td.Name == name {
			d := td
			return &d
		}
	}
	return nil
}

// WithLiveModelInfo updates effortLevels from live provider metadata; the
// task_list tool's reasoning_effort enum must be kept in sync, or the model
// sees the constructor enum (e.g. OpenAI's low/medium/high/xhigh) instead of the
// live model's levels (e.g. Kimi's minimal/low/medium/high).
func TestProfile_WithLiveModelInfoSyncsEffortToolSchema(t *testing.T) {
	p := newAnthropicProfile("claude-opus-4-6")
	if findToolDef(p, "task_list") == nil {
		t.Skip("profile has no task_list tool")
	}
	q := p.WithLiveModelInfo(llm.ModelInfo{ReasoningEffortLevels: []string{"minimal", "low", "medium", "high"}})

	got := findToolDef(q, "task_list")
	want := tool.DefTaskList([]string{"minimal", "low", "medium", "high"})
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if !bytes.Equal(gb, wb) {
		t.Errorf("task_list effort schema not synced to live levels after WithLiveModelInfo\n got=%s\nwant=%s", gb, wb)
	}
}

// After an Anthropic model switch, WithModel must rebuild — not shallow-clone —
// so every model-derived schema (the task_list reasoning_effort enum, context,
// provider options) matches the new model. The switched profile's tool defs must
// equal a freshly constructed profile for the target model.
func TestAnthropicProfile_WithModelRebuildsToolSchemas(t *testing.T) {
	switched := newAnthropicProfile("claude-opus-4-6").WithModel("claude-opus-4-5-20251101")
	fresh := newAnthropicProfile("claude-opus-4-5-20251101")

	sb, _ := json.Marshal(switched.ToolDefinitions())
	fb, _ := json.Marshal(fresh.ToolDefinitions())
	if !bytes.Equal(sb, fb) {
		t.Errorf("WithModel(opus-4-5) tool defs differ from a fresh opus-4-5 profile (stale effort schema after switch)")
	}
	if switched.ContextWindowSize() != fresh.ContextWindowSize() {
		t.Errorf("context window after switch = %d, want %d", switched.ContextWindowSize(), fresh.ContextWindowSize())
	}
}

// A qualified, dated, "[1m]" openrouter-anthropic ref must report the 1M context
// window — the GetModelInfo-based resolver can't see the suffix.
func TestOpenRouterAnthropicProfile_OneMillionContext(t *testing.T) {
	p := newOpenRouterAnthropicProfile("anthropic/claude-opus-4-5-20251101[1m]")
	if p.ContextWindowSize() != 1_000_000 {
		t.Errorf("openrouter-anthropic [1m] context = %d, want 1000000", p.ContextWindowSize())
	}
	if got := p.ReasoningEffortLevels(); len(got) != 3 {
		t.Errorf("effort levels = %v, want 3 (opus-4-5 family)", got)
	}
}

// A model carrying the Anthropic "[1m]" 1M-context suffix must resolve its
// family's effort levels from the catalog (the key omits the suffix), not fall
// back to the full default set — otherwise the profile would report "max" as
// supported for a model that tops out at "high".
func TestAnthropicProfile_EffortLevelsForOneMillionSuffix(t *testing.T) {
	p := newAnthropicProfile("claude-opus-4-5[1m]")
	if got := p.ReasoningEffortLevels(); len(got) != 3 {
		t.Fatalf("ReasoningEffortLevels() = %v, want 3 (opus-4-5 family, 1M suffix stripped)", got)
	}
}

// WithModel on the Anthropic path must re-resolve effort levels for the new
// model. Switching a running session from a max-capable model (opus-4-6) to one
// capped at high (opus-4-5) must not leave the stale max-capable level set, or
// buildModelRequest would treat "max" as supported on the new model.
func TestAnthropicProfile_WithModelReResolvesEffortLevels(t *testing.T) {
	p := newAnthropicProfile("claude-opus-4-6")
	if got := p.ReasoningEffortLevels(); len(got) != 4 {
		t.Fatalf("opus-4-6 levels = %v, want 4 (low,medium,high,max)", got)
	}
	q := p.WithModel("claude-opus-4-5-20251101")
	if got := q.ReasoningEffortLevels(); len(got) != 3 {
		t.Fatalf("WithModel(opus-4-5) levels = %v, want 3 (re-resolved, not the stale opus-4-6 set)", got)
	}
}

// A rebuild-based WithModel must carry the cheap-model routing (set via
// WithCheapModel) across the model switch; the constructor resets it, so a model
// change would otherwise drop configured side-call routing.
func TestAnthropicProfile_WithModelPreservesCheapModel(t *testing.T) {
	p := WithCheapModel(newAnthropicProfile("claude-opus-4-6"), "kimi/kimi-for-coding")
	if p.CheapModelRefString() != "kimi/kimi-for-coding" {
		t.Fatalf("setup: cheap model = %q", p.CheapModelRefString())
	}
	q := p.WithModel("claude-opus-4-5-20251101")
	if got := q.CheapModelRefString(); got != "kimi/kimi-for-coding" {
		t.Errorf("cheap model lost after WithModel: got %q, want kimi/kimi-for-coding", got)
	}
}

// An openrouter-anthropic "[1m]" ref must request the 1M-context beta header, not
// just claim the 1M window — otherwise Serf budgets 1M while the API serves 200K.
func TestOpenRouterAnthropicProfile_OneMillionBetaHeader(t *testing.T) {
	p := newOpenRouterAnthropicProfile("anthropic/claude-opus-4-5-20251101[1m]")
	opts, _ := p.ProviderOptions()["anthropic"].(map[string]any)
	if opts == nil {
		t.Fatalf("no anthropic provider options: %#v", p.ProviderOptions())
	}
	if opts["beta_headers"] != anthropicBeta1M {
		t.Errorf("beta_headers = %v, want %q for a [1m] ref", opts["beta_headers"], anthropicBeta1M)
	}
}

func TestJobControlCapabilityIncludesDelegateAndSendMessage(t *testing.T) {
	defs := toolDefinitionsForCapabilities([]toolCapability{capabilityJobControl}, nil)
	have := map[string]bool{}
	for _, d := range defs {
		have[d.Name] = true
	}
	for _, name := range []string{"delegate", "job_watch", "job_send_message", "job_read_output", "job_list", "job_stop"} {
		if !have[name] {
			t.Errorf("capabilityJobControl missing %q", name)
		}
	}
}

func TestStandardProfilesAdvertiseJobControlWithoutLegacyAgentControl(t *testing.T) {
	profiles := map[string][]toolCapability{
		"openai":    openAICodexCapabilities,
		"anthropic": anthropicStyleCapabilities,
		"gemini":    geminiStyleCapabilities,
	}

	for profile, capabilities := range profiles {
		t.Run(profile, func(t *testing.T) {
			defs := toolDefinitionsForCapabilities(capabilities, nil)
			have := map[string]bool{}
			for _, d := range defs {
				have[d.Name] = true
			}

			for _, name := range []string{"delegate", "job_watch", "job_send_message", "job_read_output", "job_list", "job_stop"} {
				if !have[name] {
					t.Errorf("profile missing job-control tool %q", name)
				}
			}
		})
	}
}
