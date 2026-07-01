package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestS3Cov_ClampRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		lo, hi, count  int
		wantLo, wantHi int
	}{
		{-5, 100, 10, 0, 9}, // both clamped to bounds
		{3, 7, 10, 3, 7},    // in range unchanged
		{20, 30, 10, 9, 9},  // lo past end clamped to last
		{5, -3, 10, 5, 0},   // hi negative clamped to 0
	}
	for _, tc := range tests {
		lo, hi, err := clampRange(tc.lo, tc.hi, tc.count)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if lo != tc.wantLo || hi != tc.wantHi {
			t.Errorf("clampRange(%d,%d,%d) = %d,%d want %d,%d", tc.lo, tc.hi, tc.count, lo, hi, tc.wantLo, tc.wantHi)
		}
	}
}

func TestS3Cov_ParseDashRange(t *testing.T) {
	t.Parallel()
	if lo, hi, ok := parseDashRange("3-7"); !ok || lo != 3 || hi != 7 {
		t.Fatalf("3-7 => %d,%d,%v", lo, hi, ok)
	}
	for _, bad := range []string{"nodash", "-5", "3-", "a-b", "-1-2", "3"} {
		if _, _, ok := parseDashRange(bad); ok {
			t.Errorf("parseDashRange(%q) unexpectedly ok", bad)
		}
	}
}

func TestS3Cov_ScalarStringAndFormatNumber(t *testing.T) {
	t.Parallel()
	cases := map[any]string{
		"str":               "str",
		true:                "true",
		float64(42):         "42",
		float64(3.5):        "3.5",
		json.Number("1000"): "1000",
	}
	for v, want := range cases {
		if got := scalarString(v); got != want {
			t.Errorf("scalarString(%v) = %q, want %q", v, got, want)
		}
	}
	// Objects/arrays/null render empty.
	if scalarString(map[string]any{"a": 1}) != "" || scalarString(nil) != "" {
		t.Fatal("structured values should render empty")
	}
	if formatNumber(5.0) != "5" || formatNumber(2.25) != "2.25" {
		t.Fatalf("formatNumber wrong")
	}
}

func TestS3Cov_ToolPurpose(t *testing.T) {
	t.Parallel()
	if got := toolPurpose(json.RawMessage(`{"purpose":"explain"}`)); got != "explain" {
		t.Fatalf("purpose = %q", got)
	}
	if got := toolPurpose(json.RawMessage(`{"intent":"find bug"}`)); got != "find bug" {
		t.Fatalf("intent = %q", got)
	}
	if got := toolPurpose(json.RawMessage(`{"other":"x"}`)); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := toolPurpose(nil); got != "" {
		t.Fatalf("nil args => %q", got)
	}
}

func TestS3Cov_HostOf(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                        "",
		"https://example.com/a/b": "example.com",
		"not a url":               "not a url",
		"https://host:8080/path":  "host:8080",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestS3Cov_PrettyJSON(t *testing.T) {
	t.Parallel()
	got, ok := prettyJSON(`{"b":1,"a":2}`)
	if !ok || !strings.Contains(got, "\n") {
		t.Fatalf("expected indented JSON, got %q ok=%v", got, ok)
	}
	if _, ok := prettyJSON("plain text"); ok {
		t.Fatal("plain text should not be pretty JSON")
	}
	if _, ok := prettyJSON("42"); ok {
		t.Fatal("bare scalar should not be attempted")
	}
	if _, ok := prettyJSON(""); ok {
		t.Fatal("empty should be false")
	}
}

func TestS3Cov_PrettyJSONValue(t *testing.T) {
	t.Parallel()
	got, ok := prettyJSONValue(map[string]any{"k": "v"})
	if !ok || !strings.Contains(got, `"k"`) {
		t.Fatalf("prettyJSONValue = %q ok=%v", got, ok)
	}
}

// s3cov_entries builds transcript entries at seq 0..n-1.
func s3cov_entries(turns ...schema.Turn) []transcript.Entry {
	out := make([]transcript.Entry, len(turns))
	for i, tn := range turns {
		out[i] = transcript.Entry{Kind: "entry", Seq: i, Turn: tn}
	}
	return out
}

func TestS3Cov_OwningAndCallOwnerSeq(t *testing.T) {
	t.Parallel()

	callMsg := llm.Assistant("calling")
	callMsg.Content = append(callMsg.Content, llm.ContentPart{
		Kind:     llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{ID: "call-1", Name: "read_file"},
	})
	entries := s3cov_entries(
		schema.NewTurn(schema.TurnUserInput, llm.User("hi")),
		schema.NewTurn(schema.TurnAssistant, callMsg),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call-1", "ok", false)),
	)

	if seq, ok := callOwnerSeq(entries, "call-1"); !ok || seq != 1 {
		t.Fatalf("callOwnerSeq = %d,%v want 1,true", seq, ok)
	}
	if _, ok := callOwnerSeq(entries, "missing"); ok {
		t.Fatal("expected miss for unknown id")
	}

	// Assistant turn owns itself.
	if seq, ok := owningAssistantSeq(entries, 1); !ok || seq != 1 {
		t.Fatalf("owningAssistantSeq(assistant) = %d,%v", seq, ok)
	}
	// Tool result maps back to the issuing assistant.
	if seq, ok := owningAssistantSeq(entries, 2); !ok || seq != 1 {
		t.Fatalf("owningAssistantSeq(toolresult) = %d,%v want 1,true", seq, ok)
	}
	// User turn owns nothing.
	if _, ok := owningAssistantSeq(entries, 0); ok {
		t.Fatal("user turn should own no assistant seq")
	}
}

func TestS3Cov_JobResultBody(t *testing.T) {
	t.Parallel()

	t.Run("renders job result with output", func(t *testing.T) {
		t.Parallel()
		raw := `{"job_id":"J1","status":"completed","transcript_ref":"local:abc","output":"line one\nline two"}`
		body, ok := jobResultBody(raw)
		if !ok {
			t.Fatal("expected ok")
		}
		for _, want := range []string{"job_id=J1", "status=completed", "transcript_ref=local:abc", "line one"} {
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q:\n%s", want, body)
			}
		}
	})

	t.Run("non-job JSON returns false", func(t *testing.T) {
		t.Parallel()
		if _, ok := jobResultBody(`{"unrelated":"value"}`); ok {
			t.Fatal("expected false for non-job body")
		}
	})

	t.Run("extra keys returns false", func(t *testing.T) {
		t.Parallel()
		if _, ok := jobResultBody(`{"job_id":"J1","status":"ok","transcript_ref":"r","surprise":1}`); ok {
			t.Fatal("expected false when unknown keys present")
		}
	})
}
