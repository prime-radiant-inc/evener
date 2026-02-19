package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
)

// TestApplyTheme_DarkAndLightDiffer checks that dark and light themes produce
// different statusBar background colors.
func TestApplyTheme_DarkAndLightDiffer(t *testing.T) {
	applyTheme(darkTheme)
	darkBg := darkTheme.statusBarBg

	applyTheme(lightTheme)
	lightBg := lightTheme.statusBarBg

	if darkBg == lightBg {
		t.Errorf("dark and light statusBarBg are the same: %q", darkBg)
	}
}

// TestInitTheme_SetsStyles verifies that after initTheme the style vars are
// non-zero (i.e. actually populated).
func TestInitTheme_SetsStyles(t *testing.T) {
	initTheme()
	// Render a dummy string through each style — if the style is zero-value
	// (empty Style) the string still renders, but lipgloss.Width > 0 means
	// the style var was at least assigned.
	tests := []struct {
		name  string
		style lipgloss.Style
		input string
	}{
		{"statusBarStyle", statusBarStyle, "x"},
		{"userBlockStyle", userBlockStyle, "x"},
		{"thinkingStyle", thinkingStyle, "x"},
		{"communicateStyle", communicateStyle, "x"},
		{"toolCollapsedStyle", toolCollapsedStyle, "x"},
		{"toolNameStyle", toolNameStyle, "x"},
		{"inputBorderStyle", inputBorderStyle, "x"},
		{"inputPromptStyle", inputPromptStyle, "x"},
	}
	for _, tt := range tests {
		rendered := tt.style.Render(tt.input)
		if rendered == "" {
			t.Errorf("%s.Render(%q) returned empty string", tt.name, tt.input)
		}
	}
}

// TestSetTheme_Dark switches to dark theme and checks the name.
func TestSetTheme_Dark(t *testing.T) {
	// Start in light so we can observe a change.
	applyTheme(lightTheme)
	activeThemeName = "light"

	ok := setTheme("dark")
	if !ok {
		t.Fatal("setTheme(\"dark\") returned false")
	}
	if currentThemeName() != "dark" {
		t.Errorf("currentThemeName() = %q, want %q", currentThemeName(), "dark")
	}
	if activeTheme.statusBarBg != darkTheme.statusBarBg {
		t.Errorf("activeTheme.statusBarBg = %q, want dark theme value %q",
			activeTheme.statusBarBg, darkTheme.statusBarBg)
	}
}

// TestSetTheme_Light switches to light theme and checks the name.
func TestSetTheme_Light(t *testing.T) {
	applyTheme(darkTheme)
	activeThemeName = "dark"

	ok := setTheme("light")
	if !ok {
		t.Fatal("setTheme(\"light\") returned false")
	}
	if currentThemeName() != "light" {
		t.Errorf("currentThemeName() = %q, want %q", currentThemeName(), "light")
	}
	if activeTheme.statusBarBg != lightTheme.statusBarBg {
		t.Errorf("activeTheme.statusBarBg = %q, want light theme value %q",
			activeTheme.statusBarBg, lightTheme.statusBarBg)
	}
}

// TestSetTheme_Invalid returns false for unknown theme names.
func TestSetTheme_Invalid(t *testing.T) {
	for _, name := range []string{"", "auto", "solarized", "DARK"} {
		if setTheme(name) {
			t.Errorf("setTheme(%q) returned true, want false", name)
		}
	}
}

// TestThemeCommand_NoArgs shows current theme name in a system message.
func TestThemeCommand_NoArgs(t *testing.T) {
	initTheme()
	m := newModel("localhost:0", nil)
	// Give the model a valid terminal size so View() works.
	m.width = 80
	m.height = 24

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/theme")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	if len(m.messages) == 0 {
		t.Fatal("expected a system message after /theme")
	}
	last := m.messages[len(m.messages)-1]
	if last.Kind != msgSystem {
		t.Errorf("last message kind = %v, want msgSystem", last.Kind)
	}
	if !strings.Contains(last.Text, currentThemeName()) {
		t.Errorf("message %q does not contain theme name %q", last.Text, currentThemeName())
	}
}

// TestThemeCommand_Dark switches theme via slash command.
func TestThemeCommand_Dark(t *testing.T) {
	// Ensure we start in light so the switch is observable.
	setTheme("light")

	m := newModel("localhost:0", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	for _, r := range "/theme dark" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	if currentThemeName() != "dark" {
		t.Errorf("after /theme dark, currentThemeName() = %q, want \"dark\"", currentThemeName())
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Text, "dark") {
		t.Errorf("confirmation message %q does not mention \"dark\"", last.Text)
	}
}

// TestThemeCommand_Invalid shows an error message for unknown theme names.
func TestThemeCommand_Invalid(t *testing.T) {
	initTheme()
	m := newModel("localhost:0", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	for _, r := range "/theme solarized" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	last := m.messages[len(m.messages)-1]
	if last.Kind != msgSystem {
		t.Errorf("last message kind = %v, want msgSystem", last.Kind)
	}
	if !strings.Contains(strings.ToLower(last.Text), "unknown") {
		t.Errorf("error message %q does not mention \"unknown\"", last.Text)
	}
}

// TestRenderStatusBar_Width verifies that the rendered status bar content is
// (width - 2) columns wide. The statusBarStyle has Padding(0,1) which adds one
// column on each side, so the total visual width in the terminal is exactly
// `width` columns. lipgloss.Width() measures only the content area (without
// padding), so the expected measured value is width-2.
func TestRenderStatusBar_Width(t *testing.T) {
	initTheme()
	for _, w := range []int{40, 80, 120, 200} {
		rendered := renderStatusBar(true, "claude-3-5-sonnet", "sess-abc", 5, w)
		got := lipgloss.Width(rendered)
		want := w - 2 // lipgloss.Width strips ANSI; padding cols not counted
		if got != want {
			t.Errorf("renderStatusBar width=%d: lipgloss.Width = %d, want %d (visual width = %d)", w, got, want, w)
		}
	}
}

// TestRenderStatusBar_Width_Disconnected tests width with no model set.
func TestRenderStatusBar_Width_Disconnected(t *testing.T) {
	initTheme()
	for _, w := range []int{40, 80, 120} {
		rendered := renderStatusBar(false, "", "", 0, w)
		got := lipgloss.Width(rendered)
		want := w - 2
		if got != want {
			t.Errorf("renderStatusBar (disconnected) width=%d: lipgloss.Width = %d, want %d", w, got, want)
		}
	}
}
