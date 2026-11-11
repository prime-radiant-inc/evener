package contextmgr

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/llm"
)

// summaryRepeatManager builds a manager whose only live compaction layer is
// summarize, driven by the scripted reply function.
func summaryRepeatManager(respFn func(llm.Request) (llm.Response, error)) *Manager {
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: respFn})
	return newCompactionManager(client, minSummarizingWindow, 2, 0.0001)
}

// emptyThenValidReply scripts a model that answers empty while empty is set
// and a valid summary reply once it clears.
func emptyThenValidReply(empty *atomic.Bool) func(llm.Request) (llm.Response, error) {
	return func(llm.Request) (llm.Response, error) {
		if empty.Load() {
			return llm.Response{Message: llm.Assistant("")}, nil
		}
		return llm.Response{Message: llm.Assistant("## Progress\nrecovered-summary-9c1b")}, nil
	}
}

func countSummaryFailureWarnings(warnings []events.WarningData) int {
	count := 0
	for _, warning := range warnings {
		if warning.Code == events.WarningCodeSummaryFailure && warning.SummaryFailure != nil {
			count++
		}
	}
	return count
}

// An automatic pass that fails the same way as the one before it stays quiet:
// the warning is news once per streak, not once per compaction. A summarize
// layer that runs to completion re-arms it, so the next failure warns again.
func TestMaybeCompactSuppressesRepeatedIdenticalSummaryFailure(t *testing.T) {
	t.Parallel()
	empty := &atomic.Bool{}
	empty.Store(true)
	cm := summaryRepeatManager(emptyThenValidReply(empty))
	history := canaryHistory()
	emit, warnings := warningsFrom()

	cm.MaybeCompact(context.Background(), &history, 0, emit)
	cm.MaybeCompact(context.Background(), &history, 0, emit)
	if got := countSummaryFailureWarnings(*warnings); got != 1 {
		t.Fatalf("summary failure warnings = %d, want the first only (warnings %v)", got, *warnings)
	}

	// A completing summarize layer — installed or not — re-arms publication.
	empty.Store(false)
	cm.MaybeCompact(context.Background(), &history, 0, emit)
	if got := countSummaryFailureWarnings(*warnings); got != 1 {
		t.Fatalf("success pass added warnings: %d", got)
	}

	empty.Store(true)
	cm.MaybeCompact(context.Background(), &history, 0, emit)
	if got := countSummaryFailureWarnings(*warnings); got != 2 {
		t.Fatalf("summary failure warnings after re-arm = %d, want 2", got)
	}
}

// A summarize layer that completes without installing anything re-arms
// publication too: on a window too small to summarize, the layer runs as a
// successful no-op, and the next identical automatic failure must warn again.
func TestNoOpSummarizeLayerStillReArmsRepeatPublication(t *testing.T) {
	t.Parallel()
	empty := &atomic.Bool{}
	empty.Store(true)
	cm := summaryRepeatManager(emptyThenValidReply(empty))
	history := canaryHistory()
	emit, warnings := warningsFrom()

	cm.MaybeCompact(context.Background(), &history, 0, emit)
	if got := countSummaryFailureWarnings(*warnings); got != 1 {
		t.Fatalf("summary failure warnings = %d, want the first only (warnings %v)", got, *warnings)
	}

	// A window whose input limit falls below the summarizing minimum makes the
	// summarize layer a successful no-op: it runs to completion, installs
	// nothing, and that completion re-arms publication.
	cm.SetProfile(testOpenAIProfileWithContextWindow(minSummarizingWindow / 2))
	cm.MaybeCompact(context.Background(), &history, 0, emit)
	if got := countSummaryFailureWarnings(*warnings); got != 1 {
		t.Fatalf("no-op pass added warnings: %d", got)
	}

	cm.SetProfile(testOpenAIProfileWithContextWindow(minSummarizingWindow))
	cm.MaybeCompact(context.Background(), &history, 0, emit)
	if got := countSummaryFailureWarnings(*warnings); got != 2 {
		t.Fatalf("summary failure warnings after a no-op completion = %d, want the re-armed failure to warn", got)
	}
}

// The session-log strategy's automatic summarize layer suppresses repeated
// identical failures the same way as the default strategy's pass, so the
// guarantee does not depend on which strategy a session runs.
func TestSessionLogStrategySuppressesRepeatedIdenticalSummaryFailure(t *testing.T) {
	t.Parallel()
	empty := &atomic.Bool{}
	empty.Store(true)
	cm := summaryRepeatManager(emptyThenValidReply(empty))
	strategy, _ := newTestSessionLogStrategy(t, cm)
	history := canaryHistory()
	emit, warnings := warningsFrom()

	for range 2 {
		if err := strategy.ManageContext(context.Background(), &history, 0, emit); err != nil {
			t.Fatalf("ManageContext: %v", err)
		}
	}
	if got := countSummaryFailureWarnings(*warnings); got != 1 {
		t.Fatalf("summary failure warnings = %d, want the first only (warnings %v)", got, *warnings)
	}
}

// Explicit compaction always reports its outcome, and its publication refreshes
// the repeat latch: a later identical automatic failure stays quiet because the
// user just heard this one.
func TestForceCompactAlwaysPublishesSummaryFailure(t *testing.T) {
	t.Parallel()
	empty := &atomic.Bool{}
	empty.Store(true)
	cm := summaryRepeatManager(emptyThenValidReply(empty))
	history := canaryHistory()
	emit, warnings := warningsFrom()

	cm.MaybeCompact(context.Background(), &history, 0, emit)
	if got := countSummaryFailureWarnings(*warnings); got != 1 {
		t.Fatalf("automatic pass = %d warnings, want 1", got)
	}
	cm.ForceCompact(context.Background(), &history, "", emit)
	if got := countSummaryFailureWarnings(*warnings); got != 2 {
		t.Fatalf("explicit compact = %d warnings total, want the explicit one published too", got)
	}
	cm.MaybeCompact(context.Background(), &history, 0, emit)
	if got := countSummaryFailureWarnings(*warnings); got != 2 {
		t.Fatalf("identical automatic failure after explicit publication = %d warnings, want it suppressed", got)
	}
}

// A failure with a different signature than the previous one is news again,
// whatever the previous one was: a different validation reason, or a provider
// failure carrying a different HTTP status on the same route.
func TestDifferentSummaryFailureSignatureWarnsAgain(t *testing.T) {
	t.Parallel()
	t.Run("validation reason", func(t *testing.T) {
		t.Parallel()
		empty := &atomic.Bool{}
		empty.Store(true)
		cm := summaryRepeatManager(func(llm.Request) (llm.Response, error) {
			if empty.Load() {
				return llm.Response{Message: llm.Assistant("")}, nil
			}
			return llm.Response{Message: llm.Assistant("cut off"), Finish: llm.FinishReason{Reason: llm.FinishReasonLength}}, nil
		})
		history := canaryHistory()
		emit, warnings := warningsFrom()

		cm.MaybeCompact(context.Background(), &history, 0, emit)
		empty.Store(false)
		cm.MaybeCompact(context.Background(), &history, 0, emit)
		if got := countSummaryFailureWarnings(*warnings); got != 2 {
			t.Fatalf("summary failure warnings = %d, want both signatures published", got)
		}
		reasons := []string{}
		for _, warning := range *warnings {
			if warning.SummaryFailure != nil {
				reasons = append(reasons, warning.SummaryFailure.Terminal.Reason)
			}
		}
		if len(reasons) == 2 && reasons[0] == reasons[1] {
			t.Fatalf("warnings carry the same signature: %v", reasons)
		}
	})
	t.Run("provider status", func(t *testing.T) {
		t.Parallel()
		var status atomic.Int32
		status.Store(503)
		cm := summaryRepeatManager(func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", int(status.Load()), "unavailable", nil, nil)
		})
		history := canaryHistory()
		emit, warnings := warningsFrom()

		cm.MaybeCompact(context.Background(), &history, 0, emit)
		status.Store(500)
		cm.MaybeCompact(context.Background(), &history, 0, emit)
		if got := countSummaryFailureWarnings(*warnings); got != 2 {
			t.Fatalf("summary failure warnings = %d, want both statuses published", got)
		}
		published := []int{}
		for _, warning := range *warnings {
			if warning.SummaryFailure != nil {
				published = append(published, warning.SummaryFailure.Terminal.Status)
			}
		}
		if len(published) == 2 && published[0] == published[1] {
			t.Fatalf("warnings carry the same status: %v", published)
		}
	})
}

// The latch helpers tolerate concurrent compaction publishers (the explicit
// /compact racing the round loop's pass is documented behavior), so they
// serialize on the manager's mutex and always leave a valid stored signature.
func TestSummaryFailureRepeatLatchConcurrentAccess(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	cm := NewManager(testOpenAIProfileWithContextWindow(minSummarizingWindow), client, cheapmodel.New(client))
	emptyReply := &diagnostic.SummaryFailure{Terminal: diagnostic.SummaryAttempt{Route: "session", Kind: "rejected_output", Reason: "empty_reply", Status: 429, Attempts: 3}}
	outputLimit := &diagnostic.SummaryFailure{Terminal: diagnostic.SummaryAttempt{Route: "session", Kind: "rejected_output", Reason: "output_limit", Status: 500, Attempts: 1}}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				cm.claimSummaryFailureRepeat(emptyReply)
				cm.publishSummaryFailureRepeat(outputLimit)
				cm.clearSummaryFailureRepeat()
			}
		})
	}
	wg.Wait()

	// Whatever the interleaving stored, a cleared latch claims any signature.
	cm.clearSummaryFailureRepeat()
	if !cm.claimSummaryFailureRepeat(emptyReply) {
		t.Fatal("claim after clear refused")
	}
	if cm.claimSummaryFailureRepeat(emptyReply) {
		t.Fatal("identical repeat claimed twice")
	}
	// Status and attempts are part of the identity: they say what the
	// warning would say.
	if !cm.claimSummaryFailureRepeat(&diagnostic.SummaryFailure{Terminal: diagnostic.SummaryAttempt{Route: "session", Kind: "rejected_output", Reason: "empty_reply", Status: 500, Attempts: 3}}) {
		t.Fatal("a failure differing only in status was suppressed as a repeat")
	}
}
