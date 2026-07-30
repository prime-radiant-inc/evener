package main

import (
	"encoding/json"
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
		Attempt: 9, MaxAttempts: 11, DelayMS: 60000, ErrorClass: "rate_limit", StatusCode: 429,
	}))

	if m.modelRetry == nil {
		t.Fatal("modelRetry not recorded")
	}
	if m.modelRetry.Attempt != 9 || m.modelRetry.MaxAttempts != 11 {
		t.Errorf("attempt = %d/%d, want 9/11", m.modelRetry.Attempt, m.modelRetry.MaxAttempts)
	}
	if got := composerRetryChip(m.modelRetry); got != "rate limited · retry 9/11 · 60s" {
		t.Errorf("chip = %q, want %q", got, "rate limited · retry 9/11 · 60s")
	}
}

// The retry describes a wait in progress. Once the model produces output the
// wait is over, and a stale "retry 9/11" sitting next to live tokens would be a
// lie the user has no way to discount.
func TestModelRetryClearsWhenTheModelProducesOutput(t *testing.T) {
	m := newSessionHubModel(nil)
	m.applyHubNotification(modelRetryNotification(t, appwire.ThreadModelRetryParams{
		Attempt: 1, MaxAttempts: 11, DelayMS: 1000, ErrorClass: "rate_limit", StatusCode: 429,
	}))
	if m.modelRetry == nil {
		t.Fatal("precondition: modelRetry not recorded")
	}

	raw, err := json.Marshal(appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m.applyHubNotification(appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: raw})

	if m.modelRetry != nil {
		t.Errorf("modelRetry survived live model output: %+v", m.modelRetry)
	}
}

// A non-rate-limit retryable failure must not be labelled a rate limit; the
// user's response to the two differs (wait vs investigate).
func TestComposerRetryChipNamesNonRateLimitCausesGenerically(t *testing.T) {
	chip := composerRetryChip(&appwire.ThreadModelRetryParams{
		Attempt: 2, MaxAttempts: 11, DelayMS: 4000, ErrorClass: "server", StatusCode: 503,
	})
	if chip != "provider error · retry 2/11 · 4s" {
		t.Errorf("chip = %q, want %q", chip, "provider error · retry 2/11 · 4s")
	}
}
