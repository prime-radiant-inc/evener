package tuitheme

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// probedOriginalFg / probedOriginalBg cache the terminal's default
// foreground and background colors as discovered at startup. Used both
// to drive "system" theme detection without re-probing and to restore
// the exact original colors on exit (instead of OSC 110/111 reset,
// which isn't universally supported).
var (
	probedOriginalFg string // "#rrggbb" or empty if probe failed
	probedOriginalBg string
	probedDone       bool
)

// ProbeTerminalDefaults queries the terminal for its default foreground
// and background via OSC 10/11 with the `?` argument, caches the results
// for later restore + system-theme detection, and returns whether the
// probe produced a usable bg value. Idempotent: subsequent calls return
// the cached result.
//
// Must be called at startup BEFORE ApplyTerminalBg() — otherwise the
// probe would read back our painted colors instead of the terminal's
// originals.
func ProbeTerminalDefaults() bool {
	if probedDone {
		return probedOriginalBg != ""
	}
	probedDone = true
	if !stdoutIsTerminal() {
		return false
	}
	if c := termenv.ForegroundColor(); c != nil {
		probedOriginalFg = colorToHex(c)
	}
	if c := termenv.BackgroundColor(); c != nil {
		probedOriginalBg = colorToHex(c)
	}
	return probedOriginalBg != ""
}

// detectSystemThemeKey returns "dark" or "light" by inspecting the
// cached probed background. Falls back to termenv.HasDarkBackground()
// (which has its own fallback chain via COLORFGBG / ANSI defaults)
// when the probe was unavailable.
func detectSystemThemeKey() string {
	if probedOriginalBg != "" {
		if relativeLuminanceHex(probedOriginalBg) < 0.5 {
			return "dark"
		}
		return "light"
	}
	if termenv.HasDarkBackground() {
		return "dark"
	}
	return "light"
}

// colorToHex converts a termenv.Color (RGBColor, ANSIColor, ANSI256Color)
// to a "#rrggbb" hex string. Returns "" on failure.
func colorToHex(c termenv.Color) string {
	if c == nil {
		return ""
	}
	// RGBColor is a `type RGBColor string`; its string form is the hex.
	if rgb, ok := c.(termenv.RGBColor); ok {
		s := strings.TrimSpace(string(rgb))
		if len(s) == 7 && s[0] == '#' {
			return s
		}
		if len(s) == 4 && s[0] == '#' {
			// #rgb → #rrggbb
			return "#" + string([]byte{s[1], s[1], s[2], s[2], s[3], s[3]})
		}
		return ""
	}
	// ANSI / ANSI256 → convert via lipgloss's color palette. termenv's
	// ConvertToRGB returns a colorful.Color. We re-use the chain via the
	// color's RGBA() method which yields 16-bit channels.
	rgba := termenv.ConvertToRGB(c)
	r, g, b := rgba.R, rgba.G, rgba.B
	return fmt.Sprintf("#%02x%02x%02x", int(r*255), int(g*255), int(b*255))
}

// relativeLuminanceHex parses "#rrggbb" and returns WCAG relative
// luminance in [0,1]. Returns 0.5 (neutral) on malformed input.
func relativeLuminanceHex(hex string) float64 {
	if len(hex) != 7 || hex[0] != '#' {
		return 0.5
	}
	channel := func(s string) float64 {
		v, err := strconv.ParseInt(s, 16, 32)
		if err != nil {
			return 0
		}
		c := float64(v) / 255.0
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r := channel(hex[1:3])
	g := channel(hex[3:5])
	b := channel(hex[5:7])
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// ApplyTerminalBg sends OSC 10 (default foreground) and OSC 11 (default
// background) to align the terminal's defaults with the active theme.
// This makes every cell we don't paint (the gap between content and
// footer, lines shorter than the terminal width) inherit the theme
// palette — and, importantly, makes every unstyled text span render in
// the theme's Text color rather than the terminal's configured default.
// Without OSC 10, code paths that render text without an explicit
// Foreground (some dashboard rows, etc.) fall back to terminal default
// fg, which can collide with our painted bg for a black-on-black row.
//
// Supported by iTerm2, Kitty, Alacritty, WezTerm, Gnome Terminal, xterm,
// and Terminal.app. Unsupported terminals ignore the sequences. The
// matching ResetTerminalBg() MUST run on exit to restore the user's
// original colors.
func ApplyTerminalBg() {
	if !stdoutIsTerminal() {
		return
	}
	th := ActiveTheme()
	if fg := string(th.Text); fg != "" {
		termenv.SetForegroundColor(termenv.RGBColor(fg))
	}
	if bg := string(th.Bg); bg != "" {
		termenv.SetBackgroundColor(termenv.RGBColor(bg))
	}
}

// ResetTerminalBg restores the terminal's default foreground/background
// to the values captured by ProbeTerminalDefaults at startup. Sets the
// exact original colors via OSC 10/11 with explicit values — more
// reliable than OSC 110/111 reset (which not all terminals honor) since
// we know exactly what the user had.
//
// Falls back to OSC 110/111 when the startup probe didn't produce
// usable values.
func ResetTerminalBg() {
	if !stdoutIsTerminal() {
		return
	}
	if probedOriginalFg != "" {
		termenv.SetForegroundColor(termenv.RGBColor(probedOriginalFg))
	} else {
		_, _ = os.Stdout.WriteString("\x1b]110\x07")
	}
	if probedOriginalBg != "" {
		termenv.SetBackgroundColor(termenv.RGBColor(probedOriginalBg))
	} else {
		_, _ = os.Stdout.WriteString("\x1b]111\x07")
	}
}

func stdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
