package agent

import (
	"testing"

	"primeradiant.com/evener/agent/transcript"
)

// TestRestoredTranscriptOpenFlagSurvivesEmptyRefresh pins the property the
// explicit opened flag exists for, at the seam where it is decided.
//
// A restore-time refresh re-reads the transcript and can produce an empty
// or nil entry list (a header-only file re-reads as nil); ok must not flip
// to false when that happens.
func TestRestoredTranscriptOpenFlagSurvivesEmptyRefresh(t *testing.T) {
	for name, refreshEntries := range map[string][]transcript.Entry{
		"nil entries":   nil,
		"empty entries": {},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Session{}
			s.setRestoredTranscript(transcript.Header{SessionID: "sid"}, refreshEntries, true)
			_, entries, ok := s.RestoredTranscript()
			if !ok {
				t.Fatal("ok flipped to false when the refresh produced an empty entry list; ok must mean 'a transcript was opened'")
			}
			if len(entries) != 0 {
				t.Fatalf("entries = %d, want 0", len(entries))
			}
		})
	}
}
