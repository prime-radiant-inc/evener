# Sidebar IA v2 (session tiering + archiving) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the project-tier sidebar with projects ordered by recency whose sessions are tiered Current/Recent/Archived, add manual + auto-aged (2-week) archiving for sessions and projects, and remove the `serf-e2e-*` prefix bucket.

**Architecture:** A new sqlite-backed `ArchiveStore` (in the existing `~/.serf/index.db`) records explicit user archive/unarchive decisions. `hubcore.BuildTree` takes those decisions plus a `now` and computes each session's tier (effective-archived = user decision if present, else last-activity > 2 weeks) and each project's placement (active list vs. archived group), ordering active projects by most-recent session start. The template, sidebar JS, and CSS render the new shape; a `POST /api/archive` endpoint persists decisions.

**Tech Stack:** Go (`database/sql` + `modernc.org/sqlite`), Go `html/template`, vanilla JS (no bundler), CSS. Tests: Go `testing`, the hub's `jstest` harness (`cmd/serf-hub/jstest/run-all.sh`, run from inside that dir).

## Global Constraints

- Design source of truth: `docs/superpowers/specs/2026-06-17-sidebar-ia-archiving-design.md`. Copy its rules exactly.
- **Current** = last activity ≤ 24h; **Recent** = > 24h and ≤ 2 weeks; **Archived** = effective-archived. Auto-archive threshold = **14 days** of inactivity (`UpdatedAt` is "last activity"). Most-recent session **start** = `max(CreatedAt)` across a project's sessions.
- Effective-archived rule: `userDecision if present, else (now - lastActivity) > 14d`. Archive sets the decision true; unarchive sets it false (sticky — no re-auto-archive this pass).
- Archive is **hub-side UI state only** — never write into a session's own state dir. Store in `index.db`.
- Preserve the existing `Needs you` tier, `clusterRepeatedTitles`, and subagent/fork nesting. Remove `TierActive/Recent/Older/Test`, `tierFor`, `tierRank`, `isTestProject`, `e2eProjectPrefix`, `TierGroups`, `DateGroup`/`DateGroupsAt`.
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit. Match surrounding code style. Pristine test output.
- Renderer JS split is unrelated to these files, but do not regress it; `make lint` must stay `0 issues.` ×4 and the full `jstest` suite green at every commit.
- Out of scope: pinning; DB-side pagination / lazy session loading; a real disposable/test flag.

---

### Task 1: Archive store (sqlite-backed)

**Files:**
- Create: `cmd/serf-hub/internal/hubcore/archive.go`
- Test: `cmd/serf-hub/internal/hubcore/archive_test.go`

**Interfaces:**
- Produces:
  - `type ArchiveKey struct { Kind, ID string }` (Kind ∈ `"session"`,`"project"`)
  - `func NewArchiveStore(dbPath string) *ArchiveStore`
  - `func (s *ArchiveStore) Decisions() (map[ArchiveKey]bool, error)` — explicit decisions only; empty (not nil-error) when `dbPath==""` or table absent.
  - `func (s *ArchiveStore) Set(kind, id string, archived bool, now time.Time) error` — upsert.

- [ ] **Step 1: Write the failing test**

```go
package hubcore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveStoreSetAndRead(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s := NewArchiveStore(db)
	now := time.Unix(1_700_000_000, 0)

	if err := s.Set("session", "sess-1", true, now); err != nil {
		t.Fatalf("set archive: %v", err)
	}
	if err := s.Set("project", "proj-a", true, now); err != nil {
		t.Fatalf("set project: %v", err)
	}
	// unarchive flips it back
	if err := s.Set("session", "sess-1", false, now); err != nil {
		t.Fatalf("unset: %v", err)
	}

	got, err := s.Decisions()
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	if v, ok := got[ArchiveKey{"session", "sess-1"}]; !ok || v != false {
		t.Fatalf("session decision = %v,%v; want false,true", v, ok)
	}
	if v, ok := got[ArchiveKey{"project", "proj-a"}]; !ok || v != true {
		t.Fatalf("project decision = %v,%v; want true,true", v, ok)
	}
}

func TestArchiveStoreEmptyWhenNoDB(t *testing.T) {
	s := NewArchiveStore("")
	got, err := s.Decisions()
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestArchiveStore -v`
Expected: FAIL — `undefined: NewArchiveStore` / `ArchiveKey`.

- [ ] **Step 3: Write minimal implementation**

```go
package hubcore

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
)

// ArchiveKey identifies an archivable entity: a session by ID or a project by name.
type ArchiveKey struct {
	Kind string // "session" | "project"
	ID   string
}

// ArchiveStore persists explicit user archive/unarchive decisions in index.db.
// Auto-archive (by inactivity age) is computed at tree-build time and is NOT
// stored here — only deliberate user decisions are recorded.
type ArchiveStore struct {
	dbPath string
}

// NewArchiveStore returns a store backed by the SQLite file at dbPath. An empty
// dbPath yields a store whose Decisions() is always empty (graceful no-op).
func NewArchiveStore(dbPath string) *ArchiveStore { return &ArchiveStore{dbPath: dbPath} }

const createArchiveTable = `
CREATE TABLE IF NOT EXISTS archive (
  kind       TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  archived   INTEGER NOT NULL,
  decided_at INTEGER NOT NULL,
  PRIMARY KEY (kind, id)
)`

func (s *ArchiveStore) open() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(createArchiveTable); err != nil { //nolint:noctx // local file DB
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Set upserts a user decision: archived=true to archive, false to unarchive.
func (s *ArchiveStore) Set(kind, id string, archived bool, now time.Time) error {
	if s.dbPath == "" {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	flag := 0
	if archived {
		flag = 1
	}
	_, err = db.Exec( //nolint:noctx // local file DB
		`INSERT INTO archive (kind, id, archived, decided_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(kind, id) DO UPDATE SET archived=excluded.archived, decided_at=excluded.decided_at`,
		kind, id, flag, now.Unix())
	return err
}

// Decisions returns every explicit decision. Empty when no DB / no table.
func (s *ArchiveStore) Decisions() (map[ArchiveKey]bool, error) {
	out := make(map[ArchiveKey]bool)
	if s.dbPath == "" {
		return out, nil
	}
	if _, err := os.Stat(s.dbPath); os.IsNotExist(err) {
		return out, nil
	}
	db, err := s.open()
	if err != nil {
		return out, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT kind, id, archived FROM archive`) //nolint:noctx // local file DB
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, id string
		var flag int
		if err := rows.Scan(&k, &id, &flag); err != nil {
			return out, err
		}
		out[ArchiveKey{Kind: k, ID: id}] = flag == 1
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestArchiveStore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/archive.go cmd/serf-hub/internal/hubcore/archive_test.go
git commit -m "Add hub-side archive store (sqlite) for session/project decisions"
```

---

### Task 2: Tier-classification helpers (pure functions)

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go` (add helpers + windows; do not wire into BuildTree yet)
- Test: `cmd/serf-hub/internal/hubcore/tree_test.go` (add cases; file already exists)

**Interfaces:**
- Consumes: `ArchiveKey` (Task 1).
- Produces:
  - `const currentWindow = 24 * time.Hour`, `const archiveWindow = 14 * 24 * time.Hour`
  - `func classifySession(decision *bool, lastActivity, now time.Time) string` → `"current"|"recent"|"archived"`

- [ ] **Step 1: Write the failing test**

```go
func TestClassifySession(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	yes, no := true, false
	cases := []struct {
		name     string
		decision *bool
		age      time.Duration
		want     string
	}{
		{"fresh -> current", nil, 1 * time.Hour, "current"},
		{"yesterday -> recent", nil, 36 * time.Hour, "recent"},
		{"3 weeks -> archived (auto)", nil, 21 * 24 * time.Hour, "archived"},
		{"manual archive overrides fresh", &yes, 1 * time.Hour, "archived"},
		{"manual unarchive overrides old", &no, 30 * 24 * time.Hour, "recent"},
		{"boundary 24h -> current", nil, 24 * time.Hour, "current"},
		{"boundary 14d -> recent", nil, 14 * 24 * time.Hour, "recent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifySession(c.decision, now.Add(-c.age), now)
			if got != c.want {
				t.Fatalf("classifySession=%q want %q", got, c.want)
			}
		})
	}
}
```

Note the boundary semantics this pins: `≤ 24h` is current, `≤ 14d` is recent (so exactly 14d is recent, `> 14d` auto-archives); a manual-unarchive of a 30d item lands in `recent` (it is not current, but it is visible).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestClassifySession -v`
Expected: FAIL — `undefined: classifySession`.

- [ ] **Step 3: Write minimal implementation**

Add to `tree.go` (near the old `recentWindow`; you will delete the old tier constants/functions in Task 3):

```go
const (
	currentWindow = 24 * time.Hour
	archiveWindow = 14 * 24 * time.Hour
)

// classifySession returns a session's sidebar tier from its last activity and
// archive decision. A user decision (archive/unarchive) overrides the auto rule;
// otherwise inactivity older than archiveWindow auto-archives.
func classifySession(decision *bool, lastActivity, now time.Time) string {
	archived := now.Sub(lastActivity) > archiveWindow
	if decision != nil {
		archived = *decision
	}
	if archived {
		return "archived"
	}
	if now.Sub(lastActivity) <= currentWindow {
		return "current"
	}
	return "recent"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestClassifySession -v`
Expected: PASS. Also run `go build ./cmd/serf-hub/...` — expected clean (new functions may be unused until Task 3; if `make lint` flags unused, proceed straight to Task 3 in the same review cycle, but the build must pass).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go
git commit -m "Add session-tier classification helper (current/recent/archived)"
```

---

### Task 3: Rewrite BuildTree to the new model (Go + template, one vertical slice)

This is the core change. It must stay green: the Go data model, the `sidebar.html` template that consumes it, and the render test all change together.

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go` (Tree/TreeProject fields; rewrite BuildTree; delete old tier machinery)
- Modify: `cmd/serf-hub/internal/hubcore/tree_test.go`
- Modify: `cmd/serf-hub/templates/partials/sidebar.html`
- Modify: `cmd/serf-hub/web_api_tree.go:25` (call site) — pass an empty decisions map + now for now (real wiring is Task 4)
- Modify: any other `BuildTree(` caller (grep first: `grep -rn "BuildTree(" cmd/serf-hub`)
- Modify: `cmd/serf-hub/web_test.go` if it asserts removed tier labels

**Interfaces:**
- Consumes: `classifySession` (Task 2), `ArchiveKey` (Task 1).
- Produces:
  - `Tree{ NeedsYou []TreeNode; Live []TreeNode; Projects []TreeProject; ArchivedProjects []TreeProject }`
  - `TreeProject{ Name string; Current, Recent, Archived []TreeNode; IsArchived bool; MostRecentStart time.Time; RollupState string; RollupLive, RollupAttn int; ... existing header fields ... }`
  - `func BuildTree(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool) Tree`
  - `func BuildTreeAt(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, now time.Time) Tree`

- [ ] **Step 1: Write the failing test**

Add to `tree_test.go` (adapt the existing `meta(...)`/builder helpers in that file — read them first; this uses synthetic metas with controlled `CreatedAt`/`UpdatedAt`):

```go
func TestBuildTreeSessionTiersAndProjectOrder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, createdAgo, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-createdAgo), UpdatedAt: now.Add(-updatedAgo),
			Config: schema.ConfigSnapshot{ /* set whatever projectName() reads, matching existing helpers */ }}
	}
	metas := []schema.SessionMeta{
		mk("a-cur", "alpha", 2*time.Hour, 1*time.Hour),    // alpha: current
		mk("a-old", "alpha", 40*24*time.Hour, 30*24*time.Hour), // alpha: auto-archived
		mk("b-rec", "beta", 50*time.Hour, 48*time.Hour),   // beta: recent only (most-recent start newer than alpha? no)
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)

	// alpha has a current session and a >14d session -> alpha is an active project
	// with one Current and one Archived session.
	var alpha *TreeProject
	for i := range tree.Projects {
		if tree.Projects[i].Name == "alpha" {
			alpha = &tree.Projects[i]
		}
	}
	if alpha == nil {
		t.Fatalf("alpha should be an active project")
	}
	if len(alpha.Current) != 1 || len(alpha.Archived) != 1 || len(alpha.Recent) != 0 {
		t.Fatalf("alpha tiers: current=%d recent=%d archived=%d", len(alpha.Current), len(alpha.Recent), len(alpha.Archived))
	}
	// Projects ordered by most-recent session START desc: alpha's newest start is 2h ago,
	// beta's is 50h ago -> alpha first.
	if len(tree.Projects) < 2 || tree.Projects[0].Name != "alpha" {
		t.Fatalf("project order wrong: %+v", projectNames(tree.Projects))
	}
}

func TestBuildTreeArchivedProjectPlacement(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			Config: schema.ConfigSnapshot{ /* project=proj per existing helpers */ }}
	}
	// gamma: every session auto-archived -> gamma goes to ArchivedProjects.
	metas := []schema.SessionMeta{mk("g1", "gamma", 30*24*time.Hour)}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if len(tree.Projects) != 0 {
		t.Fatalf("gamma should not be active, got %v", projectNames(tree.Projects))
	}
	if len(tree.ArchivedProjects) != 1 || tree.ArchivedProjects[0].Name != "gamma" {
		t.Fatalf("gamma should be an archived project")
	}

	// manual project archive forces gamma2 to ArchivedProjects even though it's fresh.
	metas2 := []schema.SessionMeta{mk("d1", "delta", 1*time.Hour)}
	tree2 := BuildTreeAt(metas2, nil, map[ArchiveKey]bool{{Kind: "project", ID: "delta"}: true}, now)
	if len(tree2.Projects) != 0 || len(tree2.ArchivedProjects) != 1 {
		t.Fatalf("delta manual-archived should be in ArchivedProjects")
	}
}
```

Add a small helper if not present: `func projectNames(ps []TreeProject) []string { ... }`. Match how the existing `meta()`/project helpers in `tree_test.go` set the project name (read the file — projectName() derives it from `ConfigSnapshot`/`EnvInfo`; reuse the existing test constructor instead of hand-building if one exists).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestBuildTree -v`
Expected: FAIL — new fields/`BuildTreeAt` undefined, or wrong shape.

- [ ] **Step 3: Write minimal implementation (Go)**

In `tree.go`:
1. Change `Tree` and `TreeProject` to the Interfaces shape above. Keep `RollupState/RollupLive/RollupAttn` and the existing header-derivation logic. Replace the flat `Sessions []TreeNode` usage: after you build a project's `[]TreeNode` (the same list `clusterRepeatedTitles` produces today), split it into `Current/Recent/Archived` by `classifySession(decisionFor(node), node.UpdatedAt, now)`, where `decisionFor` looks up `decisions[ArchiveKey{"session", node.ID}]` (returning `*bool`).
2. Compute `MostRecentStart = max(CreatedAt)` over the project's top-level sessions.
3. Project placement: `IsArchived = decisions[{"project",name}]==true || (len(Current)==0 && len(Recent)==0)`. Active projects (`!IsArchived`) go to `Tree.Projects` sorted by `MostRecentStart` desc; the rest to `Tree.ArchivedProjects` (sort by `MostRecentStart` desc too).
4. `BuildTree(metas, live, decisions)` calls `BuildTreeAt(metas, live, decisions, time.Now())`. Move any internal `time.Now()` to the `now` parameter.
5. Delete `TierActive/Recent/Older/Test`, `recentWindow`, `tierFor`, `tierRank`, `tierLabel`, `TierGroup`, `TierGroups`, `DateGroup`, `DateGroupsAt`, `DateGroups`, `e2eProjectPrefix`, `isTestProject`, and the `Tier/MostRecent/Age`-by-tier fields no longer used. Keep `NeedsYou`, `clusterRepeatedTitles`, subagent/fork nesting, `nodeKind/nodeTitle/projectName`, `AgeString`.

- [ ] **Step 4: Rewrite the template**

Rewrite `cmd/serf-hub/templates/partials/sidebar.html` to the new shape (match the file's existing markup conventions/classes — read it first; reuse `sb-row`, `status-dot`, the `sidebarProject` sub-template, etc.):

```gotemplate
<nav class="sidebar" aria-label="Sessions">
  {{if .NeedsYou}}
  <section class="sidebar-tier needs-you-tier" data-tier="needs-you">
    <header class="sidebar-section-header tier-header needs-you-header"><span>Needs you</span><span class="count">{{len .NeedsYou}}</span></header>
    {{range .NeedsYou}}{{/* keep the existing needs-you row markup */}}{{end}}
  </section>
  {{end}}

  {{range .Projects}}
    {{template "sidebarProject" .}}
  {{end}}

  {{if .ArchivedProjects}}
  <section class="sidebar-tier archived-projects" data-tier="archived-projects">
    <details>
      <summary class="sidebar-section-header tier-header">Archived projects <span class="count">{{len .ArchivedProjects}}</span></summary>
      {{range .ArchivedProjects}}{{template "sidebarProject" .}}{{end}}
    </details>
  </section>
  {{end}}
</nav>

{{define "sidebarProject"}}
<section class="sidebar-project" data-project="{{.Name}}">
  <header class="project-header">
    <span class="project-name">{{.Name}}</span>
    {{/* keep the existing rollup status dots (RollupState/RollupLive/RollupAttn) */}}
    <button type="button" class="archive-btn" data-archive-kind="project" data-archive-id="{{.Name}}"
      aria-label="Archive project">⋯</button>
  </header>
  {{if .Current}}<div class="session-tier" data-tier="current"><div class="session-tier-label">Current</div>
    {{range .Current}}{{template "sidebarSession" .}}{{end}}</div>{{end}}
  {{if .Recent}}<div class="session-tier" data-tier="recent"><div class="session-tier-label">Recent</div>
    {{range .Recent}}{{template "sidebarSession" .}}{{end}}</div>{{end}}
  {{if .Archived}}<details class="session-tier archived" data-tier="archived">
    <summary class="session-tier-label">Archived <span class="count">{{len .Archived}}</span></summary>
    {{range .Archived}}{{template "sidebarSession" .}}{{end}}</details>{{end}}
</section>
{{end}}

{{define "sidebarSession"}}
  {{/* Reuse the existing session-row markup (sb-row, status-dot, cluster handling,
       subagent/fork children). Add an archive control on the row:
       <button class="archive-btn" data-archive-kind="session" data-archive-id="{{.ID}}" ...>⋯</button>
       Preserve cluster-header / cluster-member / subagent-row rendering from the old template. */}}
{{end}}
```

Carry over verbatim, from the old template, the needs-you row markup and the full session-row markup (cluster header/members, subagent/fork children) into the two `define` blocks — do not lose that behavior.

- [ ] **Step 5: Fix call sites + run Go tests**

Update `web_api_tree.go:25` to `hubcore.BuildTree(metas, live, map[hubcore.ArchiveKey]bool{})` (real decisions arrive in Task 4). Update any other `BuildTree(` caller and any `web_test.go` assertion referencing removed tier labels (`Active`/`Recent`/`Older`/`Test runs`).

Run: `go test ./cmd/serf-hub/... ./cmd/serf-hub/internal/hubcore/ -v 2>&1 | tail -20`
Expected: PASS, including the new BuildTree tests and the existing render tests.

- [ ] **Step 6: Build + lint + jstest**

Run: `make build-hub && make lint && (cd cmd/serf-hub/jstest && bash run-all.sh)`
Expected: build OK; `0 issues.` ×4; `jstest: all tests passed`.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go \
        cmd/serf-hub/templates/partials/sidebar.html cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_test.go
git commit -m "Sidebar IA v2: session tiers + project recency order + archived placement"
```

---

### Task 4: Wire the archive store into tree building

**Files:**
- Modify: `cmd/serf-hub/config.go` (WebConfig gains an archive store) and the hub startup that constructs `WebConfig` (grep `WebConfig{` and `NewPastIndexWithDB` to find where index.db path is known — reuse `cfg.PastIndexDB`/`DefaultPastIndexDBPath()`).
- Modify: `cmd/serf-hub/web_api_tree.go` (`navigationTreeInputs` reads decisions; `BuildTree` call passes them)
- Test: `cmd/serf-hub/internal/hubcore/` already covers the store; add/extend a hub test only if a natural seam exists.

**Interfaces:**
- Consumes: `NewArchiveStore` (Task 1), `BuildTree` with decisions (Task 3).
- Produces: `WebConfig.Archive *hubcore.ArchiveStore`; `navigationTreeInputs` unchanged signature but now also returns/*uses* decisions (simplest: add `archiveDecisions(ctx)` helper returning `map[hubcore.ArchiveKey]bool` from `s.cfg.Archive`, empty on nil/error).

- [ ] **Step 1: Write the failing test**

Add to `cmd/serf-hub/web_api_tree_test.go` (create if absent) a test that a `WebServer` whose `cfg.Archive` has a project archived produces a tree with that project in `ArchivedProjects`. If the existing hub test harness (`newHubRPCTestServer`, see `app_auth_test.go`) makes this awkward, instead assert at the helper level: `archiveDecisions` returns the stored map.

```go
func TestArchiveDecisionsFlowIntoTree(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	if err := store.Set("project", "alpha", true, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	got, err := store.Decisions()
	if err != nil || !got[hubcore.ArchiveKey{Kind: "project", ID: "alpha"}] {
		t.Fatalf("decision not stored: %v %v", got, err)
	}
}
```

- [ ] **Step 2: Run it — expect FAIL** (compile error until you add the field) → Run: `go test ./cmd/serf-hub/ -run TestArchiveDecisionsFlowIntoTree -v`.

- [ ] **Step 3: Implement** — add `Archive *hubcore.ArchiveStore` to `WebConfig`; construct it at hub startup with the index.db path; add `func (s *WebServer) archiveDecisions() map[hubcore.ArchiveKey]bool` (nil-safe, error→empty); change the `BuildTree` call in `web_api_tree.go` to pass `s.archiveDecisions()`.

- [ ] **Step 4: Run** `go test ./cmd/serf-hub/... -v 2>&1 | tail -15` → PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/config.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_tree_test.go cmd/serf-hub/*.go
git commit -m "Wire archive store into navigation tree building"
```

---

### Task 5: `POST /api/archive` endpoint

**Files:**
- Modify: the hub API registration + a handler file (grep `"/api/` and the tree endpoint registration to find the mux + auth pattern; add the handler beside it, e.g. in `web_api_tree.go` or a new `web_api_archive.go`).
- Test: `cmd/serf-hub/web_api_archive_test.go`

**Interfaces:**
- Consumes: `WebConfig.Archive` (Task 4), `ArchiveStore.Set` (Task 1).
- Produces: route `POST /api/archive`, body `{"kind":"session"|"project","id":"...","archived":true|false}` → 200 `{"ok":true}`; 400 on bad kind/empty id; auth-gated like sibling `/api/` routes.

- [ ] **Step 1: Write the failing test** — issue a POST through the existing hub test server (`newHubRPCTestServer`, with `WebConfig{Archive: hubcore.NewArchiveStore(tmpDB), Past: hubcore.NewPastIndex("")}` + the test auth token), assert 200 and that `store.Decisions()` then contains the decision; assert 400 for `kind:"bogus"`.

```go
func TestArchiveEndpointSetsDecision(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})
	resp := hub.do(t, "POST", "/api/archive", `{"kind":"session","id":"s1","archived":true}`)
	if resp.Code != 200 {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	d, _ := store.Decisions()
	if !d[hubcore.ArchiveKey{Kind: "session", ID: "s1"}] {
		t.Fatalf("decision not persisted")
	}
}
```

Match `newHubRPCTestServer`'s actual helper API (read `app_auth_test.go` for the exact request helper and auth injection; adapt `hub.do` to whatever exists).

- [ ] **Step 2: Run — expect FAIL** (404/route missing) → `go test ./cmd/serf-hub/ -run TestArchiveEndpoint -v`.

- [ ] **Step 3: Implement** the handler: decode JSON, validate `kind ∈ {session,project}` and non-empty `id`, call `s.cfg.Archive.Set(kind, id, archived, time.Now())`, write `{"ok":true}`. Register under the same auth middleware as the tree endpoint. Return 405 for non-POST.

- [ ] **Step 4: Run** `go test ./cmd/serf-hub/ -run TestArchiveEndpoint -v` → PASS; then `make lint` → `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/web_api_archive*.go cmd/serf-hub/*.go
git commit -m "Add POST /api/archive endpoint to persist archive decisions"
```

---

### Task 6: Sidebar JS — archive control + archived disclosures

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js`
- Test: `cmd/serf-hub/jstest/test-sidebar-archive.js` (new; model on existing `test-sidebar-*.js`)

**Interfaces:**
- Consumes: `POST /api/archive` (Task 5); the `.archive-btn[data-archive-kind][data-archive-id]` controls and `details.session-tier.archived` markup (Task 3).
- Produces: clicking an archive control POSTs the decision then refreshes the tree (reuse the sidebar's existing appwire/tree refresh path — grep `sidebar.js` for how it currently refreshes); archived `<details>` default-collapsed.

- [ ] **Step 1: Write the failing test** — in the jstest harness, load the sidebar JS against fixture markup containing an `.archive-btn`, simulate a click, assert it issues a POST to `/api/archive` with the right body (stub `fetch`), and that an archived `<details>` is collapsed by default. Read an existing `test-sidebar-*.js` for the load/setup pattern; MUST `process.exit(0)` on success.

- [ ] **Step 2: Run — expect FAIL** → `cd cmd/serf-hub/jstest && node test-sidebar-archive.js` (expect failure/throw).

- [ ] **Step 3: Implement** the click handler in `sidebar.js` (delegate on `.archive-btn`, read `data-archive-kind`/`data-archive-id`, `fetch("/api/archive", {method:"POST", headers, body})`, then trigger the existing refresh). Ensure archived `<details>` are not force-opened.

- [ ] **Step 4: Run** `cd cmd/serf-hub/jstest && bash run-all.sh` → `jstest: all tests passed`.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/jstest/test-sidebar-archive.js
git commit -m "Sidebar: archive/unarchive control + collapsed archived disclosures"
```

---

### Task 7: CSS for session tiers, archived disclosures, archive control

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`
- Test: covered by `jstest` rendering + visual check; no new unit test required, but run the suite.

**Interfaces:**
- Consumes: classes from Task 3 (`.session-tier`, `.session-tier-label`, `details.session-tier.archived`, `.archived-projects`, `.archive-btn`).

- [ ] **Step 1: Implement styles** matching the golden grammar (neutral, recede archived; `.session-tier-label` a quiet dim label like other section labels; `.archive-btn` a low-emphasis hover control; archived disclosures neutral). Reuse existing tokens (`--text-muted`, `--space-*`, `--text-*`). No new colors; archived content recedes (neutral), not amber/red.

- [ ] **Step 2: Run** `make build-hub && (cd cmd/serf-hub/jstest && bash run-all.sh) && make lint`
Expected: build OK; `jstest: all tests passed`; `0 issues.` ×4.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "Sidebar: style session tiers + archived disclosures"
```

---

## Self-review notes (author)

- **Spec coverage:** Needs-you retained (Task 3); projects-by-recency (Task 3); session tiers Current/Recent/Archived (Tasks 2–3); manual + auto-2wk archive with unarchive + provenance rule (Tasks 1–2, effective-archived in `classifySession`); index.db persistence (Task 1) + endpoint (Task 5) + wiring (Task 4); template/JS/CSS (Tasks 3,6,7); `serf-e2e-*` bucket removed (Task 3 deletes `isTestProject`/`e2eProjectPrefix`). Scale fast-follow is explicitly out of scope (Global Constraints).
- **Type consistency:** `ArchiveKey{Kind,ID}`, `classifySession(decision *bool, lastActivity, now)`, `BuildTree(metas, live, decisions)` / `BuildTreeAt(..., now)`, `TreeProject.{Current,Recent,Archived,IsArchived,MostRecentStart}`, `Tree.{Projects,ArchivedProjects}` used consistently across tasks.
- **Adapt-to-codebase flags:** Tasks 3–6 intentionally say "match existing helpers/markup/refresh path" for the template session-row, the hub test request helper, and the sidebar refresh — these must be read from the live files; the novel logic (archive store, classification, BuildTree shape, endpoint contract) is given in full.
