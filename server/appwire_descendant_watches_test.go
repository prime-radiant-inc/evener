package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

// A descendant thread never carried a diagnostics block: the projection is
// event-shaped and no case writes Evener.Diagnostics, so the child's row in
// thread/list had no watches even when the child session held some. The
// thread path now samples the child's own live watches through the accessor
// seam and merges them onto the returned thread, while the root row keeps
// exactly the watches its own envelope already carried.
func TestThreadListCarriesDescendantSessionWatches(t *testing.T) {
	srv := seedDescendantWatchServer(t, func(threadID string) []agent.WatchStatusInfo {
		if threadID != "child" {
			t.Errorf("accessor consulted for %q, want only the descendant", threadID)
			return nil
		}
		return []agent.WatchStatusInfo{{ID: "watch-child", Source: "self", Events: []string{"output"}}}
	})

	response, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	if len(response.Data) != 2 || response.Data[0].ID != "root" {
		t.Fatalf("thread list = %+v, want root then child", response.Data)
	}
	// The root's own watches are untouched.
	if got := response.Data[0].Evener.Diagnostics; got == nil || len(got.Watches) != 1 || got.Watches[0].ID != "watch-root" {
		t.Fatalf("root diagnostics = %+v, want only its own watch", got)
	}
	child := response.Data[1]
	if child.Evener.Diagnostics == nil || len(child.Evener.Diagnostics.Watches) != 1 || child.Evener.Diagnostics.Watches[0].ID != "watch-child" {
		t.Fatalf("child diagnostics = %+v, want the child's own watch", child.Evener.Diagnostics)
	}
	if child.Evener.Diagnostics.Watches[0].Source != "self" {
		t.Fatalf("child watch = %+v, want the sampled row", child.Evener.Diagnostics.Watches[0])
	}

	// thread/read samples through the same seam.
	read, ok := srv.appThreadForID("child")
	if !ok {
		t.Fatal("appThreadForID(child) = false")
	}
	if read.Evener.Diagnostics == nil || len(read.Evener.Diagnostics.Watches) != 1 || read.Evener.Diagnostics.Watches[0].ID != "watch-child" {
		t.Fatalf("read child diagnostics = %+v, want the child's own watch", read.Evener.Diagnostics)
	}
	// The root read path is its own envelope, never the descendant seam.
	root, ok := srv.appThreadForID("root")
	if !ok {
		t.Fatal("appThreadForID(root) = false")
	}
	if root.Evener.Diagnostics == nil || len(root.Evener.Diagnostics.Watches) != 1 || root.Evener.Diagnostics.Watches[0].ID != "watch-root" {
		t.Fatalf("read root diagnostics = %+v, want only its own watch", root.Evener.Diagnostics)
	}
}

// The descendant rows are rebuilt per call and the slices are copied, so a
// caller mutating a response can never reach the agent state behind the seam.
func TestThreadListDescendantWatchesDoNotAliasTheSource(t *testing.T) {
	statuses := []agent.WatchStatusInfo{{
		ID:            "watch-child",
		Source:        "self",
		Events:        []string{"output"},
		DeliveryTimes: []string{"t0"},
	}}
	srv := seedDescendantWatchServer(t, func(string) []agent.WatchStatusInfo { return statuses })

	response, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	rows := response.Data[1].Evener.Diagnostics.Watches
	if len(rows) != 1 {
		t.Fatalf("child watches = %+v, want one row", rows)
	}
	response.Data[1].Evener.Diagnostics.Watches[0].ID = "mutated"
	response.Data[1].Evener.Diagnostics.Watches[0].Events[0] = "mutated"
	response.Data[1].Evener.Diagnostics.Watches[0].DeliveryTimes[0] = "mutated"

	if statuses[0].ID != "watch-child" || statuses[0].Events[0] != "output" || statuses[0].DeliveryTimes[0] != "t0" {
		t.Fatalf("response aliased the agent status: %+v", statuses)
	}
}

// seedDescendantWatchServer builds a server with a root envelope carrying one
// watch and a child descendant thread, and installs the descendant accessor.
func seedDescendantWatchServer(t *testing.T, fn func(threadID string) []agent.WatchStatusInfo) *Server {
	t.Helper()
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	srv.mu.Lock()
	srv.appEnvelope.Detailed = &DetailedStatus{Watches: []agent.WatchStatusInfo{{ID: "watch-root", Source: "timer"}}}
	srv.appDescendants["child"] = &appDescendantProjection{thread: appwire.Thread{
		ID: "child", SessionID: "child", Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:child", Kind: "subagent"},
	}}
	srv.mu.Unlock()
	srv.SetDescendantLiveWatchesFunc(fn)
	return srv
}
