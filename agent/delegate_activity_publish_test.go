package agent

import (
	"testing"
	"time"
)

// TestReportActivityCoalescesActivityOnlyPublications pins the publication
// rate of a streaming delegate's activity. The child reports activity for
// every streamed delta; each publication is a DELEGATE_UPDATED that the root
// re-samples its diagnostics for and forwards to every client, so an
// activity-only advance publishes at most once per
// delegateActivityPublishInterval. The recorded activity stays exact for the
// quiet watchdog and for the next snapshot that is published.
func TestReportActivityCoalescesActivityOnlyPublications(t *testing.T) {
	_, controller, lease, clock := newStableQuietSupervisionHarness(t)
	var published []time.Time
	controller.emitUpdate = func(plan delegateUpdatePlan) {
		for _, row := range plan.rows {
			published = append(published, row.latestActivityAt)
		}
	}
	start := clock.Now()
	report := func(at time.Time) {
		t.Helper()
		if err := controller.ReportActivity(lease, at); err != nil {
			t.Fatalf("ReportActivity: %v", err)
		}
	}

	var last time.Time
	for i := 1; i <= 200; i++ { // a burst of deltas, 1ms apart
		last = start.Add(time.Duration(i) * time.Millisecond)
		report(last)
	}
	if len(published) != 1 || !published[0].Equal(start.Add(time.Millisecond)) {
		t.Fatalf("burst of 200 reports published %v, want once, at the first activity %v", published, start.Add(time.Millisecond))
	}
	controller.mu.Lock()
	recorded := controller.live[lease.delegateID].activityAt
	controller.mu.Unlock()
	if !recorded.Equal(last) {
		t.Fatalf("recorded activity = %v, want the latest report %v", recorded, last)
	}

	next := start.Add(time.Millisecond + delegateActivityPublishInterval)
	report(next)
	if len(published) != 2 || !published[1].Equal(next) {
		t.Fatalf("after one interval published %d times, want a second publication at %v", len(published), next)
	}
}
