package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries a failed root's
// failure summary on its row, and no key on a healthy row (S1c).
func TestLocalDaemonSourceListCarriesTheRootsFailure(t *testing.T) {
	failure := &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 401}}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/failed", ThreadID: "th_failed", SessionID: "sess_failed"}, Status: appwire.ThreadStatusSystemError, Failure: failure},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/idle", ThreadID: "th_idle", SessionID: "sess_idle"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*appwire.ThreadFailure{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.Failure
	}
	if !reflect.DeepEqual(byID["th_failed"], failure) || byID["th_idle"] != nil {
		t.Fatalf("failures = %+v, want th_failed's %+v and none on th_idle", byID, failure)
	}
	byID["th_failed"].Cause.Status = 500
	if failure.Cause.Status != 401 {
		t.Fatal("a listed row aliases the roster's failure")
	}
}
