package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubRelaySharedSessionAliasesDeliverEachNotificationOnce(t *testing.T) {
	const (
		rootRef  = "local:root-thread"
		childRef = "local:child-thread"
	)
	pool := &aliasRelayPool{}
	source := &aliasRelaySource{pool: pool}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	longLived := dialHubRPC(t, hub)
	defer longLived.Close()
	if _, err := longLived.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize long-lived client: %v", err)
	}
	for _, ref := range []string{rootRef, childRef} {
		if _, err := longLived.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
			t.Fatalf("subscribe long-lived client to %s: %v", ref, err)
		}
	}
	if got := pool.listenerCount(); got != 2 {
		t.Fatalf("relay listeners = %d, want two alias handles sharing one session", got)
	}

	fresh := dialHubRPC(t, hub)
	defer fresh.Close()
	if _, err := fresh.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize fresh client: %v", err)
	}
	if _, err := fresh.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatalf("subscribe fresh client: %v", err)
	}

	params, err := json.Marshal(appwire.ReasoningSummaryDeltaParams{
		ThreadID:     "root-thread",
		Ref:          rootRef,
		TurnID:       "turn-alias",
		ItemID:       "item-alias",
		SummaryIndex: 0,
		Delta:        "one logical delta",
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.emit(t, appwire.Notification{Method: appwire.NotifyReasoningSummaryDelta, Params: params})
	// Every alias fanout has acknowledged and enqueued the delta before emit
	// returns. This connection-wide marker is therefore an ordered drain barrier.
	appServer.BroadcastAll("test/alias-barrier", map[string]any{})

	if got := aliasDeltaCountUntilBarrier(t, longLived, "one logical delta"); got != 1 {
		t.Fatalf("long-lived root+child client received delta %d times, want once", got)
	}
	if got := aliasDeltaCountUntilBarrier(t, fresh, "one logical delta"); got != 1 {
		t.Fatalf("fresh root-only client received delta %d times, want once", got)
	}

	params, err = json.Marshal(appwire.ReasoningSummaryDeltaParams{
		ThreadID:     "child-thread",
		Ref:          childRef,
		TurnID:       "turn-alias",
		ItemID:       "item-alias-child",
		SummaryIndex: 0,
		Delta:        "one child delta",
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.emit(t, appwire.Notification{Method: appwire.NotifyReasoningSummaryDelta, Params: params})
	appServer.BroadcastAll("test/alias-barrier", map[string]any{})
	if got := aliasDeltaCountUntilBarrier(t, longLived, "one child delta"); got != 1 {
		t.Fatalf("long-lived root+child client received child delta %d times, want once", got)
	}
	if got := aliasDeltaCountUntilBarrier(t, fresh, "one child delta"); got != 0 {
		t.Fatalf("fresh root-only client received child delta %d times, want none", got)
	}
}

type aliasRelaySource struct {
	relayLifecycleSource
	pool *aliasRelayPool
}

func (*aliasRelaySource) ID() string { return "local" }

func (s *aliasRelaySource) AcquireRelaySession(appwire.ThreadReadParams) (appsource.RelaySessionLease, error) {
	return &aliasRelayLease{pool: s.pool}, nil
}

type aliasRelayLease struct {
	pool *aliasRelayPool
}

func (l *aliasRelayLease) Read(_ context.Context, params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appsource.RelayReadResult{}, err
	}
	return appsource.RelayReadResult{
		Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        ref.ThreadID,
			SessionID: ref.ThreadID,
			Source:    ref.SourceID,
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}},
		Handoff: &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
	}, nil
}

func (l *aliasRelayLease) Listen(context.Context) (<-chan appsource.RelayDelivery, error) {
	return l.pool.listen(), nil
}

func (*aliasRelayLease) Close() {}

type aliasRelayPool struct {
	mu        sync.Mutex
	listeners []chan appsource.RelayDelivery
}

func (p *aliasRelayPool) listen() <-chan appsource.RelayDelivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	listener := make(chan appsource.RelayDelivery)
	p.listeners = append(p.listeners, listener)
	return listener
}

func (p *aliasRelayPool) listenerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.listeners)
}

func (p *aliasRelayPool) emit(t *testing.T, notification appwire.Notification) {
	t.Helper()
	p.mu.Lock()
	listeners := append([]chan appsource.RelayDelivery(nil), p.listeners...)
	p.mu.Unlock()
	for _, listener := range listeners {
		ack := make(chan struct{})
		var once sync.Once
		delivery := appsource.RelayDelivery{
			Notification: notification,
			Acknowledge:  func() { once.Do(func() { close(ack) }) },
		}
		select {
		case listener <- delivery:
		case <-time.After(2 * time.Second):
			t.Fatal("relay listener did not accept notification")
		}
		select {
		case <-ack:
		case <-time.After(2 * time.Second):
			t.Fatal("relay listener did not acknowledge notification")
		}
	}
}

func aliasDeltaCountUntilBarrier(t *testing.T, client *appwire.Client, delta string) int {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	count := 0
	for {
		select {
		case notification, ok := <-client.Notifications():
			if !ok {
				t.Fatal("client notification stream closed before barrier")
			}
			if notification.Method == "test/alias-barrier" {
				return count
			}
			if notification.Method == appwire.NotifyReasoningSummaryDelta {
				var params appwire.ReasoningSummaryDeltaParams
				if json.Unmarshal(notification.Params, &params) == nil && params.Delta == delta {
					count++
				}
			}
		case <-timer.C:
			t.Fatal("timed out waiting for alias delivery barrier")
		}
	}
}
