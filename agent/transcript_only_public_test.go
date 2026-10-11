package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
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
