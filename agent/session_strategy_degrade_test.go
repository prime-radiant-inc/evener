package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// toggledDegradeStrategy is a context strategy whose AfterAction can be
// switched between failing and succeeding, so a test can drive a
// degrade / recover / degrade sequence through the session's warning path.
type toggledDegradeStrategy struct{ fail atomic.Bool }

func (s *toggledDegradeStrategy) Name() string { return "toggled-degrade" }
func (s *toggledDegradeStrategy) Tools() []tool.RegisteredTool {
	return nil
}
func (s *toggledDegradeStrategy) ManageContext(context.Context, *[]schema.Turn, int, func(events.EventKind, events.EventData)) error {
	return nil
}
func (s *toggledDegradeStrategy) AfterAction(context.Context, []schema.Turn, *llm.Client) error {
	if s.fail.Load() {
		return errors.New("auxiliary summarization failed")
	}
	return nil
}

// TestNotifyStrategyAfterAction_DedupsRepeatedFailureAndClearsOnRecovery pins
// the strategy degradation contract: a persistently failing opt-in strategy
// emits exactly one warning per failing streak (no flood), while a successful
// AfterAction clears the degraded state so a later failure warns again. The
// main turn is never failed — notifyStrategyAfterAction returns nil.
func TestNotifyStrategyAfterAction_DedupsRepeatedFailureAndClearsOnRecovery(t *testing.T) {
	t.Parallel()
	toggle := &toggledDegradeStrategy{}
	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{contextStrategyOverride: toggle},
	}))
	col := newChanCollector()
	go col.drain(sess)

	// Three consecutive failures of the same streak produce one warning.
	toggle.fail.Store(true)
	for range 3 {
		if err := sess.notifyStrategyAfterAction(context.Background()); err != nil {
			t.Fatalf("notifyStrategyAfterAction returned err = %v, want nil (warning only)", err)
		}
	}

	// A successful AfterAction clears the degraded state.
	toggle.fail.Store(false)
	if err := sess.notifyStrategyAfterAction(context.Background()); err != nil {
		t.Fatalf("recovery call returned err = %v, want nil", err)
	}

	// The next failure is a new streak and warns again.
	toggle.fail.Store(true)
	if err := sess.notifyStrategyAfterAction(context.Background()); err != nil {
		t.Fatalf("post-recovery failure returned err = %v, want nil", err)
	}

	sess.Close()
	<-col.done

	if got := len(col.warnings()); got != 2 {
		t.Fatalf("expected exactly 2 degradation warnings (one per failing streak), got %d: %v", got, col.messages())
	}
}
