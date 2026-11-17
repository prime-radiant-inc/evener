package appserver

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apphistory"
)

// fanoutSelections is the set of content filters a benchmark's subscribers
// spread over, which is how a hub's connections actually look: many clients at
// a few filter settings, each with its own filter generation.
func fanoutSelections() []appwire.ThreadContentParams {
	no := false
	return []appwire.ThreadContentParams{
		{},
		{IncludeThinking: &no},
		{IncludeToolCalls: &no},
		{IncludeThinking: &no, IncludeToolCalls: &no},
		{IncludeConversation: &no},
	}
}

// fanoutFixture builds a server whose subscribers cycle through the benchmark's
// selections, and returns a drain func that empties every connection's outbound
// channel without blocking or spinning. The drain runs outside the measured
// region, so a benchmark iteration measures fanout rather than eviction.
func fanoutFixture(b *testing.B, subscribers int) (*Server, func()) {
	b.Helper()
	s := NewServer(ServerConfig{})
	selections := fanoutSelections()
	for i := range subscribers {
		conn := s.NewConnection("conn-" + strconv.Itoa(i))
		conn.setInitialized()
		s.registerConnection(conn)
		conn.Subscribe("thread")
		s.subs.setContent(conn.id, "thread", 0, apphistory.NewFilter(selections[i%len(selections)].Selection()))
	}
	drain := func() {
		for i := range subscribers {
			conn := s.conns["conn-"+strconv.Itoa(i)]
			for {
				select {
				case <-conn.send:
				default:
					goto drained
				}
			}
		drained:
		}
	}
	return s, drain
}

// historyPayload is one recorded history update carrying a realistic mix of
// items, at a size a hub actually relays (24 items, mixed types).
func historyPayload() appwire.HistoryUpdatedParams {
	items := make([]appwire.ThreadItem, 0, 24)
	for i := range 12 {
		suffix := strconv.Itoa(i)
		items = append(items,
			appwire.ThreadItem{Type: "agentMessage", ID: "a" + suffix, Text: "a reasonably sized assistant message body for the fanout benchmark"},
			appwire.ThreadItem{Type: "reasoning", ID: "r" + suffix, Text: "a reasonably sized reasoning body for the fanout benchmark"},
		)
	}
	return appwire.HistoryUpdatedParams{
		ThreadID: "thread",
		Ref:      "local:thread",
		Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: int64(len(items))},
		Items:    items,
	}
}

// BenchmarkFilteredFanout measures one broadcast to many filtered subscribers.
// gate_hold_ns/broadcast is the headline: it is how long the delivery gate —
// the lock every connected client's delivery waits on — is held, which is the
// latency this issue is about. encodes/broadcast is the payload work that gate
// paid for. ns/op includes the drain between broadcasts and is not the metric.
func BenchmarkFilteredFanout(b *testing.B) {
	for _, subscribers := range []int{4, 32} {
		b.Run("subscribers="+strconv.Itoa(subscribers), func(b *testing.B) {
			s, drain := fanoutFixture(b, subscribers)
			payload := historyPayload()
			var encodes, held atomic.Int64
			s.afterFanoutEncoded = func(count int) { encodes.Add(int64(count)) }
			s.deliveryGateHeld = func(d time.Duration) { held.Add(int64(d)) }
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				s.Broadcast("thread", appwire.NotifyHistoryUpdated, payload)
				b.StopTimer()
				drain()
				b.StartTimer()
			}
			b.StopTimer()
			b.ReportMetric(float64(held.Load())/float64(b.N), "gate_hold_ns/broadcast")
			b.ReportMetric(float64(encodes.Load())/float64(b.N), "encodes/broadcast")
		})
	}
}

// BenchmarkFilteredFanoutOverlay measures the per-chunk overlay path: an upsert
// followed by a delta, the hot shape for a streaming turn. A delta reaches
// every reader as the recorded bytes, so its gate cost is routing plus enqueue.
func BenchmarkFilteredFanoutOverlay(b *testing.B) {
	for _, subscribers := range []int{4, 32} {
		b.Run("subscribers="+strconv.Itoa(subscribers), func(b *testing.B) {
			s, drain := fanoutFixture(b, subscribers)
			upsert := appwire.OverlayUpsertedParams{
				ThreadID: "thread",
				Ref:      "local:thread",
				Item: appwire.OverlayItem{
					Key: "stream:s1:agentMessage", Kind: appwire.OverlayStream, StreamID: "s1", RoundID: "round1",
					Item: appwire.ThreadItem{Type: "agentMessage", ID: "a1", Text: "streamed text"},
				},
			}
			delta := appwire.OverlayDeltaParams{ThreadID: "thread", Key: upsert.Item.Key, Field: appwire.OverlayDeltaText, Delta: "chunk"}
			var encodes, held atomic.Int64
			s.afterFanoutEncoded = func(count int) { encodes.Add(int64(count)) }
			s.deliveryGateHeld = func(d time.Duration) { held.Add(int64(d)) }
			// One iteration is one upsert plus one delta, which is one chunk of a
			// streaming turn.
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				s.Broadcast("thread", appwire.NotifyOverlayUpserted, upsert)
				s.Broadcast("thread", appwire.NotifyOverlayDelta, delta)
				b.StopTimer()
				drain()
				b.StartTimer()
			}
			b.StopTimer()
			b.ReportMetric(float64(held.Load())/float64(b.N)/2, "gate_hold_ns/broadcast")
			b.ReportMetric(float64(encodes.Load())/float64(b.N)/2, "encodes/broadcast")
		})
	}
}
