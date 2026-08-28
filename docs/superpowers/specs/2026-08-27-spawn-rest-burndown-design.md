# Legacy Spawn REST Surface Burndown

## Goal

Remove the unused `POST /api/spawn` REST API in a single focused change while
keeping session launch behavior on the typed AppWire `thread/start` path.

## Caller and contract evidence

- The shipped SPA starts sessions in
  `cmd/evener-hub/frontend/src/panes/spawn/startThread.ts`, which builds
  `appwire.ThreadStartParams` and requests `thread/start`. There is no frontend
  request to `/api/spawn`.
- The TUI starts sessions through `Client.ThreadStart` in
  `cmd/evener-tui/hub_commands.go`; it has no REST spawn caller.
- The hub's AppWire router owns the real thread-start behavior through
  `hubThreadStart`, and the existing AppWire tests exercise launch input,
  images, access mode, model selection, and failure behavior.
- The remaining `hubapi.Client.Spawn` call is test/fuzz support for the retired
  REST contract, not a product caller. The route-specific server tests and
  route-only coverage likewise have no production consumer.

## Implementation scope

1. Remove the hub mux registration and the route-only implementation in
   `cmd/evener-hub/web_spawn.go`.
2. Remove the route-only `spawnRequest`, `hubapi.SpawnRequest`,
   `hubapi.SpawnResponse`, client method, health capability, and their tests.
   Preserve `hubapi.RefResponse` as a small independent response type because
   the clear-session REST endpoint still uses it.
3. Remove only tests and coverage cases that exercise the retired route. Keep
   shared launcher/input helpers and all meaningful AppWire `thread/start`
   tests. Convert the sandbox containment self-test to dispatch the real
   AppWire method through the hub router.
4. Keep append-only fuzz route registries and route-indexed seed/corpus data
   unchanged. Retain the retired route's fuzz slots and adjust only comments
   that falsely describe a live handler. Do not edit the harvested fixture.
5. Update current launch guidance and comments that would otherwise instruct a
   user or maintainer to call the deleted route. Leave historical plans,
   research records, and scenario cards that use spawn only as setup for a
   different behavior unchanged; they are not shipped product callers and
   rewriting them would expand this micro-PR.

## Non-goals

This change does not alter `dirs/create`, `git/head`, mobile pairing,
archive, favorite, pin sections, delete, any other endpoint, the shared
`Spawner`/launch implementation, or the AppWire protocol. It does not add a
test whose purpose is to assert that legacy code is absent.

## Verification contract

- Run focused Go tests for `hubapi` and `cmd/evener-hub`, including the
  AppWire launch and sandbox containment coverage.
- Run frontend formatting on touched `src/` files and the canonical frontend
  gate if frontend comments are changed.
- Run `make lint`, `make vet`, `make test`, and the appropriate web/browser
  gates available on the host.
- Fetch `origin/main` again immediately before publishing, rebase if it moved,
  verify the exact commit and local gates, then publish a non-draft PR without
  merging it.
