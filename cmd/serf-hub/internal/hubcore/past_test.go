package hubcore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

func writeMeta(t *testing.T, dir string, meta schema.SessionMeta) {
	t.Helper()
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
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
	writeMeta(t, projA, schema.SessionMeta{
		ID:             "01A",
		Model:          "gpt-5.2",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/a"},
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-1 * time.Hour),
		OriginalPrompt: "fix the bug",
	})
	writeMeta(t, projB, schema.SessionMeta{
		ID:             "01B",
		Model:          "claude-opus-4-7",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/b"},
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
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02OLD",
		CreatedAt:      updated.Add(-2 * time.Hour),
		UpdatedAt:      updated,
		OriginalPrompt: "beta task",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01NEW",
		CreatedAt:      updated.Add(-time.Hour),
		UpdatedAt:      updated,
		OriginalPrompt: "alpha task",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "03TITLEB",
		CreatedAt:      updated.Add(-3 * time.Hour),
		UpdatedAt:      updated.Add(-time.Hour),
		OriginalPrompt: "bravo task",
	})
	writeMeta(t, proj, schema.SessionMeta{
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
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01A",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/a"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix the bug in handler",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01B",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/b"},
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
	writeMeta(t, proj, schema.SessionMeta{
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
	writeMeta(t, proj, schema.SessionMeta{
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
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01AUTH",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/auth-service"},
		UpdatedAt:      time.Now().Add(time.Hour),
		OriginalPrompt: "repair login token refresh",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01BILLING",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/invoices"},
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
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01PREFIX",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/prefix"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "auth token cleanup",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02SUBSTR",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/substr"},
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
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01FTS",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/fts"},
		UpdatedAt:      time.Now(),
		OriginalPrompt: "auth token cleanup",
	})
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02SUBSTR",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/substr"},
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
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01PRIVATE",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/private"},
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
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01A",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/work/a"},
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
		writeMeta(t, proj, schema.SessionMeta{
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
	writeMeta(t, proj, schema.SessionMeta{
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

	writeMeta(t, proj, schema.SessionMeta{
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

func TestPastIndex_FindWithMalformedGlob(t *testing.T) {
	idx := NewPastIndex("[unclosed")
	// Rebuild must propagate the glob compile error rather than swallow it: a
	// silently-ignored ErrBadPattern leaves the index empty so Find still
	// reports false, masking the lost error.
	if err := idx.Rebuild(); !errors.Is(err, filepath.ErrBadPattern) {
		t.Fatalf("expected Rebuild to propagate ErrBadPattern, got %v", err)
	}
	got, ok := idx.Find("anything")
	if ok {
		t.Fatal("expected Find to return false on malformed glob")
	}
	if got.ID != "" || got.StateDir != "" {
		t.Errorf("expected zero PastEntry on miss, got %+v", got)
	}
}

func TestPastIndex_RebuildFTSError(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01A",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix auth",
	})
	// Use a directory as the DB path so SQLite open fails.
	dbPath := filepath.Join(root, "index.db")
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// The Rebuild should succeed even though rebuildFTS failed.
	// FTS flag should be false.
	got := idx.Search("auth", 50, 0)
	if len(got) != 1 || got[0].ID != "01A" {
		t.Fatalf("expected fallback search to work: got %v", got)
	}
}

func TestPastIndex_AllMetas(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "01A",
		UpdatedAt: time.Now(),
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	metas := idx.AllMetas()
	if len(metas) != 1 || metas[0].ID != "01A" {
		t.Fatalf("AllMetas: got %v", metas)
	}
}

func TestPastIndex_SearchFTSSpecialCharsOnly(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "01A",
		UpdatedAt:      time.Now(),
		OriginalPrompt: "fix auth",
	})
	idx := NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, ".serf", "index.db"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// A special-char-only query yields no FTS tokens, so ftsQuery must return ""
	// to skip the FTS path entirely rather than emit a malformed MATCH string.
	// Observing this directly proves the fallback decision; the zero-results
	// check alone holds regardless of which path ran.
	if q := ftsQuery("!@#$%"); q != "" {
		t.Fatalf("ftsQuery(special-only): got %q want empty", q)
	}
	// Search then falls back to the in-memory substring scan, which finds no
	// match for a query with no alphanumeric content.
	got := idx.Search("!@#$%", 50, 0)
	if len(got) != 0 {
		t.Fatalf("expected no results for special-char-only query: got %v", got)
	}
}

func TestChmodSQLiteIndexFiles_Error(t *testing.T) {
	dir := t.TempDir()
	// Place the db path under a parent component that is a regular file, not a
	// directory. chmod on any path beneath it returns ENOTDIR regardless of uid,
	// so this exercises the error path even when the test runs as root.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(blocker, "db")
	err := chmodSQLiteIndexFiles(dbPath)
	if err == nil {
		t.Fatal("expected error when a parent path component is a regular file")
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("expected ENOTDIR, got %v", err)
	}
}

func TestPastIndex_FindEmptyStateGlob(t *testing.T) {
	idx := NewPastIndex("")
	got, ok := idx.Find("something")
	if ok {
		t.Fatal("expected Find to return false when stateGlob is empty")
	}
	if got.ID != "" || got.StateDir != "" {
		t.Errorf("expected zero PastEntry on miss, got %+v", got)
	}
}

// TestPastIndex_FindEmptySessionIDSkipsRebuild pins the other half of Find's
// short-circuit guard: an empty session id must return false WITHOUT triggering
// a rebuild, even when the glob points at real on-disk sessions. Dropping the
// guard would route the empty id through Rebuild, which populates the index as a
// side effect — observable here as a non-empty All() snapshot.
func TestPastIndex_FindEmptySessionIDSkipsRebuild(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "x")
	_ = os.MkdirAll(proj, 0o755)
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "01A",
		UpdatedAt: time.Now(),
	})
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	got, ok := idx.Find("")
	if ok {
		t.Fatal("expected Find to return false for an empty session id")
	}
	if got.ID != "" || got.StateDir != "" {
		t.Errorf("expected zero PastEntry on miss, got %+v", got)
	}
	if all := idx.All(); len(all) != 0 {
		t.Fatalf("Find(\"\") must not trigger a rebuild, but index holds %d entries", len(all))
	}
}

func TestPastIndexOnChangeFiresOnContentDeltaOnly(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	writeMeta := func(id string) {
		m := schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
		if err := schema.SaveSessionMeta(proj, m); err != nil {
			t.Fatal(err)
		}
	}
	writeMeta("01A")
	idx := NewPastIndex(filepath.Join(dir, "*"))
	fired := 0
	idx.SetOnChange(func() { fired++ })
	_ = idx.Rebuild() // first content load: delta vs empty → fires
	if fired != 1 {
		t.Fatalf("first rebuild with content should fire once, got %d", fired)
	}
	_ = idx.Rebuild() // identical content → no delta → no fire
	if fired != 1 {
		t.Fatalf("re-rebuild with no delta must not fire, got %d", fired)
	}
	writeMeta("01B")
	_ = idx.Rebuild() // new meta → delta → fires
	if fired != 2 {
		t.Fatalf("rebuild after new content should fire, got %d", fired)
	}
}

func TestUpdateMetaReordersAndPreservesStateDir(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	for _, id := range []string{"01A", "01B"} {
		m := schema.SessionMeta{ID: id, Name: id, UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
		if err := schema.SaveSessionMeta(proj, m); err != nil {
			t.Fatal(err)
		}
	}
	idx := NewPastIndexWithDB(filepath.Join(dir, "*"), filepath.Join(dir, "index.db"))
	_ = idx.Rebuild()
	before, _ := idx.Find("01A")
	renamed := before.Meta
	renamed.Name = "renamed-title"
	renamed.UpdatedAt = time.Unix(1_700_100_000, 0) // newer → sorts first
	idx.UpdateMeta("01A", renamed)

	got, ok := idx.Find("01A")
	if !ok || got.Meta.Name != "renamed-title" {
		t.Fatalf("UpdateMeta did not update the entry: %+v", got)
	}
	if got.StateDir != before.StateDir {
		t.Fatalf("StateDir must be preserved: %q != %q", got.StateDir, before.StateDir)
	}
	if all := idx.All(); all[0].ID != "01A" {
		t.Fatalf("renamed newer entry should sort first, got %q", all[0].ID)
	}
	// FTS search must find the new title.
	if hits := idx.Search("renamed-title", 10, 0); len(hits) == 0 || hits[0].ID != "01A" {
		t.Fatalf("search must reflect the new title, got %+v", hits)
	}
}

// TestUpdateMetaConcurrentWithRebuildIsRaceFree pins the fix for a latent
// data race: Rebuild publishes i.all and then, AFTER releasing the lock,
// keeps reading that same slice's backing array for its slow
// rebuildFTS/contentFingerprint work. UpdateMeta used to mutate i.all's
// backing array in place (re-slicing append for removal, append+copy for
// insert), so a concurrent UpdateMeta could write into the very array an
// in-flight, unlocked Rebuild was still reading. This test has no
// assertions of its own — under `go test -race` the race detector is the
// failure mode.
func TestUpdateMetaConcurrentWithRebuildIsRaceFree(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	for _, id := range []string{"01A", "01B"} {
		m := schema.SessionMeta{ID: id, Name: id, UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
		if err := schema.SaveSessionMeta(proj, m); err != nil {
			t.Fatal(err)
		}
	}
	idx := NewPastIndexWithDB(filepath.Join(dir, "*"), filepath.Join(dir, "index.db"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}

	const iterations = 300
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for n := 0; n < iterations; n++ {
			_ = idx.Rebuild()
		}
	}()
	go func() {
		defer wg.Done()
		for n := 0; n < iterations; n++ {
			meta := schema.SessionMeta{
				ID:        "01A",
				Name:      "renamed-title",
				UpdatedAt: time.Unix(1_700_000_000+int64(n), 0),
				EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/w"},
			}
			idx.UpdateMeta("01A", meta)
		}
	}()
	wg.Wait()
}

func TestPastIndex_RecentModels_DedupesGlobalRecencyLastN(t *testing.T) {
	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "anthropic", Model: "claude-opus-4-6", UpdatedAt: now.Add(-1 * time.Minute)},
		{ID: "b", ProfileID: "openai", Model: "gpt-5.2", UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "c", ProfileID: "anthropic", Model: "claude-opus-4-6", UpdatedAt: now.Add(-3 * time.Minute)}, // dup of a, older — dropped
		{ID: "d", ProfileID: "openai", Model: "gpt-5-mini", UpdatedAt: now.Add(-4 * time.Minute)},
		{ID: "e", ProfileID: "google", Model: "gemini-3-pro", UpdatedAt: now.Add(-5 * time.Minute)},
		{ID: "f", ProfileID: "zai", Model: "glm-5.2", UpdatedAt: now.Add(-6 * time.Minute)},
		{ID: "g", ProfileID: "mistral", Model: "mistral-large", UpdatedAt: now.Add(-7 * time.Minute)}, // 6th distinct — excluded by limit=5
	})
	got := idx.RecentModels(5)
	want := []appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
		{Provider: "openai", Model: "gpt-5.2"},
		{Provider: "openai", Model: "gpt-5-mini"},
		{Provider: "google", Model: "gemini-3-pro"},
		{Provider: "zai", Model: "glm-5.2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentModels(5) = %+v, want %+v", got, want)
	}
}

func TestPastIndex_RecentModels_EmptyIndexReturnsNil(t *testing.T) {
	idx := NewPastIndex("")
	if got := idx.RecentModels(5); got != nil {
		t.Fatalf("RecentModels on empty index = %+v, want nil", got)
	}
}

func TestPastIndex_RecentModels_SkipsBlankProviderOrModel(t *testing.T) {
	idx := NewPastIndex("")
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "", Model: "gpt-5.2", UpdatedAt: time.Now()},
		{ID: "b", ProfileID: "openai", Model: "", UpdatedAt: time.Now()},
		{ID: "c", ProfileID: "openai", Model: "gpt-5.2", UpdatedAt: time.Now()},
	})
	got := idx.RecentModels(5)
	want := []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentModels = %+v, want %+v (blank provider/model entries skipped)", got, want)
	}
}
