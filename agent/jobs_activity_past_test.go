package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

func TestLoadSessionJobActivityTree_FollowsOnlyStableDelegateChildren(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootpast"
	childID := "childpast"
	strayID := "straypast"
	started := time.Unix(100, 0).UTC()
	ended := started.Add(time.Second)

	writePastStableDelegates(t, stateDir, rootID,
		pastStableDescriptor(rootID, childID, "child task"),
		pastStableDescriptor(rootID, strayID, "stray task"),
	)
	if err := os.WriteFile(transcriptPath(stateDir, childID), []byte("malformed eligible child transcript\n"), 0o600); err != nil {
		t.Fatalf("corrupt child transcript: %v", err)
	}
	if err := os.Remove(transcriptPath(stateDir, strayID)); err != nil {
		t.Fatalf("remove stray transcript: %v", err)
	}
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_shell", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started, Description: "root shell"},
	)
	s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child_shell", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started, Description: "child shell"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_child_shell", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMeta(t, stateDir, childID, "Child")

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root.SessionID != rootID || len(got.Root.Entries) != 3 {
		t.Fatalf("root activity = %+v", got.Root)
	}
	child := pastFindDelegate(t, got.Root, childID)
	if child.Child == nil || len(child.Child.Entries) != 1 || child.Child.Entries[0].Job == nil || child.Child.Entries[0].Job.JobID != "job_child_shell" {
		t.Fatalf("child subtree = %+v", child)
	}
	stray := pastFindDelegate(t, got.Root, strayID)
	if stray.Child != nil || stray.Branch.Error == "" {
		t.Fatalf("missing child delegate = %+v", stray)
	}
}

func TestLoadSessionJobActivityTree_RejectsOutOfStateDirChild(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootboundary"
	childID := "childboundary"
	outsideStateDir := t.TempDir()
	started := time.Unix(200, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "outside"))
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	writeRawSessionMeta(t, filepath.Join(stateDir, "sessions", childID+".meta.json"), schema.SessionMeta{
		ID: childID, Name: "Outside", WorktreePath: filepath.Join(outsideStateDir, "evil"),
	})
	childJobsPath := s1cov_writeJobLog(t, outsideStateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_outside_shell", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started},
	)
	before, err := os.Stat(childJobsPath)
	if err != nil {
		t.Fatal(err)
	}

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	delegate := pastFindDelegate(t, got.Root, childID)
	if delegate.Child != nil || delegate.Branch.Error == "" {
		t.Fatalf("delegate = %+v", delegate)
	}
	after, err := os.Stat(childJobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.ModTime() != before.ModTime() || after.Size() != before.Size() {
		t.Fatalf("outside job log changed: before=%v/%d after=%v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
}

func TestLoadSessionJobActivityTree_UsesMaxPersistedRootRevisionAcrossDescendants(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootrevision"
	childID := "childrevision"
	started := time.Unix(250, 0).UTC()
	writePastStableDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "revision"))
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started},
	)
	savePastActivityMetaWithTreeRevision(t, stateDir, rootID, "Root", "", 3)
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 7)

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 7 {
		t.Fatalf("revision=%d, want 7", got.Revision)
	}
}

// TestLoadSessionJobActivityTree_ReadsSharedDelegateJournalOncePerRoot covers
// #448's evidence that loadHistoricalActivityBase re-read and re-folded the
// shared root delegates.jsonl once per VISITED session, making loading
// O(sessions x delegate events). root -> child1 -> child2 are three visited
// sessions sharing one delegates.jsonl at the root; the journal must be
// scanned exactly once regardless.
func TestLoadSessionJobActivityTree_ReadsSharedDelegateJournalOncePerRoot(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootonce"
	child1ID := "child1once"
	child2ID := "child2once"
	started := time.Unix(300, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID,
		pastStableDescriptor(rootID, child1ID, "child1 task"),
		pastStableDescriptor(child1ID, child2ID, "child2 task"),
	)
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child1ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child1", Type: jobstore.JobShell, OwnerSessionID: child1ID, VisibleToSession: child1ID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child2ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child2", Type: jobstore.JobShell, OwnerSessionID: child2ID, VisibleToSession: child2ID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, child1ID, "Child1", rootID, 0)
	savePastActivityMetaWithTreeRevision(t, stateDir, child2ID, "Child2", rootID, 0)

	var delegateScans int32
	original := scanDelegateJournal
	scanDelegateJournal = func(ctx context.Context, path string, limits delegatestore.ScanLimits) ([]delegatestore.Event, delegatestore.ReadDiagnostics, error) {
		atomic.AddInt32(&delegateScans, 1)
		return original(ctx, path, limits)
	}
	defer func() { scanDelegateJournal = original }()

	got, err := LoadSessionJobActivityTree(context.Background(), stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	child1 := pastFindDelegate(t, got.Root, child1ID)
	if child1.Child == nil {
		t.Fatalf("expected child1 subtree, got %+v", child1)
	}
	child2 := pastFindDelegate(t, *child1.Child, child2ID)
	if child2.Child == nil {
		t.Fatalf("expected child2 subtree, got %+v", child2)
	}
	if delegateScans != 1 {
		t.Fatalf("delegates.jsonl scanned %d times across 3 visited sessions, want exactly 1", delegateScans)
	}
}

// TestLoadSessionJobActivityTree_StopsOpeningLaterSessionsAfterCancellation
// covers #448's acceptance criterion that cancellation is checked between
// descendant sessions, not only between records within one journal: once the
// request context is canceled, no later session's jobs.jsonl is opened.
func TestLoadSessionJobActivityTree_StopsOpeningLaterSessionsAfterCancellation(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootcancel"
	child1ID := "child1cancel"
	child2ID := "child2cancel"
	started := time.Unix(400, 0).UTC()

	writePastStableDelegates(t, stateDir, rootID,
		pastStableDescriptor(rootID, child1ID, "child1 task"),
		pastStableDescriptor(rootID, child2ID, "child2 task"),
	)
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child1ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child1", Type: jobstore.JobShell, OwnerSessionID: child1ID, VisibleToSession: child1ID, StartedAt: &started},
	)
	s1cov_writeJobLog(t, stateDir, child2ID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child2", Type: jobstore.JobShell, OwnerSessionID: child2ID, VisibleToSession: child2ID, StartedAt: &started},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMetaWithTreeRevision(t, stateDir, child1ID, "Child1", rootID, 0)
	savePastActivityMetaWithTreeRevision(t, stateDir, child2ID, "Child2", rootID, 0)

	ctx, cancel := context.WithCancel(context.Background())
	var scannedPaths []string
	original := scanJobJournal
	scanJobJournal = func(ctx context.Context, path string, limits jobstore.ScanLimits) ([]jobstore.Event, error) {
		scannedPaths = append(scannedPaths, path)
		cancel() // cancel once the first session's own journal is reached
		return original(ctx, path, limits)
	}
	defer func() { scanJobJournal = original }()

	if _, err := LoadSessionJobActivityTree(ctx, stateDir, rootID, appwire.JobsListParams{}); err == nil {
		t.Fatal("expected an error from a canceled request")
	}
	if len(scannedPaths) != 1 {
		t.Fatalf("scanned %d job journals after cancellation, want exactly 1 (root only): %v", len(scannedPaths), scannedPaths)
	}
}

func pastStableDescriptor(ownerSessionID, childSessionID, task string) delegatestore.Descriptor {
	return delegatestore.Descriptor{
		ChildSessionID:   childSessionID,
		TranscriptRef:    encodeRef("", childSessionID),
		OwnerSessionID:   ownerSessionID,
		VisibleSessionID: ownerSessionID,
		Task:             task,
		AgentType:        "general",
		ToolNameCeiling:  []string{"communicate"},
		Resumable:        true,
	}
}

func writePastStableDelegates(t *testing.T, stateDir, rootSessionID string, descriptors ...delegatestore.Descriptor) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range descriptors {
		writer, err := transcript.NewWriter(transcriptPath(stateDir, descriptor.ChildSessionID), transcript.Header{
			SessionID:       descriptor.ChildSessionID,
			ParentSessionID: descriptor.OwnerSessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store, err := delegatestore.Open(filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(nil)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]delegatestore.Event, 0, len(descriptors))
	for i, descriptor := range descriptors {
		events = append(events, delegatestore.Event{
			Kind:       delegatestore.EventDelegateCreated,
			TS:         time.Unix(int64(i+1), 0).UTC(),
			DelegateID: "dlg_" + strings.TrimPrefix(descriptor.ChildSessionID, "child"),
			Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
		})
	}
	if _, _, err := store.AppendBatch(state, events); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func savePastActivityMeta(t *testing.T, stateDir, sessionID, name string) {
	t.Helper()
	savePastActivityMetaWithTreeRevision(t, stateDir, sessionID, name, "", 0)
}

func savePastActivityMetaWithTreeRevision(t *testing.T, stateDir, sessionID, name, rootID string, revision uint64) {
	t.Helper()
	meta := schema.SessionMeta{ID: sessionID, ProfileID: "openai", Model: "gpt-5.2", Name: name, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(), JobTreeRevision: revision}
	if strings.TrimSpace(rootID) != "" {
		meta.JobTreeRootSessionID = rootID
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta(%s): %v", sessionID, err)
	}
}

func pastFindDelegate(t *testing.T, root appwire.JobActivitySession, childID string) appwire.JobActivityDelegate {
	t.Helper()
	for _, entry := range root.Entries {
		if entry.Delegate != nil && entry.Delegate.ChildSessionID == childID {
			return *entry.Delegate
		}
	}
	t.Fatalf("no delegate child=%q in %+v", childID, root.Entries)
	return appwire.JobActivityDelegate{}
}

func writeRawSessionMeta(t *testing.T, path string, meta schema.SessionMeta) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}
