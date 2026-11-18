package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// forceSystemPromptRenderFailure makes the embedded template renderer fail, so
// renderSystemPrompt takes its warning path. The renderer is a package var
// precisely so a test can do this; it is the only way to reach that path,
// because the templates it renders are compiled into the binary.
func forceSystemPromptRenderFailure(t *testing.T) {
	old := executeSystemPromptTemplate
	executeSystemPromptTemplate = func(promptData) (string, error) {
		return "", errors.New("forced render failure")
	}
	t.Cleanup(func() { executeSystemPromptTemplate = old })
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
	// TRIPWIRE: fn is a single in-process session call (SetModel,
	// registerTool, swapEnvAndRefresh) with no real I/O; it normally returns
	// in well under a second. 30s only fires on a genuine self-deadlock, not
	// scheduler contention under a loaded suite.
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not return within 30s: it is deadlocked, not slow", what)
	}
}

// Mutations update the session's live capabilities and environment. Every
// provider request keeps the prompt established at construction.
func TestSessionChangesPreserveSystemPrompt(t *testing.T) {
	s := newSession(t)
	startup := s.cachedSystemPrompt
	render := executeSystemPromptTemplate
	calls := 0
	executeSystemPromptTemplate = func(data promptData) (string, error) {
		calls++
		return render(data)
	}
	t.Cleanup(func() { executeSystemPromptTemplate = render })

	for _, tc := range []struct {
		name   string
		change func() error
	}{
		{"model", func() error { return s.SetModel("gpt-5.4") }},
		{"tool", func() error {
			s.registerTool("probe", "d", map[string]any{}, func(context.Context, any) (any, error) { return nil, nil })
			return nil
		}},
		{"directory", func() error {
			next := s.currentEnv().(*execenv.LocalExecutionEnvironment).WithWorkingDirectory(t.TempDir())
			return s.swapEnvAndRefresh(next, nil)
		}},
		{"set goal", func() error { _, err := s.SetGoal(t.Context(), "goal-27"); return err }},
		{"clear goal", s.ClearGoal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			awaitOrFail(t, tc.name, func() {
				if err := tc.change(); err != nil {
					t.Errorf("change session: %v", err)
				}
			})
			if calls != before || s.cachedSystemPrompt != startup {
				t.Errorf("session change rendered the system prompt %d times, identical bytes = %t", calls-before, s.cachedSystemPrompt == startup)
			}
		})
	}
}

// Startup diagnostics are delivered after the event that establishes the session.
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
