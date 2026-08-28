# Git HEAD AppWire Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the Spawn pane's git HEAD lookup from `/api/git/head` to a typed hub-scoped AppWire method while preserving its display-only, fail-soft behavior and removing only the legacy route surface.

**Architecture:** Add `evener/git/head` to the AppWire catalog with typed `GitHeadParams` and `GitHeadResponse`. Factor canonical directory validation, the injectable seam, and the `resolveGitHead` call into a hub handler helper. Register that helper on the hub router and call it from the generated frontend AppWire client. Remove the HTTP route and route-specific probes; keep the shared git helper and semantic tests.

**Tech Stack:** Go AppWire server/client, generated Go/TypeScript protocol artifacts, React/TypeScript Spawn pane, Vitest, Go tests, Make gates.

**Spec:** `docs/superpowers/specs/2026-08-27-appwire-git-head-design.md`

## Global Constraints

- Work in an isolated worktree created for this migration and publish the
  finished tree to the PR branch.
- Base the work on the explicitly fetched `origin/main` commit and do not
  include model-list, navigation, or tree-residue changes.
- Preserve empty-cwd skipping, stale-cwd fencing, missing/non-Git/error empty
  success, detached HEAD short-SHA/`HEAD` fallback, and the configured
  `ResolveGitHead` seam.
- Use `make generate`; never hand-edit generated protocol output.
- Remove the legacy route and route-specific residue without adding an absence
  test for the old route.
- Run the four-angle simplify-code review before implementation and after the
  implementation; apply only quality-preserving findings.

## Tasks

- [x] Add the typed protocol contract. In `appwire/types.go`, define
  `GitHeadParams{CWD string}` and `GitHeadResponse{Head string}`. In
  `appwire/types.go` and `appwire/protocol.go`, add the ordered
  `MethodEvenerGitHead = "evener/git/head"` hub-scoped catalog entry. In
  `appwire/client.go`, add `Client.GitHead(ctx, GitHeadParams)` using the
  standard request wrapper. Add round-trip and hub-scope coverage alongside
  the existing AppWire wrapper tests.
- [x] Generate protocol artifacts and update protocol-count comments affected
  by the catalog addition. Run `make generate`, inspect the diff, and keep
  only the generated documentation and TypeScript changes caused by this
  method.
- [x] Register the hub handler. Add a small resolver that trims `cwd`, skips
  empty input, canonicalizes the existing directory through `fspaths`, selects
  `WebConfig.ResolveGitHead` or `resolveGitHead`, and converts every lookup
  failure to `{head:""}`. Register it in
  `registerMiscHandlers` under `MethodEvenerGitHead`; test the configured seam
  through an AppWire round trip and cover empty, missing, and error cases.
- [x] Migrate the Spawn caller. Change `resolveHeadBranch` to accept the
  typed AppWire client and call `client.request("evener/git/head", {cwd})`,
  returning `data.head`.
  Keep the empty-cwd fast path, fail-soft handling, and the effect's active
  flag/dependency fencing. Update Spawn tests to script the AppWire method and
  retain meaningful HEAD, empty, error, and stale-response behavior.
- [x] Remove the REST surface and route-specific residue. Delete the HTTP
  registration and handler, remove `/api/git/head` coverage/sandbox/fuzz
  probes and REST-only comments, and update the active parity contract to
  describe AppWire. Retain the shared `resolveGitHead` helper and its detached
  HEAD semantic coverage; leave historical records untouched.
- [x] Run the post-implementation simplify-code review and apply only
  behavior-preserving quality findings. Re-run affected tests after any
  change, then run `make generate`, focused Go/frontend tests, `make test-web`,
  `make test-web-browser` when available, and the applicable repository gates.
  Commit each coherent stage with a detailed message and publish a PR against
  `main` without merging.

## Verification Commands

```sh
git fetch origin
git diff --check
make generate
go test ./appwire ./cmd/evener-hub -count=1
cd cmd/evener-hub/frontend && npx vitest run src/panes/spawn/branch.test.ts src/panes/spawn/Spawn.test.tsx
cd ../../..
make test-web
make test-web-browser
make merge-approval-gate
```
