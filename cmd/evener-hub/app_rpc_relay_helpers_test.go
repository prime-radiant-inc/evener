package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

type relaySubscribeResult struct {
	notifications <-chan appwire.Notification
	err           error
}

type scriptedRelaySource struct {
	relayLifecycleSource
	id             string
	results        <-chan relaySubscribeResult
	subscribeCalls chan<- struct{}
}

func (s *scriptedRelaySource) ID() string {
	if s.id != "" {
		return s.id
	}
	return s.relayLifecycleSource.ID()
}

func (s *scriptedRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	select {
	case s.subscribeCalls <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case result := <-s.results:
		return result.notifications, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func awaitRelaySubscribeCall(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay subscribe call")
	}
}

func relayDeltaNotification(t *testing.T, threadID, delta string) appwire.Notification {
	t.Helper()
	return appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			Delta:    delta,
		}),
	}
}

// codexRelayThread returns the relay-source thread fixture the codex
// subscription scenarios key on: ID and session pinned to threadID, send
// capability on the codex ref.
func codexRelayThread(threadID string) appwire.Thread {
	return appwire.Thread{
		ID:        threadID,
		SessionID: threadID,
		Source:    "codex",
		Evener:    appwire.EvenerThread{Ref: "codex:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
	}
}
