# Sidebar Rebuild & Thread Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the htmx server-rendered sidebar with a client-rendered, keyed-reconcile navigation tree fed by a memoized `/api/tree`, and add path-safe project identity, favorites, a row menu, project delete, test-run classification, and in-scope session rename.

**Architecture:** The hub computes one memoized `Tree` (keyed on an inputs-version + 30s time bucket) and serves it as additive JSON on `/api/tree`; a rewritten `sidebar.js` holds that data as state and projects it to the DOM via hand-rolled reconciliation keyed on the server's scope-qualified `RowID`, so DOM node identity (hover, open menus, scroll) survives updates. Mutations are optimistic through a pending-ops overlay reconciled on the next resync. Project identity binds to the full working directory (a path slug), so every destructive verb is path-validated; rename is daemon-truth for live sessions and a re-check-guarded meta edit for ended ones.

**Tech Stack:** Go (hub: cmd/serf-hub + internal/hubcore; agent module for Origin/rename), vanilla JS (client-rendered sidebar, no framework), SQLite (decision stores), jstest (JSDOM suites), htmx (workspace swaps only).
---

## Module & gate map

Two Go modules are touched (`go.work` maps `.` and `./agent`; `envvars` is its own module):

- **Root module `primeradiant.com/serf`** (`.`): `cmd/serf-hub`, `hubapi`, `appwire`, `cmd/serf`, `server`, `rendezvous`.
- **Agent module `primeradiant.com/serf/agent`** (`./agent`): `agent`, `agent/schema`.
- **Envvars module** (`./envvars`): the `SERF_SESSION_ORIGIN` declaration only.

Per-task gates (run from repo root `/Users/jesse/prime-radiant/toil-suite/serf`):

- Root Go tests for a hub change: `go test ./cmd/serf-hub/... ./hubapi/... ./appwire/...`
- Agent Go tests: `cd agent && go test ./...`
- Root lint: `golangci-lint run ./...` (root) / `cd agent && golangci-lint run ./...` (agent).
- jstest: `sh cmd/serf-hub/jstest/run-all.sh` (JSDOM; each `test-*.js` exits 0/1). Requires `NODE_PATH` to a jsdom install; `run-all.sh` auto-detects `./node_modules` or `/tmp/serf-jstest-jsdom/node_modules`.
- AppWire doc regen: `make generate` (runs `go generate ./appwire/...` → `docs/appwire-protocol.md`); `make lint-generated` fails if stale.
- Full gate (final task): `make lint && make test && sh cmd/serf-hub/jstest/run-all.sh`.

Never `git add -A`; every commit lists exact paths.

## File Structure

**Created (Go, hub):**
- `cmd/serf-hub/internal/hubcore/favorite.go` — `FavoriteStore` (clone of `ArchiveStore`, `favorite` table).
- `cmd/serf-hub/internal/hubcore/treecache.go` — `TreeCache` (inputs-version + 30s bucket memo) and `InputsVersion` (atomic counter).
- `cmd/serf-hub/web_api_favorite.go` — `POST /api/favorite`.
- `cmd/serf-hub/web_api_project_delete.go` — `POST /api/project/delete`.
- `cmd/serf-hub/web_api_rename.go` — `POST /api/sessions/{ref}/rename` handler body.
- `cmd/serf-hub/internal/hubcore/remotecache.go` — async remote-source thread cache.

**Modified (Go, hub):** `internal/hubcore/tree.go`, `archive.go`, `past.go`, `roster.go`, `config.go`, `session_order.go` (none), `web_api_tree.go`, `web_api_archive.go`, `web.go`, `main.go`, `app_rpc.go`, `app_compact.go`, `app_threadread.go`, `internal/appsource/source.go`, `local_daemon.go`, `codex_source.go`; `hubapi/types.go`.

**Modified (Go, agent + appwire + daemon):** `agent/schema/snapshot.go`, `agent/session.go`, `agent/session_state.go`, `agent/session_init.go`, `agent/session_namer.go`, `appwire/types.go`, `appwire/protocol.go`, `appwire/client.go`, `server/appwire_runtime.go`, `server/server.go`, `cmd/serf/serve.go`, `cmd/serf/run.go`, `envvars/envvars.go`.

**Created (client + tests):** rewritten `cmd/serf-hub/assets/sidebar.js`; new `cmd/serf-hub/jstest/test-sidebar-model.js`, `test-sidebar-reconcile.js`, `test-sidebar-overlay.js`, `test-sidebar-menu.js`, `test-sidebar-migration.js`, `test-sidebar-survivors.js`.

**Modified (client + docs):** `cmd/serf-hub/templates/app.html`, `cmd/serf-hub/assets/style.css`, `docs/environment.md`, `docs/appwire-protocol.md` (generated), `docs/serf-hub-web-routing.md`.

**Deleted (final Phase B task, once the new suite is green):** `cmd/serf-hub/templates/partials/sidebar.html`, hub sidebar handlers/routes, and the subsumed `test-sidebar-*.js` files.

---

# Phase A — Hub core (Go)

## Task 1: Project identity — group by full working directory

Today `BuildTreeAt` groups sessions by `projectName(m)` = `filepath.Base(EffectiveWorkingDir(m))` (tree.go:340-341, :211-217), so `/a/foo` and `/b/foo` collapse into one node with a "first non-empty WorkingDir seen" (tree.go:349-353). This task rekeys grouping on the full path, adds `TreeProject.Key` (a path slug) and a `Worktrees` count; `Name` stays the basename for display.

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go` (grouping map, `TreeProject`, slug helper)
- Test: `cmd/serf-hub/internal/hubcore/tree_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/internal/hubcore/tree_test.go`:

```go
func TestCoBasenameProjectsAreDistinctNodes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/a/foo"}},
		{ID: "01B", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/b/foo"}},
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if len(tree.Projects) != 2 {
		t.Fatalf("want 2 distinct projects for co-basename dirs, got %d: %+v", len(tree.Projects), tree.Projects)
	}
	keys := map[string]string{}
	for _, p := range tree.Projects {
		if p.Name != "foo" {
			t.Fatalf("both projects display basename foo, got %q", p.Name)
		}
		keys[p.Key] = p.WorkingDir
	}
	if len(keys) != 2 {
		t.Fatalf("want 2 distinct Keys, got %v", keys)
	}
	if keys["no-project"] != "" {
		t.Fatalf("no session should land under no-project key: %v", keys)
	}
}

func TestNoProjectKeyIsStable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{{ID: "01A", CreatedAt: now, UpdatedAt: now}}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if len(tree.Projects) != 1 || tree.Projects[0].Key != "no-project" || tree.Projects[0].Name != "(no project)" {
		t.Fatalf("want a single (no project)/no-project node, got %+v", tree.Projects)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestCoBasenameProjectsAreDistinctNodes|TestNoProjectKeyIsStable' -v`
Expected: FAIL — one merged project (`len==1`) and `TreeProject.Key` undefined (compile error), so add the field first.

- [ ] **Step 3: Add `Key`/`Worktrees` to `TreeProject` and a slug helper**

In `cmd/serf-hub/internal/hubcore/tree.go`, add to the `TreeProject` struct (after `Name string`):

```go
	// Key is the stable, path-derived project identifier: a slug of the full
	// working directory ("<basename>-<8-hex sha256(path)>", or "no-project" for
	// pathless sessions). Readable and collision-resistant, not collision-free —
	// every destructive use is path-validated. Name stays the basename for display.
	Key string
```

and after `MoreArchived int`:

```go
	// Worktrees is the count of distinct non-empty WorktreePath values across
	// the project's sessions, surfaced in the delete confirmation.
	Worktrees int
```

Add the imports `crypto/sha256` and `encoding/hex` to the `import` block, and this helper near `projectName`:

```go
// projectSlug is the stable path-derived project key: "<basename>-<8hex>" of
// the full working directory, or "no-project" when the path is empty. The
// basename is sanitized for use as a URL/query key.
func projectSlug(path string) string {
	if path == "" {
		return "no-project"
	}
	sum := sha256.Sum256([]byte(path))
	base := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(filepath.Base(path))
	return base + "-" + hex.EncodeToString(sum[:4])
}
```

- [ ] **Step 4: Rekey grouping on the full path**

In `BuildTreeAt`, change the `projectAccum` to carry the display name and rekey the map on the path. Replace the grouping block (currently keyed by `pname := projectName(m)`):

```go
	type projectAccum struct {
		name       string                          // basename for display
		topLevel   []schema.SessionMeta
		children   map[string][]schema.SessionMeta // parentID -> children
		workingDir string                          // the full grouping path ("" for no-project)
		worktrees  map[string]bool                 // distinct WorktreePath set
	}
	projects := make(map[string]*projectAccum) // keyed by EffectiveWorkingDir path
	projectOrder := []string{}                 // insertion order (paths) for stable output
```

and the per-meta grouping loop head (was `pname := projectName(m); if _, ok := projects[pname]; ...`):

```go
	for _, m := range metas {
		path := EffectiveWorkingDir(m)
		acc := projects[path]
		if acc == nil {
			acc = &projectAccum{name: projectName(m), children: map[string][]schema.SessionMeta{}, worktrees: map[string]bool{}}
			projects[path] = acc
			projectOrder = append(projectOrder, path)
		}
		if acc.workingDir == "" && path != "" {
			acc.workingDir = path
		}
		if m.WorktreePath != "" {
			acc.worktrees[m.WorktreePath] = true
		}
```

Then replace every later `for _, pname := range projectOrder { acc := projects[pname]` with `for _, path := range projectOrder { acc := projects[path]` and use `acc.name` where the old code used `pname` as the display `Project`/`Name`. In the `TreeProject{...}` literal set:

```go
			Name:            acc.name,
			Key:             projectSlug(path),
			WorkingDir:      acc.workingDir,
			Worktrees:       len(acc.worktrees),
```

Update the session-node `Project:` fields (in the `sessions`, subagent, and fork builders) from `pname` to `acc.name`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestCoBasename|TestNoProjectKey|TestBuildTree|TestArchive' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go
git commit -m "feat(hub): key projects by full working dir with a path slug"
```

---

## Task 2: Project archive decisions bind to the path (legacy basename read-fallback)

Project archive placement keys on `ArchiveKey{Kind:"project", ID: pname}` (tree.go:503) — the basename. Rekey it to the path with a legacy-basename read-fallback and stated precedence: a path-keyed row always wins over a legacy basename row (round-2 B7, round-3 G3).

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Test: `cmd/serf-hub/internal/hubcore/tree_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestProjectArchiveDecisionPathKeyedWithLegacyFallback(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, wd string) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: wd}}
	}
	// Legacy basename row archives BOTH co-basename projects (read fallback).
	legacy := map[ArchiveKey]bool{{Kind: "project", ID: "foo"}: true}
	tree := BuildTreeAt([]schema.SessionMeta{mk("01A", "/a/foo"), mk("01B", "/b/foo")}, nil, legacy, now)
	if len(tree.Projects) != 0 || len(tree.ArchivedProjects) != 2 {
		t.Fatalf("legacy basename row should archive both; got projects=%d archived=%d", len(tree.Projects), len(tree.ArchivedProjects))
	}
	// A path-keyed row wins over the legacy row: /a/foo explicitly unarchived.
	precedence := map[ArchiveKey]bool{{Kind: "project", ID: "foo"}: true, {Kind: "project", ID: "/a/foo"}: false}
	tree = BuildTreeAt([]schema.SessionMeta{mk("01A", "/a/foo"), mk("01B", "/b/foo")}, nil, precedence, now)
	if len(tree.Projects) != 1 || tree.Projects[0].WorkingDir != "/a/foo" {
		t.Fatalf("path row false must win over legacy true; got %+v", tree.Projects)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestProjectArchiveDecisionPathKeyedWithLegacyFallback -v`
Expected: FAIL — the current `decisions[ArchiveKey{Kind:"project", ID: pname}]` only reads the basename and cannot express path precedence.

- [ ] **Step 3: Add the resolver and use it**

In `cmd/serf-hub/internal/hubcore/tree.go`, add:

```go
// projectArchivedDecision resolves a project's manual archive decision. A
// path-keyed row always wins; a legacy basename-keyed row is honored only when
// no path-keyed row exists (round-2 B7 / round-3 G3 precedence). Returns false
// when neither row is present.
func projectArchivedDecision(decisions map[ArchiveKey]bool, path, basename string) bool {
	if v, ok := decisions[ArchiveKey{Kind: "project", ID: path}]; ok {
		return v
	}
	if v, ok := decisions[ArchiveKey{Kind: "project", ID: basename}]; ok {
		return v
	}
	return false
}
```

Replace the `isArchived` computation:

```go
		isArchived := projectArchivedDecision(decisions, path, acc.name) ||
			(len(current) == 0 && len(recent) == 0)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestProjectArchiveDecision|TestArchiveDecisionsFlowIntoTree' -v`
Expected: PASS. (`TestArchiveDecisionsFlowIntoTree` uses a basename row `"alpha"` for path `/projects/alpha`; the fallback keeps it green.)

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go
git commit -m "feat(hub): path-key project archive decisions with legacy fallback + precedence"
```

---

## Task 3: Orphan-live grouping rewrite (shared rules + no-project key)

`handleAPITree`'s orphan-live path (web_api_tree.go:70-106) groups leftover live sessions by `filepath.Base(le.WorkingDir)` with the old `projectKey(name)` slug and no worktree substitution (round-2 A10). Rewrite it onto `EffectiveWorkingDir`-equivalent rules and `projectSlug`, with a `no-project` key for pathless entries. Live entries synthesized from `appwire.Thread` carry `WorktreePath == ""`, so `EffectiveWorkingDir` reduces to `le.WorkingDir` here — group directly on `le.WorkingDir`.

**Files:**
- Modify: `cmd/serf-hub/web_api_tree.go`
- Modify: `cmd/serf-hub/internal/hubcore/tree.go` (export `ProjectSlug`)
- Test: `cmd/serf-hub/web_api_tree_test.go` (append)

- [ ] **Step 1: Export the slug helper for package `main`**

In `cmd/serf-hub/internal/hubcore/tree.go`, add an exported wrapper (package `main` cannot call the unexported `projectSlug`):

```go
// ProjectSlug is the exported path-derived project key used by the hub's
// orphan-live grouping and project-key resolution. See projectSlug.
func ProjectSlug(path string) string { return projectSlug(path) }
```

- [ ] **Step 2: Write the failing test**

```go
func TestOrphanLiveGroupingUsesPathSlug(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "01L", WorkingDir: "/a/foo"}, SessionID: "01L", Status: "active"},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Projects) != 1 || resp.Projects[0].Key != hubcore.ProjectSlug("/a/foo") {
		t.Fatalf("orphan-live must use the path slug; got %+v", resp.Projects)
	}
}
```

Add imports `encoding/json`, `net/http`, `net/http/httptest`, `primeradiant.com/serf/hubapi`, `primeradiant.com/serf/rendezvous` to the test file if absent.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/ -run TestOrphanLiveGroupingUsesPathSlug -v`
Expected: FAIL — key is `projectKey("foo")` == `"foo"`, not the slug.

- [ ] **Step 4: Rewrite the orphan-live grouping**

In `handleAPITree` replace the orphan-live block (from `project := filepath.Base(le.WorkingDir)` through the appended `hubapi.TreeProject{...}`) with:

```go
		project := filepath.Base(le.WorkingDir)
		if le.WorkingDir == "" || project == "." {
			project = "(no project)"
		}
		key := hubcore.ProjectSlug(le.WorkingDir)
		node := hubcore.TreeNode{
			ID:        le.SessionID,
			Title:     liveTitle(le.SessionID, le, s.cfg.Past),
			Project:   project,
			State:     hubcore.NormalizeState(le.Status),
			Kind:      "session",
			CreatedAt: le.StartedAt,
			UpdatedAt: le.StartedAt,
			Age:       hubcore.AgeString(le.StartedAt),
		}
		apiNode := s.apiTreeNode("project", key, node, true)
		if idx, ok := projectIndexes[key]; ok {
			p := &resp.Projects[idx]
			p.Sessions = append(p.Sessions, apiNode)
			if hubcore.AttentionRank(node.State) > hubcore.AttentionRank(p.RollupState) {
				p.RollupState = node.State
			}
			continue
		}
		projectIndexes[key] = len(resp.Projects)
		resp.Projects = append(resp.Projects, hubapi.TreeProject{
			Key:         key,
			Name:        project,
			WorkingDir:  le.WorkingDir,
			RollupState: node.State,
			Sessions:    []hubapi.TreeNode{apiNode},
		})
```

Also change the main project loop's `key := projectKey(p.Name)` to `key := p.Key` (the path slug from Task 1).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/ -run 'TestOrphanLive|TestAPITree' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_tree_test.go
git commit -m "feat(hub): rewrite orphan-live grouping onto the path slug + no-project key"
```

---

## Task 4: Tree JSON additive fields + tier projections

Grow `hubapi.TreeResponse`/`TreeProject`/`TreeNode` with the additive fields the client renders, and project the tiers in `handleAPITree`. The TUI is unaffected (it reads appwire `ThreadList`, not `/api/tree`). `TestRuns` is declared here but stays empty until Task 15 populates `IsTestRun`.

**Files:**
- Modify: `hubapi/types.go`
- Modify: `cmd/serf-hub/web_api_tree.go`
- Modify: `cmd/serf-hub/internal/hubcore/tree.go` (expose `Tree.NeedsYou` already exists; add `Tier` to nodes at projection time)
- Test: `hubapi/types_test.go` (append) + `cmd/serf-hub/web_api_tree_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/web_api_tree_test.go`:

```go
func TestTreeResponseProjectsCarryAdditiveFields(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/proj", GitBranch: "main"}},
	}
	past := hubcore.NewPastIndex("")
	web := NewWebServer(hubcore.WebConfig{Past: past})
	web.injectMetasForTest(metas) // helper: see step 3
	req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TestRuns == nil {
		// field must exist and marshal (empty slice or null both acceptable)
	}
	if len(resp.Projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(resp.Projects))
	}
	p := resp.Projects[0]
	if p.RollupLive != 0 || p.MoreCurrent != 0 || p.Worktrees != 0 {
		t.Fatalf("additive project fields should be zero-valued here: %+v", p)
	}
	if len(p.Sessions) != 1 || p.Sessions[0].Branch != "main" || p.Sessions[0].Tier != "recent" {
		t.Fatalf("node additive fields wrong: %+v", p.Sessions)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/ -run TestTreeResponseProjectsCarryAdditiveFields -v`
Expected: FAIL — `TreeResponse.TestRuns`, `TreeProject.RollupLive`, `TreeNode.Tier` undefined.

- [ ] **Step 3: Add the wire fields**

In `hubapi/types.go`, extend the three structs (keep existing fields):

```go
type TreeResponse struct {
	GeneratedAt      time.Time        `json:"generated_at"`
	Sources          []Source         `json:"sources"`
	Live             []TreeNode       `json:"live"`
	NeedsYou         []TreeNode       `json:"needs_you"`
	Favorites        []TreeNode       `json:"favorites"`
	Projects         []TreeProject    `json:"projects"`
	ArchivedProjects []TreeProject    `json:"archived_projects"`
	TestRuns         []TreeProject    `json:"test_runs"`
	AttentionSummary AttentionSummary `json:"attentionSummary"` // serf:naming-ignore
}
```

```go
type TreeProject struct {
	Key             string     `json:"key"`
	Name            string     `json:"name"`
	WorkingDir      string     `json:"working_dir,omitempty"`
	RollupState     string     `json:"rollup_state,omitempty"`
	RollupLive      int        `json:"rollup_live,omitempty"`
	RollupAttn      int        `json:"rollup_attn,omitempty"`
	DefaultExpanded bool       `json:"default_expanded,omitempty"`
	MoreCurrent     int        `json:"more_current,omitempty"`
	MoreRecent      int        `json:"more_recent,omitempty"`
	MoreArchived    int        `json:"more_archived,omitempty"`
	Worktrees       int        `json:"worktrees,omitempty"`
	Sessions        []TreeNode `json:"sessions"`
}
```

Add to `TreeNode` (after `Kind`):

```go
	Tier         string `json:"tier,omitempty"`
	Branch       string `json:"branch,omitempty"`
	ClusterCount int    `json:"cluster_count,omitempty"`
	Favorite     bool   `json:"favorite,omitempty"`
	Rename       bool   `json:"rename,omitempty"`
```

- [ ] **Step 4: Add the test helper + project the tiers**

Add to a hub test helper file (`cmd/serf-hub/web_test.go` has `NewWebServer` usages; add near them) — a small seam that seeds a nil-DB past index:

```go
// injectMetasForTest replaces the past index with one holding the given metas.
func (s *WebServer) injectMetasForTest(metas []schema.SessionMeta) {
	idx := hubcore.NewPastIndex("")
	idx.SeedForTest(metas)
	s.cfg.Past = idx
}
```

In `internal/hubcore/past.go` add:

```go
// SeedForTest replaces the in-memory index with the given metas (StateDir left
// blank). Test-only seam; production always goes through Rebuild.
func (i *PastIndex) SeedForTest(metas []schema.SessionMeta) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.all = i.all[:0]
	i.byID = map[string]PastEntry{}
	for _, m := range metas {
		pe := PastEntry{ID: m.ID, Meta: m}
		i.all = append(i.all, pe)
		i.byID[m.ID] = pe
	}
	sort.SliceStable(i.all, func(a, b int) bool { return sessionMetaLess(i.all[a].Meta, i.all[b].Meta) })
}
```

In `handleAPITree`, project the additive fields. Replace the project-loop body's `ap := hubapi.TreeProject{...}` construction and add the new tier arrays. First, factor a node-with-tier projector; extend `apiTreeNode` to accept a tier:

```go
func (s *WebServer) apiTreeProject(scope string, p hubcore.TreeProject) hubapi.TreeProject {
	ap := hubapi.TreeProject{
		Key:             p.Key,
		Name:            p.Name,
		WorkingDir:      p.WorkingDir,
		RollupState:     p.RollupState,
		RollupLive:      p.RollupLive,
		RollupAttn:      p.RollupAttn,
		DefaultExpanded: p.Expanded,
		MoreCurrent:     p.MoreCurrent,
		MoreRecent:      p.MoreRecent,
		MoreArchived:    p.MoreArchived,
		Worktrees:       p.Worktrees,
	}
	for _, n := range p.Current {
		ap.Sessions = append(ap.Sessions, s.apiTreeNodeTier(scope, p.Key, "current", n))
	}
	for _, n := range p.Recent {
		ap.Sessions = append(ap.Sessions, s.apiTreeNodeTier(scope, p.Key, "recent", n))
	}
	for _, n := range p.Archived {
		ap.Sessions = append(ap.Sessions, s.apiTreeNodeTier(scope, p.Key, "archived", n))
	}
	return ap
}
```

Add `apiTreeNodeTier` wrapping `apiTreeNode` and stamping the row-level fields:

```go
func (s *WebServer) apiTreeNodeTier(scope, projectKey, tier string, n hubcore.TreeNode) hubapi.TreeNode {
	out := s.apiTreeNode(scope, projectKey, n, treeNodeCanActLive(n) && s.isLive(n.ID))
	out.Tier = tier
	out.Branch = n.Branch
	out.ClusterCount = n.ClusterCount
	out.Favorite = s.isFavorite(n.ID)      // Task 8 wires isFavorite; stub returns false until then
	out.Rename = s.rowRenameable(n.ID)     // Task 18 wires rowRenameable; stub returns local-only
	return out
}
```

For this task, add temporary stubs so the file compiles (real bodies land in Tasks 8/18):

```go
func (s *WebServer) isFavorite(id string) bool   { return false }
func (s *WebServer) rowRenameable(id string) bool { return isLocalRouteID(id) }
```

Replace the project loop and add the NeedsYou/Archived projections at the end of `handleAPITree` (before `writeAPIJSON`):

```go
	for _, p := range tree.Projects {
		projectIndexes[p.Key] = len(resp.Projects)
		ap := s.apiTreeProject("project", p)
		for _, n := range ap.Sessions {
			seenProjectRefs[n.SessionID] = true
		}
		resp.Projects = append(resp.Projects, ap)
	}
	for _, p := range tree.ArchivedProjects {
		resp.ArchivedProjects = append(resp.ArchivedProjects, s.apiTreeProject("project", p))
	}
	for _, n := range tree.NeedsYou {
		resp.NeedsYou = append(resp.NeedsYou, s.apiTreeNodeTier("needsyou", "", "needsyou", n))
	}
```

(Keep the existing `resp.Live` and orphan-live loops. Remove the now-dead `seenProjectRefs`/`projectIndexes`/`allProjects` bootstrap that the old inline `ap := hubapi.TreeProject{...}` used, replacing it with the loop above.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... ./hubapi/... -run 'TestTreeResponse|TestAPITree|TestOrphanLive' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add hubapi/types.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/internal/hubcore/past.go cmd/serf-hub/web_test.go
git commit -m "feat(hub): additive /api/tree fields + tier projections (NeedsYou/Archived/TestRuns)"
```

---

## Task 5: Cluster synthetic IDs

Cluster nodes ship with an empty `ID` (tree.go:768-777); under RowID keying every pair of clusters in a project collapses to `project:<key>:` (round-2 A7/B4; `hubapi.ParseRef("")` → `""`). Give each cluster a stable synthetic ID `cluster:<8hex sha256(title)>`, scoped by project.

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Test: `cmd/serf-hub/internal/hubcore/tree_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestTwoClustersInOneProjectGetDistinctIDs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	old := now.Add(-30 * 24 * time.Hour) // ended, clusterable
	mk := func(id, title string) schema.SessionMeta {
		return schema.SessionMeta{ID: id, Name: title, CreatedAt: old, UpdatedAt: old, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/p"}}
	}
	metas := []schema.SessionMeta{
		mk("01A", "alpha"), mk("01B", "alpha"), mk("01C", "alpha"),
		mk("01D", "beta"), mk("01E", "beta"), mk("01F", "beta"),
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	var clusters []TreeNode
	for _, p := range tree.ArchivedProjects {
		for _, n := range append(append([]TreeNode{}, p.Archived...), p.Recent...) {
			if n.Kind == "cluster" {
				clusters = append(clusters, n)
			}
		}
	}
	if len(clusters) != 2 {
		t.Fatalf("want 2 clusters, got %d", len(clusters))
	}
	if clusters[0].ID == "" || clusters[1].ID == "" || clusters[0].ID == clusters[1].ID {
		t.Fatalf("clusters need distinct non-empty IDs: %q vs %q", clusters[0].ID, clusters[1].ID)
	}
	for _, c := range clusters {
		if !strings.HasPrefix(c.ID, "cluster:") {
			t.Fatalf("cluster ID must be cluster:<hex>, got %q", c.ID)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestTwoClustersInOneProjectGetDistinctIDs -v`
Expected: FAIL — both cluster IDs are `""`.

- [ ] **Step 3: Assign the synthetic ID**

In `clusterRepeatedTitles`, set the `ID` on the emitted cluster node (the `out = append(out, TreeNode{...})` literal):

```go
		out = append(out, TreeNode{
			ID:           clusterID(s.Project, title),
			Title:        title,
			Project:      s.Project,
			State:        "ended",
			Kind:         "cluster",
			ClusterCount: len(members),
			UpdatedAt:    s.UpdatedAt,
			Age:          s.Age,
			Children:     members,
		})
```

Add the helper:

```go
// clusterID is the stable synthetic id for a repeated-title cluster, scoped by
// project so equal titles in different projects never collide, and never empty
// (an empty id renders as an empty ref and collides all clusters in a project
// at RowID "project:<key>:" — round-2 A7/B4).
func clusterID(project, title string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + title))
	return "cluster:" + hex.EncodeToString(sum[:4])
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestTwoClusters|TestBuildTree|TestCluster' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go
git commit -m "feat(hub): stable synthetic IDs for repeated-title clusters"
```

---

## Task 6: Subagent per-tier 50-cap

Top-level rows are capped at `maxSidebarSessionsPerTier` (50) via `capTier`, but subagent children are appended uncapped (tree.go:427-454). Cap the subagent list the same way so a parent with hundreds of workers can't bloat a node.

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Test: `cmd/serf-hub/internal/hubcore/tree_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestSubagentChildrenCappedPerTier(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	parent := schema.SessionMeta{ID: "01P", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/p"}}
	metas := []schema.SessionMeta{parent}
	for i := 0; i < 60; i++ {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("01S%02d", i), IsSubagent: true, ParentSessionID: "01P",
			CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/p"},
		})
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	var subs int
	for _, p := range tree.Projects {
		for _, n := range p.Current {
			if n.ID == "01P" {
				subs = len(n.Children)
			}
		}
	}
	if subs != maxSidebarSessionsPerTier {
		t.Fatalf("subagent children should cap at %d, got %d", maxSidebarSessionsPerTier, subs)
	}
}
```

Add `"fmt"` to the test imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestSubagentChildrenCappedPerTier -v`
Expected: FAIL — 60 children emitted.

- [ ] **Step 3: Cap the subagent slice**

In `BuildTreeAt`, after the `subagents`/`forks` are sorted (the `sort.SliceStable(subagents, ...)` call) and before the append loop, add:

```go
		subagents, _ = capTier(subagentNodesUnused(subagents), maxSidebarSessionsPerTier)
```

Simpler — cap the meta slice directly (it is `[]schema.SessionMeta`, already recency-sorted):

```go
		if len(subagents) > maxSidebarSessionsPerTier {
			subagents = subagents[:maxSidebarSessionsPerTier]
		}
```

(Insert immediately after `sort.SliceStable(subagents, func(i, j int) bool { return sessionMetaLess(subagents[i], subagents[j]) })`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestSubagentChildrenCapped|TestBuildTree' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go
git commit -m "feat(hub): cap subagent children per tier like top-level rows"
```

---

## Task 7: FavoriteStore + `Delete` on both decision stores

Add a `favorite` table (clone of `ArchiveStore`'s shape) and a `Delete(kind, id)` on both stores. Both live in the same `~/.serf/index.db`.

**Files:**
- Create: `cmd/serf-hub/internal/hubcore/favorite.go`
- Modify: `cmd/serf-hub/internal/hubcore/archive.go` (add `Delete`)
- Test: `cmd/serf-hub/internal/hubcore/favorite_test.go` (create), `archive_test.go` (append)

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/internal/hubcore/favorite_test.go`:

```go
package hubcore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFavoriteStoreSetAndDelete(t *testing.T) {
	dir := t.TempDir()
	fav := NewFavoriteStore(filepath.Join(dir, "index.db"))
	now := time.Unix(1_700_000_000, 0)
	if err := fav.Set("session", "01A", true, now); err != nil {
		t.Fatal(err)
	}
	got, err := fav.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if !got[ArchiveKey{Kind: "session", ID: "01A"}] {
		t.Fatalf("favorite not persisted: %v", got)
	}
	if err := fav.Delete("session", "01A"); err != nil {
		t.Fatal(err)
	}
	got, _ = fav.Favorites()
	if _, present := got[ArchiveKey{Kind: "session", ID: "01A"}]; present {
		t.Fatalf("row should be gone after Delete: %v", got)
	}
}

func TestArchiveStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewArchiveStore(filepath.Join(dir, "index.db"))
	now := time.Unix(1_700_000_000, 0)
	_ = store.Set("project", "/a/foo", true, now)
	if err := store.Delete("project", "/a/foo"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Decisions()
	if _, present := got[ArchiveKey{Kind: "project", ID: "/a/foo"}]; present {
		t.Fatalf("archive row should be gone: %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestFavoriteStore|TestArchiveStoreDelete' -v`
Expected: FAIL — `NewFavoriteStore` and `Delete` undefined.

- [ ] **Step 3: Add `Delete` to `ArchiveStore`**

In `cmd/serf-hub/internal/hubcore/archive.go`, add:

```go
// Delete removes a decision row. A no-op when the DB path is empty or the row
// is absent (idempotent — the delete/scrub paths call it unconditionally).
func (s *ArchiveStore) Delete(kind, id string) error {
	if s.dbPath == "" {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`DELETE FROM archive WHERE kind = ? AND id = ?`, kind, id) //nolint:noctx // local file DB
	return err
}
```

- [ ] **Step 4: Create `FavoriteStore`**

Create `cmd/serf-hub/internal/hubcore/favorite.go` mirroring `archive.go` (same `open`/`Set`/`Delete` shape, `favorite` table, `Favorites()` reader):

```go
package hubcore

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
)

// FavoriteStore persists explicit user favorite decisions in index.db, cloned
// from ArchiveStore's shape (kind="session"). It shares the same DB file.
type FavoriteStore struct {
	dbPath string
	fs     afero.Fs
}

func NewFavoriteStore(dbPath string) *FavoriteStore {
	return &FavoriteStore{dbPath: dbPath, fs: afero.NewOsFs()}
}

func (s *FavoriteStore) SetFs(fs afero.Fs) *FavoriteStore { s.fs = fs; return s }

const createFavoriteTable = `
CREATE TABLE IF NOT EXISTS favorite (
  kind       TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  favorited  INTEGER NOT NULL,
  decided_at INTEGER NOT NULL,
  PRIMARY KEY (kind, id)
)`

func (s *FavoriteStore) open() (*sql.DB, error) {
	if err := s.fs.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(createFavoriteTable); err != nil { //nolint:noctx // local file DB
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (s *FavoriteStore) Set(kind, id string, favorited bool, now time.Time) error {
	if s.dbPath == "" {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	flag := 0
	if favorited {
		flag = 1
	}
	_, err = db.Exec( //nolint:noctx // local file DB
		`INSERT INTO favorite (kind, id, favorited, decided_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(kind, id) DO UPDATE SET favorited=excluded.favorited, decided_at=excluded.decided_at`,
		kind, id, flag, now.Unix())
	return err
}

func (s *FavoriteStore) Delete(kind, id string) error {
	if s.dbPath == "" {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`DELETE FROM favorite WHERE kind = ? AND id = ?`, kind, id) //nolint:noctx // local file DB
	return err
}

// Favorites returns every favorited=true decision. Empty when no DB / no table.
func (s *FavoriteStore) Favorites() (map[ArchiveKey]bool, error) {
	out := make(map[ArchiveKey]bool)
	if s.dbPath == "" {
		return out, nil
	}
	if _, err := s.fs.Stat(s.dbPath); os.IsNotExist(err) {
		return out, nil
	}
	db, err := s.open()
	if err != nil {
		return out, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT kind, id, favorited FROM favorite`) //nolint:noctx // local file DB
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
		if flag == 1 {
			out[ArchiveKey{Kind: k, ID: id}] = true
		}
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestFavoriteStore|TestArchiveStoreDelete' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/favorite.go cmd/serf-hub/internal/hubcore/favorite_test.go cmd/serf-hub/internal/hubcore/archive.go
git commit -m "feat(hub): favorite decision store + Delete on both decision stores"
```

---

## Task 8: `/api/favorite` endpoint, Pinned tier, and un-archive legacy-row lifecycle

Add `POST /api/favorite` (mirror of `/api/archive`), wire `FavoriteStore` into `WebConfig`/`WebServer`, build the Pinned tier (unarchived favorited sessions, most-recent first, excluding Needs-you) into `TreeResponse.Favorites`, and add the un-archive legacy-basename lifecycle to `/api/archive`.

**Files:**
- Create: `cmd/serf-hub/web_api_favorite.go`
- Modify: `cmd/serf-hub/internal/hubcore/config.go` (`WebConfig.Favorite`), `cmd/serf-hub/web.go` (route + `isFavorite`), `cmd/serf-hub/web_api_tree.go` (Pinned tier + real `isFavorite`), `cmd/serf-hub/web_api_archive.go` (legacy lifecycle), `cmd/serf-hub/main.go` (construct store)
- Test: `cmd/serf-hub/web_api_favorite_test.go` (create), `web_api_archive_test.go` (append)

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/web_api_favorite_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func TestFavoriteEndpointSetsDecision(t *testing.T) {
	dir := t.TempDir()
	fav := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Favorite: fav, Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(`{"kind":"session","id":"01A","favorited":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := fav.Favorites()
	if !got[hubcore.ArchiveKey{Kind: "session", ID: "01A"}] {
		t.Fatalf("favorite not persisted: %v", got)
	}
}

func TestUnarchiveProjectDropsLegacyBasenameRow(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	_ = store.Set("project", "foo", true, timeNowForTest()) // legacy basename row
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(`{"kind":"project","id":"/a/foo","archived":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	got, _ := store.Decisions()
	if _, present := got[hubcore.ArchiveKey{Kind: "project", ID: "foo"}]; present {
		t.Fatalf("legacy basename row should be dropped on un-archive: %v", got)
	}
	if v := got[hubcore.ArchiveKey{Kind: "project", ID: "/a/foo"}]; v {
		t.Fatalf("path row should be false, got true")
	}
}
```

Add a `timeNowForTest` helper if none exists, or reuse `time.Unix(1_700_000_000, 0)` inline.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/ -run 'TestFavoriteEndpoint|TestUnarchiveProjectDropsLegacy' -v`
Expected: FAIL — no `/api/favorite` route; `WebConfig.Favorite` undefined.

- [ ] **Step 3: Add `Favorite` to `WebConfig` and construct it**

In `internal/hubcore/config.go`, add to `WebConfig` (after `Archive *ArchiveStore`):

```go
	Favorite *FavoriteStore // favorite decision store; nil when not configured
```

In `cmd/serf-hub/main.go`, after `archive := hubcore.NewArchiveStore(pastIndexDB)`:

```go
	favorite := hubcore.NewFavoriteStore(pastIndexDB)
```

and add `Favorite: favorite,` to the `NewWebServer(hubcore.WebConfig{...})` literal.

- [ ] **Step 4: Add the endpoint + route + Pinned tier + legacy lifecycle**

Create `cmd/serf-hub/web_api_favorite.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleAPIFavorite handles POST /api/favorite.
// Body: {"kind":"session","id":"...","favorited":true|false}
func (s *WebServer) handleAPIFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Kind      string `json:"kind"`
		ID        string `json:"id"`
		Favorited bool   `json:"favorited"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Kind != "session" && body.Kind != "project" {
		writeAPIError(w, http.StatusBadRequest, `kind must be "session" or "project"`)
		return
	}
	if body.ID == "" {
		writeAPIError(w, http.StatusBadRequest, "id is required")
		return
	}
	if s.cfg.Favorite == nil {
		writeAPIError(w, http.StatusInternalServerError, "favorite store not configured")
		return
	}
	if err := s.cfg.Favorite.Set(body.Kind, body.ID, body.Favorited, time.Now()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "favorite store error: "+err.Error())
		return
	}
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```

In `cmd/serf-hub/web.go` `Handler()`, add after the `/api/archive` route:

```go
	mux.HandleFunc("/api/favorite", s.handleAPIFavorite)
```

Add the favorite lookup helpers and Pinned tier to `web_api_tree.go`. Replace the temporary `isFavorite` stub with:

```go
func (s *WebServer) favoriteDecisions() map[hubcore.ArchiveKey]bool {
	if s.cfg.Favorite == nil {
		return map[hubcore.ArchiveKey]bool{}
	}
	f, err := s.cfg.Favorite.Favorites()
	if err != nil {
		return map[hubcore.ArchiveKey]bool{}
	}
	return f
}

func (s *WebServer) isFavorite(id string) bool {
	return s.favoriteDecisions()[hubcore.ArchiveKey{Kind: "session", ID: id}]
}
```

**Efficiency note (fix the per-node DB open):** `apiTreeNodeTier` (Task 4) calls `s.isFavorite(n.ID)` per node, and `isFavorite` opens SQLite each call — O(nodes) DB opens per `/api/tree`. Compute the favorites map **once per request** in `handleAPITree` and thread it through. Change `apiTreeNodeTier`'s signature to `apiTreeNodeTier(scope, projectKey, tier string, favs map[hubcore.ArchiveKey]bool, n hubcore.TreeNode)` and replace the `s.isFavorite(n.ID)` call with `favs[hubcore.ArchiveKey{Kind: "session", ID: n.ID}]`; `apiTreeProject` takes and forwards `favs`; `handleAPITree` computes `favs := s.favoriteDecisions()` once and passes it to every projection call (NeedsYou, Pinned, project loops). Update the Task 4/Task 15 call sites accordingly. (`isFavorite` remains for the earlier compile stub; delete it once `favs` is threaded.)

Build the Pinned tier in `handleAPITree` (before `writeAPIJSON`). Collect favorited, unarchived sessions across all projects excluding those already in `NeedsYou`:

```go
	needsYouIDs := map[string]bool{}
	for _, n := range resp.NeedsYou {
		needsYouIDs[n.SessionID] = true
	}
	favs := s.favoriteDecisions()
	for _, p := range tree.Projects {
		for _, n := range append(append([]hubcore.TreeNode{}, p.Current...), p.Recent...) {
			if n.Kind != "session" || !favs[hubcore.ArchiveKey{Kind: "session", ID: n.ID}] {
				continue
			}
			if needsYouIDs[hubRefFromTreeNodeID(n.ID).SessionID] {
				continue
			}
			resp.Favorites = append(resp.Favorites, s.apiTreeNodeTier("pinned", "", "pinned", n))
		}
	}
	sort.SliceStable(resp.Favorites, func(i, j int) bool {
		return resp.Favorites[i].UpdatedAt.After(resp.Favorites[j].UpdatedAt)
	})
```

Add the un-archive legacy lifecycle to `handleAPIArchive` (web_api_archive.go), after the successful `s.cfg.Archive.Set(...)`:

```go
	if body.Kind == "project" && !body.Archived {
		// Visible-wins: dropping the path row's archive also drops any legacy
		// basename row that could re-hide this (or a co-basename) project
		// (round-3 G3). filepath.Base of a path id yields the legacy key.
		_ = s.cfg.Archive.Delete("project", filepath.Base(body.ID))
	}
```

Add `"path/filepath"` to `web_api_archive.go` imports and `"sort"` to `web_api_tree.go` imports if absent.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/ -run 'TestFavorite|TestUnarchive|TestArchive|TestAPITree' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/web_api_favorite.go cmd/serf-hub/web_api_favorite_test.go cmd/serf-hub/web.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_archive.go cmd/serf-hub/internal/hubcore/config.go cmd/serf-hub/main.go
git commit -m "feat(hub): /api/favorite endpoint, Pinned tier, un-archive legacy-row lifecycle"
```

---

## Task 9: Memoized tree (`TreeCache` + `InputsVersion`)

Add a hub-level memo that computes `BuildTree` plus the attention *summary* once per `(inputs-version, 30s time bucket)`. The attention *map* is NOT cached — the watcher keeps deriving fresh (round-3 H2). `handleAPITree` reads the memo.

**Files:**
- Create: `cmd/serf-hub/internal/hubcore/treecache.go`
- Modify: `cmd/serf-hub/internal/hubcore/config.go` (`WebConfig.Inputs`), `cmd/serf-hub/web.go` (construct cache), `cmd/serf-hub/web_api_tree.go` (use memo)
- Test: `cmd/serf-hub/internal/hubcore/treecache_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/internal/hubcore/treecache_test.go`:

```go
package hubcore

import (
	"testing"
	"time"
)

func TestTreeCacheMemoizesByVersionAndBucket(t *testing.T) {
	cache := &TreeCache{}
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	compute := func() (Tree, AttentionSummary) { calls++; return Tree{}, AttentionSummary{} }

	cache.Get(1, now, compute)
	cache.Get(1, now.Add(5*time.Second), compute) // same version, same 30s bucket
	if calls != 1 {
		t.Fatalf("same version+bucket should compute once, got %d", calls)
	}
	cache.Get(2, now, compute) // version bump busts
	if calls != 2 {
		t.Fatalf("version bump should recompute, got %d", calls)
	}
	cache.Get(2, now.Add(31*time.Second), compute) // next time bucket busts
	if calls != 3 {
		t.Fatalf("bucket roll should recompute, got %d", calls)
	}
}

func TestInputsVersionBump(t *testing.T) {
	iv := &InputsVersion{}
	if iv.Load() != 0 {
		t.Fatal("fresh version should be 0")
	}
	iv.Bump()
	iv.Bump()
	if iv.Load() != 2 {
		t.Fatalf("want 2 after two bumps, got %d", iv.Load())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestTreeCache|TestInputsVersion' -v`
Expected: FAIL — types undefined.

- [ ] **Step 3: Implement the cache and version**

Create `cmd/serf-hub/internal/hubcore/treecache.go`:

```go
package hubcore

import (
	"sync"
	"sync/atomic"
	"time"
)

// treeBucketSeconds is the wall-clock granularity at which a fresh tree is
// recomputed even without an inputs change, so relative ages and 24h/2wk tier
// boundaries advance. The memoized tree carries Age strings up to one bucket
// stale (round-2 A6); the web renders ages client-side and ignores them.
const treeBucketSeconds = 30

// InputsVersion is a monotonically increasing counter bumped whenever an input
// to the tree changes (past-index content delta, roster membership/state delta,
// archive/favorite writes, attention poke). The TreeCache keys on it.
type InputsVersion struct{ v atomic.Uint64 }

func (iv *InputsVersion) Bump()        { iv.v.Add(1) }
func (iv *InputsVersion) Load() uint64 { return iv.v.Load() }

// TreeCache memoizes one (Tree, AttentionSummary) per (inputs-version, 30s time
// bucket). Response shaping and volatile derivations happen post-memo.
type TreeCache struct {
	mu      sync.Mutex
	valid   bool
	version uint64
	bucket  int64
	tree    Tree
	summary AttentionSummary
}

// Get returns the memoized value, recomputing via compute only when the inputs
// version or the 30s time bucket has changed.
func (c *TreeCache) Get(version uint64, now time.Time, compute func() (Tree, AttentionSummary)) (Tree, AttentionSummary) {
	bucket := now.Unix() / treeBucketSeconds
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && c.version == version && c.bucket == bucket {
		return c.tree, c.summary
	}
	tree, sum := compute()
	c.tree, c.summary, c.version, c.bucket, c.valid = tree, sum, version, bucket, true
	return tree, sum
}
```

- [ ] **Step 4: Wire it into `WebConfig`/`WebServer`/`handleAPITree`**

In `internal/hubcore/config.go` `WebConfig`, add:

```go
	Inputs *InputsVersion // shared inputs-version counter; nil in tests (memo treats as version 0)
```

In `cmd/serf-hub/web.go` `WebServer` struct, add a field `treeCache *hubcore.TreeCache`, and in `NewWebServer` set `web.treeCache = &hubcore.TreeCache{}`.

In `handleAPITree`, replace the direct `tree := hubcore.BuildTree(...)` + `_, attentionSummary := hubcore.DeriveAttention(...)` with a memoized compute:

```go
	metas, live := s.navigationTreeInputs(r.Context())
	decisions := s.archiveDecisions()
	var version uint64
	if s.cfg.Inputs != nil {
		version = s.cfg.Inputs.Load()
	}
	tree, attentionSummary := s.treeCache.Get(version, time.Now(), func() (hubcore.Tree, hubcore.AttentionSummary) {
		t := hubcore.BuildTree(metas, live, decisions)
		_, sum := hubcore.DeriveAttention(metas, live, decisions)
		return t, sum
	})
```

In `cmd/serf-hub/main.go`, create the shared counter and pass it:

```go
	inputs := &hubcore.InputsVersion{}
```

add `Inputs: inputs,` to the `WebConfig` literal, and change `pokeAttention` to also bump inputs:

```go
	pokeAttention := func() {
		inputs.Bump()
		select {
		case attentionPoke <- struct{}{}:
		default:
		}
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... ./cmd/serf-hub/internal/hubcore/ -run 'TestTreeCache|TestInputsVersion|TestAPITree' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/treecache.go cmd/serf-hub/internal/hubcore/treecache_test.go cmd/serf-hub/internal/hubcore/config.go cmd/serf-hub/web.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/main.go
git commit -m "feat(hub): memoize the tree by inputs-version + 30s time bucket"
```

---

## Task 10: Inputs-version hooks (content-delta-gated)

Bump the inputs version on real input changes only: a `PastIndex` rebuild that observes an actual content delta (a `Find`-miss that rebuilds without new content must NOT bump — round-3 G5), a roster membership/state delta, and archive/favorite writes. Poke already bumps (Task 9).

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/past.go`, `roster.go`, `archive.go`, `favorite.go` (add `SetOnChange`)
- Modify: `cmd/serf-hub/main.go` (wire the hooks)
- Test: `cmd/serf-hub/internal/hubcore/past_test.go`, `roster_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/internal/hubcore/past_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestPastIndexOnChangeFiresOnContentDeltaOnly -v`
Expected: FAIL — `SetOnChange` undefined.

- [ ] **Step 3: Add content-delta gating to `PastIndex`**

In `past.go`, add fields to the struct: `onChange func()` and `fingerprint uint64`. Add:

```go
// SetOnChange registers a callback fired by Rebuild/UpdateMeta only when the
// indexed content actually changes (a Find-miss rebuild with no delta does not
// fire — round-3 G5). Nil disables the hook.
func (i *PastIndex) SetOnChange(fn func()) { i.onChange = fn }

// contentFingerprint hashes the (id, UpdatedAt) pairs of the sorted entries so
// Rebuild can detect a genuine content delta without a deep compare.
func contentFingerprint(all []PastEntry) uint64 {
	h := fnv.New64a()
	for _, e := range all {
		_, _ = h.Write([]byte(e.ID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.Meta.Name))
		_, _ = h.Write([]byte{0})
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(e.Meta.UpdatedAt.UnixNano()))
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}
```

Add `"hash/fnv"` and `"encoding/binary"` to `past.go` imports. At the end of `Rebuild`, after the `i.mu.Unlock()` that publishes the new `all`, compute and compare the fingerprint and fire:

```go
	fp := contentFingerprint(all)
	i.mu.Lock()
	changed := fp != i.fingerprint
	i.fingerprint = fp
	i.mu.Unlock()
	if changed && i.onChange != nil {
		i.onChange()
	}
```

(Place this after the FTS rebuild block so a fired hook reflects the published state. `Name` is included in the fingerprint so a rename via UpdateMeta — Task 12 — is a genuine delta.)

- [ ] **Step 4: Add `SetOnChange` to roster + stores**

In `roster.go`, add `onChange func()` and `fingerprint uint64` fields, plus:

```go
// SetOnChange registers a callback fired by Refresh only when the live set's
// membership or per-session status actually changes. Nil disables the hook.
func (r *Roster) SetOnChange(fn func()) { r.onChange = fn }

func rosterFingerprint(bySess map[string]LiveEntry) uint64 {
	ids := make([]string, 0, len(bySess))
	for id := range bySess {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := fnv.New64a()
	for _, id := range ids {
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(bySess[id].Status))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}
```

Add `"hash/fnv"` to `roster.go` imports. At the end of `Refresh`, after `r.mu.Unlock()`:

```go
	fp := rosterFingerprint(bySess)
	r.mu.Lock()
	changed := fp != r.fingerprint
	r.fingerprint = fp
	r.mu.Unlock()
	if changed && r.onChange != nil {
		r.onChange()
	}
```

In `archive.go` and `favorite.go`, add an `onChange func()` field + `SetOnChange` setter, and call `s.fireChange()` at the end of `Set` and `Delete` (both stores):

```go
func (s *ArchiveStore) SetOnChange(fn func()) { s.onChange = fn }
func (s *ArchiveStore) fireChange()           { if s.onChange != nil { s.onChange() } }
```

(and the analogous pair on `FavoriteStore`). Call `s.fireChange()` before returning `nil` from `Set`/`Delete`.

- [ ] **Step 5: Wire the hooks in `main.go`**

After constructing `past`, `roster`, `archive`, `favorite`, `inputs`:

```go
	bump := inputs.Bump
	past.SetOnChange(bump)
	roster.SetOnChange(bump)
	archive.SetOnChange(bump)
	favorite.SetOnChange(bump)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestPastIndexOnChange|TestRoster|TestArchive|TestFavorite' -v && go build ./cmd/serf-hub/...`
Expected: PASS + build OK.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/past.go cmd/serf-hub/internal/hubcore/roster.go cmd/serf-hub/internal/hubcore/archive.go cmd/serf-hub/internal/hubcore/favorite.go cmd/serf-hub/internal/hubcore/past_test.go cmd/serf-hub/main.go
git commit -m "feat(hub): content-delta-gated inputs-version hooks on index/roster/stores"
```

---

## Task 11: `/api/tree/project` served from the memo

Replace the dying `/_partials/sidebar/project` with `GET /api/tree/project?key=<slug>`, served by indexing the memoized full tree (never a fresh full-meta scan — round-2 A4). The client resync re-requests only projects it has expanded.

**Files:**
- Modify: `cmd/serf-hub/web.go` (route), `cmd/serf-hub/web_api_tree.go` (handler)
- Test: `cmd/serf-hub/web_api_tree_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestAPITreeProjectServedFromTree(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01A", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/proj"}},
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	web.injectMetasForTest(metas)
	key := hubcore.ProjectSlug("/w/proj")
	req := httptest.NewRequest(http.MethodGet, "/api/tree/project?key="+key, nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var p hubapi.TreeProject
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Key != key || len(p.Sessions) != 1 {
		t.Fatalf("want the single project with its session, got %+v", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/ -run TestAPITreeProjectServedFromTree -v`
Expected: FAIL — 404 (no route).

- [ ] **Step 3: Add the handler + route**

In `web_api_tree.go`, factor the memoized-tree read into a helper and add the project handler:

```go
func (s *WebServer) memoTree(ctx context.Context) (hubcore.Tree, hubcore.AttentionSummary) {
	metas, live := s.navigationTreeInputs(ctx)
	decisions := s.archiveDecisions()
	var version uint64
	if s.cfg.Inputs != nil {
		version = s.cfg.Inputs.Load()
	}
	return s.treeCache.Get(version, time.Now(), func() (hubcore.Tree, hubcore.AttentionSummary) {
		t := hubcore.BuildTree(metas, live, decisions)
		_, sum := hubcore.DeriveAttention(metas, live, decisions)
		return t, sum
	})
}

func (s *WebServer) handleAPITreeProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "key is required")
		return
	}
	tree, _ := s.memoTree(r.Context())
	for _, p := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		if p.Key == key {
			writeAPIJSON(w, http.StatusOK, s.apiTreeProject("project", p))
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, "project not found")
}
```

Refactor `handleAPITree` to call `s.memoTree(r.Context())` instead of the inline compute (single source). Register the route in `web.go` `Handler()` — before the `/api/tree` route so the longer path matches (Go's `ServeMux` prefers the exact longer pattern, but register explicitly):

```go
	mux.HandleFunc("/api/tree/project", s.handleAPITreeProject)
	mux.HandleFunc("/api/tree", s.handleAPITree)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/ -run 'TestAPITreeProject|TestAPITree' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/web.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_tree_test.go
git commit -m "feat(hub): serve /api/tree/project by indexing the memoized tree"
```

---

## Task 12: `PastIndex.UpdateMeta` (targeted upsert + FTS re-rank)

Add a targeted meta upsert for rename: re-insert the entry at its new sorted position (the title is a sort-key component — session_order.go:30-33) and rewrite the FTS rows so search order stays correct (round-3 H3), without a synchronous disk `Rebuild()`. Fires the content-delta hook.

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/past.go`
- Test: `cmd/serf-hub/internal/hubcore/past_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestUpdateMetaReordersAndPreservesStateDir -v`
Expected: FAIL — `UpdateMeta` undefined.

- [ ] **Step 3: Implement `UpdateMeta`**

In `past.go`:

```go
// UpdateMeta targets one existing entry: it replaces the meta, re-inserts the
// entry at its new sorted position (the title is a sort-key component), and
// re-numbers the FTS rows so search ordering stays correct — without a
// synchronous disk Rebuild the rename response path cannot afford (round-2
// A5/B1, round-3 H3). StateDir is preserved (rename never moves the files).
// No-op when the id is not already indexed.
func (i *PastIndex) UpdateMeta(id string, meta schema.SessionMeta) {
	i.mu.Lock()
	old, ok := i.byID[id]
	if !ok {
		i.mu.Unlock()
		return
	}
	pe := PastEntry{ID: id, Meta: meta, StateDir: old.StateDir}
	i.byID[id] = pe
	for idx := range i.all {
		if i.all[idx].ID == id {
			i.all = append(i.all[:idx], i.all[idx+1:]...)
			break
		}
	}
	pos := sort.Search(len(i.all), func(k int) bool { return !sessionMetaLess(i.all[k].Meta, meta) })
	i.all = append(i.all, PastEntry{})
	copy(i.all[pos+1:], i.all[pos:])
	i.all[pos] = pe
	all := append([]PastEntry(nil), i.all...)
	i.mu.Unlock()

	if i.dbPath != "" {
		if err := i.rebuildFTS(all); err == nil {
			i.mu.Lock()
			i.fts = true
			i.mu.Unlock()
		}
	}
	fp := contentFingerprint(all)
	i.mu.Lock()
	changed := fp != i.fingerprint
	i.fingerprint = fp
	i.mu.Unlock()
	if changed && i.onChange != nil {
		i.onChange()
	}
}
```

(`rebuildFTS(all)` replaces every FTS row and re-numbers `sort_rank` from the re-sorted slice — a superset of "the affected range" and simpler than a partial patch; rename is a rare user action so the cost is acceptable.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestUpdateMeta|TestPastIndex' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/past.go cmd/serf-hub/internal/hubcore/past_test.go
git commit -m "feat(hub): PastIndex.UpdateMeta targeted upsert with FTS re-rank"
```

---

## Task 13: Remote-source async cache

The Codex remote source costs a synchronous network hop per render (`remoteTreeThreads`, web_api_tree.go:132-155). Move it behind an async cache refreshed on a ~30s ticker and on poke; `remoteTreeThreads` reads the cache.

**Files:**
- Create: `cmd/serf-hub/internal/hubcore/remotecache.go`
- Modify: `cmd/serf-hub/web_api_tree.go` (read cache), `cmd/serf-hub/main.go` (start refresher)
- Test: `cmd/serf-hub/internal/hubcore/remotecache_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/internal/hubcore/remotecache_test.go`:

```go
package hubcore

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestRemoteThreadCacheReadReturnsLastStored(t *testing.T) {
	c := &RemoteThreadCache{}
	if got := c.Get(); got != nil {
		t.Fatalf("empty cache should return nil, got %v", got)
	}
	threads := []appwire.Thread{{ID: "t1"}, {ID: "t2"}}
	c.Store(threads)
	got := c.Get()
	if len(got) != 2 || got[0].ID != "t1" {
		t.Fatalf("cache should return stored threads, got %+v", got)
	}
	// Get returns a copy — mutating it must not corrupt the cache.
	got[0].ID = "mutated"
	if c.Get()[0].ID != "t1" {
		t.Fatal("Get must return a defensive copy")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run TestRemoteThreadCacheReadReturnsLastStored -v`
Expected: FAIL — type undefined.

- [ ] **Step 3: Implement the cache**

Create `cmd/serf-hub/internal/hubcore/remotecache.go`:

```go
package hubcore

import (
	"sync"

	"primeradiant.com/serf/appwire"
)

// RemoteThreadCache holds the most recent remote-source thread list so a tree
// render never blocks on a network hop. A background refresher (main.go) calls
// Store on a ~30s ticker and on poke; the tree read path calls Get.
type RemoteThreadCache struct {
	mu      sync.RWMutex
	threads []appwire.Thread
}

func (c *RemoteThreadCache) Store(threads []appwire.Thread) {
	c.mu.Lock()
	c.threads = append([]appwire.Thread(nil), threads...)
	c.mu.Unlock()
}

func (c *RemoteThreadCache) Get() []appwire.Thread {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.threads == nil {
		return nil
	}
	return append([]appwire.Thread(nil), c.threads...)
}
```

- [ ] **Step 4: Read the cache in the tree path + start the refresher**

Add `RemoteThreadCache *hubcore.RemoteThreadCache` to `WebConfig`. In `remoteTreeThreads`, when the cache is configured, return `s.cfg.RemoteThreadCache.Get()` instead of doing the synchronous `s.sources.All()` walk; keep a `refreshRemoteThreads(ctx)` method that performs the old synchronous walk and calls `Store`. In `main.go`, construct `remoteCache := &hubcore.RemoteThreadCache{}`, pass it in `WebConfig`, and start a goroutine:

```go
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		refresh := func() {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			remoteCache.Store(web.refreshRemoteThreads(ctx))
		}
		refresh()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				refresh()
			case <-attentionPoke:
				refresh()
			}
		}
	}()
```

Note: the attention watcher already drains `attentionPoke`; to share the poke, add a second buffered channel `remotePoke` bumped alongside in `pokeAttention`, or have `pokeAttention` fan out. Simplest: add a dedicated `remotePoke := make(chan struct{}, 1)` and have `pokeAttention` non-blocking-send to both. Wire the refresher's `case <-remotePoke`.

When `RemoteThreadCache` is nil (tests), `remoteTreeThreads` falls back to the old synchronous behavior, so existing tests are unaffected.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... ./cmd/serf-hub/internal/hubcore/ -run 'TestRemoteThreadCache|TestAPITree' -v && go build ./cmd/serf-hub/...`
Expected: PASS + build OK.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/remotecache.go cmd/serf-hub/internal/hubcore/remotecache_test.go cmd/serf-hub/internal/hubcore/config.go cmd/serf-hub/web_api_tree.go cmd/serf-hub/main.go
git commit -m "feat(hub): async remote-source thread cache (~30s + poke)"
```

---

## Task 14: Project delete endpoint

`POST /api/project/delete {key, workingDir}` removes a project's session files and scrubs its decision rows. Resolution is from `PastIndex.All()` (carries `StateDir`; `AllMetas()` cannot — round-2 A1) filtered to the project's `EffectiveWorkingDir`. The body carries the exact working dir and the server validates it against the key's current tree entry (round-2 A11 — never invert a lossy slug on a destructive path). Per session: a probe-resolved `Roster.Find` re-check (round-2 A9), then remove `sessions/<id>.{meta.json,transcript.jsonl,log.jsonl}` and `sessions/<id>/`; scrub archive+favorite session rows. After the loop: scrub the `("project", path)` rows and the legacy `("project", basename)` rows when unambiguous (round-3 G3), rebuild, poke. `worktrees/` is never touched.

**Files:**
- Create: `cmd/serf-hub/web_api_project_delete.go`
- Modify: `cmd/serf-hub/web.go` (route)
- Test: `cmd/serf-hub/web_api_project_delete_test.go` (create)

- [ ] **Step 1: Write the failing test (resolution + removal + scrub)**

Create `cmd/serf-hub/web_api_project_delete_test.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func writeSession(t *testing.T, stateDir, id, wd string) {
	t.Helper()
	m := schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: wd}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(stateDir, "sessions")
	for _, suffix := range []string{".transcript.jsonl", ".log.jsonl"} {
		if err := os.WriteFile(filepath.Join(sess, id+suffix), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sess, id), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestProjectDeleteRemovesFilesAndScrubs(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "sha1")
	writeSession(t, stateDir, "01A", "/w/proj")
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	_ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	_ = archive.Set("session", "01A", true, time.Unix(1_700_000_000, 0))
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	body := `{"key":"` + hubcore.ProjectSlug("/w/proj") + `","workingDir":"/w/proj"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Deleted []string `json:"deleted"`
		Skipped []struct{ ID, Reason string } `json:"skipped"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Deleted) != 1 {
		t.Fatalf("want 1 deleted ref, got %+v", resp)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", "01A"+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed", suffix)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", "01A")); !os.IsNotExist(err) {
		t.Fatal("per-session dir should be removed")
	}
	d, _ := archive.Decisions()
	if _, present := d[hubcore.ArchiveKey{Kind: "session", ID: "01A"}]; present {
		t.Fatalf("session archive row should be scrubbed: %v", d)
	}
}

func TestProjectDeleteRejectsKeyWorkingDirMismatch(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "sha1")
	writeSession(t, stateDir, "01A", "/w/proj")
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	_ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
	body := `{"key":"` + hubcore.ProjectSlug("/w/proj") + `","workingDir":"/w/WRONG"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatch must be rejected 400, got %d", rec.Code)
	}
}

func TestProjectDeleteRefusesWhenLive(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "sha1")
	writeSession(t, stateDir, "01A", "/w/proj")
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	_ = past.Rebuild()
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: "01A", Status: "active"})
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster})
	body := `{"key":"` + hubcore.ProjectSlug("/w/proj") + `","workingDir":"/w/proj"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("live project delete must 409, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", "01A.meta.json")); os.IsNotExist(err) {
		t.Fatal("nothing should be removed when refused")
	}
}
```

Add a `newBody` helper (`strings.NewReader`) to the test file, or use `strings.NewReader` inline (import `strings`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/ -run TestProjectDelete -v`
Expected: FAIL — 404 (no route).

- [ ] **Step 3: Implement the handler**

Create `cmd/serf-hub/web_api_project_delete.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

type projectDeleteSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// handleAPIProjectDelete removes every session file under a project and scrubs
// its decision rows. Path-validated; refuses when anything is live at entry.
func (s *WebServer) handleAPIProjectDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Key        string `json:"key"`
		WorkingDir string `json:"workingDir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Key == "" || body.WorkingDir == "" {
		writeAPIError(w, http.StatusBadRequest, "key and workingDir are required")
		return
	}
	if s.cfg.Past == nil {
		writeAPIError(w, http.StatusInternalServerError, "past index not configured")
		return
	}
	// Validate the body against the current tree entry for that key — never
	// invert the lossy slug on a destructive path (round-2 A11).
	tree, _ := s.memoTree(r.Context())
	var matched *hubcore.TreeProject
	for _, p := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		if p.Key == body.Key {
			pp := p
			matched = &pp
			break
		}
	}
	if matched == nil || matched.WorkingDir != body.WorkingDir {
		writeAPIError(w, http.StatusBadRequest, "key does not match workingDir")
		return
	}

	// Resolve the session set from All() (carries StateDir), uncapped.
	var entries []hubcore.PastEntry
	for _, e := range s.cfg.Past.All() {
		if hubcore.EffectiveWorkingDir(e.Meta) == body.WorkingDir {
			entries = append(entries, e)
		}
	}

	// Whole-project fast path: refuse when anything is live at entry.
	if s.cfg.Roster != nil {
		var liveNames []string
		for _, e := range entries {
			if _, ok := s.cfg.Roster.Find(e.ID); ok {
				liveNames = append(liveNames, hubcore.ShortID(e.ID))
			}
		}
		if len(liveNames) > 0 {
			writeAPIJSON(w, http.StatusConflict, map[string]any{"error": "project has live sessions", "live": liveNames})
			return
		}
	}

	deleted := []string{}
	skipped := []projectDeleteSkip{}
	for _, e := range entries {
		// TOCTOU re-check via the probe-resolved Roster.Find (round-2 A9): a
		// genuine resume between entry and removal aborts this session.
		if s.cfg.Roster != nil {
			if _, ok := s.cfg.Roster.Find(e.ID); ok {
				skipped = append(skipped, projectDeleteSkip{ID: e.ID, Reason: "resumed live"})
				continue
			}
		}
		sess := filepath.Join(e.StateDir, "sessions")
		for _, p := range []string{
			filepath.Join(sess, e.ID+".meta.json"),
			filepath.Join(sess, e.ID+".transcript.jsonl"),
			filepath.Join(sess, e.ID+".log.jsonl"),
		} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				skipped = append(skipped, projectDeleteSkip{ID: e.ID, Reason: err.Error()})
			}
		}
		_ = os.RemoveAll(filepath.Join(sess, e.ID))
		if s.cfg.Archive != nil {
			_ = s.cfg.Archive.Delete("session", e.ID)
		}
		if s.cfg.Favorite != nil {
			_ = s.cfg.Favorite.Delete("session", e.ID)
		}
		deleted = append(deleted, e.ID)
	}

	// Scrub the project-level decision rows: the path row always, and the
	// legacy basename row only when no other project still uses that basename
	// (round-3 G3 — otherwise the legacy row re-hides a recreated project).
	basename := filepath.Base(body.WorkingDir)
	basenameStillUsed := false
	for _, e := range s.cfg.Past.All() {
		wd := hubcore.EffectiveWorkingDir(e.Meta)
		if wd != body.WorkingDir && filepath.Base(wd) == basename {
			basenameStillUsed = true
			break
		}
	}
	if s.cfg.Archive != nil {
		_ = s.cfg.Archive.Delete("project", body.WorkingDir)
		if !basenameStillUsed {
			_ = s.cfg.Archive.Delete("project", basename)
		}
	}
	if s.cfg.Favorite != nil {
		_ = s.cfg.Favorite.Delete("project", body.WorkingDir)
		if !basenameStillUsed {
			_ = s.cfg.Favorite.Delete("project", basename)
		}
	}

	_ = s.cfg.Past.Rebuild() // also the FTS scrub
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "skipped": skipped})
}
```

Register in `web.go` `Handler()`:

```go
	mux.HandleFunc("/api/project/delete", s.handleAPIProjectDelete)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/ -run TestProjectDelete -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/web_api_project_delete.go cmd/serf-hub/web_api_project_delete_test.go cmd/serf-hub/web.go
git commit -m "feat(hub): project delete endpoint (path-validated, live-refusing, scrubbing)"
```

---

## Task 15: `Origin` on `SessionMeta` + `SERF_SESSION_ORIGIN` plumb + TestRuns classification

Add `Origin string` to `schema.SessionMeta`, fed from `SERF_SESSION_ORIGIN` through the shared `agent.NewSession` path (both `serf serve` and one-shot `serf run`), preserved on resume; then classify a project into "Test runs" when it has ≥1 session and *every* session carries `Origin=="test"` (TestRuns precedence over ArchivedProjects — round-2 B6). NOTE: this coordinates with WS2's `SessionMeta` fields — regenerate `goldenMetaJSON` from live marshal output, do not hand-edit assuming other WS2 keys landed.

**Files:**
- Modify: `agent/schema/snapshot.go` (`Origin`), `agent/session.go` (`origin` field), `agent/session_state.go` (`Meta` stamp), `agent/session_init.go` (set from env + restore from meta), `envvars/envvars.go` (`SERFSessionOrigin`), `docs/environment.md`
- Modify: `cmd/serf-hub/internal/hubcore/tree.go` (`IsTestRun`), `cmd/serf-hub/web_api_tree.go` (TestRuns routing)
- Test: `agent/snapshot_golden_test.go` (regenerate), `agent/session_origin_test.go` (create), `cmd/serf-hub/internal/hubcore/tree_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Create `agent/session_origin_test.go` (drive `NewSession` through env and assert `Meta().Origin`):

```go
package agent_test

import (
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/envvars"
)

func TestSessionOriginFromEnv(t *testing.T) {
	t.Setenv(envvars.SERFSessionOrigin.Name, "test")
	sess := newTestSession(t) // existing agent_test helper that calls agent.NewSession
	defer sess.Close()
	if got := sess.Meta().Origin; got != "test" {
		t.Fatalf("Origin should come from SERF_SESSION_ORIGIN, got %q", got)
	}
}
```

(Use whatever session-construction helper the agent test package already provides; grep `agent/*_test.go` for an existing `NewSession` harness and reuse it — do not introduce a new construction path.)

Append to `cmd/serf-hub/internal/hubcore/tree_test.go`:

```go
func TestAllTestSessionsClassifyAsTestRun(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, origin string) schema.SessionMeta {
		return schema.SessionMeta{ID: id, Origin: origin, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/tp"}}
	}
	tree := BuildTreeAt([]schema.SessionMeta{mk("01A", "test"), mk("01B", "test")}, nil, map[ArchiveKey]bool{}, now)
	var testRun *TreeProject
	for i := range tree.Projects {
		if tree.Projects[i].IsTestRun {
			testRun = &tree.Projects[i]
		}
	}
	if testRun == nil {
		t.Fatalf("all-test project should be flagged IsTestRun; projects=%+v", tree.Projects)
	}
	// One unmarked session reclassifies the project (hiding real work is worse).
	tree = BuildTreeAt([]schema.SessionMeta{mk("01A", "test"), mk("01B", "")}, nil, map[ArchiveKey]bool{}, now)
	for _, p := range tree.Projects {
		if p.IsTestRun {
			t.Fatalf("a mixed project must not be IsTestRun")
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test . -run TestSessionOriginFromEnv -v` and `go test ./cmd/serf-hub/internal/hubcore/ -run TestAllTestSessionsClassifyAsTestRun -v`
Expected: FAIL — `Origin`, `SERFSessionOrigin`, `IsTestRun` undefined.

- [ ] **Step 3: Add `Origin` to the schema + `Session`**

In `agent/schema/snapshot.go`, add after the `IsSubagent` field:

```go
	// Origin marks how the session was launched: "test" for agentic-testing
	// runs (set via SERF_SESSION_ORIGIN), empty for normal sessions. The hub
	// classifies an all-"test" project into the "Test runs" group.
	Origin string `json:"origin,omitempty"`
```

In `agent/session.go`, add an `origin string` field to the `Session` struct (near other init-time fields).

In `agent/session_init.go`, in the fresh-create path (`initSessionState`, called from `NewSession`), set:

```go
	s.origin = envvars.SERFSessionOrigin.Getenv()
```

and in the restore-from-meta path (where naming is seeded from `meta` at session_init.go:366-371), add:

```go
	s.origin = meta.Origin
```

(so resume preserves the persisted origin). Add `"primeradiant.com/serf/envvars"` to `session_init.go` imports if absent.

In `agent/session_state.go`, in the `Meta()` return literal, add:

```go
		Origin:              s.origin,
```

- [ ] **Step 4: Declare the env var**

In `envvars/envvars.go`, add to the `var (...)` block (near `SERFStateDir`):

```go
	SERFSessionOrigin = Var{Name: "SERF_SESSION_ORIGIN", Summary: "Marks a session's launch origin (e.g. \"test\" for agentic-testing runs).", Visibility: Public}
```

and add `SERFSessionOrigin,` to the `allVars` slice (else it is invisible to `All()`/`Find()` and the audit). Add a row to `docs/environment.md` under `## Serf Commands`:

```
| `SERF_SESSION_ORIGIN` | Marks a session's launch origin (e.g. `test`) so the hub groups agentic-test runs. |
```

- [ ] **Step 5: Regenerate the golden**

`goldenMetaJSON` (agent/snapshot_golden_test.go:107) is byte-frozen. With `Origin` unset in `goldenMeta()` it stays absent (`omitempty`) and the golden is unchanged. To exercise it, add `Origin: "test"` to `goldenMeta()` and regenerate the const from live output:

Run: `cd agent && go test . -run TestSessionMeta_GoldenWireFormat -v`
If it fails, copy the `got:` bytes into `goldenMetaJSON` (do NOT hand-edit — the `origin` key position follows struct field order, and other WS2 keys may or may not be present). Re-run until PASS.

- [ ] **Step 6: Add `IsTestRun` classification + TestRuns routing**

In `internal/hubcore/tree.go`, add `IsTestRun bool` to `TreeProject`. In the `projectAccum`, track `allTest bool` and `count int`; in the per-meta loop set:

```go
		acc.count++
		if m.Origin != "test" {
			acc.anyNonTest = true
		}
```

(add `count int` and `anyNonTest bool` to `projectAccum`). In the `TreeProject{...}` literal:

```go
			IsTestRun: acc.count > 0 && !acc.anyNonTest,
```

In `web_api_tree.go` `handleAPITree`, route test-run projects into `resp.TestRuns` (precedence over Archived): when building `resp.Projects`/`resp.ArchivedProjects`, first check `IsTestRun`:

```go
	for _, p := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		if p.IsTestRun {
			resp.TestRuns = append(resp.TestRuns, s.apiTreeProject("project", p))
			continue
		}
		if p.IsArchived {
			resp.ArchivedProjects = append(resp.ArchivedProjects, s.apiTreeProject("project", p))
			continue
		}
		projectIndexes[p.Key] = len(resp.Projects)
		ap := s.apiTreeProject("project", p)
		for _, n := range ap.Sessions {
			seenProjectRefs[n.SessionID] = true
		}
		resp.Projects = append(resp.Projects, ap)
	}
```

(Replace the two separate `tree.Projects`/`tree.ArchivedProjects` loops from Task 4 with this single loop over both; `IsArchived` is already true for archived-list entries.)

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd agent && go test . -run 'TestSessionOrigin|TestSessionMeta_Golden' -v` and `go test ./cmd/serf-hub/... ./cmd/serf-hub/internal/hubcore/ -run 'TestAllTestSessions|TestAPITree' -v` and `cd envvars && go test ./...` and `go test ./ -run TestSupportedEnvVars` (root audit).
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add agent/schema/snapshot.go agent/session.go agent/session_init.go agent/session_state.go agent/session_origin_test.go agent/snapshot_golden_test.go envvars/envvars.go docs/environment.md cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go cmd/serf-hub/web_api_tree.go
git commit -m "feat: SessionMeta.Origin via SERF_SESSION_ORIGIN + TestRuns classification"
```

---

## Task 16: Rename — agent core (`Session.Rename` + namer suppression)

Add a `Session.Rename(name)` that sets `Name`/`NameSource="user"`/`NameUpdatedAt` and persists, updating in-memory `s.naming` under `s.mu` so the auto-namer can't clobber it (round-2 A8). The suppression falls out for free: `shouldNameFromCompaction` (session_namer.go:294-302) and `shouldApplySessionNameLocked` (:360-374) already reject a source that is neither `"prompt"` nor `"compaction"`, and the prompt-namer launch gate (:197) bails once `s.naming.set` is true.

**Files:**
- Modify: `agent/session_namer.go` (`sessionNameSourceUser` constant), `agent/session.go` (`Rename` method)
- Test: `agent/session_rename_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `agent/session_rename_test.go`:

```go
package agent_test

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
)

func TestRenameSetsUserSourceAndSurvivesCompaction(t *testing.T) {
	sess := newTestSession(t) // reuse the agent_test harness
	defer sess.Close()
	sess.Rename("my chosen title")
	m := sess.Meta()
	if m.Name != "my chosen title" || m.NameSource != "user" {
		t.Fatalf("rename should set Name + NameSource=user, got %+v", m)
	}
	// A compaction-derived name must NOT overwrite a user rename.
	if sess.ShouldNameFromCompactionForTest() { // small test seam over shouldNameFromCompaction
		t.Fatal("user-named session must not accept a compaction name")
	}
}
```

If no `ShouldNameFromCompactionForTest` seam exists, assert via `Meta().Name` after driving a compaction turn through the existing harness; the key invariant is that `Name` stays `"my chosen title"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test . -run TestRenameSetsUserSourceAndSurvivesCompaction -v`
Expected: FAIL — `Rename` undefined.

- [ ] **Step 3: Add the source constant + `Rename` method**

In `agent/session_namer.go`, add next to `sessionNameSourcePrompt`/`sessionNameSourceCompaction`:

```go
	sessionNameSourceUser = "user"
```

In `agent/session.go`, add (modeled on `SetModel`, session.go:579-618 — mutate under `s.mu`, then `maybeAutoSave` which re-locks via `Meta()`):

```go
// Rename sets a user-chosen session title. It records NameSource="user" so the
// auto-namers (prompt + compaction) will never overwrite it — shouldApplySession
// NameLocked and shouldNameFromCompaction both reject any source that is not
// "prompt"/"compaction". Persists meta so the name survives a daemon crash.
func (s *Session) Rename(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return
	}
	s.naming.value = name
	s.naming.source = sessionNameSourceUser
	s.naming.updated = s.sclock().Now().UTC()
	s.naming.set = true
	s.mu.Unlock()
	// maybeAutoSave re-acquires s.mu via s.Meta(); must not hold the lock here.
	s.maybeAutoSave()
}
```

(`strings` is already imported in session.go.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test . -run TestRename -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_namer.go agent/session.go agent/session_rename_test.go
git commit -m "feat(agent): Session.Rename with user source + namer suppression"
```

---

## Task 17: Rename — the wire atom: appwire types+catalog, daemon handler, hub relay, docs (one commit)

This task lands the appwire method/params/capability/client, the catalog entry, the DAEMON router handler + wiring, and the HUB router relay + capability gate in a single commit, because the catalog↔router cross-checks are bidirectional — any subset leaves a red gate. Both cross-check tests must be GREEN at this task's commit.

**Files:**
- Modify: `appwire/types.go` (constant + params + `ThreadCapabilities.Rename`), `appwire/protocol.go` (catalog entry), `appwire/client.go` (`ThreadNameSet`)
- Modify: `server/server.go` (`nameFunc` + `SetNameFunc`), `server/appwire_runtime.go` (`handleAppThreadNameSet` + registration + `appCapabilities.Rename`), `cmd/serf/serve.go` (wire)
- Modify: `cmd/serf-hub/internal/appsource/source.go` (interface), `local_daemon.go` (impl + caps), `codex_source.go` (stub + caps)
- Modify: `cmd/serf-hub/app_rpc.go` (hub router), `app_compact.go` (`threadActionAvailable` "rename"), `app_threadread.go` (local caps projection)
- Modify (regenerated): `docs/appwire-protocol.md`
- Test: `appwire/protocol_test.go` is exercised by `make lint-generated`; add a targeted param test in `appwire/types_test.go` if present.

- [ ] **Step 1: Write the failing test**

Add to `appwire` a small round-trip test (or `cmd/serf-hub/appwire_catalog_test.go` will fail once Task 18 registers the hub route). Create `appwire/rename_test.go`:

```go
package appwire

import "testing"

func TestThreadNameSetInCatalog(t *testing.T) {
	found := false
	for _, m := range Methods {
		if m.Name == MethodSerfThreadNameSet {
			found = true
			if m.Scope != ScopeBoth {
				t.Fatalf("rename must be ScopeBoth, got %v", m.Scope)
			}
		}
	}
	if !found {
		t.Fatal("serf/thread/name/set missing from the catalog")
	}
	var caps ThreadCapabilities
	caps.Rename = true // must compile
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./appwire/ -run TestThreadNameSetInCatalog -v`
Expected: FAIL — `MethodSerfThreadNameSet`, `ThreadCapabilities.Rename` undefined.

- [ ] **Step 3: Add the constant, params, capability**

In `appwire/types.go`, add to the method const block (near `MethodSerfThreadTranscriptsList`):

```go
	MethodSerfThreadNameSet = "serf/thread/name/set"
```

Add the params type (near `ThreadModelSetParams`, types.go:576):

```go
// ThreadNameSetParams renames a thread (user-chosen title).
type ThreadNameSetParams struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}
```

Add to `ThreadCapabilities` (types.go:233-250), after `Goal`:

```go
	// Rename advertises support for serf/thread/name/set. True for a live serf
	// session (the daemon method) and for ended local sessions (the hub edits
	// meta); false for Codex-bridged threads.
	Rename bool `json:"rename"`
```

- [ ] **Step 4: Add the catalog entry + client method**

In `appwire/protocol.go` `var Methods`, add after the `MethodThreadModelSet` row:

```go
	{MethodSerfThreadNameSet, ThreadNameSetParams{}, EmptyResponse{}, ScopeBoth, "Sets a user-chosen session title (rename)."},
```

In `appwire/client.go`, add (modeled on `ThreadModelSet`, client.go:285-287):

```go
func (c *Client) ThreadNameSet(ctx context.Context, params ThreadNameSetParams) error {
	return c.request(ctx, MethodSerfThreadNameSet, params, nil)
}
```

- [ ] **Step 5: Regenerate the protocol doc**

Run: `make generate`
Expected: `docs/appwire-protocol.md` updated with the new method row and `ThreadNameSetParams`. Verify: `make lint-generated` passes.

- [ ] **Step 6: Add the daemon func pointer + handler**

In `server/server.go`, add a field `nameFunc func(string)` (near `modelFunc`) and a setter (modeled on `SetModelFunc`, server.go:404-408):

```go
// SetNameFunc sets the function called by the rename appwire method.
func (s *Server) SetNameFunc(fn func(string)) {
	s.mu.Lock()
	s.nameFunc = fn
	s.mu.Unlock()
}
```

In `server/appwire_runtime.go`, register in `registerAppWireHandlers` (alongside the others):

```go
	appserver.HandleTyped(router, appwire.MethodSerfThreadNameSet, s.handleAppThreadNameSet)
```

Add the handler (modeled on `handleAppThreadModelSet`, appwire_runtime.go:359-375):

```go
func (s *Server) handleAppThreadNameSet(_ context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("name is required")
	}
	s.mu.RLock()
	fn := s.nameFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("rename not available")
	}
	fn(name)
	return appwire.EmptyResponse{}, nil
}
```

In `appCapabilities` (appwire_runtime.go:548-571), add to the returned struct:

```go
		Rename: s.nameFunc != nil && !closed,
```

In `cmd/serf/serve.go`, wire the func alongside the other setters (serve.go:321-349):

```go
	srv.SetNameFunc(func(name string) { getSession().Rename(name) })
```

- [ ] **Step 7: Extend the `Source` interface + implementations**

In `cmd/serf-hub/internal/appsource/source.go`, add to the interface (after `SetThreadReasoningEffort`):

```go
	SetThreadName(context.Context, appwire.ThreadNameSetParams) error
```

In `local_daemon.go`, add (modeled on `SetThreadModel`, local_daemon.go:215-223):

```go
func (s *LocalDaemonSource) SetThreadName(ctx context.Context, params appwire.ThreadNameSetParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(client *appwire.Client) error {
		return client.ThreadNameSet(ctx, params)
	})
}
```

In `codex_source.go`, add the unsupported stub (modeled on its `SetThreadModel`, codex_source.go:350-352):

```go
func (s *CodexSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	return appwire.Unavailable("rename is not supported for codex threads")
}
```

In `local_daemon.go`'s capability projection (local_daemon.go:542-553) set `Rename` from the daemon's advertised caps; in `codex_source.go`'s projection leave `Rename` unset (defaults false).

- [ ] **Step 8: Add the hub router case + capability gate**

In `cmd/serf-hub/app_rpc.go`, register the hub router case (modeled on `MethodThreadModelSet`, app_rpc.go:494-503):

```go
	appserver.HandleTyped(server.Router(), appwire.MethodSerfThreadNameSet, func(ctx context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "rename"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.SetThreadName(ctx, params)
	})
```

In `cmd/serf-hub/app_compact.go` `threadActionAvailable` (app_compact.go:70-95), add:

```go
	case "rename":
		return caps.Rename
```

In `cmd/serf-hub/app_threadread.go` local past-session capability projection (app_threadread.go:157-161), set `Rename: true` (ended local sessions are renameable via the hub path).

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./appwire/ ./server/... -run 'ThreadNameSet|RouterMatchesCatalog' -v` and `go test ./cmd/serf-hub/ -run 'RouterMatchesCatalog' -v`
Expected: PASS, both cross-checks green.

- [ ] **Step 10: Commit**

```bash
git add appwire/types.go appwire/protocol.go appwire/client.go appwire/rename_test.go docs/appwire-protocol.md server/server.go server/appwire_runtime.go cmd/serf/serve.go cmd/serf-hub/internal/appsource/source.go cmd/serf-hub/internal/appsource/local_daemon.go cmd/serf-hub/internal/appsource/codex_source.go cmd/serf-hub/app_rpc.go cmd/serf-hub/app_compact.go cmd/serf-hub/app_threadread.go
git commit -m "feat(appwire,server,hub): serf/thread/name/set — types, catalog, daemon handler, hub relay in one atom"
```

---

## Task 18: Rename — hub REST endpoint + `TreeNode.Rename` resolution

Add the REST endpoint with live/ended paths (both apply `UpdateMeta` + inputs bump — round-3 G1), the route dispatch from `handleAPISession`, and the source-kind-derived `hubapi.TreeNode.Rename` resolution.

**Files:**
- Create: `cmd/serf-hub/web_api_rename.go` (REST endpoint)
- Modify: `cmd/serf-hub/web_api_tree.go` (`rowRenameable`) + `web.go`/`web_api_tree.go` route dispatch (`handleAPISession` "rename" sub-case)
- Test: `cmd/serf-hub/web_api_rename_test.go` (create)

- [ ] **Step 1: Write the failing test (ended path)**

Create `cmd/serf-hub/web_api_rename_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func TestRenameEndedSessionEditsMetaAndRefreshesIndex(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "p1")
	m := schema.SessionMeta{ID: "01A", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01A/rename", strings.NewReader(`{"name":"new title"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := past.Find("01A")
	if got.Meta.Name != "new title" || got.Meta.NameSource != "user" {
		t.Fatalf("ended rename must edit meta + refresh index, got %+v", got.Meta)
	}
	// The persisted file must also reflect the new name.
	on, _ := schema.LoadSessionMeta(stateDir, "01A")
	if on.Name != "new title" {
		t.Fatalf("meta file not updated: %+v", on)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/serf-hub/ -run TestRenameEndedSessionEditsMetaAndRefreshesIndex -v`
Expected: FAIL — no `/rename` sub-route.

- [ ] **Step 3: Add the REST endpoint (live + ended paths)**

Create `cmd/serf-hub/web_api_rename.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

// handleAPIRename renames a session. Live serf sessions route through the
// daemon method (daemon-truth); ended local sessions have their meta edited
// behind a probe-resolved Roster.Find re-check. Both paths refresh the past
// index (UpdateMeta) + bump inputs so the next resync reflects the new name
// (round-3 G1). Legacy daemons that 404 the method surface a toast client-side;
// the hub never falls back to editing a *live* session's meta file.
func (s *WebServer) handleAPIRename(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "name is required")
		return
	}
	ref := appRefFromRouteID(id)

	if s.isLive(id) {
		source, err := sourceForThread(s.sources, ref, "")
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "session not live")
			return
		}
		if err := source.SetThreadName(r.Context(), appwire.ThreadNameSetParams{Ref: ref, Name: name}); err != nil {
			writeAPIWireError(w, http.StatusBadGateway, err)
			return
		}
		s.refreshRenamedMeta(id, name)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Ended path: edit meta behind a pre-write Roster.Find re-check; if it turns
	// live, route through the daemon instead (round-2 A2 / round-3 G1).
	if s.cfg.Roster != nil {
		if _, live := s.cfg.Roster.Find(canonicalRouteID(id)); live {
			source, err := sourceForThread(s.sources, ref, "")
			if err == nil {
				if err := source.SetThreadName(r.Context(), appwire.ThreadNameSetParams{Ref: ref, Name: name}); err == nil {
					s.refreshRenamedMeta(id, name)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}
	}
	pe, ok := s.cfg.Past.Find(canonicalRouteID(id))
	if !ok {
		writeAPIError(w, http.StatusNotFound, "session not found")
		return
	}
	meta, err := schema.LoadSessionMeta(pe.StateDir, pe.ID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "load meta: "+err.Error())
		return
	}
	meta.Name = name
	meta.NameSource = "user"
	meta.NameUpdatedAt = time.Now().UTC()
	if err := schema.SaveSessionMeta(pe.StateDir, meta); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save meta: "+err.Error())
		return
	}
	s.cfg.Past.UpdateMeta(pe.ID, meta) // re-sort + FTS + inputs bump
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	w.WriteHeader(http.StatusNoContent)
}

// refreshRenamedMeta re-reads the persisted meta after a live rename and pushes
// it into the past index so the next tree resync shows the new name without a
// full Rebuild.
func (s *WebServer) refreshRenamedMeta(id, name string) {
	rid := canonicalRouteID(id)
	if pe, ok := s.cfg.Past.Find(rid); ok {
		if meta, err := schema.LoadSessionMeta(pe.StateDir, pe.ID); err == nil {
			s.cfg.Past.UpdateMeta(pe.ID, meta)
			if s.cfg.PokeAttention != nil {
				s.cfg.PokeAttention()
			}
			return
		}
		m := pe.Meta
		m.Name = name
		m.NameSource = "user"
		s.cfg.Past.UpdateMeta(pe.ID, m)
	}
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
}
```

Dispatch it from `handleAPISession` (web_api_tree.go:440-479 switch) by adding a case:

```go
	case "rename":
		s.handleAPIRename(w, r, routeID)
```

- [ ] **Step 4: Resolve `hubapi.TreeNode.Rename` by source kind**

Replace the temporary `rowRenameable` stub (Task 4) in `web_api_tree.go` with the source-kind-derived resolution (round-3 G2 — never a per-thread `ReadThread` in the tree build):

```go
// rowRenameable reports whether a tree row exposes the rename menu item. Local
// rows are always renameable (ended via the hub meta-edit path, live via the
// daemon method); Codex-bridged rows are not. Derived from the ref's host, not
// a per-thread probe.
func (s *WebServer) rowRenameable(id string) bool {
	return isLocalRouteID(id)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/serf-hub/... ./server/... -run 'TestRename|MatchesCatalog|TestAPITree' -v`
Expected: PASS (including the previously-red catalog cross-check tests, now that both routers register the method).

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/web_api_rename.go cmd/serf-hub/web_api_rename_test.go cmd/serf-hub/web_api_tree.go
git commit -m "feat(hub): rename REST endpoint (live/ended paths) + TreeNode.Rename resolution"
```

- [ ] **Step 7: Phase A gate**

Run: `golangci-lint run ./...` (root) && `cd agent && golangci-lint run ./... && cd .. && go test ./cmd/serf-hub/... ./hubapi/... ./appwire/... ./server/... && cd agent && go test ./... && cd ../envvars && go test ./...`
Expected: all green. Commit any lint fixups with `chore(hub): phase A lint`.

# Phase B — Client (vanilla JS)

The new `sidebar.js` holds `/api/tree` data as state and projects it to the DOM via keyed reconciliation on the server's `RowID`. It replaces the htmx partial entirely. The jstest harness is Node + JSDOM: each `test-*.js` reads the asset source with `fs.readFileSync`, evals it in a JSDOM window with `window.fetch`/`window.SerfAppwire` stubbed, and asserts, exiting 0/1 (see `cmd/serf-hub/jstest/run-all.sh`).

## Task 19: New sidebar renderer core — model, fetch, keyed reconcile, skeleton, active-row

Rewrite `cmd/serf-hub/assets/sidebar.js` around a client model (`{tree, expanded:Set, lazyCache:Map, seq, pending:Map}`), a `/api/tree` fetch, and hand-rolled reconciliation keyed on `RowID`. Type+key match patches in place, so DOM node identity (hover, scroll, open menus) survives updates. Rows stay real `<a href>` links with their htmx workspace-swap attributes; `htmx.process()` runs on created rows. Active-row marking and active-project auto-expand are driven off `htmx:afterSwap` on `#workspace`. First paint renders skeleton rows immediately.

**Files:**
- Rewrite: `cmd/serf-hub/assets/sidebar.js` (this task builds the core; Tasks 20-22 extend the same file)
- Modify: `cmd/serf-hub/templates/app.html` (client-rendered `#sidebar` container)
- Test: `cmd/serf-hub/jstest/test-sidebar-model.js`, `test-sidebar-reconcile.js` (create)

- [ ] **Step 1: Write the failing reconcile-identity test**

Create `cmd/serf-hub/jstest/test-sidebar-reconcile.js`:

```js
// Keyed reconcile: unchanged RowIDs keep DOM node identity across renders, and
// the same session in Needs-you + a project + Pinned yields three stable nodes.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(src);
  return w;
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
function node(rowId, sid, state) {
  return { row_id: rowId, ref: "local:" + sid, session_id: sid, title: sid, state: state, kind: "session", tier: "current", live: state !== "ended" };
}

const w = boot();
const tree = emptyTree();
const sid = "01A";
tree.needs_you = [node("needsyou::local:" + sid, sid, "awaiting")];
tree.favorites = [node("pinned::local:" + sid, sid, "awaiting")];
tree.projects = [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true, sessions: [node("project:p1:local:" + sid, sid, "awaiting")] }];

w.SerfSidebar.renderTree(tree);
const rows1 = w.document.querySelectorAll(".sb-row");
if (rows1.length !== 3) throw new Error("same session across 3 tiers must yield 3 rows, got " + rows1.length);
// Tag the project row; a second identical render must keep the SAME node.
const projRow = w.document.querySelector('[data-row-id="project:p1:local:' + sid + '"]');
projRow.__probe = true;
w.SerfSidebar.renderTree(tree);
const projRow2 = w.document.querySelector('[data-row-id="project:p1:local:' + sid + '"]');
if (!projRow2 || projRow2.__probe !== true) throw new Error("unchanged RowID must keep DOM node identity");
console.log("ok reconcile keeps identity + cross-tier duplicates");
```

Create `cmd/serf-hub/jstest/test-sidebar-model.js`:

```js
// First paint renders skeleton immediately; /api/tree resolves into rows;
// client-built rows carry the htmx workspace-swap attributes and get processed.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside><main id="workspace"></main></body></html>`, {
  runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/s/01A",
});
const w = dom.window;
let processed = 0;
w.htmx = { process() { processed++; } };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
const tree = { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true,
    sessions: [{ row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "hi", state: "idle", kind: "session", tier: "current", live: false }] }],
  attentionSummary: { needsYou: 0, error: 0, working: 0 } };
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree) });
w.eval(src);

// Skeleton painted synchronously on eval, before fetch resolves.
if (!w.document.querySelector("#sidebar .sb-skeleton")) throw new Error("skeleton must paint on first eval");

setTimeout(() => {
  const row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  if (!row) throw new Error("row not rendered after fetch");
  if (row.getAttribute("hx-get") !== "/_partials/s/01A/workspace") throw new Error("row missing hx-get workspace swap");
  if (row.getAttribute("href") !== "/s/01A") throw new Error("row missing href");
  if (row.getAttribute("hx-push-url") !== "/s/01A") throw new Error("row missing hx-push-url");
  if (processed < 1) throw new Error("htmx.process must run on created rows");
  if (!row.hasAttribute("data-active")) throw new Error("row matching /s/01A pathname must be active");
  console.log("ok model+skeleton+htmx.process+active-row");
  process.exit(0);
}, 20);
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-reconcile.js; node test-sidebar-model.js`
Expected: FAIL — the rewritten `sidebar.js` and `SerfSidebar.renderTree` do not exist yet (the current file has no such API).

- [ ] **Step 3: Rewrite `sidebar.js` core**

Replace `cmd/serf-hub/assets/sidebar.js` with the client-rendered renderer. Core structure (the whole file; Tasks 20-22 add to the marked sections):

```js
// Sidebar: client-rendered navigation tree. Data (/api/tree) is the state; the
// DOM is a keyed projection reconciled on the server's scope-qualified RowID, so
// unchanged rows keep node identity (hover, scroll, open menus) across updates.
(function () {
  "use strict";

  var EXPAND_PREFIX = "serf-hub.sidebar.expanded.";
  var model = { tree: null, expanded: new Set(), lazyCache: new Map(), seq: 0, pending: new Map() };
  window.SerfSidebarModel = model; // test/inspection surface

  function sidebarEl() { return document.getElementById("sidebar"); }

  // --- Skeleton first paint --------------------------------------------------
  function paintSkeleton() {
    var el = sidebarEl();
    if (!el || el.querySelector(".sb-tree")) return;
    var html = '<div class="sb-skeleton" aria-hidden="true">';
    for (var i = 0; i < 6; i++) html += '<div class="sb-skeleton-row"></div>';
    html += "</div>";
    el.innerHTML = html;
  }

  // --- Row + section builders ------------------------------------------------
  function rowKey(n) { return n.row_id; }

  function buildRow(n) {
    var a = document.createElement("a");
    a.className = "sb-row";
    a.setAttribute("data-row-id", n.row_id);
    a.setAttribute("data-state", n.state);
    a.setAttribute("data-ref", n.ref);
    a.setAttribute("href", "/s/" + n.session_id);
    a.setAttribute("hx-get", "/_partials/s/" + n.session_id + "/workspace");
    a.setAttribute("hx-target", "#workspace");
    a.setAttribute("hx-swap", "innerHTML");
    a.setAttribute("hx-push-url", "/s/" + n.session_id);
    a.innerHTML =
      '<div class="dot-col"><span class="status-dot" data-state="' + n.state + '"></span></div>' +
      '<div class="text-col"><div class="title"></div><div class="meta"></div></div>';
    a.querySelector(".title").textContent = n.title;
    var meta = a.querySelector(".meta");
    if (n.branch) meta.appendChild(metaSpan(n.branch));
    meta.appendChild(metaSpan(ageString(n.updated_at)));
    if (n.favorite) a.setAttribute("data-favorite", "");
    // Task 21 attaches the ⋯ menu button here.
    return a;
  }
  function metaSpan(text) { var s = document.createElement("span"); s.textContent = text; return s; }

  function patchRow(a, n) {
    if (a.getAttribute("data-state") !== n.state) {
      a.setAttribute("data-state", n.state);
      var dot = a.querySelector(".status-dot");
      if (dot) dot.setAttribute("data-state", n.state);
    }
    var title = a.querySelector(".title");
    if (title && title.textContent !== n.title) title.textContent = n.title;
    if (n.favorite) a.setAttribute("data-favorite", ""); else a.removeAttribute("data-favorite");
  }

  // --- Keyed reconcile -------------------------------------------------------
  // Patch children of `container` to match `nodes` (an array of TreeNodes),
  // keyed by row_id. Existing keys are patched in place and reordered; missing
  // keys are removed; new keys are created + htmx.process'd.
  function reconcile(container, nodes, build, patch) {
    var existing = {};
    var kids = container.children;
    for (var i = 0; i < kids.length; i++) {
      var k = kids[i].getAttribute("data-row-id");
      if (k) existing[k] = kids[i];
    }
    var seen = {};
    var prev = null;
    for (var j = 0; j < nodes.length; j++) {
      var n = nodes[j];
      var key = rowKey(n);
      seen[key] = true;
      var el = existing[key];
      if (el) { patch(el, n); } else { el = build(n); if (window.htmx && window.htmx.process) window.htmx.process(el); }
      // Place el right after prev (stable order without destroying nodes).
      var ref = prev ? prev.nextSibling : container.firstChild;
      if (el !== ref) container.insertBefore(el, ref);
      prev = el;
    }
    for (var m = container.children.length - 1; m >= 0; m--) {
      var ck = container.children[m].getAttribute("data-row-id");
      if (ck && !seen[ck]) container.removeChild(container.children[m]);
    }
  }

  // --- Full tree render ------------------------------------------------------
  function renderTree(tree) {
    model.tree = tree;
    var el = sidebarEl();
    if (!el) return;
    var root = el.querySelector(".sb-tree");
    if (!root) { el.innerHTML = '<nav class="sb-tree" aria-label="Sessions"></nav>'; root = el.querySelector(".sb-tree"); }
    var flat = flatten(applyPending(tree)); // applyPending is Task 20; identity until then
    reconcile(root, flat, buildRowOrSection, patchRowOrSection);
    syncActiveRow();
  }

  // flatten emits one keyed element descriptor per rendered row/section in
  // order: NeedsYou, Pinned, active projects, archived projects, test runs.
  // For the core task, render session rows grouped under project section
  // headers; expansion + lazy children are honored via model.expanded.
  function flatten(tree) {
    var out = [];
    (tree.needs_you || []).forEach(function (n) { out.push(n); });
    (tree.favorites || []).forEach(function (n) { out.push(n); });
    (tree.projects || []).forEach(function (p) { pushProject(out, p); });
    // archived_projects + test_runs handled by Task 21/22 section wrappers.
    return out;
  }
  function pushProject(out, p) {
    // Project header is itself a keyed element (data-row-id="header:<key>").
    out.push({ row_id: "header:" + p.key, __project: p });
    var expanded = model.expanded.has(p.key) || p.default_expanded;
    if (expanded) (p.sessions || []).forEach(function (n) { out.push(n); });
  }

  function buildRowOrSection(n) { return n.__project ? buildProjectHeader(n.__project) : buildRow(n); }
  function patchRowOrSection(el, n) { if (n.__project) patchProjectHeader(el, n.__project); else patchRow(el, n); }

  function buildProjectHeader(p) {
    var d = document.createElement("div");
    d.className = "project-header sb-row";
    d.setAttribute("data-row-id", "header:" + p.key);
    d.setAttribute("data-project-key", p.key);
    d.setAttribute("role", "button");
    d.setAttribute("aria-expanded", String(model.expanded.has(p.key) || p.default_expanded));
    d.innerHTML = '<span class="project-name"></span><span class="project-rollup"></span>';
    d.querySelector(".project-name").textContent = p.name;
    d.addEventListener("click", function () { toggleProject(p.key); });
    return d;
  }
  function patchProjectHeader(el, p) {
    el.setAttribute("aria-expanded", String(model.expanded.has(p.key) || p.default_expanded));
  }

  function toggleProject(key) {
    if (model.expanded.has(key)) model.expanded.delete(key); else model.expanded.add(key);
    persistExpanded(key);
    if (model.tree) renderTree(model.tree);
  }
  function persistExpanded(key) {
    try {
      if (model.expanded.has(key)) window.localStorage.setItem(EXPAND_PREFIX + key, "true");
      else window.localStorage.removeItem(EXPAND_PREFIX + key);
    } catch (e) {}
  }

  function applyPending(tree) { return tree; } // Task 20 replaces this.

  // --- Age formatting (client-side; server ages are up to one bucket stale) --
  function ageString(iso) {
    if (!iso) return "";
    var t = Date.parse(iso);
    if (isNaN(t)) return "";
    var d = (Date.now() - t) / 1000;
    if (d < 60) return "now";
    if (d < 3600) return Math.floor(d / 60) + "m";
    if (d < 86400) return Math.floor(d / 3600) + "h";
    return Math.floor(d / 86400) + "d";
  }

  // --- Active row (driven off htmx:afterSwap on #workspace) -------------------
  function syncActiveRow() {
    var path = (window.location && window.location.pathname) || "";
    var clean = path.replace(/\/+$/, "");
    var rows = document.querySelectorAll("#sidebar .sb-row[href]");
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].getAttribute("href") === clean) {
        rows[i].setAttribute("data-active", "");
        var hdr = rows[i].closest ? null : null; // project auto-expand: Task 22
      } else {
        rows[i].removeAttribute("data-active");
      }
    }
  }

  // --- Fetch + lifecycle -----------------------------------------------------
  function fetchTree() {
    var mySeq = ++model.seq;
    return window.fetch("/api/tree").then(function (r) { return r.json(); }).then(function (tree) {
      if (mySeq !== model.seq) return; // sequence guard: a newer fetch won
      renderTree(tree);
      migrateExpansionKeys(); // Task 22 (no-op until then)
    }).catch(function () {});
  }
  function migrateExpansionKeys() {} // Task 22 replaces this.

  paintSkeleton();
  fetchTree();

  document.body && document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e && e.target && e.target.id === "workspace") syncActiveRow();
  });

  window.SerfSidebar = { renderTree: renderTree, refresh: fetchTree, close: function () {} };
})();
```

(This preserves the mobile-drawer/rail/open-beside handlers from the old file — carry them across verbatim in Task 22; for this core task keep `close` a stub and re-add the drawer/rail logic in Task 22 so the survivor tests pass.)

- [ ] **Step 4: Update the app shell**

In `cmd/serf-hub/templates/app.html`, change the `#sidebar` aside from an htmx partial to a client-rendered container (keep the resizer sibling untouched):

```html
  <aside id="sidebar" aria-label="Sessions"></aside>
```

(Remove the `hx-get="/_partials/sidebar"`, `hx-trigger`, `hx-swap`, and the `<div class="sidebar-loading">` child.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-reconcile.js && node test-sidebar-model.js`
Expected: both print `ok ...` and exit 0.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/templates/app.html cmd/serf-hub/jstest/test-sidebar-reconcile.js cmd/serf-hub/jstest/test-sidebar-model.js
git commit -m "feat(web): client-rendered sidebar renderer with keyed reconcile"
```

---

## Task 20: Update contract + pending overlay

Wire the qualifying-event allowlist, the honest instant attention path, the coalesced/sequence-guarded resync (≥2s + 60s idle), and the pending-ops overlay with per-op completion predicates, post-POST resync, and 30s eviction safety net.

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js` (replace `applyPending`, add event wiring + mutation API)
- Test: `cmd/serf-hub/jstest/test-sidebar-overlay.js` (create)

- [ ] **Step 1: Write the failing overlay test**

Create `cmd/serf-hub/jstest/test-sidebar-overlay.js`:

```js
// Overlay: a favorite (mutation-type) op stays applied across resyncs until a
// resync reflects the field, and does NOT roll back when no qualifying event
// fires (post-POST resync confirms it). An archive (disappearance-type) op
// completes on POST-2xx. 30s eviction is a safety net only.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function boot(trees) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  let i = 0;
  const posts = [];
  w.fetch = (url, opts) => {
    if (opts && opts.method === "POST") { posts.push({ url, body: JSON.parse(opts.body) }); return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) }); }
    return Promise.resolve({ ok: true, json: () => Promise.resolve(trees[Math.min(i++, trees.length - 1)]) });
  };
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(src);
  return { w, posts };
}
function tree(fav) {
  return { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
    projects: [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true,
      sessions: [{ row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "s", state: "idle", kind: "session", tier: "current", favorite: fav }] }],
    attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}

(async () => {
  // Baseline: not favorited. Optimistic favorite. Next resync (post-POST) shows
  // favorite=true → op completes, no rollback.
  const { w, posts } = boot([tree(false), tree(true)]);
  await new Promise(r => setTimeout(r, 20));
  w.SerfSidebar.favorite("local:01A", true);
  // Optimistic: the row shows the star immediately, before any resync.
  let row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  if (!row.hasAttribute("data-favorite")) throw new Error("optimistic favorite must apply immediately");
  if (!posts.some(p => p.url === "/api/favorite" && p.body.favorited === true)) throw new Error("favorite must POST /api/favorite");
  await new Promise(r => setTimeout(r, 20)); // post-POST resync resolves to tree(true)
  row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  if (!row.hasAttribute("data-favorite")) throw new Error("confirmed favorite must persist after resync (no false rollback)");
  console.log("ok overlay favorite confirms via post-POST resync");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-overlay.js`
Expected: FAIL — `SerfSidebar.favorite` / overlay undefined.

- [ ] **Step 3: Implement the overlay + resync + events**

In `sidebar.js`, replace the `applyPending` stub and add the mutation/resync machinery. Replace `function applyPending(tree) { return tree; }` with:

```js
  // Pending overlay: every optimistic op is re-applied on top of every admitted
  // resync until it completes. Per-op predicates (round-2 A3): mutation-type
  // ops (favorite, rename) complete when a resync reflects the field;
  // disappearance-type ops (archive, delete) complete on POST-2xx (absence is
  // success). Eviction at 30s is a safety net only.
  function applyPending(tree) {
    if (!model.pending.size) return tree;
    var clone = JSON.parse(JSON.stringify(tree));
    model.pending.forEach(function (op) { op.apply(clone); });
    return clone;
  }

  function forEachNode(tree, fn) {
    var lists = [tree.needs_you, tree.favorites].concat(
      (tree.projects || []).map(function (p) { return p.sessions; }),
      (tree.archived_projects || []).map(function (p) { return p.sessions; }),
      (tree.test_runs || []).map(function (p) { return p.sessions; }));
    lists.forEach(function (l) { (l || []).forEach(fn); });
  }

  function nodeReflects(tree, ref, check) {
    var ok = false;
    forEachNode(tree, function (n) { if (n.ref === ref && check(n)) ok = true; });
    return ok;
  }

  function addPending(op) {
    op.at = Date.now();
    model.pending.set(op.id, op);
    if (model.tree) renderTree(model.tree);
    setTimeout(function () {
      var p = model.pending.get(op.id);
      if (p && p === op) { model.pending.delete(op.id); if (op.onEvict) op.onEvict(); if (model.tree) renderTree(model.tree); }
    }, 30000);
  }

  // Called by every admitted resync: drop mutation-type ops the payload now
  // reflects.
  function reconcilePending(tree) {
    model.pending.forEach(function (op, id) {
      if (op.confirm && op.confirm(tree)) model.pending.delete(id);
    });
  }

  // --- Public mutation API ---------------------------------------------------
  function favorite(ref, on) {
    var sid = ref.replace(/^local:/, "");
    addPending({
      id: "fav:" + ref, apply: function (t) { forEachNode(t, function (n) { if (n.ref === ref) n.favorite = on; }); },
      confirm: function (t) { return nodeReflects(t, ref, function (n) { return !!n.favorite === on; }); },
    });
    postJSON("/api/favorite", { kind: "session", id: sid, favorited: on });
  }
  function archive(ref, on) {
    var sid = ref.replace(/^local:/, "");
    var op = { id: "arch:" + ref, apply: function (t) { if (on) forEachNode(t, function (n) { if (n.ref === ref) n.__drop = true; }); } };
    addPending(op);
    postJSON("/api/archive", { kind: "session", id: sid, archived: on }).then(function () { model.pending.delete(op.id); scheduleResync(); });
  }
  function rename(ref, name) {
    var sid = ref.replace(/^local:/, "");
    addPending({
      id: "name:" + ref, apply: function (t) { forEachNode(t, function (n) { if (n.ref === ref) n.title = name; }); },
      confirm: function (t) { return nodeReflects(t, ref, function (n) { return n.title === name; }); },
    });
    postJSON("/api/sessions/" + encodeURIComponent(ref) + "/rename", { name: name }).catch(function () {});
  }

  function postJSON(url, body) {
    return window.fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
      .then(function (r) {
        if (!r || !r.ok) { throw new Error("post failed"); }
        scheduleResync(); // every mutation POST-2xx schedules a resync (round-3 H1)
        return r;
      })
      .catch(function (e) {
        if (window.SerfToast) window.SerfToast.show("Action failed", "error");
        throw e;
      });
  }
```

Filter dropped nodes in `flatten` (skip `n.__drop`). Add the coalesced resync + event wiring near the lifecycle section:

```js
  var resyncTimer = null, lastResync = 0;
  function scheduleResync() {
    if (resyncTimer) return;
    var wait = Math.max(0, 2000 - (Date.now() - lastResync)); // ≥2s spacing, trailing
    resyncTimer = setTimeout(function () { resyncTimer = null; lastResync = Date.now(); doResync(); }, wait);
  }
  function doResync() {
    var mySeq = ++model.seq;
    window.fetch("/api/tree").then(function (r) { return r.json(); }).then(function (tree) {
      if (mySeq !== model.seq) return; // sequence guard
      reconcilePending(tree);
      renderTree(tree);
      // Re-request children for projects the user expanded (served from the memo).
      model.expanded.forEach(function (key) { refetchProjectChildren(key); });
    }).catch(function () {}); // resync failure keeps the last good model rendered
  }
  function refetchProjectChildren(key) {
    window.fetch("/api/tree/project?key=" + encodeURIComponent(key)).then(function (r) { return r.ok ? r.json() : null; }).then(function (p) {
      if (!p || !model.tree) return;
      model.lazyCache.set(key, p);
      var projects = (model.tree.projects || []).concat(model.tree.archived_projects || [], model.tree.test_runs || []);
      for (var i = 0; i < projects.length; i++) { if (projects[i].key === key) { projects[i].sessions = p.sessions; } }
      renderTree(model.tree);
    }).catch(function () {});
  }

  var QUALIFYING = { "thread/started": 1, "thread/closed": 1, "thread/status/changed": 1, "serf/job/started": 1, "serf/job/finished": 1, "serf/attention/changed": 1 };
  function onNotification(method, params) {
    if (method === "serf/attention/changed") { applyAttentionInstant(params); scheduleResync(); return; }
    if (QUALIFYING[method]) scheduleResync();
  }
  // Instant path: attention changed[] carries the coarse 4-value level and only
  // fires on level transitions; adjust existing rows' tint + badge instantly.
  function applyAttentionInstant(params) {
    (params && params.changed || []).forEach(function (ch) {
      var rows = document.querySelectorAll('#sidebar .sb-row[data-ref="local:' + ch.threadId + '"]');
      for (var i = 0; i < rows.length; i++) rows[i].setAttribute("data-attention", ch.level);
    });
  }

  if (window.SerfAppwire && window.SerfAppwire.onNotification) window.SerfAppwire.onNotification(onNotification);
  if (window.SerfAppwire && window.SerfAppwire.onConnectionRestored) window.SerfAppwire.onConnectionRestored(scheduleResync);
  setInterval(scheduleResync, 60000); // 60s idle resync
```

Extend the exported surface: `window.SerfSidebar = { renderTree, refresh: fetchTree, favorite, archive, rename, close: function () {} };`

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-overlay.js && node test-sidebar-reconcile.js && node test-sidebar-model.js`
Expected: all `ok`.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/jstest/test-sidebar-overlay.js
git commit -m "feat(web): sidebar update contract + pending overlay with per-op predicates"
```

---

## Task 21: Row menu component + actions

Add the `⋯` popover (chip-picker positioning/dismiss/singleton patterns from dir-picker.js:7-72), keyboard navigation, a ≥24px reveal button, and per-row-kind items. Session rows: Open, Open beside, Favorite/Unfavorite, Rename, Archive/Unarchive. Project rows: New session, Settings, Archive/Unarchive, Delete…. Rename shows only when `node.rename` is true. If a reconcile removes the row anchoring an open menu, the menu closes and focus moves to the nearest surviving row.

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js` (menu component + wire into `buildRow`/`buildProjectHeader`)
- Modify: `cmd/serf-hub/assets/style.css` (menu + reveal-button styles)
- Test: `cmd/serf-hub/jstest/test-sidebar-menu.js` (create)

- [ ] **Step 1: Write the failing menu test**

Create `cmd/serf-hub/jstest/test-sidebar-menu.js`:

```js
// Menu: ⋯ opens a popover; session rows show Rename only when node.rename;
// choosing Favorite calls SerfSidebar.favorite; Escape closes; removing the
// anchor row closes the menu.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function tree(renameable) {
  return { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
    projects: [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true,
      sessions: [{ row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "s", state: "idle", kind: "session", tier: "current", rename: renameable }] }],
    attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree(true)) });
w.htmx = { process() {} };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
w.eval(src);
setTimeout(() => {
  const row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  const btn = row.querySelector(".sb-menu-btn");
  if (!btn) throw new Error("row must carry a ⋯ menu button");
  btn.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
  const menu = w.document.querySelector(".sb-menu");
  if (!menu) throw new Error("clicking ⋯ opens a menu");
  const items = [].map.call(menu.querySelectorAll(".sb-menu-item"), (e) => e.textContent);
  if (!items.some((t) => /Rename/.test(t))) throw new Error("renameable row must offer Rename, got " + items);
  let favCalled = null;
  w.SerfSidebar.favorite = (ref, on) => { favCalled = [ref, on]; };
  [].find.call(menu.querySelectorAll(".sb-menu-item"), (e) => /Favorite/.test(e.textContent)).dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
  if (!favCalled || favCalled[0] !== "local:01A") throw new Error("Favorite item must call SerfSidebar.favorite");
  console.log("ok menu open + rename gating + favorite action");
  process.exit(0);
}, 20);
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-menu.js`
Expected: FAIL — no `.sb-menu-btn`.

- [ ] **Step 3: Implement the menu**

In `sidebar.js`, attach a reveal button in `buildRow` (before `return a;`):

```js
    var menuBtn = document.createElement("button");
    menuBtn.type = "button";
    menuBtn.className = "sb-menu-btn btn-icon";
    menuBtn.setAttribute("aria-label", "row menu");
    menuBtn.setAttribute("aria-haspopup", "menu");
    menuBtn.textContent = "⋯"; // ⋯
    menuBtn.addEventListener("click", function (e) {
      e.preventDefault(); e.stopPropagation();
      openMenu(menuBtn, sessionMenuItems(n));
    });
    a.appendChild(menuBtn);
```

and add the menu component (chip-picker singleton/dismiss patterns):

```js
  var openMenuEl = null, menuAnchor = null;
  function closeMenu() {
    if (openMenuEl) { openMenuEl.remove(); openMenuEl = null; menuAnchor = null; document.removeEventListener("click", onDocClick, true); }
  }
  function onDocClick(e) { if (openMenuEl && !openMenuEl.contains(e.target) && e.target !== menuAnchor) closeMenu(); }
  function openMenu(anchor, items) {
    closeMenu();
    var menu = document.createElement("div");
    menu.className = "sb-menu";
    menu.setAttribute("role", "menu");
    items.forEach(function (it) {
      if (it.hidden) return;
      var b = document.createElement("button");
      b.type = "button"; b.className = "sb-menu-item"; b.setAttribute("role", "menuitem"); b.textContent = it.label;
      b.addEventListener("click", function (e) { e.preventDefault(); e.stopPropagation(); closeMenu(); it.run(); });
      menu.appendChild(b);
    });
    document.body.appendChild(menu);
    var r = anchor.getBoundingClientRect();
    menu.style.position = "absolute";
    menu.style.top = (r.bottom + window.scrollY + 2) + "px";
    menu.style.left = (r.left + window.scrollX) + "px";
    openMenuEl = menu; menuAnchor = anchor;
    setTimeout(function () { document.addEventListener("click", onDocClick, true); }, 0);
    document.addEventListener("keydown", onMenuKeydown);
    var first = menu.querySelector(".sb-menu-item"); if (first) first.focus();
  }
  function onMenuKeydown(e) {
    if (!openMenuEl) { document.removeEventListener("keydown", onMenuKeydown); return; }
    var items = [].slice.call(openMenuEl.querySelectorAll(".sb-menu-item"));
    var idx = items.indexOf(document.activeElement);
    if (e.key === "Escape") { e.preventDefault(); var a = menuAnchor; closeMenu(); if (a) a.focus(); }
    else if (e.key === "ArrowDown") { e.preventDefault(); (items[idx + 1] || items[0]).focus(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); (items[idx - 1] || items[items.length - 1]).focus(); }
  }

  function sessionMenuItems(n) {
    return [
      { label: "Open", run: function () { window.location.href = "/s/" + n.session_id; } },
      { label: "Open beside", run: function () { if (window.SerfPanes) window.SerfPanes.open("/thread/" + encodeURIComponent(n.ref), n.title); } },
      { label: n.favorite ? "Unfavorite" : "Favorite", run: function () { window.SerfSidebar.favorite(n.ref, !n.favorite); } },
      { label: "Rename", hidden: !n.rename, run: function () { startInlineRename(n); } },
      { label: n.tier === "archived" ? "Unarchive" : "Archive", run: function () { window.SerfSidebar.archive(n.ref, n.tier !== "archived"); } },
    ];
  }
  function projectMenuItems(p) {
    return [
      { label: "New session", run: function () { window.location.href = "/new?dir=" + encodeURIComponent(p.working_dir); } },
      { label: "Settings", run: function () { window.location.href = "/settings/project?cwd=" + encodeURIComponent(p.working_dir); } },
      { label: "Archive", run: function () { window.fetch("/api/archive", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ kind: "project", id: p.working_dir, archived: true }) }).then(scheduleResync); } },
      { label: "Delete…", run: function () { confirmDeleteProject(p); } },
    ];
  }
  function startInlineRename(n) {
    var row = document.querySelector('#sidebar .sb-row[data-row-id="' + cssEscape(n.row_id) + '"]');
    if (!row) return;
    var title = row.querySelector(".title");
    var input = document.createElement("input");
    input.className = "sb-rename-input"; input.value = n.title;
    title.replaceWith(input); input.focus(); input.select();
    function commit() { var v = input.value.trim(); if (v && v !== n.title) window.SerfSidebar.rename(n.ref, v); if (model.tree) renderTree(model.tree); }
    input.addEventListener("keydown", function (e) { if (e.key === "Enter") { e.preventDefault(); commit(); } else if (e.key === "Escape") { e.preventDefault(); if (model.tree) renderTree(model.tree); } });
    input.addEventListener("blur", commit);
  }
  function confirmDeleteProject(p) {
    var n = p.session_count || (p.sessions || []).length;
    if (!window.confirm("Delete project " + p.name + "? " + n + " session(s), " + (p.worktrees || 0) + " worktree(s). Worktrees are not touched.")) return;
    window.fetch("/api/project/delete", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ key: p.key, workingDir: p.working_dir }) })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (res) {
        if (res && res.deleted && onDeletedRedirect(res.deleted)) return;
        scheduleResync();
      });
  }
  function onDeletedRedirect(deleted) {
    var path = window.location.pathname || "";
    for (var i = 0; i < deleted.length; i++) { if (path === "/s/" + deleted[i]) { window.location.href = "/new"; return true; } }
    return false;
  }
  function cssEscape(s) { return (window.CSS && window.CSS.escape) ? window.CSS.escape(s) : s.replace(/["\\]/g, "\\$&"); }
```

Attach the project-header menu button in `buildProjectHeader` (append a `.sb-menu-btn` calling `openMenu(btn, projectMenuItems(p))`). In `reconcile`, when removing a node whose child contains the open menu anchor, call `closeMenu()` and focus the previous sibling row — add before `container.removeChild(...)`:

```js
        if (menuAnchor && container.children[m].contains(menuAnchor)) {
          var survivor = container.children[m].previousElementSibling || container.children[m].nextElementSibling;
          closeMenu();
          if (survivor && survivor.focus) survivor.focus();
        }
```

- [ ] **Step 4: Add menu + reveal-button CSS**

Append to `cmd/serf-hub/assets/style.css` (≥24px reveal button; menu popover; contrast per Task 24 tokens):

```css
.sb-menu-btn { min-width: 24px; min-height: 24px; opacity: 0; background: transparent; border: none; color: var(--text-muted); }
.sb-row:hover .sb-menu-btn, .sb-row:focus-within .sb-menu-btn { opacity: 1; }
.sb-menu { z-index: 60; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: var(--radius-md); padding: var(--space-1); box-shadow: 0 4px 16px rgba(0,0,0,.35); min-width: 160px; }
.sb-menu-item { display: block; width: 100%; text-align: left; padding: var(--space-2) var(--space-3); background: transparent; border: none; color: var(--text); font-size: var(--text-sm); min-height: 24px; }
.sb-menu-item:hover, .sb-menu-item:focus { background: var(--surface-secondary); }
.sb-rename-input { width: 100%; font-size: var(--text-base); background: var(--bg); color: var(--text); border: 1px solid var(--accent); border-radius: var(--radius-md); }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-menu.js && node test-sidebar-overlay.js && node test-sidebar-model.js && node test-sidebar-reconcile.js`
Expected: all `ok`.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-sidebar-menu.js
git commit -m "feat(web): row menu (session/project) with rename gating + anchor-removal close"
```

---

## Task 22: localStorage migration, a11y, and survivor behaviors (rail / drawer / open-beside)

Run the one-time expansion-key migration *after the first tree render* (the old→new mapping needs each project's path, which only the first payload provides — round-3 G4; co-basename collisions copy the old value to all matching new keys). Carry the surviving contract behaviors — mobile drawer, rail mode (⌘B + persistence), open-beside, `aria-expanded` on toggles — into the rewritten module verbatim from the old `sidebar.js`.

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js` (real `migrateExpansionKeys`; re-add drawer/rail/open-beside blocks)
- Test: `cmd/serf-hub/jstest/test-sidebar-migration.js`, `test-sidebar-survivors.js` (create)

- [ ] **Step 1: Write the failing tests**

Create `cmd/serf-hub/jstest/test-sidebar-migration.js`:

```js
// Old basename-keyed expansion entries migrate to the new path-slug keys after
// the first render; a co-basename collision copies the old value to all matches.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.localStorage.setItem("serf-hub.sidebar.expanded.foo", "true"); // legacy basename key
const tree = { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [
    { key: "foo-aaaa1111", name: "foo", working_dir: "/a/foo", default_expanded: false, sessions: [] },
    { key: "foo-bbbb2222", name: "foo", working_dir: "/b/foo", default_expanded: false, sessions: [] },
  ], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree) });
w.htmx = { process() {} };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
w.eval(src);
setTimeout(() => {
  if (w.localStorage.getItem("serf-hub.sidebar.expanded.foo-aaaa1111") !== "true") throw new Error("migration must copy to first co-basename key");
  if (w.localStorage.getItem("serf-hub.sidebar.expanded.foo-bbbb2222") !== "true") throw new Error("migration must copy to all co-basename keys");
  console.log("ok expansion-key migration post-first-render + copy-to-all");
  process.exit(0);
}, 20);
```

Create `cmd/serf-hub/jstest/test-sidebar-survivors.js`:

```js
// Surviving contracts carried into the rewrite: rail toggle + persistence,
// and mobile drawer close API.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <button data-sidebar-rail-toggle></button>
  <aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } }) });
w.htmx = { process() {} };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
w.eval(src);
w.document.querySelector("[data-sidebar-rail-toggle]").dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (!w.document.body.hasAttribute("data-sidebar-rail")) throw new Error("rail toggle must set body[data-sidebar-rail]");
if (w.localStorage.getItem("serf-hub.sidebar.rail") !== "true") throw new Error("rail must persist");
if (typeof w.SerfSidebar.close !== "function") throw new Error("drawer close API must survive");
console.log("ok survivors: rail toggle + persistence + close API");
process.exit(0);
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-migration.js; node test-sidebar-survivors.js`
Expected: FAIL — migration is a no-op and the rail block was dropped in the rewrite.

- [ ] **Step 3: Implement migration + re-add survivor blocks**

Replace `function migrateExpansionKeys() {}` with:

```js
  // One-time migration of legacy basename-keyed expansion entries to the new
  // path-slug keys. Runs after the first render (needs each project's key+name);
  // co-basename collisions copy the old value to every matching new key.
  var migratedExpansion = false;
  function migrateExpansionKeys() {
    if (migratedExpansion || !model.tree) return;
    migratedExpansion = true;
    var all = (model.tree.projects || []).concat(model.tree.archived_projects || [], model.tree.test_runs || []);
    all.forEach(function (p) {
      try {
        var legacy = window.localStorage.getItem(EXPAND_PREFIX + p.name);
        if (legacy === null) return;
        if (window.localStorage.getItem(EXPAND_PREFIX + p.key) === null) window.localStorage.setItem(EXPAND_PREFIX + p.key, legacy);
        if (legacy === "true") model.expanded.add(p.key);
      } catch (e) {}
    });
  }
```

Seed `model.expanded` from the new keys on load (in `renderTree` or after migration) so persisted expansion restores:

```js
  function restoreExpanded(tree) {
    var all = (tree.projects || []).concat(tree.archived_projects || [], tree.test_runs || []);
    all.forEach(function (p) {
      try { if (window.localStorage.getItem(EXPAND_PREFIX + p.key) === "true") model.expanded.add(p.key); } catch (e) {}
    });
  }
```

Call `restoreExpanded(tree)` at the top of `renderTree` before `flatten`. In `syncActiveRow`, auto-expand the active row's project (round-trip the header's `data-project-key`):

```js
      if (rows[i].getAttribute("href") === clean) {
        rows[i].setAttribute("data-active", "");
        var sec = rows[i].previousElementSibling;
        // find the enclosing project header key and ensure expanded
        var key = rows[i].getAttribute("data-project-key-of");
        if (key && !model.expanded.has(key)) { model.expanded.add(key); persistExpanded(key); if (model.tree) renderTree(model.tree); }
      }
```

(Stamp `data-project-key-of` on each session row in `buildRow` when it is pushed under a project in `pushProject`, or resolve the key from the preceding header during flatten — set `n.__projectKey` in `pushProject` and copy it onto the row's `data-project-key-of` attribute in `buildRow`.)

Re-add, verbatim from the pre-rewrite `sidebar.js`, the blocks for: the mobile hamburger drawer (`setSidebarOpen`/`onOutsideClick`/`[data-sidebar-toggle]` handlers), rail mode (`RAIL_KEY`, `isRailEnabled`, `setRail`, `toggleRail`, the `[data-sidebar-rail-toggle]` click, the ⌘B keydown, `syncRailToggleLabel`), and open-beside on subagent rows (`onSidebarOpenBeside` + keydown). Expose `close` via `setSidebarOpen(false)` as before: `window.SerfSidebar.close = function () { setSidebarOpen(false); };`

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-migration.js && node test-sidebar-survivors.js && node test-sidebar-menu.js && node test-sidebar-overlay.js && node test-sidebar-model.js && node test-sidebar-reconcile.js`
Expected: all `ok`.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/jstest/test-sidebar-migration.js cmd/serf-hub/jstest/test-sidebar-survivors.js
git commit -m "feat(web): expansion-key migration + carry rail/drawer/open-beside survivors"
```

---

## Task 23: Delete the Go sidebar templates, routes, and subsumed jstests

Now that the new suite covers the surviving contract (resizer markup is app-shell-level and untouched; rail/drawer/open-beside covered by `test-sidebar-survivors.js`; reconcile/overlay/menu/migration covered by the new tests), remove the dead server-rendered sidebar.

**Files:**
- Delete: `cmd/serf-hub/templates/partials/sidebar.html`
- Modify: `cmd/serf-hub/web.go` (remove `handleSidebar`, `handleSidebarProject`, `/_partials/sidebar*` cases, `sidebarTmpl` field + parse + `sidebarTemplateFuncs`)
- Delete: subsumed `cmd/serf-hub/jstest/test-sidebar-{active,appwire-refresh,archive,cluster,collapse,disclosure-preserve,lazy-children,tiers}.js`; keep `test-sidebar-resizer-markup.js` (app-shell markup, still valid) and `test-sidebar-open-beside.js` only if still asserting live markup — otherwise delete and rely on `test-sidebar-survivors.js`.
- Modify: Go tests referencing `/_partials/sidebar` (web_test.go) — delete or repoint the sidebar-partial assertions.

- [ ] **Step 1: Confirm the new suite is green (guard before deleting)**

Run: `sh cmd/serf-hub/jstest/run-all.sh`
Expected: PASS including all `test-sidebar-*.js`. Only proceed if green.

- [ ] **Step 2: Remove the server-rendered sidebar**

Delete `cmd/serf-hub/templates/partials/sidebar.html`. In `cmd/serf-hub/web.go`: delete `handleSidebar`, `handleSidebarProject`, the two `case r.URL.Path == "/_partials/sidebar..."` branches in `handleInternalPartial`, the `sidebarTmpl` field, its `template.Must(...ParseFS(...))` in `NewWebServer`, and the `sidebarTemplateFuncs` var (now unused). Delete the subsumed `test-sidebar-*.js` files listed above. In `cmd/serf-hub/web_test.go`, delete the tests that request `/_partials/sidebar` and assert its HTML (they exercised the deleted route); keep app-shell tests (`app.html` assertions) that still hold.

- [ ] **Step 3: Verify build + tests + jstest**

Run: `go build ./cmd/serf-hub/... && go test ./cmd/serf-hub/... && sh cmd/serf-hub/jstest/run-all.sh`
Expected: PASS. `grep -rn "_partials/sidebar\|sidebarTmpl\|handleSidebar" cmd/serf-hub` returns nothing.

- [ ] **Step 4: Commit**

```bash
git rm cmd/serf-hub/templates/partials/sidebar.html cmd/serf-hub/jstest/test-sidebar-active.js cmd/serf-hub/jstest/test-sidebar-appwire-refresh.js cmd/serf-hub/jstest/test-sidebar-archive.js cmd/serf-hub/jstest/test-sidebar-cluster.js cmd/serf-hub/jstest/test-sidebar-collapse.js cmd/serf-hub/jstest/test-sidebar-disclosure-preserve.js cmd/serf-hub/jstest/test-sidebar-lazy-children.js cmd/serf-hub/jstest/test-sidebar-tiers.js
git add cmd/serf-hub/web.go cmd/serf-hub/web_test.go
git commit -m "refactor(hub): remove server-rendered sidebar templates/routes + subsumed jstests"
```

# Phase C — Typography, e2e, final gates

## Task 24: Typography & density pass

Raise the sidebar's readability floor: 11px minimum; `--text-dim` (≈2.9:1, fails AA) banned below 12px; row meta at 11px on a ≥4.5:1 pairing verified in both themes; ≥24px hit boxes on all sidebar controls at desktop; the whole project header is the toggle target (its buttons `stopPropagation`); subagent rows and any "Completed (N)" toggle ≥24px.

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/jstest/test-color-system-css.js` extension OR a new `test-sidebar-density-css.js` (create) that parses `style.css` and asserts the rules.

- [ ] **Step 1: Write the failing CSS assertion test**

Create `cmd/serf-hub/jstest/test-sidebar-density-css.js`:

```js
// Sidebar density/contrast floor: row meta uses >=11px and not --text-dim;
// menu/reveal controls are >=24px.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function ruleBody(selector) {
  const i = css.indexOf(selector);
  if (i < 0) return "";
  return css.slice(i, css.indexOf("}", i));
}
const fails = [];
const metaRule = ruleBody(".sb-row .meta");
if (!/font-size:\s*var\(--text-xs\)|font-size:\s*11px/.test(metaRule)) fails.push("row meta must be 11px (--text-xs)");
if (/--text-dim/.test(metaRule)) fails.push("row meta must not use --text-dim");
const menuBtn = ruleBody(".sb-menu-btn");
if (!/min-height:\s*24px/.test(menuBtn) || !/min-width:\s*24px/.test(menuBtn)) fails.push("reveal button must be >=24px");
if (fails.length) { fails.forEach((f) => console.log("FAIL: " + f)); process.exit(1); }
console.log("ok sidebar density/contrast floor");
process.exit(0);
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-density-css.js`
Expected: FAIL — the `.sb-row .meta` rule does not exist yet in `style.css`.

- [ ] **Step 3: Add the density/contrast rules**

Append to `cmd/serf-hub/assets/style.css`:

```css
/* Sidebar readability floor (design diagnostic WS3): 11px minimum; row meta on
   a >=4.5:1 pairing (--text-muted), never --text-dim below 12px. */
.sb-row { min-height: 32px; }
.sb-row .title { font-size: var(--text-base); line-height: var(--leading-tight); overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
.sb-row .meta { font-size: var(--text-xs); color: var(--text-muted); display: flex; gap: var(--space-3); }
.sb-tree .project-header { min-height: 32px; cursor: pointer; }
.sb-tree .project-header .sb-menu-btn, .sb-tree .project-header button { min-width: 24px; min-height: 24px; }
.subagent-row, .subagent-toggle { min-height: 24px; }
@media (min-width: 768px) {
  .sb-row [role="button"], .sb-menu-btn { min-height: 24px; min-width: 24px; }
}
```

Verify contrast in both themes: `--text-muted` (#7a7a86 on #0a0a0e ≈ 4.7:1 dark; check the light palette pairing in the `data-theme="light"` block and, if the light `--text-muted` falls below 4.5:1 on the light sidebar background, bump it there only).

- [ ] **Step 4: Run test + visual check**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-density-css.js && node test-color-system-css.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-sidebar-density-css.js
git commit -m "feat(web): sidebar typography & density floor (11px, >=4.5:1, >=24px targets)"
```

---

## Task 25: e2e scenario cards

Prove the rebuilt surface end-to-end against a freshly built hub, per the `e2e-scenario-testing` skill: falsifiable assertions driven through the real web UI. Cover: expand-non-live-project-survives-a-working-session; favorite → Pinned across reload; project delete full cycle (validation, live-refusal, partial report, open-session redirect, post-delete tree, re-created project not auto-archived); rename live + ended (survives compaction and the first resync).

**Files:**
- Create: scenario card files under `test/scenarios/sidebar-rebuild/` (or the repo's existing scenario location — grep `docs/agentic-testing.md` for the canonical path and follow it).

- [ ] **Step 1: Invoke the skill and author the cards**

Use the `e2e-scenario-testing` skill. Build the binaries: `go build -o /tmp/serf-hub ./cmd/serf-hub && go build -o /tmp/serf ./cmd/serf`. Source the repo `.env` (`. "$PWD/.env"`) and launch the hub against a scratch `SERF_STATE_DIR`. Write one card per scenario with explicit pass/fail assertions on the live DOM (e.g. via the browser skill or `curl /api/tree` + JSON assertions).

- [ ] **Step 2: Card — expand-non-live survives a working session**

Assertions: with a collapsed non-live project expanded (fetch `/api/tree/project`), spawn/advance a live session elsewhere so a resync fires; the previously-expanded project's rows remain present (the resync re-requests expanded projects from the memo). Falsifiable: the expanded rows must not vanish.

- [ ] **Step 3: Card — favorite → Pinned across reload**

Assertions: `POST /api/favorite {session, true}`; `/api/tree` `favorites[]` includes the session; reload the page; the Pinned tier still renders it (localStorage-independent — server truth).

- [ ] **Step 4: Card — project delete full cycle**

Assertions: (a) `POST /api/project/delete` with a mismatched `workingDir` → 400; (b) with a live session in the project → 409 listing names, files intact; (c) after shutting the session down → 200, `deleted[]` non-empty, files gone; (d) if the open workspace was a deleted session, the client lands on `/new`; (e) `/api/tree` no longer lists the project; (f) recreating a session at the same directory shows the project again (not silently archived).

- [ ] **Step 5: Card — rename live + ended**

Assertions: rename a live session via the menu → the row title updates within ≤2s (post-POST resync); drive a compaction turn → the title does NOT revert (namer suppression). Rename an ended session → the meta file on disk shows the new name and the first resync shows it (no rollback toast).

- [ ] **Step 6: Run the cards + commit**

Run each card against the freshly built hub; all assertions pass.

```bash
git add test/scenarios/sidebar-rebuild/
git commit -m "test(e2e): sidebar rebuild scenario cards (expand/favorite/delete/rename)"
```

---

## Task 26: Full-repo gates + doc regen + "what changed" note

**Files:**
- Modify: `docs/serf-hub-web-routing.md` (route changes), `docs/appwire-protocol.md` (already regenerated in Task 17 — verify fresh)
- Verify: whole-repo lint + tests + jstest.

- [ ] **Step 1: Regenerate + verify the appwire doc is fresh**

Run: `make generate && make lint-generated`
Expected: no diff (Task 17 already committed the regenerated `docs/appwire-protocol.md`).

- [ ] **Step 2: Update the web-routing doc**

In `docs/serf-hub-web-routing.md`, replace the `/_partials/sidebar` + `/_partials/sidebar/project` entries with the new endpoints: `GET /api/tree` (now the sidebar's data source), `GET /api/tree/project?key=`, `POST /api/favorite`, `POST /api/project/delete`, `POST /api/sessions/{ref}/rename`; note the sidebar is client-rendered (no server partial). Use the `maintaining-documentation` skill to keep terminology consistent.

- [ ] **Step 3: Full-repo gates**

Run:
```bash
make lint
make test
sh cmd/serf-hub/jstest/run-all.sh
```
Expected: all green across every module (root, agent, llm, auth, fuzz, invariant, envvars) and the JSDOM suite.

- [ ] **Step 4: "What changed" note + commit**

Append a short "What changed (2026-07-04 sidebar rebuild)" section to `docs/serf-hub-web-routing.md` (or the web-ui README) summarizing: client-rendered keyed sidebar off `/api/tree`; path-based project identity + slug keys; favorites + Pinned; project delete; test-run classification; in-scope rename; the dead server-rendered sidebar removed.

```bash
git add docs/serf-hub-web-routing.md docs/appwire-protocol.md
git commit -m "docs: web-routing + appwire protocol reflect the sidebar rebuild"
```

---

## Self-Review (spec §-by-§ → task map)

Every Review-log fold and mechanism maps to a task that implements **and** tests it:

- **Identity (Decision 3 / round-1 A1·B1):** full-path grouping + slug Key → Task 1; path-keyed archive with legacy read-fallback + **precedence** → Task 2; orphan-live rewrite + `no-project` → Task 3; un-archive legacy-row lifecycle (G3) → Task 8; delete legacy-row scrub (G3) → Task 14.
- **Tree JSON:** additive fields + tier projections → Task 4; **cluster synthetic IDs** (A7/B4) → Task 5; **subagent 50-cap** → Task 6; rowless-collapsed shaping post-memo → Task 11; ages client-side (A6) → Task 19 (`ageString`).
- **Decision stores:** favorite table + **Delete on both** → Task 7; `/api/favorite` + Pinned → Task 8.
- **Memo (H2):** BuildTree + summary only, watcher stays fresh → Task 9; **content-delta-gated** hooks incl. Find's rebuild (G5) → Task 10; `/api/tree/project` from the memo (A4) → Task 11.
- **`UpdateMeta` (A5/B1, H3):** sorted re-insert + FTS re-rank → Task 12.
- **Remote async cache:** → Task 13.
- **Delete:** resolution from `All()` w/ StateDir (A1), key↔workingDir validation (A11), per-session `Roster.Find` re-check (A9), `.log.jsonl` + `<id>/` removal (B2), store scrubs incl. legacy row (B7/G3), live-refusal 409, deleted/skipped → Task 14.
- **Origin (Decision 4):** SessionMeta.Origin + `SERF_SESSION_ORIGIN` (coordinates w/ WS2 goldens), **TestRuns-over-Archived** (B6) → Task 15.
- **Rename (Decision 2):** `serf/thread/name/set` + catalog + `ThreadCapabilities.Rename` + docs regen (B5) → Task 17; agent in-memory naming update (A8) + namer suppression → Task 16; live path UpdateMeta+bump + ended pre-write re-check (A2) + UpdateMeta+bump (G1) + legacy-daemon 404 toast, no live file-edit fallback (G2) + `TreeNode.Rename` resolution (G2) → Task 18.
- **Renderer/overlay:** RowID keying incl. cross-tier duplicates (B3) → Task 19; sequence guard, instant attention path, ≥2s + 60s resync, per-op predicates, **post-POST resync** (H1), 30s eviction → Task 20; menu incl. anchor-removal close → Task 21; **migration post-first-render + copy-to-all** (G4) + survivors (H5: resizer app-shell + rail + open-beside) → Task 22; delete old only after new suite covers surviving contract → Task 23.
- **Typography/density:** → Task 24. **e2e cards** (all four spec scenarios) → Task 25. **Final gates + doc regen** → Task 26.

Type/name consistency checked across tasks: `TreeProject.Key`/`ProjectSlug`; `InputsVersion`/`TreeCache`; `PastIndex.UpdateMeta`/`SetOnChange`; `MethodSerfThreadNameSet`/`ThreadNameSetParams`/`ThreadCapabilities.Rename`/`Client.ThreadNameSet`/`Source.SetThreadName`/`Session.Rename`/`sessionNameSourceUser`; client `SerfSidebar.{renderTree,favorite,archive,rename}`; `hubapi.TreeNode.{Tier,Branch,ClusterCount,Favorite,Rename}`.

---

Plan complete and saved to `docs/superpowers/plans/2026-07-04-sidebar-rebuild.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints.

Note on ordering: execute **Task 17 before Task 16** (Task 16's daemon handler references the appwire types Task 17 adds), and keep Tasks 16-18 in the same working tree so the catalog↔router cross-check tests go green together.








