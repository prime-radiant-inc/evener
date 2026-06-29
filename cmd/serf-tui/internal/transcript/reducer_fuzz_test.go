package transcript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/appwire"
)

// FuzzApplyThreadItem drives the reducer's real fold seam: ApplyThreadItem
// dispatches on item.Type and, for tool items, decodes the untrusted item.Raw /
// item.Output JSON via subagentRunFromToolItem (json.Unmarshal of the wire
// payload). Feeding a fuzzed ThreadItem (type + raw JSON + fields) through a
// fresh reducer exercises the JSON decode plus every fold branch. The companion
// ApplySerfJob path is driven from the same fuzzed bytes. Oracle: no-panic floor
// plus a structural invariant — every active-index map entry must point at a
// real message slot after the fold.
func FuzzApplyThreadItem(f *testing.F) {
	seeds := []struct {
		typ, raw, output, toolName, args string
	}{
		{"agentMessage", "", "", "", ""},
		{"reasoning", "", "", "", ""},
		{"userMessage", "", "", "", ""},
		{"systemMessage", "", "", "", ""},
		{"commandExecution", `{"job_id":"j1","type":"delegate","status":"running"}`, "", "delegate", `{"task":"x"}`},
		{"commandExecution", `{"delegate_id":"d1","status":"completed","task":"t"}`, "", "delegate", ""},
		{"commandExecution", "", `{"job_id":"j2","type":"shell"}`, "shell", `{"command":"ls"}`},
		{"commandExecution", `{"current_job_id":"c","total_bytes":42}`, "", "delegate_send", `{"to":"d"}`},
		{"commandExecution", "not json", "", "read_file", `{"file_path":"/a"}`},
	}
	for _, s := range seeds {
		f.Add(s.typ, s.raw, s.output, s.toolName, s.args)
	}

	f.Fuzz(func(t *testing.T, typ, raw, output, toolName, args string) {
		item := appwire.ThreadItem{
			ID:            "item-1",
			TurnID:        "turn_3",
			CallID:        "call-1",
			Type:          typ,
			Text:          output,
			Output:        output,
			ToolName:      toolName,
			ArgumentsJSON: args,
		}
		if json.Valid([]byte(raw)) {
			item.Raw = json.RawMessage(raw)
		} else if raw != "" {
			item.Raw = json.RawMessage(raw) // intentionally feed invalid bytes too
		}

		r := NewTranscriptReducer(nil, nil, nil)
		r.ApplyThreadItem(item, TurnIndexFromID(item.TurnID), false)
		r.ApplyThreadItem(item, TurnIndexFromID(item.TurnID), true)

		// Drive the job-folding decode path from the same payload.
		var job appwire.SerfJobInfo
		if json.Unmarshal([]byte(raw), &job) == nil {
			r.ApplySerfJob(job)
		}

		// Structural invariant: active maps must never dangle past the message slice.
		n := len(r.Messages())
		for id, idx := range r.ActiveTools() {
			if idx < 0 || idx >= n {
				t.Fatalf("activeTools[%q]=%d out of range (len=%d)", id, idx, n)
			}
		}
		for id, idx := range r.ActiveMessages() {
			if idx < 0 || idx >= n {
				t.Fatalf("activeMessages[%q]=%d out of range (len=%d)", id, idx, n)
			}
		}
	})
}
