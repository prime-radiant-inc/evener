package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// A compaction fold copy re-appends an entry the file already holds; it projects
// nothing, so counting it doubles the session's figures and disagrees with the
// failure glyphs the same rule promises to match. The derived scans must count
// only entries that are not fold copies.
func TestDerivedTotalsExcludeFoldCopies(t *testing.T) {
	answer := assistantTextEntry(2, "answer")
	answer.Turn.Usage = llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}
	call := assistantToolCallEntry(4, "c1", "shell", `{}`)
	fail := toolResultEntry(5, "c1", "shell", "boom")
	fail.Turn.Message.Content[0].ToolResult.IsError = true

	copyOfAnswer, copyOfFail := answer, fail
	answerOrdinal, failOrdinal := uint64(1), uint64(4)
	copyOfAnswer.Turn.OriginalOrdinal = &answerOrdinal
	copyOfFail.Turn.OriginalOrdinal = &failOrdinal

	path := writeEntries(t, userEntry(1, "q"), answer, call, fail, copyOfAnswer, copyOfFail)

	usage, failed, err := NewTurnCache().DerivedTotalsFromFile(path, testMaxLineBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.InputTokens != 2 || usage.OutputTokens != 2 || usage.TotalTokens != 4 {
		t.Fatalf("derived usage = %+v, want the single {2 2 4} with the fold copy excluded", usage)
	}
	if failed != 1 {
		t.Fatalf("derived failures = %d, want 1 with the fold copy of the failure excluded", failed)
	}

	onlyUsage, err := NewTurnCache().UsageTotalFromFile(path, testMaxLineBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	if onlyUsage == nil || onlyUsage.InputTokens != 2 || onlyUsage.TotalTokens != 4 {
		t.Fatalf("usage total = %+v, want {2 2 4} with the fold copy excluded", onlyUsage)
	}

	onlyFailures, err := NewTurnCache().FailedToolCallsFromFile(path, testMaxLineBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	if onlyFailures != 1 {
		t.Fatalf("failure total = %d, want 1 with the fold copy excluded", onlyFailures)
	}

	live := transcript.NewFailureCounter(0)
	for _, entry := range []transcript.Entry{userEntry(1, "q"), answer, call, fail, copyOfAnswer, copyOfFail} {
		live.Observe(entry.Turn)
	}
	if got := live.Count(); got != 1 {
		t.Fatalf("live failure counter = %d, want 1 with the fold copy excluded", got)
	}
}

func identityEntry(seq int, turn schema.Turn) transcript.Entry {
	turn.Format = schema.TurnFormatIdentity
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: turn}
}

// The full read's per-turn usage must also exclude a fold copy: the copy's
// original already contributes its usage to the turn, so summing both doubles
// the turn's (and the session's projected) figure.
func TestItemTurnsExcludeFoldCopies(t *testing.T) {
	open := schema.Turn{Kind: schema.TurnUserInput, TurnID: "t1", TurnKind: schema.TurnSpanExecution, Message: llm.User("q")}
	answer := schema.Turn{Kind: schema.TurnAssistant, TurnID: "t1", Message: llm.Assistant("answer"), Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}
	copied := answer
	ordinal := uint64(1)
	copied.OriginalOrdinal = &ordinal

	entries := []transcript.Entry{identityEntry(1, open), identityEntry(2, answer), identityEntry(3, copied)}
	path := writeEntries(t, entries...)
	turns := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	var found bool
	for _, turn := range turns {
		if turn.Usage == nil {
			continue
		}
		found = true
		if turn.Usage.InputTokens != 2 || turn.Usage.OutputTokens != 2 || turn.Usage.TotalTokens != 4 {
			t.Fatalf("turn usage = %+v, want the single {2 2 4} with the fold copy excluded", turn.Usage)
		}
	}
	if !found {
		t.Fatal("no turn carried usage; the fixture did not exercise the projection")
	}
}
