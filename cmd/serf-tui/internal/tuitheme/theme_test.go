package tuitheme

import (
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

// TestInitTheme_SetsStyles verifies that after InitTheme the style vars are
// non-zero (i.e. actually populated).
func TestInitTheme_SetsStyles(t *testing.T) {
	InitTheme()
	tests := []struct {
		name  string
		style lipgloss.Style
		input string
	}{
		{"statusBarStyle", statusBarStyle, "x"},
		{"UserBlockStyle", UserBlockStyle, "x"},
		{"ThinkingStyle", ThinkingStyle, "x"},
		{"CommunicateStyle", CommunicateStyle, "x"},
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
	styles := DefaultTUIStyles()
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
	SetTheme("light")

	ok := SetTheme("dark")
	if !ok {
		t.Fatal("SetTheme(\"dark\") returned false")
	}
	if CurrentThemeName() != "dark" {
		t.Errorf("CurrentThemeName() = %q, want %q", CurrentThemeName(), "dark")
	}
	if ActiveTheme().BgRaised != darkTheme.BgRaised {
		t.Errorf("ActiveTheme().BgRaised = %q, want %q", ActiveTheme().BgRaised, darkTheme.BgRaised)
	}
}

// TestSetTheme_Light switches to light theme and checks the name.
func TestSetTheme_Light(t *testing.T) {
	SetTheme("dark")

	ok := SetTheme("light")
	if !ok {
		t.Fatal("SetTheme(\"light\") returned false")
	}
	if CurrentThemeName() != "light" {
		t.Errorf("CurrentThemeName() = %q, want %q", CurrentThemeName(), "light")
	}
	if ActiveTheme().BgRaised != lightTheme.BgRaised {
		t.Errorf("ActiveTheme().BgRaised = %q, want %q", ActiveTheme().BgRaised, lightTheme.BgRaised)
	}
}

func TestSetTheme_System(t *testing.T) {
	SetTheme("dark")

	ok := SetTheme("system")
	if !ok {
		t.Fatal("SetTheme(\"system\") returned false")
	}
	if CurrentThemeName() != "system" {
		t.Errorf("CurrentThemeName() = %q, want %q", CurrentThemeName(), "system")
	}
}

func TestThemePreferencePersistsInStateDir(t *testing.T) {
	stateDir := t.TempDir()
	if !SetThemeAndPersist(stateDir, "light") {
		t.Fatal("SetThemeAndPersist(light) returned false")
	}
	if got, ok := LoadThemePreference(stateDir); !ok || got != "light" {
		t.Fatalf("stored theme=%q ok=%v, want light", got, ok)
	}

	SetTheme("dark")
	InitThemeFromStateDir(stateDir)
	if CurrentThemeName() != "light" {
		t.Fatalf("theme after InitThemeFromStateDir=%q, want light", CurrentThemeName())
	}
}

// TestSetTheme_Invalid returns false for unknown theme names.
func TestSetTheme_Invalid(t *testing.T) {
	for _, name := range []string{"", "auto", "solarized", "DARK"} {
		if SetTheme(name) {
			t.Errorf("SetTheme(%q) returned true, want false", name)
		}
	}
}
