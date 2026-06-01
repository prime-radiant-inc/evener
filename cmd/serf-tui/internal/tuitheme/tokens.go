// tokens.go — central theme registry for serf-tui.
//
// Established in the TUI deep UX pass (2026-05-24). The Theme struct
// holds every color and layout token; the `themes` registry binds names
// to Theme structs. Active theme is swapped via ApplyThemeName(name) which
// also invalidates the cached markdown renderer.
//
// To add a new theme: define a Theme struct literal, register it in
// themeRegistry. No other code changes needed.
package tuitheme

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

// markdownInvalidator is invoked whenever the active theme changes so the
// markdown renderer can drop its cached, theme-colored output. The default
// placeholder counts calls; the renderer wires its real reset via
// SetMarkdownInvalidator.
var markdownInvalidator = func() { markdownInvalidationCount++ }

// SetMarkdownInvalidator wires the callback invoked on every theme change.
// The markdown renderer calls this once at startup so a re-theme drops its
// color-baked cache. tuitheme never imports the renderer; the dependency
// points inward via this setter.
func SetMarkdownInvalidator(fn func()) {
	markdownInvalidator = fn
}

// ActiveTheme returns the currently selected Theme.
func ActiveTheme() Theme {
	if th, ok := themeRegistry[activeThemeKey]; ok {
		return th
	}
	return themeRegistry["dark"]
}

// ApplyThemeName swaps the active theme by registry key. Not safe for
// concurrent use; called only from SetTheme() on bubbletea's main goroutine.
func ApplyThemeName(name string) bool {
	if _, ok := themeRegistry[name]; !ok {
		return false
	}
	activeThemeKey = name
	markdownInvalidator()
	return true
}

// Quiet-ink palette. Neutral grey-leaning surfaces (slight warm shift
// in light, slight cool shift in dark) with one restrained slate-blue
// accent and a warm-tan secondary. State colors are desaturated mid-
// tones — terracotta / slate / gold / sage — that signal without
// shouting. Tints are barely-perceptible washes meant to whisper.

var darkTheme = Theme{
	Name: "dark",
	// Dark slate-grey with a hint of cool blue — softer than near-black
	// so painted surfaces sit calmly without bottoming out as a void.
	// Raised surfaces preserve the slight blue cast.
	Bg:               lipgloss.Color("#191D27"),
	BgRaised:         lipgloss.Color("#232838"),
	SurfaceSecondary: lipgloss.Color("#2d3349"),
	Rule:             lipgloss.Color("#262c3d"),
	RuleSoft:         lipgloss.Color("#1e2230"),
	// Neutral cream-leaning ink, tiers stepping down in lightness with
	// minimal hue shift so the eye reads them as "less" rather than
	// "different".
	Text:      lipgloss.Color("#e8e8ea"),
	TextMuted: lipgloss.Color("#a0a0a4"),
	TextDim:   lipgloss.Color("#76767c"),
	TextGhost: lipgloss.Color("#5e5e66"),
	// Slate-blue accent with a hint of warmth, warm-tan secondary. No
	// neon, no candy.
	Accent:          lipgloss.Color("#6b9ec8"),
	AccentSecondary: lipgloss.Color("#a8927a"),
	// Calm state palette: terracotta / slate / muted gold / muted sage.
	StateAwaiting:   lipgloss.Color("#c47878"),
	StateProcessing: lipgloss.Color("#6b9ec8"),
	StateWarning:    lipgloss.Color("#c4a06a"),
	StateIdle:       lipgloss.Color("#88a878"),
	StateEnded:      lipgloss.Color("#5e5e64"),
	StateSubagent:   lipgloss.Color("#a8927a"),
	BtnPrimaryText:  lipgloss.Color("#0f0f11"),
	// Tints — faint elevated darks, used as backgrounds on tinted rows.
	StateAwaitingTint:   lipgloss.Color("#1d1516"),
	StateProcessingTint: lipgloss.Color("#13181f"),
	StateWarningTint:    lipgloss.Color("#1d1a14"),
	StateIdleTint:       lipgloss.Color("#161c14"),
	AccentTint:          lipgloss.Color("#13181f"),
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
	// Very slight warm off-white — feels like quality bond paper rather
	// than buttery cream or cold #fafafa.
	Bg:               lipgloss.Color("#f8f7f3"),
	BgRaised:         lipgloss.Color("#f1efe9"),
	SurfaceSecondary: lipgloss.Color("#e6e3da"),
	Rule:             lipgloss.Color("#d8d4ca"),
	RuleSoft:         lipgloss.Color("#ebe7dd"),
	// Near-black ink, neutral with a hair of warmth so it doesn't read
	// blue on the paper.
	Text:      lipgloss.Color("#1a1a1e"),
	TextMuted: lipgloss.Color("#4a4a50"),
	TextDim:   lipgloss.Color("#6a6a70"),
	TextGhost: lipgloss.Color("#969098"),
	// Slate-blue accent, warm-tan secondary — same intent as the dark
	// theme but deeper for contrast against light paper.
	Accent:          lipgloss.Color("#3d6790"),
	AccentSecondary: lipgloss.Color("#7a6850"),
	// Calm states: deep terracotta / slate / amber / forest.
	StateAwaiting:   lipgloss.Color("#9a3c3c"),
	StateProcessing: lipgloss.Color("#3d6790"),
	StateWarning:    lipgloss.Color("#8a6420"),
	StateIdle:       lipgloss.Color("#4a6a35"),
	StateEnded:      lipgloss.Color("#76746e"),
	StateSubagent:   lipgloss.Color("#7a6850"),
	BtnPrimaryText:  lipgloss.Color("#f8f7f3"),
	// Tints — barely-perceptible washes.
	StateAwaitingTint:   lipgloss.Color("#ede0e0"),
	StateProcessingTint: lipgloss.Color("#dfe5ec"),
	StateWarningTint:    lipgloss.Color("#ebe5d3"),
	StateIdleTint:       lipgloss.Color("#e1e6d6"),
	AccentTint:          lipgloss.Color("#dfe5ec"),
	IndentToolBody:      4,
	IndentSubagent:      2,
	GapTurn:             1,
	GapSection:          2,
	ColumnDur:           8,
	LeftBarGlyph:        "▍",
	RuleGlyph:           "┄",
}
