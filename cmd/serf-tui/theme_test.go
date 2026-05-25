package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestApplyTheme_DarkAndLightDiffer checks that dark and light themes produce
// different background colors.
func TestApplyTheme_DarkAndLightDiffer(t *testing.T) {
	if darkTheme.BgRaised == lightTheme.BgRaised {
		t.Errorf("dark and light BgRaised are the same: %q", darkTheme.BgRaised)
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
	if activeTheme().BgRaised != darkTheme.BgRaised {
		t.Errorf("activeTheme().BgRaised = %q, want %q", activeTheme().BgRaised, darkTheme.BgRaised)
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
	if activeTheme().BgRaised != lightTheme.BgRaised {
		t.Errorf("activeTheme().BgRaised = %q, want %q", activeTheme().BgRaised, lightTheme.BgRaised)
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
