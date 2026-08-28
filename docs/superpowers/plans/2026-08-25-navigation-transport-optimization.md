# Navigation Transport Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Web UI's repeated monolithic `/api/tree` transfer with bounded, revisioned navigation resources that rebuild once per semantic revision and revalidate only affected loaded resources.

**Architecture:** A per-`WebServer` `NavigationService` owns immutable tree projections, semantic resource revisions, single-flight construction, bounded representation caches, and typed AppWire invalidations. HTTP serves a small manifest plus bounded section, catalog, project, page, and session-location resources with ETag and gzip; the frontend uses one resource revalidator and resource-local last-good state rather than a monolithic tree refresh.

**Tech Stack:** Go 1.26, `net/http`, `encoding/json`, `compress/gzip`, `golang.org/x/sync/singleflight`, AppWire code generation, React 19, TypeScript 6, Zustand 5, Vitest 4, Biome 2.

**Spec:** `docs/superpowers/specs/2026-08-25-tree-transport-optimization-design.md`

## Global Constraints

- Read `docs/developing-evener/testing.md` before changing tests.
- Keep default tests deterministic and offline; use scripted AppWire/source boundaries.
- One `NavigationService` belongs to one `WebServer`; no global cache or cross-auth sharing.
- Hard limits: 64 sources; section/page limit 50; catalog limit 100; 2,000 session nodes; depth 32; 2 MiB session-resource JSON; 256 KiB manifest JSON; 512 KiB catalog JSON; uint32 offsets.
- String limits: title 200 runes; project/source/pin label and branch 512 runes; ref/opaque ID 1,024 bytes; working directory 4,096 bytes.
- Representation cache limits: 256 entries or 64 MiB estimated object + JSON + gzip bytes, whichever comes first.
- Frontend hydration concurrency is four; at most one request per representation may be in flight.
- AppWire sequence is transport-only. HTTP identity is `(resource key, generation_id, resource revision)`.
- Every navigation response uses `Cache-Control: private, no-cache`; matching `If-None-Match` returns bodyless 304; gzip varies on `Accept-Encoding`.
- Logs, metrics, and diagnostics never record raw URLs, query strings, keys, IDs, refs, titles, prompts, or paths.
- A failed refresh preserves last-good resource data and reports a resource-local stale/error state.
- The legacy `/api/tree` endpoint is a temporary adapter over `NavigationService`, never a second builder/cache.
- Before each frontend gate, run `npx biome check --write` on touched files under `cmd/evener-hub/frontend/src/`.
- Stage named paths only in every commit. Never use `git add .` or `git add -A`.

## File and Responsibility Map

### New backend files

- `hubapi/navigation.go` — public REST resource and mutation metadata types.
- `cmd/evener-hub/navigation_projection.go` — pure bounded projection from one immutable core snapshot.
- `cmd/evener-hub/navigation_projection_test.go` — shape, ordering, bounds, fingerprints, and location tests.
- `cmd/evener-hub/navigation_cache.go` — representation identity, single-flight, LRU, JSON/gzip variants.
- `cmd/evener-hub/navigation_cache_test.go` — cache isolation, eviction, concurrent miss, ETag identity tests.
- `cmd/evener-hub/navigation_service.go` — generation, sequence, resource revisions, coherent snapshot publication, clock scheduling.
- `cmd/evener-hub/navigation_service_test.go` — dependency matrix, no-op, race, last-good, and scheduler tests.
- `cmd/evener-hub/web_api_navigation.go` — route dispatch, validation, conditional/gzip HTTP responses.
- `cmd/evener-hub/web_api_navigation_test.go` — endpoint, security, header, paging, and redaction tests.
- `cmd/evener-hub/navigation_benchmark_test.go` — fixed legacy/new fixture and build/allocation/byte benchmarks.
- `cmd/evener-hub/testdata/navigation_legacy_baseline.json` — checked-in fixed-clock baseline measurements.

### New frontend files

- `cmd/evener-hub/frontend/src/stores/navigation/types.ts` — resource keys and local resource-state types derived from generated wire types.
- `cmd/evener-hub/frontend/src/stores/navigation/revalidator.ts` — one in-flight request per representation, target revisions, force tokens, ETags, aborts.
- `cmd/evener-hub/frontend/src/stores/navigation/revalidator.test.ts` — concurrency, stale response, 304, generation, and wildcard tests.
- `cmd/evener-hub/frontend/src/stores/navigation/store.ts` — manifest/section/catalog/project/location state and hydration actions.
- `cmd/evener-hub/frontend/src/stores/navigation/store.test.ts` — boot, paging, reconnect, failures, mutations, and no-idle-fetch tests.
- `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts` — resource-local selectors for rail, attention, titles, and menus.
- `cmd/evener-hub/frontend/src/stores/navigation/testing.ts` — deterministic resource fixtures for component tests.

### Existing files with focused changes

- `appwire/types.go`, `appwire/protocol.go` — capability and typed invalidation contract.
- `internal/appserver/server.go` — initialize response hook for navigation capability state.
- `cmd/evener-hub/app_rpc.go` — hub capability values.
- `cmd/evener-hub/web.go` — service ownership and navigation routes.
- `cmd/evener-hub/web_api_tree.go` — temporary adapter only; remove direct rebuild path.
- `cmd/evener-hub/main.go`, `main_background.go` — producer hooks and service lifecycle.
- `cmd/evener-hub/internal/hubcore/{archive,favorite,pin_section,past,roster,remotecache}.go` — content-gated producer signals.
- `hubapi/client.go`, `hubapi/types.go` — new resource methods and legacy compatibility.
- `cmd/evener-hub/frontend/src/protocol/types.gen.ts` — generated contract output.
- `cmd/evener-hub/frontend/src/shell/rail/{Rail,railNodes,RailRow,railPending}.tsx|ts` — resource-backed rail.
- `cmd/evener-hub/frontend/src/{notifications,shell,panes/session}/...` — reconnect, attention, deep-link, title, and menu consumers.

---

### Task 1: Freeze the Legacy Performance Baseline

**Files:**
- Create: `cmd/evener-hub/navigation_benchmark_test.go`
- Create: `cmd/evener-hub/testdata/navigation_legacy_baseline.json`
- Modify: `cmd/evener-hub/web_api_tree.go`
- Test: `cmd/evener-hub/navigation_benchmark_test.go`

**Interfaces:**
- Produces: `newNavigationBenchmarkFixture(tb testing.TB) *WebServer`
- Produces: `BenchmarkLegacyNavigationBaseline`
- Produces: fixed `hubNavigationNow func() time.Time` seam used only to make response timestamps deterministic.

- [ ] **Step 1: Write the failing deterministic fixture test**

Add a test that builds exactly 20 non-Git project directories and 50 current sessions per project, fixes the clock, calls `/api/tree`, and compares response bytes plus allocation metadata with the checked-in baseline schema:

```go
type navigationBaseline struct {
    ResponseBytes int64 `json:"response_bytes"`
    AllocsBytes   int64 `json:"allocs_bytes_per_op"`
    Projects      int   `json:"projects"`
    Sessions      int   `json:"sessions"`
}

func TestLegacyNavigationBaselineFixture(t *testing.T) {
    web := newNavigationBenchmarkFixture(t)
    body := requestLegacyTree(t, web)
    if !bytes.Contains(body, []byte(`"projects"`)) {
        t.Fatal("legacy fixture did not exercise project rows")
    }
    if got := countLegacySessions(t, body); got != 1000 {
        t.Fatalf("sessions=%d, want 1000", got)
    }
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `go test ./cmd/evener-hub -run TestLegacyNavigationBaselineFixture -count=1`

Expected: FAIL because the fixture helpers and fixed clock seam do not exist.

- [ ] **Step 3: Add the minimal fixture, benchmark, and baseline file**

Use IDs `session-%03d-%03d`, names built from the ID plus seven repetitions of `representative-title-data-`, and timestamps one minute apart under a fixed UTC time. The benchmark warms once, then measures the real handler:

```go
func BenchmarkLegacyNavigationBaseline(b *testing.B) {
    web := newNavigationBenchmarkFixture(b)
    _ = requestLegacyTree(b, web)
    b.ReportAllocs()
    b.ResetTimer()
    for range b.N {
        _ = requestLegacyTree(b, web)
    }
}
```

Record five 20-iteration samples and commit the median response bytes and `B/op` to the JSON baseline. Keep the observed 429 KB report in documentation only; the fixed fixture is the gate reference.

- [ ] **Step 4: Run fixture and benchmark verification**

Run:

```bash
go test ./cmd/evener-hub -run TestLegacyNavigationBaselineFixture -count=1
go test ./cmd/evener-hub -run '^$' -bench BenchmarkLegacyNavigationBaseline -benchtime=20x -count=5 -benchmem
```

Expected: test PASS; benchmark reports five samples and no live/network dependency.

- [ ] **Step 5: Commit the baseline**

```bash
git add cmd/evener-hub/navigation_benchmark_test.go cmd/evener-hub/testdata/navigation_legacy_baseline.json cmd/evener-hub/web_api_tree.go
git commit -m "test(hub): freeze navigation transport baseline"
```

### Task 2: Add Navigation Wire and AppWire Contracts

**Files:**
- Create: `hubapi/navigation.go`
- Create: `hubapi/navigation_test.go`
- Modify: `appwire/types.go`
- Modify: `appwire/protocol.go`
- Modify: `appwire/protocol_test.go`
- Modify: `internal/appserver/server.go`
- Modify: `internal/appserver/server_test.go`
- Modify: `cmd/evener-hub/app_rpc.go`
- Modify: `cmd/evener-hub/appwire_catalog_test.go`
- Generate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Produces: `hubapi.NavigationManifest`, `NavigationSectionResource`, `NavigationPinSectionCatalog`, `NavigationProjectCatalog`, `NavigationProjectResource`, `NavigationProjectPage`, `NavigationSessionLocation`, `NavigationMutation`.
- Produces: `appwire.NavigationCapability`, `NavigationInvalidationTarget`, `NavigationInvalidatedPayload`.
- Produces: `appwire.NotifyEvenerNavigationInvalidated`.

- [ ] **Step 1: Write contract tests before types**

Test exact JSON names, non-null arrays, target variants, and initialize capability version:

```go
func TestNavigationInvalidatedPayloadJSON(t *testing.T) {
    payload := appwire.NavigationInvalidatedPayload{
        GenerationID: "generation-a",
        Sequence: 7,
        Targets: []appwire.NavigationInvalidationTarget{{
            Kind: appwire.NavigationTargetProject,
            ProjectKey: "project-key",
            Revision: 3,
        }},
    }
    got, err := json.Marshal(payload)
    if err != nil { t.Fatal(err) }
    want := `{"generationId":"generation-a","sequence":7,"targets":[{"kind":"project","projectKey":"project-key","revision":3}]}`
    if string(got) != want { t.Fatalf("got %s, want %s", got, want) }
}
```

- [ ] **Step 2: Run contract tests and verify failure**

Run: `go test ./hubapi ./appwire ./internal/appserver ./cmd/evener-hub -run 'Test.*Navigation' -count=1`

Expected: compile FAIL because navigation types/constants are undefined.

- [ ] **Step 3: Define exact Go contracts and initialize capability**

Use named target-kind constants and one validated target struct:

```go
const (
    NavigationTargetManifest          = "manifest"
    NavigationTargetSection           = "section"
    NavigationTargetPinCatalog        = "pin_catalog"
    NavigationTargetPinSection        = "pin_section"
    NavigationTargetCatalog           = "catalog"
    NavigationTargetProject           = "project"
    NavigationTargetAllLoadedProjects = "all_loaded_projects"
)

type NavigationCapability struct {
    Version      int    `json:"version"`
    GenerationID string `json:"generationId"`
    Sequence     uint64 `json:"sequence"`
}
```

Add `Navigation *NavigationCapability `json:"navigation,omitempty"`` to `InitializeResponse`. Add the hubapi resource structs exactly as specified, including explicit empty slices and optional fields.

- [ ] **Step 4: Register the notification and regenerate TypeScript**

Add the notification catalog entry, then run:

```bash
make generate
go test ./hubapi ./appwire ./internal/appwirets ./internal/appserver ./cmd/evener-hub -run 'Test.*(Navigation|Catalog|Initialize)' -count=1
```

Expected: generated `types.gen.ts` includes capability, resource, target, and notification types; all focused tests PASS.

- [ ] **Step 5: Commit contracts and generated output**

```bash
git add hubapi/navigation.go hubapi/navigation_test.go appwire/types.go appwire/protocol.go appwire/protocol_test.go internal/appserver/server.go internal/appserver/server_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/appwire_catalog_test.go cmd/evener-hub/frontend/src/protocol/types.gen.ts
git commit -m "feat(protocol): define navigation resource contracts"
```

### Task 3: Build Pure Bounded Navigation Projections

**Files:**
- Create: `cmd/evener-hub/navigation_projection.go`
- Create: `cmd/evener-hub/navigation_projection_test.go`
- Modify: `cmd/evener-hub/web_api_tree.go`
- Modify: `cmd/evener-hub/internal/hubcore/tree.go`
- Test: `cmd/evener-hub/internal/hubcore/tree_test.go`

**Interfaces:**
- Consumes: Task 2 hubapi resource types.
- Produces: `buildNavigationProjection(inputs navigationBuildInputs) (navigationProjection, error)`.
- Produces: `navigationProjection.Resource(key navigationResourceKey) (any, navigationFingerprint, error)`.
- Produces: `navigationResourceBounds` constants and `NavigationSessionLocation` index.

- [ ] **Step 1: Write projection and hard-bound tests**

Cover stable order, exact current fields, no sessions in the manifest/catalogs, 50/100 row limits, uint32 offset behavior, 2,000-node/32-level/2 MiB truncation, string truncation, malformed identity refusal, and location summaries.

```go
func TestNavigationSectionAppliesRecursiveBounds(t *testing.T) {
    input := deepNavigationFixture(40)
    projection, err := buildNavigationProjection(input)
    if err != nil { t.Fatal(err) }
    section := projection.LivePage(0, 50)
    if !section.Truncated { t.Fatal("deep section must report truncation") }
    if got := countNavigationNodes(section.Sessions); got > maxNavigationNodes {
        t.Fatalf("nodes=%d, max=%d", got, maxNavigationNodes)
    }
}
```

- [ ] **Step 2: Run projection tests and verify failure**

Run: `go test ./cmd/evener-hub ./cmd/evener-hub/internal/hubcore -run 'TestNavigation(Project|Section|Manifest|Location|Bounds)' -count=1`

Expected: compile FAIL because projection functions do not exist.

- [ ] **Step 3: Implement the pure projector**

Move response shaping out of `handleAPITree` into pure functions that receive one immutable tree/live/decision/source snapshot. Derive row IDs, tier, pin membership, and display age at resource boundaries. Use one shared bounded traversal:

```go
type navigationTraversal struct {
    nodes int
    bytes int
    depth int
    truncated bool
}

func (p *navigationProjector) projectNodes(rows []hubcore.TreeNode, limit int) ([]hubapi.NavigationSessionSummary, int) {
    // Stop before every row/branch that would exceed count, depth, or byte bounds.
}
```

Do not call `Roster.Find`, SQLite stores, project resolution, or `time.Now` from recursive row projection.

- [ ] **Step 4: Run projection tests and legacy agreement tests**

Run:

```bash
go test ./cmd/evener-hub -run 'TestNavigation(Project|Section|Manifest|Location|Bounds|Projection)' -count=1
go test ./cmd/evener-hub/internal/hubcore -run 'Test.*Tree' -count=1
```

Expected: PASS; current tree ordering/lineage tests remain green.

- [ ] **Step 5: Commit the projector**

```bash
git add cmd/evener-hub/navigation_projection.go cmd/evener-hub/navigation_projection_test.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/tree_test.go
git commit -m "feat(hub): project bounded navigation resources"
```

### Task 4: Add the Single-Flight Representation Cache

**Files:**
- Create: `cmd/evener-hub/navigation_cache.go`
- Create: `cmd/evener-hub/navigation_cache_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: `navigationResourceKey` with canonical kind/key/tier/offset/limit identity.
- Produces: `navigationRepresentation` with object, JSON, gzip, weak ETag, generation, revision, and size estimate.
- Produces: `navigationRepresentationCache.Get(ctx, key, build)` and `Stats()`.

- [ ] **Step 1: Write cache identity, single-flight, and eviction tests**

```go
func TestNavigationCacheConcurrentMissBuildsOnce(t *testing.T) {
    cache := newNavigationRepresentationCache(256, 64<<20)
    var calls atomic.Int32
    build := func(context.Context) (navigationRepresentation, error) {
        calls.Add(1)
        return representationFixture("project:p1", 4), nil
    }
    runConcurrent(t, 20, func() { _, _ = cache.Get(t.Context(), projectKey("p1"), build) })
    if got := calls.Load(); got != 1 { t.Fatalf("build calls=%d, want 1", got) }
}
```

Test page identity changes with tier/offset/limit and encoding does not collide. Test 256-entry and 64 MiB LRU eviction.

- [ ] **Step 2: Run cache tests and verify failure**

Run: `go test ./cmd/evener-hub -run TestNavigationCache -count=1`

Expected: compile FAIL because cache types are undefined.

- [ ] **Step 3: Implement cache with `singleflight.Group` and LRU**

Use `container/list` for LRU and the existing `golang.org/x/sync/singleflight` dependency. Build JSON and gzip once. Weak ETag format is:

```go
func navigationETag(key navigationResourceKey, generation string, revision uint64) string {
    return fmt.Sprintf(`W/"nav-%s-%x-%d"`, generation, sha256.Sum256([]byte(key.String())), revision)
}
```

Keep exact keys out of metrics and logs.

- [ ] **Step 4: Run cache tests and race test**

Run:

```bash
go test ./cmd/evener-hub -run TestNavigationCache -count=1
go test -race ./cmd/evener-hub -run TestNavigationCache -count=1
```

Expected: PASS with one build under concurrent miss and bounded memory.

- [ ] **Step 5: Commit the cache**

```bash
git add cmd/evener-hub/navigation_cache.go cmd/evener-hub/navigation_cache_test.go go.mod go.sum
git commit -m "feat(hub): cache encoded navigation resources"
```

### Task 5: Implement `NavigationService` Revisions and Coherent Snapshots

**Files:**
- Create: `cmd/evener-hub/navigation_service.go`
- Create: `cmd/evener-hub/navigation_service_test.go`
- Modify: `cmd/evener-hub/web.go`
- Modify: `cmd/evener-hub/internal/hubcore/config.go`
- Modify: `cmd/evener-hub/internal/hubcore/treecache.go`

**Interfaces:**
- Consumes: Tasks 3–4 projector and cache.
- Produces: `newNavigationService(navigationServiceConfig) *NavigationService`.
- Produces: `Representation(ctx, navigationResourceKey) (navigationRepresentation, error)`.
- Produces: `Invalidate(navigationChangeHint)` for source-driven refresh and `Refresh(ctx, navigationChangeHint) (hubapi.NavigationMutation, error)` for mutation paths that must return committed targets.
- Produces: `Capability() appwire.NavigationCapability`, `Start(ctx)`, and `Stats()`.

- [ ] **Step 1: Write service revision/dependency/coherence tests**

Test no-op stability, exact dependent targets, `all_loaded_projects`, one core build for many resource misses, concurrent refresh, invalidation during build, 503 under context cancellation, last-good retention, generation isolation, and the next 24h/14d scheduler boundary.

```go
func TestNavigationServiceRejectsSnapshotInvalidatedDuringBuild(t *testing.T) {
    source := newBlockingNavigationSource()
    service := newTestNavigationService(source)
    done := make(chan navigationRepresentation, 1)
    go func() { rep, _ := service.Representation(t.Context(), manifestKey()); done <- rep }()
    source.WaitForCapture(t)
    service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
    source.Release()
    rep := <-done
    if rep.Revision != service.CurrentRevision(manifestKey().Semantic()) {
        t.Fatalf("published stale revision %d", rep.Revision)
    }
}
```

- [ ] **Step 2: Run service tests and verify failure**

Run: `go test ./cmd/evener-hub -run TestNavigationService -count=1`

Expected: compile FAIL because service APIs are undefined.

- [ ] **Step 3: Implement per-server service ownership**

Add `navigation *NavigationService` to `WebServer`; construct it after sources and before `appRPC`. The service captures source revisions, builds one core snapshot, rereads revisions before publish, and retries until context completion. Store resource fingerprints and increment only semantically changed resources. Start one next-boundary timer from main lifecycle context.

- [ ] **Step 4: Run service tests, race tests, and old cache tests**

Run:

```bash
go test ./cmd/evener-hub -run 'TestNavigationService|TestWeb.*TreeCache' -count=1
go test -race ./cmd/evener-hub -run TestNavigationService -count=1
go test ./cmd/evener-hub/internal/hubcore -run TestTreeCache -count=1
```

Expected: PASS. Legacy cache tests stay green until adapter retirement.

- [ ] **Step 5: Commit the service**

```bash
git add cmd/evener-hub/navigation_service.go cmd/evener-hub/navigation_service_test.go cmd/evener-hub/web.go cmd/evener-hub/internal/hubcore/config.go cmd/evener-hub/internal/hubcore/treecache.go
git commit -m "feat(hub): own revisioned navigation snapshots"
```

### Task 6: Serve Conditional Bounded Navigation HTTP Resources

**Files:**
- Create: `cmd/evener-hub/web_api_navigation.go`
- Create: `cmd/evener-hub/web_api_navigation_test.go`
- Create: `cmd/evener-hub/navigation_metrics.go`
- Modify: `cmd/evener-hub/web.go`
- Modify: `cmd/evener-hub/web_api.go`
- Modify: `cmd/evener-hub/http_recorder.go`
- Test: `cmd/evener-hub/http_recorder_test.go`

**Interfaces:**
- Consumes: `NavigationService.Representation`.
- Produces: manifest, live/needs-you/pin, pin catalog, project catalog, project root/page, and session-location handlers.
- Produces: `writeNavigationRepresentation(w, r, rep)`.

- [ ] **Step 1: Write table-driven route/header/security tests**

Test methods, limit/offset/tier validation, exact one-time path decoding, noncanonical encoding, absent keys, 50/100 limits, source cap, ETag/304, gzip, `Vary`, `Content-Length`, generation/revision headers, auth isolation, and redacted metrics.

```go
func TestNavigationManifestConditionalGzip(t *testing.T) {
    web := newNavigationHTTPTestServer(t)
    first := requestNavigation(t, web, "/api/navigation", "gzip", "")
    if first.Header().Get("Content-Encoding") != "gzip" { t.Fatal("missing gzip") }
    second := requestNavigation(t, web, "/api/navigation", "gzip", first.Header().Get("ETag"))
    if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
        t.Fatalf("conditional response=%d bytes=%d", second.Code, second.Body.Len())
    }
}
```

- [ ] **Step 2: Run HTTP tests and verify failure**

Run: `go test ./cmd/evener-hub -run 'TestNavigation.*(HTTP|Route|Conditional|Auth|Redact)' -count=1`

Expected: FAIL with 404 or undefined handler helpers.

- [ ] **Step 3: Implement routes and response writer**

Register `/api/navigation` and `/api/navigation/` under the existing auth/CSP/recorder stack. Parse canonical identities into `navigationResourceKey`. Never pass decoded values to filesystem APIs. Write cached identity or gzip bytes directly and emit only route-class metrics.

- [ ] **Step 4: Run HTTP, recorder, and security tests**

Run:

```bash
go test ./cmd/evener-hub -run 'TestNavigation|TestHTTPRequestRecorder' -count=1
go test -race ./cmd/evener-hub -run 'TestNavigation.*(HTTP|Auth)' -count=1
```

Expected: PASS; raw navigation key/ref values are absent from recorder/metric assertions.

- [ ] **Step 5: Commit HTTP resources**

```bash
git add cmd/evener-hub/web_api_navigation.go cmd/evener-hub/web_api_navigation_test.go cmd/evener-hub/navigation_metrics.go cmd/evener-hub/web.go cmd/evener-hub/web_api.go cmd/evener-hub/http_recorder.go cmd/evener-hub/http_recorder_test.go
git commit -m "feat(hub): serve conditional navigation resources"
```

### Task 7: Wire Typed Invalidation and Mutation Convergence

**Files:**
- Modify: `cmd/evener-hub/navigation_service.go`
- Modify: `cmd/evener-hub/main.go`
- Modify: `cmd/evener-hub/main_background.go`
- Modify: `cmd/evener-hub/web_api_tree.go`
- Modify: `cmd/evener-hub/web_api_archive.go`
- Modify: `cmd/evener-hub/web_api_favorite.go`
- Modify: `cmd/evener-hub/web_api_pin_section.go`
- Modify: `cmd/evener-hub/web_api_rename.go`
- Modify: `cmd/evener-hub/web_api_project_delete.go`
- Modify: `cmd/evener-hub/web_api_session_delete.go`
- Modify: `cmd/evener-hub/internal/hubcore/pin_section.go`
- Modify: `cmd/evener-hub/internal/hubcore/remotecache.go`
- Tests: matching `*_test.go` files for each producer/mutation.

**Interfaces:**
- Consumes: typed AppWire targets and `NavigationService.Refresh`.
- Produces: one content-gated `evener/navigation/invalidated` event per changed refresh.
- Produces: hub-owned REST mutation responses with `navigation` metadata.

- [ ] **Step 1: Write producer and duplicate-prevention tests**

Prove no-op roster/past/remote/store refresh emits no event, one real change emits one ordered event, unknown project scope emits `all_loaded_projects`, and a REST mutation returns the same target revisions later broadcast.

```go
func TestArchiveMutationReturnsAndBroadcastsSameNavigationTargets(t *testing.T) {
    web, client := newNavigationMutationServer(t)
    response := postArchive(t, web, "session", "local:s1", true)
    event := readNavigationInvalidation(t, client)
    if diff := cmp.Diff(response.Navigation.Targets, event.Targets); diff != "" {
        t.Fatalf("targets mismatch (-response +event):\n%s", diff)
    }
    assertNoSecondNavigationEvent(t, client)
}
```

- [ ] **Step 2: Run producer/mutation tests and verify failure**

Run: `go test ./cmd/evener-hub ./cmd/evener-hub/internal/hubcore -run 'Test.*Navigation.*(Invalidate|Mutation|NoOp|Remote|Pin)' -count=1`

Expected: FAIL because source hooks and mutation metadata are not wired.

- [ ] **Step 3: Add content-gated producer hooks and publication**

Compose existing roster/past/archive/favorite hooks with service refresh; add equivalent content-fingerprint hooks to pin sections and remote cache. Hub-owned REST mutations await `NavigationService.Refresh` and include its metadata. AppWire-routed daemon actions rely only on the hub invalidation event and do not synthesize navigation data in daemon result types.

- [ ] **Step 4: Run all mutation and notification tests**

Run:

```bash
go test ./cmd/evener-hub -run 'Test.*(Archive|Favorite|Pin|Rename|ProjectDelete|SessionDelete|Navigation)' -count=1
go test ./cmd/evener-hub/internal/hubcore -run 'Test.*(Archive|Favorite|Pin|Remote|Roster|Past)' -count=1
```

Expected: PASS with legacy `evener/tree/changed` tests retained only for adapter compatibility.

- [ ] **Step 5: Commit invalidation wiring**

```bash
git add cmd/evener-hub/navigation_service.go cmd/evener-hub/main.go cmd/evener-hub/main_background.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/web_api_archive.go cmd/evener-hub/web_api_favorite.go cmd/evener-hub/web_api_pin_section.go cmd/evener-hub/web_api_rename.go cmd/evener-hub/web_api_project_delete.go cmd/evener-hub/web_api_session_delete.go cmd/evener-hub/web_api_archive_test.go cmd/evener-hub/web_api_favorite_test.go cmd/evener-hub/web_api_pin_section_test.go cmd/evener-hub/web_api_rename_test.go cmd/evener-hub/web_api_project_delete_test.go cmd/evener-hub/web_api_session_delete_test.go cmd/evener-hub/navigation_service_test.go cmd/evener-hub/internal/hubcore/pin_section.go cmd/evener-hub/internal/hubcore/pin_section_test.go cmd/evener-hub/internal/hubcore/remotecache.go cmd/evener-hub/internal/hubcore/remotecache_test.go
git commit -m "feat(hub): publish scoped navigation invalidations"
```

### Task 8: Add Hub API Clients and the Thin Legacy Adapter

**Files:**
- Modify: `hubapi/client.go`
- Modify: `hubapi/client_test.go`
- Modify: `cmd/evener-hub/web_api_tree.go`
- Modify: `cmd/evener-hub/web_api_tree_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/treecache.go`
- Test: `cmd/evener-hub/web_api_tree_lastgood_test.go`

**Interfaces:**
- Produces: `Client.NavigationManifest`, `NavigationSection`, `NavigationPinSections`, `NavigationCatalog`, `NavigationProject`, `NavigationProjectPage`, `NavigationSessionLocation`.
- Produces: conditional request result `{NotModified bool, ETag string, Value T}`.
- Keeps: `Client.Tree` and `/api/tree` as deprecated compatibility adapters over `NavigationService`.

- [ ] **Step 1: Write client and adapter tests**

Test URL encoding, conditional headers, 304, typed errors, and prove repeated `/api/tree` adapter calls do not invoke legacy snapshot/build/project-resolution seams.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go test ./hubapi ./cmd/evener-hub -run 'Test.*NavigationClient|TestAPITree.*Adapter' -count=1`

Expected: compile FAIL because client methods and adapter assertions are missing.

- [ ] **Step 3: Implement conditional client helper and adapter**

Add a generic private `getConditional` helper and typed methods. Rewrite `handleAPITree` to project the legacy shape from the current immutable service snapshot and cache the encoded legacy representation by navigation generation/revision. Mark `Client.Tree` deprecated in its doc comment.

- [ ] **Step 4: Run client and legacy behavior suites**

Run:

```bash
go test ./hubapi -count=1
go test ./cmd/evener-hub -run 'Test.*(APITree|Navigation)' -count=1
```

Expected: PASS; legacy output semantics remain, but no request-time metadata scan/build occurs on cache hit.

- [ ] **Step 5: Commit client and adapter**

```bash
git add hubapi/client.go hubapi/client_test.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/web_api_tree_test.go cmd/evener-hub/web_api_tree_lastgood_test.go cmd/evener-hub/internal/hubcore/treecache.go
git commit -m "feat(hubapi): add navigation resource clients"
```

### Task 9: Implement the Frontend Resource Revalidator

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/navigation/types.ts`
- Create: `cmd/evener-hub/frontend/src/stores/navigation/revalidator.ts`
- Create: `cmd/evener-hub/frontend/src/stores/navigation/revalidator.test.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/testing/notifications.ts`

**Interfaces:**
- Consumes: generated navigation wire and invalidation types.
- Produces: `ResourceKey`, `ResourceState<T>`, `NavigationRevalidator`.
- Produces: `invalidate(target)`, `force(keys)`, `load(key, request)`, `resetGeneration(generationID)`.

- [ ] **Step 1: Write fake-fetch revalidator tests**

Cover one in-flight request, target revision raised mid-flight, one trailing request, newer response satisfying target, abort, stale discard, 304, `all_loaded_projects`, sequence-gap force token, and generation reset.

```ts
test("an invalidation during a request causes at most one trailing request", async () => {
  const harness = revalidatorHarness();
  const first = harness.load(projectRootKey("p1"));
  harness.invalidate({ kind: "project", projectKey: "p1", revision: 2 });
  harness.resolveNext(resourceResponse(1));
  await first;
  expect(harness.requests()).toHaveLength(2);
  harness.resolveNext(resourceResponse(2));
  await harness.settled();
  expect(harness.state(projectRootKey("p1")).revision).toBe(2);
});
```

- [ ] **Step 2: Run the test and verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/navigation/revalidator.test.ts --maxWorkers=1`

Expected: FAIL because modules do not exist.

- [ ] **Step 3: Implement the generic revalidator**

Use `AbortController`, ETag storage, `targetRevision`, and `forceToken`. Never set resource data to null on refresh error. Treat advertised capability protocol errors as errors; do not fall back.

- [ ] **Step 4: Format and run focused tests**

Run:

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/stores/navigation src/protocol/testing/notifications.ts
npx vitest run src/stores/navigation/revalidator.test.ts --maxWorkers=1
npm run typecheck
```

Expected: PASS.

- [ ] **Step 5: Commit the revalidator**

```bash
git add cmd/evener-hub/frontend/src/stores/navigation/types.ts cmd/evener-hub/frontend/src/stores/navigation/revalidator.ts cmd/evener-hub/frontend/src/stores/navigation/revalidator.test.ts cmd/evener-hub/frontend/src/protocol/testing/notifications.ts
git commit -m "feat(web): add navigation resource revalidator"
```

### Task 10: Implement the Frontend Navigation Store and Hydration

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/navigation/store.ts`
- Create: `cmd/evener-hub/frontend/src/stores/navigation/store.test.ts`
- Create: `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts`
- Create: `cmd/evener-hub/frontend/src/stores/navigation/testing.ts`
- Modify: `cmd/evener-hub/frontend/src/App.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/notifications/index.ts`
- Modify: `cmd/evener-hub/frontend/src/notifications/index.test.ts`

**Interfaces:**
- Consumes: Task 9 revalidator.
- Produces: `navigationStore`, `useNavigationStore`, `initNavigation(client)`, project/section/catalog/page loaders, location lookup, and reset helper.
- Produces: selectors for loaded global rows, project summaries/resources, attention summary, and session lookup.

- [ ] **Step 1: Write store boot/reconnect/failure tests**

Test capability absence fallback, capability version 1, manifest → first resources, concurrency four, empty-section skip, saved expansion hydration, no idle requests, typed invalidation, sequence gap, reconnect, 404 catalog recovery, and last-good errors.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/stores/navigation/store.test.ts src/notifications/index.test.ts --maxWorkers=1`

Expected: FAIL because store/init functions are undefined.

- [ ] **Step 3: Implement store and notification ownership**

Initialize from `InitializeResponse.navigation`. Fetch manifest first, then bounded visible resources under a four-request semaphore. Subscribe only to `evener/navigation/invalidated` for navigation. Keep `evener/attention/changed` in the dedicated alert state without navigation fetches. Capability absence calls the existing legacy store only during the migration release.

- [ ] **Step 4: Format and run store/App tests**

Run:

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/stores/navigation src/notifications/index.ts src/notifications/index.test.ts src/App.test.tsx
npx vitest run src/stores/navigation/store.test.ts src/notifications/index.test.ts src/App.test.tsx --maxWorkers=2
npm run typecheck
```

Expected: PASS; App test records no unexpected `/api/tree` on a version-1 server.

- [ ] **Step 5: Commit the store**

```bash
git add cmd/evener-hub/frontend/src/stores/navigation/store.ts cmd/evener-hub/frontend/src/stores/navigation/store.test.ts cmd/evener-hub/frontend/src/stores/navigation/selectors.ts cmd/evener-hub/frontend/src/stores/navigation/testing.ts cmd/evener-hub/frontend/src/App.test.tsx cmd/evener-hub/frontend/src/notifications/index.ts cmd/evener-hub/frontend/src/notifications/index.test.ts
git commit -m "feat(web): hydrate scoped navigation resources"
```

### Task 11: Migrate the Rail to Resource-Local Data

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railPending.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railPending.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railController.ts`
- Test: `cmd/evener-hub/frontend/src/shell/rail/railController.test.ts`

**Interfaces:**
- Consumes: Task 10 selectors/loaders.
- Produces: pure `NavigationRailModel` consumed by `railNodes`.
- Preserves: expansion persistence, overflow rows, optimistic mutations, row/menu behavior, and rendering CSS.

- [ ] **Step 1: Rewrite pure projection tests first**

Express fixtures as manifest + section/catalog/project resources. Prove Live, pins, project tiers, archived/test groups, expansion, bounded page overflow, and stale project rows match current visible semantics.

- [ ] **Step 2: Run rail projection/component tests and verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/shell/rail/railNodes.test.ts src/shell/rail/Rail.test.tsx --maxWorkers=2`

Expected: type/test FAIL because rail still consumes `TreeResponse`.

- [ ] **Step 3: Implement resource-backed rail and mutation convergence**

Replace `treeStore` selectors with `navigationStore` selectors. Project expansion loads one project root; overflow loads one canonical page. Mutation success feeds returned navigation targets to the store, removes optimistic state after target satisfaction, and never calls a whole-tree refresh. AppWire-routed actions wait for invalidation only.

- [ ] **Step 4: Format and run the complete rail suite**

Run:

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/shell/rail
npx vitest run src/shell/rail --maxWorkers=2
npm run typecheck
```

Expected: PASS with no CSS changes unless a failing browser/geometry test requires one.

- [ ] **Step 5: Commit the rail migration**

```bash
git add cmd/evener-hub/frontend/src/shell/rail/Rail.tsx cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx cmd/evener-hub/frontend/src/shell/rail/railNodes.ts cmd/evener-hub/frontend/src/shell/rail/railNodes.test.ts cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx cmd/evener-hub/frontend/src/shell/rail/railPending.ts cmd/evener-hub/frontend/src/shell/rail/railPending.test.ts cmd/evener-hub/frontend/src/shell/rail/railController.ts cmd/evener-hub/frontend/src/shell/rail/railController.test.ts
git commit -m "feat(web): render rail from navigation resources"
```

### Task 12: Migrate Deep Links, Menus, Titles, and Attention

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.edge.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/threadTitle.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/threadTitle.test.ts`
- Modify: `cmd/evener-hub/frontend/src/notifications/attention.ts`
- Modify: `cmd/evener-hub/frontend/src/notifications/attention.test.ts`
- Modify: `cmd/evener-hub/frontend/src/notifications/title.ts`
- Modify: `cmd/evener-hub/frontend/src/notifications/title.test.ts`
- Modify: `cmd/evener-hub/frontend/src/notifications/favicon.ts`
- Modify: `cmd/evener-hub/frontend/src/notifications/favicon.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/DockHost.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/DockHost.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/welcome/Welcome.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/welcome/Welcome.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/palette/CommandPalette.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/palette/CommandPalette.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/needsYouCycle.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/needsYouCycle.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/mobile/TreeDrawer.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/mobile/TreeDrawer.test.tsx`

**Interfaces:**
- Consumes: `NavigationSessionLocation` and Task 10 selectors.
- Removes: project-tree ancestry scans and direct `findSessionNode` dependency.
- Preserves: pane placement, needs-you cycling, menu eligibility/state, title fallback, and alert edge-fire semantics.

- [ ] **Step 1: Write missing-location and collapsed-project tests**

Prove a deep-linked child opens under its top-level owner without loading all projects; SessionChrome gets tier/pin/session summary from location; title falls back to location summary; attention notification updates counts without any navigation fetch.

- [ ] **Step 2: Run focused consumer tests and verify failure**

Run:

```bash
cd cmd/evener-hub/frontend
npx vitest run src/shell/AppShell.test.tsx src/panes/session/chrome/SessionChrome.test.tsx src/notifications/attention.test.ts src/notifications/title.test.ts --maxWorkers=2
```

Expected: FAIL because consumers still scan `TreeResponse`.

- [ ] **Step 3: Replace scans with location/resource selectors**

Use the location endpoint on route/session demand; cache successful locations by generation/revision. Needs-you cycling, the command palette, Welcome, rail/mobile badges, and favicon/title state read bounded navigation resources. Title and menu code prefer live thread data, then location/session summary. Do not trigger project expansion solely for a title or menu.

- [ ] **Step 4: Format and run shell/notification suites**

Run:

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/shell/AppShell.tsx src/panes/session/chrome src/panes/session/threadTitle.ts src/notifications src/shell/mobile
npx vitest run src/shell/AppShell.test.tsx src/shell/DockHost.test.tsx src/panes/session/chrome src/panes/session/Session.test.tsx src/panes/session/threadTitle.test.ts src/panes/welcome src/notifications src/shell/palette/CommandPalette.test.tsx src/shell/rail/needsYouCycle.test.ts src/shell/sessionMenu/SessionMenu.test.tsx src/shell/mobile/TreeDrawer.test.tsx --maxWorkers=2
npm run typecheck
```

Expected: PASS.

- [ ] **Step 5: Commit secondary consumer migration**

```bash
git add cmd/evener-hub/frontend/src/shell/AppShell.tsx cmd/evener-hub/frontend/src/shell/AppShell.test.tsx cmd/evener-hub/frontend/src/shell/DockHost.tsx cmd/evener-hub/frontend/src/shell/DockHost.test.tsx cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.edge.test.tsx cmd/evener-hub/frontend/src/panes/session/Session.tsx cmd/evener-hub/frontend/src/panes/session/Session.test.tsx cmd/evener-hub/frontend/src/panes/session/threadTitle.ts cmd/evener-hub/frontend/src/panes/session/threadTitle.test.ts cmd/evener-hub/frontend/src/panes/welcome/Welcome.tsx cmd/evener-hub/frontend/src/panes/welcome/Welcome.test.tsx cmd/evener-hub/frontend/src/notifications/attention.ts cmd/evener-hub/frontend/src/notifications/attention.test.ts cmd/evener-hub/frontend/src/notifications/title.ts cmd/evener-hub/frontend/src/notifications/title.test.ts cmd/evener-hub/frontend/src/notifications/favicon.ts cmd/evener-hub/frontend/src/notifications/favicon.test.ts cmd/evener-hub/frontend/src/shell/palette/CommandPalette.tsx cmd/evener-hub/frontend/src/shell/palette/CommandPalette.test.tsx cmd/evener-hub/frontend/src/shell/rail/needsYouCycle.ts cmd/evener-hub/frontend/src/shell/rail/needsYouCycle.test.ts cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.tsx cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.test.tsx cmd/evener-hub/frontend/src/shell/mobile/TreeDrawer.tsx cmd/evener-hub/frontend/src/shell/mobile/TreeDrawer.test.tsx
git commit -m "feat(web): use explicit navigation session locations"
```

### Task 13: Prove Integration, Observability, and Performance Budgets

**Files:**
- Modify: `cmd/evener-hub/navigation_benchmark_test.go`
- Modify: `cmd/evener-hub/navigation_metrics.go`
- Create: `cmd/evener-hub/navigation_integration_test.go`
- Modify: `cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx`
- Modify: `test/scenarios/sidebar-expand-survives-live-resync.md`
- Modify: `test/scenarios/sidebar-archived-testruns-reachability.md`
- Modify: `test/scenarios/local-sidebar-url-stability.md`
- Modify: `test/scenarios/sidebar-project-delete-full-cycle.md`
- Modify: `test/scenarios/sidebar-rename-live-and-ended.md`
- Modify: `test/scenarios/attention-needs-you-end-to-end.md`
- Test: `cmd/evener-hub/frontend/src/App.test.tsx`

**Interfaces:**
- Consumes: complete server/client resource flow.
- Produces: deterministic mandatory/expanded hydration measurements and diagnostics counters.

- [ ] **Step 1: Add failing end-to-end request-count and performance assertions**

Use a scripted source and real AppWire/HTTP paths. Assert idle zero requests; one status change affects only named loaded representations; mutation/event dedupe; reconnect/gap conditional revalidation; one build/resolution pass; and fixed budgets (15% uncompressed mandatory, 10% gzip mandatory, 35% gzip four-project expansion, 20% legacy `B/op`).

- [ ] **Step 2: Run integration/benchmark tests and inspect failures**

Run:

```bash
go test ./cmd/evener-hub -run TestNavigationIntegration -count=1
go test ./cmd/evener-hub -run TestNavigationPerformanceBudgets -count=1
go test ./cmd/evener-hub -run '^$' -bench 'Benchmark(Legacy|Navigation)' -benchtime=20x -count=5 -benchmem
```

Expected before final tuning: tests identify exact over-budget resources or duplicate request/build counts; do not loosen thresholds.

- [ ] **Step 3: Fix root causes and expose redacted diagnostics**

Use cached bytes for every hit, remove any accidental project/global refresh, and ensure diagnostics aggregate only resource class/status/bytes/duration/counter values. Update browser scenario assertions to inspect request counts and preserved behavior.

- [ ] **Step 4: Run integration, frontend, and browser guards**

Run:

```bash
go test ./cmd/evener-hub -run 'TestNavigation(Integration|Performance|Metrics)' -count=1
cd cmd/evener-hub/frontend
npx biome check --write src/dev/shellguard-entry.tsx src
npm test
cd ../../../..
make test-web-browser
```

Expected: PASS. If Chrome is unavailable, report the exact prerequisite; do not claim the browser gate passed.

- [ ] **Step 5: Commit proof and diagnostics**

```bash
git add cmd/evener-hub/navigation_benchmark_test.go cmd/evener-hub/navigation_metrics.go cmd/evener-hub/navigation_integration_test.go cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx test/scenarios/sidebar-expand-survives-live-resync.md test/scenarios/sidebar-archived-testruns-reachability.md test/scenarios/local-sidebar-url-stability.md test/scenarios/sidebar-project-delete-full-cycle.md test/scenarios/sidebar-rename-live-and-ended.md test/scenarios/attention-needs-you-end-to-end.md
git commit -m "test: prove scoped navigation performance"
```

### Task 14: Isolate the Legacy Adapter and Run Final Gates

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/navigation/legacyAdapter.ts`
- Create: `cmd/evener-hub/frontend/src/stores/navigation/legacyAdapter.test.ts`
- Delete: `cmd/evener-hub/frontend/src/stores/tree.ts`
- Delete or migrate: `cmd/evener-hub/frontend/src/stores/tree.test.ts`
- Delete or migrate: `cmd/evener-hub/frontend/src/stores/tree.edge.test.ts`
- Create: `cmd/evener-hub/frontend/src/stores/navigation/noLegacyImports.test.ts`
- Modify: production/test files named by that static test until only `legacyAdapter.ts` references `/api/tree`.
- Modify: `docs/superpowers/specs/2026-08-25-tree-transport-optimization-design.md` with measured results and migration-release adapter status.

**Interfaces:**
- Removes: frontend `TreeResponse` authority, `refreshGeneration`, `inflightRefresh`, 250 ms tree debounce, stale client tree callbacks, and direct `/api/tree` fetches on capability version 1.
- Retains: one capability-absent `legacyAdapter.ts`, plus thin server `/api/tree` and `hubapi.Client.Tree` adapters for the declared migration release only.

- [ ] **Step 1: Add a static legacy-reference gate**

Add a filesystem test that permits `/api/tree` only in
`stores/navigation/legacyAdapter.ts`, forbids production imports from
`stores/tree`, and forbids `evener/tree/changed`/`REFRESH_NOTIFICATIONS` in the
version-1 navigation path.

- [ ] **Step 2: Run the static gate and verify failure**

Run: `rg -n 'stores/tree|/api/tree|evener/tree/changed|REFRESH_NOTIFICATIONS' cmd/evener-hub/frontend/src appwire cmd/evener-hub hubapi`

Expected: current legacy store/imports appear and must be migrated or explicitly allowlisted.

- [ ] **Step 3: Delete old frontend authority and migrate remaining tests/fixtures**

Move reusable fixture builders to `stores/navigation/testing.ts`. Move only the
one-shot legacy fetch/normalization needed for capability absence into
`legacyAdapter.ts`; it owns no notification subscription, reconnect handler,
timer, or global store. Remove the old store and refresh list. Run the static
test repeatedly until it reports no legacy production import. Record the
migration-release adapter and its explicit next-release removal gate in the
spec results section.

- [ ] **Step 4: Run formatting and canonical gates**

Run in order:

```bash
cd cmd/evener-hub/frontend
npx biome check --write src
cd ../../../..
make lint
make vet
make test
make test-web-browser
git diff --check
```

Expected: every command exits zero. A timeout, sandbox denial, or missing Chrome leaves that gate incomplete and must be reported exactly.

- [ ] **Step 5: Review changed files and commit cleanup**

```bash
git status --short
git diff --stat
mapfile -t cleanup_paths < <(git diff --name-only --diff-filter=ACDMRT)
printf '%s\n' "${cleanup_paths[@]}"
for path in "${cleanup_paths[@]}"; do
  case "$path" in
    cmd/evener-hub/frontend/src/*|docs/superpowers/specs/2026-08-25-tree-transport-optimization-design.md) ;;
    *) echo "unexpected cleanup path: $path" >&2; exit 1 ;;
  esac
done
git add -- "${cleanup_paths[@]}"
git commit -m "refactor(web): retire monolithic tree refreshes"
```

Verify the staged list contains only navigation implementation paths before committing. Do not stage unrelated workspace edits.

## Post-Implementation Review and Delivery

After every task, the subagent-driven executor must run a two-stage review:

1. requirements/spec compliance for that task;
2. code quality, race, security, and maintainability review.

Fix Critical and Important findings before starting the next task. After Task 14, run `superpowers:verification-before-completion`, inspect the actual network/resource behavior against every acceptance criterion, and report:

- commits created;
- files changed;
- focused and full gate results;
- deterministic and live byte/request/allocation/build measurements;
- adapter/fallback status and explicit removal release; and
- any gate that could not run.
