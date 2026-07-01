package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// s2cov_afterActionErrStrategy is a context strategy whose AfterAction always
// errors, so notifyStrategyAfterAction's warning branch runs.
type s2cov_afterActionErrStrategy struct{}

func (s2cov_afterActionErrStrategy) Name() string                 { return "s2cov-afteraction-err" }
func (s2cov_afterActionErrStrategy) Tools() []tool.RegisteredTool { return nil }
func (s2cov_afterActionErrStrategy) ManageContext(_ context.Context, _ *[]schema.Turn, _ int, _ func(events.EventKind, events.EventData)) error {
	return nil
}
func (s2cov_afterActionErrStrategy) AfterAction(_ context.Context, _ []schema.Turn, _ *llm.Client) error {
	return errors.New("afteraction boom")
}

// TestS2Cov_NotifyStrategyAfterAction_WarnsOnError proves a strategy AfterAction
// error surfaces as a warning, not a turn failure.
func TestS2Cov_NotifyStrategyAfterAction_WarnsOnError(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{contextStrategyOverride: s2cov_afterActionErrStrategy{}},
	}))
	var col chanCollector
	go col.drain(sess)

	if err := sess.notifyStrategyAfterAction(context.Background()); err != nil {
		t.Fatalf("notifyStrategyAfterAction returned err = %v, want nil (warning only)", err)
	}

	sess.Close()
	if !col.contains("strategy AfterAction error") {
		t.Fatalf("no AfterAction warning; got %v", col.messages())
	}
}
