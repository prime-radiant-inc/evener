package launchconfig

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

func TestLaunchOverridesModalUsesOverlay(t *testing.T) {
	withTestColorProfile(t)
	m := NewLaunchOverridesModal()
	got := m.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "╭") {
		t.Errorf("launch_overrides should use Overlay primitive: %q", plain)
	}
}

func TestLaunchOverridesModal_AddsField(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	// cursor starts at 0 ("model"); press Enter to request an edit
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = updated
	if cmd == nil {
		t.Fatal("Enter should request an edit")
	}
	msg := cmd()
	req, ok := msg.(LaunchSettingsEditRequestMsg)
	if !ok {
		t.Fatalf("msg = %T, want LaunchSettingsEditRequestMsg", msg)
	}
	if req.Layer != "launch" {
		t.Errorf("Layer = %q, want launch", req.Layer)
	}
	if req.Field == "" {
		t.Errorf("missing field: %+v", req)
	}
}

func TestLaunchOverridesModal_ProducesOverrideOnSubmit(t *testing.T) {
	fifty := 50
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{MaxRounds: &fifty})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("Ctrl-S should submit")
	}
	msg := cmd()
	res, ok := msg.(LaunchOverridesResultMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if res.Overrides == nil || res.Overrides.MaxRounds == nil || *res.Overrides.MaxRounds != 50 {
		t.Errorf("Overrides = %+v", res.Overrides)
	}
}

func TestLaunchOverridesModal_EscapeCancels(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc should produce a result cmd")
	}
	res := cmd().(LaunchOverridesResultMsg)
	if !res.Cancelled {
		t.Errorf("Esc should cancel")
	}
}

func TestLaunchOverridesModal_UsesSchemaRows(t *testing.T) {
	m := NewLaunchOverridesModalWithSchema(appwire.LaunchConfigLayer{Agent: "serf"}, testLaunchSchema())
	view := m.View()
	if !strings.Contains(view, "Agent") {
		t.Fatalf("view should use schema label:\n%s", view)
	}
	if strings.Contains(view, "App replay size") {
		t.Fatalf("override view should exclude non-per-launch field:\n%s", view)
	}
}

func TestLaunchOverridesModal_SchemaPathFieldRequestsCompletion(t *testing.T) {
	m := NewLaunchOverridesModalWithSchema(appwire.LaunchConfigLayer{}, []appwire.LaunchOption{
		{Field: "system_prompt_file", Label: "System prompt file", Kind: "path", PathKind: "file", PerLaunch: true},
	})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should request edit")
	}
	req := cmd().(LaunchSettingsEditRequestMsg)
	if req.Field != "system_prompt_file" || !req.PathCompletion {
		t.Fatalf("request=%+v, want path completion", req)
	}
}

func TestLaunchOverridesModal_MCPsRowRequestsEdit(t *testing.T) {
	m := NewLaunchOverridesModalWithSchema(appwire.LaunchConfigLayer{MCPs: []appwire.MCPServerSpec{{Name: "docs", Command: "docs-mcp"}}}, []appwire.LaunchOption{
		{Field: "mcps", Label: "MCP servers", Kind: "mcpServerList", PerLaunch: true},
	})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("mcps row should request an edit")
	}
	req := cmd().(LaunchSettingsEditRequestMsg)
	if req.Field != "mcps" || req.CurrentValue != `[{"name":"docs","command":"docs-mcp","args":null}]` {
		t.Fatalf("request=%+v, want serialized mcps edit value", req)
	}
}

func TestLaunchOverridesModal_ApplyEditMCPs(t *testing.T) {
	m := NewLaunchOverridesModalWithSchema(appwire.LaunchConfigLayer{}, []appwire.LaunchOption{
		{Field: "mcps", Label: "MCP servers", Kind: "mcpServerList", PerLaunch: true},
	})
	got, err := m.ApplyEdit("mcps", "docs:sh -c docs")
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	cur := got.Current()
	if len(cur.MCPs) != 1 || cur.MCPs[0].Name != "docs" || cur.MCPs[0].Command != "sh" || strings.Join(cur.MCPs[0].Args, " ") != "-c docs" {
		t.Fatalf("MCPs=%+v", cur.MCPs)
	}
}
