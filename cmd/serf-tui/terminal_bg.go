package main

import (
	"os"

	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// applyTerminalBg sends OSC 11 to set the terminal's default background
// color to the active theme's Bg. This makes every cell we don't paint
// (the gap between content and footer, lines shorter than the terminal
// width) inherit the theme palette — so our painted surfaces sit on
// matching paper rather than on whatever the user's terminal config has.
//
// Supported by iTerm2, Kitty, Alacritty, WezTerm, Gnome Terminal, xterm,
// and Terminal.app. Unsupported terminals ignore the sequence. The
// matching resetTerminalBg() MUST run on exit to restore the user's
// original background.
func applyTerminalBg() {
	if !stdoutIsTerminal() {
		return
	}
	bg := string(activeTheme().Bg)
	if bg == "" {
		return
	}
	termenv.SetBackgroundColor(termenv.RGBColor(bg))
}

// resetTerminalBg sends OSC 111 to reset the terminal's default
// background color back to the user's configured value. Called from
// defer in main() so a normal exit, panic-with-recover, or signal that
// runs deferred functions all restore the terminal.
func resetTerminalBg() {
	if !stdoutIsTerminal() {
		return
	}
	// OSC 111 ST — reset background to terminal default.
	_, _ = os.Stdout.WriteString("\x1b]111\x07")
}

func stdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
