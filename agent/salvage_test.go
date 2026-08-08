package agent

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// This file pins salvageText and partialJSONStringFields: turning a partial
// (never-finished) *llm.Response into the text persisted as a salvaged
// assistant turn, per docs/superpowers/specs/2026-08-07-provider-failure-feedback-design.md
// "Component 3: partial-preserving settlement".

func toolCallPart(name, args string) llm.ContentPart {
	return llm.ContentPart{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        "call_1",
			Name:      name,
			Arguments: []byte(args),
		},
	}
}

func textPart(text string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentText, Text: text}
}

func thinkingPart(text string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: text}}
}

func responseWith(parts ...llm.ContentPart) *llm.Response {
	return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: parts}}
}

func TestSalvageText_TruncatedToolCallArgs(t *testing.T) {
	partial := responseWith(toolCallPart("write_file", `{"path":"a.md","content":"# Plan\nlots of tex`))

	got := salvageText(partial)

	want := "[incomplete tool call: write_file — this call never executed]\n" +
		"path: a.md\n" +
		"content: # Plan\nlots of tex"
	if got != want {
		t.Fatalf("salvageText() = %q, want %q", got, want)
	}
}

func TestSalvageText_TextThenToolMarkerOrdering(t *testing.T) {
	partial := responseWith(
		textPart("here's my plan: "),
		toolCallPart("write_file", `{"path":"a.md"}`),
	)

	got := salvageText(partial)

	want := "here's my plan: \n\n" +
		"[incomplete tool call: write_file — this call never executed]\n" +
		"path: a.md"
	if got != want {
		t.Fatalf("salvageText() = %q, want %q", got, want)
	}
}

func TestSalvageText_ReasoningOnly_ReturnsEmpty(t *testing.T) {
	partial := responseWith(thinkingPart("thinking hard about the plan"))

	if got := salvageText(partial); got != "" {
		t.Fatalf("salvageText() = %q, want empty (reasoning is never salvaged)", got)
	}
}

func TestSalvageText_ReasoningIgnoredAlongsideText(t *testing.T) {
	partial := responseWith(
		thinkingPart("thinking hard about the plan"),
		textPart("here's my plan"),
	)

	if got := salvageText(partial); got != "here's my plan" {
		t.Fatalf("salvageText() = %q, want %q", got, "here's my plan")
	}
}

func TestSalvageText_Nil_ReturnsEmpty(t *testing.T) {
	if got := salvageText(nil); got != "" {
		t.Fatalf("salvageText(nil) = %q, want empty", got)
	}
}

func TestSalvageText_MultipleToolCalls_EachGetsAMarker(t *testing.T) {
	partial := responseWith(
		toolCallPart("write_file", `{"path":"a.md"}`),
		toolCallPart("write_file", `{"path":"b.md"}`),
	)

	got := salvageText(partial)

	want := "[incomplete tool call: write_file — this call never executed]\n" +
		"path: a.md\n\n" +
		"[incomplete tool call: write_file — this call never executed]\n" +
		"path: b.md"
	if got != want {
		t.Fatalf("salvageText() = %q, want %q", got, want)
	}
}

func TestPartialJSONStringFields_TruncatedContentTail(t *testing.T) {
	raw := `{"path":"a.md","content":"# Plan\nlots of tex`

	got := partialJSONStringFields(raw)

	want := []struct{ Key, Value string }{
		{"path", "a.md"},
		{"content", "# Plan\nlots of tex"},
	}
	assertFields(t, got, want)
}

func TestPartialJSONStringFields_SkipsNonStringTopLevelFields(t *testing.T) {
	raw := `{"count":5,"name":"foo","nested":{"a":"b"},"ok":true,"list":[1,2,3],"tag":"bar"}`

	got := partialJSONStringFields(raw)

	want := []struct{ Key, Value string }{
		{"name", "foo"},
		{"tag", "bar"},
	}
	assertFields(t, got, want)
}

func TestPartialJSONStringFields_EmptyOnNonObject(t *testing.T) {
	if got := partialJSONStringFields("not json"); len(got) != 0 {
		t.Fatalf("partialJSONStringFields() = %+v, want empty", got)
	}
}

func TestPartialJSONStringFields_EmptyObject(t *testing.T) {
	if got := partialJSONStringFields("{}"); len(got) != 0 {
		t.Fatalf("partialJSONStringFields() = %+v, want empty", got)
	}
}

func TestPartialJSONStringFields_TruncatedMidKey(t *testing.T) {
	raw := `{"path":"a.md","cont`

	got := partialJSONStringFields(raw)

	want := []struct{ Key, Value string }{
		{"path", "a.md"},
	}
	assertFields(t, got, want)
}

func assertFields(t *testing.T, got, want []struct{ Key, Value string }) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("partialJSONStringFields() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("field %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
