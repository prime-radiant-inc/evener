package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestManagerWatchResyncInterruptsWithoutStatusSelector(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	watch, err := jm.configureHubWatch("local:worker", "worker-instance", "1", "local:manager", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	meta := appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "1"}
	resync := appwire.SessionWatchUpdate{Metadata: meta, Notification: appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{Ref: meta.Ref}).Notification}
	for range 2 {
		if err := jm.onHubObservation(watch.WatchID, resync); err != nil {
			t.Fatal(err)
		}
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watch.WatchID)
	suspended := ok && cfg.hubSuspended
	jm.mu.Unlock()
	if !suspended {
		t.Fatal("completion-only resync did not suspend durable watch")
	}
	snapshot := appwire.SessionWatchUpdate{Metadata: meta, Snapshot: &appwire.ThreadReadResponse{BootGeneration: "1", Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: meta.Ref, InstanceID: meta.InstanceID}}}}
	if err := jm.onHubObservation(watch.WatchID, snapshot); err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	suspended = cfg.hubSuspended
	jm.mu.Unlock()
	if suspended {
		t.Fatal("authoritative catch-up did not resume watch")
	}
	notices := 0
	wantIdentity := meta.Ref + "#interruption"
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchSend == nil || event.WatchSend.Key.WatchID != watch.WatchID {
			continue
		}
		if event.WatchSend.TriggerIdentity == wantIdentity {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("interruption notices=%d want=1", notices)
	}
}
