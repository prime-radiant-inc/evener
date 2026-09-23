package agent

import (
	"encoding/base64"
	"encoding/json"
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
// preserving the human-readable primary case. SentArguments() returns the
// stored value verbatim — the read side the doctor displays.
func TestAssistantHistoryMessage_RawArgumentsPreservesInvalidUTF8(t *testing.T) {
	cases := []struct {
		name      string
		args      json.RawMessage
		wantRaw   string // expected raw_arguments after JSON round-trip
		validUTF8 bool
	}{
		{
			name:      "invalid_utf8_base64_prefixed",
			args:      json.RawMessage(`{"value": broken` + "\xff"),
			wantRaw:   rawArgumentsBase64Prefix + base64.StdEncoding.EncodeToString([]byte(`{"value": broken`+"\xff")),
			validUTF8: false,
		},
		{
			name:      "valid_utf8_plain_string",
			args:      json.RawMessage(`{"value": broken`),
			wantRaw:   `{"value": broken`,
			validUTF8: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if utf8.Valid(tc.args) != tc.validUTF8 {
				t.Fatalf("fixture UTF-8 validity = %v, want %v", !tc.validUTF8, tc.validUTF8)
			}
			if json.Valid(tc.args) {
				t.Fatalf("fixture must not be valid JSON so the recording branch fires")
			}

			decodedCall := recordAndRoundTrip(t, tc.args)
			if got := decodedCall.RawArguments; got != tc.wantRaw {
				t.Fatalf("round-tripped raw_arguments = %q, want %q", got, tc.wantRaw)
			}
			// SentArguments() returns the stored raw_arguments verbatim — the
			// read-side value the doctor transcript render displays. For the
			// base64 case that is the "base64:"-prefixed encoding, not the
			// original bytes (no read-side decoder).
			if got, want := decodedCall.SentArguments(), tc.wantRaw; got != want {
				t.Fatalf("SentArguments() = %q, want the stored raw_arguments verbatim %q", got, want)
			}
		})
	}
}

// recordAndRoundTrip runs the recording path on a single tool-call message and
// returns the decoded ToolCallData as it survives a JSON marshal/unmarshal of
// the stored turn — the same serialization the transcript writer performs.
func recordAndRoundTrip(t *testing.T, arguments json.RawMessage) *llm.ToolCallData {
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
	// the transcript writer does, then read the tool call from the stored record.
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
	return decodedCall
}
