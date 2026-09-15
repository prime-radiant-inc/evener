package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"primeradiant.com/evener/appwire"
)

// withPlainColorProfile forces lipgloss to the Ascii profile for the duration
// of a test, so the renderer's own styling adds no escapes and a control
// character found in the output can only have come from the data under test.
// Without it, SGR escapes from styling would drown out the scan.
func withPlainColorProfile(t *testing.T) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
}

// strayControl returns the first control character in text other than the
// newline and tab the TUI's layout keeps, as a U+ notation string, or "" when
// there is none.
func strayControl(text string) string {
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return fmt.Sprintf("%U", r)
		}
	}
	return ""
}

// TestDetailsDrawerSharedNotesStripControls feeds every shared-notes field the
// drawer prints — the human note, the agent note, and each URL entry's label,
// URL, and id — a legacy pre-strip payload, and asserts the rendered drawer
// carries no control character other than newline/tab while the note's own
// text survives.
func TestDetailsDrawerSharedNotesStripControls(t *testing.T) {
	withPlainColorProfile(t)
	payload := func(marker string) string {
		return marker + "\x1b[2J\x1b]0;owned\x07\u0085\u009b\u009d\x7f end"
	}
	cases := []struct {
		name   string
		detail hubSessionDetail
		marker string
	}{
		{
			name:   "human note",
			detail: hubSessionDetail{HumanNote: payload("human-note-marker")},
			marker: "human-note-marker",
		},
		{
			name:   "agent note",
			detail: hubSessionDetail{AgentNote: payload("agent-note-marker")},
			marker: "agent-note-marker",
		},
		{
			name: "url label",
			detail: hubSessionDetail{SessionURLs: []appwire.SessionURL{{
				Label: payload("url-label-marker"),
				URL:   "https://example.com/label-case",
			}}},
			marker: "url-label-marker",
		},
		{
			name: "url value",
			detail: hubSessionDetail{SessionURLs: []appwire.SessionURL{{
				URL: "https://example.com/url-value-marker" + "\x1b[2J\x1b]0;owned\x07\u0085\u009b\u009d\x7f",
			}}},
			marker: "url-value-marker",
		},
		{
			name: "url id",
			detail: hubSessionDetail{SessionURLs: []appwire.SessionURL{{
				URL: "https://example.com/id-case",
				ID:  "url-id-marker" + "\x1b[2J\x1b]0;owned\x07\u0085\u009b\u009d\x7f",
			}}},
			marker: "url-id-marker",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.detail.Capabilities.SharedNotes = true
			got := detailsDrawer{Detail: tc.detail}.View()
			if stray := strayControl(got); stray != "" {
				t.Fatalf("details drawer rendered control %s:\n%q", stray, got)
			}
			if !strings.Contains(got, tc.marker) {
				t.Fatalf("drawer lost %q while stripping:\n%q", tc.marker, got)
			}
		})
	}
}
