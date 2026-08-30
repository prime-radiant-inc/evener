package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// writeSinglePassFixture writes a transcript with header prelude content
// (system prompt), user input, an assistant tool call, a tool result, and a
// usage/timestamp-carrying assistant turn: every shape TurnsFromFile stamps.
func writeSinglePassFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID:    "th_single",
		SystemPrompt: "You are Evener.",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	ts := time.Unix(1_700_000_000, 0).UTC()
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("run"))); err != nil {
		t.Fatalf("append user: %v", err)
	}
	call := llm.ToolCallData{ID: "call_read", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}
	if err := w.Append(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}); err != nil {
		t.Fatalf("append call: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_read", "read_file", "out", false))); err != nil {
		t.Fatalf("append result: %v", err)
	}
	usageTurn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("done"))
	usageTurn.Usage = llm.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}
	usageTurn.Timestamp = ts
	if err := w.Append(usageTurn); err != nil {
		t.Fatalf("append usage: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// singlePassProjector is the projector shape the server installs: it emits
// one item per turn so len(turns) tracks the entry count, and reads the turn
// it was handed (which is the point of the EntryProjector contract).
func singlePassProjector(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
	return []appwire.ThreadItem{{Type: "agentMessage", ID: turnID, TurnID: turnID, Text: string(turn.Kind)}}
}

// TestTurnsFromFileSinglePassMatchesEntriesForm is the differential proof for
// the single-pass change: TurnsFromFile over the file must produce exactly
// the turns TurnsFromEntries produces over the same transcript's decoded
// header+entries, including the prelude turn, the turn ids (1-based entry
// indexing), and the usage/timestamp stamps.
func TestTurnsFromFileSinglePassMatchesEntriesForm(t *testing.T) {
	path := writeSinglePassFixture(t)

	fileTurns, err := TurnsFromFile(path, 1<<20, singlePassProjector)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	w, entries, err := transcript.OpenWriterForSession(path, "th_single")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	defer w.Close() //nolint:errcheck // read-only fixture use
	entryTurns, err := TurnsFromEntries(w.Header(), entries, singlePassProjector)
	if err != nil {
		t.Fatalf("TurnsFromEntries: %v", err)
	}

	if len(fileTurns) == 0 {
		t.Fatal("fixture projected to zero turns; differential proof is vacuous")
	}
	if !reflect.DeepEqual(fileTurns, entryTurns) {
		t.Fatalf("turns diverge:\nfile:    %#v\nentries: %#v", fileTurns, entryTurns)
	}
	// The prelude turn must survive the single-pass read in both forms.
	sawPrelude := false
	for _, turn := range fileTurns {
		if turn.ID == appwire.SystemPreludeTurnID {
			sawPrelude = true
		}
	}
	if !sawPrelude {
		t.Fatalf("single-pass read dropped the prelude turn; turn ids: %v", singlePassTurnIDs(fileTurns))
	}
}

// TestTurnsFromFileSinglePassStillStampsUsageAndTimestamp pins the stamps the
// per-entry projection applies, proving the shared projector kept them.
func TestTurnsFromFileSinglePassStillStampsUsageAndTimestamp(t *testing.T) {
	path := writeSinglePassFixture(t)
	turns, err := TurnsFromFile(path, 1<<20, singlePassProjector)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	var stamped *appwire.Turn
	for i := range turns {
		if turns[i].Usage != nil {
			stamped = &turns[i]
		}
	}
	if stamped == nil {
		t.Fatalf("no turn carried usage; ids=%v", singlePassTurnIDs(turns))
	}
	if stamped.Usage.TotalTokens != 7 {
		t.Fatalf("usage total tokens=%d, want 7", stamped.Usage.TotalTokens)
	}
	if stamped.StartedAt == nil || *stamped.StartedAt != time.Unix(1_700_000_000, 0).UTC().UnixMilli() {
		t.Fatalf("usage turn StartedAt=%v, want the entry timestamp", stamped.StartedAt)
	}
}

// TestTurnsFromFileSinglePassSkipsOversizeLine keeps the reader's line-limit
// contract after the single-pass change (the limit previously gated the
// separate ScanPrelude pass as well).
func TestTurnsFromFileSinglePassSkipsOversizeLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oversize.transcript.jsonl")
	header, err := json.Marshal(transcript.Header{Kind: "header", SessionID: "th_over"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := json.Marshal(schema.NewTurn(schema.TurnUserInput, llm.User("hi")))
	if err != nil {
		t.Fatal(err)
	}
	big := append([]byte(nil), entry...)
	for len(big) < 64 {
		big = append(big, 'x')
	}
	if err := os.WriteFile(path, []byte(string(header)+"\n"+string(big)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TurnsFromFile(path, 32, singlePassProjector); err == nil {
		t.Fatal("oversize line was accepted")
	}
}

func singlePassTurnIDs(turns []appwire.Turn) []string {
	ids := make([]string, 0, len(turns))
	for _, turn := range turns {
		ids = append(ids, turn.ID)
	}
	return ids
}
