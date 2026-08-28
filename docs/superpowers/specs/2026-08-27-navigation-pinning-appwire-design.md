# Navigation Pinning AppWire Migration Design

## Goal

Move the remaining browser navigation-pinning mutations from REST to typed
AppWire and remove their REST surface. Pin-section reads continue to use the
existing bounded `evener/navigation/read` pin catalog; this change does not add
another read contract.

## Scope

The migration covers the production browser callers and server behavior behind:

- pin-section rename and delete;
- session assignment by existing section ID or new/reused section name; and
- session unpin.

It does not change archive, favorite, session deletion, project deletion, or
session rename behavior. It adds no REST fallback or compatibility layer.

## Protocol

Add four hub-scoped typed AppWire methods:

- `evener/pin-section/rename` with `{section_id, name}`;
- `evener/pin-section/delete` with `{section_id}`;
- `evener/session-pin/assign` with `{session_ref, section_id?, section_name?}`;
- `evener/session-pin/unpin` with `{session_ref}`.

The assign method requires exactly one of `section_id` and `section_name`.
Responses retain the current durable contract:

- pin-section summaries contain `id`, canonical `name`, and `member_count`;
- every mutation returns `ok`, an idempotent `changed` receipt, and the
  `navigation` mutation produced by the committed refresh.

| Method | Additional response fields |
| --- | --- |
| rename | canonical `section` summary |
| delete | removed `member_count` |
| assign | canonical qualified `session_ref` and assigned `section` |
| unpin | canonical qualified `session_ref`, with no assigned section |

The protocol types live in `appwire`; the obsolete REST-only pin types leave
`hubapi`. Generated TypeScript and protocol documentation are refreshed through
the repository generator.

## Server

A focused AppWire handler module owns validation, store calls, conversion to
wire summaries, navigation refresh, and attention notification. Extract the
current REST-scoped top-level-session resolver into a transport-neutral helper
and reuse it so qualified/local references, live sessions not yet in PastIndex,
and rejection of clusters, subagents, forks, malformed refs, and unknown refs
remain unchanged.

Store errors map as follows while retaining their existing message text:

- invalid names, invalid exactly-one-of `section_id`/`section_name`
  combinations, and invalid session references: `InvalidParams`;
- unknown section IDs: a machine-readable `resourceNotFound` AppWire error;
- duplicate normalized names: `Conflict`;
- missing store or unexpected store failure: `InternalError`; and
- missing/failed navigation refresh: `Unavailable`.

A successful no-op uses a shared `NavigationService` empty-mutation helper to
return the current navigation generation with no targets and publish no
invalidation. Existing REST mutations that need the same receipt delegate to
that helper. A changed mutation refreshes navigation once, returns the exact
published targets, and nudges attention once.

## Browser

Rail action helpers call the four methods through the connected typed AppWire
client. Rail and session-menu adapters pass their current client into the
helpers, as the favorite action already does. The pin picker continues loading
sections from the navigation store's pin catalog and refreshes that catalog
when an assignment fails because its selected section disappeared. Other
AppWire failures retain their server-provided message for the existing dialogs
and toasts.

The rail's delete confirmation currently uses `listPinSections` to obtain a
durable member count. Migrate that flow to `loadPinCatalog` plus
`selectPinSections`, then delete `listPinSections`; no replacement read helper
is added.

## REST Removal

After all production callers use AppWire, remove route registration for:

- `GET /api/pin-sections`;
- `PATCH /api/pin-sections/{id}`;
- `DELETE /api/pin-sections/{id}`;
- `POST /api/session-pin`; and
- `DELETE /api/session-pin?ref=...`.

Delete their handlers and tests whose only subject is HTTP method/path/JSON
behavior. Do not add a source scan or route-absence test; the typed protocol,
server behavior, and browser caller tests are the maintained contract.

## Testing

- AppWire protocol tests pin method registration and JSON round trips for all
  parameter and response shapes.
- Hub handler tests exercise real pin stores and navigation services for name
  normalization/reuse, ID assignment, validation/error mapping, rename
  conflicts, delete member counts, canonical session references, idempotent
  `changed` receipts, exact navigation targets/publications, and attention
  notification only on change.
- Browser action tests use the typed fake client to prove exact method/parameter
  selection, returned results, missing-client behavior where applicable, and
  disappeared-section recovery without asserting rendered scripts or source
  text.
- During development, run generation, focused Go/frontend tests, and Biome on
  touched `src/` files. Before opening the PR, run `make vet`,
  `make test-web-browser`, and the canonical `make merge-approval-gate` once.
