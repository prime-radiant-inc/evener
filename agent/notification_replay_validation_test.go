package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// Every source payload comes from a real producer;
// only the persisted receiver's causal metadata is faulted for finding one.
func TestNotificationReplayProvenance(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"shell", "shell_nil_provenance", "current_callback"} {
		for _, metadata := range []string{"matching", "different", "missing", "duplicate_conflict"} {
			if source == "shell_nil_provenance" && (metadata == "missing" || metadata == "duplicate_conflict") {
				continue
			}
			t.Run(fmt.Sprintf("%s/receiver=%s", source, metadata), func(t *testing.T) {
				f := newColdStableDelegateFixture(t, "")
				jm, err := newJobManagerNoSync(f.stateDir, f.childID, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = jm.closeStoreOnly() })
				var id string
				var message llm.Message
				var causal *provenance.Causal
				var jobID string
				if source != "current_callback" {
					jm.parentDelegateID = f.delegateID
					if source != "shell_nil_provenance" {
						causal = provenance.WithWatch(nil, "watch-round4-source", "generation-round4-source", "delivery-round4-source", f.childID, "")
					}
					jm.currentProvenance = func() *provenance.Causal { return causal }
					record, err := jm.createShell(createShellOpts{Command: "true", Description: "opaque-round4-shell"})
					if err != nil {
						t.Fatal(err)
					}
					jobID = record.JobID
					code := 0
					if err := jm.finalize(jobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
						t.Fatal(err)
					}
					record = loadStableShellRecord(t, jm, jobID)
					if record.NotifyState != jobstore.NotifyPending {
						t.Fatalf("source state=%s", record.NotifyState)
					}
					id = stableShellAttentionID(jobID, record.TerminalGen)
					store, err := delegatestore.Open(delegateResourceStorePath(f.stateDir, f.meta.ID))
					if err != nil {
						t.Fatal(err)
					}
					events, err := store.Load()
					if err != nil {
						t.Fatal(err)
					}
					state, err := delegatestore.Fold(events)
					if err != nil {
						t.Fatal(err)
					}
					message = llm.UserMachinery(stableShellAttentionContent(filepath.Join(jm.dir, "jobs.jsonl"), state[f.delegateID].Descriptor, record))
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					state := replayValidationProduceLocalCallback(t, jm)
					id, message, causal = stableWatchAttentionID(state), llm.UserMachinery(state.Frame), state.Provenance
				}
				if causal == nil && source != "shell_nil_provenance" {
					t.Fatal("producer omitted causal identity")
				}
				if err := jm.closeStoreOnly(); err != nil {
					t.Fatal(err)
				}
				path := transcriptPath(f.stateDir, f.childID)
				writer, _, err := transcript.OpenWriterForSession(path, f.childID)
				if err != nil {
					t.Fatal(err)
				}
				turn := schema.NewTurn(schema.TurnSteering, message)
				turn.AttentionID, turn.StableTurnID = id, newQueueEntryID()
				turn.NotificationProvenance = provenance.Clone(causal)
				switch metadata {
				case "different":
					turn.NotificationProvenance = provenance.WithWatch(nil, "watch-round4-conflict", "generation-round4-conflict", "delivery-round4-conflict", f.childID, "")
				case "missing":
					turn.NotificationProvenance = nil
				}
				if _, err := writer.Record(turn, transcript.RecordOptions{Door: transcript.DoorSynced, Place: transcript.PlaceDelivery}); err != nil {
					t.Fatal(err)
				}
				if metadata == "duplicate_conflict" {
					duplicate := turn
					duplicate.NotificationProvenance = provenance.WithWatch(nil, "watch-round4-duplicate", "generation-round4-duplicate", "delivery-round4-duplicate", f.childID, "")
					if _, err := writer.Record(duplicate, transcript.RecordOptions{Door: transcript.DoorSynced, Place: transcript.PlaceDelivery}); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				root, restoreErr := restoreDelegateResourceBootstrapSession(f.client, f.profile, f.workspace, f.meta, f.stateDir)
				if root != nil {
					t.Cleanup(root.Close)
				}
				receipts := 0
				if source != "current_callback" {
					receipts = len(producerSourceEvents(t, jm, jobstore.EventJobNotificationDelivered))
				} else {
					receipts = len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered))
				}
				matches := 0
				for _, e := range notificationRawEntries(t, path) {
					if e.Turn.AttentionID == id {
						matches++
						if metadata != "duplicate_conflict" && !reflect.DeepEqual(e.Turn.NotificationProvenance, turn.NotificationProvenance) {
							t.Error("bootstrap rewrote existing receiver causal identity")
						}
					}
				}
				t.Logf("source=%s receiver=%s restoreError=%t sourceReceipts=%d canonicalRecords=%d providerRequests=%d", source, metadata, restoreErr != nil, receipts, matches, len(f.adapter.Requests()))
				if metadata != "matching" {
					if restoreErr == nil || receipts != 0 {
						t.Errorf("conflicting receiver provenance accepted: restoreError=%v sourceReceipts=%d", restoreErr, receipts)
					}
				} else if restoreErr != nil || receipts != 1 {
					t.Errorf("matching causal identity did not settle: restoreError=%v receipts=%d", restoreErr, receipts)
				}
				wantMatches := 1
				if metadata == "duplicate_conflict" {
					wantMatches = 2
				}
				if matches != wantMatches || len(f.adapter.Requests()) != 0 {
					t.Error("bootstrap duplicated receiver custody or executed provider")
				}
			})
		}
	}
}

func replayValidationProduceLocalCallback(t *testing.T, jm *jobManager) jobstore.WatchSendState {
	t.Helper()
	jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
	r, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, AfterSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(r.WatchID)
	jm.mu.Unlock()
	if !ok {
		t.Fatal("missing source registration")
	}
	jm.fireProgressTick(key, cfg)
	produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(produced) != 1 || !produced[0].WatchSend.CallbackFrame || produced[0].WatchSend.StableReceiver || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
		t.Fatal("real callback producer did not leave one unacknowledged local frame")
	}
	return *produced[0].WatchSend
}

func TestNotificationReplayClosedCallback(t *testing.T) {
	t.Parallel()
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprintf("already_custodied=%t", accepted), func(t *testing.T) {
			f := newColdStableDelegateFixture(t, "")
			jm, err := newJobManagerNoSync(f.stateDir, f.childID, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = jm.closeStoreOnly() })
			path := transcriptPath(f.stateDir, f.childID)
			if accepted {
				writer, _, err := transcript.OpenWriterForSession(path, f.childID)
				if err != nil {
					t.Fatal(err)
				}
				receiver := &Session{id: f.childID, stateDir: f.stateDir}
				receiver.attachTranscript(writer)
				receiver.SetNotifyFunc(func() { _ = jm.store.Close() })
				jm.retirementOwner = receiver
				t.Cleanup(func() { _ = receiver.closeAttachedTranscript() })
			}
			state := replayValidationProduceLocalCallback(t, jm)
			if accepted {
				jm.retirementOwner.SetNotifyFunc(nil)
				if err := jm.retirementOwner.closeAttachedTranscript(); err != nil {
					t.Fatal(err)
				}
				jm.retirementOwner = nil
				reopenProducerSource(t, jm)
			}
			before := countAttentionEntries(t, path, stableWatchAttentionID(state))
			wantBefore := 0
			if accepted {
				wantBefore = 1
			}
			if before != wantBefore {
				t.Fatalf("before closure canonical=%d want=%d", before, wantBefore)
			}
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			store, err := delegatestore.Open(delegateResourceStorePath(f.stateDir, f.meta.ID))
			if err != nil {
				t.Fatal(err)
			}
			events, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			tree, err := delegatestore.Fold(events)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = store.AppendBatch(tree, []delegatestore.Event{{Kind: delegatestore.EventDelegateResumabilityClosed, DelegateID: f.delegateID, TS: time.Unix(1700001000, 0), ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "round4-permanent-closure"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			root, err := restoreDelegateResourceBootstrapSession(f.client, f.profile, f.workspace, f.meta, f.stateDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(root.Close)
			for range 2 {
				if err := repairStableWatchDeliveriesForBootstrap(root.delegateController); err != nil {
					t.Fatal(err)
				}
			}
			aggregate := delegateAggregateSnapshot(t, root.delegateController, f.delegateID)
			if aggregate.Resumable || aggregate.Phase != delegatestore.PhaseClosed {
				t.Fatal("closure was not durable")
			}
			childCount := countAttentionEntries(t, path, stableWatchAttentionID(state))
			rootCount := countAttentionEntries(t, transcriptPath(f.stateDir, f.meta.ID), stableWatchAttentionID(state))
			receipts := producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)
			drops := producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)
			t.Logf("alreadyCustodied=%t before=%d afterChild=%d afterAncestor=%d delivered=%d dropped=%d providerRequests=%d", accepted, before, childCount, rootCount, len(receipts), len(drops), len(f.adapter.Requests()))
			if accepted {
				if childCount != 1 || rootCount != 1 || len(receipts) != 1 || len(drops) != 0 || !exactWatchSendEvent(receipts[0], jobstore.EventWatchSendDelivered, state) {
					t.Error("accepted-before-closure frame lost exact receipt/ancestor handoff")
				}
			} else if childCount != 0 || rootCount != 0 || len(receipts) != 0 || len(drops) != 1 {
				t.Error("never-custodied closed receiver gained custody/ancestor attention instead of durable drop")
			} else if !exactWatchSendEvent(drops[0], jobstore.EventWatchSendDropped, state) || drops[0].WatchSend.DiagnosticReason == "" {
				t.Error("closed receiver drop lost exact source identity or diagnostic reason")
			}
			if len(f.adapter.Requests()) != 0 {
				t.Error("bootstrap executed provider")
			}
		})
	}
}

func replayValidationCloseDelegate(t *testing.T, f coldStableDelegateFixture) {
	t.Helper()
	store, err := delegatestore.Open(delegateResourceStorePath(f.stateDir, f.meta.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendBatch(tree, []delegatestore.Event{{
		Kind: delegatestore.EventDelegateResumabilityClosed, DelegateID: f.delegateID,
		TS: time.Unix(1700001000, 0), ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "replay-validation-closure"},
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationReplayHistoricalClosedCallback(t *testing.T) {
	t.Parallel()
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprintf("already_custodied=%t", accepted), func(t *testing.T) {
			f := newColdStableDelegateFixture(t, "")
			jm, err := newJobManagerNoSync(f.stateDir, f.childID, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = jm.closeStoreOnly() })
			jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
			r, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, RepeatSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			key, cfg, ok := jm.watchConfigByIDLocked(r.WatchID)
			jm.mu.Unlock()
			if !ok {
				t.Fatal("missing source registration")
			}
			path := transcriptPath(f.stateDir, f.childID)
			if accepted {
				writer, _, err := transcript.OpenWriterForSession(path, f.childID)
				if err != nil {
					t.Fatal(err)
				}
				receiver := &Session{id: f.childID, stateDir: f.stateDir}
				receiver.attachTranscript(writer)
				receiver.SetNotifyFunc(func() { _ = jm.store.Close() })
				jm.retirementOwner = receiver
				jm.fireProgressTick(key, cfg)
				receiver.SetNotifyFunc(nil)
				if err := receiver.closeAttachedTranscript(); err != nil {
					t.Fatal(err)
				}
				jm.retirementOwner = nil
				reopenProducerSource(t, jm)
			} else {
				jm.fireProgressTick(key, cfg)
			}
			// Keep the accepted predecessor's ack retry from settling before restart.
			jm.appendEvent = func(event jobstore.Event) error {
				if err := jm.store.Append(event); err != nil {
					return err
				}
				if event.Kind == jobstore.EventWatchSendPending {
					return jm.store.Close()
				}
				return nil
			}
			jm.fireProgressTick(key, cfg)
			reopenProducerSource(t, jm)
			if _, err := jm.clearWatch(key); err != nil {
				t.Fatal(err)
			}
			produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(produced) != 2 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
				t.Fatal("expected historical and current source debt")
			}
			first, second := *produced[0].WatchSend, *produced[1].WatchSend
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			replayValidationCloseDelegate(t, f)
			root, err := restoreDelegateResourceBootstrapSession(f.client, f.profile, f.workspace, f.meta, f.stateDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(root.Close)
			for range 2 {
				if err := repairStableWatchDeliveriesForBootstrap(root.delegateController); err != nil {
					t.Fatal(err)
				}
			}
			want := 0
			if accepted {
				want = 1
			}
			acks, drops := producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered), producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)
			if got := countAttentionEntries(t, path, stableWatchAttentionID(first)); got != want {
				t.Errorf("historical child custody=%d want=%d", got, want)
			}
			if got := countAttentionEntries(t, transcriptPath(f.stateDir, f.meta.ID), stableWatchAttentionID(first)); got != want {
				t.Errorf("historical ancestor custody=%d want=%d", got, want)
			}
			if countAttentionEntries(t, path, stableWatchAttentionID(second)) != 0 || countAttentionEntries(t, transcriptPath(f.stateDir, f.meta.ID), stableWatchAttentionID(second)) != 0 {
				t.Error("never-custodied current frame resurrected")
			}
			if len(acks) != want || (want != 0 && !exactWatchSendEvent(acks[0], jobstore.EventWatchSendDelivered, first)) {
				t.Errorf("historical receipts=%d want=%d", len(acks), want)
			}
			if len(drops) != 1 || !exactWatchSendEvent(drops[0], jobstore.EventWatchSendDropped, second) || drops[0].WatchSend.DiagnosticReason == "" {
				t.Error("current closed frame did not receive one exact diagnostic drop")
			}
			if len(f.adapter.Requests()) != 0 {
				t.Error("bootstrap executed provider")
			}
		})
	}
}

func TestNotificationReplayClosedCallbackUncertainCustody(t *testing.T) {
	t.Parallel()
	for _, accepted := range []bool{false, true} {
		for _, boundary := range []string{"missing", "identity", "missing_during_open", "sync", "close"} {
			t.Run(fmt.Sprintf("already_custodied=%t/%s", accepted, boundary), func(t *testing.T) {
				f := newColdStableDelegateFixture(t, "")
				jm, err := newJobManagerNoSync(f.stateDir, f.childID, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = jm.closeStoreOnly() })
				state := replayValidationProduceLocalCallback(t, jm)
				path := transcriptPath(f.stateDir, f.childID)
				if accepted {
					writer, _, err := transcript.OpenWriterForSession(path, f.childID)
					if err != nil {
						t.Fatal(err)
					}
					receiver := &Session{id: f.childID, stateDir: f.stateDir}
					receiver.attachTranscript(writer)
					if err := jm.acceptWatchFrame(state); err == nil {
						t.Fatal("missing receiver unexpectedly accepted")
					}
					jm.retirementOwner = receiver
					if err := jm.acceptWatchFrame(state); err != nil {
						t.Fatal(err)
					}
					jm.retirementOwner = nil
					if err := receiver.closeAttachedTranscript(); err != nil {
						t.Fatal(err)
					}
				}
				if err := jm.closeStoreOnly(); err != nil {
					t.Fatal(err)
				}
				replayValidationCloseDelegate(t, f)
				held := errors.New("controlled receiver I/O failure")
				fs := producerQueryFaultFS{Fs: afero.NewOsFs()}
				switch boundary {
				case "missing", "identity":
					if err := os.Rename(path, path+".held"); err != nil {
						t.Fatal(err)
					}
					if boundary == "identity" {
						writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "different-replay-receiver"})
						if err != nil {
							t.Fatal(err)
						}
						if err := writer.Close(); err != nil {
							t.Fatal(err)
						}
					}
				case "sync":
					fs.syncErr = held
				case "close":
					fs.closeErr = held
				}
				root, restoreErr := restoreDelegateResourceBootstrapSessionWithTestConfig(f.client, f.profile, f.workspace, f.meta, f.stateDir, testConfig{
					sandboxProber: bwrapCapableProber(f.workspace),
					delegateAttentionOpenWriter: func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
						if boundary == "missing_during_open" {
							if err := os.Rename(path, path+".held"); err != nil {
								return nil, nil, err
							}
						}
						return transcript.OpenWriterForSessionWithFS(fs, path, id)
					},
				})
				if root != nil {
					root.Close()
				}
				if restoreErr == nil || ((boundary == "sync" || boundary == "close") && !errors.Is(restoreErr, held)) {
					t.Errorf("uncertain custody restore error=%v", restoreErr)
				}
				if len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 {
					t.Error("uncertain custody settled source")
				}
				if boundary == "missing" || boundary == "missing_during_open" {
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						t.Error("custody probe recreated missing receiver transcript")
					}
				}
				if boundary == "missing" || boundary == "identity" || boundary == "missing_during_open" {
					if boundary == "identity" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Rename(path+".held", path); err != nil {
						t.Fatal(err)
					}
				}
				root, err = restoreDelegateResourceBootstrapSession(f.client, f.profile, f.workspace, f.meta, f.stateDir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(root.Close)
				if err := repairStableWatchDeliveriesForBootstrap(root.delegateController); err != nil {
					t.Fatal(err)
				}
				acks, drops := producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered), producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)
				want := 0
				if accepted {
					want = 1
				}
				if countAttentionEntries(t, path, stableWatchAttentionID(state)) != want || len(acks) != want || len(drops) != 1-want {
					t.Error("repaired custody did not settle exactly once without resurrection")
				}
				if len(f.adapter.Requests()) != 0 {
					t.Error("bootstrap executed provider")
				}
			})
		}
	}
}
