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
	"primeradiant.com/serf/llm"
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

// TestSetModelReportsRenderFailureWithoutSelfDeadlock is the third site that
// refreshes the prompt cache under s.mu, and the one an earlier lexical scan of
// mine missed: SetModel's guard clauses unlock and return, which fooled a
// held-lock counter into thinking the region had closed by the time it reached
// the refresh.
//
// That miss is the reason the rule is enforced in the callee rather than at the
// call sites -- but reportPromptRenderFailure still says callers MUST invoke it
// after releasing s.mu, so the rule was RELOCATED, not retired, and each of the
// three unlocked call sites needs its own pin. Moving this one above
// s.mu.Unlock() leaves the whole package green without this test.
//
// Unlike its two siblings this drives a real session, because SetModel resolves
// a profile and rewrites session state before it ever reaches the refresh.
func TestSetModelReportsRenderFailureWithoutSelfDeadlock(t *testing.T) {
	// Built directly rather than through newSession: that helper registers
	// t.Cleanup(sess.Close), and Close needs s.mu -- the exact mutex the failure
	// under test leaves held. The cleanup would then block forever and convert a
	// named 10s failure into a package timeout that reads as a flake. This is the
	// same fixture hazard, one layer down.
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	forceSystemPromptRenderFailure(t)

	awaitOrFail(t, "SetModel with a failing prompt render", func() {
		if err := sess.SetModel("gpt-5.2"); err != nil {
			t.Errorf("SetModel: %v", err)
		}
	})

	if !sawRenderFailureWarning(sess) {
		t.Fatal("no warning emitted: the render failure was swallowed")
	}
	// Closed here rather than in a t.Cleanup, and deliberately: the failure this
	// test exists to catch leaves s.mu held by a parked goroutine, and Close
	// needs that mutex. A cleanup would hang on it and turn a named 10s failure
	// into a package timeout that reads as a flake. Reached only when SetModel
	// returned, so the lock is free.
	sess.Close()
}

// TestConstructionRenderFailureArrivesAfterSessionStart pins the fourth call
// site, which is the one that must NOT emit.
//
// initSessionState refreshes the prompt cache during construction, before
// SESSION_START has been emitted. A diagnostic that reaches the stream first has
// no tracked thread to land on and is lost for good rather than merely reordered
// (see emitSessionStartEnvelope), so this site buffers into
// pendingTranscriptWarnings instead of reporting immediately.
//
// Asserting ORDER rather than presence is what makes this a pin: replacing the
// buffer with a direct report still emits the warning, and only its position
// relative to SESSION_START says which one happened.
func TestConstructionRenderFailureArrivesAfterSessionStart(t *testing.T) {
	forceSystemPromptRenderFailure(t)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sawStart := false
	for {
		select {
		case ev := <-sess.events:
			if ev.Kind == events.EventSessionStart {
				sawStart = true
				continue
			}
			warning, ok := ev.Data.(events.WarningData)
			if !ok || !strings.Contains(warning.Message, "forced render failure") {
				continue
			}
			if !sawStart {
				t.Fatal("the render-failure warning was emitted BEFORE SESSION_START: " +
					"a client has no thread to land it on yet, so it is lost, not reordered")
			}
			return
		default:
			t.Fatal("construction never reported the render failure")
		}
	}
}

// sawRenderFailureWarning drains what the session has emitted and reports
// whether the prompt render failure is among it. SetModel emits a model-changed
// event too, so this cannot just read the first event off the channel.
func sawRenderFailureWarning(s *Session) bool {
	for {
		select {
		case ev := <-s.events:
			if warning, ok := ev.Data.(events.WarningData); ok &&
				strings.Contains(warning.Message, "forced render failure") {
				return true
			}
		default:
			return false
		}
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
