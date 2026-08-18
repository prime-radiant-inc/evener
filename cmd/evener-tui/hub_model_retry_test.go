package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func modelRetryNotification(t *testing.T, params appwire.ThreadModelRetryParams) appwire.Notification {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return appwire.Notification{Method: appwire.NotifySerfThreadModelRetry, Params: raw}
}

// itemLifecycleNotification builds an item/completed notification carrying
// the given item type, for exercising clearModelRetryOnProgress's
// per-item-kind rule.
func itemLifecycleNotification(t *testing.T, method, itemType string) appwire.Notification {
	t.Helper()
	raw, err := json.Marshal(appwire.ItemLifecycleParams{Item: appwire.ThreadItem{Type: itemType}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return appwire.Notification{Method: method, Params: raw}
}

// kata e79v: the daemon reports a model-call retry on serf/thread/modelRetry
// (kata 4zn8). The TUI ignored the method outright, so a rate-limited session
// looked identical to a wedged one — the exact symptom 4zn8 fixed for the web
// client, still live in the other one.
//
// Held as ephemeral state rather than transcript lines, matching the web
// client's decision for the same reason: one rate-limited session logged 91
// retries in four hours, and 91 transcript rows is noise. The chip strip
// re-renders from this every frame and the next retry supersedes it.
func TestApplyHubNotificationRecordsModelRetry(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 9, MaxAttempts: 11, AttemptCap: 11, DelayMS: 60000, ErrorClass: "rate_limit", StatusCode: 429,
	}))

	if m.modelRetry == nil {
		t.Fatal("modelRetry not recorded")
	}
	if m.modelRetry.Attempt != 9 || m.modelRetry.AttemptCap != 11 {
		t.Errorf("attempt = %d/%d, want 9/11", m.modelRetry.Attempt, m.modelRetry.AttemptCap)
	}
	if got := composerRetryChip(m.modelRetry, "", false); got != "rate limited — attempt 9/11 — retrying in 60s — 0s on this call" {
		t.Errorf("chip = %q, want %q", got, "rate limited — attempt 9/11 — retrying in 60s — 0s on this call")
	}
}

// The retry describes a wait — or a grind — in progress. A user watching a
// provider chew through retries needs the chip to stay up while deltas
// flow, or the indicator flickers away and reads as "stuck" rather than
// "working" (the vanishing-chip bug this component exists to fix). Only
// turn boundaries and a model-output item's completion end that wait.
func TestModelRetrySurvivesModelOutputDelta(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	if m.modelRetry == nil {
		t.Fatal("precondition: modelRetry not recorded")
	}

	raw, err := json.Marshal(appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m.applyHubNotification(appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: raw})

	if m.modelRetry == nil {
		t.Error("modelRetry cleared on an assistant delta; deltas must not clear the chip")
	}
}

// Reasoning and tool-output deltas are the same "still grinding" signal as
// an assistant delta and must not clear the chip either.
func TestModelRetrySurvivesReasoningAndToolOutputDeltas(t *testing.T) {
	for _, method := range []string{appwire.NotifyReasoningSummaryDelta, appwire.NotifyToolOutputDelta} {
		m := newSessionHubModel(nil)
		m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
			Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
		}))
		if m.modelRetry == nil {
			t.Fatalf("precondition: modelRetry not recorded for %s", method)
		}

		var raw json.RawMessage
		var err error
		if method == appwire.NotifyReasoningSummaryDelta {
			raw, err = json.Marshal(appwire.ReasoningSummaryDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "thinking"})
		} else {
			raw, err = json.Marshal(appwire.ToolOutputDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "output"})
		}
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		m.applyHubNotification(appwire.Notification{Method: method, Params: raw})

		if m.modelRetry == nil {
			t.Errorf("modelRetry cleared on %s; deltas must not clear the chip", method)
		}
	}
}

// A systemMessage completing mid-grind (a user steering "are you stuck?")
// must not clear the chip — that is exactly the vanishing-chip bug this
// component exists to fix.
func TestModelRetrySurvivesSystemMessageItemCompletion(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	if m.modelRetry == nil {
		t.Fatal("precondition: modelRetry not recorded")
	}

	m.applyHubNotification(itemLifecycleNotification(t, appwire.NotifyItemCompleted, "systemMessage"))

	if m.modelRetry == nil {
		t.Error("modelRetry cleared on a systemMessage item completion; only model-output items may clear it")
	}
}

// item/started always precedes the deltas for the item it announces — if it
// cleared the chip, the retried call's own first delta would find modelRetry
// already nil, silently defeating the delta-survival rule above. Only a
// model-output item's COMPLETION (or a turn boundary) may clear it.
func TestModelRetrySurvivesModelOutputItemStart(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	if m.modelRetry == nil {
		t.Fatal("precondition: modelRetry not recorded")
	}

	m.applyHubNotification(itemLifecycleNotification(t, appwire.NotifyItemStarted, "agentMessage"))

	if m.modelRetry == nil {
		t.Error("modelRetry cleared on item/started; only completion of a model-output item may clear it")
	}
}

// A user-input item completing must not clear the chip either — it is not
// evidence the model call finished.
func TestModelRetrySurvivesUserItemCompletion(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	if m.modelRetry == nil {
		t.Fatal("precondition: modelRetry not recorded")
	}

	m.applyHubNotification(itemLifecycleNotification(t, appwire.NotifyItemCompleted, "userMessage"))

	if m.modelRetry == nil {
		t.Error("modelRetry cleared on a userMessage item completion; only model-output items may clear it")
	}
}

// Completion of a model-output item (assistant message, reasoning, tool
// call) is the actual signal the call finished, and must clear the chip.
func TestModelRetryClearsOnModelOutputItemCompletion(t *testing.T) {
	for _, itemType := range []string{"agentMessage", "reasoning", "commandExecution"} {
		m := newSessionHubModel(nil)
		m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
			Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
		}))
		if m.modelRetry == nil {
			t.Fatalf("precondition: modelRetry not recorded for %s", itemType)
		}

		m.applyHubNotification(itemLifecycleNotification(t, appwire.NotifyItemCompleted, itemType))

		if m.modelRetry != nil {
			t.Errorf("modelRetry survived completion of a %s item; model-output completion must clear it", itemType)
		}
	}
}

// Turn boundaries always end the wait, regardless of item kind.
func TestModelRetryClearsOnTurnBoundaries(t *testing.T) {
	for _, method := range []string{appwire.NotifyTurnCompleted, appwire.NotifyTurnStarted} {
		m := newSessionHubModel(nil)
		m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
			Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
		}))
		if m.modelRetry == nil {
			t.Fatalf("precondition: modelRetry not recorded for %s", method)
		}

		raw, err := json.Marshal(appwire.TurnCompletedParams{TurnID: "turn_1"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		m.applyHubNotification(appwire.Notification{Method: method, Params: raw})

		if m.modelRetry != nil {
			t.Errorf("modelRetry survived %s; turn boundaries must clear it", method)
		}
	}
}

// A non-rate-limit retryable failure must not be labelled a rate limit; the
// user's response to the two differs (wait vs investigate).
func TestComposerRetryChipNamesNonRateLimitCausesGenerically(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 2, MaxAttempts: 11, AttemptCap: 11, DelayMS: 4000, ErrorClass: "server", StatusCode: 503,
	}, "", false)
	if want := "provider error — attempt 2/11 — retrying in 4s — 0s on this call"; chip != want {
		t.Errorf("chip = %q, want %q", chip, want)
	}
}

// The denominator is AttemptCap, not the raw policy budget (MaxAttempts):
// once a group has a consume-phase failure the effective bound drops to an
// early-stop count, and rendering the untouched policy max would promise
// patience the budget won't deliver.
func TestComposerRetryChipUsesAttemptCapAsDenominator(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 3, MaxAttempts: 11, AttemptCap: 4, DelayMS: 0, ErrorClass: "server", StatusCode: 503,
	}, "", false)
	if !strings.Contains(chip, "attempt 3/4") {
		t.Errorf("chip = %q, want it to contain %q", chip, "attempt 3/4")
	}
}

// A chain walk (a fallback taking over the call) resets the attempt count;
// without the model tag a user cannot tell "same model, still failing" from
// "now trying a different model".
func TestComposerRetryChipShowsModelTagOnFallback(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 0, ErrorClass: "server", StatusCode: 503,
		Model: "anthropic/claude-opus-4",
	}, "openai/gpt-5", false)
	if !strings.Contains(chip, "provider error (anthropic/claude-opus-4)") {
		t.Errorf("chip = %q, want it to contain the fallback model tag immediately after the cause", chip)
	}
}

// No tag when the retry is still on the session's primary model — tagging
// every retry would bury the one signal (a fallback) that actually matters.
func TestComposerRetryChipOmitsModelTagWhenSameAsPrimary(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 0, ErrorClass: "server", StatusCode: 503,
		Model: "openai/gpt-5",
	}, "openai/gpt-5", false)
	if strings.Contains(chip, "(openai/gpt-5)") {
		t.Errorf("chip = %q, want no model tag when retry.Model matches the primary model", chip)
	}
}

// GroupElapsedMS is wall-clock time since the retry group's first attempt —
// rendered so a user can tell how long the current model call has actually
// been running, independent of the attempt count.
func TestComposerRetryChipRendersElapsedMinutes(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 3, MaxAttempts: 11, AttemptCap: 11, DelayMS: 0, ErrorClass: "server", StatusCode: 503,
		GroupElapsedMS: 14 * 60 * 1000,
	}, "", false)
	if !strings.Contains(chip, "14m on this call") {
		t.Errorf("chip = %q, want it to contain %q", chip, "14m on this call")
	}
}

// formatExactGap matches the web reference's own formatExactGap
// (liveness.ts) case for case: whole seconds under a minute, whole minutes
// with no trailing seconds on an exact minute, and whole minutes plus
// remainder seconds otherwise — always floored, never rounded.
func TestFormatExactGap(t *testing.T) {
	cases := []struct {
		gapMS int64
		want  string
	}{
		{5_000, "5s"},
		{59_000, "59s"},
		{180_000, "3m"},
		{185_000, "3m 5s"},
		{185_999, "3m 5s"},
	}
	for _, tc := range cases {
		if got := formatExactGap(tc.gapMS); got != tc.want {
			t.Errorf("formatExactGap(%d) = %q, want %q", tc.gapMS, got, tc.want)
		}
	}
}

// While the chip is up and deltas are flowing, the reported delay has already
// expired: rendering it asserts a countdown that is over, and GroupElapsedMS
// is a frozen server snapshot, so "retrying in 45s — 0m on this call" is a
// false statement about a call that has been streaming for minutes. The wait
// reads "in progress" instead — the same rule the web client applies.
func TestComposerRetryChipReadsInProgressWhileStreaming(t *testing.T) {
	retry := &appwire.ThreadModelRetryParams{
		Attempt: 2, MaxAttempts: 11, AttemptCap: 4, DelayMS: 45000, ErrorClass: "server", StatusCode: 503,
		GroupElapsedMS: 14 * 60 * 1000,
	}
	if want := "provider error — attempt 2/4 — retrying in 45s — 14m on this call"; composerRetryChip(retry, "", false) != want {
		t.Errorf("waiting chip = %q, want %q", composerRetryChip(retry, "", false), want)
	}
	if want := "provider error — attempt 2/4 — in progress — 14m on this call"; composerRetryChip(retry, "", true) != want {
		t.Errorf("in-progress chip = %q, want %q", composerRetryChip(retry, "", true), want)
	}
}

// A delta is the retried call producing output again, which ends the wait the
// delay describes. The chip stays up (clearing it is the vanishing-chip bug)
// but must stop counting down.
func TestModelRetryChipReadsInProgressAfterDelta(t *testing.T) {
	for _, method := range []string{appwire.NotifyAgentMessageDelta, appwire.NotifyReasoningSummaryDelta, appwire.NotifyToolOutputDelta} {
		m := newSessionHubModel(nil)
		m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
			Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 45000, ErrorClass: "rate_limit", StatusCode: 429,
		}))
		if m.modelRetryInProgress {
			t.Fatalf("a freshly reported retry is a wait, not progress (%s)", method)
		}

		var raw json.RawMessage
		var err error
		switch method {
		case appwire.NotifyReasoningSummaryDelta:
			raw, err = json.Marshal(appwire.ReasoningSummaryDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "thinking"})
		case appwire.NotifyToolOutputDelta:
			raw, err = json.Marshal(appwire.ToolOutputDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "output"})
		default:
			raw, err = json.Marshal(appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "hello"})
		}
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		m.applyHubNotification(appwire.Notification{Method: method, Params: raw})

		if m.modelRetry == nil {
			t.Fatalf("modelRetry cleared on %s; deltas must not clear the chip", method)
		}
		if got := composerRetryChip(m.modelRetry, "", m.modelRetryInProgress); !strings.Contains(got, "in progress") {
			t.Errorf("chip after %s = %q, want it to read %q", method, got, "in progress")
		}
	}
}

// A newly reported retry is a fresh wait: the countdown must come back even
// though the previous attempt had started streaming.
func TestModelRetryChipCountsDownAgainAfterANewRetry(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	raw, err := json.Marshal(appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m.applyHubNotification(appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: raw})
	if !m.modelRetryInProgress {
		t.Fatal("precondition: delta did not mark the retry in progress")
	}

	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 2, MaxAttempts: 11, AttemptCap: 11, DelayMS: 8000, ErrorClass: "rate_limit", StatusCode: 429,
	}))

	if m.modelRetryInProgress {
		t.Error("a newly reported retry must read as a wait again, not as in progress")
	}
	if got := composerRetryChip(m.modelRetry, "", m.modelRetryInProgress); !strings.Contains(got, "— retrying in 8s —") {
		t.Errorf("chip = %q, want the new retry's countdown", got)
	}
}

// A hub older than this client sends no AttemptCap at all; rendering the zero
// value gives "attempt 2/0", the dishonest denominator the cap field exists to
// eliminate. The policy budget is the honest fallback.
func TestComposerRetryChipFallsBackToMaxAttemptsWhenCapMissing(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 2, MaxAttempts: 11, AttemptCap: 0, DelayMS: 4000, ErrorClass: "server", StatusCode: 503,
	}, "", false)
	if !strings.Contains(chip, "attempt 2/11") {
		t.Errorf("chip = %q, want it to contain %q", chip, "attempt 2/11")
	}
}

// When a hub sends neither AttemptCap nor MaxAttempts, there is no honest
// denominator at all — "attempt 3/0" asserts a budget of zero, which is
// false. The attempt count renders bare instead.
func TestComposerRetryChipOmitsDenominatorWhenNoBoundKnown(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 3, MaxAttempts: 0, AttemptCap: 0, DelayMS: 4000, ErrorClass: "server", StatusCode: 503,
	}, "", false)
	if !strings.Contains(chip, "attempt 3") || strings.Contains(chip, "attempt 3/") {
		t.Errorf("chip = %q, want it to contain bare %q and no %q", chip, "attempt 3", "attempt 3/")
	}
}

// The chip describes the VIEWED session's model call, so only that session's
// deltas end its wait. A delta from another session is no evidence this call
// resumed streaming, and marking it in progress would replace the countdown —
// the load-bearing part of the chip — with a standing false claim for any user
// who has a second, busy session open.
func TestModelRetryInProgressIgnoresForeignSessionDeltas(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 2, MaxAttempts: 11, AttemptCap: 11, DelayMS: 45000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	if m.modelRetry == nil {
		t.Fatal("precondition: modelRetry not recorded")
	}

	raw, err := json.Marshal(appwire.AgentMessageDeltaParams{
		Ref: "local:01OTHER", ThreadID: "01OTHER", TurnID: "turn_1", ItemID: "item_1", Delta: "hello",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m.applyHubNotification(appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: raw})

	if m.modelRetryInProgress {
		t.Error("a delta from another session marked the viewed session's retry in progress")
	}
	if got := composerRetryChip(m.modelRetry, "", m.modelRetryInProgress); !strings.Contains(got, "— retrying in 45s —") {
		t.Errorf("chip = %q, want the countdown intact after a foreign delta", got)
	}
}

// A freshly reported retry stamps modelRetryReceivedAt — the TUI-side
// receivedAt applyModelRetryTick compares against DelayMS. Without a fresh
// stamp on every retry, a chain-walk's new retry would inherit a stale
// timestamp and could read as already elapsed the instant it arrives.
func TestApplyHubNotificationStampsModelRetryReceivedAt(t *testing.T) {
	m := newSessionHubModel(nil)
	before := time.Now()
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 45000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	after := time.Now()

	if m.modelRetryReceivedAt.Before(before) || m.modelRetryReceivedAt.After(after) {
		t.Errorf("modelRetryReceivedAt = %v, want between %v and %v", m.modelRetryReceivedAt, before, after)
	}
}

// A newly reported retry must arm the timer half of the in-progress OR, or a
// reader who watches a chain-walk's new retry with no further deltas would
// never see the second retry's own wait resolve either.
func TestApplyHubNotificationSchedulesModelRetryTick(t *testing.T) {
	m := newSessionHubModel(nil)
	cmd := m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 45000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	if cmd == nil {
		t.Fatal("a newly reported retry did not schedule a re-check tick")
	}
}

// This is the actual gap the tick exists to close: a reader who gets no
// further deltas during the wait must still see the chip flip once DelayMS
// has genuinely elapsed, not stay wedged on a stale countdown forever.
func TestApplyModelRetryTickFlipsInProgressOnceDelayElapses(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	// Back-date receivedAt past the reported delay instead of sleeping —
	// same "set the timestamp directly" pattern hub_model_test.go already
	// uses for lastCtrlC's window check.
	m.modelRetryReceivedAt = time.Now().Add(-2 * time.Second)

	cmd := m.applyModelRetryTick()

	if !m.modelRetryInProgress {
		t.Error("modelRetryInProgress still false after DelayMS elapsed with no delta")
	}
	if cmd != nil {
		t.Error("tick rescheduled itself after the wait resolved; it should stop once flipped")
	}
}

// While the delay has not yet elapsed, the tick must keep re-checking (return
// a non-nil reschedule cmd) rather than giving up after one look.
func TestApplyModelRetryTickReschedulesWhileWaitStillOpen(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 60000, ErrorClass: "rate_limit", StatusCode: 429,
	}))

	cmd := m.applyModelRetryTick()

	if m.modelRetryInProgress {
		t.Error("modelRetryInProgress went true before DelayMS elapsed")
	}
	if cmd == nil {
		t.Error("tick did not reschedule itself while the wait is still open")
	}
}

// A retry already marked in progress by a delta (markModelRetryInProgress)
// needs no further ticking — the tick loop must actually stop, not run
// forever after the chip has resolved.
func TestApplyModelRetryTickNoOpWhenAlreadyInProgress(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 60000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	m.modelRetryInProgress = true

	if cmd := m.applyModelRetryTick(); cmd != nil {
		t.Error("tick rescheduled itself for a retry already marked in progress")
	}
}

// With no pending retry at all, a stray tick (e.g. one already in flight when
// the retry resolved through some other path) must be a harmless no-op.
func TestApplyModelRetryTickNoOpWithoutPendingRetry(t *testing.T) {
	m := newSessionHubModel(nil)

	if cmd := m.applyModelRetryTick(); cmd != nil {
		t.Error("tick rescheduled itself with no pending retry to watch")
	}
}

// The tick must actually be wired into Update — modelRetryTickMsg is a real
// tea.Msg some caller has to route, not just a function nothing ever calls.
func TestHubModelUpdateAppliesModelRetryTick(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	m.modelRetryReceivedAt = time.Now().Add(-2 * time.Second)

	updated, cmd := m.Update(modelRetryTickMsg{})

	next, ok := updated.(hubModel)
	if !ok {
		t.Fatalf("Update returned %T, want hubModel", updated)
	}
	if !next.modelRetryInProgress {
		t.Error("Update(modelRetryTickMsg{}) did not flip modelRetryInProgress once DelayMS elapsed")
	}
	if cmd != nil {
		t.Error("Update(modelRetryTickMsg{}) rescheduled the tick after the wait resolved")
	}
}
