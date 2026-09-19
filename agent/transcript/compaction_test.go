package transcript

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestCompactionManifestConcreteLocators(t *testing.T) {
	source := Entry{Kind: "entry", Seq: 7, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("retained input"))}
	added := schema.NewTurn(schema.TurnSteering, llm.User("hook context"))
	first := Entry{Kind: "entry", Seq: 12, Turn: schema.NewTurn(schema.TurnSummary, llm.User("older summary"))}
	first.Turn.Compaction = &schema.CompactionManifest{Version: 1, SessionID: "owner", History: []schema.CompactionHistoryItem{{Source: &schema.CompactionLocator{EntrySeq: 7}}, {Added: &added}}}
	index := 1
	second := Entry{Kind: "entry", Seq: 20, Turn: schema.NewTurn(schema.TurnSummary, llm.User("new summary"))}
	second.Turn.Compaction = &schema.CompactionManifest{Version: 1, SessionID: "owner", History: []schema.CompactionHistoryItem{{Source: &schema.CompactionLocator{EntrySeq: 7}}, {Source: &schema.CompactionLocator{EntrySeq: 12, AddedIndex: &index}}}}
	entries := []Entry{source, first, second}
	if err := ValidateCompactionManifests("owner", entries); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*schema.CompactionManifest)
	}{
		{"wrong owner", func(m *schema.CompactionManifest) { m.SessionID = "other" }},
		{"unsupported version", func(m *schema.CompactionManifest) { m.Version = 2 }},
		{"forward", func(m *schema.CompactionManifest) { m.History[0].Source = &schema.CompactionLocator{EntrySeq: 21} }},
		{"missing", func(m *schema.CompactionManifest) { m.History[0].Source = &schema.CompactionLocator{EntrySeq: 8} }},
		{"reference chain", func(m *schema.CompactionManifest) {
			i := 0
			m.History[1].Source = &schema.CompactionLocator{EntrySeq: 12, AddedIndex: &i}
		}},
		{"duplicate", func(m *schema.CompactionManifest) { m.History[1] = m.History[0] }},
		{"two arms", func(m *schema.CompactionManifest) { m.History[0].Added = &added }},
		{"empty arm", func(m *schema.CompactionManifest) { m.History[0] = schema.CompactionHistoryItem{} }},
		{"tool payload", func(m *schema.CompactionManifest) {
			bad := added
			bad.Message = llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Content: "private"}}}}
			m.History[1] = schema.CompactionHistoryItem{Added: &bad}
		}},
		{"accepted identity", func(m *schema.CompactionManifest) {
			bad := added
			bad.StableTurnID = "accepted"
			m.History[1] = schema.CompactionHistoryItem{Added: &bad}
		}},
		{"nested", func(m *schema.CompactionManifest) {
			bad := added
			bad.Compaction = first.Turn.Compaction
			m.History[1] = schema.CompactionHistoryItem{Added: &bad}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := second
			manifest := *second.Turn.Compaction
			manifest.History = append([]schema.CompactionHistoryItem(nil), manifest.History...)
			bad.Turn.Compaction = &manifest
			tc.change(&manifest)
			if err := ValidateCompactionManifests("owner", []Entry{source, first, bad}); err == nil {
				t.Fatal("invalid committed projection accepted")
			}
		})
	}
}

func TestIndexedOrdinaryAppendReportsActualRecord(t *testing.T) {
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("record"))
	var absent *Writer
	if _, recorded, err := absent.AppendEntry(turn); err != nil || recorded {
		t.Fatalf("absent writer recorded=%v err=%v", recorded, err)
	}
	w, err := NewWriter(filepath.Join(t.TempDir(), "transcript.jsonl"), Header{SessionID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	seq, recorded, err := w.AppendEntry(turn)
	if err != nil || !recorded || seq != 0 {
		t.Fatalf("actual entry seq=%d recorded=%v err=%v", seq, recorded, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, recorded, err := w.AppendDurableEntry(turn); err != nil || recorded {
		t.Fatalf("closed writer recorded=%v err=%v", recorded, err)
	}
}
