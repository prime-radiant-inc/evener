package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// TestDelegateIdleRelease_ColdRestoreReopensItsScratch: a delegate's scratch is
// named in its root's tree, so the idle release keeps it and each cold restore
// of the delegate reopens that same directory with its files intact rather than
// minting a fresh one. Two release/restore cycles also pin that the first
// restored runtime's release leaves nothing that keeps the second from it.
func TestDelegateIdleRelease_ColdRestoreReopensItsScratch(t *testing.T) {
	// Not parallel: the sandboxed children mint their scratch under TMPDIR.
	t.Setenv("TMPDIR", t.TempDir())
	_, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done one") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done two") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done three") },
	}})
	// Teardown completes after the resident pointer is cleared. Key its
	// completion by runtime, not session ID, which both restores reuse.
	var teardownMu sync.Mutex
	teardownDone := make(map[*Session]chan struct{})
	teardownDoneFor := func(runtime *Session) chan struct{} {
		teardownMu.Lock()
		defer teardownMu.Unlock()
		if teardownDone[runtime] == nil {
			teardownDone[runtime] = make(chan struct{})
		}
		return teardownDone[runtime]
	}
	shortGrace := 100 * time.Millisecond
	s := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "scratch-continuity-*"),
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot:          true,
			minimalSystemPrompt:      true,
			noSyncJobStore:           true,
			sandboxProber:            sandbox.FakeProber{Facts: facts},
			delegateIdleReleaseDelay: &shortGrace,
			idleTeardownMemberSettled: func(runtime *Session) {
				done := teardownDoneFor(runtime)
				teardownMu.Lock()
				defer teardownMu.Unlock()
				select {
				case <-done: // a second settle of the same runtime
				default:
					close(done)
				}
			},
		},
	}))
	defer s.Close()
	tree := s.delegateController

	// TRIPWIRE: scripted adapters plus an in-process sandboxed child; the runs
	// and releases normally settle in well under a second each. 30s per stage
	// only bounds a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := s.createDelegate(ctx, delegateArgs{Task: "own scratch", Sandbox: "workspace-write", DelegationAllowance: new(0)})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	child := s.subagents.get(res.ChildSessionID)
	if child == nil {
		t.Fatalf("subagent %s not found", res.ChildSessionID)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("delegate run did not finish: %v", ctx.Err())
	}

	scratchDir := childScratchDir(t, child.sess)
	if want := filepath.Join("evener-scratch-"+s.ID(), res.ChildSessionID); !filepath.IsAbs(scratchDir) || filepath.Join(filepath.Base(filepath.Dir(scratchDir)), filepath.Base(scratchDir)) != want {
		t.Fatalf("delegate scratch = %q, want it at %s in the root's tree", scratchDir, want)
	}
	artifact := filepath.Join(scratchDir, "artifact.txt")
	if err := os.WriteFile(artifact, []byte("durable"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	restoreAndCheck := func(label string, prev *subagent) *subagent {
		t.Helper()
		// TRIPWIRE: the 100ms grace normally fires within moments of the run
		// finishing; 15s only bounds a genuine hang.
		waitForCondition(t, 15*time.Second, "idle release of "+label, func() bool {
			tree.mu.Lock()
			released := tree.live[res.DelegateID] == nil || tree.live[res.DelegateID].runtime == nil
			tree.mu.Unlock()
			return released
		})
		// Pointer removal admits a racing cold restore while the old runtime
		// still holds its lease; that restore would fall back to a fresh
		// scratch. Wait for that exact runtime's teardown first.
		select {
		case <-teardownDoneFor(prev.sess):
		// TRIPWIRE: teardown normally settles in milliseconds; 15s only bounds a genuine hang.
		case <-time.After(15 * time.Second):
			t.Fatalf("idle teardown of %s did not finish", label)
		}
		// TRIPWIRE: the scripted send and restore complete in well under a
		// second; 30s only bounds a genuine hang.
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer sendCancel()
		send := (delegateRuntime{owner: s}).send(sendCtx, res.DelegateID, "run "+label, 0).result
		if send.Err != nil {
			t.Fatalf("delegate_send after idle release (%s): %+v", label, send)
		}
		var restored *subagent
		// TRIPWIRE: the cold restore normally appears within milliseconds of
		// the send; 15s only bounds a genuine hang.
		waitForCondition(t, 15*time.Second, "cold-restored record for "+label, func() bool {
			restored = s.subagents.get(res.ChildSessionID)
			return restored != nil && restored != prev && restored.sess != nil
		})
		restored.mu.Lock()
		rdone := restored.done
		restored.mu.Unlock()
		select {
		case <-rdone:
		case <-sendCtx.Done():
			t.Fatalf("restored run (%s) did not finish: %v", label, sendCtx.Err())
		}
		if got := childScratchDir(t, restored.sess); filepath.Clean(got) != filepath.Clean(scratchDir) {
			t.Fatalf("restored delegate (%s) got scratch %q, want its own %q", label, got, scratchDir)
		}
		if data, err := os.ReadFile(artifact); err != nil || string(data) != "durable" {
			t.Fatalf("restored delegate (%s) lost its scratch file: read err = %v", label, err)
		}
		return restored
	}
	child = restoreAndCheck("first restore", child)
	_ = restoreAndCheck("second restore", child)
}

// childScratchDir is the scratch a delegate's own environment owns.
func childScratchDir(t *testing.T, sess *Session) string {
	t.Helper()
	local, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("child env is not a LocalExecutionEnvironment")
	}
	dir := local.SessionScratchDir()
	if dir == "" {
		t.Fatal("sandboxed child owns no scratch")
	}
	return dir
}
