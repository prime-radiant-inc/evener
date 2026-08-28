# Remove the Unused Session REST Surface

## Status

Approved implementation slice: remove the remaining session REST surface after
the browser and TUI moved to AppWire. The `rename` and `delete` routes were
already migrated to typed AppWire methods, so this change removes the final
`/api/sessions/` dispatcher and route registration. This change adds no
compatibility layer and no new AppWire method.

## Caller evidence

- The shipped frontend has no request for the retired routes. Session reads and
  mutations use the existing typed AppWire methods, including `rename` and
  `delete`.
- Production Go code has no caller of `hubapi.Client.Session`, `Send`, `Tasks`,
  `Interrupt`, `Compact`, `Clear`, `Fork`, or `SetModel`. The TUI uses the
  existing typed AppWire thread, turn, and task methods; hub startup uses only
  `hubapi.Client.Health`.
- The hub already registers the corresponding AppWire reads and mutations. The
  remaining HTTP references are the dispatcher, its handlers and response
  types, route-specific tests, and fuzz/coverage inventories.

## Removal scope

1. Remove the `/api/sessions/` mux registration and `handleAPISession`; all
   session operations, including `rename` and `delete`, now use AppWire.
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
   fuzz tables and current coverage route inventories without disturbing the
   typed AppWire `rename`/`delete` behavior or append-only compatibility data.
6. Correct the live REST polling examples and route inventory in
   `docs/developing-evener/agentic-testing.md`. Historical specs, plans,
   research records, and scenario reports remain unchanged.

## Explicit preservation

- Preserve `GET /api/health`, `/auth`, `/doc/*`, the SPA/static resources, and
  all unrelated REST routes.
- Preserve the typed AppWire implementations and browser behavior for session
  rename and delete. Their former REST dispatcher branches, handlers, tests,
  and fuzz coverage are already removed by the preceding migrations.
- Preserve the existing AppWire protocol and implementations for thread reads,
  turn start/interrupt, task list, fork, clear, compaction, shutdown, model,
  and reasoning effort. Do not add replacement methods.
- Do not add tests that assert legacy symbols or routes are absent.

## Verification

- Run focused `hubapi` and `cmd/evener-hub` tests after the route-only tests
  and probes are removed.
- Audit production Go and frontend sources again to confirm no session callers
  remain under `/api/sessions/`.
- Fetch and rebase onto the current `origin/main` before publication. On that
  exact head, run formatting, `git diff --check`, `make vet`, and the canonical
  `make merge-approval-gate`.
- Push the verified head as a non-draft PR. Merge only after exact-head CI and
  whole-range review pass.
