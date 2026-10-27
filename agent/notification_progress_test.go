package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Regression: the receipt already exists, so repair leaves no
// model input. The outer completion hold must retain the resource-progress wake.
func TestNotificationProgressHeldReceipt(t *testing.T) {
	t.Parallel()
	for _, nested := range []bool{false, true} {
		name := "single"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			clock := agenttest.NewFakeClock()
			sess, adapter, id := drainingDelegateWithUpdate(t, func(cfg *SessionConfig) { cfg.clock = clock })
			fs := installResolutionSyncFailuresWriter(t, sess, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil || fs.injectedFailures() != 1 || len(adapter.requestsFor("root")) != 1 {
				t.Fatalf("response error=%v failures=%d requests=%d", err, fs.injectedFailures(), len(adapter.requestsFor("root")))
			}
			observed := rootAttentionFold(t, sess)
			if _, ok := observed.observed[id]; !ok || len(observed.observed) != 1 {
				t.Fatalf("missing actual successful response receipt: %v", observed.observed)
			}
			var wakes, callbacksAtWake atomic.Int32
			sess.SetNotifyFunc(func() {
				sess.attentionMu.Lock()
				callbacksAtWake.Store(int32(sess.attentionCallbacks))
				sess.attentionMu.Unlock()
				wakes.Add(1)
			})
			releaseOuter := sync.OnceFunc(sess.holdJobNotificationWake())
			defer releaseOuter()
			var inner func()
			if nested {
				inner = sync.OnceFunc(sess.holdJobNotificationWake())
				defer inner()
			}
			clock.Advance(jobNotificationRetryInitialDelay)
			clock.Drain()
			sess.mu.Lock()
			retry, hinted := sess.notificationSchedule.retry.active, sess.notificationSchedule.hinted
			sess.mu.Unlock()
			if retry || hinted || wakes.Load() != 0 {
				t.Fatalf("held receipt repair retry=%v hinted=%v wakes=%d", retry, hinted, wakes.Load())
			}
			if inner != nil {
				inner()
				if wakes.Load() != 0 {
					t.Fatalf("inner release woke %d times", wakes.Load())
				}
			}
			releaseOuter()
			requireRecoveredReceipt(t, sess, id)
			sess.pendingJobNotifsMu.Lock()
			holds, deferred := sess.notifyWakeHolds, sess.notifyWakeDeferred
			sess.pendingJobNotifsMu.Unlock()
			if holds != 0 || deferred || wakes.Load() != 1 || callbacksAtWake.Load() != 0 || sess.notificationExecutionState().recovering || len(adapter.requestsFor("root")) != 1 {
				t.Fatalf("released receipt holds=%d deferred=%v wakes=%d callbacks=%d recovering=%v requests=%d", holds, deferred, wakes.Load(), callbacksAtWake.Load(), sess.notificationExecutionState().recovering, len(adapter.requestsFor("root")))
			}
		})
	}
}

func TestNotificationProgressReleaseDuringRetryTail(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	sess, adapter, id := drainingDelegateWithUpdate(t, func(cfg *SessionConfig) { cfg.clock = clock })
	entered, resumeGate := make(chan struct{}), make(chan struct{})
	complete := progressObserve(sess, func(point string, _ *Session) {
		if point == "retry-leave" {
			close(entered)
			<-resumeGate
		}
	})
	fs := installResolutionSyncFailuresWriter(t, sess, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil || fs.injectedFailures() != 1 || len(adapter.requestsFor("root")) != 1 {
		t.Fatalf("response error=%v failures=%d requests=%d", err, fs.injectedFailures(), len(adapter.requestsFor("root")))
	}
	observed := rootAttentionFold(t, sess)
	if _, ok := observed.observed[id]; !ok || len(observed.observed) != 1 {
		t.Fatalf("missing actual successful response receipt: %v", observed.observed)
	}
	progressAttachController(t, sess, clock)
	var wakes atomic.Int32
	sess.SetNotifyFunc(func() {
		requireProgressCustodyReleased(t, sess)
		if !complete.Load() {
			t.Error("publication before complete release returned")
		}
		wakes.Add(1)
	})
	releaseOuter := sync.OnceFunc(sess.holdJobNotificationWake())
	defer releaseOuter()
	resume := sync.OnceFunc(func() { close(resumeGate) })
	cleanupProgressWorkers(t, sess, clock, resume, nil)
	clock.Advance(jobNotificationRetryInitialDelay)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("retry never reached post-custody gate")
	}
	sess.attentionMu.Lock()
	callbacks := sess.attentionCallbacks
	sess.attentionMu.Unlock()
	sess.pendingJobNotifsMu.Lock()
	holds, deferred := sess.notifyWakeHolds, sess.notifyWakeDeferred
	sess.pendingJobNotifsMu.Unlock()
	if callbacks != 0 || holds != 1 || !deferred || wakes.Load() != 0 {
		t.Fatalf("gate callbacks=%d holds=%d deferred=%v wakes=%d", callbacks, holds, deferred, wakes.Load())
	}
	t.Logf("retry paused after custody release: callbacks=%d holds=%d deferred=%v wakes=%d", callbacks, holds, deferred, wakes.Load())
	releaseOuter()
	requireRecoveredReceipt(t, sess, id)
	if wakes.Load() != 0 {
		t.Fatalf("outer release published while retry participant still owns tail: wakes=%d", wakes.Load())
	}
	t.Logf("outer release completed before retry tail: wakes=%d", wakes.Load())
	resume()
	clock.Drain()
	sess.pendingJobNotifsMu.Lock()
	holds, deferred = sess.notifyWakeHolds, sess.notifyWakeDeferred
	sess.pendingJobNotifsMu.Unlock()
	t.Logf("retry tail completed: wakes=%d holds=%d deferred=%v recovering=%v requests=%d", wakes.Load(), holds, deferred, sess.notificationExecutionState().recovering, len(adapter.requestsFor("root")))
	if wakes.Load() != 1 || holds != 0 || deferred || sess.notificationExecutionState().recovering || len(adapter.requestsFor("root")) != 1 {
		t.Fatalf("expected exactly one useful release wake, got %d", wakes.Load())
	}
	if !requestsContain(adapter.requestsFor("root"), "UPDATE_PAYLOAD_913") {
		t.Fatal("original response omitted original payload")
	}
}

// A producer can already have deferred canonical dispatch while the retry is
// pending. Release may then overtake the timer before its dispatch hold check.
func TestNotificationProgressReleaseBeforeRetryDispatch(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	sess, adapter, id := drainingDelegateWithUpdate(t, func(cfg *SessionConfig) { cfg.clock = clock })
	entered, resumeGate := make(chan struct{}), make(chan struct{})
	complete := progressObserve(sess, func(point string, _ *Session) {
		if point == "retry-dispatch" {
			close(entered)
			<-resumeGate
		}
	})
	fs := installResolutionSyncFailuresWriter(t, sess, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil || fs.injectedFailures() != 1 || len(adapter.requestsFor("root")) != 1 {
		t.Fatalf("response error=%v failures=%d requests=%d", err, fs.injectedFailures(), len(adapter.requestsFor("root")))
	}
	observed := rootAttentionFold(t, sess)
	if _, ok := observed.observed[id]; !ok || len(observed.observed) != 1 {
		t.Fatalf("missing actual receipt: %v", observed.observed)
	}
	progressAttachController(t, sess, clock)
	var wakes atomic.Int32
	sess.SetNotifyFunc(func() {
		requireProgressCustodyReleased(t, sess)
		if !complete.Load() {
			t.Error("publication before complete release returned")
		}
		wakes.Add(1)
	})
	releaseOuter := sync.OnceFunc(sess.holdJobNotificationWake())
	defer releaseOuter()
	// This real canonical request is deferred by the established hold. It does
	// not emit a hint, alter a receipt or manufacture a progress callback.
	if err := sess.dispatchNotificationWake(); err != nil {
		t.Fatal(err)
	}
	resume := sync.OnceFunc(func() { close(resumeGate) })
	cleanupProgressWorkers(t, sess, clock, resume, nil)
	clock.Advance(jobNotificationRetryInitialDelay)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("retry never reached pre-dispatch gate")
	}
	sess.attentionMu.Lock()
	callbacks := sess.attentionCallbacks
	sess.attentionMu.Unlock()
	sess.pendingJobNotifsMu.Lock()
	holds, deferred := sess.notifyWakeHolds, sess.notifyWakeDeferred
	sess.pendingJobNotifsMu.Unlock()
	if callbacks != 1 || holds != 1 || !deferred || wakes.Load() != 0 {
		t.Fatalf("gate callbacks=%d holds=%d deferred=%v wakes=%d", callbacks, holds, deferred, wakes.Load())
	}
	t.Logf("before retry dispatch: callbacks=%d holds=%d deferred=%v wakes=%d", callbacks, holds, deferred, wakes.Load())
	releaseOuter()
	requireRecoveredReceipt(t, sess, id)
	if wakes.Load() != 0 {
		t.Fatal("release published while timer still retained callback custody")
	}
	t.Logf("outer release completed before retry dispatch: wakes=%d", wakes.Load())
	resume()
	clock.Drain()
	requireRecoveredReceipt(t, sess, id)
	sess.pendingJobNotifsMu.Lock()
	holds, deferred = sess.notifyWakeHolds, sess.notifyWakeDeferred
	sess.pendingJobNotifsMu.Unlock()
	t.Logf("retry completed: wakes=%d holds=%d deferred=%v recovering=%v requests=%d", wakes.Load(), holds, deferred, sess.notificationExecutionState().recovering, len(adapter.requestsFor("root")))
	if wakes.Load() != 1 || holds != 0 || deferred || sess.notificationExecutionState().recovering || len(adapter.requestsFor("root")) != 1 || !requestsContain(adapter.requestsFor("root"), "UPDATE_PAYLOAD_913") {
		t.Fatalf("expected exactly one useful progress wake, got %d", wakes.Load())
	}
}

func TestNotificationProgressTimerBeforeReleasePending(t *testing.T) {
	t.Parallel()
	for _, nested := range []bool{false, true} {
		name := "single"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			clock := agenttest.NewFakeClock()
			sess, adapter, id := drainingDelegateWithUpdate(t, func(cfg *SessionConfig) { cfg.clock = clock })
			releaseOuter := sync.OnceFunc(sess.holdJobNotificationWake())
			defer releaseOuter()
			var inner func()
			if nested {
				inner = sync.OnceFunc(sess.holdJobNotificationWake())
				defer inner()
			}
			var wakes, callbacksAtWake atomic.Int32
			sess.SetNotifyFunc(func() {
				sess.attentionMu.Lock()
				callbacksAtWake.Store(int32(sess.attentionCallbacks))
				sess.attentionMu.Unlock()
				wakes.Add(1)
			})
			sess.scheduleNotificationRetry()
			clock.Advance(jobNotificationRetryInitialDelay)
			clock.Drain()
			sess.mu.Lock()
			retry, hinted := sess.notificationSchedule.retry.active, sess.notificationSchedule.hinted
			sess.mu.Unlock()
			if retry || hinted || wakes.Load() != 0 {
				t.Fatalf("held pending retry=%v hinted=%v wakes=%d", retry, hinted, wakes.Load())
			}
			if inner != nil {
				inner()
				if wakes.Load() != 0 {
					t.Fatal("inner release woke")
				}
			}
			releaseOuter()
			if wakes.Load() != 1 || callbacksAtWake.Load() != 0 {
				t.Fatalf("release wakes=%d callbacks=%d", wakes.Load(), callbacksAtWake.Load())
			}
			if _, err := sess.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			requireRecoveredReceipt(t, sess, id)
			if len(adapter.requestsFor("root")) != 1 || !requestsContain(adapter.requestsFor("root"), "UPDATE_PAYLOAD_913") {
				t.Fatal("pending input must appear in exactly one real request")
			}
		})
	}
}

func TestNotificationProgressReleaseSuppression(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"running", "stop", "close"} {
		t.Run(mode, func(t *testing.T) {
			clock := agenttest.NewFakeClock()
			inputAtRelease, resumeInput := make(chan struct{}), make(chan struct{})
			resume := sync.OnceFunc(func() { close(resumeInput) })
			sess, adapter, id := drainingDelegateWithUpdate(t, func(cfg *SessionConfig) {
				cfg.clock = clock
				if mode == "running" {
					cfg.testOnly.notificationExecutionRelease = func(released bool) {
						if !released {
							close(inputAtRelease)
							<-resumeInput
						}
					}
				}
			})
			fs := installResolutionSyncFailuresWriter(t, sess, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var inputDone chan error
			var inputExited chan struct{}
			cleanupProgressWorkers(t, sess, clock, resume, func() <-chan struct{} { return inputExited })
			if mode == "running" {
				inputDone = make(chan error, 1)
				inputExited = make(chan struct{})
				go func() {
					defer close(inputExited)
					_, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification)
					inputDone <- err
				}()
				select {
				case <-inputAtRelease:
				case <-ctx.Done():
					t.Fatal("real input did not reach its running release boundary")
				}
			} else if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil {
				t.Fatal("response must hit the injected resolution-sync failure")
			}
			if fs.injectedFailures() != 1 || len(adapter.requestsFor("root")) != 1 {
				t.Fatalf("failures=%d requests=%d", fs.injectedFailures(), len(adapter.requestsFor("root")))
			}
			observed := rootAttentionFold(t, sess)
			if _, ok := observed.observed[id]; !ok || len(observed.observed) != 1 {
				t.Fatalf("missing actual receipt: %v", observed.observed)
			}
			releaseOuter := sync.OnceFunc(sess.holdJobNotificationWake())
			defer releaseOuter()
			var wakes atomic.Int32
			sess.SetNotifyFunc(func() { wakes.Add(1) })
			// Stop must be admitted while the actual failed response still owns
			// recovery, not after the fixture has made the session quiescent.
			if mode == "stop" {
				if _, err := sess.InterruptClientMutation(ctx, appwire.TurnInterruptParams{ClientMutationID: "correction-release-stop"}, func() {}); err != nil {
					t.Fatal(err)
				}
			}
			clock.Advance(jobNotificationRetryInitialDelay)
			clock.Drain()
			sess.pendingJobNotifsMu.Lock()
			holds, deferred := sess.notifyWakeHolds, sess.notifyWakeDeferred
			sess.pendingJobNotifsMu.Unlock()
			if holds != 1 || !deferred || wakes.Load() != 0 {
				t.Fatalf("before suppression holds=%d deferred=%v wakes=%d", holds, deferred, wakes.Load())
			}
			// Repair only the real response's index, not its observation. This makes
			// recovery false so running/Stop/Close, not a retry, must suppress release.
			if err := sess.ackNotifications(nil); err != nil {
				t.Fatal(err)
			}
			requireRecoveredReceipt(t, sess, id)
			if sess.notificationExecutionState().recovering {
				t.Fatal("recovery would mask the suppression under test")
			}
			switch mode {
			case "running":
				if !sess.notificationExecutionState().running {
					t.Fatal("real input no longer running")
				}
			case "stop":
				if !sess.notificationStopHeld() {
					t.Fatal("actual Stop hold not established")
				}
			case "close":
				sess.Close()
				if !sess.notificationExecutionState().held {
					t.Fatal("actual Close not held")
				}
			}
			releaseOuter()
			sess.pendingJobNotifsMu.Lock()
			holds, deferred = sess.notifyWakeHolds, sess.notifyWakeDeferred
			sess.pendingJobNotifsMu.Unlock()
			if holds != 0 || deferred || wakes.Load() != 0 {
				t.Fatalf("%s release holds=%d deferred=%v wakes=%d", mode, holds, deferred, wakes.Load())
			}
			if mode == "running" {
				resume()
				if err := <-inputDone; err == nil {
					t.Fatal("real response lost its injected error")
				}
				inputDone = nil
			}
			requireRecoveredReceipt(t, sess, id)
			if len(adapter.requestsFor("root")) != 1 || !requestsContain(adapter.requestsFor("root"), "UPDATE_PAYLOAD_913") {
				t.Fatal("suppression changed real provider evidence")
			}
		})
	}
}

// progressObserve installs a session-local observer. Publication markers follow
// the complete existing attention/retirement release closure, not count zero.
// The atomic pointer makes replacement safe. Tests join timer callbacks before
// returning, so observer closures never outlive their fixtures.
func progressObserve(s *Session, f func(string, *Session)) *atomic.Bool {
	complete := new(atomic.Bool)
	s.testOnlyNotificationProgress.Store(&notificationProgressObserver{observe: func(point string) {
		if point == "custody-return" {
			complete.Store(true)
		}
		if f != nil {
			f(point, s)
		}
	}})
	return complete
}

// The controller is real, but its autonomous retirement loop is not started.
func progressAttachController(t *testing.T, s *Session, c *agenttest.FakeClock) *RetirementController {
	t.Helper()
	controller, err := NewRetirementController(0, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.AttachRoot(s); err != nil {
		t.Fatal(err)
	}
	return controller
}
func requireProgressDisposed(t *testing.T, s *Session) {
	t.Helper()
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	if s.notificationProgress != nil || s.notifyWakeHolds != 0 || s.notifyWakeDeferred {
		t.Fatalf("not disposed token=%+v holds=%d deferred=%v", s.notificationProgress, s.notifyWakeHolds, s.notifyWakeDeferred)
	}
}
func requireProgressCustodyReleased(t *testing.T, s *Session) {
	t.Helper()
	s.attentionMu.Lock()
	callbacks := s.attentionCallbacks
	s.attentionMu.Unlock()
	c := s.retirementController.Load()
	leases := 0
	if c != nil {
		c.mu.Lock()
		leases = len(c.active)
		c.mu.Unlock()
	}
	if callbacks != 0 || leases != 0 {
		t.Errorf("raw publication precedes complete custody: attention=%d retirement leases=%d", callbacks, leases)
	}
}
func progressReceipt(t *testing.T, clock *agenttest.FakeClock) (*Session, *messagingAdapter, string) {
	t.Helper()
	s, a, id := drainingDelegateWithUpdate(t, func(c *SessionConfig) { c.clock = clock })
	fs := installResolutionSyncFailuresWriter(t, s, 1)
	if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err == nil || fs.injectedFailures() != 1 || len(a.requestsFor("root")) != 1 {
		t.Fatalf("failed response err=%v failures=%d requests=%d", err, fs.injectedFailures(), len(a.requestsFor("root")))
	}
	fold := rootAttentionFold(t, s)
	if _, ok := fold.observed[id]; !ok || len(fold.observed) != 1 {
		t.Fatal("actual response receipt missing")
	}
	return s, a, id
}
func waitForProgress(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatal(what)
	}
}

// Register after fixture creation but before starting workers. LIFO cleanup
// releases every gate before joining, even when a checkpoint calls Fatal.
// Timer callbacks can start input workers, so inspect inputDone only after Drain.
func cleanupProgressWorkers(t *testing.T, s *Session, clock *agenttest.FakeClock, release func(), inputDone func() <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		release()
		drained := make(chan struct{})
		go func() { clock.Drain(); close(drained) }()
		if !joinProgressWorker(t, drained, "timer callbacks") {
			return
		}
		if inputDone != nil {
			if done := inputDone(); done != nil && !joinProgressWorker(t, done, "input worker") {
				return
			}
		}
		s.testOnlyNotificationProgress.Store(nil)
	})
}

func joinProgressWorker(t *testing.T, done <-chan struct{}, what string) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(10 * time.Second):
		t.Errorf("cleanup %s join timed out", what)
		return false
	}
}

func TestNotificationProgressHoldClaimBoundary(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"before", "after"} {
		t.Run(when, func(t *testing.T) {
			clock := agenttest.NewFakeClock()
			s, a, id := progressReceipt(t, clock)
			progressAttachController(t, s, clock)
			var complete atomic.Bool
			var wakes atomic.Int32
			var release func()
			// Record the return of the whole attention/retirement release.
			progressObserve(s, func(point string, s *Session) {
				if point == "custody-return" {
					complete.Store(true)
				}
				if point == "claim-"+when && release == nil {
					release = sync.OnceFunc(s.holdJobNotificationWake())
				}
			})
			t.Cleanup(func() { s.testOnlyNotificationProgress.Store(nil) })
			s.SetNotifyFunc(func() {
				requireProgressCustodyReleased(t, s)
				if !complete.Load() {
					t.Error("whole release closure has not returned")
				}
				wakes.Add(1)
			})
			clock.Advance(jobNotificationRetryInitialDelay)
			clock.Drain()
			if release == nil {
				t.Fatal("claim hook did not fire")
			}
			defer release()
			s.pendingJobNotifsMu.Lock()
			p, holds, deferred := s.notificationProgress, s.notifyWakeHolds, s.notifyWakeDeferred
			s.pendingJobNotifsMu.Unlock()
			if when == "before" {
				if p == nil || p.users != 0 || holds != 1 || !deferred || wakes.Load() != 0 {
					t.Fatalf("hold before claim lost token=%+v holds=%d deferred=%v wakes=%d", p, holds, deferred, wakes.Load())
				}
			} else {
				if p != nil || holds != 1 || deferred || wakes.Load() != 1 {
					t.Fatalf("hold after claim revoked handoff token=%+v holds=%d deferred=%v wakes=%d", p, holds, deferred, wakes.Load())
				}
			}
			release()
			requireProgressDisposed(t, s)
			requireRecoveredReceipt(t, s, id)
			if wakes.Load() != 1 || len(a.requestsFor("root")) != 1 || !requestsContain(a.requestsFor("root"), "UPDATE_PAYLOAD_913") {
				t.Fatal("hold boundary changed handoff or original request")
			}
		})
	}
}

func TestNotificationProgressCanonicalHintDisposition(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"outstanding", "consumed-finished", "consumed-running"} {
		t.Run(mode, func(t *testing.T) {
			clock := agenttest.NewFakeClock()
			inputEntered, inputResume := make(chan struct{}), make(chan struct{})
			resumeInput := sync.OnceFunc(func() { close(inputResume) })
			s, a, id := drainingDelegateWithUpdate(t, func(c *SessionConfig) {
				c.clock = clock
				if mode == "consumed-running" {
					c.testOnly.notificationExecutionRelease = func(released bool) {
						if !released {
							close(inputEntered)
							<-inputResume
						}
					}
				}
			})
			progressAttachController(t, s, clock)
			outer := sync.OnceFunc(s.holdJobNotificationWake())
			defer outer()
			entered, resume := make(chan struct{}), make(chan struct{})
			unpause := sync.OnceFunc(func() { close(resume) })
			var complete atomic.Bool
			progressObserve(s, func(point string, s *Session) {
				if point == "custody-return" {
					complete.Store(true)
				}
				if point == "retry-dispatch" {
					close(entered)
					<-resume
				}
			})
			var wakes atomic.Int32
			var inputDone chan error
			var inputExited chan struct{}
			cleanupProgressWorkers(t, s, clock, func() { unpause(); resumeInput() }, func() <-chan struct{} { return inputExited })
			s.SetNotifyFunc(func() {
				n := wakes.Add(1)
				if n == 1 && mode != "outstanding" {
					if mode == "consumed-running" {
						inputDone = make(chan error, 1)
						inputExited = make(chan struct{})
						go func() {
							defer close(inputExited)
							_, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification)
							inputDone <- err
						}()
						waitForProgress(t, inputEntered, "input never reached actual release boundary")
					} else {
						if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
							t.Error(err)
						}
					}
				}
				if n > 1 {
					requireProgressCustodyReleased(t, s)
					if !complete.Load() {
						t.Error("raw publication before whole release returned")
					}
				}
			})
			s.scheduleNotificationRetry()
			clock.Advance(jobNotificationRetryInitialDelay)
			waitForProgress(t, entered, "retry registration did not finish")
			outer()
			if wakes.Load() != 1 {
				t.Fatalf("canonical handoff count=%d", wakes.Load())
			}
			if mode == "outstanding" {
				s.mu.Lock()
				hinted := s.notificationSchedule.hinted
				s.mu.Unlock()
				if !hinted || pendingUpdateCount(t, s) != 1 {
					t.Fatal("outstanding canonical input no longer has a reserved hint")
				}
			} else {
				requireRecoveredReceipt(t, s, id)
			}
			unpause()
			clock.Drain()
			requireProgressDisposed(t, s)
			want := int32(1)
			if mode == "consumed-finished" {
				want = 2
			}
			if wakes.Load() != want {
				t.Fatalf("%s wakes=%d want=%d", mode, wakes.Load(), want)
			}
			if mode == "outstanding" {
				if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "consumed-running" {
				resumeInput()
				if err := <-inputDone; err != nil {
					t.Fatal(err)
				}
			}
			requireRecoveredReceipt(t, s, id)
			if len(a.requestsFor("root")) != 1 || !requestsContain(a.requestsFor("root"), "UPDATE_PAYLOAD_913") {
				t.Fatal("canonical hint duplicated or changed real request")
			}
		})
	}
}

func TestNotificationProgressLegacyHintDisposition(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"outstanding", "consumed-finished", "consumed-running", "absent-callback"} {
		t.Run(mode, func(t *testing.T) {
			clock := agenttest.NewFakeClock()
			inputEntered, inputResume := make(chan struct{}), make(chan struct{})
			resumeInput := sync.OnceFunc(func() { close(inputResume) })
			s, a, id := drainingDelegateWithUpdate(t, func(c *SessionConfig) {
				c.clock = clock
				if mode == "consumed-running" {
					c.testOnly.notificationExecutionRelease = func(released bool) {
						if !released {
							close(inputEntered)
							<-inputResume
						}
					}
				}
			})
			if mode == "outstanding" || mode == "absent-callback" {
				if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
			}
			progressAttachController(t, s, clock)
			outer := sync.OnceFunc(s.holdJobNotificationWake())
			defer outer()
			s.enqueueJobNotificationAndNotify(jobNotification{JobID: "legacy-token-source", Status: jobNotificationEventWatch})
			entered, resume := make(chan struct{}), make(chan struct{})
			unpause := sync.OnceFunc(func() { close(resume) })
			var complete atomic.Bool
			progressObserve(s, func(point string, s *Session) {
				if point == "custody-return" {
					complete.Store(true)
				}
				if point == "retry-dispatch" {
					close(entered)
					<-resume
				}
			})
			var wakes atomic.Int32
			var inputDone chan error
			var inputExited chan struct{}
			cleanupProgressWorkers(t, s, clock, func() { unpause(); resumeInput() }, func() <-chan struct{} { return inputExited })
			if mode != "absent-callback" {
				s.SetNotifyFunc(func() {
					n := wakes.Add(1)
					if n == 1 && mode != "outstanding" {
						if mode == "consumed-running" {
							inputDone = make(chan error, 1)
							inputExited = make(chan struct{})
							go func() {
								defer close(inputExited)
								_, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification)
								inputDone <- err
							}()
							waitForProgress(t, inputEntered, "legacy input never reached release boundary")
						} else {
							if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
								t.Error(err)
							}
						}
					}
					if n > 1 {
						requireProgressCustodyReleased(t, s)
						if !complete.Load() {
							t.Error("legacy resource publication before complete release")
						}
					}
				})
			}
			s.scheduleNotificationRetry()
			clock.Advance(jobNotificationRetryInitialDelay)
			waitForProgress(t, entered, "legacy timer not admitted")
			outer()
			s.pendingJobNotifsMu.Lock()
			p := s.notificationProgress
			legacyHint := p != nil && p.legacyHint
			s.pendingJobNotifsMu.Unlock()
			if mode == "absent-callback" {
				if legacyHint {
					t.Fatal("nil legacy callback marked satisfaction")
				}
			} else if !legacyHint {
				t.Fatal("real legacy handoff was not recorded before return")
			}
			s.mu.Lock()
			hinted := s.notificationSchedule.hinted
			s.mu.Unlock()
			if hinted {
				t.Fatal("legacy handoff changed canonical hinted")
			}
			unpause()
			clock.Drain()
			requireProgressDisposed(t, s)
			want := int32(1)
			if mode == "consumed-finished" {
				want = 2
			}
			if mode == "absent-callback" {
				want = 0
			}
			if wakes.Load() != want {
				t.Fatalf("%s wakes=%d want=%d", mode, wakes.Load(), want)
			}
			if mode == "consumed-running" {
				resumeInput()
				if err := <-inputDone; err != nil {
					t.Fatal(err)
				}
			}
			requireRecoveredReceipt(t, s, id)
			if len(a.requestsFor("root")) != 1 || !requestsContain(a.requestsFor("root"), "UPDATE_PAYLOAD_913") {
				t.Fatal("legacy case lost original one-request receipt")
			}
			if mode == "outstanding" || mode == "absent-callback" {
				if s.peekNotifications() != 1 {
					t.Fatal("outstanding legacy queue was consumed without input")
				}
			} else {
				if s.peekNotifications() != 0 {
					t.Fatal("actual legacy input did not drain queue")
				}
			}
		})
	}
}

func TestNotificationProgressNilCallbackDoesNotReplay(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	s, a, id := progressReceipt(t, clock)
	release := sync.OnceFunc(s.holdJobNotificationWake())
	defer release()
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	release()
	requireProgressDisposed(t, s)
	requireRecoveredReceipt(t, s, id)
	var wakes atomic.Int32
	s.SetNotifyFunc(func() { wakes.Add(1) })
	if wakes.Load() != 0 || len(a.requestsFor("root")) != 1 {
		t.Fatal("nil callback created retroactive receipt-only replay")
	}
}

func TestNotificationProgressStaleChecksDoNotJoin(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"before-admission", "during-admission"} {
		t.Run(when, func(t *testing.T) {
			clock := agenttest.NewFakeClock()
			s, a, id := progressReceipt(t, clock)
			c := progressAttachController(t, s, clock)
			var admissions, returns atomic.Int32
			var wakes atomic.Int32
			s.SetNotifyFunc(func() { wakes.Add(1) })
			progressObserve(s, func(point string, s *Session) {
				if point == "retry-admission-before" {
					admissions.Add(1)
				}
				if point == "custody-return" {
					returns.Add(1)
				}
				if when == "during-admission" && point == "retry-admission-after" {
					s.suspendNotificationWake()
				}
			})
			defer s.testOnlyNotificationProgress.Store(nil)
			if when == "before-admission" {
				s.suspendNotificationWake()
			}
			clock.Advance(jobNotificationRetryInitialDelay)
			clock.Drain()
			want := int32(0)
			if when == "during-admission" {
				want = 1
			}
			if admissions.Load() != want || returns.Load() != want || wakes.Load() != 0 {
				t.Fatalf("stale %s admissions=%d full returns=%d wakes=%d", when, admissions.Load(), returns.Load(), wakes.Load())
			}
			requireProgressDisposed(t, s)
			c.mu.Lock()
			leases := len(c.active)
			c.mu.Unlock()
			if leases != 0 {
				t.Fatal("stale callback retained retirement lease")
			}
			if err := s.ackNotifications(nil); err != nil {
				t.Fatal(err)
			}
			requireRecoveredReceipt(t, s, id)
			if len(a.requestsFor("root")) != 1 {
				t.Fatal("stale callback reran provider")
			}
		})
	}
}

func TestNotificationProgressCurrentAdmissionFailurePinsBeforeRearm(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	a := newMessagingAdapter()
	s := newMessagingRoot(t, a, func(c *SessionConfig) { c.clock = clock })
	c := progressAttachController(t, s, clock)
	var wakes atomic.Int32
	s.SetNotifyFunc(func() { wakes.Add(1) })
	claim, snapshot, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("real preparing claim %v %+v", err, snapshot)
	}
	defer c.Abort(claim, "")
	outer := sync.OnceFunc(s.holdJobNotificationWake())
	defer outer()
	if err := s.dispatchNotificationWake(); err != nil {
		t.Fatal(err)
	}
	var checked atomic.Bool
	progressObserve(s, func(point string, s *Session) {
		if point != "retry-transition" {
			return
		}
		s.mu.Lock()
		active := s.notificationSchedule.retry.active
		s.mu.Unlock()
		s.pendingJobNotifsMu.Lock()
		p := s.notificationProgress
		users := 0
		if p != nil {
			users = p.users
		}
		s.pendingJobNotifsMu.Unlock()
		if active || users != 1 {
			t.Errorf("admission-failed transition active=%v users=%d", active, users)
		}
		outer()
		if wakes.Load() != 0 {
			t.Error("release published through failed-admission pin")
		}
		checked.Store(true)
	})
	defer s.testOnlyNotificationProgress.Store(nil)
	s.scheduleNotificationRetry()
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	if !checked.Load() || wakes.Load() != 0 || !s.notificationExecutionState().recovering {
		t.Fatal("failed admission did not rearm before disposal")
	}
	requireProgressDisposed(t, s)
	c.mu.Lock()
	leases := len(c.active)
	c.mu.Unlock()
	if leases != 0 {
		t.Fatal("failed admission gained retirement custody")
	}
	s.testOnlyNotificationProgress.Store(nil)
	c.Abort(claim, "")
	// The retained existing timer, not an external kick, must recover after abort.
	clock.Advance(2 * jobNotificationRetryInitialDelay)
	clock.Drain()
	requireProgressDisposed(t, s)
	if wakes.Load() != 1 || s.notificationExecutionState().recovering || len(a.requestsFor("root")) != 0 {
		t.Fatal("existing rearmed timer did not recover without model input")
	}
}

func TestNotificationProgressReentryPreservesFreshWork(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	a := newMessagingAdapter()
	a.script("root", func(llm.Request) llm.Response { return finalResponse("NEW_TOKEN_RESPONSE") })
	s := newMessagingRoot(t, a, func(c *SessionConfig) { c.clock = clock })
	progressAttachController(t, s, clock)
	var wakes atomic.Int32
	var freshHold func()
	var fresh *notificationProgress
	var installFreshHold atomic.Bool
	progressObserve(s, func(point string, s *Session) {
		if point == "release-dispatch" && installFreshHold.CompareAndSwap(true, false) {
			freshHold = sync.OnceFunc(s.holdJobNotificationWake())
		}
	})
	defer s.testOnlyNotificationProgress.Store(nil)
	s.SetNotifyFunc(func() {
		n := wakes.Add(1)
		requireProgressCustodyReleased(t, s)
		if n != 1 {
			return
		}
		s.pendingJobNotifsMu.Lock()
		detached := s.notificationProgress == nil
		s.pendingJobNotifsMu.Unlock()
		if !detached {
			t.Error("old raw claim not detached before reentry")
		}
		release := s.holdJobNotificationWake()
		if _, err := s.appendDelegateNotificationDurably("new-token-work", "FRESH_TOKEN_PAYLOAD"); err != nil {
			t.Error(err)
		}
		if err := s.requestNotificationWake(); err != nil {
			t.Error(err)
		}
		installFreshHold.Store(true)
		release()
		s.pendingJobNotifsMu.Lock()
		fresh = s.notificationProgress
		s.pendingJobNotifsMu.Unlock()
	})
	s.scheduleNotificationRetry()
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	if freshHold == nil || fresh == nil {
		t.Fatal("reentry did not create and park real fresh work")
	}
	defer freshHold()
	s.pendingJobNotifsMu.Lock()
	kept := s.notificationProgress == fresh && fresh.users == 0 && s.notifyWakeHolds == 1 && s.notifyWakeDeferred
	s.pendingJobNotifsMu.Unlock()
	if !kept {
		t.Fatal("old callback return erased fresh token")
	}
	freshHold()
	requireProgressDisposed(t, s)
	if wakes.Load() != 2 {
		t.Fatalf("fresh input handoff wakes=%d", wakes.Load())
	}
	if _, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if len(a.requestsFor("root")) != 1 || !requestsContain(a.requestsFor("root"), "FRESH_TOKEN_PAYLOAD") {
		t.Fatal("fresh work did not reach exactly one real provider request")
	}
	requireRecoveredReceipt(t, s, "new-token-work")
}

func TestNotificationProgressConcurrentWatchTransportIsIndependent(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	s, a, id := drainingDelegateWithUpdate(t, func(c *SessionConfig) { c.clock = clock })
	jm := s.jobManager
	watch, err := jm.configureWatch(watchArgs{
		Source: "self", Target: runtimeMessageAliasCaller,
		Events:      []string{"assistant.tool"},
		EventFilter: &watchEventFilter{ToolName: "read_file", Status: "ok"},
		Send:        &watchSendArgs{To: runtimeMessageAliasCaller},
	})
	if err != nil || !watch.Watching || watch.WatchID == "" {
		t.Fatalf("phase=configure watch=%+v err=%v", watch, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) // Tripwire, not pacing.
	initialEntered, initialResume, initialDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	watchEntered, watchResume := make(chan struct{}), make(chan struct{})
	retryEntered, retryResume := make(chan struct{}), make(chan struct{})
	published := make(chan struct{})
	releaseInitial := sync.OnceFunc(func() { close(initialResume) })
	releaseWatch := sync.OnceFunc(func() { close(watchResume) })
	releaseRetry := sync.OnceFunc(func() { close(retryResume) })
	var phase atomic.Value
	phase.Store("before-producer")
	var selectedMu sync.Mutex
	var selected jobstore.WatchSendState
	var selectedSet bool
	var attempts atomic.Int32
	var setupAckFailures atomic.Int32
	var timedAttempts atomic.Int32
	var expectedWatchGeneration int
	var acked atomic.Int32
	var complete atomic.Bool
	var wakes atomic.Int32
	var producerStarted bool
	originalAppend := jm.appendEvent
	identity := func() string {
		selectedMu.Lock()
		defer selectedMu.Unlock()
		return fmt.Sprintf("watch=%s delivery=%s key=%+v seq=%d", watch.WatchID, selected.DeliveryID, selected.Key, selected.UpdateSeq)
	}
	custody := func() (callbacks, leases int) {
		s.attentionMu.Lock()
		callbacks = s.attentionCallbacks
		s.attentionMu.Unlock()
		if controller := s.retirementController.Load(); controller != nil {
			controller.mu.Lock()
			leases = len(controller.active)
			controller.mu.Unlock()
		}
		return
	}
	diagnostic := func() string {
		callbacks, leases := custody()
		jm.mu.Lock()
		claimed := len(jm.deliveringWatchSends)
		active, generation := jm.watchTransportRetry.active, jm.watchTransportRetry.generation
		jm.mu.Unlock()
		return fmt.Sprintf("phase=%s %s attempts=%d setup-failures=%d timed-attempts=%d callbacks=%d leases=%d claims=%d watch-retry=%v generation=%d", phase.Load(), identity(), attempts.Load(), setupAckFailures.Load(), timedAttempts.Load(), callbacks, leases, claimed, active, generation)
	}
	wait := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("%s %s err=%v", diagnostic(), what, ctx.Err())
		}
	}
	// Registered before any worker starts. LIFO cleanup runs before newMessagingRoot's
	// session shutdown. Release every gate before joining producer/timer workers.
	t.Cleanup(func() {
		releaseInitial()
		releaseRetry()
		releaseWatch()
		if producerStarted {
			select {
			case <-initialDone:
			case <-time.After(10 * time.Second):
				t.Errorf("cleanup producer join timed out: phase=%s %s", phase.Load(), identity())
			}
		}
		drained := make(chan struct{})
		go func() { clock.Drain(); close(drained) }()
		select {
		case <-drained:
			s.testOnlyNotificationProgress.Store(nil)
			jm.appendEvent = originalAppend
		case <-time.After(10 * time.Second):
			t.Errorf("cleanup timer join timed out: phase=%s %s", phase.Load(), identity())
		}
		cancel()
	})
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind != jobstore.EventWatchSendDelivered || e.WatchSend == nil || e.WatchSend.Key.WatchID != watch.WatchID {
			return originalAppend(e)
		}
		selectedMu.Lock()
		if !selectedSet {
			selected = *e.WatchSend
			selectedSet = true
		}
		matches := e.WatchSend.Key == selected.Key && e.WatchSend.DeliveryID == selected.DeliveryID && e.WatchSend.UpdateSeq == selected.UpdateSeq
		selectedMu.Unlock()
		if !matches { // A different frame must not be held by this selected-ID gate.
			return originalAppend(e)
		}
		n := attempts.Add(1)
		unexpected := func() error {
			t.Errorf("unexpected selected acknowledgement: %s", diagnostic())
			return errors.New("unexpected selected acknowledgement phase or custody")
		}
		switch phase.Load().(string) {
		case "selected-producer":
			if n != 1 {
				return unexpected()
			}
			close(initialEntered)
			select {
			case <-initialResume:
				if phase.Load() != "setup-ack-failures" || setupAckFailures.Add(1) != 1 {
					return unexpected()
				}
				return errors.New("selected first eager source acknowledgement fault")
			case <-ctx.Done():
				t.Errorf("phase=%s initial gate timed out %s err=%v", phase.Load(), identity(), ctx.Err())
				return ctx.Err()
			}
		case "setup-ack-failures":
			// onSessionEventKD pumps once for sends, then unconditionally again
			// for notifications. Neither eager pump owns a timer callback.
			callbacks, leases := custody()
			jm.mu.Lock()
			_, claim := jm.deliveringWatchSends[e.WatchSend.DeliveryID]
			active := jm.watchTransportRetry.active
			jm.mu.Unlock()
			if n != 2 || callbacks != 0 || leases != 0 || !claim || !active || setupAckFailures.Load() != 1 {
				return unexpected()
			}
			setupAckFailures.Add(1)
			return errors.New("selected second eager source acknowledgement fault")
		case "both-real-timers":
			// Timer origin is established by the phase after clock advance,
			// retired retry generation, real live claim and actual custody.
			callbacks, leases := custody()
			jm.mu.Lock()
			_, claim := jm.deliveringWatchSends[e.WatchSend.DeliveryID]
			active, generation := jm.watchTransportRetry.active, jm.watchTransportRetry.generation
			jm.mu.Unlock()
			if n != 3 || setupAckFailures.Load() != 2 || active || generation != expectedWatchGeneration || !claim || callbacks < 1 || leases < 1 || !timedAttempts.CompareAndSwap(0, 1) {
				return unexpected()
			}
			close(watchEntered)
			// The main wait owns the deadline. Preserve this unrelated custody
			// until success or cleanup explicitly opens the gate, including on a
			// negative publication check, rather than race that check's deadline.
			<-watchResume
			err := originalAppend(e)
			if err == nil {
				acked.Add(1)
			}
			return err
		default:
			return unexpected()
		}
	}
	phase.Store("selected-producer")
	producerStarted = true
	go func() {
		defer close(initialDone)
		onSessionEventKD(jm, events.EventToolCallEnd, events.ToolCallEndData{
			ToolName: "read_file", CallID: "token-watch-selected-read",
			ArgumentsJSON: `{"file_path":"token-watch-input"}`, Output: "WATCH_TOKEN_PAYLOAD",
		})
	}()
	wait(initialEntered, "initial real source acknowledgement not reached")
	selectedMu.Lock()
	chosen := selected
	selectedMu.Unlock()
	watchID := fmt.Sprintf("watch:%s:%d", chosen.DeliveryID, chosen.UpdateSeq)
	if chosen.Key.WatchID != watch.WatchID || chosen.Key.WatchGeneration == "" || chosen.DeliveryID == "" || chosen.ReceiverSessionID != s.ID() {
		t.Fatalf("phase=%s invalid selected source identity %s receiver=%s", phase.Load(), identity(), chosen.ReceiverSessionID)
	}
	jm.mu.Lock()
	_, initialClaim := jm.deliveringWatchSends[chosen.DeliveryID]
	jm.mu.Unlock()
	pending, err := s.pendingDelegateAttentionIDs()
	hasReceiptPair := len(pending) == 2 && ((pending[0] == id && pending[1] == watchID) || (pending[1] == id && pending[0] == watchID))
	if err != nil || !initialClaim || !hasReceiptPair {
		t.Fatalf("phase=%s claim=%v pending=%v watch-receipt=%s %s err=%v", phase.Load(), initialClaim, pending, watchID, identity(), err)
	}
	phase.Store("actual-response-with-initial-claim")
	fs := installResolutionSyncFailuresWriter(t, s, 1)
	_, responseErr := s.ProcessInputKind(t.Context(), "", nil, EntryNotification)
	if responseErr == nil || fs.injectedFailures() != 1 || len(a.requestsFor("root")) != 1 {
		t.Fatalf("phase=%s %s response err=%v sync-failures=%d requests=%d", phase.Load(), identity(), responseErr, fs.injectedFailures(), len(a.requestsFor("root")))
	}
	fold := rootAttentionFold(t, s)
	_, updateObserved := fold.observed[id]
	_, watchObserved := fold.observed[watchID]
	if len(fold.observed) != 2 || !updateObserved || !watchObserved || !requestsContain(a.requestsFor("root"), "UPDATE_PAYLOAD_913") || !requestsContain(a.requestsFor("root"), "WATCH_TOKEN_PAYLOAD") || attempts.Load() != 1 {
		t.Fatalf("phase=%s %s actual observations=%v requests=%d attempts=%d err=%v", phase.Load(), identity(), fold.observed, len(a.requestsFor("root")), attempts.Load(), responseErr)
	}
	phase.Store("setup-ack-failures")
	releaseInitial()
	wait(initialDone, "initial real producer did not return")
	source, err := jm.store.LoadWatchSends()
	pendingSource := source.Pending[chosen.Key]
	jm.mu.Lock()
	_, stillClaimed := jm.deliveringWatchSends[chosen.DeliveryID]
	watchRetryActive, watchGeneration := jm.watchTransportRetry.active, jm.watchTransportRetry.generation
	jm.mu.Unlock()
	s.mu.Lock()
	canonicalRetryActive := s.notificationSchedule.retry.active
	s.mu.Unlock()
	if err != nil || pendingSource == nil || pendingSource.DeliveryID != chosen.DeliveryID || pendingSource.UpdateSeq != chosen.UpdateSeq || len(source.Pending) != 1 || len(source.Superseded) != 0 || stillClaimed || !watchRetryActive || !canonicalRetryActive || acked.Load() != 0 || attempts.Load() != 2 || setupAckFailures.Load() != 2 || timedAttempts.Load() != 0 {
		t.Fatalf("phase=%s %s source=%+v claimed=%v watch-retry=%v canonical-retry=%v acked=%d err=%v", phase.Load(), identity(), source, stillClaimed, watchRetryActive, canonicalRetryActive, acked.Load(), err)
	}
	callbacksBeforeClock, leasesBeforeClock := custody()
	s.pendingJobNotifsMu.Lock()
	tokenBeforeClock := s.notificationProgress
	s.pendingJobNotifsMu.Unlock()
	if callbacksBeforeClock != 0 || leasesBeforeClock != 0 || tokenBeforeClock != nil {
		t.Fatalf("preclock residue: %s token=%p", diagnostic(), tokenBeforeClock)
	}
	expectedWatchGeneration = watchGeneration
	phase.Store("both-retries-scheduled")
	t.Logf("setup complete: %s watch-receipt=%s two-observed=true requests=1 source-pending=1 canonical-retry=true watch-retry=true attempts=2 setup-ack-failures=2 timed-attempts=0", identity(), watchID)
	c := progressAttachController(t, s, clock)
	progressObserve(s, func(point string, session *Session) {
		if session != s {
			return
		}
		if point == "retry-dispatch" {
			close(retryEntered)
			// The main wait and cleanup own cancellation, not this custody gate.
			<-retryResume
		}
		if point == "custody-return" {
			complete.Store(true)
		}
	})
	s.SetNotifyFunc(func() {
		n := wakes.Add(1)
		s.attentionMu.Lock()
		callbacks := s.attentionCallbacks
		s.attentionMu.Unlock()
		c.mu.Lock()
		leases := len(c.active)
		c.mu.Unlock()
		s.pendingJobNotifsMu.Lock()
		detached := s.notificationProgress == nil
		s.pendingJobNotifsMu.Unlock()
		jm.mu.Lock()
		_, watchClaim := jm.deliveringWatchSends[chosen.DeliveryID]
		jm.mu.Unlock()
		if n != 1 || callbacks != 1 || leases != 1 || !detached || !complete.Load() || !watchClaim || acked.Load() != 0 {
			t.Errorf("phase=%s %s raw publication wakes=%d callbacks=%d leases=%d detached=%v complete-release=%v watch-claim=%v acked=%d", phase.Load(), identity(), n, callbacks, leases, detached, complete.Load(), watchClaim, acked.Load())
		}
		if n == 1 {
			close(published)
		}
	})
	callbacksBeforeClock, leasesBeforeClock = custody()
	if callbacksBeforeClock != 0 || leasesBeforeClock != 0 || attempts.Load() != 2 {
		t.Fatalf("unexpected preclock delivery: %s", diagnostic())
	}
	phase.Store("both-real-timers")
	clock.Advance(jobNotificationRetryInitialDelay)
	wait(watchEntered, "selected watch retry did not reach its source acknowledgement gate")
	wait(retryEntered, "canonical retry did not reach its registered dispatch gate")
	jm.mu.Lock()
	_, watchClaim := jm.deliveringWatchSends[chosen.DeliveryID]
	watchRetryActive = jm.watchTransportRetry.active
	currentWatchGeneration := jm.watchTransportRetry.generation
	jm.mu.Unlock()
	s.mu.Lock()
	canonicalRetryActive = s.notificationSchedule.retry.active
	s.mu.Unlock()
	// Receipt repair may advance the admitted canonical generation. The real
	// timer-only dispatch gate, token pin and custody prove its origin, not
	// generation equality after admission. Watch generation remains exact.
	if canonicalRetryActive {
		t.Fatalf("canonical timer still active at dispatch gate: %s", diagnostic())
	}
	s.pendingJobNotifsMu.Lock()
	token := s.notificationProgress
	users := 0
	if token != nil {
		users = token.users
	}
	s.pendingJobNotifsMu.Unlock()
	s.attentionMu.Lock()
	callbacks := s.attentionCallbacks
	s.attentionMu.Unlock()
	c.mu.Lock()
	leases := len(c.active)
	c.mu.Unlock()
	if !watchClaim || watchRetryActive || currentWatchGeneration != watchGeneration || token == nil || users != 1 || callbacks != 2 || leases != 2 || wakes.Load() != 0 || complete.Load() || attempts.Load() != 3 || setupAckFailures.Load() != 2 || timedAttempts.Load() != 1 {
		t.Fatalf("phase=%s %s claim=%v watch-retry=%v generation=%d/%d token=%p users=%d callbacks=%d leases=%d wakes=%d complete=%v attempts=%d", phase.Load(), identity(), watchClaim, watchRetryActive, currentWatchGeneration, watchGeneration, token, users, callbacks, leases, wakes.Load(), complete.Load(), attempts.Load())
	}
	t.Logf("both timed owners: %s callbacks=2 leases=2 token-users=1 watch-retry-active=false wakes=0", identity())
	phase.Store("canonical-publishes-before-watch-completes")
	releaseRetry()
	wait(published, "canonical raw progress incorrectly waited for unrelated watch custody")
	if attempts.Load() != 3 || wakes.Load() != 1 || acked.Load() != 0 {
		t.Fatalf("phase=%s %s attempts=%d wakes=%d acked=%d", phase.Load(), identity(), attempts.Load(), wakes.Load(), acked.Load())
	}
	t.Logf("independent publication: %s complete-canonical-release=true callbacks=1 leases=1 detached=true wakes=1 source-acked=0", identity())
	phase.Store("finish-watch-and-both-ledgers")
	releaseWatch()
	drained := make(chan struct{})
	go func() { clock.Drain(); close(drained) }()
	wait(drained, "real timer callbacks did not finish")
	requireProgressDisposed(t, s)
	requireProgressCustodyReleased(t, s)
	recovered, err := readDelegateAttentionFold(s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	_, updateObserved = recovered.observed[id]
	_, watchObserved = recovered.observed[watchID]
	if len(recovered.observed) != 2 || !updateObserved || !watchObserved || len(recovered.pendingIDs()) != 0 || recovered.resolutions[id] != delegateAttentionConsumed || recovered.resolutions[watchID] != delegateAttentionConsumed {
		t.Fatalf("phase=%s %s receiver observed=%v pending=%v resolutions=%v", phase.Load(), identity(), recovered.observed, recovered.pendingIDs(), recovered.resolutions)
	}
	source, err = jm.store.LoadWatchSends()
	if err != nil || len(source.Pending) != 0 || len(source.Superseded) != 0 {
		t.Fatalf("phase=%s %s source=%+v err=%v", phase.Load(), identity(), source, err)
	}
	journal, err := jobstore.ReadEvents(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	delivered := 0
	for _, e := range journal {
		if e.Kind == jobstore.EventWatchSendDelivered && e.WatchSend != nil && e.WatchSend.Key == chosen.Key && e.WatchSend.DeliveryID == chosen.DeliveryID && e.WatchSend.UpdateSeq == chosen.UpdateSeq {
			delivered++
		}
	}
	jm.mu.Lock()
	active := jm.watchTransportRetry.active
	claims := len(jm.deliveringWatchSends)
	jm.mu.Unlock()
	if active || claims != 0 || delivered != 1 || acked.Load() != 1 || attempts.Load() != 3 || setupAckFailures.Load() != 2 || timedAttempts.Load() != 1 || len(a.requestsFor("root")) != 1 || wakes.Load() != 1 || pendingUpdateCount(t, s) != 0 || !requestsContain(a.requestsFor("root"), "UPDATE_PAYLOAD_913") || !requestsContain(a.requestsFor("root"), "WATCH_TOKEN_PAYLOAD") {
		t.Fatalf("phase=%s %s retry=%v claims=%d delivered=%d acked=%d attempts=%d requests=%d wakes=%d", phase.Load(), identity(), active, claims, delivered, acked.Load(), attempts.Load(), len(a.requestsFor("root")), wakes.Load())
	}
	t.Logf("settled: %s source-delivered=1 receiver-observed=2 receiver-consumed=2 pending=0 callbacks=0 leases=0 token=nil watch-retry=false requests=1 wakes=1 attempts=3 setup-ack-failures=2 timed-attempts=1 both-payloads=true", identity())
}

func TestNotificationProgressLateParticipantDefeatsOldClaim(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	s, a, id := progressReceipt(t, clock)
	progressAttachController(t, s, clock)
	entered, resume := make(chan struct{}), make(chan struct{})
	unpause := sync.OnceFunc(func() { close(resume) })
	var first atomic.Bool
	var claimed atomic.Int32
	published := make(chan struct{})
	progressObserve(s, func(point string, s *Session) {
		if point == "claim-before" && first.CompareAndSwap(false, true) {
			close(entered)
			<-resume
		}
	})
	cleanupProgressWorkers(t, s, clock, unpause, nil)
	s.SetNotifyFunc(func() {
		requireProgressCustodyReleased(t, s)
		if claimed.Add(1) == 1 {
			close(published)
		}
	})
	clock.Advance(jobNotificationRetryInitialDelay)
	waitForProgress(t, entered, "original last owner did not reach claim")
	// The first user has decremented to zero but not claimed. A new still-current
	// retry must join that identity, not allocate or leave an unpinned interval.
	s.pendingJobNotifsMu.Lock()
	original := s.notificationProgress
	users := original.users
	s.pendingJobNotifsMu.Unlock()
	if users != 0 {
		t.Fatal("old owner did not leave before claim")
	}
	s.scheduleNotificationRetry()
	clock.Advance(jobNotificationRetryInitialDelay)
	waitForProgress(t, published, "joining owner did not discharge shared token")
	unpause()
	clock.Drain()
	requireProgressDisposed(t, s)
	requireRecoveredReceipt(t, s, id)
	if claimed.Load() != 1 || len(a.requestsFor("root")) != 1 {
		t.Fatal("late original claim duplicated the joined handoff")
	}
}
