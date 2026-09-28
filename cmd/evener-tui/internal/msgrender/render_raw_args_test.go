package msgrender

import (
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// TestRenderToolCall_RejectedCallBoundedRawFallback mirrors the hub's
// TestRenderMarkdown_ResultToolBoundedRawFallback for the tool-card path: a
// rejected call with malformed raw arguments longer than the 120-rune
// tool-card bound must render a TRUNCATED form, not the full payload.
func TestRenderToolCall_RejectedCallBoundedRawFallback(t *testing.T) {
	t.Parallel()
	// Malformed JSON (bare key) longer than toolCardRawFallbackMaxRunes (120).
	longRaw := `{command: "` + strings.Repeat("x", 500) + `", }`
	info := transcript.ToolCallInfo{
		Name:    "shell",
		RawArgs: longRaw,
		Done:    true,
		Error:   "arguments not valid JSON",
	}
	// Use a wide terminal so the DotLeader does not further truncate the
	// target; the 120-rune oneLineTrunc bound is what this test exercises.
	rendered := RenderToolCall(info, 300, false)
	// The full 500-char payload must NOT appear; the bounded version should.
	if strings.Contains(rendered, strings.Repeat("x", 200)) {
		t.Errorf("expected tool-card raw fallback to be bounded to 120 runes, but found a 200+ char run of the payload in:\n%s", rendered)
	}
	// The truncated form should still be present (the raw args up to the limit
	// + ellipsis), so the user sees the model's actual input.
	if !strings.Contains(rendered, strings.Repeat("x", 100)) {
		t.Errorf("expected the bounded raw fallback to contain a long prefix of the raw args, got:\n%s", rendered)
	}
}

// TestParsesAsJSONObject_NullAndArray verifies that null and valid non-object
// JSON (arrays) are NOT classified as JSON objects, so the tool-card raw
// fallback fires for them — matching the hub's parseArgs (returns nil for
// null). A bare null decodes with a nil map, which must be treated as
// non-object.
func TestParsesAsJSONObject_NullAndArray(t *testing.T) {
	t.Parallel()
	if parsesAsJSONObject("null") {
		t.Errorf("parsesAsJSONObject(\"null\") = true, want false (null is not an object)")
	}
	if parsesAsJSONObject(`["a","b"]`) {
		t.Errorf(`parsesAsJSONObject(["a","b"]) = true, want false (array is not an object)`)
	}
	if parsesAsJSONObject(`{"key":"val"}`) {
		// This is correct — a valid object IS an object. Just confirming
		// the positive case still works after the null fix.
	} else {
		t.Errorf(`parsesAsJSONObject({"key":"val"}) = false, want true`)
	}
	if parsesAsJSONObject("") {
		t.Errorf(`parsesAsJSONObject("") = true, want false`)
	}
	if parsesAsJSONObject("not json") {
		t.Errorf(`parsesAsJSONObject("not json") = true, want false`)
	}
}

// TestOneLineTrunc_CarriageReturnHandling verifies that \r is stripped (not
// normalized to a space), matching the hub's oneLine
// (agent/transcript_render.go:1762-1764): "a\rb" -> "ab", "a\r\nb" -> "a b".
func TestOneLineTrunc_CarriageReturnHandling(t *testing.T) {
	t.Parallel()
	// \r alone is stripped: "a\rb" -> "ab".
	got := oneLineTrunc("a\rb", 100)
	if got != "ab" {
		t.Errorf("oneLineTrunc(\"a\\rb\") = %q, want \"ab\"", got)
	}
	// \r\n: \n becomes space, \r is stripped: "a\r\nb" -> "a b".
	got = oneLineTrunc("a\r\nb", 100)
	if got != "a b" {
		t.Errorf("oneLineTrunc(\"a\\r\\nb\") = %q, want \"a b\"", got)
	}
	// \n alone becomes space: "a\nb" -> "a b".
	got = oneLineTrunc("a\nb", 100)
	if got != "a b" {
		t.Errorf("oneLineTrunc(\"a\\nb\") = %q, want \"a b\"", got)
	}
}
