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

// Workshop-log palette. Warm paper-and-ink in light mode, warm noir in
// dark mode. Surfaces are subtle (≤4% delta from Bg) so painted regions
// sit quietly against whatever terminal background the user has, rather
// than reading as a cold-grey rectangle floating on cream.
//
// Accents lean rust/amber rather than blue, and states are slightly
// desaturated — pink/red/blue/green tuned to feel inked, not neon.

var darkTheme = Theme{
	Name: "dark",
	// Warm noir surfaces — slight reddish-brown cast vs the previous
	// blue-grey. Stays dark enough for a Solarized Dark or true-black
	// terminal config to coexist.
	Bg:               lipgloss.Color("#0d0c0a"),
	BgRaised:         lipgloss.Color("#1a1814"),
	SurfaceSecondary: lipgloss.Color("#26221c"),
	Rule:             lipgloss.Color("#1f1d18"),
	RuleSoft:         lipgloss.Color("#181612"),
	// Warm cream text on warm-dark bg reads as paper-ink-inverted, not
	// digital. TextMuted/Dim/Ghost step down in lightness AND saturation.
	Text:      lipgloss.Color("#ede5d4"),
	TextMuted: lipgloss.Color("#a89e8a"),
	TextDim:   lipgloss.Color("#7a7261"),
	TextGhost: lipgloss.Color("#5a5448"),
	// Amber accent + sepia secondary; both warm tones that pair with cream
	// text without going neon.
	Accent:          lipgloss.Color("#e0a04a"),
	AccentSecondary: lipgloss.Color("#bf8e62"),
	// Slightly desaturated state palette — coral/slate/gold/sage, plus a
	// muted brown for ended sessions.
	StateAwaiting:   lipgloss.Color("#e07a72"),
	StateProcessing: lipgloss.Color("#7d9ee0"),
	StateWarning:    lipgloss.Color("#e0b870"),
	StateIdle:       lipgloss.Color("#a8c87a"),
	StateEnded:      lipgloss.Color("#5a564e"),
	StateSubagent:   lipgloss.Color("#bf8e62"),
	BtnPrimaryText:  lipgloss.Color("#0d0c0a"),
	// Tints — barely-perceptible washes for state-tinted rows.
	StateAwaitingTint:   lipgloss.Color("#241612"),
	StateProcessingTint: lipgloss.Color("#13192a"),
	StateWarningTint:    lipgloss.Color("#241e14"),
	StateIdleTint:       lipgloss.Color("#171f14"),
	AccentTint:          lipgloss.Color("#241e10"),
	IndentToolBody:      4,
	IndentSubagent:      2,
	GapTurn:             1,
	GapSection:          2,
	ColumnDur:           8,
	LeftBarGlyph:        "▍",
	RuleGlyph:           "┄",
}

var lightTheme = Theme{
	Name: "light",
	// Warm paper. Bg is a soft cream rather than cold #fafafa, so painted
	// surfaces sit quietly against typical light terminals without
	// flashing as a colder rectangle.
	Bg:               lipgloss.Color("#fbf8f2"),
	BgRaised:         lipgloss.Color("#f5f0e6"),
	SurfaceSecondary: lipgloss.Color("#ebe4d2"),
	Rule:             lipgloss.Color("#d8d0bc"),
	RuleSoft:         lipgloss.Color("#ebe4d2"),
	// Warm near-black ink. Tiers desaturate toward sepia rather than blue
	// grey.
	Text:      lipgloss.Color("#1d1a14"),
	TextMuted: lipgloss.Color("#4a443a"),
	TextDim:   lipgloss.Color("#6e6759"),
	TextGhost: lipgloss.Color("#9a917f"),
	// Rust accent + olive secondary — workshop-log ink stamps.
	Accent:          lipgloss.Color("#9a4515"),
	AccentSecondary: lipgloss.Color("#5a4d2a"),
	// Desaturated states. StateAwaiting is a deeper less-neon red; idle
	// is a deeper forest green; processing is a slate blue with warmth.
	StateAwaiting:   lipgloss.Color("#a3293e"),
	StateProcessing: lipgloss.Color("#3d5ca6"),
	StateWarning:    lipgloss.Color("#8a5a14"),
	StateIdle:       lipgloss.Color("#446d28"),
	StateEnded:      lipgloss.Color("#7a7367"),
	StateSubagent:   lipgloss.Color("#5a4d2a"),
	BtnPrimaryText:  lipgloss.Color("#fbf8f2"),
	// Tints — gentle warm washes.
	StateAwaitingTint:   lipgloss.Color("#f1e1dd"),
	StateProcessingTint: lipgloss.Color("#e3e7f0"),
	StateWarningTint:    lipgloss.Color("#f0e9d5"),
	StateIdleTint:       lipgloss.Color("#e5ead7"),
	AccentTint:          lipgloss.Color("#f1e6d9"),
	IndentToolBody:      4,
	IndentSubagent:      2,
	GapTurn:             1,
	GapSection:          2,
	ColumnDur:           8,
	LeftBarGlyph:        "▍",
	RuleGlyph:           "┄",
}
