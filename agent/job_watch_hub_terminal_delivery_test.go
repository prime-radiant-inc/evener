package agent

import (
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func TestHubWatchTerminalDeliverySurvivesPriorLastNoticeSettlement(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{"completion", "end"} {
		t.Run(terminal, func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
			jm := root.jobManager
			watch, err := jm.configureHubWatch("local:worker", "worker-instance", "7@worker", "local:manager", []string{"status"}, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Event: "status", Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker", EntryID: "prior", CausalKnown: true}}); err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			_, cfg, ok := jm.watchConfigByIDLocked(watch.WatchID)
			if !ok {
				jm.mu.Unlock()
				t.Fatal("prior watch config disappeared before terminal transition")
			}
			if len(cfg.pending) != 1 {
				pendingCount := len(cfg.pending)
				jm.mu.Unlock()
				t.Fatalf("prior pending notice count = %d, want 1", pendingCount)
			}
			var prior jobstore.WatchSendState
			for _, state := range cfg.pending {
				prior = *state
			}
			jm.mu.Unlock()

			detached := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseBoundary := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseBoundary()
			jm.testOnlyAfterHubWatchDetach = func() {
				close(detached)
				<-release
			}
			tokens := make(chan jobNotification, 1)
			jm.enqueue = func(n jobNotification) { tokens <- n }
			finished := make(chan error, 1)
			if terminal == "completion" {
				accepted := true
				go func() {
					finished <- jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Event: "status", Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker", EntryID: "accepted-terminal", CausalKnown: true, EndTurn: true, EndReason: "done", TerminalAccepted: &accepted}})
				}()
			} else {
				go func() { finished <- jm.endHubWatch(watch.WatchID, "target replaced; re-register", nil) }()
			}
			// TRIPWIRE: local journal detachment normally finishes below a second; this only catches a deadlock.
			awaitWithin(t, 10*time.Second, "endHubWatch detachment boundary", func() { <-detached })
			if err := jm.settleWatchSendDelivered(cfg, prior); err != nil {
				releaseBoundary()
				t.Fatal(err)
			}
			releaseBoundary()
			// TRIPWIRE: after releasing the barrier, local final-send recording is sub-second work.
			awaitWithin(t, 10*time.Second, "endHubWatch final send recording", func() {
				if err := <-finished; err != nil {
					t.Errorf("endHubWatch: %v", err)
				}
			})
			jm.testOnlyAfterHubWatchDetach = nil

			var token jobNotification
			// TRIPWIRE: recording already queued the token; this bound detects a lost delivery, not normal latency.
			awaitWithin(t, 10*time.Second, "final caller wake token", func() { token = <-tokens })
			if token.WatchSend == nil {
				t.Fatalf("queued caller token has no watch send: %#v", token)
			}
			finals := 0
			for _, event := range loadJobStoreEvents(t, jm) {
				state := event.WatchSend
				if event.Kind != jobstore.EventWatchSendPending || state == nil {
					continue
				}
				if state.DeliveryID != token.WatchSend.DeliveryID || state.Key != token.WatchSend.Key || state.UpdateSeq != token.WatchSend.UpdateSeq || state.DeliveryID == prior.DeliveryID {
					continue
				}
				if state.EndNotice != (terminal == "end") {
					continue
				}
				if terminal == "completion" && state.TriggerIdentity != "worker-instance/7@worker/accepted-terminal:status:completed" {
					continue
				}
				finals++
			}
			if finals != 1 {
				t.Fatalf("durable %s notices=%d, want 1", terminal, finals)
			}
		})
	}
}
