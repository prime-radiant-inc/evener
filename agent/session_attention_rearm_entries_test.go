package agent

import (
	"context"
	"os"
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
	if err := os.MkdirAll(schema.SessionsDir(stateDir), 0o755); err != nil {
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
	if _, err := writer.Record(pending, transcript.RecordOptions{Door: transcript.DoorDurable}); err != nil {
		_ = writer.Close()
		t.Fatalf("append pending attention: %v", err)
	}
	consumed := schema.NewTurn(schema.TurnSteering, llm.User(`<delegate-notification delegate_id="dlg_rearm">already resolved</delegate-notification>`))
	consumed.AttentionID = consumedID
	consumed.StableTurnID = newQueueEntryID()
	if _, err := writer.Record(consumed, transcript.RecordOptions{Door: transcript.DoorDurable}); err != nil {
		_ = writer.Close()
		t.Fatalf("append consumed attention: %v", err)
	}
	resolution := schema.NewTurn(schema.TurnAttentionResolution, llm.User(""))
	resolution.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID:      consumedID,
		Disposition:      string(delegateAttentionConsumed),
		ResumeGeneration: 0,
	}
	if _, err := writer.Record(resolution, transcript.RecordOptions{Door: transcript.DoorDurable}); err != nil {
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
// the same final entry list stored on disk.
func TestRootDelegateAttention_RestoreRearmFoldsTheRetainedEntries(t *testing.T) {
	var foldedEntries []transcript.Entry
	restoreCfg := RestoreSessionConfig{}
	restoreCfg.testOnly.delegateAttentionFoldEntries = func(entries []transcript.Entry) (delegateAttentionFold, error) {
		foldedEntries = entries
		return foldDelegateAttention(entries)
	}
	restored, pendingID := rearmFixtureSessionWithConfig(t, restoreCfg)
	defer restored.Close()

	if foldedEntries == nil {
		t.Fatal("restore rearm never took the entries fold branch")
	}
	onDisk, err := readTranscriptFull(context.Background(), transcriptPath(restored.stateDir, restored.id), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	retained := onDisk.Entries
	if len(foldedEntries) != len(retained) {
		t.Fatalf("entries passed to the fold = %d, on-disk entries = %d; the rearm must fold the same final list on disk", len(foldedEntries), len(retained))
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

// TestRootDelegateAttention_RearmFromTranscriptDoesNotOpenTheFile proves the
// restore-path rearm folds the in-memory entries restore already decoded: the
// transcript file is REMOVED before the rearm runs, so a fold that re-opened
// it would find nothing (the file form treats a missing transcript as an
// empty fold) and the pending attention would vanish from the wake cache.
func TestRootDelegateAttention_RearmFromTranscriptDoesNotOpenTheFile(t *testing.T) {
	var foldedEntries []transcript.Entry
	restoreCfg := RestoreSessionConfig{}
	restoreCfg.testOnly.delegateAttentionFoldEntries = func(entries []transcript.Entry) (delegateAttentionFold, error) {
		foldedEntries = entries
		return foldDelegateAttention(entries)
	}
	restoreCfg.testOnly.delegateAttentionReadFold = func(string, string) (delegateAttentionFold, error) {
		t.Fatal("rearm opened the transcript file instead of folding in-memory entries")
		return delegateAttentionFold{}, nil
	}
	restored, pendingID := rearmFixtureSessionWithConfig(t, restoreCfg)
	defer restored.Close()

	if foldedEntries == nil {
		t.Fatal("restore rearm never took the entries fold branch")
	}
	if err := os.Remove(transcriptPath(restored.stateDir, restored.id)); err != nil {
		t.Fatalf("remove transcript to prove in-memory fold: %v", err)
	}
	restored.attentionMu.Lock()
	restored.rootAttentionWake = false
	restored.rootAttentionWakeIDs = nil
	restored.attentionMu.Unlock()
	if err := restored.rearmRootDelegateAttentionFromTranscript(foldedEntries); err != nil {
		t.Fatalf("rearmRootDelegateAttentionFromTranscript after file removal: %v", err)
	}
	restored.attentionMu.Lock()
	woke := restored.rootAttentionWake
	_, hasPending := restored.rootAttentionWakeIDs[pendingID]
	wakeCount := len(restored.rootAttentionWakeIDs)
	restored.attentionMu.Unlock()
	if !woke || !hasPending || wakeCount != 1 {
		t.Fatalf("wake after file removal: woke=%t hasPending=%t count=%d, want woke=true with only %q", woke, hasPending, wakeCount, pendingID)
	}
	fold, err := foldDelegateAttention(foldedEntries)
	if err != nil {
		t.Fatalf("fold entries: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{pendingID}) {
		t.Fatalf("pending after file removal = %#v, want exactly the unresolved attention", got)
	}
}

// TestRootDelegateAttention_RearmYieldsSamePendingIDsAsFileFold is the
// differential proof: the entries restore folded must match what the file
// itself folds to, over a fixture with both an open and a resolved attention.
func TestRootDelegateAttention_RearmYieldsSamePendingIDsAsFileFold(t *testing.T) {
	var foldedEntries []transcript.Entry
	restoreCfg := RestoreSessionConfig{}
	restoreCfg.testOnly.delegateAttentionFoldEntries = func(entries []transcript.Entry) (delegateAttentionFold, error) {
		foldedEntries = entries
		return foldDelegateAttention(entries)
	}
	restored, pendingID := rearmFixtureSessionWithConfig(t, restoreCfg)
	defer restored.Close()

	if foldedEntries == nil {
		t.Fatal("restore rearm never took the entries fold branch")
	}
	fileFold, err := readDelegateAttentionFold(transcriptPath(restored.stateDir, restored.id), restored.id)
	if err != nil {
		t.Fatalf("read file fold: %v", err)
	}
	entriesFold, err := foldDelegateAttention(foldedEntries)
	if err != nil {
		t.Fatalf("fold retained entries: %v", err)
	}
	if want := fileFold.pendingIDs(); !reflect.DeepEqual(entriesFold.pendingIDs(), want) || !reflect.DeepEqual(want, []string{pendingID}) {
		t.Fatalf("pending diverge: file=%#v entries=%#v, want exactly the unresolved attention", fileFold.pendingIDs(), entriesFold.pendingIDs())
	}
}

// TestRestoredSessionRetainsNoDecodedTranscript pins that restore does not keep
// the decoded transcript slice after folding it: once restore returns, the
// entries are collectable while the restored session lives on.
func TestRestoredSessionRetainsNoDecodedTranscript(t *testing.T) {
	var first weak.Pointer[transcript.Entry]
	restoreCfg := RestoreSessionConfig{}
	restoreCfg.testOnly.delegateAttentionFoldEntries = func(entries []transcript.Entry) (delegateAttentionFold, error) {
		if len(entries) > 0 {
			first = weak.Make(&entries[0])
		}
		return foldDelegateAttention(entries)
	}
	restored, _ := rearmFixtureSessionWithConfig(t, restoreCfg)
	defer restored.Close()
	if first == (weak.Pointer[transcript.Entry]{}) {
		t.Fatal("restore folded no entries")
	}
	runtime.GC()
	if first.Value() != nil {
		t.Fatal("the restored session still references its decoded transcript entries")
	}
	runtime.KeepAlive(restored)
}
