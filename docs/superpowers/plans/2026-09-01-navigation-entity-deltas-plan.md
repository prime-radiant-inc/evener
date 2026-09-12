# Navigation Entity Deltas Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in navigation representation v2 with exact-base cumulative entity/container deltas, bounded snapshot fallback, deletion tombstones, structurally shared frontend reconciliation, and reconnect-safe generation fencing while retaining representation v1 for old clients.

**Architecture:** `NavigationService` remains the only projection, generation, resource-revision, and invalidation authority. The existing `evener/navigation/read` method negotiates representations through the capability’s `ReadVersions: []int{1, 2}`: omitted/version-1 requests retain the current snapshot contract, while version 2 uses stateless normalized resource-scoped entities, complete ordering containers, exact base tuples, cumulative semantic deltas, and bounded history. Entity/container records have no numeric revision. The frontend explicitly requests v2, validates and applies each response transactionally, and memoizes normalized selectors and rail rows.

**Tech Stack:** Go 1.27, AppWire JSON-RPC/WebSocket, AppWire TypeScript generation, React 19, TypeScript 6, Zustand 5, Vitest 4, Biome 2, headless Chrome shellguard.

**Spec:** `docs/superpowers/specs/2026-09-01-navigation-entity-deltas-design.md`

**Execution status:** Tasks 1–8 below record the original implementation sequence
from the historical `679c08a29eb60986a376e7472164d209c15149fb`
baseline. They are already represented in the current redesign baseline
`383c93802b1ead5816155788cb5800fce59e2ce7` and must not be rerun or used to
create duplicate files. The only executable next work is Slice D and its final
verification in
`docs/superpowers/plans/2026-09-01-navigation-entity-deltas-unblock-plan.md`.
The interface excerpts below describe contracts that Slice D must preserve,
not current red boundaries.

## Global Constraints

- Read `docs/developing-evener/testing.md` before changing tests.
- Keep default tests deterministic and offline. Use scripted navigation sources, AppWire transports, fake clients, explicit promises/channels, and clock seams; do not add sleeps.
- Preserve the capability envelope at `Version: 1` and advertise `ReadVersions: []int{1, 2}`. Do not bump the AppWire protocol solely for this additive representation negotiation.
- An omitted `representationVersion` means representation v1. New frontend reads send `representationVersion: 2` explicitly.
- Preserve v1 request/response behavior: `etag`, `status: "ok" | "not_modified"`, and `data` remain valid for old clients.
- V2 accepts only an exact `NavigationReadBase` `(generationId, revision, etag)`. A missing base is the valid initial-read case; a present but partial, null-valued, unsafe, or extraneous base is invalid.
- V2 exact-current base returns `not_modified`; an exact retained older base returns one cumulative delta; initial, unknown, evicted, future, wrong-generation, or wrong-ETag bases return a full snapshot; a known removed resource returns `gone`.
- Every changed ordering container carries its complete current child list and is applied with all entity/container changes in one frontend transaction.
- Retain immutable normalized snapshots under the existing server-global navigation representation-cache bounds: at most 256 entries or 64 MiB estimated object plus JSON bytes per `NavigationService`; eviction causes full fallback, never an error.
- Preserve current navigation bounds: 64 sources; section/project-page limit 50; catalog limit 100; uint32 offsets; 2,000 session nodes; depth 32; 2 MiB session resource; 256 KiB manifest; 512 KiB catalog; representation cache 256 entries/64 MiB.
- Keep resource revisions and invalidation sequences within `Number.MAX_SAFE_INTEGER`.
- Do not add or accept per-entity/per-container numeric revisions. Key presence,
  entity kind/value, and container owner/complete child order determine semantic
  record changes.
- Preserve `commitTargetsLocked`, mutation receipts, invalidation sequence increments, publication FIFO, `DrainPublications`, and `BroadcastAll` fan-out behavior.
- Entity identity is scoped to one canonical resource representation. Do not add a global cross-resource entity store.
- Generated files change only through `make generate`.
- Before frontend gates, run `npx biome check --write` on the named touched paths under `frontend/src/`. Do not include `frontend/scripts/` in explicit Biome invocations; that directory is outside the enforced scope documented in `AGENTS.md`.
- Stage only named paths. Never use `git add .` or `git add -A`.

## File and Responsibility Map

### Backend files added by the historical implementation (now existing)

- `hubapi/navigation_delta.go` — normalized entity, ordering-container, metadata, snapshot, and delta wire types plus validation.
- `hubapi/navigation_test.go` — JSON shape, non-null arrays, union, key, and reference validation.
- `cmd/evener-hub/navigation_normalize.go` — pure conversion from existing bounded resource objects to canonical normalized snapshots.
- `cmd/evener-hub/navigation_normalize_test.go` — deterministic entity/container order and shallow session normalization.
- `cmd/evener-hub/navigation_delta.go` — pure base-to-current cumulative differ.
- `cmd/evener-hub/navigation_delta_test.go` — entity changes, deletion, reorder, reparent, and tombstone cases.
- `cmd/evener-hub/navigation_history.go` — exact-base lookup sharing the existing 256-entry/64 MiB representation-cache policy.
- `cmd/evener-hub/navigation_history_test.go` — tuple matching, accounting, eviction, and concurrency.

### Frontend files added by the historical implementation (now existing)

- `cmd/evener-hub/frontend/src/stores/navigation/codec.ts` — strict v2 envelope/snapshot/delta decoding.
- `cmd/evener-hub/frontend/src/stores/navigation/codec.test.ts` — status exclusivity, bounds, unions, and dangling-reference rejection.
- `cmd/evener-hub/frontend/src/stores/navigation/merge.ts` — transactional structural-sharing snapshot/delta reconciliation.
- `cmd/evener-hub/frontend/src/stores/navigation/merge.test.ts` — equal fallback, keyed changes, reparent, deletion, and tombstone tests.
- `cmd/evener-hub/frontend/src/stores/navigation/selectors.test.ts` — selector/model reference-stability tests.
- `cmd/evener-hub/frontend/src/shell/rail/railRenderObserver.tsx` — nullable render observer context used by tests and shellguard.

### Existing files with focused changes

- `appwire/types.go`, `appwire/protocol.go`, and tests — additive capability/read-version contracts.
- `internal/appwirets/emit.go`, `internal/appwirets/emit_test.go` — normalized hubapi generator roots.
- `docs/appwire-protocol.md`, `cmd/evener-hub/frontend/src/protocol/types.gen.ts` — generated output.
- `cmd/evener-hub/navigation_cache.go`, `navigation_service.go`, `app_navigation.go`, and tests — normalized representations, history, v1/v2 dispatch, and tombstone reads.
- `cmd/evener-hub/main.go`, `navigation_publisher_test.go` — unchanged FIFO `BroadcastAll` behavior assertions.
- `cmd/evener-hub/frontend/src/protocol/client.ts`, reconnect tests, and fake client/socket files — newest `InitializeResponse` on every ready transition.
- `cmd/evener-hub/frontend/src/stores/navigation/{types,revalidator,store,testing}.ts` and tests — v2 bases, merge, lifecycle fencing, and boot.
- `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts` — memoized normalized selectors.
- `cmd/evener-hub/frontend/src/shell/rail/{Rail,RailRow,railNodes}.tsx|ts` and tests — stable models, callbacks, nodes, and memoized rows.
- `cmd/evener-hub/frontend/src/widgets/tree/index.tsx`, `tree.test.tsx` — stable row-info objects with current callbacks.
- `cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx`, `cmd/evener-hub/frontend/scripts/shellguard/run.mjs` — real-browser render-count and geometry proof.

---

### Historical Task 1: Deliver the Latest Initialize Result on Reconnect

**Files:**
- Modify: `cmd/evener-hub/frontend/src/protocol/client.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/client.test.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/reconnect.test.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/testing/fakeClient.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/testing/fakeClient.test.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/testing/fakeSocket.ts`

**Interfaces:**
- Changes: `onReady(cb: (initialize: InitializeResponse) => void)` supplies the newest handshake result.

- [ ] **Step 1: Write the reconnect regression**

```ts
test("reconnect delivers generation B instead of cached generation A", async () => {
  const harness = reconnectHarness();
  const generations: string[] = [];
  harness.client.onReady((value) => generations.push(value.navigation?.generationId ?? "missing"));
  await harness.connect({ version: 1, generationId: "a", sequence: 0, readVersions: [1, 2] });
  await harness.reconnect({ version: 1, generationId: "b", sequence: 0, readVersions: [1, 2] });
  expect(generations).toEqual(["a", "b"]);
  expect((await harness.client.connect()).navigation?.generationId).toBe("b");
});
```

- [ ] **Step 2: Run focused tests and verify failure**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/protocol/client.test.ts src/protocol/reconnect.test.ts src/protocol/testing/fakeClient.test.ts --maxWorkers=1
```

Expected: reconnect exposes generation A because `connect()` still resolves the first initialize result.

- [ ] **Step 3: Implement the fresh ready handoff**

```ts
private latestInitialize: InitializeResponse | null = null;
private readonly readyHandlers = new Set<(value: InitializeResponse) => void>();

private enterReady(value: InitializeResponse): void {
  this.latestInitialize = value;
  this.connectPromise = Promise.resolve(value);
  this.setState("ready");
}
```

Call `enterReady(result)` before heartbeat arming. Every registered callback receives that exact transition's `InitializeResponse`; `connect()` also resolves the newest successful initialize result. Update the fake client to exercise the same callback contract. The navigation consumer migration happens after v2 types exist in Task 6.

- [ ] **Step 4: Format and run lifecycle tests**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/protocol/client.ts src/protocol/client.test.ts src/protocol/reconnect.test.ts src/protocol/testing/fakeClient.ts src/protocol/testing/fakeClient.test.ts src/protocol/testing/fakeSocket.ts
npx vitest run src/protocol/client.test.ts src/protocol/reconnect.test.ts src/protocol/testing/fakeClient.test.ts --maxWorkers=2
npm run typecheck
```

Expected: PASS; both production and fake clients deliver initialize A then B to ready handlers, and `connect()` exposes B after reconnect.

- [ ] **Step 5: Commit lifecycle fencing**

```bash
git add -- cmd/evener-hub/frontend/src/protocol/client.ts cmd/evener-hub/frontend/src/protocol/client.test.ts cmd/evener-hub/frontend/src/protocol/reconnect.test.ts cmd/evener-hub/frontend/src/protocol/testing/fakeClient.ts cmd/evener-hub/frontend/src/protocol/testing/fakeClient.test.ts cmd/evener-hub/frontend/src/protocol/testing/fakeSocket.ts
git commit -m "fix(web): fence navigation reconnect generations"
```

### Historical Task 2: Add Backward-Compatible Wire Contracts

**Files:**
- Modify: `hubapi/navigation_delta.go`
- Modify: `hubapi/navigation_test.go`
- Modify: `appwire/types.go`
- Modify: `appwire/protocol.go`
- Modify: `appwire/types_test.go`
- Modify: `appwire/protocol_test.go`
- Modify: `internal/appwirets/emit.go`
- Modify: `internal/appwirets/emit_test.go`
- Generate: `docs/appwire-protocol.md`
- Generate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Produces: `NavigationCapability.ReadVersions []int` while retaining `NavigationCapability.Version == 1`.
- Produces: `NavigationReadBase`, `NavigationReadParams.RepresentationVersion`, `Base`, and existing `ETag`.
- Produces: v2 `ok` plus `representation: snapshot|delta`, `not_modified`, and `gone` response variants while retaining the v1 envelope.
- Produces: `hubapi.NavigationSnapshot.Validate(resource string) error` and `NavigationDelta.Validate(resource string) error`.

- [ ] **Step 1: Write failing compatibility and v2 JSON tests**

```go
func TestNavigationCapabilityAdvertisesBothReadVersions(t *testing.T) {
    got, err := json.Marshal(NavigationCapability{
        Version: 1, GenerationID: "generation-a", Sequence: 7,
        ReadVersions: []int{1, 2},
    })
    if err != nil { t.Fatal(err) }
    want := `{"version":1,"generationId":"generation-a","sequence":7,"readVersions":[1,2]}`
    if string(got) != want { t.Fatalf("got %s, want %s", got, want) }
}

func TestNavigationReadV2BaseJSON(t *testing.T) {
    base := NavigationReadBase{GenerationID: "generation-a", Revision: 4, ETag: `W/"nav-a-4"`}
    got, err := json.Marshal(NavigationReadParams{
        RepresentationVersion: 2, Resource: "project", ProjectKey: "p1", Base: &base,
    })
    if err != nil { t.Fatal(err) }
    want := `{"representationVersion":2,"resource":"project","projectKey":"p1","base":{"generationId":"generation-a","revision":4,"etag":"W/\"nav-a-4\""}}`
    if string(got) != want { t.Fatalf("got %s, want %s", got, want) }
}

func TestNavigationReadV2RejectsExplicitNullBase(t *testing.T) {
    var got NavigationReadParams
    err := json.Unmarshal([]byte(`{"representationVersion":2,"resource":"manifest","base":null}`), &got)
    if err == nil { t.Fatal("explicit null base accepted") }
}
```

- [ ] **Step 2: Run the focused tests and verify failure**

Run:

```bash
go test ./appwire ./hubapi ./internal/appwirets -run 'Test.*(NavigationCapability|NavigationReadV2|NavigationEntity|GeneratedFileCurrent)' -count=1
```

Historical red expectation on `679c08a29`: compile failure because the additive
fields and normalized types did not exist. At `383c93802` these files and types
already exist; do not recreate them. The executable no-counter red boundary is
defined by the unblock plan.

- [ ] **Step 3: Define exact additive contracts**

```go
type NavigationCapability struct {
    Version      int    `json:"version"`
    GenerationID string `json:"generationId"`
    Sequence     uint64 `json:"sequence"`
    ReadVersions []int  `json:"readVersions,omitempty"`
}

type NavigationReadBase struct {
    GenerationID string `json:"generationId"`
    Revision     uint64 `json:"revision"`
    ETag         string `json:"etag"`
}

type NavigationReadParams struct {
    RepresentationVersion uint8               `json:"representationVersion,omitempty"`
    Resource              string             `json:"resource"`
    Section               string             `json:"section,omitempty"`
    SectionID             string             `json:"sectionId,omitempty"`
    Catalog               string             `json:"catalog,omitempty"`
    ProjectKey            string             `json:"projectKey,omitempty"`
    Tier                  string             `json:"tier,omitempty"`
    Ref                   string             `json:"ref,omitempty"`
    Offset                *uint32            `json:"offset,omitempty"`
    Limit                 *uint32            `json:"limit,omitempty"`
    ETag                  string             `json:"etag,omitempty"` // v1 only
    Base                  *NavigationReadBase `json:"base,omitempty"` // v2 only
}

type NavigationRepresentation string

const (
    NavigationRepresentationSnapshot NavigationRepresentation = "snapshot"
    NavigationRepresentationDelta    NavigationRepresentation = "delta"
)

type NavigationReadResponse struct {
    Status         string                   `json:"status"`
    Representation NavigationRepresentation `json:"representation,omitempty"`
    GenerationID   string                   `json:"generationId"`
    Revision       uint64                   `json:"revision"`
    ETag           string                   `json:"etag"`
    Base           *NavigationReadBase      `json:"base,omitempty"`
    Data           json.RawMessage          `json:"data,omitempty"`
}
```

Implement presence-aware/custom `NavigationReadParams` JSON decoding: omission of `base` is valid, but an explicitly present `base: null`, incomplete object, unknown field, or unsafe value is invalid params. Do not rely on a pointer alone, because omission and JSON null otherwise both decode to `nil`.

For v2, encode `not_modified` without representation/base/data; `ok` + `snapshot` without base; `ok` + `delta` with the exact echoed base; and `gone` without data. Define the spec’s resource-scoped `NavigationEntityRecord`, `NavigationContainerOwner`, `NavigationOrderContainer`, `NavigationSnapshot`, and `NavigationDelta` shapes exactly. Entity and container records omit numeric revisions. Entity values are a strictly validated shallow resource-specific union. All arrays marshal non-null. Validation rejects unknown fields (including the obsolete record-level `revision`), duplicate keys, wrong union members, missing declared containers, dangling child refs, multiple parents, invalid owners/slots, cycles, and referenced removals.

- [ ] **Step 4: Generate and verify contracts**

```bash
make generate
go test ./appwire ./hubapi ./internal/appwirets -run 'Test.*(Navigation|GeneratedFileCurrent)' -count=1
```

Expected: PASS; generated TypeScript exposes `readVersions`, representation v2 fields, and concrete normalized roots.

- [ ] **Step 5: Commit named paths**

```bash
git add -- hubapi/navigation_delta.go hubapi/navigation_test.go appwire/types.go appwire/protocol.go appwire/types_test.go appwire/protocol_test.go internal/appwirets/emit.go internal/appwirets/emit_test.go docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts
git commit -m "feat(protocol): add navigation read representation v2"
```

### Historical Task 3: Normalize Existing Bounded Resources

**Files:**
- Modify: `cmd/evener-hub/navigation_normalize.go`
- Modify: `cmd/evener-hub/navigation_normalize_test.go`
- Modify: `cmd/evener-hub/navigation_projection_test.go`

**Interfaces:**
- Consumes: existing `navigationProjection.Resource(key)` objects.
- Produces: `normalizeNavigationResource(key navigationResourceKey, object any) (hubapi.NavigationSnapshot, error)`.
- Produces: deterministic session-child and project-tier containers; embedded job arrays remain in the owning shallow session entity in this delivery.

- [ ] **Step 1: Write a failing complete-container test**

```go
func TestNormalizeNavigationSectionCreatesCompleteContainers(t *testing.T) {
    object := hubapi.NavigationSectionResource{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{{
        Ref: "local:parent", HostID: "local", SessionID: "parent", Title: "parent",
        Project: "p", State: "active", Kind: "session", Live: true,
        RunningJobs: hubapi.NavigationArray[hubapi.NavigationJobSummary]{{JobID: "j1", JobType: "shell", Status: "running"}},
        Children: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{},
    }}}
    got, err := normalizeNavigationResource(navigationResourceKey{Kind: navigationResourceLive}, object)
    if err != nil { t.Fatal(err) }
    if err := got.Validate("section"); err != nil { t.Fatal(err) }
    assertContainerKeys(t, got, sessionChildrenContainer("local:parent"), []string{})
    assertEmbeddedRunningJobIDs(t, got, "local:parent", []string{"j1"})
}

func TestNormalizeNavigationRecordsOmitNumericRevisions(t *testing.T) {
    snapshot := normalizeFixture(t, snapshotWithSession("s1", "one"))
    encoded, err := json.Marshal(snapshot)
    if err != nil { t.Fatal(err) }
    var wire map[string]any
    if err := json.Unmarshal(encoded, &wire); err != nil { t.Fatal(err) }
    assertRecordsOmitKey(t, wire["entities"], "revision")
    assertRecordsOmitKey(t, wire["containers"], "revision")
}
```

- [ ] **Step 2: Run the normalizer test and verify failure**

Run: `go test ./cmd/evener-hub -run 'TestNormalizeNavigation' -count=1`

Expected on the pre-redesign implementation: normalized entity/container wire
records contain the obsolete `revision` field.

- [ ] **Step 3: Implement deterministic normalization**

```go
func normalizeNavigationResource(key navigationResourceKey, object any) (hubapi.NavigationSnapshot, error) {
    builder := newNavigationDocumentBuilder(key)
    switch value := object.(type) {
    case hubapi.NavigationManifest:
        builder.addManifest(value)
    case hubapi.NavigationSectionResource:
        builder.addPage(value.Remaining, value.Truncated, key.Offset, key.Limit)
        builder.addSessions(builder.root, value.Sessions)
    case hubapi.NavigationPinSectionCatalog:
        builder.addPinCatalog(value)
    case hubapi.NavigationProjectCatalog:
        builder.addProjectCatalog(value)
    case hubapi.NavigationProjectResource:
        builder.addProjectTiers(value)
    case hubapi.NavigationProjectPage:
        builder.addProjectPage(value, key.Limit)
    case hubapi.NavigationSessionLocation:
        builder.addLocation(value)
    default:
        return hubapi.NavigationSnapshot{}, fmt.Errorf("unsupported navigation object %T", object)
    }
    return builder.finish()
}
```

`addSessions` recursively emits one shallow session entity and one complete child container per session; embedded job arrays remain in the owning session entity in this delivery. `finish` sorts entities by `(kind,key)`, containers by key, preserves order only in `Children`, and calls `Validate`.

Normalization never consults prior state. Cumulative diffing compares entities by
key/kind/canonical value and containers by key/owner/complete ordered children; a
removed key disappears. Canonical comparison uses typed values or deterministic
existing JSON, never map iteration order. A hub generation change clears
resource authority; the frontend's semantic snapshot reconciler may still
preserve equal object identity across that reset.

- [ ] **Step 4: Run normalizer and projection tests**

```bash
go test ./hubapi ./cmd/evener-hub -run 'Test(NormalizeNavigation|NavigationProjection|NavigationSection|NavigationProjectPage)' -count=1
```

Expected: PASS with existing bounds and row ordering unchanged.

- [ ] **Step 5: Commit normalization**

```bash
git add -- cmd/evener-hub/navigation_normalize.go cmd/evener-hub/navigation_normalize_test.go cmd/evener-hub/navigation_projection_test.go
git commit -m "feat(hub): normalize navigation resources"
```

### Historical Task 4: Add Cumulative Diffs and Bounded History

**Files:**
- Modify: `cmd/evener-hub/navigation_delta.go`
- Modify: `cmd/evener-hub/navigation_delta_test.go`
- Modify: `cmd/evener-hub/navigation_history.go`
- Modify: `cmd/evener-hub/navigation_history_test.go`

**Interfaces:**
- Preserves: `diffNavigationSnapshots(key navigationResourceKey, baseVersion, currentVersion appwire.NavigationReadBase, base, current hubapi.NavigationSnapshot) (hubapi.NavigationDelta, error)`.
- Produces: `Remember(view, version, snapshot, gone)` and exact `Lookup(view, base)` on `navigationHistory`.

- [ ] **Step 1: Write failing reparent and eviction tests**

```go
func TestNavigationDeltaReparentReplacesBothContainers(t *testing.T) {
    key := navigationResourceKey{Kind: navigationResourceLive, Limit: 50}
    baseVersion := appwire.NavigationReadBase{GenerationID: "g", Revision: 1, ETag: "tag-1"}
    currentVersion := appwire.NavigationReadBase{GenerationID: "g", Revision: 2, ETag: "tag-2"}
    base := testNavigationSnapshot(t, key, 1, map[string][]string{"left": {"s1", "s2"}, "right": {"s3"}})
    current := testNavigationSnapshot(t, key, 2, map[string][]string{"left": {"s2"}, "right": {"s1", "s3"}})
    delta, err := diffNavigationSnapshots(key, baseVersion, currentVersion, base, current)
    if err != nil { t.Fatal(err) }
    assertDeltaContainer(t, delta, "left", []string{"s2"})
    assertDeltaContainer(t, delta, "right", []string{"s1", "s3"})
}

func TestNavigationHistoryEvictsOldestGlobally(t *testing.T) {
    history := newNavigationHistory(2, 1<<20)
    view := navigationResourceKey{Kind: navigationResourceLive, Limit: 50}
    for revision := uint64(1); revision <= 3; revision++ {
        object := hubapi.NavigationSectionResource{
            GenerationID: "g",
            Revision: revision,
            Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{
                navigationSchemaSession(fmt.Sprintf("local:s%d", revision), fmt.Sprintf("s%d", revision)),
            },
        }
        snapshot, err := normalizeNavigationResource(view, object)
        if err != nil { t.Fatal(err) }
        version := appwire.NavigationReadBase{GenerationID: "g", Revision: revision, ETag: fmt.Sprintf("tag-%d", revision)}
        if err := history.Remember(view, version, &snapshot, false); err != nil { t.Fatal(err) }
    }
    if _, ok := history.Lookup(view, appwire.NavigationReadBase{GenerationID: "g", Revision: 1, ETag: "tag-1"}); ok {
        t.Fatal("oldest version remained retained")
    }
}
```

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./cmd/evener-hub -run 'TestNavigation(Delta|History)' -count=1`

Historical red expectation on `679c08a29`: compile failure because differ and
history types did not exist. At `383c93802` both exist and Slice D modifies them
in place.

- [ ] **Step 3: Implement direct cumulative diff and dual-bound LRU**

```go
func diffNavigationSnapshots(
    key navigationResourceKey,
    baseVersion, currentVersion appwire.NavigationReadBase,
    base, current hubapi.NavigationSnapshot,
) (hubapi.NavigationDelta, error)
```

Keep the existing implementation body and remove only its record-counter
comparisons. Its five inputs and all four authority checks are mandatory:
validate base, validate current, apply and validate the candidate, and prove via
sorted `reflect.DeepEqual` that it reconstructs the current snapshot. Removing
those checks is not a no-counter simplification.

Integrate with the existing representation-cache ownership and policy: use one server-global LRU order, index by complete canonical resource key plus exact generation/revision/ETag, retain immutable snapshot clones, and perform overflow-safe estimated object-plus-JSON byte accounting. Production bounds remain 256 entries and 64 MiB across the `NavigationService`; tests inject smaller limits. Wrong ETag, wrong resource/generation, future, and evicted revisions are lookup misses and therefore snapshot fallbacks.

- [ ] **Step 4: Run focused and race tests**

```bash
go test ./cmd/evener-hub -run 'TestNavigation(Delta|History)' -count=1
go test -race ./cmd/evener-hub -run 'TestNavigation(Delta|History)' -count=1
```

Expected: PASS with complete reordered/reparented containers and deterministic eviction.

- [ ] **Step 5: Commit history**

```bash
git add -- cmd/evener-hub/navigation_delta.go cmd/evener-hub/navigation_delta_test.go cmd/evener-hub/navigation_history.go cmd/evener-hub/navigation_history_test.go
git commit -m "feat(hub): retain bounded navigation delta history"
```

### Historical Task 5: Serve V1 and V2 from One Coherent Service

**Files:**
- Modify: `cmd/evener-hub/navigation_cache.go`
- Modify: `cmd/evener-hub/navigation_cache_test.go`
- Modify: `cmd/evener-hub/navigation_service.go`
- Modify: `cmd/evener-hub/navigation_service_test.go`
- Modify: `cmd/evener-hub/app_navigation.go`
- Modify: `cmd/evener-hub/app_navigation_test.go`
- Modify: `cmd/evener-hub/main.go`
- Modify: `cmd/evener-hub/navigation_publisher_test.go`

**Interfaces:**
- Produces: `NavigationService.ReadV2(ctx, key, base) (navigationReadResult, error)`.
- Keeps: `NavigationService.Representation` for v1.
- Changes: `Capability()` returns `Version: 1, ReadVersions: []int{1,2}`.

- [ ] **Step 1: Write the v1 compatibility and v2 matrix tests**

```go
func TestHubNavigationReadKeepsV1AndSelectsV2Explicitly(t *testing.T) {
    service := newNavigationReadTestService(t)
    legacy := dispatchNavigationRead(t, service, appwire.NavigationReadParams{Resource: "manifest"})
    if legacy.Status != "ok" || len(legacy.Data) == 0 || legacy.Representation != "" {
        t.Fatalf("legacy response=%+v", legacy)
    }
    modern := dispatchNavigationRead(t, service, appwire.NavigationReadParams{RepresentationVersion: 2, Resource: "manifest"})
    if modern.Status != "ok" || modern.Representation != appwire.NavigationRepresentationSnapshot || len(modern.Data) == 0 {
        t.Fatalf("v2 response=%+v", modern)
    }
}
```

Add individually named tests (or named table subtests) for exact-current
`not_modified`, retained older `delta`, wrong tuple/full snapshot, evicted
base/full snapshot, known tombstone `gone`, exact tombstone `not_modified`,
gone→present reappearance, and never-known unavailable. The lifecycle test must
drive one resource through present→gone→present and assert: stale rows disappear
on `gone`; the tombstone base is installed; its exact reread is `not_modified`;
reappearance returns current content under later **resource** authority; and a
never-known key remains unavailable rather than being mistaken for a tombstone.

- [ ] **Step 2: Run service/handler tests and verify failure**

Run:

```bash
go test ./cmd/evener-hub -run 'Test(HubNavigationRead|NavigationServiceReadV2|NavigationPublisher)' -count=1
```

Expected: v2 cases fail because the handler only supports v1 ETag reads.

- [ ] **Step 3: Implement strict representation dispatch**

```go
switch params.RepresentationVersion {
case 0, 1:
    if params.Base != nil { return nil, appwire.InvalidParams("base is valid only for representationVersion 2") }
    return navigationReadV1(ctx, navigation, params, fields)
case 2:
    if params.ETag != "" { return nil, appwire.InvalidParams("etag is valid only for representationVersion 1") }
    result, err := navigation.ReadV2(ctx, key, params.Base)
    if err != nil { return nil, navigationReadError(err) }
    return encodeNavigationReadV2(result)
default:
    return nil, appwire.InvalidParams("unsupported navigation representation version")
}
```

`ReadV2` atomically captures semantic state, current generation/revision, and immutable projection. Exact current returns without rebuilding. Known tombstones use a deterministic version/ETag. Retained older snapshots are diffed directly against current; misses return snapshots. Every sent snapshot or delta target is remembered under the shared cache bounds, and tombstone state remains convergent. History reads never mutate resource revisions or sequence.

- [ ] **Step 4: Prove races and publication behavior**

```bash
go test ./cmd/evener-hub -run 'Test(HubNavigationRead|NavigationService|NavigationPublisher)' -count=1
go test -race ./cmd/evener-hub -run 'Test(NavigationServiceReadV2|NavigationServiceConcurrent|NavigationPublisherLifecycle)' -count=1
```

Expected: PASS; publisher tests still observe FIFO sequences and exactly one `BroadcastAll` for each committed payload.

- [ ] **Step 5: Commit server integration**

```bash
git add -- cmd/evener-hub/navigation_cache.go cmd/evener-hub/navigation_cache_test.go cmd/evener-hub/navigation_service.go cmd/evener-hub/navigation_service_test.go cmd/evener-hub/app_navigation.go cmd/evener-hub/app_navigation_test.go cmd/evener-hub/main.go cmd/evener-hub/navigation_publisher_test.go
git commit -m "feat(hub): serve navigation entity deltas"
```

### Historical Task 6: Validate and Structurally Reconcile V2 in the Browser

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/merge.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/merge.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/types.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/testing.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/revalidator.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/revalidator.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.test.ts`

**Interfaces:**
- Produces: `decodeNavigationResponse(key, sentBase, wire)`.
- Produces: `reconcileSnapshot(previous, incoming)` and `applyDelta(previous, delta)`.
- Produces: normalized `ResourceState` with `presence`, `document`, and exact `version`.
- Produces: v2 requests with `representationVersion: 2` and the stored exact base.
- Preserves: one in-flight read and at most one cumulative trailing read per resource.

- [ ] **Step 1: Write equal-fallback and atomic-reparent tests**

```ts
test("equal fallback preserves document and entity identity", () => {
  const first = normalizeSnapshot(normalizedFixture({ left: ["s1", "s2"], right: ["s3"] }));
  const incoming = normalizeSnapshot(normalizedFixture({ left: ["s1", "s2"], right: ["s3"] }));
  const merged = reconcileSnapshot(first, incoming);
  expect(merged).toBe(first);
  expect(merged.entities.get("session:s1")).toBe(first.entities.get("session:s1"));
  expect(merged.containers.get("left")).toBe(first.containers.get("left"));
});

test("reparent applies both complete parent lists transactionally", () => {
  const before = normalizeSnapshot(normalizedFixture({ left: ["s1", "s2"], right: ["s3"] }));
  const after = applyDelta(before, deltaFixture({ left: ["s2"], right: ["s1", "s3"] }));
  expect(after.containers.get("left")?.children).toEqual(["s2"]);
  expect(after.containers.get("right")?.children).toEqual(["s1", "s3"]);
  expect(after.entities.get("session:s2")).toBe(before.entities.get("session:s2"));
});

test("invalid delta preserves graph and recovers with one no-base snapshot", async () => {
  const h = v2RevalidatorHarnessWithInstalledSnapshot(4);
  const before = h.graph();
  h.resolveDeltaWithDanglingChild(4, 5);
  await h.settled();
  expect(h.graph()).toBe(before);
  expect(h.requests().at(-1)?.base).toBeUndefined();
  h.resolveSnapshot(5);
  await h.settled();
  expect(h.state().version?.revision).toBe(5);
});

test("three invalidations during one v2 read schedule one cumulative trailing read", async () => {
  const h = v2RevalidatorHarness();
  const first = h.loadProject("p1");
  h.invalidateProject("p1", 2);
  h.invalidateProject("p1", 3);
  h.invalidateProject("p1", 4);
  h.resolveSnapshot(1);
  await first;
  expect(h.requests()).toHaveLength(2);
  expect(h.requests()[1]?.base?.revision).toBe(1);
  h.resolveDelta(1, 4);
  await h.settled();
  expect(h.state("p1").version?.revision).toBe(4);
});
```

- [ ] **Step 2: Run tests and verify failure**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/stores/navigation/codec.test.ts src/stores/navigation/merge.test.ts src/stores/navigation/revalidator.test.ts src/stores/navigation/store.test.ts --maxWorkers=1
```

Historical red expectation on `679c08a29`: module-resolution failure because
codec and merge modules did not exist. At `383c93802` both exist and Slice D
modifies them in place.

- [ ] **Step 3: Implement detached validation and lazy-copy merge**

```ts
export function reconcileSnapshot(previous: NormalizedResource | null, incoming: NormalizedResource): NormalizedResource {
  const graph = reconcileGraph(previous?.graph, {
    metadata: incoming.graph.metadata,
    metadataSupplied: true,
    entities: incoming.graph.entities.values(),
    removedEntityKeys: [],
    containers: incoming.graph.containers.values(),
    removedContainerKeys: [],
    complete: true,
    prepared: true,
  });
  validateGraphForResource(incoming.key, incoming.version, graph);
  return Object.freeze({ ...incoming, graph, version: cloneAndDeepFreezeJSON(incoming.version) });
}
```

`applyDelta` updates detached map copies, replaces each supplied container with its complete child list, removes named keys, validates all references and resource metadata, and calls `reconcileSnapshot` only after the candidate is valid. `decodeNavigationResponse` rejects wrong base echoes and illegal status/body combinations before merge.

If decoding or atomic delta validation fails, preserve the installed graph, clear only its authority base, increment the request fence, and issue exactly one v2 read without `base`. Accept only the fenced full snapshot response and pass it through `reconcileSnapshot`; do not publish the invalid delta, retry it, or clear equal entity/container objects.

Navigation uses `client.onReady((initialize) => start(initialize))` and never calls `connect()` inside `onReady`. It accepts capability envelope version 1 only when `readVersions` includes 2, then sends `representationVersion: 2`. On generation/client reset, increment epochs, abort controllers, clear authority bases/targets, reject old waiters, retain equal graph objects provisionally, and prevent old completions from publishing. Same-generation equal sequence does not broadly reload; a higher sequence revalidates loaded keys; a lower sequence is a protocol error. A sequence gap creates no unseen key. Complete resource/page keys fence project switching. Any number of mid-flight invalidations sets one `rerun` flag; a below-target response cannot publish and causes one trailing request from the still-installed exact base.

Keep concrete regressions for every branch, not a prose-only matrix:

- same-generation equal-sequence ready updates capability without a broad read;
- same-generation higher-sequence ready forces each loaded v2 key exactly once
  from its installed base;
- same-generation lower-sequence ready is rejected while installed authority,
  graph identity, and resource bases remain unchanged;
- a notification sequence gap revalidates demanded loaded keys from exact bases;
- a generation change clears authority and rejects an abort-resistant stale
  completion before it can publish; and
- `gone` clears the graph/rows, exact tombstone reread stays converged, and a
  later snapshot reappearance installs only current rows.

- [ ] **Step 4: Format, test, and typecheck**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/stores/navigation/types.ts src/stores/navigation/testing.ts src/stores/navigation/codec.ts src/stores/navigation/codec.test.ts src/stores/navigation/merge.ts src/stores/navigation/merge.test.ts src/stores/navigation/revalidator.ts src/stores/navigation/revalidator.test.ts src/stores/navigation/store.ts src/stores/navigation/store.test.ts
npx vitest run src/stores/navigation/codec.test.ts src/stores/navigation/merge.test.ts src/stores/navigation/revalidator.test.ts src/stores/navigation/store.test.ts --maxWorkers=1
npm run typecheck
```

Expected: PASS for equal full fallback, delta, gone, duplicate keys, union mismatch, missing containers, dangling refs, generation reset, sequence gaps, project switching, stale responses, mutation waiters, and one trailing request.

- [ ] **Step 5: Commit codec and merge**

```bash
git add -- cmd/evener-hub/frontend/src/stores/navigation/types.ts cmd/evener-hub/frontend/src/stores/navigation/testing.ts cmd/evener-hub/frontend/src/stores/navigation/codec.ts cmd/evener-hub/frontend/src/stores/navigation/codec.test.ts cmd/evener-hub/frontend/src/stores/navigation/merge.ts cmd/evener-hub/frontend/src/stores/navigation/merge.test.ts cmd/evener-hub/frontend/src/stores/navigation/revalidator.ts cmd/evener-hub/frontend/src/stores/navigation/revalidator.test.ts cmd/evener-hub/frontend/src/stores/navigation/store.ts cmd/evener-hub/frontend/src/stores/navigation/store.test.ts
git commit -m "feat(web): reconcile navigation entity deltas"
```

### Historical Task 7: Preserve Selector, Node, and Row Identity

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/selectors.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railRenderObserver.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/tree/index.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/tree/tree.test.tsx`

**Interfaces:**
- Produces: memoized normalized session/project selectors.
- Produces: stable `RailNode`, `RailRowActions`, and `TreeRowInfo` references for unchanged rows.
- Produces: memoized `RailRow` with a nullable render observer.

- [ ] **Step 1: Write identity and render-count tests**

```ts
test("one changed session preserves unrelated selector and node identity", () => {
  const before = navigationFixtureState([session("s1", "one"), session("s2", "two")]);
  const beforeModel = selectRailModel(before);
  const after = applyFixtureDelta(before, updateSession("s1", { title: "changed" }));
  const afterModel = selectRailModel(after);
  expect(afterModel.sessions.get("s2")).toBe(beforeModel.sessions.get("s2"));
  expect(afterModel.nodes.get("s2")).toBe(beforeModel.nodes.get("s2"));
});

test("unchanged row does not render after a sibling delta", async () => {
  const counts = new Map<string, number>();
  render(<RailRenderObserver value={(id) => counts.set(id, (counts.get(id) ?? 0) + 1)}><Rail /></RailRenderObserver>);
  counts.clear();
  await applySessionDelta("s1", { title: "changed" });
  expect(counts.get("navigation:live:s1")).toBe(1);
  expect(counts.get("navigation:live:s2") ?? 0).toBe(0);
});
```

- [ ] **Step 2: Run focused tests and verify failure**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/stores/navigation/selectors.test.ts src/shell/rail/Rail.test.tsx src/shell/rail/RailRow.test.tsx src/shell/rail/railNodes.test.ts src/widgets/tree/tree.test.tsx --maxWorkers=1
```

Expected: identity/render assertions fail because selectors and rail adapters rebuild objects.

- [ ] **Step 3: Add safe memoization boundaries**

Use per-resource map references and `WeakMap<NavigationEntity, RailSession | RailProject>` caches. Reuse a node only when its entity, complete child container, and expansion input are identical. Build `RailRowActions` once through stable delegates to current refs.

`Tree` caches row info by node ID and structural fields; callbacks resolve the current node and current handlers through refs:

```ts
const handlersRef = useRef({ onToggle, onActivate });
handlersRef.current = { onToggle, onActivate };
const nodesRef = useRef(new Map<string, T>());
nodesRef.current = new Map(flat.map((entry) => [entry.node.id, entry.node]));

const info = rowInfoCache.current.getOrCreate(node.id, depth, expanded, hasChildren, {
  toggle: () => { const current = nodesRef.current.get(node.id); if (current) handlersRef.current.onToggle(current); },
  activate: () => { const current = nodesRef.current.get(node.id); if (current) handlersRef.current.onActivate(current); },
});
```

Wrap `RailRow` in `memo`; its observer defaults to null. Do not ignore unstable callbacks in a comparator.

- [ ] **Step 4: Format and run selector/rail/tree suites**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/stores/navigation/selectors.ts src/stores/navigation/selectors.test.ts src/shell/rail/Rail.tsx src/shell/rail/Rail.test.tsx src/shell/rail/RailRow.tsx src/shell/rail/RailRow.test.tsx src/shell/rail/railNodes.ts src/shell/rail/railNodes.test.ts src/shell/rail/railRenderObserver.tsx src/widgets/tree/index.tsx src/widgets/tree/tree.test.tsx
npx vitest run src/stores/navigation/selectors.test.ts src/shell/rail src/widgets/tree/tree.test.tsx --maxWorkers=2
npm run typecheck
```

Expected: PASS; unchanged session, project, node, and row references survive equal fallback and sibling changes.

- [ ] **Step 5: Commit render isolation**

```bash
git add -- cmd/evener-hub/frontend/src/stores/navigation/selectors.ts cmd/evener-hub/frontend/src/stores/navigation/selectors.test.ts cmd/evener-hub/frontend/src/shell/rail/Rail.tsx cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx cmd/evener-hub/frontend/src/shell/rail/railNodes.ts cmd/evener-hub/frontend/src/shell/rail/railNodes.test.ts cmd/evener-hub/frontend/src/shell/rail/railRenderObserver.tsx cmd/evener-hub/frontend/src/widgets/tree/index.tsx cmd/evener-hub/frontend/src/widgets/tree/tree.test.tsx
git commit -m "perf(web): isolate unchanged navigation row renders"
```

### Historical Task 8: Prove Browser Behavior and Run Final Gates

**Files:**
- Modify: `cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/shellguard/run.mjs`
- Modify: `cmd/evener-hub/frontend/src/App.test.tsx`

**Interfaces:**
- Produces: `window.applyShellNavigationDelta()` and `window.measureRailRenderCounts()` in the shellguard harness.
- Preserves: existing desktop/mobile scroll, overflow, and tap-target assertions.

- [ ] **Step 1: Add a failing real-browser render assertion**

In the harness, reset counts, emit one v2 invalidation, return a delta changing only `local:p0-s0`, await the matching title in the DOM, and expose counts. Add this runner assertion:

```js
function assertDeltaRenders(result) {
  const failures = [];
  if ((result.counts["navigation:project:p0:current:local:p0-s0"] ?? 0) < 1) {
    failures.push("changed navigation row did not render");
  }
  for (const [id, count] of Object.entries(result.counts)) {
    if (id !== "navigation:project:p0:current:local:p0-s0" && count !== 0) {
      failures.push(`unchanged row ${id} rendered ${count} time(s)`);
    }
  }
  if (result.document.scrollHeight > result.viewport.height + 1) failures.push("delta caused document overflow");
  return failures;
}
```

- [ ] **Step 2: Run the browser guard and verify failure**

Run: `make test-web-browser`

Expected before harness/production wiring: shellguard fails because the v2 mutation and render-count APIs are absent or unchanged rows render.

- [ ] **Step 3: Wire the deterministic v2 browser scenario**

Use `FakeClient` with capability `{version: 1, readVersions: [1, 2], generationId: "shellguard-generation", sequence: 0}`. Script initial v2 snapshots, then one revision-2 delta. Wrap the real `AppShell` in `RailRenderObserver`, clear counts after boot, expose an awaitable delta function, and run the existing geometry measurements after the delta settles. Report actual row counts and unchanged geometry in the implementation handoff; do not edit the approved design to store transient test output.

- [ ] **Step 4: Run formatting and all final gates in order**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/dev/shellguard-entry.tsx src/App.test.tsx
cd ../../..
make generate
go test ./appwire ./hubapi ./internal/appwirets ./cmd/evener-hub -run 'Test.*Navigation' -count=1
go test -race ./cmd/evener-hub -run 'Test(NavigationServiceReadV2|NavigationHistory|NavigationPublisher)' -count=1
make lint
make vet
make test
make test-web
make test-race
make merge-approval-gate
make test-web-browser
git diff --check
```

Expected: every command exits zero. A missing Chrome, sandbox denial, timeout, or setup failure leaves that gate incomplete and must be reported as such.

- [ ] **Step 5: Review and commit proof paths**

```bash
git status --short
git diff --stat
git add -- cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx cmd/evener-hub/frontend/scripts/shellguard/run.mjs cmd/evener-hub/frontend/src/App.test.tsx
git diff --cached --name-only
git commit -m "test: prove navigation delta convergence and rendering"
```

The staged-name output must contain only the three named paths. Report focused tests, race tests, canonical gate, browser gate, changed-row count, unchanged-row counts, history eviction behavior, and any incomplete environmental gate.

## Post-Implementation Review

After each task, run two reviews before continuing:

1. requirements and spec compliance for that task;
2. code quality, race safety, strict validation, structural sharing, and maintainability.

Fix Critical and Important findings before the next task. After Task 8, use `superpowers:verification-before-completion` and verify the actual no-record-counter wire matrix, v1 compatibility, exact-base behavior, tombstone convergence, content-based entity upserts, complete-container reparenting, publication sequences, reconnect generation, structural identity, render counts, and every final gate against the spec.
