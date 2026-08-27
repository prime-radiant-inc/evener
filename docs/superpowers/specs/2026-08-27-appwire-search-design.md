# AppWire Search Microproject

## Goal

Move the command palette's cross-session search from the hub's REST
`GET /api/search?q=` route to a hub-scoped AppWire request. The palette must
keep its existing live/past sections and qualified session refs while using
the same authenticated WebSocket connection as the rest of the application.

## Wire contract

Add the hub-only method `evener/search` to the AppWire catalog, including the
typed `appwire.Client.Search` helper used by Go-side round-trip tests and
future non-browser clients.

`SearchParams`:

```json
{"query":"string"}
```

The hub trims and lowercases the query for matching. An empty query returns
the most recent past results and all current live sessions, subject to the
existing result limits. `SearchResponse` contains `live` and `past` arrays of
`SearchResult` objects. Each result has:

```json
{
  "id":"string",
  "title":"string",
  "project":"string",
  "state":"string",
  "age":"string",
  "ref":"source:thread"
}
```

Live results retain the hub's current ordering and normalized state. Past
results retain the current 20-result limit, ended state, age formatting,
generated-name title selection, and project basename. `ref` remains the
qualified ref used to open the session; a bare ID is not a valid replacement.
Empty result arrays are encoded as arrays, not JSON null.

The method is `ScopeHub`; it aggregates the hub roster and past index and is
not forwarded to a daemon or external AppWire source. Search failures use the
normal AppWire error envelope.

## Server changes

- Move the existing search projection into a hub helper shared by the AppWire
  handler and its focused tests, retaining the existing `PastIndex.Search`,
  `LiveEntryWithPastLess`, `liveTitle`, `searchPastTitle`, and
  `hubRefFromTreeNodeID` behavior.
- Register `evener/search` in the hub AppWire server and catalog.
- Remove the `/api/search` route, its HTTP-only response types, and its
  search-only helpers after the AppWire handler owns the behavior.
- Remove only route-specific HTTP/fuzz coverage; keep behavior coverage on the
  helper and one real AppWire client/server round trip.
- Regenerate `docs/appwire-protocol.md` and
  `cmd/evener-hub/frontend/src/protocol/types.gen.ts` with `make generate`.

## Frontend changes

- Replace `fetchSearch(query)`'s HTTP implementation with a typed
  `evener/search` request through the existing connection store. Import the
  generated `SearchResponse` and `SearchResult` types rather than defining a
  second palette-local wire shape.
- Keep `fetchSearch` as the small palette seam around the typed request, and
  preserve stale-response suppression, debounce, empty-array handling, and
  failure presentation.
- Update palette tests to script `evener/search` on `FakeClient`; do not retain
  tests that assert REST URLs or browser fetch behavior.
- Remove production and test references to `/api/search` from the palette.

## Non-goals

- Do not change the search matching fields, sort order, result sections, or
  session-opening URL behavior.
- Do not add a compatibility REST fallback.
- Do not change the separate `thread/list` navigation contract; this method is
  a deliberately small replacement for the palette's richer live/past result
  projection. Keep the existing request-time index lookup and ordering logic;
  this microproject must not broaden cache or roster semantics.

## Verification

- Focused Go AppWire/hub tests and focused palette tests.
- `make generate`, followed by generated-output freshness checks.
- `make lint`, `make vet`, `make test`, and `make test-web`.
- Search production code for `/api/search`, `fetchSearch`, and the removed HTTP
  response types before opening the PR.
