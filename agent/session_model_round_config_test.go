package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// barrierFallbackAdapter is a scripted provider at the LLM boundary for the
// CORE-02 regression: the primary model call blocks until released and then
// returns a permanent (403) error, while every other model answers normally.
// Stream is unsupported so callModel takes the Complete path.
type barrierFallbackAdapter struct {
	name string

	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	models []string
}

func (a *barrierFallbackAdapter) Name() string { return a.name }

func (a *barrierFallbackAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *barrierFallbackAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.models = append(a.models, req.Model)
	a.mu.Unlock()
	if req.Model == "primary" {
		a.once.Do(func() { close(a.entered) })
		select {
		case <-a.release:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "no access", nil, nil)
	}
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("fallback answered")}, nil
}

func (a *barrierFallbackAdapter) Models() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.models...)
}

// TestSession_FallbackUsesDispatchSnapshot pins CORE-02: a round that is
// already in flight must keep using the fallback configuration it was
// dispatched with, even when a concurrent SetModel revalidates and rewrites
// s.cfg.ModelFallbacks while the primary call is outstanding. Reading the live
// slice inside the fallback chain let that write both race the read and empty
// the in-flight round's chain: a cross-surface switch drops the same-surface
// fallback entry, and this round then loses its fallback entirely.
func TestSession_FallbackUsesDispatchSnapshot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &barrierFallbackAdapter{
		name:    "openai",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	c.Register(adapter)
	c.Register(&fakeAdapter{name: "anthropic"})

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ResolveProfile: testResolver,
		// A cross-instance, same-surface entry: valid against the openai
		// profile now, dropped once the session switches to anthropic.
		ModelFallbacks: []string{"openai/gpt-fallback"},
		testOnly:       testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	// TRIPWIRE: scripted in-process adapter with a channel barrier, no real
	// I/O; this ceiling only fires on a genuine deadlock, never as a timing
	// mechanism.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := llm.Request{Provider: "openai", Model: "primary", Messages: []llm.Message{llm.User("hi")}}
	type outcome struct {
		resp sessionModelResponse
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, _, _, _, callErr := sess.callModelWithFallback(ctx, NewOpenAIProfile("primary"), req, nil, "", 0)
		done <- outcome{resp: resp, err: callErr}
	}()

	// Wait until the primary call is genuinely in flight before switching.
	select {
	case <-adapter.entered:
	case <-ctx.Done():
		t.Fatal("primary model call never started")
	}

	// Cross-surface switch: revalidates and drops the now-invalid fallback
	// entry from s.cfg.ModelFallbacks. The in-flight round must not see that.
	if err := sess.SetModel("anthropic/claude-opus-4-6"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if len(sess.cfg.ModelFallbacks) != 0 {
		t.Fatalf("cfg.ModelFallbacks after cross-surface switch = %v, want empty", sess.cfg.ModelFallbacks)
	}

	close(adapter.release)

	res := <-done
	if res.err != nil {
		t.Fatalf("in-flight round error = %v, want the dispatch-time fallback to answer", res.err)
	}
	if got := res.resp.Response.Text(); !strings.Contains(got, "fallback answered") {
		t.Fatalf("round text = %q, want the fallback's answer", got)
	}
	if got := adapter.Models(); len(got) != 2 || got[0] != "primary" || got[1] != "gpt-fallback" {
		t.Fatalf("attempted models = %v, want [primary gpt-fallback]", got)
	}
}
