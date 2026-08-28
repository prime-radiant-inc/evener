package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// lifecycleTestSession builds a session matching the lifecycle harness config
// (fake clock, deny env, per-child factory), returning it plus the fake clock and
// an events-drain join. It is the shared rig for the C1/C2 op tests below.
func lifecycleTestSession(t *testing.T, parentResponder func(llm.Request) llm.Response, factory func() *llm.Client) (*Session, *agenttest.FakeClock) {
	t.Helper()
	clk := agenttest.NewFakeClock()
	env := &agenttest.DenyEnv{WorkDir: lifecycleWorkDir}

	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: parentResponder})
	cfg := SessionConfig{
		clock:                 clk,
		MaxSubagentDepth:      1,
		MaxToolRoundsPerInput: 10,
		LLMSleep:              func(_ context.Context, d time.Duration) error { clk.Sleep(d); return nil },
	}
	cfg.testOnly.childClientFactory = factory

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	t.Cleanup(func() {
		sess.Close()
		<-drainDone
	})
	return sess, clk
}

// TestRestoreSessionConfigUsesInjectedClock keeps the restore path on the same
// deterministic clock boundary as NewSession. Stateful fuzzers advance this
// clock to settle job and watch work without relying on wall time.
func TestRestoreSessionConfigUsesInjectedClock(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider:  "openai",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
	})

	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		&agenttest.DenyEnv{WorkDir: t.TempDir()},
		schema.SessionMeta{ID: ulid.Make().String(), ProfileID: "openai", Model: "gpt-5.2", CreatedAt: clk.Now()},
		RestoreSessionConfig{
			StateDir: t.TempDir(),
			clock:    clk,
			testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer sess.Close()

	if sess.clock != clk {
		t.Fatalf("restored session clock = %T, want injected fake clock", sess.clock)
	}
	if sess.jobManager.clock != clk {
		t.Fatalf("restored job manager clock = %T, want injected fake clock", sess.jobManager.clock)
	}
}

func TestRestoreSessionLifetimeOwnsRootAndChild(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider:  "openai",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
	})
	meta := schema.SessionMeta{
		ID:        ulid.Make().String(),
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		&agenttest.DenyEnv{WorkDir: t.TempDir()},
		meta,
		RestoreSessionConfig{
			LifetimeContext: owner,
			StateDir:        t.TempDir(),
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(restored.Close)

	prepared := prepareSubagentForTreeRevisionTest(t, restored, "child task")
	t.Cleanup(prepared.runCancel)
	t.Cleanup(func() { releasePreparedTreeSlot(prepared) })
	t.Cleanup(prepared.sub.sess.Close)

	for name, done := range map[string]<-chan struct{}{
		"root":      restored.sessionCtx.Done(),
		"child":     prepared.sub.sess.sessionCtx.Done(),
		"child run": prepared.runCtx.Done(),
	} {
		select {
		case <-done:
			t.Fatalf("%s context ended before owner cancellation", name)
		default:
		}
	}

	cancelOwner()
	for name, done := range map[string]<-chan struct{}{
		"root":      restored.sessionCtx.Done(),
		"child":     prepared.sub.sess.sessionCtx.Done(),
		"child run": prepared.runCtx.Done(),
	} {
		select {
		case <-done:
		default:
			t.Fatalf("%s context outlived restored-session owner", name)
		}
	}
}

// TestLifecycleBackgroundShellQuiesces proves the C2 path: a turn that issues a
// background shell tool call actually starts a background job, and quiesceJobs
// drives it to a terminal status deterministically.
func TestLifecycleBackgroundShellQuiesces(t *testing.T) {
	var round atomic.Int64
	responder := func(llm.Request) llm.Response {
		if round.Add(1) == 1 {
			return buildResponse(kindShellBackground, 0)
		}
		return agenttest.FinalResponse("done")
	}
	sess, clk := lifecycleTestSession(t, responder, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run a background job", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	quiesceJobs(sess, clk)

	if running := sess.jobManager.runningJobIDs(); len(running) != 0 {
		t.Fatalf("after quiesce, %d jobs still running, want 0", len(running))
	}
	jobs := sess.jobManager.list(listFilter{})
	var shellJobs int
	for _, rec := range jobs {
		if rec.Type == jobstore.JobShell {
			shellJobs++
			if !rec.Status.IsTerminal() {
				t.Fatalf("background shell job %s status = %s, want terminal", rec.JobID, rec.Status)
			}
		}
	}
	if shellJobs != 1 {
		t.Fatalf("found %d shell jobs, want exactly 1 (background job must actually start)", shellJobs)
	}
}
