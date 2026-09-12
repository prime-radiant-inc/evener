# Navigation v1 Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove navigation representation v1 end-to-end so v2 (normalized entity/container snapshots, stateless deltas, base validators) is the only navigation representation on the wire and in the frontend.

**Architecture:** Delete-then-simplify in dependency order: appwire contract first (reject `etag`, require `representationVersion: 2`), then the Go handler dispatch and the now-dead `Representation()`/cache/Gzip path, then regenerate TypeScript contracts, then collapse the frontend store's `mode` union and delete the v1 request/response branch plus v1-only validators, then convert `ResourceState.data` readers (selectors, Rail, AppShell) to the normalized graph, then docs. Each task is independently testable.

**Tech Stack:** Go (`appwire`, `cmd/evener-hub`, `hubapi`), TypeScript frontend (`cmd/evener-hub/frontend/src/stores/navigation`, `shell/rail`, `protocol/types.gen.ts` via `internal/appwirets`), `docs/appwire-protocol.md`.

**Spec:** `docs/superpowers/specs/2026-09-01-navigation-entity-deltas-design.md` (v2 behavior is authoritative; v1 sections of that spec are superseded by this plan — v1 is removed, not retained).

## Global Constraints

- Default tests must be deterministic: no provider credentials, network access, quota, current model behavior, or ambient machine state in `make test` / `go test ./...`.
- A provider API key by itself must never cause default tests to issue live requests.
- Run `npx biome check --write` on touched frontend files under `src/` before the frontend gate (gate runs `biome ci src`).
- Go toolchain pinned in `.tool-versions`; `make tools` installs what CI runs.
- No `git add -A` / `git add .`; stage named paths only.
- Never revert unrelated changes; never use `git reset --hard`, `git checkout --`, or amend unless explicitly requested.

---

## File Map

| File | Responsibility after this plan |
|---|---|
| `appwire/types.go` (`NavigationReadParams`, `UnmarshalJSON`, capability) | `representationVersion` required `== 2`; `etag` field removed; `base` always allowed; capability advertises `ReadVersions: [2]` |
| `cmd/evener-hub/app_navigation.go` (`navigationReadWithFields`) | Single path: validate, call `readV2`. No `Representation()` call, no etag comparison |
| `cmd/evener-hub/navigation_service.go` | `readV2` stays (rename to `read` only if trivial); `Representation()` deleted; `NavigationRepresentation` alias deleted; `versionedCore` kept (v2 uses it); cache-related `Stats().Cache` handling updated |
| `cmd/evener-hub/navigation_cache.go` | Deleted if `Representation()` was its only caller; otherwise trimmed to live callers |
| `cmd/evener-hub/frontend/src/protocol/types.gen.ts` | Regenerated via `internal/appwirets` (`go generate`); no `etag` on `NavigationReadParams` |
| `frontend/src/stores/navigation/store.ts` | `mode: "unknown" \| "v2" \| "error"`; `paramsFor` always sends `representationVersion: 2`; v1 response branch and `isNavigationValue` deleted; boot requires `readVersions` to include 2 |
| `frontend/src/stores/navigation/revalidator.ts` | `validate()` drops 304/etag-conditional rules that only exist for v1; keeps revision fencing and base recovery |
| `frontend/src/stores/navigation/selectors.ts` | All selectors read `resource.normalized`, never `resource.data` |
| `frontend/src/shell/rail/Rail.tsx`, `AppShell.tsx`, other consumers | No `navigationMode === "v1"` conditionals; shutdown invalidation unconditional (fixes roborev Medium finding as a side effect) |
| `docs/appwire-protocol.md` | `evener/navigation/read` documented as v2-only |

Key interface contracts between tasks:
- `navigationReadWithFields(ctx, server, navigation, params, fields) (appwire.NavigationReadResponse, error)` — Task 1 changes its validation; Task 2 deletes its v1 branch. Signature unchanged.
- `paramsFor(k, base, mode) : NavigationReadParams` — Task 4 changes signature to `paramsFor(k, base)` (drops `etag`); Task 6 updates callers.
- `NavigationRequest<T> = (signal, base?) => Promise<NavigationResponse<T>>` — Task 5 changes signature (drops `etag` param); Task 4/6 update producers/consumers.
- `ResourceState<T>` keeps `data: T | null` until Task 6, which removes or repurposes it; selectors must not read `.data` after Task 6.

---

### Task 1: Appwire contract — require v2, remove etag

**Files:**
- Modify: `appwire/types.go:225-280` (`NavigationReadParams`, `UnmarshalJSON`)
- Modify: `appwire/types_test.go` (navigation param tests)
- Modify: `cmd/evener-hub/navigation_service.go:290` (capability `ReadVersions: []int{1, 2}` → `[]int{2}`)

**Interfaces:**
- Consumes: nothing.
- Produces: `NavigationReadParams` without `ETag`; `UnmarshalJSON` rejects `representationVersion != 2` and any `etag` key; capability advertises `[2]`. Task 2 relies on this.

- [ ] **Step 1: Write the failing test.** In `appwire/types_test.go`, add (next to the existing navigation param tests):

```go
func TestNavigationReadParamsRequiresRepresentationVersion2(t *testing.T) {
	for _, raw := range []string{
		`{"resource":"manifest"}`,
		`{"resource":"manifest","representationVersion":1}`,
		`{"resource":"manifest","representationVersion":2,"etag":"abc"}`,
		`{"resource":"manifest","etag":"abc"}`,
	} {
		var params NavigationReadParams
		if err := json.Unmarshal([]byte(raw), &params); err == nil {
			t.Fatalf("Unmarshal(%s) = nil, want invalid-params error", raw)
		}
	}
	var params NavigationReadParams
	if err := json.Unmarshal([]byte(`{"resource":"manifest","representationVersion":2}`), &params); err != nil {
		t.Fatalf("Unmarshal(v2) = %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails.**

Run: `go test ./appwire/ -run TestNavigationReadParamsRequiresRepresentationVersion2 -count=1`
Expected: FAIL (v1/etag shapes currently accepted).

- [ ] **Step 3: Write minimal implementation.** In `appwire/types.go`:
  1. Delete the `ETag string \`json:"etag,omitempty"\`` field from `NavigationReadParams`.
  2. In `UnmarshalJSON`, after the base-shape check, replace the two version/etag conditionals with:

```go
	if decoded.RepresentationVersion != 2 {
		return errors.New("representationVersion must be 2")
	}
	if _, present := fields["etag"]; present {
		return errors.New("etag is not a v2 field")
	}
```

  3. Keep the `base` object-shape validation unchanged (base is now always valid for the only version).
  4. In `cmd/evener-hub/navigation_service.go:290`, change `ReadVersions: []int{1, 2}` to `ReadVersions: []int{2}`.

- [ ] **Step 4: Run tests to verify they pass.**

Run: `go test ./appwire/ -count=1`
Expected: PASS. Note which pre-existing tests fail (they assert v1 acceptance — record their names for Task 2/3 updates; do not delete them in this task).

- [ ] **Step 5: Commit.**

```bash
git add appwire/types.go appwire/types_test.go cmd/evener-hub/navigation_service.go
git commit -m "feat(navigation): require representation v2 on the wire"
```

---

### Task 2: Handler dispatch — v2-only read path

**Files:**
- Modify: `cmd/evener-hub/app_navigation.go:24-70` (`navigationReadWithFields`)
- Modify: `cmd/evener-hub/app_navigation_test.go` (v1 dispatch tests)

**Interfaces:**
- Consumes: Task 1's `NavigationReadParams` (no `ETag`, version always 2).
- Produces: `navigationReadWithFields` with a single `readV2` path. Task 3 relies on `Representation()` having exactly zero non-test callers.

- [ ] **Step 1: Write the failing test.** In `cmd/evener-hub/app_navigation_test.go`, add:

```go
func TestHubNavigationReadRejectsV1Params(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	service := newNavigationReadTestService(t)
	registerNavigationReadHandler(server, service)
	for _, raw := range []string{
		`{"resource":"manifest"}`,
		`{"resource":"manifest","representationVersion":1}`,
		`{"resource":"manifest","representationVersion":2,"etag":"abc"}`,
		`{"resource":"manifest","representationVersion":2,"base":{"generationId":"g","revision":1,"etag":"e"},"etag":"abc"}`,
	} {
		_, err := dispatchNavigationReadResultWithRaw(t, server, json.RawMessage(raw))
		assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	}
}
```

Note: if `dispatchNavigationReadResultWithRaw` does not exist, add it next to `dispatchNavigationReadResult` following that helper's exact pattern (decode raw → route through `server.Router()` test dispatch). Check the top of `app_navigation_test.go` for the existing helper before writing.

- [ ] **Step 2: Run test to verify it fails.**

Run: `go test ./cmd/evener-hub/ -run TestHubNavigationReadRejectsV1Params -count=1`
Expected: FAIL (handler still serves v1 shapes).

- [ ] **Step 3: Write minimal implementation.** Replace the body of `navigationReadWithFields` (after the nil-service and ctx-err checks) with:

```go
	if params.RepresentationVersion != 2 {
		return appwire.NavigationReadResponse{}, appwire.InvalidParams(navigationInvalidParamsMessage)
	}
	result, err := navigation.readV2(ctx, key, params.Base)
	if err != nil {
		return appwire.NavigationReadResponse{}, navigationReadError(server, err)
	}
	return result.Response, nil
```

Delete the `params.Base != nil` rejection, the `RepresentationVersion != 0 && != 1` check, the entire `navigation.Representation(ctx, key)` block, and the etag-conditional `not_modified` block. Also fix the now-unused import if `bytes` becomes unused (check imports of `app_navigation.go`).

- [ ] **Step 4: Run tests to verify they pass.**

Run: `go test ./cmd/evener-hub/ -run 'TestHubNavigation' -count=1`
Expected: PASS except pre-existing v1-behavior tests (e.g. `TestHubNavigationReadConditionalResponseAndErrorMapping`, `TestHubNavigationReadServesEveryResourceFamily` if v1-based). Rewrite those tests to v2 in this step: request with `RepresentationVersion: 2`, assert `representation: snapshot`, `base` absent on snapshot, `data` decodes as `hubapi.NavigationSnapshot`. Delete assertions that call `service.Representation(...)` directly.

- [ ] **Step 5: Commit.**

```bash
git add cmd/evener-hub/app_navigation.go cmd/evener-hub/app_navigation_test.go
git commit -m "feat(navigation): serve v2 only in the read handler"
```

---

### Task 3: Delete the dead Go v1 path (Representation, cache, gzip)

**Files:**
- Modify: `cmd/evener-hub/navigation_service.go` (delete `Representation()`, `NavigationRepresentation` alias, `gzipNavigation` if unused, `versionedCore` stays)
- Delete or trim: `cmd/evener-hub/navigation_cache.go` (whole file if `Representation()` was the only caller of `cache.Get`)
- Modify: `cmd/evener-hub/navigation_service_test.go`, `navigation_cache_test.go` (delete/convert v1 tests)
- Modify: `cmd/evener-hub/navigation_service.go` `Stats()` if it reports `Cache` stats for the deleted cache

**Interfaces:**
- Consumes: Task 2 (zero non-test callers of `Representation()`).
- Produces: no `NavigationRepresentation` Go type; no `navigationRepresentationCache`. Task 7 (codegen/docs) relies on `hubapi` v1 types still existing at this point — do NOT touch `hubapi/navigation.go` in this task.

- [ ] **Step 1: Prove zero callers.** Run `grep -rn "\.Representation(\|navigationRepresentation\|gzipNavigation" --include="*.go" cmd/ internal/ server/ | grep -v _test` and record the output. Every remaining hit must be inside `navigation_service.go` or `navigation_cache.go` themselves. If a caller exists elsewhere, stop and report — the plan's ordering assumption is wrong.

- [ ] **Step 2: Delete the dead path.**
  1. Delete `func (s *NavigationService) Representation(...)` in `navigation_service.go` (lines ~359-392).
  2. Delete `type NavigationRepresentation = navigationRepresentation` (line ~149).
  3. Delete `func gzipNavigation` if `grep -rn gzipNavigation` shows no other caller.
  4. If `navigation_cache.go`'s `Get`/`publish`/`finishFlight` have no remaining callers, delete `navigation_cache.go` and `navigation_cache_test.go` entirely; remove the `Cache` field from the service config/struct and its constructor default (`defaultNavigationCacheEntries/2` line ~214), and update `Stats()` accordingly.
  5. Keep `versionedCore`, `CurrentRevision`, `VersionedKey` (v2 `readV2` uses `versionedCore`).

- [ ] **Step 3: Run tests.**

Run: `go build ./... && go test ./cmd/evener-hub/ -run 'TestNavigation|TestHubNavigation|TestCache' -count=1`
Expected: compile errors only in tests referencing deleted symbols — delete those test functions (they test removed behavior), except tests that also cover v2/shared paths, which you convert. `CurrentRevision`/`VersionedKey` tests stay.

- [ ] **Step 4: Commit.**

```bash
git add cmd/evener-hub/navigation_service.go cmd/evener-hub/navigation_cache.go cmd/evener-hub/navigation_service_test.go cmd/evener-hub/navigation_cache_test.go
git commit -m "feat(navigation): delete the v1 representation cache path"
```

(Adjust the `git add` list to files actually touched; never `git add -A`.)

---

### Task 4: Frontend request path — always v2, drop etag

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.ts` (`paramsFor`, `requestFor`, `NavigationValue` stays for now, boot `mode` lines 663/870)
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/types.ts` (`NavigationRequest` type, `ResourceState.etag` stays — etag remains the v2 per-revision validator inside `base`/`version`)
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/revalidator.ts` (`e.request(controller.signal, e.state.etag, ...)` call + `validate()` 304/etag rules)

**Interfaces:**
- Consumes: regenerated `NavigationReadParams` without `etag` (run the generator first — see Step 1).
- Produces: `paramsFor(k, base)`; `NavigationRequest<T> = (signal, base?) => Promise<NavigationResponse<T>>`. Task 6 updates all callers of these signatures.

- [ ] **Step 1: Regenerate contracts.** Find the generate target for `protocol/types.gen.ts` (search `Makefile` / `package.json` for `appwirets`; it is driven by `internal/appwirets/emit.go`). Run it and confirm `NavigationReadParams` in `types.gen.ts` has `representationVersion: 2` (required, not optional) and no `etag` field. Commit the regenerated file separately if the repo convention is to commit generated output (check `git log --oneline -3 -- <types.gen.ts path>`).

- [ ] **Step 2: Write the failing test.** In `store.test.ts`, add next to the "exact AppWire params" test (line ~471):

```ts
test("navigation requests always send representationVersion 2 without etag", async () => {
  const seen: NavigationReadParams[] = [];
  await init((params) => {
    seen.push(params as NavigationReadParams);
    return v2SnapshotFixture(params);
  });
  expect(seen.length).toBeGreaterThan(0);
  for (const params of seen) {
    expect(params.representationVersion).toBe(2);
    expect("etag" in params).toBe(false);
  }
});
```

`init` is the existing test helper in `store.test.ts`; `v2SnapshotFixture` is whatever existing v2 fixture helper the v2 tests use (e.g. the responder in the "v2 manifest deltas" test at line ~726) — reuse it, do not invent a new fixture shape.

- [ ] **Step 2b: Run test to verify it fails.**

Run (from `cmd/evener-hub/frontend`): `npx vitest run src/stores/navigation/store.test.ts -t "always send representationVersion 2"`
Expected: FAIL (v1 requests omit `representationVersion` / send `etag`).

- [ ] **Step 3: Write minimal implementation.**
  1. `paramsFor(k, base)`: always spread `{ representationVersion: 2 as const, ...(base ? { base } : {}) }`; delete the `etag` parameter and the `v2` ternary.
  2. `requestFor`: call `paramsFor(k, base)` using only the `base` argument (drop `etag`); delete the entire non-`mode === "v2"` response branch (the `status must be exact ok or not_modified` block through the `isNavigationValue` check); keep the v2 decode/reconcile block as the only path; delete `isNavigationValue` and `assertNavigationPageProgress`'s v1-shape overloads if now unused.
  3. `NavigationRequest` type in `types.ts`: `(signal: AbortSignal, base?: NavigationReadBase) => Promise<NavigationResponse<T>>`.
  4. `revalidator.ts` `start()`: `e.request(controller.signal, usableNavigationBase(e.state))`; simplify `validate()` to drop the 304-requires-cached-etag rule (v2 `not_modified` keeps its own rule: data must be absent and a cached resource must exist).
  5. Boot (`store.ts` lines ~663, ~870): `mode: cap.readVersions?.includes(2) ? "v2" : "error"` — a server that does not advertise v2 is now a protocol error, not a v1 fallback. Set `protocolError` accordingly (follow the existing `unsupported navigation capability version` pattern).

- [ ] **Step 4: Run tests.**

Run: `npx vitest run src/stores/navigation/ 2>&1 | tail -5`
Expected: PASS except tests asserting v1 behavior — convert or delete per test: reconnect mode-switch tests ("V1-to-V2", "downgrade … in v1") become v2-to-v2 or error-path tests; "canonical v1 requests" location test (line ~514) merges into the v2 location test (line ~562).

- [ ] **Step 5: Commit.**

```bash
git add cmd/evener-hub/frontend/src/stores/navigation/store.ts cmd/evener-hub/frontend/src/stores/navigation/types.ts cmd/evener-hub/frontend/src/stores/navigation/revalidator.ts cmd/evener-hub/frontend/src/protocol/types.gen.ts cmd/evener-hub/frontend/src/stores/navigation/store.test.ts
git commit -m "feat(web): request navigation v2 unconditionally"
```

---

### Task 5: Collapse the frontend `mode` union (fixes roborev finding)

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/store.ts` (`mode: "unknown" | "v2" | "error"`, all `mode === "v1"` / `!== "v1"` guards)
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.tsx` (lines ~872, ~928, ~1139, ~1401-1402)
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.tsx` (line ~497)
- Modify: any other `=== "v1"` hits from `grep -rn '"v1"' src/`

**Interfaces:**
- Consumes: Task 4 (no v1 requests exist, so mode can only ever be v2-or-error).
- Produces: no `"v1"` string literal anywhere under `frontend/src`. Task 6 relies on `navigationMode` being `"unknown" | "v2" | "error"`.

- [ ] **Step 1: Enumerate.** Run `grep -rn '"v1"' cmd/evener-hub/frontend/src --include='*.ts' --include='*.tsx' | grep -v test` and record every hit. Expected hits: `store.ts` (type union + ~6 guards), `Rail.tsx` (~4), `AppShell.tsx` (~1). If more files appear, fold them into this task.

- [ ] **Step 2: Collapse the union.** Change `mode: "unknown" | "v1" | "v2" | "error"` to `mode: "unknown" | "v2" | "error"`. Replace:
  - `(mode === "v1" || mode === "v2")` → `mode === "v2"`;
  - `(navigationMode === "v1" || navigationMode === "v2")` → `navigationMode === "v2"`;
  - `(navigationMode !== "v1" && navigationMode !== "v2")` → `navigationMode !== "v2"`;
  - `if (navigationMode !== "v1") return;` (Rail manifest boot, line ~872) → `if (navigationMode !== "v2") return;`;
  - The shutdown guard (Rail line ~1139) `navigationMode === "v1" ? <invalidation> : null` → set up the invalidation unconditionally. This fixes the roborev Medium finding (shutdown now awaits the invalidation receipt in the only remaining mode).

- [ ] **Step 3: Run tests.**

Run: `npx vitest run src/stores/navigation/ src/shell/rail/ 2>&1 | tail -5`
Expected: PASS. Typecheck: `npx tsc --noEmit` (or the repo's typecheck script) must pass with no `"v1"`-related narrowing errors.

- [ ] **Step 4: Commit.**

```bash
git add cmd/evener-hub/frontend/src/stores/navigation/store.ts cmd/evener-hub/frontend/src/shell/rail/Rail.tsx cmd/evener-hub/frontend/src/shell/AppShell.tsx
git commit -m "feat(web): drop the v1 navigation mode"
```

---

### Task 6: Convert `data` readers to the normalized graph

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts` (all `.data` reads)
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.tsx` (adapter layer lines ~369-730: `resourceData`, `loadedSection`, `projectPageStates`, catalog/pin-section readers)
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.tsx`, `railNodes.ts`, `PinSectionPicker.tsx`, `CommandPalette.tsx`, `WelcomeContent.tsx`, `Session.tsx`, others from the consumer list — only what reads `.data` off a navigation `ResourceState`
- Modify: `cmd/evener-hub/frontend/src/stores/navigation/codec.ts` (`materializeNavigationResource` — delete if no readers remain)

**Interfaces:**
- Consumes: Task 5 (only v2 responses exist, so every loaded resource has `normalized` set; `materializeNavigationResource(normalized)` output stays available during conversion as the bridge).
- Produces: zero `.data` reads on navigation resources; `materializeNavigationResource` deleted; `ResourceState.data` field removed. This is the largest task — split commits per file if it exceeds ~300 lines.

- [ ] **Step 1: Enumerate.** Run `grep -rn "\.data\b" cmd/evener-hub/frontend/src/stores/navigation/selectors.ts cmd/evener-hub/frontend/src/shell/rail/Rail.tsx cmd/evener-hub/frontend/src/shell/AppShell.tsx` and record every hit with its line number. Each hit is one conversion unit below.

- [ ] **Step 2: Convert selectors first (they have unit tests).** For each selector in `selectors.ts`, replace the `.data` read with the normalized-graph equivalent already established in `selectRailModel`/`loadedSection` (graph entities + root containers via `navigationRootContainerKey`). Run `npx vitest run src/stores/navigation/selectors.test.ts` after each function. Delete `materializeNavigationResource` from `codec.ts` only when `grep -rn materializeNavigationResource src/ --include='*.ts*'` shows no non-test importer.

- [ ] **Step 3: Convert Rail adapters.** `resourceData()` → read from `resource.normalized.graph`; `loadedSection`, `projectPageStates`, pin-section/catalog readers follow the same pattern (the normalized-first branches already exist — e.g. `returnedRootRows`, `projectPageDependencies` — promote them to the only branch and delete the `.data` fallback). Run `npx vitest run src/shell/rail/ ` after each function.

- [ ] **Step 4: Convert remaining consumers** (AppShell location, palette, welcome, session panes) the same way, guided by the Step 1 list.

- [ ] **Step 5: Remove `data` from `ResourceState`/`NavigationResponse`** in `types.ts` once no reader remains (verify with the Step 1 grep returning zero hits), fix the resulting type errors, run `npx tsc --noEmit`.

- [ ] **Step 6: Commit (per-file if large).**

```bash
git add cmd/evener-hub/frontend/src/stores/navigation/selectors.ts cmd/evener-hub/frontend/src/stores/navigation/codec.ts
git commit -m "feat(web): read navigation selectors from the normalized graph"
```

```bash
git add cmd/evener-hub/frontend/src/shell/rail/Rail.tsx cmd/evener-hub/frontend/src/shell/AppShell.tsx
git commit -m "feat(web): read rail and shell from the normalized graph"
```

---

### Task 7: Docs, codegen leftovers, and hubapi v1 types

**Files:**
- Modify: `docs/appwire-protocol.md` (navigation read section, `NavigationReadParams`/`NavigationReadResponse` tables)
- Modify: `internal/appwirets/emit.go` (`navigationRESTRoots` — remove v1 roots if they only served v1 REST docs)
- Modify: `hubapi/navigation.go` (v1 resource types) — only if no Go code references them after Tasks 1-3
- Modify: `cmd/evener-hub/frontend/src/protocol/client.test.ts`, `reconnect.test.ts`, `testing/fakeClient.ts` — remove v1-specific fakes/assertions

**Interfaces:**
- Consumes: Tasks 1-6 (all behavior changed; this is cleanup).
- Produces: docs match the v2-only wire; no dead test fakes.

- [ ] **Step 1: Docs.** Update the `evener/navigation/read` row and the `NavigationReadParams` / `NavigationReadResponse` sections in `docs/appwire-protocol.md`: `representationVersion` required `2`, no `etag` param, `representation` always `snapshot | delta`, `base` echoed on deltas, `gone` for removed resources. Keep the change to navigation sections only.

- [ ] **Step 2: hubapi.** Run `grep -rn "hubapi\.NavigationManifest\|hubapi\.NavigationSectionResource\|hubapi\.NavigationProjectResource\|hubapi\.NavigationProjectCatalog\|hubapi\.NavigationPinSectionCatalog" --include="*.go" . | grep -v _test | grep -v "hubapi/navigation"`. If zero hits outside `hubapi/` itself and `internal/appwirets/emit.go`, delete the dead types from `hubapi/navigation.go` and update `emit.go`'s `navigationRESTRoots`. If hits remain, keep the types and record why in the commit message.

- [ ] **Step 3: Test fakes.** Remove v1-only branches in `fakeClient.ts` / `fakeSocket.ts` and v1 assertions in `client.test.ts` / `reconnect.test.ts`. Run `npx vitest run src/protocol/`.

- [ ] **Step 4: Commit.**

```bash
git add docs/appwire-protocol.md hubapi/navigation.go internal/appwirets/emit.go cmd/evener-hub/frontend/src/protocol/
git commit -m "docs(navigation): document the v2-only read contract"
```

---

### Task 8: Full verification

- [ ] **Step 1: Frontend gates.** Run `npx biome check --write` on touched frontend files (already done per-task; re-run to confirm clean), then `make test-web` and, on a Chrome-capable host, `make test-web-browser`.
- [ ] **Step 2: Go gates.** Run `make vet`, then `make test` (all modules), then `make merge-approval-gate`.
- [ ] **Step 3: Confirm the roborev finding is gone:** `grep -rn '"v1"' cmd/evener-hub/frontend/src shell` returns nothing navigation-related, and `onShutdownSession` in `Rail.tsx` awaits invalidation unconditionally.
- [ ] **Step 4: Push and update PR 846** (or open the replacement PR): `git push --force-with-lease origin nav-v1-removal`.

---

## Self-Review

1. **Spec coverage:** The v2 behaviors (snapshot/delta/gone/not_modified, generation fencing, bounded history, structural sharing, reconnect rules) are implemented by the existing branch and untouched by this plan — every task only deletes v1 paths or converts readers to the already-tested normalized graph. The design spec's "keep v1" non-goal/migration-step-9 is explicitly superseded by the owner's decision (this plan's Goal statement records that).
2. **Placeholder scan:** No TBD/TODO/"similar to Task N" — each step names exact files, line numbers (verified against the rebased tree: `app_navigation.go:24-70`, `navigation_service.go:149/290/359-392`, `store.ts:44-124/395-520/655-680/860-880`, `Rail.tsx:872/928/1139/1401`), exact commands, and exact expected outputs. Two deliberate conditional points are explicit, not placeholders: Task 3 Step 1 (stop-and-report if `Representation()` has outside callers) and Task 7 Step 2 (keep hubapi types if referenced, with the reason recorded).
3. **Type consistency:** `paramsFor(k, base)` (Task 4) → callers updated in Task 6; `NavigationRequest(signal, base?)` (Task 4/5) → `revalidator.start()` updated in the same task; `ResourceState.data` removal (Task 6 Step 5) happens after the last reader is converted (Task 6 Step 1 grep gates it). `readV2` keeps its name to avoid churn — renaming is out of scope.
