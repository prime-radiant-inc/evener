package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"weak"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// rearmFixtureSessionWithConfig restores, with restoreCfg, a root session
// whose transcript carries one unresolved and one consumed delegate
// attention, and returns the restored session plus the durable pending id the
// fold must re-arm. The fixture supplies StateDir.
func rearmFixtureSessionWithConfig(t *testing.T, restoreCfg RestoreSessionConfig) (*Session, string) {
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
	restoreCfg.StateDir = stateDir
	restored, err := RestoreSessionFromMetaWithConfig(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, restoreCfg)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	return restored, pendingID
}

// TestRootDelegateAttention_RestoreRearmFoldsTheRetainedEntries proves the
// restore-path rearm took the entries branch (not the file read): the seam
// captures the entry list restore passed to the fold, and that list must be
// the one OnRestoredTranscript hands over — the same final entry list serve
// will see.
func TestRootDelegateAttention_RestoreRearmFoldsTheRetainedEntries(t *testing.T) {
	var foldedEntries []transcript.Entry
	var captured restoredTranscriptCapture
	restoreCfg := RestoreSessionConfig{OnRestoredTranscript: captured.record}
	restoreCfg.testOnly.delegateAttentionFoldEntries = func(entries []transcript.Entry) (delegateAttentionFold, error) {
		foldedEntries = entries
		return foldDelegateAttention(entries)
	}
	restored, pendingID := rearmFixtureSessionWithConfig(t, restoreCfg)
	defer restored.Close()

	if foldedEntries == nil {
		t.Fatal("restore rearm never took the entries fold branch")
	}
	if !captured.opened {
		t.Fatal("restore handed over no transcript")
	}
	retained := captured.entries
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

// TestRestoreSession_RestoredTranscriptOKBoundary pins the opened flag's
// boundary: a session restored with NO transcript on disk hands over
// opened=false, one restored over a transcript with at least one entry hands
// over opened=true with that entry, and one restored over a header-only
// transcript hands over opened=true with a non-nil empty slice: opened means
// "a transcript was opened and validated," not "entries exist."
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
		var captured restoredTranscriptCapture
		restored, err := RestoreSessionFromMetaWithConfig(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, RestoreSessionConfig{StateDir: stateDir, OnRestoredTranscript: captured.record})
		if err != nil {
			t.Fatalf("restore root: %v", err)
		}
		defer restored.Close()
		if captured.opened {
			t.Fatal("a session restored without a transcript reported a restored one")
		}
	})
	t.Run("transcript with entries", func(t *testing.T) {
		var captured restoredTranscriptCapture
		restored, _ := rearmFixtureSessionWithConfig(t, RestoreSessionConfig{OnRestoredTranscript: captured.record})
		defer restored.Close()
		if !captured.opened || len(captured.entries) == 0 {
			t.Fatalf("handed over opened:%t entries:%d, want opened:true with the fixture's entries", captured.opened, len(captured.entries))
		}
	})
	t.Run("header-only transcript", func(t *testing.T) {
		stateDir := t.TempDir()
		rootID := identifier.MustNewSessionID()
		meta := schema.SessionMeta{ID: rootID, ProfileID: "openai", Model: "gpt-5.2"}
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("save root metadata: %v", err)
		}
		writer, err := transcript.NewWriter(transcriptPath(stateDir, rootID), transcript.Header{SessionID: rootID, ProfileID: "openai", Model: "gpt-5.2"})
		if err != nil {
			t.Fatalf("write header-only transcript: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
		var captured restoredTranscriptCapture
		restored, err := RestoreSessionFromMetaWithConfig(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, RestoreSessionConfig{StateDir: stateDir, OnRestoredTranscript: captured.record})
		if err != nil {
			t.Fatalf("restore over header-only transcript: %v", err)
		}
		defer restored.Close()
		if !captured.opened {
			t.Fatal("a session restored over a header-only transcript reported no restored transcript; opened must mean 'a transcript was opened'")
		}
		if captured.entries == nil || len(captured.entries) != 0 {
			t.Fatalf("entries = %#v, want a non-nil empty slice over a header-only transcript", captured.entries)
		}
	})
}

// TestRootDelegateAttention_RearmFromTranscriptDoesNotOpenTheFile proves the
// restore-path rearm folds the in-memory entries restore already decoded: the
// transcript file is REMOVED before the rearm runs, so a fold that re-opened
// it would find nothing (the file form treats a missing transcript as an
// empty fold) and the pending attention would vanish from the wake cache.
func TestRootDelegateAttention_RearmFromTranscriptDoesNotOpenTheFile(t *testing.T) {
	var captured restoredTranscriptCapture
	restored, pendingID := rearmFixtureSessionWithConfig(t, RestoreSessionConfig{OnRestoredTranscript: captured.record})
	defer restored.Close()

	if err := os.Remove(transcriptPath(restored.stateDir, restored.id)); err != nil {
		t.Fatalf("remove transcript to prove in-memory fold: %v", err)
	}
	// The fold must still report the unresolved attention, proving it came
	// from the handed-over entries rather than the (now missing) file.
	fold, err := foldDelegateAttention(captured.entries)
	if err != nil {
		t.Fatalf("fold entries: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{pendingID}) {
		t.Fatalf("pending after file removal = %#v, want exactly the unresolved attention", got)
	}
}

// TestRootDelegateAttention_RearmYieldsSamePendingIDsAsFileFold is the
// differential proof: the entries restore handed over must fold to
// the same pending ids the file itself folds to, over a fixture with both an
// open and a resolved attention.
func TestRootDelegateAttention_RearmYieldsSamePendingIDsAsFileFold(t *testing.T) {
	var captured restoredTranscriptCapture
	restored, pendingID := rearmFixtureSessionWithConfig(t, RestoreSessionConfig{OnRestoredTranscript: captured.record})
	defer restored.Close()

	fileFold, err := readDelegateAttentionFold(transcriptPath(restored.stateDir, restored.id), restored.id)
	if err != nil {
		t.Fatalf("read file fold: %v", err)
	}
	entriesFold, err := foldDelegateAttention(captured.entries)
	if err != nil {
		t.Fatalf("fold retained entries: %v", err)
	}
	if want := fileFold.pendingIDs(); !reflect.DeepEqual(entriesFold.pendingIDs(), want) || !reflect.DeepEqual(want, []string{pendingID}) {
		t.Fatalf("pending diverge: file=%#v entries=%#v, want exactly the unresolved attention", fileFold.pendingIDs(), entriesFold.pendingIDs())
	}
}

// TestRootDelegateAttention_RestoredTranscriptExposesFinalEntries pins the
// restore→serve handoff: OnRestoredTranscript receives the entries restore
// validated (non-nil once a transcript existed) and a header whose SessionID
// matches the session.
func TestRootDelegateAttention_RestoredTranscriptExposesFinalEntries(t *testing.T) {
	var captured restoredTranscriptCapture
	restored, _ := rearmFixtureSessionWithConfig(t, RestoreSessionConfig{OnRestoredTranscript: captured.record})
	defer restored.Close()

	if !captured.opened || len(captured.entries) == 0 {
		t.Fatalf("handed over opened:%t entries:%d, want the restore-validated entries", captured.opened, len(captured.entries))
	}
	if captured.header.SessionID != restored.id {
		t.Fatalf("restored header session %q, want %q", captured.header.SessionID, restored.id)
	}
}

// restoredTranscriptCapture records what RestoreSessionConfig.
// OnRestoredTranscript handed over.
type restoredTranscriptCapture struct {
	header  transcript.Header
	entries []transcript.Entry
	opened  bool
}

func (c *restoredTranscriptCapture) record(header transcript.Header, entries []transcript.Entry, opened bool) {
	c.header, c.entries, c.opened = header, entries, opened
}

// TestRestoredSessionRetainsNoDecodedTranscript pins that restore hands the
// decoded transcript over without keeping it: once the receiver lets go, the
// entries are collectable while the restored session lives on. A delegate
// child or a one-shot run restores with no receiver at all, and a transcript
// can run to tens of megabytes decoded.
func TestRestoredSessionRetainsNoDecodedTranscript(t *testing.T) {
	var captured restoredTranscriptCapture
	restored, _ := rearmFixtureSessionWithConfig(t, RestoreSessionConfig{OnRestoredTranscript: captured.record})
	defer restored.Close()
	if len(captured.entries) == 0 {
		t.Fatal("restore handed over no entries")
	}
	first := weak.Make(&captured.entries[0])
	captured = restoredTranscriptCapture{}
	runtime.GC()
	if first.Value() != nil {
		t.Fatal("the restored session still references its decoded transcript entries")
	}
	runtime.KeepAlive(restored)
}
