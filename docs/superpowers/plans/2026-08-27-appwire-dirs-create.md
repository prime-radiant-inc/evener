# AppWire Directory Creation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Spawn's REST directory creation request with the typed hub AppWire method `evener/dirs/create` while preserving behavior and deleting the retired REST route.

**Architecture:** The hub owns one typed `evener/dirs/create` handler. A focused helper keeps path normalization, `os.Stat`, `MkdirAll`, and the existing injected filesystem seam together; the HTTP handler is deleted rather than retained as a compatibility path. Spawn calls that hub method through the existing shared AppWire client and keeps its current confirmation/error UI.

**Tech Stack:** Go, JSON-RPC-style AppWire over WebSocket, React/TypeScript, Vitest, generated AppWire TypeScript/docs catalogs, Makefile verification gates.

**Spec:** `docs/superpowers/specs/2026-08-27-appwire-dirs-create-design.md`

## Global Constraints

- Work only in `/Users/jesse/git/prime-radiant/evener/.worktrees/appwire-dirs-create` on `codex/appwire-dirs-create`.
- Base the branch on the explicitly fetched `origin/main`; do not include model-list, navigation, or tree-residue changes.
- Preserve the exact path normalization, `0755` creation mode, idempotent existing-directory result, conflict text, and internal failure text from `handleAPIDirCreate`.
- The `/rpc` route remains the sole authenticated browser transport for this operation; do not add a REST fallback.
- Read `docs/developing-evener/testing.md` before changing tests; default tests remain deterministic and provider/network independent.
- Do not add an absence assertion for `/api/dirs/create`; delete obsolete route tests and keep meaningful AppWire behavior tests.
- Run the four-angle read-only simplify-code review before implementation and again after implementation; apply only quality-preserving findings.

---

### Task 1: Commit the wire contract and behavior-first test plan

**Files:**
- Modify: `appwire/types.go` — add `MethodEvenerDirsCreate`, `DirsCreateParams`, and `DirsCreateResponse`.
- Modify: `appwire/protocol.go` — add the `ScopeHub` catalog row.
- Modify: `appwire/client.go` — add `Client.DirsCreate`.
- Test: `appwire/cov_rhub_appwire_test.go` — add the typed wrapper round trip.
- Test: `cmd/evener-hub/app_dirs_test.go` — add the real hub AppWire behavior contract.

**Interfaces:**
- Produces the wire method `evener/dirs/create`, params `{path:string}`, and result `{path:string,created:boolean}`.
- The hub behavior test calls `client.DirsCreate(context.Context, appwire.DirsCreateParams)` against `newHubRPCTestServer`.

- [ ] **Step 1: Write failing Go behavior tests.**

  Add cases that call the not-yet-registered method through a real AppWire client and assert:

  ```go
  response, err := client.DirsCreate(context.Background(), appwire.DirsCreateParams{Path: target})
  if err != nil { t.Fatal(err) }
  if response.Path != filepath.Clean(target) || !response.Created { t.Fatalf("response=%+v", response) }
  ```

  Cover a missing nested directory, an existing directory returning
  `Created:false`, a file returning `CodeConflict` and the existing message,
  a relative path returning `CodeInvalidParams`, `~` expansion, and an injected
  `MkdirAll` failure returning `CodeInternalError` with its message. Use temp
  directories and `WebConfig.MkdirAll`; do not touch the real home or network.

- [ ] **Step 2: Run the tests and verify the failure is the missing contract.**

  Run:

  ```bash
  go test ./appwire ./cmd/evener-hub -run 'TestClientRequestWrappersRoundTrip|TestHubRPCDirsCreate' -count=1
  ```

  Expected: failure because `DirsCreate` and the hub method are not yet
  defined/registered, not because of a malformed fixture.

- [ ] **Step 3: Add the minimal wire declarations and wrapper.**

  Follow the neighboring `PathsComplete` declarations:

  ```go
  MethodEvenerDirsCreate = "evener/dirs/create"

  type DirsCreateParams struct {
      Path string `json:"path"`
  }

  type DirsCreateResponse struct {
      Path string `json:"path"`
      Created bool `json:"created"`
  }

  func (c *Client) DirsCreate(ctx context.Context, params DirsCreateParams) (DirsCreateResponse, error) {
      var out DirsCreateResponse
      err := c.request(ctx, MethodEvenerDirsCreate, params, &out)
      return out, err
  }
  ```

  Add the catalog summary `Creates a missing working directory and its
  parents for Spawn preflight.` and the wrapper round-trip case asserting the
  method, params, and decoded response.

- [ ] **Step 4: Run the test to confirm the remaining red state is server registration.**

  Run the same focused command. Expected: the wrapper round trip passes and
  the hub behavior test reports `method not found` until Task 2 registers the
  handler.

- [ ] **Step 5: Commit the contract tests and declarations.**

  ```bash
  git add appwire/types.go appwire/protocol.go appwire/client.go appwire/cov_rhub_appwire_test.go cmd/evener-hub/app_dirs_test.go
  git commit -m "feat: define AppWire directory creation contract"
  ```

### Task 2: Run the pre-implementation simplify review

**Files:**
- Review: the Task 1 diff from `git diff @{upstream}...HEAD` plus any working diff.

- [ ] **Step 1: Dispatch the four read-only simplify reviewers in parallel.**

  Use the simplify-code skill at
  `/Users/jesse/.codex/plugins/cache/simplify-code-dev/simplify-code/0.1.0/skills/simplify-code/SKILL.md`, assigning the Reuse, Simplification, Efficiency, and Altitude angles to separate reviewers. Reviewers do not edit.

- [ ] **Step 2: Apply only quality-preserving findings.**

  Deduplicate findings by line/mechanism. Do not delete or weaken behavior
  tests, rename the new public method/types, or alter the wire behavior.

- [ ] **Step 3: Re-run the focused tests and commit any review-only cleanup.**

  ```bash
  go test ./appwire ./cmd/evener-hub -run 'TestClientRequestWrappersRoundTrip|TestHubRPCDirsCreate' -count=1
  git diff --check
  git commit -am "refactor: simplify directory AppWire contract"
  ```

  Skip the cleanup commit when the reviewers report the contract is already
  clear; record that result in the implementation handoff.

### Task 3: Implement the hub operation and remove the REST route

**Files:**
- Create: `cmd/evener-hub/app_dirs.go` — focused hub directory-creation helper.
- Modify: `cmd/evener-hub/app_rpc.go` — register `evener/dirs/create`.
- Modify: `cmd/evener-hub/web.go` — remove `/api/dirs/create` registration.
- Modify: `cmd/evener-hub/web_api.go` — remove `handleAPIDirCreate`.
- Modify: `cmd/evener-hub/app_rpc_test.go` — include the new method in the exact hub-handler set.
- Modify: `cmd/evener-hub/web_test.go` — delete the obsolete REST behavior test.
- Modify: `cmd/evener-hub/cov_web_core_api_test.go` — remove only directory REST probes.
- Modify: `cmd/evener-hub/cov_core_api_pass4_fuzz_test.go` — remove only direct REST-handler probes.
- Modify: `cmd/evener-hub/web_mutating_fuzz_test.go` — remove the retired route and seed.
- Modify: `cmd/evener-hub/web_fuzz_test.go` — remove the route from the explanatory exclusion comment.
- Modify: `cmd/evener-hub/sandbox_test.go` and `cmd/evener-hub/sandbox_selftest_test.go` — remove the route-only recording seam and block.
- Modify: `cmd/evener-hub/internal/hubcore/config.go` — update the seam comment to name the AppWire operation.

**Interfaces:**
- `hubDirsCreate(cfg hubcore.WebConfig, params appwire.DirsCreateParams) (appwire.DirsCreateResponse, error)` returns the typed result or one of the standard AppWire errors.
- `registerMiscHandlers` registers the method with `appserver.HandleTyped`.

- [ ] **Step 1: Run the red behavior test from Task 1.**

  ```bash
  go test ./cmd/evener-hub -run TestHubRPCDirsCreate -count=1
  ```

  Confirm it fails with AppWire method-not-found before writing production
  behavior.

- [ ] **Step 2: Extract the existing behavior into the focused helper.**

  Move the exact sequence into `app_dirs.go`, returning:

  ```go
  if path == "" { return appwire.DirsCreateResponse{}, appwire.InvalidParams("path is required") }
  if !filepath.IsAbs(path) { return appwire.DirsCreateResponse{}, appwire.InvalidParams("absolute path required") }
  if info, err := os.Stat(path); err == nil {
      if !info.IsDir() { return appwire.DirsCreateResponse{}, appwire.Conflict("a file already exists at that path") }
      return appwire.DirsCreateResponse{Path: path, Created: false}, nil
  }
  if err := mkdirAll(path, 0o755); err != nil {
      return appwire.DirsCreateResponse{}, appwire.InternalError(err.Error())
  }
  return appwire.DirsCreateResponse{Path: path, Created: true}, nil
  ```

  Keep `envvars.Home.Getenv()`, `filepath.Clean`, the `WebConfig.MkdirAll`
  seam, and the current treatment of non-nil stat errors unchanged.

- [ ] **Step 3: Register the typed handler and remove the HTTP route.**

  Add:

  ```go
  appserver.HandleTyped(server.Router(), appwire.MethodEvenerDirsCreate,
      func(_ context.Context, params appwire.DirsCreateParams) (appwire.DirsCreateResponse, error) {
          return hubDirsCreate(cfg, params)
      })
  ```

  Delete the HTTP registration and handler, then delete only their route
  probes/fixtures. Retain the shared `MkdirAll` config seam for AppWire tests.

- [ ] **Step 4: Update the exact hub handler-set test.**

  Add `appwire.MethodEvenerDirsCreate` to the named expected set. Its empty
  params dispatch may return invalid params, but it must not return
  method-not-found.

- [ ] **Step 5: Run the focused Go tests.**

  ```bash
  go test ./appwire ./cmd/evener-hub -run 'TestClientRequestWrappersRoundTrip|TestHubRPCDirsCreate|TestHubRPCRegistersExpectedHandlerSet' -count=1
  ```

  Expected: PASS, including the real filesystem and injected failure cases.

- [ ] **Step 6: Commit the server implementation and route removal.**

  ```bash
  git add appwire cmd/evener-hub
  git commit -m "feat: serve directory creation over AppWire"
  ```

### Task 4: Migrate Spawn preflight with TDD

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/preflight.ts` — request the typed method.
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/preflight.test.ts` — replace fetch tests with FakeClient AppWire tests.
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx` — pass `client` to `createDir`.
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx` — script and assert the AppWire call.

**Interfaces:**
- `createDir(client: AppwireClientLike, path: string): Promise<void>` awaits `client.request("evener/dirs/create", {path})`.
- `handleCreateConfirm` calls `createDir(client, path)` and keeps the existing catch/finally behavior.

- [ ] **Step 1: Write the failing frontend request assertion.**

  Replace the REST assertion with:

  ```ts
  const fake = new FakeClient("ready");
  fake.on("evener/dirs/create", () => ({ path: "/tmp/new", created: true }));
  await createDir(fake, "/tmp/new");
  expect(fake.calls[0]).toEqual({ method: "evener/dirs/create", params: { path: "/tmp/new" } });
  ```

  Keep a rejection test using a `WireError` with the conflict message so the
  caller's user-visible error path remains covered.

- [ ] **Step 2: Run the frontend test and verify the expected red state.**

  ```bash
  cd cmd/evener-hub/frontend
  npx vitest run src/panes/spawn/preflight.test.ts
  ```

  Expected: failure because `createDir` still has the old signature and REST
  implementation.

- [ ] **Step 3: Implement the smallest typed client change.**

  ```ts
  export async function createDir(client: AppwireClientLike, path: string): Promise<void> {
    await client.request("evener/dirs/create", { path });
  }
  ```

  Pass the already-injected `client` from `Spawn` at the one confirmation call
  site. Do not add a second client or fetch fallback.

- [ ] **Step 4: Update the integrated Spawn scenario.**

  Script `evener/dirs/create` in the test's ready FakeClient and assert the
  recorded method/params after clicking `Create & start`; retain the existing
  assertion that `thread/start` occurs and the button re-enables.

- [ ] **Step 5: Run focused frontend tests and formatting.**

  ```bash
  cd cmd/evener-hub/frontend
  npx vitest run src/panes/spawn/preflight.test.ts src/panes/spawn/Spawn.test.tsx
  npx biome check --write src/panes/spawn/preflight.ts src/panes/spawn/preflight.test.ts src/panes/spawn/Spawn.tsx src/panes/spawn/Spawn.test.tsx
  ```

  Expected: PASS with no REST call assertion remaining in these production
  caller tests.

- [ ] **Step 6: Commit the frontend migration.**

  ```bash
  git add cmd/evener-hub/frontend/src/panes/spawn
  git commit -m "feat: use AppWire for Spawn directory creation"
  ```

### Task 5: Regenerate catalogs and update active documentation

**Files:**
- Modify: `cmd/evener-hub/frontend/src/protocol/types.gen.ts` — generated output.
- Modify: `docs/appwire-protocol.md` — generated output.
- Modify: `docs/web-ui/parity/parity-m6-surfaces.md` — active Spawn contract.

- [ ] **Step 1: Run generation.**

  ```bash
  make generate
  ```

- [ ] **Step 2: Update the active parity contract.**

  Change the acceptance step from POSTing `/api/dirs/create` to requesting
  `evener/dirs/create` over the authenticated AppWire connection. Keep the
  existing non-OK/error-message behavior expressed in AppWire terms.

- [ ] **Step 3: Verify generated output and commit.**

  ```bash
  make generate
  git diff --check
  git add cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md docs/web-ui/parity/parity-m6-surfaces.md
  git commit -m "docs: publish directory creation AppWire contract"
  ```

### Task 6: Run the post-implementation simplify review

**Files:**
- Review: the complete branch diff from `git diff @{upstream}...HEAD` plus any working diff.

- [ ] **Step 1: Dispatch the four read-only simplify reviewers in parallel.**

  Use the same simplify-code skill and the four Reuse, Simplification,
  Efficiency, and Altitude angles. Reviewers must not edit.

- [ ] **Step 2: Apply only quality-preserving findings.**

  Do not alter the wire contract, route removal scope, tests, or user-visible
  error behavior. Record skipped findings and why.

- [ ] **Step 3: Run focused tests after review cleanup.**

  ```bash
  go test ./appwire ./cmd/evener-hub -run 'TestClientRequestWrappersRoundTrip|TestHubRPCDirsCreate|TestHubRPCRegistersExpectedHandlerSet' -count=1
  cd cmd/evener-hub/frontend
  npx vitest run src/panes/spawn/preflight.test.ts src/panes/spawn/Spawn.test.tsx
  ```

### Task 7: Verify the complete microproject and publish the PR

**Files:**
- Verify: all branch files; no additional source changes expected.

- [ ] **Step 1: Audit route residue.**

  ```bash
  rg -n '/api/dirs/create|handleAPIDirCreate' --glob '!docs/superpowers/**' --glob '!docs/web-ui/parity/contracts-sidebar-search-settings.md' .
  ```

  Expected: no current source, test, active-doc, or generated reference; any
  remaining archival record must be explicitly identified and left untouched.

- [ ] **Step 2: Run the repository gates.**

  ```bash
  make lint
  make vet
  make test
  make test-web
  make test-web-browser
  ```

  Run `make help` first if the target descriptions have not been checked in
  this worktree. All commands must exit zero; record any host-specific browser
  limitation rather than claiming it passed.

- [ ] **Step 3: Inspect the final branch.**

  ```bash
  git status --short --branch
  git diff @{upstream}...HEAD --stat
  git diff --check @{upstream}...HEAD
  ```

  Confirm the branch is clean, contains only this microproject, and is based
  on current `origin/main`.

- [ ] **Step 4: Publish but do not merge.**

  Push `codex/appwire-dirs-create` and open a PR against `main` with a body
  that names the contract, route removal, tests, generated artifacts, and any
  unresolved risk. Return the exact branch, commit list, PR URL, and command
  output to Jesse.
