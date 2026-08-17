package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/provenance"
)

type delegateStopWaitBarrierContext struct {
	entered  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func newDelegateStopWaitBarrierContext() *delegateStopWaitBarrierContext {
	return &delegateStopWaitBarrierContext{
		entered:  make(chan struct{}, 8),
		canceled: make(chan struct{}),
	}
}

func (c *delegateStopWaitBarrierContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *delegateStopWaitBarrierContext) Done() <-chan struct{} {
	c.entered <- struct{}{}
	return c.canceled
}
func (c *delegateStopWaitBarrierContext) Err() error {
	select {
	case <-c.canceled:
		return context.Canceled
	default:
		return nil
	}
}
func (c *delegateStopWaitBarrierContext) Value(any) any { return nil }
func (c *delegateStopWaitBarrierContext) cancel() {
	c.once.Do(func() { close(c.canceled) })
}

func TestDelegateControllerCloseDrainProgressCoversBothWaitOrders(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (*delegateTreeController, *delegateStopState, func(), func())
	}{
		{
			name: "generation finish",
			setup: func(t *testing.T) (*delegateTreeController, *delegateStopState, func(), func()) {
				c, _ := newDelegateControllerTestHarness(t, 2, 1)
				seedDelegateControllerIdle(t, c, "dlg_target", "")
				seedDelegateControllerRunning(t, c, "dlg_first", "dlg_target")
				seedDelegateControllerRunning(t, c, "dlg_second", "dlg_target")
				result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
				if err != nil {
					t.Fatalf("StopSubtree: %v", err)
				}
				finish := func(id string) func() {
					return func() {
						_, err := c.FinishGeneration(delegateLease{delegateID: id, generation: 1}, delegateFinish{
							outcome:     delegatestore.OutcomeCompleted,
							disposition: delegatestore.DispositionCompletedNoAction,
						})
						if err != nil {
							t.Fatalf("FinishGeneration(%s): %v", id, err)
						}
					}
				}
				stop := c.stopForResult(result)
				return c, stop, finish("dlg_first"), finish("dlg_second")
			},
		},
		{
			name: "start release",
			setup: func(t *testing.T) (*delegateTreeController, *delegateStopState, func(), func()) {
				c, _ := newDelegateControllerTestHarness(t, 3, 1)
				seedDelegateControllerRunning(t, c, "dlg_target", "")
				seedDelegateControllerIdle(t, c, "dlg_first", "dlg_target")
				seedDelegateControllerIdle(t, c, "dlg_second", "dlg_target")
				lease := delegateLease{delegateID: "dlg_target", generation: 1}
				actor := delegateActor{lease: &lease}
				first, err := c.ReserveStart(actor, "dlg_first")
				if err != nil {
					t.Fatalf("ReserveStart(first): %v", err)
				}
				second, err := c.ReserveStart(actor, "dlg_second")
				if err != nil {
					t.Fatalf("ReserveStart(second): %v", err)
				}
				if _, err := c.FinishGeneration(lease, delegateFinish{
					outcome: delegatestore.OutcomeFailed,
					reason:  "test parent setup",
				}); err != nil {
					t.Fatalf("FinishGeneration(parent): %v", err)
				}
				result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
				if err != nil {
					t.Fatalf("StopSubtree: %v", err)
				}
				abort := func(reservation *delegateStartReservation) func() {
					return func() {
						if err := c.AbortStart(reservation); err != nil {
							t.Fatalf("AbortStart: %v", err)
						}
					}
				}
				stop := c.stopForResult(result)
				return c, stop, abort(first), abort(second)
			},
		},
		{
			name: "shell finish",
			setup: func(t *testing.T) (*delegateTreeController, *delegateStopState, func(), func()) {
				c, _ := newDelegateControllerTestHarness(t, 1, 1)
				seedDelegateControllerRunning(t, c, "dlg_target", "")
				lease := delegateLease{delegateID: "dlg_target", generation: 1}
				first, err := c.BeginShellWork(lease)
				if err != nil {
					t.Fatalf("BeginShellWork(first): %v", err)
				}
				second, err := c.BeginShellWork(lease)
				if err != nil {
					t.Fatalf("BeginShellWork(second): %v", err)
				}
				if cancelNow, err := c.CommitShellWork(first, "job-first", func() {}); err != nil || cancelNow {
					t.Fatalf("CommitShellWork(first) = cancel:%t err:%v", cancelNow, err)
				}
				if cancelNow, err := c.CommitShellWork(second, "job-second", func() {}); err != nil || cancelNow {
					t.Fatalf("CommitShellWork(second) = cancel:%t err:%v", cancelNow, err)
				}
				if _, err := c.FinishGeneration(lease, delegateFinish{
					outcome: delegatestore.OutcomeFailed,
					reason:  "test parent setup",
				}); err != nil {
					t.Fatalf("FinishGeneration(parent): %v", err)
				}
				result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
				if err != nil {
					t.Fatalf("StopSubtree: %v", err)
				}
				finish := func(token delegateWorkToken, jobID string) func() {
					return func() {
						if _, err := c.ReportShellFinished(token, jobID); err != nil {
							t.Fatalf("ReportShellFinished(%s): %v", jobID, err)
						}
					}
				}
				stop := c.stopForResult(result)
				return c, stop, finish(first, "job-first"), finish(second, "job-second")
			},
		},
		{
			name: "delivery completion",
			setup: func(t *testing.T) (*delegateTreeController, *delegateStopState, func(), func()) {
				c, _ := newDelegateControllerTestHarness(t, 1, 1)
				seedDelegateControllerIdle(t, c, "dlg_target", "")
				seedDelegateControllerIdle(t, c, "dlg_first", "dlg_target")
				seedDelegateControllerIdle(t, c, "dlg_second", "dlg_target")
				c.mu.Lock()
				c.live["dlg_target"] = &delegateLiveState{runtime: &Session{}}
				c.mu.Unlock()
				seedDelegateControllerDelivery(t, c, "dlg_first")
				seedDelegateControllerDelivery(t, c, "dlg_second")
				plans := c.ReplayDeliveries()
				if len(plans) != 2 {
					t.Fatalf("ReplayDeliveries count = %d, want 2", len(plans))
				}
				first, admitted, err := c.BeginDelivery(plans[0])
				if err != nil || !admitted {
					t.Fatalf("BeginDelivery(first) = admitted:%t err:%v", admitted, err)
				}
				second, admitted, err := c.BeginDelivery(plans[1])
				if err != nil || !admitted {
					t.Fatalf("BeginDelivery(second) = admitted:%t err:%v", admitted, err)
				}
				result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
				if err != nil {
					t.Fatalf("StopSubtree: %v", err)
				}
				complete := func(token delegateDeliveryToken) func() {
					return func() {
						if _, err := c.CompleteDelivery(token, false); err != nil {
							t.Fatalf("CompleteDelivery: %v", err)
						}
					}
				}
				stop := c.stopForResult(result)
				return c, stop, complete(first), complete(second)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, stop, mutateBeforeWait, mutateAfterWait := test.setup(t)
			mutateBeforeWait()
			ctx := newDelegateStopWaitBarrierContext()
			result := make(chan error, 1)
			go func() { result <- c.drainStopForClose(ctx, stop) }()
			<-ctx.entered
			mutateAfterWait()
			ctx.cancel()
			if err := <-result; err != nil {
				t.Fatalf("drainStopForClose after exact progress = %v", err)
			}
			select {
			case <-stop.done:
			default:
				t.Fatal("stop did not complete after both exact drain orders")
			}
		})
	}
}

func TestDelegateControllerCloseDrainRetriesStaleEvidence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerRunning(t, c, "dlg_unrelated", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	stop := c.stopForResult(result)
	transcriptPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := syscall.Mkfifo(transcriptPath, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	nextTranscriptPath := transcriptPath + ".next"
	if err := syscall.Mkfifo(nextTranscriptPath, 0o600); err != nil {
		t.Fatalf("Mkfifo next pass: %v", err)
	}
	readerOpened := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		for pass := range 2 {
			file, openErr := os.OpenFile(transcriptPath, os.O_WRONLY, 0)
			if openErr != nil {
				writerDone <- openErr
				return
			}
			readerOpened <- struct{}{}
			if pass == 0 {
				<-releaseFirst
				if renameErr := os.Rename(nextTranscriptPath, transcriptPath); renameErr != nil {
					writerDone <- renameErr
					return
				}
			}
			_, writeErr := file.WriteString(`{"kind":"header","format_version":2,"session_id":"child-dlg_target","created_at":"0001-01-01T00:00:00Z","profile_id":"","model":""}` + "\n")
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				writerDone <- errors.Join(writeErr, closeErr)
				return
			}
		}
		writerDone <- nil
	}()

	drainResult := make(chan error, 1)
	go func() { drainResult <- c.drainStopForClose(context.Background(), stop) }()
	<-readerOpened
	if _, err := c.BeginShellWork(delegateLease{delegateID: "dlg_unrelated", generation: 1}); err != nil {
		t.Fatalf("BeginShellWork unrelated evidence mutation: %v", err)
	}
	close(releaseFirst)
	if err := <-drainResult; err != nil {
		var writerErr error
		select {
		case writerErr = <-writerDone:
		default:
			// Release the second FIFO writer on the old early-return path.
			reader, openErr := os.Open(transcriptPath)
			if openErr == nil {
				_ = reader.Close()
			}
			writerErr = <-writerDone
		}
		t.Fatalf("drainStopForClose after stale evidence = %v (writer: %v)", err, writerErr)
	}
	if err := <-writerDone; err != nil {
		t.Fatalf("transcript FIFO writer: %v", err)
	}
	select {
	case <-stop.done:
	default:
		t.Fatal("stop remained pending after stale evidence recollection")
	}
}

func TestDelegateControllerStopPersistsBeforeCancellationPlan(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	cancelled := false
	c.live["dlg_target"].binding.cancel = func() { cancelled = true }
	result, plan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Kind != delegatestore.EventDelegateSubtreeStopRequested || events[len(events)-1].Seq != result.requestSeq {
		t.Fatalf("last event = %#v, want durable stop request %d", events, result.requestSeq)
	}
	if cancelled || len(plan.cancel) != 1 {
		t.Fatalf("cancellation before plan execution = %t, plan=%#v", cancelled, plan)
	}
	plan.cancel[0]()
	if !cancelled {
		t.Fatal("captured cancellation plan did not cancel exact runtime")
	}
}

func TestDelegateControllerStopDrainsSteeringAndModelClaims(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	steeringClaim, err := c.BeginSteerPersistence(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("BeginSteerPersistence: %v", err)
	}
	steeringEntry, err := steeringClaim.runtime.appendDelegateSteeringDurably("stop-racing steer", steeringClaim.entryID)
	if err != nil {
		t.Fatalf("append steering: %v", err)
	}
	modelClaim, err := c.BeginModelRequest(lease)
	if err != nil {
		t.Fatalf("BeginModelRequest: %v", err)
	}
	modelSnapshot := modelClaim.runtime.delegateModelHistorySnapshot()
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.BeginSteerPersistence(rootDelegateActor("root-session"), lease.delegateID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginSteerPersistence after stop error = %v, want target busy", err)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile with claims in flight: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before steering and model claims drained")
	default:
	}
	if _, err := c.CompleteSteerPersistence(steeringClaim, steeringEntry); err != nil {
		t.Fatalf("CompleteSteerPersistence for fsynced pre-stop steer: %v", err)
	}
	if _, err := c.CompleteModelRequest(modelClaim, modelSnapshot, replayScope{}); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("CompleteModelRequest after stop error = %v, want stale lease", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile after claim drain: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after steering and model claims drained")
	}

	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart successor: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart successor: %v", err)
	}
	if err := c.AttachRuntime(started.lease, steeringClaim.runtime); err != nil {
		t.Fatalf("AttachRuntime successor: %v", err)
	}
	inputClaim, err := c.BeginStartInput(started.lease)
	if err != nil {
		t.Fatalf("BeginStartInput successor: %v", err)
	}
	if _, err := c.CompleteStartInput(inputClaim, true, delegateFinish{}); err != nil {
		t.Fatalf("CompleteStartInput successor: %v", err)
	}
	history, err := completeDelegateModelRequest(c, started.lease)
	if err != nil {
		t.Fatalf("successor model request: %v", err)
	}
	if got := countMessageText(history, "stop-racing steer"); got != 1 {
		t.Fatalf("successor replay contains accepted pre-stop steer %d times, want once: %#v", got, history)
	}
}

func TestDelegateControllerStopCancellationPlanIsLeafFirst(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerRunning(t, c, "dlg_grandchild", "dlg_child")
	order := make([]string, 0, 3)
	for _, id := range []string{"dlg_parent", "dlg_child", "dlg_grandchild"} {
		delegateID := id
		c.live[id].binding.cancel = func() { order = append(order, delegateID) }
	}
	_, plan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(plan)
	want := []string{"dlg_grandchild", "dlg_child", "dlg_parent"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("cancellation order = %#v, want leaf-first %#v", order, want)
	}
}

func TestDelegateControllerSameTargetStopRetryJoins(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	first, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("first StopSubtree: %v", err)
	}
	second, plan, plans, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("second StopSubtree: %v", err)
	}
	if second.requestSeq != first.requestSeq || second.done != first.done || len(plan.cancel) != 1 || len(plans.updates) != 0 {
		t.Fatalf("joined stop = %#v plan=%#v plans=%#v, want same durable operation", second, plan, plans)
	}
}

func TestDelegateControllerDifferentTargetStopIsBusy(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_first", "")
	seedDelegateControllerIdle(t, c, "dlg_second", "")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_first"); err != nil {
		t.Fatalf("first StopSubtree: %v", err)
	}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_second"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("different stop error = %v, want busy", err)
	}
}

func TestDelegateControllerCoveringAndIntersectingStopAreBusy(t *testing.T) {
	for _, targetFirst := range []string{"dlg_parent", "dlg_child"} {
		t.Run(targetFirst, func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 2, 1)
			if targetFirst == "dlg_child" {
				seedDelegateControllerRunning(t, c, "dlg_parent", "")
			} else {
				seedDelegateControllerIdle(t, c, "dlg_parent", "")
			}
			seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
			actor := rootDelegateActor("root-session")
			if targetFirst == "dlg_child" {
				lease := delegateLease{delegateID: "dlg_parent", generation: 1}
				actor = delegateActor{lease: &lease}
			}
			if _, _, _, err := c.StopSubtree(actor, targetFirst); err != nil {
				t.Fatalf("first StopSubtree: %v", err)
			}
			other := "dlg_parent"
			if targetFirst == other {
				other = "dlg_child"
			}
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), other); !errors.Is(err, errDelegateTargetBusy) {
				t.Fatalf("overlapping stop error = %v, want busy", err)
			}
		})
	}
}

func TestDelegateControllerSuccessorWaitsForStopCompletion(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart before completion = %v, want busy", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("ReserveStart after completion: %v", err)
	}
}

func TestDelegateControllerRestartThenStopUsesNewRequestSequence(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	first, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("first StopSubtree: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	restarted := restartDelegateDeliveryController(t, c, path)
	second, _, _, err := restarted.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("second StopSubtree: %v", err)
	}
	if second.requestSeq <= first.requestSeq {
		t.Fatalf("restart request seq = %d, want > %d", second.requestSeq, first.requestSeq)
	}
}

func TestDelegateControllerStopRescansCancellationAttentionBeforeCompletion(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	evidence := emptyDelegateReconcileEvidence(c)
	evidence.attention["dlg_target"] = []string{"attention-1"}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile attention: %v", err)
	}
	if len(plans.attention) != 1 || plans.attention[0].attentionID != "attention-1" {
		t.Fatalf("attention plans = %#v", plans.attention)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before attention resolution")
	default:
	}
	if _, err := c.ReportAttentionResolved(result.requestSeq, evidence.evidenceVersion, "dlg_target", "attention-1", delegateAttentionDiscarded, nil); err != nil {
		t.Fatalf("ReportAttentionResolved: %v", err)
	}
	if _, err := c.Reconcile(evidence); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("stale Reconcile error = %v, want busy", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("fresh Reconcile: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after fresh empty attention fold")
	}
}

func TestDelegateControllerStopPreservesOwnerDeliveryOutsideSubtree(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	parentRuntime := &Session{}
	c.mu.Lock()
	c.live["dlg_parent"].runtime = parentRuntime
	c.live["dlg_parent"].binding.runtime = parentRuntime
	c.mu.Unlock()
	parentLease := delegateLease{delegateID: "dlg_parent", generation: 1}
	if _, _, _, err := c.StopSubtree(delegateActor{lease: &parentLease}, "dlg_child"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.durable["dlg_child"].PendingDeliveries) != 1 || len(plans.deliveries) != 1 || plans.deliveries[0].receiver != parentRuntime {
		t.Fatalf("outside-owner delivery = pending:%#v plans:%#v", c.durable["dlg_child"].PendingDeliveries, plans.deliveries)
	}
}

func TestDelegateControllerStopSuppressesCoveredOwnerDelivery(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.durable["dlg_child"].PendingDeliveries) != 0 || len(plans.deliveries) != 0 {
		t.Fatalf("covered-owner delivery survived = pending:%#v plans:%#v", c.durable["dlg_child"].PendingDeliveries, plans.deliveries)
	}
}

func TestDelegateControllerStopDefersExternalOwnerDeliveryUntilCompletion(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if plans := c.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("delivery released during stop: %#v", plans)
	}
	plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].deliveryID != "dlg_target/delivery/1" {
		t.Fatalf("post-completion plans = %#v", plans.deliveries)
	}
}

func TestDelegateControllerIdleStopWithPendingDeliveryPersistsAndSuppresses(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if result.requestSeq == 0 || c.durable["dlg_target"].PendingStopSeq != result.requestSeq || len(c.ReplayDeliveries()) != 0 {
		t.Fatalf("idle stop result=%#v aggregate=%#v", result, c.durable["dlg_target"])
	}
}

func TestDelegateControllerStopRequestAppendFailureDispatchesNothing(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	cancelled := false
	c.live["dlg_target"].binding.cancel = func() { cancelled = true }
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, plan, plans, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err == nil {
		t.Fatal("StopSubtree succeeded after store close")
	}
	if cancelled || len(plan.cancel) != 0 || len(plans.updates) != 0 || c.stop != nil {
		t.Fatalf("failed request dispatched state cancelled=%t plan=%#v plans=%#v stop=%#v", cancelled, plan, plans, c.stop)
	}
}

func TestDelegateControllerStopCompletionAppendFailureKeepsFence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err == nil {
		t.Fatal("Reconcile completion succeeded after store close")
	}
	select {
	case <-result.done:
		t.Fatal("failed completion closed stop")
	default:
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart after failed completion = %v, want busy", err)
	}
}

func TestDelegateControllerStopReconcilesRecoveryRequiredInputFailure(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store before input failure: %v", err)
	}
	if _, err := c.AdmitStartInput(started.lease, func() error { return errors.New("input persistence failed") }); err == nil {
		t.Fatal("AdmitStartInput double failure returned nil")
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()

	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile recovery-required input: %v", err)
	}
	if aggregate := c.durable["dlg_target"]; aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("aggregate after recovery reconciliation = %#v, want stopped closed run", aggregate)
	}
	if live := c.live["dlg_target"]; live == nil || live.binding != nil || live.recoveryRequired {
		t.Fatalf("live state after recovery reconciliation = %#v, want released binding and latch", live)
	}
	if turns, _ := c.capacityInUse(); turns != 0 {
		t.Fatalf("turn capacity after recovery reconciliation = %d, want zero", turns)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile stop completion: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop did not complete after fresh reconciliation evidence")
	}
}

func TestDelegateControllerStopReconcilesRecoveryRequiredSettlementFailure(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	claim, continueRun, err := c.BeginSettlement(lease)
	if err != nil || continueRun || claim == nil {
		t.Fatalf("BeginSettlement = claim:%#v continue:%t err:%v", claim, continueRun, err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store before settlement failure: %v", err)
	}
	if _, err := c.CompleteSettlement(claim, nil); err == nil {
		t.Fatal("CompleteSettlement succeeded after store close")
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "settlement persistence failed"}); err == nil {
		t.Fatal("fallback FinishGeneration succeeded after store close")
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()

	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	c.mu.Lock()
	runtime := c.live[lease.delegateID].binding.runtime
	c.mu.Unlock()
	if err := c.ReportFinalizationQuiesced(lease, runtime); err != nil {
		t.Fatalf("ReportFinalizationQuiesced: %v", err)
	}
	c.mu.Lock()
	claimRetained := c.hasSettlementClaimLocked(lease)
	c.mu.Unlock()
	if !claimRetained {
		t.Fatal("durable stop released the failed settlement claim before recovery fsync")
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile recovery-required settlement: %v", err)
	}
	if aggregate := c.durable[lease.delegateID]; aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("aggregate after recovery reconciliation = %#v, want stopped closed run", aggregate)
	}
	c.mu.Lock()
	claimRetained = c.hasSettlementClaimLocked(lease)
	c.mu.Unlock()
	if claimRetained {
		t.Fatal("successful recovery retained the settlement claim")
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile stop completion: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop did not complete after settlement recovery")
	}
}

func TestDelegateControllerRecoveryStabilizationWaitsForAdmittedSteer(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	runtime := attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}

	steeringClaim, err := c.BeginSteerPersistence(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("BeginSteerPersistence: %v", err)
	}
	finalizationClaim, continueRun, err := c.BeginFinalization(lease, delegateSettlementTerminal)
	if err != nil || continueRun || finalizationClaim == nil {
		t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalizationClaim, continueRun, err)
	}
	c.mu.Lock()
	c.live[lease.delegateID].attentionIDs = []string{"attention-recovery"}
	c.mu.Unlock()
	if err := c.RequireFinalizationRecovery(finalizationClaim); err != nil {
		t.Fatalf("RequireFinalizationRecovery: %v", err)
	}
	if err := c.ReportFinalizationQuiesced(lease, runtime); err != nil {
		t.Fatalf("ReportFinalizationQuiesced: %v", err)
	}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}

	plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile with admitted steer: %v", err)
	}
	if len(plans.attention) != 0 {
		t.Fatalf("recovery produced stabilization before admitted steer drained: %#v", plans.attention)
	}
	if err := c.AbortSteerPersistence(steeringClaim); err != nil {
		t.Fatalf("AbortSteerPersistence: %v", err)
	}
	plans, err = c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile after steer drain: %v", err)
	}
	if len(plans.attention) != 1 || !plans.attention[0].stabilize || plans.attention[0].attentionID != "attention-recovery" {
		t.Fatalf("recovery stabilization plans = %#v, want exact attention after steer drain", plans.attention)
	}
}

func TestDelegateControllerRecoveryStopAppendFailureReturnsAndRetries(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store before input failure: %v", err)
	}
	if _, err := c.AdmitStartInput(started.lease, func() error { return errors.New("input persistence failed") }); err == nil {
		t.Fatal("AdmitStartInput double failure returned nil")
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store for stop request: %v", err)
	}
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), started.lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close store before recovery append: %v", err)
	}

	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err == nil {
		t.Fatal("Reconcile recovery append succeeded after store close")
	}
	select {
	case <-result.done:
		t.Fatal("failed recovery append completed stop")
	default:
	}
	if live := c.live[started.lease.delegateID]; live == nil || live.binding == nil || !live.recoveryRequired {
		t.Fatalf("failed recovery append released exact state: %#v", live)
	}
	if turns, _ := c.capacityInUse(); turns != 1 {
		t.Fatalf("turn capacity after failed recovery append = %d, want one", turns)
	}

	retryStore, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store for retry: %v", err)
	}
	t.Cleanup(func() { _ = retryStore.Close() })
	c.mu.Lock()
	c.store = retryStore
	c.mu.Unlock()
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("retry recovery append: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("retry stop completion: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("same stop did not complete after recovery append retry")
	}
}

func TestDelegateControllerCloseRetriesFailedStopDriver(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store before input failure: %v", err)
	}
	if _, err := c.AdmitStartInput(started.lease, func() error { return errors.New("input persistence failed") }); err == nil {
		t.Fatal("AdmitStartInput double failure returned nil")
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store for stop request: %v", err)
	}
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), started.lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	stop := c.stopForResult(result)
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close store before driver recovery append: %v", err)
	}
	driver := &delegateStopDriver{done: make(chan struct{}), err: errors.New("recovery append failed")}
	close(driver.done)
	c.mu.Lock()
	stop.driver = driver
	c.stopDriver = driver
	c.mu.Unlock()
	retryStore, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store for close retry: %v", err)
	}
	t.Cleanup(func() { _ = retryStore.Close() })
	c.mu.Lock()
	c.store = retryStore
	c.mu.Unlock()

	if err := c.joinOrDrainStopForClose(context.Background(), stop); err != nil {
		t.Fatalf("joinOrDrainStopForClose did not retry failed driver: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("close retry did not complete the same pending stop")
	}
	if joined, err := c.joinStopReconcileDriver(context.Background()); !joined || err != nil {
		t.Fatalf("recovered driver remained a failed close result: joined=%t err=%v", joined, err)
	}
}

func TestDelegateControllerResumabilityAppendFailureMutatesNothing(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	before := cloneDelegateControllerState(t, c.durable)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	plans, err := c.CloseResumability(rootDelegateActor("root-session"), "dlg_target", "retired")
	if err == nil {
		t.Fatal("CloseResumability succeeded after store close")
	}
	if len(plans.updates) != 0 || !reflect.DeepEqual(c.durable, before) {
		t.Fatalf("failed resumability closure mutated state plans=%#v durable=%#v", plans, c.durable)
	}
}

func TestDelegateControllerRootCloseJoinsPendingStopWithoutSecondIdentity(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("Close did not join pending stop")
	}
	events, err := c.store.Load()
	if err == nil {
		t.Fatal("Load after Close succeeded, want closed store")
	}
	_ = events
	raw := readDelegateControllerFile(t, filepath.Join(c.stateDir, "delegate-events.jsonl"))
	if got := bytes.Count(raw, []byte(`"delegate_subtree_stop_requested"`)); got != 1 {
		t.Fatalf("stop request count = %d, want 1\n%s", got, raw)
	}
}

func TestDelegateControllerRootCloseFencesAdmissionWhileReceiptDrains(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plan := c.ReplayDeliveries()[0]
	token, admitted, err := c.BeginDelivery(plan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with outstanding receipt = %v, want canceled", err)
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart after close fence = %v, want busy", err)
	}
	if _, err := c.CompleteDelivery(token, false); err != nil {
		t.Fatalf("CompleteDelivery cleanup: %v", err)
	}
}

func TestDelegateControllerRootCloseDoesNotClaimRetainedDelivery(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(c.deliveryClaims) != 0 || len(c.deliveries) != 0 {
		t.Fatalf("close manufactured delivery work: claims=%#v receipts=%#v", c.deliveryClaims, c.deliveries)
	}
}

func TestDelegateControllerRootCloseInvalidatesCollectedEvidence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	evidence := emptyDelegateReconcileEvidence(c)
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Reconcile(evidence); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("pre-close evidence after close = %v, want busy", err)
	}
}

func TestDelegateControllerRootCloseReconcilesClosedRootDescendantWork(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_root", "")
	seedDelegateControllerRunning(t, c, "dlg_running", "dlg_root")
	seedDelegateControllerIdle(t, c, "dlg_delivery", "dlg_root")
	seedDelegateControllerIdle(t, c, "dlg_attention", "dlg_root")

	lease := delegateLease{delegateID: "dlg_running", generation: 1}
	workToken, err := c.BeginShellWork(lease)
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	if cancelNow, err := c.CommitShellWork(workToken, "job-running", func() {}); err != nil || cancelNow {
		t.Fatalf("CommitShellWork = cancel:%t err:%v", cancelNow, err)
	}
	seedDelegateControllerDelivery(t, c, "dlg_delivery")
	deliveryPlans := c.ReplayDeliveries()
	if len(deliveryPlans) != 1 {
		t.Fatalf("ReplayDeliveries count = %d, want 1", len(deliveryPlans))
	}
	deliveryToken, admitted, err := c.BeginDelivery(deliveryPlans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	attentionPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_attention.transcript.jsonl")
	writeDelegateAttentionTranscript(t, attentionPath, "child-dlg_attention", "attention-close-closed-root")
	if _, err := c.CloseResumability(rootDelegateActor("root-session"), "dlg_root", "root retired"); err != nil {
		t.Fatalf("CloseResumability: %v", err)
	}
	if phase := c.durable["dlg_root"].Phase; phase != delegatestore.PhaseClosed {
		t.Fatalf("root phase before Close = %s, want closed", phase)
	}

	var cancellationErr error
	cancellationRan := false
	c.live["dlg_running"].binding.cancel = func() {
		if cancellationRan {
			return
		}
		cancellationRan = true
		_, finishErr := c.FinishGeneration(lease, delegateFinish{
			outcome:     delegatestore.OutcomeCompleted,
			disposition: delegatestore.DispositionCompletedNoAction,
		})
		_, shellErr := c.ReportShellFinished(workToken, "job-running")
		_, deliveryErr := c.CompleteDelivery(deliveryToken, false)
		cancellationErr = errors.Join(finishErr, shellErr, deliveryErr)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if cancellationErr != nil {
		t.Fatalf("descendant cancellation cleanup: %v", cancellationErr)
	}
	if aggregate := c.durable["dlg_running"]; aggregate.CurrentRunOpen || aggregate.PendingStopSeq != 0 {
		t.Fatalf("running descendant after Close = %#v", aggregate)
	}
	if len(c.work) != 0 || len(c.deliveries) != 0 {
		t.Fatalf("process receipts after Close: work=%#v deliveries=%#v", c.work, c.deliveries)
	}
	pending, err := readPendingDelegateAttention(attentionPath, "child-dlg_attention")
	if err != nil {
		t.Fatalf("read attention after Close: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending attention after Close = %#v", pending)
	}
	raw := readDelegateControllerFile(t, filepath.Join(c.stateDir, "delegate-events.jsonl"))
	if got := bytes.Count(raw, []byte(`"delegate_subtree_stop_requested"`)); got != 1 {
		t.Fatalf("root stop request count = %d, want 1\n%s", got, raw)
	}
}

func seedDelegateControllerDelivery(t *testing.T, c *delegateTreeController, delegateID string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle {
		t.Fatalf("delegate %s is not idle", delegateID)
	}
	lease := delegateLease{delegateID: delegateID, generation: aggregate.Generation + 1}
	packet := delegateControllerReportedPacket("seed")
	_, err := c.appendLocked(
		delegateControllerRunStartedEvent(delegateID, lease.generation, delegatestore.TriggerOwnerInput, c.now()),
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateTerminalPrepared,
			DelegateID: delegateID,
			TerminalPrepared: &delegatestore.TerminalPrepared{
				Generation: lease.generation,
				Packet:     packet,
			},
		},
		delegateRunFinishedEvent(lease, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", c.now(), delegateDeliveryID(delegateID, lease.generation), nil),
	)
	if err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
}

// TestDelegateControllerStopFencedSteerKeepsItsCausalProvenance pins the half of
// the fsynced-pre-stop acceptance that 554221673 did not carry over.
//
// A caller-originated steer (SteerCaller) carries causal watch provenance. On the
// ordinary path CompleteSteerPersistence records it as a pendingSteers admission,
// BeginModelRequest lifts it into the model claim, and CompleteModelRequest unions
// it into the successor runtime -- so every event that turn emits still names the
// watch that drove it.
//
// The stop-fenced path returns before the admission is recorded. The steer's TEXT
// survives (the transcript is the replay authority) but its causal origin does
// not, and the loss is silent: the steer looks delivered.
func TestDelegateControllerStopFencedSteerKeepsItsCausalProvenance(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}

	origin := &provenance.Causal{
		WatchKeys: []provenance.WatchKey{{WatchID: "wch_origin", WatchGeneration: "1"}},
		Chain:     []provenance.Entry{{Kind: "watch_delivery", WatchID: "wch_origin", DeliveryID: "dlv_origin"}},
	}
	c.mu.Lock()
	steeringClaim, err := c.beginSteerPersistenceLocked(lease.delegateID, origin)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("beginSteerPersistence with provenance: %v", err)
	}
	steeringEntry, err := steeringClaim.runtime.appendDelegateSteeringDurably("provenance-carrying steer", steeringClaim.entryID)
	if err != nil {
		t.Fatalf("append steering: %v", err)
	}

	// The covering stop releases the binding while the steer's transcript fsync
	// has already landed, which is the accepted-under-a-stop case.
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if _, err := c.CompleteSteerPersistence(steeringClaim, steeringEntry); err != nil {
		t.Fatalf("CompleteSteerPersistence for fsynced pre-stop steer: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	<-result.done

	successor := startDelegateSuccessorGeneration(t, c, lease.delegateID, steeringClaim.runtime)
	if _, err := completeDelegateModelRequest(c, successor); err != nil {
		t.Fatalf("successor model request: %v", err)
	}

	got := steeringClaim.runtime.activeCausalProvenance()
	if got == nil || len(got.WatchKeys) == 0 {
		t.Fatalf("successor active provenance = %#v, want the steer's originating watch key", got)
	}
	if got.WatchKeys[0].WatchID != "wch_origin" {
		t.Fatalf("successor active provenance watch = %q, want wch_origin", got.WatchKeys[0].WatchID)
	}
}

// startDelegateSuccessorGeneration drives a fresh generation for delegateID onto
// runtime and returns its lease.
func startDelegateSuccessorGeneration(t *testing.T, c *delegateTreeController, delegateID string, runtime *Session) delegateLease {
	t.Helper()
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), delegateID)
	if err != nil {
		t.Fatalf("ReserveStart successor: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart successor: %v", err)
	}
	if err := c.AttachRuntime(started.lease, runtime); err != nil {
		t.Fatalf("AttachRuntime successor: %v", err)
	}
	inputClaim, err := c.BeginStartInput(started.lease)
	if err != nil {
		t.Fatalf("BeginStartInput successor: %v", err)
	}
	if _, err := c.CompleteStartInput(inputClaim, true, delegateFinish{}); err != nil {
		t.Fatalf("CompleteStartInput successor: %v", err)
	}
	return started.lease
}
