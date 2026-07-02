package doctor

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// This file fuzzes RenderTranscript (transcript.go), the doctor package's
// transcript→text renderer for both the outline and markdown views. It transforms
// a structured TranscriptResult into a bounded text block ending in an honest
// elision footer. It must never panic on adversarial turn content, must be
// deterministic, and must always emit its accounting footer.

// FuzzArRenderTranscript builds a TranscriptResult from fuzzed turn fields and
// renders it in both formats. Oracles beyond never-panic:
//
//   - FOOTER PRESENT: the render always ends with the "— turns_total=… " footer
//     echoing the result's own counts, so the accounting can never silently
//     vanish.
//   - DETERMINISM: identical input renders identically.
//   - VALID UTF-8 preservation for valid-UTF-8 inputs.
func FuzzArRenderTranscript(f *testing.F) {
	type seed struct {
		kind, text, arg, content string
		nTurns                   uint8
		outline                  bool
	}
	seeds := []seed{
		{"ASSISTANT", "hello", "shell {cmd}", "output here", 1, false},
		{"USER_INPUT", "multi\nline\ntext", "", "", 2, true},
		{"TOOL_RESULTS", "", "arg\r\npreview", "err\nbody", 3, false},
		{"", "", "", "", 0, true},
		{"ASSISTANT", strings.Repeat("x", 5000), strings.Repeat("a", 5000), strings.Repeat("c", 5000), 4, false},
		{"世界", "héllo 🌍", "パス", "тело", 2, true},
	}
	for _, s := range seeds {
		f.Add(s.kind, s.text, s.arg, s.content, s.nTurns, s.outline)
	}

	f.Fuzz(func(t *testing.T, kind, text, arg, content string, nTurns8 uint8, outline bool) {
		n := int(nTurns8)%8 + 1 // 1..8 turns, keep it bounded
		r := TranscriptResult{
			SessionID:     "sess",
			ResultTool:    "communicate",
			TurnsTotal:    n + 3,
			TurnsRendered: n,
			Elided:        3,
		}
		for i := 0; i < n; i++ {
			r.Turns = append(r.Turns, TurnSummary{
				Index:       i + 1,
				Kind:        kind,
				Role:        "assistant",
				Text:        text,
				ToolCalls:   []ToolCallSummary{{Name: kind, ArgPreview: arg}},
				ToolResults: []ToolResultSummary{{Name: kind, ContentPreview: content, IsError: true}},
			})
		}

		format := "markdown"
		if outline {
			format = "outline"
		}
		out := RenderTranscript(r, format)

		footer := fmt.Sprintf("— turns_total=%d turns_rendered=%d elided=%d", r.TurnsTotal, r.TurnsRendered, r.Elided)
		if !strings.Contains(out, footer) {
			t.Fatalf("RenderTranscript(%s) missing accounting footer %q:\n%s", format, footer, out)
		}

		if out2 := RenderTranscript(r, format); out != out2 {
			t.Fatalf("RenderTranscript non-deterministic")
		}

		inputsValid := utf8.ValidString(kind) && utf8.ValidString(text) && utf8.ValidString(arg) && utf8.ValidString(content)
		if inputsValid && !utf8.ValidString(out) {
			t.Fatalf("RenderTranscript emitted invalid UTF-8 from valid input")
		}
	})
}
