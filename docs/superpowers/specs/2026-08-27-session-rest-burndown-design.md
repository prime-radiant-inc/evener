# Remove the Unused Session REST Surface

## Status

Approved implementation slice: remove the session REST reads and mutations that
have no production caller after the browser and TUI moved to AppWire. Keep the
`/api/sessions/` dispatcher only for the active `rename` and `delete` routes
being migrated separately. This change adds no compatibility layer and no new
AppWire method.

## Caller evidence

- The shipped frontend has no request for the retired routes. Its only
  `/api/sessions/` requests are the active `rename` and `delete` rail actions.
- Production Go code has no caller of `hubapi.Client.Session`, `Send`, `Tasks`,
  `Interrupt`, `Compact`, `Clear`, `Fork`, or `SetModel`. The TUI uses the
  existing typed AppWire thread, turn, and task methods; hub startup uses only
  `hubapi.Client.Health`.
- The hub already registers the corresponding AppWire reads and mutations. The
  remaining HTTP references are dispatcher branches, handlers and response
  types used only by those branches, route-specific tests, and fuzz/coverage
  inventories.

## Removal scope

1. Reduce `handleAPISession` to its shared ref parsing plus the `rename` and
   `delete` branches. Preserve the mux registration until both active routes
   are migrated.
2. Delete handlers and helpers that become unreachable after removing the
   listed reads, send, fork, clear, model, reasoning-effort, interrupt,
   compact, and shutdown branches. Retain shared workspace projection,
   capability derivation, daemon status reads, and deletion fencing used by
   active code. Remove only the HTTP `renderSessionTasks` adapter; preserve the
   AppWire `hubTasksList` -> `pastTasksListResponse` -> `loadPersistedTasks`
   fallback and its behavior tests.
3. Remove the obsolete `hubapi.Client` session wrappers and their private POST
   helper. Keep `NewClient`, URL construction, GET support, `Health`, health
   response/error types, refs, and shared session capability types used by
   workspace/navigation code.
4. Remove REST-only request/response/detail types once the caller audit shows
   no remaining use. Do not remove shared `SessionCapabilities` or other
   domain types consumed outside the retired route.
5. Delete tests and fixtures whose contract is the retired HTTP adapter.
   Preserve meaningful AppWire thread/turn/task behavior tests instead of
   translating transport-only assertions. Remove retired routes from mutable
   fuzz tables and current coverage route inventories without disturbing
   active `rename`/`delete` cases or append-only compatibility data.
6. Correct the live REST polling examples and route inventory in
   `docs/developing-evener/agentic-testing.md`. Historical specs, plans,
   research records, and scenario reports remain unchanged.

## Explicit preservation

- Preserve `GET /api/health`, `/auth`, `/doc/*`, the SPA/static resources, and
  all unrelated REST routes.
- Preserve `POST /api/sessions/{ref}/rename` and
  `POST /api/sessions/{ref}/delete`, including their dispatcher, handlers,
  browser callers, tests, and fuzz coverage.
- Preserve the existing AppWire protocol and implementations for thread reads,
  turn start/interrupt, task list, fork, clear, compaction, shutdown, model,
  and reasoning effort. Do not add replacement methods.
- Do not add tests that assert legacy symbols or routes are absent.

## Verification

- Run focused `hubapi` and `cmd/evener-hub` tests after the route-only tests
  and probes are removed.
- Audit production Go and frontend sources again to confirm only active
  `rename`/`delete` callers remain under `/api/sessions/`.
- Fetch and rebase onto the current `origin/main` before publication. On that
  exact head, run formatting, `git diff --check`, `make vet`, and the canonical
  `make merge-approval-gate`.
- Push the verified head as a non-draft PR and do not merge it.
