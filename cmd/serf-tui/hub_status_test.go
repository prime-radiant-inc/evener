package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestRenderHubSessionStatusWithoutDiagnosticsMatchesThinSummary guards the
// existing contract: when SerfDiagnostics is nil, the rendered status is
// exactly the thin summary (no diagnostics sections leaking through).
func TestRenderHubSessionStatusWithoutDiagnosticsMatchesThinSummary(t *testing.T) {
	detail := hubSessionDetail{
		SessionID:       "01ABC",
		SourceLabel:     "serf",
		Model:           "openai/gpt-5",
		Profile:         "openai",
		WorkingDir:      "/tmp/proj",
		TurnCount:       3,
		ContextPressure: 0.21,
		Diagnostics:     nil,
	}
	got := renderHubSessionStatus(detail, nil, appwire.AuthStatusResponse{}, nil, nil, 80)

	for _, want := range []string{
		"Session:  01ABC",
		"Source:   serf",
		"Dir:      /tmp/proj",
		"Turns:    3",
		"Context:  21% used",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("thin status missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Tools (", "MCP Servers (", "Skills (", "Plugins ("} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("thin status should not include diagnostics section %q when Diagnostics is nil:\n%s", unwanted, got)
		}
	}
}

// TestRenderHubSessionStatusRendersDiagnosticsSections verifies the rich
// /status output: tools by source, MCP servers, skills, plugins, hooks,
// jobs, agents. Mirrors the legacy standalone TUI's renderDetailedStatus.
func TestRenderHubSessionStatusRendersDiagnosticsSections(t *testing.T) {
	detail := hubSessionDetail{
		SessionID: "01ABC",
		Model:     "openai/gpt-5",
		Profile:   "openai",
		Diagnostics: &appwire.SerfDiagnostics{
			Tools: []appwire.SerfToolInfo{
				{Name: "read_file", Source: "core"},
				{Name: "write_file", Source: "core"},
				{Name: "linear_create", Source: "mcp:linear"},
				{Name: "custom_thing", Source: "plugin:demo"},
			},
			MCP: []appwire.SerfMCPServerInfo{
				{Name: "linear", Tools: []string{"linear_create"}},
			},
			Skills: []appwire.SerfSkillInfo{
				{Name: "writing-plans", Description: "Build implementation plans"},
			},
			Plugins: []appwire.SerfPluginInfo{
				{Name: "superpowers", Version: "5.1.0", SkillCount: 30, AgentCount: 2, HookCount: 4},
			},
			Hooks: map[string]int{
				"SessionStart":     2,
				"UserPromptSubmit": 1,
			},
			Jobs: []appwire.SerfJobInfo{
				{JobID: "job_1", JobType: "delegate", Status: "running"},
			},
			Agents: []string{"reviewer", "planner"},
		},
	}

	got := renderHubSessionStatus(detail, nil, appwire.AuthStatusResponse{}, nil, nil, 80)

	for _, want := range []string{
		"Tools (4):",
		"Core:",
		"read_file",
		"write_file",
		"MCP [linear]:",
		"linear_create",
		"Custom:",
		"custom_thing",
		"MCP Servers (1):",
		"linear (1 tools)",
		"Skills (1):",
		"writing-plans",
		"Plugins (1):",
		"superpowers v5.1.0 (30 skills, 2 agents, 4 hooks)",
		"Hooks (2):",
		"SessionStart: 2",
		"UserPromptSubmit: 1",
		"Jobs (1):",
		"job_1 (delegate, running)",
		"Agents (2):",
		"reviewer",
		"planner",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rich status missing %q:\n%s", want, got)
		}
	}
}

func TestFormatContextFragment(t *testing.T) {
	cases := []struct {
		name   string
		detail hubSessionDetail
		want   string
	}{
		{
			name:   "nothing-known",
			detail: hubSessionDetail{},
			want:   "",
		},
		{
			name:   "pressure-only",
			detail: hubSessionDetail{ContextPressure: 0.23},
			want:   "23%",
		},
		{
			name:   "used-plus-pressure-no-window",
			detail: hubSessionDetail{ContextUsed: 45000, ContextPressure: 0.23},
			want:   "45k (23%)",
		},
		{
			name:   "full-rich-form",
			detail: hubSessionDetail{ContextUsed: 46000, ContextWindow: 200000, ContextPressure: 0.23},
			want:   "46k/200k (23%, 134k to compact)",
		},
		{
			name:   "past-compact-threshold-clamps-remaining",
			detail: hubSessionDetail{ContextUsed: 195000, ContextWindow: 200000, ContextPressure: 0.975},
			want:   "195k/200k (98%, 0 to compact)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatContextFragment(tc.detail); got != tc.want {
				t.Errorf("formatContextFragment(%+v) = %q, want %q", tc.detail, got, tc.want)
			}
		})
	}
}

// TestWriteWrappedStatusListWrapsAtWidth ensures the tool list helper wraps
// across multiple rows when the items don't fit on one line.
func TestWriteWrappedStatusListWrapsAtWidth(t *testing.T) {
	var b strings.Builder
	items := []string{
		"alpha_tool_with_long_name",
		"beta_tool_also_long",
		"gamma_tool_more_chars",
		"delta_tool",
	}
	writeWrappedStatusList(&b, "Core:", items, 50)
	out := b.String()
	lines := strings.Split(strings.TrimLeft(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped output to span multiple lines, got:\n%s", out)
	}
	// Continuation lines are indented to align with the first item after the label.
	wantIndent := strings.Repeat(" ", len("  Core: "))
	for i, line := range lines[1:] {
		if !strings.HasPrefix(line, wantIndent) {
			t.Fatalf("continuation line %d should start with %q indent:\n%q", i+1, wantIndent, line)
		}
	}
}
