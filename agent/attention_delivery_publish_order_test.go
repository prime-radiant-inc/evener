package agent

import (
	"testing"

	"primeradiant.com/evener/agent/transcript"
)

// TestADeliveryPublishesItsReportOnlyAfterItsAcknowledgment pins the order that
// makes a delivery's report and the release it owes one fact.
//
// A delivered child report is two facts at once: it raises the release floor a
// stop's quiet is read against, and it is the row that release frees an earlier
// report from. The acknowledgment is what raises the floor. Publishing the
// report into resident history before the acknowledgment lets a request build
// in the window — it copies the new report from history while the floor it
// needs is still unraised, so it presents the child's report and withholds the
// earlier report that same acknowledgment released. No resident release ever
// produces that order.
//
// The assertion reads resident history at the instant the report is durably
// recorded and the acknowledgment has not yet committed: the state a projection
// landing in the window would copy. A test that read history only after the
// delivery returned could not tell the two orders apart.
func TestADeliveryPublishesItsReportOnlyAfterItsAcknowledgment(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "CHILD_REPORT")
	if len(plans.deliveries) != 1 {
		t.Fatalf("deliveries=%d, want 1", len(plans.deliveries))
	}
	plan := plans.deliveries[0]

	// A resident receiver: the delegate is live, so the delivery appends into a
	// real session history — the history an automatic request projects.
	runtime := newSession(t, withProfile(namedInstanceProfile("ant", "anthropic", "claude-sonnet-4-5")), withDir(c.stateDir), withConfig(SessionConfig{StateDir: c.stateDir}))
	runtime.id = "child-dlg_target"
	runtime.stateDir = c.stateDir
	runtime.delegateController = c
	runtime.owningDelegateID = "dlg_target"
	writer, err := transcript.NewWriter(transcriptPath(c.stateDir, runtime.id), transcript.Header{SessionID: runtime.id})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	runtime.attachTranscript(writer, nil)
	c.mu.Lock()
	c.live["dlg_target"] = &delegateLiveState{runtime: runtime}
	c.mu.Unlock()

	reportID := delegateAttentionID(plan.deliveryID)
	// The window: the report is durable, the acknowledgment has not landed.
	publishedBeforeAck := false
	observedWindow := false
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.delegateAttentionAfterReportDeposit = func() {
			observedWindow = true
			runtime.mu.Lock()
			defer runtime.mu.Unlock()
			for _, turn := range runtime.history {
				if turn.AttentionID == reportID {
					publishedBeforeAck = true
				}
			}
		}
	})

	if _, err := deliverDelegatePacket(plan, runtime); err != nil {
		t.Fatalf("deliver the child report: %v", err)
	}
	if !observedWindow {
		t.Fatalf("premise broken: the deposit seam never ran, so this test cannot tell publishing before the acknowledgment from publishing after it")
	}
	if publishedBeforeAck {
		t.Fatalf("the report %q entered resident history before its acknowledgment advanced the release floor: a request built in that window presents the child's report while withholding the earlier report the same acknowledgment releases", reportID)
	}
	// And after the acknowledgment it is published, so the two facts arrive
	// together rather than the row being lost.
	runtime.mu.Lock()
	published := false
	for _, turn := range runtime.history {
		if turn.AttentionID == reportID {
			published = true
		}
	}
	runtime.mu.Unlock()
	if !published {
		t.Fatalf("the report %q was never published into resident history after its acknowledgment: the request that must present it cannot carry it", reportID)
	}
}
