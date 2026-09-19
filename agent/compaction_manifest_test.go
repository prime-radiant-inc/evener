package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
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

func TestForkRebasesConcreteCompactionSourcesWithoutAdoptingReceipt(t *testing.T) {
	stateDir, parentID := buildParentSession(t)
	input := schema.NewTurn(schema.TurnUserInput, llm.User("inherited retained input"))
	added := schema.NewTurn(schema.TurnSteering, llm.User("inherited hook"))
	first := schema.NewTurn(schema.TurnSummary, llm.User("first"))
	first.Compaction = &schema.CompactionManifest{Version: 1, SessionID: parentID, History: []schema.CompactionHistoryItem{{Source: &schema.CompactionLocator{EntrySeq: 7}}, {Added: &added}}}
	second := schema.NewTurn(schema.TurnSummary, llm.User("second"))
	index := 1
	second.Compaction = &schema.CompactionManifest{Version: 1, SessionID: parentID, History: []schema.CompactionHistoryItem{{Source: &schema.CompactionLocator{EntrySeq: 7}}, {Source: &schema.CompactionLocator{EntrySeq: 12, AddedIndex: &index}}}}
	second.SkillState = &schema.SkillTurnState{Compaction: &schema.SkillCompactionReceipt{SessionID: parentID, Revision: 9, Operation: schema.SkillCompactionOperation{PublicationID: "parent-publication"}}}
	path := transcriptPath(stateDir, parentID)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(transcript.Header{Kind: "header", FormatVersion: 2, SessionID: parentID}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []transcript.Entry{{Kind: "entry", Seq: 7, Turn: input}, {Kind: "entry", Seq: 12, Turn: first}, {Kind: "entry", Seq: 20, Turn: second}, {Kind: "entry", Seq: 23, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("fork here"))}} {
		if err := enc.Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	child, err := ForkSession(stateDir, parentID, 4, "child input", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := readTranscriptFull(transcriptPath(stateDir, child))
	if err != nil {
		t.Fatal(err)
	}
	history := mustResumeHistory(t, data.Entries)
	if len(history) != 4 || history[1].Message.Text() != "inherited retained input" || history[2].Message.Text() != "inherited hook" {
		t.Fatal("fork lost retained sources")
	}
	receipt := data.Entries[2].Turn.SkillState.Compaction
	if receipt.SessionID != parentID || receipt.Operation.PublicationID != "parent-publication" {
		t.Fatal("fork adopted parent receipt ownership")
	}
	if source := data.Entries[2].Turn.Compaction.History[1].Source; source.EntrySeq != 1 || source.AddedIndex == nil || *source.AddedIndex != 1 {
		t.Fatalf("inline source was not rebased: %+v", source)
	}
}

func TestInheritedManifestRefusesBeforeOwnershipAcquisition(t *testing.T) {
	for _, owner := range []string{"other-parent", "expected-parent"} {
		t.Run(owner, func(t *testing.T) {
			input := schema.NewTurn(schema.TurnUserInput, llm.User("parent input"))
			marker := schema.NewTurn(schema.TurnSummary, llm.User("parent summary"))
			source := 7
			if owner == "expected-parent" {
				source = 8
			}
			marker.Compaction = &schema.CompactionManifest{Version: 1, SessionID: owner, History: []schema.CompactionHistoryItem{{Source: &schema.CompactionLocator{EntrySeq: source}}}}
			acquired := 0
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true, AcquireSessionOwnership: func(string) error { acquired++; return nil }, spawn: spawnConfig{parentSessionID: "expected-parent", inheritedContext: []transcript.Entry{{Seq: 7, Turn: input}, {Seq: 10, Turn: marker}}}, testOnly: testConfig{skipGitSnapshot: true}})
			if s != nil {
				s.Close()
			}
			if err == nil || acquired != 0 {
				t.Fatalf("malformed inherited history err=%v acquired=%d", err, acquired)
			}
		})
	}
}
