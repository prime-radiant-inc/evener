package tuipick

import (
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestThemePickerUsesOverlayBorder(t *testing.T) {
	withTestColorProfile(t)
	p := NewThemePicker()
	got := p.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "╭") {
		t.Errorf("theme picker should use Overlay primitive (rounded border): %q", plain)
	}
	if !strings.Contains(plain, "Select theme") {
		t.Errorf("theme picker should show title 'Select theme': %q", plain)
	}
}

// TestThemePicker_InitialCursor verifies the picker pre-selects the active theme.
func TestThemePicker_InitialCursor(t *testing.T) {
	prev := tuitheme.CurrentThemeName()
	t.Cleanup(func() { tuitheme.SetTheme(prev) })

	tuitheme.SetTheme("system")
	p := NewThemePicker()
	want := 0 // "system" is index 0
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (system)", p.cursor, want)
	}

	tuitheme.SetTheme("light")
	p = NewThemePicker()
	want = 2 // "light" is index 2
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (light)", p.cursor, want)
	}

	tuitheme.SetTheme("dark")
	p = NewThemePicker()
	want = 1 // "dark" is index 1
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (dark)", p.cursor, want)
	}
}

// TestThemePicker_ViewContainsThemes verifies the picker View shows all expected themes.
func TestThemePicker_ViewContainsThemes(t *testing.T) {
	tuitheme.InitTheme()
	p := NewThemePicker()
	view := p.View()
	// Hardcoded so the test catches removals from ThemePickerItems regardless
	// of what the SUT iterates.
	wantThemes := []string{"system", "dark", "light"}
	for _, name := range wantThemes {
		if !strings.Contains(view, name) {
			t.Errorf("picker view missing theme %q", name)
		}
	}
}

func TestThemePickerRendersAsPopupPane(t *testing.T) {
	withTestColorProfile(t)
	tuitheme.SetTheme("dark")
	p := NewThemePicker()

	view := p.View()
	if !strings.Contains(view, "\x1b[") {
		t.Fatalf("theme picker popup should render terminal styling:\n%s", view)
	}
	plain := ansiPattern.ReplaceAllString(view, "")
	if !strings.Contains(plain, "  Select theme") {
		t.Fatalf("theme picker popup should have pane padding:\n%s", plain)
	}
}
