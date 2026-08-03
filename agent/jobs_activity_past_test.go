package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

func TestLoadSessionJobActivityTree_FollowsOnlyDurableDelegateChildren(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootpast"
	childID := "childpast"
	strayID := "straypast"
	started := time.Unix(100, 0).UTC()
	ended := started.Add(time.Second)

	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventDelegateCreated, TS: started, DelegateID: "dlg_child", Delegate: &jobstore.DelegateEvent{ChildSessionID: childID, TranscriptRef: encodeRef("", childID), OwnerSessionID: rootID, VisibleSessionID: rootID, Generation: "gen_1", Resumable: true}},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_delegate_child", Type: jobstore.JobDelegate, OwnerSessionID: rootID, VisibleToSession: rootID, DelegateID: "dlg_child", StartedAt: &started, Task: "child task", TranscriptRef: encodeRef("", childID)},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_delegate_child", Status: jobstore.StatusCompleted, EndedAt: &ended},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started.Add(2*time.Second), JobID: "job_stray", Type: jobstore.JobDelegate, OwnerSessionID: rootID, VisibleToSession: rootID, DelegateID: "dlg_stray", StartedAt: &started, Task: "stray task", TranscriptRef: encodeRef("", strayID)},
	)
	s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child_shell", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started, Description: "child shell"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_child_shell", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
	s1cov_writeJobLog(t, stateDir, strayID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_stray_shell", Type: jobstore.JobShell, OwnerSessionID: strayID, VisibleToSession: strayID, StartedAt: &started, Description: "stray shell"},
	)

	savePastActivityMeta(t, stateDir, rootID, "Root")
	savePastActivityMeta(t, stateDir, childID, "Child")
	savePastActivityMeta(t, stateDir, strayID, "Stray")

	got, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root.SessionID != rootID {
		t.Fatalf("root=%q, want %q", got.Root.SessionID, rootID)
	}
	if len(got.Root.Entries) != 2 {
		t.Fatalf("entries=%d, want 2 durable delegate rows", len(got.Root.Entries))
	}
	child := pastFindDelegate(t, got.Root, childID)
	if child.Child == nil {
		t.Fatalf("child delegate=%+v", child)
	}
	if len(child.Child.Entries) != 1 || child.Child.Entries[0].Job == nil || child.Child.Entries[0].Job.JobID != "job_child_shell" {
		t.Fatalf("child subtree=%+v", child.Child)
	}
	stray := pastFindDelegateByID(t, got.Root, "dlg_stray")
	if stray.DelegateID != "dlg_stray" {
		t.Fatalf("stray delegate=%+v", stray)
	}
	if stray.Child != nil || stray.Branch.Error == "" {
		t.Fatalf("stray delegate=%+v", stray)
	}
}

func TestLoadSessionJobActivityTree_RejectsOutOfStateDirChild(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootboundary"
	outsideStateDir := t.TempDir()
	childID := "childboundary"
	started := time.Unix(200, 0).UTC()

	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventDelegateCreated, TS: started, DelegateID: "dlg_outside", Delegate: &jobstore.DelegateEvent{ChildSessionID: childID, TranscriptRef: encodeRef("", childID), OwnerSessionID: rootID, VisibleSessionID: rootID, Generation: "gen_1", Resumable: true}},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_outside", Type: jobstore.JobDelegate, OwnerSessionID: rootID, VisibleToSession: rootID, DelegateID: "dlg_outside", StartedAt: &started, TranscriptRef: encodeRef("", childID)},
	)
	savePastActivityMeta(t, stateDir, rootID, "Root")

	outsideMetaPath := filepath.Join(stateDir, "sessions", childID+".meta.json")
	writeRawSessionMeta(t, outsideMetaPath, schema.SessionMeta{ID: childID, Name: "Outside", WorktreePath: filepath.Join(outsideStateDir, "evil")})
	childJobsPath := s1cov_writeJobLog(t, outsideStateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_outside_shell", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started, Description: "outside shell"},
	)
	before, err := os.Stat(childJobsPath)
	if err != nil {
		t.Fatalf("stat outside jobs: %v", err)
	}

	got, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	delegate := pastFindDelegate(t, got.Root, childID)
	if delegate.Child != nil || delegate.Branch.Error == "" {
		t.Fatalf("delegate=%+v", delegate)
	}
	if delegate.Branch.Error == "" {
		t.Fatalf("branch error=%q", delegate.Branch.Error)
	}
	after, err := os.Stat(childJobsPath)
	if err != nil {
		t.Fatalf("restat outside jobs: %v", err)
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
	ended := started.Add(time.Second)

	s1cov_writeJobLog(t, stateDir, rootID,
		jobstore.Event{Kind: jobstore.EventDelegateCreated, TS: started, DelegateID: "dlg_revision", Delegate: &jobstore.DelegateEvent{ChildSessionID: childID, TranscriptRef: encodeRef("", childID), OwnerSessionID: rootID, VisibleSessionID: rootID, Generation: "gen_1", Resumable: true}},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_delegate_revision", Type: jobstore.JobDelegate, OwnerSessionID: rootID, VisibleToSession: rootID, DelegateID: "dlg_revision", StartedAt: &started, TranscriptRef: encodeRef("", childID)},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_delegate_revision", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
	s1cov_writeJobLog(t, stateDir, childID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_child_revision", Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &started, Description: "child shell"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_child_revision", Status: jobstore.StatusCompleted, EndedAt: &ended},
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

func TestLoadSessionJobActivityTree_ContinuationFollowsDurablePath(t *testing.T) {
	stateDir := t.TempDir()
	rootID := "rootcont"
	childID := "childcont"
	started := time.Unix(300, 0).UTC()
	ended := started.Add(time.Second)
	var events []jobstore.Event
	events = append(events,
		jobstore.Event{Kind: jobstore.EventDelegateCreated, TS: started, DelegateID: "dlg_cont", Delegate: &jobstore.DelegateEvent{ChildSessionID: childID, TranscriptRef: encodeRef("", childID), OwnerSessionID: rootID, VisibleSessionID: rootID, Generation: "gen_1", Resumable: true}},
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_delegate_cont", Type: jobstore.JobDelegate, OwnerSessionID: rootID, VisibleToSession: rootID, DelegateID: "dlg_cont", StartedAt: &started, TranscriptRef: encodeRef("", childID)},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_delegate_cont", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
	s1cov_writeJobLog(t, stateDir, rootID, events...)
	var childEvents []jobstore.Event
	for i := 0; i < activityMaxWorkUnits+2; i++ {
		ts := started.Add(time.Duration(i+1) * time.Second)
		childEvents = append(childEvents, jobstore.Event{Kind: jobstore.EventJobStarted, TS: ts, JobID: "job_child_" + strings.Repeat("x", 0) + time.Unix(int64(i+1), 0).UTC().Format("150405") + string(rune('a'+(i%26))), Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: childID, StartedAt: &ts, Description: "child"})
	}
	s1cov_writeJobLog(t, stateDir, childID, childEvents...)
	savePastActivityMetaWithTreeRevision(t, stateDir, rootID, "Root", "", 4)
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 9)

	first, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 9 {
		t.Fatalf("first revision=%d, want 9", first.Revision)
	}
	child := pastFindDelegate(t, first.Root, childID)
	if child.Child == nil || !child.Child.Branch.Truncated || child.Child.Branch.Continuation == "" {
		t.Fatalf("child branch=%+v child=%+v", child.Branch, child.Child)
	}
	cont, err := LoadSessionJobActivityTree(stateDir, rootID, appwire.JobsListParams{Continuation: child.Child.Branch.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	if cont.Root.SessionID != rootID || cont.Root.Ref != encodeRef("", rootID) {
		t.Fatalf("continued root=%+v", cont.Root)
	}
	if len(cont.Root.Entries) != 1 || cont.Root.Entries[0].Delegate == nil || cont.Root.Entries[0].Delegate.Child == nil {
		t.Fatalf("continued envelope=%+v", cont.Root.Entries)
	}
	if cont.Root.Entries[0].Delegate.Child.SessionID != childID {
		t.Fatalf("continued child=%+v", cont.Root.Entries[0].Delegate.Child)
	}
	if cont.Revision != 9 {
		t.Fatalf("continued revision=%d, want 9", cont.Revision)
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
		if entry.Delegate == nil {
			continue
		}
		if childID == "" || entry.Delegate.ChildSessionID == childID {
			return *entry.Delegate
		}
	}
	t.Fatalf("no delegate child=%q in %+v", childID, root.Entries)
	return appwire.JobActivityDelegate{}
}

func pastFindDelegateByID(t *testing.T, root appwire.JobActivitySession, delegateID string) appwire.JobActivityDelegate {
	t.Helper()
	for _, entry := range root.Entries {
		if entry.Delegate != nil && entry.Delegate.DelegateID == delegateID {
			return *entry.Delegate
		}
	}
	t.Fatalf("no delegate id=%q in %+v", delegateID, root.Entries)
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
