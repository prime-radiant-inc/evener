package tuitext_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitext"
)

func FuzzTruncateText(f *testing.F) {
	f.Add("hello world", 8)
	f.Add("日本語", 3)
	f.Fuzz(func(t *testing.T, input string, width int) {
		if width < -1024 || width > 1024 {
			return
		}
		got := tuitext.TruncateText(input, width)
		if width <= 0 && got != "" {
			t.Fatalf("non-positive width produced %q", got)
		}
		if width > 0 && lipgloss.Width(got) > width {
			t.Fatalf("result width %d exceeds %d: %q", lipgloss.Width(got), width, got)
		}
		multi := tuitext.TruncateMultilineText(input, width)
		if strings.Count(multi, "\n") != strings.Count(input, "\n") {
			t.Fatalf("multiline truncation changed line count")
		}
	})
}
