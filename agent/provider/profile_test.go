package provider

import "testing"

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
			for _, name := range []string{"spawn_agent", "resume_agent", "wait", "close_agent"} {
				if have[name] {
					t.Errorf("profile still advertises legacy agent-control tool %q", name)
				}
			}
		})
	}
}
