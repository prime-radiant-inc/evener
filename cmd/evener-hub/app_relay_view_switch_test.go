package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHubRelayViewSwitchDeliversEachDeltaOnce probes the delivery layer for
// the #641 duplicate-delta signature: while the relay streams one block of
// deltas, connections subscribe, unsubscribe, and re-subscribe (the view
// switching the issue's repro describes), and every delta must reach each
// subscribed connection exactly once. The relay and appserver layers pass
// this — the #641 root cause was the projector's retry reset (see
// appprojector's reasoning-reset regression test) — and this test pins the
// delivery-once contract the fix must not regress.
func TestHubRelayViewSwitchDeliversEachDeltaOnce(t *testing.T) {
	const deltaCount = 12
	thread := appwire.Thread{
		ID:        "thread-view-switch",
		SessionID: "thread-view-switch",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:thread-view-switch"},
	}
	deliveries := make(chan appsource.RelayDelivery, deltaCount*4)
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: deliveries,
	}
	source := &relaySessionTestSource{thread: thread, lease: lease}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	if _, err := clientA.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize A: %v", err)
	}
	if _, err := clientA.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: thread.Evener.Ref, Subscribe: true}); err != nil {
		t.Fatalf("thread read A: %v", err)
	}

	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	if _, err := clientB.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize B: %v", err)
	}
	if _, err := clientB.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: thread.Evener.Ref, Subscribe: true}); err != nil {
		t.Fatalf("thread read B: %v", err)
	}

	// streamDeltas streams one block, waiting for each frame's acknowledgement
	// so the block is fully delivered before the next phase begins.
	streamDeltas := func(from, to int) {
		for i := from; i < to; i++ {
			payload, _ := json.Marshal(appwire.AgentMessageDeltaParams{
				ThreadID: thread.ID,
				Ref:      thread.Evener.Ref,
				TurnID:   "turn-view-switch",
				ItemID:   "item-view-switch",
				Delta:    fmt.Sprintf("delta-%d ", i),
			})
			ack := make(chan struct{})
			var once sync.Once
			deliveries <- appsource.RelayDelivery{
				Notification: appwire.Notification{
					Method: appwire.NotifyAgentMessageDelta,
					Params: payload,
				},
				Acknowledge: func() { once.Do(func() { close(ack) }) },
			}
			select {
			case <-ack:
			case <-time.After(2 * time.Second):
				t.Errorf("delta %d was not acknowledged", i)
				return
			}
		}
	}

	counts := func(c *appwire.Client) map[string]int {
		result := map[string]int{}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case notification := <-c.Notifications():
				var params appwire.AgentMessageDeltaParams
				if notification.Method == appwire.NotifyAgentMessageDelta && json.Unmarshal(notification.Params, &params) == nil {
					result[params.Delta]++
				}
			case <-time.After(500 * time.Millisecond):
				return result
			}
		}
		return result
	}

	// Phase 1: both views live, one block streams. Each delta exactly once
	// per subscribed connection.
	streamDeltas(0, deltaCount)
	for label, client := range map[string]*appwire.Client{"A": clientA, "B": clientB} {
		received := counts(client)
		for i := range deltaCount {
			key := fmt.Sprintf("delta-%d ", i)
			switch got := received[key]; got {
			case 1:
			default:
				t.Errorf("client %s received delta %d %d times, want exactly 1 — duplicate delta delivery (the #641 signature)", label, i, got)
			}
		}
	}

	// Phase 2: A leaves the view, a new block streams. A gets nothing from
	// this block; B keeps the once-each contract.
	if _, err := clientA.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: thread.Evener.Ref}); err != nil {
		t.Fatalf("thread unsubscribe A: %v", err)
	}
	streamDeltas(deltaCount, deltaCount*2)
	receivedA := counts(clientA)
	for key, got := range receivedA {
		if got > 0 && key >= fmt.Sprintf("delta-%d ", deltaCount) {
			t.Errorf("client A received %q %d times after unsubscribing, want 0 — unsubscribed connection still receiving", key, got)
		}
	}
	receivedB := counts(clientB)
	for i := deltaCount; i < deltaCount*2; i++ {
		key := fmt.Sprintf("delta-%d ", i)
		switch got := receivedB[key]; got {
		case 1:
		default:
			t.Errorf("client B received delta %d %d times, want exactly 1 — duplicate delta delivery", i, got)
		}
	}

	// Phase 3: a new view of the same thread subscribes (the reconnect /
	// view-re-entry shape) and a third block streams.
	clientC := dialHubRPC(t, hub)
	defer clientC.Close()
	if _, err := clientC.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize C: %v", err)
	}
	if _, err := clientC.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: thread.Evener.Ref, Subscribe: true}); err != nil {
		t.Fatalf("thread read C: %v", err)
	}
	streamDeltas(deltaCount*2, deltaCount*3)
	receivedC := counts(clientC)
	for i := deltaCount * 2; i < deltaCount*3; i++ {
		key := fmt.Sprintf("delta-%d ", i)
		switch got := receivedC[key]; got {
		case 1:
		default:
			t.Errorf("client C received delta %d %d times, want exactly 1 — duplicate delta delivery", i, got)
		}
	}
}
