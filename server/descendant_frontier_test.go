package server

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestDescendantFrontierAddressedSpawn(t *testing.T) {
	t.Parallel()
	p := &parityProvider{childRelease: make(chan struct{})}
	close(p.childRelease)
	p.script(step(parityResponse(parityCall("frontier-spawn", "delegate", map[string]any{"prompt": parityChildTask}))), step(parityCommunicate("frontier-root-done", "root-result", true)))
	client := llm.NewClient()
	client.Register(p)
	dir := t.TempDir()
	root, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	wireTranscriptHistory(t, root, srv)
	srv.mu.RLock()
	rootRegistered := srv.appDescendantFrontiers[root.ID()] != nil
	srv.mu.RUnlock()
	if rootRegistered {
		t.Fatal("root history wiring registered a descendant sampler")
	}
	srv.SetDescendantTranscriptPathFunc(func(id string) string { return filepath.Join(dir, "sessions", id+".transcript.jsonl") })
	routed := make(chan events.SessionEvent, 8)
	root.SetDescendantEventFunc(func(ev events.SessionEvent) {
		srv.RecordDescendantAppEvent(root.ID(), ev)
		if ev.Kind == events.EventExecutionFirstRecorded {
			routed <- ev
		}
	})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range root.Events() {
		}
	}()
	t.Cleanup(func() { root.Close(); <-drained })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second) // Hang tripwire, not synchronization.
	defer cancel()
	if _, err := root.ProcessInput(ctx, "frontier-root-input", nil); err != nil {
		t.Fatal(err)
	}
	var ev events.SessionEvent
	select {
	case ev = <-routed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	want := ev.Data.(events.ExecutionFirstRecordedData).TurnID
	metadata, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{ThreadID: ev.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	history, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{ThreadID: ev.SessionID, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if want == "" || history.LatestExecutionTurnID != want {
		t.Fatalf("triggered child execution=%q, history=%q", want, history.LatestExecutionTurnID)
	}
	if got := metadata.Thread.Evener.LatestExecutionTurnID; got != want {
		t.Fatalf("routed real child execution=%q history=%q metadata=%q", want, history.LatestExecutionTurnID, got)
	}
}

func TestDescendantFrontierRegistrationFence(t *testing.T) {
	t.Parallel()
	for _, generation := range []bool{false, true} {
		t.Run(fmt.Sprintf("generation-%t", generation), func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			t.Cleanup(srv.Close)
			srv.mu.Lock()
			srv.appThreadID = "frontier-owner"
			srv.mu.Unlock()
			a := srv.descendantFrontierHooks("frontier-owner", "frontier-child")
			sampled, resume, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			t.Cleanup(func() { resumeOnce.Do(func() { close(resume) }) })
			a.RegisterFrontier(func() events.ExecutionFrontier {
				close(sampled)
				<-resume
				return events.ExecutionFrontier{TurnID: "frontier-A", Sequence: 1}
			})
			if generation {
				a.PublishFrontier("101")
			}
			srv.mu.RLock()
			carried := srv.appDescendantFrontiers["frontier-child"]
			srv.mu.RUnlock()
			event := events.SessionEvent{SessionID: "frontier-child", Kind: events.EventExecutionFirstRecorded, Data: events.ExecutionFirstRecordedData{TurnID: "not-the-source"}}
			go func() { defer close(returned); srv.RecordDescendantAppEvent("frontier-owner", event) }()
			select {
			case <-sampled:
			case <-time.After(30 * time.Second):
				t.Fatal("child sample not reached")
			}
			// Registering and routing B while A is sampled proves neither Server.mu
			// nor the projection gate spans A's sample.
			a.ReleaseFrontier()
			srv.RecordDescendantAppEvent("frontier-owner", event) // Empty interval: MISS, no sample.
			b := srv.descendantFrontierHooks("frontier-owner", "frontier-child")
			b.RegisterFrontier(func() events.ExecutionFrontier { return events.ExecutionFrontier{TurnID: "frontier-B", Sequence: 1} })
			if generation {
				b.PublishFrontier("102")
			}
			srv.RecordDescendantAppEvent("frontier-owner", event)
			a.PublishFrontier("stale-generation")
			a.ReleaseFrontier()
			srv.mu.RLock()
			current := srv.appDescendantFrontiers["frontier-child"]
			before := srv.appDescendants["frontier-child"].thread.Evener.LatestExecutionTurnID
			srv.mu.RUnlock()
			if current == nil || current.registration == carried.registration || before != "frontier-B" {
				t.Fatalf("replacement before A resumed: current=%+v row=%q", current, before)
			}
			if generation && current.bootGeneration != "102" {
				t.Error("A publish altered B generation")
			}
			resumeOnce.Do(func() { close(resume) })
			select {
			case <-returned:
			case <-time.After(30 * time.Second):
				t.Fatal("old callback did not return")
			}
			srv.mu.RLock()
			after := srv.appDescendants["frontier-child"].thread.Evener.LatestExecutionTurnID
			srv.mu.RUnlock()
			if after != "frontier-B" {
				t.Fatalf("released A regressed B row to %q", after)
			}
			// A callback delayed BEFORE route entry safely resolves B, regardless
			// of the old event's payload, which is provenance, not a frontier.
			sequence := srv.appNotifier.CurrentSequence()
			srv.RecordDescendantAppEvent("frontier-owner", event)
			srv.mu.RLock()
			after = srv.appDescendants["frontier-child"].thread.Evener.LatestExecutionTurnID
			srv.mu.RUnlock()
			if after != "frontier-B" {
				t.Fatal("before-route callback did not resample B")
			}
			if srv.appNotifier.CurrentSequence() != sequence {
				t.Fatal("frontier-only row install emitted a notification")
			}
			b.ReleaseFrontier()
			b.ReleaseFrontier()
			srv.mu.RLock()
			remaining := len(srv.appDescendantFrontiers)
			srv.mu.RUnlock()
			if remaining != 0 {
				t.Fatalf("idempotent B release retained %d entries", remaining)
			}
		})
	}
}

func TestDescendantFrontierPublicationIsImmutable(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	srv.mu.Lock()
	srv.appThreadID = "frontier-owner"
	srv.mu.Unlock()
	hooks := srv.descendantFrontierHooks("frontier-owner", "frontier-child")
	// Publication before attach cannot mint an identity or entry.
	hooks.PublishFrontier("premature")
	srv.mu.RLock()
	before := len(srv.appDescendantFrontiers)
	srv.mu.RUnlock()
	if before != 0 {
		t.Fatal("publish minted registration")
	}
	hooks.RegisterFrontier(func() events.ExecutionFrontier {
		return events.ExecutionFrontier{TurnID: "frontier-child-turn", Sequence: 1}
	})
	srv.mu.RLock()
	carried := srv.appDescendantFrontiers["frontier-child"]
	srv.mu.RUnlock()
	hooks.PublishFrontier("101")
	srv.mu.RLock()
	current := srv.appDescendantFrontiers["frontier-child"]
	srv.mu.RUnlock()
	if current == carried || current.registration != carried.registration || carried.bootGeneration != "" || current.bootGeneration != "101" {
		t.Fatalf("publication mutated carried registration: carried=%+v current=%+v", carried, current)
	}
}

func TestDescendantFrontierInstallPairIsPerRegistration(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	srv.mu.Lock()
	srv.appThreadID = "frontier-owner"
	srv.mu.Unlock()
	stamp := events.ExecutionFrontier{TurnID: "turn_old", Sequence: 9}
	hooks := srv.descendantFrontierHooks("frontier-owner", "frontier-child")
	hooks.RegisterFrontier(func() events.ExecutionFrontier { return stamp })
	srv.mu.RLock()
	uninstalled := srv.appDescendantFrontiers["frontier-child"]
	srv.mu.RUnlock()
	event := events.SessionEvent{SessionID: "frontier-child", Kind: events.EventExecutionFirstRecorded, Data: events.ExecutionFirstRecordedData{TurnID: "provenance-only"}}
	srv.RecordDescendantAppEvent("frontier-owner", events.SessionEvent{SessionID: "frontier-child", Kind: events.EventSessionStart, Data: events.SessionStartData{LatestExecutionTurnID: stamp.TurnID, LatestExecutionSequence: stamp.Sequence}})
	srv.mu.RLock()
	installed := srv.appDescendantFrontiers["frontier-child"]
	srv.mu.RUnlock()
	if installed == uninstalled || installed.lastInstall != stamp || uninstalled.lastInstall != (events.ExecutionFrontier{}) {
		t.Fatalf("install mutated carried entry: carried=%+v installed=%+v", uninstalled, installed)
	}
	hooks.PublishFrontier("101")
	srv.mu.RLock()
	published := srv.appDescendantFrontiers["frontier-child"]
	srv.mu.RUnlock()
	if published == installed || published.lastInstall != stamp || installed.bootGeneration != "" || published.bootGeneration != "101" {
		t.Fatal("publication did not preserve the immutable last-install pair")
	}
	// Equal stamps still write idempotently and copy-replace the immutable entry.
	srv.RecordDescendantAppEvent("frontier-owner", event)
	srv.mu.RLock()
	equal := srv.appDescendantFrontiers["frontier-child"]
	row := srv.appDescendants["frontier-child"].thread.Evener.LatestExecutionTurnID
	srv.mu.RUnlock()
	if equal == published || equal.lastInstall != stamp || row != stamp.TurnID {
		t.Fatalf("equal stamp was not an idempotent install: entry=%+v row=%q", equal, row)
	}
	// Sequence 1 belongs to a fresh registration. The predecessor's sequence 9
	// remains in the row but must never fence this runtime's first installation.
	replacement := srv.descendantFrontierHooks("frontier-owner", "frontier-child")
	stamp = events.ExecutionFrontier{TurnID: "turn_new", Sequence: 1}
	replacement.RegisterFrontier(func() events.ExecutionFrontier { return stamp })
	srv.RecordDescendantAppEvent("frontier-owner", event)
	srv.mu.RLock()
	current := srv.appDescendantFrontiers["frontier-child"]
	row = srv.appDescendants["frontier-child"].thread.Evener.LatestExecutionTurnID
	srv.mu.RUnlock()
	if current.registration == equal.registration || current.lastInstall != stamp || row != stamp.TurnID || equal.lastInstall.Sequence != 9 {
		t.Fatalf("new registration compared against predecessor row or mutated old entry: current=%+v old=%+v row=%q", current, equal, row)
	}
}

func TestDescendantFrontierSessionStartOrdersHeldSample(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	srv.mu.Lock()
	srv.appThreadID = "frontier-owner"
	srv.mu.Unlock()
	sampled, resume, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(resume) }) })
	hooks := srv.descendantFrontierHooks("frontier-owner", "frontier-child")
	hooks.RegisterFrontier(func() events.ExecutionFrontier {
		stamp := events.ExecutionFrontier{TurnID: "turn_older", Sequence: 3}
		close(sampled)
		<-resume
		return stamp
	})
	srv.mu.RLock()
	carried := srv.appDescendantFrontiers["frontier-child"]
	srv.mu.RUnlock()
	go func() {
		defer close(returned)
		srv.RecordDescendantAppEvent("frontier-owner", events.SessionEvent{SessionID: "frontier-child", Kind: events.EventExecutionFirstRecorded, Data: events.ExecutionFirstRecordedData{TurnID: "provenance-only"}})
	}()
	select {
	case <-sampled:
	case <-time.After(30 * time.Second): // Hang tripwire, not scheduling.
		t.Fatal("held bootstrap-predecessor sample did not run")
	}
	bootstrap := events.ExecutionFrontier{TurnID: "turn_bootstrap", Sequence: 4}
	start := events.SessionEvent{SessionID: "frontier-child", Kind: events.EventSessionStart, Data: events.SessionStartData{LatestExecutionTurnID: bootstrap.TurnID, LatestExecutionSequence: bootstrap.Sequence}}
	srv.RecordDescendantAppEvent("frontier-owner", start)
	srv.mu.RLock()
	installed := srv.appDescendantFrontiers["frontier-child"]
	srv.mu.RUnlock()
	if installed == carried || installed.registration != carried.registration || installed.lastInstall != bootstrap || carried.lastInstall != (events.ExecutionFrontier{}) {
		t.Fatalf("bootstrap did not copy-replace last-install pair: carried=%+v installed=%+v", carried, installed)
	}
	once.Do(func() { close(resume) })
	select {
	case <-returned:
	case <-time.After(30 * time.Second): // Hang tripwire, not scheduling.
		t.Fatal("held bootstrap-predecessor sample did not return")
	}
	// A delayed SessionStart is subject to the same guard, not just ordinary
	// samples. Its row replacement must preserve the already installed frontier.
	start.Data = events.SessionStartData{LatestExecutionTurnID: "turn_stale_bootstrap", LatestExecutionSequence: 2}
	srv.RecordDescendantAppEvent("frontier-owner", start)
	srv.mu.RLock()
	current := srv.appDescendantFrontiers["frontier-child"]
	row := srv.appDescendants["frontier-child"].thread.Evener.LatestExecutionTurnID
	srv.mu.RUnlock()
	if current != installed || current.lastInstall != bootstrap || row != bootstrap.TurnID {
		t.Fatalf("held sample or stale bootstrap displaced newer pair: current=%+v row=%q", current, row)
	}
}

func TestDescendantFrontierGenerationChangedDuringSample(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	srv.mu.Lock()
	srv.appThreadID = "frontier-owner"
	srv.mu.Unlock()
	hooks := srv.descendantFrontierHooks("frontier-owner", "frontier-child")
	sampled, resume, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(resume) }) })
	hooks.RegisterFrontier(func() events.ExecutionFrontier {
		close(sampled)
		<-resume
		return events.ExecutionFrontier{TurnID: "stale-generation-sample", Sequence: 1}
	})
	event := events.SessionEvent{SessionID: "frontier-child", Kind: events.EventExecutionFirstRecorded, Data: events.ExecutionFirstRecordedData{TurnID: "provenance-only"}}
	go func() { defer close(returned); srv.RecordDescendantAppEvent("frontier-owner", event) }()
	select {
	case <-sampled:
	case <-time.After(30 * time.Second):
		t.Fatal("sample not reached")
	}
	hooks.PublishFrontier("101")
	once.Do(func() { close(resume) })
	select {
	case <-returned:
	case <-time.After(30 * time.Second):
		t.Fatal("sample did not return")
	}
	srv.mu.RLock()
	frontier := srv.appDescendants["frontier-child"].thread.Evener.LatestExecutionTurnID
	srv.mu.RUnlock()
	if frontier != "" {
		t.Fatalf("changed generation accepted carried sample=%q", frontier)
	}
}

func TestDescendantFrontierSupersededRoot(t *testing.T) {
	t.Parallel()
	p := &parityProvider{childRelease: make(chan struct{})}
	close(p.childRelease)
	p.script(step(parityResponse(parityCall("superseded-spawn", "delegate", map[string]any{"prompt": parityChildTask}))), step(parityCommunicate("superseded-root-done", "root-result", true)))
	client := llm.NewClient()
	client.Register(p)
	old, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), agent.SessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	wireTranscriptHistory(t, old, srv)
	replacement, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), agent.SessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replacement.Close)
	wireTranscriptHistory(t, replacement, srv)
	routed := make(chan struct{}, 4)
	old.SetDescendantEventFunc(func(ev events.SessionEvent) {
		srv.RecordDescendantAppEvent(old.ID(), ev)
		if ev.Kind == events.EventExecutionFirstRecorded {
			routed <- struct{}{}
		}
	})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range old.Events() {
		}
	}()
	t.Cleanup(func() { old.Close(); <-drained })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := old.ProcessInput(ctx, "superseded-root-input", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-routed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	srv.mu.RLock()
	entries, rows := len(srv.appDescendantFrontiers), len(srv.appDescendants)
	srv.mu.RUnlock()
	if entries != 0 || rows != 0 {
		t.Fatalf("superseded root child registered or installed: entries=%d rows=%d", entries, rows)
	}
	if old.LatestExecutionTurnID() == "" {
		t.Fatal("root control did not record an execution")
	}
}

func TestDescendantFrontierIdleRestoredChild(t *testing.T) {
	t.Parallel()
	p := &parityProvider{childRelease: make(chan struct{})}
	close(p.childRelease)
	p.script(step(parityResponse(parityCall("restore-frontier-spawn", "delegate", map[string]any{"prompt": parityChildTask}))), step(parityCommunicate("restore-frontier-root-done", "root-result", true)))
	client := llm.NewClient()
	client.Register(p)
	dir := t.TempDir()
	root, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	childIdle := make(chan events.DelegateUpdatedData, 8)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range root.Events() {
			if data, ok := ev.Data.(events.DelegateUpdatedData); ok && data.Lifecycle == "idle" && data.Outcome == "completed" {
				childIdle <- data
			}
		}
	}()
	t.Cleanup(func() { root.Close(); <-drained })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "restore-frontier-root-input", nil); err != nil {
		t.Fatal(err)
	}
	var child events.DelegateUpdatedData
	select {
	case child = <-childIdle:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	root.Close()
	meta, err := schema.LoadSessionMeta(dir, child.ChildSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.IsSubagent {
		t.Fatal("fixture is not a real spawned child")
	}
	restored, err := agent.RestoreSessionFromMetaWithConfig(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, agent.RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	want := restored.LatestExecutionTurnID()
	if want == "" {
		t.Fatal("completed child execution missing from attach seed")
	}
	var start events.SessionEvent
	for start.Kind != events.EventSessionStart {
		select {
		case start = <-restored.Events():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if start.Kind == events.EventExecutionFirstRecorded {
			t.Fatal("idle restored child opened a new execution")
		}
	}
	if !start.Data.(events.SessionStartData).Restored {
		t.Fatal("constructor did not emit restored SessionStart")
	}
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	identity, err := PrepareAppIdentity("local", root.ID(), root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(identity.WithRecordedLength(root.TranscriptRecordedLength()).WithBootGeneration("1"), nil)
	srv.SetDescendantTranscriptPathFunc(func(id string) string { return filepath.Join(dir, "sessions", id+".transcript.jsonl") })
	srv.RecordDescendantAppEvent(root.ID(), start)
	metadata, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{ThreadID: child.ChildSessionID})
	if err != nil {
		t.Fatal(err)
	}
	history, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{ThreadID: child.ChildSessionID, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if history.LatestExecutionTurnID != want {
		t.Fatalf("restored child seed=%q history=%q", want, history.LatestExecutionTurnID)
	}
	if got := metadata.Thread.Evener.LatestExecutionTurnID; got != want {
		t.Fatalf("restored child start row metadata=%q attach seed/history=%q", got, want)
	}
}
