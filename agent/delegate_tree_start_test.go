package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
)

// AdmitStartInput is the test-only callback adapter used by controller fuzz
// programs that exercise transcript success and failure as one operation. It
// runs the callback only after BeginStartInput releases the controller mutex.
func (c *delegateTreeController) AdmitStartInput(lease delegateLease, admitInput func() error) (delegateMutationPlans, error) {
	claim, err := c.BeginStartInput(lease)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	inputErr := admitInput()
	if inputErr == nil {
		return c.CompleteStartInput(claim, true, delegateFinish{})
	}
	terminal, finish := c.inputPersistFailureBatch(lease, inputErr)
	plans, finishErr := c.CompleteStartInput(claim, false, delegateFinish{
		outcome:     finish.RunFinished.Outcome.Status,
		disposition: finish.RunFinished.Disposition,
		reason:      finish.RunFinished.Outcome.Reason,
		packet:      &terminal.TerminalPrepared.Packet,
		endedAt:     finish.RunFinished.Outcome.EndedAt,
	})
	return plans, errors.Join(inputErr, finishErr)
}

func TestDelegateControllerCreatedAppendFailurePublishesNothing(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	reservation, err := c.ReserveCreate(rootDelegateActor("root-session"), delegateControllerCreateDescriptor())
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	beforeBytes := readDelegateControllerFile(t, path)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	commit, err := c.CommitStart(reservation)
	if err == nil {
		t.Fatal("CommitStart succeeded after store close")
	}
	if len(commit.plan.rows) != 0 {
		t.Fatalf("failed commit published plan %#v", commit.plan)
	}
	if len(c.durable) != 0 || len(c.live) != 0 {
		t.Fatalf("failed create published durable=%#v live=%#v", c.durable, c.live)
	}
	if turns, drives := c.capacityInUse(); turns != 0 || drives != 0 {
		t.Fatalf("capacity after failed create = (%d, %d), want zero", turns, drives)
	}
	if got := readDelegateControllerFile(t, path); !bytes.Equal(got, beforeBytes) {
		t.Fatalf("bytes changed after failed create:\n got %q\nwant %q", got, beforeBytes)
	}
}

func TestDelegateControllerRunStartedAppendFailureDoesNotInstallBinding(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	beforeBytes := readDelegateControllerFile(t, path)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	commit, err := c.CommitStart(reservation)
	if err == nil {
		t.Fatal("CommitStart succeeded after store close")
	}
	if len(commit.plan.rows) != 0 {
		t.Fatalf("failed run start published plan %#v", commit.plan)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Generation != 0 || aggregate.Phase != delegatestore.PhaseIdle {
		t.Fatalf("aggregate after failed run start = %#v", aggregate)
	}
	if live := c.live["dlg_target"]; live != nil && live.binding != nil {
		t.Fatalf("binding installed after failed run start: %#v", live.binding)
	}
	if turns, _ := c.capacityInUse(); turns != 0 {
		t.Fatalf("capacity after failed run start = %d, want zero", turns)
	}
	if got := readDelegateControllerFile(t, path); !bytes.Equal(got, beforeBytes) {
		t.Fatalf("bytes changed after failed run start:\n got %q\nwant %q", got, beforeBytes)
	}
}

func TestDelegateControllerCreateCommitIsOneAtomicBatch(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	reservation, err := c.ReserveCreate(rootDelegateActor("root-session"), delegateControllerCreateDescriptor())
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	raw := readDelegateControllerFile(t, path)
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 2 || !bytes.Contains(lines[1], []byte(`"delegate_created"`)) || !bytes.Contains(lines[1], []byte(`"delegate_run_started"`)) {
		t.Fatalf("create commit is not one created+run-started batch:\n%s", raw)
	}
	if row := started.plan.rows[0]; row.id != reservation.delegateID || row.lifecycle != delegateLifecycleRunning || row.revision != 2 || row.transcriptRef != reservation.descriptor.TranscriptRef {
		t.Fatalf("create commit plan = %#v", row)
	}
	live := c.live[reservation.delegateID]
	if live == nil || live.binding == nil || live.binding.lease != started.lease || live.binding.runtime != nil || live.binding.ready {
		t.Fatalf("create commit live state = %#v, want exact non-launched binding", live)
	}
	if turns, drives := c.capacityInUse(); turns != 1 || drives != 0 {
		t.Fatalf("create commit capacity = (%d,%d), want (1,0)", turns, drives)
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	folded, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(folded, c.durable) {
		t.Fatalf("create commit fold differs from c.durable:\n got %#v\nwant %#v", folded, c.durable)
	}
}

func TestDelegateControllerCrashBeforeCreateCommitLeavesNoChildArtifacts(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	reservation, err := c.ReserveCreate(rootDelegateActor("root-session"), delegateControllerCreateDescriptor())
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	assertDelegateControllerPathAbsent(t, reservation.transcriptPath)
	assertDelegateControllerPathAbsent(t, reservation.worktreePath)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}

	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         reopened,
		rootSessionID: "root-session",
		stateDir:      c.stateDir,
		worktreeRoot:  c.worktreeRoot,
		turnLimit:     1,
		driveLimit:    1,
		now:           c.now,
	})
	if err != nil {
		t.Fatalf("restart controller: %v", err)
	}
	if len(restarted.durable) != 0 || len(restarted.live) != 0 {
		t.Fatalf("pre-commit reservation survived restart: durable=%#v live=%#v", restarted.durable, restarted.live)
	}
	assertDelegateControllerPathAbsent(t, reservation.transcriptPath)
	assertDelegateControllerPathAbsent(t, reservation.worktreePath)
}

func TestDelegateControllerCrashAfterCreateCommitReconcilesOwnedPartialArtifacts(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	reservation, err := c.ReserveCreate(rootDelegateActor("root-session"), delegateControllerCreateDescriptor())
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if got := c.durable[started.lease.delegateID].Descriptor; got.TranscriptRef != reservation.descriptor.TranscriptRef || got.WorkingDir != reservation.worktreePath {
		t.Fatalf("committed descriptor = %#v, want reserved deterministic paths", got)
	}
	assertDelegateControllerPathAbsent(t, reservation.transcriptPath)
	assertDelegateControllerPathAbsent(t, reservation.worktreePath)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}

	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         reopened,
		rootSessionID: "root-session",
		stateDir:      c.stateDir,
		worktreeRoot:  c.worktreeRoot,
		turnLimit:     1,
		driveLimit:    1,
		now:           c.now,
	})
	if err != nil {
		t.Fatalf("restart controller: %v", err)
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	aggregate := restarted.durable[started.lease.delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.CurrentRunOpen {
		t.Fatalf("reconciled aggregate = %#v, want idle closed run", aggregate)
	}
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "runtime_lost" {
		t.Fatalf("reconciled outcome = %#v, want failed/runtime_lost", aggregate.LatestOutcome)
	}
	if aggregate.Descriptor.TranscriptRef != reservation.descriptor.TranscriptRef || aggregate.Descriptor.WorkingDir != reservation.worktreePath {
		t.Fatalf("reconciled descriptor lost path ownership: %#v", aggregate.Descriptor)
	}
	events, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load restarted store: %v", err)
	}
	folded, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatalf("Fold restarted store: %v", err)
	}
	if !reflect.DeepEqual(folded, restarted.durable) {
		t.Fatalf("persisted fold differs from controller durable:\n got %#v\nwant %#v", folded, restarted.durable)
	}
}

func TestDelegateControllerCommittedUnreadyStartAdmitsOnlyStopOrFinish(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if _, err := c.BeginModelRequest(started.lease); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginModelRequest unready error = %v, want busy", err)
	}
	if err := c.BeginTool(started.lease); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginTool unready error = %v, want busy", err)
	}
	runtime := &Session{}
	if err := c.AttachRuntime(started.lease, runtime); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	if _, err := c.BeginModelRequest(started.lease); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginModelRequest before input error = %v, want busy", err)
	}
	plan, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeStopped, reason: "stopped_before_launch"})
	if err != nil {
		t.Fatalf("FinishGeneration exact unready lease: %v", err)
	}
	if row := plan.updates[0].rows[0]; row.lifecycle != delegateLifecycleIdle || row.lastOutcome == nil || row.lastOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("finish plan = %#v, want idle stopped", row)
	}
}

func TestDelegateControllerExactFinishSettlesStoppingGeneration(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	c.mu.Lock()
	_, err = c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateSubtreeStopRequested,
		DelegateID: "dlg_target",
		SubtreeStopRequested: &delegatestore.SubtreeStopRequested{
			TargetDelegateID: "dlg_target",
		},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("append stop request: %v", err)
	}
	if _, err := c.BeginModelRequest(started.lease); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginModelRequest while stopping error = %v, want busy", err)
	}
	plan, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "cancelled"})
	if err != nil {
		t.Fatalf("FinishGeneration stopping exact lease: %v", err)
	}
	row := plan.updates[0].rows[0]
	if row.phase != delegatestore.PhaseStopping || row.lastOutcome == nil || row.lastOutcome.Status != delegatestore.OutcomeStopped || row.lastOutcome.Reason != "stopped_by_parent" {
		t.Fatalf("stopping finish plan = %#v", row)
	}
	if turns, _ := c.capacityInUse(); turns != 0 {
		t.Fatalf("capacity after stopping finish = %d, want zero", turns)
	}
}

func TestDelegateControllerInputAndCompensatingFinishFailureKeepsExactBinding(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, runtime := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	beforeBytes := readDelegateControllerFile(t, path)
	inputErr := errors.New("injected input persistence failure")
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	plan, err := c.AdmitStartInput(started.lease, func() error { return inputErr })
	if !errors.Is(err, inputErr) || !strings.Contains(err.Error(), "store is closed") {
		t.Fatalf("AdmitStartInput error = %v, want input and compensating append failures", err)
	}
	if row := plan.updates[0].rows[0]; row.lifecycle != delegateLifecycleRunning || row.lastOutcome != nil {
		t.Fatalf("double-failure plan = %#v, want captured running state", row)
	}
	live := c.live["dlg_target"]
	if live == nil || live.binding == nil || live.binding.lease != started.lease || live.binding.runtime != runtime || !live.recoveryRequired {
		t.Fatalf("double-failure live state = %#v, want exact latched binding", live)
	}
	if turns, _ := c.capacityInUse(); turns != 1 {
		t.Fatalf("double-failure capacity = %d, want 1", turns)
	}
	if aggregate := c.durable["dlg_target"]; aggregate.Phase != delegatestore.PhaseRunning || !aggregate.CurrentRunOpen {
		t.Fatalf("double-failure aggregate = %#v, want durable running", aggregate)
	}
	if got := readDelegateControllerFile(t, path); !bytes.Equal(got, beforeBytes) {
		t.Fatalf("double-failure bytes changed:\n got %q\nwant %q", got, beforeBytes)
	}
	if _, err := c.BeginModelRequest(started.lease); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("recovery-required model admission error = %v, want busy", err)
	}
}

func TestDelegateControllerStopCanSettleRecoveryRequiredStart(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	inputErr := errors.New("injected input persistence failure")
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	if _, err := c.AdmitStartInput(started.lease, func() error { return inputErr }); err == nil {
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

	plan, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeStopped, reason: "stopped_for_recovery"})
	if err != nil {
		t.Fatalf("FinishGeneration recovery-required lease: %v", err)
	}
	if row := plan.updates[0].rows[0]; row.lifecycle != delegateLifecycleIdle || row.lastOutcome == nil || row.lastOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("recovery stop plan = %#v, want idle stopped", row)
	}
	if live := c.live["dlg_target"]; live == nil || live.binding != nil || live.recoveryRequired {
		t.Fatalf("live state after recovery stop = %#v, want resident without binding or latch", live)
	}
	if turns, _ := c.capacityInUse(); turns != 0 {
		t.Fatalf("capacity after recovery stop = %d, want zero", turns)
	}
}

func TestDelegateControllerInputPersistFailureUsesCanonicalAtomicFinish(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	inputErr := errors.New("injected input persistence failure")
	plan, err := c.AdmitStartInput(started.lease, func() error { return inputErr })
	if !errors.Is(err, inputErr) {
		t.Fatalf("AdmitStartInput error = %v, want input failure", err)
	}
	row := plan.updates[0].rows[0]
	if row.lifecycle != delegateLifecycleIdle || row.lastOutcome == nil || row.lastOutcome.Status != delegatestore.OutcomeFailed || row.lastOutcome.Reason != "input_persist_failed" {
		t.Fatalf("input-failure plan = %#v", row)
	}
	if turns, _ := c.capacityInUse(); turns != 0 {
		t.Fatalf("capacity after input failure = %d, want zero", turns)
	}
	if live := c.live["dlg_target"]; live == nil || live.binding != nil {
		t.Fatalf("binding after settled input failure = %#v, want released resident", live)
	}
	raw := readDelegateControllerFile(t, path)
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 4 {
		t.Fatalf("store lines = %d, want version/create/start/input-failure batches\n%s", len(lines), raw)
	}
	if !bytes.Contains(lines[3], []byte(`"delegate_terminal_prepared"`)) || !bytes.Contains(lines[3], []byte(`"delegate_run_finished"`)) {
		t.Fatalf("input-failure batch is not canonical terminal+finish: %s", lines[3])
	}
}

func TestDelegateControllerInputPersistFailureClaimsWaiterAndReturnsDelivery(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	waiter, err := c.RegisterInlineWaiter(reservation)
	if err != nil {
		t.Fatalf("RegisterInlineWaiter: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if err := c.AttachRuntime(started.lease, &Session{}); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	inputErr := errors.New("injected input persistence failure")
	plans, err := c.AdmitStartInput(started.lease, func() error { return inputErr })
	if !errors.Is(err, inputErr) {
		t.Fatalf("AdmitStartInput error = %v, want input failure", err)
	}
	if len(plans.updates) != 1 || len(plans.deliveries) != 1 || plans.deliveries[0].waiter != waiter {
		t.Fatalf("input-failure plans = %#v, want exact claimed waiter", plans)
	}
	c.mu.Lock()
	remaining := c.live["dlg_target"].waiters[started.lease.generation]
	c.mu.Unlock()
	if remaining != nil {
		t.Fatalf("claimed waiter remains registered: %#v", remaining)
	}
	if _, err := deliverDelegatePacket(plans.deliveries[0], nil); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	resolution := <-waiter.resolution
	if resolution.fallback || resolution.packet == nil || resolution.packet.Kind != delegatestore.PacketTerminalError || !strings.Contains(string(resolution.packet.Message), "input persistence failed") || resolution.commit == nil {
		t.Fatalf("input-failure waiter resolution = %#v", resolution)
	}
	if _, err := resolution.commit.Complete(false); err != nil {
		t.Fatalf("Complete(false): %v", err)
	}
}

func TestDelegateControllerInputPersistFinishAppendFailureDoesNotClaimWaiter(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	waiter, err := c.RegisterInlineWaiter(reservation)
	if err != nil {
		t.Fatalf("RegisterInlineWaiter: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if err := c.AttachRuntime(started.lease, &Session{}); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	before := readDelegateControllerFile(t, path)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	inputErr := errors.New("injected input persistence failure")
	plans, err := c.AdmitStartInput(started.lease, func() error { return inputErr })
	if !errors.Is(err, inputErr) || !strings.Contains(err.Error(), "store is closed") {
		t.Fatalf("AdmitStartInput error = %v, want input and append failures", err)
	}
	if len(plans.deliveries) != 0 {
		t.Fatalf("failed append published delivery plans: %#v", plans.deliveries)
	}
	c.mu.Lock()
	live := c.live["dlg_target"]
	remaining := live.waiters[started.lease.generation]
	recoveryRequired := live.recoveryRequired
	c.mu.Unlock()
	if remaining != waiter || !recoveryRequired {
		t.Fatalf("failed append waiter=%#v recovery=%t, want original waiter and recovery latch", remaining, recoveryRequired)
	}
	select {
	case resolution := <-waiter.resolution:
		t.Fatalf("failed append resolved waiter: %#v", resolution)
	default:
	}
	if got := readDelegateControllerFile(t, path); !bytes.Equal(got, before) {
		t.Fatalf("failed append changed bytes:\n got %q\nwant %q", got, before)
	}
}

func TestDelegateControllerFinishGenerationUsesCanonicalAtomicTerminalBatch(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if _, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "construction_failed"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	raw := readDelegateControllerFile(t, path)
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 4 || !bytes.Contains(lines[3], []byte(`"delegate_terminal_prepared"`)) || !bytes.Contains(lines[3], []byte(`"delegate_run_finished"`)) {
		t.Fatalf("finish batch is not canonical terminal+finish:\n%s", raw)
	}
}

func TestDelegateControllerFinishGenerationEscapesArbitraryReasonAsJSON(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	reason := "construction failed: \x01"
	plan, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: reason})
	if err != nil {
		t.Fatalf("FinishGeneration arbitrary reason: %v", err)
	}
	if got := plan.updates[0].rows[0].lastOutcome; got == nil || got.Reason != reason {
		t.Fatalf("last outcome = %#v, want exact reason %q", got, reason)
	}
}

func TestDelegateControllerAdmitStartInputSuccessMarksReady(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	calls := 0
	plan, err := c.AdmitStartInput(started.lease, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	if calls != 1 {
		t.Fatalf("input admission calls = %d, want 1", calls)
	}
	if row := plan.updates[0].rows[0]; row.lifecycle != delegateLifecycleRunning {
		t.Fatalf("successful input plan = %#v, want running", row)
	}
	if _, err := completeDelegateModelRequest(c, started.lease); err != nil {
		t.Fatalf("BeginModelRequest ready start: %v", err)
	}
	if err := c.BeginTool(started.lease); err != nil {
		t.Fatalf("BeginTool ready start: %v", err)
	}
}

func TestDelegateControllerStartInputClaimStopWinsBeforeCompletion(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	claim, err := c.BeginStartInput(started.lease)
	if err != nil {
		t.Fatalf("BeginStartInput: %v", err)
	}
	_, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	plans, err := c.CompleteStartInput(claim, true, delegateFinish{})
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("CompleteStartInput after stop error = %v, want target busy", err)
	}
	if len(plans.updates) != 1 {
		t.Fatalf("CompleteStartInput after stop plans = %#v, want stopped update", plans)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate == nil || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("input-claim stop aggregate = %#v, want durably stopped generation", aggregate)
	}
	if live := c.live["dlg_target"]; live == nil || live.binding != nil {
		t.Fatalf("input-claim stop live state = %#v, want released binding", live)
	}
	if turns, _ := c.capacityInUse(); turns != 0 {
		t.Fatalf("input-claim stop retained capacity = %d, want zero", turns)
	}
}

func TestDelegateControllerCommittedStartFailureStopWins(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	_, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	plans, claimedForClose, err := c.FailCommittedStart(started.lease, delegatePermanentStartFailure(errors.New("construction failed"), "construction_failed"), "construction_failed", nil)
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("FailCommittedStart after stop error = %v, want target busy", err)
	}
	if claimedForClose {
		t.Fatal("FailCommittedStart claimed a runtime without an exact close candidate")
	}
	if len(plans.updates) != 1 {
		t.Fatalf("FailCommittedStart after stop plans = %#v, want stopped update", plans)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate == nil || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped || !aggregate.Resumable {
		t.Fatalf("committed-failure stop aggregate = %#v, want stopped and resumable", aggregate)
	}
	if live := c.live["dlg_target"]; live == nil || live.binding != nil {
		t.Fatalf("committed-failure stop live state = %#v, want released binding", live)
	}
	if turns, _ := c.capacityInUse(); turns != 0 {
		t.Fatalf("committed-failure stop retained capacity = %d, want zero", turns)
	}
}

func TestDelegateControllerRuntimeAttachmentIsOneToOne(t *testing.T) {
	t.Run("retained runtime is not replaced", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerIdle(t, c, "dlg_target", "")
		first, retained := commitAttachedDelegateControllerStart(t, c, "dlg_target")
		if _, err := c.AdmitStartInput(first.lease, func() error { return nil }); err != nil {
			t.Fatalf("AdmitStartInput: %v", err)
		}
		if _, err := c.FinishGeneration(first.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "completed"}); err != nil {
			t.Fatalf("FinishGeneration: %v", err)
		}

		reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target")
		if err != nil {
			t.Fatalf("ReserveStart: %v", err)
		}
		second, err := c.CommitStart(reservation)
		if err != nil {
			t.Fatalf("CommitStart: %v", err)
		}
		if err := c.AttachRuntime(second.lease, &Session{}); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("AttachRuntime replacement error = %v, want busy", err)
		}
		live := c.live["dlg_target"]
		if live.runtime != retained || live.binding == nil || live.binding.runtime != nil {
			t.Fatalf("live state after rejected replacement = %#v, want retained runtime and unattached binding", live)
		}
		if err := c.AttachRuntime(second.lease, retained); err != nil {
			t.Fatalf("AttachRuntime exact retained runtime: %v", err)
		}
	})

	t.Run("one runtime cannot attach to two delegates", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 2, 1)
		seedDelegateControllerIdle(t, c, "dlg_first", "")
		seedDelegateControllerIdle(t, c, "dlg_second", "")
		firstReservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_first")
		if err != nil {
			t.Fatalf("ReserveStart first: %v", err)
		}
		first, err := c.CommitStart(firstReservation)
		if err != nil {
			t.Fatalf("CommitStart first: %v", err)
		}
		secondReservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_second")
		if err != nil {
			t.Fatalf("ReserveStart second: %v", err)
		}
		second, err := c.CommitStart(secondReservation)
		if err != nil {
			t.Fatalf("CommitStart second: %v", err)
		}

		runtime := &Session{}
		if err := c.AttachRuntime(first.lease, runtime); err != nil {
			t.Fatalf("AttachRuntime first: %v", err)
		}
		if err := c.AttachRuntime(second.lease, runtime); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("AttachRuntime second owner error = %v, want busy", err)
		}
		if live := c.live["dlg_second"]; live.runtime != nil || live.binding == nil || live.binding.runtime != nil {
			t.Fatalf("second live state after rejected attachment = %#v", live)
		}
	})

	t.Run("ambiguous resident ownership is rejected", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 2, 1)
		seedDelegateControllerIdle(t, c, "dlg_first", "")
		seedDelegateControllerIdle(t, c, "dlg_second", "")
		runtime := &Session{}
		c.mu.Lock()
		c.live["dlg_first"] = &delegateLiveState{runtime: runtime}
		c.live["dlg_second"] = &delegateLiveState{runtime: runtime}
		c.mu.Unlock()

		reservation, err := c.ReserveAttention(runtime, "attention-exact")
		if reservation != nil {
			_ = c.AbortStart(reservation)
		}
		if !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("ReserveAttention ambiguous owner error = %v, want busy", err)
		}
	})
}

func TestDelegateControllerReserveAttentionRequiresResidentRuntimeAndPendingID(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, runtime := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	if _, err := c.AdmitStartInput(started.lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	if _, err := c.FinishGeneration(started.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "completed"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}

	if _, err := c.ReserveAttention(&Session{}, "attention-1"); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("ReserveAttention foreign runtime error = %v, want exact-runtime rejection", err)
	}
	if _, err := c.ReserveAttention(runtime, ""); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveAttention empty ID error = %v, want busy", err)
	}
	reservation, err := c.ReserveAttention(runtime, "attention-1")
	if err != nil {
		t.Fatalf("ReserveAttention exact runtime: %v", err)
	}
	if turns, drives := c.capacityInUse(); turns != 0 || drives != 1 {
		t.Fatalf("attention capacity = (%d, %d), want (0, 1)", turns, drives)
	}
	attention, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart attention: %v", err)
	}
	if aggregate := c.durable["dlg_target"]; aggregate.Trigger != delegatestore.TriggerAttention || aggregate.Generation != 2 {
		t.Fatalf("attention aggregate = %#v, want generation 2 attention", aggregate)
	}
	live := c.live["dlg_target"]
	if live.binding == nil || live.binding.runtime != runtime || !live.binding.ready {
		t.Fatalf("attention binding = %#v, want exact ready resident runtime", live.binding)
	}
	if _, err := completeDelegateModelRequest(c, attention.lease); err != nil {
		t.Fatalf("BeginModelRequest attention: %v", err)
	}
}

func TestDelegateControllerAttentionCommitBindsSelectedPendingTranscriptEntry(t *testing.T) {
	const (
		sessionID   = "child-dlg_target"
		attentionID = "attention-exact"
	)
	c, runtime, transcriptPath := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	reservation, err := c.ReserveAttention(runtime, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	storePath := filepath.Join(c.stateDir, "delegate-events.jsonl")
	beforeJournal := readDelegateControllerFile(t, storePath)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("persist accepted attention before injected journal failure: %v", err)
	}
	if _, err := c.CommitStart(reservation); err == nil {
		t.Fatal("attention generation commit succeeded after delegate store close")
	}
	fold, err := readDelegateAttentionFold(transcriptPath, sessionID)
	if err != nil {
		t.Fatalf("read consumed attention: %v", err)
	}
	if fold.resolutions[attentionID] != delegateAttentionConsumed || fold.resumeGenerations[attentionID] != 1 {
		t.Fatalf("consumed marker = %q generation %d, want exact selected ID/generation 1", fold.resolutions[attentionID], fold.resumeGenerations[attentionID])
	}
	if got := readDelegateControllerFile(t, storePath); !bytes.Equal(got, beforeJournal) {
		t.Fatal("failed post-consumption append changed delegate journal")
	}
	if aggregate := c.durable["dlg_target"]; aggregate.Generation != 0 || !aggregate.NeedsAttention || aggregate.CurrentRunOpen {
		t.Fatalf("failed post-consumption append published aggregate: %#v", aggregate)
	}
	if len(c.reservations) != 1 || len(c.attentionWakeIDs["dlg_target"]) != 1 || c.live["dlg_target"].binding != nil {
		t.Fatalf("failed acceptance did not retain narrow retry state: reservations=%#v unresolved=%#v live=%#v", c.reservations, c.attentionWakeIDs["dlg_target"], c.live["dlg_target"])
	}
	reopened, err := delegatestore.Open(storePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("retry accepted attention: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("retry committed attention: %v", err)
	}
	live := c.live["dlg_target"]
	if started.lease.generation != 1 || live == nil || live.binding == nil || live.binding.lease != started.lease || live.binding.runtime != runtime || !live.binding.ready || len(live.attentionIDs) != 0 {
		t.Fatalf("retried attention binding = started:%#v live:%#v", started, live)
	}
	entries := readAttentionTranscriptEntries(t, transcriptPath)
	resolutionCount := 0
	for _, entry := range entries {
		if entry.Turn.AttentionResolution != nil && entry.Turn.AttentionResolution.AttentionID == attentionID {
			resolutionCount++
		}
	}
	if resolutionCount != 1 {
		t.Fatalf("attention retry appended %d exact resolution markers, want 1", resolutionCount)
	}
}

func TestDelegateControllerReservationReceiptCannotRedirectCommit(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	descriptor := delegateControllerCreateDescriptor()
	loopDetection := false
	descriptor.TaskTemplates = []taskpkg.TaskTemplate{{Title: "frozen", Prompt: "frozen prompt", ReasoningEffort: "high", Type: "verify", Insert: "parent_tasks"}}
	descriptor.ToolNameCeiling = []string{"communicate"}
	descriptor.ResultSchema = json.RawMessage(`{"type":"object"}`)
	descriptor.Config = schema.ConfigSnapshot{
		ToolOutputLimits:       map[string]schema.ToolOutputLimit{"read_file": {MaxChars: 111}},
		AgentName:              "controller-agent",
		ReasoningEffort:        "high",
		ModelFallbacks:         []string{"openai/frozen"},
		ShareTasksWithChildren: true,
		EnableLoopDetection:    &loopDetection,
	}
	descriptor.SharedTaskStoreOwnerSessionID = "root-session"
	reservation, err := c.ReserveCreate(rootDelegateActor("root-session"), descriptor)
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	wantID := reservation.delegateID
	wantTranscriptRef := reservation.descriptor.TranscriptRef
	wantWorkingDir := reservation.descriptor.WorkingDir
	wantTask := reservation.descriptor.Task
	wantTaskTemplates := append([]taskpkg.TaskTemplate(nil), reservation.descriptor.TaskTemplates...)
	wantSchema := append(json.RawMessage(nil), reservation.descriptor.ResultSchema...)
	wantConfig := reservation.descriptor.Config.Clone()
	wantSharedTaskStoreOwner := reservation.descriptor.SharedTaskStoreOwnerSessionID
	wantTranscriptPath := reservation.transcriptPath
	wantWorktreePath := reservation.worktreePath

	forged := *reservation
	if err := c.AbortStart(&forged); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("AbortStart copied receipt error = %v, want busy", err)
	}
	if turns, drives := c.capacityInUse(); turns != 1 || drives != 0 {
		t.Fatalf("capacity after forged abort = (%d,%d), want (1,0)", turns, drives)
	}

	descriptor.ToolNameCeiling[0] = "caller-mutated"
	descriptor.TaskTemplates[0].Prompt = "caller-mutated"
	descriptor.Config.ToolOutputLimits["read_file"] = schema.ToolOutputLimit{MaxChars: 999}
	descriptor.Config.ModelFallbacks[0] = "openai/caller-mutated"
	*descriptor.Config.EnableLoopDetection = true
	reservation.delegateID = "dlg_redirected"
	reservation.descriptor.Task = "redirected task"
	reservation.descriptor.TaskTemplates[0].Prompt = "receipt-mutated"
	reservation.descriptor.ToolNameCeiling[0] = "receipt-mutated"
	reservation.descriptor.TranscriptRef = "local:redirected"
	reservation.descriptor.WorkingDir = filepath.Join(t.TempDir(), "redirected")
	reservation.descriptor.ResultSchema = json.RawMessage(`{"type":"number"}`)
	reservation.descriptor.Config.ToolOutputLimits["read_file"] = schema.ToolOutputLimit{MaxChars: 777}
	reservation.descriptor.Config.ModelFallbacks[0] = "openai/receipt-mutated"
	*reservation.descriptor.Config.EnableLoopDetection = true
	reservation.descriptor.SharedTaskStoreOwnerSessionID = "redirected-owner"
	reservation.transcriptPath = filepath.Join(t.TempDir(), "redirected.transcript.jsonl")
	reservation.worktreePath = filepath.Join(t.TempDir(), "redirected-worktree")

	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if started.lease.delegateID != wantID {
		t.Fatalf("committed delegate ID = %q, want reserved %q", started.lease.delegateID, wantID)
	}
	if started.ctx == nil || started.ctx.Err() != nil || started.transcriptPath != wantTranscriptPath || started.worktreePath != wantWorktreePath || started.descriptor.Task != wantTask || !reflect.DeepEqual(started.descriptor.TaskTemplates, wantTaskTemplates) || started.descriptor.TranscriptRef != wantTranscriptRef || started.descriptor.WorkingDir != wantWorkingDir || !bytes.Equal(started.descriptor.ResultSchema, wantSchema) || !reflect.DeepEqual(started.descriptor.Config, wantConfig) || started.descriptor.SharedTaskStoreOwnerSessionID != wantSharedTaskStoreOwner {
		t.Fatalf("committed construction outputs = %#v, want authoritative reserved descriptor and paths", started)
	}
	if c.durable["dlg_redirected"] != nil || c.live["dlg_redirected"] != nil {
		t.Fatalf("caller-mutated target became authoritative: durable=%#v live=%#v", c.durable["dlg_redirected"], c.live["dlg_redirected"])
	}
	aggregate := c.durable[wantID]
	if aggregate == nil {
		t.Fatalf("reserved delegate %q was not committed", wantID)
	}
	if got := aggregate.Descriptor; got.Task != wantTask || !reflect.DeepEqual(got.TaskTemplates, wantTaskTemplates) || got.TranscriptRef != wantTranscriptRef || got.WorkingDir != wantWorkingDir || !reflect.DeepEqual(got.ToolNameCeiling, []string{"communicate"}) || !bytes.Equal(got.ResultSchema, wantSchema) || !reflect.DeepEqual(got.Config, wantConfig) || got.SharedTaskStoreOwnerSessionID != wantSharedTaskStoreOwner {
		t.Fatalf("committed descriptor trusted caller mutation:\n got %#v\nwant task=%q transcript=%q working_dir=%q tools=[communicate] schema=%s", got, wantTask, wantTranscriptRef, wantWorkingDir, wantSchema)
	}
	if turns, drives := c.capacityInUse(); turns != 1 || drives != 0 {
		t.Fatalf("capacity after authoritative commit = (%d,%d), want (1,0)", turns, drives)
	}
}

func delegateControllerCreateDescriptor() delegatestore.Descriptor {
	return delegatestore.Descriptor{
		Task:            "create a dormant delegate",
		Description:     "controller test",
		AgentType:       "general",
		ToolNameCeiling: []string{"communicate"},
		Isolation:       "worktree",
		Resumable:       true,
	}
}

func commitAttachedDelegateControllerStart(t *testing.T, c *delegateTreeController, delegateID string) (delegateStartCommit, *Session) {
	t.Helper()
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	runtime := &Session{}
	if err := c.AttachRuntime(started.lease, runtime); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	return started, runtime
}

func assertDelegateControllerPathAbsent(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		t.Fatal("expected a deterministic non-empty path")
	}
	if _, err := os.Lstat(filepath.Clean(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %s exists or has unexpected error: %v", path, err)
	}
}

// TestDelegateControllerCommittedStartFailureAppendFailureIsNotAStopWin pins the
// distinction FailCommittedStart exists to make. A pre-attach stop race has two
// outcomes, and they are not interchangeable: the stop WON (the stopped-finish
// landed durably, the generation is closed, and the caller may dispose the
// unadopted child), or the stopped-finish APPEND FAILED (the generation is still
// durably open, recovery is fenced, and the child must be retained).
//
// Both exits of finishStoppedStartLocked are errDelegateTargetBusy-shaped -- the
// failure path returns errors.Join(errDelegateTargetBusy, appendErr) -- so
// classifying them with errors.Is matches the joined error too and reports a
// failed durable write as a won race.
func TestDelegateControllerCommittedStartFailureAppendFailureIsNotAStopWin(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, c, "dlg_target")
	_, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	// The generation is durably open and the store now refuses every write, so
	// the stopped-finish append cannot land. A closed store is the real failure
	// this path must survive, not an injected one.
	if err := c.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	_, claimedForClose, failErr := c.FailCommittedStart(
		started.lease,
		delegatePermanentStartFailure(errors.New("construction failed"), "construction_failed"),
		"construction_failed",
		nil,
	)
	if got := committedStartFailureDisposition(failErr); got != delegateCommittedStartFailureAppendFailed {
		t.Fatalf("disposition = %d, want %d (append failed); err = %v",
			got, delegateCommittedStartFailureAppendFailed, failErr)
	}
	if claimedForClose {
		t.Fatal("a failed durable append claimed the runtime for close")
	}
	if live := c.live["dlg_target"]; live == nil || !live.recoveryRequired {
		t.Fatalf("live state = %#v, want recoveryRequired after a failed append", live)
	}
}
