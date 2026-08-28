# Scoped AppWire Navigation Reads Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hub browser's `/api/navigation` read family with the
scoped `evener/navigation/read` AppWire method, preserve all current
navigation behavior, and remove the HTTP navigation adapter in one reviewable
PR-sized microproject.

**Architecture:** Keep `NavigationService` and its pure projection/cache layer
as the authority. Add one hub-only AppWire method with a resource discriminator,
bounded request fields, a revision/generation/ETag response envelope, and raw
JSON data selected by the resource. Migrate the browser store's request seam
and test doubles to AppWire. Leave broad `thread/list` and the TUI unchanged in
this slice; they are separate consumers with a separate migration decision.

**Tech Stack:** Go, AppWire JSON-RPC over WebSocket, generated TypeScript
protocol types, Zustand, Vitest, Go tests, Biome, golangci-lint.

**Spec:** `docs/superpowers/specs/2026-08-27-scoped-appwire-navigation-reads-design.md`

## Global Constraints

- Read `docs/developing-evener/testing.md` before adding or changing tests.
- Treat the spec as the protocol source of truth. Preserve its navigation
  projection fields, limits, ordering, generation/revision/ETag semantics,
  invalidation sequence handling, coalescing, stale retry, and generation
  fencing.
- Do not add a compatibility HTTP shim after the browser moves. Remove the
  `/api/navigation` route and HTTP-only adapter in this PR.
- Do not change the broad `thread/list` protocol or migrate the TUI here.
- Keep the response data's existing JSON shape; only the transport and envelope
  change.
- Use `apply_patch` for source edits, inspect every delegated diff, and commit
  each coherent microtask. Never add unrelated working-tree files.
- Before any final completion claim, run fresh verification and retain failure
  output if a gate fails.

---

## Task 1: Establish the protocol types and generated catalog

**Files:** `appwire/types.go`, `appwire/protocol.go`, `appwire/client.go`,
`appwire/*_test.go`, generated AppWire docs/TypeScript artifacts.

- [x] Read the testing guide and inspect existing typed-method tests and code
  generation instructions.
- [x] Add `MethodEvenerNavigationRead`, `NavigationReadParams` (with optional
  pointer page fields), and `NavigationReadResponse` using the exact contract
  in the spec.
- [x] Register the method in `appwire.Methods` with `ScopeHub` and a precise
  summary; add the typed Go client helper if the client helper surface uses
  one helper per method.
- [x] Add protocol tests for JSON field names, enum/status values, raw data
  round trips, and catalog membership.
- [x] Regenerate the checked-in AppWire Markdown and TypeScript artifacts using
  the repository's documented generator, then inspect the diff for only the
  new method/types.
- [x] Run the focused `appwire` tests and `git diff --check`.

## Task 2: Add the hub AppWire navigation handler

**Files:** `cmd/evener-hub/app_rpc.go`, a focused handler/test file, and
navigation service tests as needed.

- [x] Add table-driven failing tests for every resource family and for invalid
  required/non-empty extraneous fields, explicit zero limits, paging overflow,
  and unpaged page fields before implementation.
- [x] Validate `NavigationReadParams` at the handler boundary and map valid
  requests to the existing internal resource key without changing projection
  code or service limits.
- [x] Call `NavigationService.Representation` with the request context. Return
  `ok` plus the exact representation JSON for a changed read, and
  `not_modified` without data for an exact ETag match.
- [x] Map missing resources and service/context failures to existing AppWire
  errors. Never expose HTTP statuses, gzip bytes, or route parsing in the
  handler.
- [x] Register the method in the hub router/catalog (including test-only hub
  servers without a navigation service); return explicit AppWire unavailable
  when the service is absent. Do not register it on the daemon.
- [x] Run focused `cmd/evener-hub` and `appserver` tests.

## Task 3: Replace the frontend navigation request seam

**Files:** `cmd/evener-hub/frontend/src/stores/navigation/store.ts`,
`revalidator.ts`, `types.ts`, protocol test doubles, and focused store tests.

- [x] Replace the HTTP request callback with an AppWire request using the
  generated method/params/result types.
- [x] Map each existing `ResourceKey` to the new structured params, sending
  decoded identity values and preserving the existing page defaults.
- [x] Normalize `ok`/`not_modified` into the revalidator contract while
  retaining ETag, generation, revision, body validation, late-response
  fencing, late-result suppression (not physical abort; the current browser
  AppWire client has no abort-signal option), and error behavior.
- [x] Script the existing generated-catalog fake with `FakeClient.on(...)`; do
  not add a URL-specific fake path.
- [x] Preserve the revalidator state-machine tests; add focused AppWire
  parameter/result coverage for boot fanout, paging, conditional reads,
  reconnect/generation reset, invalidation gaps, invalid payloads,
  cancellation, and late responses.
- [x] Run Biome on touched `src/` files and the focused frontend navigation
  tests/typecheck.

## Task 4: Migrate browser harnesses and consumers

**Files:** shellguard/dev entry, frontend tests/components that stub
navigation, and notification tests that trigger navigation refreshes.

- [x] In `src/dev/shellguard-entry.tsx`, replace the old navigation fetch stubs
  with a fake AppWire client method handler that returns the same bounded
  resources; cover the real AppWire frame separately in the hub WebSocket test.
- [x] Update the named component and notification tests to script AppWire
  responses and assert the actual interaction contract rather than HTTP URLs.
- [x] Confirm all production frontend references to `/api/navigation` are gone;
  retain only historical documentation if it is explicitly marked as a
  migration note, otherwise remove it.
- [x] Run focused component/notification checks after Biome formatting; leave
  the canonical `make test-web` gate to Task 6.

## Task 5: Remove the HTTP navigation adapter

**Files:** `cmd/evener-hub/web.go`, the navigation HTTP adapter and its
tests/benchmarks/metrics, and any route-only helper imports.

- [x] Remove `/api/navigation` registrations and only the navigation-specific
  raw-path guard branch; preserve shared auth/middleware/bootstrap behavior.
- [x] Delete HTTP representation/error/encoding adapter code and route-only
  tests/benchmarks; retain pure projection/service tests and shared types used
  by AppWire.
- [x] Update comments and route inventories so the deleted HTTP API cannot be
  mistaken for a supported compatibility path.
- [x] Run the hub package tests and search for remaining production references
  to the removed route.

## Task 6: Integrated verification and handoff

- [x] Run `make help` and use the documented targets for the repository gates.
- [x] Run focused AppWire, appserver, hub, and frontend tests first; then run
  `make lint`, `make vet`, `make test`, `make test-web`, and the browser gate
  when the host supports it.
- [x] Run generated-output freshness checks and inspect `git diff --stat`,
  `git diff --check`, and the complete diff for scope drift.
- [x] Run the required post-implementation `simplify-code` review with four
  independent read-only reviewers. The same pre-implementation review was
  completed after this spec and plan were written. Apply only
  quality-preserving fixes, rerun affected tests, and do not let simplification
  alter the protocol contract or reduce meaningful test coverage.
- [x] Commit the implementation with a detailed message, fetch/rebase if
  `origin/main` advanced, and push `codex/appwire-navigation-read`.
- [x] Report the branch, commit, verification evidence, and any remaining
  follow-up microprojects without merging the PR automatically.

## Follow-up microprojects (not part of this branch)

| Slice | Decision |
| --- | --- |
| Dead `/api/tree`, standalone `/api/fork`, superseded `/api/spawn-schema` | Remove stale clients, fixtures, capability fields, and route inventory first. |
| `/api/search` | Add a focused `evener/search` method, migrate callers, remove REST. |
| `/api/models` | Extend `model/list` to carry the remaining rich catalog/diagnostic data, migrate callers, remove REST. |
| Existing AppWire equivalents (`spawn`, path validation, upgrade, send, interrupt, compact, shutdown, clear, fork, model, reasoning effort, rename, tasks) | Each named API is its own logical conversion PR; delete the REST equivalent with its final caller. The live session-fork route is distinct from the already-dead standalone `/api/fork` surface. |
| New hub-only operations (git head, directory creation, rail mutations, deletion) | Add narrow AppWire methods, migrate one caller family per PR, then remove REST. |
| HTTP bootstrap/static/raw bytes | Retain until a separate auth/static/document transport design is explicitly approved. |
