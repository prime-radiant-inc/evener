//go:build !serffuzz

package tuitheme

import (
	"os"

	"github.com/muesli/termenv"
	"golang.org/x/term"
)

var terminalIsStdout = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
var terminalForeground = termenv.ForegroundColor
var terminalBackground = termenv.BackgroundColor
var terminalHasDarkBg = termenv.HasDarkBackground
var terminalSetForeground = func(c termenv.Color) { termenv.DefaultOutput().SetForegroundColor(c) }
var terminalSetBackground = func(c termenv.Color) { termenv.DefaultOutput().SetBackgroundColor(c) }
var terminalWriteString = func(s string) { _, _ = os.Stdout.WriteString(s) }
