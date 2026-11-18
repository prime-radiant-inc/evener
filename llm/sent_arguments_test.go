package llm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestSentArgumentsBytes asserts the byte-returning variant returns
// byte-identical content to []byte(SentArguments()) across every shape the
// three rendering sites (agent/transcript_render.go writeToolCardLine and
// writeResultToolMessage, and agent/doctor/health.go toolCallSignature) pass
// through the accessor — including the raw-args/invalid-JSON shape where
// Arguments holds the replay-safe {} placeholder and RawArguments holds the
// model's original malformed bytes.
func TestSentArgumentsBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tc   *ToolCallData
	}{
		{"valid json arguments", &ToolCallData{Arguments: json.RawMessage(`{"command":"ls -la"}`)}},
		{"empty object arguments", &ToolCallData{Arguments: json.RawMessage(`{}`)}},
		{"nil arguments", &ToolCallData{}},
		{"raw malformed raw-args shape", &ToolCallData{Arguments: json.RawMessage(`{}`), RawArguments: `{command: "ls", }`}},
		{"raw-args precedence over non-placeholder arguments", &ToolCallData{Arguments: json.RawMessage(`{"x":1}`), RawArguments: `{message: "done", }`}},
		{"raw-args with nil arguments", &ToolCallData{Arguments: nil, RawArguments: `{bare key`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.tc.SentArgumentsBytes()
			// Byte-identical content to []byte(SentArguments()) across every shape.
			want := []byte(c.tc.SentArguments())
			if !bytes.Equal(got, want) {
				t.Fatalf("SentArgumentsBytes() = %q, want %q ([]byte(SentArguments()))", got, want)
			}
			// Precedence matches SentArguments: RawArguments wins when set, else
			// the recorded Arguments.
			if c.tc.RawArguments != "" {
				if string(got) != c.tc.RawArguments {
					t.Errorf("precedence: got %q, want RawArguments %q", got, c.tc.RawArguments)
				}
			} else if !bytes.Equal(got, c.tc.Arguments) {
				t.Errorf("Arguments branch: got %q, want tc.Arguments %q", got, c.tc.Arguments)
			}
		})
	}
}

func TestToolCallDataArgumentBytesRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, arguments, diagnostic string
		needsBytes                  bool
	}{
		{"compact", `{"z":1e2,"a":"\u0061"}`, "", false},
		{"formatted", " \t" + `{ "z" : 1e2, "a" : "\u0061" }` + "\n", "", true},
		{"HTML-sensitive string", `{"value":"<>&"}`, "", true},
		{"malformed diagnostic", "{}", `{"x":nope}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := ToolCallData{Arguments: json.RawMessage(tc.arguments), RawArguments: tc.diagnostic}
			record, err := json.Marshal(call)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Arguments     json.RawMessage `json:"arguments"`
				ArgumentBytes []byte          `json:"arguments_bytes"`
			}
			if err := json.Unmarshal(record, &saved); err != nil {
				t.Fatal(err)
			}
			if len(saved.Arguments) == 0 || (saved.ArgumentBytes != nil) != tc.needsBytes {
				t.Errorf("stored arguments = %q, original byte count = %d", saved.Arguments, len(saved.ArgumentBytes))
			}
			var restored ToolCallData
			if err := json.Unmarshal(record, &restored); err != nil {
				t.Fatal(err)
			}
			if string(restored.Arguments) != tc.arguments || restored.RawArguments != tc.diagnostic {
				t.Errorf("restored arguments = %q, diagnostic = %q", restored.Arguments, restored.RawArguments)
			}
		})
	}
}

func TestToolCallDataKeepsStructuredArgumentsWithoutMatchingBytes(t *testing.T) {
	t.Parallel()
	t.Run("damaged replay-only field", func(t *testing.T) {
		var call ToolCallData
		if err := json.Unmarshal([]byte(`{"arguments_replay":"!"}`), &call); err == nil {
			t.Fatal("damaged sole argument field was accepted")
		}
		if err := json.Unmarshal([]byte(`{"arguments":{"x":7},"arguments_replay":"!"}`), &call); err != nil {
			t.Fatal(err)
		}
		if got := call.SentArguments(); got != `{"x":7}` {
			t.Errorf("structured arguments = %q", got)
		}
	})
	t.Run("changed structured value with the same length", func(t *testing.T) {
		original := "{ \"path\": \"" + strings.Repeat("a", 1024) + "\" }\n"
		data, err := json.Marshal(ToolCallData{Arguments: json.RawMessage(original)})
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		fields["arguments"] = bytes.Replace(fields["arguments"], []byte("aaa"), []byte("bbb"), 1)
		updated, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		var call ToolCallData
		if err := json.Unmarshal(updated, &call); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(call.Arguments, fields["arguments"]) {
			t.Error("restoring the edited argument applied a spelling from another value")
		}
	})
	for _, tc := range []struct {
		name, original, diagnostic string
	}{
		{"legacy record", "", ""},
		{"conflicting value", `{"x":8}`, ""},
		{"invalid original", `{"x":`, ""},
		{"diagnostic record", "{ \"x\":7 }", `{"x":broken}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const arguments = `{"x":7}`
			data, err := json.Marshal(struct {
				Arguments    json.RawMessage `json:"arguments"`
				Original     []byte          `json:"arguments_bytes,omitempty"`
				RawArguments string          `json:"raw_arguments,omitempty"`
				Extra        bool            `json:"extra"`
			}{json.RawMessage(arguments), []byte(tc.original), tc.diagnostic, true})
			if err != nil {
				t.Fatal(err)
			}
			var call ToolCallData
			if err := json.Unmarshal(data, &call); err != nil {
				t.Fatal(err)
			}
			if string(call.Arguments) != arguments || call.RawArguments != tc.diagnostic {
				t.Errorf("restored arguments = %q, diagnostic = %q", call.Arguments, call.RawArguments)
			}
		})
	}
}
