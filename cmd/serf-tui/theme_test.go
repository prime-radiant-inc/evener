package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestApplyTheme_DarkAndLightDiffer checks that dark and light themes produce
// different background colors.
func TestApplyTheme_DarkAndLightDiffer(t *testing.T) {
	if darkThemeV2.BgRaised == lightThemeV2.BgRaised {
		t.Errorf("dark and light BgRaised are the same: %q", darkThemeV2.BgRaised)
	}
}

// TestInitTheme_SetsStyles verifies that after initTheme the style vars are
// non-zero (i.e. actually populated).
func TestInitTheme_SetsStyles(t *testing.T) {
	initTheme()
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
	}
	for _, tt := range tests {
		rendered := tt.style.Render(tt.input)
		if rendered == "" {
			t.Errorf("%s.Render(%q) returned empty string", tt.name, tt.input)
		}
	}
}

func TestTUIStylesRenderSelectedRow(t *testing.T) {
	withTestColorProfile(t)
	styles := defaultTUIStyles()
	got := styles.Selected.Render("selected")
	if got == "selected" {
		t.Fatal("selected style should add terminal styling")
	}
}

func withTestColorProfile(t *testing.T) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previous)
	})
}

// TestSetTheme_Dark switches to dark theme and checks the name.
func TestSetTheme_Dark(t *testing.T) {
	setTheme("light")

	ok := setTheme("dark")
	if !ok {
		t.Fatal("setTheme(\"dark\") returned false")
	}
	if currentThemeName() != "dark" {
		t.Errorf("currentThemeName() = %q, want %q", currentThemeName(), "dark")
	}
	if activeThemeV2().BgRaised != darkThemeV2.BgRaised {
		t.Errorf("activeThemeV2().BgRaised = %q, want %q", activeThemeV2().BgRaised, darkThemeV2.BgRaised)
	}
}

// TestSetTheme_Light switches to light theme and checks the name.
func TestSetTheme_Light(t *testing.T) {
	setTheme("dark")

	ok := setTheme("light")
	if !ok {
		t.Fatal("setTheme(\"light\") returned false")
	}
	if currentThemeName() != "light" {
		t.Errorf("currentThemeName() = %q, want %q", currentThemeName(), "light")
	}
	if activeThemeV2().BgRaised != lightThemeV2.BgRaised {
		t.Errorf("activeThemeV2().BgRaised = %q, want %q", activeThemeV2().BgRaised, lightThemeV2.BgRaised)
	}
}

func TestSetTheme_System(t *testing.T) {
	setTheme("dark")

	ok := setTheme("system")
	if !ok {
		t.Fatal("setTheme(\"system\") returned false")
	}
	if currentThemeName() != "system" {
		t.Errorf("currentThemeName() = %q, want %q", currentThemeName(), "system")
	}
}

func TestThemePreferencePersistsInStateDir(t *testing.T) {
	stateDir := t.TempDir()
	if !setThemeAndPersist(stateDir, "light") {
		t.Fatal("setThemeAndPersist(light) returned false")
	}
	if got, ok := loadThemePreference(stateDir); !ok || got != "light" {
		t.Fatalf("stored theme=%q ok=%v, want light", got, ok)
	}

	setTheme("dark")
	initThemeFromStateDir(stateDir)
	if currentThemeName() != "light" {
		t.Fatalf("theme after initThemeFromStateDir=%q, want light", currentThemeName())
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

// TestThemeCommand_OpensPicker verifies that /theme opens the theme picker.
func TestThemeCommand_OpensPicker(t *testing.T) {
	initTheme()
	m := newModel("localhost:0", "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	for _, r := range "/theme" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	if m.themePicker == nil {
		t.Fatal("expected themePicker to be non-nil after /theme")
	}
}

// TestThemeCommand_PickerSelectDark selects dark via the picker.
func TestThemeCommand_PickerSelectDark(t *testing.T) {
	setTheme("light")

	m := newModel("localhost:0", "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	// Open picker.
	for _, r := range "/theme" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	if m.themePicker == nil {
		t.Fatal("picker not opened")
	}

	// The picker starts at the active theme; ensure we're on dark.
	for themePickerItems[m.themePicker.cursor] != "dark" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	if m.themePicker != nil {
		t.Error("themePicker should be nil after selection")
	}
	if currentThemeName() != "dark" {
		t.Errorf("currentThemeName() = %q, want \"dark\"", currentThemeName())
	}
	// Should have a confirmation system message.
	if len(m.messages) == 0 {
		t.Fatal("expected confirmation message")
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Text, "dark") {
		t.Errorf("confirmation message %q does not mention \"dark\"", last.Text)
	}
}

// TestThemeCommand_PickerCancel cancels the picker without changing theme.
func TestThemeCommand_PickerCancel(t *testing.T) {
	setTheme("light")
	initialMsg := 0

	m := newModel("localhost:0", "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	initialMsg = len(m.messages)

	for _, r := range "/theme" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	// Press escape to cancel.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(model)

	if m.themePicker != nil {
		t.Error("themePicker should be nil after escape")
	}
	if currentThemeName() != "light" {
		t.Errorf("theme changed on cancel: got %q, want \"light\"", currentThemeName())
	}
	if len(m.messages) != initialMsg {
		t.Errorf("cancel should not add messages: had %d, now %d", initialMsg, len(m.messages))
	}
}

// TestThemePicker_InitialCursor verifies the picker pre-selects the active theme.
func TestThemePicker_InitialCursor(t *testing.T) {
	setTheme("system")
	p := newThemePicker()
	want := 0 // "system" is index 0
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (system)", p.cursor, want)
	}

	setTheme("light")
	p = newThemePicker()
	want = 2 // "light" is index 2
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (light)", p.cursor, want)
	}

	setTheme("dark")
	p = newThemePicker()
	want = 1 // "dark" is index 1
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (dark)", p.cursor, want)
	}
}

// TestThemePicker_ViewContainsThemes verifies the picker View shows both themes.
func TestThemePicker_ViewContainsThemes(t *testing.T) {
	initTheme()
	p := newThemePicker()
	view := p.View()
	for _, name := range themePickerItems {
		if !strings.Contains(view, name) {
			t.Errorf("picker view missing theme %q", name)
		}
	}
}

func TestThemePickerRendersAsPopupPane(t *testing.T) {
	withTestColorProfile(t)
	setTheme("dark")
	p := newThemePicker()

	view := p.View()
	if !strings.Contains(view, "\x1b[") {
		t.Fatalf("theme picker popup should render terminal styling:\n%s", view)
	}
	plain := ansiPattern.ReplaceAllString(view, "")
	if !strings.Contains(plain, "  Select theme") {
		t.Fatalf("theme picker popup should have pane padding:\n%s", plain)
	}
}

// TestRenderStatusBar_Width verifies that the rendered status bar is exactly
// terminal-width columns wide (no padding — spacing is embedded in the content string).
func TestRenderStatusBar_Width(t *testing.T) {
	initTheme()
	for _, w := range []int{40, 80, 120, 200} {
		rendered := renderStandaloneStatusBar(true, "claude-3-5-sonnet", "sess-abc", "", 5, 42000, 200000, false, 0, 0, false, w)
		got := lipgloss.Width(rendered)
		if got != w {
			t.Errorf("renderStatusBar width=%d: lipgloss.Width = %d, want %d", w, got, w)
		}
	}
}

// TestRenderStatusBar_Width_Disconnected tests width with no model set.
func TestRenderStatusBar_Width_Disconnected(t *testing.T) {
	initTheme()
	for _, w := range []int{40, 80, 120} {
		rendered := renderStandaloneStatusBar(false, "", "", "", 0, 0, 0, false, 0, 0, false, w)
		got := lipgloss.Width(rendered)
		if got != w {
			t.Errorf("renderStatusBar (disconnected) width=%d: lipgloss.Width = %d, want %d", w, got, w)
		}
	}
}

// TestWrapText_FitsOnOneLine checks no wrapping when text fits.
func TestWrapText_FitsOnOneLine(t *testing.T) {
	lines := wrapText("hello world", 20, 20)
	if len(lines) != 1 || lines[0] != "hello world" {
		t.Errorf("got %v, want [\"hello world\"]", lines)
	}
}

// TestWrapText_WrapsAtWordBoundary checks wrapping splits on spaces.
func TestWrapText_WrapsAtWordBoundary(t *testing.T) {
	lines := wrapText("hello world foo", 11, 20)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}
	if lines[0] != "hello world" {
		t.Errorf("first line = %q, want \"hello world\"", lines[0])
	}
	if lines[1] != "foo" {
		t.Errorf("second line = %q, want \"foo\"", lines[1])
	}
}

// TestWrapText_Empty returns nil for empty input.
func TestWrapText_Empty(t *testing.T) {
	if lines := wrapText("", 20, 20); lines != nil {
		t.Errorf("got %v, want nil", lines)
	}
}

// TestWrapText_MultiLine checks multiple wraps with different first/cont budgets.
func TestWrapText_MultiLine(t *testing.T) {
	lines := wrapText("aaa bbb ccc ddd eee", 7, 7)
	for _, l := range lines {
		if len(l) > 7 {
			t.Errorf("line %q exceeds budget 7", l)
		}
	}
	joined := strings.Join(lines, " ")
	if joined != "aaa bbb ccc ddd eee" {
		t.Errorf("rejoined = %q, want original text", joined)
	}
}
