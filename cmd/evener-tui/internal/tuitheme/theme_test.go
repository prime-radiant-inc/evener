package tuitheme

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestApplyTheme_DarkAndLightDiffer checks that dark and light themes produce
// different color values for several key tokens.
func TestApplyTheme_DarkAndLightDiffer(t *testing.T) {
	if darkTheme.BgRaised == lightTheme.BgRaised {
		t.Errorf("dark and light BgRaised are the same: %q", darkTheme.BgRaised)
	}
	if darkTheme.Bg == lightTheme.Bg {
		t.Errorf("dark and light Bg are the same: %q", darkTheme.Bg)
	}
	if darkTheme.Text == lightTheme.Text {
		t.Errorf("dark and light Text are the same: %q", darkTheme.Text)
	}
}

// TestInitTheme_SetsStyles verifies that after InitTheme the style vars
// actually carry their configured attributes (not zero-value styles).
// With TrueColor enabled, every style that has a color or padding set must
// produce a rendered string that differs from the plain input.
func TestInitTheme_SetsStyles(t *testing.T) {
	withTestColorProfile(t)
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
		if rendered == tt.input {
			t.Errorf("%s.Render(%q) returned plain input; expected ANSI-styled output after InitTheme", tt.name, tt.input)
		}
	}
}

// TestTUIStylesRenderSelectedRow verifies that the Selected style uses the
// correct theme tokens (Text foreground + SurfaceSecondary background + Bold).
func TestTUIStylesRenderSelectedRow(t *testing.T) {
	withTestColorProfile(t)
	th := ActiveTheme()
	styles := DefaultTUIStyles()
	got := styles.Selected.Render("selected")
	want := lipgloss.NewStyle().Foreground(th.Text).Background(th.SurfaceSecondary).Bold(true).Render("selected")
	if got != want {
		t.Errorf("Selected.Render(%q) = %q, want %q (Text+SurfaceSecondary+Bold)", "selected", got, want)
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
	t.Cleanup(func() { SetTheme("dark") })
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
	t.Cleanup(func() { SetTheme("dark") })
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
	t.Cleanup(func() { SetTheme("dark") })
	SetTheme("dark")

	ok := SetTheme("system")
	if !ok {
		t.Fatal("SetTheme(\"system\") returned false")
	}
	if CurrentThemeName() != "system" {
		t.Errorf("CurrentThemeName() = %q, want %q", CurrentThemeName(), "system")
	}
	resolved := ActiveTheme().Name
	if resolved != "dark" && resolved != "light" {
		t.Errorf("ActiveTheme().Name = %q after SetTheme(\"system\"); want \"dark\" or \"light\"", resolved)
	}
}

func TestThemePreferencePersistsInStateDir(t *testing.T) {
	t.Cleanup(func() { SetTheme("dark") })
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
