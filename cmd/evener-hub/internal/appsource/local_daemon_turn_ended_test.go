package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each root's turn end
// on the row, so the controller can tell Finished from Idle for a remote
// session (S4).
func TestLocalDaemonSourceListCarriesTheRootsTurnEnd(t *testing.T) {
	const ended = int64(1_790_000_000_123)
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/ended", ThreadID: "th_ended", SessionID: "sess_ended"}, Status: appwire.ThreadStatusIdle, LastTurnEndedAt: ended},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/fresh", ThreadID: "th_fresh", SessionID: "sess_fresh"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	endedByID := map[string]int64{}
	for _, thread := range resp.Data {
		endedByID[thread.ID] = thread.Evener.LastTurnEndedAt
	}
	if endedByID["th_ended"] != ended || endedByID["th_fresh"] != 0 {
		t.Fatalf("turn ends = %v, want th_ended at %d and none on th_fresh", endedByID, ended)
	}
}
