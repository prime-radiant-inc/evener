package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// This catches two writers settling one accepted DeliveryID: the journal-derived
// superseded candidate and the still-live source receipt retry. No manager is
// reconstructed and no runtime map is seeded by the test.
func TestSupersededCandidateSharesSettlementRetryOwnership(t *testing.T) {
	t.Parallel()
	for _, send := range []bool{false, true} {
		for _, overlap := range []bool{false, true} {
			t.Run(fmt.Sprintf("send=%t/overlap=%t", send, overlap), func(t *testing.T) {
				adapter := newMessagingAdapter()
				s := newMessagingRoot(t, adapter, nil)
				jm := s.jobManager
				jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
				args := watchArgs{Source: "self", Target: runtimeMessageAliasCaller, RepeatSeconds: 60}
				if send {
					args.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ownership-5a"}
				}
				result, err := jm.configureWatch(args)
				if err != nil {
					t.Fatal(err)
				}
				jm.mu.Lock()
				key, cfg, ok := jm.watchConfigByIDLocked(result.WatchID)
				jm.mu.Unlock()
				if !ok {
					t.Fatal("recurring watch missing")
				}

				// Inject only at the existing source append boundary. The failed
				// write is real I/O against this source file through a read-only
				// descriptor. Keep the owning Store alive so its bound loaders and
				// all production retry/candidate state survive the fault unchanged.
				readOnly, err := os.Open(filepath.Join(jm.dir, "jobs.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = readOnly.Close() })
				failedAppend := func(event jobstore.Event) error {
					return json.NewEncoder(readOnly).Encode(event)
				}
				var firstWriteErr error
				jm.appendEvent = func(event jobstore.Event) error {
					if event.Kind == jobstore.EventWatchSendDelivered {
						firstWriteErr = failedAppend(event)
						return firstWriteErr
					}
					return jm.store.Append(event)
				}
				jm.fireProgressTick(key, cfg) // A gains canonical custody, receipt fails.
				var writeErr *os.PathError
				if !errors.As(firstWriteErr, &writeErr) || writeErr.Op != "write" {
					t.Fatalf("source fault did not attempt real file write: %v", firstWriteErr)
				}
				produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(produced) != 1 || producerRecordCount(t, s) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
					t.Fatal("A did not reach custody before failed source receipt")
				}
				a := *produced[0].WatchSend
				// Hold B after real custody but before its source receipt. This
				// preserves the current B cursor and its actual delivery claim,
				// while A's candidate and retry still refer to this live config.
				bEntered, bGate, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				releaseB := sync.OnceFunc(func() { close(bGate) })
				finishB := sync.OnceFunc(func() { releaseB(); <-bDone })
				defer finishB()
				jm.appendEvent = func(event jobstore.Event) error {
					if event.Kind == jobstore.EventWatchSendDelivered {
						if event.WatchSend.DeliveryID == a.DeliveryID {
							return failedAppend(event)
						}
						close(bEntered)
						<-bGate
					}
					return jm.store.Append(event)
				}
				go func() { defer close(bDone); jm.fireProgressTick(key, cfg) }()
				awaitSignal(t, bEntered, "B custody before source receipt")
				produced = producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(produced) != 2 {
					t.Fatalf("produced=%d, want A and B", len(produced))
				}
				b := *produced[1].WatchSend
				fold, err := jm.store.LoadWatchSends()
				jm.mu.Lock()
				_, retryA := jm.stableWatchSettlementRetries[a.DeliveryID]
				baseline := cfg.deliveries
				jm.mu.Unlock()
				if err != nil || len(fold.Pending) != 1 || fold.Pending[b.Key] == nil || fold.Pending[b.Key].DeliveryID != b.DeliveryID || len(fold.Superseded) != 1 || fold.Superseded[a.DeliveryID] == nil || !retryA {
					t.Fatalf("live cut pending=%d candidates=%d retryA=%t err=%v", len(fold.Pending), len(fold.Superseded), retryA, err)
				}
				check := func(stage string, wantA, wantB, wantDelta int, wantRetry bool) {
					t.Helper()
					counts := map[string]int{}
					for _, event := range producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered) {
						counts[event.WatchSend.DeliveryID]++
					}
					canonical := map[string]int{}
					for _, entry := range notificationRawEntries(t, transcriptPath(s.stateDir, s.ID())) {
						if id := entry.Turn.AttentionID; id != "" {
							canonical[id]++
						}
					}
					jm.mu.Lock()
					_, retry := jm.stableWatchSettlementRetries[a.DeliveryID]
					delta := cfg.deliveries - baseline
					jm.mu.Unlock()
					requests := len(adapter.requestsFor("root"))
					pendingEvents := len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending))
					t.Logf("%s: A receipts=%d B receipts=%d accounting delta=%d retryA=%t canonical A=%d B=%d provider=%d produced=%d", stage, counts[a.DeliveryID], counts[b.DeliveryID], delta, retry, canonical["watch:"+a.DeliveryID+fmt.Sprintf(":%d", a.UpdateSeq)], canonical["watch:"+b.DeliveryID+fmt.Sprintf(":%d", b.UpdateSeq)], requests, pendingEvents)
					if counts[a.DeliveryID] != wantA || counts[b.DeliveryID] != wantB || len(counts) != min(wantB, 1)+min(wantA, 1) {
						t.Errorf("%s: raw source receipts A=%d B=%d IDs=%d, want %d/%d/%d", stage, counts[a.DeliveryID], counts[b.DeliveryID], len(counts), wantA, wantB, min(wantB, 1)+min(wantA, 1))
					}
					if retry != wantRetry {
						t.Errorf("%s: stale live A retry=%t, want %t", stage, retry, wantRetry)
					}
					if send && delta != wantDelta {
						t.Errorf("%s: explicit accounting increments=%d, want %d", stage, delta, wantDelta)
					}
					if !send && delta != 0 {
						t.Errorf("%s: callback counted as explicit send: %d", stage, delta)
					}
					if len(canonical) != 2 || canonical["watch:"+a.DeliveryID+fmt.Sprintf(":%d", a.UpdateSeq)] != 1 || canonical["watch:"+b.DeliveryID+fmt.Sprintf(":%d", b.UpdateSeq)] != 1 || requests != 0 || pendingEvents != 2 {
						t.Errorf("%s: duplicate/lost canonical content or new provider/production work: canonical=%v requests=%d produced=%d", stage, canonical, requests, pendingEvents)
					}
				}
				check("live candidate plus retry", 0, 0, 0, true)

				var attempts atomic.Int32
				if !overlap {
					jm.appendEvent = func(event jobstore.Event) error {
						if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend.DeliveryID == a.DeliveryID && attempts.Add(1) == 1 {
							return failedAppend(event)
						}
						return jm.store.Append(event)
					}
					if err := jm.dispatchProducedWatchSendsFrom(jm.store.LoadWatchSends); err == nil {
						t.Error("first failed A receipt was not propagated")
					}
					check("candidate recovered after first retry write failed", 1, 0, 1, false)
				} else {
					entered, gate := make(chan struct{}), make(chan struct{})
					release := sync.OnceFunc(func() { close(gate) })
					defer release()
					jm.appendEvent = func(event jobstore.Event) error {
						if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend.DeliveryID == a.DeliveryID && attempts.Add(1) == 1 {
							close(entered)
							<-gate
						}
						return jm.store.Append(event)
					}
					done := make(chan error, 1)
					go func() { _, err := jm.retryStableWatchSettlements(); done <- err }()
					awaitSignal(t, entered, "held live source receipt append")
					if err := jm.dispatchProducedWatchSendsFrom(jm.store.LoadWatchSends); err != nil {
						t.Error(err)
					}
					// A's retry owns the in-flight write. A candidate must not
					// independently acknowledge/count it before that write returns.
					check("concurrent dispatch while retry append held", 0, 0, 0, true)
					release()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
				if err := jm.dispatchProducedWatchSendsFrom(jm.store.LoadWatchSends); err != nil {
					t.Fatal(err)
				}
				check("next healthy pump with current B held", 1, 0, 1, false)
				fold, err = jm.store.LoadWatchSends()
				if err != nil || len(fold.Pending) != 1 || fold.Pending[b.Key] == nil || fold.Pending[b.Key].DeliveryID != b.DeliveryID {
					t.Fatalf("A settlement changed current B: pending=%d err=%v", len(fold.Pending), err)
				}
				finishB()
				if err := jm.dispatchProducedWatchSendsFrom(jm.store.LoadWatchSends); err != nil {
					t.Fatal(err)
				}
				check("B released and healthy pump", 1, 1, 2, false)
				fold, err = jm.store.LoadWatchSends()
				if err != nil || len(fold.Pending) != 0 || len(fold.Superseded) != 0 {
					t.Fatalf("recovery left source work: pending=%d candidates=%d err=%v", len(fold.Pending), len(fold.Superseded), err)
				}
			})
		}
	}
}

func TestSettlementCandidateKeepsUnarmedRetryAuthority(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	jm := fixture.sourceJM
	cfg := fixture.onlyWatchConfig(t)
	armFailure := errors.New("receiver arm failed")
	jm.stableWatchArmFailure = func(string) error { return armFailure }
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "unarmed A"})
	a := fixture.requireOnePending(t).state
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "current B"})
	b := fixture.requireOnePending(t).state
	jm.mu.Lock()
	snapshot, retrying := jm.stableWatchSettlementRetries[a.DeliveryID]
	jm.mu.Unlock()
	if !retrying || !snapshot.unarmed || snapshot.cfg != cfg {
		t.Fatal("superseded A lost its original config or required-arm metadata")
	}
	fold, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if err := jm.settleSupersededWatchSends(fold.Superseded); !errors.Is(err, armFailure) {
		t.Fatalf("candidate settlement bypassed required arm: %v", err)
	}
	if got := countExactWatchSendEvents(loadJobStoreEvents(t, jm), jobstore.EventWatchSendDelivered, a); got != 0 || !stableWatchSettlementRetrying(jm, a.DeliveryID) || jm.stableWatchReceipt(a.DeliveryID) == nil {
		t.Fatalf("failed candidate arm lost retry/receipt or acknowledged A: receipts=%d", got)
	}
	jm.stableWatchArmFailure = nil
	if err := jm.settleSupersededWatchSends(fold.Superseded); err != nil {
		t.Fatal(err)
	}
	// Simulate a batch's stale read, not stale runtime state: its saved entry was
	// settled by the candidate before it acquired the existing per-ID claim.
	armCalls := 0
	jm.stableWatchArmFailure = func(string) error { armCalls++; return armFailure }
	if !jm.claimWatchSendDelivery(a.DeliveryID) {
		t.Fatal("candidate leaked its delivery claim")
	}
	err = jm.retryClaimedStableWatchSettlement(snapshot)
	jm.releaseWatchSendDeliveryClaim(a.DeliveryID)
	if err != nil || armCalls != 0 {
		t.Fatalf("settled stale snapshot rearmed receiver: calls=%d err=%v", armCalls, err)
	}
	if got := countExactWatchSendEvents(loadJobStoreEvents(t, jm), jobstore.EventWatchSendDelivered, a); got != 1 || stableWatchSettlementRetrying(jm, a.DeliveryID) {
		t.Fatalf("candidate plus stale retry receipts=%d, want one and no retained retry", got)
	}
	if got := fixture.requireOnePending(t).state; got.DeliveryID != b.DeliveryID || got.UpdateSeq != b.UpdateSeq {
		t.Fatalf("A settlement changed current B: %#v", got)
	}
	requireOnlyStableWatchReceipts(t, fixture, b.DeliveryID)
}
