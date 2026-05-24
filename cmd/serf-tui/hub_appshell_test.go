package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHubModelAppShellKeepsDashboardFooterUnderPaletteOverlay(t *testing.T) {
	m := sampleHubModel(100)
	m.openCommandPalette()

	got := m.View()
	requireOrderedText(t, got, "SERF LIVE", "Command palette", "dashboard")
}

func TestHubModelAppShellSessionTopBarAndComposerRegion(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []chatMessage{{Kind: msgAssistant, Text: "Ready for shell work."}}

	got := m.View()
	requireOrderedText(t, got, "serf / session / send task", "Ready for shell work.", "> ")
}

func TestHubModelAppShellAddsSubtleChromeStyles(t *testing.T) {
	withTestColorProfile(t)
	got := appShell{
		TopBar: "serf live",
		Body:   "Live now\n> idle serf session",
		Footer: "enter open",
	}.View()

	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("app shell should style chrome for hierarchy:\n%q", got)
	}
}

func TestHubModelAppShellAnchorsFooterToKnownHeight(t *testing.T) {
	got := appShell{
		TopBar: "serf live",
		Body:   "one live session",
		Footer: "up/down select  enter open",
		Height: 8,
	}.View()

	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 8 {
		t.Fatalf("shell height=%d, want 8:\n%q", len(lines), got)
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "up/down select") {
		t.Fatalf("footer not anchored to bottom, last line=%q:\n%s", last, got)
	}
}

func TestHubModelCtrlOReturnsDashboardFromCommandPaletteOverlay(t *testing.T) {
	m := sampleHubModel(100)
	m.openCommandPalette()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd != nil {
		t.Fatal("ctrl+o dashboard return should be synchronous")
	}
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
	if got.commandPalette != nil {
		t.Fatalf("command palette stayed open after ctrl+o")
	}
}

func TestHubModelCtrlOClearsSessionOverlay(t *testing.T) {
	m := newSessionHubModel(nil)
	picker := newModelPicker([]modelPickerItem{{id: "openai/gpt-5", display: "openai/gpt-5"}}, "", 100)
	m.sessionModelPicker = &picker

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd != nil {
		t.Fatal("ctrl+o dashboard return should be synchronous")
	}
	got := updated.(hubModel)
	if got.mode != hubModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
	if got.sessionModelPicker != nil {
		t.Fatalf("session model picker stayed open after ctrl+o")
	}
}

func TestActionBarWrapsBeforeDroppingDashboardHint(t *testing.T) {
	got := actionBarForWidth(60, "up/down select", "enter open", "p project", "n new", "/ palette", "ctrl+o dashboard", "q quit")

	if !strings.Contains(got, "ctrl+o dashboard") {
		t.Fatalf("wrapped action bar dropped dashboard hint:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 60 {
			t.Fatalf("action bar line too wide (%d): %q\n%s", len([]rune(line)), line, got)
		}
	}
}

func TestDashboardFooterUsesTextKeyNames(t *testing.T) {
	withTestColorProfile(t)
	got := dashboardFooter(100)
	// KbdHint chips: verify key and action labels are both present
	for _, label := range []string{"select", "open", "new", "filter", "dashboard", "quit"} {
		if !strings.Contains(got, label) {
			t.Fatalf("dashboard footer missing label %q:\n%s", label, got)
		}
	}
}

func TestDashboardFooterContainsKbdHintChrome(t *testing.T) {
	withTestColorProfile(t)
	got := dashboardFooter(100)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("dashboardFooter should style kbd tokens with ANSI escapes: %q", got)
	}
}

func TestDashboardHeaderUsesSectionDivider(t *testing.T) {
	withTestColorProfile(t)
	got := dashboardHeader("http://hub.test", 3, 100)
	for _, want := range []string{"SERF LIVE", "http://hub.test", "─", "┄"} {
		if !strings.Contains(got, want) {
			t.Errorf("dashboardHeader missing %q in: %q", want, got)
		}
	}
}

func requireOrderedText(t *testing.T, got string, parts ...string) {
	t.Helper()
	pos := -1
	for _, part := range parts {
		next := strings.Index(got, part)
		if next < 0 {
			t.Fatalf("view missing %q:\n%s", part, got)
		}
		if next < pos {
			t.Fatalf("view rendered %q before prior parts %v:\n%s", part, parts, got)
		}
		pos = next
	}
}
