package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// The only held operation is the attributed unlocked finalization recheck.
// The production callback merely hands off a signal and returns.
func TestFinalizationHandoffOwnedByRunner(t *testing.T) {
	t.Parallel()
	runFinalizationHandoffOwnedByRunner(t)
}

func runFinalizationHandoffOwnedByRunner(t *testing.T) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = repeatFinalResponse(1, "complete")
		root := restoreSupervisionRoot(t, fixture, nil)
		c := root.delegateController
		var armed atomic.Bool
		entered := make(chan struct{})
		completed := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		defer unblock()
		var admission <-chan struct{}
		root.cfg.testOnly.subagentBeforeGenerationReleased = func(_ *subagent, lease delegateLease) {
			c.mu.Lock()
			admission = c.live[lease.delegateID].finalizing.released
			c.mu.Unlock()
			armed.Store(true)
		}
		root.cfg.testOnly.notificationRecheckBeforeDispatch = func() {
			if armed.CompareAndSwap(true, false) {
				close(entered)
				<-release
				close(completed)
			}
		}
		wakes := make(chan struct{}, 1)
		root.SetNotifyFunc(func() {
			select {
			case wakes <- struct{}{}:
			default:
			}
		})
		sub := warmStableDelegateUnservedRoot(t, root, fixture)
		<-entered
		sub.mu.Lock()
		done := sub.done
		sub.mu.Unlock()
		sendersDone := make(chan struct{})
		go func() { root.sendersWG.Wait(); close(sendersDone) }()
		synctest.Wait()
		select {
		case <-admission:
		default:
			t.Error("finalization admission remains held during unlocked handoff")
		}
		c.mu.Lock()
		fenced := c.live[fixture.delegateID].finalizing != nil
		c.mu.Unlock()
		if fenced {
			t.Error("released finalization still installed")
		}
		select {
		case <-done:
			t.Error("actual runner completed while its finalization handoff remained held")
		default:
		}
		select {
		case <-sendersDone:
			t.Error("owning senders completed while finalization handoff remained held")
		default:
		}
		// External shutdown has entered, but is held at its existing seam until
		// the test releases the owned handoff. No Close timeout proves a join.
		closeBegun, closeMayProceed := make(chan struct{}), make(chan struct{})
		allowClose := sync.OnceFunc(func() { close(closeMayProceed) })
		t.Cleanup(allowClose)
		defer allowClose()
		root.cfg.testOnly.closeAfterDisposeSweepJoin = func() {
			close(closeBegun)
			<-closeMayProceed
		}
		eventsDrained := make(chan struct{})
		root.ConsumeEventsLossless(func(events.SessionEvent) {}, func() { close(eventsDrained) })
		closed := make(chan struct{})
		go func() { root.Close(); close(closed) }()
		<-closeBegun
		select {
		case <-sendersDone:
			t.Error("external shutdown observed sender completion before owned handoff returned")
		default:
		}
		unblock()
		<-completed
		<-done
		<-sendersDone
		allowClose()
		<-closed
		<-eventsDrained
	})
}

func TestFinalizationHandoffCreateReclamationOuterAdmission(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = repeatFinalResponse(2, "complete")
	root := restoreSupervisionRoot(t, fixture, nil)
	rc, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	c := root.delegateController
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	root.cfg.testOnly.subagentBeforeGenerationReleased = func(_ *subagent, lease delegateLease) {
		if lease.delegateID == fixture.delegateID {
			close(entered)
			<-release
		}
	}
	warmStableDelegateUnservedRoot(t, root, fixture)
	<-entered
	c.mu.Lock()
	c.maxRetainedTerminal = 1
	c.mu.Unlock()
	observed := make(chan int, 10)
	root.cfg.testOnly.notificationRecheckBeforeDispatch = func() {
		c.mu.Lock()
		cleared := c.live[fixture.delegateID].runtime == nil && c.live[fixture.delegateID].finalizing == nil
		c.mu.Unlock()
		if !cleared {
			return
		}
		rc.mu.Lock()
		active := 0
		for _, blocker := range rc.active {
			if blocker.SessionID == root.ID() && blocker.Category == "delegate" {
				active++
			}
		}
		rc.mu.Unlock()
		select {
		case observed <- active:
		default:
		}
	}
	result := root.createDelegate(context.Background(), delegateArgs{Task: "new delegate after reclamation"})
	unblock()
	root.sendersWG.Wait()
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case active := <-observed:
		if active != 0 {
			t.Fatalf("reclamation recheck retained %d root delegate retirement admission(s)", active)
		}
	default:
		t.Fatal("reclamation handoff did not fire")
	}
}

func TestFinalizationHandoffRecursiveWakeOuterAdmission(t *testing.T) {
	// Not parallel: the empty-fold barrier wraps the package-wide filesystem seam.
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = repeatFinalResponse(1, "complete")
	root := restoreSupervisionRoot(t, fixture, nil)
	rc, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	c := root.delegateController
	if _, err := c.openDelegateAttention(fixture.delegateID, "stale-empty-handoff"); err != nil {
		t.Fatal(err)
	}
	path := transcriptPath(root.stateDir, fixture.childID)
	if err := os.Chtimes(path, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	compute := readExistingDelegateAttentionFoldCompute
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var once sync.Once
	readExistingDelegateAttentionFoldCompute = func(readPath, sessionID string) (delegateAttentionFold, int64, error) {
		fold, consumed, readErr := compute(readPath, sessionID)
		if readPath == path && readErr == nil && len(fold.pendingIDs()) == 0 {
			once.Do(func() { close(entered); <-release })
		}
		return fold, consumed, readErr
	}
	done := make(chan error, 1)
	joined := false
	defer func() {
		unblock()
		if !joined {
			<-done
		}
		root.sendersWG.Wait()
		readExistingDelegateAttentionFoldCompute = compute
	}()
	go func() { done <- c.requestSessionWake(fixture.childID) }()
	<-entered
	// Remember this invocation's exact lease, not unrelated runner admissions.
	rc.mu.Lock()
	outerLeases := make(map[uint64]bool)
	for lease, blocker := range rc.active {
		if blocker.SessionID == root.ID() && blocker.Category == "delegate" {
			outerLeases[lease] = true
		}
	}
	rc.mu.Unlock()
	if len(outerLeases) != 1 {
		t.Fatalf("empty wake outer admissions=%d, want 1", len(outerLeases))
	}
	// Controlled old-finalization seam, as in the attachment carrier tests.
	// Real restoration must release it and carry true through the recursive pass.
	admission := make(chan struct{})
	root.delegateRestoreAfterCommit = func(id string) {
		c.mu.Lock()
		if c.live[id] == nil {
			c.live[id] = &delegateLiveState{}
		}
		c.live[id].finalizing = &delegateFinalization{runtime: &Session{}, released: admission}
		c.mu.Unlock()
	}
	observed := make(chan bool, 10)
	root.cfg.testOnly.notificationRecheckBeforeDispatch = func() {
		select {
		case <-admission:
		default:
			return
		}
		rc.mu.Lock()
		retained := false
		for lease := range outerLeases {
			_, active := rc.active[lease]
			retained = retained || active
		}
		rc.mu.Unlock()
		select {
		case observed <- retained:
		default:
		}
	}
	if err := c.enqueueNotification(fixture.childID, handoffNotification()); err != nil {
		t.Fatal(err)
	}
	unblock()
	err = <-done
	joined = true
	root.sendersWG.Wait()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case retained := <-observed:
		if retained {
			t.Fatal("recursive recheck retained the empty wake's outer retirement admission")
		}
	default:
		t.Fatal("recursive restoration did not produce a finalization handoff")
	}
	assertHandoffRaw(t, path, true)
}

// Direct fixture attachments have no enclosing manager/restore admission.
func recheckNotificationsForTest(c *delegateTreeController, recheck bool) {
	if recheck {
		c.recheckNotificationReceivers()
	}
}

func attachRuntimeWithHandoffForTest(c *delegateTreeController, lease delegateLease, runtime *Session) error {
	recheck, err := c.AttachRuntime(lease, runtime)
	if recheck {
		c.recheckNotificationReceivers()
	}
	return err
}

func restoreColdRuntimeWithHandoffForTest(s *Session, id string) (*Session, *subagent, error) {
	owner, sub, recheck, err := s.restoreColdDelegateAttentionRuntime(id)
	if recheck {
		s.delegateController.recheckNotificationReceivers()
	}
	return owner, sub, err
}

func abortOwedBootstrapWithHandoffForTest(s *Session, gate *owedBootstrapRestore, cause error) error {
	recheck, err := s.abortOwedDelegateBootstrap(gate, cause)
	if recheck {
		s.delegateController.recheckNotificationReceivers()
	}
	return err
}

func failCommittedStartWithHandoffForTest(runtime delegateRuntime, started delegateStartCommit, isolation delegateIsolation, prepared *preparedSubagentRun, attached bool, cause error, reason string) delegateResult {
	result, recheck := runtime.failCommittedStart(started, isolation, prepared, attached, cause, reason)
	if recheck {
		runtime.owner.delegateController.recheckNotificationReceivers()
	}
	return result
}

const handoffPayload = "payload-9360a37d-finalization"
const handoffAcceptedID = "handoff-accepted-3e924d"

type handoffProvider struct {
	*fakeAdapter
	requests chan llm.Request
	fail     bool
}

func (a *handoffProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if requestContainsText(req, handoffPayload) {
		a.requests <- req
		if a.fail {
			return llm.Response{}, &llm.ConfigurationError{Message: "scripted provider refusal"}
		}
		return finalResponse("complete"), nil
	}
	return a.fakeAdapter.Complete(ctx, req)
}

func handoffNotification() notification {
	return notification{ID: handoffAcceptedID, Message: llm.User(handoffPayload), Provenance: testProvenance("watch_handoff", "generation_handoff")}
}

func assertHandoffRaw(t *testing.T, path string, observed bool) {
	t.Helper()
	n := handoffNotification()
	assertNotificationRecords(t, path, n.ID, n.Message, 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		var raw struct {
			Turn struct {
				ID         string             `json:"attention_id"`
				Provenance *provenance.Causal `json:"notification_provenance"`
			} `json:"turn"`
		}
		if json.Unmarshal(line, &raw) == nil && raw.Turn.ID == n.ID {
			accepted++
			if !provenance.ContainsWatch(raw.Turn.Provenance, "watch_handoff", "generation_handoff") {
				t.Fatal("raw accepted record lost producer provenance")
			}
		}
	}
	if accepted != 1 {
		t.Fatalf("independent raw accepted records=%d, want 1", accepted)
	}
	count := 0
	for _, entry := range notificationRawEntries(t, path) {
		if entry.Turn.Kind == schema.TurnAssistant && slices.Contains(entry.Turn.ObservedNotificationIDs, n.ID) {
			count++
		}
	}
	if want := map[bool]int{false: 0, true: 1}[observed]; count != want {
		t.Fatalf("raw successful-response receipts=%d, want %d", count, want)
	}
	fold, err := readExistingDelegateAttentionFold(path, strings.TrimSuffix(filepath.Base(path), ".transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if pending := slices.Contains(fold.pendingIDs(), n.ID); pending == observed {
		t.Fatalf("same accepted ID pending=%v, observed=%v", pending, observed)
	}
}

func TestFinalizationHandoffAcceptedColdRecovery(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful_response", true: "failed_response"}[failed], func(t *testing.T) {
			fixture := newColdStableDelegateFixture(t, "")
			fixture.adapter.steps = repeatFinalResponse(1, "warm")
			provider := &handoffProvider{fakeAdapter: fixture.adapter, requests: make(chan llm.Request, 4), fail: failed}
			fixture.client.Register(provider)
			root := restoreSupervisionRoot(t, fixture, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			t.Cleanup(unblock)
			defer unblock()
			root.cfg.testOnly.subagentBeforeGenerationReleased = func(_ *subagent, lease delegateLease) {
				if lease.generation == 1 {
					close(entered)
					<-release
				}
			}
			sub := warmStableDelegateUnservedRoot(t, root, fixture)
			<-entered
			if err := root.delegateController.enqueueNotification(fixture.childID, handoffNotification()); err != nil {
				t.Fatal(err)
			}
			assertHandoffRaw(t, sub.sess.TranscriptPath(), false)
			select {
			case <-provider.requests:
				t.Fatal("receiver executed before exact finalization released")
			default:
			}
			unblock()
			req := <-provider.requests
			if !requestContainsText(req, handoffPayload) {
				t.Fatal("provider request lost accepted opaque body")
			}
			root.sendersWG.Wait()
			assertHandoffRaw(t, sub.sess.TranscriptPath(), !failed)
			if !failed {
				if generation := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID).Generation; generation != 2 {
					t.Fatalf("generation=%d, want warm plus one notification generation", generation)
				}
			}
		})
	}
	// The old process retains the real writer and finalization until process
	// death. No terminal Close/Stop parks the accepted ID before restoration.
	t.Run("process_death_then_autonomous_cold_restore", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		receiptPath := filepath.Join(t.TempDir(), "accepted.json")
		cmd := exec.Command(executable, "-test.run=^TestFinalizationHandoffCrashHelper$", "-test.timeout=60s")
		cmd.Env = append(os.Environ(), "EVENER_HANDOFF_CRASH_STATE="+fixture.stateDir, "EVENER_HANDOFF_ROOT="+fixture.meta.ID, "EVENER_HANDOFF_CHILD="+fixture.childID, "EVENER_HANDOFF_DELEGATE="+fixture.delegateID, "EVENER_HANDOFF_RECEIPT="+receiptPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("crash helper: %v\n%s", err, output)
		}
		data, err := os.ReadFile(receiptPath)
		if err != nil {
			t.Fatal(err)
		}
		var accepted notification
		if err := json.Unmarshal(data, &accepted); err != nil {
			t.Fatal(err)
		}
		if accepted.ID != handoffAcceptedID || accepted.Message.Text() != handoffPayload || !provenance.ContainsWatch(accepted.Provenance, "watch_handoff", "generation_handoff") {
			t.Fatalf("helper acceptance boundary lost immutable identity/body/provenance: %+v", accepted)
		}
		path := transcriptPath(fixture.stateDir, fixture.childID)
		assertHandoffRaw(t, path, false)
		provider := &handoffProvider{fakeAdapter: fixture.adapter, requests: make(chan llm.Request, 4)}
		fixture.client.Register(provider)
		root := restoreSupervisionRoot(t, fixture, nil)
		wakes := make(chan struct{}, 1)
		root.SetNotifyFunc(func() {
			select {
			case wakes <- struct{}{}:
			default:
			}
		})
		req := <-provider.requests
		if !requestContainsText(req, accepted.Message.Text()) {
			t.Fatal("autonomous cold restore request lost original accepted body")
		}
		root.sendersWG.Wait()
		assertHandoffRaw(t, path, true)
		if generation := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID).Generation; generation != 2 {
			t.Fatalf("restored generation=%d, want warm plus one receipt-bearing generation", generation)
		}
	})
}

func TestFinalizationHandoffCrashHelper(t *testing.T) {
	stateDir := os.Getenv("EVENER_HANDOFF_CRASH_STATE")
	if stateDir == "" {
		t.Skip("subprocess only")
	}
	meta, err := schema.LoadSessionMeta(stateDir, os.Getenv("EVENER_HANDOFF_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{name: "openai", steps: repeatFinalResponse(1, "warm")}
	client := llm.NewClient()
	client.Register(adapter)
	root, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{StateDir: stateDir, ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true, sandboxProber: bwrapCapableProber(meta.EnvInfo.WorkingDir)}})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	root.cfg.testOnly.subagentBeforeGenerationReleased = func(*subagent, delegateLease) {
		close(entered)
		select {} // Intentionally abandoned by process death, not lifecycle proof.
	}
	sendAndAcknowledge(t, root, os.Getenv("EVENER_HANDOFF_DELEGATE"), "warm")
	<-entered
	n := handoffNotification()
	if err := root.delegateController.enqueueNotification(os.Getenv("EVENER_HANDOFF_CHILD"), n); err != nil {
		t.Fatal(err)
	}
	assertHandoffRaw(t, transcriptPath(stateDir, os.Getenv("EVENER_HANDOFF_CHILD")), false)
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("EVENER_HANDOFF_RECEIPT"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestFinalizationHandoffCloseRefusalIdentity(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	err := c.requestSessionWake("unadmitted")
	var closing notificationWakeClosingError
	if !errors.As(err, &closing) || !errors.Is(err, errDelegateTargetBusy) || err.Error() != errDelegateTargetBusy.Error() {
		t.Fatalf("early close refusal lost private provenance or target_busy machine value: %T %v", err, err)
	}
	if c.recheckNotificationReceivers() {
		t.Fatal("closed controller attempted receiver traversal")
	}
}

func TestFinalizationHandoffAttachmentBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mode delegateRuntimeAttachMode
		fail bool
	}{
		{"idle", delegateRuntimeAttachIdle, false},
		{"started", delegateRuntimeAttachStarted, false},
		{"yielded_waiting", delegateRuntimeAttachWaiting, false},
		{"post_attachment_failure", delegateRuntimeAttachIdle, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newColdStableDelegateFixture(t, "")
			root := restoreSupervisionRoot(t, fixture, nil)
			c := root.delegateController
			started, _, err := c.idleDelegateRestoreCommit(fixture.delegateID)
			if tc.mode != delegateRuntimeAttachIdle {
				reservation, reserveErr := c.ReserveStart(rootDelegateActor(root.ID()), fixture.delegateID)
				if reserveErr != nil {
					t.Fatal(reserveErr)
				}
				started, err = c.CommitStart(reservation)
			}
			if err != nil {
				t.Fatal(err)
			}
			gate := &owedBootstrapRestore{held: true, holdDrives: true, pending: make(map[*Session]func()), done: make(map[*subagent]bool)}
			released := make(chan struct{})
			c.mu.Lock()
			if c.live[fixture.delegateID] == nil {
				c.live[fixture.delegateID] = &delegateLiveState{}
			}
			if tc.mode == delegateRuntimeAttachWaiting {
				c.live[fixture.delegateID].binding.yielded = true
			}
			c.live[fixture.delegateID].finalizing = &delegateFinalization{runtime: root, delegateID: fixture.delegateID, generation: started.lease.generation, released: released}
			c.mu.Unlock()
			if tc.fail {
				root.delegateRestoreBeforeSideEffects = func(child *Session) {
					child.mu.Lock()
					child.notifyFunc = nil // Force gate.add to fail after successful attachment.
					child.mu.Unlock()
				}
			}
			var calls atomic.Int32
			root.cfg.testOnly.notificationRecheckBeforeDispatch = func() { calls.Add(1) }
			sub, recheck, err := root.reconstructDelegateAttentionRuntime(root, started, tc.mode, gate)
			if (err != nil) != tc.fail || !recheck {
				t.Fatalf("reconstruction returned sub=%p recheck=%v err=%v", sub, recheck, err)
			}
			select {
			case <-released:
			default:
				t.Fatal("attachment did not release the old finalization")
			}
			if calls.Load() != 0 {
				t.Fatal("attachment dispatched before its outer owner released the gate")
			}
			gate.open()
			// This callback positively re-enters each lock-owning component and
			// observes completed publication, not just a successful inner unlock.
			seen := make(chan bool, 1)
			root.cfg.testOnly.notificationRecheckBeforeDispatch = func() {
				tracked := root.subagents.get(fixture.childID)
				root.subagents.mu.Lock()
				published := len(root.subagents.reconstructing) == 0
				root.subagents.mu.Unlock()
				c.mu.Lock()
				bound := tracked != nil && c.live[fixture.delegateID].runtime == tracked.sess
				c.mu.Unlock()
				if tracked != nil {
					tracked.mu.Lock()
					bound = bound && !tracked.driving
					tracked.mu.Unlock()
				}
				gate.mu.Lock()
				open := !gate.held
				gate.mu.Unlock()
				seen <- published && bound && open
			}
			c.recheckNotificationReceivers()
			if !<-seen {
				t.Fatal("handoff preceded manager binding/publication, guard release or gate opening")
			}
			root.cfg.testOnly.notificationRecheckBeforeDispatch = nil
			if tc.mode == delegateRuntimeAttachStarted {
				plans, _, err := c.FailCommittedStart(started.lease, delegatePermanentStartFailure(errors.New("unlaunched boundary fixture"), "fixture"), "fixture", nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := root.executeDelegateMutationPlans(plans); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("existing_runtime_collision", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		root := restoreSupervisionRoot(t, fixture, nil)
		c := root.delegateController
		started, _, err := c.idleDelegateRestoreCommit(fixture.delegateID)
		if err != nil {
			t.Fatal(err)
		}
		existing, _, err := root.reconstructDelegateAttentionRuntime(root, started, delegateRuntimeAttachIdle)
		if err != nil {
			t.Fatal(err)
		}
		released := make(chan struct{})
		c.mu.Lock()
		c.live[fixture.delegateID].finalizing = &delegateFinalization{runtime: root, released: released}
		c.mu.Unlock()
		installation, err := c.beginIdleRuntimeInstallation(fixture.delegateID)
		if err != nil {
			t.Fatal(err)
		}
		var recheck bool
		selected, inserted, err := root.subagents.admitReconstructed(&subagent{id: existing.id, sess: root}, func(selected *subagent) error {
			var err error
			recheck, err = installation.attach(selected.sess)
			return err
		})
		installation.release()
		if err != nil || inserted || selected != existing || !recheck {
			t.Fatalf("collision selected=%p existing=%p inserted=%v recheck=%v err=%v", selected, existing, inserted, recheck, err)
		}
		c.recheckNotificationReceivers()
		<-released
	})
}

func TestFinalizationHandoffReleaseCarriers(t *testing.T) {
	t.Parallel()
	t.Run("restore_reclamation_failure", func(t *testing.T) {
		runFinalizationHandoffLifecycleSubprocess(t, "restore_reclamation_failure")
	})
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed_start", true: "stop_reconciliation"}[stop], func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 2, 2)
			seedDelegateControllerIdle(t, c, "carrier", "")
			reservation, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "carrier")
			if err != nil {
				t.Fatal(err)
			}
			started, err := c.CommitStart(reservation)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &Session{}
			if err := attachRuntimeWithHandoffForTest(c, started.lease, runtime); err != nil {
				t.Fatal(err)
			}
			released := make(chan struct{})
			c.mu.Lock()
			c.live["carrier"].finalizing = &delegateFinalization{runtime: &Session{}, released: released}
			c.mu.Unlock()
			var plans delegateMutationPlans
			if stop {
				// A failed durable compensation leaves the real binding recovery-required.
				c.mu.Lock()
				c.live["carrier"].recoveryRequired = true
				c.mu.Unlock()
				_, cancel, _, stopErr := c.StopSubtree(rootDelegateActor(c.rootSessionID), "carrier")
				if stopErr != nil {
					t.Fatal(stopErr)
				}
				executeDelegateCancelPlan(cancel)
				plans, err = c.Reconcile(emptyDelegateReconcileEvidence(c))
			} else {
				plans, _, err = c.FailCommittedStart(started.lease, delegatePermanentStartFailure(errors.New("carrier failure"), "fixture"), "fixture", nil)
			}
			if plans.retirementRelease != nil {
				plans.retirementRelease()
			}
			if err != nil || !plans.recheckNotifications {
				t.Fatalf("generation release carrier recheck=%v err=%v", plans.recheckNotifications, err)
			}
			<-released
		})
	}
	t.Run("commit_replacement", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 2, 2)
		seedDelegateControllerIdle(t, c, "carrier", "")
		reservation, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "carrier")
		if err != nil {
			t.Fatal(err)
		}
		released := make(chan struct{})
		c.mu.Lock()
		record, recordErr := c.reservationRecordLocked(reservation)
		if recordErr != nil {
			c.mu.Unlock()
			t.Fatal(recordErr)
		}
		record.runtime = &Session{}
		c.live["carrier"] = &delegateLiveState{finalizing: &delegateFinalization{runtime: &Session{}, released: released}}
		c.mu.Unlock()
		started, err := c.CommitStart(reservation)
		if err != nil || !started.recheckNotifications {
			t.Fatalf("commit replacement recheck=%v err=%v", started.recheckNotifications, err)
		}
		<-released
	})
	for _, retirement := range []bool{false, true} {
		t.Run(map[bool]string{false: "reclamation", true: "closed_retirement"}[retirement], func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 4, 2)
			c.maxRetainedTerminal = 1
			runtime := seedDelegateReclaimRuntime(t, c, "carrier", "", time.Unix(10, 0).UTC(), false, false)
			claim, err := c.ClaimRuntimeReclamation(1)
			if err != nil || claim == nil {
				t.Fatalf("claim=%v err=%v", claim, err)
			}
			released := make(chan struct{})
			c.mu.Lock()
			c.live["carrier"].finalizing = &delegateFinalization{runtime: runtime, released: released}
			c.mu.Unlock()
			before, err := c.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if retirement {
				c.mu.Lock()
				c.releaseRuntimeReclamationLocked(claim)
				c.retirementClaim = &RetirementClaim{tree: c}
				c.rootRuntime = &Session{closing: true}
				c.mu.Unlock()
				err = c.releaseRetiredRuntimes(map[string]*Session{"carrier": runtime})
			} else {
				var reclaimed bool
				reclaimed, err = c.CompleteRuntimeReclamation(claim, map[string]*Session{"carrier": runtime})
				recheckNotificationsForTest(c, reclaimed)
			}
			if err != nil {
				t.Fatal(err)
			}
			<-released
			c.mu.Lock()
			cleared := c.live["carrier"].runtime == nil && len(c.reclamations) == 0
			c.mu.Unlock()
			if !cleared {
				t.Fatal("release handoff retained process pointer or reclamation claim")
			}
			after, err := c.store.Load()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("process-only removal rewrote canonical journal: %v", err)
			}
		})
	}
}

func runFinalizationHandoffRestoreReclamationFailure(t *testing.T) {
	t.Helper()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	eventsDrained := make(chan struct{})
	root.ConsumeEventsLossless(func(events.SessionEvent) {}, func() { close(eventsDrained) })
	t.Cleanup(func() {
		root.Close()
		root.sendersWG.Wait()
		<-eventsDrained
	})
	c := root.delegateController
	c.maxRetainedTerminal = 1
	resident := seedDelegateReclaimRuntime(t, c, "dlg_old", "", time.Unix(5, 0).UTC(), false, false)
	// Rechecking the reclaimed old delegate must find valid, empty attention
	// evidence. Only the distinct restore target's metadata is intentionally absent.
	writer, writeErr := transcript.NewWriter(transcriptPath(root.stateDir, resident.ID()), transcript.Header{
		SessionID:       resident.ID(),
		ParentSessionID: root.ID(),
	})
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	c.mu.Lock()
	c.live["dlg_old"].finalizing = &delegateFinalization{runtime: resident, released: released}
	target := delegateControllerCreatedEvent("dlg_restore", "")
	target.Created.Descriptor.OwnerSessionID = root.ID()
	_, err := c.appendLocked(target)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := c.ReserveStart(rootDelegateActor(root.ID()), "dlg_restore")
	if err != nil {
		t.Fatal(err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatal(err)
	}
	defer c.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	rc, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	outerRelease, err := root.beginRetirementMutation("delegate")
	if err != nil {
		t.Fatal(err)
	}
	defer outerRelease()
	root.cfg.testOnly.delegateRuntimeReclaimClose = func(*Session) {}
	rechecks := 0
	root.cfg.testOnly.notificationRecheckBeforeDispatch = func() { rechecks++ }
	_, _, finish, restoreErr := (delegateRuntime{owner: root}).restoreIdleForSend(started)
	recheck := finish(nil, restoreErr)
	if !errors.Is(restoreErr, os.ErrNotExist) || !recheck {
		t.Fatalf("failed restore carrier recheck=%v err=%v", recheck, restoreErr)
	}
	var missingMeta *os.PathError
	wantMeta := filepath.Join(root.stateDir, sessionsSubdir, started.descriptor.ChildSessionID+".meta.json")
	if !errors.As(restoreErr, &missingMeta) || missingMeta.Path != wantMeta {
		t.Fatalf("restore failure did not concern the target metadata %q: %v", wantMeta, restoreErr)
	}
	select {
	case <-released:
	default:
		t.Fatal("reclamation retained the finalization admission")
	}
	if rechecks != 0 {
		t.Fatalf("restoration consumed %d rechecks before its outer caller", rechecks)
	}
	outerRelease()
	recheckNotificationsForTest(c, recheck)
	if rechecks != 1 {
		t.Fatalf("outer consumer dispatched %d rechecks, want 1", rechecks)
	}
}

func TestFinalizationHandoffLifecycle(t *testing.T) {
	t.Parallel()
	runFinalizationHandoffLifecycleSubprocess(t, "1")
}

func runFinalizationHandoffLifecycleSubprocess(t *testing.T, scenario string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestFinalizationHandoffLifecycleHelper$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "EVENER_HANDOFF_LIFECYCLE="+scenario)
	output, err := cmd.CombinedOutput() // Drains all stderr through normal process exit.
	if err != nil {
		t.Fatalf("lifecycle helper: %v\n%s", err, output)
	}
	for line := range bytes.SplitSeq(output, []byte{'\n'}) {
		var record struct {
			Level     string          `json:"level"`
			SessionID string          `json:"session_id"`
			Error     json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue // Go test runner framing, not a structured log record.
		}
		if record.Level == "WARN" || record.Level == "ERROR" {
			t.Errorf("joined lifecycle emitted diagnostic: level=%s session=%s error=%s", record.Level, record.SessionID, record.Error)
		}
	}
}

func TestFinalizationHandoffDiagnostics(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"storage", "target", "storage_then_close"} {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.Command(executable, "-test.run=^TestFinalizationHandoffDiagnosticsHelper$", "-test.timeout=60s")
			cmd.Env = append(os.Environ(), "EVENER_HANDOFF_DIAGNOSTIC="+scenario)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("diagnostic helper: %v\n%s", err, output)
			}
			var expected struct {
				SessionID string `json:"expected_session_id"`
				Error     string `json:"expected_error"`
			}
			for line := range bytes.SplitSeq(output, []byte{'\n'}) {
				var receipt = expected
				if json.Unmarshal(line, &receipt) == nil && receipt.SessionID != "" {
					expected = receipt
				}
			}
			warnings := 0
			for line := range bytes.SplitSeq(output, []byte{'\n'}) {
				var record struct {
					Level     string `json:"level"`
					SessionID string `json:"session_id"`
					Error     string `json:"error"`
				}
				if json.Unmarshal(line, &record) == nil && record.Level == "WARN" && record.SessionID == expected.SessionID && record.Error == expected.Error && record.Error != "" {
					warnings++
				}
			}
			if warnings == 0 {
				t.Fatal("real receiver error was suppressed by traversal or later shutdown")
			}
		})
	}
}

func TestFinalizationHandoffDiagnosticsHelper(t *testing.T) {
	scenario := os.Getenv("EVENER_HANDOFF_DIAGNOSTIC")
	if scenario == "" {
		t.Skip("subprocess only")
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = repeatFinalResponse(1, "warm healthy sibling")
	root := restoreSupervisionRoot(t, fixture, agenttest.NewFakeClock())
	c := root.delegateController
	healthy := root.createDelegate(context.Background(), delegateArgs{Task: "healthy diagnostic sibling"})
	if healthy.Err != nil {
		t.Fatal(healthy.Err)
	}
	root.sendersWG.Wait()
	sub := root.subagents.get(healthy.ChildSessionID)
	sub.mu.Lock()
	sub.driving = true
	sub.mu.Unlock()
	c.mu.Lock()
	c.holdAttentionRestoreLocked(fixture.delegateID)
	c.attentionStateLocked(healthy.DelegateID).driveTurn = 2
	c.mu.Unlock()
	if err := c.enqueueNotification(fixture.childID, notification{ID: "diagnostic-broken", Message: llm.User("opaque-broken-a872")}); err != nil {
		t.Fatal(err)
	}
	if err := c.enqueueNotification(healthy.ChildSessionID, handoffNotification()); err != nil {
		t.Fatal(err)
	}
	c.releaseAttentionRestoreHold(fixture.delegateID)
	releaseDriveGuard(sub)
	provider := &handoffProvider{fakeAdapter: fixture.adapter, requests: make(chan llm.Request, 4)}
	fixture.client.Register(provider)
	var once sync.Once
	c.mu.Lock()
	originalRef := c.durable[fixture.delegateID].Descriptor.TranscriptRef
	originalChild := c.durable[fixture.delegateID].Descriptor.ChildSessionID
	c.mu.Unlock()
	restoreDescriptor := func() {
		c.mu.Lock()
		c.durable[fixture.delegateID].Descriptor.TranscriptRef = originalRef
		c.durable[fixture.delegateID].Descriptor.ChildSessionID = originalChild
		c.mu.Unlock()
	}
	defer restoreDescriptor()
	var captured error
	root.cfg.testOnly.afterSessionWakeRecoveryScheduled = func(_ string, err error) { captured = err }
	root.cfg.testOnly.notificationRecheckBeforeDispatch = func() {
		once.Do(func() {
			c.mu.Lock()
			if scenario == "target" {
				c.durable[fixture.delegateID].Descriptor.ChildSessionID = "different-target"
			} else {
				c.durable[fixture.delegateID].Descriptor.TranscriptRef = encodeRef("", "missing-transcript")
			}
			c.mu.Unlock()
		})
	}
	closed := make(chan struct{})
	if scenario == "storage_then_close" {
		closeRequest, closeBegun, closeMayProceed := make(chan struct{}), make(chan struct{}), make(chan struct{})
		allowClose := sync.OnceFunc(func() { close(closeMayProceed) })
		defer allowClose()
		t.Cleanup(allowClose)
		root.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun); <-closeMayProceed }
		go func() { <-closeRequest; root.Close(); close(closed) }()
		root.cfg.testOnly.afterSessionWakeRecoveryScheduled = func(_ string, err error) {
			captured = err
			close(closeRequest) // A test seam, never the production wake callback.
			<-closeBegun
		}
		c.recheckNotificationReceivers()
		var earlyClose notificationWakeClosingError
		if captured == nil || errors.As(captured, &earlyClose) {
			t.Fatalf("produced storage error was reclassified: %T %v", captured, captured)
		}
		select {
		case <-provider.requests:
			t.Fatal("traversal continued to the healthy sibling after external Close won")
		default:
		}
		restoreDescriptor()
		allowClose()
		<-closed
	} else {
		c.recheckNotificationReceivers()
		<-provider.requests
		root.sendersWG.Wait()
		assertHandoffRaw(t, sub.sess.TranscriptPath(), true)
		restoreDescriptor()
		root.Close()
	}
	if scenario == "target" {
		captured = errDelegateDeliveryReceiverUnavailable
	}
	if captured == nil {
		t.Fatal("expected receiver failure was not produced")
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"expected_session_id": fixture.childID, "expected_error": captured.Error()}); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizationHandoffLifecycleHelper(t *testing.T) {
	if os.Getenv("EVENER_HANDOFF_LIFECYCLE") == "" {
		t.Skip("subprocess only")
	}
	// Not parallel: process-global logger, deliberately no default/log bridge.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if os.Getenv("EVENER_HANDOFF_LIFECYCLE") == "restore_reclamation_failure" {
		runFinalizationHandoffRestoreReclamationFailure(t)
		return
	}
	runFinalizationHandoffOwnedByRunner(t)
	fixture := newColdStableDelegateFixture(t, "") // Includes the empty cold child.
	root := restoreSupervisionRoot(t, fixture, nil)
	eventsDrained := make(chan struct{})
	root.ConsumeEventsLossless(func(events.SessionEvent) {}, func() { close(eventsDrained) })
	wake := make(chan struct{}, 1)
	callbackReturned := make(chan struct{})
	root.SetNotifyFunc(func() {
		wake <- struct{}{}
		close(callbackReturned)
	})
	closed := make(chan struct{})
	go func() {
		<-wake
		<-callbackReturned
		root.Close()
		close(closed)
	}()
	if err := root.enqueueNotification(notification{ID: "external-close", Message: llm.User("payload-close-08b1")}); err != nil {
		t.Fatal(err)
	}
	<-closed
	root.sendersWG.Wait()
	<-eventsDrained
	// The old root lifetime is closed, so even a cold empty child is not restored.
	if root.delegateController.recheckNotificationReceivers() {
		t.Fatal("closed root traversed its empty cold receiver")
	}
}
