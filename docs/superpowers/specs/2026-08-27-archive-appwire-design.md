# Project archive mutation over AppWire

## Goal

Move the web UI's project/session archive action from `POST /api/archive` to a
typed hub AppWire request, while preserving the durable archive decision,
navigation convergence, and attention invalidation behavior.

This is one migration PR. Favorite, pin sections, project/session deletion,
mobile pairing, spawn, and every other endpoint remain unchanged.

## Contract

The hub exposes the new ScopeHub request `evener/archive/set`:

```json
{
  "kind": "session" | "project",
  "id": "canonical archive target id",
  "workingDir": "canonical project directory, required for project targets",
  "archived": true | false
}
```

`workingDir` is omitted for session targets. The typed result is:

```json
{
  "ok": true,
  "navigation": {
    "generation_id": "...",
    "targets": [
      { "kind": "...", "...": "...", "revision": 1 }
    ]
  }
}
```

`navigation` is the committed mutation receipt. It is returned from the same
refresh that publishes `evener/navigation/invalidated`, so a client can apply
the receipt before or alongside the notification. The shared navigation
mutation shape belongs to `appwire`; the existing `hubapi` name becomes an
alias so other hub code keeps one wire shape. Its existing `generation_id`
field is retained because the receipt is also the response shape used by the
remaining hub-owned mutation responses; only the new archive params introduce
the AppWire-style `workingDir` key.

## Server behavior

The AppWire handler retains the REST handler's semantics:

1. Validate the kind and non-empty ID.
2. For a project, reject `no-project`, require `workingDir`, resolve the
   directory, and require the resolved project ID to match `id`.
3. Call `ArchiveStore.Set(kind, id, archived, time.Now())`. Both archive and
   unarchive remain explicit upserts, including the canonical project ID.
4. Refresh navigation with the existing hint: project targets refresh the
   scoped project; session targets refresh all loaded projects.
5. Poke the attention watcher after the refresh and return the committed
   navigation mutation.

Validation failures are AppWire invalid-params errors. Store and navigation
failures remain internal/unavailable AppWire errors through the normal server
error boundary. No HTTP route is retained as a compatibility alias.

## Client and tests

`setArchived` obtains the connected typed AppWire client and invokes
`evener/archive/set`; no `fetch` call remains in that action. The action tests
script a `FakeClient` request and check the exact typed parameters/result. Rail
integration tests use the same AppWire seam while continuing to prove the
optimistic overlay, target convergence, and rollback toast.

The hub tests dispatch the real registered AppWire handler and verify session
and project persistence, validation, response receipts, and publisher
convergence. Existing navigation-convergence coverage is migrated from the
deleted HTTP helper to AppWire dispatch. Route-specific archive tests and the
retired route file are removed; no test asserts that the old route is absent.

Generated AppWire TypeScript and protocol documentation are refreshed with
`make generate`.

## Verification

Use the repository's deterministic gates and the frontend guidance from
`docs/developing-evener/testing.md`: prove new tests red before implementation,
run focused Go/frontend tests, run Biome on touched `src/` files, then run
`make test`, `make lint`, `make vet`, and the appropriate browser gate when the
host supports it. Fetch `origin/main` before publication, rebase if it moved,
rerun affected verification, and publish a non-draft PR without merging.
