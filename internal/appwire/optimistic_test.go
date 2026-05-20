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
	ref    string
	failed bool
	reason string
}

func (h *fakeHandle) Fail(reason string) { h.failed = true; h.reason = reason }

func (f *fakeCoordinator) Register(method, text, ref string) appwire.PendingHandle {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := &fakeHandle{method: method, text: text, ref: ref}
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
		Ref:            appwire.Ref{SourceID: "local", ThreadID: "t1"}.String(),
		ExpectedTurnID: "turn_1",
		Input:          []appwire.InputItem{{Type: "text", Text: "hold on, look at this first"}},
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
	if e.ref != "local:t1" {
		t.Fatalf("ref = %q, want local:t1", e.ref)
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
		Ref:            appwire.Ref{SourceID: "local", ThreadID: "t1"}.String(),
		ExpectedTurnID: "turn_1",
		Input:          []appwire.InputItem{{Type: "text", Text: "go ahead"}},
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
	if err := client.TurnSteer(ctx, appwire.TurnSteerParams{Ref: "local:t1", ExpectedTurnID: "turn_1", Input: []appwire.InputItem{{Type: "text", Text: "x"}}}); err != nil {
		t.Fatalf("TurnSteer: %v", err)
	}
}

func TestTurnStart_DoesNotRegisterPending(t *testing.T) {
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
		transport.DeliverError(req.Request.ID, appwire.CodeInternalError, "boom")
	}()
	_, err := client.TurnStart(ctx, appwire.TurnStartParams{
		Ref:   appwire.Ref{SourceID: "local", ThreadID: "t1"}.String(),
		Input: []appwire.InputItem{{Type: "text", Text: "first message"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.entries) != 0 {
		t.Fatalf("turn/start should use caller local echo, got pending entries=%+v", coord.entries)
	}
}

func TestTurnQueue_DoesNotRegisterPending(t *testing.T) {
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
	if err := client.TurnQueue(ctx, appwire.TurnQueueParams{
		Ref:   appwire.Ref{SourceID: "local", ThreadID: "t1"}.String(),
		Input: []appwire.InputItem{{Type: "text", Text: "queued msg"}},
	}); err != nil {
		t.Fatalf("TurnQueue: %v", err)
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.entries) != 0 {
		t.Fatalf("TurnQueue should not register transcript pending entries, got %+v", coord.entries)
	}
}

func TestTurnDrainAsSteer_RegistersPending_TextEmpty(t *testing.T) {
	// Drain has no text intent — the wrapper still registers with text=""
	// so the coordinator can render the drain-special chip.
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
	if err := client.TurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{
		Ref: appwire.Ref{SourceID: "local", ThreadID: "t1"}.String(),
	}); err != nil {
		t.Fatalf("TurnDrainAsSteer: %v", err)
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.entries) != 1 || coord.entries[0].method != "turn/drainAsSteer" {
		t.Fatalf("entry: %+v", coord.entries)
	}
	if coord.entries[0].ref != "local:t1" {
		t.Fatalf("ref = %q, want local:t1", coord.entries[0].ref)
	}
}

var _ = fmt.Sprintf
