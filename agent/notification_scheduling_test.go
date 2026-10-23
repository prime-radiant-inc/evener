package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

func TestNotificationRestoredCallerHasOneTransport(t *testing.T) {
	a := &skillContinuationAdapter{}
	s := observationSession(t, a, false)
	state := jobstore.WatchSendState{
		Key:        jobstore.WatchSendKey{VisibleSessionID: s.ID(), WatchID: "watch-restore-7", WatchGeneration: "wg-7", ResolvedSendTo: runtimeMessageAliasCaller},
		DeliveryID: "restored-caller-7", UpdateSeq: 1, Frame: "payload-restored-7a91", ReceiverSessionID: s.ID(),
		Provenance: testProvenance("watch-restore-7", "wg-7"),
	}
	if err := s.jobManager.appendWatchSendEvents([]jobstore.Event{{Kind: jobstore.EventWatchSendPending, TS: time.Now(), WatchSend: &state}}); err != nil {
		t.Fatal(err)
	}
	if err := s.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
	if err != nil {
		t.Fatal(err)
	}
	s.jobManager = restored
	restored.retirementOwner, restored.delegateController = s, s.delegateController
	appendEvent := restored.appendEvent
	ackAttempts := 0
	restored.appendEvent = func(event jobstore.Event) error {
		if event.Kind == jobstore.EventWatchSendDelivered {
			ackAttempts++
			return errors.New("controlled source ack failure")
		}
		return appendEvent(event)
	}
	_ = s.retryRestoredPendingWatchSends(context.Background())
	if ackAttempts == 0 {
		t.Fatal("source ack fault was not exercised")
	}
	assertNotificationPending(t, s, []string{stableWatchAttentionID(state)})
	a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
		count := 0
		for _, msg := range req.Messages {
			count += strings.Count(msg.Text(), state.Frame)
		}
		if count != 1 {
			t.Errorf("restored caller payload occurrences = %d, want one canonical presentation", count)
		}
		return llm.Response{Message: llm.Assistant("observed-restored-7")}, nil
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, s, []string{})
	restored.appendEvent = appendEvent
	if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)); got != 1 {
		t.Fatalf("source delivered records = %d, want 1", got)
	}
	assertNotificationPending(t, s, []string{})
}

func TestNotificationScheduling(t *testing.T) {
	t.Parallel()
	for _, resident := range []bool{false, true} {
		for _, running := range []bool{false, true} {
			name := fmt.Sprintf("resident=%t/running=%t", resident, running)
			t.Run("root/"+name, func(t *testing.T) {
				a := &skillContinuationAdapter{}
				s := observationSession(t, a, false)
				observationEnqueue(t, s, "root-first", "root-body-913a")
				if !resident {
					s = observationReopen(t, s, a)
				}
				wakes := make(chan struct{}, 8)
				// Registration after durable admission must recover a lost edge.
				s.SetNotifyFunc(func() { wakes <- struct{}{} })
				select {
				case <-wakes:
				default:
					t.Fatal("late callback lost durable notification")
				}
				var active atomic.Int32
				a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
					if active.Add(1) != 1 {
						t.Error("parallel root executor")
					}
					defer active.Add(-1)
					observationRequest(t, req, []string{"root-body-913a"}, nil)
					if running {
						observationEnqueue(t, s, "root-second", "root-body-728b")
						if err := s.dispatchNotificationWake(); err != nil {
							t.Error(err)
						}
						select {
						case <-wakes:
							t.Error("root woke while executor still owned input")
						default:
						}
					}
					return llm.Response{Message: llm.Assistant("root-result-817c")}, nil
				}, func(req llm.Request) (llm.Response, error) {
					observationRequest(t, req, []string{"root-body-728b"}, nil)
					return llm.Response{Message: llm.Assistant("root-successor-671d")}, nil
				}}
				if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				if running {
					select {
					case <-wakes:
					default:
						t.Fatal("root release lost successor")
					}
					if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
						t.Fatal(err)
					}
				}
				assertNotificationPending(t, s, []string{})
				want := 1
				if running {
					want++
				}
				if got := len(a.Requests()); got != want {
					t.Fatalf("provider requests=%d, want %d", got, want)
				}
			})
			t.Run("delegate/"+name, func(t *testing.T) {
				fixture := newColdStableDelegateFixture(t, "")
				delivered := make(chan llm.Request, 2)
				var root *Session
				var active atomic.Int32
				if resident {
					fixture.adapter.steps = append(fixture.adapter.steps, func(llm.Request) llm.Response { return finalResponse("warm-791a") })
				}
				fixture.adapter.steps = append(fixture.adapter.steps, func(req llm.Request) llm.Response {
					if active.Add(1) != 1 {
						t.Error("parallel child executor")
					}
					defer active.Add(-1)
					delivered <- req
					if running {
						if err := root.delegateController.enqueueNotification(fixture.childID, notification{ID: "target-successor", Message: llm.User("payload-successor-39ad")}); err != nil {
							t.Error(err)
						}
						if err := root.delegateController.requestSessionWake(fixture.childID); err != nil {
							t.Error(err)
						}
					}
					return finalResponse("result-731d")
				})
				if running {
					fixture.adapter.steps = append(fixture.adapter.steps, func(req llm.Request) llm.Response {
						if active.Add(1) != 1 {
							t.Error("parallel successor executor")
						}
						defer active.Add(-1)
						delivered <- req
						return finalResponse("successor-817f")
					})
				}
				root = restoreSupervisionRoot(t, fixture, nil)
				if resident {
					warmStableSupervisionDelegate(t, root, fixture)
					waitForStableSupervisionRun(t, root, fixture.childID)
				}
				// No root callback or parent-model turn is installed.
				if err := root.delegateController.enqueueNotification(fixture.childID, notification{ID: "target-519a", Message: llm.User("payload-529da")}); err != nil {
					t.Fatal(err)
				}
				for i, payload := range []string{"payload-529da", "payload-successor-39ad"} {
					if i == 1 && !running {
						break
					}
					select {
					case req := <-delivered:
						observationRequest(t, req, []string{payload}, nil)
					case <-time.After(5 * time.Second):
						t.Fatal("exact receiver did not reach provider without a parent turn")
					}
				}
				waitForStableSupervisionRun(t, root, fixture.childID)
				want := 1
				if resident {
					want++
				}
				if running {
					want++
				}
				if got := supervisionRequestCount(fixture.adapter); got != want {
					t.Fatalf("provider requests=%d, want %d", got, want)
				}
			})
		}
	}
	t.Run("capacity refusal recovers without parent input", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		reached := make(chan llm.Request, 1)
		fixture.adapter.steps = []func(llm.Request) llm.Response{func(req llm.Request) llm.Response { reached <- req; return finalResponse("capacity-recovered-73af") }}
		root := restoreSupervisionRoot(t, fixture, nil)
		c := root.delegateController
		c.mu.Lock()
		c.driveLimit = 1
		generation := c.durable[fixture.delegateID].Generation
		if !c.reserveCapacityLocked(delegateDriveCapacity) {
			t.Fatal("saturate capacity")
		}
		c.mu.Unlock()
		if err := root.delegateController.enqueueNotification(fixture.childID, notification{ID: "capacity-817f", Message: llm.User("capacity-body-98af")}); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		started := c.durable[fixture.delegateID].Generation != generation
		c.mu.Unlock()
		if started {
			t.Fatal("receiver admitted a generation without capacity")
		}
		select {
		case <-reached:
			t.Fatal("receiver bypassed capacity")
		default:
		}
		c.mu.Lock()
		c.releaseCapacityLocked(delegateDriveCapacity)
		c.mu.Unlock()
		c.recheckNotificationReceivers()
		select {
		case req := <-reached:
			observationRequest(t, req, []string{"capacity-body-98af"}, nil)
		case <-time.After(5 * time.Second):
			t.Fatal("capacity release stranded receiver without parent input")
		}
		waitForStableSupervisionRun(t, root, fixture.childID)
	})
	t.Run("failed receiver does not block healthy sibling", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		reached := make(chan llm.Request, 1)
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("healthy-warm-79af") },
			func(req llm.Request) llm.Response { reached <- req; return finalResponse("healthy-handled-78ae") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		healthy := root.createDelegate(context.Background(), delegateArgs{Task: "healthy sibling-19ab"})
		if healthy.Err != nil {
			t.Fatal(healthy.Err)
		}
		waitForStableSupervisionRun(t, root, healthy.ChildSessionID)
		if err := os.Remove(filepath.Join(fixture.stateDir, sessionsSubdir, fixture.childID+".meta.json")); err != nil {
			t.Fatal(err)
		}
		if err := root.delegateController.enqueueNotification(fixture.childID, notification{ID: "broken-781a", Message: llm.User("broken-body-61af")}); err != nil {
			t.Fatal(err)
		}
		if err := root.delegateController.enqueueNotification(healthy.ChildSessionID, notification{ID: "healthy-817a", Message: llm.User("healthy-body-91ac")}); err != nil {
			t.Fatal(err)
		}
		select {
		case req := <-reached:
			observationRequest(t, req, []string{"healthy-body-91ac"}, []string{"broken-body-61af"})
		case <-time.After(5 * time.Second):
			t.Fatal("failed cold receiver blocked healthy sibling")
		}
		waitForStableSupervisionRun(t, root, healthy.ChildSessionID)
	})
	t.Run("capacity recheck preserves unadmitted fairness", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		root := restoreSupervisionRoot(t, fixture, nil)
		c := root.delegateController
		c.mu.Lock()
		c.attentionStateLocked(fixture.delegateID).driveTurn = 7
		c.attentionDriveSeq = 7
		c.mu.Unlock()
		c.recheckNotificationReceivers()
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.attentionDriveSeq != 7 {
			t.Fatalf("recheck spent fairness without an admission: turn=%d, want 7", c.attentionDriveSeq)
		}
	})
	t.Run("approval hold and decision", func(t *testing.T) {
		a := &fakeAdapter{name: "openai"}
		var requests atomic.Int32
		s := escalatableSession(t, withAdapter(a), withConfig(SessionConfig{StateDir: t.TempDir()}), withoutGitSnapshot())
		wakes := make(chan struct{}, 8)
		s.SetNotifyFunc(func() { wakes <- struct{}{} })
		a.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
			requests.Add(1)
			res, _ := deniedResult("/etc/hosts")
			s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t))
			return finalResponse("approval-denied-71ac")
		}, func(req llm.Request) llm.Response {
			requests.Add(1)
			observationRequest(t, req, []string{"approval-body-91af"}, nil)
			return finalResponse("approval-successor-68ab")
		}}
		done := make(chan error, 1)
		go func() { _, err := s.ProcessInput(context.Background(), "start approval-73af", nil); done <- err }()
		ids := awaitPending(t, s, 1)
		observationEnqueue(t, s, "approval-71af", "approval-body-91af")
		select {
		case <-wakes:
			t.Fatal("pending approval bypassed running executor")
		default:
		}
		if requests.Load() != 1 {
			t.Fatal("pending approval started a parallel model request")
		}
		if err := s.ResolveSandboxEscalation(ids[0], false); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		// The answer may consume the message in the current continuation, otherwise
		// release must arrange exactly one successor without another user input.
		if s.hasPendingNotifications() {
			select {
			case <-wakes:
			default:
				t.Fatal("approval decision lost successor wake")
			}
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
		}
		assertNotificationPending(t, s, []string{})
		if got := len(a.Requests()); got != 2 {
			t.Fatalf("requests=%d, want initial plus one notification", got)
		}
	})
	t.Run("question hold and answer", TestAskUser_ReplyDrainsRootDelegateAttentionRefusedAtEntryGate)
	t.Run("Stop hold and reengagement", TestStopParksRootDelegateAttentionUntilReEngagement)
}

// The provider boundary fixes arrival after request construction but before the
// executor releases. The wake callback is the daemon adapter, not an executor.
func TestNotificationAtExecutorRelease(t *testing.T) {
	t.Parallel()
	t.Run("storage recovery after release budget", func(t *testing.T) {
		h := newStandDownHarness(t)
		turnID, refusal := h.sess.mintRunningTurnID()
		if refusal != turnNameMinted {
			t.Fatalf("mint refusal = %v", refusal)
		}
		writes := make(chan struct{}, 32)
		h.sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
			writes <- struct{}{}
			return errors.New("controlled release failure")
		}
		baselineTimers := h.clock.BlockedCount()
		h.sess.releaseRunningTurnID(turnID)
		<-writes
		for range runningTurnReleaseRetryLimit {
			h.clock.BlockUntil(baselineTimers + 1)
			h.clock.Advance(jobNotificationRetryMaxDelay)
			<-writes
		}
		h.clock.BlockUntil(baselineTimers + 1)
		h.sess.mu.Lock()
		retained := h.sess.notificationSchedule.releaseTurnID
		h.sess.mu.Unlock()
		if retained != turnID {
			t.Fatalf("release exhaustion retained %q, want exact obligation %q", retained, turnID)
		}
		if err := h.sess.finishNotificationExecution(nil); err != nil {
			t.Fatal(err)
		}
		h.sess.suspendNotificationWake()
		h.sess.clientMutations.faults.BeforeEffectSnapshotRename = nil
		h.clock.Advance(jobNotificationRetryMaxDelay)
		select {
		case <-h.woken:
		case <-time.After(5 * time.Second):
			t.Fatal("storage recovery did not recheck execution without user input")
		}
		if got := h.sess.clientMutations.snapshot().ActiveTurnID; got != "" {
			t.Fatalf("recovered active turn = %q", got)
		}
	})
	a := &skillContinuationAdapter{}
	s := observationSession(t, a, false)
	wakes := make(chan struct{}, 8)
	s.SetNotifyFunc(func() { wakes <- struct{}{} })
	observationEnqueue(t, s, "release-a", "payload-a-723a")
	<-wakes
	a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
		observationRequest(t, req, []string{"payload-a-723a"}, []string{"payload-b-19fa"})
		observationEnqueue(t, s, "release-b", "payload-b-19fa")
		select {
		case <-wakes:
			t.Error("notification dispatched before the current executor released")
		default:
		}
		return llm.Response{Message: llm.Assistant("result-192a")}, nil
	}, func(req llm.Request) (llm.Response, error) {
		observationRequest(t, req, []string{"payload-b-19fa"}, nil)
		return llm.Response{Message: llm.Assistant("result-b-81fe")}, nil
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("release stranded the arrival after the last request")
	}
	assertNotificationPending(t, s, []string{"release-b"})
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, s, []string{})
	observationEnqueue(t, s, "release-b", "payload-b-19fa")
	select {
	case <-wakes:
		t.Fatal("already observed ID scheduled a redundant executor")
	default:
	}
	// The previous ProcessInputKind has returned, so this arrival is on the
	// idle side of the same release handoff, not merely after request build.
	a.steps = append(a.steps, func(req llm.Request) (llm.Response, error) {
		observationRequest(t, req, []string{"payload-c-7a19"}, nil)
		return llm.Response{Message: llm.Assistant("result-c-728e")}, nil
	})
	observationEnqueue(t, s, "release-c", "payload-c-7a19")
	select {
	case <-wakes:
	default:
		t.Fatal("arrival after executor release lost its wake")
	}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, s, []string{})
	if got := len(a.Requests()); got != 3 {
		t.Fatalf("requests across both release sides = %d, want 3", got)
	}
}

// notificationTestPending reads durable receiver membership, not wake state.
// Fixture identities and paths are immutable for the lifetime of these tests.
func notificationTestPending(t testing.TB, s *Session) map[string]struct{} {
	t.Helper()
	fold, err := readDelegateAttentionFold(transcriptPath(s.stateDir, s.id), s.id)
	if err != nil {
		t.Errorf("read pending notifications: %v", err)
	}
	ids := make(map[string]struct{})
	for _, id := range fold.pendingIDs() {
		ids[id] = struct{}{}
	}
	return ids
}

// These projection readers preserve historical controller-record assertions.
// They never wake or execute a receiver, and are not readiness inputs.
func notificationTestProjectedReceiver(c *delegateTreeController) (string, string, bool) {
	if c == nil {
		return "", "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var selected, attention string
	var turn uint64
	for id, state := range c.attention {
		if len(state.wakeIDs) == 0 || state.parked || !c.delegateAttentionWakeEligibleLocked(id) {
			continue
		}
		ready, _ := c.attentionWakeAncestorGateLocked(id)
		if !ready {
			continue
		}
		if selected == "" || state.driveTurn < turn || state.driveTurn == turn && id < selected {
			selected, turn, attention = id, state.driveTurn, ""
			for candidate := range state.wakeIDs {
				if attention == "" || candidate < attention {
					attention = candidate
				}
			}
		}
	}
	return selected, attention, selected != ""
}
func notificationTestTakeProjectedReceiver(c *delegateTreeController) (string, string, bool) {
	id, attention, ok := notificationTestProjectedReceiver(c)
	if ok {
		c.mu.Lock()
		c.attentionDriveSeq++
		c.attentionStateLocked(id).driveTurn = c.attentionDriveSeq
		c.holdAttentionRestoreLocked(id)
		c.mu.Unlock()
	}
	return id, attention, ok
}
func notificationTestProjectionRunnable(c *delegateTreeController) bool {
	if _, _, ok := notificationTestProjectedReceiver(c); ok {
		return true
	}
	return c != nil && len(c.permanentlyFencedDelegateAttention()) != 0
}
