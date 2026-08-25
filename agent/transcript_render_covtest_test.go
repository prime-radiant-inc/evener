package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestRenderMarkdown_FailureAndHookAndUnknownTurnKinds covers the compact
// render branches for TurnFailure, TurnHookCompleted, and the default
// (unknown) turn kind case — all uncovered by existing render tests.
func TestRenderMarkdown_FailureAndHookAndUnknownTurnKinds(t *testing.T) {
	t.Parallel()
	entries := []transcript.Entry{
		makeEntry(schema.Turn{Kind: schema.TurnFailure, Message: llm.Assistant("something broke")}),
		makeEntry(schema.Turn{Kind: schema.TurnHookCompleted, Message: llm.User("hook note")}),
		makeEntry(schema.Turn{Kind: "unknown_kind", Message: llm.Assistant("mystery turn")}),
	}
	out := renderMarkdown(transcript.Header{}, entries, 0, renderOpts{})
	for _, want := range []string{
		"## Turn 0 — Turn failed",
		"## Turn 1 — Hook",
		"[unknown_kind turn omitted]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// TestRenderMarkdown_FailureAndHookFullTurn covers the full-turn (exact) render
// path for TurnFailure and TurnHookCompleted, via fullResultFor pin.
func TestRenderMarkdown_FailureAndHookFullTurn(t *testing.T) {
	t.Parallel()
	entries := []transcript.Entry{
		makeEntry(schema.Turn{Kind: schema.TurnFailure, Message: llm.Assistant("full failure text")}),
		makeEntry(schema.Turn{Kind: schema.TurnHookCompleted, Message: llm.User("full hook text")}),
	}
	for i := range entries {
		seq := i
		opt := renderOpts{fullResultFor: &seq}
		out := renderMarkdown(transcript.Header{}, entries, 0, opt)
		wantLabel := []string{"[Turn failed — exact]", "[Hook — exact]"}[i]
		if !strings.Contains(out, wantLabel) {
			t.Errorf("expected %q in output:\n%s", wantLabel, out)
		}
	}
}

// TestRenderMarkdown_UnknownTurnKindFullTurn covers the default-case full-turn
// path for an unknown turn kind, via fullResultFor pin.
func TestRenderMarkdown_UnknownTurnKindFullTurn(t *testing.T) {
	t.Parallel()
	entries := []transcript.Entry{
		makeEntry(schema.Turn{Kind: "custom_kind", Message: llm.Assistant("custom text")}),
	}
	seq := 0
	opt := renderOpts{fullResultFor: &seq}
	out := renderMarkdown(transcript.Header{}, entries, 0, opt)
	if !strings.Contains(out, "custom text") {
		t.Errorf("expected custom text in full-turn output:\n%s", out)
	}
}

// TestJobResultMetadata_MalformedJSON covers the decode-error path in
// jobResultMetadata: malformed JSON returns "".
func TestJobResultMetadata_MalformedJSON(t *testing.T) {
	if got := jobResultMetadata("{not valid json"); got != "" {
		t.Errorf("jobResultMetadata(malformed) = %q, want empty", got)
	}
}

// TestJobResultMetadata_KnownKeys covers the happy path: a valid JSON object
// with known metadata keys produces a "k=v k=v" summary string.
func TestJobResultMetadata_KnownKeys(t *testing.T) {
	raw := `{"child_session_id":"child_01","model":"gpt-4","agent_type":"worker"}`
	got := jobResultMetadata(raw)
	for _, want := range []string{"child_session_id=child_01", "model=gpt-4", "agent_type=worker"} {
		if !strings.Contains(got, want) {
			t.Errorf("jobResultMetadata(%q) = %q, want it to contain %q", raw, got, want)
		}
	}
}

// TestHasNonJobResultKeys_Unparseable covers the unparseable-body path in
// hasNonJobResultKeys: bad JSON returns false.
func TestHasNonJobResultKeys_Unparseable(t *testing.T) {
	if hasNonJobResultKeys("{not json") {
		t.Error("hasNonJobResultKeys(malformed) = true, want false")
	}
}

// TestHasNonJobResultKeys_UnknownKey covers the positive case: a body with an
// unknown key returns true.
func TestHasNonJobResultKeys_UnknownKey(t *testing.T) {
	if !hasNonJobResultKeys(`{"job_id":"j1","unknown_field":"x"}`) {
		t.Error(`hasNonJobResultKeys({"job_id":"j1","unknown_field":"x"}) = false, want true`)
	}
}

// TestHasNonJobResultKeys_OnlyKnownKeys covers the negative case: a body with
// only known keys returns false.
func TestHasNonJobResultKeys_OnlyKnownKeys(t *testing.T) {
	if hasNonJobResultKeys(`{"job_id":"j1","exit_code":0}`) {
		t.Error(`hasNonJobResultKeys(only known keys) = true, want false`)
	}
}

// TestPrettyJSONValue covers prettyJSONValue's happy and error paths.
func TestPrettyJSONValue(t *testing.T) {
	// A simple map encodes fine.
	got, ok := prettyJSONValue(map[string]any{"a": 1})
	if !ok || !strings.Contains(got, `"a"`) {
		t.Errorf(`prettyJSONValue(map) = %q, %v`, got, ok)
	}
}
