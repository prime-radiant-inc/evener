package agent

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/fuzz/fault"
	"primeradiant.com/evener/llm"
)

func TestTranscriptPublicationRetainedUnsyncedRecord(t *testing.T) {
	t.Parallel()
	plan := bytes.Repeat([]byte{1}, 128)
	plan[5] = 0 // The first entry's Sync, after its successful Write.
	w, err := transcript.NewWriterWithFS(fault.FS(afero.NewMemMapFs(), fault.FromBytes(plan)), "/session/transcript.jsonl", transcript.Header{SessionID: "retained-owner"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	s := &Session{id: "retained-owner"}
	s.attachTranscript(w, nil)
	var boundary int64
	var state *appwire.ConversationProvenanceState
	s.SetTranscriptPublishedFunc(func(length int64, next *appwire.ConversationProvenanceState) { boundary, state = length, next })
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("retained-input-231"))
	turn.ConversationInputIDs = []string{"input-231"}
	s.attentionMu.Lock()
	rec, err := s.recordTranscriptLocked(turn, transcript.DoorBuffered, transcript.PlaceSession)
	s.attentionMu.Unlock()
	warnings := w.DrainWarnings()
	if len(warnings) != 1 || !errors.Is(warnings[0], fault.ErrInjected) {
		t.Fatalf("fixture did not trigger retained fsync failure: %v", warnings)
	}
	if err != nil || !rec.Recorded || boundary != rec.Offset+rec.Length || state == nil || !slices.Contains(state.AdmittedInputs, "input-231") {
		t.Fatalf("retained record was not paired with adoption: rec=%+v state=%+v err=%v", rec, state, err)
	}
}

func TestTranscriptPublicationSnapshotReuseAndCopies(t *testing.T) {
	t.Parallel()
	s, _ := newExecutionSession(t)
	var state *appwire.ConversationProvenanceState
	var boundary int64
	s.SetTranscriptPublishedFunc(func(length int64, next *appwire.ConversationProvenanceState) { boundary, state = length, next })
	seeded := state
	if state == nil || boundary != s.TranscriptRecordedLength() {
		t.Fatal("handoff was not seeded")
	}
	if err := s.writeTranscript(schema.NewTurn(schema.TurnHookCompleted, llm.User("publication-hook-191"))); err != nil {
		t.Fatal(err)
	}
	if state != seeded {
		t.Fatal("non-provenance record allocated a new graph")
	}
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("publication-input-192"))
	turn.ConversationInputIDs = []string{"input-192"}
	if err := s.writeTranscript(turn); err != nil {
		t.Fatal(err)
	}
	admitted := state
	if state == seeded || boundary != s.TranscriptRecordedLength() {
		t.Fatal("input adoption did not publish a new paired snapshot")
	}
	oldBoundary := boundary
	copyTurn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("fold-copy-193"))
	ordinal := uint64(0)
	copyTurn.OriginalOrdinal = &ordinal
	copyTurn.ConversationProvenance = &schema.ConversationOutputProvenance{Version: 1, InstanceID: s.ID(), FrontierID: "copy-frontier", RequestID: "copy-request"}
	copyTurn.ConversationFrontier = &schema.ConversationFrontier{ID: "copy-frontier", AddedInputs: []string{"input-192"}}
	if err := s.writeTranscript(copyTurn); err != nil {
		t.Fatal(err)
	}
	if state != admitted || boundary <= oldBoundary {
		t.Fatal("fold copy changed provenance or failed to publish its bytes")
	}
	if len(seeded.AdmittedInputs) != 0 || len(seeded.Frontiers) != 0 {
		t.Fatal("publication mutated an earlier snapshot")
	}
	// A failed append cannot complete another boundary or invent its frontier.
	if err := s.attachedTranscript().Close(); err != nil {
		t.Fatal(err)
	}
	oldBoundary = boundary
	s.attentionMu.Lock()
	rec, err := s.recordTranscriptLocked(turn, transcript.DoorSynced, transcript.PlaceSession)
	s.attentionMu.Unlock()
	if !errors.Is(err, transcript.ErrWriterClosed) || rec.Recorded || boundary != oldBoundary || state != admitted {
		t.Fatalf("failed record published: rec=%+v err=%v", rec, err)
	}
}
