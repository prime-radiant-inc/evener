package llm

import (
	"bytes"
	"encoding/json"
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
