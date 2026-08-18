//go:build serffuzz

package tuitheme

import "github.com/muesli/termenv"

var terminalIsStdout = func() bool { return false }
var terminalForeground = func() termenv.Color { return nil }
var terminalBackground = func() termenv.Color { return nil }
var terminalHasDarkBg = func() bool { return true }
var terminalSetForeground = func(termenv.Color) {}
var terminalSetBackground = func(termenv.Color) {}
var terminalWriteString = func(string) {}
