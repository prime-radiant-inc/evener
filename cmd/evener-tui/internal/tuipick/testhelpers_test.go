package tuipick

import (
	"regexp"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ansiPattern matches ANSI escape sequences for stripping styled output in tests.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[\x20-\x2f]*[\x40-\x7e]`)

// withTestColorProfile forces lipgloss into TrueColor for the duration of a
// test so style rendering emits ANSI escapes regardless of the host terminal,
// restoring the previous profile on cleanup.
func withTestColorProfile(t *testing.T) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previous)
	})
}
