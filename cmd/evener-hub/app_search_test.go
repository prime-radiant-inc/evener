package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
