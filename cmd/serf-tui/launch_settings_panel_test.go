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
