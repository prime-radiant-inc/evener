package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
)

func writeMeta(t *testing.T, dir string, meta agent.SessionMeta) {
	t.Helper()
	if err := agent.SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
}

func TestPastIndex_RebuildLoadsAllMetas(t *testing.T) {
	root := t.TempDir()
	projA := filepath.Join(root, "projects", "aaa")
	projB := filepath.Join(root, "projects", "bbb")
	for _, p := range []string{projA, projB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	writeMeta(t, projA, agent.SessionMeta{
		ID:           "01A",
		Model:        "gpt-5.2",
		EnvInfo:      agent.EnvironmentInfo{WorkingDir: "/work/a"},
		CreatedAt:    now.Add(-2 * time.Hour),
		UpdatedAt:    now.Add(-1 * time.Hour),
		OriginalTask: "fix the bug",
	})
	writeMeta(t, projB, agent.SessionMeta{
		ID:           "01B",
		Model:        "claude-opus-4-7",
		EnvInfo:      agent.EnvironmentInfo{WorkingDir: "/work/b"},
		CreatedAt:    now.Add(-30 * time.Minute),
		UpdatedAt:    now,
		OriginalTask: "refactor auth",
	})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got := idx.All()
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	// Sorted by UpdatedAt desc.
	if got[0].ID != "01B" {
		t.Errorf("first: %s", got[0].ID)
	}
}

func TestPastIndex_RebuildOrdersByUpdatedCreatedTitleAndID(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	updated := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	writeMeta(t, proj, agent.SessionMeta{
		ID:           "02OLD",
		CreatedAt:    updated.Add(-2 * time.Hour),
		UpdatedAt:    updated,
		OriginalTask: "beta task",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:           "01NEW",
		CreatedAt:    updated.Add(-time.Hour),
		UpdatedAt:    updated,
		OriginalTask: "alpha task",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:           "03TITLEB",
		CreatedAt:    updated.Add(-3 * time.Hour),
		UpdatedAt:    updated.Add(-time.Hour),
		OriginalTask: "bravo task",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:           "04TITLEA",
		CreatedAt:    updated.Add(-3 * time.Hour),
		UpdatedAt:    updated.Add(-time.Hour),
		OriginalTask: "alpha task",
	})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	got := idx.Search("task", 10, 0)
	gotIDs := make([]string, 0, len(got))
	for _, entry := range got {
		gotIDs = append(gotIDs, entry.ID)
	}
	want := []string{"01NEW", "02OLD", "04TITLEA", "03TITLEB"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", gotIDs, want)
	}
}

func TestPastIndex_Search(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:           "01A",
		EnvInfo:      agent.EnvironmentInfo{WorkingDir: "/work/a"},
		UpdatedAt:    time.Now(),
		OriginalTask: "fix the bug in handler",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:           "01B",
		EnvInfo:      agent.EnvironmentInfo{WorkingDir: "/work/b"},
		UpdatedAt:    time.Now(),
		OriginalTask: "refactor auth flow",
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("auth", 50, 0)
	if len(got) != 1 || got[0].ID != "01B" {
		t.Fatalf("Search auth: got %v", got)
	}
	got = idx.Search("/work/a", 50, 0)
	if len(got) != 1 || got[0].ID != "01A" {
		t.Fatalf("Search /work/a: got %v", got)
	}
	got = idx.Search("01B", 50, 0)
	if len(got) != 1 || got[0].ID != "01B" {
		t.Fatalf("Search 01B: got %v", got)
	}
	got = idx.Search("xyz", 50, 0)
	if len(got) != 0 {
		t.Fatalf("Search xyz: got %v", got)
	}
}

func TestPastIndex_Pagination(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	for i := 0; i < 5; i++ {
		writeMeta(t, proj, agent.SessionMeta{
			ID:        string(rune('A' + i)),
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	page1 := idx.Search("", 2, 0)
	page2 := idx.Search("", 2, 2)
	page3 := idx.Search("", 2, 4)
	if len(page1) != 2 || len(page2) != 2 || len(page3) != 1 {
		t.Fatalf("pagination: %d/%d/%d", len(page1), len(page2), len(page3))
	}
}

func TestPastIndex_Find(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:        "01A",
		UpdatedAt: time.Now(),
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got, ok := idx.Find("01A")
	if !ok {
		t.Fatal("expected found")
	}
	if got.Meta.ID != "01A" {
		t.Errorf("meta.ID: %q", got.Meta.ID)
	}
	if got.StateDir != proj {
		t.Errorf("StateDir: %q want %q", got.StateDir, proj)
	}
}

func TestPastIndex_FindRefreshesNewSessionOnMiss(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Find("01NEW"); ok {
		t.Fatal("session should not be indexed before meta exists")
	}

	writeMeta(t, proj, agent.SessionMeta{
		ID:           "01NEW",
		UpdatedAt:    time.Now(),
		OriginalTask: "created after hub start",
	})
	got, ok := idx.Find("01NEW")
	if !ok {
		t.Fatal("expected Find to refresh a newly persisted session")
	}
	if got.StateDir != proj {
		t.Errorf("StateDir: %q want %q", got.StateDir, proj)
	}
}
