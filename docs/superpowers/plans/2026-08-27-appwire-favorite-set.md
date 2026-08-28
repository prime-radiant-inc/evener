# AppWire project favorite mutation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the used web UI project-favorite mutation from `POST /api/favorite` to a typed hub AppWire method while preserving validation, persistence, navigation invalidation, and mutation notification.

**Architecture:** Add `evener/favorite/set` to the typed AppWire catalog. Define the favorite params/result beside the other protocol types and make the shared navigation mutation AppWire-owned, with a `hubapi` alias for existing Go callers. Register a focused hub handler that uses the existing favorite store and navigation service. Make the rail action use the AppWire client, then remove only the retired favorite REST route and its transport-specific residue.

**Tech Stack:** Go, AppWire typed router/client, generated TypeScript protocol types, React/TypeScript frontend, Vitest, repository `make` gates.

**Spec:** `docs/superpowers/specs/2026-08-27-appwire-favorite-set-design.md`

## Global Constraints

- Work only in the isolated worktree on `codex/appwire-favorite`.
- Fetch `origin main` before implementation and again immediately before publishing.
- Keep the change to the used project favorite mutation; do not convert or edit unrelated API families.
- Read `docs/developing-evener/testing.md` before test edits (completed before this plan).
- Use failing tests before production implementation, then run the required four-angle simplify review before and after implementation.
- Do not add tests asserting that the retired REST route is absent.
- Commit frequently with detailed intent; never bypass hooks.
- Publish a non-draft PR if authenticated; do not merge it.

---

## Task 1: Record the contract and baseline

- [ ] Confirm the worktree is clean and based on the latest fetched `origin/main`.
- [ ] Keep the design/spec and this plan committed before code changes.
- [ ] Run `git diff --check` and inspect the documentation diff.

## Task 2: Run the required pre-implementation simplify review

- [ ] Review the committed diff at `git diff @{upstream}...HEAD` from four angles: reuse, simplification, efficiency, and altitude.
- [ ] Apply only quality-preserving documentation/plan corrections.
- [ ] Re-run `git diff --check` and commit any review corrections separately.

## Task 3: Add failing AppWire and frontend contract tests

- [ ] Add focused hub tests in `cmd/evener-hub/app_favorite_test.go` that dispatch `evener/favorite/set`, reject the obsolete session shape, persist a project favorite, and verify the returned navigation mutation and mutation notification.
- [ ] Update the meaningful rail action test to provide a fake AppWire client and assert the method and typed params instead of mocking `fetch` for favorites.
- [ ] Run the focused Go and frontend tests and record the expected failure caused by the not-yet-defined AppWire method/types/handler.

## Task 4: Implement the typed AppWire contract and handler

- [ ] Add `MethodEvenerFavoriteSet`, `FavoriteSetParams`, and `FavoriteSetResponse` to `appwire` and its method catalog.
- [ ] Move the shared `NavigationMutation` definition into `appwire`; retain `hubapi.NavigationMutation` as an alias and update the TypeScript generator root.
- [ ] Add the typed client convenience method if it matches existing client conventions, with a focused client test if added.
- [ ] Register a focused favorite handler in `cmd/evener-hub/app_rpc.go` (or a dedicated neighboring file) and preserve store, navigation refresh, and notification ordering.
- [ ] Run generation so protocol documentation and frontend method types are derived from the catalog.
- [ ] Run focused Go tests and commit the green contract/handler implementation.

## Task 5: Migrate the rail and remove the superseded REST transport

- [ ] Pass the AppWire client into the rail favorite action and keep the existing navigation convergence call unchanged.
- [ ] Update rail tests and current scenario/docs to describe the AppWire mutation.
- [ ] Remove `/api/favorite` registration, the REST handler, and only tests/helpers/residue that exist for that route; preserve helpers still needed by session pin behavior.
- [ ] Adjust the app RPC method-set and coverage tests to expect/cover the AppWire method rather than the REST route.
- [ ] Run frontend formatting on touched `src/` files and commit this migration separately.

## Task 6: Run the required post-implementation simplify review

- [ ] Re-run the four-angle review over the complete branch diff.
- [ ] Apply only simplifications that preserve behavior, scope, and generated-output correctness.
- [ ] Re-run focused tests after every simplification and commit review corrections.

## Task 7: Verify, rebase, and publish

- [ ] Run focused Go tests, focused frontend tests, `make test-web`, `make lint`, `make vet`, and `make test` as supported by the repository environment; run browser coverage when available.
- [ ] Run `make merge-approval-gate` or document the exact environment limitation if a component is unavailable.
- [ ] Inspect status, diff/stat, generated-output freshness, and commit history.
- [ ] Fetch `origin main` again, rebase the branch onto the current `origin/main`, resolve and retest any conflicts, and verify the final diff.
- [ ] Publish the branch and create a non-draft PR without merging it.
- [ ] Report the branch, final commit(s), PR URL, files changed, and verification evidence, including remote CI state if available.

## Self-review checklist

- [ ] No TODO/TBD placeholders remain in the spec, plan, or implementation.
- [ ] Only the project favorite mutation changed.
- [ ] Session favorite rejection remains explicit.
- [ ] No test asserts absence of the old REST route.
- [ ] Navigation invalidation and mutation notification still occur after a successful set.
- [ ] Frontend code uses the typed AppWire client and generated protocol types.
