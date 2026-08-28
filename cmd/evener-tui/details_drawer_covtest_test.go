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
	if got, want := b.String(), "Model:    gpt-5 (openai)\n"; got != want {
		t.Fatalf("model line = %q, want %q", got, want)
	}
}

func TestCovWriteModelOrProviderLine_NoModelWithProfile(t *testing.T) {
	var b strings.Builder
	writeModelOrProviderLine(&b, "", "openai")
	if got, want := b.String(), "Provider: openai\n"; got != want {
		t.Fatalf("provider line = %q, want %q", got, want)
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
		HookEvents: []appwire.EvenerHookEventStatus{{Event: "PreToolCall", Count: 2}, {Event: "PostToolCall", Count: 5}},
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
	if !strings.Contains(got, "Hook Events (2)") {
		t.Fatalf("should show hook events:\n%s", got)
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
	if !strings.Contains(got, "Hook Events (0)") {
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
	want := "send, steer, interrupt, compact, clear, fork, resume, shutdown, change model"
	if got != want {
		t.Fatalf("all capabilities = %q, want %q", got, want)
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
	if want := "send, compact"; got != want {
		t.Fatalf("partial capabilities = %q, want %q", got, want)
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
