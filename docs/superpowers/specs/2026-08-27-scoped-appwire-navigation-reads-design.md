# Scoped AppWire Navigation Reads

## Status

Approved for implementation as the first REST-to-AppWire migration slice.

## Problem

The hub's web client currently reads its navigation projection through a family
of authenticated HTTP JSON routes under `/api/navigation`. That projection is
already hub-owned, revisioned, bounded, and invalidated over AppWire. Keeping
the read path on a second transport makes the browser maintain two request
systems and leaves an application REST surface in a codebase whose application
protocol is AppWire.

The existing `thread/list` method is not a drop-in replacement. It is a
Codex-shaped list used by the daemon and other protocol consumers; it does not
represent the hub's grouped sections, project tiers, pin sections, or
generation-aware navigation cache. The web navigation replacement therefore
needs its own deliberately scoped method rather than adding navigation-specific
flags to `thread/list`.

## Goals

- Move every current `/api/navigation` read to the hub's AppWire WebSocket.
- Preserve the current navigation projection, limits, ordering, recursive
  child bounds, generation fencing, revision checks, conditional reads,
  invalidation handling, stale/retry behavior, and late-result suppression.
- Give the Go and TypeScript protocol layers one named method and generated
  method catalog entry.
- Remove the `/api/navigation` HTTP route and its HTTP-only adapter once all
  browser callers and tests use the method.
- Keep this slice independently reviewable. Existing REST routes that map to
  other AppWire methods are converted in later PRs, one logical API at a time;
  they are not left as compatibility shims after their callers move.

## Non-goals

- Do not change the broad `thread/list` contract or migrate the TUI in this
  slice. A TUI-specific consumer migration can be a later PR if it benefits
  from the navigation projection.
- Do not redesign authentication, the WebSocket connection lifecycle, static
  pages/assets, PWA bootstrap, health/build probes, or raw document/image
  bytes. Those are HTTP transport/bootstrap concerns, not application JSON
  APIs.
- Do not change projection data fields or resource limits.
- Do not add a second notification. The existing
  `evener/navigation/invalidated` notification remains the invalidation stream.

## Protocol

Add the hub-scoped request method `evener/navigation/read`.

### Request

The request uses one discriminated `resource` and only the fields relevant to
that resource:

```go
type NavigationReadParams struct {
    Resource   string `json:"resource"`
    Section    string `json:"section,omitempty"`
    SectionID  string `json:"sectionId,omitempty"`
    Catalog    string `json:"catalog,omitempty"`
    ProjectKey string `json:"projectKey,omitempty"`
    Tier       string `json:"tier,omitempty"`
    Ref        string `json:"ref,omitempty"`
    Offset     *uint32 `json:"offset,omitempty"`
    Limit      *uint32 `json:"limit,omitempty"`
    ETag       string `json:"etag,omitempty"`
}
```

The accepted resource values and required fields are:

| resource | required fields | page parameters |
| --- | --- | --- |
| `manifest` | none | none |
| `section` | `section` = `live` or `needs_you` | `offset`, `limit` |
| `pin_catalog` | none | `offset`, `limit` |
| `pin_section` | `sectionId` | `offset`, `limit` |
| `catalog` | `catalog` = `projects`, `archived_projects`, or `test_runs` | `offset`, `limit` |
| `project` | `projectKey` | none |
| `project_page` | `projectKey`, `tier` = `current`, `recent`, or `archived` | `offset`, `limit` |
| `location` | `ref` | none |

For paged resources, an omitted `offset` defaults to zero and an omitted
`limit` defaults to the existing resource-specific cap
(`maxNavigationSectionRows` = 50 or `maxNavigationCatalogRows` = 100). An
explicit `offset: 0` is valid; an explicit `limit: 0` is invalid. Limits above
the cap and offsets that do not fit in `uint32` are invalid. Unpaged resources
reject either page field even when its value is zero. The pointer fields make
omitted and explicit zero values distinguishable on the wire. The server
rejects unknown resources, missing or non-empty extraneous fields, and invalid
tiers/sections/catalogs. `ETag` is an opaque conditional validator; it is
compared only to the exact cached representation selected by the request.

### Response

```go
type NavigationReadResponse struct {
    Status       string          `json:"status"` // `ok` or `not_modified`
    GenerationID string          `json:"generationId"`
    Revision     uint64          `json:"revision"`
    ETag         string          `json:"etag"`
    Data         json.RawMessage `json:"data,omitempty"`
}
```

`Data` is a raw JSON value because one method intentionally serves several
resource shapes. The `resource` discriminator makes the shape unambiguous and
the existing browser validators continue to enforce the bounded navigation
schema before data enters the store. The response does not expose gzip bytes or
HTTP status codes.

- `status: "ok"` includes the selected resource in `data`.
- `status: "not_modified"` omits `data`; the client retains its prior value.
- `generationId`, `revision`, and `etag` are mandatory in both cases. The
  envelope values are authoritative for revalidation. The response data keeps
  its existing `generation_id`/`revision` fields; the client validates them
  against the envelope once before accepting the resource.
- The response resource data retains the existing navigation JSON field names
  and metadata (`generation_id` and `revision`) so the projection contract does
  not change during the transport migration.

The generated TypeScript result consequently exposes `data` as `unknown`; the
navigation store narrows it with the existing resource-specific validators.

### Errors

Malformed parameters use `CodeInvalidParams` (`-32602`) with
`ErrorInvalidParams`. Missing projects, pin sections, or session locations are
reported as `CodeUnavailable` (`-32014`) with `ErrorActionUnavailable`, because
AppWire has no separate not-found code and the requested projection is not
available in the current hub snapshot. A missing navigation service and a
context-cancelled service wait use the same `CodeUnavailable`/
`ErrorActionUnavailable` classification. Unexpected projection/encoding
failures use `CodeInternalError` (`-32603`) with `ErrorInternal`. No HTTP status
or body text is part of the new contract.

## Server architecture

The AppWire router performs structural JSON decoding; the handler performs
semantic validation exactly once. It maps a valid `NavigationReadParams` to the
existing internal `navigationResourceKey`, calls
`NavigationService.Representation`, and compares the request ETag to the
representation ETag. It returns the representation's immutable object as raw
JSON for a changed read, or a `not_modified` response for an exact match.

`NavigationService` remains the sole authority for generation, revision,
projection coherence, cache entries, and refresh/invalidation publication.
The handler must not rebuild a projection, read source state directly, or
duplicate cache logic. It must also honor the request context so an aborted
WebSocket request stops waiting for service work.

The hub registers the method in its `ScopeHub` router catalog unconditionally;
when a test-only server has no navigation service, the handler returns the
explicit unavailable error. The generated catalog and router cross-check must
show it as `ScopeHub` (it remains absent from the daemon router). The handler
must be covered by focused tests for each resource family, conditional
responses, bad parameter combinations, missing resources, service failures,
and context cancellation.

## Browser architecture

`NavigationRevalidator` keeps its current state machine: one entry per resource
key, request coalescing, stale/target revision fencing, forced retries after
gaps, generation resets, waiter behavior, and protection against late results.
Only its request callback changes from `fetch` to `client.request`. AppWire's
browser client does not currently accept an abort signal, so a superseded
request may finish on the socket; epoch/generation guards must ensure it cannot
change store state. Server-side request contexts are still honored.

The navigation store maps its existing `ResourceKey` to
`NavigationReadParams`, calls `evener/navigation/read`, and normalizes the
result into the revalidator's internal response shape. It validates the
generation, revision, ETag, response status, and resource body using the
existing checks. A `not_modified` result is represented as a conditional hit
without replacing stored data.

The shellguard and frontend unit tests use the fake AppWire client and scripted
method responses. They must assert method names and structured params rather
than URLs. A transport-level integration/browser check must observe at least
one real `evener/navigation/read` frame and confirm that navigation data causes
no `/api/navigation` request. Production frontend source must contain no
`/api/navigation` reads.

## Removal boundary

This microproject removes:

- registration of `/api/navigation` and the navigation-specific raw-path guard
  branch;
- the HTTP navigation representation writer, HTTP ETag/encoding adapter, and
  route-specific metrics that exist only for that route;
- browser navigation fetch helpers and HTTP navigation error assertions;
- stale route-only tests, fixtures, and comments.

Projection helpers, the navigation service, invalidation notification, and
shared navigation data types remain where they are useful to the AppWire
handler and other hub internals. Remove only HTTP representation/client helpers
that have no remaining consumers; do not remove shared `hubapi` projection
types merely because their original route was HTTP. No unrelated REST endpoint
is deleted here.

## Future migration sequence

After this PR, each application API is migrated in its own small PR. Existing
REST equivalents of AppWire methods are removed in the same PR as their final
caller moves. Clearly unused surfaces such as the stale tree/fork routes and
the superseded spawn-schema route can be deleted in an upfront cleanup PR once
their legacy-only references are removed. The remaining new hub capabilities
(search, directory creation, git head, rail mutations, and deletion) get
focused AppWire methods rather than being folded into this navigation method.

## Acceptance criteria

- All navigation data requests use AppWire and a real browser navigation boot
  still shows sessions/projects, including reconnect, conditional reads,
  pagination, deep links, and invalidation refreshes. The retained HTTP
  bootstrap/static/raw-byte requests remain outside this claim.
- The AppWire method is present in Go/TypeScript generated artifacts and is
  registered on the hub router, with no daemon registration.
- No production or test path expects `/api/navigation` to exist.
- Focused tests, frontend gates, Go tests, lint, and vet pass.
- The diff contains only this conversion plus its protocol/test/documentation
  support; other REST-to-AppWire conversions remain separate.
