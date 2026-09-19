package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestSemanticReadersRejectDanglingCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(transcript.Header{Kind: "header", FormatVersion: 2, SessionID: "owner"}); err != nil {
		t.Fatal(err)
	}
	marker := schema.NewTurn(schema.TurnSummary, llm.User("summary"))
	marker.Compaction = &schema.CompactionManifest{Version: 1, SessionID: "owner", History: []schema.CompactionHistoryItem{{Source: &schema.CompactionLocator{EntrySeq: 7}}}}
	if err := enc.Encode(transcript.Entry{Kind: "entry", Seq: 12, Turn: marker}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readTranscript(path); err == nil {
		t.Error("ordinary semantic reader accepted a missing retained source")
	}
	if _, err := validateStrictChildTranscript(path, "owner", transcript.DefaultMaxLineBytes); err == nil {
		t.Error("payload-discarding semantic reader accepted a missing retained source")
	}
	if w, _, err := transcript.OpenWriterForSession(path, "owner"); err == nil {
		_ = w.Close()
		t.Error("resume writer accepted a missing retained source")
	}
}

func TestResumeRetainsCanonicalOccurrencesAcrossTwoFolds(t *testing.T) {
	input := schema.NewTurn(schema.TurnUserInput, llm.User("retained input"))
	input.StableTurnID = "accepted-once"
	hook := schema.NewTurn(schema.TurnSteering, llm.User("hook context"))
	first := schema.NewTurn(schema.TurnSummary, llm.User("first summary"))
	first.Compaction = &schema.CompactionManifest{Version: 1, SessionID: "owner", History: []schema.CompactionHistoryItem{{Source: &schema.CompactionLocator{EntrySeq: 5}}, {Added: &hook}}}
	addedIndex := 1
	second := schema.NewTurn(schema.TurnSummary, llm.User("second summary"))
	second.Compaction = &schema.CompactionManifest{Version: 1, SessionID: "owner", History: []schema.CompactionHistoryItem{{Source: &schema.CompactionLocator{EntrySeq: 5}}, {Source: &schema.CompactionLocator{EntrySeq: 10, AddedIndex: &addedIndex}}, {Source: &schema.CompactionLocator{EntrySeq: 10}}}}
	history := mustResumeHistory(t, []transcript.Entry{{Seq: 5, Turn: input}, {Seq: 10, Turn: first}, {Seq: 14, Turn: second}})
	if len(history) != 4 {
		t.Fatalf("retained %d turns, want final marker and three exact source occurrences", len(history))
	}
	if history[1].StableTurnID != "accepted-once" || history[2].Message.Text() != "hook context" || history[3].Message.Text() != "first summary" {
		t.Fatal("wrong direct canonical sources")
	}
}

func mustResumeHistory(t testing.TB, entries []transcript.Entry) []schema.Turn {
	t.Helper()
	turns, err := ResumeHistory(entries)
	if err != nil {
		t.Fatal(err)
	}
	return turns
}
