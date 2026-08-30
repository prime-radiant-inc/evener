package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// rearmFixtureSession restores a root session whose transcript carries one
// unresolved and one consumed delegate attention, and returns the restored
// session plus the durable pending id the fold must re-arm.
func rearmFixtureSession(t *testing.T) (*Session, string) {
	return rearmFixtureSessionWithTestOnly(t, testConfig{})
}

func rearmFixtureSessionWithTestOnly(t *testing.T, testOnly testConfig) (*Session, string) {
	t.Helper()
	stateDir := t.TempDir()
	rootID := identifier.MustNewSessionID()
	if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	const pendingID = "delegate:dlg_rearm/delivery/1"
	const consumedID = "delegate:dlg_rearm/delivery/2"
	writer, err := transcript.NewWriter(transcriptPath(stateDir, rootID), transcript.Header{SessionID: rootID, ProfileID: "openai", Model: "gpt-5.2"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	pending := schema.NewTurn(schema.TurnSteering, llm.User(`<delegate-notification delegate_id="dlg_rearm">stay pending</delegate-notification>`))
	pending.AttentionID = pendingID
	pending.StableTurnID = newQueueEntryID()
	if err := writer.AppendDurable(pending); err != nil {
		_ = writer.Close()
		t.Fatalf("append pending attention: %v", err)
	}
	consumed := schema.NewTurn(schema.TurnSteering, llm.User(`<delegate-notification delegate_id="dlg_rearm">already resolved</delegate-notification>`))
	consumed.AttentionID = consumedID
	consumed.StableTurnID = newQueueEntryID()
	if err := writer.AppendDurable(consumed); err != nil {
		_ = writer.Close()
		t.Fatalf("append consumed attention: %v", err)
	}
	resolution := schema.NewTurn(schema.TurnAttentionResolution, llm.User(""))
	resolution.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID:      consumedID,
		Disposition:      string(delegateAttentionConsumed),
		ResumeGeneration: 0,
	}
	if err := writer.AppendDurable(resolution); err != nil {
		_ = writer.Close()
		t.Fatalf("append resolution: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close root transcript: %v", err)
	}
	meta := schema.SessionMeta{ID: rootID, ProfileID: "openai", Model: "gpt-5.2"}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("save root metadata: %v", err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, RestoreSessionConfig{StateDir: stateDir, testOnly: testOnly})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	return restored, pendingID
}

// TestRootDelegateAttention_RestoreRearmFoldsTheRetainedEntries proves the
// restore-path rearm took the entries branch (not the file read): the seam
// captures the entry list restore passed to the fold, and that list must be
// the one RestoredTranscript exposes — the same final entry list serve will
// see.
func TestRootDelegateAttention_RestoreRearmFoldsTheRetainedEntries(t *testing.T) {
	var foldedEntries []transcript.Entry
	testOnly := testConfig{}
	testOnly.delegateAttentionFoldEntries = func(entries []transcript.Entry) (delegateAttentionFold, error) {
		foldedEntries = entries
		return foldDelegateAttention(entries)
	}
	restored, pendingID := rearmFixtureSessionWithTestOnly(t, testOnly)
	defer restored.Close()

	if foldedEntries == nil {
		t.Fatal("restore rearm never took the entries fold branch")
	}
	_, retained, ok := restored.RestoredTranscript()
	if !ok {
		t.Fatal("restored session retained no transcript entries")
	}
	if len(foldedEntries) != len(retained) {
		t.Fatalf("entries passed to the fold = %d, retained entries = %d; the rearm must fold the same final list serve sees", len(foldedEntries), len(retained))
	}
	for i := range foldedEntries {
		if foldedEntries[i].Turn.StableTurnID != retained[i].Turn.StableTurnID {
			t.Fatalf("folded entry %d stable turn = %q, want the retained %q", i, foldedEntries[i].Turn.StableTurnID, retained[i].Turn.StableTurnID)
		}
	}
	fold, err := foldDelegateAttention(foldedEntries)
	if err != nil {
		t.Fatalf("fold captured entries: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{pendingID}) {
		t.Fatalf("pending from the captured entries = %#v, want exactly the unresolved attention", got)
	}
}

// TestRestoreSession_RestoredTranscriptOKBoundary pins the ok flag's
// boundary: a session restored with NO transcript on disk reports ok=false,
// and one restored over a transcript with at least one entry reports ok=true
// with that entry. (A header-only transcript currently lands on the ok=false
// side of the line: resumeWriter returns a nil entry slice when no entry
// line follows the header, and RestoredTranscript keys ok on entries != nil.
// That is the pre-existing resumeWriter behavior, not a restore decision.)
func TestRestoreSession_RestoredTranscriptOKBoundary(t *testing.T) {
	t.Run("no transcript on disk", func(t *testing.T) {
		stateDir := t.TempDir()
		rootID := identifier.MustNewSessionID()
		if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
			t.Fatalf("mkdir sessions: %v", err)
		}
		meta := schema.SessionMeta{ID: rootID, ProfileID: "openai", Model: "gpt-5.2"}
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("save root metadata: %v", err)
		}
		restored, err := RestoreSessionFromMeta(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
		if err != nil {
			t.Fatalf("restore root: %v", err)
		}
		defer restored.Close()
		if _, _, ok := restored.RestoredTranscript(); ok {
			t.Fatal("a session restored without a transcript reported a restored one")
		}
	})
	t.Run("transcript with entries", func(t *testing.T) {
		restored, _ := rearmFixtureSession(t)
		defer restored.Close()
		_, entries, ok := restored.RestoredTranscript()
		if !ok || len(entries) == 0 {
			t.Fatalf("RestoredTranscript = ok:%t entries:%d, want ok:true with the fixture's entries", ok, len(entries))
		}
	})
}

// TestRootDelegateAttention_RearmFromTranscriptDoesNotOpenTheFile proves the
// restore-path rearm folds the in-memory entries restore already decoded: the
// transcript file is REMOVED before the rearm runs, so a fold that re-opened
// it would find nothing (the file form treats a missing transcript as an
// empty fold) and the pending attention would vanish from the wake cache.
func TestRootDelegateAttention_RearmFromTranscriptDoesNotOpenTheFile(t *testing.T) {
	restored, pendingID := rearmFixtureSession(t)
	defer restored.Close()

	if err := os.Remove(transcriptPath(restored.stateDir, restored.id)); err != nil {
		t.Fatalf("remove transcript to prove in-memory fold: %v", err)
	}
	// The fold must still report the unresolved attention, proving it came
	// from the retained entries rather than the (now missing) file.
	fold, err := foldDelegateAttention(restoredEntriesForRearm(t, restored))
	if err != nil {
		t.Fatalf("fold entries: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{pendingID}) {
		t.Fatalf("pending after file removal = %#v, want exactly the unresolved attention", got)
	}
}

// TestRootDelegateAttention_RearmYieldsSamePendingIDsAsFileFold is the
// differential proof: the entries the restored session retained must fold to
// the same pending ids the file itself folds to, over a fixture with both an
// open and a resolved attention.
func TestRootDelegateAttention_RearmYieldsSamePendingIDsAsFileFold(t *testing.T) {
	restored, pendingID := rearmFixtureSession(t)
	defer restored.Close()

	fileFold, err := readDelegateAttentionFold(transcriptPath(restored.stateDir, restored.id), restored.id)
	if err != nil {
		t.Fatalf("read file fold: %v", err)
	}
	entriesFold, err := foldDelegateAttention(restoredEntriesForRearm(t, restored))
	if err != nil {
		t.Fatalf("fold retained entries: %v", err)
	}
	if want := fileFold.pendingIDs(); !reflect.DeepEqual(entriesFold.pendingIDs(), want) || !reflect.DeepEqual(want, []string{pendingID}) {
		t.Fatalf("pending diverge: file=%#v entries=%#v, want exactly the unresolved attention", fileFold.pendingIDs(), entriesFold.pendingIDs())
	}
}

// TestRootDelegateAttention_RestoredTranscriptExposesFinalEntries pins the
// restore→serve handoff: RestoredTranscript returns the entries restore
// validated (non-nil once a transcript existed) and a header whose SessionID
// matches the session.
func TestRootDelegateAttention_RestoredTranscriptExposesFinalEntries(t *testing.T) {
	restored, _ := rearmFixtureSession(t)
	defer restored.Close()

	header, entries, ok := restored.RestoredTranscript()
	if !ok || len(entries) == 0 {
		t.Fatalf("RestoredTranscript = ok:%t entries:%d, want the restore-validated entries", ok, len(entries))
	}
	if header.SessionID != restored.id {
		t.Fatalf("restored header session %q, want %q", header.SessionID, restored.id)
	}
	if _, _, ok := (&Session{id: restored.id}).RestoredTranscript(); ok {
		t.Fatal("a session without a restored transcript reported one")
	}
}

func restoredEntriesForRearm(t *testing.T, s *Session) []transcript.Entry {
	t.Helper()
	_, entries, ok := s.RestoredTranscript()
	if !ok {
		t.Fatal("session retained no restored transcript entries")
	}
	return entries
}
