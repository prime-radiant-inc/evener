package appserver

import "sort"

// SubscriptionDebugSnapshot is a point-in-time view of the live AppWire
// connection, subscription, and hydration-capture registry. It deliberately
// contains no notification payloads or authentication material.
type SubscriptionDebugSnapshot struct {
	RoutedSequence          uint64                         `json:"routedSequence"`
	NextHydrationGeneration uint64                         `json:"nextHydrationGeneration"`
	Connections             []ConnectionDebugSnapshot      `json:"connections"`
	Subscriptions           []SubscriptionDebugSnapshotRow `json:"subscriptions"`
}

// ConnectionDebugSnapshot describes one registered AppWire connection and
// the response-bound hydration captures it still owns.
type ConnectionDebugSnapshot struct {
	ConnectionID      string                   `json:"connectionId"`
	SendQueueDepth    int                      `json:"sendQueueDepth"`
	SendQueueCapacity int                      `json:"sendQueueCapacity"`
	PendingHydrations []HydrationDebugSnapshot `json:"pendingHydrations"`
}

// HydrationDebugSnapshot describes one capture waiting for its matching
// response to enter the connection's send queue.
type HydrationDebugSnapshot struct {
	ResponseID             string `json:"responseId"`
	ThreadID               string `json:"threadId"`
	Generation             uint64 `json:"generation"`
	Replace                bool   `json:"replace"`
	ReleaseOnErrorResponse bool   `json:"releaseOnErrorResponse"`
}

// SubscriptionDebugSnapshotRow describes the current registry entry for one
// connection and thread.
type SubscriptionDebugSnapshotRow struct {
	ConnectionID   string `json:"connectionId"`
	ThreadID       string `json:"threadId"`
	Buffering      bool   `json:"buffering"`
	Generation     uint64 `json:"generation"`
	Cut            uint64 `json:"cut"`
	BufferedFrames int    `json:"bufferedFrames"`
	Withdrawn      bool   `json:"withdrawn"`
}

// DebugSubscriptions snapshots the live registry without exposing frame
// contents. It is safe to call while broadcasts and capture responses race;
// each component is sampled under the lock that owns it.
func (s *Server) DebugSubscriptions() SubscriptionDebugSnapshot {
	s.projectionMu.Lock()
	defer s.projectionMu.Unlock()

	s.mu.RLock()
	connections := make([]*Connection, 0, len(s.conns))
	for _, conn := range s.conns {
		connections = append(connections, conn)
	}
	s.mu.RUnlock()

	out := SubscriptionDebugSnapshot{
		RoutedSequence:          s.routedSeq,
		NextHydrationGeneration: s.nextHydrationGeneration,
		Connections:             make([]ConnectionDebugSnapshot, 0, len(connections)),
		Subscriptions:           s.subs.debugSnapshot(),
	}
	for _, conn := range connections {
		out.Connections = append(out.Connections, conn.debugSnapshot())
	}
	sort.Slice(out.Connections, func(i, j int) bool {
		return out.Connections[i].ConnectionID < out.Connections[j].ConnectionID
	})
	return out
}

func (s *Subscriptions) debugSnapshot() []SubscriptionDebugSnapshotRow {
	s.mu.RLock()
	rows := make([]SubscriptionDebugSnapshotRow, 0)
	for connectionID, subscriptions := range s.byConn {
		for threadID, subscription := range subscriptions {
			rows = append(rows, SubscriptionDebugSnapshotRow{
				ConnectionID:   connectionID,
				ThreadID:       threadID,
				Buffering:      subscription.buffering,
				Generation:     subscription.generation,
				Cut:            subscription.cut,
				BufferedFrames: len(subscription.buffer),
				Withdrawn:      subscription.withdrawn,
			})
		}
	}
	s.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ConnectionID != rows[j].ConnectionID {
			return rows[i].ConnectionID < rows[j].ConnectionID
		}
		return rows[i].ThreadID < rows[j].ThreadID
	})
	return rows
}

func (c *Connection) debugSnapshot() ConnectionDebugSnapshot {
	c.sendMu.RLock()
	queueDepth := len(c.send)
	queueCapacity := cap(c.send)
	c.sendMu.RUnlock()

	c.responseMu.Lock()
	pending := make([]HydrationDebugSnapshot, 0, len(c.hydrations))
	for _, finalizer := range c.hydrations {
		pending = append(pending, HydrationDebugSnapshot{
			ResponseID:             finalizer.responseID,
			ThreadID:               finalizer.threadID,
			Generation:             finalizer.generation,
			Replace:                finalizer.replace,
			ReleaseOnErrorResponse: finalizer.releaseOnErrorResponse,
		})
	}
	c.responseMu.Unlock()
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Generation != pending[j].Generation {
			return pending[i].Generation < pending[j].Generation
		}
		return pending[i].ResponseID < pending[j].ResponseID
	})
	return ConnectionDebugSnapshot{
		ConnectionID:      c.id,
		SendQueueDepth:    queueDepth,
		SendQueueCapacity: queueCapacity,
		PendingHydrations: pending,
	}
}
