package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
)

func TestThreadHistoryRecoveryClosesEmptyOverlayGap(t *testing.T) {
	for _, paired := range []bool{false, true} {
		name := "unpaired"
		if paired {
			name = "paired"
		}
		t.Run(name, func(t *testing.T) {
			// Installed before the harness so its cleanup closes the projection
			// goroutine before removing these package-level failure seams.
			repair := breakProjection(t, nil)
			hx := newHistoryHarness(t)
			h := hx.history
			h.serial.Lock()
			if paired {
				h.complete(hx.writer.RecordedLength(), nil)
			}
			first := hx.record(t, "empty-gap-input-461")
			h.applyQueued(first.Offset + first.Length)
			if h.recover() {
				h.serial.Unlock()
				t.Fatal("injected rebuild failures did not fail recovery")
			}
			h.mu.Lock()
			emptyGap := h.gap != nil && h.gap.offset == h.gapEndLocked() && len(h.queue) == 0
			h.mu.Unlock()
			repair()
			h.serial.Unlock()
			if !emptyGap {
				t.Fatal("failed recovery did not open an empty overlay gap")
			}
			if err := h.recoverForRead(); err != nil {
				t.Fatal(err)
			}
			h.serial.Lock()
			defer h.serial.Unlock()
			if h.Failed() != nil {
				t.Fatal("read did not recover the failed history")
			}
			h.mu.Lock()
			gap := h.gap
			h.mu.Unlock()
			if gap != nil {
				t.Errorf("fillOverlayGap must close a gap at end, got %+v", gap)
			}
			next := hx.record(t, "empty-gap-input-462")
			h.overlayEvent(events.New(events.LoopDetectionData{Message: "empty-gap-notice-463"}))
			snapshot := hx.overlay.Snapshot()
			if len(snapshot) != 1 || snapshot[0].Anchor == nil || snapshot[0].Anchor.Entry != next.Ordinal+1 {
				t.Fatalf("recovered overlay skipped queued entry %d: %+v", next.Ordinal, snapshot)
			}
		})
	}
}
