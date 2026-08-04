//go:build serffuzz

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzJobDelegateExactCreateSend drives defensive create/send branches with
// deterministic sessions and durable fixture records.
func FuzzJobDelegateExactCreateSend(f *testing.F) {
	for op := byte(0); op < 13; op++ {
		f.Add(op)
	}
	f.Fuzz(func(t *testing.T, op byte) {
		switch op % 13 {
		case 0:
			called := false
			s := &Session{delegateRestoreResumeHistory: func([]transcript.Entry) []schema.Turn {
				called = true
				return nil
			}}
			_ = s.resumeHistoryForRestore(nil)
			if !called {
				t.Fatal("resume history override was not called")
			}
		case 1:
			res := (&Session{}).createDelegate(context.Background(), delegateArgs{Task: "create"})
			if res.Err == nil {
				t.Fatal("create without a job manager succeeded")
			}
		case 2:
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
			}})
			s := newDelegateTestSession(t, client)
			s.cfg.testOnly.sandboxProber = sandbox.FakeProber{Facts: sandbox.HostFacts{
				OS:               "linux",
				Home:             t.TempDir(),
				BwrapPath:        "/fixture/bwrap",
				BwrapCapable:     true,
				OverlaySupported: true,
			}}
			res := s.createDelegate(context.Background(), delegateArgs{Task: "sandboxed", Sandbox: "restricted", Background: true})
			if res.Err != nil {
				t.Fatalf("sandboxed create: %v", res.Err)
			}
		case 3:
			jdaf100CreateAttachRollback(t)
		case 4:
			jdaf100CreateLaunchRollback(t)
		case 5:
			res := (&Session{}).sendDelegateMessage(context.Background(), sendMessageArgs{Target: "dlg_missing", Message: "hello"})
			if res.Err == nil {
				t.Fatal("send without a job manager succeeded")
			}
		case 6:
			s := &Session{}
			s.cfg.spawn.parentSteerDelivered = func(string, *provenance.Causal, string) bool { return true }
			s.setActiveEntryKind(EntryWatchDelivery)
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: runtimeMessageAliasCaller, Message: "callback"})
			if res.Err != nil || !res.Delivered {
				t.Fatalf("watch callback send = %+v", res)
			}
		case 7:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			setStoredDelegateTerminalStatus(t, s, rec, jobstore.Status("pending"), "pending")
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "hello"})
			requireSendSeed100Error(t, res, "target_not_resumable:")
		case 8:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusCompleted, "exit_zero")
			rec = loadShellRecord(t, s.jobManager, rec.JobID)
			_, childID, err := decodeRef(rec.TranscriptRef)
			if err != nil {
				t.Fatal(err)
			}
			s.subagents.track(&subagent{id: childID, sess: &Session{}, driving: true})
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "hello"})
			_ = res
		case 9:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusCompleted, "exit_zero")
			rec = loadShellRecord(t, s.jobManager, rec.JobID)
			_, childID, err := decodeRef(rec.TranscriptRef)
			if err != nil {
				t.Fatal(err)
			}
			s.subagents.track(&subagent{id: childID, sess: &Session{}, running: true})
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "hello"})
			requireSendSeed100Error(t, res, "target_not_resumable:")
		case 10:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusCompleted, "exit_zero")
			rec = loadShellRecord(t, s.jobManager, rec.JobID)
			_, childID, err := decodeRef(rec.TranscriptRef)
			if err != nil {
				t.Fatal(err)
			}
			s.subagents.track(&subagent{id: childID, sess: &Session{}, running: true})
			now := s.jobManager.now()
			if err := s.jobManager.appendEvent(jobstore.Event{
				Kind:             jobstore.EventJobStarted,
				TS:               now,
				JobID:            jobstore.NewJobID(s.ID()),
				DelegateID:       jobstore.NewDelegateID(),
				Type:             jobstore.JobDelegate,
				OwnerSessionID:   s.ID(),
				VisibleToSession: s.ID(),
				StartedAt:        &now,
				TranscriptRef:    rec.TranscriptRef,
			}); err != nil {
				t.Fatal(err)
			}
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "hello"})
			if res.Action != "steered" {
				t.Fatalf("running delegate redirect = %+v", res)
			}
		case 11:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusCompleted, "exit_zero")
			removeChildSessionMeta(t, s, rec)
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "hello"})
			if res.Err == nil {
				t.Fatalf("missing-runtime restore = %+v", res)
			}
		case 12:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			if err := os.WriteFile(filepath.Join(s.jobManager.dir, "jobs.jsonl"), []byte("{\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "hello"})
			if res.Err == nil {
				t.Fatal("send with corrupt delegate store succeeded")
			}
		}
	})
}
