# Spawn-Schema REST Burndown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the unused `/api/spawn-schema` application JSON API and its
route-only Go contract while preserving the existing AppWire launch schema.

**Architecture:** This is a delete-only migration. The hub will expose launch
schema through the existing typed `evener/launch/schema` AppWire method. No
new method, wire type, compatibility route, or absence test is introduced.

**Tech Stack:** Go, AppWire, shell coverage harness, Markdown API research
documentation.

**Spec:** [2026-08-27-spawn-schema-rest-burndown-design.md](../specs/2026-08-27-spawn-schema-rest-burndown-design.md)

## Global Constraints

- Preserve unrelated `/api/spawn` behavior and the shared `launchHarnessIDs`
  helper.
- Do not edit historical superpowers specs/plans that document prior designs.
- Do not add tests whose only purpose is to assert that the old route is gone.
- Before changing tests, follow `docs/developing-evener/testing.md`.
- Run the required pre- and post-implementation four-reviewer
  `simplify-code` passes; reviewers are read-only and may suggest only
  behavior-preserving quality changes.
- Use explicit paths with `git add` after checking `git status`; never bypass
  hooks.

---

## Task 1: Remove the HTTP route and route-only server contract

**Files:**

- Modify `cmd/evener-hub/web.go`.
- Modify `cmd/evener-hub/web_api.go`.
- Modify `hubapi/types.go`.

**Steps:**

- [ ] Remove the `/api/spawn-schema` mux registration.
- [ ] Remove `handleAPISpawnSchema` and its response-only `SpawnSchema` and
      `SpawnField` types.
- [ ] Remove `HealthCapabilities.SpawnSchema`, since it advertised only the
      deleted route.
- [ ] Run `gofmt -w cmd/evener-hub/web.go cmd/evener-hub/web_api.go hubapi/types.go`.
- [ ] Run `go test ./cmd/evener-hub ./hubapi`.

## Task 2: Remove the unused HTTP client method and its focused tests

**Files:**

- Modify `hubapi/client.go`.
- Modify `hubapi/client_test.go`.
- Modify `hubapi/refs_fuzz_test.go`.

**Steps:**

- [ ] Delete `Client.SpawnSchema` without changing shared HTTP helpers or
      the still-live spawn client.
- [ ] Delete the focused success/error HTTP-client tests for that method.
- [ ] Remove the fuzz coverage call for the deleted method while retaining
      coverage for the remaining client contracts.
- [ ] Run `gofmt -w hubapi/client.go hubapi/client_test.go hubapi/refs_fuzz_test.go`.
- [ ] Run `go test ./hubapi`.

## Task 3: Retire route-only coverage probes and update current research docs

**Files:**

- Modify `cmd/evener-hub/web_test.go`.
- Modify `cmd/evener-hub/cov_web_core_api_test.go`.
- Modify `cmd/evener-hub/cov_core_api_pass4_fuzz_test.go`.
- Modify `scripts/coverage/e2e-cover.sh`.
- Modify `docs/research/api-fuzzing-toolkit.md`.

**Steps:**

- [ ] Remove the focused `TestWeb_APISpawnSchema` route test.
- [ ] Remove route-table and direct-handler coverage calls that target the
      deleted path; keep neighboring retained-API coverage intact.
- [ ] Remove `/api/spawn-schema` from the real-binary HTTP coverage battery.
- [ ] Update the current API-surface table to point at the existing AppWire
      launch-schema catalog rather than the deleted HTTP route.
- [ ] Run `gofmt -w cmd/evener-hub/web_test.go cmd/evener-hub/cov_web_core_api_test.go cmd/evener-hub/cov_core_api_pass4_fuzz_test.go`.
- [ ] Run `go test ./cmd/evener-hub`.

## Task 4: Re-audit, run gates, and commit

**Files:** None beyond Tasks 1–3.

**Steps:**

- [ ] Run `rg -n -S '/api/spawn-schema|\\bSpawnSchema\\b|\\bSpawnField\\b'` and
      classify only historical design references as remaining results.
- [ ] Run `go test ./hubapi ./cmd/evener-hub`.
- [ ] Run `make lint`.
- [ ] Run `make vet`.
- [ ] Run `make test`.
- [ ] Run `git diff --check` and inspect the complete diff for accidental
      changes.
- [ ] Run the required post-implementation `simplify-code` review and apply
      only accepted quality-preserving suggestions.
- [ ] Repeat the focused tests and applicable gates after any review fix.
- [ ] Commit with a detailed message, fetch `origin/main`, rebase if the base
      moved, and push the microproject branch.
