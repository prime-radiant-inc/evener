# Named Pinned Session Sections Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the WebUI’s single session-level Pinned list with durable, user-named, alphabetical pinned sections that each own at most one assignment per top-level session.

**Architecture:** Add a transactional `PinSectionStore` beside the existing archive/favorite stores in Hub SQLite. REST handlers own validation and exactly-once tree notifications; `/api/tree` projects renderable assignments through the existing favorite-authority machinery while a section-list endpoint exposes hidden empty sections. The React rail keeps its current tree/store/action boundaries, adds a focused section picker and section-heading controls, and uses the existing optimistic-overlay and browser disclosure-state patterns.

**Tech Stack:** Go, `database/sql`, modernc SQLite, Hub REST/JSON, React 19, TypeScript 6, Zustand, CSS modules, Vitest/Testing Library, deterministic scripted-provider browser scenarios.

## Global Constraints

- Read `docs/testing.md` before changing tests; default tests must not depend on credentials, network access, quota, current model behavior, wall-clock timing outside the process, or ambient developer state.
- A pinned session belongs to exactly one section; moving it is one atomic upsert, never an unpin-then-pin sequence.
- Section names contain at most 80 Unicode code points after trimming and use a Unicode case-folded uniqueness key. Creation reuses an existing equivalent name; rename conflicts return `409` and never merge sections.
- Non-empty renderable sections appear between **Live** and **Projects**, sorted case-insensitively by name. Sessions within each section retain server order: newest `updated_at` first.
- Live pinned sessions remain visible in both **Live** and their pinned section, and every duplicate `TreeNode` carries the same `pin_section_id`.
- Empty and temporarily unrenderable sections remain durable and selectable but stay hidden from the sidebar.
- New sections are created only through **Pin this session…** or **Move pinned session…**; do not add a standalone sidebar creation control or central manager.
- Only visible section headings expose Rename and Delete. Deleting a section confirms its durable `member_count` and atomically unpins every member.
- Project favorites keep their current `favorite` table, `favorite` wire flags, endpoint behavior, row actions, authority rules, and tests.
- Session `POST /api/favorite` support is removed in the same change; it returns `400` directing callers to `/api/session-pin`.
- REST tree types in `hubapi/types.go` and `frontend/src/stores/tree.ts` are hand-maintained mirrors. Do not run or edit AppWire generation for this feature.
- Mutation handlers call `notifyMutation` exactly once after a successful state change. Stores do not broadcast. Failed requests and true no-ops broadcast zero times.
- SQLite foreign keys must be enabled on every `PinSectionStore` connection before schema, query, or mutation work.
- Preserve unrelated workspace changes. In particular, do not stage or restore the pre-existing deletion of `docs/superpowers/plans/2026-08-02-all-open-katas.md`.

## File Structure

**Create**

- `cmd/serf-hub/internal/hubcore/pin_section.go` — SQLite schema, Unicode name normalization, section CRUD, assignment CRUD, transactional migration, and typed store errors.
- `cmd/serf-hub/internal/hubcore/pin_section_test.go` — deterministic store, migration, concurrency, and foreign-key tests.
- `cmd/serf-hub/web_api_pin_section.go` — section-list, assign/move/create-or-reuse, unpin, rename, and delete handlers.
- `cmd/serf-hub/web_api_pin_section_test.go` — REST status/body/validation/no-op/notification tests.
- `cmd/serf-hub/frontend/src/shell/rail/PinSectionPicker.tsx` — accessible pin/move picker and new-section name step.
- `cmd/serf-hub/frontend/src/shell/rail/PinSectionPicker.test.tsx` — picker loading, selection, current marker, name validation, reuse, and failure tests.

**Modify**

- `cmd/serf-hub/internal/hubcore/config.go` — add `PinSections *PinSectionStore` to `WebConfig`.
- `cmd/serf-hub/main.go` — construct the pin store from `index.db` and pass it into `WebConfig`.
- `cmd/serf-hub/web.go` — register `/api/pin-sections`, `/api/pin-sections/`, and `/api/session-pin`.
- `cmd/serf-hub/web_api_favorite.go` and `web_api_favorite_test.go` — retain project mutations and reject session mutations.
- `cmd/serf-hub/web_api_tree.go`, `web_api_tree_test.go`, and `web_api_tree_favorite_revalidation_test.go` — migrate legacy pins with the request’s authority snapshot, project named sections, preserve dormant assignments, and remove session favorites projection.
- `hubapi/types.go` and `hubapi/client_test.go` — add REST wire types and replace `favorites` with `pin_sections`.
- `cmd/serf-hub/web_api_session_delete.go`, `web_api_session_delete_test.go`, `web_api_project_delete.go`, and `web_api_project_delete_test.go` — clean assignments for deleted sessions only and surface pin-store decision errors.
- `cmd/serf-hub/frontend/src/stores/tree.ts` and `tree.test.ts` — mirror/normalize `pin_sections` and `pin_section_id`.
- `cmd/serf-hub/frontend/src/shell/rail/actions.ts` and `actions.test.ts` — typed section and assignment requests; leave project `setFavorite` intact.
- `cmd/serf-hub/frontend/src/shell/rail/railNodes.ts` and `railNodes.test.ts` — section node projection and stable section disclosure IDs.
- `cmd/serf-hub/frontend/src/shell/rail/railPending.ts` and `railPending.test.ts` — optimistic pin, move, unpin, rename, and delete projections.
- `cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx` and `RailRow.test.tsx` — replace binary session favorite action with pin/move requests while preserving project favorite actions.
- `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx`, `Rail.test.tsx`, and `Rail.module.css` — section rendering, picker/dialog state, heading overflow controls, collapse state, rename, delete confirmation, and mutation orchestration.
- `test/scenarios/sidebar-favorite-pinned-across-reload.md` — replace the binary favorite scenario with named-section durability, reuse, move, hidden-empty, delete, and project-favorite checks.

**Delete after replacement tests pass**

- No source file is deleted. The legacy `FavoriteStore` remains for project favorites.

---

### Task 1: Build the durable pin-section store and migration

**Files:**
- Create: `cmd/serf-hub/internal/hubcore/pin_section.go`
- Create: `cmd/serf-hub/internal/hubcore/pin_section_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/config.go:58-60`
- Modify: `cmd/serf-hub/main.go:180-195,330-345`

**Interfaces:**
- Consumes: `ArchiveKey`, `FavoriteDecisionClassification`, and `FavoriteDecisionState` from `hubcore`.
- Produces:

```go
const PinSectionNameMaxRunes = 80

var (
    ErrPinSectionName     = errors.New("invalid pin section name")
    ErrPinSectionNotFound = errors.New("pin section not found")
    ErrPinSectionConflict = errors.New("pin section name already exists")
)

type PinSection struct {
    ID          string
    Name        string
    MemberCount int
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type SessionPin struct {
    SessionID string
    SectionID string
    AssignedAt time.Time
}

type LegacyPinDecision struct {
    StoredID       string
    Classification FavoriteDecisionClassification
}

type PinSectionStore struct {
    dbPath string
    fs     afero.Fs
    openDB func(driverName, dataSourceName string) (*sql.DB, error)
}

func NewPinSectionStore(dbPath string) *PinSectionStore
func NormalizePinSectionName(raw string) (display string, key string, err error)
func (s *PinSectionStore) Sections() ([]PinSection, error)
func (s *PinSectionStore) Assign(sectionID, sessionID string, now time.Time) (PinSection, bool, error)
func (s *PinSectionStore) CreateOrReuseAndAssign(name, sessionID string, now time.Time) (PinSection, bool, error)
func (s *PinSectionStore) Unpin(sessionID string) (bool, error)
func (s *PinSectionStore) Rename(sectionID, name string, now time.Time) (PinSection, bool, error)
func (s *PinSectionStore) DeleteSection(sectionID string) (memberCount int, changed bool, err error)
func (s *PinSectionStore) DeleteSession(sessionID string) (bool, error)
func (s *PinSectionStore) Assignments() (map[string]SessionPin, error)
func (s *PinSectionStore) MigrateLegacy(decisions []LegacyPinDecision, now time.Time) (bool, error)
```

- Adds `PinSections *PinSectionStore` to `hubcore.WebConfig`.

- [ ] **Step 1: Write failing normalization, CRUD, and assignment tests**

Create table-driven tests with these exact assertions:

```go
func TestNormalizePinSectionName(t *testing.T) {
    tests := []struct {
        raw, display, key string
        wantErr bool
    }{
        {raw: "  Research  ", display: "Research", key: "research"},
        {raw: "Straße", display: "Straße", key: "strasse"},
        {raw: "\t\n", wantErr: true},
        {raw: strings.Repeat("界", 81), wantErr: true},
    }
    for _, tt := range tests {
        display, key, err := NormalizePinSectionName(tt.raw)
        if (err != nil) != tt.wantErr || display != tt.display || key != tt.key {
            t.Fatalf("NormalizePinSectionName(%q) = %q, %q, %v", tt.raw, display, key, err)
        }
    }
}

func TestPinSectionStoreCreateReusesCaseFoldedNameAndMovesAtomically(t *testing.T) {
    store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
    first, changed, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0))
    if err != nil || !changed { t.Fatalf("first = %+v, %v, %v", first, changed, err) }
    reused, changed, err := store.CreateOrReuseAndAssign("research", "session-b", time.Unix(2, 0))
    if err != nil || !changed || reused.ID != first.ID { t.Fatalf("reuse = %+v, %v, %v", reused, changed, err) }
    other, _, err := store.CreateOrReuseAndAssign("Client", "session-a", time.Unix(3, 0))
    if err != nil { t.Fatal(err) }
    pins, err := store.Assignments()
    if err != nil { t.Fatal(err) }
    if len(pins) != 2 || pins["session-a"].SectionID != other.ID { t.Fatalf("pins = %+v", pins) }
}
```

Also add named tests for:

- alphabetical `Sections()` ordering and durable `MemberCount`;
- empty sections surviving `Unpin` and remaining in `Sections()`;
- `Rename` allowing a case-only display change and returning `ErrPinSectionConflict` for another section’s key;
- `DeleteSection` returning the durable member count and cascading assignments;
- `Assign`, `Unpin`, `Rename`, and `DeleteSection` returning `changed=false` for true no-ops;
- every opened connection reporting `PRAGMA foreign_keys = 1`;
- concurrent equivalent `CreateOrReuseAndAssign` calls converging on one section;
- last-committed assignment winning for one session without duplicate rows.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test ./cmd/serf-hub/internal/hubcore -run 'TestNormalizePinSectionName|TestPinSectionStore' -count=1
```

Expected: compile failure because `PinSectionStore` and its types do not exist.

- [ ] **Step 3: Implement schema, normalization, and transactional operations**

Use two tables and one migration marker:

```sql
CREATE TABLE IF NOT EXISTS pin_section (
  id         TEXT    NOT NULL PRIMARY KEY,
  name       TEXT    NOT NULL,
  name_key   TEXT    NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS session_pin (
  session_id  TEXT    NOT NULL PRIMARY KEY,
  section_id  TEXT    NOT NULL REFERENCES pin_section(id) ON DELETE CASCADE,
  assigned_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS hub_schema_migration (
  name       TEXT    NOT NULL PRIMARY KEY,
  applied_at INTEGER NOT NULL
);
```

In `open`, execute `PRAGMA foreign_keys = ON` and verify it with `PRAGMA foreign_keys` before any schema work. Generate opaque IDs with `crypto/rand` and hex encoding; do not derive IDs from names. Use `golang.org/x/text/cases.Fold()` to compute `name_key` after `strings.TrimSpace`, and count `utf8.RuneCountInString(display)`.

Implement create/reuse and assignment in one `sql.Tx`. Handle a unique-key race by selecting the existing row inside a fresh transaction and then upserting `session_pin`:

```sql
INSERT INTO session_pin(session_id, section_id, assigned_at)
VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE
SET section_id = excluded.section_id, assigned_at = excluded.assigned_at
WHERE session_pin.section_id <> excluded.section_id;
```

Derive `changed` from `RowsAffected`; do not update `assigned_at` for a no-op assignment to the same section.

- [ ] **Step 4: Write failing legacy migration tests**

Seed the existing `favorite` table directly with true, false, valid, dormant, and confirmed-invalid session rows, then call:

```go
changed, err := store.MigrateLegacy([]LegacyPinDecision{
    {StoredID: "valid", Classification: FavoriteDecisionClassification{
        State: FavoriteDecisionValid,
        CanonicalKey: ArchiveKey{Kind: "session", ID: "canonical-valid"},
    }},
    {StoredID: "remote-missing", Classification: FavoriteDecisionClassification{State: FavoriteDecisionDormant}},
    {StoredID: "subagent", Classification: FavoriteDecisionClassification{State: FavoriteDecisionConfirmedInvalid}},
}, time.Unix(10, 0))
```

Assert:

- one ordinary section named `Pinned` exists;
- `canonical-valid` and `remote-missing` are assigned;
- `subagent` is absent;
- every `kind='session'` favorite row, including false rows, is deleted;
- project favorite rows remain byte-for-byte unchanged;
- a second migration returns `changed=false`;
- unpinning after migration and reopening the store never recreates the assignment.

- [ ] **Step 5: Implement idempotent migration**

Inside one transaction:

1. return `changed=false` when `hub_schema_migration.name = 'named-pin-sections-v1'` exists;
2. inspect the supplied legacy classifications;
3. create/reuse `Pinned` only when at least one valid or dormant true favorite survives;
4. use `CanonicalKey.ID` for valid rows and `StoredID` for dormant rows;
5. discard confirmed-invalid rows;
6. delete all `favorite.kind='session'` rows;
7. insert the migration marker last.

A failure rolls back tables, assignments, favorite-row deletion, and marker insertion together.

- [ ] **Step 6: Wire production construction and verify**

In `main.go`, construct both stores from `pastIndexDB`:

```go
favorite := hubcore.NewFavoriteStore(pastIndexDB)
pinSections := hubcore.NewPinSectionStore(pastIndexDB)
```

Pass `PinSections: pinSections` in `WebConfig`. Tests constructing `WebConfig` may leave it nil until their pin behavior is under test.

Run:

```bash
gofmt -w cmd/serf-hub/internal/hubcore/pin_section.go cmd/serf-hub/internal/hubcore/pin_section_test.go cmd/serf-hub/internal/hubcore/config.go cmd/serf-hub/main.go
go test ./cmd/serf-hub/internal/hubcore -run 'PinSection|MigrateLegacy' -count=1
go test ./cmd/serf-hub -run 'TestMain' -count=1
git diff --check
```

Expected: all focused tests pass.

- [ ] **Step 7: Commit Task 1**

```bash
git add cmd/serf-hub/internal/hubcore/pin_section.go cmd/serf-hub/internal/hubcore/pin_section_test.go cmd/serf-hub/internal/hubcore/config.go cmd/serf-hub/main.go
git commit -m "feat(hub): persist named session pin sections"
```

---

### Task 2: Add the REST mutation and section-list contracts

**Files:**
- Create: `cmd/serf-hub/web_api_pin_section.go`
- Create: `cmd/serf-hub/web_api_pin_section_test.go`
- Modify: `cmd/serf-hub/web.go:157-173`
- Modify: `cmd/serf-hub/web_api_favorite.go:14-55`
- Modify: `cmd/serf-hub/web_api_favorite_test.go`
- Modify: `hubapi/types.go`
- Modify: `hubapi/client_test.go`

**Interfaces:**
- Consumes: Task 1 store methods and existing `topLevelFavoriteSessionID` validation.
- Produces REST shapes:

```go
type PinSection struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    MemberCount int    `json:"member_count"`
}

type SessionPinAssignment struct {
    SessionRef  string     `json:"session_ref"`
    Section     PinSection `json:"section"`
}

type SessionPinMutationResponse struct {
    OK         bool                 `json:"ok"`
    Changed    bool                 `json:"changed"`
    Assignment SessionPinAssignment `json:"assignment"`
}
```

- Produces:
  - `GET /api/pin-sections`
  - `PATCH /api/pin-sections/<id>`
  - `DELETE /api/pin-sections/<id>`
  - `POST /api/session-pin`
  - `DELETE /api/session-pin?ref=<session-ref>`

- [ ] **Step 1: Write failing route and contract tests**

Add table tests through `NewWebServer(...).Handler()` for exact methods, statuses, and JSON:

```go
func TestAPISessionPinCreateReuseMoveAndUnpin(t *testing.T) {
    web, store := pinSectionAPIWeb(t, topLevelMeta("session-a"))

    created := postJSON(t, web.Handler(), "/api/session-pin", `{"session_ref":"local:session-a","section_name":"Research"}`)
    if created.Code != http.StatusOK { t.Fatalf("create = %d: %s", created.Code, created.Body.String()) }

    sections := getJSON(t, web.Handler(), "/api/pin-sections")
    assertJSONContains(t, sections.Body.Bytes(), `"name":"Research"`, `"member_count":1`)

    unpinned := deleteURL(t, web.Handler(), "/api/session-pin?ref=local%3Asession-a")
    if unpinned.Code != http.StatusOK { t.Fatalf("unpin = %d: %s", unpinned.Code, unpinned.Body.String()) }

    got, err := store.Sections()
    if err != nil || len(got) != 1 || got[0].MemberCount != 0 { t.Fatalf("sections = %+v, %v", got, err) }
}
```

Cover:

- POST requires exactly one of `section_id` or `section_name`;
- POST rejects cluster, subagent, fork, malformed, and unknown session refs with `400`;
- unknown section ID returns `404`;
- empty/overlong names return `400`;
- case-folded create reuses and returns the canonical section;
- PATCH case-only rename succeeds, conflicting rename returns `409`, missing section returns `404`;
- DELETE section returns `member_count` and removes assignments;
- DELETE session-pin no-op returns `changed=false` and does not broadcast;
- changed mutation broadcasts one `serf/tree/changed`; failure and no-op broadcast zero;
- nil `PinSections` returns `500` without panic;
- non-allowed methods return `405`.

Extend `web_api_favorite_test.go` with:

```go
func TestAPIFavoriteRejectsSessionKindAfterNamedPins(t *testing.T) {
    rr := postFavorite(t, NewWebServer(hubcore.WebConfig{Favorite: hubcore.NewFavoriteStore(t.TempDir()+"/index.db")}), "session", "local:s1", true)
    if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "/api/session-pin") {
        t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
    }
}
```

Keep project-kind success tests unchanged.

- [ ] **Step 2: Run the API tests and verify RED**

```bash
go test ./cmd/serf-hub -run 'TestAPIPinSection|TestAPISessionPin|TestAPIFavoriteRejectsSession' -count=1
```

Expected: compile failures for missing handlers and Hub API types.

- [ ] **Step 3: Implement handler parsing and error mapping**

Define one store-error mapper:

```go
func writePinSectionError(w http.ResponseWriter, err error) {
    switch {
    case errors.Is(err, hubcore.ErrPinSectionName):
        writeAPIError(w, http.StatusBadRequest, err.Error())
    case errors.Is(err, hubcore.ErrPinSectionNotFound):
        writeAPIError(w, http.StatusNotFound, err.Error())
    case errors.Is(err, hubcore.ErrPinSectionConflict):
        writeAPIError(w, http.StatusConflict, err.Error())
    default:
        writeAPIError(w, http.StatusInternalServerError, "pin section store error: "+err.Error())
    }
}
```

Route collection and item paths separately:

```go
mux.HandleFunc("/api/pin-sections", s.handleAPIPinSections)
mux.HandleFunc("/api/pin-sections/", s.handleAPIPinSection)
mux.HandleFunc("/api/session-pin", s.handleAPISessionPin)
```

For POST, canonicalize the requested ref through `topLevelFavoriteSessionID` before any store write. Call `notifyMutation` only when the store returns `changed=true`. For DELETE unpin, return the canonical ref and `changed` even when no assignment existed.

- [ ] **Step 4: Add Hub API mirrors and project-only favorite behavior**

Add `PinSection`, `SessionPinAssignment`, and `SessionPinMutationResponse` to `hubapi/types.go`; add JSON round-trip assertions in `hubapi/client_test.go`. These are REST types, so do not touch `appwire`, `docs/appwire-protocol.md`, or `types.gen.ts`.

Change `handleAPIFavorite` to accept only `kind="project"`; keep the project store write, optimistic WebUI path, and notification behavior unchanged. Return:

```json
{"error":"session favorites moved to /api/session-pin"}
```

for `kind="session"`.

- [ ] **Step 5: Verify API behavior and commit**

```bash
gofmt -w cmd/serf-hub/web_api_pin_section.go cmd/serf-hub/web_api_pin_section_test.go cmd/serf-hub/web.go cmd/serf-hub/web_api_favorite.go cmd/serf-hub/web_api_favorite_test.go hubapi/types.go hubapi/client_test.go
go test ./cmd/serf-hub -run 'TestAPIPinSection|TestAPISessionPin|TestAPIFavorite' -count=1
go test ./hubapi -count=1
git diff --check
git add cmd/serf-hub/web_api_pin_section.go cmd/serf-hub/web_api_pin_section_test.go cmd/serf-hub/web.go cmd/serf-hub/web_api_favorite.go cmd/serf-hub/web_api_favorite_test.go hubapi/types.go hubapi/client_test.go
git commit -m "feat(hub): expose named session pin APIs"
```

---

### Task 3: Project named sections into the tree and preserve lifecycle integrity

**Files:**
- Modify: `cmd/serf-hub/web_api_tree.go`
- Modify: `cmd/serf-hub/web_api_tree_test.go`
- Modify: `cmd/serf-hub/web_api_tree_favorite_revalidation_test.go`
- Modify: `hubapi/types.go`
- Modify: `cmd/serf-hub/web_api_session_delete.go`
- Modify: `cmd/serf-hub/web_api_session_delete_test.go`
- Modify: `cmd/serf-hub/web_api_project_delete.go`
- Modify: `cmd/serf-hub/web_api_project_delete_test.go`

**Interfaces:**
- Consumes: `PinSectionStore.Assignments`, `Sections`, and `MigrateLegacy`; Task 2 API wire types.
- Produces:

```go
type PinSectionTree struct {
    ID       string     `json:"id"`
    Name     string     `json:"name"`
    Sessions []TreeNode `json:"sessions"`
}

type TreeResponse struct {
    // existing fields except Favorites
    PinSections []PinSectionTree `json:"pin_sections"`
}

type TreeNode struct {
    // existing fields
    PinSectionID string `json:"pin_section_id,omitempty"`
}
```

- Produces request-scoped helpers:

```go
func (s *WebServer) ensureLegacyPinsMigrated(now time.Time, authority hubcore.FavoriteAuthority) error
func classifySessionPins(assignments map[string]hubcore.SessionPin, authority hubcore.FavoriteAuthority) hubcore.FavoriteRevalidation
func pinSectionTrees(sections []hubcore.PinSection, assignments map[string]hubcore.SessionPin, visible map[hubcore.ArchiveKey]bool, nodes map[string]hubapi.TreeNode) []hubapi.PinSectionTree
```

- [ ] **Step 1: Write failing tree-shape and duplicate-membership tests**

Replace binary favorite expectations with exact section assertions:

```go
func TestAPITreeProjectsNamedPinSectionsAndDuplicateMembership(t *testing.T) {
    web, store := namedPinTreeWeb(t, []schema.SessionMeta{{ID: "s1", Title: "Alpha", UpdatedAt: time.Unix(20, 0)}})
    section, _, err := store.CreateOrReuseAndAssign("Research", "s1", time.Unix(1, 0))
    if err != nil { t.Fatal(err) }

    got := decodeTree(t, web)
    if len(got.PinSections) != 1 || got.PinSections[0].ID != section.ID { t.Fatalf("sections = %+v", got.PinSections) }
    if got.PinSections[0].Sessions[0].PinSectionID != section.ID { t.Fatalf("pinned row = %+v", got.PinSections[0].Sessions[0]) }
    live := findTreeNode(t, got.Live, "s1")
    if live.PinSectionID != section.ID { t.Fatalf("live duplicate = %+v", live) }
}
```

Add tests for:

- alphabetical section order using `Research`, `client`, and `Personal`;
- newest-first sessions inside one section;
- hidden empty sections omitted from `/api/tree` but retained by `GET /api/pin-sections`;
- a section with only dormant remote assignments omitted without deleting assignments;
- dormant remote assignment reappearing in its original section after source recovery;
- subagent/fork/cluster/confirmed-invalid assignment never rendering;
- project favorite flags still appearing on project rows;
- `TreeResponse` no longer serializing `favorites`;
- `pin_section_id` present on duplicate Live, Project, Test-run, and pinned-section copies of a session.

- [ ] **Step 2: Run tree tests and verify RED**

```bash
go test ./cmd/serf-hub -run 'TestAPITree.*PinSection|TestAPITreeProjectsNamedPinSections|TestAPITree.*Dormant' -count=1
go test ./hubapi -run 'Test.*Tree' -count=1
```

Expected: compile or assertion failures because `TreeResponse` still has `Favorites` and nodes lack `PinSectionID`.

- [ ] **Step 3: Implement request-scoped migration and authority classification**

In `handleAPITree`, use the authority returned by `memoTreeWithAuthority`. Before reading assignments:

1. load legacy favorite decisions;
2. classify them with `hubcore.ClassifyFavoriteDecisions(decisions, authority)`;
3. build `[]LegacyPinDecision` for every stored `kind="session"` row, preserving the original ID plus classification;
4. call `MigrateLegacy` once; if it changes storage, invalidate the tree cache inputs and continue from fresh pin-store reads;
5. continue using the same favorite decisions for project flags only.

Do not run migration during `NewWebServer`: authority is incomplete there. Do not classify a missing remote row as confirmed-invalid.

Classify stored assignments by adapting them to `ArchiveKey{Kind:"session", ID: sessionID}` and reuse `ClassifyFavoriteDecisions`. `Presentation` supplies renderable canonical identities; dormant classifications keep storage untouched.

- [ ] **Step 4: Build section wire groups and annotate every session copy**

Create one assignment lookup by canonical and stored aliases. Update `apiTreeNodeTier` or add a final recursive annotation helper so every node copy receives `PinSectionID`:

```go
func annotatePinSection(node hubapi.TreeNode, bySession map[string]string) hubapi.TreeNode {
    if sectionID := bySession[node.Ref]; sectionID != "" { node.PinSectionID = sectionID }
    if node.PinSectionID == "" { node.PinSectionID = bySession[node.SessionID] }
    for i := range node.Children { node.Children[i] = annotatePinSection(node.Children[i], bySession) }
    return node
}
```

Build a node index from authoritative top-level candidates, group only renderable assignments by stable section ID, sort sessions with `UpdatedAt.After`, drop zero-renderable groups from `/api/tree`, and sort groups by `strings.ToLower(name)` with section ID as the deterministic tie-breaker.

Remove `Favorites []TreeNode` from `hubapi.TreeResponse`; add `PinSections []PinSectionTree`. Update `isEmptyTree` consumers in Task 4 rather than retaining a compatibility field.

- [ ] **Step 5: Write failing delete-cleanup tests**

Add these tests with the existing session/project deletion harnesses and explicit postconditions:

```go
func TestAPISessionDeleteRemovesPinAssignmentButKeepsEmptySection(t *testing.T) {
    web, pinStore, sectionID, targetID := sessionDeleteWebWithPin(t)
    rr := deleteSessionRequest(t, web, "local:"+targetID)
    if rr.Code != http.StatusOK { t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String()) }
    pins, err := pinStore.Assignments()
    if err != nil || len(pins) != 0 { t.Fatalf("pins = %+v, %v", pins, err) }
    sections, err := pinStore.Sections()
    if err != nil || len(sections) != 1 || sections[0].ID != sectionID || sections[0].MemberCount != 0 {
        t.Fatalf("sections = %+v, %v", sections, err)
    }
}

func TestAPIProjectDeleteRemovesPinsOnlyForDeletedSessions(t *testing.T) {
    web, pinStore, deletedID, skippedID := projectDeleteWebWithPins(t)
    result := deleteProjectRequest(t, web)
    assertDeletedAndSkipped(t, result, deletedID, skippedID)
    pins, err := pinStore.Assignments()
    if err != nil { t.Fatal(err) }
    if _, ok := pins[deletedID]; ok { t.Fatalf("deleted assignment survived: %+v", pins) }
    if _, ok := pins[skippedID]; !ok { t.Fatalf("skipped assignment removed: %+v", pins) }
}

func TestAPIProjectDeleteReportsPinStoreCleanupError(t *testing.T) {
    web := projectDeleteWebWithFailingPinStore(t, errors.New("forced pin cleanup failure"))
    result := deleteProjectRequest(t, web)
    if !slices.ContainsFunc(result.DecisionErrors, func(s string) bool {
        return strings.Contains(s, "pin section store error: forced pin cleanup failure")
    }) { t.Fatalf("decision errors = %v", result.DecisionErrors) }
}
```

Add the small fixture helpers beside the existing `seedProjectDeleteDecisions` helper; they must use temp state roots and real stores, with only `openDB` replaced for the forced-error case. Assert delete handlers still clean archive and project-favorite decisions exactly as before.

- [ ] **Step 6: Implement lifecycle cleanup**

After a session is actually removed, call `PinSections.DeleteSession(canonicalSessionID)`. For project deletion, call it only for IDs in `result.Deleted`, never `result.Skipped`. Append failures to the existing `DecisionErrors` result in the same style as archive/favorite cleanup. Never delete a now-empty section.

Do not call `notifyMutation` from cleanup: session/project deletion already owns its one tree notification.

- [ ] **Step 7: Verify backend replacement and commit**

```bash
gofmt -w cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_tree_test.go cmd/serf-hub/web_api_tree_favorite_revalidation_test.go hubapi/types.go cmd/serf-hub/web_api_session_delete.go cmd/serf-hub/web_api_session_delete_test.go cmd/serf-hub/web_api_project_delete.go cmd/serf-hub/web_api_project_delete_test.go
go test ./cmd/serf-hub -run 'TestAPITree|TestAPISessionDelete|TestAPIProjectDelete' -count=1
go test ./hubapi -count=1
git diff --check
git add cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_tree_test.go cmd/serf-hub/web_api_tree_favorite_revalidation_test.go hubapi/types.go cmd/serf-hub/web_api_session_delete.go cmd/serf-hub/web_api_session_delete_test.go cmd/serf-hub/web_api_project_delete.go cmd/serf-hub/web_api_project_delete_test.go
git commit -m "feat(hub): project named pin sections in navigation"
```

---

### Task 4: Add frontend tree mirrors, requests, and optimistic projections

**Files:**
- Modify: `cmd/serf-hub/frontend/src/stores/tree.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/tree.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/actions.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/actions.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railNodes.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railNodes.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railPending.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railPending.test.ts`

**Interfaces:**
- Consumes: Task 2 REST endpoints and Task 3 tree JSON.
- Produces frontend types:

```ts
export interface PinSectionSummary {
  id: string;
  name: string;
  member_count: number;
}

export interface PinSectionTree {
  id: string;
  name: string;
  sessions: TreeNode[];
}

export interface TreeNode {
  // existing fields
  pin_section_id?: string;
}

export interface TreeResponse {
  // existing fields except favorites
  pin_sections: PinSectionTree[];
}
```

- Produces request functions:

```ts
export function listPinSections(): Promise<PinSectionSummary[]>;
export function assignSessionPin(ref: string, target: { section_id: string } | { section_name: string }): Promise<SessionPinMutationResponse>;
export function unpinSession(ref: string): Promise<{ ok: true; changed: boolean }>;
export function renamePinSection(id: string, name: string): Promise<PinSectionSummary>;
export function deletePinSection(id: string): Promise<{ ok: true; changed: boolean; member_count: number }>;
```

- Produces `PendingOp` variants:

```ts
| { kind: "sessionPin"; ref: string; section: PinSectionSummary }
| { kind: "sessionUnpin"; ref: string }
| { kind: "pinSectionRename"; id: string; name: string }
| { kind: "pinSectionDelete"; id: string }
```

- [ ] **Step 1: Write failing REST normalization tests**

In `stores/tree.test.ts`, decode a wire fixture with `pin_sections: null`, nested `sessions: null`, and `pin_section_id`; assert normalization produces arrays and preserves IDs. Remove the old `favorites` normalization assertion.

In `actions.test.ts`, assert exact requests:

```ts
await assignSessionPin("local:s1", { section_name: "Research" });
expect(fetch).toHaveBeenCalledWith("/api/session-pin", expect.objectContaining({
  method: "POST",
  body: JSON.stringify({ session_ref: "local:s1", section_name: "Research" }),
}));
```

Also assert encoded DELETE/PATCH URLs, same-origin credentials, successful response parsing, and propagation of JSON `{error}` messages.

- [ ] **Step 2: Run focused tests and verify RED**

```bash
npm --prefix cmd/serf-hub/frontend test -- src/stores/tree.test.ts src/shell/rail/actions.test.ts
```

Expected: TypeScript or assertion failures for missing pin-section types and actions.

- [ ] **Step 3: Implement hand-maintained REST mirrors and actions**

Update `WireTreeResponse` to normalize `pin_sections`, and recursively normalize each section’s session nodes. Delete `favorites` from frontend interfaces and `normalizeResponse`.

Factor the existing action fetch/error logic into a small local helper without changing project `setFavorite`:

```ts
async function requestJSON<T>(url: string, init: RequestInit): Promise<T> {
  const res = await fetch(url, { credentials: "same-origin", ...init });
  if (!res.ok) throw new Error(await parseActionError(res));
  return (await res.json()) as T;
}
```

Do not create a global section Zustand store. `listPinSections` is fetched by the picker on each open.

- [ ] **Step 4: Write failing section-node and optimistic-projection tests**

Add pure tests for:

- `pinSectionNodes(section, isExpanded)` preserving section session order;
- stable disclosure ID `pinsection:<opaque-id>` independent of the display name;
- `sessionPin` moving all duplicate copies’ `pin_section_id` and placing one row in the target section;
- pinning to a hidden empty section materializing that section optimistically;
- `sessionUnpin` removing the section row, clearing every duplicate membership flag, and hiding the section only when empty;
- rename preserving section ID and sessions while alphabetical rendering is handled by `Rail` sorting;
- delete removing the section and clearing all duplicate membership flags;
- project `favorite` overlays remaining unchanged.

Use a fixture with one session duplicated in `live`, `projects[0].sessions`, and `pin_sections[0].sessions`; assert every copy changes consistently.

- [ ] **Step 5: Implement pure rail node and pending helpers**

Add a section-specific node builder rather than representing sections as fake projects:

```ts
export function pinSectionNodes(section: PinSectionTree, isExpanded: ExpandedLookup): RailNode[] {
  return sessionNodes(section.sessions, isExpanded);
}

export function pinSectionDisclosureID(sectionID: string): string {
  return `pinsection:${sectionID}`;
}
```

In `applyPending`, centralize duplicate annotation with a recursive function keyed by `session.ref`. For a pending pin, copy the authoritative session node from any current tree tier, set `pin_section_id`, remove it from any previous pin section, and append it to the target only once. Keep ordering stable during the request; the authoritative refresh restores recent-activity order.

- [ ] **Step 6: Verify and commit frontend data layer**

```bash
npm --prefix cmd/serf-hub/frontend test -- src/stores/tree.test.ts src/shell/rail/actions.test.ts src/shell/rail/railNodes.test.ts src/shell/rail/railPending.test.ts
npm --prefix cmd/serf-hub/frontend run typecheck
npm --prefix cmd/serf-hub/frontend run lint
git diff --check
git add cmd/serf-hub/frontend/src/stores/tree.ts cmd/serf-hub/frontend/src/stores/tree.test.ts cmd/serf-hub/frontend/src/shell/rail/actions.ts cmd/serf-hub/frontend/src/shell/rail/actions.test.ts cmd/serf-hub/frontend/src/shell/rail/railNodes.ts cmd/serf-hub/frontend/src/shell/rail/railNodes.test.ts cmd/serf-hub/frontend/src/shell/rail/railPending.ts cmd/serf-hub/frontend/src/shell/rail/railPending.test.ts
git commit -m "feat(webui): model named pin sections"
```

---

### Task 5: Build the pin/move picker and named-section rail UX

**Files:**
- Create: `cmd/serf-hub/frontend/src/shell/rail/PinSectionPicker.tsx`
- Create: `cmd/serf-hub/frontend/src/shell/rail/PinSectionPicker.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailRow.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.module.css`

**Interfaces:**
- Consumes: Task 4 action functions, types, `PendingOp`, and existing `Dialog`, `Input`, `Button`, `Menu`, `Tree`, `Chevron`, and toast primitives.
- Produces:

```ts
export interface PinSectionPickerProps {
  session: TreeNode;
  currentSectionId?: string;
  mode: "pin" | "move";
  onAssign: (target: { section_id: string } | { section_name: string }, section?: PinSectionSummary) => Promise<void>;
  onUnpin?: () => Promise<void>;
  onClose: () => void;
}
```

- Extends `RailRowActions` with:

```ts
onPinSectionRequest(session: TreeNode): void;
onMovePinSectionRequest(session: TreeNode): void;
```

and removes session `onToggleFavorite`; project `onToggleFavoriteProject` remains.

- [ ] **Step 1: Write failing picker interaction and accessibility tests**

Create `PinSectionPicker.test.tsx` tests that:

1. fetch on every mount/open and show a loading state;
2. render hidden empty sections from `listPinSections` in alphabetical order;
3. label the current section with accessible text `Current section` and disable its selection;
4. call `onAssign({section_id: "id"}, summary)` for an existing section;
5. expose **New section…** inside the picker, switch to the name input, trim input, and call `onAssign({section_name: "Research"})`;
6. keep entered text and show an inline error when the request rejects;
7. reject empty and 81-code-point names before calling the API;
8. show **Unpin** only in move mode and call `onUnpin`;
9. close on Cancel and return focus through the existing Dialog behavior.

Use `userEvent` and role/name queries; do not assert CSS class strings.

- [ ] **Step 2: Run picker tests and verify RED**

```bash
npm --prefix cmd/serf-hub/frontend test -- src/shell/rail/PinSectionPicker.test.tsx
```

Expected: import failure because `PinSectionPicker.tsx` does not exist.

- [ ] **Step 3: Implement the picker as a focused dialog flow**

The row’s overflow menu contains one item, **Pin this session…** or **Move pinned session…**. Selecting it opens `PinSectionPicker`; the section choices and **New section…** live inside that flow, satisfying the approved “new section in the pin menu” behavior without adding nested popup menus.

Use a semantic list of buttons inside `Dialog`. Render the current section with both visible checkmark and `aria-current="true"`. Fetch sections in `useEffect` on mount; this component is mounted fresh on every open. Keep name mode local:

```ts
const normalized = name.trim();
const count = Array.from(normalized).length;
if (count === 0) setError("Section name is required");
else if (count > 80) setError("Section names must be 80 characters or fewer");
else await onAssign({ section_name: normalized });
```

Server errors remain authoritative and appear inline through `errorText`.

- [ ] **Step 4: Write failing rail row and section-heading tests**

Update `RailRow.test.tsx` to assert:

- unassigned top-level session menu label **Pin this session…**;
- assigned top-level session menu label **Move pinned session…**;
- nested, fork, and cluster rows have neither action;
- project rows still say **Add to pinned**/**Remove from pinned** and invoke `onToggleFavoriteProject`.

Update `Rail.test.tsx` with a tree containing `Client`, `Personal`, and `research` in shuffled wire order. Assert:

- headings render `Client`, `Personal`, `research` between Live and Projects in case-insensitive alphabetical order;
- there is no heading named `Pinned` unless a user section itself has that name;
- no **Add pinned section** control exists;
- collapse buttons expose `aria-expanded`, hide/show their own sessions independently, and persist `pinsection:<id>` through `railExpansion`;
- a rename preserves the disclosure key and reorders headings after refresh;
- heading overflow menus expose Rename and Delete;
- delete confirmation says `Delete “Client”? This will unpin 3 sessions.` using durable `member_count` from a fresh `listPinSections` call;
- confirmed delete projects `{kind:"pinSectionDelete"}` optimistically and refreshes tree plus picker data;
- the last unpinned row hides its section while a later picker still lists it;
- a live assigned session appears in both Live and its named section;
- mutation failure rolls back pending state and emits the existing error toast.

- [ ] **Step 5: Implement row menu changes and section rendering**

In `sessionMenuItems`, replace the favorite item only:

```ts
items.push({
  id: "pin-section",
  label: session.pin_section_id ? "Move pinned session…" : "Pin this session…",
  onSelect: () => session.pin_section_id
    ? actions.onMovePinSectionRequest(session)
    : actions.onPinSectionRequest(session),
});
```

Keep top-level validation and all rename/archive/delete entries unchanged.

Create a `PinnedRailSection` local component in `Rail.tsx` with one `<section>`, one disclosure button, one heading overflow `Menu`, and one `Tree`. Key disclosure state with `pinSectionDisclosureID(section.id)`. Sort a copied array:

```ts
const pinSections = [...tree.pin_sections].sort((a, b) =>
  a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id),
);
```

Do not mutate the Zustand tree object.

- [ ] **Step 6: Wire picker, rename, delete, and optimistic lifecycle**

Add Rail state for the picker target/mode, rename target/value/error, and delete target/member count. Reuse `runAction` for assignment, move, unpin, rename, and delete. For create-or-reuse, use the canonical section returned by the API to construct the pending projection; do not trust the typed display name as identity.

After every section mutation:

1. keep its `PendingOp` installed;
2. await `treeStore.getState().refresh()`;
3. let a later picker fetch `GET /api/pin-sections` on mount;
4. remove the pending operation.

For delete, fetch all summaries before opening confirmation so the count includes dormant members. If that fetch fails, show a toast and do not open a misleading confirmation.

Add only focused CSS classes for the section heading row, heading action, picker list, current marker, and inline validation. Reuse design tokens and existing rail spacing.

- [ ] **Step 7: Verify UX tests and commit**

```bash
npm --prefix cmd/serf-hub/frontend test -- src/shell/rail/PinSectionPicker.test.tsx src/shell/rail/RailRow.test.tsx src/shell/rail/Rail.test.tsx
npm --prefix cmd/serf-hub/frontend run typecheck
npm --prefix cmd/serf-hub/frontend run lint
npm --prefix cmd/serf-hub/frontend run build
git diff --check
git add cmd/serf-hub/frontend/src/shell/rail/PinSectionPicker.tsx cmd/serf-hub/frontend/src/shell/rail/PinSectionPicker.test.tsx cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx cmd/serf-hub/frontend/src/shell/rail/RailRow.test.tsx cmd/serf-hub/frontend/src/shell/rail/Rail.tsx cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx cmd/serf-hub/frontend/src/shell/rail/Rail.module.css
git commit -m "feat(webui): organize pinned sessions into named sections"
```

---

### Task 6: Replace legacy coverage, document the deterministic scenario, and run final gates

**Files:**
- Modify: `cmd/serf-hub/frontend/src/App.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/notifications/attention.test.ts`
- Modify: `cmd/serf-hub/frontend/src/notifications/index.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/welcome/Welcome.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/DockRegion.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailHost.test.tsx`
- Modify: `cmd/serf-hub/cov_final_session_tree_fuzz_test.go`
- Modify: `cmd/serf-hub/cov_session_residue_pass5_fuzz_test.go`
- Modify: `cmd/serf-hub/cov_exact_lifecycle_tree_fuzz_test.go`
- Modify: `cmd/serf-hub/cov_session_tree_pass6_fuzz_test.go`
- Modify: `cmd/serf-hub/cov_web_core_api_test.go`
- Modify: `test/scenarios/sidebar-favorite-pinned-across-reload.md`
- Verify: all files changed by Tasks 1–5.

**Interfaces:**
- Consumes: the complete named-section backend and frontend.
- Produces: no new production interface; this task proves replacement completeness and records reproducible browser acceptance steps.

- [ ] **Step 1: Find every stale binary session-favorite caller**

Run:

```bash
rg -n 'setFavorite\("session"|kind["'"']?:[[:space:]]*["'"']session|tree\.favorites|favorites:' cmd/serf-hub hubapi test --glob '*.{go,ts,tsx,md}'
```

Classify every hit:

- project favorite hits stay unchanged;
- session mutation hits move to `/api/session-pin` fixtures;
- tree fixture hits replace `favorites` with `pin_sections`;
- historical comments that describe removed behavior are rewritten or removed.

The command must return no active session-favorite caller after edits. Do not mass-replace `favorite`; project behavior intentionally remains.

- [ ] **Step 2: Mutation-prove the critical new tests**

Before final verification, temporarily make each mutation below, run its named focused test, confirm RED for the stated reason, then restore only the temporary mutation:

1. remove `PRAGMA foreign_keys = ON` → foreign-key test fails;
2. replace Unicode case folding with `strings.ToLower` → `Straße` reuse test fails;
3. skip annotation of Live duplicates → duplicate-membership tree/frontend test fails;
4. emit `notifyMutation` for `changed=false` → no-op notification test fails;
5. derive delete confirmation count from visible rows → dormant-member count test fails;
6. add an **Add pinned section** button → absence test fails.

Record the commands and observed failures in the implementation report or commit notes; do not leave mutation code in the workspace.

- [ ] **Step 3: Rewrite the deterministic browser scenario**

Update `test/scenarios/sidebar-favorite-pinned-across-reload.md` to use a fresh Hub state directory and dedicated Chrome profile. Specify these executable checks:

1. create **Client work** through **Pin this session… → New section…**;
2. pin a second session into **Client work** through the existing-section choice;
3. create **Research**, hard reload, and verify alphabetical top-level headings and durable assignments;
4. collapse **Research**, reload, and verify its disclosure state remains collapsed;
5. move one session from **Client work** to **Research** through **Move pinned session…**;
6. unpin the last **Client work** member and verify the empty section disappears;
7. open another session’s picker and verify hidden **Client work** remains selectable;
8. reuse **Client work**, rename it, and verify the section’s disclosure identity/state survives;
9. create a dormant remote assignment through the API fixture, verify the delete confirmation includes it in `member_count`, then cancel;
10. delete the visible section, confirm all members unpin, hard reload, and verify it stays gone;
11. favorite and unfavorite a project and verify project behavior is unchanged;
12. assert every `eval` still targets the scenario Hub’s expected `location.port`.

Use a raw API request plus hard reload for durability assertions so optimistic UI cannot provide a false green.

- [ ] **Step 4: Run focused package and frontend suites**

```bash
go test ./cmd/serf-hub/internal/hubcore -run 'PinSection|Favorite' -count=1
go test ./cmd/serf-hub -run 'TestAPI(PinSection|SessionPin|Tree|SessionDelete|ProjectDelete|Favorite)' -count=1
go test ./hubapi -count=1
npm --prefix cmd/serf-hub/frontend test -- src/stores/tree.test.ts src/shell/rail
npm --prefix cmd/serf-hub/frontend run typecheck
npm --prefix cmd/serf-hub/frontend run lint
npm --prefix cmd/serf-hub/frontend run build
```

Expected: every command exits zero. Read and fix all warnings and failures; do not weaken assertions or skip tests.

- [ ] **Step 5: Run repository verification**

```bash
make lint
make build
ROOT_FULL=1 make test
git diff --check
git status --short
```

If the rail CSS changes geometry beyond existing unit coverage, also run:

```bash
npm --prefix cmd/serf-hub/frontend run layoutguard
npm --prefix cmd/serf-hub/frontend run overflowguard
```

Expected: all commands exit zero. `git status --short` may still show only the pre-existing unrelated deletion plus intended Task 6 changes before commit.

- [ ] **Step 6: Commit scenario and compatibility updates**

Stage files explicitly from the stale-caller audit plus the scenario; never use `git add -A`:

```bash
git add \
  cmd/serf-hub/frontend/src/App.test.tsx \
  cmd/serf-hub/frontend/src/notifications/attention.test.ts \
  cmd/serf-hub/frontend/src/notifications/index.test.ts \
  cmd/serf-hub/frontend/src/panes/welcome/Welcome.test.tsx \
  cmd/serf-hub/frontend/src/shell/AppShell.test.tsx \
  cmd/serf-hub/frontend/src/shell/DockRegion.test.tsx \
  cmd/serf-hub/frontend/src/shell/rail/RailHost.test.tsx \
  cmd/serf-hub/cov_final_session_tree_fuzz_test.go \
  cmd/serf-hub/cov_session_residue_pass5_fuzz_test.go \
  cmd/serf-hub/cov_exact_lifecycle_tree_fuzz_test.go \
  cmd/serf-hub/cov_session_tree_pass6_fuzz_test.go \
  cmd/serf-hub/cov_web_core_api_test.go \
  test/scenarios/sidebar-favorite-pinned-across-reload.md
git diff --cached --name-only
git commit -m "test(webui): cover named pinned session sections"
```

Confirm `docs/superpowers/plans/2026-08-02-all-open-katas.md` is not staged.

- [ ] **Step 7: Run the canonical post-merge-equivalent gate**

```bash
make merge-approval-gate
```

Expected: `make lint`, `make build`, and `ROOT_FULL=1 make test` all pass serially. Report the exact command, exit status, task commits, and final `git status --short` without claiming the unrelated deletion is part of this work.
