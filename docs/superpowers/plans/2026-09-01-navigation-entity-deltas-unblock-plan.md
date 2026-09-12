# Navigation Entity-Delta Unblock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the verified navigation v2 acceptance blockers without changing
v1 behavior, remove the unbounded per-View state implied by record-level
revisions, then prove strict convergence, immutable structural sharing, and
unchanged-row render isolation.

**Architecture:** Repair the existing v2 implementation in dependency order.
The complete bounded `ResourceKey` is protocol identity; resource generation,
revision, ETag, and invalidation sequence are authority. Normalized entity and
container records are stateless and carry no numeric revision. Exact retained
bases produce semantic cumulative deltas; exact-base misses produce a silent
View-local snapshot. The frontend reconciles semantically equal keyed records
through one deeply immutable merge, materializes recursively bottom-up, and
preserves Rail row identity.

**Tech Stack:** Go 1.27, AppWire JSON-RPC, TypeScript, React 19, Vitest, Biome, Chrome shellguard.

**Spec:** `docs/superpowers/specs/2026-09-01-navigation-entity-deltas-design.md`

**Prior plan:** `docs/superpowers/plans/2026-09-01-navigation-entity-deltas-plan.md`

## Global Constraints

- The binding spec wins over this repair plan, the prior plan, and current code.
- Preserve navigation representation v1 and every existing invalidation, reconnect, tombstone, retry, and stale-response fence outside the seven findings.
- V2 entity keys are resource-local and include kind plus every applicable selector field: project/section/ref, tier, page offset, and page limit; generation and resource revision are not part of that scope.
- V2 entity/container records contain no numeric revision. Do not retain a
  current snapshot per arbitrary View merely to synthesize record counters.
- Keep untrusted graph keys within the existing 2,048-byte limit and do not echo raw refs, paths, titles, keys, or bodies in errors.
- Keep service-global representation plus delta-history retention bounded at 256 entries / 64 MiB.
- Session entities are shallow: their wire `children` field is always an empty array; recursion exists only through `children` containers.
- Default tests stay deterministic, offline, credential-free, and independent of ambient machine state.
- Before changing tests, follow `docs/developing-evener/testing.md`; before final gates, follow `docs/developing-evener/README.md` and inspect `make help`.
- Use strict red/green TDD. Never weaken/delete an assertion to reach green.
- One writer at a time in this worktree. Stage named paths only. Do not amend or
  merge. The user has granted standing authorization to push/open or update the
  separate Navigation PR only after exact-head review and gates; that authority
  never includes merging it.

---

## Slice A — Protocol and convergence

### Task 1: Contiguous paging and complete view identity

**Files:**
- Modify: `cmd/evener-hub/navigation_cache.go`
- Modify: `cmd/evener-hub/navigation_projection.go`
- Modify: `cmd/evener-hub/navigation_projection_test.go`
- Modify: `cmd/evener-hub/navigation_normalize.go`
- Modify: `cmd/evener-hub/navigation_normalize_test.go`
- Modify: `cmd/evener-hub/navigation_service.go`
- Modify: `cmd/evener-hub/navigation_service_test.go`
- Modify: `cmd/evener-hub/navigation_history.go`
- Modify: `cmd/evener-hub/navigation_history_test.go`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/types.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts`
- Modify: adjacent Rail/store tests that directly construct normalized keys.

**Interfaces:**
- Produces backend selector identity:

```go
func (key navigationResourceKey) View() navigationResourceKey
func navigationViewScope(key navigationResourceKey) string
func navigationEntityKey(key navigationResourceKey, kind, identity string) string
func navigationRootContainerKey(key navigationResourceKey, slot string) string
func navigationOwnedContainerKey(entityKey, slot string) string
```

- `View` canonicalizes defaults and clears only `Generation`/`Revision`.
- `navigationViewScope` is the collision-safe, cross-language form
  `nav2/<kind>/<base64url(id)>/<base64url(sectionID)>/<base64url(projectKey)>/<base64url(tier)>/<offset>/<limit>`.
- Entity local IDs are `hex(sha256(kind + "\x00" + logicalIdentity))`; root/owned keys inherit the exact view prefix.
- Produces frontend equivalents:

```ts
export function navigationViewScope(key: ResourceKey): string;
export function navigationRootContainerKey(key: ResourceKey, slot: string): string;
export function navigationOwnedContainerKey(entityKey: string, slot: string): string;
export function nextNavigationOffset(offset: number, returnedTopLevelRows: number): number;
```

- `nextNavigationOffset` is the only continuation calculation: `offset + returnedTopLevelRows`. It never uses requested `limit`.

- [ ] **Step 1: Add red backend pagination and scope tests**

Add `TestNavigationByteTruncatedCatalogContinuationIsContiguous`, `TestNavigationByteTruncatedProjectPageContinuationIsContiguous`, and `TestNormalizeNavigationKeysIncludeCompleteViewScope`. Each continuation test must:

```go
// Project once with a byte envelope that admits a non-empty strict prefix smaller
// than Limit, then request the next page at Offset + len(returnedRows).
if got := len(firstRows); got == 0 || got >= int(limit) {
    t.Fatalf("fixture did not force byte truncation: got %d of %d", got, limit)
}
nextOffset := offset + uint32(len(firstRows))
// Concatenating page 1 and page 2 must equal the same prefix of an unbounded
// reference projection: no duplicate and no skipped row.
```

The scope test must normalize the same session into two keys differing in exactly one selector dimension (tier, offset, or limit), assert different entity/container key prefixes, assert every key starts with `navigationViewScope(key)+"/"`, and assert every key remains at most 2,048 bytes for maximum accepted identities.

- [ ] **Step 2: Run backend tests and record the expected failures**

```bash
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test ./cmd/evener-hub \
  -run 'Test(NavigationByteTruncated|NormalizeNavigationKeysIncludeCompleteViewScope)' \
  -count=1
```

Expected red boundaries: continuation starts at requested `limit`; normalized keys are raw `session:<ref>` / `project:<key>` and collide across views.

- [ ] **Step 3: Add red frontend continuation/scope tests**

Correct the existing short-page assertion rather than deleting it:

```ts
expect(nextNavigationOffset(2, 2)).toBe(4); // not 2 + requested limit 50
```

Cover global sections, pin sections, catalogs, project roots, and project pages. For every path, the next requested offset must equal the original offset plus the number of top-level rows actually returned. Add parity vectors for `navigationViewScope` that match literal Go vectors containing Unicode and delimiter characters.

- [ ] **Step 4: Run frontend tests and record the expected failures**

From `cmd/evener-hub/frontend`:

```bash
npx vitest run \
  src/stores/navigation/store.test.ts \
  src/stores/navigation/selectors.test.ts \
  src/shell/rail/railNodes.test.ts \
  src/shell/rail/Rail.test.tsx \
  --maxWorkers=4 --configLoader runner
```

Expected red boundaries: requested limits advance cursors; no complete-view scope helper exists.

- [ ] **Step 5: Implement view scoping and contiguous continuation**

Canonicalize once, use URL-safe raw base64 for selector strings, and construct every entity/container through the five named helpers. Add `offset` and `limit` to v2 normalized metadata for every paged resource; add `limit` to project-page metadata. Keep v1 projector objects unchanged.

Replace every continuation formula of the form `offset + limit` with `nextNavigationOffset(offset, returnedTopLevelRows)`. For project roots, use the count in that tier's returned root container; for project pages/sections/catalogs, use the returned root container's child count.

- [ ] **Step 6: Isolate exact history by complete view**

Make `navigationHistory.key` use `view.View()`. `ReadV2` may produce a delta only
from the exact complete View and exact retained resource base. Never compare a
project page to a project root or another page. A history miss returns a full
snapshot; normalization does not require a previous snapshot and
`navigationResourceState` retains no View-specific normalized baseline.

Add a regression that reads project root, current page at `(offset=2, limit=2)`,
and archived page at the same offset/limit, mutates one, and proves only the
exact retained view can produce a delta; cross-view and evicted bases fall back
to snapshots without clearing unrelated Views.

- [ ] **Step 7: Run focused Go/frontend gates**

Run the Step 2 and Step 4 commands. Expected: all pass. Also run:

```bash
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test ./cmd/evener-hub ./hubapi \
  -run 'Test(NavigationHistory|NavigationReadV2|NavigationProjectPage|NormalizeNavigation)' \
  -count=1
```

- [ ] **Step 8: Commit named paths**

```bash
git add -- cmd/evener-hub/navigation_cache.go cmd/evener-hub/navigation_projection.go \
  cmd/evener-hub/navigation_projection_test.go cmd/evener-hub/navigation_normalize.go \
  cmd/evener-hub/navigation_normalize_test.go cmd/evener-hub/navigation_service.go \
  cmd/evener-hub/navigation_service_test.go cmd/evener-hub/navigation_history.go \
  cmd/evener-hub/navigation_history_test.go \
  cmd/evener-hub/frontend/src/stores/navigation/types.ts \
  cmd/evener-hub/frontend/src/stores/navigation/selectors.ts \
  cmd/evener-hub/frontend/src/stores/navigation/store.test.ts \
  cmd/evener-hub/frontend/src/shell/rail/Rail.tsx \
  cmd/evener-hub/frontend/src/shell/rail/railNodes.ts \
  cmd/evener-hub/frontend/src/stores/navigation/selectors.test.ts \
  cmd/evener-hub/frontend/src/shell/rail/railNodes.test.ts \
  cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx

git commit -m "fix(hub): scope navigation views and page contiguously"
```

Obtain fresh spec and quality review before Task 2.

### Task 2: Strict resource-schema validation

**Files:**
- Create: `cmd/evener-hub/navigation_schema.go`
- Create: `cmd/evener-hub/navigation_schema_test.go`
- Modify: `cmd/evener-hub/navigation_normalize.go`
- Modify: `cmd/evener-hub/navigation_history.go`
- Modify: `cmd/evener-hub/navigation_delta.go`
- Modify: `hubapi/navigation_delta.go` only for generic graph invariants shared by every resource.
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/merge.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/merge.test.ts`
- Modify: v2 test/shellguard fixtures that use old unscoped keys.

**Interfaces:**

```go
func validateNavigationResourceSnapshot(
    key navigationResourceKey,
    generation string,
    revision uint64,
    snapshot hubapi.NavigationSnapshot,
) error
```

```ts
export function validateSnapshotForResource(
  key: ResourceKey,
  version: NavigationReadBase,
  snapshot: NavigationSnapshot,
): void;

export function validateGraphForResource(
  key: ResourceKey,
  version: NavigationReadBase,
  graph: NavigationGraph,
): void;
```

`decodeNavigationResponse` validates snapshots before normalization. Delta decoding validates field/record shape and scope; `applyDelta` stages changes, then calls `validateGraphForResource` before publication.

- [ ] **Step 1: Add a table-driven red backend schema matrix**

`TestValidateNavigationResourceSnapshotRejectsWrongSchema` must start from one valid fixture per resource and independently mutate:

```go
cases := []string{
    "metadata generation or revision mismatch",
    "paged metadata selector mismatch",
    "wrong or unknown entity kind",
    "missing required value field",
    "unknown value field",
    "non-empty session value children",
    "wrong root slot",
    "extra root",
    "entity-owned slot not children/current/recent/archived as applicable",
    "owner missing",
    "orphan represented entity",
    "duplicate logical identity",
    "wrong view scope",
}
```

Every case must assert a generic error category and must not expect raw identity text in the error.

- [ ] **Step 2: Add a red frontend schema matrix**

Add `codec rejects wrong resource metadata, value schema, slots, scope, and orphan entities`. Use one valid snapshot per `ResourceKey`, then mutate exactly one invariant per assertion. Include invalid delta upserts that are individually well-shaped but yield an invalid final resource graph; assert the prior graph and maps retain `===` identity after rejection.

- [ ] **Step 3: Run the red schema tests**

```bash
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test ./cmd/evener-hub ./hubapi \
  -run 'TestValidateNavigationResourceSnapshotRejectsWrongSchema' -count=1
```

From the frontend directory:

```bash
npx vitest run src/stores/navigation/codec.test.ts src/stores/navigation/merge.test.ts \
  --maxWorkers=4 --configLoader runner
```

Expected: generic validation currently accepts at least the empty session value, permissive slots, orphan, and selector-mismatched metadata.

- [ ] **Step 4: Implement exact backend schemas**

Use `json.Decoder.DisallowUnknownFields`, require one JSON value and EOF, then validate required non-zero identities/count ranges with existing navigation identity/size rules. Enforce this resource matrix:

| Resource | exact root/owner slots | entities |
|---|---|---|
| manifest | one `resource_root/manifest` | none |
| live, needs_you, pin_section | one `resource_root/sessions`; session `children` owners | shallow sessions |
| pin_catalog | one `resource_root/pin_sections` | pin-section descriptors |
| projects, archived_projects, test_runs | one `resource_root/projects` | project summaries |
| project | one project anchor owning exactly current/recent/archived; session children owners | one `{key}` anchor plus shallow sessions |
| project_page | one `resource_root/sessions`; session children owners | shallow sessions |
| location | one `resource_root/session` with zero/one child; session children owners | zero/one shallow session |

Require every represented non-anchor entity to appear exactly once as a child, every owner to exist, all keys to match the complete view prefix, and backend-generated entity keys to match the typed logical identity digest.

- [ ] **Step 5: Implement matching frontend schemas**

Use exact-key checks for fixed metadata/entity shapes, safe-integer/range checks, complete view-prefix checks, duplicate logical-identity checks, and the same owner/slot matrix. Frontend treats the 64-hex local entity digest as opaque but requires its syntax and the exact computed view prefix. Keep all validation synchronous and content-free in its errors.

- [ ] **Step 6: Prove invalid-delta recovery remains atomic**

Extend `revalidator.test.ts`/`store.test.ts` so a strict-schema failure clears the unusable base, preserves the installed graph, issues one forced full-snapshot read, and converges without publishing the invalid staged graph.

- [ ] **Step 7: Run focused gates and commit**

Run Steps 3 plus:

```bash
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test ./cmd/evener-hub ./hubapi ./appwire -count=1
```

```bash
npx vitest run src/stores/navigation/codec.test.ts src/stores/navigation/merge.test.ts \
  src/stores/navigation/revalidator.test.ts src/stores/navigation/store.test.ts \
  --maxWorkers=4 --configLoader runner
```

Format touched Go/TS files, stage only the named paths, and commit:

```bash
git commit -m "fix(hub): validate navigation resources strictly"
```

Obtain fresh spec and quality review before Slice B.

---

## Slice B — Immutable graph safety

### Task 3: Deep immutable keyed reconciliation

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/merge.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/merge.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.test.ts`

**Interfaces:**

```ts
export function cloneAndDeepFreezeJSON<T>(value: T): T;
export function equalJSON(left: unknown, right: unknown): boolean;
export function reconcileSnapshot(previous: NormalizedResource | undefined, next: NormalizedResource): NormalizedResource;
export function applyDelta(previous: NormalizedResource, delta: NavigationDelta, version: NavigationReadBase): NormalizedResource;
```

`equalJSON` compares object keys independent of insertion order and array order exactly. Neither function retains aliases from decoded input. Map copies occur only after a real add/remove/change.

- [ ] **Step 1: Add red identity/freeze tests**

Add:

```ts
test("equal delta upserts preserve entity container map and graph identity", () => {
  // Upsert property-order-different but semantically equal entity metadata and
  // an equal complete container. Every object, both maps, and graph stay ===.
});

test("normalized metadata and entity values are deeply frozen and detached", () => {
  // Mutating the original decoded object cannot change installed graph values;
  // Object.isFrozen is true recursively for metadata, entity value, jobs, and arrays.
});
```

Also assert: one changed entity clones only the entity map; one changed container clones only the container map; version/request metadata changes may create a resource wrapper but must preserve the graph object.

- [ ] **Step 2: Run red merge tests**

```bash
npx vitest run src/stores/navigation/merge.test.ts src/stores/navigation/codec.test.ts \
  src/stores/navigation/store.test.ts --maxWorkers=4 --configLoader runner
```

Expected red boundaries: shallow freeze, `JSON.stringify` property-order inequality, eager map/object replacement.

- [ ] **Step 3: Implement one immutable merge path**

Normalize decoded JSON with `cloneAndDeepFreezeJSON`. Reconcile snapshot and delta records through the same keyed helper:

```ts
const chosen = existing && existing.kind === incoming.kind && equalJSON(existing.value, incoming.value)
  ? existing
  : freezeEntity(incoming);
```

For containers, equality includes owner and ordered children. Do not clone a map
until `chosen !== existing`, a present key is removed, or a new key is added.
Build the staged graph, strictly validate it, and only then return/publish it.
Equal generation-reset snapshots may reuse semantically equal entity/container
objects while generation authority stays in `NormalizedResource.version`.

- [ ] **Step 4: Run focused navigation/store tests**

```bash
npx vitest run \
  src/stores/navigation/codec.test.ts \
  src/stores/navigation/merge.test.ts \
  src/stores/navigation/selectors.test.ts \
  src/stores/navigation/store.test.ts \
  src/stores/navigation/revalidator.test.ts \
  --maxWorkers=4 --configLoader runner
```

Expected: all pass, including exact object/map/graph identities and deep freeze.

- [ ] **Step 5: Commit and review**

Format, stage the six named paths only, commit:

```bash
git commit -m "fix(web): reconcile navigation graphs immutably"
```

Obtain fresh spec and quality review before Task 4.

### Task 4: Dependency-correct bottom-up materialization

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/selectors.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.test.ts`

**Interfaces:**

```ts
export function materializeNavigationResource(resource: NormalizedResource): Readonly<Record<string, unknown>>;
export function selectRailModel(resource: NormalizedResource): Readonly<Record<string, unknown>>;
```

Each entity cache entry depends on the frozen entity record, its direct child-container object, and the ordered **materialized child object identities**. Cached values are never mutated.

- [ ] **Step 1: Add the red grandchild regression**

```ts
test("grandchild changes invalidate every recursive ancestor materialization", () => {
  // Build parent -> child -> grandchild. Change only the grandchild entity.
  // Grandchild, child, and parent materialized objects must change; an unrelated
  // sibling branch and its node array must stay ===; new values must be visible.
});
```

Also assert repeated selection of the same graph returns the same root and nested objects, and loading/error-only `ResourceState` changes preserve the selected graph/model identity.

- [ ] **Step 2: Run red selector tests**

```bash
npx vitest run src/stores/navigation/codec.test.ts src/stores/navigation/selectors.test.ts \
  src/stores/navigation/store.test.ts --maxWorkers=4 --configLoader runner
```

Expected red boundary: the parent cache sees unchanged direct entity identities and returns stale recursive data; `selectRailModel` mutates cached `children`.

- [ ] **Step 3: Materialize bottom-up without mutation**

Recursively materialize children first, then compare the ordered resulting child identities with the cache entry. Construct a new frozen shallow session only when the entity, child container, or a materialized child identity changed:

```ts
const materialized = Object.freeze({
  ...(entity.value as Record<string, unknown>),
  children: Object.freeze(children),
});
```

Replace mutable `cached.children = ...` logic with immutable cache entries. Key root lookup through `navigationRootContainerKey` / `navigationOwnedContainerKey`; do not reintroduce literal `root:*` or global entity keys.

- [ ] **Step 4: Run graph/store tests and commit**

Run the five-file Slice B command from Task 3 Step 4. Format, stage the five named paths, commit:

```bash
git commit -m "fix(web): invalidate recursive navigation materialization"
```

Obtain fresh spec and quality review before Slice C.

---

## Slice C — Rendering identity and proof

### Task 5: Complete project/page and node memo dependencies

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/selectors.test.ts`

**Interfaces:**

```ts
type ProjectPageDependency = Readonly<{
  id: string;       // keyID(complete project_page ResourceKey)
  tier: "current" | "recent" | "archived";
  graphOrData: object | null;
}>;
```

A cached `RailProject` depends on summary identity, root graph/data identity, and the ordered `ProjectPageDependency[]`. Loading/error flags do not invalidate graph-derived navigation nodes. Node builders depend on immutable project/session identity, child-node array identity, display label, and the memoized expansion lookup identity.

- [ ] **Step 1: Add red page/project/node identity tests**

Add `project model cache includes ordered project-page dependencies`:

```ts
// Same summary/root/pages => same RailProject.
// Loading one new complete page => new project containing those rows.
// Updating an unrelated resource => same project, project node, session node,
// child array, and unchanged sibling identities.
```

Add a nested-child case proving changed descendants produce changed ancestor nodes with current content, while an unrelated sibling node stays `===`. Cover archived project/session builders, not only active projects.

- [ ] **Step 2: Run red rendering-model tests**

```bash
npx vitest run src/stores/navigation/selectors.test.ts \
  src/shell/rail/railNodes.test.ts src/shell/rail/Rail.test.tsx \
  --maxWorkers=4 --configLoader runner
```

Expected red boundaries: `projectModelCache` omits pages; graph-native projects are rebuilt; node caches ignore descendant/page dependencies.

- [ ] **Step 3: Implement complete memo dependencies**

Replace `WeakMap<summary, Map<root, project>>` with one record per summary containing root identity, ordered page IDs/graph identities, and result. Compare page dependencies by length/order/id/object identity. Build graph-native projects only when one dependency changes.

Cache session/project/archived node arrays bottom-up. Include the memoized `isExpanded` function identity so descendant disclosure changes cannot reuse stale nodes; unchanged navigation updates retain it. Keep `RailRow`'s memo boundary and Tree's stable `TreeRowInfo` cache intact.

- [ ] **Step 4: Run rendering tests and commit**

Run Step 2 plus `RailRow.test.tsx`. Format, stage the six named paths, commit:

```bash
git commit -m "fix(web): preserve unchanged navigation row identities"
```

Obtain fresh spec and quality review before Task 6.

### Task 6: Production row-render acceptance

**Files:**
- Modify: `cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/shellguard/run.mjs`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx` only if needed to host the focused observer assertion.
- Modify: v2 fixture helpers/tests still using unscoped keys.
- Create final evidence at
  `.superpowers/sdd/2026-09-01-navigation-entity-deltas-unblock-plan/final-report.md`
  in the existing gitignored SDD workspace; do not add scratch reports to tracked
  repository paths.

**Interfaces:**
- Reuse `RailRenderObserver` / `useRailRenderObserver`; do not add a second instrumentation mechanism.
- `window.applyShellNavigationDelta()` changes exactly one visible session entity.
- `window.measureRailRenderCounts()` returns per-row invocation counts after the delta.

- [ ] **Step 1: Add/repair the focused render-spy acceptance**

Use the scoped-key fixture helpers from Task 1. Clear counts immediately before publishing the delta. Assert:

```js
const changed = scopedChangedRowID;
if ((counts[changed] ?? 0) < 1) failures.push("changed navigation row did not render");
for (const id of visibleRowIDs) {
  if (id !== changed && (counts[id] ?? 0) !== 0) {
    failures.push(`unchanged row ${id} rendered ${counts[id]} time(s)`);
  }
}
```

The fixture must exercise the production `AppShell -> navigationStore -> Rail -> Tree -> RailRow` path, not a component-only surrogate.

- [ ] **Step 2: Run focused unit/type/browser gates**

From `cmd/evener-hub/frontend`:

```bash
npx biome check --write src/dev/shellguard-entry.tsx \
  src/shell/rail/Rail.tsx src/shell/rail/Rail.test.tsx \
  src/shell/rail/RailRow.tsx src/shell/rail/RailRow.test.tsx
npx vitest run \
  src/stores/navigation/codec.test.ts \
  src/stores/navigation/merge.test.ts \
  src/stores/navigation/selectors.test.ts \
  src/stores/navigation/store.test.ts \
  src/stores/navigation/revalidator.test.ts \
  src/shell/rail/railNodes.test.ts \
  src/shell/rail/Rail.test.tsx \
  src/shell/rail/RailRow.test.tsx \
  --maxWorkers=4 --configLoader runner
npm run typecheck
npm run shellguard
```

Every command must exit 0. A sandbox denial, missing Chrome, timeout, or launch failure leaves browser verification incomplete; report it exactly and rerun from the parent environment if available.

- [ ] **Step 3: Run focused Go and race gates**

From repository root:

```bash
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test ./cmd/evener-hub ./hubapi ./appwire -count=1
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test -race ./cmd/evener-hub ./hubapi -run 'Navigation|navigation' -count=1
git diff --check
```

- [ ] **Step 4: Commit the acceptance fixture**

Stage only actually changed named fixture/test paths and commit:

```bash
git commit -m "test(web): prove navigation row render isolation"
```

- [ ] **Step 5: Preserve the reviewed Task 6 boundary**

Obtain the scoped Task 6 review and record its browser evidence, but do not run a
whole-branch final review or claim delivery readiness yet. The no-counter Slice D
below changes the wire, backend, generated contracts, frontend fixtures, and
final acceptance surface. Task 8 is the only final review/gate/delivery task.

---

## Slice D — Remove record-level revision state

### Task 7: Stateless records and bounded View-local fallback

**Decision:** Entity/container numeric revisions are removed from v2. The
resource-level `(generationId, revision, etag)` base and Navigation capability
sequence remain unchanged. This v2 representation is not deployed, so producer,
strict consumer, generated contracts, tests, and docs change atomically; do not
add a permissive dual-schema migration.

**Files:**
- Modify: `hubapi/navigation_delta.go`
- Modify: `hubapi/navigation_test.go`
- Modify: `cmd/evener-hub/navigation_normalize.go`
- Modify: `cmd/evener-hub/navigation_normalize_test.go`
- Modify: `cmd/evener-hub/navigation_delta.go`
- Modify: `cmd/evener-hub/navigation_delta_test.go`
- Modify: `cmd/evener-hub/navigation_schema.go`
- Modify: `cmd/evener-hub/navigation_schema_test.go`
- Modify: `cmd/evener-hub/navigation_service.go`
- Modify: `cmd/evener-hub/navigation_service_test.go`
- Modify: `cmd/evener-hub/app_navigation_test.go` if the handler-level lifecycle
  matrix belongs there rather than in the service test
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/merge.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/merge.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/revalidator.test.ts` only
  if lifecycle/recovery assertions cannot stay in `store.test.ts`
- Modify: Navigation frontend/shellguard fixtures that construct entity/container
  records and fail typecheck after the contract change
- Generate only through `make generate`:
  `cmd/evener-hub/frontend/src/protocol/types.gen.ts` and
  `docs/appwire-protocol.md`

**Must not change:** `NavigationReadBase.revision`, response/resource metadata
revision, semantic `navigationResourceState.Revision`, ETag construction,
history keys, cache versions, capability sequence, v1 behavior, exact-base
echo/validation, reconnect equal/higher/lower rules, or strict graph invariants.

- [ ] **Step 1: Add decisive red contract and eviction regressions**

Add backend JSON-shape tests proving entity/container records omit `revision`.
Add frontend strict-codec tests that accept the new exact record keys and reject
the obsolete extra `revision`. Add a service test with an injected tiny history:

1. read one complete project-page View and retain its resource base;
2. read enough other Views to evict that exact history entry;
3. mutate the original View and commit the next resource revision;
4. read from the evicted base and assert a silent full snapshot containing the
   new semantic content, with no record counters;
5. assert unrelated Views retain their installed resource authority and no
   cross-View delta is attempted.

The same service test must still prove that a retained exact base returns a
content-based cumulative delta and that a cross-View base returns a snapshot.

Add a named backend lifecycle test that drives one resource through
present→gone→present and separately queries a never-known resource. It must
assert all of the following:

1. the first present read is a snapshot;
2. removal returns `gone` with a later resource revision/ETag and no data;
3. a read from that exact tombstone base returns `not_modified`;
4. reappearance returns current content under a later resource revision as a
   snapshot or a delta only when the exact retained tombstone base supports it;
5. the reappeared graph contains no rows from the prior presence; and
6. a never-known key remains unavailable and is not fabricated as a tombstone.

Add a frontend lifecycle test that installs a nonempty graph, accepts `gone`,
proves the graph/visible rows are cleared while the tombstone base is retained,
accepts exact tombstone `not_modified`, then accepts a reappearance snapshot and
proves only current rows exist.

The no-counter change must also rerun—not rewrite or weaken—the concrete
authority/fencing regressions already in `store.test.ts`:

- `same-generation equal-sequence reconnect updates capability without broad reload`;
- `same-generation higher-sequence reconnect advances and forces every loaded v2 base exactly once`;
- `same-generation lower-sequence reconnect preserves installed v2 authority and identities without a read`;
- `sequence gaps revalidate demanded locations`;
- `stale client completion cannot overwrite newer client`; and
- `a stale malformed response cannot poison or force the active client`.

- [ ] **Step 2: Run the red boundaries**

```bash
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test ./hubapi ./cmd/evener-hub \
  -run 'Test.*(Navigation.*Record|NavigationReadV2.*History|ExactCompleteView|PresentGone|Tombstone|Reappear)' \
  -count=1

cd cmd/evener-hub/frontend
npx vitest run src/stores/navigation/codec.test.ts \
  src/stores/navigation/merge.test.ts \
  src/stores/navigation/store.test.ts \
  src/stores/navigation/revalidator.test.ts \
  --maxWorkers=4 --configLoader runner
```

Expected: current wire emits/requires record `revision`; the strict frontend
rejects records without it and accepts the obsolete shape.

- [ ] **Step 3: Remove backend record counters and baseline retention**

- Delete only `Revision` from `NavigationEntityRecord` and
  `NavigationOrderContainer`, plus their nonzero validation checks.
- Build normalized records without counters; delete
  `assignNavigationKeyRevisions` and its overflow paths.
- Diff entities by key presence plus kind/canonical value; diff containers by
  key presence plus owner/complete ordered children. Preserve reconstruction
  validation and explicit removals. Keep the current five-argument
  `diffNavigationSnapshots(key, baseVersion, currentVersion, base, current)`
  boundary, base/current resource validation, applied-candidate validation, and
  exact reconstruction proof; only record-counter comparisons disappear.
- Delete `SnapshotView`/`Snapshot` from `navigationResourceState`, the read-time
  previous-baseline lookup, and refresh-time
  `navigationAssignStateSnapshotsContext`. Preserve semantic resource
  fingerprint/revision state and exact bounded `navigationHistory`.

- [ ] **Step 4: Change strict frontend records and semantic sharing**

- Require exact entity keys `{key, kind, value}` and exact container keys
  `{key, owner, children}`; reject obsolete extras.
- Reconcile an entity by `kind + equalJSON(value)` and a container by
  `equalJSON(owner) + equalJSON(ordered children)`.
- Preserve lazy map copies, deep freeze/detachment, atomic graph validation,
  invalid-delta recovery, equal snapshot identity, selector/node identity, and
  unchanged-row render behavior.
- Update only per-record fixture fields. Resource/version/metadata `revision`
  values stay present and asserted.

- [ ] **Step 5: Generate and run focused green gates**

Start this step from the repository root:

```bash
gofmt -w hubapi/navigation_delta.go hubapi/navigation_test.go \
  cmd/evener-hub/navigation_normalize.go cmd/evener-hub/navigation_normalize_test.go \
  cmd/evener-hub/navigation_delta.go cmd/evener-hub/navigation_delta_test.go \
  cmd/evener-hub/navigation_schema.go cmd/evener-hub/navigation_schema_test.go \
  cmd/evener-hub/navigation_service.go cmd/evener-hub/navigation_service_test.go
make generate
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test ./hubapi ./appwire \
  ./internal/appwirets ./cmd/evener-hub -run 'Test.*Navigation' -count=1
GOCACHE="$EVENER_SCRATCH_DIR/go-build" go test -race ./cmd/evener-hub \
  -run 'Test(NavigationReadV2|NavigationHistory|NavigationService)' -count=1

cd cmd/evener-hub/frontend
npx biome check --write \
  src/stores/navigation/codec.ts src/stores/navigation/codec.test.ts \
  src/stores/navigation/merge.ts src/stores/navigation/merge.test.ts \
  src/stores/navigation/store.test.ts src/stores/navigation/revalidator.test.ts \
  src/dev/shellguard-entry.tsx
npx vitest run src/stores/navigation/codec.test.ts \
  src/stores/navigation/merge.test.ts \
  src/stores/navigation/revalidator.test.ts \
  src/stores/navigation/store.test.ts \
  src/stores/navigation/selectors.test.ts \
  src/shell/rail/Rail.test.tsx src/shell/rail/RailRow.test.tsx \
  src/shell/rail/railNodes.test.ts --maxWorkers=4 --configLoader runner
npm run typecheck
npm run shellguard
```

If typecheck exposes additional fixture files containing the removed record
field, enumerate each path explicitly in a second Biome invocation before
staging. Never format a whole source directory or a glob.

- [ ] **Step 6: Commit named paths and obtain two independent reviews**

Stage only actual Task 7 paths by name. Commit the no-counter contract and
implementation as a focused commit; do not fold it into prior commits. Obtain:

1. a fresh binding-spec review proving resource authority/history/reconnect and
   every no-counter acceptance criterion; then
2. a distinct quality/security/race/test review.

Fix every Critical/Important finding with a red regression and scoped re-review.
Do not silently delete coverage that formerly asserted counters; replace it with
content, reconstruction, history-miss, strict-schema, or identity proof.

- [ ] **Step 7: Hand off the reviewed redesign slice to final verification**

Record the exact reviewed Task 7 HEAD and confirm its ancestry from redesign
baseline `383c93802b1ead5816155788cb5800fce59e2ce7`. Do not push or integrate in
this step. Task 8 owns all whole-slice and delivery claims.

---

## Slice E — Final review, gates, and separate PR

### Task 8: Verify the redesign and deliver without merging

- [ ] **Step 1: Review the exact redesign range**

Review
`383c93802b1ead5816155788cb5800fce59e2ce7..HEAD`. The first fresh reviewer must
return explicit PASS/FAIL for the binding spec, every no-counter requirement,
the present→gone→present/never-known lifecycle, exact/cross-View/evicted history,
resource-authority checks, reconnect equal/higher/lower and generation fences,
v1 compatibility, generated contracts, and all prior accepted I2 behavior. Only
after PASS, use a different reviewer for correctness, security, race safety,
maintainability, and assertion quality. Fix Critical/Important findings with a
red regression and obtain scoped re-review. After two incomplete cycles on one
finding, stop and reslice rather than churn.

- [ ] **Step 2: Run consistent exact-head gates**

Activate `superpowers:verification-before-completion`, inspect `make help`, and
on the exact reviewed clean head run sequentially:

```bash
make generate
make lint
make vet
make test
make test-web
make test-race
make test-web-browser
make merge-approval-gate
git diff --check 383c93802b1ead5816155788cb5800fce59e2ce7..HEAD
git status --short
git log --oneline 383c93802b1ead5816155788cb5800fce59e2ce7..HEAD
```

Every gate counts as passed only on exit 0. A timeout, sandbox denial, missing
Chrome/tool, or launch failure remains incomplete. Generated output must leave
no unexplained diff, and the worktree must finish clean.

- [ ] **Step 3: Reconcile acceptance and write durable evidence**

Create/update
`.superpowers/sdd/2026-09-01-navigation-entity-deltas-unblock-plan/final-report.md`
with:

- baseline/head, clean status, and every redesign commit;
- every prior blocker and no-counter criterion mapped to implementation plus
  red/green evidence;
- task and whole-range review verdicts and fixes;
- every focused/race/generated/full/browser command with its exact exit result;
- gone/reappearance, exact/cross-View/eviction, reconnect, structural identity,
  and unchanged-row evidence;
- all changed/generated paths and overlap with Atomic paging; and
- explicit status `DONE`, `DONE_WITH_CONCERNS`, or `BLOCKED`.

- [ ] **Step 4: Integrate safely, re-review, rerun, and update the PR**

Fetch only the intended `origin/main` ref without tags. Recheck target branch,
base ref, ancestry, dirty overlap, and same-path overlap immediately before a
safe replay/integration. Never use `git pull`, force-push, amend, or merge the PR.
On the resulting delivery branch, obtain a fresh whole-package overlap review
against that exact `origin/main`, then rerun every Step 2 gate on the integrated
HEAD. Under the user's standing authorization, push/open or update the separate
Navigation PR only after both review and gates are green. Watch Actions and
roborev; fix valid findings with the same red/review/gate discipline and update
the remote non-force. **Never merge.**
