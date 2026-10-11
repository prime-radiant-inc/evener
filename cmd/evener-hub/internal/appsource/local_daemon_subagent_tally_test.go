package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each root's tally on
// the row when its tree has a subagent, and no key otherwise (S3).
func TestLocalDaemonSourceListCarriesTheRootsSubagentTally(t *testing.T) {
	tally := appwire.SubagentTally{Running: 2, Failed: 1, Done: 5}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/tree", ThreadID: "th_tree", SessionID: "sess_tree"}, Status: appwire.ThreadStatusActive, Subagents: tally},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/alone", ThreadID: "th_alone", SessionID: "sess_alone"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*appwire.SubagentTally{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.Subagents
	}
	if !reflect.DeepEqual(byID["th_tree"], &tally) || byID["th_alone"] != nil {
		t.Fatalf("tallies = %+v, want th_tree's %+v and none on th_alone", byID, tally)
	}
}
