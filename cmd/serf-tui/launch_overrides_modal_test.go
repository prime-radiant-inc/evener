package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

func TestLaunchOverridesModal_AddsField(t *testing.T) {
	m := newLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	// cursor starts at 0 ("model"); press Enter to request an edit
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = updated
	if cmd == nil {
		t.Fatal("Enter should request an edit")
	}
	msg := cmd()
	req, ok := msg.(launchSettingsEditRequestMsg)
	if !ok {
		t.Fatalf("msg = %T, want launchSettingsEditRequestMsg", msg)
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
	m := newLaunchOverridesModalWith(appwire.LaunchConfigLayer{MaxRounds: &fifty})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("Ctrl-S should submit")
	}
	msg := cmd()
	res, ok := msg.(launchOverridesResultMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if res.Overrides == nil || res.Overrides.MaxRounds == nil || *res.Overrides.MaxRounds != 50 {
		t.Errorf("Overrides = %+v", res.Overrides)
	}
}

func TestLaunchOverridesModal_EscapeCancels(t *testing.T) {
	m := newLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc should produce a result cmd")
	}
	res := cmd().(launchOverridesResultMsg)
	if !res.Cancelled {
		t.Errorf("Esc should cancel")
	}
}
