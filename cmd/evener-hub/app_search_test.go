package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// A search must not pull the whole past index into memory: the fetch that
// feeds the Past group is bounded by the page size plus the live sessions the
// group skips, not math.MaxInt32, so it stays finite as session history grows
// (#2873).
func TestHubSearchBoundsThePastFetch(t *testing.T) {
	now := time.Now()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	// More matching past sessions than the bound, so an unbounded fetch would
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
	if want := searchPastLimit + 2; limits[0] != want {
		t.Fatalf("past fetch limit=%d, want %d (the page size plus the live sessions the group skips)", limits[0], want)
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
