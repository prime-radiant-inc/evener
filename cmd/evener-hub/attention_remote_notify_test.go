package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestWatchHubAttentionNotifiesForRemoteSessionApproval pins #2529. Sessions on
// other hosts reach the hub only through the remote-thread cache, so the
// attention watcher must derive from the same remote-inclusive inputs the
// navigation tree uses. Before the fix the watcher read the roster alone and
// never saw the remote session, so no evener/attention/changed broadcast fired
// when that session gained a pending approval.
func TestWatchHubAttentionNotifiesForRemoteSessionApproval(t *testing.T) {
	oldTicker := hubTicker
	hubTicker = func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	}
	t.Cleanup(func() { hubTicker = oldTicker })

	root := t.TempDir()
	cache := &hubcore.RemoteThreadCache{}
	archive := hubcore.NewArchiveStore(filepath.Join(root, "archive.db"))
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Past:              hubcore.NewPastIndex(filepath.Join(root, "projects", "*")),
		RemoteThreadCache: cache,
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	poke := make(chan struct{})
	done := make(chan struct{})
	go func() {
		watchHubAttention(ctx, poke, archive, web)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// The initial seed run is synchronous before the watcher loop, so this
	// unbuffered send returns only after that seed recorded a baseline with the
	// remote approval still absent.
	poke <- struct{}{}

	cache.StoreSnapshot([]appwire.Thread{{
		ID:        "remote-thread",
		SessionID: "remote-thread",
		Source:    "remote-host",
		Name:      "Remote session",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{
			Ref:                "remote-host:remote-thread",
			PendingEscalations: []appwire.SandboxEscalationRequested{{}},
		},
	}}, true)

	poke <- struct{}{}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case notif := <-client.Notifications():
			if notif.Method != appwire.NotifyEvenerAttentionChanged {
				continue
			}
			var payload appwire.AttentionChangedPayload
			if err := json.Unmarshal(notif.Params, &payload); err != nil {
				t.Fatalf("decode %s: %v", notif.Method, err)
			}
			for _, changed := range payload.Changed {
				if changed.ID == "remote-host:remote-thread" && changed.Level == "needs_you" && changed.ApprovalPending {
					return
				}
			}
			t.Fatalf("attention change did not carry the remote approval: %+v", payload)
		case <-deadline:
			t.Fatal("timed out waiting for evener/attention/changed after a remote session gained a pending approval")
		}
	}
}
