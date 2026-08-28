# Project favorite mutation over AppWire

## Context

The web UI currently changes project favorites through `POST /api/favorite`.
The production caller is the project action in the navigation rail. The route
also retains an intentional rejection for the obsolete session-favorite shape;
that rejection is part of the current contract and must not disappear as an
incidental result of the transport migration.

Jesse has confirmed that this service has no external customers. The used
project mutation can therefore move to AppWire and the superseded REST route
can be retired in the same change. The migration remains deliberately limited
to this one mutation; other REST endpoints are separate microprojects.

## Contract

Add the hub-scoped method `evener/favorite/set` with these typed values:

```go
type FavoriteSetParams struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Favorited bool   `json:"favorited"`
}

type FavoriteSetResponse struct {
	OK         bool               `json:"ok"`
	Navigation NavigationMutation `json:"navigation"`
}
```

Keeping `Kind` is intentional. The server continues to reject `kind=session`
with the existing guidance that session favorites moved to the session-pin
API, rejects other kinds, and requires a non-empty ID. This preserves the
route's meaningful validation while making the project operation typed at the
AppWire boundary.

`NavigationMutation` becomes an AppWire-owned shared type with the existing
`generation_id` and `targets` JSON shape. `hubapi.NavigationMutation` becomes a
type alias so existing navigation code keeps its public Go shape while the
generated AppWire and frontend types have one source of truth.

## Server behavior

The typed handler will:

1. Validate kind and ID, returning AppWire invalid-params errors.
2. Return an internal error if the favorite store is unavailable.
3. Call the existing favorite store with the supplied project ID and boolean.
4. Refresh navigation with the project ID as the change hint.
5. Notify the existing mutation attention hook.
6. Return `{ok: true, navigation: ...}` using the committed navigation
   invalidation targets.

The favorite store, navigation invalidation, notification, and error semantics
are not otherwise changed. The web UI will call the generated AppWire client
method and continue feeding the returned navigation mutation into the existing
convergence/invalidation logic.

## Removal and verification

Remove the `/api/favorite` registration, handler, and tests that only exercise
that REST transport. Move or retain any session-pin helper code that happens
to share the old handler file. Update current frontend/scenario documentation
that describes the active project-favorite transport; preserve dated migration
history. Do not add tests whose purpose is to assert that the old route is
absent.

The focused tests cover AppWire validation, project persistence, navigation
mutation delivery, and frontend method/parameter usage. The implementation is
then checked with the repository's frontend and Go gates, including the
canonical merge-approval sequence where practical.

## Non-goals

- Archive, pin sections, project/session delete, mobile pairing, spawn, or any
  unrelated endpoint.
- Changing favorite storage or navigation invalidation semantics.
- Backward compatibility for external REST clients.
- A broad REST-to-AppWire conversion in this PR.
