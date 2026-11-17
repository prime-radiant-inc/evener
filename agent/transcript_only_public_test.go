package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func transcriptDataOf(t *testing.T, entries []transcript.Entry) transcriptData {
	t.Helper()
	data := transcriptData{Entries: entries}
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		data.EntryLines = append(data.EntryLines, line)
	}
	return data
}

// The model-facing transcript (read_session_transcript, find, raw ranges)
// shows exactly what it shows without transcript-only entries, renumbered the
// same way.
func TestPublicTranscriptOmitsTranscriptOnlyEntries(t *testing.T) {
	plainTurns := transcriptOnlyFixtureTurns()
	plain := entriesOf(plainTurns)
	interleaved := entriesOf(schematest.InterleaveTranscriptOnly(plainTurns))
	if got, want := publicTranscriptEntries(interleaved), publicTranscriptEntries(plain); !reflect.DeepEqual(got, want) {
		t.Fatalf("public entries differ:\n got %d\nwant %d", len(got), len(want))
	}
	got := publicTranscriptData(transcriptDataOf(t, interleaved))
	want := publicTranscriptData(transcriptDataOf(t, plain))
	if !reflect.DeepEqual(got.Entries, want.Entries) || !reflect.DeepEqual(got.EntryLines, want.EntryLines) {
		t.Fatal("public transcript data differs")
	}
	sampleLine := transcriptDataOf(t, interleaved[:1]).EntryLines[0]
	if _, include, err := publicTranscriptLine(sampleLine, 0); err != nil || include {
		t.Fatalf("publicTranscriptLine of a transcript-only entry: include=%v err=%v", include, err)
	}
	if _, err := transcriptExpansionJSONL(transcriptDataOf(t, interleaved), 0); err == nil {
		t.Fatal("a transcript-only entry was expandable as a public turn")
	}
}

// A compaction fold re-appends, after its markers, an entry it already wrote
// before them (schema.Turn.OriginalOrdinal). The model never sees the pair
// twice: ResumeHistory anchors on the last marker and keeps only the copy. The
// public transcript is a view of the same conversation, so it must show the
// block once too, the way the ATIF projection and the doctor's transcript read
// already do.
func TestPublicTranscriptOmitsFoldCopies(t *testing.T) {
	t.Parallel()
	const text = "one message, folded once"
	original := schema.NewTurn(schema.TurnUserInput, llm.User(text))
	ordinal := uint64(0)
	copied := schema.NewTurn(schema.TurnUserInput, llm.User(text))
	copied.OriginalOrdinal = &ordinal
	entries := []transcript.Entry{makeEntry(original), makeEntry(copied)}

	if got := publicTranscriptEntries(entries); len(got) != 1 {
		t.Fatalf("public transcript shows %d entries, want exactly 1: a fold copy repeats the entry its original_ordinal names", len(got))
	}
	if got := publicTranscriptData(transcriptDataOf(t, entries)); len(got.Entries) != 1 {
		t.Fatalf("public transcript data holds %d entries, want exactly 1", len(got.Entries))
	}

	// The rendered document and the expand_turn addresses must agree. Addresses
	// count public entries, so a dropped one renumbers what follows it — the
	// behavior an already-dropped kind like TURN_COMPLETION has always had.
	// What matters here is that the surviving block renders once and stays
	// expandable at its own address.
	root := newBucket(t)
	id := "02wMz5Txv1C3Hut0M8GCeB"
	writeFindTranscript(t, root, id, time.Time{}, original, copied)
	value, err := readMarkdownPage(context.Background(), filepath.Clean(transcriptPath(root, id)), root, "", schema.SessionMeta{}, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if got := strings.Count(value.(readMarkdownEnvelope).Content, text); got != 1 {
		t.Fatalf("rendered transcript shows the block %d times, want exactly 1", got)
	}
	pin := 0
	page, err := readMarkdownPage(context.Background(), filepath.Clean(transcriptPath(root, id)), root, "", schema.SessionMeta{}, "", &pin, 0, 0)
	if err != nil {
		t.Fatalf("expand the surviving block: %v", err)
	}
	if got := strings.Count(page.(readMarkdownEnvelope).Content, text); got != 1 {
		t.Fatalf("expanded block shows the text %d times, want exactly 1", got)
	}
}
