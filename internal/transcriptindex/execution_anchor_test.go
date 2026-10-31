package transcriptindex

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestLatestExecutionTurnIDUsesExecutionSpansNotLatestItems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []schema.Turn
		want    string
	}{
		{name: "empty"},
		{
			name: "gap and delivery do not replace completed execution",
			entries: []schema.Turn{
				{TurnID: "A", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("A")},
				{TurnID: "gap", TurnKind: schema.TurnSpanGap, Kind: schema.TurnModelSwitch},
				{TurnID: "delivery", TurnKind: schema.TurnSpanDelivery, Kind: schema.TurnUserInput, Message: llm.User("delivered")},
			},
			want: "A",
		},
		{
			name: "later unfinished execution replaces older execution",
			entries: []schema.Turn{
				{TurnID: "A", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("A")},
				{TurnID: "gap", TurnKind: schema.TurnSpanGap, Kind: schema.TurnModelSwitch},
				{TurnID: "B", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("B")},
			},
			want: "B",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			dir := filepath.Join(t.TempDir(), "index")
			writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "anchor-test"})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range tc.entries {
				entry.Format = schema.TurnFormatIdentity
				if err := writer.Append(entry); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			index, err := Open(path, dir)
			if err != nil {
				t.Fatal(err)
			}
			window, err := index.Latest(1)
			if err != nil {
				t.Fatal(err)
			}
			if window.LatestExecutionTurnID != tc.want {
				t.Fatalf("LatestExecutionTurnID=%q, want %q", window.LatestExecutionTurnID, tc.want)
			}
			if err := index.Close(); err != nil {
				t.Fatal(err)
			}
			index, err = Open(path, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer index.Close()
			reopened, err := index.Latest(1)
			if err != nil {
				t.Fatal(err)
			}
			if reopened.LatestExecutionTurnID != tc.want {
				t.Fatalf("reopened LatestExecutionTurnID=%q, want %q", reopened.LatestExecutionTurnID, tc.want)
			}
		})
	}
}

func TestLatestExecutionRecordReadsOneSummaryRegardlessOfTrailingHistory(t *testing.T) {
	execution := turnRecord{Kind: turnKindExecution, ID: strRef{Off: 1, Len: 1}}
	for _, turnCount := range []uint64{1, 2, 1_000_001, 1_000_000_001} {
		reads := 0
		got, found, err := latestExecutionRecord(1, turnCount, func(slot uint64) (turnRecord, error) {
			reads++
			if slot != 0 {
				t.Fatalf("read slot=%d, want execution slot 0", slot)
			}
			return execution, nil
		})
		if err != nil || !found || got.Kind != turnKindExecution {
			t.Fatalf("turnCount=%d record=(%+v,%v,%v), want execution", turnCount, got, found, err)
		}
		if reads != 1 {
			t.Fatalf("turnCount=%d reads=%d, want one regardless of trailing rows", turnCount, reads)
		}
	}
}

func TestLatestExecutionRecordValidatesLocatorAndKind(t *testing.T) {
	for _, tc := range []struct {
		name      string
		slot      uint64
		count     uint64
		record    turnRecord
		wantReads int
		wantFound bool
		wantErr   bool
	}{
		{name: "empty sentinel"},
		{name: "out of range", slot: 2, count: 1, wantReads: 0, wantErr: true},
		{name: "wrong kind", slot: 1, count: 1, record: turnRecord{Kind: turnKindGap, ID: strRef{Off: 1, Len: 1}}, wantReads: 1, wantErr: true},
		{name: "execution", slot: 1, count: 1, record: turnRecord{Kind: turnKindExecution, ID: strRef{Off: 1, Len: 1}}, wantReads: 1, wantFound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			_, found, err := latestExecutionRecord(tc.slot, tc.count, func(uint64) (turnRecord, error) {
				reads++
				return tc.record, nil
			})
			if reads != tc.wantReads || found != tc.wantFound || (err != nil) != tc.wantErr {
				t.Fatalf("reads=%d found=%v err=%v, want reads=%d found=%v", reads, found, err, tc.wantReads, tc.wantFound)
			}
		})
	}
}

func TestLatestExecutionLocatorTracksExtensionAndCrossHandleAdoption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	dir := filepath.Join(t.TempDir(), "index")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "anchor-extension"})
	if err != nil {
		t.Fatal(err)
	}
	appendIdentity := func(entry schema.Turn) {
		t.Helper()
		entry.Format = schema.TurnFormatIdentity
		if err := writer.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	appendIdentity(schema.Turn{TurnID: "A", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("A")})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	first, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	writer, _, err = transcript.OpenWriterForSession(path, "anchor-extension")
	if err != nil {
		t.Fatal(err)
	}
	appendIdentity(schema.Turn{TurnID: "gap", TurnKind: schema.TurnSpanGap, Kind: schema.TurnModelSwitch, ModelSwitch: &schema.ModelSwitchInfo{OldProvider: "openai", OldModel: "gpt-5.4", NewProvider: "openai", NewModel: "gpt-4.1-mini"}})
	appendIdentity(schema.Turn{TurnID: "B", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("B unfinished")})
	appendIdentity(schema.Turn{TurnID: "delivery", TurnKind: schema.TurnSpanDelivery, Kind: schema.TurnUserInput, Message: llm.User("delivered")})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.CatchUp(); err != nil {
		t.Fatal(err)
	}
	for name, index := range map[string]*Index{"extending": first, "adopting": second} {
		window, err := index.Latest(1)
		if err != nil {
			t.Fatalf("%s handle Latest: %v", name, err)
		}
		if window.LatestExecutionTurnID != "B" {
			t.Fatalf("%s handle latest execution=%q, want unfinished B", name, window.LatestExecutionTurnID)
		}
	}
}

func TestLatestExecutionLocatorCorruptionRebuildsFromTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	dir := t.TempDir()
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "anchor-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	entry := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "A", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("A")}
	if err := writer.Append(entry); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	rewriteMetaField(t, dir, "latest_execution_turn_slot", `2`)
	rebuilt := openIndex(t, path, dir)
	defer rebuilt.Close()
	if rebuilt.rebuilds != 1 {
		t.Fatalf("rebuilds=%d, want one rebuild after invalid locator adoption", rebuilt.rebuilds)
	}
	window, err := rebuilt.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	if window.LatestExecutionTurnID != "A" {
		t.Fatalf("rebuilt latest execution=%q, want A", window.LatestExecutionTurnID)
	}
}
