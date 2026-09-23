package agent

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestAssistantHistoryMessage_RawArgumentsPreservesInvalidUTF8 verifies the
// recording path stores raw tool-call argument bytes losslessly even when they
// contain invalid UTF-8. The durable transcript is serialized as JSON, and
// json.Marshal coerces invalid-UTF-8 bytes in a string field to U+FFFD, so the
// raw bytes must survive the JSON round-trip the transcript writer performs.
//
// FU6: when the raw bytes are not valid UTF-8, the recording site stores them
// base64-encoded with the "base64:" prefix so json.Marshal cannot mangle them;
// when they are valid UTF-8, it stores the plain string unchanged (no marker),
// preserving the human-readable primary case.
func TestAssistantHistoryMessage_RawArgumentsPreservesInvalidUTF8(t *testing.T) {
	// Raw argument bytes containing an invalid UTF-8 byte (0xff). The model
	// sent these; the durable record must preserve them losslessly. The
	// payload is malformed JSON (a bareword value), so it is not valid JSON
	// and the assistantHistoryMessage recording branch fires, just as it does
	// for the ASCII malformed-args case in session_openai_malformed_tool_call_test.
	invalidArgs := json.RawMessage(`{"value": broken` + "\xff")
	if utf8.Valid(invalidArgs) {
		t.Fatalf("test fixture must contain invalid UTF-8")
	}
	if json.Valid(invalidArgs) {
		t.Fatalf("test fixture must not be valid JSON so the recording branch fires")
	}

	invalidRaw := recordAndRoundTrip(t, invalidArgs)
	if want := rawArgumentsBase64Prefix + base64.StdEncoding.EncodeToString(invalidArgs); invalidRaw != want {
		t.Fatalf("invalid-UTF-8 round-tripped raw_arguments = %q, want base64-prefixed %q", invalidRaw, want)
	}
	if !strings.HasPrefix(invalidRaw, rawArgumentsBase64Prefix) {
		t.Fatalf("invalid-UTF-8 raw_arguments = %q, want the %q marker", invalidRaw, rawArgumentsBase64Prefix)
	}

	// Valid-UTF-8 malformed args (the existing primary case: a bareword value)
	// must store the plain string unchanged — no marker — so the diagnostic
	// stays human-readable.
	validArgs := json.RawMessage(`{"value": broken`)
	if !utf8.Valid(validArgs) {
		t.Fatalf("valid-UTF-8 fixture must be valid UTF-8")
	}
	if json.Valid(validArgs) {
		t.Fatalf("valid-UTF-8 fixture must not be valid JSON so the recording branch fires")
	}
	validRaw := recordAndRoundTrip(t, validArgs)
	if want := string(validArgs); validRaw != want {
		t.Fatalf("valid-UTF-8 round-tripped raw_arguments = %q, want the plain bytes verbatim %q (no marker)", validRaw, want)
	}
	if strings.HasPrefix(validRaw, rawArgumentsBase64Prefix) {
		t.Fatalf("valid-UTF-8 raw_arguments = %q, must not carry the base64 marker", validRaw)
	}
}

// recordAndRoundTrip runs the recording path on a single tool-call message and
// returns the raw_arguments value as it survives a JSON marshal/unmarshal of
// the stored turn — the same serialization the transcript writer performs.
func recordAndRoundTrip(t *testing.T, arguments json.RawMessage) string {
	t.Helper()

	msg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_under_test",
				Name:      "my_tool",
				Arguments: arguments,
			},
		}},
	}

	recorded := assistantHistoryMessage(msg)
	call := recorded.Content[0].ToolCall
	if call == nil {
		t.Fatalf("missing tool call after recording")
	}
	if string(call.Arguments) != `{}` {
		t.Fatalf("recorded arguments = %q, want replay-safe {}", call.Arguments)
	}
	if call.RawArguments == "" {
		t.Fatalf("raw arguments not recorded for invalid-JSON call")
	}

	// The durable transcript serializes the turn as JSON via json.NewEncoder.
	// Round-trip the recorded turn through json.Marshal/Unmarshal the same way
	// the transcript writer does, then read raw_arguments from the stored record.
	turn := schema.NewTurn(schema.TurnAssistant, recorded)
	encoded, err := json.Marshal(turn)
	if err != nil {
		t.Fatalf("marshal recorded turn: %v", err)
	}
	var decoded schema.Turn
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal recorded turn: %v", err)
	}
	decodedCall := decoded.Message.Content[0].ToolCall
	if decodedCall == nil {
		t.Fatalf("missing tool call after round-trip")
	}
	return decodedCall.RawArguments
}
