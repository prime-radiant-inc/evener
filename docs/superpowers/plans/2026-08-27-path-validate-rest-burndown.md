# Path Validate REST Burndown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the duplicate `/api/path/validate` application JSON route
and its route-only probes while preserving the existing
`evener/path/validate` AppWire method and validation behavior.

**Architecture:** This is a delete-and-document-correction migration. The
typed AppWire method is the only application path-validation request surface;
no new method, compatibility route, or HTTP fallback is introduced.

**Tech Stack:** Go, AppWire, route/fuzz coverage, shell coverage harness,
Markdown behavior contracts.

**Spec:** [2026-08-27-path-validate-rest-burndown-design.md](../specs/2026-08-27-path-validate-rest-burndown-design.md)

## Global Constraints

- Preserve `evener/path/validate`, `fspaths.ValidateLaunchPath`, and the
  existing AppWire handler/tests.
- Remove only the duplicate HTTP route and route-specific probes.
- Keep `/api/dirs/create` unchanged; it is a separate application API with no
  AppWire equivalent in this slice.
- Do not edit historical superpowers specs/plans.
- Do not add tests whose only purpose is to assert that the old route is gone.
- Before changing tests, follow `docs/developing-evener/testing.md`.
- Run the required pre- and post-implementation four-reviewer
  `simplify-code` passes.
- Use explicit paths with `git add` after checking `git status`; never bypass
  hooks.

---

## Task 1: Remove the HTTP route and handler

**Files:**

- Modify `cmd/evener-hub/web.go`.
- Modify `cmd/evener-hub/web_api.go`.

**Steps:**

- [ ] Remove the `/api/path/validate` mux registration.
- [ ] Remove `handleAPIPathValidate` and its now-unused HTTP-only import.
- [ ] Leave the AppWire registration in `cmd/evener-hub/app_rpc.go` and
      `fspaths.ValidateLaunchPath` unchanged.
- [ ] Run `gofmt -w cmd/evener-hub/web.go cmd/evener-hub/web_api.go`.

## Task 2: Remove route-only coverage and harness probes

**Files:**

- Modify `cmd/evener-hub/cov_web_core_api_test.go`.
- Modify `cmd/evener-hub/cov_core_api_pass4_fuzz_test.go`.
- Modify `scripts/coverage/e2e-cover.sh`.

**Steps:**

- [ ] Remove the route-table and direct-handler probes for
      `/api/path/validate`.
- [ ] Remove the coverage-harness curl for the deleted route and keep the
      remaining shutdown comment concise.
- [ ] Keep AppWire path-validation tests, fuzz coverage, and the unrelated
      `/api/dirs/create` coverage.
- [ ] Run `gofmt -w` on changed Go files and `git diff --check`.
- [ ] Run `go test ./cmd/evener-hub`.

## Task 3: Correct current parity contracts

**Files:**

- Modify `docs/web-ui/parity/parity-m6-surfaces.md`.
- Modify `docs/web-ui/parity/parity-m7-settings.md`.

**Steps:**

- [ ] Replace the spawn preflight sentence that claims validation uses an HTTP
      GET with the existing AppWire request behavior.
- [ ] Remove the `launchconfig.validatePath` raw-fetch fallback claim and state
      that it uses `evener/path/validate` through AppWire.
- [ ] Do not rewrite historical superpowers documents.

## Task 4: Re-audit, simplify, gate, and publish

**Files:** None beyond Tasks 1–3.

**Steps:**

- [ ] Run `rg -n -S '/api/path/validate|handleAPIPathValidate'` and classify
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
