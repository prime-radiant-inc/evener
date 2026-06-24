package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// shellQuote wraps s in single quotes so a filesystem path can be embedded
// safely in a shell command built by these hook tests.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

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

	mu       sync.Mutex
	requests []llm.Request
	steps    []func(req llm.Request) llm.Response
	i        int
}

func (a *fakeAdapter) Name() string { return a.name }

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

func (a *fakeAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
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
type fakeErrAdapter struct {
	name string

	mu       sync.Mutex
	requests []llm.Request
	steps    []func(req llm.Request) (llm.Response, error)
	i        int
}

func (a *fakeErrAdapter) Name() string { return a.name }

func (a *fakeErrAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	if a.i >= len(a.steps) {
		return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("done")}, nil
	}
	resp, err := a.steps[a.i](req)
	a.i++
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
	return finalResponse("done"), nil
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
