package agent

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func TestDelegateAttentionDriverReadFailureRetriesAcceptedNotification(t *testing.T) {
	t.Parallel()
	for _, guarded := range []bool{false, true} {
		name := "before-claim"
		if guarded {
			name = "under-claim"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newColdDelegateFixture(t, "")
			response := llm.Assistant("driver-retry-response-sentinel")
			fixture.adapter.steps = []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
				func(req llm.Request) llm.Response {
					observationRequest(t, req, []string{"driver-retry-payload-sentinel"}, nil)
					return llm.Response{Message: response}
				},
			}
			clock := agenttest.NewFakeClockAt(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
			root := restoreSupervisionRoot(t, fixture, clock)
			// Keep the root wake loop unstarted: recovery must belong to the
			// child's real installed notify callback and receiver-owned retry.
			sub := warmDelegateUnservedRoot(t, root, fixture)
			waitForStableSupervisionRun(t, root, fixture.childID)
			const id = "attention:driver-read-retry"
			if appended, err := sub.sess.appendDelegateNotificationDurably(id, "driver-retry-payload-sentinel"); err != nil || !appended {
				t.Fatalf("append accepted notification = %t, %v", appended, err)
			}
			// This is the only consumer of the root event stream. Clear warm-run
			// diagnostics before faulting the synchronous production callback.
			for len(root.Events()) != 0 {
				<-root.Events()
			}
			var healthyDispatchReads, driverReads, faults atomic.Int32
			var claimReached, faultEnabled atomic.Bool
			faultEnabled.Store(true)
			updateSessionTestConfig(root, func(cfg *testConfig) {
				cfg.delegateAttentionBeforeDriveClaim = func(got *subagent) {
					if got == sub {
						claimReached.Store(true)
					}
				}
			})
			updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
				cfg.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
					sub.sess.mu.Lock()
					hinted := sub.sess.notificationSchedule.hinted
					sub.sess.mu.Unlock()
					if faultEnabled.Load() {
						if !hinted {
							healthyDispatchReads.Add(1)
						} else {
							driverReads.Add(1)
							if !guarded || claimReached.Load() {
								faults.Add(1)
								return delegateAttentionFold{}, &os.PathError{Op: "read", Path: path, Err: syscall.EIO}
							}
						}
					}
					return readDelegateAttentionFold(path, sessionID)
				}
			})
			if err := sub.sess.requestNotificationWake(); err != nil {
				t.Fatalf("healthy dispatcher failed before installed callback: %v", err)
			}
			wantReads := int32(1)
			if guarded {
				wantReads = 2
			}
			if faults.Load() != 1 || healthyDispatchReads.Load() < 2 || driverReads.Load() != wantReads || claimReached.Load() != guarded {
				t.Fatalf("wrong fault boundary: faults=%d healthy=%d driver=%d claimed=%t", faults.Load(), healthyDispatchReads.Load(), driverReads.Load(), claimReached.Load())
			}
			sub.mu.Lock()
			driving := sub.driving
			sub.mu.Unlock()
			if driving || supervisionRequestCount(fixture.adapter) != 1 {
				t.Fatalf("fault leaked guard or launched request: driving=%t requests=%d", driving, supervisionRequestCount(fixture.adapter))
			}
			warnings := 0
			for len(root.Events()) != 0 {
				event := <-root.Events()
				if warning, ok := event.Data.(events.WarningData); ok {
					warnings++
					if warning.Source != string(diagnostic.SourceEvener) {
						t.Fatalf("read failure attributed outside runtime: %#v", warning)
					}
					t.Logf("captured driver I/O diagnostic: %#v", warning)
				}
			}
			if warnings != 1 {
				t.Fatalf("driver fault warnings=%d, want 1", warnings)
			}
			sub.sess.mu.Lock()
			armed := sub.sess.notificationSchedule.retry.active
			sub.sess.mu.Unlock()
			if !armed {
				t.Fatal("driver read fault stranded accepted notification without receiver retry")
			}
			assertAttentionResponseReceipts(t, sub.sess, id, 0)
			faultEnabled.Store(false)
			clock.Advance(jobNotificationRetryInitialDelay)
			clock.Drain()
			waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
			if supervisionRequestCount(fixture.adapter) != 2 || faults.Load() != 1 {
				t.Fatalf("retry requests=%d faults=%d, want warm plus one real presenting request", supervisionRequestCount(fixture.adapter), faults.Load())
			}
			assertStableSupervisionAttentionReceipt(t, root, sub, id, 2, response)
		})
	}
}
