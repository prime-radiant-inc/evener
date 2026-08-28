# Remove the Legacy Spawn REST Surface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans) to implement this plan.

**Goal:** Delete the unused `POST /api/spawn` REST surface while preserving the
typed AppWire `thread/start` launch path used by the SPA, TUI, and hub.

**Architecture:** The hub's AppWire router is the canonical launch boundary.
`thread/start` validates typed input, invokes the shared `hubThreadStart`
launch path, and returns the session reference. The REST route is an obsolete
adapter around the same launcher. Removing that adapter must not remove shared
`Spawner`, launch configuration, input-item validation, or session-send code.

**Tech Stack:** Go, AppWire, React/TypeScript frontend, deterministic Go and
frontend test gates, GitHub pull request workflow.

**Spec:** [2026-08-27-spawn-rest-burndown-design.md](../specs/2026-08-27-spawn-rest-burndown-design.md)

## Global Constraints

- Work only in `.worktrees/appwire-spawn-rest-burndown`, branched from the
  explicitly fetched `origin/main`.
- Keep the change limited to the unused spawn REST route and code solely
  supporting it. Do not modify other endpoint migrations.
- Do not add an absence test for `/api/spawn`; delete obsolete route tests and
  retain meaningful AppWire behavior tests instead.
- Read `docs/developing-evener/testing.md` before changing tests. Keep default
  tests deterministic and do not introduce provider or network dependence.
- Treat append-only fuzz route lists, route-indexed seeds, and harvested corpus
  fixtures as compatibility data. Leave their data and indices unchanged;
  comments may be clarified where they no longer describe a live handler.
- Use `apply_patch` for source edits, run formatters only on touched files, and
  commit coherent steps frequently. Never merge the resulting PR.

## Task 1: Record the plan and perform the pre-implementation quality review

**Files:**

- `docs/superpowers/specs/2026-08-27-spawn-rest-burndown-design.md`
- `docs/superpowers/plans/2026-08-27-spawn-rest-burndown.md`

**Steps:**

1. Confirm the caller inventory: SPA `startThread`, TUI `ThreadStart`, hub
   AppWire registration/tests, REST route registration, and REST-only tests.
2. Review the spec/plan with four independent read-only angles: reuse and
   duplication, simplest safe deletion, efficiency/scope, and readability/
   abstraction altitude. Correct only plan defects before touching code.
3. Inspect the working tree and commit the spec/plan as the first tracked
   change with a detailed intent-focused commit message.

## Task 2: Remove the route and its REST-only public contracts

**Files:**

- `cmd/evener-hub/web.go`
- `cmd/evener-hub/web_spawn.go`
- `cmd/evener-hub/web_types.go`
- `cmd/evener-hub/web_api.go`
- `hubapi/client.go`
- `hubapi/types.go`
- `hubapi/client_test.go`
- `hubapi/types_test.go`
- `hubapi/refs_fuzz_test.go`

**Steps:**

1. Remove the `/api/spawn` mux registration and delete
   `web_spawn.go`, including its access-mode adapters, harness mapping,
   handler, and structured-error writer.
2. Delete `spawnRequest` from `web_types.go`.
3. Remove `Client.Spawn`, `SpawnRequest`, and `SpawnResponse`. Replace the
   `RefResponse` alias with an equivalent independent struct so the clear
   endpoint's response contract remains unchanged.
4. Remove the `HealthCapabilities.Spawn` field and its health computation and
   assertions. Preserve all other health capabilities and endpoints.
5. Delete REST spawn request/client/type tests and the fuzz call that only
   exists to exercise `Client.Spawn`. Do not remove shared `Spawner` or
   `hubcore.SpawnRequest` types used by AppWire.
6. Format the touched Go files and run the focused package tests to catch
   accidental references before broader cleanup.

## Task 3: Retain real AppWire coverage and remove route-only test machinery

**Files:**

- `cmd/evener-hub/web_test.go`
- `cmd/evener-hub/image_attachments_test.go`
- `cmd/evener-hub/web_covtest_test.go`
- `cmd/evener-hub/cov_provider_render_pass4_fuzz_test.go`
- `cmd/evener-hub/cov_web_views_spawn_fuzz_test.go`
- `cmd/evener-hub/cov_web_workspace_pass5_fuzz_test.go`
- `cmd/evener-hub/sandbox_selftest_test.go`
- `cmd/evener-hub/sandbox_test.go`
- `cmd/evener-hub/internal/hubedge/auth_token_test.go`

**Steps:**

1. Delete the `/api/spawn` handler tests, route-only fake spawner helpers, the
   route-only health capability test, and the route-only image forwarding test.
2. Remove only direct calls to deleted route helpers from coverage fuzz tests.
   Keep their target names and all unrelated coverage branches stable.
3. Change the sandbox containment self-test to JSON-dispatch typed
   `appwire.MethodThreadStart` through `s.Web.appRPC.Router()`, then retain its
   recording-spawner assertion. This verifies the real AppWire handler and
   meaningful launch containment rather than a deleted transport.
4. Point the auth query-token rejection test at retained `/api/health` while
   preserving the same authentication contract.
5. Update the shared sandbox comment from “spawn” to `thread/start`; preserve
   the shared recording spawner and launch/input tests.
6. Leave `cmd/evener-fuzz-harvest/harvest_test.go` and the append-only route and
   seed data in `web_mutating_fuzz_test.go` unchanged except for explanatory
   comments that would otherwise claim the retired handler still exists.

## Task 4: Update current guidance without rewriting historical records

**Files:**

- `cmd/evener-hub/frontend/src/panes/spawn/startThread.ts`
- `cmd/evener-hub/frontend/src/panes/spawn/accessMode.ts`
- `docs/developing-evener/agentic-testing.md`
- `docs/developing-evener/conventions/naming.md`
- `docs/web-ui/parity/parity-m6-surfaces.md`
- `scripts/e2e/e2e-webui-turn-controls.sh`
- `test/scenarios/INDEX.md`
- `test/scenarios/spawn-empty-prompt-starts-dormant.md`
- `test/scenarios/spawn-failure-ux-post-ws5.md`

**Steps:**

1. Remove stale frontend comments that name the deleted REST shim while
   leaving the `thread/start` implementation and behavior unchanged.
2. Replace the current runbook's REST spawn recipe with its existing AppWire
   `thread/start` guidance and update the current naming/parity references.
3. Change the manual e2e recipe's launch instruction to use the web UI/TUI
   AppWire path rather than printing a deleted curl command.
4. Update the dormant-session scenario's browser-free setup to send
   `thread/start` while retaining its meaningful UI and navigation assertions.
   Delete the separate scenario card and index entry whose sole purpose is
   testing the retired REST spawn failure route. Leave other scenario cards
   that use launch as setup for another behavior and historical docs untouched.
5. Run Biome on the two touched frontend source files and inspect the diff for
   unintended behavior changes.

## Task 5: Run the post-implementation simplify review and verification

**Steps:**

1. Review the complete branch diff with four independent read-only
   simplify-code angles: reuse, simplification, efficiency, and readability/
   altitude. Apply only quality-preserving changes; do not weaken tests,
   change AppWire behavior, or alter compatibility data.
2. Run focused tests for `./hubapi` and `./cmd/evener-hub`, including the
   AppWire and sandbox tests affected by the deletion.
3. Run `make lint`, `make vet`, `make test`, `make test-web`, and
   `make test-web-browser` when the host supports the browser gate. Run the
   canonical `make merge-approval-gate` if the repository's current gate
   configuration requires it, recording any independent infrastructure
   limitation rather than masking it.
4. Inspect `git diff --check`, status, and the final commit history. Fetch
   `origin/main` again immediately before publishing; rebase the worktree if
   main advanced, rerun relevant verification, and verify the PR head matches
   the tested commit.
5. Publish a non-draft PR using authenticated GitHub tooling, do not merge it,
   and report the branch, final commit, PR URL, changed files, and exact
   verification results.
