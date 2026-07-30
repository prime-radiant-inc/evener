package main

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

// kata e79v: composerRetryChip formatting and hubModel.modelRetry recording were
// each covered, and the wiring BETWEEN them was not — so a retry could be
// recorded, formatted correctly, and still never reach the rendered strip. This
// closes that gap: notification in, rendered composer view out.
func TestComposerPanelRendersModelRetryFromNotification(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "serf"
	m.detail.Model = "ratelimited/fake-model"
	m.width = 200

	raw, err := json.Marshal(appwire.ThreadModelRetryParams{
		Attempt: 9, MaxAttempts: 11, DelayMS: 8000, ErrorClass: "rate_limit", StatusCode: 429,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m.applyHubNotification(appwire.Notification{Method: appwire.NotifySerfThreadModelRetry, Params: raw})
	if m.modelRetry == nil {
		t.Fatal("precondition: notification did not record modelRetry")
	}

	view := m.sessionComposerPanel().View()
	if !strings.Contains(view, "rate limited") {
		t.Errorf("composer view does not surface the retry.\nview:\n%s", view)
	}
	if !strings.Contains(view, "retry 9/11") {
		t.Errorf("composer view lacks the attempt position.\nview:\n%s", view)
	}
}
