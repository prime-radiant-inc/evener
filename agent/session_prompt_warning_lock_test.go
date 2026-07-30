package agent

import (
	"context"
	"embed"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
)

// forceSystemPromptRenderFailure makes the embedded template renderer fail, so
// renderSystemPrompt takes its warning path. The renderer is a package var
// precisely so a test can do this; it is the only way to reach that path,
// because the templates it renders are compiled into the binary.
func forceSystemPromptRenderFailure(t *testing.T) {
	t.Helper()
	old := renderEmbeddedSystemPrompt
	renderEmbeddedSystemPrompt = func(*sectionResolver, embed.FS, string, string, promptData) (string, []promptSource, error) {
		return "", nil, errors.New("forced render failure")
	}
	t.Cleanup(func() { renderEmbeddedSystemPrompt = old })
}

// awaitOrFail runs fn and fails loudly if it has not returned within the
// budget, instead of hanging until the package timeout. A self-deadlock and a
// slow machine look identical to a bare channel receive; only one of them
// should be able to eat the whole package clock.
func awaitOrFail(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return within 10s: it is deadlocked, not slow", what)
	}
}

func promptWarningSession(t *testing.T) *Session {
	t.Helper()
	return &Session{
		id:      "prompt-warning",
		profile: NewOpenAIProfile("gpt-5.2"),
		reg:     tool.NewRegistry(),
		env:     execenv.NewLocalExecutionEnvironment(t.TempDir()),
		events:  make(chan events.SessionEvent, 16),
	}
}

// TestRegisterToolReportsRenderFailureWithoutSelfDeadlock pins the lock
// discipline for the system prompt's own failure warning.
//
// RegisterTool rebuilds the prompt cache under s.mu, and it must: the cache and
// the env it derives from are both guarded by that mutex. But the render can
// fail, and reporting that failure means emitting -- and emit's very first act
// is activeCausalProvenance(), which takes s.mu. On a non-reentrant mutex that
// is a self-deadlock, reached with no concurrency at all.
//
// So the warning must leave the critical section before it is emitted. This
// test drives the real entry point rather than renderSystemPrompt directly:
// calling the renderer straight only proves the failure path returns a string,
// which it did while the lock bug was live.
func TestRegisterToolReportsRenderFailureWithoutSelfDeadlock(t *testing.T) {
	forceSystemPromptRenderFailure(t)
	s := promptWarningSession(t)

	awaitOrFail(t, "RegisterTool with a failing prompt render", func() {
		s.RegisterTool("probe", "d", map[string]any{}, func(context.Context, any) (any, error) { return nil, nil })
	})

	// The failure must still be reported. A fix that silences the warning to
	// dodge the lock would pass a liveness-only assertion.
	select {
	case ev := <-s.events:
		warning, ok := ev.Data.(events.WarningData)
		if !ok {
			t.Fatalf("emitted %T, want WarningData", ev.Data)
		}
		if !strings.Contains(warning.Message, "forced render failure") {
			t.Fatalf("warning = %q, want it to name the render failure", warning.Message)
		}
	default:
		t.Fatal("no warning emitted: the render failure was swallowed")
	}
}

// TestSwapEnvAndRefreshReportsRenderFailureWithoutSelfDeadlock is the same
// contract at the other call site that refreshes the prompt cache under s.mu.
// The two are pinned separately because they are separate critical sections:
// fixing one does not fix the other, and only a per-site test says so.
func TestSwapEnvAndRefreshReportsRenderFailureWithoutSelfDeadlock(t *testing.T) {
	forceSystemPromptRenderFailure(t)
	s := promptWarningSession(t)
	// Built via WithWorkingDirectory, never NewLocalExecutionEnvironment:
	// swapEnvAndRefresh's contract requires it, and a test that violates the
	// contract is testing a state production cannot reach.
	next := s.env.(*execenv.LocalExecutionEnvironment).WithWorkingDirectory(t.TempDir())

	awaitOrFail(t, "swapEnvAndRefresh with a failing prompt render", func() {
		s.swapEnvAndRefresh(next)
	})

	select {
	case ev := <-s.events:
		warning, ok := ev.Data.(events.WarningData)
		if !ok {
			t.Fatalf("emitted %T, want WarningData", ev.Data)
		}
		if !strings.Contains(warning.Message, "forced render failure") {
			t.Fatalf("warning = %q, want it to name the render failure", warning.Message)
		}
	default:
		t.Fatal("no warning emitted: the render failure was swallowed")
	}
}
