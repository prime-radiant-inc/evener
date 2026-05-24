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
