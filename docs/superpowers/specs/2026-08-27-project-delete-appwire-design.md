# Project deletion over AppWire

## Goal

Move the rail's project-delete action from `POST /api/project/delete` to one
typed hub AppWire method while preserving the existing destructive-operation
contract.

This is one migration PR. Session deletion remains on its existing REST route.
No compatibility route or adapter is retained.

## Contract

The hub exposes the new ScopeHub request `evener/project/delete`:

```json
{
  "key": "canonical project ID",
  "workingDir": "canonical project directory"
}
```

The typed result is:

```json
{
  "deleted": ["session IDs removed by this request"],
  "skipped": [
    { "id": "session ID", "reason": "why ownership or cleanup failed" }
  ],
  "navigation": {
    "generation_id": "...",
    "targets": [
      { "kind": "project", "projectKey": "...", "revision": 1 }
    ]
  }
}
```

`navigation` is the committed mutation receipt. A request that deletes no
session returns the current generation with no targets and publishes no
invalidation. A request that deletes at least one session returns the project
target from the same refresh that publishes `evener/navigation/invalidated`.

If any project session is live at the initial whole-project check, the request
fails before deletion with AppWire conflict code `-32013`, message
`project has live sessions`, and error data containing the same short session
IDs previously returned as `live`. Invalid key/path inputs use invalid-params;
server state and cleanup-commit failures use internal-error; navigation refresh
failures use unavailable.

## Server behavior

The existing project deletion implementation remains the source of truth. Its
HTTP decoding/writing boundary is replaced by a typed method that:

1. Requires `key` and `workingDir`, rejects `no-project`, validates the project
   ID, resolves `workingDir`, and requires the resolved ID to equal `key`.
2. Requires the trusted Past index and deletion store, and resumes an existing
   durable deletion generation before consulting the current tree.
3. Validates `key` and the canonical path against the current visible or
   archived tree entry.
4. Resolves the complete uncapped project membership from `PastIndex.All()`,
   preserving each entry's trusted `StateDir`, and fails closed before deleting
   anything if membership resolution fails.
5. Refuses the entire operation if any candidate is live at entry. It then
   acquires each per-session deletion owner, recording concurrent resumes,
   API-log reservations, or other ownership failures as skips.
6. Commits the durable project-deletion fence, runs the existing cleanup path,
   removes decisions only for artifacts actually deleted, rebuilds Past,
   refreshes roster/tree inputs, and marks the generation deleted only when the
   cleanup contract is complete.
7. Returns the existing deleted/skipped details and refreshes navigation only
   when at least one session was removed.

The implementation is moved out of the REST-named file, not copied. The typed
result and skip shapes live in `appwire`; the existing session REST envelope
reuses the skip shape while its cleanup helpers remain shared. The session
route and caller are not migrated in this change.

## Client and tests

`deleteProject` obtains the connected typed AppWire client and invokes
`evener/project/delete`; no project-delete `fetch` remains. Its action tests use
`FakeClient` to prove exact typed params, result forwarding, AppWire failure
propagation, and the disconnected-client failure. Rail tests retain the current
optimistic hide, receipt convergence, deleted-pane closure, partial-skip
warning, and error rollback behavior.

Existing project deletion tests keep exercising the real deletion machinery
but dispatch through the registered AppWire router. Coverage is organized
around four contracts: validation and error data, deletion and idempotence,
fence/concurrency outcomes, and receipt/publication convergence. HTTP-only
method/JSON coverage and direct route-handler fuzz calls are removed with the
route. Session-deletion tests remain on their current REST seam.

Current routing, parity, and manual scenario documentation is updated to name
the AppWire method. Historical implementation plans remain historical.
Generated AppWire TypeScript and protocol documentation are refreshed with
`make generate`.

## Verification

Follow `docs/developing-evener/testing.md` and strict TDD for the new typed
contract and production caller. Run focused AppWire/hub/frontend tests, Biome
on touched frontend `src/` files, the real-browser gate, and the canonical
`make merge-approval-gate`. Fetch `origin/main` again before publication; if it
advanced, rebase and rerun affected verification before pushing and opening a
non-draft PR. Do not merge.
