package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

func TestCredentialsPanel_RendersList(t *testing.T) {
	m := newCredentialsPanel()
	updated, _ := m.Update(authListResultMsg{List: appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{
		{Provider: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey", "oauth"}},
		{Provider: "anthropic", ActiveSource: "absent", AuthModes: []string{"apiKey"}},
	}}})
	view := updated.(credentialsPanel).View()
	for _, want := range []string{"openai", "anthropic", "oauth", "absent"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestCredentialsPanel_EnterTriggersSet(t *testing.T) {
	m := newCredentialsPanel()
	updated, _ := m.Update(authListResultMsg{List: appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{
		{Provider: "anthropic", ActiveSource: "absent", AuthModes: []string{"apiKey"}},
	}}})
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd")
	}
	msg := cmd()
	got, ok := msg.(credentialsActionMsg)
	if !ok {
		t.Fatalf("cmd msg = %T", msg)
	}
	if got.Action != "set" || got.Provider != "anthropic" {
		t.Errorf("msg = %+v", got)
	}
}
