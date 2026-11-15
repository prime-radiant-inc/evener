package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/llm"
)

type descendantRuntimeSample struct{ childID, turnID string }

type descendantRuntimeGate struct {
	beforeRoute bool
	used        atomic.Bool
	sampled     chan descendantRuntimeSample
	resume      chan struct{}
	once        sync.Once
}

func (g *descendantRuntimeGate) release() { g.once.Do(func() { close(g.resume) }) }

// This fixture scripts only the LLM boundary. Child creation, retained-runtime
// reclamation, cold sends, writer hooks and the descendant consumer are real.
type descendantRuntimeFixture struct {
	root      *agent.Session
	srv       *Server
	provider  *parityProvider
	ctx       context.Context
	idle      chan events.DelegateUpdatedData
	recorded  chan events.SessionEvent
	mu        sync.Mutex
	bindings  map[string][]agent.TranscriptPublicationHooks
	samples   map[string][]*atomic.Int64
	gate      *descendantRuntimeGate
	calls     atomic.Int64
	receivers sync.WaitGroup
}

func newDescendantRuntimeFixture(t *testing.T, generation bool, gitWorkspace ...string) *descendantRuntimeFixture {
	t.Helper()
	p := &parityProvider{childRelease: make(chan struct{})}
	close(p.childRelease)
	client := llm.NewClient()
	client.Register(p)
	workspace, stateDir := t.TempDir(), t.TempDir()
	if len(gitWorkspace) != 0 {
		workspace = gitWorkspace[0]
	}
	root, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workspace), agent.SessionConfig{StateDir: stateDir, MaxRetainedTerminal: 1})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	wireTranscriptHistory(t, root, srv)
	srv.SetDescendantTranscriptPathFunc(func(id string) string { return filepath.Join(stateDir, "sessions", id+".transcript.jsonl") })
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second) // Hang tripwire, not a scheduling budget.
	t.Cleanup(cancel)
	f := &descendantRuntimeFixture{root: root, srv: srv, provider: p, ctx: ctx, idle: make(chan events.DelegateUpdatedData, 32), recorded: make(chan events.SessionEvent, 32), bindings: make(map[string][]agent.TranscriptPublicationHooks), samples: make(map[string][]*atomic.Int64)}
	root.SetDescendantTranscriptPublicationFunc(func(id string) agent.TranscriptPublicationHooks {
		hooks, _ := srv.transcriptPublicationHooks(id)
		frontier := srv.descendantFrontierHooks(root.ID(), id)
		samples := &atomic.Int64{}
		register := frontier.RegisterFrontier
		frontier.RegisterFrontier = func(sample func() events.ExecutionFrontier) {
			register(func() events.ExecutionFrontier {
				samples.Add(1)
				stamp := sample()
				f.mu.Lock()
				gate := f.gate
				f.mu.Unlock()
				if gate != nil && !gate.beforeRoute && gate.used.CompareAndSwap(false, true) {
					gate.sampled <- descendantRuntimeSample{id, stamp.TurnID}
					<-gate.resume
				}
				return stamp
			})
		}
		hooks.RegisterFrontier, hooks.PublishFrontier, hooks.ReleaseFrontier = frontier.RegisterFrontier, frontier.PublishFrontier, frontier.ReleaseFrontier
		if !generation {
			hooks.PublishFrontier = nil
		}
		f.mu.Lock()
		f.bindings[id] = append(f.bindings[id], hooks)
		f.samples[id] = append(f.samples[id], samples)
		f.mu.Unlock()
		return hooks
	})
	route := func(ev events.SessionEvent) {
		f.mu.Lock()
		gate := f.gate
		f.mu.Unlock()
		if gate != nil && gate.beforeRoute && ev.Kind == events.EventExecutionFirstRecorded && gate.used.CompareAndSwap(false, true) {
			gate.sampled <- descendantRuntimeSample{ev.SessionID, ev.Data.(events.ExecutionFirstRecordedData).TurnID}
			<-gate.resume
		}
		srv.RecordDescendantAppEvent(root.ID(), ev)
		if ev.Kind == events.EventExecutionFirstRecorded {
			f.recorded <- ev
		}
	}
	root.SetDescendantEventFunc(func(ev events.SessionEvent) {
		if ev.Kind != events.EventExecutionFirstRecorded {
			route(ev)
			return
		}
		// Delay the actual receiver, not the managed emitter's close join.
		// No production session or Server operation is substituted here.
		f.receivers.Go(func() { route(ev) })
	})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range root.Events() {
			if data, ok := ev.Data.(events.DelegateUpdatedData); ok && data.Lifecycle == "idle" && data.Outcome == "completed" {
				f.idle <- data
			}
		}
	}()
	t.Cleanup(func() { root.Close(); f.receivers.Wait(); <-drained })
	return f
}

func (f *descendantRuntimeFixture) tool(t *testing.T, name string, args map[string]any) {
	t.Helper()
	id := fmt.Sprintf("runtime-%s-%d", name, f.calls.Add(1))
	f.provider.script(step(parityResponse(parityCall(id, name, args))), step(parityCommunicate(id+"-done", "root-result", true)))
	if _, err := f.root.ProcessInput(f.ctx, "runtime-root-input", nil); err != nil {
		t.Fatal(err)
	}
}

func (f *descendantRuntimeFixture) awaitIdle(t *testing.T, childID string) events.DelegateUpdatedData {
	t.Helper()
	for {
		select {
		case data := <-f.idle:
			if childID == "" || data.ChildSessionID == childID {
				return data
			}
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
			return events.DelegateUpdatedData{}
		}
	}
}

func (f *descendantRuntimeFixture) awaitRecorded(t *testing.T, childID, turnID string) {
	t.Helper()
	for {
		select {
		case ev := <-f.recorded:
			if ev.SessionID == childID && (turnID == "" || ev.Data.(events.ExecutionFirstRecordedData).TurnID == turnID) {
				return
			}
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
			return
		}
	}
}

func (f *descendantRuntimeFixture) assertDurableGeneration(t *testing.T, childID string) {
	t.Helper()
	data, err := os.ReadFile(schema.BootGenerationPath(f.root.StateDir(), childID))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(data))
	f.srv.mu.RLock()
	entry := f.srv.appDescendantFrontiers[childID]
	f.srv.mu.RUnlock()
	if want == "" || entry == nil || entry.bootGeneration != want {
		t.Fatalf("registry generation=%+v durable generation=%q", entry, want)
	}
}

func TestDescendantFrontierRuntimeReplacement(t *testing.T) {
	t.Parallel()
	for _, generation := range []bool{false, true} {
		for _, beforeRoute := range []bool{false, true} {
			t.Run(fmt.Sprintf("generation-%t/before-route-%t", generation, beforeRoute), func(t *testing.T) {
				f := newDescendantRuntimeFixture(t, generation)
				gate := &descendantRuntimeGate{beforeRoute: beforeRoute, sampled: make(chan descendantRuntimeSample, 1), resume: make(chan struct{})}
				f.mu.Lock()
				f.gate = gate
				f.mu.Unlock()
				t.Cleanup(gate.release)
				f.tool(t, "delegate", map[string]any{"prompt": parityChildTask})
				first := f.awaitIdle(t, "")
				var held descendantRuntimeSample
				select {
				case held = <-gate.sampled:
				case <-f.ctx.Done():
					t.Fatal(f.ctx.Err())
				}
				if held.childID != first.ChildSessionID || held.turnID == "" {
					t.Fatalf("held wrong real child callback: held=%+v child=%+v", held, first)
				}
				if _, err := f.root.DrainJobTree(f.ctx); err != nil {
					t.Fatal(err)
				}
				f.srv.mu.RLock()
				old := f.srv.appDescendantFrontiers[first.ChildSessionID]
				f.srv.mu.RUnlock()
				if old == nil {
					t.Fatal("real child did not register")
				}
				if generation {
					f.assertDurableGeneration(t, first.ChildSessionID)
				}
				// Capacity pressure reclaims the actual resident child, not a hand-
				// built registry entry. Its next public send must cold-restore it.
				f.tool(t, "delegate", map[string]any{"prompt": parityChildTask})
				f.awaitIdle(t, "")
				if _, err := f.root.DrainJobTree(f.ctx); err != nil {
					t.Fatal(err)
				}
				f.srv.mu.RLock()
				released := f.srv.appDescendantFrontiers[first.ChildSessionID] == nil
				f.srv.mu.RUnlock()
				if !released {
					t.Fatal("real reclamation retained A registration")
				}
				f.tool(t, "delegate_send", map[string]any{"to": first.DelegateID, "message": "replacement-input", "max_wait_ms": 10000})
				f.awaitIdle(t, first.ChildSessionID)
				f.awaitRecorded(t, first.ChildSessionID, "")
				f.srv.mu.RLock()
				replacement := f.srv.appDescendantFrontiers[first.ChildSessionID]
				f.srv.mu.RUnlock()
				if replacement == nil || replacement.registration == old.registration {
					t.Fatal("public cold send did not register a replacement runtime")
				}
				if generation && (old.bootGeneration == "" || replacement.bootGeneration == "" || old.bootGeneration == replacement.bootGeneration) {
					t.Fatal("generation-present fixture did not publish distinct corroboration")
				}
				if generation {
					f.assertDurableGeneration(t, first.ChildSessionID)
				}
				metadata, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: first.ChildSessionID})
				if err != nil {
					t.Fatal(err)
				}
				history, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: first.ChildSessionID, IncludeTurns: true})
				if err != nil {
					t.Fatal(err)
				}
				want := history.LatestExecutionTurnID
				if want == "" || want == held.turnID || metadata.Thread.Evener.LatestExecutionTurnID != want {
					h := f.srv.appHistoryForID(first.ChildSessionID)
					producer, _ := f.srv.appHistories.publications.Load(first.ChildSessionID)
					t.Fatalf("replacement metadata=%q history=%q old=%q historyProducerCurrent=%t", metadata.Thread.Evener.LatestExecutionTurnID, want, held.turnID, h != nil && h.historyPublication == producer)
				}
				f.mu.Lock()
				oldHooks := f.bindings[first.ChildSessionID][0]
				samples := f.samples[first.ChildSessionID]
				replacementSamples := samples[len(samples)-1]
				f.mu.Unlock()
				if oldHooks.PublishFrontier != nil {
					oldHooks.PublishFrontier("stale-fixture-generation")
				}
				oldHooks.ReleaseFrontier()
				f.srv.mu.RLock()
				preserved := f.srv.appDescendantFrontiers[first.ChildSessionID] == replacement
				f.srv.mu.RUnlock()
				if !preserved {
					t.Fatal("A publish/release changed B registration")
				}
				beforeSamples := replacementSamples.Load()
				gate.release()
				f.awaitRecorded(t, first.ChildSessionID, held.turnID)
				after, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: first.ChildSessionID})
				if err != nil {
					t.Fatal(err)
				}
				if after.Thread.Evener.LatestExecutionTurnID != want {
					t.Fatalf("old callback regressed replacement frontier: got=%q want=%q", after.Thread.Evener.LatestExecutionTurnID, want)
				}
				resamples := replacementSamples.Load() - beforeSamples
				if beforeRoute && resamples != 1 {
					t.Fatalf("before-route old callback did not sample B exactly once: samples=%d", resamples)
				}
				t.Logf("real cold replacement, generation=%t beforeRoute=%t identities=%d/%d old=%q current=%q replacementResamples=%d", generation, beforeRoute, old.registration, replacement.registration, held.turnID, want, resamples)
			})
		}
	}
}

func TestDescendantFrontierRuntimeWorktreeDispose(t *testing.T) {
	t.Parallel()
	// The lane must really disappear from git, not only from a scripted model.
	workspace := t.TempDir()
	gittest.Run(t, workspace, "init", "-q")
	gittest.Run(t, workspace, "commit", "-q", "--allow-empty", "-m", "frontier-fixture")
	f := newDescendantRuntimeFixture(t, false, workspace)
	gate := &descendantRuntimeGate{sampled: make(chan descendantRuntimeSample, 1), resume: make(chan struct{})}
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	t.Cleanup(gate.release)
	f.tool(t, "delegate", map[string]any{"prompt": parityChildTask, "isolation": "worktree"})
	child := f.awaitIdle(t, "")
	var held descendantRuntimeSample
	select {
	case held = <-gate.sampled:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if held.childID != child.ChildSessionID {
		t.Fatal("held callback belongs to another child")
	}
	if _, err := f.root.DrainJobTree(f.ctx); err != nil {
		t.Fatal(err)
	}
	metadata, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: child.ChildSessionID})
	if err != nil {
		t.Fatal(err)
	}
	lane, before := metadata.Thread.CWD, metadata.Thread.Evener.LatestExecutionTurnID
	if lane == "" || lane == workspace {
		t.Fatalf("delegate has no isolated lane: %q", lane)
	}
	dirty := filepath.Join(lane, "frontier-dirty")
	if err := os.WriteFile(dirty, []byte("fixture-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.tool(t, "manage_worktree", map[string]any{"operation": "dispose", "id": child.DelegateID})
	f.srv.mu.RLock()
	stillRegistered := f.srv.appDescendantFrontiers[child.ChildSessionID] != nil
	f.srv.mu.RUnlock()
	if !stillRegistered {
		t.Fatal("refused dirty disposal released the child")
	}
	if _, err := os.Stat(lane); err != nil {
		t.Fatal("dirty refusal removed lane")
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	f.tool(t, "manage_worktree", map[string]any{"operation": "dispose", "id": child.DelegateID})
	if _, err := os.Stat(lane); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("public dispose left actual lane: %v", err)
	}
	f.srv.mu.RLock()
	remaining := f.srv.appDescendantFrontiers[child.ChildSessionID] != nil
	f.srv.mu.RUnlock()
	if remaining {
		t.Error("actual worktree disposal retained sampled runtime registration")
	}
	gate.release()
	f.awaitRecorded(t, child.ChildSessionID, held.turnID)
	after, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: child.ChildSessionID})
	if err != nil {
		t.Fatal(err)
	}
	if after.Thread.Evener.LatestExecutionTurnID != before {
		t.Fatalf("disposed runtime callback installed frontier=%q, previous=%q", after.Thread.Evener.LatestExecutionTurnID, before)
	}
}

// A late callback resolves the replacement but samples before its next execution.
// Its held sample must not displace that same registration's newer install.
func TestDescendantFrontierSameRegistrationStaleInstall(t *testing.T) {
	for _, generation := range []bool{false, true} {
		t.Run(fmt.Sprintf("generation-%t", generation), func(t *testing.T) {
			f := newDescendantRuntimeFixture(t, generation)
			oldRoute := &descendantRuntimeGate{beforeRoute: true, sampled: make(chan descendantRuntimeSample, 1), resume: make(chan struct{})}
			f.mu.Lock()
			f.gate = oldRoute
			f.mu.Unlock()
			t.Cleanup(oldRoute.release)
			f.tool(t, "delegate", map[string]any{"prompt": parityChildTask})
			first := f.awaitIdle(t, "")
			var retired descendantRuntimeSample
			select {
			case retired = <-oldRoute.sampled:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			if retired.childID != first.ChildSessionID {
				t.Fatal("wrong old callback")
			}
			if _, err := f.root.DrainJobTree(f.ctx); err != nil {
				t.Fatal(err)
			}
			f.tool(t, "delegate", map[string]any{"prompt": parityChildTask})
			f.awaitIdle(t, "")
			if _, err := f.root.DrainJobTree(f.ctx); err != nil {
				t.Fatal(err)
			}
			f.srv.mu.RLock()
			absent := f.srv.appDescendantFrontiers[first.ChildSessionID] == nil
			f.srv.mu.RUnlock()
			if !absent {
				t.Fatal("A was not reclaimed")
			}
			f.tool(t, "delegate_send", map[string]any{"to": first.DelegateID, "message": "replacement-first-input", "max_wait_ms": 10000})
			f.awaitIdle(t, first.ChildSessionID)
			f.awaitRecorded(t, first.ChildSessionID, "")
			initial, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: first.ChildSessionID, IncludeTurns: true})
			if err != nil {
				t.Fatal(err)
			}
			b0 := initial.LatestExecutionTurnID
			f.srv.mu.RLock()
			replacement := f.srv.appDescendantFrontiers[first.ChildSessionID]
			f.srv.mu.RUnlock()
			if replacement == nil || b0 == "" || b0 == retired.turnID {
				t.Fatal("replacement baseline not reached")
			}
			// Let A enter the route and resolve B, but pause immediately after
			// B's real accessor returns B0, before A's row install.
			oldSample := &descendantRuntimeGate{sampled: make(chan descendantRuntimeSample, 1), resume: make(chan struct{})}
			f.mu.Lock()
			f.gate = oldSample
			f.mu.Unlock()
			t.Cleanup(oldSample.release)
			oldRoute.release()
			var sampled descendantRuntimeSample
			select {
			case sampled = <-oldSample.sampled:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			if sampled.childID != first.ChildSessionID || sampled.turnID != b0 {
				t.Fatalf("old callback did not resample B0: %+v want %s", sampled, b0)
			}
			// The current runtime now records a second real execution and its
			// typed event returns from the route before A's install resumes.
			f.tool(t, "delegate_send", map[string]any{"to": first.DelegateID, "message": "replacement-second-input", "max_wait_ms": 10000})
			f.awaitIdle(t, first.ChildSessionID)
			f.awaitRecorded(t, first.ChildSessionID, "")
			current, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: first.ChildSessionID, IncludeTurns: true})
			if err != nil {
				t.Fatal(err)
			}
			b1 := current.LatestExecutionTurnID
			f.srv.mu.RLock()
			installed := f.srv.appDescendantFrontiers[first.ChildSessionID]
			same := installed != nil && installed.registration == replacement.registration && installed.bootGeneration == replacement.bootGeneration
			before := f.srv.appDescendants[first.ChildSessionID].thread.Evener.LatestExecutionTurnID
			f.srv.mu.RUnlock()
			if !same || b1 == "" || b1 == b0 || before != b1 {
				t.Fatalf("current runtime did not advance same registration: same=%t B0=%q B1=%q row=%q", same, b0, b1, before)
			}
			oldSample.release()
			f.awaitRecorded(t, first.ChildSessionID, retired.turnID)
			metadata, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: first.ChildSessionID})
			if err != nil {
				t.Fatal(err)
			}
			history, err := f.srv.handleAppThreadRead(f.ctx, appwire.ThreadReadParams{ThreadID: first.ChildSessionID, IncludeTurns: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("sameRegistration=%t generation=%q retired=%q B0=%q B1=%q before=%q after=%q history=%q", same, replacement.bootGeneration, retired.turnID, b0, b1, before, metadata.Thread.Evener.LatestExecutionTurnID, history.LatestExecutionTurnID)
			if metadata.Thread.Evener.LatestExecutionTurnID != history.LatestExecutionTurnID {
				t.Fatalf("late A callback regressed B's metadata after B's own event returned: metadata=%q history=%q", metadata.Thread.Evener.LatestExecutionTurnID, history.LatestExecutionTurnID)
			}
		})
	}
}
