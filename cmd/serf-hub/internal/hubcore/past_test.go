package hubcore

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
		ID:             "01A",
		Model:          "gpt-5.2",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/a"},
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-1 * time.Hour),
		OriginalPrompt: "fix the bug",
	})
	writeMeta(t, projB, agent.SessionMeta{
		ID:             "01B",
		Model:          "claude-opus-4-7",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/b"},
		CreatedAt:      now.Add(-30 * time.Minute),
		UpdatedAt:      now,
		OriginalPrompt: "refactor auth",
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
		ID:             "02OLD",
		CreatedAt:      updated.Add(-2 * time.Hour),
		UpdatedAt:      updated,
		OriginalPrompt: "beta task",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01NEW",
		CreatedAt:      updated.Add(-time.Hour),
		UpdatedAt:      updated,
		OriginalPrompt: "alpha task",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "03TITLEB",
		CreatedAt:      updated.Add(-3 * time.Hour),
		UpdatedAt:      updated.Add(-time.Hour),
		OriginalPrompt: "bravo task",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "04TITLEA",
		CreatedAt:      updated.Add(-3 * time.Hour),
		UpdatedAt:      updated.Add(-time.Hour),
		OriginalPrompt: "alpha task",
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
		ID:             "01A",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/a"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix the bug in handler",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01B",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/b"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "refactor auth flow",
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

func TestPastIndex_SearchMatchesGeneratedName(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01NAMED",
		Name:           "Launch Config Cheap Model",
		OriginalPrompt: "unrelated original prompt",
		UpdatedAt:      time.Now(),
	})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("cheap model", 50, 0)
	if len(got) != 1 || got[0].ID != "01NAMED" {
		t.Fatalf("Search cheap model: got %v", got)
	}
}

func TestPastIndex_SearchSQLiteFTSMatchesGeneratedName(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01NAMED",
		Name:           "Launch Config Cheap Model",
		OriginalPrompt: "unrelated original prompt",
		UpdatedAt:      time.Now(),
	})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, ".serf", "index.db"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("cheap model", 50, 0)
	if len(got) != 1 || got[0].ID != "01NAMED" {
		t.Fatalf("Search cheap model: got %v", got)
	}
}

func TestPastIndex_SearchUsesSQLiteFTSWhenConfigured(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01AUTH",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/auth-service"},
		UpdatedAt:      time.Now().Add(time.Hour),
		OriginalPrompt: "repair login token refresh",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01BILLING",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/invoices"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "invoice cleanup",
	})

	dbPath := filepath.Join(root, ".serf", "index.db")
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected sqlite index at %s: %v", dbPath, err)
	}

	got := idx.Search("token", 50, 0)
	if len(got) != 1 || got[0].ID != "01AUTH" {
		t.Fatalf("Search token: got %v", got)
	}
	got = idx.Search("auth-service", 50, 0)
	if len(got) != 1 || got[0].ID != "01AUTH" {
		t.Fatalf("Search working dir: got %v", got)
	}
	got = idx.Search("01BILL", 50, 0)
	if len(got) != 1 || got[0].ID != "01BILLING" {
		t.Fatalf("Search ID prefix: got %v", got)
	}
	got = idx.Search("BILL", 50, 0)
	if len(got) != 1 || got[0].ID != "01BILLING" {
		t.Fatalf("Search ID substring: got %v", got)
	}
}

func TestPastIndex_SearchWithSQLitePreservesSubstringMatches(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01PREFIX",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/prefix"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "auth token cleanup",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "02SUBSTR",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/substr"},
		UpdatedAt:      time.Now().Add(-time.Minute),
		OriginalPrompt: "preauth redirect cleanup",
	})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, ".serf", "index.db"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("auth", 50, 0)
	gotIDs := make([]string, 0, len(got))
	for _, entry := range got {
		gotIDs = append(gotIDs, entry.ID)
	}
	want := []string{"01PREFIX", "02SUBSTR"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("Search auth IDs=%v, want %v", gotIDs, want)
	}
}

func TestPastIndex_SearchWithSQLiteMergesFTSAndSubstringMatches(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01FTS",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/fts"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "auth token cleanup",
	})
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "02SUBSTR",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/substr"},
		UpdatedAt:      time.Now().Add(-time.Minute),
		OriginalPrompt: "preauth cleanup",
	})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, ".serf", "index.db"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("auth cleanup", 50, 0)
	gotIDs := make([]string, 0, len(got))
	for _, entry := range got {
		gotIDs = append(gotIDs, entry.ID)
	}
	want := []string{"01FTS", "02SUBSTR"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("Search auth cleanup IDs=%v, want %v", gotIDs, want)
	}
}

func TestPastIndex_SQLiteIndexUsesPrivateFilePermissions(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01PRIVATE",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/private"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "sensitive prompt",
	})
	indexDir := filepath.Join(root, ".serf")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(indexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(indexDir, "index.db")
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("index dir mode=%#o, want existing 0755", got)
	}
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("index db mode=%#o, want 0600", got)
	}
}

func TestPastIndex_SearchFallsBackWhenSQLiteUnavailable(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, agent.SessionMeta{
		ID:             "01A",
		EnvInfo:        agent.EnvironmentInfo{WorkingDir: "/work/a"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix auth",
	})

	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), root)
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	got := idx.Search("auth", 50, 0)
	if len(got) != 1 || got[0].ID != "01A" {
		t.Fatalf("fallback search: got %v", got)
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
		ID:             "01NEW",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "created after hub start",
	})
	got, ok := idx.Find("01NEW")
	if !ok {
		t.Fatal("expected Find to refresh a newly persisted session")
	}
	if got.StateDir != proj {
		t.Errorf("StateDir: %q want %q", got.StateDir, proj)
	}
}
