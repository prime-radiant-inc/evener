package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

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

	resp := hubSearch(hubcore.WebConfig{Past: idx}, appwire.SearchParams{Query: "generated"})
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

// TestHubSearchOmitsLiveSessionFromPast pins #2681: a live session writes its
// meta file soon after it starts, so the past index holds its record too. Search
// must not return it twice; it belongs in Live alone, never alongside an ended
// row carrying the same ref.
func TestHubSearchOmitsLiveSessionFromPast(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "project-x-0123456789")
	const liveID = "02wMz5TxvLgZ6BB3uYgqz5"
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{
		ID:             liveID,
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
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, WorkingDir: "/projects/alpha", SessionID: liveID, Status: appwire.ThreadStatusActive},
	)

	resp := hubSearch(hubcore.WebConfig{Roster: roster, Past: idx}, appwire.SearchParams{Query: "generated"})
	if len(resp.Live) != 1 || resp.Live[0].ID != liveID {
		t.Fatalf("live=%+v, want the running session only", resp.Live)
	}
	if len(resp.Past) != 0 {
		t.Fatalf("past=%+v, want the live session omitted", resp.Past)
	}
}

// TestHubSearchKeepsLiveSessionMatchingPastFieldsFindable guards the regression
// a naive live-id suppression would introduce: the past matcher (hubcore.matches)
// also searches OriginalPrompt and EnvInfo.WorkingDir, which the live filter does
// not. A running session whose only match is its working directory must stay
// discoverable, not vanish from both lists. Suppression applies only to rows the
// live pass actually emitted.
func TestHubSearchKeepsLiveSessionMatchingPastFieldsFindable(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "project-x-0123456789")
	const liveID = "02wMz5TxvLgZ6BB3uYgqz5"
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{
		ID:             liveID,
		UpdatedAt:      time.Now(),
		Name:           "Unrelated Title",
		OriginalPrompt: "unrelated original prompt",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, WorkingDir: "/projects/alpha", SessionID: liveID, Status: appwire.ThreadStatusActive},
	)

	resp := hubSearch(hubcore.WebConfig{Roster: roster, Past: idx}, appwire.SearchParams{Query: "alpha"})
	found := 0
	for _, r := range append(append([]appwire.SearchResult{}, resp.Live...), resp.Past...) {
		if r.ID == liveID {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("live=%+v past=%+v: session matching on its working dir found %d times, want exactly 1", resp.Live, resp.Past, found)
	}
}

// TestHubSearchFillsPastLimitAfterSuppressingLiveRow pins the over-fetch in
// hubSearch: the suppressed live row sorts inside the first searchPastLimit
// matches, so without fetching past searchPastLimit+suppressed rows and trimming,
// past would come back one short of its limit and hide an ended session.
func TestHubSearchFillsPastLimitAfterSuppressingLiveRow(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "project-x-0123456789")
	const liveID = "02wMz5TxvLgZ6BB3uYgqz5"
	now := time.Now()
	// The live session's meta is the newest, so its past row sorts first — inside
	// the first searchPastLimit matches, where the suppression can cost a slot.
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{
		ID:        liveID,
		UpdatedAt: now,
		Name:      "Frobnitz Live",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < searchPastLimit+1; i++ {
		id, err := identifier.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.SaveSessionMeta(project, schema.SessionMeta{
			ID:        id,
			UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute),
			Name:      "Frobnitz Ended",
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, WorkingDir: "/projects/alpha", SessionID: liveID, Status: appwire.ThreadStatusActive},
	)

	resp := hubSearch(hubcore.WebConfig{Roster: roster, Past: idx}, appwire.SearchParams{Query: "frobnitz"})
	if len(resp.Live) != 1 || resp.Live[0].ID != liveID {
		t.Fatalf("live=%+v, want the running session", resp.Live)
	}
	if len(resp.Past) != searchPastLimit {
		t.Fatalf("past results=%d, want %d after suppressing the live row", len(resp.Past), searchPastLimit)
	}
	for _, r := range resp.Past {
		if r.ID == liveID {
			t.Fatalf("past includes live session %s: %+v", liveID, resp.Past)
		}
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
	resp := hubSearch(hubcore.WebConfig{Roster: roster}, appwire.SearchParams{})
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
	resp := hubSearch(hubcore.WebConfig{Roster: roster}, appwire.SearchParams{})
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

	resp := hubSearch(hubcore.WebConfig{Roster: roster}, appwire.SearchParams{})
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
