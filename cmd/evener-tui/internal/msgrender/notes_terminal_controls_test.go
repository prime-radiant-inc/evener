package msgrender

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
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

// notesPayload is one legacy shared-notes body: ordinary note text carrying
// the terminal controls a pre-strip write could have stored in it. The marker
// text around them proves the payload still rendered.
const notesPayload = "Human: rebuild the cache\x1b[2J before Thursday\n" +
	"Agent: \u009b31mstill \x07working\x7f on it\n" +
	"docs (https://example.com/x\u009d) [url\u0085id]"

// TestRenderSteeringStripsControls: a legacy human-note update is persisted
// as a steering turn, so transcript.MsgSteering is one of the readers through
// which notes-derived text reaches the terminal.
func TestRenderSteeringStripsControls(t *testing.T) {
	withPlainColorProfile(t)
	got := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgSteering, Text: notesPayload}, 100, false)
	if stray := strayControl(got); stray != "" {
		t.Fatalf("rendered steering body carries control %s:\n%q", stray, got)
	}
	for _, want := range []string{"rebuild the cache", "still working", "https://example.com/x"} {
		if !strings.Contains(got, want) {
			t.Fatalf("steering body lost %q while stripping:\n%q", want, got)
		}
	}
}

// TestRenderSharedNotesToolsStripControls: every tool in the shared-notes family
// renders notes text and URL lines — urls_add and urls_remove echo the entry they
// touched, notes_agent_set the stored note — so each goes through the
// terminal-boundary strip rather than the generic JSON body.
func TestRenderSharedNotesToolsStripControls(t *testing.T) {
	withPlainColorProfile(t)
	for _, name := range []string{"notes_read", "notes_agent_set", "urls_add", "urls_remove"} {
		t.Run(name, func(t *testing.T) {
			tc := transcript.ToolCallInfo{
				Name:     name,
				Output:   notesPayload,
				Done:     true,
				Expanded: true,
			}
			got := RenderToolCall(tc, 100, false)
			if stray := strayControl(got); stray != "" {
				t.Fatalf("rendered %s body carries control %s:\n%q", name, stray, got)
			}
			for _, want := range []string{"rebuild the cache", "still working", "https://example.com/x"} {
				if !strings.Contains(got, want) {
					t.Fatalf("%s body lost %q while stripping:\n%q", name, want, got)
				}
			}
		})
	}
}

// TestRenderNonNotesToolKeepsANSI is the over-reach guard: only the shared-notes
// family is routed through the terminal-boundary strip, because every other tool body —
// and shell, command, and file output generally — can legitimately carry ANSI.
func TestRenderNonNotesToolKeepsANSI(t *testing.T) {
	withPlainColorProfile(t)
	const ansiPayload = "before \x1b[31mred\x1b[0m after"
	// custom_tool and provider__op hit the unknown and MCP fallbacks; web_fetch
	// is a registered renderer with no Body, so its expanded output takes the
	// legacy fallback. All three must render the payload byte for byte.
	for _, tool := range []string{"custom_tool", "provider__op", "web_fetch"} {
		t.Run(tool, func(t *testing.T) {
			tc := transcript.ToolCallInfo{
				Name:     tool,
				Output:   ansiPayload,
				Done:     true,
				Expanded: true,
			}
			got := RenderToolCall(tc, 100, false)
			if !strings.Contains(got, ansiPayload) {
				t.Fatalf("tool %q no longer renders its ANSI payload intact:\n%q", tool, got)
			}
		})
	}
}
