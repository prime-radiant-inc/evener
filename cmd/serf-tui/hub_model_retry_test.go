package main

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
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
	if got := composerRetryChip(m.modelRetry, ""); got != "rate limited · attempt 9/11 · 60s · 0m on this call" {
		t.Errorf("chip = %q, want %q", got, "rate limited · attempt 9/11 · 60s · 0m on this call")
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
	}, "")
	if want := "provider error · attempt 2/11 · 4s · 0m on this call"; chip != want {
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
	}, "")
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
	}, "openai/gpt-5")
	if !strings.Contains(chip, "· anthropic/claude-opus-4") {
		t.Errorf("chip = %q, want it to contain the fallback model tag", chip)
	}
}

// No tag when the retry is still on the session's primary model — tagging
// every retry would bury the one signal (a fallback) that actually matters.
func TestComposerRetryChipOmitsModelTagWhenSameAsPrimary(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, AttemptCap: 11, DelayMS: 0, ErrorClass: "server", StatusCode: 503,
		Model: "openai/gpt-5",
	}, "openai/gpt-5")
	if strings.Contains(chip, "gpt-5 ·") {
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
	}, "")
	if !strings.Contains(chip, "14m on this call") {
		t.Errorf("chip = %q, want it to contain %q", chip, "14m on this call")
	}
}
