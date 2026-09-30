package hub

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// A search must not pull the whole past index into memory: the fetch that
// feeds the Past group is paged at searchPastLimit per call, not math.MaxInt32,
// so it stays finite as session history grows (#2873).
func TestHubSearchBoundsThePastFetch(t *testing.T) {
	now := time.Now()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	// More matching past sessions than one page, so an unbounded fetch would
	// have to return every one of them.
	for i := range searchPastLimit + 5 {
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
			ID: hubtest.SessionID(t), UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute),
			Name: "frobnitz chatter",
		}); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	var limits []int
	past.SetSearchProbeForTest(func(limit, offset int) { limits = append(limits, limit) })
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SessionID: hubtest.SessionID(t), Status: appwire.ThreadStatusActive},
		hubcore.LiveEntry{PID: 2, SessionID: hubtest.SessionID(t), Status: appwire.ThreadStatusActive},
	)
	if _, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, now); err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 {
		t.Fatalf("Search calls=%v, want exactly the one bounded past fetch", limits)
	}
	if limits[0] != searchPastLimit {
		t.Fatalf("past fetch limit=%d, want %d (one bounded page, not math.MaxInt32)", limits[0], searchPastLimit)
	}
}

// The scope still filters over the whole index, not just the newest page: newer
// matching unarchived sessions are more numerous than searchPastLimit, so a
// fetch that bounded before filtering would return only unarchived matches and
// leave the archived scope empty. The fetch must filter before the limit cuts
// (#2873).
func TestHubSearchFindsAScopedMatchPastThePageSize(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	for i := range searchPastLimit + 5 {
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
			ID: hubtest.SessionID(t), UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute),
			Name: "frobnitz chatter",
		}); err != nil {
			t.Fatal(err)
		}
	}
	archivedID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: archivedID, UpdatedAt: now.Add(-time.Hour), Name: "frobnitz archive",
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(filepath.Join(root, "index.db"))
	if err := archive.Set("", "session", archivedID, true, now); err != nil {
		t.Fatal(err)
	}
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Archive: archive}, appwire.SearchParams{Query: "frobnitz", Scope: appwire.SearchScopeArchived}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{archivedID}) {
		t.Fatalf("archived past = %v, want the older archived session found past the newer unarchived matches", got)
	}
}

// A live session's own prompt match keeps the same rule Search applies: a query
// whose words appear non-contiguously matches through the FTS token-prefix path
// but not the substring scan, and pastMatched previously retained it. Matches
// must union FTS with the substring scan so the live session still lists (#2873).
func TestHubSearchLivePromptMatchUsesTheFTSRule(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	liveID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: liveID, UpdatedAt: now, Name: "bar and foo",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(projectsRoot, "*"), filepath.Join(root, "index.db"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{PID: 1, WorkingDir: "/projects/alpha", SessionID: liveID, Status: appwire.ThreadStatusActive})
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "foo bar"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(resp.Live); !reflect.DeepEqual(got, []string{liveID}) {
		t.Fatalf("live = %v, want the live session found by its FTS token-prefix prompt match", got)
	}
}

// A query with live sessions but no past index must not panic: the live
// prompt-match lookup treats a nil index as no past match, the same as the map
// it replaced (#2873).
func TestHubSearchWithLiveSessionsAndNoPastIndex(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SessionID: "02wMz5TxvLgZ6BB3uYgqz5", Status: appwire.ThreadStatusActive},
	)
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, time.Now())
	if err != nil {
		t.Fatalf("hubSearch: %v", err)
	}
	if got := searchIDs(resp.Live); len(got) != 0 {
		t.Fatalf("live = %v, want none with no past index and no ID or title match", got)
	}
}

func TestHubSearchIncludesMatchingPastSession(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "project-x-0123456789")
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{
		ID:             "02wMz5TxvLgZ6BB3uYgqz5",
		UpdatedAt:      time.Now(),
		Name:           "Generated Frobnitz Title",
		OriginalPrompt: "unrelated original prompt",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: idx}, appwire.SearchParams{Query: "generated"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Live == nil || resp.Past == nil {
		t.Fatalf("search arrays must be non-nil: %+v", resp)
	}
	if len(resp.Past) != 1 {
		t.Fatalf("past results=%d, want 1: %+v", len(resp.Past), resp.Past)
	}
	got := resp.Past[0]
	if got.ID != "02wMz5TxvLgZ6BB3uYgqz5" || got.Title != "Generated Frobnitz Title" || got.Ref != "local:"+got.ID {
		t.Fatalf("past result=%+v", got)
	}
	// A past session has ended: it has no live ask or escalation left to be
	// pending, the same reason a past navigation row never carries either flag.
	if got.AskPending || got.ApprovalPending {
		t.Fatalf("past result=%+v, want neither AskPending nor ApprovalPending", got)
	}
}

// TestHubSearchLiveResultCarriesApprovalPending pins #2567's wire contract: a
// live session blocked on a sandbox approval surfaces in search the same way
// it surfaces in navigation, computed from the same LiveEntry.PendingEscalation
// navigation rows read (hubcore.approvalPendingFor), not a second derivation.
func TestHubSearchLiveResultCarriesApprovalPending(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{
			PID: 1, WorkingDir: "/projects/evener", SessionID: "02wMz5TxvLgZ6BB3uYgqz5",
			Status: appwire.ThreadStatusActive, PendingEscalation: true,
		},
	)
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Roster: roster}, appwire.SearchParams{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Live) != 1 || !resp.Live[0].ApprovalPending {
		t.Fatalf("live=%+v, want one result carrying ApprovalPending", resp.Live)
	}
	if resp.Live[0].AskPending {
		t.Fatalf("live=%+v, want AskPending false with no pending question", resp.Live)
	}
}

// TestHubSearchLiveResultCarriesAskPending mirrors the approval case for an
// unanswered ask_user question (hubcore.askPendingFor's LiveEntry.PendingAsk).
func TestHubSearchLiveResultCarriesAskPending(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{
			PID: 1, WorkingDir: "/projects/evener", SessionID: "02wMz5TxvLgZ6BB3uYgqz5",
			Status: appwire.ThreadStatusAwaiting, PendingAsk: true,
		},
	)
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Roster: roster}, appwire.SearchParams{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Live) != 1 || !resp.Live[0].AskPending {
		t.Fatalf("live=%+v, want one result carrying AskPending", resp.Live)
	}
	if resp.Live[0].ApprovalPending {
		t.Fatalf("live=%+v, want ApprovalPending false with no pending escalation", resp.Live)
	}
}

func TestHubSearchOrdersLiveResultsByPastAwareRecency(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	const (
		newestID = "02wMz5Txv1C3Hut0M8GCeB"
		olderID  = "02wMz5Txv2enqVTitaig6F"
		tieAID   = "02wMz5Txv47YP64RR3B9YJ"
		tieBID   = "02wMz5Txv5aIxgf9yVdd0N"
	)
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 2, StartedAt: base.Add(-time.Hour), WorkingDir: "/projects/evener", SessionID: olderID, Status: appwire.ThreadStatusIdle},
		hubcore.LiveEntry{PID: 1, StartedAt: base, WorkingDir: "/projects/evener", SessionID: newestID, Status: appwire.ThreadStatusIdle},
		hubcore.LiveEntry{PID: 4, StartedAt: base.Add(-2 * time.Hour), WorkingDir: "/projects/evener", SessionID: tieBID, Status: appwire.ThreadStatusIdle},
		hubcore.LiveEntry{PID: 3, StartedAt: base.Add(-2 * time.Hour), WorkingDir: "/projects/evener", SessionID: tieAID, Status: appwire.ThreadStatusIdle},
	)

	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Roster: roster}, appwire.SearchParams{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(resp.Live))
	for _, result := range resp.Live {
		got = append(got, result.ID)
	}
	want := []string{newestID, olderID, tieAID, tieBID}
	if len(got) != len(want) {
		t.Fatalf("live result count=%d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("live order=%v, want %v", got, want)
		}
	}
}

func TestHubRPCSearchRoundTrip(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "project-x-0123456789")
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{
		ID:             "02wMz5TxvLgZ6BB3uYgqz5",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix the frobnitz",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.Search(context.Background(), appwire.SearchParams{Query: "frobnitz"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Past) != 1 || resp.Past[0].ID != "02wMz5TxvLgZ6BB3uYgqz5" {
		t.Fatalf("past=%+v", resp.Past)
	}
}
