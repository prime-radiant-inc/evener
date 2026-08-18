package tuitheme

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/muesli/termenv"
)

// FuzzColorParsing drives the package's real color-decode seam: colorToHex
// canonicalizes a termenv RGB color string ("#rgb"/"#rrggbb") and
// relativeLuminanceHex parses the resulting hex via strconv.ParseInt to compute
// WCAG luminance (the basis for system dark/light detection). The parse surface
// is thin (this package is mostly static theme tables), so this targets the only
// real decoders. Oracle: no-panic floor plus a finiteness invariant — luminance
// must never be NaN/Inf (a non-finite value would corrupt the <0.5 dark/light
// comparison). Note: the documented [0,1] range is NOT asserted here because it
// is not enforced for malformed hex — strconv.ParseInt accepts a signed channel
// like "-1" (e.g. input "#00-100"), and colorToHex passes through any 7-char
// '#'-prefixed string without validating its digits. Real callers only ever feed
// a terminal's valid hex, so this stays latent (see report).
func FuzzColorParsing(f *testing.F) {
	seeds := []string{
		"#000000", "#ffffff", "#abc", "#6b9ec8", "#fff",
		"", "#", "#zz", "#12", "#1234567", "not-a-color",
		"#0f0f11", "#GGGGGG", "rgb(1,2,3)", "#00000g",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	var wholePackage sync.Once
	f.Fuzz(func(t *testing.T, raw string) {
		wholePackage.Do(func() { exerciseThemePackage(t) })
		// colorToHex over the RGBColor branch (the string-typed decode path).
		hex := colorToHex(termenv.RGBColor(raw))
		if hex != "" {
			if len(hex) != 7 || hex[0] != '#' {
				t.Fatalf("colorToHex returned non-canonical %q for input %q", hex, raw)
			}
		}

		lum := relativeLuminanceHex(raw)
		if math.IsNaN(lum) || math.IsInf(lum, 0) {
			t.Fatalf("relativeLuminanceHex(%q) = %v is non-finite", raw, lum)
		}
		// Canonicalized hex must also yield a finite luminance.
		if hex != "" {
			lum2 := relativeLuminanceHex(hex)
			if math.IsNaN(lum2) || math.IsInf(lum2, 0) {
				t.Fatalf("relativeLuminanceHex(canonical %q) = %v is non-finite", hex, lum2)
			}
		}
	})
}

func exerciseThemePackage(t *testing.T) {
	savedKey, savedName := activeThemeKey, activeThemeName
	savedInvalidator := markdownInvalidator
	savedFg, savedBg, savedDone := probedOriginalFg, probedOriginalBg, probedDone
	savedTerminal, savedForeground := terminalIsStdout, terminalForeground
	savedBackground, savedDark := terminalBackground, terminalHasDarkBg
	savedSetFg, savedSetBg := terminalSetForeground, terminalSetBackground
	savedWrite := terminalWriteString
	savedRead, savedMkdir := readThemeFile, makeThemeDirs
	savedWriteFile, savedMarshal := writeThemeFile, marshalThemePrefs
	t.Cleanup(func() {
		activeThemeKey, activeThemeName = savedKey, savedName
		markdownInvalidator = savedInvalidator
		probedOriginalFg, probedOriginalBg, probedDone = savedFg, savedBg, savedDone
		terminalIsStdout, terminalForeground = savedTerminal, savedForeground
		terminalBackground, terminalHasDarkBg = savedBackground, savedDark
		terminalSetForeground, terminalSetBackground = savedSetFg, savedSetBg
		terminalWriteString = savedWrite
		readThemeFile, makeThemeDirs = savedRead, savedMkdir
		writeThemeFile, marshalThemePrefs = savedWriteFile, savedMarshal
	})

	_ = terminalIsStdout()
	_ = terminalForeground()
	_ = terminalBackground()
	_ = terminalHasDarkBg()
	terminalSetForeground(termenv.RGBColor("#000000"))
	terminalSetBackground(termenv.RGBColor("#ffffff"))
	terminalWriteString("")
	markdownInvalidator()

	terminalIsStdout = func() bool { return false }
	terminalHasDarkBg = func() bool { return true }
	terminalSetForeground = func(termenv.Color) {}
	terminalSetBackground = func(termenv.Color) {}
	terminalWriteString = func(string) {}

	_ = Themes()
	SetMarkdownInvalidator(func() {})
	if ApplyThemeName("missing") || !ApplyThemeName("dark") {
		t.Fatal("theme registry acceptance mismatch")
	}
	activeThemeKey = "missing"
	_ = ActiveTheme()
	activeThemeKey = "dark"
	_ = DefaultTUIStyles()
	InitTheme()
	for _, name := range []string{"dark", "light", "system"} {
		if !SetTheme(name) {
			t.Fatalf("SetTheme(%q) rejected", name)
		}
	}
	if SetTheme("missing") {
		t.Fatal("SetTheme accepted invalid name")
	}
	_ = CurrentThemeName()

	dir := t.TempDir()
	InitThemeFromStateDir(dir)
	if !SetThemeAndPersist(dir, "light") || SetThemeAndPersist(dir, "missing") {
		t.Fatal("SetThemeAndPersist acceptance mismatch")
	}
	InitThemeFromStateDir(dir)
	for _, name := range []string{"system", "dark", "light"} {
		if !validThemeName(name) {
			t.Fatalf("validThemeName(%q) rejected", name)
		}
	}
	if validThemeName("missing") || themePreferencePath(" \t") != "" {
		t.Fatal("invalid preference input accepted")
	}
	_ = themePreferencePath("  " + dir + "  ")
	if _, ok := LoadThemePreference(""); ok {
		t.Fatal("empty preference path loaded")
	}

	prefPath := themePreferencePath(dir)
	readThemeFile = func(string) ([]byte, error) { return nil, errors.New("read") }
	_, _ = LoadThemePreference(dir)
	readThemeFile = func(string) ([]byte, error) { return []byte("{"), nil }
	_, _ = LoadThemePreference(dir)
	readThemeFile = func(string) ([]byte, error) { return []byte(`{"theme":"missing"}`), nil }
	_, _ = LoadThemePreference(dir)
	readThemeFile = func(string) ([]byte, error) { return []byte(`{"theme":" dark "}`), nil }
	if got, ok := LoadThemePreference(dir); !ok || got != "dark" {
		t.Fatalf("valid preference = %q, %v", got, ok)
	}
	readThemeFile = os.ReadFile

	if err := saveThemePreference("", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := saveThemePreference(dir, "missing"); err != nil {
		t.Fatal(err)
	}
	makeThemeDirs = func(string, os.FileMode) error { return errors.New("mkdir") }
	_ = saveThemePreference(dir, "dark")
	makeThemeDirs = os.MkdirAll
	marshalThemePrefs = func(any, string, string) ([]byte, error) { return nil, errors.New("marshal") }
	_ = saveThemePreference(dir, "dark")
	marshalThemePrefs = json.MarshalIndent
	writeThemeFile = func(string, []byte, os.FileMode) error { return errors.New("write") }
	_ = saveThemePreference(dir, "dark")
	writeThemeFile = os.WriteFile
	if err := saveThemePreference(dir, "dark"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(prefPath)); err != nil {
		t.Fatal(err)
	}

	probedDone, probedOriginalBg = true, ""
	if ProbeTerminalDefaults() {
		t.Fatal("empty cached probe reported usable")
	}
	probedOriginalBg = "#ffffff"
	if !ProbeTerminalDefaults() {
		t.Fatal("cached probe not reused")
	}
	probedDone, probedOriginalFg, probedOriginalBg = false, "", ""
	if ProbeTerminalDefaults() {
		t.Fatal("non-terminal probe reported usable")
	}
	terminalIsStdout = func() bool { return true }
	terminalForeground = func() termenv.Color { return nil }
	terminalBackground = func() termenv.Color { return nil }
	probedDone = false
	if ProbeTerminalDefaults() {
		t.Fatal("nil terminal colors reported usable")
	}
	terminalForeground = func() termenv.Color { return termenv.RGBColor("#123456") }
	terminalBackground = func() termenv.Color { return termenv.RGBColor("#ffffff") }
	probedDone = false
	if !ProbeTerminalDefaults() {
		t.Fatal("terminal colors not captured")
	}
	for _, bg := range []string{"#000000", "#ffffff"} {
		probedOriginalBg = bg
		_ = detectSystemThemeKey()
	}
	probedOriginalBg = ""
	terminalHasDarkBg = func() bool { return true }
	_ = detectSystemThemeKey()
	terminalHasDarkBg = func() bool { return false }
	_ = detectSystemThemeKey()

	terminalIsStdout = func() bool { return false }
	ApplyTerminalBg()
	ResetTerminalBg()
	_ = stdoutIsTerminal()
	terminalIsStdout = func() bool { return true }
	ApplyTerminalBg()
	probedOriginalFg, probedOriginalBg = "#010203", "#040506"
	ResetTerminalBg()
	probedOriginalFg, probedOriginalBg = "", ""
	ResetTerminalBg()

	_ = colorToHex(nil)
	_ = colorToHex(termenv.RGBColor("bad"))
	_ = colorToHex(termenv.RGBColor("#abc"))
	_ = colorToHex(termenv.ANSIColor(1))
	_ = relativeLuminanceHex("bad")
	_ = relativeLuminanceHex("#zzzzzz")
	_ = relativeLuminanceHex("#010101")
	_ = relativeLuminanceHex("#ffffff")
}
