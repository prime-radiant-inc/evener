package contextmgr

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A summarizer's model call can fail with a provider body that echoes the
// conversation it was asked to summarize, and a context strategy's warnings
// reach Notification hooks (#3386). Every warning a failed summarization
// emits names what failed and leaves the body out.
const summarizerCanary = "user-private-prompt"

func canaryManager(t *testing.T) *Manager {
	t.Helper()
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", http.StatusBadRequest, "invalid messages[1].content: "+summarizerCanary, nil, nil)
	}})
	cm := NewManager(testOpenAIProfileWithContextWindow(1000), client, cheapmodel.New(client))
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = 0.0001
	cm.PreserveRecentTurns = 2
	return cm
}

func canaryHistory() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug in auth.go")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("also fix tests")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixing tests")),
		schema.NewTurn(schema.TurnUserInput, llm.User("what's the status")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("almost done")),
	}
}

// warningsFrom returns the emit function a strategy calls and the warnings
// it collected.
func warningsFrom() (func(events.EventKind, events.EventData), *[]events.WarningData) {
	var warnings []events.WarningData
	return func(kind events.EventKind, data events.EventData) {
		if warning, ok := data.(events.WarningData); ok && kind == events.EventWarning {
			warnings = append(warnings, warning)
		}
	}, &warnings
}

func requireBareSummarizerWarning(t *testing.T, warnings []events.WarningData, label string) {
	t.Helper()
	found := false
	for _, warning := range warnings {
		if strings.Contains(warning.Message, summarizerCanary) {
			t.Fatalf("warning %q carries the provider's body", warning.Message)
		}
		found = found || strings.HasPrefix(warning.Message, label)
	}
	if !found {
		t.Fatalf("no %q warning in %+v", label, warnings)
	}
}

func TestSummarizerFailureWarningsLeaveTheProviderBodyOut(t *testing.T) {
	t.Run("MaybeCompact", func(t *testing.T) {
		cm := canaryManager(t)
		cm.CheckpointThreshold = 2.0
		history := canaryHistory()
		emit, warnings := warningsFrom()
		cm.MaybeCompact(context.Background(), &history, 0, emit)
		requireBareSummarizerWarning(t, *warnings, "LLM summarization failed")
	})
	t.Run("ForceCompact", func(t *testing.T) {
		cm := canaryManager(t)
		history := canaryHistory()
		emit, warnings := warningsFrom()
		cm.ForceCompact(context.Background(), &history, "", emit)
		requireBareSummarizerWarning(t, *warnings, "LLM summarization failed")
	})
	t.Run("predictive checkpoint", func(t *testing.T) {
		cm := canaryManager(t)
		cm.SummarizeThreshold = 2.0
		history := canaryHistory()
		emit, warnings := warningsFrom()
		if err := NewCheckpointPredStrategy(cm).ManageContext(context.Background(), &history, 0, emit); err != nil {
			t.Fatalf("ManageContext: %v", err)
		}
		requireBareSummarizerWarning(t, *warnings, "Predictive checkpoint failed")
	})
	t.Run("predictive summarize", func(t *testing.T) {
		cm := canaryManager(t)
		cm.CheckpointThreshold = 2.0
		history := canaryHistory()
		emit, warnings := warningsFrom()
		if err := NewCheckpointPredStrategy(cm).ManageContext(context.Background(), &history, 0, emit); err != nil {
			t.Fatalf("ManageContext: %v", err)
		}
		requireBareSummarizerWarning(t, *warnings, "LLM summarization failed")
	})
	t.Run("session log", func(t *testing.T) {
		cm := canaryManager(t)
		cm.CheckpointThreshold = 2.0
		strategy, err := NewSessionLogStrategy(cm, &fakeStrategyHost{stateDir: t.TempDir(), id: "canary", profile: testOpenAIProfileWithContextWindow(1000)})
		if err != nil {
			t.Fatalf("NewSessionLogStrategy: %v", err)
		}
		history := canaryHistory()
		emit, warnings := warningsFrom()
		if err := strategy.ManageContext(context.Background(), &history, 0, emit); err != nil {
			t.Fatalf("ManageContext: %v", err)
		}
		requireBareSummarizerWarning(t, *warnings, "LLM summarization failed")
	})
}
