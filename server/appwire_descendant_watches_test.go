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
	srv := seedDescendantWatchServer(t, func(threadIDs []string) map[string][]agent.WatchStatusInfo {
		calls++
		assertPageIDs(t, threadIDs, "root", "child")
		// The root is now sampled too, but this seam has no fresh root answer: an
		// omitted ID leaves the root row's own envelope watches in place.
		return map[string][]agent.WatchStatusInfo{
			"child": {{ID: "watch-child", Source: "self", Events: []string{"output"}}},
		}
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

// The list path samples the whole page in ONE resolver call. A per-row call
// searched the agent's live tree once per row, so a page of many live sessions
// paid that walk once per session; the request carries every row ID, in row
// order, and the answer is looked up per row.
func TestThreadListSamplesLiveWatchesOncePerPage(t *testing.T) {
	var samples int
	var page []string
	srv := seedDescendantWatchServer(t, func(threadIDs []string) map[string][]agent.WatchStatusInfo {
		samples++
		page = append([]string(nil), threadIDs...)
		return map[string][]agent.WatchStatusInfo{
			"root":  {{ID: "watch-root-live", Source: "self", Events: []string{"output"}}},
			"child": {{ID: "watch-child", Source: "self", Events: []string{"output"}}},
		}
	})

	response, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	if samples != 1 {
		t.Fatalf("resolver samples = %d, want exactly one for the page", samples)
	}
	assertPageIDs(t, page, "root", "child")
	for index, want := range []string{"watch-root-live", "watch-child"} {
		got := response.Data[index].Evener.Diagnostics
		if got == nil || len(got.Watches) != 1 || got.Watches[0].ID != want {
			t.Fatalf("row %d diagnostics = %+v, want %s from the one sample", index, got, want)
		}
	}
}

// The root row's watches are refreshed on the LIST path too. A watch armed on
// the root session after the last diagnostics refresh (no turn boundary) appears
// in the next thread/list response, merged from the live sample exactly as a
// descendant's row is. The cached projection is untouched and the single-thread
// READ path still does not sample.
func TestThreadListSamplesRootLiveWatchesWithoutTurnBoundary(t *testing.T) {
	var rootSamples int
	srv := seedDescendantWatchServer(t, func(threadIDs []string) map[string][]agent.WatchStatusInfo {
		assertPageIDs(t, threadIDs, "root", "child")
		rootSamples++
		// The child is sampled too, with no fresh answer for it: an omitted ID
		// leaves the child row's cached projection standing.
		return map[string][]agent.WatchStatusInfo{
			"root": {{ID: "watch-root-new", Source: "self", Events: []string{"output"}}},
		}
	})
	// Nothing refreshes the diagnostics facet when a watch is armed, so the
	// cached root envelope carries no watch: this is the stale projection the
	// list read has to supersede.
	srv.mu.Lock()
	srv.appEnvelope.Detailed = &DetailedStatus{}
	srv.mu.Unlock()

	response, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	root := response.Data[0]
	if root.Evener.Diagnostics == nil || len(root.Evener.Diagnostics.Watches) != 1 || root.Evener.Diagnostics.Watches[0].ID != "watch-root-new" {
		t.Fatalf("root diagnostics = %+v, want the freshly sampled watch", root.Evener.Diagnostics)
	}

	// Only the returned copy is merged; the cached envelope stays as installed.
	srv.mu.RLock()
	cached := srv.appEnvelope.Detailed
	srv.mu.RUnlock()
	if cached == nil || len(cached.Watches) != 0 {
		t.Fatalf("cached root diagnostics = %+v, want the projection unchanged", cached)
	}

	// thread/read must not sample the root either: appThreadForID answers under
	// the subscription cut and never reaches the session.
	afterList := rootSamples
	if _, ok := srv.appThreadForID("root"); !ok {
		t.Fatal("appThreadForID(root) = false")
	}
	if rootSamples != afterList {
		t.Fatalf("thread/read sampled the root seam %d extra times, want none", rootSamples-afterList)
	}
}

// A root watch removed since the last refresh leaves the row too. The live
// resolver's non-nil empty answer is what tells "the root has no watches now"
// apart from "this ID is unknown", which leaves the cached projection alone.
func TestThreadListClearsClearedRootWatchFromLiveSample(t *testing.T) {
	srv := seedDescendantWatchServer(t, func([]string) map[string][]agent.WatchStatusInfo {
		// A present entry with an empty non-nil slice is the "no watches now"
		// answer; the child stays omitted, which leaves its row alone.
		return map[string][]agent.WatchStatusInfo{"root": {}}
	})

	response, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	root := response.Data[0]
	if root.Evener.Diagnostics != nil && len(root.Evener.Diagnostics.Watches) != 0 {
		t.Fatalf("root diagnostics = %+v, want the cleared watch gone", root.Evener.Diagnostics)
	}
}

// A DESCENDANT watch removed since the last refresh leaves the child's row on
// the next thread/list, exactly as the root's does. The resolver answers a known
// descendant with no watches with a non-nil empty sample, which replaces the
// cached watches; a nil answer is still "no fresh sample" and leaves the cached
// projection standing.
func TestThreadListClearsRemovedDescendantWatchFromLiveSample(t *testing.T) {
	answer := []agent.WatchStatusInfo{}
	srv := seedDescendantWatchServer(t, func([]string) map[string][]agent.WatchStatusInfo {
		if answer == nil {
			// No entry for the child is the nil answer: no fresh sample, so the
			// cached projection stands.
			return nil
		}
		return map[string][]agent.WatchStatusInfo{"child": answer}
	})
	// The child's cached projection still carries the watch it held before the
	// clear.
	srv.mu.Lock()
	srv.appDescendants["child"].thread.Evener.Diagnostics = &appwire.EvenerDiagnostics{
		Watches: []appwire.EvenerWatchInfo{{ID: "watch-child", Source: "self"}},
	}
	srv.mu.Unlock()

	response, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	child := response.Data[1]
	if child.Evener.Diagnostics != nil && len(child.Evener.Diagnostics.Watches) != 0 {
		t.Fatalf("cleared descendant diagnostics = %+v, want the removed watch gone", child.Evener.Diagnostics)
	}

	// A nil answer is "no fresh sample": the cached projection stands.
	answer = nil
	response, err = srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	child = response.Data[1]
	if child.Evener.Diagnostics == nil || len(child.Evener.Diagnostics.Watches) != 1 || child.Evener.Diagnostics.Watches[0].ID != "watch-child" {
		t.Fatalf("nil-answer descendant diagnostics = %+v, want the cached watch preserved", child.Evener.Diagnostics)
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
	srv := seedDescendantWatchServer(t, func([]string) map[string][]agent.WatchStatusInfo {
		return map[string][]agent.WatchStatusInfo{"child": statuses}
	})

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
	srv := seedDescendantWatchServer(t, func([]string) map[string][]agent.WatchStatusInfo {
		return map[string][]agent.WatchStatusInfo{
			"child": {{ID: "watch-child", Source: "self", Events: []string{"output"}}},
		}
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
func seedDescendantWatchServer(t *testing.T, fn func(threadIDs []string) map[string][]agent.WatchStatusInfo) *Server {
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

// assertPageIDs pins the IDs one resolver call was handed: the list path samples
// every row of the page at once, so the request carries the page's IDs in row
// order.
func assertPageIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("resolver page = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("resolver page = %v, want %v", got, want)
		}
	}
}
