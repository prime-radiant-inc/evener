package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/server"
)

func TestRenderDetailedStatus_BasicFields(t *testing.T) {
	info := server.StatusInfo{
		SessionID:       "abc123",
		Model:           "gpt-5",
		Profile:         "openai",
		Turns:           12,
		ContextPressure: 0.42,
	}
	out := renderDetailedStatus(info, 80)

	for _, want := range []string{"Session:  abc123", "Model:    gpt-5 (openai)", "Turns:    12", "Context:  42% used"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRenderDetailedStatus_ToolCategories(t *testing.T) {
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed: &server.DetailedStatus{
			Tools: []server.ToolInfo{
				{Name: "shell", Source: "core"},
				{Name: "read_file", Source: "core"},
				{Name: "linear__search", Source: "mcp:streamlinear"},
				{Name: "my_tool", Source: "custom"},
			},
		},
	}
	out := renderDetailedStatus(info, 80)

	if !strings.Contains(out, "Tools (4):") {
		t.Errorf("missing tools header in:\n%s", out)
	}
	if !strings.Contains(out, "Core: shell, read_file") {
		t.Errorf("missing core tools in:\n%s", out)
	}
	if !strings.Contains(out, "MCP [streamlinear]:") || !strings.Contains(out, "linear__search") {
		t.Errorf("missing MCP tools in:\n%s", out)
	}
	if !strings.Contains(out, "Custom: my_tool") {
		t.Errorf("missing custom tools in:\n%s", out)
	}
}

func TestRenderDetailedStatus_Plugins(t *testing.T) {
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed: &server.DetailedStatus{
			Plugins: []server.PluginStatusInfo{
				{Name: "superpowers", Version: "4.3.0", SkillCount: 8, AgentCount: 2, HookCount: 12},
			},
		},
	}
	out := renderDetailedStatus(info, 80)

	if !strings.Contains(out, "Plugins (1):") {
		t.Errorf("missing plugins header in:\n%s", out)
	}
	if !strings.Contains(out, "superpowers v4.3.0 (8 skills, 2 agents, 12 hooks)") {
		t.Errorf("missing plugin details in:\n%s", out)
	}
}

func TestRenderDetailedStatus_ShowsEmptySections(t *testing.T) {
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed:  &server.DetailedStatus{},
	}
	out := renderDetailedStatus(info, 80)

	for _, section := range []string{"Tools (0)", "MCP Servers (0)", "Skills (0)", "Plugins (0)", "Hooks (0)", "Subagents (0)", "Agents (0)"} {
		if !strings.Contains(out, section) {
			t.Errorf("missing %q in output:\n%s", section, out)
		}
	}
}

func TestRenderDetailedStatus_NilDetailed(t *testing.T) {
	info := server.StatusInfo{
		SessionID:       "s1",
		Model:           "m",
		Profile:         "p",
		Turns:           5,
		ContextPressure: 0.1,
	}
	out := renderDetailedStatus(info, 80)

	// Should still show basic info.
	if !strings.Contains(out, "Session:  s1") {
		t.Errorf("missing session in:\n%s", out)
	}
	// Should not have any detailed sections (no Detailed struct at all).
	if strings.Contains(out, "Tools") {
		t.Errorf("should not contain Tools when Detailed is nil:\n%s", out)
	}
}

func TestRenderDetailedStatus_Hooks(t *testing.T) {
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed: &server.DetailedStatus{
			Hooks: map[string]int{
				"PreToolUse":   3,
				"SessionStart": 1,
			},
		},
	}
	out := renderDetailedStatus(info, 80)

	if !strings.Contains(out, "Hooks (2):") {
		t.Errorf("missing hooks header in:\n%s", out)
	}
	if !strings.Contains(out, "PreToolUse: 3") {
		t.Errorf("missing PreToolUse count in:\n%s", out)
	}
	if !strings.Contains(out, "SessionStart: 1") {
		t.Errorf("missing SessionStart count in:\n%s", out)
	}
}

func TestRenderDetailedStatus_ToolsWrapAtWidth(t *testing.T) {
	// Create enough tools that they'd exceed a narrow width.
	tools := []server.ToolInfo{
		{Name: "shell", Source: "core"},
		{Name: "read_file", Source: "core"},
		{Name: "write_file", Source: "core"},
		{Name: "edit_file", Source: "core"},
		{Name: "glob", Source: "core"},
		{Name: "grep", Source: "core"},
		{Name: "web_fetch", Source: "core"},
	}
	info := server.StatusInfo{
		SessionID: "s1",
		Model:     "m",
		Profile:   "p",
		Detailed:  &server.DetailedStatus{Tools: tools},
	}
	// Width 40 means "  Core: " (8 chars) + tools must wrap.
	out := renderDetailedStatus(info, 40)

	// The core tools line should be split across multiple lines.
	lines := strings.Split(out, "\n")
	coreLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Count lines that are part of the core tools listing.
		if strings.HasPrefix(trimmed, "Core:") || (coreLines > 0 && !strings.HasPrefix(trimmed, "MCP") && !strings.HasPrefix(trimmed, "Custom") && !strings.Contains(trimmed, ":") && trimmed != "" && !strings.HasPrefix(trimmed, "Tools") && !strings.HasPrefix(trimmed, "MCP Servers") && !strings.HasPrefix(trimmed, "Skills") && !strings.HasPrefix(trimmed, "Plugins") && !strings.HasPrefix(trimmed, "Hooks") && !strings.HasPrefix(trimmed, "Subagents") && !strings.HasPrefix(trimmed, "Agents")) {
			coreLines++
		} else if coreLines > 0 {
			break
		}
	}
	if coreLines < 2 {
		t.Errorf("expected core tools to wrap across multiple lines at width 40, got %d lines:\n%s", coreLines, out)
	}
	// No line should exceed the width.
	for i, line := range lines {
		if len(line) > 40 {
			t.Errorf("line %d exceeds width 40 (%d chars): %q", i, len(line), line)
		}
	}
}
