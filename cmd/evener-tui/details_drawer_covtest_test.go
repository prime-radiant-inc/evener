package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// ---- modelAndProfile ---------------------------------------------------------

func TestCovModelAndProfile_BothPresent(t *testing.T) {
	if got := modelAndProfile("gpt-5", "openai"); got != "gpt-5 (openai)" {
		t.Fatalf("both present = %q, want 'gpt-5 (openai)'", got)
	}
}

func TestCovModelAndProfile_ModelOnly(t *testing.T) {
	if got := modelAndProfile("gpt-5", ""); got != "gpt-5" {
		t.Fatalf("model only = %q, want 'gpt-5'", got)
	}
}

func TestCovModelAndProfile_ProfileOnly(t *testing.T) {
	if got := modelAndProfile("", "openai"); got != "openai" {
		t.Fatalf("profile only = %q, want 'openai'", got)
	}
}

func TestCovModelAndProfile_Neither(t *testing.T) {
	if got := modelAndProfile("", ""); got != "" {
		t.Fatalf("neither = %q, want empty", got)
	}
}

// ---- writeModelOrProviderLine ------------------------------------------------

func TestCovWriteModelOrProviderLine_WithModel(t *testing.T) {
	var b strings.Builder
	writeModelOrProviderLine(&b, "gpt-5", "openai")
	got := b.String()
	if !strings.Contains(got, "Model:") || !strings.Contains(got, "gpt-5") || !strings.Contains(got, "openai") {
		t.Fatalf("with model = %q", got)
	}
}

func TestCovWriteModelOrProviderLine_NoModelWithProfile(t *testing.T) {
	var b strings.Builder
	writeModelOrProviderLine(&b, "", "openai")
	got := b.String()
	if !strings.Contains(got, "Provider:") || !strings.Contains(got, "openai") {
		t.Fatalf("no model with profile = %q", got)
	}
}

func TestCovWriteModelOrProviderLine_Neither(t *testing.T) {
	var b strings.Builder
	writeModelOrProviderLine(&b, "", "")
	if b.String() != "" {
		t.Fatalf("neither = %q, want empty", b.String())
	}
}

// ---- writeEvenerDiagnostics --------------------------------------------------

func TestCovWriteEvenerDiagnostics_CoreAndCustomTools(t *testing.T) {
	var b strings.Builder
	diag := &appwire.EvenerDiagnostics{
		Tools: []appwire.EvenerToolInfo{
			{Name: "read", Source: "core"},
			{Name: "custom_tool", Source: "custom"},
		},
	}
	writeEvenerDiagnostics(&b, diag)
	got := b.String()
	if !strings.Contains(got, "Tools (2)") {
		t.Fatalf("should show tool count:\n%s", got)
	}
	if !strings.Contains(got, "Core:") {
		t.Fatalf("should show Core tools:\n%s", got)
	}
	if !strings.Contains(got, "Custom:") {
		t.Fatalf("should show Custom tools:\n%s", got)
	}
}

func TestCovWriteEvenerDiagnostics_MCPTools(t *testing.T) {
	var b strings.Builder
	diag := &appwire.EvenerDiagnostics{
		Tools: []appwire.EvenerToolInfo{
			{Name: "linear_create", Source: "mcp:linear"},
			{Name: "slack_post", Source: "mcp:slack"},
		},
		MCP: []appwire.EvenerMCPServerInfo{
			{Name: "linear", Tools: []string{"linear_create"}, Status: "connected"},
		},
	}
	writeEvenerDiagnostics(&b, diag)
	got := b.String()
	if !strings.Contains(got, "MCP [linear]:") {
		t.Fatalf("should show MCP server tools:\n%s", got)
	}
	if !strings.Contains(got, "MCP Servers (1)") {
		t.Fatalf("should show MCP servers count:\n%s", got)
	}
}

func TestCovWriteEvenerDiagnostics_SkillsPluginsHooksJobsAgents(t *testing.T) {
	var b strings.Builder
	diag := &appwire.EvenerDiagnostics{
		Skills: []appwire.EvenerSkillInfo{{Name: "brainstorming"}},
		Plugins: []appwire.EvenerPluginInfo{
			{Name: "my-plugin", Version: "1.0", SkillCount: 2, AgentCount: 1, HookCount: 3},
		},
		Hooks:  map[string]int{"PreToolCall": 2, "PostToolCall": 5},
		Jobs:   []appwire.EvenerJobInfo{{JobID: "job_1", JobType: "shell", Status: "running"}},
		Agents: []string{"agent_one"},
	}
	writeEvenerDiagnostics(&b, diag)
	got := b.String()
	if !strings.Contains(got, "Skills (1)") || !strings.Contains(got, "brainstorming") {
		t.Fatalf("should show skills:\n%s", got)
	}
	if !strings.Contains(got, "Plugins (1)") || !strings.Contains(got, "my-plugin") {
		t.Fatalf("should show plugins:\n%s", got)
	}
	if !strings.Contains(got, "Hooks (2)") {
		t.Fatalf("should show hooks:\n%s", got)
	}
	if !strings.Contains(got, "Jobs (1)") || !strings.Contains(got, "job_1") {
		t.Fatalf("should show jobs:\n%s", got)
	}
	if !strings.Contains(got, "Agents (1)") || !strings.Contains(got, "agent_one") {
		t.Fatalf("should show agents:\n%s", got)
	}
}

func TestCovWriteEvenerDiagnostics_PluginNoVersion(t *testing.T) {
	var b strings.Builder
	diag := &appwire.EvenerDiagnostics{
		Plugins: []appwire.EvenerPluginInfo{
			{Name: "noversion"},
		},
	}
	writeEvenerDiagnostics(&b, diag)
	got := b.String()
	if !strings.Contains(got, "v?") {
		t.Fatalf("plugin with no version should show v?:\n%s", got)
	}
}

func TestCovWriteEvenerDiagnostics_NoHooks(t *testing.T) {
	var b strings.Builder
	diag := &appwire.EvenerDiagnostics{}
	writeEvenerDiagnostics(&b, diag)
	got := b.String()
	if !strings.Contains(got, "Hooks (0)") {
		t.Fatalf("should show hooks count 0:\n%s", got)
	}
}

func TestCovWriteEvenerDiagnostics_MCPServerWithError(t *testing.T) {
	var b strings.Builder
	diag := &appwire.EvenerDiagnostics{
		MCP: []appwire.EvenerMCPServerInfo{
			{Name: "github", Tools: []string{"repo"}, Status: "error", Error: "conn refused"},
		},
	}
	writeEvenerDiagnostics(&b, diag)
	got := b.String()
	if !strings.Contains(got, "last error: conn refused") {
		t.Fatalf("should show MCP server error:\n%s", got)
	}
}

func TestCovWriteEvenerDiagnostics_MCPServerNoStatus(t *testing.T) {
	var b strings.Builder
	diag := &appwire.EvenerDiagnostics{
		MCP: []appwire.EvenerMCPServerInfo{
			{Name: "linear", Tools: []string{"x"}},
		},
	}
	writeEvenerDiagnostics(&b, diag)
	got := b.String()
	// Should NOT have " — " after tools count when no status
	if strings.Contains(got, "(1 tools) —") {
		t.Fatalf("should not have trailing dash when no status:\n%s", got)
	}
}

// ---- capabilityList ---------------------------------------------------------

func TestCovCapabilityList_All(t *testing.T) {
	caps := hubSessionCapabilities{
		Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true,
		Fork: true, Resume: true, Shutdown: true, ChangeModel: true,
	}
	got := capabilityList(caps)
	for _, want := range []string{"send", "steer", "interrupt", "compact", "clear", "fork", "resume", "shutdown", "change model"} {
		if !strings.Contains(got, want) {
			t.Fatalf("capabilityList missing %q: %s", want, got)
		}
	}
}

func TestCovCapabilityList_None(t *testing.T) {
	got := capabilityList(hubSessionCapabilities{})
	if got != "" {
		t.Fatalf("no capabilities = %q, want empty", got)
	}
}

func TestCovCapabilityList_Partial(t *testing.T) {
	got := capabilityList(hubSessionCapabilities{Send: true, Compact: true})
	if !strings.Contains(got, "send") || !strings.Contains(got, "compact") {
		t.Fatalf("partial caps = %q", got)
	}
	if strings.Contains(got, "steer") {
		t.Fatalf("partial caps should not contain steer: %q", got)
	}
}

// ---- sortedHookEntries -------------------------------------------------------

func TestCovSortedHookEntries(t *testing.T) {
	got := sortedHookEntries(map[string]int{"B": 2, "A": 1, "C": 3})
	if len(got) != 3 {
		t.Fatalf("count = %d, want 3", len(got))
	}
	if got[0] != "A: 1" || got[1] != "B: 2" || got[2] != "C: 3" {
		t.Fatalf("sorted = %v, want [A: 1, B: 2, C: 3]", got)
	}
}

func TestCovSortedHookEntries_Empty(t *testing.T) {
	got := sortedHookEntries(nil)
	if len(got) != 0 {
		t.Fatalf("empty = %v, want empty", got)
	}
}

// ---- sortedKeys --------------------------------------------------------------

func TestCovSortedKeys(t *testing.T) {
	got := sortedKeys(map[string][]string{"b": {"x"}, "a": {"y"}})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("sortedKeys = %v, want [a b]", got)
	}
}

// ---- detailsDrawer.View: Diagnostics section --------------------------------

func TestCovDetailsDrawer_DiagnosticsNilShowsGhost(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{Title: "Test", Diagnostics: nil}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "not reported") {
		t.Fatalf("nil diagnostics should show ghost text:\n%s", plain)
	}
}

func TestCovDetailsDrawer_WithDiagnostics(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{
		Title: "Test",
		Diagnostics: &appwire.EvenerDiagnostics{
			Tools: []appwire.EvenerToolInfo{{Name: "read", Source: "core"}},
		},
	}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Tools (1)") {
		t.Fatalf("should show diagnostics tools:\n%s", plain)
	}
}

func TestCovDetailsDrawer_WithWebURL(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{
		Detail: hubSessionDetail{Ref: "local:01ABC"},
		HubURL: "http://hub:8080",
	}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Web:") || !strings.Contains(plain, "/s/") {
		t.Fatalf("should show web URL:\n%s", plain)
	}
}

func TestCovDetailsDrawer_WithCapabilities(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{
		Capabilities: hubSessionCapabilities{Send: true, Steer: true},
	}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Capabilities:") || !strings.Contains(plain, "send") {
		t.Fatalf("should show capabilities:\n%s", plain)
	}
}

func TestCovDetailsDrawer_WithRecentErrors(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{
		RecentErrors: []string{"error one", "error two"},
	}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "RECENT ERRORS") || !strings.Contains(plain, "error one") {
		t.Fatalf("should show recent errors:\n%s", plain)
	}
}

func TestCovDetailsDrawer_WithFailedToolCalls(t *testing.T) {
	withTestColorProfile(t)
	failed := 3
	d := detailsDrawer{Detail: hubSessionDetail{
		FailedToolCalls: &failed,
	}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Failed:") || !strings.Contains(plain, "3") {
		t.Fatalf("should show failed tool calls:\n%s", plain)
	}
}

func TestCovDetailsDrawer_WithTurnCount(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{TurnCount: 5}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Turns:") || !strings.Contains(plain, "5") {
		t.Fatalf("should show turn count:\n%s", plain)
	}
}

func TestCovDetailsDrawer_WithContextPressure(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{ContextPressure: 0.5}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Context:") || !strings.Contains(plain, "50%") {
		t.Fatalf("should show context pressure:\n%s", plain)
	}
}
