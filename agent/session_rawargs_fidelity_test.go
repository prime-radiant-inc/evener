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
// stored value verbatim — the display-only read side the doctor renders.
//
// Two classes of arguments must be preserved:
//   - not valid JSON (the original malformed-args case): Arguments holds the
//     replay-safe {} placeholder.
//   - valid JSON but not valid UTF-8 (Go's json.Valid does not check UTF-8):
//     Arguments holds the {} placeholder too — the true bytes are not safe to
//     carry in a JSON field that will be marshaled, so they must live in
//     RawArguments.
func TestAssistantHistoryMessage_RawArgumentsPreservesInvalidUTF8(t *testing.T) {
	cases := []struct {
		name      string
		args      json.RawMessage
		wantRaw   string // expected raw_arguments after JSON round-trip
		validUTF8 bool
		validJSON bool
	}{
		{
			name:      "not_valid_json_invalid_utf8_base64_prefixed",
			args:      json.RawMessage(`{"value": broken` + "\xff"),
			wantRaw:   rawArgumentsBase64Prefix + base64.StdEncoding.EncodeToString([]byte(`{"value": broken`+"\xff")),
			validUTF8: false,
			validJSON: false,
		},
		{
			name:      "not_valid_json_valid_utf8_plain_string",
			args:      json.RawMessage(`{"value": broken`),
			wantRaw:   `{"value": broken`,
			validUTF8: true,
			validJSON: false,
		},
		{
			// Valid JSON but invalid UTF-8: Go's json.Valid does not validate
			// UTF-8, so this passes json.Valid while carrying \xff inside a
			// string value. ValidateRawArguments (registry.go) rejects it as
			// "input is not valid UTF-8", so the call never dispatches — but
			// the recording branch must still preserve the bytes so the
			// durable record shows what the model actually sent, not the
			// U+FFFD-coerced form json.Marshal would produce.
			name:      "valid_json_invalid_utf8_base64_prefixed",
			args:      json.RawMessage("{\"end_turn\":true,\"message\":\"top-level\",\"output\":\"{\xff}\"}"),
			wantRaw:   rawArgumentsBase64Prefix + base64.StdEncoding.EncodeToString([]byte("{\"end_turn\":true,\"message\":\"top-level\",\"output\":\"{\xff}\"}")),
			validUTF8: false,
			validJSON: true,
		},
		{
			// A literal argument text that begins with "base64:" but is valid
			// UTF-8 and not valid JSON: stored verbatim — the prefix is
			// display-only and not a decode contract, so a literal "base64:"
			// value is indistinguishable from an encoded payload. The
			// SentArguments() doc states this.
			name:      "literal_base64_prefix_valid_utf8_plain_string",
			args:      json.RawMessage(`base64:not-json`),
			wantRaw:   `base64:not-json`,
			validUTF8: true,
			validJSON: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if utf8.Valid(tc.args) != tc.validUTF8 {
				t.Fatalf("fixture UTF-8 validity = %v, want %v", !tc.validUTF8, tc.validUTF8)
			}
			if json.Valid(tc.args) != tc.validJSON {
				t.Fatalf("fixture JSON validity = %v, want %v", !tc.validJSON, tc.validJSON)
			}

			decodedCall := recordAndRoundTrip(t, tc.args)
			if got := decodedCall.RawArguments; got != tc.wantRaw {
				t.Fatalf("round-tripped raw_arguments = %q, want %q", got, tc.wantRaw)
			}
			// SentArguments() returns the stored raw_arguments verbatim — the
			// display-only read-side value the doctor transcript render
			// displays. It is NOT a decode contract: for the base64 case the
			// returned value is the "base64:"-prefixed encoding, not the
			// original bytes, and a literal "base64:" argument is stored
			// verbatim and indistinguishable.
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
		t.Fatalf("raw arguments not recorded for call needing preservation")
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

// TestAssistantHistoryMessage_PassthroughValidArgs verifies the recording
// predicate does not fire for the common case: arguments that are both valid
// JSON and valid UTF-8 pass through untouched — Arguments keeps the original
// bytes and RawArguments stays empty. This guards against the widened
// !(json.Valid && utf8.Valid) predicate ever clobbering a good call with the
// {} placeholder.
func TestAssistantHistoryMessage_PassthroughValidArgs(t *testing.T) {
	args := json.RawMessage(`{"a":1}`)
	if !json.Valid(args) || !utf8.Valid(args) {
		t.Fatalf("fixture must be valid JSON and valid UTF-8")
	}

	msg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_passthrough",
				Name:      "my_tool",
				Arguments: args,
			},
		}},
	}

	recorded := assistantHistoryMessage(msg)
	call := recorded.Content[0].ToolCall
	if call == nil {
		t.Fatalf("missing tool call after recording")
	}
	if got, want := string(call.Arguments), `{"a":1}`; got != want {
		t.Fatalf("passthrough arguments = %q, want %q (untouched)", got, want)
	}
	if call.RawArguments != "" {
		t.Fatalf("passthrough raw_arguments = %q, want empty (no recording)", call.RawArguments)
	}

	// Round-trip through JSON the same way the transcript writer does.
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
	if got, want := string(decodedCall.Arguments), `{"a":1}`; got != want {
		t.Fatalf("round-tripped passthrough arguments = %q, want %q", got, want)
	}
	if decodedCall.RawArguments != "" {
		t.Fatalf("round-tripped passthrough raw_arguments = %q, want empty", decodedCall.RawArguments)
	}
}
