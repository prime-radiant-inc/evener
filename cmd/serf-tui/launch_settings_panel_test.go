package main

import (
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
