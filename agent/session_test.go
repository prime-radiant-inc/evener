package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// marshalToMap serializes an event payload to JSON and decodes it back into a
// map[string]any, so tests can assert the wire-level JSON shape (e.g. that a
// legacy key is absent) of a payload struct.
func marshalToMap(t *testing.T, data events.EventData) map[string]any {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return m
}

type fakeAdapter struct {
	name string
	// liveModels, when set, scripts the adapter's llm.LiveModelLister
	// listing; an unscripted fake cannot list models at all.
	liveModels func(ctx context.Context) ([]registry.Model, error)

	// steps is the CONSTRUCTION-TIME script: hundreds of fixtures build it in
	// the struct literal, before any drive exists. After construction every
	// read and write goes through the synchronized methods below — a test
	// reaching for the field mid-drive reopens the race the setters exist to
	// prevent (the agenttest.FakeAdapter carries the no-field engine version
	// for adapters whose construction surface is small).
	mu       sync.Mutex
	requests []llm.Request
	steps    []func(req llm.Request) llm.Response
	i        int
}

func (a *fakeAdapter) Name() string { return a.name }

// LiveModels implements llm.LiveModelLister when a test scripts a listing.
func (a *fakeAdapter) LiveModels(ctx context.Context) ([]registry.Model, error) {
	if a.liveModels == nil {
		return nil, errors.New("fake adapter does not list models")
	}
	return a.liveModels(ctx)
}

func (a *fakeAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	if a.i >= len(a.steps) {
		return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("done")}, nil
	}
	resp := a.steps[a.i](req)
	a.i++
	// Fill required response fields best-effort.
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *fakeAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// setSteps scripts the adapter's response sequence. A retained lane's
// background drive can call Complete while a test scripts the next turn, so
// reassignment takes the same mutex Complete reads under; the cursor (i)
// survives it, which is what lets a test script a turn mid-run.
// Call it from the test goroutine, never from a scripted step: Complete holds
// the mutex while it runs the step.
func (a *fakeAdapter) setSteps(steps []func(req llm.Request) llm.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steps = steps
}

// appendSteps adds one step to the end of the script under the same mutex.
// Like setSteps, it is not callable from a scripted step.
func (a *fakeAdapter) appendSteps(step func(req llm.Request) llm.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steps = append(a.steps, step)
}

// scriptLen reports the scripted turn count for fixtures that key off it.
// The script is only touched under the mutex, so even a setup-time read goes
// through here rather than reaching for the field.
func (a *fakeAdapter) scriptLen() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.steps)
}

// stepAt returns the currently scripted step at index, under the same mutex
// Complete reads under, pairing with setStepAt for tests that capture a step
// and wrap it with extra assertions. It panics on an out-of-range index.
func (a *fakeAdapter) stepAt(index int) func(req llm.Request) llm.Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	if index < 0 || index >= len(a.steps) {
		panic("fakeAdapter: script index out of range")
	}
	return a.steps[index]
}

// setStepAt replaces the step at index under the same mutex, so wrapping a
// step beside a concurrent drive cannot race. It panics on an out-of-range
// index: a test addressing a position beyond its own script is a fixture bug.
func (a *fakeAdapter) setStepAt(index int, step func(req llm.Request) llm.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if index < 0 || index >= len(a.steps) {
		panic("fakeAdapter: script index out of range")
	}
	a.steps[index] = step
}

func (a *fakeAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

// A retained lane's background drive can call Complete at any moment, so
// scripting the next turn mid-drive must not write the script beside that
// read: the race detector marks every test running when it catches one.
func TestFakeAdapterScriptResistsConcurrentDrive(t *testing.T) {
	t.Parallel()
	a := &fakeAdapter{name: "race-fixture"}
	a.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("scripted")} },
	})
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			_, _ = a.Complete(context.Background(), llm.Request{})
		}
	})
	for range 200 {
		a.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("next")} },
		})
	}
	wg.Wait()
}

// The driver's cursor belongs to the adapter, not the slice: a mid-run
// replacement keeps serving from the same position in the new sequence, and
// appendSteps extends it. The lane fixtures script mid-run turns on exactly
// this contract (the notificationTurn note in session_worktree_orphan_test.go).
func TestFakeAdapterScriptReplacementPreservesCursor(t *testing.T) {
	t.Parallel()
	a := &fakeAdapter{name: "script-fixture"}
	a.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("first")} },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("second")} },
	})
	if got := completeText(t, a); got != "first" {
		t.Fatalf("first completion = %q, want the first scripted step", got)
	}
	a.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("replacement first")} },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("replacement second")} },
	})
	if got := completeText(t, a); got != "replacement second" {
		t.Fatalf("completion after replacing the script = %q, want the replacement's step at the surviving cursor", got)
	}
	a.appendSteps(func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("appended")} })
	if got := a.scriptLen(); got != 3 {
		t.Fatalf("scriptLen = %d, want 3", got)
	}
	if got := completeText(t, a); got != "appended" {
		t.Fatalf("completion after appending = %q, want the appended step", got)
	}
}

// completeText runs one model call through the fake and returns the answer's
// text.
func completeText(t *testing.T, a *fakeAdapter) string {
	t.Helper()
	resp, err := a.Complete(context.Background(), llm.Request{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return resp.Message.Text()
}

type compactionEventStrategy struct {
	emitCompaction bool
}

func (s compactionEventStrategy) Name() string { return "compaction-event-test" }

func (s compactionEventStrategy) Tools() []tool.RegisteredTool { return nil }

func (s compactionEventStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	_ = ctx
	_ = history
	_ = sysPromptChars
	if s.emitCompaction {
		emitFn(events.EventContextCompaction, events.ContextCompactionData{Layer: "test"})
	}
	return nil
}

func (s compactionEventStrategy) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
	_ = ctx
	_ = history
	_ = client
	return nil
}

func countHookStarts(evs []events.SessionEvent, event plugin.HookEvent) int {
	count := 0
	for _, ev := range evs {
		if ev.Kind != events.EventHookStart {
			continue
		}
		if data, ok := ev.Data.(events.HookStartData); ok && data.Event == string(event) {
			count++
		}
	}
	return count
}

type streamingAdapter struct {
	name string

	mu             sync.Mutex
	completeCalls  int
	streamCalls    int
	requests       []llm.Request
	completeResult llm.Response
	streamErr      error
	streamScript   func(*llm.ChanStream)
}

func (a *streamingAdapter) Name() string { return a.name }

func (a *streamingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	a.completeCalls++
	a.requests = append(a.requests, req)
	resp := a.completeResult
	a.mu.Unlock()
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *streamingAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	a.mu.Lock()
	a.streamCalls++
	a.requests = append(a.requests, req)
	err := a.streamErr
	script := a.streamScript
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	_ = streamCtx
	st := llm.NewChanStream(cancel)
	go func() {
		defer st.CloseSend()
		if script != nil {
			script(st)
		}
	}()
	return st, nil
}

func (a *streamingAdapter) Counts() (completeCalls int, streamCalls int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.completeCalls, a.streamCalls
}

// fakeErrAdapter is like fakeAdapter but supports steps that return errors.
// The script lives in the shared engine: the step is read under its mutex and
// run outside it, so a blocked step never holds the script and no fixture can
// assign the slice beside a drive.
type fakeErrAdapter struct {
	name string

	script agenttest.Script[func(req llm.Request) (llm.Response, error)]

	mu       sync.Mutex
	requests []llm.Request
}

// newFakeErrAdapter builds a scripted adapter over steps. The script owns the
// cursor and its lock, so a test cannot hand-roll a second cursor beside it.
func newFakeErrAdapter(name string, steps []func(req llm.Request) (llm.Response, error)) *fakeErrAdapter {
	a := &fakeErrAdapter{name: name}
	a.script.Replace(steps)
	return a
}

func (a *fakeErrAdapter) Name() string { return a.name }

func (a *fakeErrAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	a.requests = append(a.requests, req)
	a.mu.Unlock()
	step, ok := a.script.Next()
	if !ok {
		return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("done")}, nil
	}
	resp, err := step(req)
	if err != nil {
		return resp, err
	}
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *fakeErrAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *fakeErrAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

// blockingAdapter is a test adapter whose Complete blocks until context is cancelled.
type blockingAdapter struct {
	name    string
	blocked chan struct{} // closed when LLM call starts blocking
}

func (a *blockingAdapter) Name() string { return a.name }
func (a *blockingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	close(a.blocked)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
func (a *blockingAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

type closeRaceAdapter struct {
	name    string
	blocked chan struct{}
	release chan struct{}
}

func (a *closeRaceAdapter) Name() string { return a.name }
func (a *closeRaceAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	close(a.blocked)
	<-a.release
	return agenttest.FinalResponse("done"), nil
}
func (a *closeRaceAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

type blockingCleanupEnv struct {
	execenv.ExecutionEnvironment
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func (e *blockingCleanupEnv) Cleanup() {
	e.startedOnce.Do(func() { close(e.started) })
	<-e.release
	e.ExecutionEnvironment.Cleanup()
}

// cleanupTrackingEnv wraps an execenv.ExecutionEnvironment and records the order
// of operations during shutdown. Cleanup() pauses briefly so that any
// SESSION_END event already in the buffered channel has time to be consumed,
// which lets us prove the ordering via a shared log.
type cleanupTrackingEnv struct {
	execenv.ExecutionEnvironment
	mu  sync.Mutex
	log []string
}

func (e *cleanupTrackingEnv) Cleanup() {
	e.mu.Lock()
	e.log = append(e.log, "cleanup_start")
	e.mu.Unlock()

	// Pause to give the consumer goroutine time to drain any events that
	// were already in the buffered channel. If SESSION_END was sent before
	// Cleanup, the consumer will record "session_end_received" during this
	// sleep, causing it to appear before "cleanup_end" in the log.
	time.Sleep(100 * time.Millisecond)

	e.ExecutionEnvironment.Cleanup()

	e.mu.Lock()
	e.log = append(e.log, "cleanup_end")
	e.mu.Unlock()
}

func (e *cleanupTrackingEnv) Log() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.log...)
}

func (e *cleanupTrackingEnv) Append(op string) {
	e.mu.Lock()
	e.log = append(e.log, op)
	e.mu.Unlock()
}
