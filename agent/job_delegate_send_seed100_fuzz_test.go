//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzJobDelegateSendSeed100Edges exercises deterministic send and restore
// guards whose inputs normally require a partially lost delegate runtime.
func FuzzJobDelegateSendSeed100Edges(f *testing.F) {
	for seed := byte(0); seed < 21; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed byte) {
		switch seed % 21 {
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
			s := &Session{subagents: newSubagentManager(nil, 0)}
			rec := sendSeed100RestoreRecord("parent", "child")
			if _, err := s.restoreTerminalDelegateChild(rec, "child", &delegateRestorePreflight{}); err == nil || err.Error() != "state directory is not configured" {
				t.Fatalf("error = %v", err)
			}
		case 7:
			s := &Session{stateDir: t.TempDir(), subagents: newSubagentManager(nil, 0)}
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
		case 10:
			s := &Session{}
			s.cfg.spawn.parentSteer = func(string, *provenance.Causal) {}
			delegateSendTestHooks.afterClassify = func(got *Session) { got.cfg.spawn.parentSteer = nil }
			t.Cleanup(func() { delegateSendTestHooks.afterClassify = nil })
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: runtimeMessageAliasCaller, Message: "seed"})
			if res.Err != nil || !res.Delivered {
				t.Fatalf("local caller steering = %+v, want delivered", res)
			}
		case 11:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			delegateSendTestHooks.findJob = func(*jobManager, string) (*jobstore.JobRecord, error) {
				return nil, errors.New("seed lookup")
			}
			t.Cleanup(func() { delegateSendTestHooks.findJob = nil })
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed"})
			requireSendSeed100Error(t, res, "target_not_found: seed lookup")
		case 12:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			events := loadJobStoreEvents(t, s.jobManager)
			events = events[:len(events)-1]
			rewriteJobStoreEvents(t, s.jobManager, events)
			s.subagents.track(&subagent{id: rec.DelegateRestore.ChildSessionID, sess: &Session{}})
			delegateSendTestHooks.finalize = func(*Session, string, string, *subagent) error { return errors.New("seed finalize") }
			t.Cleanup(func() { delegateSendTestHooks.finalize = nil })
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed"})
			requireSendSeed100Error(t, res, "target_not_resumable: finalize observed-terminal")
		case 13:
			s := newDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			preflight := s.assessDelegateResumability(rec, delegateResumabilityPreflight).Preflight
			old := delegateRestoreSession
			delegateRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, RestoreSessionConfig) (*Session, error) {
				return nil, errors.New("restore fault")
			}
			t.Cleanup(func() { delegateRestoreSession = old })
			if _, err := s.restoreTerminalDelegateChild(rec, rec.DelegateRestore.ChildSessionID, preflight); err == nil {
				t.Fatal("restore session fault was ignored")
			}
		case 14:
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			events := loadJobStoreEvents(t, s.jobManager)
			events = events[:len(events)-1]
			rewriteJobStoreEvents(t, s.jobManager, events)
			s.subagents.track(&subagent{id: rec.DelegateRestore.ChildSessionID, sess: &Session{}})
			calls := 0
			delegateSendTestHooks.findJob = func(jm *jobManager, id string) (*jobstore.JobRecord, error) {
				calls++
				if calls > 1 {
					return nil, errors.New("reload fault")
				}
				return findJobRecord(jm, id)
			}
			t.Cleanup(func() { delegateSendTestHooks.findJob = nil })
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed"})
			requireSendSeed100Error(t, res, "target_not_found: reload fault")
		case 15, 16:
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			s := newDelegateRestorePreflightSession(t, client)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			delegateSendTestHooks.beforePostState = func(sub *subagent) { sub.mu.Lock(); sub.running = true; sub.mu.Unlock() }
			delegateSendTestHooks.findRunning = func(*jobManager, string) (*jobstore.JobRecord, error) {
				if seed%21 == 15 {
					return nil, errors.New("running lookup fault")
				}
				return rec, nil
			}
			t.Cleanup(func() { delegateSendTestHooks.beforePostState = nil; delegateSendTestHooks.findRunning = nil })
			_ = s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed", OnIdle: "start"})
		case 17:
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			s := newDelegateRestorePreflightSession(t, client)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			delegateSendTestHooks.resume = func(*jobManager, string, string, *subagent, string, string, any, *jobstore.DelegateRestoreDescriptor, bool, *provenance.Causal) (*runningJob, <-chan error, *jobstore.JobRecord, error) {
				return nil, nil, rec, nil
			}
			t.Cleanup(func() { delegateSendTestHooks.resume = nil })
			_ = s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed", OnIdle: "start"})
		case 18:
			s := newDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			rec.Reason = "stopped"
			delegateSendTestHooks.findJob = func(*jobManager, string) (*jobstore.JobRecord, error) { return rec, nil }
			old := delegateRestoreSession
			delegateRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, RestoreSessionConfig) (*Session, error) {
				return nil, errors.New("seed restore fault")
			}
			t.Cleanup(func() {
				delegateSendTestHooks.findJob = nil
				delegateRestoreSession = old
			})
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: rec.DelegateID, Message: "seed", OnIdle: "start"})
			requireSendSeed100Error(t, res, "target_not_resumable: delegate session")
		case 19:
			base := NewOpenAIProfile("base")
			s := &Session{resolveProfile: func(string) (*provider.Profile, error) {
				return provider.WithProviderID(NewOpenAIProfile("restored"), "other"), nil
			}}
			got, err := s.resolveDelegateRestoreProfileRef(base, "other", "restored")
			if err != nil || got == nil || got.ID() != "other" {
				t.Fatalf("cross-provider restore profile = (%v, %v)", got, err)
			}
		case 20:
			rig := newWtDlgRepo(t, delegateTestClient(func(llm.Request) llm.Response {
				return communicateWithDefaultOutput("done")
			}))
			res := rig.s.createDelegate(nil, delegateArgs{Task: "lane", Isolation: "worktree", Background: true})
			if res.Err != nil {
				t.Fatalf("create isolated delegate: %v", res.Err)
			}
			desc := &jobstore.DelegateRestoreDescriptor{
				Isolation: "worktree", WorkingDir: rig.lanePath(t, res.DelegateID), LocalEnvPolicy: "default",
			}
			if _, err := rig.s.restoreDelegateChildEnvironment(desc, "dlg_other"); err == nil {
				t.Fatal("foreign delegate restored into locked worktree")
			}
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
