package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func TestSession_DetailedStatus_DelegatesMatchControllerFoldAfterReopen(t *testing.T) {
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Description = "stable status description"
		descriptor.ParentWatchGranted = true
		descriptor.DelegationAllowance = 2
	})
	want, _, err := LoadSessionDelegateStatus(fixture.stateDir, fixture.meta.ID)
	if err != nil {
		t.Fatalf("cold stable status: %v", err)
	}
	reopened, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer reopened.Close()
	gotStatus := reopened.DetailedStatus()
	if len(want) != 1 || len(gotStatus.Delegates) != 1 {
		t.Fatalf("cold/reopened stable delegates = %d/%d, want 1/1", len(want), len(gotStatus.Delegates))
	}
	if !reflect.DeepEqual(want, gotStatus.Delegates) {
		t.Fatalf("stable status differs from reopened fold:\ncold=%+v\nreopened=%+v", want, gotStatus.Delegates)
	}
	got := gotStatus.Delegates[0]
	if got.DelegateID != fixture.delegateID || got.ChildSessionID != fixture.childID || got.OwnerSessionID != fixture.meta.ID || got.Type != "delegate" || got.Phase != "idle" || got.ProjectionRevision == 0 {
		t.Fatalf("stable delegate status = %+v", got)
	}
	if got.Description != "stable status description" || !got.ParentWatchGranted || got.DelegationAllowance != 2 {
		t.Fatalf("descriptor fidelity = %+v", got)
	}
}

func TestStableDelegateAttention_RestoreAndColdRead(t *testing.T) {
	tests := []struct {
		name              string
		pending           bool
		owed              bool
		journalAttention  bool
		lifecycle         string
		transcriptFailure string
		wantAttention     bool
		wantColdError     bool
		wantRestore       bool
		deferredLaunch    bool
		nestedOwner       bool
		callbackOwnership bool
	}{
		{name: "stale false with pending attention", pending: true, wantAttention: true, wantRestore: true},
		{name: "stale true without pending attention", journalAttention: true, wantRestore: true},
		{name: "closed delegate skips missing transcript", journalAttention: true, lifecycle: "closed", transcriptFailure: "missing", wantRestore: true},
		{name: "stopping delegate skips missing transcript", journalAttention: true, lifecycle: "stopping", transcriptFailure: "missing", wantRestore: true},
		{name: "permanently fenced delegate skips missing transcript", journalAttention: true, lifecycle: "fenced", transcriptFailure: "missing", wantRestore: true},
		{name: "eligible missing transcript is an error", transcriptFailure: "missing", wantColdError: true},
		{name: "eligible unreadable transcript is an error", transcriptFailure: "unreadable", wantColdError: true},
		{name: "owed generation is admitted before boolean repair", owed: true, journalAttention: true, wantRestore: true},
		{name: "recovered owed launch waits for final reconciliation", wantRestore: true, deferredLaunch: true},
		{name: "nested cold owner side effects wait and invalidate stale owed launch", wantRestore: true, nestedOwner: true},
		{name: "owed run callback replacement survives gate open", callbackOwnership: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newColdStableDelegateFixture(t, "")
			targetDelegateID := fixture.delegateID
			targetChildID := fixture.childID
			if tt.lifecycle == "fenced" {
				targetDelegateID = "dlg_permanently_fenced"
				targetChildID = "permanentlyfenced"
			}
			childPath := transcriptPath(fixture.stateDir, targetChildID)
			attentionID := "watch:restore-cold-read:" + strings.ReplaceAll(tt.name, " ", "-")
			if tt.pending || tt.owed {
				if appended, err := appendColdDelegateNotificationDurablyWithOpen(
					childPath,
					targetChildID,
					attentionID,
					"restore and cold-read attention",
					time.Unix(1_700_000_500, 0).UTC(),
					transcript.OpenWriterForSession,
				); err != nil || !appended {
					t.Fatalf("append attention = appended:%t err:%v", appended, err)
				}
			}
			if tt.owed {
				writer, _, err := transcript.OpenWriterForSession(childPath, targetChildID)
				if err != nil {
					t.Fatalf("open owed attention transcript: %v", err)
				}
				resolution := delegateAttentionResolutionTurnForGeneration(attentionID, delegateAttentionConsumed, 1)
				if err := writer.AppendDurable(resolution); err != nil {
					_ = writer.Close()
					t.Fatalf("append owed resolution: %v", err)
				}
				if err := writer.Close(); err != nil {
					t.Fatalf("close owed attention transcript: %v", err)
				}
			}

			if tt.journalAttention || tt.lifecycle == "closed" || tt.lifecycle == "stopping" || tt.lifecycle == "fenced" {
				store, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
				if err != nil {
					t.Fatalf("open delegate store: %v", err)
				}
				events, err := store.Load()
				if err != nil {
					_ = store.Close()
					t.Fatalf("load delegate store: %v", err)
				}
				state, err := delegatestore.Fold(events)
				if err != nil {
					_ = store.Close()
					t.Fatalf("fold delegate store: %v", err)
				}
				appendEvents := make([]delegatestore.Event, 0, 3)
				if tt.lifecycle == "fenced" {
					descriptor := state[fixture.delegateID].Descriptor
					descriptor.ChildSessionID = targetChildID
					descriptor.TranscriptRef = encodeRef("", targetChildID)
					descriptor.ParentDelegateID = fixture.delegateID
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:       delegatestore.EventDelegateCreated,
						DelegateID: targetDelegateID,
						Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
					})
				}
				if tt.journalAttention {
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:       delegatestore.EventDelegateAttentionChanged,
						DelegateID: targetDelegateID,
						AttentionChanged: &delegatestore.DelegateAttentionChanged{
							NeedsAttention: true,
						},
					})
				}
				switch tt.lifecycle {
				case "closed":
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:               delegatestore.EventDelegateResumabilityClosed,
						DelegateID:         fixture.delegateID,
						ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "test closed"},
					})
				case "stopping":
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:                 delegatestore.EventDelegateSubtreeStopRequested,
						DelegateID:           fixture.delegateID,
						SubtreeStopRequested: &delegatestore.SubtreeStopRequested{TargetDelegateID: fixture.delegateID},
					})
				case "fenced":
					appendEvents = append(appendEvents, delegatestore.Event{
						Kind:               delegatestore.EventDelegateResumabilityClosed,
						DelegateID:         fixture.delegateID,
						ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "test permanent fence"},
					})
				}
				if _, _, err := store.AppendBatch(state, appendEvents); err != nil {
					_ = store.Close()
					t.Fatalf("append delegate setup: %v", err)
				}
				if err := store.Close(); err != nil {
					t.Fatalf("close delegate store: %v", err)
				}
			}

			switch tt.transcriptFailure {
			case "missing":
				if err := os.Remove(childPath); err != nil && !os.IsNotExist(err) {
					t.Fatalf("remove child transcript: %v", err)
				}
			case "unreadable":
				if err := os.WriteFile(childPath, []byte("not a transcript\n"), 0o644); err != nil {
					t.Fatalf("corrupt child transcript: %v", err)
				}
			}

			cold, _, coldErr := LoadSessionDelegateStatus(fixture.stateDir, fixture.meta.ID)
			if tt.wantColdError {
				if coldErr == nil {
					t.Fatal("cold delegate status accepted an eligible missing/unreadable transcript")
				}
				return
			}
			if coldErr != nil {
				t.Fatalf("cold delegate status: %v", coldErr)
			}
			coldIndex := slices.IndexFunc(cold, func(row DelegateStatusInfo) bool { return row.DelegateID == targetDelegateID })
			if coldIndex < 0 || cold[coldIndex].NeedsAttention != tt.wantAttention {
				t.Fatalf("cold delegate status = %+v, want needs_attention=%t", cold, tt.wantAttention)
			}
			journalEvents, err := delegatestore.ReadEvents(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
			if err != nil {
				t.Fatalf("read journal after cold status: %v", err)
			}
			journalState, err := delegatestore.Fold(journalEvents)
			if err != nil {
				t.Fatalf("fold journal after cold status: %v", err)
			}
			wantJournalAttention := tt.journalAttention && tt.lifecycle != "closed" && tt.lifecycle != "stopping"
			if got := journalState[targetDelegateID].NeedsAttention; got != wantJournalAttention {
				t.Fatalf("cold status wrote journal needs_attention=%t, want unchanged %t", got, wantJournalAttention)
			}
			if tt.callbackOwnership {
				normalWakes, replacementWakes := 0, 0
				target := &Session{notifyFunc: func() { normalWakes++ }}
				sub := &subagent{sess: target, running: true}
				gate := &owedBootstrapRestore{held: true, pending: make(map[*Session]func()), done: make(map[*subagent]bool)}
				if err := gate.add(nil, sub, delegateStartCommit{}); err != nil {
					t.Fatalf("install owed bootstrap gate: %v", err)
				}
				target.notify()
				target.SetNotifyFunc(func() { replacementWakes++ })
				gate.open()
				target.notify()
				if normalWakes != 1 || replacementWakes != 1 {
					t.Fatalf("gate callback ownership = normal:%d replacement:%d, want queued normal once and replacement wake once", normalWakes, replacementWakes)
				}
				return
			}

			if !tt.wantRestore {
				return
			}
			var release chan struct{}
			if tt.owed {
				release = make(chan struct{})
				fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
					<-release
					return communicateResponse(true, "owed attention restored")
				}}
			}
			restored, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
			if err != nil {
				if release != nil {
					close(release)
				}
				t.Fatalf("restore delegate status: %v", err)
			}
			defer func() {
				if release != nil {
					close(release)
				}
				restored.Close()
			}()

			restored.delegateController.mu.Lock()
			aggregate := restored.delegateController.durable[targetDelegateID]
			wakeIDs := slices.Sorted(maps.Keys(restored.delegateController.attentionWakeIDs[targetDelegateID]))
			restored.delegateController.mu.Unlock()
			if aggregate == nil || aggregate.NeedsAttention != tt.wantAttention {
				t.Fatalf("restored aggregate = %+v, want needs_attention=%t", aggregate, tt.wantAttention)
			}
			if tt.pending && !reflect.DeepEqual(wakeIDs, []string{attentionID}) {
				t.Fatalf("restored unresolved attention = %#v, want %#v", wakeIDs, []string{attentionID})
			}
			if !tt.pending && len(wakeIDs) != 0 {
				t.Fatalf("restored unresolved attention = %#v, want none", wakeIDs)
			}

			if tt.owed {
				data, err := os.ReadFile(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
				if err != nil {
					t.Fatalf("read owed delegate journal: %v", err)
				}
				lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
				var lastBatch struct {
					Events []delegatestore.Event `json:"events"`
				}
				if len(lines) == 0 || json.Unmarshal(lines[len(lines)-1], &lastBatch) != nil {
					t.Fatalf("decode final owed admission batch: %q", lines[len(lines)-1])
				}
				if len(lastBatch.Events) != 2 || lastBatch.Events[0].Kind != delegatestore.EventDelegateAttentionChanged || lastBatch.Events[0].AttentionChanged == nil || lastBatch.Events[0].AttentionChanged.NeedsAttention || lastBatch.Events[1].Kind != delegatestore.EventDelegateRunStarted || lastBatch.Events[1].RunStarted == nil || lastBatch.Events[1].RunStarted.Generation != 1 {
					t.Fatalf("final owed admission batch = %#v, want attention false then generation 1 RunStarted", lastBatch.Events)
				}
			}

			if tt.deferredLaunch {
				const (
					owedID       = "watch:restore-cold-read:deferred-owed"
					witnessID    = "dlg_reconcile_witness"
					witnessChild = "reconcilewitness"
					witnessWake  = "watch:restore-cold-read:reconcile-witness"
					laterWake    = "watch:restore-cold-read:opened-after-reconcile"
				)
				if appended, err := appendColdDelegateNotificationDurablyWithOpen(
					childPath,
					targetChildID,
					owedID,
					"recover this owed generation",
					time.Unix(1_700_000_600, 0).UTC(),
					transcript.OpenWriterForSession,
				); err != nil || !appended {
					t.Fatalf("append deferred owed attention = appended:%t err:%v", appended, err)
				}
				writer, _, err := transcript.OpenWriterForSession(childPath, targetChildID)
				if err != nil {
					t.Fatalf("open deferred owed transcript: %v", err)
				}
				if err := writer.AppendDurable(delegateAttentionResolutionTurnForGeneration(owedID, delegateAttentionConsumed, 1)); err != nil {
					_ = writer.Close()
					t.Fatalf("append deferred owed resolution: %v", err)
				}
				if err := writer.Close(); err != nil {
					t.Fatalf("close deferred owed transcript: %v", err)
				}

				witnessPath := transcriptPath(fixture.stateDir, witnessChild)
				witnessWriter, err := transcript.NewWriter(witnessPath, transcript.Header{SessionID: witnessChild, ParentSessionID: fixture.meta.ID})
				if err != nil {
					t.Fatalf("create reconciliation witness transcript: %v", err)
				}
				if err := witnessWriter.Close(); err != nil {
					t.Fatalf("close reconciliation witness transcript: %v", err)
				}
				if appended, err := appendColdDelegateNotificationDurablyWithOpen(
					witnessPath,
					witnessChild,
					witnessWake,
					"prove final reconciliation completed",
					time.Unix(1_700_000_601, 0).UTC(),
					transcript.OpenWriterForSession,
				); err != nil || !appended {
					t.Fatalf("append reconciliation witness = appended:%t err:%v", appended, err)
				}

				restored.delegateController.mu.Lock()
				descriptor := restored.delegateController.durable[targetDelegateID].Descriptor
				descriptor.ChildSessionID = witnessChild
				descriptor.TranscriptRef = encodeRef("", witnessChild)
				descriptor.Task = "reconciliation witness"
				_, err = restored.delegateController.appendLocked(
					delegatestore.Event{
						Kind:       delegatestore.EventDelegateAttentionChanged,
						DelegateID: targetDelegateID,
						AttentionChanged: &delegatestore.DelegateAttentionChanged{
							NeedsAttention: true,
						},
					},
					delegatestore.Event{
						Kind:       delegatestore.EventDelegateCreated,
						DelegateID: witnessID,
						Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
					},
				)
				restored.delegateController.owedAdmission = true
				restored.delegateController.mu.Unlock()
				if err != nil {
					t.Fatalf("append deferred launch setup: %v", err)
				}

				childReady := make(chan *Session, 1)
				restored.delegateRestoreBeforeSideEffects = func(child *Session) { childReady <- child }
				opened := make(chan error, 1)
				releaseRun := make(chan struct{})
				defer close(releaseRun)
				fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
					child := <-childReady
					child.delegateController.mu.Lock()
					witness := child.delegateController.durable[witnessID]
					_, witnessPublished := child.delegateController.attentionWakeIDs[witnessID][witnessWake]
					child.delegateController.mu.Unlock()
					if witness == nil || !witness.NeedsAttention || !witnessPublished {
						opened <- fmt.Errorf("owed launch observed unreconciled witness: aggregate=%+v published=%t", witness, witnessPublished)
						<-releaseRun
						return communicateResponse(true, "unreconciled")
					}
					if _, err := child.appendDelegateNotificationDurably(laterWake, "opened after final reconciliation"); err != nil {
						opened <- err
					} else {
						opened <- child.armDelegateAttention(laterWake)
					}
					<-releaseRun
					return communicateResponse(true, "deferred launch complete")
				}}

				if err := restored.flushPendingDelegateDeliveries(); err != nil {
					t.Fatalf("flush deferred owed launch: %v", err)
				}
				if err := <-opened; err != nil {
					t.Fatalf("deferred owed launch: %v", err)
				}
				restored.delegateController.mu.Lock()
				target := restored.delegateController.durable[targetDelegateID]
				_, laterPublished := restored.delegateController.attentionWakeIDs[targetDelegateID][laterWake]
				restored.delegateController.mu.Unlock()
				if target == nil || !target.NeedsAttention || !laterPublished {
					t.Fatalf("post-reconciliation attention was clobbered: aggregate=%+v published=%t", target, laterPublished)
				}
			}

			if tt.nestedOwner {
				const (
					nestedID     = "dlg_nested_owed"
					witnessID    = "dlg_nested_reconcile_witness"
					witnessChild = "nestedreconcilewitness"
					witnessWake  = "watch:restore-cold-read:nested-reconcile-witness"
					owedID       = "watch:restore-cold-read:nested-owed"
				)
				nestedChild := identifier.MustNewSessionID()
				nestedPath := transcriptPath(fixture.stateDir, nestedChild)
				nestedWriter, err := transcript.NewWriter(nestedPath, transcript.Header{
					SessionID:       nestedChild,
					ParentSessionID: fixture.childID,
					ProfileID:       "openai",
					Model:           "gpt-5.2",
				})
				if err != nil {
					t.Fatalf("create nested owed transcript: %v", err)
				}
				if err := nestedWriter.Close(); err != nil {
					t.Fatalf("close nested owed transcript: %v", err)
				}
				if appended, err := appendColdDelegateNotificationDurablyWithOpen(
					nestedPath,
					nestedChild,
					owedID,
					"recover nested owed generation",
					time.Unix(1_700_000_700, 0).UTC(),
					transcript.OpenWriterForSession,
				); err != nil || !appended {
					t.Fatalf("append nested owed attention = appended:%t err:%v", appended, err)
				}
				nestedWriter, _, err = transcript.OpenWriterForSession(nestedPath, nestedChild)
				if err != nil {
					t.Fatalf("open nested owed transcript: %v", err)
				}
				if err := nestedWriter.AppendDurable(delegateAttentionResolutionTurnForGeneration(owedID, delegateAttentionConsumed, 1)); err != nil {
					_ = nestedWriter.Close()
					t.Fatalf("append nested owed resolution: %v", err)
				}
				if err := nestedWriter.Close(); err != nil {
					t.Fatalf("close nested owed resolution: %v", err)
				}
				nestedMeta := fixture.meta
				nestedMeta.ID = nestedChild
				nestedMeta.ParentSessionID = fixture.childID
				nestedMeta.IsSubagent = true
				if err := schema.SaveSessionMeta(fixture.stateDir, nestedMeta); err != nil {
					t.Fatalf("save nested owed metadata: %v", err)
				}

				witnessPath := transcriptPath(fixture.stateDir, witnessChild)
				witnessWriter, err := transcript.NewWriter(witnessPath, transcript.Header{SessionID: witnessChild, ParentSessionID: fixture.meta.ID})
				if err != nil {
					t.Fatalf("create nested reconciliation witness: %v", err)
				}
				if err := witnessWriter.Close(); err != nil {
					t.Fatalf("close nested reconciliation witness: %v", err)
				}
				if appended, err := appendColdDelegateNotificationDurablyWithOpen(
					witnessPath,
					witnessChild,
					witnessWake,
					"prove nested owner side effects waited",
					time.Unix(1_700_000_701, 0).UTC(),
					transcript.OpenWriterForSession,
				); err != nil || !appended {
					t.Fatalf("append nested reconciliation witness = appended:%t err:%v", appended, err)
				}

				restored.delegateController.mu.Lock()
				nestedDescriptor := restored.delegateController.durable[targetDelegateID].Descriptor
				nestedDescriptor.ChildSessionID = nestedChild
				nestedDescriptor.TranscriptRef = encodeRef("", nestedChild)
				nestedDescriptor.ParentDelegateID = targetDelegateID
				nestedDescriptor.OwnerSessionID = fixture.childID
				nestedDescriptor.VisibleSessionID = fixture.childID
				nestedDescriptor.Task = "nested owed child"
				witnessDescriptor := restored.delegateController.durable[targetDelegateID].Descriptor
				witnessDescriptor.ChildSessionID = witnessChild
				witnessDescriptor.TranscriptRef = encodeRef("", witnessChild)
				witnessDescriptor.Task = "nested reconciliation witness"
				_, err = restored.delegateController.appendLocked(
					delegatestore.Event{
						Kind:       delegatestore.EventDelegateCreated,
						DelegateID: nestedID,
						Created:    &delegatestore.DelegateCreated{Descriptor: nestedDescriptor},
					},
					delegatestore.Event{
						Kind:       delegatestore.EventDelegateAttentionChanged,
						DelegateID: nestedID,
						AttentionChanged: &delegatestore.DelegateAttentionChanged{
							NeedsAttention: true,
						},
					},
					delegatestore.Event{
						Kind:       delegatestore.EventDelegateCreated,
						DelegateID: witnessID,
						Created:    &delegatestore.DelegateCreated{Descriptor: witnessDescriptor},
					},
				)
				restored.delegateController.owedAdmission = true
				restored.delegateController.mu.Unlock()
				if err != nil {
					t.Fatalf("append nested owed setup: %v", err)
				}

				parentJM, err := newJobManagerNoSync(fixture.stateDir, fixture.childID, nil)
				if err != nil {
					t.Fatalf("open cold parent job manager: %v", err)
				}
				parentJM.parentDelegateID = targetDelegateID
				parentJM.now = func() time.Time { return time.Unix(1_700_000_702, 0).UTC() }
				record, err := parentJM.createShell(createShellOpts{Command: "true", Description: "invalidate nested owed child"})
				if err == nil {
					code := 0
					err = parentJM.finalize(record.JobID, jobstore.StatusCompleted, "exit_zero", &code)
				}
				if closeErr := parentJM.closeStoreOnly(); err == nil {
					err = closeErr
				}
				if err != nil {
					t.Fatalf("seed cold parent terminal notification: %v", err)
				}

				releaseNested := make(chan struct{})
				var releaseNestedOnce sync.Once
				defer releaseNestedOnce.Do(func() { close(releaseNested) })
				fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
					<-releaseNested
					return communicateResponse(true, "nested owed run stopped")
				}}
				notified := make(chan error, 1)
				stopDone := make(chan (<-chan struct{}), 1)
				var notifyOnce sync.Once
				restored.delegateRestoreBeforeSideEffects = func(child *Session) {
					if child.ID() != fixture.childID {
						return
					}
					child.SetNotifyFunc(func() {
						notifyOnce.Do(func() {
							nestedSub := child.subagents.get(nestedChild)
							nestedRunning := false
							if nestedSub != nil {
								nestedSub.mu.Lock()
								nestedRunning = nestedSub.running
								nestedSub.mu.Unlock()
							}
							child.delegateController.mu.Lock()
							witness := child.delegateController.durable[witnessID]
							_, published := child.delegateController.attentionWakeIDs[witnessID][witnessWake]
							child.delegateController.mu.Unlock()
							if witness == nil || !witness.NeedsAttention || !published || !nestedRunning {
								notified <- fmt.Errorf("queued cold-parent drive observed publication=%t aggregate=%+v nested_running=%t", published, witness, nestedRunning)
								return
							}
							result, cancelPlan, _, stopErr := child.delegateController.StopSubtreeAndDrive(rootDelegateActor(fixture.meta.ID), targetDelegateID)
							executeDelegateCancelPlan(cancelPlan)
							stopDone <- result.done
							notified <- stopErr
						})
					})
				}

				if err := restored.flushPendingDelegateDeliveries(); err != nil {
					t.Fatalf("flush nested cold-owner owed recovery: %v", err)
				}
				if err := <-notified; err != nil {
					t.Fatalf("cold parent pending notification: %v", err)
				}
				releaseNestedOnce.Do(func() { close(releaseNested) })
				if done := <-stopDone; done != nil {
					<-done
				}
				restored.delegateController.mu.Lock()
				nested := restored.delegateController.durable[nestedID]
				nestedLive := restored.delegateController.live[nestedID]
				restored.delegateController.mu.Unlock()
				nestedRunning := false
				if parentSub := restored.subagents.get(fixture.childID); parentSub != nil && parentSub.sess != nil {
					if nestedSub := parentSub.sess.subagents.get(nestedChild); nestedSub != nil {
						nestedSub.mu.Lock()
						nestedRunning = nestedSub.running
						nestedSub.mu.Unlock()
					}
				}
				if nested == nil || nested.CurrentRunOpen || nested.Generation != 1 || nestedLive == nil || nestedLive.binding != nil || nestedRunning {
					t.Fatalf("invalidated nested owed runtime leaked or launched: aggregate=%+v live=%+v", nested, nestedLive)
				}
			}
		})
	}
}

func TestSession_DetailedStatus_CoreTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// Should have core tools.
	if len(ds.Tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	// All tools from a vanilla session should be "core".
	for _, tool := range ds.Tools {
		if tool.Source != "core" {
			t.Errorf("tool %q has source %q, want core", tool.Name, tool.Source)
		}
	}

	// Verify some known core tools are present.
	toolNames := map[string]bool{}
	for _, tool := range ds.Tools {
		toolNames[tool.Name] = true
	}
	for _, name := range []string{"shell", "read_file", "write_file", "edit_file"} {
		if !toolNames[name] {
			t.Errorf("missing core tool %q", name)
		}
	}
}

func TestSession_DetailedStatus_CustomTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Register a custom tool after session init.
	sess.RegisterTool("my_custom_tool", "A custom tool", map[string]any{
		"type": "object", "properties": map[string]any{},
	}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	ds := sess.DetailedStatus()

	found := false
	for _, tool := range ds.Tools {
		if tool.Name == "my_custom_tool" {
			if tool.Source != "custom" {
				t.Errorf("custom tool source = %q, want custom", tool.Source)
			}
			found = true
		}
	}
	if !found {
		t.Error("custom tool not found in DetailedStatus")
	}
}

func TestSession_DetailedStatus_Skills(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a skill directory.
	skillDir := filepath.Join(dir, "skills", "my-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: A test skill
---
# My Skill
`), 0o644)

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	found := false
	for _, skill := range ds.Skills {
		if skill.Name == "my-skill" {
			found = true
			if skill.Description != "A test skill" {
				t.Errorf("skill description = %q, want %q", skill.Description, "A test skill")
			}
		}
	}
	if !found {
		t.Error("skill my-skill not found in DetailedStatus")
	}
}

func TestSession_DetailedStatus_EmptySections(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// No MCP servers in vanilla session.
	if len(ds.MCP) != 0 {
		t.Errorf("expected no MCP servers, got %d", len(ds.MCP))
	}
	// No plugins in a vanilla session.
	if len(ds.Plugins) != 0 {
		t.Errorf("expected no plugins, got %d", len(ds.Plugins))
	}
	// No jobs.
	if len(ds.Jobs) != 0 {
		t.Errorf("expected no jobs, got %d", len(ds.Jobs))
	}
	// Core agents are always present.
	foundDefault := false
	foundExplorer := false
	foundSubagent := false
	for _, name := range ds.Agents {
		if name == "default" {
			foundDefault = true
		}
		if name == "explorer" {
			foundExplorer = true
		}
		if name == "subagent" {
			foundSubagent = true
		}
	}
	if !foundDefault {
		t.Errorf("expected core 'default' agent in %v", ds.Agents)
	}
	if !foundExplorer {
		t.Errorf("expected core 'explorer' agent in %v", ds.Agents)
	}
	if !foundSubagent {
		t.Errorf("expected core 'subagent' agent in %v", ds.Agents)
	}
}

func TestSession_DetailedStatus_ConfiguredWorkflowPlugin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), coordinatorWorkflowSessionConfig(t, SessionConfig{}))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	if len(ds.Plugins) != 1 {
		t.Fatalf("expected 1 coordinator workflow plugin, got %d", len(ds.Plugins))
	}
	if ds.Plugins[0].Name != coordinatorWorkflowPluginName {
		t.Fatalf("plugin name = %q, want %q", ds.Plugins[0].Name, coordinatorWorkflowPluginName)
	}

	foundReviewer := slices.Contains(ds.Agents, "reviewer")
	if !foundReviewer {
		t.Fatalf("expected configured coordinator workflow reviewer agent in %v", ds.Agents)
	}
}

func TestSession_DetailedStatus_Jobs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	exitCode := 7
	startedAt := time.Now().UTC()
	endedAt := startedAt.Add(time.Second)
	const jobID = "job_status_projection"
	if err := sess.jobManager.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		OwnerSessionID:   sess.ID(),
		VisibleToSession: sess.ID(),
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append started event: %v", err)
	}
	if err := sess.jobManager.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      jobstore.StatusFailed,
		Reason:      "exit_nonzero",
		ExitCode:    &exitCode,
		EndedAt:     &endedAt,
		OutputBytes: 128,
	}); err != nil {
		t.Fatalf("append finished event: %v", err)
	}

	ds := sess.DetailedStatus()

	if len(ds.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(ds.Jobs))
	}
	job := ds.Jobs[0]
	if job.JobID != jobID || job.JobType != string(jobstore.JobShell) || job.Status != string(jobstore.StatusFailed) ||
		job.Reason != "exit_nonzero" || job.TranscriptRef != shellTranscriptRef(jobID) ||
		job.OutputBytes != 128 || job.ExitCode == nil || *job.ExitCode != exitCode {
		t.Fatalf("job status = %+v", job)
	}
}

func TestDetailedStatusJobRecords_OmitsLegacyDelegateActivations(t *testing.T) {
	records := detailedStatusJobRecords([]*jobstore.JobRecord{{
		JobID:  "job_exhausted",
		Type:   jobstore.JobType(delegateResourceType),
		Status: jobstore.StatusExhausted,
		Reason: "tool_round_budget_exhausted",
	}})
	if len(records) != 0 {
		t.Fatalf("legacy delegate activation records = %+v, want none", records)
	}
}

func TestSession_DetailedStatus_JobsKeepsActiveAndBoundsTerminal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	base := time.Now().UTC()
	runningStartedAt := base.Add(-time.Hour)
	const runningJobID = "job_running_old"

	// Build every event up front and append it as one batch so the store fsyncs
	// once instead of ~105 times. AppendBatch assigns contiguous Seq in slice
	// order, identical to sequential Append, so Fold/sort order is unchanged.
	events := []jobstore.Event{{
		Kind:             jobstore.EventJobStarted,
		TS:               runningStartedAt,
		JobID:            runningJobID,
		Type:             jobstore.JobShell,
		Status:           jobstore.StatusRunning,
		OwnerSessionID:   sess.ID(),
		VisibleToSession: sess.ID(),
		StartedAt:        &runningStartedAt,
	}}

	for i := range detailedStatusTerminalJobsLimit + 2 {
		startedAt := base.Add(time.Duration(i) * time.Second)
		endedAt := startedAt.Add(time.Second)
		jobID := fmt.Sprintf("job_terminal_%02d", i)
		events = append(events, jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               startedAt,
			JobID:            jobID,
			Type:             jobstore.JobShell,
			Status:           jobstore.StatusRunning,
			OwnerSessionID:   sess.ID(),
			VisibleToSession: sess.ID(),
			StartedAt:        &startedAt,
		}, jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          endedAt,
			JobID:       jobID,
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &endedAt,
			OutputBytes: int64(i),
		})
	}

	if err := sess.jobManager.store.AppendBatch(events); err != nil {
		t.Fatalf("append job events: %v", err)
	}

	ds := sess.DetailedStatus()
	seen := map[string]JobStatusInfo{}
	terminal := 0
	for _, job := range ds.Jobs {
		seen[job.JobID] = job
		if jobstore.Status(job.Status).IsTerminal() {
			terminal++
		}
	}

	if _, ok := seen[runningJobID]; !ok {
		t.Fatalf("active job %q missing from DetailedStatus jobs: %+v", runningJobID, ds.Jobs)
	}
	if terminal != detailedStatusTerminalJobsLimit {
		t.Fatalf("terminal jobs = %d, want %d", terminal, detailedStatusTerminalJobsLimit)
	}
	if _, ok := seen["job_terminal_00"]; ok {
		t.Fatalf("oldest terminal job should be excluded from bounded DetailedStatus jobs: %+v", ds.Jobs)
	}
	if _, ok := seen[fmt.Sprintf("job_terminal_%02d", detailedStatusTerminalJobsLimit+1)]; !ok {
		t.Fatalf("newest terminal job missing from DetailedStatus jobs: %+v", ds.Jobs)
	}
}

func TestSession_DetailedStatus_ToolsSorted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	f := &fakeAdapter{
		name:  "openai",
		steps: []func(req llm.Request) llm.Response{},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	names := make([]string, len(ds.Tools))
	for i, tool := range ds.Tools {
		names[i] = tool.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("tools not sorted: %v", names)
	}
}

// TestDetailedStatus_HookEvents_ExcludesDeadHooks verifies that /status's supported
// hook count reflects only hooks that can actually run: a hook whose handler type is
// unsupported (http) or whose matcher is an invalid regex is dispatch-time dead, so
// it must not be counted as a supported active hook. The legacy Hooks map (registered
// hooks per event) still counts them (Fix 4).
func TestDetailedStatus_HookEvents_ExcludesDeadHooks(t *testing.T) {
	t.Parallel()
	pluginDir := t.TempDir()
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	os.MkdirAll(metaDir, 0o755)
	os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "dead-hook-test"}`), 0o644)
	hooksDir := filepath.Join(pluginDir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	// PreToolUse: ONLY an http handler (never executes → dispatch-time dead).
	// PostToolUse: a command handler with an invalid-regex matcher (skipped at dispatch).
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(`{
		"hooks": {
			"PreToolUse":  [{"matcher": "*", "hooks": [{"type": "http", "url": "http://example"}]}],
			"PostToolUse": [{"matcher": "(", "hooks": [{"type": "command", "command": "echo x", "timeout": 5}]}]
		}
	}`), 0o644)

	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// Neither dead hook may surface as a supported active hook.
	for _, he := range ds.HookEvents {
		if !he.Supported {
			continue
		}
		if he.Event == plugin.HookPreToolUse {
			t.Errorf("PreToolUse has only an http (unsupported-type) handler; it must not be a supported active hook (got Count=%d)", he.Count)
		}
		if he.Event == plugin.HookPostToolUse {
			t.Errorf("PostToolUse's only handler has an invalid matcher; it must not be a supported active hook (got Count=%d)", he.Count)
		}
	}
}

// TestDetailedStatus_HookEvents verifies that DetailedStatus.HookEvents lists
// supported hook events with their tier and count, and lists recognized-but-
// unsupported events with Supported=false, Count=0, Tier="reserved-placeholder".
// The legacy Hooks map is preserved for backward compatibility.
func TestDetailedStatus_HookEvents(t *testing.T) {
	t.Parallel()
	// Build a plugin dir with PreToolUse (supported) and "Setup" (recognized but
	// not fired by evener — reserved-placeholder).
	pluginDir := t.TempDir()
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	os.MkdirAll(metaDir, 0o755)
	os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "hook-diag-test"}`), 0o644)
	hooksDir := filepath.Join(pluginDir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(`{
		"hooks": {
			"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo ok", "timeout": 5}]}],
			"Setup":      [{"matcher": "*", "hooks": [{"type": "command", "command": "echo setup", "timeout": 5}]}]
		}
	}`), 0o644)

	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ds := sess.DetailedStatus()

	// Legacy Hooks map should have PreToolUse with count ≥ 1.
	if ds.Hooks[plugin.HookPreToolUse] < 1 {
		t.Errorf("Hooks[PreToolUse] = %d, want ≥ 1", ds.Hooks[plugin.HookPreToolUse])
	}

	// HookEvents should include PreToolUse as supported/claude-compatible-subset.
	var foundSupported, foundUnsupported bool
	for _, he := range ds.HookEvents {
		switch he.Event {
		case plugin.HookPreToolUse:
			if !he.Supported {
				t.Errorf("PreToolUse: Supported = false, want true")
			}
			if he.Tier != "claude-compatible-subset" {
				t.Errorf("PreToolUse: Tier = %q, want claude-compatible-subset", he.Tier)
			}
			if he.Count < 1 {
				t.Errorf("PreToolUse: Count = %d, want ≥ 1", he.Count)
			}
			foundSupported = true
		case "Setup":
			if he.Supported {
				t.Errorf("Setup: Supported = true, want false")
			}
			if he.Tier != "reserved-placeholder" {
				t.Errorf("Setup: Tier = %q, want reserved-placeholder", he.Tier)
			}
			if he.Count != 0 {
				t.Errorf("Setup: Count = %d, want 0", he.Count)
			}
			foundUnsupported = true
		}
	}
	if !foundSupported {
		t.Error("HookEvents missing PreToolUse (supported)")
	}
	if !foundUnsupported {
		t.Error("HookEvents missing Setup (unsupported/reserved-placeholder)")
	}
}
