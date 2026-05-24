package main

import (
	"testing"
)

func TestThemeRegistryHasDarkAndLight(t *testing.T) {
	registry := Themes()
	if _, ok := registry["dark"]; !ok {
		t.Errorf("missing 'dark' theme")
	}
	if _, ok := registry["light"]; !ok {
		t.Errorf("missing 'light' theme")
	}
}

func TestThemeStructFieldsPopulated(t *testing.T) {
	for name, th := range Themes() {
		if th.Name != name {
			t.Errorf("theme %q has Name=%q", name, th.Name)
		}
		if th.Text == "" {
			t.Errorf("theme %q has empty Text", name)
		}
		if th.Accent == "" {
			t.Errorf("theme %q has empty Accent", name)
		}
		if th.StateAwaiting == "" {
			t.Errorf("theme %q has empty StateAwaiting", name)
		}
	}
}

func TestSetThemeChangesActiveTheme(t *testing.T) {
	t.Cleanup(func() { setThemeV2("dark") })

	setThemeV2("dark")
	if activeThemeV2().Name != "dark" {
		t.Errorf("expected dark, got %q", activeThemeV2().Name)
	}
	setThemeV2("light")
	if activeThemeV2().Name != "light" {
		t.Errorf("expected light, got %q", activeThemeV2().Name)
	}
}

func TestSetThemeIgnoresUnknown(t *testing.T) {
	t.Cleanup(func() { setThemeV2("dark") })
	setThemeV2("dark")
	ok := setThemeV2("nonexistent")
	if ok {
		t.Errorf("setThemeV2 should return false for unknown name")
	}
	if activeThemeV2().Name != "dark" {
		t.Errorf("unknown name should not change active theme")
	}
}

func TestSetThemeCallsMarkdownInvalidator(t *testing.T) {
	t.Cleanup(func() {
		setThemeV2("dark")
		markdownInvalidationCount = 0
	})
	markdownInvalidationCount = 0
	setThemeV2("light")
	if markdownInvalidationCount != 1 {
		t.Errorf("expected 1 invalidation, got %d", markdownInvalidationCount)
	}
}
