package doctor

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// An archive of a phase 2 transcript holds transcript-only records, one inside
// a tool round here. They were never conversation, so the reconstruction is
// the one the archive without them gives.
func TestReconstructSkipsTranscriptOnlyRecords(t *testing.T) {
	source := reconstructionSourceFixture(t)
	_, want, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
	if err != nil {
		t.Fatal(err)
	}
	var withExtra []archivedMessage
	for _, m := range source.Messages {
		m.Ordinal = len(withExtra)
		withExtra = append(withExtra, m)
		if m.Kind == string(schema.TurnAssistant) {
			for _, kind := range []schema.TurnKind{schema.TurnCommunicate, schema.TurnNotice} {
				withExtra = append(withExtra, archivedMessage{ID: 100 + len(withExtra), Ordinal: len(withExtra), Timestamp: m.Timestamp, SourceType: "entry", Kind: string(kind)})
			}
		}
	}
	withExtra = append(withExtra, archivedMessage{ID: 200, Ordinal: len(withExtra), Timestamp: source.Messages[len(source.Messages)-1].Timestamp, SourceType: "entry", Kind: string(schema.TurnCompletion)})
	source.Messages = withExtra
	_, got, err := reconstructEntries(source, schema.SessionMeta{}, nil, &reconstructionReport{})
	if err != nil {
		t.Fatalf("reconstruct with transcript-only records: %v", err)
	}
	// The closing reconstruction notice is stamped when reconstruction runs.
	got[len(got)-1].Turn.Timestamp, want[len(want)-1].Turn.Timestamp = time.Time{}, time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries differ:\n got %+v\nwant %+v", got, want)
	}
}
