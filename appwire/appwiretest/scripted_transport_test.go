package appwiretest_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/appwire/appwiretest"
)

func TestScriptedTransport_ResponseAndNotification(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	go func() {
		req := <-transport.Sent()
		// ST-001: verify method and params are forwarded, not just the ID.
		if req.Request.Method != "test/echo" {
			t.Errorf("request method: got %q, want %q", req.Request.Method, "test/echo")
		}
		var params map[string]any
		if err := json.Unmarshal(req.Request.Params, &params); err != nil {
			t.Errorf("unmarshal params: %v", err)
		} else if params["x"] != float64(1) {
			t.Errorf("params[x]: got %v, want 1", params["x"])
		}
		transport.DeliverResponse(req.Request.ID, map[string]any{"ok": true})
	}()

	var out map[string]any
	if err := client.Request(ctx, "test/echo", map[string]any{"x": 1}, &out); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("response: %v", out)
	}

	transport.DeliverNotification(appwire.Notification{Method: "demo/event"})
	select {
	case n := <-client.Notifications():
		if n.Method != "demo/event" {
			t.Fatalf("notification: %s", n.Method)
		}
	case <-time.After(100 * time.Millisecond): // ST-004: delivery is synchronous; 100ms is ample
		t.Fatal("notification not received")
	}
}

// TestScriptedTransport_DeliverError verifies that DeliverError causes
// client.Request to return a non-nil WireError with the expected code and
// message. This exercises the primary error-response path that downstream
// consumers rely on. (ST-002)
func TestScriptedTransport_DeliverError(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	go func() {
		req := <-transport.Sent()
		transport.DeliverError(req.Request.ID, -32600, "bad")
	}()

	var out map[string]any
	err := client.Request(ctx, "test/error", nil, &out)
	if err == nil {
		t.Fatal("expected error from Request, got nil")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("expected WireError, got %T: %v", err, err)
	}
	if wire.Code != -32600 {
		t.Errorf("error code: got %d, want -32600", wire.Code)
	}
	if !strings.Contains(wire.Message, "bad") {
		t.Errorf("error message: got %q, want to contain %q", wire.Message, "bad")
	}
}

// TestScriptedTransport_Close verifies that Close() unblocks Recv (causing
// client.Notifications to be closed), that double-close is a no-op, and that
// a subsequent Request returns an error. (ST-003)
func TestScriptedTransport_Close(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	// Double-close must be a no-op (idempotency guard).
	if err := transport.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("second Close (idempotency): %v", err)
	}

	// The client's recv goroutine detects the closed inbound channel and closes
	// Notifications(). Wait up to 100 ms for the goroutine to be scheduled.
	select {
	case _, ok := <-client.Notifications():
		if ok {
			t.Fatal("Notifications() channel should be closed after transport.Close()")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Notifications() channel not closed after transport.Close()")
	}

	// Any subsequent Request must fail because the transport is closed.
	var out map[string]any
	if err := client.Request(ctx, "test/noop", nil, &out); err == nil {
		t.Fatal("expected error from Request after Close, got nil")
	}
}
