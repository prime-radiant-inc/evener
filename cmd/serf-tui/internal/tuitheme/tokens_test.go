package tuitheme

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// contrastRatio computes WCAG 2.x contrast between two hex colors
// formatted as "#rrggbb". Returns ratio ≥1.0 (higher = more contrast).
func contrastRatio(fg, bg string) float64 {
	lf := relativeLuminance(fg)
	lb := relativeLuminance(bg)
	if lf < lb {
		lf, lb = lb, lf
	}
	return (lf + 0.05) / (lb + 0.05)
}

func relativeLuminance(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0
	}
	parseChan := func(s string) float64 {
		v, _ := strconv.ParseInt(s, 16, 32)
		c := float64(v) / 255.0
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r := parseChan(hex[0:2])
	g := parseChan(hex[2:4])
	b := parseChan(hex[4:6])
	return 0.2126*r + 0.7152*g + 0.0722*b
}

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
	t.Cleanup(func() { ApplyThemeName("dark") })

	ApplyThemeName("dark")
	if ActiveTheme().Name != "dark" {
		t.Errorf("expected dark, got %q", ActiveTheme().Name)
	}
	ApplyThemeName("light")
	if ActiveTheme().Name != "light" {
		t.Errorf("expected light, got %q", ActiveTheme().Name)
	}
}

func TestSetThemeIgnoresUnknown(t *testing.T) {
	t.Cleanup(func() { ApplyThemeName("dark") })
	ApplyThemeName("dark")
	ok := ApplyThemeName("nonexistent")
	if ok {
		t.Errorf("ApplyThemeName should return false for unknown name")
	}
	if ActiveTheme().Name != "dark" {
		t.Errorf("unknown name should not change active theme")
	}
}

func TestSetThemeCallsMarkdownInvalidator(t *testing.T) {
	// Save and restore the real invalidator so we can intercept calls.
	saved := markdownInvalidator
	t.Cleanup(func() {
		markdownInvalidator = saved
		ApplyThemeName("dark")
		markdownInvalidationCount = 0
	})
	markdownInvalidationCount = 0
	markdownInvalidator = func() { markdownInvalidationCount++ }
	ApplyThemeName("light")
	if markdownInvalidationCount != 1 {
		t.Errorf("expected 1 invalidation, got %d", markdownInvalidationCount)
	}
}

func TestNoTokenIsEmpty(t *testing.T) {
	for name, th := range Themes() {
		fields := map[string]lipgloss.Color{
			"Bg":                  th.Bg,
			"BgRaised":            th.BgRaised,
			"SurfaceSecondary":    th.SurfaceSecondary,
			"Rule":                th.Rule,
			"RuleSoft":            th.RuleSoft,
			"Text":                th.Text,
			"TextMuted":           th.TextMuted,
			"TextDim":             th.TextDim,
			"TextGhost":           th.TextGhost,
			"Accent":              th.Accent,
			"AccentSecondary":     th.AccentSecondary,
			"StateAwaiting":       th.StateAwaiting,
			"StateProcessing":     th.StateProcessing,
			"StateWarning":        th.StateWarning,
			"StateIdle":           th.StateIdle,
			"StateEnded":          th.StateEnded,
			"StateSubagent":       th.StateSubagent,
			"BtnPrimaryText":      th.BtnPrimaryText,
			"StateAwaitingTint":   th.StateAwaitingTint,
			"StateProcessingTint": th.StateProcessingTint,
			"StateWarningTint":    th.StateWarningTint,
			"StateIdleTint":       th.StateIdleTint,
			"AccentTint":          th.AccentTint,
		}
		for field, c := range fields {
			if string(c) == "" {
				t.Errorf("theme %q field %q is empty", name, field)
			}
		}
	}
}

func TestBgNotEqualText(t *testing.T) {
	for name, th := range Themes() {
		if string(th.Bg) == string(th.Text) {
			t.Errorf("theme %q: Bg == Text (%q); content invisible", name, th.Bg)
		}
	}
}

// TestTextTiersMeetMinContrast guards against the text tiers regressing
// back to unreadable values. Thresholds:
//   - Text:      ≥7.0  (WCAG AAA body)
//   - TextMuted: ≥4.5  (WCAG AA body)
//   - TextDim:   ≥3.0  (WCAG AA large/UI)
//   - TextGhost: ≥2.5  (chrome floor — visible, recedes)
//   - StateEnded:≥2.5  (used as text in some panels)
//
// Computed against the theme's own Bg token.
func TestTextTiersMeetMinContrast(t *testing.T) {
	cases := []struct {
		field string
		min   float64
		get   func(Theme) lipgloss.Color
	}{
		{"Text", 7.0, func(t Theme) lipgloss.Color { return t.Text }},
		{"TextMuted", 4.5, func(t Theme) lipgloss.Color { return t.TextMuted }},
		{"TextDim", 3.0, func(t Theme) lipgloss.Color { return t.TextDim }},
		{"TextGhost", 2.5, func(t Theme) lipgloss.Color { return t.TextGhost }},
		{"StateEnded", 2.5, func(t Theme) lipgloss.Color { return t.StateEnded }},
	}
	for name, th := range Themes() {
		for _, c := range cases {
			ratio := contrastRatio(string(c.get(th)), string(th.Bg))
			if ratio < c.min {
				t.Errorf("theme %q field %q: contrast %.2f:1 against Bg (need ≥%.1f)", name, c.field, ratio, c.min)
			}
		}
	}
}
