package provider

import "testing"

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
