package agent

import (
	"strings"
	"testing"
)

func TestFormatPluginAgentsPrompt_NoAgents(t *testing.T) {
	result := FormatPluginAgentsPrompt(nil)
	if result != "" {
		t.Errorf("expected empty string for nil, got %q", result)
	}
	result = FormatPluginAgentsPrompt(map[string]PluginAgent{})
	if result != "" {
		t.Errorf("expected empty string for empty map, got %q", result)
	}
}

func TestFormatPluginAgentsPrompt_WithAgents(t *testing.T) {
	agents := map[string]PluginAgent{
		"my-plugin:reviewer": {Name: "reviewer", Description: "Reviews code for quality", PluginName: "my-plugin"},
		"my-plugin:tester":   {Name: "tester", Description: "Generates test cases", PluginName: "my-plugin"},
	}
	result := FormatPluginAgentsPrompt(agents)
	if !strings.Contains(result, "my-plugin:reviewer") {
		t.Error("should contain agent name 'my-plugin:reviewer'")
	}
	if !strings.Contains(result, "Reviews code for quality") {
		t.Error("should contain agent description")
	}
	if !strings.Contains(result, "my-plugin:tester") {
		t.Error("should contain second agent name")
	}
	if !strings.Contains(result, "Generates test cases") {
		t.Error("should contain second agent description")
	}
	if !strings.Contains(result, "available_agents") {
		t.Error("should have available_agents XML tag")
	}
	if !strings.Contains(result, "spawn_agent") {
		t.Error("should mention spawn_agent usage")
	}
}

func TestFormatPluginAgentsPrompt_Sorted(t *testing.T) {
	agents := map[string]PluginAgent{
		"z-plugin:agent": {Name: "agent", Description: "Z agent", PluginName: "z-plugin"},
		"a-plugin:agent": {Name: "agent", Description: "A agent", PluginName: "a-plugin"},
	}
	result := FormatPluginAgentsPrompt(agents)
	aIdx := strings.Index(result, "a-plugin:agent")
	zIdx := strings.Index(result, "z-plugin:agent")
	if aIdx < 0 || zIdx < 0 {
		t.Fatalf("expected both agents in output, got: %s", result)
	}
	if aIdx >= zIdx {
		t.Errorf("agents should be sorted alphabetically: a at %d, z at %d", aIdx, zIdx)
	}
}
