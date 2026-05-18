package appwiretest_test

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/appwire/appwiretest"
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
	case <-time.After(time.Second):
		t.Fatal("notification not received")
	}
}
