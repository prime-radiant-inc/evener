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
// thread/list path now samples the child's own live watches through the
// accessor seam and merges them onto the returned thread, while the root row
// keeps exactly the watches its own envelope already carried. The single-thread
// read path does not sample: it returns the already-projected thread.
func TestThreadListCarriesDescendantSessionWatches(t *testing.T) {
	var calls int
	srv := seedDescendantWatchServer(t, func(threadID string) []agent.WatchStatusInfo {
		if threadID != "child" {
			t.Errorf("accessor consulted for %q, want only the descendant", threadID)
			return nil
		}
		calls++
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

	// thread/read must NOT sample through the seam. appThreadForID answers the
	// read path, which runs under the subscription cut, whose contract
	// (appThreadReadSnapshot) is cheap and never blocking on a session lock. The
	// child read therefore returns the already-projected thread -- no
	// diagnostics block -- while only the list path above samples the child's
	// own watches.
	afterList := calls
	read, ok := srv.appThreadForID("child")
	if !ok {
		t.Fatal("appThreadForID(child) = false")
	}
	if read.Evener.Diagnostics != nil {
		t.Fatalf("read child diagnostics = %+v, want the projected thread with no sampled watches", read.Evener.Diagnostics)
	}
	if calls != afterList {
		t.Fatalf("thread/read consulted the descendant seam %d extra times, want none", calls-afterList)
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

// The merged diagnostics block is a deep copy, not just a fresh Watches slice:
// the rest of the cached projection's block (Tools, Skills, Agents, ...) must not
// be reachable from the returned thread.
func TestThreadListDescendantWatchesDeepCopyDiagnostics(t *testing.T) {
	srv := seedDescendantWatchServer(t, func(string) []agent.WatchStatusInfo {
		return []agent.WatchStatusInfo{{ID: "watch-child", Source: "self", Events: []string{"output"}}}
	})
	srv.mu.Lock()
	srv.appDescendants["child"].thread.Evener.Diagnostics = &appwire.EvenerDiagnostics{
		Tools:  []appwire.EvenerToolInfo{{Name: "orig-tool"}},
		Skills: []appwire.EvenerSkillInfo{{Name: "orig-skill"}},
		Agents: []string{"orig-agent"},
	}
	srv.mu.Unlock()

	response, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	got := response.Data[1].Evener.Diagnostics
	if got == nil || len(got.Tools) != 1 || len(got.Skills) != 1 || len(got.Agents) != 1 {
		t.Fatalf("merged diagnostics = %+v, want the projection's block plus watches", got)
	}
	if got.Tools[0].Name != "orig-tool" || got.Skills[0].Name != "orig-skill" || got.Agents[0] != "orig-agent" {
		t.Fatalf("merged diagnostics = %+v, want the projection's own fields preserved", got)
	}
	got.Tools[0].Name = "mutated"
	got.Skills[0].Name = "mutated"
	got.Agents[0] = "mutated"

	srv.mu.RLock()
	cached := srv.appDescendants["child"].thread.Evener.Diagnostics
	srv.mu.RUnlock()
	if cached.Tools[0].Name != "orig-tool" || cached.Skills[0].Name != "orig-skill" || cached.Agents[0] != "orig-agent" {
		t.Fatalf("response aliased the cached projection diagnostics: %+v", cached)
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
