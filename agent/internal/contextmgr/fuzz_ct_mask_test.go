//go:build serffuzz

package contextmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzCtMaskToolResult drives maskToolResultContent — the mask decision core
// lifted out of maskObservations — with a fuzzed injected summarizer so no real
// summarization runs. Oracles: determinism; never mask error results, result-tool
// results, non-string content, or already-masked "[…]" content; and when it does
// mask, the replacement is strictly shorter and equals the injected summary.
func FuzzCtMaskToolResult(f *testing.F) {
	f.Add("some long tool output", false, "read_file", "result", "[short]", false)
	f.Add("[already masked]", false, "shell", "result", "x", false)
	f.Add("errtext", true, "shell", "result", "y", false)
	f.Add("resulttool content", false, "result", "result", "z", false)
	f.Add("payload", false, "grep", "result", "payload-way-longer", true)

	f.Fuzz(func(t *testing.T, contentStr string, isError bool, name, resultToolName, summaryOut string, nonString bool) {
		var content any = contentStr
		if nonString {
			content = len(contentStr) // exercise the non-string branch
		}
		tr := &llm.ToolResultData{
			ToolCallID: "tc",
			Name:       name,
			Content:    content,
			IsError:    isError,
		}
		summarize := func(string, any, json.RawMessage) string { return summaryOut }

		newContent, mask := maskToolResultContent(tr, resultToolName, summarize, nil)
		if nc2, m2 := maskToolResultContent(tr, resultToolName, summarize, nil); nc2 != newContent || m2 != mask {
			t.Fatalf("non-deterministic: (%q,%v) vs (%q,%v)", newContent, mask, nc2, m2)
		}

		if isError && mask {
			t.Fatalf("masked an error result")
		}
		if name == resultToolName && mask {
			t.Fatalf("masked a result-tool result")
		}
		if nonString && mask {
			t.Fatalf("masked non-string content")
		}
		if s, ok := content.(string); ok {
			if strings.HasPrefix(s, "[") && strings.HasSuffix(strings.TrimSpace(s), "]") && mask {
				t.Fatalf("re-masked already-masked content %q", s)
			}
		}

		if mask {
			s, ok := content.(string)
			if !ok {
				t.Fatalf("mask true on non-string content")
			}
			if len(newContent) >= len(s) {
				t.Fatalf("mask not strictly shorter: len(new)=%d >= len(orig)=%d", len(newContent), len(s))
			}
			if newContent != summaryOut {
				t.Fatalf("masked content %q != injected summary %q", newContent, summaryOut)
			}
		} else if newContent != "" {
			t.Fatalf("no-mask must return empty newContent, got %q", newContent)
		}
	})
}
