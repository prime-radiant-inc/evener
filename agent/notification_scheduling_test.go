package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
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
					// TRIPWIRE: the scripted receiver sends the actual request, only a hang reaches this guard.
					case <-time.After(30 * time.Second):
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
		testNotificationCompetition(t, false, 1)
	})
	t.Run("failed receiver does not block healthy sibling", func(t *testing.T) {
		testNotificationCompetition(t, true, 1)
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
	for _, released := range []bool{false, true} {
		name := "concurrent publication before release lock"
		if released {
			name = "concurrent publication after release lock before dispatch"
		}
		t.Run(name, func(t *testing.T) {
			a := &skillContinuationAdapter{}
			s := observationSession(t, a, false)
			wakes := make(chan struct{}, 8)
			s.SetNotifyFunc(func() { wakes <- struct{}{} })
			observationEnqueue(t, s, "boundary-a", "boundary-first-713a")
			<-wakes
			boundary, resume := make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			unblock := func() { resumeOnce.Do(func() { close(resume) }) }
			t.Cleanup(unblock)
			updateSessionTestConfig(s, func(cfg *testConfig) {
				cfg.notificationExecutionRelease = func(idle bool) {
					if idle == released {
						close(boundary)
						<-resume
					}
				}
			})
			a.steps = []func(llm.Request) (llm.Response, error){
				func(req llm.Request) (llm.Response, error) {
					observationRequest(t, req, []string{"boundary-first-713a"}, []string{"boundary-next-82ac"})
					return llm.Response{Message: llm.Assistant("boundary-first-response-813f")}, nil
				},
				func(req llm.Request) (llm.Response, error) {
					observationRequest(t, req, []string{"boundary-next-82ac"}, nil)
					return llm.Response{Message: llm.Assistant("boundary-next-response-719e")}, nil
				},
			}
			done := make(chan error, 1)
			joined := false
			t.Cleanup(func() {
				unblock()
				if !joined {
					if err := <-done; err != nil {
						t.Error(err)
					}
				}
				updateSessionTestConfig(s, func(cfg *testConfig) { cfg.notificationExecutionRelease = nil })
			})
			go func() {
				_, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
				done <- err
			}()
			waitForTestSignal(t, boundary, "executor release lock boundary")
			// The old input is blocked at release while another goroutine's append
			// publishes through the real common readiness path.
			observationEnqueue(t, s, "boundary-b", "boundary-next-82ac")
			assertNotificationPending(t, s, []string{"boundary-b"})
			if !released && len(wakes) != 0 {
				t.Fatal("publication bypassed the still-running executor")
			}
			if released && len(wakes) != 1 {
				t.Fatal("idle publication did not claim the handoff wake")
			}
			unblock()
			err := <-done
			joined = true
			if err != nil {
				t.Fatal(err)
			}
			if len(wakes) != 1 {
				t.Fatalf("publication and release produced %d wakes, want one", len(wakes))
			}
			<-wakes
			updateSessionTestConfig(s, func(cfg *testConfig) { cfg.notificationExecutionRelease = nil })
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			assertNotificationPending(t, s, []string{})
			if got := len(a.Requests()); got != 2 || len(wakes) != 0 {
				t.Fatalf("provider requests=%d wakes=%d, want two requests and no redundant wake", got, len(wakes))
			}
		})
	}
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
		t.Cleanup(func() {
			h.clock.Drain()
			h.sess.clientMutations.faults.BeforeEffectSnapshotRename = nil
			h.sess.Close()
		})
		baselineTimers := h.clock.BlockedCount()
		h.sess.releaseRunningTurnID(turnID)
		<-writes
		for range runningTurnReleaseRetryLimit {
			h.clock.BlockUntil(baselineTimers + 1)
			h.clock.Advance(jobNotificationRetryMaxDelay)
			<-writes
		}
		h.clock.BlockUntil(baselineTimers + 1)
		h.clock.Drain()
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
		waitForTestSignal(t, h.woken, "storage recovery recheck without user input")
		h.clock.Drain()
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

// Storage remains broken after every fast release retry. The only stimulus
// after repair is the already-armed recovery timer, not another user message.
func TestNotificationReleaseStorageRecovery(t *testing.T) {
	t.Parallel()
	a := newMessagingAdapter()
	clock := agenttest.NewFakeClock()
	fifoA, releaseA := waitProducerFIFO(t)
	defer releaseA()
	fifoB, releaseB := waitProducerFIFO(t)
	defer releaseB()
	a.script("root", func(llm.Request) llm.Response { return incidentShellResponse("release-shell-a", fifoA) },
		func(llm.Request) llm.Response { return incidentShellResponse("release-shell-b", fifoB) },
		func(llm.Request) llm.Response { return finalResponse("release-initial-11") })
	root := newMessagingRoot(t, a, func(cfg *SessionConfig) { cfg.clock = clock })
	root.ConsumeEventsLossless(func(events.SessionEvent) {}, func() {})
	wake, hint := newDrainWake()
	root.SetNotifyFunc(hint)
	if _, err := root.ProcessInput(context.Background(), "release-start-11", nil); err != nil {
		t.Fatal(err)
	}
	started := producerSourceEvents(t, root.jobManager, jobstore.EventJobStarted)
	if len(started) != 2 {
		t.Fatalf("shell sources=%d", len(started))
	}
	shellDone := make([]<-chan struct{}, len(started))
	root.jobManager.mu.Lock()
	for i, source := range started {
		if run := root.jobManager.running[source.JobID]; run != nil {
			shellDone[i] = run.done
		}
	}
	root.jobManager.mu.Unlock()
	t.Cleanup(func() {
		releaseA()
		releaseB()
		for _, done := range shellDone {
			if done != nil {
				awaitSignal(t, done, "storage recovery shell cleanup finalizer")
			}
		}
		clock.Drain()
		root.Close()
	})
	for i, done := range shellDone {
		if done == nil {
			t.Fatalf("held shell %s has no finalizer", started[i].JobID)
		}
	}
	releaseA()
	waitForTestSignal(t, shellDone[0], "first shell terminal and custody finalizer")
	if got := producerRecordCount(t, root); got != 1 {
		t.Fatalf("first shell custody=%d, want 1", got)
	}
	first := producerSourceEvents(t, root.jobManager, jobstore.EventJobFinished)
	firstAck := producerSourceEvents(t, root.jobManager, jobstore.EventJobNotificationDelivered)
	if len(first) != 1 || len(firstAck) != 1 || first[0].JobID != started[0].JobID || firstAck[0].JobID != first[0].JobID || firstAck[0].TerminalGen != first[0].TerminalGen {
		t.Fatalf("first shell source=%+v acknowledgment=%+v started=%+v", first, firstAck, started[0])
	}
	incidentPendingFacts(t, root.stateDir, root.ID(), stableShellAttentionID(first[0].JobID, first[0].TerminalGen))
	<-wake
	path := clientMutationFilePath(root.stateDir, root.ID())
	backup := path + ".release-test-backup"
	blocked := false
	repair := func() {
		if !blocked {
			return
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Error(err)
			return
		}
		if err := os.Rename(backup, path); err != nil {
			t.Error(err)
			return
		}
		blocked = false
	}
	t.Cleanup(repair)
	a.script("root", func(req llm.Request) llm.Response {
		if err := incidentJobPayload(req, started[0].JobID, "completed", "exit_zero"); err != nil {
			t.Error(err)
		}
		for _, frame := range incidentFrames(req) {
			if frame.attrs["job_id"] == started[1].JobID {
				t.Error("first snapshot contains not-yet-produced B")
			}
		}
		releaseB()
		awaitSignal(t, shellDone[1], "late shell terminal and custody finalizer")
		fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.ID()), root.ID())
		if err != nil || len(fold.order) != 2 {
			t.Errorf("late shell custody=%v err=%v, want both shells", fold.order, err)
		}
		if err := os.Rename(path, backup); err != nil {
			t.Error(err)
			return finalResponse("filesystem-fixture-failed")
		}
		blocked = true
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Error(err)
			return finalResponse("filesystem-fixture-failed")
		}
		return finalResponse("release-first-observed-11")
	}, func(req llm.Request) llm.Response {
		for _, source := range started {
			if err := incidentJobPayload(req, source.JobID, "completed", "exit_zero"); err != nil {
				t.Error(err)
			}
		}
		return finalResponse("release-second-observed-11")
	})
	baseline := clock.BlockedCount()
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	finished := producerSourceEvents(t, root.jobManager, jobstore.EventJobFinished)
	acks := producerSourceEvents(t, root.jobManager, jobstore.EventJobNotificationDelivered)
	if len(finished) != 2 || len(acks) != 2 {
		t.Fatalf("shell terminal sources=%+v acknowledgments=%+v", finished, acks)
	}
	for i, source := range finished {
		if source.JobID != started[i].JobID || acks[i].JobID != source.JobID || acks[i].TerminalGen != source.TerminalGen {
			t.Fatalf("shell source=%+v acknowledgment=%+v started=%+v", source, acks[i], started[i])
		}
	}
	incidentPendingFacts(t, root.stateDir, root.ID(), stableShellAttentionID(finished[1].JobID, finished[1].TerminalGen))
	for range runningTurnReleaseRetryLimit {
		clock.BlockUntil(baseline + 1)
		clock.Advance(jobNotificationRetryMaxDelay)
	}
	clock.BlockUntil(baseline + 1)
	clock.Drain()
	root.mu.Lock()
	retained := root.notificationSchedule.releaseTurnID
	root.mu.Unlock()
	if retained == "" || root.clientMutations.snapshot().ActiveTurnID != retained {
		t.Fatal("exhausted retries lost exact release obligation")
	}
	if len(a.requestsFor("root")) != 4 {
		t.Fatal("storage failure ran another model request")
	}
	// The lifetime channel also carries resource rescans. Discard the late
	// shell's earlier hint before awaiting the paced recovery's new hint.
	select {
	case <-wake:
	default:
	}
	repair()
	clock.Advance(jobNotificationRetryMaxDelay)
	waitForTestSignal(t, wake, "storage repair retained-work wake")
	clock.Drain()
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if len(a.requestsFor("root")) != 5 || root.clientMutations.snapshot().ActiveTurnID != "" {
		t.Fatalf("repaired storage requests=%d active=%q retained=%q", len(a.requestsFor("root")), root.clientMutations.snapshot().ActiveTurnID, retained)
	}
	finished = producerSourceEvents(t, root.jobManager, jobstore.EventJobFinished)
	if len(finished) != 2 {
		t.Fatalf("terminal sources=%+v", finished)
	}
	ids := map[string]string{}
	for _, source := range finished {
		id := "shell:" + source.JobID + ":" + source.TerminalGen
		ids[source.JobID] = id
		incidentNotificationFacts(t, root.stateDir, root.ID(), id)
	}
	idA, idB := ids[started[0].JobID], ids[started[1].JobID]
	incidentCoverage(t, root.stateDir, root.ID(), map[string][]string{"release-first-observed-11": {idA}, "release-second-observed-11": {idA, idB}})
}

func TestNotificationExecutionReleaseOnUnwind(t *testing.T) {
	for _, cause := range []string{"error", "cancellation", "panic"} {
		t.Run(cause, func(t *testing.T) {
			a := &skillContinuationAdapter{}
			s := observationSession(t, a, false)
			wakes := make(chan struct{}, 8)
			s.SetNotifyFunc(func() { wakes <- struct{}{} })
			observationEnqueue(t, s, "unwind-pending", "unwind-body-29ae")
			<-wakes
			failure := errors.New("controlled pre-incorporation failure")
			if cause == "cancellation" {
				failure = context.Canceled
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			updateSessionTestConfig(s, func(cfg *testConfig) {
				cfg.failTurnBeforeRecording = func() error {
					if !s.notificationExecutionState().running {
						t.Error("input did not publish its running lifetime")
					}
					if cause == "panic" {
						panic(failure)
					}
					if cause == "cancellation" {
						cancel()
						return ctx.Err()
					}
					return failure
				}
			})
			var recovered any
			var err error
			func() {
				defer func() { recovered = recover() }()
				_, err = s.ProcessInputKind(ctx, "", nil, EntryNotification)
			}()
			if cause == "panic" {
				if recoveredErr, ok := recovered.(error); !ok || !errors.Is(recoveredErr, failure) {
					t.Fatalf("recovered=%v, want controlled panic", recovered)
				}
			} else if !errors.Is(err, failure) || recovered != nil {
				t.Fatalf("error=%v panic=%v, want controlled error", err, recovered)
			}
			if s.notificationExecutionState().running {
				t.Fatal("input unwind retained common execution suppression")
			}
			assertNotificationPending(t, s, []string{"unwind-pending"})
			if got := len(a.Requests()); got != 0 || len(wakes) != 1 {
				t.Fatalf("unwind provider requests=%d wakes=%d, want no request and one retained-work hint", got, len(wakes))
			}
		})
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

// This passive projection reader preserves controller-record assertions.
// It neither selects nor mutates receivers, and is not a readiness input.
func (c *delegateTreeController) hasPendingDelegateAttention() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for delegateID, state := range c.attention {
		if state.parked {
			continue
		}
		aggregate := c.durable[delegateID]
		if len(state.wakeIDs) != 0 && aggregate != nil && aggregate.Resumable && aggregate.PendingStopSeq == 0 && aggregate.Phase != delegatestore.PhaseClosed {
			return true
		}
	}
	return false
}
