package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
)

// retryLoggerGateHandler parks only the actual controller warning, identified
// by its structured receiver attribute, before the handler touches the capture.
// Other warning records retain their normal handler behavior.
type retryLoggerGateHandler struct {
	slog.Handler
	receiver string
	entered  chan struct{}
	permit   chan struct{}
	once     sync.Once
}

func (h *retryLoggerGateHandler) Handle(ctx context.Context, record slog.Record) error {
	matching := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "session_id" && attr.Value.String() == h.receiver {
			matching = true
		}
		return true
	})
	if matching {
		h.once.Do(func() { close(h.entered); <-h.permit })
	}
	return h.Handler.Handle(ctx, record)
}

// This deliberately store-only fixture characterizes cross-owner logging, not
// Session.Close cancellation. It does not identify the original CI timer owner.
func TestNotificationRetryLoggerCaptureAcrossFixtureCleanup(t *testing.T) {
	// Not parallel: swaps the process default slog logger.
	clock := agenttest.NewFakeClock()
	var controller *delegateTreeController
	const receiver = "child-dlg_logger_retry"
	if !t.Run("store_only_fixture", func(owner *testing.T) {
		c, _ := newDelegateControllerTestHarness(owner, 2, 1)
		controller = c
		// The outer test owns this deliberately escaped controller. Fence and
		// dispatch its remaining retry before the outer test can finish.
		t.Cleanup(func() {
			c.mu.Lock()
			c.closing = true
			c.mu.Unlock()
			clock.Advance(jobNotificationRetryMaxDelay)
			clock.Drain()
			if got := clock.PendingCallbacks(); got != 0 {
				t.Errorf("callbacks after final cleanup = %d, want 0", got)
			}
		})
		seedDelegateControllerIdle(owner, c, "dlg_logger_retry", "")
		c.rootRuntime = &Session{id: c.rootSessionID, clock: clock, delegateController: c}
		if _, err := c.openDelegateAttention("dlg_logger_retry", "logger-retry-id"); err != nil {
			owner.Fatal(err)
		}
		// A real missing transcript starts the real receiver's retry. No test
		// directly sets active/generation state or invokes the callback.
		if err := c.requestSessionWake(receiver); !errors.Is(err, os.ErrNotExist) {
			owner.Fatalf("initial wake error = %v, want missing transcript", err)
		}
		if got := clock.PendingCallbacks(); got != 1 {
			owner.Fatalf("callbacks before fixture cleanup = %d, want 1", got)
		}
	}) {
		t.Fatal("fixture failed before retry ownership could be checked")
	}
	pendingAfterFixture := clock.PendingCallbacks()
	controller.mu.Lock()
	closing := controller.closing
	controller.mu.Unlock()
	if pendingAfterFixture != 1 || closing {
		t.Fatalf("store-only fixture control: pending=%d closing=%t, want 1/false", pendingAfterFixture, closing)
	}
	t.Logf("store-only fixture ended with callbacks=%d closing=%t, outer test retains cleanup ownership", pendingAfterFixture, closing)

	var logged diagnosticLogCapture
	handler := &retryLoggerGateHandler{
		Handler: slog.NewJSONHandler(&logged, nil), receiver: receiver,
		entered: make(chan struct{}), permit: make(chan struct{}),
	}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	unblock := sync.OnceFunc(func() { close(handler.permit) })
	t.Cleanup(func() {
		unblock()
		clock.Drain()
		slog.SetDefault(previous)
	})
	clock.Advance(jobNotificationRetryInitialDelay)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-handler.entered:
	case <-timer.C:
		t.Fatal("retry did not reach the global logger capture")
	}
	// Releasing the writer and taking the snapshot are sibling operations.
	// Only the shared capture helper synchronizes their buffer accesses.
	unblock()
	_ = logged.String()
	clock.Drain()

	decoder := json.NewDecoder(strings.NewReader(logged.String()))
	warnings := 0
	for {
		var record map[string]any
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode captured warning: %v", err)
		}
		if record["msg"] != "notification wake failed" {
			continue
		}
		warnings++
		if record["level"] != "WARN" || record["session_id"] != receiver {
			t.Errorf("retry warning attributes = %v, want WARN for %s", record, receiver)
		}
		errText, _ := record["error"].(string)
		if !strings.Contains(errText, receiver+".transcript.jsonl") {
			t.Errorf("retry warning error = %q, want missing receiver transcript", errText)
		}
	}
	if warnings != 1 {
		t.Fatalf("captured retry warnings = %d, want 1", warnings)
	}
}
