package main

import (
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

func TestCommandPaletteShowsSlashItems(t *testing.T) {
	withTestColorProfile(t)
	entries := []commandPaletteEntry{
		{
			Item:    tuipick.PickerPanelItem{ID: "command:spawn", Label: "/spawn", Detail: "New session"},
			Kind:    commandPaletteCommand,
			Command: "spawn",
		},
	}
	p := newCommandPalette("Commands", entries, 80)
	got := p.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "/spawn") || !strings.Contains(plain, "New session") {
		t.Errorf("palette item missing in: %q", plain)
	}
	if !strings.Contains(plain, "╭") {
		t.Errorf("palette should use Overlay primitive (rounded border): %q", plain)
	}
}
