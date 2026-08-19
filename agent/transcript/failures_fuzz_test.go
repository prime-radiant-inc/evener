//go:build evenerfuzz

package transcript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// FuzzToolFailureRule drives the failure rule and the counter built on it. Both
// were entirely unreached by fuzzing, and both read values the model produces:
// a tool's opaque tool state, and the names a turn announces.
//
// The rule matters beyond a number. FailedToolResult is deliberately the single
// definition shared by the daemon counting failures as it writes them and the
// hub counting them by reading a finished transcript back, so a disagreement
// shows up as a session whose failure count contradicts the glyphs drawn beside
// it. The oracles are the rule's own documented clauses:
//
//   - communicate is never a failure, whatever else is true of it, because
//     ProjectTurn drops those results and they render no row to disagree with.
//   - An errored result is always a failure, except communicate.
//   - A non-shell result that did not error is never a failure, no matter what
//     its tool state says — only shell tools carry an exit code.
//   - A shell result that did not error is a failure exactly when its state
//     records a nonzero exit code. Absent, unreadable, or field-less state
//     reads as "no exit code recorded", which is not a failure.
//   - ExitCodeFromToolState is total over arbitrary bytes and never reports a
//     code it did not read.
func FuzzToolFailureRule(f *testing.F) {
	f.Add("shell", false, []byte(`{"exit_code":1}`))
	f.Add("shell", false, []byte(`{"exit_code":0}`))
	f.Add("shell", false, []byte(`{}`))
	f.Add("shell", false, []byte(``))
	f.Add("shell", false, []byte(`not json`))
	f.Add("communicate", true, []byte(`{"exit_code":9}`))
	f.Add("read", true, []byte(``))
	f.Add("read", false, []byte(`{"exit_code":7}`))
	f.Add("exec_command", false, []byte(`{"exit_code":-1}`))

	f.Fuzz(func(t *testing.T, name string, isError bool, state []byte) {
		if len(state) > 4096 {
			t.Skip()
		}
		raw := json.RawMessage(state)

		code := ExitCodeFromToolState(raw)
		if len(state) == 0 && code != nil {
			t.Fatalf("ExitCodeFromToolState(empty) = %d, want nil", *code)
		}
		if code != nil && !json.Valid(state) {
			t.Fatalf("ExitCodeFromToolState read %d out of state that is not valid JSON: %q", *code, state)
		}

		failed := FailedToolResult(name, isError, raw)

		switch {
		case name == "communicate":
			if failed {
				t.Fatalf("communicate counted as a failure (isError=%v, state=%q)", isError, state)
			}
		case isError:
			if !failed {
				t.Fatalf("errored %q result not counted as a failure", name)
			}
		case !IsShellTool(name):
			if failed {
				t.Fatalf("clean non-shell %q result counted as a failure on state %q", name, state)
			}
		default:
			want := code != nil && *code != 0
			if failed != want {
				t.Fatalf("shell %q with state %q: failed=%v, want %v (exit code %v)", name, state, failed, want, code)
			}
		}
	})
}

// FuzzFailureCounterAttribution drives the counter over synthesized turns.
//
// Its load-bearing property is attribution: a fork child's transcript opens
// with a verbatim copy of its parent's prefix, and charging those failures to
// the child is the bug fromEntryOrdinal exists to prevent. Raising that ordinal
// may only ever lower the count — never raise it — and a name announced before
// the cut must still resolve a result that lands after it.
func FuzzFailureCounterAttribution(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3}, 0)
	f.Add([]byte{1, 1, 1}, 2)
	f.Add([]byte{}, 5)
	f.Add([]byte{2, 2, 2, 2, 2}, 1)

	f.Fuzz(func(t *testing.T, script []byte, from int) {
		if len(script) > 512 {
			script = script[:512]
		}
		if from < 0 {
			from = -from
		}
		from %= 64

		// Each byte becomes one turn: a call announcing a name, a result that
		// carries its own name, or a result that omits it and must be resolved
		// from the call seen earlier.
		turns := make([]schema.Turn, 0, len(script))
		for i, b := range script {
			id := string(rune('a' + i%26))
			var part llm.ContentPart
			switch b % 3 {
			case 0:
				part = llm.ContentPart{
					Kind:     llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{ID: id, Name: ShellToolNames[int(b)%len(ShellToolNames)]},
				}
			case 1:
				part = llm.ContentPart{
					Kind: llm.ContentToolResult,
					ToolResult: &llm.ToolResultData{
						ToolCallID: id,
						Name:       "read",
						IsError:    b%2 == 0,
					},
				}
			default:
				part = llm.ContentPart{
					Kind: llm.ContentToolResult,
					ToolResult: &llm.ToolResultData{
						ToolCallID: id,
						ToolState:  json.RawMessage(`{"exit_code":1}`),
					},
				}
			}
			turns = append(turns, schema.Turn{Message: llm.Message{Content: []llm.ContentPart{part}}})
		}

		count := func(fromOrdinal int) int {
			c := NewFailureCounter(fromOrdinal)
			last := 0
			for _, turn := range turns {
				c.Observe(turn)
				// Counting forward only: a live session's figure is never a
				// window, so it must never move backwards as turns arrive.
				if c.Count() < last {
					t.Fatalf("count went backwards: %d after %d", c.Count(), last)
				}
				last = c.Count()
			}
			return c.Count()
		}

		all := count(0)
		if got := count(1); got != all {
			t.Fatalf("ordinal 0 counted %d but ordinal 1 counted %d; both mean \"inherited nothing\"", all, got)
		}
		if got := count(from); got > all {
			t.Fatalf("starting at ordinal %d counted %d, more than counting everything (%d)", from, got, all)
		}
		if all > len(turns) {
			t.Fatalf("counted %d failures across %d single-part turns", all, len(turns))
		}

		// A nil counter is a no-op rather than a panic: callers hold one only
		// when a session tracks failures at all.
		var nilCounter *FailureCounter
		nilCounter.Observe(schema.Turn{})
		if nilCounter.Count() != 0 {
			t.Fatal("nil counter reported a nonzero count")
		}
	})
}
