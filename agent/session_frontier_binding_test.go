package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The hook boundary observes actual construction and teardown. It does not
// replace any session, delegate controller, writer or lifecycle operation.
type frontierBindingRecorder struct {
	mu        sync.Mutex
	next      int
	current   map[string]int
	registers int
	releases  int
	samples   map[string]func() events.ExecutionFrontier
}

func (r *frontierBindingRecorder) hooks(id string) TranscriptPublicationHooks {
	var identity int
	return TranscriptPublicationHooks{
		RegisterFrontier: func(sample func() events.ExecutionFrontier) {
			_ = sample() // Registration must be outside Session.mu.
			r.mu.Lock()
			defer r.mu.Unlock()
			r.next++
			identity = r.next
			r.registers++
			if r.current == nil {
				r.current = make(map[string]int)
				r.samples = make(map[string]func() events.ExecutionFrontier)
			}
			r.current[id] = identity
			r.samples[id] = sample
		},
		ReleaseFrontier: func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.releases++
			if r.current[id] == identity {
				delete(r.current, id)
			}
		},
	}
}

func (r *frontierBindingRecorder) state(id string) (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current[id], r.registers, r.releases
}

func newFrontierBindingRoot(t *testing.T, recorder *frontierBindingRecorder, cfg testConfig) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("binding-output") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("binding-output") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("binding-output") },
	}})
	cfg.skipGitSnapshot, cfg.minimalSystemPrompt, cfg.noSyncJobStore = true, true, true
	root := newSession(t, withClient(client), withConfig(SessionConfig{StateDir: t.TempDir(), testOnly: cfg}))
	root.SetDescendantTranscriptPublicationFunc(recorder.hooks)
	return root
}

func spawnFrontierBindingChild(t *testing.T, root *Session, beforeFinish ...func(*subagent)) (*subagent, string) {
	t.Helper()
	result := root.createDelegate(tripwireCtx(t), delegateArgs{Task: "binding-input", DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	child := root.subagents.get(result.ChildSessionID)
	if child == nil {
		t.Fatal("real delegate has no resident child")
	}
	if len(beforeFinish) != 0 {
		beforeFinish[0](child)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	ctx := tripwireCtx(t)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return child, result.DelegateID
}

func TestTranscriptFrontierBindingRootClose(t *testing.T) {
	t.Parallel()
	r := &frontierBindingRecorder{}
	root := newFrontierBindingRoot(t, r, testConfig{disableDelegateIdleRelease: true})
	child, _ := spawnFrontierBindingChild(t, root)
	if current, registers, _ := r.state(child.sess.ID()); current == 0 || registers != 1 {
		t.Fatalf("child did not register at attach: current=%d registers=%d", current, registers)
	}
	root.Close()
	if current, _, releases := r.state(child.sess.ID()); current != 0 || releases == 0 {
		t.Fatalf("real root close retained child registration: current=%d releases=%d", current, releases)
	}
}

func TestTranscriptFrontierBindingIdleReleaseColdRestore(t *testing.T) {
	t.Parallel()
	r := &frontierBindingRecorder{}
	settled := make(chan *Session, 4)
	finalized := make(chan *subagent, 4)
	zero := time.Duration(0)
	allowFinish := make(chan struct{})
	var allowOnce sync.Once
	defer allowOnce.Do(func() { close(allowFinish) })
	root := newFrontierBindingRoot(t, r, testConfig{
		delegateIdleReleaseDelay: &zero,
		subagentAfterFinalStatePublish: func(child *subagent) {
			finalized <- child
			<-allowFinish
		},
		idleTeardownMemberSettled: func(s *Session) { settled <- s },
	})
	child, delegateID := spawnFrontierBindingChild(t, root, func(*subagent) { allowOnce.Do(func() { close(allowFinish) }) })
	if initial := <-finalized; initial != child {
		t.Fatal("initial finalization reported another child")
	}
	select {
	case old := <-settled:
		if old != child.sess {
			t.Fatal("settled wrong runtime")
		}
	case <-tripwireCtx(t).Done():
		t.Fatal("idle teardown did not settle")
	}
	if current, _, releases := r.state(child.sess.ID()); current != 0 || releases == 0 {
		t.Errorf("real idle reclamation retained registration: current=%d releases=%d", current, releases)
	}
	updateSessionTestConfig(root, func(cfg *testConfig) { cfg.disableDelegateIdleRelease = true })
	ctx := tripwireCtx(t)
	result := (delegateRuntime{owner: root}).send(ctx, delegateID, "cold-binding-input", 0).result
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	var restored *subagent
	select {
	case restored = <-finalized:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if restored == nil || restored == child || restored.sess == child.sess {
		t.Fatal("send did not cold-restore a replacement runtime")
	}
	restored.mu.Lock()
	done := restored.done
	restored.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if current, registers, _ := r.state(restored.sess.ID()); current == 0 || registers != 2 {
		t.Fatalf("cold replacement did not freshly register: current=%d registers=%d", current, registers)
	}
	// Releasing A again must present A's retained closure, never a lookup by ID.
	child.sess.releaseTranscriptFrontier()
	if current, _, _ := r.state(restored.sess.ID()); current == 0 {
		t.Fatal("old runtime release removed replacement")
	}
}

func TestTranscriptFrontierBindingFailedConstructors(t *testing.T) {
	t.Parallel()
	t.Run("fresh-after-attach", func(t *testing.T) {
		r := &frontierBindingRecorder{}
		client := llm.NewClient()
		client.Register(&fakeAdapter{name: "openai"})
		cfg := SessionConfig{StateDir: t.TempDir(), ContextStrategy: "invalid-frontier-strategy", testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}
		cfg.spawn.descendantPublication = r.hooks
		sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
		if err == nil || sess != nil {
			t.Fatalf("invalid strategy did not fail constructor: session=%p error=%v", sess, err)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.registers != 1 {
			t.Fatalf("failed before registration, registers=%d", r.registers)
		}
		if len(r.current) != 0 || r.releases != 1 {
			t.Fatalf("nil fresh return retained registration: entries=%d releases=%d", len(r.current), r.releases)
		}
	})
	t.Run("restored-after-recovery", func(t *testing.T) {
		dir := t.TempDir()
		original := newQueuePersistTestSession(t, dir)
		start, err := original.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "frontier-recovery", Input: []appwire.InputItem{{Type: "text", Text: "frontier-recovery-input"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := original.claimClientMutationStart(); err != nil || !ok {
			t.Fatalf("claim=%t error=%v", ok, err)
		}
		if err := original.beginClientMutationFailure("frontier-recovery", errors.New("frontier-fault")); err != nil {
			t.Fatal(err)
		}
		original.Close()
		meta, err := schema.LoadSessionMeta(dir, original.ID())
		if err != nil {
			t.Fatal(err)
		}
		meta.Config.ContextStrategy = "invalid-frontier-strategy"
		r := &frontierBindingRecorder{}
		recorded := make(chan events.ExecutionFrontier, 1)
		earlyPublication := make(chan events.SessionEvent, 1)
		resume, callbackCurrent := make(chan struct{}), make(chan int, 1)
		var resumeOnce sync.Once
		defer resumeOnce.Do(func() { close(resume) })
		var sampled events.ExecutionFrontier
		client := llm.NewClient()
		client.Register(&fakeAdapter{name: "openai"})
		cfg := RestoreSessionConfig{StateDir: dir, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}
		cfg.spawn.parentSessionID = "frontier-owner"
		cfg.spawn.descendantPublication = r.hooks
		cfg.spawn.descendantEvent = func(ev events.SessionEvent) {
			if ev.Kind == events.EventExecutionFirstRecorded {
				earlyPublication <- ev
			}
		}
		cfg.OnRestoredTranscript = func(_ transcript.Header, entries []transcript.Entry, _ bool) {
			foundFailure := false
			for _, entry := range entries {
				foundFailure = foundFailure || entry.Turn.Kind == schema.TurnFailure && entry.Turn.TurnID == start.Turn.ID
			}
			if !foundFailure {
				t.Fatal("constructor callback ran before failure recovery recorded its entry")
			}
			r.mu.Lock()
			sample := r.samples[original.ID()]
			r.mu.Unlock()
			go func() {
				recorded <- sample()
				<-resume
				current, _, _ := r.state(original.ID())
				callbackCurrent <- current
			}()
			select {
			case sampled = <-recorded:
			case <-tripwireCtx(t).Done():
				t.Fatal("constructor recovery callback did not sample")
			}
		}
		sess, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, cfg)
		if err == nil || sess != nil {
			t.Fatalf("invalid strategy did not fail restored constructor: session=%p error=%v", sess, err)
		}
		if sampled.TurnID != start.Turn.ID || sampled.Sequence == 0 {
			t.Fatalf("recovery committed frontier has wrong execution: %+v", sampled)
		}
		select {
		case ev := <-earlyPublication:
			t.Errorf("failed constructor published a held first-record signal: %+v", ev)
		default:
		}
		r.mu.Lock()
		entries, registers, releases := len(r.current), r.registers, r.releases
		r.mu.Unlock()
		if registers != 1 {
			t.Fatalf("restore failed before attach, registers=%d", registers)
		}
		if entries != 0 || releases != 1 {
			t.Fatalf("post-recovery nil return retained registration: entries=%d releases=%d", entries, releases)
		}
		resumeOnce.Do(func() { close(resume) })
		select {
		case current := <-callbackCurrent:
			if current != 0 {
				t.Fatalf("post-nil sampled callback still matched registration=%d", current)
			}
		case <-tripwireCtx(t).Done():
			t.Fatal("post-nil callback did not resume")
		}
	})
}

func TestTranscriptFrontierBindingNilHooks(t *testing.T) {
	t.Parallel()
	root := newFrontierBindingRoot(t, &frontierBindingRecorder{}, testConfig{disableDelegateIdleRelease: true})
	var mu sync.Mutex
	calls := 0
	root.SetDescendantTranscriptPublicationFunc(func(string) TranscriptPublicationHooks {
		return TranscriptPublicationHooks{ReleaseFrontier: func() { mu.Lock(); calls++; mu.Unlock() }}
	})
	child, _ := spawnFrontierBindingChild(t, root)
	teardownChildSession(context.Background(), child.sess)
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("nil register triggered release, calls=%d", calls)
	}
}

func TestTranscriptFrontierBindingWorktreeDispose(t *testing.T) {
	t.Parallel()
	// Real git is required: disposal must actually destroy the isolated lane.
	cfg := worktreeTestSessionConfig()
	cfg.testOnly.disableDelegateIdleRelease = true
	cfg.testOnly.minimalWorktreeToolRegistry = false
	repo := newWorktreeRepoWithConfig(t, cfg)
	writer, err := repo.s.createTranscript("frontier-dispose-fixture", repo.s.transcriptHeader(repo.s.createdAt))
	if err != nil {
		t.Fatal(err)
	}
	repo.s.attachTranscript(writer, nil)
	repo.s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("dispose-result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("dispose-result") },
	}})
	r := &frontierBindingRecorder{}
	repo.s.SetDescendantTranscriptPublicationFunc(r.hooks)
	sampled, resume, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	var callbackOnce sync.Once
	defer once.Do(func() { close(resume) })
	repo.s.SetDescendantEventFunc(func(ev events.SessionEvent) {
		if ev.Kind != events.EventExecutionFirstRecorded {
			return
		}
		r.mu.Lock()
		sample := r.samples[ev.SessionID]
		r.mu.Unlock()
		if sample == nil {
			return
		}
		callbackOnce.Do(func() {
			// The receiver owns this delayed callback, not the emitter's close
			// join. The real writer, emitter and lifecycle remain unchanged.
			go func() {
				_ = sample()
				close(sampled)
				<-resume
				close(returned)
			}()
		})
	})
	ctx := tripwireCtx(t)
	created := repo.s.createDelegate(ctx, delegateArgs{Task: "frontier-dispose-input", Isolation: "worktree", DelegationAllowance: new(0)})
	if created.Err != nil {
		t.Fatal(created.Err)
	}
	child := repo.s.subagents.get(created.ChildSessionID)
	if child == nil {
		t.Fatal("isolated delegate has no runtime")
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-sampled:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := repo.s.DrainJobTree(ctx); err != nil {
		t.Fatal(err)
	}
	lane := child.sess.currentEnv().WorkingDirectory()
	dirty := filepath.Join(lane, "frontier-dirty")
	if err := os.WriteFile(dirty, []byte("frontier-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.s.worktreeDispose(ctx, created.DelegateID, false, false); err == nil {
		t.Fatal("dirty lane was disposed")
	}
	if current, _, releases := r.state(created.ChildSessionID); current == 0 || releases != 0 {
		t.Fatalf("refused dispose released current runtime: current=%d releases=%d", current, releases)
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.s.worktreeDispose(ctx, created.DelegateID, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lane); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful dispose left lane, error=%v", err)
	}
	if current, _, releases := r.state(created.ChildSessionID); current != 0 || releases == 0 {
		t.Errorf("actual disposal retained sampled runtime registration: current=%d releases=%d", current, releases)
	}
	once.Do(func() { close(resume) })
	select {
	case <-returned:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestTranscriptFrontierBindingReleaseOrders(t *testing.T) {
	t.Parallel()
	for _, unhookFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("unhook-first-%t", unhookFirst), func(t *testing.T) {
			r := &frontierBindingRecorder{}
			root := newFrontierBindingRoot(t, r, testConfig{disableDelegateIdleRelease: true})
			child, _ := spawnFrontierBindingChild(t, root)
			entry := delegateRuntimeReclamationEntry{runtime: child.sess, ownerRuntime: root, childSessionID: child.sess.ID()}
			unhook := func() { unhookReclaimedRuntimeRecord(entry) }
			teardown := func() { teardownChildSession(context.Background(), child.sess) }
			if unhookFirst {
				unhook()
				teardown()
			} else {
				teardown()
				unhook()
			}
			if current, _, releases := r.state(child.sess.ID()); current != 0 || releases != 2 {
				t.Fatalf("double release: current=%d calls=%d", current, releases)
			}
		})
	}
}

func TestTranscriptFrontierBindingPostSuccessDiscards(t *testing.T) {
	t.Parallel()
	for _, restoredCandidate := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore-funnel-%t", restoredCandidate), func(t *testing.T) {
			r := &frontierBindingRecorder{}
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			cfg := SessionConfig{StateDir: t.TempDir(), testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}}
			cfg.spawn.descendantPublication = r.hooks
			sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sess.Close)
			if current, registers, _ := r.state(sess.ID()); current == 0 || registers != 1 {
				t.Fatalf("successful constructor returned without registration: current=%d registers=%d", current, registers)
			}
			if restoredCandidate {
				sess.discardRestoredCandidate()
			} else {
				disposeUnadoptedSubagentSession(sess)
			}
			if current, _, releases := r.state(sess.ID()); current != 0 || releases == 0 {
				t.Fatalf("post-success discard retained binding: current=%d releases=%d", current, releases)
			}
		})
	}
}
