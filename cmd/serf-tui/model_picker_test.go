package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelPicker_FilterAndSelect(t *testing.T) {
	items := []modelPickerItem{
		{id: "gpt-4o", display: "gpt-4o"},
		{id: "gpt-4o-mini", display: "gpt-4o-mini"},
		{id: "o3", display: "o3"},
	}
	p := newModelPicker(items, "gpt-4o", 80)

	// Type "mini" to filter.
	var tm tea.Model = p
	for _, ch := range "mini" {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	mp := tm.(modelPicker)
	filtered := mp.filtered()
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered result, got %d", len(filtered))
	}
	if filtered[0].id != "gpt-4o-mini" {
		t.Errorf("filtered[0] = %q, want gpt-4o-mini", filtered[0].id)
	}

	// Press enter to select.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mp = tm.(modelPicker)
	if mp.selected != "gpt-4o-mini" {
		t.Errorf("selected = %q, want gpt-4o-mini", mp.selected)
	}
	if !mp.done {
		t.Error("expected done=true after enter")
	}
}

func TestModelPicker_Escape(t *testing.T) {
	items := []modelPickerItem{{id: "m1", display: "m1"}}
	p := newModelPicker(items, "", 80)

	result, _ := p.Update(tea.KeyMsg{Type: tea.KeyEscape})
	mp := result.(modelPicker)
	if !mp.cancelled {
		t.Error("expected cancelled=true on escape")
	}
	if !mp.done {
		t.Error("expected done=true on escape")
	}
}

func TestModelPicker_ActiveHighlight(t *testing.T) {
	items := []modelPickerItem{
		{id: "gpt-4o", display: "gpt-4o"},
		{id: "gpt-4o-mini", display: "gpt-4o-mini"},
	}
	p := newModelPicker(items, "gpt-4o-mini", 80)

	view := p.View()
	if !strings.Contains(view, "(active)") {
		t.Error("view should contain '(active)' tag for current model")
	}
}

func TestModelPicker_DisabledItemRendersReasonAndCannotSelect(t *testing.T) {
	items := []modelPickerItem{
		{id: "openai/gpt-5", display: "openai/gpt-5", disabledReason: "login required: run /auth openai"},
		{id: "ollama/llama3", display: "ollama/llama3"},
	}
	p := newModelPicker(items, "", 80)

	if view := p.View(); !strings.Contains(view, "disabled: login required") || !strings.Contains(view, "/auth openai") {
		t.Fatalf("picker did not render disabled reason:\n%s", view)
	}

	tm, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("disabled selection returned unexpected command")
	}
	mp := tm.(modelPicker)
	if mp.done || mp.selected != "" {
		t.Fatalf("disabled row should keep picker open without selection: done=%v selected=%q", mp.done, mp.selected)
	}

	tm, _ = mp.Update(tea.KeyMsg{Type: tea.KeyDown})
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mp = tm.(modelPicker)
	if !mp.done || mp.selected != "ollama/llama3" {
		t.Fatalf("enabled row selection done=%v selected=%q, want ollama/llama3", mp.done, mp.selected)
	}
}

func TestModelPicker_Navigation(t *testing.T) {
	items := []modelPickerItem{
		{id: "a", display: "a"},
		{id: "b", display: "b"},
		{id: "c", display: "c"},
	}
	p := newModelPicker(items, "", 80)
	if p.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", p.cursor)
	}

	// Down arrow.
	tm, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	mp := tm.(modelPicker)
	if mp.cursor != 1 {
		t.Errorf("after down: cursor = %d, want 1", mp.cursor)
	}

	// Down again.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mp = tm.(modelPicker)
	if mp.cursor != 2 {
		t.Errorf("after 2x down: cursor = %d, want 2", mp.cursor)
	}

	// Down at bottom should stay.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mp = tm.(modelPicker)
	if mp.cursor != 2 {
		t.Errorf("at bottom: cursor = %d, want 2", mp.cursor)
	}

	// Up arrow.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyUp})
	mp = tm.(modelPicker)
	if mp.cursor != 1 {
		t.Errorf("after up: cursor = %d, want 1", mp.cursor)
	}
}

func TestModelPicker_Backspace(t *testing.T) {
	items := []modelPickerItem{
		{id: "gpt-4o", display: "gpt-4o"},
		{id: "gpt-4o-mini", display: "gpt-4o-mini"},
	}
	p := newModelPicker(items, "", 80)

	// Type "xyz" -- should filter to 0 results.
	var tm tea.Model = p
	for _, ch := range "xyz" {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	mp := tm.(modelPicker)
	if len(mp.filtered()) != 0 {
		t.Fatalf("expected 0 filtered after 'xyz', got %d", len(mp.filtered()))
	}

	// Backspace 3 times to clear filter.
	for i := 0; i < 3; i++ {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	mp = tm.(modelPicker)
	if mp.filter != "" {
		t.Errorf("filter should be empty after 3 backspaces, got %q", mp.filter)
	}
	if len(mp.filtered()) != 2 {
		t.Errorf("expected 2 filtered after clearing, got %d", len(mp.filtered()))
	}
}
