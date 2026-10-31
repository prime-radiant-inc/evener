package agent

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
)

func waitedResolution(t *testing.T, waiter *delegateInlineWaiter) delegateInlineResolution {
	t.Helper()
	select {
	case resolution := <-waiter.resolution:
		return resolution
	default:
		t.Fatal("inline subscriber was not resolved")
		return delegateInlineResolution{}
	}
}

func pendingDeliveryIDs(c *delegateTreeController, delegateID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, pending := range c.durable[delegateID].PendingDeliveries {
		ids = append(ids, pending.DeliveryID)
	}
	return ids
}

// A subscriber uses the same real receiver writer as ordinary delivery. The
// source journal, not a receiver queue, owns any retry before custody.
func attachDeliveryTestReceiver(t *testing.T, c *delegateTreeController) *Session {
	t.Helper()
	writer, err := transcript.NewWriter(transcriptPath(c.stateDir, c.rootSessionID), transcript.Header{SessionID: c.rootSessionID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	root := &Session{id: c.rootSessionID, stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(writer)
	c.mu.Lock()
	c.rootRuntime = root
	c.mu.Unlock()
	return root
}

func canonicalGenerationsFixture(t *testing.T, count int) (*delegateTreeController, *Session, delegateDeliveryPlan, *delegateInlineWaiter) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, count+1, 1)
	root := attachDeliveryTestReceiver(t, c)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	for i := 1; i < count; i++ {
		lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
		if err := root.executeDelegateMutationPlans(finishDelegateDeliveryGeneration(t, c, lease, []string{"first", "second"}[i-1])); err != nil {
			t.Fatal(err)
		}
	}
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "latest").deliveries[0]
	return c, root, plan, waiter
}

func TestCanonicalDeliveryRefusedPlanLeavesEarlierCustody(t *testing.T) {
	c, root, plan, waiter := canonicalGenerationsFixture(t, 2)
	forged := plan
	forged.packet = cloneDelegateTerminalPacket(plan.packet)
	forged.packet.Message = []byte(`"forged"`)
	if _, err := deliverDelegatePacket(forged, root); err != nil {
		t.Fatal(err)
	}
	if !waitedResolution(t, waiter).fallback {
		t.Fatal("forged projection did not fall back")
	}
	ids, err := root.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{delegateAttentionID(delegateDeliveryID("dlg_target", 1))}) {
		t.Fatalf("earlier canonical custody = %v", ids)
	}
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatal(err)
	}
	if ids := pendingDeliveryIDs(c, "dlg_target"); len(ids) != 0 {
		t.Fatalf("pending source = %v", ids)
	}
}

func TestCanonicalDeliveryStoppedPlanLeavesNoStaleClaim(t *testing.T) {
	c, root, plan, waiter := canonicalGenerationsFixture(t, 2)
	appendDelegateControllerStopRequest(t, c, "dlg_target")
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatal(err)
	}
	if !waitedResolution(t, waiter).fallback {
		t.Fatal("stopped subscriber did not fall back")
	}
	c.mu.Lock()
	claims := len(c.deliveryClaims)
	c.mu.Unlock()
	if claims != 0 {
		t.Fatalf("claims after stop refusal = %d", claims)
	}
}

func TestCanonicalDeliveryAbandonedProjectionKeepsOrdinaryCustody(t *testing.T) {
	c, root, plan, waiter := canonicalGenerationsFixture(t, 3)
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatal(err)
	}
	resolution := waitedResolution(t, waiter)
	if len(resolution.earlier) != 2 || resolution.commit == nil {
		t.Fatalf("resolution = %#v", resolution)
	}
	for _, commit := range []*delegateToolResultCommit{resolution.earlier[0].commit, resolution.earlier[1].commit, resolution.commit} {
		if _, err := commit.Complete(false); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := root.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{delegateAttentionID(delegateDeliveryID("dlg_target", 1)), delegateAttentionID(delegateDeliveryID("dlg_target", 2)), delegateAttentionID(plan.deliveryID)}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ordinary canonical IDs = %v, want %v", ids, want)
	}
	if len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatal("projection abandonment reopened source custody")
	}
}

func TestCanonicalDeliveryOwnerStopDoesNotRestoreProjectionClaims(t *testing.T) {
	// Serial: capture expected missing-target errors through controller teardown.
	logged := &reclaimLogCapture{Handler: slog.NewTextHandler(io.Discard, nil)}
	previous := slog.Default()
	slog.SetDefault(slog.New(logged))
	t.Cleanup(func() { slog.SetDefault(previous) })
	snapshot := func() []slog.Record {
		logged.mu.Lock()
		defer logged.mu.Unlock()
		return append([]slog.Record(nil), logged.records...)
	}
	c, _ := newDelegateControllerTestHarness(t, 3, 2)
	root := attachDeliveryTestReceiver(t, c)
	clk := agenttest.NewFakeClock()
	root.clock = clk
	ownerID := identifier.MustNewSessionID()
	created := delegateControllerCreatedEvent("dlg_owner", "")
	created.Created.Descriptor.ChildSessionID = ownerID
	created.Created.Descriptor.TranscriptRef = "local:" + ownerID
	c.mu.Lock()
	_, err := c.appendLocked(created, delegateControllerRunStartedEvent("dlg_owner", 1, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()))
	if err == nil {
		// Preserve the original synthetic binding: no runtime/executor debt.
		c.live["dlg_owner"] = &delegateLiveState{binding: &delegateRuntimeBinding{
			lease: delegateLease{delegateID: "dlg_owner", generation: 1}, cancel: func() {}, ready: true,
			evidence: &delegateGenerationEvidence{requirement: delegateCompletionReportRequired},
		}}
	}
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	seedDelegateControllerIdle(t, c, "dlg_target", "dlg_owner")
	cfg := minimalTestSessionConfig(t)
	cfg.StateDir = c.stateDir
	cfg.AgentsDocPath = filepath.Join(t.TempDir(), "absent-agents.md")
	cfg.spawn = spawnConfig{
		sessionID: ownerID, delegateController: c, delegateRootSessionID: c.rootSessionID,
		owningDelegateID: "dlg_owner", parentSessionID: root.ID(), parentDelegateID: "dlg_owner",
		depth: 1, delegationAllowance: 1,
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	// A child does not own the independently allocated environment's process table.
	t.Cleanup(env.Cleanup)
	owner := newSession(t, withConfig(cfg), withEnv(env), withClock(clk))
	ownerLease := delegateLease{delegateID: "dlg_owner", generation: 1}
	ownerUnfinished := true
	var reservation *delegateStartReservation
	var targetLease *delegateLease
	var commits []*delegateToolResultCommit
	// Registered before publication/wakes, after child auto-Close and env cleanup.
	// Only this test's exact operations need finishing if an assertion exits early.
	t.Cleanup(func() {
		if reservation != nil {
			if err := c.AbortStart(reservation); err != nil {
				t.Error(err)
			}
		}
		for _, commit := range commits {
			if _, err := commit.Complete(false); err != nil {
				t.Error(err)
			}
		}
		unfinished := 0
		leases := []*delegateLease{targetLease}
		if ownerUnfinished {
			leases = append(leases, &ownerLease)
		}
		for _, lease := range leases {
			if lease == nil {
				continue
			}
			plans, err := c.FinishGeneration(*lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "test_cleanup"})
			if err != nil {
				t.Error(err)
			} else if err := root.executeDelegateMutationPlans(plans); err != nil {
				t.Error(err)
			}
			unfinished++
		}
		c.mu.Lock()
		resident := c.live["dlg_owner"] != nil && c.live["dlg_owner"].runtime == owner
		c.mu.Unlock()
		if !resident {
			t.Error("constructed owner was not resident before controller Close")
		}
		before := snapshot()
		if err := c.Close(context.Background()); err != nil {
			t.Errorf("controller Close: %v", err)
		}
		c.mu.Lock()
		closing := c.closing
		c.mu.Unlock()
		if !closing {
			t.Error("controller Close did not fence the controller")
		}
		clk.Drain()
		clk.Advance(jobNotificationRetryMaxDelay)
		clk.Drain()
		if got := clk.PendingCallbacks(); got != 0 {
			t.Errorf("callbacks after controller Close/drain = %d, want zero", got)
		}
		if !reflect.DeepEqual(snapshot(), before) {
			t.Error("controller Close/drain produced additional diagnostics")
		}
		t.Logf("controller Close returned, unfinished synthetic leases settled=%d, callbacks after drain=%d", unfinished, clk.PendingCallbacks())
	})
	if owner.ID() != ownerID || owner.stateDir != c.stateDir || owner.delegateController != c || owner.ownsDelegateController ||
		owner.delegateRootSessionID != c.rootSessionID || owner.owningDelegateID != "dlg_owner" || owner.cfg.spawn.parentSessionID != root.ID() ||
		owner.cfg.spawn.parentDelegateID != "dlg_owner" || owner.depth != 1 || owner.transcript == nil {
		t.Fatal("constructed owner did not inherit child identity/controller ownership")
	}
	c.mu.Lock()
	c.live["dlg_owner"].runtime = owner
	c.mu.Unlock()
	actor := delegateActor{rootSessionID: "root-session", lease: &ownerLease}
	var first delegateDeliveryPlan
	for i := range 2 {
		reservation, err = c.ReserveStart(actor, "dlg_target")
		if err != nil {
			t.Fatal(err)
		}
		var waiter *delegateInlineWaiter
		if i == 1 {
			waiter, err = c.RegisterInlineWaiter(reservation)
			if err != nil {
				t.Fatal(err)
			}
		}
		started, err := c.CommitStart(reservation)
		if err != nil {
			t.Fatal(err)
		}
		reservation = nil
		targetLease = &started.lease
		plan := finishDelegateDeliveryGeneration(t, c, started.lease, "result").deliveries[0]
		targetLease = nil
		if i == 0 {
			first = plan
		}
		if _, err := deliverDelegatePacket(plan, owner); err != nil {
			t.Fatal(err)
		}
		if waiter != nil {
			resolution := waitedResolution(t, waiter)
			if len(resolution.earlier) != 1 || resolution.commit == nil {
				t.Fatalf("resolution=%#v", resolution)
			}
			commits = []*delegateToolResultCommit{resolution.earlier[0].commit, resolution.commit}
		}
	}
	stop, _, stopPlans, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.executeDelegateMutationPlans(stopPlans); err != nil {
		t.Fatal(err)
	}
	for _, commit := range commits {
		if _, err := commit.Complete(false); err != nil {
			t.Fatal(err)
		}
	}
	commits = nil
	if _, err := c.FinishGeneration(ownerLease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	ownerUnfinished = false
	for range 4 {
		plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
		if err != nil {
			t.Fatal(err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-stop.done:
	default:
		t.Fatal("owner stop did not complete")
	}
	if _, admitted, _ := c.BeginDelivery(first); admitted {
		t.Fatal("stale projection restored a stopped delivery claim")
	}
	c.mu.Lock()
	work := c.hasDeliveryWorkForOwnerLocked("dlg_owner")
	targetClaims := 0
	for _, claim := range c.deliveryClaims {
		if claim.delegateID == "dlg_target" {
			targetClaims++
		}
	}
	c.mu.Unlock()
	if work || targetClaims != 0 {
		t.Fatalf("owner work=%t target claims=%d", work, targetClaims)
	}
	if got := clk.PendingCallbacks(); got != 1 {
		t.Fatalf("armed real retry callbacks = %d, want one", got)
	}
	records := snapshot()
	if len(records) != 3 {
		t.Errorf("missing-target diagnostics = %d, want exactly three", len(records))
	}
	var restored string
	var causes []error
	targetID := c.durable["dlg_target"].Descriptor.ChildSessionID
	for _, record := range records {
		if record.Level != slog.LevelWarn {
			t.Errorf("diagnostic level = %v, want WARN", record.Level)
		}
		attrs := make(map[string]any)
		var keys []string
		record.Attrs(func(attr slog.Attr) bool {
			keys = append(keys, attr.Key)
			attrs[attr.Key] = attr.Value.Any()
			return true
		})
		sort.Strings(keys)
		switch {
		case reflect.DeepEqual(keys, []string{"action", "delegate", "error", "session"}):
			action, actionOK := attrs["action"].(string)
			cause, causeOK := attrs["error"].(string)
			if !actionOK || action == "" || !causeOK || restored != "" || attrs["session"] != root.ID() || attrs["delegate"] != "dlg_target" {
				t.Errorf("unexpected restore diagnostic attributes: %#v", attrs)
			}
			restored = cause
		case reflect.DeepEqual(keys, []string{"error", "session_id"}):
			cause, causeOK := attrs["error"].(error)
			var pathErr *fs.PathError
			if !causeOK || attrs["session_id"] != targetID || !errors.Is(cause, fs.ErrNotExist) || !errors.As(cause, &pathErr) ||
				pathErr.Path != transcriptPath(c.stateDir, targetID) {
				t.Errorf("unexpected typed missing-transcript diagnostic: %#v", attrs)
			}
			causes = append(causes, cause)
		default:
			t.Errorf("unexpected diagnostic attributes: %#v", attrs)
		}
	}
	if len(causes) != 2 || restored == "" {
		t.Errorf("restore/wake diagnostic counts = %t/%d, want one/two", restored != "", len(causes))
	}
	for _, cause := range causes {
		if cause == nil || restored != cause.Error() {
			t.Errorf("unpaired restore/wake cause: restore=%q wake=%v", restored, cause)
		}
	}
	t.Logf("real missing-target retry armed, callbacks=%d, validated diagnostics=%d", clk.PendingCallbacks(), len(records))
}

func TestCanonicalDeliveryEarlierResultsRemainOrdered(t *testing.T) {
	c, root, plan, waiter := canonicalGenerationsFixture(t, 3)
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatal(err)
	}
	resolution := waitedResolution(t, waiter)
	if len(resolution.earlier) != 2 {
		t.Fatalf("earlier results = %#v", resolution.earlier)
	}
	for i, want := range []string{"first", "second"} {
		if !strings.Contains(string(resolution.earlier[i].packet.Message), want) || resolution.earlier[i].commit.deliveryID != delegateDeliveryID("dlg_target", uint64(i+1)) {
			t.Fatalf("earlier %d = %#v", i, resolution.earlier[i])
		}
	}
	if len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatal("inline subscriber retained source custody")
	}
}

func TestCanonicalDeliverySourceAckFailureRetriesWithoutDuplicate(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	root := attachDeliveryTestReceiver(t, c)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "first").deliveries[0]
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := deliverDelegatePacket(plan, root); err == nil {
		t.Fatal("source acknowledgment unexpectedly succeeded")
	}
	// A failed immutable-packet lookup cannot steal ordinary canonical delivery.
	if !waitedResolution(t, waiter).fallback {
		t.Fatal("failed packet lookup did not fall back")
	}
	ids, err := root.pendingNotificationIDs()
	if err != nil || len(ids) != 1 {
		t.Fatalf("canonical fallback = %v, %v", ids, err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	if err := root.replayDelegateDeliveries(); err != nil {
		t.Fatal(err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, c.rootSessionID), c.rootSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fold.order) != 1 || len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatalf("fold order=%v pending=%v", fold.order, pendingDeliveryIDs(c, "dlg_target"))
	}
}

func TestCanonicalDeliveryPacketReadFailureKeepsUnrelatedProgress(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 2)
	root := attachDeliveryTestReceiver(t, c)
	seedDelegateControllerIdle(t, c, "dlg_waited", "")
	seedDelegateControllerIdle(t, c, "dlg_other", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_waited", true)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "waited")
	other, _ := startDelegateDeliveryGeneration(t, c, "dlg_other", false)
	plans.deliveries = append(plans.deliveries, finishDelegateDeliveryGeneration(t, c, other, "other").deliveries...)
	// The source writer remains open and can acknowledge custody. Only the
	// immutable packet lookup through Store.Load loses its external read path.
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(path+".saved", path) })
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatal(err)
	}
	if !waitedResolution(t, waiter).fallback {
		t.Fatal("packet lookup failure did not fall back")
	}
	assertNotificationPending(t, root, []string{delegateAttentionID(delegateDeliveryID("dlg_waited", 1)), delegateAttentionID(delegateDeliveryID("dlg_other", 1))})
	if len(pendingDeliveryIDs(c, "dlg_waited")) != 0 || len(pendingDeliveryIDs(c, "dlg_other")) != 0 {
		t.Fatal("packet lookup failure blocked source custody or unrelated progress")
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if plans := c.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("already accepted sources replayed: %#v", plans)
	}
}

func TestCanonicalDeliveryFailedReceiverPreservesSourceFIFO(t *testing.T) {
	c, first, _, _, _ := controllerWithTwoDelegateDeliveries(t, false, true)
	receiver := newFakeDelegateDeliveryReceiver()
	receiver.fail = errInjectedTranscriptWrite
	if _, err := deliverDelegatePacket(first, receiver); !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("receiver failure=%v", err)
	}
	if got := pendingDeliveryIDs(c, "dlg_target"); !reflect.DeepEqual(got, []string{delegateDeliveryID("dlg_target", 1), delegateDeliveryID("dlg_target", 2)}) {
		t.Fatalf("source FIFO=%v", got)
	}
	receiver.fail = nil
	queue := c.ReplayDeliveries()
	for len(queue) > 0 {
		plan := queue[0]
		queue = queue[1:]
		next, err := deliverDelegatePacket(plan, receiver)
		if err != nil {
			t.Fatal(err)
		}
		queue = append(queue, next.deliveries...)
	}
	if receiver.writes != 2 || len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatalf("writes=%d pending=%v", receiver.writes, pendingDeliveryIDs(c, "dlg_target"))
	}
}

// seedPendingDeliverySource replaces receiver-queue-only fixtures with an actual
// journal-backed producer obligation. It deliberately does not run transport.
func seedPendingDeliverySource(t *testing.T, s *Session, clk *agenttest.FakeClock) string {
	t.Helper()
	previous := slog.Default()
	logged := &reclaimLogCapture{Handler: previous.Handler()}
	previousWriter, previousFlags := log.Writer(), log.Flags()
	// Do not wrap the default slog handler: SetDefault links it back to log.
	slog.SetDefault(slog.New(slog.NewMultiHandler(slog.NewTextHandler(os.Stderr, nil), logged)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	snapshot := func() []slog.Record {
		logged.mu.Lock()
		defer logged.mu.Unlock()
		return append([]slog.Record(nil), logged.records...)
	}
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	c.rootSessionID = s.ID()
	created := delegateControllerCreatedEvent("dlg_pending", "")
	created.Created.Descriptor.OwnerSessionID = s.ID()
	c.mu.Lock()
	_, err := c.appendLocked(created)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed pending delegate: %v", err)
	}
	seedDelegateControllerDelivery(t, c, "dlg_pending")
	if s.transcript == nil {
		s.stateDir = c.stateDir
		writer, err := transcript.NewWriter(transcriptPath(s.stateDir, s.ID()), transcript.Header{SessionID: s.ID()})
		if err != nil {
			t.Fatal(err)
		}
		s.attachTranscript(writer)
		t.Cleanup(func() { _ = writer.Close() })
	}
	old := s.delegateController
	s.delegateController = c
	c.rootRuntime = s
	// Close/join before restoring the Session pointer or closing its writer/store.
	t.Cleanup(func() {
		defer func() { s.delegateController = old }()
		before := snapshot()
		if err := c.Close(context.Background()); err != nil {
			t.Errorf("temporary controller Close: %v", err)
		}
		clk.Drain()
		clk.Advance(jobNotificationRetryMaxDelay)
		clk.Drain()
		c.mu.Lock()
		closing := c.closing
		c.mu.Unlock()
		if !closing || clk.PendingCallbacks() != 0 {
			t.Errorf("temporary controller closing=%t callbacks=%d, want true/0", closing, clk.PendingCallbacks())
		}
		if !reflect.DeepEqual(snapshot(), before) {
			t.Errorf("temporary controller teardown added diagnostics: before=%d after=%d", len(before), len(snapshot()))
		}
		for _, record := range before {
			attrs := make(map[string]any)
			var keys []string
			record.Attrs(func(attr slog.Attr) bool {
				keys = append(keys, attr.Key)
				attrs[attr.Key] = attr.Value.Any()
				return true
			})
			sort.Strings(keys)
			if record.Level != slog.LevelWarn {
				t.Errorf("unexpected diagnostic level: %v", record.Level)
			}
			switch {
			case reflect.DeepEqual(keys, []string{"action", "delegate", "error", "session"}):
				cause, ok := attrs["error"].(string)
				if attrs["action"] != delegateAttentionRestoreLabel || attrs["delegate"] != "dlg_pending" || attrs["session"] != s.ID() || !ok || cause == "" {
					t.Errorf("unexpected restore diagnostic attributes: %#v", attrs)
				}
			case reflect.DeepEqual(keys, []string{"error", "session_id"}):
				cause, ok := attrs["error"].(error)
				var pathErr *fs.PathError
				if !ok || attrs["session_id"] != "child-dlg_pending" || !errors.Is(cause, fs.ErrNotExist) || !errors.As(cause, &pathErr) || pathErr.Path != transcriptPath(c.stateDir, "child-dlg_pending") {
					t.Errorf("unexpected typed missing-transcript diagnostic: %#v", attrs)
				}
			default:
				t.Errorf("unexpected diagnostic attributes: %#v", attrs)
			}
		}
		t.Logf("temporary controller cleanup before pointer restoration: closing=%t callbacks=%d diagnostics=%d", closing, clk.PendingCallbacks(), len(snapshot()))
	})
	return delegateDeliveryID("dlg_pending", 1)
}
