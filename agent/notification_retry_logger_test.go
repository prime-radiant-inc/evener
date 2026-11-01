package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

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
	var wakeError string
	const delegateID = "dlg_logger_retry"
	const receiver = "child-" + delegateID
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
	if !t.Run("store_only_fixture", func(owner *testing.T) {
		c, _ := newDelegateControllerTestHarness(owner, 2, 1)
		controller = c
		// The outer test owns this deliberately escaped controller. Fence and
		// dispatch its remaining retry before the outer test can finish.
		t.Cleanup(func() {
			c.mu.Lock()
			c.closing = true
			c.mu.Unlock()
			unblock()
			clock.Drain()
			clock.Advance(jobNotificationRetryMaxDelay)
			clock.Drain()
			if got := clock.PendingCallbacks(); got != 0 {
				t.Errorf("callbacks after final cleanup = %d, want 0", got)
			}
		})
		seedDelegateControllerIdle(owner, c, delegateID, "")
		c.rootRuntime = &Session{id: c.rootSessionID, clock: clock, delegateController: c}
		if _, err := c.openDelegateAttention(delegateID, "logger-retry-id"); err != nil {
			owner.Fatal(err)
		}
		// A real missing transcript starts the real receiver's retry. No test
		// directly sets active/generation state or invokes the callback.
		err := c.requestSessionWake(receiver)
		if !errors.Is(err, os.ErrNotExist) {
			owner.Fatalf("initial wake error = %v, want missing transcript", err)
		}
		var pathError *os.PathError
		if !errors.As(err, &pathError) || pathError.Path != filepath.Join(c.stateDir, "sessions", receiver+".transcript.jsonl") {
			owner.Fatalf("initial wake error = %v, want missing receiver transcript path", err)
		}
		wakeError = err.Error()
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

	clock.Advance(jobNotificationRetryInitialDelay)
	waitForTestSignal(t, handler.entered, "retry callback at global logger capture")
	// Releasing the writer and taking the snapshot are sibling operations.
	// Only the shared capture helper synchronizes their buffer accesses.
	unblock()
	_ = logged.String()
	clock.Drain()

	decoder := json.NewDecoder(strings.NewReader(logged.String()))
	var records []map[string]any
	for {
		var record map[string]any
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode captured warning: %v", err)
		}
		records = append(records, record)
	}
	want := []map[string]any{
		{"level": "WARN", "session": controller.rootSessionID, "delegate": delegateID, "error": wakeError},
		{"level": "WARN", "session_id": receiver, "error": wakeError},
	}
	if len(records) != len(want) {
		t.Fatalf("captured records = %v, want exactly one setup and one retry warning", records)
	}
	for i, record := range records {
		// Timestamps and human-readable messages are not semantic oracles.
		delete(record, "time")
		delete(record, "msg")
		if i == 0 {
			if action, ok := record["action"].(string); !ok || action == "" {
				t.Errorf("setup warning has no action attribute: %v", record)
			}
			delete(record, "action") // its text is a human-readable label
		}
		if !reflect.DeepEqual(record, want[i]) {
			t.Errorf("warning %d attributes = %v, want %v", i, record, want[i])
		}
	}
}
