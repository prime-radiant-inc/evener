package main

import (
	"os"

	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// applyTerminalBg sends OSC 10 (default foreground) and OSC 11 (default
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
// matching resetTerminalBg() MUST run on exit to restore the user's
// original colors.
func applyTerminalBg() {
	if !stdoutIsTerminal() {
		return
	}
	th := activeTheme()
	if fg := string(th.Text); fg != "" {
		termenv.SetForegroundColor(termenv.RGBColor(fg))
	}
	if bg := string(th.Bg); bg != "" {
		termenv.SetBackgroundColor(termenv.RGBColor(bg))
	}
}

// resetTerminalBg sends OSC 110/111 to reset the terminal's default
// foreground and background colors back to the user's configured
// values. Called from defer in main() so a normal exit, panic-with-
// recover, or signal that runs deferred functions all restore the
// terminal.
func resetTerminalBg() {
	if !stdoutIsTerminal() {
		return
	}
	// OSC 110 / 111 BEL — reset fg / bg to terminal default.
	_, _ = os.Stdout.WriteString("\x1b]110\x07\x1b]111\x07")
}

func stdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
