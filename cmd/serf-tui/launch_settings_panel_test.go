package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

func TestLaunchSettingsPanel_TabSwitch(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(launchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Model: "openai/gpt-5"}})
	p = updated.(launchSettingsPanel)
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyRight})
	v := updated.(launchSettingsPanel).View()
	if !strings.Contains(v, "Project") {
		t.Errorf("view should show Project tab after Right:\n%s", v)
	}
}

func TestLaunchSettingsPanel_LoadsGlobalFirst(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	cmd := p.initialCmd()
	if cmd == nil {
		t.Fatal("initialCmd nil")
	}
	if !p.loadingGlobal {
		t.Errorf("expected loadingGlobal")
	}
}

func TestLaunchSettingsPanel_EditEmitsModalRequest(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(launchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Model: "openai/gpt-5"}})
	// cursor starts at 0, which is "model"
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd requesting a modal")
	}
	msg := cmd()
	req, ok := msg.(launchSettingsEditRequestMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if req.Layer != "global" || req.Field != "model" {
		t.Errorf("req = %+v", req)
	}
	if req.CurrentValue != "openai/gpt-5" {
		t.Errorf("req.CurrentValue = %q", req.CurrentValue)
	}
}

func TestLayerRows_IncludesFastCheapModel(t *testing.T) {
	rows := layerRows(appwire.LaunchConfigLayer{FastCheapModel: "openai/gpt-5-mini"})
	for _, row := range rows {
		if row.field == "fast_cheap_model" {
			if row.value != "openai/gpt-5-mini" {
				t.Fatalf("fast_cheap_model value = %q, want openai/gpt-5-mini", row.value)
			}
			return
		}
	}
	t.Fatalf("layerRows missing fast_cheap_model row: %#v", rows)
}

func TestApplyEdit_FastCheapModel(t *testing.T) {
	got, err := applyEdit(appwire.LaunchConfigLayer{}, "fast_cheap_model", " openai/gpt-5-mini ")
	if err != nil {
		t.Fatalf("applyEdit: %v", err)
	}
	if got.FastCheapModel != "openai/gpt-5-mini" {
		t.Fatalf("FastCheapModel = %q, want openai/gpt-5-mini", got.FastCheapModel)
	}
}

func TestLayerRows_ListFieldsExposeEditableValues(t *testing.T) {
	rows := layerRows(appwire.LaunchConfigLayer{SkillsDirs: []string{"/one", "/two"}})
	for _, row := range rows {
		if row.field == "skills_dirs" {
			if row.value != "2 entries" {
				t.Fatalf("row.value=%q, want entry count", row.value)
			}
			if row.editValue != "/one, /two" {
				t.Fatalf("row.editValue=%q, want joined paths", row.editValue)
			}
			return
		}
	}
	t.Fatal("missing skills_dirs row")
}

func TestApplyEdit_RejectsMissingSkillDir(t *testing.T) {
	_, err := applyEdit(appwire.LaunchConfigLayer{}, "skills_dirs", filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("expected missing directory error")
	}
}

func TestApplyEdit_AcceptsExistingMCPConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := applyEdit(appwire.LaunchConfigLayer{}, "mcp_configs", path)
	if err != nil {
		t.Fatalf("applyEdit: %v", err)
	}
	if len(got.MCPConfigs) != 1 || got.MCPConfigs[0] != path {
		t.Fatalf("MCPConfigs=%v", got.MCPConfigs)
	}
}
