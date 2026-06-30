package contextmgr

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzSummarizeToolResult drives summarizeToolResult — the per-tool one-line
// result summarizer used by the masking strategies — together with the pure
// string/JSON helpers it fans out to (parseExitCode, extractJSONField,
// countJSONArrayElements, countLines, countNonEmptyLines) and the sibling
// parseCommunicateArgs. Input is an arbitrary tool name, an arbitrary content
// string, and arbitrary tool-call argument bytes, none of which the summarizer
// is allowed to trust.
//
// Oracles (never bare no-panic):
//   - summarizeToolResult always returns a bracketed token ("[" … "]") and is
//     deterministic. (The token may embed newlines from the verbatim tool
//     name/path/command it interpolates, so newline-freedom is NOT asserted.)
//   - parseCommunicateArgs honors its contract: a missing end_turn field yields
//     (false, ""), the returned message never has surrounding whitespace, and the
//     decode is deterministic.
//   - parseExitCode returns either "?" or a non-empty run of ASCII digits.
//   - countJSONArrayElements equals an independent json array decode's length.
//   - extractJSONField returns "" for any non-object input, and is deterministic.
//   - countLines("")==0 and countNonEmptyLines never exceeds the line count.
func FuzzSummarizeToolResult(f *testing.F) {
	f.Add("shell", "exit_code=0\nhello\n", `{"command":"ls -la"}`)
	f.Add("read_file", "a\nb\nc", `{"file_path":"/x/y.go"}`)
	f.Add("grep", "m1\nm2\n", `{"pattern":"foo"}`)
	f.Add("glob", "f1\n\nf2\n", `{"pattern":"*.go"}`)
	f.Add("delegate", `{"job_id":"job_123"}`, `{}`)
	f.Add("task_list", `[1,2,3]`, `{"action":"add"}`)
	f.Add("communicate", "msg", `{"message":"  hi  ","end_turn":true}`)
	f.Add("web_fetch", "body", `{"url":"http://x"}`)
	f.Add("unknown_tool", "whatever", `not json`)
	f.Add("shell", "garbage exit nope", ``)

	f.Fuzz(func(t *testing.T, toolName, content, argsStr string) {
		args := json.RawMessage(argsStr)

		// --- summarizeToolResult: bracketed, newline-free, deterministic. ---
		summary := summarizeToolResult(toolName, content, args)
		if !strings.HasPrefix(summary, "[") || !strings.HasSuffix(summary, "]") {
			t.Fatalf("summary not bracketed: %q (tool=%q)", summary, toolName)
		}
		if again := summarizeToolResult(toolName, content, args); again != summary {
			t.Fatalf("summarizeToolResult not deterministic: %q vs %q", summary, again)
		}

		// --- parseCommunicateArgs contract. ---
		endTurn, msg := parseCommunicateArgs(args)
		if msg != strings.TrimSpace(msg) {
			t.Fatalf("parseCommunicateArgs message has surrounding whitespace: %q", msg)
		}
		if et2, m2 := parseCommunicateArgs(args); et2 != endTurn || m2 != msg {
			t.Fatalf("parseCommunicateArgs not deterministic")
		}
		// A payload with no end_turn key must return (false, "").
		if !hasEndTurnField(args) && (endTurn || msg != "") {
			t.Fatalf("end_turn absent but got (%v, %q)", endTurn, msg)
		}

		// --- parseExitCode: "?" or a non-empty digit run. ---
		code := parseExitCode(content)
		if code != "?" {
			if code == "" || !allASCIIDigits(code) {
				t.Fatalf("parseExitCode returned non-digit %q", code)
			}
		}

		// --- countJSONArrayElements matches an independent array decode. ---
		var arr []any
		want := 0
		if json.Unmarshal([]byte(content), &arr) == nil {
			want = len(arr)
		}
		if got := countJSONArrayElements(content); got != want {
			t.Fatalf("countJSONArrayElements=%d want=%d for %q", got, want, content)
		}

		// --- extractJSONField: "" for any non-object input. ---
		var obj map[string]any
		isObject := json.Unmarshal([]byte(content), &obj) == nil
		field := extractJSONField(content, "job_id")
		if !isObject && field != "" {
			t.Fatalf("extractJSONField returned %q for non-object %q", field, content)
		}
		if f2 := extractJSONField(content, "job_id"); f2 != field {
			t.Fatalf("extractJSONField not deterministic")
		}

		// --- line counters. ---
		if countLines("") != 0 {
			t.Fatalf("countLines(\"\") != 0")
		}
		if n := countNonEmptyLines(content); n > len(strings.Split(content, "\n")) {
			t.Fatalf("countNonEmptyLines=%d exceeds line count", n)
		}
	})
}

// hasEndTurnField reports whether args is a JSON object carrying an "end_turn"
// key, mirroring the only branch under which parseCommunicateArgs may report a
// non-zero result.
func hasEndTurnField(args json.RawMessage) bool {
	var m map[string]json.RawMessage
	if len(args) == 0 || json.Unmarshal(args, &m) != nil {
		return false
	}
	_, ok := m["end_turn"]
	return ok
}

func allASCIIDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}
