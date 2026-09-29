package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A model call's error can carry a provider's body, which can echo the
// user's own request. The warnings built from those errors reach
// Notification hooks, so each stays bare (#3386): these drive every such
// warning through its real emit path with a body carrying a canary, and
// check the warning names what happened without the body.
const warningCanary = "user-private-prompt"

// canaryStrategy is a context strategy whose calls fail with the canary.
type canaryStrategy struct{}

func (canaryStrategy) Name() string                 { return "canary" }
func (canaryStrategy) Tools() []tool.RegisteredTool { return nil }
func (canaryStrategy) ManageContext(context.Context, *[]schema.Turn, int, func(events.EventKind, events.EventData)) error {
	return errors.New("summarizer said: " + warningCanary)
}
func (canaryStrategy) AfterAction(context.Context, []schema.Turn, *llm.Client) error {
	return errors.New("summarizer said: " + warningCanary)
}

func canarySession(t *testing.T, steps ...func(llm.Request) (llm.Response, error)) (*Session, *fakeErrAdapter) {
	t.Helper()
	client := llm.NewClient()
	adapter := &fakeErrAdapter{name: "canary-provider", steps: steps}
	client.Register(adapter)
	sess, err := NewSession(client, testOpenAICompatProfile("canary-provider", "canary-model", 0), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess, adapter
}

// requireBareWarning finds the warning labelled label and checks it leaves
// the canary out.
func requireBareWarning(t *testing.T, captured []events.SessionEvent, label string) {
	t.Helper()
	found := false
	for _, warning := range warningEvents(captured) {
		if strings.Contains(warning.Message, warningCanary) {
			t.Fatalf("warning %q carries the error body", warning.Message)
		}
		found = found || strings.HasPrefix(warning.Message, label)
	}
	if !found {
		t.Fatalf("no %q warning in %+v", label, warningEvents(captured))
	}
}

func TestModelCallWarningsLeaveTheErrorBodyOut(t *testing.T) {
	t.Parallel()
	body := "blocked by the content filter: messages[1].content began " + warningCanary

	t.Run("content filter retry", func(t *testing.T) {
		t.Parallel()
		sess, _ := canarySession(t, func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("canary-provider", 400, "the response was "+body, nil, nil)
		})
		eventsDone := captureSessionEvents(sess)
		_, _ = sess.ProcessInput(context.Background(), "task", nil)
		sess.Close()
		requireBareWarning(t, <-eventsDone, "Content filter hit")
	})

	t.Run("context length", func(t *testing.T) {
		t.Parallel()
		contextErr := func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("canary-provider", 413, "context length exceeded: "+body, nil, nil)
		}
		sess, _ := canarySession(t, contextErr, contextErr)
		eventsDone := captureSessionEvents(sess)
		sess.strategy = nil
		sess.contextMgr = nil
		_, _ = sess.ProcessInput(context.Background(), "task", nil)
		sess.Close()
		requireBareWarning(t, <-eventsDone, "Context length exceeded")
	})

	t.Run("context strategy", func(t *testing.T) {
		t.Parallel()
		sess, _ := canarySession(t)
		sess.strategy = canaryStrategy{}
		eventsDone := captureSessionEvents(sess)
		_, _ = sess.ProcessInput(context.Background(), "task", nil)
		sess.Close()
		requireBareWarning(t, <-eventsDone, "context strategy error")
	})

	t.Run("after action", func(t *testing.T) {
		t.Parallel()
		sess, _ := canarySession(t)
		sess.strategy = canaryStrategy{}
		eventsDone := captureSessionEvents(sess)
		if err := sess.notifyStrategyAfterAction(context.Background()); err != nil {
			t.Fatalf("notifyStrategyAfterAction: %v", err)
		}
		sess.Close()
		requireBareWarning(t, <-eventsDone, "strategy AfterAction error")
	})
}
