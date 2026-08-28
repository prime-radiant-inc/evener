# Stale `/api/tree` REST Residue Cleanup Plan

**Goal:** Remove executable and current-contract residue for the retired
`/api/tree` endpoint without deleting the shared tree projection used by
AppWire navigation.

**Spec:** `docs/superpowers/specs/2026-08-27-tree-rest-residue-burndown-design.md`

## Constraints

- Work only from a refreshed `origin/main`.
- Preserve navigation behavior and all meaningful tree/projection coverage.
- Leave append-only fuzz route indices and historical records unchanged.
- Do not add absence tests.
- Use the required pre-implementation and post-implementation
  `simplify-code` reviews.

## Task 1: Remove executable/obsolete residue

**Files:**

- Modify `scripts/coverage/e2e-cover.sh`.
- [x] Remove the `/api/tree` HTTP sweep entry. The refreshed `origin/main`
already has no obsolete skipped tree-read test, so this branch does not delete
one or claim that another test covers its local-input behavior. Keep all active
tree and navigation tests.

## Task 2: Correct active route language

**Files:**

- Modify only the stale `/api/tree` comment sites in
  `cmd/evener-hub/main.go`, `web.go`, `web_api_tree.go`,
  `web_api_project_delete.go`, `web_types.go`, and
  `internal/hubcore/tree.go`, plus the related current test comment in
  `internal/hubcore/roster_test.go`.
- Modify only related current-test comments in
  `web_api_session_delete_test.go`, `web_api_project_delete_test.go`,
  `web_api_pin_section_test.go`, `web_api_tree_deleted_workdir_test.go`, and
  `web_test.go` that describe follow-up navigation reads or projected refs.
- Update current rail comments in `frontend/src/shell/rail/RailRow.tsx` and
  `RailRow.test.tsx` that still name the retired tree handler.
- Do not edit the append-only route registry or numeric fuzz seed.

- [x] Describe the current navigation snapshot/AppWire read and shared projection
semantics. Do not change identifiers, behavior, or historical documentation.

## Task 3: Correct current parity documentation

**Files:**

- Modify the current route inventory in `docs/evener-hub-web-routing.md`.
- Modify `docs/web-ui/parity/parity-m6-surfaces.md`.
- Modify the live scenario cards that still use the retired route:
  `test/scenarios/status-vocabulary-roundtrip.md`,
  `test/scenarios/spawn-keyboard-contract.md`,
  `test/scenarios/spawn-empty-prompt-starts-dormant.md`,
  `test/scenarios/sidebar-favorite-pinned-across-reload.md`,
  `test/scenarios/ask-web-answer.md`, and `test/scenarios/INDEX.md`.

- [x] Replace current `/api/tree` claims with the existing AppWire navigation-read
  contract, distinguishing the manifest's descriptors/attention summary from
  the bounded row resources. Preserve the user-visible and notification
  behavior each card documents. Leave historical parity/design records alone.

## Task 4: Audit and verify

- [x] Run a scoped residue audit that allowlists
`internal/fuzzroutes/fuzzroutes.go` and `web_fuzz_test.go`'s append-only
retired slot while distinguishing historical records. Read the testing guide
before test changes. Run `gofmt` if needed, `git diff --check`,
`go test ./cmd/evener-hub`, `make lint`, `make vet`, and `make test`. Run the
required post-implementation `simplify-code` review and apply only
quality-preserving fixes. Rebase, whole-branch review, and publication follow
the standard finishing workflow.

## Completion criteria

- No active `/api/tree` executable probe or current-contract claim remains
  outside the explicitly retained append-only fuzz registry/seed.
- Shared tree/navigation implementation and meaningful behavior tests remain.
- No behavior or AppWire wire contract changes.
- Simplify passes, focused tests, repository gates, rebase verification, and
  whole-branch review are all clean.
