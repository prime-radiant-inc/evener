package msgrender

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

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
