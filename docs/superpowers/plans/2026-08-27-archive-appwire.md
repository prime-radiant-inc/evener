# Archive AppWire migration plan

## Goal

Replace the used web UI `POST /api/archive` mutation with the narrow typed
`evener/archive/set` AppWire method and remove the retired REST route.

## Architecture

The hub owns the mutation because archive decisions and navigation projection
are hub state. The handler is registered on the existing hub AppWire server
after the `WebServer` has its `NavigationService`, so it can preserve the
existing store, refresh, notification, and attention-poke sequence. The
frontend uses `connectionStore`'s typed client. The shared navigation mutation
receipt is defined in `appwire` and retained under the existing `hubapi` name
through a type alias; its existing `generation_id` response key is preserved
for compatibility with the other hub mutation receipts.

## Tech Stack

Go AppWire router/client, SQLite-backed `hubcore.ArchiveStore`, generated
TypeScript protocol types, React/Vitest rail tests, and the repository Make
gates.

## Spec

- Method: `evener/archive/set`, `ScopeHub`.
- Params: typed target kind, canonical `id`, optional camelCase `workingDir`,
  and `archived` boolean.
- Result: `ok` plus the committed typed `navigation` mutation receipt, whose
  existing `generation_id`/`targets` wire shape is preserved.
- Project validation and session/project refresh hints match the retired REST
  behavior exactly.
- The route implementation, route-specific tests, and route-only response
  residue are deleted. Navigation convergence tests are retained and moved to
  AppWire dispatch.
- No unrelated mutation or endpoint is changed.

## Global Constraints

- Work only in `/Users/jesse/git/prime-radiant/evener/.worktrees/appwire-archive`
  on `codex/appwire-archive`, based on the explicitly fetched current
  `origin/main`.
- Read the testing guide before editing tests; use deterministic real behavior
  below the AppWire transport seam.
- Use TDD: add a meaningful failing contract test before each implementation
  slice, observe the failure, then implement the smallest change.
- Run the simplify-code four-angle review before implementation and after
  implementation. Apply only quality-preserving findings; do not remove added
  API/test coverage.
- Run `make generate` for generated protocol output. Run Biome on touched
  frontend source files before frontend gates.
- Commit frequently with descriptive messages. Fetch `origin/main` again
  immediately before publishing; rebase and reverify if it advanced.
- Publish a non-draft PR if authenticated. Do not merge it.

## Tasks

### 1. Establish the isolated baseline

- Confirm `git status`, branch, worktree path, and exact `origin/main`.
- Record a clean baseline with the focused existing archive/navigation tests
  and the repository test target; do not modify source during this step.

### 2. Define the AppWire contract

- Add `MethodEvenerArchiveSet`, `ArchiveTargetKind` constants,
  `ArchiveParams`, `ArchiveResponse`, and the shared `NavigationMutation` to
  `appwire/types.go`.
- Keep the public `hubapi.NavigationMutation` identifier as an alias to the
  AppWire type, preserving its JSON `targets: []` zero-value behavior.
- Add the method to `appwire/protocol.go` and a typed `Client.ArchiveSet`
  wrapper in `appwire/client.go`.
- Add the client round-trip test first, run it to observe the missing-contract
  failure, then implement the contract and make it pass.
- Generate the protocol TypeScript and documentation after the Go contract is
  complete.
- Commit the contract slice.

### 3. Register and implement the hub handler

- Add a focused `cmd/evener-hub/app_archive.go` handler registration that
  closes over the `WebServer` navigation and notification seams.
- Reuse `ArchiveStore.Set`, `identifier.ResolveProject`, existing navigation
  hints, and `notifyMutation`; do not duplicate archive persistence or alter
  notification ordering.
- Add AppWire dispatch tests first for session/project persistence, project
  validation, and the typed receipt/error boundary; observe red before adding
  the handler.
- Register the handler from `NewWebServer` after the AppWire server and
  navigation service are constructed. Update the hub catalog test through
  normal registration, not an absence assertion.
- Commit the server slice.

### 4. Move the frontend caller and meaningful coverage

- Replace only `setArchived` in
  `cmd/evener-hub/frontend/src/shell/rail/actions.ts` with a typed
  `connectionStore` AppWire request. Leave favorite, pin, delete, rename, and
  other REST callers untouched.
- Convert archive cases in `actions.test.ts` to `FakeClient` tests that prove
  session omission of `workingDir`, project inclusion of `workingDir`, exact
  method name, result forwarding, and rejection propagation.
- Convert the two Rail archive integration tests to the same AppWire seam;
  retain overlay convergence and rollback behavior assertions.
- Run focused frontend tests, then `npx biome check --write` on touched
  `src/` files.
- Commit the frontend slice.

### 5. Remove REST residue and migrate convergence coverage

- Delete `cmd/evener-hub/web_api_archive.go` and remove only its
  `/api/archive` registration from `cmd/evener-hub/web.go`.
- Delete route-specific archive tests in
  `web_api_archive_test.go` and `web_api_archive_gaps_test.go`.
- Update `web_api_navigation_convergence_test.go` to dispatch
  `evener/archive/set` through the real AppWire router; rename test/helper
  references from REST to AppWire. Preserve all publication, no-op, target,
  revision, and response-convergence assertions.
- Search the final diff for `/api/archive`, `handleAPIArchive`, and
  `archiveMutationResponse`; any remaining match must be justified by a
  non-route historical/documentation reference, otherwise remove it. Do not
  add a test whose purpose is route absence.
- Commit the cleanup slice.

### 6. Simplify and verify

- Run the required four independent simplify-code angles over the complete
  branch diff. Apply only reuse, simplicity, efficiency, or altitude changes
  that preserve the typed contract and test meaning; commit any resulting
  quality edits separately.
- Run focused Go tests for `appwire`, `hubapi`, and `cmd/evener-hub` archive /
  navigation convergence coverage.
- Run `make generate` and confirm generated output is fresh.
- Run `make test-web`, `make lint`, `make vet`, and `make test`; run
  `make test-web-browser` when Chrome is available. If a gate fails, diagnose
  the root cause and make the smallest correction.
- Fetch `origin/main` explicitly. If it advanced, rebase this branch, resolve
  conflicts conservatively, rerun affected gates, and commit the rebase only
  when required by the workflow.
- Review the final diff and exact branch head, then publish the branch and
  create a non-draft PR without merging.

## Self-review

- All requested scope boundaries are explicit above.
- Every task names the intended file/interface and a concrete verification
  command or observable result.
- No placeholder tasks, backward-compatibility route, or absence test is
  planned.
- The AppWire wire keys use the repository's camelCase convention while the
  existing archive decision and navigation semantics remain unchanged.
