package appwire_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/appwire/appwiretest"
)

type fakeCoordinator struct {
	mu      sync.Mutex
	entries []*fakeHandle
}

type fakeHandle struct {
	method string
	text   string
	failed bool
	reason string
}

func (h *fakeHandle) Fail(reason string) { h.failed = true; h.reason = reason }

func (f *fakeCoordinator) Register(method, text string) appwire.PendingHandle {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := &fakeHandle{method: method, text: text}
	f.entries = append(f.entries, h)
	return h
}

func TestTurnSteer_RegistersPending_AndFailsOnRPCError(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	coord := &fakeCoordinator{}
	client.SetPendingCoordinator(coord)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	go func() {
		req := <-transport.Sent()
		transport.DeliverError(req.Request.ID, appwire.CodeUnavailable, "steer is not available for this session")
	}()

	err := client.TurnSteer(ctx, appwire.TurnSteerParams{
		Ref:  appwire.Ref{SourceID: "local", ThreadID: "t1"}.String(),
		Text: "hold on, look at this first",
	})
	if err == nil {
		t.Fatalf("TurnSteer should have returned the RPC error")
	}
	if !strings.Contains(err.Error(), "steer is not available") {
		t.Fatalf("error mismatch: %v", err)
	}

	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.entries) != 1 {
		t.Fatalf("expected 1 pending entry, got %d", len(coord.entries))
	}
	e := coord.entries[0]
	if e.method != "turn/steer" {
		t.Fatalf("method = %q, want turn/steer", e.method)
	}
	if e.text != "hold on, look at this first" {
		t.Fatalf("text = %q", e.text)
	}
	if !e.failed {
		t.Fatalf("handle was not marked failed")
	}
	if !strings.Contains(e.reason, "steer is not available") {
		t.Fatalf("fail reason = %q", e.reason)
	}
}

func TestTurnSteer_RegistersPending_NoFailOnRPCSuccess(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	coord := &fakeCoordinator{}
	client.SetPendingCoordinator(coord)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	go func() {
		req := <-transport.Sent()
		transport.DeliverResponse(req.Request.ID, struct{}{})
	}()

	if err := client.TurnSteer(ctx, appwire.TurnSteerParams{
		Ref:  appwire.Ref{SourceID: "local", ThreadID: "t1"}.String(),
		Text: "go ahead",
	}); err != nil {
		t.Fatalf("TurnSteer: %v", err)
	}

	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.entries) != 1 {
		t.Fatalf("expected 1 pending entry, got %d", len(coord.entries))
	}
	if coord.entries[0].failed {
		t.Fatalf("handle was marked failed on success")
	}
}

// With no coordinator set, TurnSteer behaves exactly as before.
func TestTurnSteer_NoCoordinator_PassThrough(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)
	go func() {
		req := <-transport.Sent()
		transport.DeliverResponse(req.Request.ID, struct{}{})
	}()
	if err := client.TurnSteer(ctx, appwire.TurnSteerParams{Ref: "local:t1", Text: "x"}); err != nil {
		t.Fatalf("TurnSteer: %v", err)
	}
}

var _ = fmt.Sprintf
