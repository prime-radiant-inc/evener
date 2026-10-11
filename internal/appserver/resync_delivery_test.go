package appserver

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestUndeliverableResyncEvictsSubscription pins spec issue 5: the appserver
// already answers a full outbound buffer during a commit by evicting the
// connection (evictSlowConsumer), which ends every subscription that
// connection held. A committed evener/thread/resync that cannot be delivered
// goes through the same CommitProjection -> evictSlowConsumer path as any
// other committed notification, so this test proves no special-casing is
// needed for it: the subscription ends, and a reconnecting client's fresh
// subscription reads the current epoch rather than anything stale.
func TestUndeliverableResyncEvictsSubscription(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	notifier := NewNotifier(10)

	conn := server.NewConnection("conn-stuck")
	server.registerConnection(conn)
	conn.Subscribe("th_1")

	// Fill the outbound buffer by construction, not by timing: the consumer
	// never drains here, so the buffer is full before the commit runs, the
	// same way TestServerBroadcastAllEvictsSlowConsumer and
	// TestSlowConsumerEvictionIsReported build a stuck connection.
	for range appwire.NotificationBufferCap {
		conn.enqueue(appwire.Message{})
	}

	// The epoch a resync carries; the commit advances it, and a reconnecting
	// client's read must observe the advanced value rather than a stale one
	// cached from before the eviction.
	var epoch uint64 = 1
	currentEpoch := func() uint64 { return epoch }

	server.CommitProjection(func() []SequencedNotification {
		epoch = 2
		return []SequencedNotification{
			notifier.Record("th_1", appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
				ThreadID: "th_1",
				Epoch:    currentEpoch(),
			}),
		}
	})

	server.mu.RLock()
	_, stillRegistered := server.conns[conn.id]
	server.mu.RUnlock()
	if stillRegistered {
		t.Fatal("a connection with a full outbound buffer was not evicted by an undeliverable resync")
	}

	if threads := server.subs.Threads(conn.id); len(threads) != 0 {
		t.Fatalf("the evicted connection still has live subscriptions: %v", threads)
	}

	// The client reconnects: a fresh connection subscribes and reads. Its
	// snapshot must reflect the epoch installed by the commit that evicted the
	// old connection, proving the failed delivery left no stale state behind.
	reconnected := server.NewConnection("conn-reconnected")
	server.registerConnection(reconnected)
	reconnected.Subscribe("th_1")
	if got := currentEpoch(); got != 2 {
		t.Fatalf("reconnected client's snapshot epoch = %d, want 2", got)
	}
}
