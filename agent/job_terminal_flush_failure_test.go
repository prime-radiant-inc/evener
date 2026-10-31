package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

// Fault probes are serial: captureReceiverErrorLogs validates structured causes
// after session cleanup and temporarily replaces the process-wide default logger.
// Partial-source failure: a successful half must not wait for the failed half.
func TestTerminalFlushPartialFailure(t *testing.T) {
	for _, fault := range []string{"terminal pending append", "watch source append"} {
		t.Run(fault, func(t *testing.T) {
			sentinel := errors.New("terminal-flush-injected-refusal")
			captureReceiverErrorLogs(t, sentinel, func() error { return sentinel })
			s, a := newTerminalFlushSession(t, true)
			jm := s.jobManager
			rec, err := jm.createShell(createShellOpts{Command: "not executed, synchronous finalizer"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := jobWatchTool(s, map[string]any{"operation": "create", "source": rec.JobID, "output_match": "partial-never-317"}, jobToolResultDefaultMaxChar); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				a.steps = append(a.steps, func(llm.Request) llm.Response { return finalResponse("partial-observed-927") })
			}
			var wakes atomic.Int32
			s.SetNotifyFunc(func() { wakes.Add(1) })
			original := jm.appendEvent
			blockedKind := jobstore.EventJobNotificationPending
			if fault == "watch source append" {
				blockedKind = jobstore.EventWatchSendPending
			}
			jm.appendEvent = func(e jobstore.Event) error {
				if e.Kind == blockedKind {
					return sentinel
				}
				return original(e)
			}
			defer func() { jm.appendEvent = original }()
			err = jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil)
			if fault == "terminal pending append" && !errors.Is(err, sentinel) {
				t.Fatalf("finalize=%v want source refusal", err)
			}
			if fault == "watch source append" && err != nil {
				t.Fatalf("healthy shell failed with watch source: %v", err)
			}
			jm.mu.Lock()
			run := jm.running[rec.JobID]
			jm.mu.Unlock()
			if fault == "terminal pending append" && run == nil {
				t.Fatal("terminal retry lost its owner")
			}
			s.pendingJobNotifsMu.Lock()
			holds := s.notifyWakeHolds
			s.pendingJobNotifsMu.Unlock()
			if holds != 0 {
				t.Fatalf("failed publication leaked hold=%d", holds)
			}
			if wakes.Load() == 0 {
				t.Fatal("healthy obligation was left without a wake")
			}
			ids, err := s.pendingNotificationIDs()
			if err != nil || len(ids) != 1 {
				t.Fatalf("first healthy custody=%v err=%v, want exactly one", ids, err)
			}
			firstID := ids[0]
			if (fault == "terminal pending append") != strings.HasPrefix(firstID, "watch:") {
				t.Fatalf("wrong healthy partition: fault=%s id=%s", fault, firstID)
			}
			_, processErr := s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
			remaining, readErr := s.pendingNotificationIDs()
			t.Logf("PARTIAL_ADMISSION fault=%q requests=%d error=%v remaining=%v read_error=%v", fault, len(a.Requests()), processErr, remaining, readErr)
			if fault == "watch source append" {
				if !errors.Is(processErr, sentinel) {
					t.Fatalf("source refusal lost: %v", processErr)
				}
			} else if processErr != nil {
				t.Fatal(processErr)
			}
			if len(a.Requests()) != 1 {
				t.Fatalf("healthy half provider requests=%d want1", len(a.Requests()))
			}
			assertNotificationObservedOnce(t, s.TranscriptPath(), firstID)
			firstGen := loadShellRecord(t, jm, rec.JobID).TerminalGen
			jm.appendEvent = original
			if fault == "terminal pending append" {
				if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
					t.Fatal(err)
				}
			} else {
				jm.dispatchProducedWatchSends()
			}
			if loadShellRecord(t, jm, rec.JobID).TerminalGen != firstGen {
				t.Fatal("retry changed shell generation")
			}
			waitForShellDone(t, jm, rec.JobID)
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if len(a.Requests()) != 2 {
				t.Fatalf("partitioned durability requires two real requests, got%d", len(a.Requests()))
			}
			source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(source) != 1 {
				t.Fatalf("durable watch source frames=%d want1", len(source))
			}
			watchID := fmt.Sprintf("watch:%s:%d", source[0].WatchSend.DeliveryID, source[0].WatchSend.UpdateSeq)
			shellID := "shell:" + rec.JobID + ":" + firstGen
			actual := map[string]int{}
			for _, e := range notificationRawEntries(t, s.TranscriptPath()) {
				if e.Turn.AttentionID != "" {
					actual[e.Turn.AttentionID]++
				}
			}
			if len(actual) != 2 || actual[watchID] != 1 || actual[shellID] != 1 {
				t.Fatalf("custody=%v, want watch=%s shell=%s once each", actual, watchID, shellID)
			}
			if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
				t.Fatalf("unsettled custody=%v err=%v", ids, err)
			}
			t.Logf("PARTIAL_FAILURE_PROVED fault=%q first_healthy_id=%s restored_ids=[%s %s] provider_requests=2 holds=0", fault, firstID, watchID, shellID)
		})
	}
}

// These are synchronous publication/fault component probes. The retained real
// process tests separately prove callback handoff and an external producer barrier.
func terminalFlushHoldState(t *testing.T, s *Session, want int) {
	t.Helper()
	s.pendingJobNotifsMu.Lock()
	holds, deferred := s.notifyWakeHolds, s.notifyWakeDeferred
	s.pendingJobNotifsMu.Unlock()
	if holds != want || (want == 0 && deferred) {
		t.Fatalf("holds=%d deferred=%t want holds=%d", holds, deferred, want)
	}
}

func terminalFlushObserveAll(t *testing.T, s *Session, a *fakeAdapter, wantRequests int) {
	t.Helper()
	if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.Requests()) != wantRequests {
		t.Fatalf("requests=%d want%d", len(a.Requests()), wantRequests)
	}
	if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
		t.Fatalf("pending=%v err=%v", ids, err)
	}
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	fold = fold.clone()
	s.attentionMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range fold.order {
		if _, ok := fold.observed[id]; !ok || fold.resolutions[id] != delegateAttentionConsumed {
			t.Errorf("unobserved/unconsumed id=%s", id)
		}
	}
}

func TestTerminalFlushBoundaryFailures(t *testing.T) {
	for _, fault := range []string{"prepare", "finished-append", "finished-forward", "pending-forward"} {
		t.Run(fault, func(t *testing.T) {
			sentinel := errors.New("terminal-flush-injected-refusal")
			captureReceiverErrorLogs(t, sentinel, func() error { return sentinel })
			s, a := newTerminalFlushSession(t, true)
			jm := s.jobManager
			rec, err := jm.createShell(createShellOpts{Command: "correction-unlaunched-621"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := jobWatchTool(s, map[string]any{"operation": "create", "source": "self", "events": []any{"job.notification"}}, jobToolResultDefaultMaxChar); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				a.steps = append(a.steps, func(llm.Request) llm.Response { return finalResponse("correction-response-621") })
			}
			var wakes atomic.Int32
			s.SetNotifyFunc(func() { wakes.Add(1) })
			originalAppend, originalForward := jm.appendEvent, jm.forward
			defer func() { jm.appendEvent, jm.forward = originalAppend, originalForward }()
			injected := false
			fail := func() error {
				s.pendingJobNotifsMu.Lock()
				held := s.notifyWakeHolds
				s.pendingJobNotifsMu.Unlock()
				if held == 0 {
					t.Error("publication fault escaped existing hold")
				}
				if !injected && fault != "pending-forward" {
					if err := s.enqueueNotification(notification{ID: "correction-healthy-621", Message: llm.User("opaque-healthy-621")}); err != nil {
						t.Fatal(err)
					}
				}
				injected = true
				if wakes.Load() != 0 {
					t.Error("wake escaped failing publication before release")
				}
				return sentinel
			}
			jm.appendEvent = func(e jobstore.Event) error {
				if fault == "finished-append" && e.Kind == jobstore.EventJobFinished {
					return fail()
				}
				return originalAppend(e)
			}
			if strings.HasSuffix(fault, "forward") {
				jm.mu.Lock()
				jm.parentJobID = "correction-parent-621"
				jm.mu.Unlock()
				jm.forward = func(e jobstore.Event) error {
					if (fault == "finished-forward" && e.Kind == jobstore.EventJobFinished) || (fault == "pending-forward" && e.Kind == jobstore.EventJobNotificationPending) {
						return fail()
					}
					return nil
				}
			}
			if fault == "prepare" {
				err = jm.finalizeWithRunMode(rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) { return "", "", nil, fail() }, true)
			} else {
				err = jm.finalize(rec.JobID, jobstore.StatusCompleted, "", nil)
			}
			if !errors.Is(err, sentinel) || !injected {
				t.Fatalf("fault not exercised err=%v injected=%t", err, injected)
			}
			terminalFlushHoldState(t, s, 0)
			if wakes.Load() != 1 {
				t.Fatalf("healthy work wakes=%d want1", wakes.Load())
			}
			jm.mu.Lock()
			run := jm.running[rec.JobID]
			jm.mu.Unlock()
			if run == nil || run.finalize != nil {
				t.Fatal("failed attempt lost retry owner or leaked attempt")
			}
			generation := ""
			if run.terminal != nil {
				generation = run.terminal.generation
			}
			ids, err := s.pendingNotificationIDs()
			if err != nil || len(ids) != 1 {
				t.Fatalf("healthy pending=%v err=%v", ids, err)
			}
			if fault == "pending-forward" {
				source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(source) != 1 {
					t.Fatalf("watch sources=%d", len(source))
				}
				expected := fmt.Sprintf("watch:%s:%d", source[0].WatchSend.DeliveryID, source[0].WatchSend.UpdateSeq)
				if ids[0] != expected {
					t.Fatalf("healthy id=%s want%s", ids[0], expected)
				}
			} else if ids[0] != "correction-healthy-621" {
				t.Fatalf("healthy id=%s", ids[0])
			}
			terminalFlushObserveAll(t, s, a, 1)
			assertNotificationObservedOnce(t, s.TranscriptPath(), ids[0])
			if fault == "finished-forward" {
				// The retained terminal takes finalizeWithRunMode's retry branch before
				// emission. A second forward failure must release that attempt too.
				wakes.Store(0)
				if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); !errors.Is(err, sentinel) {
					t.Fatalf("retry forward=%v", err)
				}
				terminalFlushHoldState(t, s, 0)
				if wakes.Load() != 0 || run.terminal.generation != generation {
					t.Fatal("failed retry changed generation or manufactured work")
				}
			}
			jm.appendEvent, jm.forward = originalAppend, originalForward
			before := wakes.Load()
			jm.testOnlyAfterNotifyPendingAppend = func(string) {
				if wakes.Load() != before {
					t.Error("retry emitted before shell canonical acceptance")
				}
			}
			if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
				t.Fatal(err)
			}
			terminalFlushHoldState(t, s, 0)
			if wakes.Load() != before+1 {
				t.Fatalf("recovery wakes=%d want%d", wakes.Load(), before+1)
			}
			finalRec := loadShellRecord(t, jm, rec.JobID)
			if generation != "" && finalRec.TerminalGen != generation {
				t.Fatal("recovery changed durable terminal generation")
			}
			shellID := "shell:" + rec.JobID + ":" + finalRec.TerminalGen
			source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(source) != 1 {
				t.Fatalf("watch sources=%d", len(source))
			}
			watchID := fmt.Sprintf("watch:%s:%d", source[0].WatchSend.DeliveryID, source[0].WatchSend.UpdateSeq)
			terminalFlushObserveAll(t, s, a, 2)
			accepted := map[string]int{}
			for _, e := range notificationRawEntries(t, s.TranscriptPath()) {
				if e.Turn.AttentionID != "" {
					accepted[e.Turn.AttentionID]++
				}
			}
			wantAccepted := 3
			if fault == "pending-forward" {
				wantAccepted = 2
			}
			if len(accepted) != wantAccepted || accepted[watchID] != 1 || accepted[shellID] != 1 {
				t.Fatalf("accepted=%v want distinct source IDs once %s %s", accepted, watchID, shellID)
			}
			jm.mu.Lock()
			remaining := jm.running[rec.JobID]
			jm.mu.Unlock()
			if remaining != nil {
				t.Fatal("recovery left running custody")
			}
			t.Logf("BOUNDARY_FAILURE_PROVED fault=%s generation=%s accepted=%v requests=2 holds=0", fault, finalRec.TerminalGen, accepted)
		})
	}
}

func TestTerminalFlushKeptSyncPolicy(t *testing.T) {
	for _, persistent := range []bool{true, false} {
		for _, direct := range []bool{true, false} {
			t.Run(fmt.Sprintf("persistent=%t/direct=%t", persistent, direct), func(t *testing.T) {
				captureReceiverErrorLogs(t, errors.New("unexpected kept-sync diagnostic"))
				s, a := newTerminalFlushSession(t, persistent)
				jm := s.jobManager
				rec, err := jm.createShell(createShellOpts{Command: "correction-kept-unlaunched-734"})
				if err != nil {
					t.Fatal(err)
				}
				for _, args := range []map[string]any{
					{"operation": "create", "source": "self", "events": []any{"job.notification"}},
					{"operation": "create", "source": rec.JobID, "output_match": "opaque-unmatched-734"},
				} {
					if _, err := jobWatchTool(s, args, jobToolResultDefaultMaxChar); err != nil {
						t.Fatal(err)
					}
				}
				a.steps = append(a.steps, func(llm.Request) llm.Response { return finalResponse("kept-response-734") })
				var wakes atomic.Int32
				s.SetNotifyFunc(func() { wakes.Add(1) })
				if direct {
					jm.mu.Lock()
					run := jm.running[rec.JobID]
					jm.mu.Unlock()
					err = jm.finalizeKeptSync(run, jobstore.StatusCompleted, "", nil)
				} else {
					err = jm.finalizeWithRunNoNotification(rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) {
						return jobstore.StatusCompleted, "", nil, nil
					})
				}
				if err != nil {
					t.Fatal(err)
				}
				terminalFlushHoldState(t, s, 0)
				// Preserve baseline self watch, EndNotice and final raw kick callbacks.
				if wakes.Load() != 3 {
					t.Fatalf("kept sync wakes=%d want baseline3", wakes.Load())
				}
				// Kept-sync policy still owes only watches, never asynchronous shell custody.
				if pending := producerSourceEvents(t, jm, jobstore.EventJobNotificationPending); len(pending) != 0 {
					t.Fatalf("kept sync shell pending=%d", len(pending))
				}
				ids, err := s.pendingNotificationIDs()
				if err != nil || len(ids) != 2 {
					t.Fatalf("kept sync ids=%v err=%v", ids, err)
				}
				sources := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(sources) != 2 {
					t.Fatalf("kept sync watch sources=%d", len(sources))
				}
				for i, source := range sources {
					expected := fmt.Sprintf("watch:%s:%d", source.WatchSend.DeliveryID, source.WatchSend.UpdateSeq)
					if ids[i] != expected {
						t.Fatalf("kept order=%v source%d=%s", ids, i, expected)
					}
				}
				terminalFlushObserveAll(t, s, a, 1)
				s.attentionMu.Lock()
				fold, err := s.delegateAttentionFoldLocked()
				fold = fold.clone()
				s.attentionMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				if len(fold.order) != 2 {
					t.Fatalf("kept sync accepted=%v", fold.order)
				}
				req := a.Requests()[0]
				for i, id := range ids {
					if !strings.HasPrefix(id, "watch:") || fold.content[id].Text() != sources[i].WatchSend.Frame {
						t.Fatalf("kept source custody changed for%s", id)
					}
					matches := 0
					for _, m := range req.Messages {
						if m.Role == llm.RoleUser && strings.Contains(m.Text(), sources[i].WatchSend.Frame) {
							matches++
						}
					}
					if matches != 1 {
						t.Fatalf("kept source frame request coverage=%d", matches)
					}
					if persistent {
						assertNotificationObservedOnce(t, s.TranscriptPath(), id)
					}
				}
				if wakes.Load() != 3 {
					t.Fatal("kept drain produced extra handoff")
				}
				t.Logf("KEPT_SYNC_PROVED direct=%t persistent=%t watchIDs=%v no_shell_notification=true requests=1 baseline_wakes=3", direct, persistent, ids)
			})
		}
	}
}

// Hold component probes, not replacements for the real terminal-finalizer regression.
func TestTerminalFlushHoldNestedAndForeign(t *testing.T) {
	// Serial: diagnostic capture replaces the default logger.
	captureReceiverErrorLogs(t, errors.New("unexpected nested-hold diagnostic"))
	owner, a := newTerminalFlushSession(t, true)
	foreign, b := newTerminalFlushSession(t, true)
	a.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("owner-receipt-73") }}
	b.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("foreign-receipt-73") }}
	var ownerWakes, foreignWakes atomic.Int32
	owner.SetNotifyFunc(func() { ownerWakes.Add(1) })
	foreign.SetNotifyFunc(func() { foreignWakes.Add(1) })
	outer := sync.OnceFunc(owner.holdJobNotificationWake())
	defer outer()
	inner := sync.OnceFunc(owner.holdJobNotificationWake())
	defer inner()
	for i := range 2 {
		if err := owner.enqueueNotification(notification{ID: fmt.Sprintf("review-owner-%d", i), Message: llm.User(fmt.Sprintf("opaque-owner-%d", i))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := foreign.enqueueNotification(notification{ID: "review-foreign", Message: llm.User("opaque-foreign")}); err != nil {
		t.Fatal(err)
	}
	if foreignWakes.Load() != 1 || ownerWakes.Load() != 0 {
		t.Fatalf("owner=%d foreign=%d", ownerWakes.Load(), foreignWakes.Load())
	}
	if _, err := foreign.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	inner()
	if ownerWakes.Load() != 0 {
		t.Fatal("inner release woke owner")
	}
	owner.mu.Lock()
	hinted := owner.notificationSchedule.hinted
	owner.mu.Unlock()
	if hinted {
		t.Fatal("held requests consumed hint eligibility")
	}
	outer()
	if ownerWakes.Load() != 1 {
		t.Fatalf("outer release wakes=%d", ownerWakes.Load())
	}
	if err := owner.dispatchNotificationWake(); err != nil {
		t.Fatal(err)
	}
	if ownerWakes.Load() != 1 {
		t.Fatal("duplicate dispatcher hint")
	}
	if _, err := owner.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if len(a.Requests()) != 1 || len(b.Requests()) != 1 {
		t.Fatal("wrong provider request counts")
	}
	for _, id := range []string{"review-owner-0", "review-owner-1"} {
		assertNotificationObservedOnce(t, owner.TranscriptPath(), id)
	}
	assertNotificationObservedOnce(t, foreign.TranscriptPath(), "review-foreign")
}

func TestTerminalFlushHoldRetryAndReleaseFailure(t *testing.T) {
	// Serial: diagnostic capture replaces the default logger.
	for _, fault := range []bool{false, true} {
		t.Run(fmt.Sprintf("release-read-failure=%t", fault), func(t *testing.T) {
			expected := errors.New("unexpected retry diagnostic")
			if fault {
				expected = syscall.EISDIR
			}
			captureReceiverErrorLogs(t, expected)
			clk := agenttest.NewFakeClock()
			a := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("retry-receipt-89") }}}
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk}), withAdapter(a), withoutGitSnapshot())
			var wakes atomic.Int32
			s.SetNotifyFunc(func() { wakes.Add(1) })
			release := sync.OnceFunc(s.holdJobNotificationWake())
			defer release()
			if err := s.enqueueNotification(notification{ID: "review-retry", Message: llm.User("opaque-retry-89")}); err != nil {
				t.Fatal(err)
			}
			if fault {
				path := s.TranscriptPath()
				backup := path + ".review-backup"
				if err := os.Rename(path, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					_ = os.Rename(backup, path)
					t.Fatal(err)
				}
				restore := sync.OnceFunc(func() {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(backup, path); err != nil {
						t.Fatal(err)
					}
				})
				defer restore()
				release()
				s.mu.Lock()
				retry := s.notificationSchedule.retry.active
				s.mu.Unlock()
				if !retry || wakes.Load() != 0 {
					t.Fatalf("failed release retry=%t wakes=%d", retry, wakes.Load())
				}
				restore()
				clk.Advance(jobNotificationRetryInitialDelay)
				clk.Drain()
			} else {
				s.scheduleNotificationRetry()
				clk.Advance(jobNotificationRetryInitialDelay)
				clk.Drain()
				s.mu.Lock()
				retry, hinted := s.notificationSchedule.retry.active, s.notificationSchedule.hinted
				s.mu.Unlock()
				if retry || hinted || wakes.Load() != 0 {
					t.Fatalf("held timer retry=%t hinted=%t wakes=%d", retry, hinted, wakes.Load())
				}
				release()
			}
			if wakes.Load() != 1 {
				t.Fatalf("recovery wakes=%d", wakes.Load())
			}
			s.pendingJobNotifsMu.Lock()
			holds, deferred := s.notifyWakeHolds, s.notifyWakeDeferred
			s.pendingJobNotifsMu.Unlock()
			if holds != 0 || deferred {
				t.Fatalf("leaked hold=%d deferred=%t", holds, deferred)
			}
			if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			assertNotificationObservedOnce(t, s.TranscriptPath(), "review-retry")
			if len(a.Requests()) != 1 {
				t.Fatalf("requests=%d", len(a.Requests()))
			}
		})
	}
}

// Component probe: pause the real outer hold's release, never the wake callback.
// This exposes the window after attempt.done closes and run.finalize clears.
func TestTerminalFlushAttemptDoneBeforeHoldRelease(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending-forward-failure=%t", failFirst), func(t *testing.T) {
			sentinel := errors.New("terminal-flush-injected-refusal")
			captureReceiverErrorLogs(t, sentinel, func() error { return sentinel })
			s, a := newTerminalFlushSession(t, true)
			jm := s.jobManager
			rec, err := jm.createShell(createShellOpts{Command: "review-unlaunched-attempt-431"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := jobWatchTool(s, map[string]any{"operation": "create", "source": "self", "events": []any{"job.notification"}}, jobToolResultDefaultMaxChar); err != nil {
				t.Fatal(err)
			}
			a.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("review-attempt-receipt-431") }}
			var wakes atomic.Int32
			s.SetNotifyFunc(func() { wakes.Add(1) })
			jm.mu.Lock()
			jm.parentJobID = "review-parent-431"
			jm.mu.Unlock()
			var fail atomic.Bool
			fail.Store(failFirst)
			originalForward := jm.forward
			jm.forward = func(e jobstore.Event) error {
				if fail.Load() && e.Kind == jobstore.EventJobNotificationPending {
					return sentinel
				}
				return nil
			}
			defer func() { jm.forward = originalForward }()
			entered, gate := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(gate) })
			originalHold := jm.holdWake
			var acquisitions atomic.Int32
			jm.holdWake = func() func() {
				release := originalHold()
				n := acquisitions.Add(1)
				return func() {
					if n == 1 {
						close(entered)
						<-gate
					}
					release()
				}
			}
			var attempt *finalizeAttempt
			firstDone := make(chan error, 1)
			go func() {
				firstDone <- jm.finalizeWithRunMode(rec.JobID, func(run *runningJob) (jobstore.Status, string, *int, error) {
					jm.mu.Lock()
					attempt = run.finalize
					jm.mu.Unlock()
					return jobstore.StatusCompleted, "", nil, nil
				}, true)
			}()
			joined := false
			defer func() {
				unblock()
				if !joined {
					<-firstDone
				}
				jm.holdWake = originalHold
			}()
			notificationAwait(t, entered)
			if attempt == nil {
				t.Fatal("attempt not captured")
			}
			select {
			case <-attempt.done:
			default:
				t.Fatal("outer release started before attempt.done closed")
			}
			if !errors.Is(attempt.err, sentinel) && failFirst {
				t.Fatalf("attempt error=%v", attempt.err)
			}
			if attempt.err != nil && !failFirst {
				t.Fatal(attempt.err)
			}
			jm.mu.Lock()
			run := jm.running[rec.JobID]
			var active *finalizeAttempt
			if run != nil {
				active = run.finalize
			}
			jm.mu.Unlock()
			if active != nil {
				t.Fatal("attempt pointer not cleared before release")
			}
			if (run != nil) != failFirst {
				t.Fatalf("retained run=%t failFirst=%t", run != nil, failFirst)
			}
			terminalFlushHoldState(t, s, 1)
			if wakes.Load() != 0 {
				t.Fatal("wake escaped outer hold")
			}
			generation := loadShellRecord(t, jm, rec.JobID).TerminalGen
			// A waiter/retry may proceed now even though the first owner's defer has not
			// released its hold. A healthy no-run return must not decrement that hold.
			fail.Store(false)
			if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
				t.Fatal(err)
			}
			terminalFlushHoldState(t, s, 1)
			if wakes.Load() != 0 {
				t.Fatal("concurrent retry released predecessor's hold")
			}
			if got := loadShellRecord(t, jm, rec.JobID).TerminalGen; got != generation {
				t.Fatalf("generation=%s want%s", got, generation)
			}
			ids, err := s.pendingNotificationIDs()
			if err != nil || len(ids) != 2 {
				t.Fatalf("ready IDs=%v error=%v", ids, err)
			}
			unblock()
			err = <-firstDone
			joined = true
			if failFirst && !errors.Is(err, sentinel) {
				t.Fatalf("first result=%v", err)
			}
			if !failFirst && err != nil {
				t.Fatal(err)
			}
			terminalFlushHoldState(t, s, 0)
			if wakes.Load() != 1 {
				t.Fatalf("final wakes=%d want1", wakes.Load())
			}
			terminalFlushObserveAll(t, s, a, 1)
			for _, id := range ids {
				assertNotificationObservedOnce(t, s.TranscriptPath(), id)
			}
			t.Logf("ATTEMPT_DONE_RELEASE_PROVED failure=%t holds=0 callbacks=1 requests=1 same_generation=true", failFirst)
		})
	}
}
