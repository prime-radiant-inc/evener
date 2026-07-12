//go:build serffuzz

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzJobDelegateSendSeed100Edges exercises deterministic send and restore
// guards whose inputs normally require a partially lost delegate runtime.
func FuzzJobDelegateSendSeed100Edges(f *testing.F) {
	for seed := byte(0); seed < 10; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed byte) {
		switch seed % 10 {
		case 0:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			events := loadJobStoreEvents(t, s.jobManager)
			for i := range events {
				if events[i].Kind == jobstore.EventJobStarted && events[i].JobID == rec.JobID {
					events[i].JobID = "job_missing"
				}
			}
			rewriteJobStoreEvents(t, s.jobManager, events)
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed"})
			requireSendSeed100Error(t, res, "target_not_resumable:")
		case 1:
			rec := sendSeed100RestoreRecord("parent", "child")
			rec.TranscriptRef = "malformed"
			rec.DelegateRestore.TranscriptRef = rec.TranscriptRef
			if got := validateDelegateRestoreState(rec, "parent", true); got != notResumableParentLinkageUnavailable {
				t.Fatalf("reason = %q", got)
			}
		case 2:
			var s *Session
			if got := s.assessDelegateResumability(nil, delegateResumabilityPreflight).Reason; got != notResumableMissingDelegateResumeMetadata {
				t.Fatalf("reason = %q", got)
			}
		case 3:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			path := filepath.Join(s.stateDir, sessionsSubdir, rec.DelegateRestore.ChildSessionID+".transcript.jsonl")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(path), path); err != nil {
				t.Fatal(err)
			}
			if got := s.assessDelegateResumability(rec, delegateResumabilityPreflight).Reason; got != notResumableCorruptChildTranscript {
				t.Fatalf("reason = %q", got)
			}
		case 4:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			rec.DelegateRestore.Sandbox = &jobstore.SandboxSnapshot{Mode: "restricted", ExtraReadRoots: []string{"/tampered"}}
			if got := s.assessDelegateResumability(rec, delegateResumabilityPreflight).Reason; got == "" {
				t.Fatal("sandbox failure reason is empty")
			}
		case 5:
			s := &Session{}
			if _, err := s.resolveDelegateRestoreProfile(schema.SessionMeta{}, &jobstore.DelegateRestoreDescriptor{}); err == nil || err.Error() != "profile unavailable" {
				t.Fatalf("error = %v", err)
			}
		case 6:
			s := &Session{subagents: newSubagentManager(nil)}
			rec := sendSeed100RestoreRecord("parent", "child")
			if _, err := s.restoreTerminalDelegateChild(rec, "child", &delegateRestorePreflight{}); err == nil || err.Error() != "state directory is not configured" {
				t.Fatalf("error = %v", err)
			}
		case 7:
			s := &Session{stateDir: t.TempDir(), subagents: newSubagentManager(nil)}
			s.subagents.mu.Lock()
			s.subagents.closing = true
			s.subagents.mu.Unlock()
			rec := sendSeed100RestoreRecord("parent", "child")
			if _, err := s.restoreTerminalDelegateChild(rec, "child", &delegateRestorePreflight{}); err != errSubagentManagerClosing {
				t.Fatalf("error = %v", err)
			}
		case 8:
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			s := newDelegateRestorePreflightSession(t, client)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			s.delegateRestoreBeforeSideEffects = func(*Session) {
				sub := s.subagents.get(rec.DelegateRestore.ChildSessionID)
				if sub != nil {
					sub.mu.Lock()
					sub.driving = true
					sub.mu.Unlock()
				}
			}
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed", OnIdle: "start"})
			if res.Err != nil || res.Action != "steered" {
				t.Fatalf("driving child send = %+v, want steered", res)
			}
		case 9:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			s.subagents.track(&subagent{id: rec.DelegateRestore.ChildSessionID, sess: &Session{}, running: true})
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed", OnIdle: "start"})
			requireSendSeed100Error(t, res, "target_not_resumable:")
		}
	})
}

func sendSeed100RestoreRecord(parentID, childID string) *jobstore.JobRecord {
	ref := encodeRef("", childID)
	resumable := true
	return &jobstore.JobRecord{
		JobID: "job_seed", DelegateID: "dlg_seed", Type: jobstore.JobDelegate,
		Status: jobstore.StatusStopped, OwnerSessionID: parentID, VisibleToSession: parentID,
		TranscriptRef: ref, Resumable: &resumable,
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{
			ChildSessionID: childID, TranscriptRef: ref, ParentSessionID: parentID,
			ParentJobID: "job_seed", OwnerSessionID: parentID, VisibleSessionID: parentID,
			WorkingDir: ".", LocalEnvPolicy: "default",
		},
	}
}

func requireSendSeed100Error(t *testing.T, result sendMessageResult, prefix string) {
	t.Helper()
	if result.Err == nil || !strings.HasPrefix(result.Err.Error(), prefix) {
		t.Fatalf("result error = %v, want prefix %q", result.Err, prefix)
	}
}
