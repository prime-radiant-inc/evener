package openaichat

import (
	"encoding/json"
	"testing"
)

func TestToolArgumentsString(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "object", raw: json.RawMessage(`{"status":"in_progress"}`), want: `{"status":"in_progress"}`},
		{name: "empty", raw: nil, want: `{}`},
		{name: "malformed", raw: json.RawMessage(`{"status": in_progress"}`), want: `{}`},
		{name: "non_object", raw: json.RawMessage(`["status"]`), want: `{}`},
		{name: "null", raw: json.RawMessage(`null`), want: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolArgumentsString(tc.raw); got != tc.want {
				t.Fatalf("ToolArgumentsString(%q) = %q, want %q", string(tc.raw), got, tc.want)
			}
		})
	}
}
