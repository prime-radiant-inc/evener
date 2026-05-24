package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTextInputModalUsesOverlay(t *testing.T) {
	withTestColorProfile(t)
	m := newTextInputModalWithTitle("Set OpenAI API key", "Paste the key:", "")
	got := m.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "╭") {
		t.Errorf("text input modal should use Overlay primitive: %q", plain)
	}
	if !strings.Contains(plain, "Set OpenAI API key") {
		t.Errorf("text input modal should show title: %q", plain)
	}
}

func TestTextInputModal_CapturesAndSubmits(t *testing.T) {
	m := newTextInputModal("API key for anthropic", "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-ant-X")})
	m = updated.(textInputModal)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd")
	}
	msg := cmd()
	res, ok := msg.(textInputResultMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want textInputResultMsg", msg)
	}
	if res.Cancelled {
		t.Errorf("should not be cancelled")
	}
	if res.Value != "sk-ant-X" {
		t.Errorf("Value = %q", res.Value)
	}
}

func TestTextInputModal_EscapeCancels(t *testing.T) {
	m := newTextInputModal("x", "")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	msg := cmd()
	res := msg.(textInputResultMsg)
	if !res.Cancelled {
		t.Errorf("Esc should cancel")
	}
}
