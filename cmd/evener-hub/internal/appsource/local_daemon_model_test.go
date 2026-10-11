package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller names each root's current
// model on its row, ahead of the model the root started on, so a switch
// reaches the controller (S17). A row whose daemon no probe has reached keeps
// the model it started on.
func TestLocalDaemonSourceListNamesTheRootsCurrentModel(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/switched", ThreadID: "th_switched", SessionID: "sess_switched", Model: "kimi-k3"}, Status: appwire.ThreadStatusIdle, CurrentModel: "gpt-5.6"},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/fresh", ThreadID: "th_fresh", SessionID: "sess_fresh", Model: "kimi-k3"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]string{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.ModelProvider
	}
	if byID["th_switched"] != "gpt-5.6" || byID["th_fresh"] != "kimi-k3" {
		t.Fatalf("models = %q, want th_switched's current gpt-5.6 and th_fresh's start model kimi-k3", byID)
	}
}
