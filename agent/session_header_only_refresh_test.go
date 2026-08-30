package agent

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// TestRestoredTranscriptOpenFlagSurvivesEmptyRefresh pins the property the
// explicit opened flag exists for, at the seam where it is decided.
//
// Before the flag, ok was inferred from entries != nil — and the restore
// path's refreshFromDisk re-reads the transcript after delegate-delivery
// replay via readTranscriptFull, whose entry list is append-built and
// therefore NIL for a header-only file. A header-only session that replayed
// a delivery would silently flip ok back to false, sending serve to the
// file form after all. The flag is captured at the open (openErr == nil)
// and survives every refresh.
//
// The two subtests exercise both refresh shapes against a session whose
// restore opened a header-only transcript: setRestoredTranscript receives
// whatever the refresh produced (nil or empty), and ok must hold.
func TestRestoredTranscriptOpenFlagSurvivesEmptyRefresh(t *testing.T) {
	for name, refreshEntries := range map[string][]transcript.Entry{
		"refresh over header-only file (nil entries from append-built read)": nil,
		"refresh over header-only file (empty non-nil entries)":              {},
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

// TestRestoreSession_HeaderOnlyOpenCapturesOKFlag pins the capture itself:
// restoring over a header-only transcript sets the opened flag even though
// the entry list is empty, so the very first RestoredTranscript call after
// restore reports ok=true.
func TestRestoreSession_HeaderOnlyOpenCapturesOKFlag(t *testing.T) {
	stateDir := t.TempDir()
	rootID := identifier.MustNewSessionID()
	meta := schema.SessionMeta{ID: rootID, ProfileID: "openai", Model: "gpt-5.2"}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	writer, err := transcript.NewWriter(transcriptPath(stateDir, rootID), transcript.Header{SessionID: rootID, ProfileID: "openai", Model: "gpt-5.2"})
	if err != nil {
		t.Fatalf("create header-only transcript: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		llm.NewClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(stateDir),
		meta, RestoreSessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer restored.Close()
	if _, _, ok := restored.RestoredTranscript(); !ok {
		t.Fatal("restore over a header-only transcript did not capture the opened flag")
	}
}
