package agent

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
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
			finalPending := 0
			for _, event := range loadJobStoreEvents(t, jm) {
				if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.DeliveryID != prior.DeliveryID {
					finalPending++
				}
			}
			if finalPending != 1 {
				t.Fatalf("durable terminal pending sends at detachment=%d, want 1", finalPending)
			}
			var finalState *jobstore.WatchSendState
			for _, event := range loadJobStoreEvents(t, jm) {
				if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.DeliveryID != prior.DeliveryID {
					state := *event.WatchSend
					finalState = &state
				}
			}
			if terminal == "end" {
				if finalState == nil || !finalState.EndNotice || finalState.TriggerIdentity != "local:worker#ended" {
					t.Fatalf("terminal end state = %#v", finalState)
				}
			} else if finalState == nil || finalState.EndNotice || finalState.TriggerIdentity != "worker-instance/7@worker/accepted-terminal:status:completed" {
				t.Fatalf("terminal completion state = %#v", finalState)
			}
			frameFields := map[string]string{}
			for line := range strings.SplitSeq(finalState.Frame, "\n") {
				if key, value, ok := strings.Cut(line, ": "); ok {
					frameFields[key] = value
				}
			}
			if frameFields["watch_id"] != watch.WatchID || frameFields["delivery_id"] != finalState.DeliveryID || frameFields["job_id"] != finalState.TriggerIdentity || frameFields["trigger"] != finalState.TriggerReason {
				t.Fatalf("terminal frame identity fields = %#v, state = %#v", frameFields, finalState)
			}
			oldStore := jm.store
			if err := oldStore.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := newJobManagerWithStoreOpen(root.stateDir, jm.sessionID, func(jobNotification) {}, jobstore.OpenNoSync, jobstore.CreateOutputNoSync)
			if err != nil {
				t.Fatalf("reopen job manager at detachment boundary: %v", err)
			}
			jm.store = restored.store
			jm.appendEvent = restored.store.Append
			jm.appendEvents = restored.store.AppendBatch
			watches, err := restored.store.LoadWatches()
			if err != nil {
				t.Fatal(err)
			}
			if watches[watch.WatchID] == nil || watches[watch.WatchID].Active {
				t.Fatalf("reopened WatchCleared fold = %#v, want inactive record", watches[watch.WatchID])
			}
			for _, priorDelivery := range restored.pendingWatchSendDeliveries(nil) {
				if priorDelivery.state.DeliveryID == prior.DeliveryID {
					if err := restored.settleWatchSendDelivered(priorDelivery.cfg, priorDelivery.state); err != nil {
						t.Fatalf("settle prior notice after reopen: %v", err)
					}
				}
			}
			pending := restored.pendingWatchSendDeliveries(nil)
			if len(pending) != 1 || pending[0].state.Key.WatchID != watch.WatchID || pending[0].state.DeliveryID != finalState.DeliveryID {
				t.Fatalf("reopened terminal pending sends = %#v, want one final send", pending)
			}
			restoredSession := &Session{jobManager: restored}
			restoredToken := watchSendToken{Key: finalState.Key, UpdateSeq: finalState.UpdateSeq, DeliveryID: finalState.DeliveryID}
			owner, restoredCfg, restoredState, ok := restoredSession.resolveWatchSendToken(&restoredToken)
			if !ok || owner != restored {
				t.Fatal("reopened terminal token did not resolve")
			}
			if restoredState.Frame != finalState.Frame {
				t.Fatalf("restored terminal frame differs from persisted frame: got %q, want %q", restoredState.Frame, finalState.Frame)
			}
			if err := owner.settleWatchSendDelivered(restoredCfg, restoredState); err != nil {
				t.Fatalf("drain restored terminal send: %v", err)
			}
			if pending := restored.pendingWatchSendDeliveries(nil); len(pending) != 0 {
				t.Fatalf("pending sends after restored delivery = %d, want 0", len(pending))
			}
			if _, _, _, ok := restoredSession.resolveWatchSendToken(&restoredToken); ok {
				t.Fatal("restored terminal token resolved again after delivery")
			}
			delivered := 0
			for _, event := range loadJobStoreEvents(t, restored) {
				if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend != nil && event.WatchSend.DeliveryID == finalState.DeliveryID {
					delivered++
				}
			}
			if delivered != 1 {
				t.Fatalf("restored terminal delivery records = %d, want 1", delivered)
			}
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

func TestHubWatchTerminalDeliveryAppendBatchFailureLeavesWatchActive(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	watch, err := jm.configureHubWatch("local:worker", "worker-instance", "7@worker", "local:manager", []string{"status"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Event: "status", Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker", EntryID: "prior", CausalKnown: true}}); err != nil {
		t.Fatal(err)
	}
	appendErr := errors.New("append batch failure")
	tokens := make(chan jobNotification, 1)
	jm.enqueue = func(n jobNotification) { tokens <- n }
	jm.appendEvents = func([]jobstore.Event) error { return appendErr }
	if err := jm.endHubWatch(watch.WatchID, "target replaced", nil); !errors.Is(err, appendErr) {
		t.Fatalf("endHubWatch error = %v, want injected append failure", err)
	}
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchCleared && event.WatchID == watch.WatchID {
			t.Fatal("WatchCleared became durable after AppendBatch failure")
		}
		if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.EndNotice {
			t.Fatal("terminal delivery became durable after AppendBatch failure")
		}
	}
	jm.mu.Lock()
	_, _, active := jm.watchConfigByIDLocked(watch.WatchID)
	jm.mu.Unlock()
	if !active {
		t.Fatal("watch config was detached after AppendBatch failure")
	}
	select {
	case token := <-tokens:
		if token.WatchSend != nil && token.WatchSend.Key.ResolvedWatchedIdentity == "local:worker#ended" {
			t.Fatalf("terminal notification queued after AppendBatch failure: %#v", token)
		}
	default:
	}
}

func TestHubWatchTerminalDeliveryRunawayDropStillDetaches(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	watch, err := jm.configureHubWatch("local:worker", "worker-instance", "7@worker", "local:manager", []string{"status"}, true)
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watch.WatchID)
	if !ok {
		jm.mu.Unlock()
		t.Fatal("watch config disappeared")
	}
	delivery := jm.watchSendSnapshot(cfg, "worker-instance/7@worker/terminal", "completed", events.SessionEvent{SessionID: jm.sessionID})
	delivery.fuseDepth = runawaySelfInfluenceDepth
	jm.mu.Unlock()
	var diagnostics []jobNotification
	jm.enqueue = func(n jobNotification) { diagnostics = append(diagnostics, n) }
	if err := jm.endHubWatch(watch.WatchID, "completed", &delivery); err != nil {
		t.Fatalf("endHubWatch: %v", err)
	}
	jm.mu.Lock()
	_, _, active := jm.watchConfigByIDLocked(watch.WatchID)
	jm.mu.Unlock()
	if active {
		t.Fatal("in-memory watch remained active after durable terminal drop")
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatal(err)
	}
	if watches[watch.WatchID] == nil || watches[watch.WatchID].Active {
		t.Fatalf("folded watch = %#v, want cleared", watches[watch.WatchID])
	}
	sends, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(sends.Pending) != 0 {
		t.Fatalf("pending terminal sends after runaway drop = %d, want 0", len(sends.Pending))
	}
	foundDrop := false
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.DiagnosticReason == "runaway" {
			foundDrop = true
		}
	}
	foundDiagnostic := false
	for _, diagnostic := range diagnostics {
		if diagnostic.OriginWatchID == watch.WatchID && diagnostic.WatchSend == nil {
			foundDiagnostic = true
		}
	}
	if !foundDrop || !foundDiagnostic {
		t.Fatalf("runaway drop recorded=%v, watch diagnostic=%v", foundDrop, foundDiagnostic)
	}
}
