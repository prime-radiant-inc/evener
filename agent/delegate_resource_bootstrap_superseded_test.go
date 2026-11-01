package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

// Extend the real legacy bootstrap fixture with a predecessor, not just a head.
// Both source entrypoints must ignore it before querying the retired receiver.
func TestDelegateResourceBootstrap_LegacySupersededWatchFramesAreNotRestored(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"WatchTarget", "ResolvedWatchedIdentity", "ResolvedSendTo"} {
		for _, childReceiver := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/child_receiver=%t", field, childReceiver), func(t *testing.T) {
				meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
				removeDelegateStoreIfPresent(t, stateDir, meta.ID)
				path := filepath.Join(jobsDir(stateDir, meta.ID), "jobs.jsonl")
				frame := func(id, target string, seq uint64) jobstore.WatchSendState {
					return jobstore.WatchSendState{Key: jobstore.WatchSendKey{VisibleSessionID: meta.ID, WatchID: "watch_" + target, WatchTarget: target, ResolvedWatchedIdentity: target, ResolvedSendTo: runtimeMessageAliasCaller, WatchGeneration: "wg_" + target}, DeliveryID: id, UpdateSeq: seq, Frame: "saved-frame-" + id, ReceiverSessionID: meta.ID, CreatedAt: time.Unix(10, 0).UTC(), UpdatedAt: time.Unix(10+int64(seq), 0).UTC()}
				}
				retired1 := frame("delivery_retired_1", "job_unrelated", 1)
				switch field {
				case "WatchTarget":
					retired1.Key.WatchTarget = "job_legacy_delegate"
				case "ResolvedWatchedIdentity":
					retired1.Key.ResolvedWatchedIdentity = "job_legacy_delegate"
				case "ResolvedSendTo":
					retired1.Key.ResolvedSendTo = "job_legacy_delegate"
				}
				if childReceiver {
					retired1.ReceiverSessionID = identifier.MustNewSessionID()
					retired1.ReceiverDelegateID = "dlg_retired_receiver"
					retired1.StableReceiver = true
				}
				retired2 := retired1
				retired2.DeliveryID = "delivery_retired_2"
				retired2.UpdateSeq = 2
				retired2.UpdatedAt = time.Unix(12, 0).UTC()
				healthy1, healthy2 := frame("delivery_shell_1", "job_shell", 1), frame("delivery_shell_2", "job_shell", 2)
				appendLegacyBootstrapEvents(t, path,
					jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_legacy_delegate", Type: jobstore.JobType(delegateResourceType), OwnerSessionID: meta.ID},
					jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_shell", Type: jobstore.JobShell, OwnerSessionID: meta.ID},
					jobstore.Event{Kind: jobstore.EventWatchSendPending, WatchSend: &retired1}, jobstore.Event{Kind: jobstore.EventWatchSendPending, WatchSend: &retired2},
					jobstore.Event{Kind: jobstore.EventWatchSendPending, WatchSend: &healthy1}, jobstore.Event{Kind: jobstore.EventWatchSendPending, WatchSend: &healthy2})
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				eventsBefore, err := jobstore.ReadEvents(path)
				if err != nil {
					t.Fatal(err)
				}
				fold := jobstore.FoldWatchSends(eventsBefore)
				if len(fold.Superseded) != 2 || fold.Superseded[retired1.DeliveryID] == nil || fold.Superseded[healthy1.DeliveryID] == nil {
					t.Fatalf("actual superseded source scenario absent: %+v", fold)
				}
				t.Logf("real source fold has retired predecessor %s and healthy predecessor %s, exclusion field=%s child receiver=%t", retired1.DeliveryID, healthy1.DeliveryID, field, childReceiver)
				restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
				if err != nil {
					t.Fatalf("actual restore rejected retired superseded history: %v", err)
				}
				defer restored.Close()
				restored.SetNotifyFunc(func() {})
				jm := restored.jobManager
				if err := jm.dispatchProducedWatchSendsFrom(jm.store.LoadWatchSends); err != nil {
					t.Fatalf("producer pump retried retired receiver: %v", err)
				}
				if _, err := restored.drainJobManagerWatchSends(context.Background(), jm); err != nil {
					t.Fatalf("owner drain retried retired receiver: %v", err)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasPrefix(after, before) {
					t.Fatal("legacy source bytes were rewritten")
				}
				log, err := jobstore.ReadEvents(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, ev := range log[len(eventsBefore):] {
					if ev.WatchSend != nil && (ev.WatchSend.DeliveryID == retired1.DeliveryID || ev.WatchSend.DeliveryID == retired2.DeliveryID) {
						t.Fatalf("retired watch history received a new terminal write: kind=%s id=%s", ev.Kind, ev.WatchSend.DeliveryID)
					}
				}
				fold = jobstore.FoldWatchSends(log)
				if fold.Superseded[retired1.DeliveryID] == nil || fold.Pending[retired2.Key] == nil || fold.Pending[retired2.Key].DeliveryID != retired2.DeliveryID {
					t.Fatal("ignored retired inventory was discarded rather than preserved")
				}
				if got := countExactWatchSendEvents(log, jobstore.EventWatchSendDropped, healthy1); got != 1 {
					t.Fatalf("healthy predecessor exact drops=%d want1", got)
				}
				if got := countExactWatchSendEvents(log, jobstore.EventWatchSendDelivered, healthy2); got != 1 {
					t.Fatalf("healthy current exact deliveries=%d want1", got)
				}
				requireWatchDeliveryReceipts(t, restored, map[string]string{watchSourceID(healthy2): watchSourcePayload(healthy2)}, nil)
				jm.mu.Lock()
				retry := jm.watchTransportRetry.active
				claims := len(jm.deliveringWatchSends)
				jm.mu.Unlock()
				if retry || claims != 0 || jm.hasPendingWatchSends() {
					t.Fatalf("retired history created active recovery: retry=%t claims=%d pending=%t", retry, claims, jm.hasPendingWatchSends())
				}
				if err := jm.dispatchProducedWatchSendsFrom(jm.store.LoadWatchSends); err != nil {
					t.Fatal(err)
				}
				final, err := jobstore.ReadEvents(path)
				if err != nil {
					t.Fatal(err)
				}
				if len(final) != len(log) {
					t.Fatal("repeated pump wrote through retired history or duplicated eligible settlement")
				}
			})
		}
	}
}
