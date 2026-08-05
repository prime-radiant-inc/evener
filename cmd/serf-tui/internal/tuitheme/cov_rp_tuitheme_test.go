package tuitheme

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitThemeFromStateDir_FallsBackToSystem(t *testing.T) {
	saved := CurrentThemeName()
	t.Cleanup(func() { SetTheme(saved) })

	// A state dir with no preferences file falls back to the "system" theme.
	SetTheme("light")
	InitThemeFromStateDir(t.TempDir())
	if CurrentThemeName() != "system" {
		t.Fatalf("theme after Init with no pref = %q, want system", CurrentThemeName())
	}

	// An empty state dir also falls back to system.
	SetTheme("light")
	InitThemeFromStateDir("")
	if CurrentThemeName() != "system" {
		t.Fatalf("theme after Init with empty dir = %q, want system", CurrentThemeName())
	}
}

func TestSetThemeAndPersist_InvalidNameReturnsFalse(t *testing.T) {
	saved := CurrentThemeName()
	t.Cleanup(func() { SetTheme(saved) })

	dir := t.TempDir()
	if SetThemeAndPersist(dir, "chartreuse") {
		t.Fatal("SetThemeAndPersist with an invalid name should return false")
	}
	// Nothing is persisted for an invalid name.
	if _, err := os.Stat(filepath.Join(dir, "tui", "preferences.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid name should not persist a preferences file; stat err = %v", err)
	}
}

func TestDetectSystemThemeKey_ProbeUnavailableFallback(t *testing.T) {
	savedBg := probedOriginalBg
	t.Cleanup(func() { probedOriginalBg = savedBg })

	// With no probed background, detection falls back to termenv's own chain,
	// which must still yield one of the two valid keys.
	probedOriginalBg = ""
	got := detectSystemThemeKey()
	if got != "dark" && got != "light" {
		t.Fatalf("fallback detectSystemThemeKey = %q, want dark|light", got)
	}
}
