# Upgrade REST Burndown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the unused `/api/upgrade` application JSON API and its
route-only test/fuzz surface while preserving the existing `evener/upgrade`
AppWire method and self-update behavior.

**Architecture:** This is a delete-and-document-correction migration. The
existing typed AppWire method is the only application upgrade request surface;
no new method, compatibility route, or HTTP fallback is introduced.

**Tech Stack:** Go, AppWire, route/fuzz coverage, Markdown behavior contracts.

**Spec:** [2026-08-27-upgrade-rest-burndown-design.md](../specs/2026-08-27-upgrade-rest-burndown-design.md)

## Global Constraints

- Preserve `hubUpgrade`, `runHubSelfUpgrade`, and the `evener/upgrade` wire
  contract.
- Remove only route-specific tests and fuzz/coverage probes; retain AppWire
  upgrade tests.
- Do not edit historical superpowers specs/plans.
- Do not add tests whose only purpose is to assert that the old route is gone.
- Before changing tests, follow `docs/developing-evener/testing.md`.
- Run the required pre- and post-implementation four-reviewer
  `simplify-code` passes.
- Use explicit paths with `git add` after checking `git status`; never bypass
  hooks.

---

## Task 1: Remove the HTTP route and handler seam

**Files:**

- Modify `cmd/evener-hub/web.go`.
- Modify `cmd/evener-hub/web_api.go`.

**Steps:**

- [ ] Remove the `/api/upgrade` mux registration.
- [ ] Remove `handleAPIUpgrade` and the route-only `webHubUpgrade` variable.
- [ ] Leave `hubUpgrade` and `runHubSelfUpgrade` in `app_upgrade.go` unchanged.
- [ ] Run `gofmt -w cmd/evener-hub/web.go cmd/evener-hub/web_api.go`.
- [ ] Run `go test ./cmd/evener-hub` after Task 2 removes its route tests.

## Task 2: Remove route-only tests and fuzz/coverage probes

**Files:**

- Modify `cmd/evener-hub/web_test.go`.
- Modify `cmd/evener-hub/cov_web_core_api_test.go`.
- Modify `cmd/evener-hub/cov_core_api_pass4_fuzz_test.go`.
- Modify `cmd/evener-hub/cov_small_tails_pass6_fuzz_test.go`.
- Modify `cmd/evener-hub/web_mutating_fuzz_test.go`.
- Modify `cmd/evener-hub/web_fuzz_test.go`.

**Steps:**

- [ ] Remove the focused HTTP self-update route test and its now-unused
      `selfupdate` import, without touching AppWire self-update tests.
- [ ] Remove route-table, direct-handler, small-tail, and mutating-fuzz
      probes for `/api/upgrade`.
- [ ] Remove the mutating-fuzz-only `stubSelfUpgrade(f)` setup; keep the same
      seam for AppWire upgrade fuzz coverage.
- [ ] Update fuzz comments so they describe the remaining route families.
- [ ] Keep the AppWire upgrade coverage and `runHubSelfUpgrade` seam tests.
- [ ] Run `gofmt -w` on all changed Go files.
- [ ] Run `go test ./cmd/evener-hub`.

## Task 3: Correct the current behavior contract

**Files:**

- Modify `docs/web-ui/parity/contracts-sidebar-search-settings.md`.

**Steps:**

- [ ] Replace the sentence describing a REST upgrade fallback with the actual
      AppWire upgrade behavior.
- [ ] Do not rewrite historical superpowers documents.

## Task 4: Re-audit, simplify, gate, and publish

**Files:** None beyond Tasks 1–3.

**Steps:**

- [ ] Run `rg -n -S '/api/upgrade|handleAPIUpgrade|webHubUpgrade'` and classify
      only historical design references as remaining results.
- [ ] Run `go test ./cmd/evener-hub`.
- [ ] Run `make lint`.
- [ ] Run `make vet`.
- [ ] Run `make test`.
- [ ] Run `git diff --check` and inspect the complete diff.
- [ ] Run the required post-implementation `simplify-code` review and apply
      only accepted quality-preserving suggestions.
- [ ] Repeat focused tests and applicable gates after any review fix.
- [ ] Fetch `origin/main`, rebase if it moved, push the branch, and open its
      focused pull request.
