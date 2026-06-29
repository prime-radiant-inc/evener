package main

import (
	"strings"
	"testing"
)

// FuzzLLMCallParsers drives llmcall's real, side-effect-free input decoders.
// parseMetadata is the "--meta key=value" config decoder; buildSystemPrompt
// (text-only, no file args) is the system-prompt assembler. The package's flag
// arg-parser (llmcallMain) is intentionally NOT fuzzed here: it couples parsing
// with a live llm.NewFromEnv() call and an os.Stdin read, so it is not a safe
// no-side-effect seam — its behavior belongs to the Workstream B sandboxed API
// fuzzing. Oracle: no-panic floor plus, for accepted metadata, every emitted key
// is non-empty.
func FuzzLLMCallParsers(f *testing.F) {
	seeds := []struct {
		which int
		s     string
	}{
		{0, "a=b"},
		{0, "key=value with spaces"},
		{0, "k1=v1\nk2=v2"},
		{0, "=novalue"},
		{0, "noequals"},
		{0, "  trimmed = me  "},
		{0, ""},
		{0, "a=b=c"},
		{1, "you are a helpful agent"},
		{1, "   "},
		{1, "line1\nline2"},
	}
	for _, s := range seeds {
		f.Add(s.which, s.s)
	}

	f.Fuzz(func(t *testing.T, which int, raw string) {
		if which&1 == 0 {
			pairs := strings.Split(raw, "\n")
			m, err := parseMetadata(pairs)
			if err != nil {
				return
			}
			for k := range m {
				if k == "" {
					t.Fatalf("parseMetadata(%q) produced an empty key: %#v", raw, m)
				}
			}
			return
		}

		// Text-only system prompt assembly is pure (no file reads).
		out, err := buildSystemPrompt(raw, "", nil)
		if err != nil {
			t.Fatalf("buildSystemPrompt(text-only) errored on %q: %v", raw, err)
		}
		if strings.TrimSpace(raw) == "" && out != "" {
			t.Fatalf("buildSystemPrompt(empty) = %q, want empty", out)
		}
	})
}
