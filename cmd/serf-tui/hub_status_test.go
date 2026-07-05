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

func TestRenderHubSessionStatusShortensLongDelegateJobIDs(t *testing.T) {
	const longJobID = "job_01KW0VERYVERYLONGIDENTIFIER"
	detail := hubSessionDetail{
		SessionID: "01ABC",
		Diagnostics: &appwire.SerfDiagnostics{
			Jobs: []appwire.SerfJobInfo{
				{JobID: longJobID, JobType: "delegate", Status: "running"},
			},
		},
	}

	collapsed := renderHubSessionStatus(detail, nil, appwire.AuthStatusResponse{}, nil, nil, 80)

	if strings.Contains(collapsed, longJobID) {
		t.Fatalf("collapsed status leaked full job id: %s", collapsed)
	}
	if !strings.Contains(collapsed, "job 01KW0V…") {
		t.Fatalf("collapsed status missing exact abbreviated job label 'job 01KW0V…': %s", collapsed)
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
			want:   "46k/200k (23%, 144k to compact)",
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

// TestHubDetailFromThreadMapsWorkingStateFields asserts hubDetailFromThread
// carries thread.Serf.{Usage,WorkMillis,ActiveTurnStartedAt} (WS2) onto
// hubSessionDetail verbatim, mirroring how Queue/Goal are already mapped.
func TestHubDetailFromThreadMapsWorkingStateFields(t *testing.T) {
	usage := &appwire.SerfUsage{
		InputTokens:     46000,
		OutputTokens:    12000,
		CacheReadTokens: 100000,
		TotalTokens:     158000,
	}
	thread := appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Source:    "local",
		Serf: appwire.SerfThread{
			Ref:                 "local:th_1",
			Usage:               usage,
			WorkMillis:          185000,
			ActiveTurnStartedAt: 1750000000,
		},
	}

	detail := hubDetailFromThread(thread)

	if detail.Usage != usage {
		t.Fatalf("Usage=%p, want the same pointer as thread.Serf.Usage=%p", detail.Usage, usage)
	}
	if detail.WorkMillis != 185000 {
		t.Fatalf("WorkMillis=%d, want 185000", detail.WorkMillis)
	}
	if detail.ActiveTurnStartedAt != 1750000000 {
		t.Fatalf("ActiveTurnStartedAt=%d, want 1750000000", detail.ActiveTurnStartedAt)
	}
}

// TestHubDetailFromThreadLeavesUsageNilWhenThreadHasNone guards the "no data"
// path: an old daemon or Codex thread reports no usage, and the mapping must
// not synthesize a zero-value struct (which would render ↑0 ↓0).
func TestHubDetailFromThreadLeavesUsageNilWhenThreadHasNone(t *testing.T) {
	detail := hubDetailFromThread(appwire.Thread{
		ID:        "th_2",
		SessionID: "th_2",
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: "local:th_2"},
	})
	if detail.Usage != nil {
		t.Fatalf("Usage=%+v, want nil", detail.Usage)
	}
	if detail.WorkMillis != 0 {
		t.Fatalf("WorkMillis=%d, want 0", detail.WorkMillis)
	}
}

// TestRenderHubSessionStatusShowsWorkAndTokenLines asserts the /status details
// drawer renders a Work: line and a full Tokens: breakdown (incl. cache-read)
// when the source reports WS2 metrics, and omits both when it doesn't.
func TestRenderHubSessionStatusShowsWorkAndTokenLines(t *testing.T) {
	withMetrics := hubSessionDetail{
		SessionID:  "01ABC",
		WorkMillis: 185000, // 3m5s -> "3m"
		Usage: &appwire.SerfUsage{
			InputTokens:     46000,
			OutputTokens:    12000,
			CacheReadTokens: 100000,
			TotalTokens:     158000,
		},
	}
	got := renderHubSessionStatus(withMetrics, nil, appwire.AuthStatusResponse{}, nil, nil, 80)
	for _, want := range []string{
		"Work:     3m",
		"Tokens:   ↑46k ↓12k · cache-read 100k · total 158k",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status with metrics missing %q:\n%s", want, got)
		}
	}

	noMetrics := hubSessionDetail{SessionID: "01ABC"}
	got = renderHubSessionStatus(noMetrics, nil, appwire.AuthStatusResponse{}, nil, nil, 80)
	for _, unwanted := range []string{"Work:", "Tokens:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("status without metrics should omit %q:\n%s", unwanted, got)
		}
	}
}

// TestSessionHeaderShowsWorkAndTokenChip asserts the in-session header meta
// strip renders compact work-time + token chips (no cache-read — the chip
// strip stays compact; the full breakdown lives in the /status drawer) when
// the source supplies WorkMillis/Usage, and omits both when neither is set.
func TestSessionHeaderShowsWorkAndTokenChip(t *testing.T) {
	withMetrics := hubModel{
		detail: hubSessionDetail{
			Title:       "Metrics session",
			SourceLabel: "serf",
			Model:       "openai/gpt-5",
			TurnCount:   3,
			WorkMillis:  185000, // "3m"
			Usage: &appwire.SerfUsage{
				InputTokens:     46000,
				OutputTokens:    12000,
				CacheReadTokens: 100000,
				TotalTokens:     158000,
			},
		},
		width: 200,
	}
	got := strings.Join(withMetrics.sessionHeaderLines(), "\n")
	for _, want := range []string{"work 3m", "tok ↑46k ↓12k"} {
		if !strings.Contains(got, want) {
			t.Errorf("metrics chip missing %q from meta strip:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cache-read") {
		t.Errorf("chip strip should stay compact (no cache-read):\n%s", got)
	}

	noMetrics := hubModel{
		detail: hubSessionDetail{
			Title:       "Plain session",
			SourceLabel: "serf",
			Model:       "openai/gpt-5",
			TurnCount:   1,
		},
		width: 200,
	}
	got = strings.Join(noMetrics.sessionHeaderLines(), "\n")
	if strings.Contains(got, "work") || strings.Contains(got, "tok ") {
		t.Errorf("plain session should omit work/token chips:\n%s", got)
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
