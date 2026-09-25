package agent

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// transcriptOnlyFixtureTurns is a session's history in file order: a finished
// tool round, a round whose call never got a result (orphan repair inserts
// one), and a later turn.
func transcriptOnlyFixtureTurns() []schema.Turn {
	call := func(id string) llm.ContentPart {
		return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: id, Name: "read_file", Arguments: json.RawMessage(`{}`)}}
	}
	result := func(id string) llm.ContentPart {
		return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: id, Name: "read_file", Content: "ok"}}
	}
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{call("c1")}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{result("c1")}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{call("c2")}}),
		schema.NewTurn(schema.TurnUserInput, llm.User("second")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("done")),
	}
}

func withCompaction(turns []schema.Turn) []schema.Turn {
	out := append([]schema.Turn(nil), turns[:2]...)
	out = append(out, schema.NewTurn(schema.TurnSummary, llm.User("summary so far")))
	return append(out, turns[2:]...)
}

// entriesOf wraps turns in transcript entries at consecutive sequence numbers.
func entriesOf(turns []schema.Turn) []transcript.Entry {
	entries := make([]transcript.Entry, len(turns))
	for i, turn := range turns {
		entries[i] = transcript.Entry{Kind: "entry", Seq: i, Turn: turn}
	}
	return entries
}

// Resume must restore exactly the history it would restore without the
// transcript-only entries, and map a fork boundary to the same place.
func TestResumeSkipsTranscriptOnlyEntries(t *testing.T) {
	for name, plainTurns := range map[string][]schema.Turn{
		"whole transcript": transcriptOnlyFixtureTurns(),
		"after compaction": withCompaction(transcriptOnlyFixtureTurns()),
	} {
		t.Run(name, func(t *testing.T) {
			plain := entriesOf(plainTurns)
			interleaved := entriesOf(schematest.InterleaveTranscriptOnly(plainTurns))
			plainHistory, plainInsertions := resumeHistoryIndexed(plain)
			history, insertions := resumeHistoryIndexed(interleaved)
			// A repair synthetic is stamped with the time repair ran.
			for _, at := range plainInsertions {
				plainHistory[at].Timestamp = time.Time{}
			}
			for _, at := range insertions {
				history[at].Timestamp = time.Time{}
			}
			if !reflect.DeepEqual(history, plainHistory) || !reflect.DeepEqual(insertions, plainInsertions) {
				t.Fatalf("resumed history differs:\n got %d turns, insertions %v\nwant %d turns, insertions %v", len(history), insertions, len(plainHistory), plainInsertions)
			}
			if len(plainInsertions) == 0 {
				t.Fatal("the fixture no longer exercises orphan repair")
			}
			// Plain entry d sits at interleaved index 2d+1, after a sample at
			// 2d; a boundary before plain entry d is either index there.
			for d := 0; d <= len(plain); d++ {
				want := resumedDivergence(plain, d, plainInsertions)
				for _, boundary := range []int{2 * d, 2*d + 1} {
					if got := resumedDivergence(interleaved, boundary, insertions); got != want {
						t.Fatalf("boundary %d (plain %d) maps to %d, want %d", boundary, d, got, want)
					}
				}
			}
		})
	}
}
