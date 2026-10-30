package agent

import (
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
				go func() { finished <- jm.endHubWatch(watch.WatchID, "manager disabled", nil) }()
			}
			<-detached // Captures the real endHubWatch detach/final-send interval.
			if err := jm.settleWatchSendDelivered(cfg, prior); err != nil {
				close(release)
				t.Fatal(err)
			}
			close(release)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			jm.testOnlyAfterHubWatchDetach = nil

			select {
			case token := <-tokens:
				if token.WatchSend == nil {
					t.Fatalf("queued caller token has no watch send: %#v", token)
				}
			case <-time.After(time.Second):
				t.Fatal("final caller wake token was not queued")
			}
			finals := 0
			for _, event := range loadJobStoreEvents(t, jm) {
				state := event.WatchSend
				if event.Kind != jobstore.EventWatchSendPending || state == nil {
					continue
				}
				if terminal == "end" && state.EndNotice {
					finals++
				}
				if terminal == "completion" && state.TriggerReason == "completed: accepted terminal done" {
					finals++
				}
			}
			if finals != 1 {
				t.Fatalf("durable %s notices=%d, want 1", terminal, finals)
			}
		})
	}
}
