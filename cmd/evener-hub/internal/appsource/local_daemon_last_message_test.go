package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each root's last
// message on its row, and no key on a row with none (S1d).
func TestLocalDaemonSourceListCarriesTheRootsLastMessage(t *testing.T) {
	const message = "Three layouts are ready for review."
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/finished", ThreadID: "th_finished", SessionID: "sess_finished"}, Status: appwire.ThreadStatusIdle, LastMessage: message},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/fresh", ThreadID: "th_fresh", SessionID: "sess_fresh"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]string{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.LastMessage
	}
	if byID["th_finished"] != message || byID["th_fresh"] != "" {
		t.Fatalf("last messages = %q, want th_finished's %q and none on th_fresh", byID, message)
	}
}
