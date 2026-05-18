# Optimistic Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every conversation-affecting click (turn/start, turn/queue, turn/steer, turn/drainAsSteer) a pulsing in-progress visual that resolves to reconciled or visibly-failed, in both renderers, and close the underlying silent-drop bug (kata wymv).

**Architecture:** A `PendingCoordinator` interface lives in `internal/appwire`. The Go appwire client's `Turn*` methods invoke `coordinator.Register(method, text) → handle` before the JSON-RPC call and `handle.Fail(reason)` on RPC error. The renderer (TUI hubModel, web SerfAppwirePending) implements the coordinator, renders the visual, and reconciles authoritative events inside its own existing notification path. The web side mirrors the Go shape in JavaScript. The daemon-side bug is fixed by gating `caps.Steer` on `processing` so external steer calls in IDLE / AWAITING reject cleanly through the new path.

**Tech Stack:** Go 1.22+ (daemon, appwire client, TUI). Vanilla JS in IIFE-modules (`window.SerfAppwire`, `window.SerfComposerAttachments` style). Bubble Tea + bubbles/spinner for TUI animation. JSDOM-based jstest harness for web. Existing `roborev` review tooling.

**Spec:** `docs/superpowers/specs/2026-05-18-optimistic-rendering-design.md`

---

## File map (full surface)

```
server/appwire_runtime.go                         modify  one-line cap gate
server/appwire_runtime_test.go                    modify  add steer-cap state table

internal/appwire/optimistic.go                    new     PendingCoordinator + PendingHandle interfaces
internal/appwire/client.go                        modify  TurnStart/TurnSteer/TurnQueue/TurnDrainAsSteer wrap register/fail; SetPendingCoordinator
internal/appwire/optimistic_test.go               new     unit tests for the wrap behavior with a fake coordinator

internal/appwire/appwiretest/scripted_transport.go    new     exported ScriptedTransport for external-package tests
internal/appwire/appwiretest/scripted_transport_test.go  new  smoke test

cmd/serf-tui/pending.go                           new     pendingCoordinator + pendingEntry; spinner glue; Msg types
cmd/serf-tui/pending_test.go                      new     coordinator behavior unit tests
cmd/serf-tui/hub_transcript_reducer.go            modify  chatMessage gets Pending/Failed/FailedReason + per-entry spinner reference
cmd/serf-tui/composer_panel.go                    modify  render Pending/Failed states inline
cmd/serf-tui/hub_model.go                         modify  wire pendingCoordinator to client; applyHubNotification → tryReconcile after reducer apply
cmd/serf-tui/optimistic_test.go                   new     end-to-end wrapper unit tests (using ScriptedTransport)

cmd/serf-hub/assets/style.css                     modify  .optimistic-pending / .optimistic-failed / .optimistic-retry / @keyframes optimistic-pulse
cmd/serf-hub/assets/appwire.js                    modify  optimisticCall helper + SerfAppwire.pending hook; wrap startTurn/queueTurn/steer/drainAsSteer
cmd/serf-hub/assets/pending.js                    new     SerfAppwirePending registry (DOM render + tryReconcile) — own IIFE module loaded before renderer
cmd/serf-hub/assets/renderer.js                   modify  register pending registry with SerfAppwire; deliverNotification calls pending.tryReconcile after reducer dispatch
cmd/serf-hub/templates/partials/workspace.html    modify  load assets/pending.js before renderer.js
cmd/serf-hub/jstest/test-optimistic-rendering.js  new     wrapper unit tests with injected fake transport

test/scenarios/web-steer-in-idle-fails-fast.md         new
test/scenarios/web-steer-success-reconciles.md         new
test/scenarios/tui-steer-in-idle-fails-fast.md         new
test/scenarios/tui-steer-success-reconciles.md         new

test/scenarios/INDEX.md                           modify  link the four new scenarios

docs/qa/to-ask-jesse.md                           modify  cross-link the plan + close gnmv-adjacent notes
```

---

## Task 1: Daemon `Steer` capability gate (closes kata wymv)

**Files:**
- Modify: `server/appwire_runtime.go:617`
- Test: `server/appwire_runtime_test.go` (append new test)

- [ ] **Step 1: Write the failing test**

Append to `server/appwire_runtime_test.go`:

```go
func TestAppCapabilities_SteerGatedOnProcessing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		state      string
		processing bool
		setSteer   bool
		wantSteer  bool
	}{
		{"processing with steerFunc", "PROCESSING", true, true, true},
		{"idle with steerFunc", "IDLE", false, true, false},
		{"awaiting with steerFunc", "AWAITING_INPUT", false, true, false},
		{"closed with steerFunc", "CLOSED", false, true, false},
		{"processing without steerFunc", "PROCESSING", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer()
			if tc.setSteer {
				s.SetSteerFunc(func(string) {})
			}
			got := s.appCapabilities(tc.state, tc.processing)
			if got.Steer != tc.wantSteer {
				t.Fatalf("Steer = %v, want %v", got.Steer, tc.wantSteer)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to confirm RED**

```bash
go test ./server -run TestAppCapabilities_SteerGatedOnProcessing -count=1 -v
```

Expected: FAIL — the idle/awaiting/closed subcases assert `Steer=false` but current code returns `true` because `Steer: s.steerFunc != nil`.

- [ ] **Step 3: Apply the fix**

Edit `server/appwire_runtime.go:617`:

```go
// BEFORE
Steer:        s.steerFunc != nil,

// AFTER
Steer:        s.steerFunc != nil && processing && !closed,
```

- [ ] **Step 4: Run test to confirm GREEN**

```bash
go test ./server -run TestAppCapabilities_SteerGatedOnProcessing -count=1 -v
```

Expected: PASS for all five subcases.

- [ ] **Step 5: Run the broader server tests**

```bash
go test ./server -count=1 -timeout 60s
```

Expected: all pass. If any existing test asserted `caps.Steer = true` in a non-processing scenario, fix the assertion — that test was encoding the bug.

- [ ] **Step 6: Commit**

```bash
git add server/appwire_runtime.go server/appwire_runtime_test.go
git commit -m "$(cat <<'EOF'
appwire: gate caps.Steer on processing (kata wymv)

Send/Queue/Clear properly gate on processing+closed; Steer was always
true whenever steerFunc was non-nil. Hub-side ensureThreadActionAvailable
accepted steer/drainAsSteer in IDLE/AWAITING_INPUT, daemon's Session.Steer
silently queued the message into steeringQueue, and the queue was only
drained when the next turn ran — leaving the user with no transcript
entry, no event, and no banner.

This single-line gate makes the hub reject external steer calls in
non-processing states with Unavailable, so the renderer surfaces the
error path the optimistic-rendering pattern is designed to display.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Exported `ScriptedTransport` test helper

**Files:**
- Create: `internal/appwire/appwiretest/scripted_transport.go`
- Create: `internal/appwire/appwiretest/scripted_transport_test.go`

The package `internal/appwire` already has a private `memoryTransport` in its own `client_test.go`. We need an exported equivalent so `cmd/serf-tui` external-package tests can drive the client through a fake transport. Leaves the private fake alone.

- [ ] **Step 1: Write the failing smoke test**

Create `internal/appwire/appwiretest/scripted_transport_test.go`:

```go
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
		transport.DeliverResponse(req.ID, map[string]any{"ok": true})
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
```

- [ ] **Step 2: Run test to confirm RED**

```bash
go test ./internal/appwire/appwiretest -count=1 -v
```

Expected: FAIL with `no Go files in .../appwiretest` (the package doesn't exist).

- [ ] **Step 3: Create the helper**

Create `internal/appwire/appwiretest/scripted_transport.go`:

```go
// Package appwiretest exposes test helpers for driving appwire.Client
// from external packages. The private memoryTransport in
// internal/appwire's own _test.go cannot be reused from cmd/serf-tui,
// so this package provides an equivalent with an exported API.
package appwiretest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"primeradiant.com/serf/internal/appwire"
)

// ScriptedTransport is a fake appwire.Transport whose Send calls are
// observable on the Sent() channel and whose Recv calls block until
// the test delivers a response or notification.
type ScriptedTransport struct {
	mu      sync.Mutex
	sent    chan appwire.Message
	inbound chan appwire.Message
	closed  bool
}

func NewScriptedTransport() *ScriptedTransport {
	return &ScriptedTransport{
		sent:    make(chan appwire.Message, 32),
		inbound: make(chan appwire.Message, 32),
	}
}

func (s *ScriptedTransport) Send(_ context.Context, msg appwire.Message) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("transport closed")
	}
	s.mu.Unlock()
	s.sent <- msg
	return nil
}

func (s *ScriptedTransport) Recv(ctx context.Context) (appwire.Message, error) {
	select {
	case msg, ok := <-s.inbound:
		if !ok {
			return appwire.Message{}, errors.New("transport closed")
		}
		return msg, nil
	case <-ctx.Done():
		return appwire.Message{}, ctx.Err()
	}
}

func (s *ScriptedTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.inbound)
	return nil
}

// Sent returns a receive-only channel of messages the client wrote.
// Tests read from this to observe outgoing requests and pick the
// correct ID for DeliverResponse.
func (s *ScriptedTransport) Sent() <-chan appwire.Message { return s.sent }

// DeliverResponse synthesizes a JSON-RPC response message for the
// given request ID and pushes it through the transport's Recv path.
func (s *ScriptedTransport) DeliverResponse(id appwire.ID, result any) {
	raw, _ := json.Marshal(result)
	s.inbound <- appwire.Message{ID: id, Result: raw}
}

// DeliverError synthesizes a JSON-RPC error response for the given
// request ID and pushes it through Recv.
func (s *ScriptedTransport) DeliverError(id appwire.ID, code int, message string) {
	s.inbound <- appwire.Message{ID: id, Error: &appwire.Error{Code: code, Message: message}}
}

// DeliverNotification pushes a notification through Recv. The client's
// Start goroutine pumps it onto Notifications().
func (s *ScriptedTransport) DeliverNotification(n appwire.Notification) {
	s.inbound <- appwire.Message{Notification: &n}
}
```

- [ ] **Step 4: Run test to confirm GREEN**

```bash
go test ./internal/appwire/appwiretest -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Vet + build**

```bash
go vet ./internal/appwire/...
go build ./internal/appwire/...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/appwire/appwiretest/
git commit -m "$(cat <<'EOF'
appwire: exported ScriptedTransport test helper

External-package tests (cmd/serf-tui, cmd/serf-hub) need to drive
appwire.Client through a fake transport. The private memoryTransport
in client_test.go is package-scoped. Add an exported equivalent in
internal/appwire/appwiretest with DeliverResponse, DeliverError, and
DeliverNotification — symmetric to what the real WebSocket transport
puts on the wire.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `PendingCoordinator` interface in appwire + RPC-error path for `TurnSteer`

**Files:**
- Create: `internal/appwire/optimistic.go`
- Modify: `internal/appwire/client.go`
- Test: `internal/appwire/optimistic_test.go`

This task introduces the optimistic hook interface and wires it through `TurnSteer` only. Tasks 4 wires the rest of the Turn* methods after this pattern is proven.

- [ ] **Step 1: Write the failing test**

Create `internal/appwire/optimistic_test.go`:

```go
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
		transport.DeliverError(req.ID, appwire.CodeUnavailable, "steer is not available for this session")
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
		transport.DeliverResponse(req.ID, struct{}{})
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

// Confirm: with no coordinator set, TurnSteer behaves exactly as before.
func TestTurnSteer_NoCoordinator_PassThrough(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)
	go func() {
		req := <-transport.Sent()
		transport.DeliverResponse(req.ID, struct{}{})
	}()
	if err := client.TurnSteer(ctx, appwire.TurnSteerParams{Ref: "local|t1", Text: "x"}); err != nil {
		t.Fatalf("TurnSteer: %v", err)
	}
}

var _ = fmt.Sprintf
```

- [ ] **Step 2: Run test to confirm RED**

```bash
go test ./internal/appwire -run TestTurnSteer_Registers -count=1 -v
```

Expected: FAIL — `client.SetPendingCoordinator` and `appwire.PendingHandle` do not exist.

- [ ] **Step 3: Create the interface file**

Create `internal/appwire/optimistic.go`:

```go
package appwire

// PendingCoordinator is the callback hook the client uses to inform a
// renderer that an outgoing optimistic request was issued. The
// coordinator is responsible for drawing the pending visual, scheduling
// the event-arrival timeout, and reconciling authoritative events
// through tryReconcile in the renderer's own notification path.
//
// Set via Client.SetPendingCoordinator. When the coordinator is nil,
// the Turn* methods pass through unchanged.
type PendingCoordinator interface {
	// Register is called immediately before the JSON-RPC request is
	// issued. The returned PendingHandle gives the client a way to
	// signal RPC-level failure (network error, hub Unavailable).
	// The coordinator owns the timeout, the reconciliation, and the
	// authoritative confirmation lifecycle.
	Register(method, text string) PendingHandle
}

// PendingHandle is the per-call lifecycle handle returned by
// PendingCoordinator.Register.
type PendingHandle interface {
	// Fail marks the pending entry as failed if it has not already
	// been reconciled. Idempotent.
	Fail(reason string)
}
```

- [ ] **Step 4: Wire `TurnSteer`**

Edit `internal/appwire/client.go`:

Add to the `Client` struct fields (around line 21):

```go
	pendingCoord PendingCoordinator
```

Add right after the `Close()` method (around line 84):

```go
// SetPendingCoordinator installs an optimistic-rendering coordinator
// that observes the four conversation-affecting Turn* methods. Pass
// nil to disable. Safe to call before Start.
func (c *Client) SetPendingCoordinator(pc PendingCoordinator) {
	c.pendingCoord = pc
}
```

Replace the existing `TurnSteer`:

```go
func (c *Client) TurnSteer(ctx context.Context, params TurnSteerParams) error {
	var handle PendingHandle
	if c.pendingCoord != nil {
		handle = c.pendingCoord.Register(MethodTurnSteer, params.Text)
	}
	err := c.request(ctx, MethodTurnSteer, params, nil)
	if err != nil && handle != nil {
		handle.Fail(err.Error())
	}
	return err
}
```

- [ ] **Step 5: Run test to confirm GREEN**

```bash
go test ./internal/appwire -run TestTurnSteer_Registers -count=1 -v
go test ./internal/appwire -run TestTurnSteer_NoCoordinator -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Full appwire suite**

```bash
go test ./internal/appwire/... -count=1
```

Expected: all pass — existing tests don't set a coordinator so they use the pass-through path.

- [ ] **Step 7: Commit**

```bash
git add internal/appwire/optimistic.go internal/appwire/client.go internal/appwire/optimistic_test.go
git commit -m "$(cat <<'EOF'
appwire: PendingCoordinator hook + TurnSteer wraps it

Define PendingCoordinator + PendingHandle interfaces. The renderer
implements them; the client calls Register before the RPC and Fail on
RPC error. With no coordinator set (everything except the TUI
production path today), the Turn* methods pass through unchanged.

This commit wires the hook through TurnSteer only. The remaining
TurnStart / TurnQueue / TurnDrainAsSteer follow the same pattern in
the next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Extend the wrap to `TurnStart`, `TurnQueue`, `TurnDrainAsSteer`

**Files:**
- Modify: `internal/appwire/client.go`
- Modify: `internal/appwire/optimistic_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/appwire/optimistic_test.go`:

```go
func TestTurnStart_RegistersPending_AndFailsOnRPCError(t *testing.T) {
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
		transport.DeliverError(req.ID, appwire.CodeInternalError, "boom")
	}()
	_, err := client.TurnStart(ctx, appwire.TurnStartParams{
		Ref:  "local|t1",
		Text: "first message",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.entries) != 1 || coord.entries[0].method != "turn/start" {
		t.Fatalf("entry: %+v", coord.entries)
	}
	if !coord.entries[0].failed {
		t.Fatal("expected failed")
	}
}

func TestTurnQueue_RegistersPending(t *testing.T) {
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
		transport.DeliverResponse(req.ID, struct{}{})
	}()
	if err := client.TurnQueue(ctx, appwire.TurnQueueParams{
		Ref:  "local|t1",
		Text: "queued msg",
	}); err != nil {
		t.Fatalf("TurnQueue: %v", err)
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.entries) != 1 || coord.entries[0].method != "turn/queue" {
		t.Fatalf("entry: %+v", coord.entries)
	}
	if coord.entries[0].text != "queued msg" {
		t.Fatalf("text mismatch")
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
		transport.DeliverResponse(req.ID, struct{}{})
	}()
	if err := client.TurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{Ref: "local|t1"}); err != nil {
		t.Fatalf("TurnDrainAsSteer: %v", err)
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.entries) != 1 || coord.entries[0].method != "turn/drainAsSteer" {
		t.Fatalf("entry: %+v", coord.entries)
	}
}
```

- [ ] **Step 2: Run tests to confirm RED**

```bash
go test ./internal/appwire -run 'TestTurnStart_Registers|TestTurnQueue_Registers|TestTurnDrainAsSteer_Registers' -count=1 -v
```

Expected: FAIL — the Turn{Start,Queue,DrainAsSteer} methods don't register or fail yet.

- [ ] **Step 3: Wire the remaining methods**

Edit `internal/appwire/client.go`. Replace the existing `TurnStart`, `TurnQueue`, `TurnDrainAsSteer` bodies. Each follows the same pattern as Task 3's TurnSteer:

```go
func (c *Client) TurnStart(ctx context.Context, params TurnStartParams) (TurnStartResponse, error) {
	var handle PendingHandle
	if c.pendingCoord != nil {
		handle = c.pendingCoord.Register(MethodTurnStart, params.Text)
	}
	var resp TurnStartResponse
	err := c.request(ctx, MethodTurnStart, params, &resp)
	if err != nil && handle != nil {
		handle.Fail(err.Error())
	}
	return resp, err
}

func (c *Client) TurnQueue(ctx context.Context, params TurnQueueParams) error {
	var handle PendingHandle
	if c.pendingCoord != nil {
		handle = c.pendingCoord.Register(MethodTurnQueue, params.Text)
	}
	err := c.request(ctx, MethodTurnQueue, params, nil)
	if err != nil && handle != nil {
		handle.Fail(err.Error())
	}
	return err
}

func (c *Client) TurnDrainAsSteer(ctx context.Context, params TurnDrainAsSteerParams) error {
	var handle PendingHandle
	if c.pendingCoord != nil {
		handle = c.pendingCoord.Register(MethodTurnDrainAsSteer, "")
	}
	err := c.request(ctx, MethodTurnDrainAsSteer, params, nil)
	if err != nil && handle != nil {
		handle.Fail(err.Error())
	}
	return err
}
```

- [ ] **Step 4: Run tests to confirm GREEN**

```bash
go test ./internal/appwire -count=1
```

Expected: all pass including the three new tests and the existing TurnSteer tests.

- [ ] **Step 5: Commit**

```bash
git add internal/appwire/client.go internal/appwire/optimistic_test.go
git commit -m "$(cat <<'EOF'
appwire: extend PendingCoordinator hook to TurnStart, TurnQueue, TurnDrainAsSteer

Mirrors the TurnSteer wrap: Register before the RPC, Fail on error.
Drain registers with text="" so the renderer can render the special
collapsed-chip without grepping the params.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: TUI `pendingCoordinator` (state + spinner + timeout)

**Files:**
- Create: `cmd/serf-tui/pending.go`
- Create: `cmd/serf-tui/pending_test.go`

The coordinator holds pending state and exposes:
- `Register(method, text) PendingHandle` (satisfies `appwire.PendingCoordinator`)
- `TryReconcile(method, text) bool` (called from `applyHubNotification` after the reducer)
- A `tea.Msg` stream so the bubbletea model can redraw on state transitions
- A clock-injectable 10s timeout

- [ ] **Step 1: Write the failing tests**

Create `cmd/serf-tui/pending_test.go`:

```go
package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeClock implements pendingClock for deterministic timeout tests.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*fakeTimer
}

type fakeTimer struct {
	c    *fakeClock
	fire time.Time
	fn   func()
	done bool
}

func (f *fakeTimer) Stop() bool {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if f.done {
		return false
	}
	f.done = true
	return true
}

func (c *fakeClock) AfterFunc(d time.Duration, fn func()) pendingTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, fire: c.now.Add(d), fn: fn}
	c.pending = append(c.pending, t)
	return t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	fires := []*fakeTimer{}
	for _, t := range c.pending {
		if !t.done && !t.fire.After(c.now) {
			t.done = true
			fires = append(fires, t)
		}
	}
	c.mu.Unlock()
	for _, t := range fires {
		t.fn()
	}
}

func drainMessages(ch <-chan tea.Msg, n int, timeout time.Duration) []tea.Msg {
	out := []tea.Msg{}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for len(out) < n {
		select {
		case m := <-ch:
			out = append(out, m)
		case <-deadline.C:
			return out
		}
	}
	return out
}

func TestPendingCoordinator_RegisterEmitsRegisteredMsg(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })

	h := p.Register("turn/steer", "look at this")
	if h == nil {
		t.Fatal("Register returned nil")
	}

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 registered msg, got %d", len(got))
	}
	reg, ok := got[0].(pendingRegisteredMsg)
	if !ok {
		t.Fatalf("got %T, want pendingRegisteredMsg", got[0])
	}
	if reg.entry.Method != "turn/steer" || reg.entry.Text != "look at this" {
		t.Fatalf("entry: %+v", reg.entry)
	}
	if !reg.entry.Pending {
		t.Fatal("new entry should be Pending=true")
	}
}

func TestPendingCoordinator_FailEmitsFailedMsg(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	h := p.Register("turn/steer", "x")
	drainMessages(msgs, 1, 100*time.Millisecond) // consume Registered

	h.Fail("steer is not available for this session")

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 failed msg, got %d", len(got))
	}
	fm, ok := got[0].(pendingFailedMsg)
	if !ok {
		t.Fatalf("got %T", got[0])
	}
	if !strings.Contains(fm.reason, "not available") {
		t.Fatalf("reason: %q", fm.reason)
	}
	if !fm.entry.Failed {
		t.Fatal("entry should be Failed=true")
	}
	if fm.entry.Pending {
		t.Fatal("entry should not be Pending after Fail")
	}
}

func TestPendingCoordinator_TimeoutMarksFailed(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	p.Register("turn/steer", "x")
	drainMessages(msgs, 1, 100*time.Millisecond) // Registered

	clock.Advance(11 * time.Second)

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 failed msg from timeout, got %d", len(got))
	}
	fm, ok := got[0].(pendingFailedMsg)
	if !ok {
		t.Fatalf("got %T", got[0])
	}
	if !strings.Contains(fm.reason, "server did not confirm") {
		t.Fatalf("reason: %q", fm.reason)
	}
}

func TestPendingCoordinator_TryReconcile_MatchesByMethodAndText(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	p.Register("turn/steer", "look at this")
	drainMessages(msgs, 1, 100*time.Millisecond) // Registered

	if !p.TryReconcile("turn/steer", "look  at  this") {
		t.Fatal("TryReconcile should match (whitespace-normalized)")
	}

	got := drainMessages(msgs, 1, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 confirmed msg, got %d", len(got))
	}
	cm, ok := got[0].(pendingConfirmedMsg)
	if !ok {
		t.Fatalf("got %T", got[0])
	}
	if cm.entry.Pending || cm.entry.Failed {
		t.Fatalf("confirmed entry should not be pending or failed: %+v", cm.entry)
	}
}

func TestPendingCoordinator_TryReconcile_NoMatchReturnsFalse(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	p.Register("turn/steer", "look at this")
	drainMessages(msgs, 1, 100*time.Millisecond)

	if p.TryReconcile("turn/steer", "completely different text") {
		t.Fatal("TryReconcile should not match unrelated text")
	}
}

func TestPendingCoordinator_FailIsIdempotent(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	msgs := make(chan tea.Msg, 8)
	p := newPendingCoordinator(clock, func(m tea.Msg) { msgs <- m })
	h := p.Register("turn/steer", "x")
	drainMessages(msgs, 1, 100*time.Millisecond)
	h.Fail("a")
	h.Fail("b") // second call must be a no-op
	got := drainMessages(msgs, 2, 100*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected 1 failed msg (idempotent), got %d", len(got))
	}
}
```

- [ ] **Step 2: Run tests to confirm RED**

```bash
go test ./cmd/serf-tui -run TestPendingCoordinator -count=1 -v
```

Expected: FAIL — the package has no `pending.go` yet.

- [ ] **Step 3: Implement the coordinator**

Create `cmd/serf-tui/pending.go`:

```go
package main

import (
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

const pendingTimeout = 10 * time.Second

// pendingClock abstracts time.AfterFunc so tests can drive timeouts
// deterministically via fakeClock.
type pendingClock interface {
	AfterFunc(d time.Duration, fn func()) pendingTimer
}

type pendingTimer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, fn func()) pendingTimer {
	return time.AfterFunc(d, fn)
}

// pendingEntry is the unit of state the coordinator tracks. ID is
// stable per Register call so reducers / view code can address an
// entry across re-renders.
type pendingEntry struct {
	ID      int64
	Method  string
	Text    string
	Pending bool
	Failed  bool
	Reason  string
}

// pendingRegisteredMsg / pendingConfirmedMsg / pendingFailedMsg are
// the tea.Msg types the coordinator emits via the send func. The
// bubbletea model handles them by updating the reducer.
type pendingRegisteredMsg struct{ entry pendingEntry }
type pendingConfirmedMsg struct{ entry pendingEntry }
type pendingFailedMsg struct {
	entry  pendingEntry
	reason string
}

type pendingCoordinator struct {
	mu      sync.Mutex
	clock   pendingClock
	send    func(tea.Msg)
	nextID  int64
	entries map[int64]*pendingEntryState
}

type pendingEntryState struct {
	entry pendingEntry
	timer pendingTimer
}

func newPendingCoordinator(clock pendingClock, send func(tea.Msg)) *pendingCoordinator {
	return &pendingCoordinator{
		clock:   clock,
		send:    send,
		entries: map[int64]*pendingEntryState{},
	}
}

type pendingHandleImpl struct {
	coord *pendingCoordinator
	id    int64
}

func (h *pendingHandleImpl) Fail(reason string) {
	h.coord.failByID(h.id, reason)
}

// Register satisfies appwire.PendingCoordinator.
func (p *pendingCoordinator) Register(method, text string) appwire.PendingHandle {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	entry := pendingEntry{ID: id, Method: method, Text: text, Pending: true}
	state := &pendingEntryState{entry: entry}
	p.entries[id] = state
	state.timer = p.clock.AfterFunc(pendingTimeout, func() {
		p.failByID(id, "server did not confirm")
	})
	p.mu.Unlock()
	p.send(pendingRegisteredMsg{entry: entry})
	return &pendingHandleImpl{coord: p, id: id}
}

// TryReconcile is called by the renderer's notification dispatcher
// after the authoritative reducer update applies. Returns true when a
// pending entry matched and was confirmed. Match: (method == entry.Method)
// AND normalizedText(text) == normalizedText(entry.Text).
func (p *pendingCoordinator) TryReconcile(method, text string) bool {
	want := normalizePendingText(text)
	p.mu.Lock()
	var match *pendingEntryState
	for _, state := range p.entries {
		if !state.entry.Pending {
			continue
		}
		if state.entry.Method != method {
			continue
		}
		if normalizePendingText(state.entry.Text) == want {
			match = state
			break
		}
	}
	if match == nil {
		p.mu.Unlock()
		return false
	}
	match.timer.Stop()
	delete(p.entries, match.entry.ID)
	match.entry.Pending = false
	p.mu.Unlock()
	p.send(pendingConfirmedMsg{entry: match.entry})
	return true
}

func (p *pendingCoordinator) failByID(id int64, reason string) {
	p.mu.Lock()
	state, ok := p.entries[id]
	if !ok || state.entry.Failed || !state.entry.Pending {
		p.mu.Unlock()
		return
	}
	state.timer.Stop()
	state.entry.Pending = false
	state.entry.Failed = true
	state.entry.Reason = reason
	delete(p.entries, id)
	entry := state.entry
	p.mu.Unlock()
	p.send(pendingFailedMsg{entry: entry, reason: reason})
}

func normalizePendingText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
```

- [ ] **Step 4: Run tests to confirm GREEN**

```bash
go test ./cmd/serf-tui -run TestPendingCoordinator -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Vet**

```bash
go vet ./cmd/serf-tui/...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/pending.go cmd/serf-tui/pending_test.go
git commit -m "$(cat <<'EOF'
tui: pendingCoordinator (state, timeouts, reconcile)

Implements appwire.PendingCoordinator for the TUI. Per-entry 10s
timeout via injectable clock. Emits pendingRegistered/Confirmed/Failed
tea.Msg events through the program's Send func so the bubbletea model
re-renders. TryReconcile matches authoritative events by method +
whitespace-normalized text — called from applyHubNotification in the
next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: TUI: extend `chatMessage` + render pending/failed states

**Files:**
- Modify: `cmd/serf-tui/hub_transcript_reducer.go`
- Modify: `cmd/serf-tui/composer_panel.go` (only if pending visuals belong here — check first)

- [ ] **Step 1: Find current chatMessage definition**

```bash
grep -n "type chatMessage\|chatMessage struct" cmd/serf-tui/*.go
```

Expected output identifies the file and line. The fields will look like `Kind`, `Text`, `ItemID`, `TurnIndex`, `Tool`. We're adding three:

- `Pending bool`
- `Failed  bool`
- `Reason  string`

- [ ] **Step 2: Write the failing test**

Append to `cmd/serf-tui/pending_test.go`:

```go
func TestHubReducer_RendersPendingChatMessage(t *testing.T) {
	r := newHubTranscriptReducer(nil, nil, nil)

	// Simulate the model handling a pendingRegisteredMsg by inserting
	// a chatMessage. The reducer doesn't know about coordinator events
	// directly — the hubModel will call this helper.
	r.appendPendingSteering("look at this")

	if len(r.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(r.messages))
	}
	got := r.messages[0]
	if got.Kind != msgSteering {
		t.Fatalf("kind = %v, want msgSteering", got.Kind)
	}
	if !got.Pending {
		t.Fatal("Pending should be true")
	}
	if got.Text != "look at this" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestHubReducer_MarksFailed(t *testing.T) {
	r := newHubTranscriptReducer(nil, nil, nil)
	r.appendPendingSteering("look at this")
	r.markPendingFailed(r.messages[0].PendingID, "boom")

	got := r.messages[0]
	if got.Pending {
		t.Fatal("Pending should be false after fail")
	}
	if !got.Failed {
		t.Fatal("Failed should be true")
	}
	if got.Reason != "boom" {
		t.Fatalf("Reason = %q", got.Reason)
	}
}

func TestHubReducer_RemovesPendingOnConfirm(t *testing.T) {
	r := newHubTranscriptReducer(nil, nil, nil)
	r.appendPendingSteering("look at this")
	id := r.messages[0].PendingID
	r.removePending(id)
	if len(r.messages) != 0 {
		t.Fatal("confirmed entries should be removed; authoritative one renders separately")
	}
}
```

- [ ] **Step 3: Confirm RED**

```bash
go test ./cmd/serf-tui -run TestHubReducer_RendersPendingChatMessage -count=1 -v
```

Expected: FAIL — Pending/Failed/Reason fields and helper methods don't exist.

- [ ] **Step 4: Find msgSteering constant or add it**

```bash
grep -n "msgUser\|msgAssistant\|msgTool\|messageKind" cmd/serf-tui/*.go
```

If `msgSteering` doesn't exist (likely — STEERING is rendered differently today), add it to the existing `messageKind` enum. Locate the iota block and append `msgSteering`.

- [ ] **Step 5: Extend chatMessage struct**

Find the `chatMessage` struct (likely in `cmd/serf-tui/model.go` or similar) and add:

```go
type chatMessage struct {
	// ... existing fields ...

	// PendingID is non-zero when this message is an optimistic placeholder
	// created in response to a user click before the authoritative event
	// arrives. It matches the pendingEntry.ID from pendingCoordinator.
	PendingID int64
	// Pending is true while the optimistic call is in flight. The composer
	// renders the row with a spinner prefix and dim color while true.
	Pending bool
	// Failed is true if the optimistic call rejected or timed out without
	// reconciling. Mutually exclusive with Pending. Render with red prefix
	// + Reason.
	Failed bool
	// Reason is the failure message when Failed is true.
	Reason string
}
```

- [ ] **Step 6: Add reducer helpers**

Append to `cmd/serf-tui/hub_transcript_reducer.go`:

```go
// appendPendingSteering renders an optimistic STEERING placeholder
// while the daemon's STEERING_INJECTED event is in flight. Returns
// the PendingID for later mark/remove operations.
func (r *hubTranscriptReducer) appendPendingSteering(text string) int64 {
	id := nextPendingID()
	r.messages = append(r.messages, chatMessage{
		Kind:      msgSteering,
		Text:      text,
		Pending:   true,
		PendingID: id,
	})
	return id
}

// appendPendingUser renders an optimistic USER_INPUT placeholder.
// Today the renderer already does silent user-message echo via
// applyUserMessageEcho; this helper extends that to set Pending so
// the spinner prefix renders.
func (r *hubTranscriptReducer) appendPendingUser(text string) int64 {
	id := nextPendingID()
	r.messages = append(r.messages, chatMessage{
		Kind:      msgUser,
		Text:      text,
		Pending:   true,
		PendingID: id,
	})
	return id
}

// appendPendingDrain renders the single transient drain-as-steer chip
// that collapses queued entries while the daemon merges them into one
// STEERING_INJECTED event.
func (r *hubTranscriptReducer) appendPendingDrain(queuedCount int) int64 {
	id := nextPendingID()
	r.messages = append(r.messages, chatMessage{
		Kind:      msgSteering,
		Text:      fmt.Sprintf("draining %d → steering", queuedCount),
		Pending:   true,
		PendingID: id,
	})
	return id
}

func (r *hubTranscriptReducer) markPendingFailed(id int64, reason string) {
	for i := range r.messages {
		if r.messages[i].PendingID != id {
			continue
		}
		r.messages[i].Pending = false
		r.messages[i].Failed = true
		r.messages[i].Reason = reason
		return
	}
}

func (r *hubTranscriptReducer) removePending(id int64) {
	for i := range r.messages {
		if r.messages[i].PendingID != id {
			continue
		}
		r.messages = append(r.messages[:i], r.messages[i+1:]...)
		return
	}
}

var pendingIDCounter int64

func nextPendingID() int64 {
	pendingIDCounter++
	return pendingIDCounter
}
```

The `fmt` import needs to be added if not present.

- [ ] **Step 7: Confirm GREEN**

```bash
go test ./cmd/serf-tui -run TestHubReducer_ -count=1 -v
```

Expected: PASS for all three new tests.

- [ ] **Step 8: Render pending/failed in composer panel**

Find where chatMessage is rendered for the conversation pane (`composer_panel.go` is likely the wrong file; check `hub_render.go` or `model.go`):

```bash
grep -n "msg.Kind == msgUser\|case msgUser\|msgAssistant.*Text\|Render.*chatMessage" cmd/serf-tui/*.go
```

In the message-rendering switch statement, for each kind branch, wrap the visible text with the pending/failed prefix:

```go
prefix := ""
if msg.Pending {
	prefix = lipgloss.NewStyle().Faint(true).Render("⠋ ") // see Task 7 for spinner
}
if msg.Failed {
	prefix = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗ ")
}
body := msg.Text
if msg.Failed && msg.Reason != "" {
	body += lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("9")).Render(" (failed: " + msg.Reason + " · press [r] to retry)")
}
// existing render of body, now with prefix
```

Run the TUI manually to eyeball it before moving on — though Task 12's scenario test is the real check.

- [ ] **Step 9: Vet + tests**

```bash
go vet ./cmd/serf-tui/...
go test ./cmd/serf-tui -count=1
```

Expected: all pass.

- [ ] **Step 10: Commit**

```bash
git add cmd/serf-tui/hub_transcript_reducer.go cmd/serf-tui/<the-render-file>.go cmd/serf-tui/pending_test.go cmd/serf-tui/model.go
git commit -m "$(cat <<'EOF'
tui: chatMessage gains Pending/Failed/Reason + reducer helpers

Extends chatMessage with PendingID/Pending/Failed/Reason so the
reducer can hold optimistic placeholders alongside authoritative
entries. New helpers appendPendingSteering, appendPendingUser,
appendPendingDrain, markPendingFailed, removePending wrap the slice
operations. The render layer prefixes pending entries with a spinner
glyph and failed ones with a red ✗ + reason.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: TUI: wire pendingCoordinator into hubModel + reconcile on notifications

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Modify: `cmd/serf-tui/pending_test.go` (add end-to-end test)

- [ ] **Step 1: Write the failing end-to-end test**

Append to `cmd/serf-tui/pending_test.go` (or split into `cmd/serf-tui/optimistic_test.go`):

```go
func TestHubModel_SteerFailsFastOnRPCUnavailable(t *testing.T) {
	t.Parallel()
	transport := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	m := newHubModel(client, "http://test", "")
	// Pretend a session is active so the steer keybind would normally fire.
	m.session.sessionRef = appwire.Ref{SourceID: "local", ThreadID: "t1"}.String()
	m.session.activeTurnID = "turn-1"

	msgs := make(chan tea.Msg, 16)
	m.pending = newPendingCoordinator(realClock{}, func(msg tea.Msg) { msgs <- msg })
	client.SetPendingCoordinator(m.pending)

	go func() {
		req := <-transport.Sent()
		transport.DeliverError(req.ID, appwire.CodeUnavailable, "steer is not available for this session")
	}()

	// Issue the steer via the TUI's normal command path. Lift the code
	// from handleSessionForceSteer for the test if needed.
	cmd := triggerSteerForTest(&m, "go check this")
	_ = cmd.Init()

	// Expect Registered then Failed within 1s.
	got := drainMessages(msgs, 2, time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 msgs (Registered+Failed), got %d", len(got))
	}
	if _, ok := got[0].(pendingRegisteredMsg); !ok {
		t.Fatalf("first msg = %T", got[0])
	}
	fm, ok := got[1].(pendingFailedMsg)
	if !ok {
		t.Fatalf("second msg = %T", got[1])
	}
	if !strings.Contains(fm.reason, "not available") {
		t.Fatalf("reason: %q", fm.reason)
	}
}
```

If `triggerSteerForTest` doesn't exist, add it as a small test-only helper in `cmd/serf-tui/pending_test.go` that drives the same call hubModel.handleSessionForceSteer would issue.

- [ ] **Step 2: Confirm RED**

```bash
go test ./cmd/serf-tui -run TestHubModel_SteerFailsFastOnRPCUnavailable -count=1 -v
```

Expected: FAIL — `m.pending` field doesn't exist; `client.SetPendingCoordinator` isn't called by hubModel; `triggerSteerForTest` may also be missing.

- [ ] **Step 3: Add `pending` field to hubModel**

Edit `cmd/serf-tui/hub_model.go`. Find the `hubModel` struct (around line 100-160) and add:

```go
	// pending coordinates optimistic-rendering placeholders for
	// turn/start, turn/queue, turn/steer, turn/drainAsSteer. Nil
	// in tests that don't exercise the optimistic path.
	pending *pendingCoordinator
```

- [ ] **Step 4: Construct + wire pending in newHubModel**

Find `newHubModel` and after the existing fields are populated:

```go
	model.pending = newPendingCoordinator(realClock{}, func(msg tea.Msg) {
		// The bubbletea program reference isn't available here yet.
		// We forward via a model-owned channel that hub_root.go's
		// program sends from on each receive.
		// Simplest pattern: use tea.Program.Send through a setter
		// the root wires up at Start.
	})
	client.SetPendingCoordinator(model.pending)
```

The forwarding pattern: in `hub_root.go` (or wherever the `tea.NewProgram(...)` runs), after the program is constructed:

```go
	program := tea.NewProgram(model, ...)
	model.pending.setSend(program.Send)
```

Add a `setSend` method to `pendingCoordinator`:

```go
func (p *pendingCoordinator) setSend(fn func(tea.Msg)) {
	p.mu.Lock()
	p.send = fn
	p.mu.Unlock()
}
```

(Make the initial `send` a buffering fallback if you prefer — for simplicity, require setSend to be called before any RPC issuance. The hub_root code is the only path that produces RPCs.)

- [ ] **Step 5: Handle pending msgs in `hubModel.Update`**

Find the existing `case hubNotificationMsg:` block in `hubModel.Update` (around line 330). Add new cases:

```go
case pendingRegisteredMsg:
	r := m.reducerForActiveSession()
	switch msg.entry.Method {
	case appwire.MethodTurnSteer:
		r.appendPendingSteering(msg.entry.Text)
	case appwire.MethodTurnStart:
		r.appendPendingUser(msg.entry.Text)
	case appwire.MethodTurnQueue:
		// Queue entries render in queue-preview chrome — see Task 9.
		// For now, no-op in the transcript pane.
	case appwire.MethodTurnDrainAsSteer:
		r.appendPendingDrain(len(m.sessionQueue))
	}
	return m, nil

case pendingFailedMsg:
	r := m.reducerForActiveSession()
	r.markPendingFailed(msg.entry.ID, msg.reason)
	return m, nil

case pendingConfirmedMsg:
	r := m.reducerForActiveSession()
	r.removePending(msg.entry.ID)
	return m, nil
```

Add `reducerForActiveSession` helper if the model doesn't already expose the active reducer.

- [ ] **Step 6: Call `tryReconcile` after applyHubNotification**

Find `applyHubNotification` (`hub_model.go:2195`). After it returns the tea.Cmd, add a tryReconcile pass on the notification's method + text:

```go
func (m *hubModel) applyHubNotification(notification appwire.Notification) tea.Cmd {
	// ... existing reducer dispatch ...

	// After the authoritative reducer update has applied, reconcile
	// any pending optimistic placeholder that matches this event.
	// Per the spec, this is the SINGLE reconciliation site on the TUI.
	if m.pending != nil {
		if text, ok := pendingReconcileTextFromNotification(notification); ok {
			m.pending.TryReconcile(notification.Method, text)
		}
	}
	return cmd
}

// pendingReconcileTextFromNotification extracts the matchable text
// payload from a notification for tryReconcile. Returns (text, true)
// only for notifications we care about. STEERING_INJECTED carries the
// steered text in params; USER_INPUT carries the input text; queue
// changes carry the entry list.
func pendingReconcileTextFromNotification(n appwire.Notification) (string, bool) {
	// Implementation depends on appwire.Notification.Params shape.
	// For each method we handle:
	//   item/added (or whichever event signals USER_INPUT) → return user text
	//   notify/steeringInjected → return injected text
	//   thread/queueChanged → see Task 11's drain-special path
	// Use the typed notification helpers in internal/appwire.
	var b struct {
		Text string `json:"text"`
	}
	if len(n.Params) > 0 {
		_ = json.Unmarshal(n.Params, &b)
	}
	return b.Text, b.Text != ""
}
```

The exact event names and payload shapes must match what the hub emits. Verify by reading `internal/appwire/types.go` for the notification method constants, then read the daemon code that emits them. If the steering text isn't on a top-level `text` field, adjust the unmarshal struct accordingly.

- [ ] **Step 7: Run the failing test**

```bash
go test ./cmd/serf-tui -run TestHubModel_SteerFailsFastOnRPCUnavailable -count=1 -v
```

Expected: PASS.

- [ ] **Step 8: Run full TUI suite**

```bash
go test ./cmd/serf-tui -count=1 -timeout 60s
```

Expected: all pass.

- [ ] **Step 9: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/pending.go cmd/serf-tui/pending_test.go cmd/serf-tui/hub_root.go
git commit -m "$(cat <<'EOF'
tui: wire pendingCoordinator into hubModel + reconcile pass

newHubModel constructs the pendingCoordinator and registers it on the
appwire.Client. tea.Program.Send is plumbed into the coordinator via
pending.setSend so coordinator-emitted msgs land in Update. New cases
in Update handle pendingRegisteredMsg / pendingFailedMsg /
pendingConfirmedMsg by mutating the reducer for the active session.

applyHubNotification now calls pending.TryReconcile after the
authoritative reducer update applies — the single reconciliation site
required by the spec.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Web: CSS pulse + failed primitives + keyframe

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1: Eyeball — locate existing message styles**

```bash
grep -n "\.user-message\|\.steering\|@keyframes search-hit-flash" cmd/serf-hub/assets/style.css
```

Note line numbers; the new rules go right after the search-hit-flash keyframe (~line 680) so they live with other animation primitives.

- [ ] **Step 2: Add the CSS block**

Append to `cmd/serf-hub/assets/style.css`:

```css
/* Optimistic rendering: applied to any conversation entry that has
   been issued but not yet reconciled with an authoritative event.
   Reconcile removes .optimistic-pending; failure adds .optimistic-failed. */
.optimistic-pending {
  animation: optimistic-pulse 1.4s ease-in-out infinite;
}

.optimistic-failed {
  opacity: 1;
  border-left: 2px solid var(--state-error);
  padding-left: 8px;
}

.optimistic-failed-reason {
  font-size: 11px;
  color: var(--state-error);
  margin-top: 4px;
}

.optimistic-retry {
  font-size: 11px;
  color: var(--text-muted);
  cursor: pointer;
  margin-left: 8px;
  user-select: none;
}

.optimistic-retry:hover { color: var(--text); }

@keyframes optimistic-pulse {
  0%, 100% { opacity: 1; }
  50%      { opacity: 0.65; }
}
```

- [ ] **Step 3: Lint / sanity check**

```bash
# CSS has no formal linter in the repo; eyeball the file:
grep -n "optimistic" cmd/serf-hub/assets/style.css
```

Expected: 7 matches (5 selectors + 1 keyframe name + 1 keyframe use inside `.optimistic-pending`).

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
hub-web: CSS primitives for optimistic-pending / -failed / -retry

.optimistic-pending applies a 1.4s opacity pulse (1.0 → 0.65 → 1.0)
to any wrapper element. .optimistic-failed swaps to a red left border
+ reason line. .optimistic-retry is the inline retry link the
pending.js registry attaches to failed chips.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Web: `SerfAppwirePending` registry module

**Files:**
- Create: `cmd/serf-hub/assets/pending.js`
- Modify: `cmd/serf-hub/templates/partials/workspace.html` (load pending.js before renderer.js)
- Create: `cmd/serf-hub/jstest/test-pending-registry.js`

- [ ] **Step 1: Write the failing jstest**

Create `cmd/serf-hub/jstest/test-pending-registry.js`:

```js
const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

const MODULE = fs.readFileSync(path.resolve(__dirname, "../assets/pending.js"), "utf8");

function build() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div id="conversation"></div>
    <div id="queue-preview"></div>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.eval(MODULE);
  return window;
}

(function test_register_renders_pending_chip() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });

  const h = reg.register({ method: "turn/steer", text: "look at this" });

  const chips = conv.querySelectorAll(".steering.optimistic-pending");
  assert.equal(chips.length, 1, "expected 1 pending steering chip");
  assert.match(chips[0].textContent, /look at this/);
  assert.equal(h.id > 0, true);
  console.log("ok register_renders_pending_chip");
})();

(function test_fail_marks_failed_with_retry() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  const h = reg.register({ method: "turn/steer", text: "x" });

  reg.fail(h, "steer is not available for this session");

  assert.ok(!conv.querySelector(".optimistic-pending"));
  const failed = conv.querySelector(".optimistic-failed");
  assert.ok(failed, "expected failed element");
  assert.match(failed.querySelector(".optimistic-failed-reason").textContent, /not available/);
  assert.ok(failed.querySelector(".optimistic-retry"), "retry link missing");
  console.log("ok fail_marks_failed_with_retry");
})();

(function test_try_reconcile_removes_match() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  reg.register({ method: "turn/steer", text: "look at this" });

  const matched = reg.tryReconcile("notify/steeringInjected", { text: "look  at  this" });
  // Reconcile is keyed by method; the wire method is the same as the
  // notification's "registry kind" (per Task 11's mapping table).
  // For unit test we use the registry's normalize logic directly:
  const matched2 = reg.tryReconcile("turn/steer", { text: "look  at  this" });
  assert.equal(matched2, true);
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 0);
  console.log("ok try_reconcile_removes_match");
})();

(function test_timeout_marks_failed() {
  const window = build();
  const fakeNow = { v: 0 };
  const timers = [];
  const fakeSetTimeout = (fn, ms) => { timers.push({ fire: fakeNow.v + ms, fn, cancelled: false }); return timers.length - 1; };
  const fakeClearTimeout = (id) => { if (timers[id]) timers[id].cancelled = true; };
  const advance = (ms) => {
    fakeNow.v += ms;
    for (const t of timers) {
      if (!t.cancelled && t.fire <= fakeNow.v) { t.cancelled = true; t.fn(); }
    }
  };

  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({
    conversation: conv,
    setTimeout: fakeSetTimeout,
    clearTimeout: fakeClearTimeout,
  });
  reg.register({ method: "turn/steer", text: "x" });
  advance(11000);

  const failed = conv.querySelector(".optimistic-failed");
  assert.ok(failed);
  assert.match(failed.querySelector(".optimistic-failed-reason").textContent, /did not confirm/);
  console.log("ok timeout_marks_failed");
})();

console.log("PASS test-pending-registry.js");
```

- [ ] **Step 2: Run, confirm RED**

```bash
cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-pending-registry.js
```

Expected: FAIL — `pending.js` doesn't exist.

- [ ] **Step 3: Implement pending.js**

Create `cmd/serf-hub/assets/pending.js`:

```js
// pending.js — optimistic-rendering registry for the web hub
// renderer. Exposes window.SerfAppwirePending.create({...}) which
// returns an instance with register/fail/tryReconcile methods. The
// instance owns DOM nodes for each pending entry and animates them
// via the .optimistic-pending class (style.css).
//
// Architecture: the registry does not subscribe to events. It is
// called explicitly by the renderer's existing notification path
// (inside deliverNotification) after the authoritative reducer
// update applies. See spec §Architecture.
(function () {
  "use strict";

  function normalizeText(s) {
    return String(s || "").replace(/\s+/g, " ").trim();
  }

  function create(opts) {
    const conv = opts.conversation;
    const setTimeoutFn = opts.setTimeout || setTimeout;
    const clearTimeoutFn = opts.clearTimeout || clearTimeout;
    const timeoutMs = (typeof opts.timeoutMs === "number") ? opts.timeoutMs : 10000;
    const onRetry = opts.onRetry || function () {};

    let nextID = 0;
    const entries = new Map(); // id → {method, text, el, timerID}

    function chipForMethod(method, text) {
      const doc = conv.ownerDocument;
      const el = doc.createElement("div");
      switch (method) {
        case "turn/steer":
        case "turn/drainAsSteer":
          el.className = "steering optimistic-pending";
          el.textContent = "↻ " + (method === "turn/drainAsSteer" ? "draining queue" : text);
          break;
        case "turn/start":
          el.className = "user-message optimistic-pending";
          el.textContent = text;
          break;
        case "turn/queue":
          // Queue items render in the queue-preview chrome; the
          // optimisticCall callsite places them there, not here.
          el.className = "queue-pending optimistic-pending";
          el.textContent = text;
          break;
        default:
          el.className = "optimistic-pending";
          el.textContent = text;
      }
      return el;
    }

    function register(intent) {
      nextID++;
      const id = nextID;
      const method = intent.method;
      const text = intent.text || "";
      const el = chipForMethod(method, text);
      conv.appendChild(el);

      const timerID = setTimeoutFn(() => {
        fail({ id }, "server did not confirm");
      }, timeoutMs);

      entries.set(id, { method, text, el, timerID });
      return { id };
    }

    function fail(handle, reason) {
      const ent = entries.get(handle.id);
      if (!ent) return;
      clearTimeoutFn(ent.timerID);
      ent.el.classList.remove("optimistic-pending");
      ent.el.classList.add("optimistic-failed");
      const doc = ent.el.ownerDocument;
      const reasonEl = doc.createElement("div");
      reasonEl.className = "optimistic-failed-reason";
      reasonEl.textContent = reason;
      ent.el.appendChild(reasonEl);
      const retry = doc.createElement("a");
      retry.className = "optimistic-retry";
      retry.textContent = "Retry";
      retry.href = "#";
      retry.addEventListener("click", (e) => {
        e.preventDefault();
        onRetry({ method: ent.method, text: ent.text });
      });
      ent.el.appendChild(retry);
      entries.delete(handle.id);
    }

    function tryReconcile(method, params) {
      const want = normalizeText(params && params.text);
      if (!want) return false;
      for (const [id, ent] of entries) {
        if (ent.method !== method) continue;
        if (normalizeText(ent.text) !== want) continue;
        clearTimeoutFn(ent.timerID);
        // The authoritative event handler renders the real entry
        // separately; we just remove the placeholder.
        if (ent.el.parentNode) ent.el.parentNode.removeChild(ent.el);
        entries.delete(id);
        return true;
      }
      return false;
    }

    return { register, fail, tryReconcile };
  }

  window.SerfAppwirePending = { create };
})();
```

- [ ] **Step 4: Load pending.js before renderer.js**

Find the template that loads renderer.js (probably `cmd/serf-hub/templates/partials/workspace.html` or a layout partial):

```bash
grep -rn "renderer.js\|appwire.js" cmd/serf-hub/templates/ | head
```

Add `<script src="/assets/pending.js"></script>` before `<script src="/assets/renderer.js"></script>`.

- [ ] **Step 5: Confirm GREEN**

```bash
cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-pending-registry.js
```

Expected: all four `ok` lines, ending with `PASS test-pending-registry.js`.

- [ ] **Step 6: Run full jstest suite**

```bash
cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: all existing tests still pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/assets/pending.js cmd/serf-hub/jstest/test-pending-registry.js cmd/serf-hub/templates/
git commit -m "$(cat <<'EOF'
hub-web: SerfAppwirePending registry (DOM + reconcile)

Exposes window.SerfAppwirePending.create({...}) returning an instance
with register/fail/tryReconcile. Pending chips are appended to the
conversation pane immediately, classed .optimistic-pending. Failure
swaps in .optimistic-failed + reason line + Retry link. tryReconcile
matches by method + whitespace-normalized text and removes the
placeholder (authoritative entry renders separately via the existing
event path). Timer cleanup is symmetric across fail / reconcile.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Web: `optimisticCall` in SerfAppwire + wire the four methods

**Files:**
- Modify: `cmd/serf-hub/assets/appwire.js`
- Create: `cmd/serf-hub/jstest/test-optimistic-rendering.js`

- [ ] **Step 1: Write the failing wrapper unit test**

Create `cmd/serf-hub/jstest/test-optimistic-rendering.js`:

```js
const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

const PENDING = fs.readFileSync(path.resolve(__dirname, "../assets/pending.js"), "utf8");
const APPWIRE = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");

function build() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><div id="conv"></div></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://test/",
  });
  const { window } = dom;
  // Stub WebSocket so the connect() at module load doesn't try to open a real socket.
  let lastSock = null;
  window.WebSocket = class FakeWS {
    constructor(url) {
      this.url = url;
      this.readyState = 1;
      this.listeners = {};
      lastSock = this;
      // Mimic the appwire-side open dispatch:
      setTimeout(() => {
        const cb = this.listeners.open && this.listeners.open[0];
        if (cb) cb({});
      }, 0);
    }
    addEventListener(name, cb) {
      (this.listeners[name] = this.listeners[name] || []).push(cb);
    }
    send(payload) {
      // tests inspect lastSent / call respond()
      this.lastSent = JSON.parse(payload);
    }
    close() {}
  };
  window.eval(PENDING);
  window.eval(APPWIRE);
  return { window, getSock: () => lastSock };
}

function respondTo(sock, id, result) {
  const cb = sock.listeners.message && sock.listeners.message[0];
  cb({ data: JSON.stringify({ jsonrpc: "2.0", id, result }) });
}
function respondErrorTo(sock, id, code, message) {
  const cb = sock.listeners.message && sock.listeners.message[0];
  cb({ data: JSON.stringify({ jsonrpc: "2.0", id, error: { code, message } }) });
}

(async function test_steer_rejects_renders_failed_chip() {
  const { window, getSock } = build();
  await new Promise(r => setTimeout(r, 5)); // let connect() open dispatch run
  const conv = window.document.getElementById("conv");
  const pending = window.SerfAppwirePending.create({ conversation: conv });
  window.SerfAppwire.setPendingRegistry(pending);

  const promise = window.SerfAppwire.steer("sess-1", "turn-1", "look here").catch(e => e);
  await new Promise(r => setTimeout(r, 5));
  const sock = getSock();
  assert.ok(sock.lastSent, "client should have sent a steer request");
  respondErrorTo(sock, sock.lastSent.id, 32008, "steer is not available for this session");

  await promise;
  await new Promise(r => setTimeout(r, 5));
  const failed = conv.querySelector(".optimistic-failed");
  assert.ok(failed, "expected failed chip after Unavailable");
  console.log("ok steer_rejects_renders_failed_chip");
})();
```

(For the full set — success-then-event, timeout, drain — extend this test in Task 11 once the reconcile hook is wired.)

- [ ] **Step 2: Confirm RED**

```bash
cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-optimistic-rendering.js
```

Expected: FAIL — `SerfAppwire.setPendingRegistry` doesn't exist.

- [ ] **Step 3: Add optimisticCall + setPendingRegistry**

Edit `cmd/serf-hub/assets/appwire.js`. Inside the IIFE, after the existing `request` function:

```js
  // Optimistic-rendering hook. The renderer registers a registry via
  // setPendingRegistry; if absent, optimisticCall passes through as a
  // bare request().
  let pendingRegistry = null;
  function setPendingRegistry(reg) { pendingRegistry = reg; }

  async function optimisticCall(method, params, intent) {
    let handle = null;
    if (pendingRegistry) {
      handle = pendingRegistry.register({ method, text: intent && intent.text || "" });
    }
    try {
      return await request(method, params);
    } catch (err) {
      if (handle && pendingRegistry) {
        pendingRegistry.fail(handle, errorMessageFor(err));
      }
      throw err;
    }
  }

  function errorMessageFor(err) {
    if (err && err.message) return err.message;
    return String(err);
  }
```

Replace the four method bodies:

```js
  function startTurn(sessionId, text, attachments) {
    return optimisticCall(METHOD.turnStart, {
      ref: refForSession(sessionId),
      input: { text: text || "", items: inputItemsFromAttachments(attachments) },
    }, { text });
  }

  function steer(sessionId, turnId, text) {
    return optimisticCall(METHOD.turnSteer, {
      ref: refForSession(sessionId), turnId: turnId || "", text: text || "",
    }, { text });
  }

  function queueTurn(sessionId, text, attachments) {
    return optimisticCall(METHOD.turnQueue, {
      ref: refForSession(sessionId),
      input: { text: text || "", items: inputItemsFromAttachments(attachments) },
    }, { text });
  }

  async function drainAsSteer(sessionId) {
    return optimisticCall(METHOD.turnDrainAsSteer, {
      ref: refForSession(sessionId),
    }, { text: "" });
  }
```

Add `setPendingRegistry` to the public exports at the bottom:

```js
  window.SerfAppwire = {
    // ... existing names ...
    setPendingRegistry,
  };
```

- [ ] **Step 4: Confirm GREEN**

```bash
cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-optimistic-rendering.js
```

Expected: PASS.

- [ ] **Step 5: Full jstest suite**

```bash
cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/appwire.js cmd/serf-hub/jstest/test-optimistic-rendering.js
git commit -m "$(cat <<'EOF'
hub-web: optimisticCall wrap in SerfAppwire for start/steer/queue/drain

startTurn / steer / queueTurn / drainAsSteer all route through
optimisticCall, which registers a pending entry, awaits the JSON-RPC
response, and calls pending.fail on error. setPendingRegistry installs
the registry; with no registry installed (older callers), the wrapper
passes through to request() unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Web: `tryReconcile` inside `deliverNotification` + drain-special

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/jstest/test-optimistic-rendering.js` (add success-then-event, timeout, drain tests)

- [ ] **Step 1: Add failing tests**

Append to `cmd/serf-hub/jstest/test-optimistic-rendering.js`:

```js
(async function test_steer_success_then_event_reconciles() {
  const { window, getSock } = build();
  await new Promise(r => setTimeout(r, 5));
  const conv = window.document.getElementById("conv");
  const pending = window.SerfAppwirePending.create({ conversation: conv });
  window.SerfAppwire.setPendingRegistry(pending);

  const promise = window.SerfAppwire.steer("sess-1", "turn-1", "go check this");
  await new Promise(r => setTimeout(r, 5));
  const sock = getSock();
  respondTo(sock, sock.lastSent.id, {});
  await promise;

  // Simulate the daemon's STEERING_INJECTED notification.
  const handlers = window.SerfAppwire._notificationHandlersForTest ? window.SerfAppwire._notificationHandlersForTest() : null;
  // If the wire-level path exposes a notify-injection helper for tests, use it.
  // Otherwise simulate by calling pending.tryReconcile directly:
  pending.tryReconcile("turn/steer", { text: "go check this" });
  assert.equal(conv.querySelector(".optimistic-pending"), null);
  console.log("ok steer_success_then_event_reconciles");
})();

(async function test_drain_collapses_queue_into_one_chip() {
  const { window } = build();
  await new Promise(r => setTimeout(r, 5));
  const conv = window.document.getElementById("conv");
  const pending = window.SerfAppwirePending.create({ conversation: conv });
  // Register three queue placeholders, then a drain placeholder, then
  // reconcile drain via the first incoming STEERING_INJECTED.
  pending.register({ method: "turn/queue", text: "q1" });
  pending.register({ method: "turn/queue", text: "q2" });
  pending.register({ method: "turn/queue", text: "q3" });
  pending.register({ method: "turn/drainAsSteer", text: "" });

  // First STEERING_INJECTED reconciles the drain placeholder.
  pending.tryReconcile("turn/drainAsSteer", { text: "q1\n\nq2\n\nq3" });
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 3); // queue items still pending
  console.log("ok drain_collapses_into_one_chip");
})();
```

- [ ] **Step 2: Confirm RED**

```bash
cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-optimistic-rendering.js
```

Expected: PASS for the new tests (since tryReconcile is being called directly in the test). This actually confirms the registry works; the renderer-side hook is the missing piece.

Now write a renderer-integration test in the same file:

```js
(async function test_renderer_deliverNotification_calls_tryReconcile() {
  // Load renderer.js too. Mock just enough that deliverNotification
  // can be invoked with a STEERING_INJECTED-like params object and
  // we observe that pending.tryReconcile receives it.
  // ... (Sketch: import renderer module, stub the reducer dispatch,
  // wire pending, dispatch a deliverNotification, assert tryReconcile
  // was called with (method, params).)
})();
```

- [ ] **Step 3: Modify renderer.js**

Find `deliverNotification` (renderer.js:304). At the end of the function (after the reducer dispatch completes):

```js
const deliverNotification = (method, params) => {
  // ... existing reducer dispatch ...

  // Single optimistic reconciliation site: after the authoritative
  // update has applied, give the pending registry a chance to find
  // a matching placeholder and remove it. The hydration-replay loop
  // (below) reuses this exact code path.
  if (this.pending) {
    const reconcileMethod = reconcileMethodFromNotification(method);
    if (reconcileMethod) {
      this.pending.tryReconcile(reconcileMethod, params || {});
    }
  }
};
```

Add the mapping helper somewhere near `classifySteering`:

```js
// Maps daemon notification methods to the appwire wire-method names
// the pending registry tracks. Returns null when the notification is
// not relevant to optimistic rendering.
function reconcileMethodFromNotification(method) {
  switch (method) {
    case "notify/steeringInjected":  return "turn/steer";
    case "notify/userInput":         return "turn/start";
    case "thread/queueChanged":      return "turn/queue";
    // drain reconciles on the first STEERING_INJECTED after the
    // drain RPC; see drain-special below.
    default: return null;
  }
}
```

Wire the renderer's pending registry up at startup. In the renderer's init code:

```js
this.pending = window.SerfAppwirePending.create({
  conversation: this.conversation,
  onRetry: (intent) => {
    // Re-issue the optimistic call by name.
    switch (intent.method) {
      case "turn/steer":         return window.SerfAppwire.steer(this.sessionId, this.activeTurnId, intent.text);
      case "turn/start":         return window.SerfAppwire.startTurn(this.sessionId, intent.text, []);
      case "turn/queue":         return window.SerfAppwire.queueTurn(this.sessionId, intent.text, []);
      case "turn/drainAsSteer":  return window.SerfAppwire.drainAsSteer(this.sessionId);
    }
  },
});
window.SerfAppwire.setPendingRegistry(this.pending);
```

For drain-special: in `deliverNotification` after the regular reconcile attempt, if the method is `notify/steeringInjected`, additionally try `pending.tryReconcile("turn/drainAsSteer", {})` (the registry's drain match accepts any text on the first matching call). To make the registry's drain match work that way, edit `pending.js`'s tryReconcile loop:

```js
function tryReconcile(method, params) {
  const want = normalizeText(params && params.text);
  for (const [id, ent] of entries) {
    if (ent.method !== method) continue;
    if (method === "turn/drainAsSteer") {
      // Drain matches first-come-first-served, no text comparison.
      removeEntry(id);
      return true;
    }
    if (!want) continue;
    if (normalizeText(ent.text) !== want) continue;
    removeEntry(id);
    return true;
  }
  return false;
}
function removeEntry(id) {
  const ent = entries.get(id);
  if (!ent) return;
  clearTimeoutFn(ent.timerID);
  if (ent.el.parentNode) ent.el.parentNode.removeChild(ent.el);
  entries.delete(id);
}
```

- [ ] **Step 4: Confirm GREEN**

```bash
cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-optimistic-rendering.js
```

Expected: all PASS.

- [ ] **Step 5: Full jstest run**

```bash
cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/pending.js cmd/serf-hub/jstest/test-optimistic-rendering.js
git commit -m "$(cat <<'EOF'
hub-web: deliverNotification reconciles pending + drain-special

Single reconciliation site: deliverNotification calls
pending.tryReconcile(method, params) after the reducer dispatch
completes. reconcileMethodFromNotification maps daemon notification
names to wire-method names. The hydration-replay loop runs through
deliverNotification, so replayed notifications get exactly one
reconcile pass.

Drain-special: tryReconcile for "turn/drainAsSteer" matches
first-come-first-served, no text comparison — the first
STEERING_INJECTED after the drain RPC consumes the drain chip.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Live end-to-end scenarios

**Files:**
- Create: `test/scenarios/web-steer-in-idle-fails-fast.md`
- Create: `test/scenarios/web-steer-success-reconciles.md`
- Create: `test/scenarios/tui-steer-in-idle-fails-fast.md`
- Create: `test/scenarios/tui-steer-success-reconciles.md`
- Modify: `test/scenarios/INDEX.md`

Each scenario file follows the existing pattern (see `test/scenarios/web-steer-live-turn.md` for shape). Below is the structure for the first; the other three follow the same shape.

- [ ] **Step 1: Write `web-steer-in-idle-fails-fast.md`**

```markdown
# web-steer-in-idle-fails-fast

Verifies the optimistic-rendering failure path against a real daemon:
the steer button rejects with the daemon's `Unavailable` reason within
~200ms, and the failed chip renders with the rejection reason and a
Retry link.

## Setup

1. `serf-hub serve --listen 127.0.0.1:0` in one shell; capture port.
2. Spawn a session with `anthropic/claude-haiku-4-5-20251001` and
   prompt `please call the read_file tool once on README.md then stop`.
   Wait for the session to reach IDLE.

## Driver

Via `superpowers-chrome:browsing`:

```
navigate http://127.0.0.1:<port>/s/<session-id>
await_element [data-steer-trigger]
eval document.querySelector('[data-steer-trigger]').disabled = false
type [data-input-form] textarea  "this should fail visibly"
click [data-steer-trigger]
await_element .optimistic-failed
extract text .optimistic-failed-reason
```

## Assertions

- An element with class `optimistic-failed` exists within 1s.
- Its `.optimistic-failed-reason` contains "not available".
- A `.optimistic-retry` link is present.
- No transcript STEERING entry is created (verified by counting
  `.steering` elements: same before and after).

## Cleanup

- Shutdown session, kill hub.
```

- [ ] **Step 2: Write `web-steer-success-reconciles.md`**

Same shape but driving against a processing session, asserting the
pending chip appears then disappears as STEERING_INJECTED arrives.

- [ ] **Step 3: Write `tui-steer-in-idle-fails-fast.md`**

```markdown
# tui-steer-in-idle-fails-fast

Same behavioral guarantee as web-steer-in-idle-fails-fast, driven via
tmux against the TUI.

## Driver

```
tmux new-session -d -s testbed 'serf-hub serve & serf-tui'
tmux send-keys -t testbed ':spawn anthropic/claude-haiku-4-5-20251001 ...'
# Wait for IDLE
tmux send-keys -t testbed 'this should fail visibly'
tmux send-keys -t testbed 'C-s'  # force-steer keybind
sleep 1
tmux capture-pane -p -t testbed | grep -E '✗ .*not available'
```

## Assertions

- A line with the `✗` failed-prefix and "not available" appears in the
  conversation pane within 2s.
- The textarea is preserved (the optimistic-failed chip is the
  rendered version of what the user typed).
- No real STEERING entry is appended to the transcript.
```

- [ ] **Step 4: Write `tui-steer-success-reconciles.md`**

Mirror shape. Drives a processing turn, sends C-s with steering text,
captures the spinner prefix appearing then being replaced by the real
STEERING divider.

- [ ] **Step 5: Update `test/scenarios/INDEX.md`**

Find the "Session workspace" section in INDEX.md and append:

```markdown
- `web-steer-in-idle-fails-fast.md` — verifies optimistic-rendering
  reject path: steer button in IDLE returns Unavailable, chip flips
  to .optimistic-failed with retry link (kata wymv + the optimistic
  rendering work).
- `web-steer-success-reconciles.md` — happy path: pending chip pulses
  then is replaced by authoritative STEERING divider.
- `tui-steer-in-idle-fails-fast.md` — TUI counterpart of fails-fast.
- `tui-steer-success-reconciles.md` — TUI counterpart of success.
```

- [ ] **Step 6: Commit**

```bash
git add test/scenarios/web-steer-in-idle-fails-fast.md test/scenarios/web-steer-success-reconciles.md test/scenarios/tui-steer-in-idle-fails-fast.md test/scenarios/tui-steer-success-reconciles.md test/scenarios/INDEX.md
git commit -m "$(cat <<'EOF'
scenarios: optimistic rendering fails-fast + reconciles paths

Four live e2e scenarios covering the new optimistic-rendering
pattern: web + TUI, success + failure. Each drives against a real
serf-hub + serf daemon spawning anthropic/claude-haiku-4-5-20251001
sessions.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review (run after task 12)

Per the writing-plans skill, run a final pass:

- [ ] **Spec coverage:** every numbered §section of the spec has at least one task implementing it. Architecture → Tasks 3-4 (Go), 9-11 (web), 7 (TUI integration). Per-action matrix → Tasks 7 (TUI) + 11 (web). Visual treatment → Tasks 6 (TUI) + 8 (web CSS) + 9 (web pending). Daemon fix → Task 1. Testing → Tasks 2 + 5 + 7 + 9 + 10 + 11 + 12. File layout → all tasks cover their share.
- [ ] **Placeholder scan:** no "TBD" / "TODO" / "implement later" / "Similar to Task N" hand-waves.
- [ ] **Type consistency:** PendingCoordinator / PendingHandle / pendingEntry / pendingRegisteredMsg names match across Tasks 3-7. `optimisticCall` / `SerfAppwirePending` / `tryReconcile` match across Tasks 9-11.

If any gap is found, fix inline and re-commit.

---

**Plan complete and saved to `docs/superpowers/plans/2026-05-18-optimistic-rendering-plan.md`. Two execution options:**

1. **Subagent-Driven (recommended)** — fresh subagent per task, two-stage review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using executing-plans, batched with checkpoints for review.

Which approach?
