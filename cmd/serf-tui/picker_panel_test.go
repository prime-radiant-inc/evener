package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPickerPanelFiltersAndRendersDisabledReasons(t *testing.T) {
	panel := newPickerPanel("Command palette", []pickerPanelItem{
		{ID: "new", Label: "New session", Detail: "open spawn form"},
		{ID: "clear", Label: "Clear current session", DisabledReason: "open a session first"},
		{ID: "codex", Label: "Codex app-server smoke", Detail: "codex-local"},
	}, 80)

	updated, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("codex")})
	panel = updated.(pickerPanel)
	view := panel.View()
	if !strings.Contains(view, "Command palette") || !strings.Contains(view, "Filter: codex") {
		t.Fatalf("picker view missing title/filter:\n%s", view)
	}
	if !strings.Contains(view, "Codex app-server smoke") {
		t.Fatalf("filtered picker missing codex row:\n%s", view)
	}
	if strings.Contains(view, "New session") {
		t.Fatalf("filtered picker kept unrelated command row:\n%s", view)
	}

	panel = newPickerPanel("Command palette", panel.items, 80)
	view = panel.View()
	if !strings.Contains(view, "Clear current session") || !strings.Contains(view, "disabled: open a session first") {
		t.Fatalf("picker did not render disabled reason:\n%s", view)
	}
}

func TestPickerPanelCannotSelectDisabledRow(t *testing.T) {
	panel := newPickerPanel("Command palette", []pickerPanelItem{
		{ID: "clear", Label: "Clear current session", DisabledReason: "open a session first"},
		{ID: "new", Label: "New session"},
	}, 80)

	updated, _ := panel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	panel = updated.(pickerPanel)
	if panel.done {
		t.Fatalf("disabled first row should not complete selection: %+v", panel)
	}

	updated, _ = panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	panel = updated.(pickerPanel)
	updated, _ = panel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	panel = updated.(pickerPanel)
	if !panel.done || panel.selected != "new" {
		t.Fatalf("enabled row not selected: %+v", panel)
	}
}

func TestPickerPanelRendersAsPopupPane(t *testing.T) {
	withTestColorProfile(t)
	panel := newPickerPanel("Command palette", []pickerPanelItem{
		{ID: "new", Label: "New session", Detail: "open spawn form"},
	}, 80)

	view := panel.View()
	if !strings.Contains(view, "\x1b[") {
		t.Fatalf("picker popup should render terminal styling:\n%s", view)
	}
	plain := ansiPattern.ReplaceAllString(view, "")
	if !strings.Contains(plain, "  Command palette") {
		t.Fatalf("picker popup should have pane padding:\n%s", plain)
	}
}
