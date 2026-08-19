package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
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
	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_root_shell", Type: jobstore.JobShell, OwnerSessionID: rootID, VisibleToSession: rootID, StartedAt: &started, Description: "root shell"},
	)
	s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child_shell", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started, Description: "child shell"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_child_shell", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMeta(t, stateDir, childID, "Child")

	got, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{})
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

	got, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{})
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

	got, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 7 {
		t.Fatalf("revision=%d, want 7", got.Revision)
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
