//go:build serffuzz

package agent

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzFc1RepairOrphanedToolResults drives repairOrphanedToolResults — the pure
// interrupted-tool-call recovery core the session applies on restore — over
// adversarial histories built from a byte-encoded op stream. A history is a mix
// of assistant turns (with 0..N tool calls), tool-result turns resolving some of
// the outstanding calls, user turns, and steering turns. Interleaving an
// unresolved assistant call with any non-matching turn is exactly what leaves an
// orphaned call for repair to synthesize a result for.
//
// Oracles (beyond never-panic):
//   - determinism: the same history repairs to the same (out, count);
//   - non-negative repair count;
//   - identity on a clean history: when count == 0, out is the input unchanged;
//   - idempotency / well-formedness: repairing the output finds nothing left
//     (count2 == 0) and returns it unchanged — the invariant the production build
//     only checks under invariant.Enabled, asserted here for arbitrary inputs;
//   - no orphaned call after repair: every assistant tool call with a non-empty ID
//     has a matching tool result at a LATER turn;
//   - preservation: repair only INSERTS synthetic tool-result turns — it never
//     drops or reorders original turns, so every non-tool-result turn count is
//     preserved exactly and the tool-result turn count only grows.
func FuzzFc1RepairOrphanedToolResults(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x05})                   // one assistant with a call, never resolved
	f.Add([]byte{0x05, 0x00})             // call then user turn -> flush
	f.Add([]byte{0x05, 0x0a})             // call then resolve
	f.Add([]byte{0x09, 0x05, 0x00, 0x0a}) // two calls, one resolved late

	f.Fuzz(func(t *testing.T, data []byte) {
		history := fc1BuildHistory(data)

		out, count := repairOrphanedToolResults(history)
		out2, count2 := repairOrphanedToolResults(history)
		// Synthetic result turns are stamped with time.Now(), so compare content
		// with timestamps zeroed — the content is what must be deterministic.
		if count != count2 || !reflect.DeepEqual(fc1ZeroTimestamps(out), fc1ZeroTimestamps(out2)) {
			t.Fatalf("non-deterministic: (%d,%d) len %d vs %d", count, count2, len(out), len(out2))
		}
		if count < 0 {
			t.Fatalf("negative repair count %d", count)
		}
		if count == 0 && !reflect.DeepEqual(out, history) {
			t.Fatalf("clean history not returned unchanged")
		}

		// Idempotency / well-formedness: a second pass finds nothing.
		reOut, reCount := repairOrphanedToolResults(out)
		if reCount != 0 {
			t.Fatalf("repair not idempotent: second pass repaired %d", reCount)
		}
		if !reflect.DeepEqual(reOut, out) {
			t.Fatalf("repair not idempotent: second pass changed the history")
		}

		// No orphaned call: every assistant tool call has a matching later result.
		fc1AssertNoOrphanedCall(t, out)

		// Preservation: only synthetic tool-result turns are inserted.
		before := fc1CountByKind(history)
		after := fc1CountByKind(out)
		for kind, n := range before {
			if kind == schema.TurnToolResults {
				continue
			}
			if after[kind] != n {
				t.Fatalf("turn kind %s count changed %d -> %d (repair dropped/added a non-synthetic turn)", kind, n, after[kind])
			}
		}
		if after[schema.TurnToolResults] < before[schema.TurnToolResults] {
			t.Fatalf("tool-result turns dropped: %d -> %d", before[schema.TurnToolResults], after[schema.TurnToolResults])
		}
	})
}

// fc1BuildHistory decodes a byte stream into a history. Each byte is one op:
// the low two bits pick the turn kind and the rest is a parameter (call count or
// resolve count). Issued-but-unresolved call IDs are tracked so tool-result turns
// reference real calls; IDs are globally unique and non-empty.
func fc1BuildHistory(data []byte) []schema.Turn {
	var history []schema.Turn
	var issued []string // outstanding call IDs available to resolve
	counter := 0

	for _, b := range data {
		op := b & 0x3
		param := int(b >> 2)
		switch op {
		case 0:
			history = append(history, schema.Turn{
				Kind:    schema.TurnUserInput,
				Message: llm.User("user " + strconv.Itoa(counter)),
			})
			counter++
		case 1:
			// Assistant with param%4 tool calls.
			n := param % 4
			parts := make([]llm.ContentPart, 0, n+1)
			parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: "assistant " + strconv.Itoa(counter)})
			for j := 0; j < n; j++ {
				id := "c" + strconv.Itoa(counter)
				counter++
				call := llm.ToolCallData{ID: id, Name: "read_file", Type: "function", Arguments: []byte(`{}`)}
				parts = append(parts, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &call})
				issued = append(issued, id)
			}
			history = append(history, schema.Turn{
				Kind:    schema.TurnAssistant,
				Message: llm.Message{Role: llm.RoleAssistant, Content: parts},
			})
		case 2:
			// Tool-result turn resolving up to param%4 outstanding calls.
			n := param % 4
			for j := 0; j < n && len(issued) > 0; j++ {
				id := issued[0]
				issued = issued[1:]
				history = append(history, schema.Turn{
					Kind:    schema.TurnToolResults,
					Message: llm.ToolResultNamed(id, "read_file", "ok", false),
				})
			}
		default:
			history = append(history, schema.Turn{
				Kind:    schema.TurnSteering,
				Message: llm.User("steer " + strconv.Itoa(counter)),
			})
			counter++
		}
	}
	return history
}

func fc1AssertNoOrphanedCall(t *testing.T, history []schema.Turn) {
	t.Helper()
	for i, turn := range history {
		if turn.Kind != schema.TurnAssistant {
			continue
		}
		for _, p := range turn.Message.Content {
			if p.Kind != llm.ContentToolCall || p.ToolCall == nil || p.ToolCall.ID == "" {
				continue
			}
			if !fc1HasLaterResult(history[i+1:], p.ToolCall.ID) {
				t.Fatalf("orphaned tool call %q at turn %d has no later result", p.ToolCall.ID, i)
			}
		}
	}
}

func fc1HasLaterResult(rest []schema.Turn, callID string) bool {
	for _, turn := range rest {
		if turn.Kind != schema.TurnTool && turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, p := range turn.Message.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == callID {
				return true
			}
		}
	}
	return false
}

func fc1ZeroTimestamps(history []schema.Turn) []schema.Turn {
	out := make([]schema.Turn, len(history))
	copy(out, history)
	for i := range out {
		out[i].Timestamp = time.Time{}
	}
	return out
}

func fc1CountByKind(history []schema.Turn) map[schema.TurnKind]int {
	m := map[schema.TurnKind]int{}
	for _, t := range history {
		m[t.Kind]++
	}
	return m
}
