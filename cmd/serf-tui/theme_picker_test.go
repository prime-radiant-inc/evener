package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestThemePickerUsesOverlayBorder(t *testing.T) {
	withTestColorProfile(t)
	p := newThemePicker()
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
	tuitheme.SetTheme("system")
	p := newThemePicker()
	want := 0 // "system" is index 0
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (system)", p.cursor, want)
	}

	tuitheme.SetTheme("light")
	p = newThemePicker()
	want = 2 // "light" is index 2
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (light)", p.cursor, want)
	}

	tuitheme.SetTheme("dark")
	p = newThemePicker()
	want = 1 // "dark" is index 1
	if p.cursor != want {
		t.Errorf("cursor = %d, want %d (dark)", p.cursor, want)
	}
}

// TestThemePicker_ViewContainsThemes verifies the picker View shows both themes.
func TestThemePicker_ViewContainsThemes(t *testing.T) {
	tuitheme.InitTheme()
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
	tuitheme.SetTheme("dark")
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
