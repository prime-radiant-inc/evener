package tuitheme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/muesli/termenv"
)

func TestValidThemeName(t *testing.T) {
	for _, name := range []string{"system", "dark", "light"} {
		if !validThemeName(name) {
			t.Errorf("validThemeName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "auto", "DARK", "solarized"} {
		if validThemeName(name) {
			t.Errorf("validThemeName(%q) = true, want false", name)
		}
	}
}

func TestThemePreferencePath_EmptyStateDir(t *testing.T) {
	if got := themePreferencePath("  "); got != "" {
		t.Fatalf("themePreferencePath(blank) = %q, want empty", got)
	}
	got := themePreferencePath("/state")
	want := filepath.Join("/state", "tui", "preferences.json")
	if got != want {
		t.Fatalf("themePreferencePath = %q, want %q", got, want)
	}
}

func TestLoadThemePreference_Failures(t *testing.T) {
	// Empty state dir → no path.
	if _, ok := LoadThemePreference(""); ok {
		t.Fatal("LoadThemePreference(\"\") ok = true, want false")
	}

	// Missing file → read error.
	dir := t.TempDir()
	if _, ok := LoadThemePreference(dir); ok {
		t.Fatal("LoadThemePreference(missing) ok = true, want false")
	}

	// Malformed JSON → unmarshal error.
	prefPath := themePreferencePath(dir)
	if err := os.MkdirAll(filepath.Dir(prefPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(prefPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := LoadThemePreference(dir); ok {
		t.Fatal("LoadThemePreference(bad json) ok = true, want false")
	}

	// Valid JSON but invalid theme name → rejected.
	data, _ := json.Marshal(tuiPreferences{Theme: "solarized"})
	if err := os.WriteFile(prefPath, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := LoadThemePreference(dir); ok {
		t.Fatal("LoadThemePreference(invalid theme) ok = true, want false")
	}

	// Valid JSON and valid theme → accepted.
	data, _ = json.Marshal(tuiPreferences{Theme: "light"})
	if err := os.WriteFile(prefPath, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, ok := LoadThemePreference(dir); !ok || got != "light" {
		t.Fatalf("LoadThemePreference(valid) = %q ok=%v, want light true", got, ok)
	}
}

func TestSaveThemePreference_NoOpCases(t *testing.T) {
	// Empty state dir is a no-op that returns nil.
	if err := saveThemePreference("", "dark"); err != nil {
		t.Fatalf("saveThemePreference(blank dir) = %v, want nil", err)
	}
	// Invalid theme name is a no-op that returns nil and writes nothing.
	dir := t.TempDir()
	if err := saveThemePreference(dir, "solarized"); err != nil {
		t.Fatalf("saveThemePreference(invalid name) = %v, want nil", err)
	}
	if _, err := os.Stat(themePreferencePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("saveThemePreference(invalid name) wrote a file; stat err = %v", err)
	}
}

func TestColorToHex(t *testing.T) {
	if got := colorToHex(nil); got != "" {
		t.Fatalf("colorToHex(nil) = %q, want empty", got)
	}
	if got := colorToHex(termenv.RGBColor("#abcdef")); got != "#abcdef" {
		t.Fatalf("colorToHex(#abcdef) = %q, want #abcdef", got)
	}
	// Short #rgb form expands to #rrggbb.
	if got := colorToHex(termenv.RGBColor("#0f0")); got != "#00ff00" {
		t.Fatalf("colorToHex(#0f0) = %q, want #00ff00", got)
	}
	// Malformed RGB string → empty.
	if got := colorToHex(termenv.RGBColor("not-a-color")); got != "" {
		t.Fatalf("colorToHex(malformed) = %q, want empty", got)
	}
	// ANSI color routes through the conversion path and yields a hex string.
	got := colorToHex(termenv.ANSIColor(1))
	if len(got) != 7 || got[0] != '#' {
		t.Fatalf("colorToHex(ANSIColor) = %q, want #rrggbb", got)
	}
}

func TestDetectSystemThemeKey_FromProbe(t *testing.T) {
	savedBg := probedOriginalBg
	t.Cleanup(func() { probedOriginalBg = savedBg })

	probedOriginalBg = "#000000"
	if got := detectSystemThemeKey(); got != "dark" {
		t.Fatalf("detectSystemThemeKey(black bg) = %q, want dark", got)
	}
	probedOriginalBg = "#ffffff"
	if got := detectSystemThemeKey(); got != "light" {
		t.Fatalf("detectSystemThemeKey(white bg) = %q, want light", got)
	}
}

func TestActiveTheme_FallsBackToDark(t *testing.T) {
	saved := activeThemeKey
	t.Cleanup(func() { activeThemeKey = saved })

	activeThemeKey = "no-such-theme"
	if got := ActiveTheme(); got.Name != themeRegistry["dark"].Name {
		t.Fatalf("ActiveTheme() with bad key = %q, want dark fallback", got.Name)
	}
}

func TestSetMarkdownInvalidator_InvokedOnThemeChange(t *testing.T) {
	saved := markdownInvalidator
	savedKey := activeThemeKey
	t.Cleanup(func() {
		markdownInvalidator = saved
		activeThemeKey = savedKey
	})

	calls := 0
	SetMarkdownInvalidator(func() { calls++ })
	if !ApplyThemeName("light") {
		t.Fatal("ApplyThemeName(light) = false, want true")
	}
	if calls != 1 {
		t.Fatalf("markdownInvalidator called %d times, want 1", calls)
	}
	// Unknown theme key does not swap and does not invalidate.
	if ApplyThemeName("no-such-theme") {
		t.Fatal("ApplyThemeName(bad) = true, want false")
	}
	if calls != 1 {
		t.Fatalf("markdownInvalidator called %d times after bad apply, want 1", calls)
	}
}

// TerminalBg helpers early-return when stdout is not a TTY (the test
// environment). Exercising the guard keeps the no-op path honest; the
// escape-sequence body requires a real terminal and is covered by the
// TUI e2e harness.
func TestTerminalBgHelpers_NoTTYNoOp(t *testing.T) {
	if stdoutIsTerminal() {
		t.Skip("stdout is a TTY; guard path not exercised in this environment")
	}
	ApplyTerminalBg()
	ResetTerminalBg()
	ProbeTerminalDefaults()
}
