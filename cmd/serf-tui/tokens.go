// tokens.go — central theme registry for serf-tui.
//
// Established in the TUI deep UX pass (2026-05-24). The Theme struct
// holds every color and layout token; the `themes` registry binds names
// to Theme structs. Active theme is swapped via applyThemeName(name) which
// also invalidates the cached markdown renderer.
//
// To add a new theme: define a Theme struct literal, register it in
// themeRegistry. No other code changes needed.
package main

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Name string

	Bg, BgRaised, SurfaceSecondary lipgloss.Color
	Rule, RuleSoft                 lipgloss.Color

	Text, TextMuted, TextDim, TextGhost lipgloss.Color

	Accent, AccentSecondary lipgloss.Color
	StateAwaiting, StateProcessing,
	StateWarning, StateIdle, StateEnded,
	StateSubagent lipgloss.Color
	BtnPrimaryText lipgloss.Color

	StateAwaitingTint, StateProcessingTint,
	StateWarningTint, StateIdleTint,
	AccentTint lipgloss.Color

	IndentToolBody, IndentSubagent int
	GapTurn, GapSection            int
	ColumnDur                      int
	LeftBarGlyph, RuleGlyph        string
}

var themeRegistry = map[string]Theme{
	"dark":  darkTheme,
	"light": lightTheme,
}

func Themes() map[string]Theme {
	return themeRegistry
}

// activeThemeKey is the resolved registry key ("dark" or "light").
// Distinct from activeThemeName in styles.go which holds the user-picked value
// ("system", "dark", or "light") — "system" resolves to one of these two keys.
var activeThemeKey = "dark"

// markdownInvalidationCount is a test hook counting invalidator calls.
// For testing only — not part of the API surface.
var markdownInvalidationCount int

// markdownInvalidator is wired by message.go init; placeholder counts calls.
var markdownInvalidator = func() { markdownInvalidationCount++ }

// activeTheme returns the currently selected Theme.
func activeTheme() Theme {
	if th, ok := themeRegistry[activeThemeKey]; ok {
		return th
	}
	return themeRegistry["dark"]
}

// applyThemeName swaps the active theme by registry key. Not safe for
// concurrent use; called only from setTheme() on bubbletea's main goroutine.
func applyThemeName(name string) bool {
	if _, ok := themeRegistry[name]; !ok {
		return false
	}
	activeThemeKey = name
	markdownInvalidator()
	return true
}

var darkTheme = Theme{
	Name:                "dark",
	Bg:                  lipgloss.Color("#0a0a0e"),
	BgRaised:            lipgloss.Color("#16161e"),
	SurfaceSecondary:    lipgloss.Color("#1c1c24"),
	Rule:                lipgloss.Color("#1a1a20"),
	RuleSoft:            lipgloss.Color("#14141a"),
	Text:                lipgloss.Color("#ececf0"),
	TextMuted:           lipgloss.Color("#a8a8b4"),
	TextDim:             lipgloss.Color("#7a7a86"),
	TextGhost:           lipgloss.Color("#56565f"),
	Accent:              lipgloss.Color("#7aa2f7"),
	AccentSecondary:     lipgloss.Color("#bb9af7"),
	StateAwaiting:       lipgloss.Color("#f7768e"),
	StateProcessing:     lipgloss.Color("#7aa2f7"),
	StateWarning:        lipgloss.Color("#e0af68"),
	StateIdle:           lipgloss.Color("#9ece6a"),
	StateEnded:          lipgloss.Color("#5a5a64"),
	StateSubagent:       lipgloss.Color("#bb9af7"),
	BtnPrimaryText:      lipgloss.Color("#0a0a0e"),
	StateAwaitingTint:   lipgloss.Color("#28171b"),
	StateProcessingTint: lipgloss.Color("#161e2c"),
	StateWarningTint:    lipgloss.Color("#26201a"),
	StateIdleTint:       lipgloss.Color("#181f17"),
	AccentTint:          lipgloss.Color("#16192c"),
	IndentToolBody:      4,
	IndentSubagent:      2,
	GapTurn:             1,
	GapSection:          2,
	ColumnDur:           8,
	LeftBarGlyph:        "▍",
	RuleGlyph:           "┄",
}

var lightTheme = Theme{
	Name:                "light",
	Bg:                  lipgloss.Color("#fafafa"),
	BgRaised:            lipgloss.Color("#f1f1f2"),
	SurfaceSecondary:    lipgloss.Color("#e6e6e8"),
	Rule:                lipgloss.Color("#dadadc"),
	RuleSoft:            lipgloss.Color("#e6e6e8"),
	Text:                lipgloss.Color("#16161e"),
	TextMuted:           lipgloss.Color("#3a3a44"),
	TextDim:             lipgloss.Color("#5e5e6a"),
	TextGhost:           lipgloss.Color("#8a8a92"),
	Accent:              lipgloss.Color("#2e58b8"),
	AccentSecondary:     lipgloss.Color("#5e35b6"),
	StateAwaiting:       lipgloss.Color("#b62a48"),
	StateProcessing:     lipgloss.Color("#2e58b8"),
	StateWarning:        lipgloss.Color("#8a5a14"),
	StateIdle:           lipgloss.Color("#336a14"),
	StateEnded:          lipgloss.Color("#7a7a82"),
	StateSubagent:       lipgloss.Color("#5e35b6"),
	BtnPrimaryText:      lipgloss.Color("#fafafa"),
	StateAwaitingTint:   lipgloss.Color("#f6e8eb"),
	StateProcessingTint: lipgloss.Color("#e8edf6"),
	StateWarningTint:    lipgloss.Color("#f5efe1"),
	StateIdleTint:       lipgloss.Color("#e8efe1"),
	AccentTint:          lipgloss.Color("#e8edf6"),
	IndentToolBody:      4,
	IndentSubagent:      2,
	GapTurn:             1,
	GapSection:          2,
	ColumnDur:           8,
	LeftBarGlyph:        "▍",
	RuleGlyph:           "┄",
}
