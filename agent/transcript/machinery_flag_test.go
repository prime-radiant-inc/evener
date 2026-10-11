package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const machineryBlock = "<system-notification>stored at /state/attachments/shot.png</system-notification>"

func entryTurn(part llm.ContentPart) schema.Turn {
	return schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{part}})
}

func marshalEntryLine(t *testing.T, entry Entry) []byte {
	t.Helper()
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	return raw
}

// TestAppendWritesMachineryFlaggedMarker: every entry a current build writes
// carries machinery_flagged, so readers treat its parts' Machinery flags as
// authoritative instead of inferring machinery from text shape.
func TestAppendWritesMachineryFlaggedMarker(t *testing.T) {
	fs := afero.NewMemMapFs()
	w, err := NewWriterWithFS(fs, "/machinery.jsonl", Header{SessionID: "sess-mach"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	raw, err := afero.ReadFile(fs, "/machinery.jsonl")
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines=%d, want header and one entry", len(lines))
	}
	if strings.Contains(lines[0], "machinery_flagged") {
		t.Fatalf("header carries machinery_flagged: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"machinery_flagged":true`) {
		t.Fatalf("entry line lacks machinery_flagged:true: %s", lines[1])
	}
}

// TestAppendFlaggedMachineryPartRoundTrips: a producer-flagged part keeps its
// flag through the persisted bytes and the strict decode.
func TestAppendFlaggedMachineryPartRoundTrips(t *testing.T) {
	fs := afero.NewMemMapFs()
	w, err := NewWriterWithFS(fs, "/machinery.jsonl", Header{SessionID: "sess-mach"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Append(entryTurn(llm.MachineryText(machineryBlock))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	raw, err := afero.ReadFile(fs, "/machinery.jsonl")
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines=%d, want header and one entry", len(lines))
	}
	if !strings.Contains(lines[1], `"machinery":true`) {
		t.Fatalf("entry line lost the part's machinery flag: %s", lines[1])
	}
	entry, err := DecodeEntry([]byte(lines[1]))
	if err != nil {
		t.Fatalf("DecodeEntry: %v", err)
	}
	if part := entry.Turn.Message.Content[0]; !part.Machinery {
		t.Fatalf("flagged part lost its machinery flag in decode: %+v", part)
	}
}

// TestDecodeEntryInfersMachineryForUnmarkedEntries: entries written before
// the flag existed carry no marker, so decode must infer machinery from the
// exact-block shape — otherwise those transcripts' machinery notes would
// surface in reloaded bubbles under flag-only filtering.
func TestDecodeEntryInfersMachineryForUnmarkedEntries(t *testing.T) {
	line := marshalEntryLine(t, Entry{Kind: "entry", Seq: 3, Turn: entryTurn(llm.ContentPart{Kind: llm.ContentText, Text: machineryBlock})})
	if strings.Contains(string(line), "machinery_flagged") {
		t.Fatalf("fixture unexpectedly carries the marker: %s", line)
	}
	entry, err := DecodeEntry(line)
	if err != nil {
		t.Fatalf("DecodeEntry: %v", err)
	}
	if part := entry.Turn.Message.Content[0]; !part.Machinery {
		t.Fatalf("unmarked entry's block-shaped part = %+v, want the pre-flag inference to flag it", part)
	}
}

// TestDecodeEntryHonorsMachineryFlaggedMarker: in a marked entry the flags
// are authoritative. An unflagged block-shaped part is a user pasting the
// block verbatim and must stay unflagged — this is exactly the paste the
// shape-based filter used to eat.
func TestDecodeEntryHonorsMachineryFlaggedMarker(t *testing.T) {
	marked := Entry{Kind: "entry", Seq: 4, MachineryFlagged: true, Turn: entryTurn(llm.ContentPart{Kind: llm.ContentText, Text: machineryBlock})}
	line := marshalEntryLine(t, marked)
	if !strings.Contains(string(line), `"machinery_flagged":true`) {
		t.Fatalf("fixture lacks the marker: %s", line)
	}
	entry, err := DecodeEntry(line)
	if err != nil {
		t.Fatalf("DecodeEntry rejected a flagged entry: %v", err)
	}
	if !entry.MachineryFlagged {
		t.Fatal("marker lost in decode")
	}
	if part := entry.Turn.Message.Content[0]; part.Machinery {
		t.Fatalf("marked entry's block-shaped part = %+v, want a verbatim paste to stay unflagged", part)
	}
}
