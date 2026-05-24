package main

import (
	"strings"
	"testing"
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
