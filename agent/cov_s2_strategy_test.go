package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestS2Cov_SelectStrategy_AllNamedStrategies drives NewSession through every
// recognized ContextStrategy so selectStrategy's switch arms are all built, plus
// the unknown-strategy error path.
func TestS2Cov_SelectStrategy_AllNamedStrategies(t *testing.T) {
	t.Parallel()
	for _, strat := range []string{"", "compact", "recall", "session-log", "ooda", "obs-mask", "checkpoint-pred", "memory-crystals", "recursive-distill"} {
		strat := strat
		t.Run("strategy="+strat, func(t *testing.T) {
			t.Parallel()
			sess := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1, ContextStrategy: strat}))
			if sess == nil {
				t.Fatalf("nil session for strategy %q", strat)
			}
		})
	}
}

func TestS2Cov_SelectStrategy_UnknownStrategyFails(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{MaxSubagentDepth: 1, ContextStrategy: "no-such-strategy"})
	if err == nil || !strings.Contains(err.Error(), "unknown context strategy") {
		t.Fatalf("err = %v, want unknown context strategy error", err)
	}
}
