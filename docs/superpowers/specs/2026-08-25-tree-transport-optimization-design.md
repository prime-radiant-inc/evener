# Navigation Transport Optimization Design

**Date:** 2026-08-25
**Status:** approved design, pending implementation plan
**Scope:** replace the Web UI's monolithic `/api/tree` refresh path with revisioned, resource-scoped navigation snapshots while preserving current navigation behavior.

## Problem

The Web UI repeatedly transfers and parses a large `/api/tree` response. The observed response is about 429 KB. At one request every 10 seconds, that rate is about 151 MiB per hour and 3.5 GiB per day for one open tab.

The current React client has no periodic `/api/tree` timer. It loads the tree once, then refetches the complete response after:

- `thread/started`;
- `thread/closed`;
- `evener/attention/changed`;
- `evener/tree/changed`;
- an AppWire reconnect;
- a successful rail mutation; or
- a manual retry.

Notification requests use a 250 ms trailing debounce. The debounce combines a close burst, but a later event or an event received while another request is in flight starts another full request. Older requests are not aborted. A generation guard prevents an old response from committing, but only after the browser has downloaded and parsed it.

The backend memoizes the core `hubcore.Tree` by input version, remote generation, and a 30-second time bucket. That cache does not cover the complete request. Every request still:

1. copies roster and past-index data;
2. clones the remote-thread snapshot;
3. resolves project identities;
4. reads archive, favorite, and pin data;
5. projects every REST row;
6. serializes the complete response.

The wire response also duplicates data. Every active and test project includes as many as 50 rows in each of three tiers even when the project is collapsed. A session can appear again in Live, Needs You, and a pin section. Only archived projects use lazy stubs.

The server sends plain JSON without `ETag`, conditional revalidation, gzip, or a serialized-response cache. A deterministic 1,000-session probe produced a 574 KB response. On an Apple M5 Max, warm full requests took roughly 13–22 ms and allocated about 23 MiB per request. A synthetic gzip pass reduced the repetitive fixture to 18.6 KB; the exact production ratio remains unmeasured.

## Goals

The redesign must:

- make idle navigation traffic zero after initial hydration, except after reconnect or a real semantic change;
- bound each navigation response independently of sessions hidden in collapsed projects;
- rebuild and serialize a resource once per semantic revision, not once per client request;
- fetch only resources affected by a change;
- prevent mutation responses, AppWire notifications, and overlapping refreshes from duplicating requests;
- retain last-good data and isolate project failures;
- preserve Live, Needs You, pins, project tiers, pagination, deep links, mutations, reconnect behavior, and notification semantics;
- provide measurements that prove request, byte, allocation, and build reductions; and
- retire the legacy tree architecture rather than maintain two permanent paths.

## Non-Goals

This design does not:

- add Server-Sent Events or another live transport;
- introduce an ordered patch log, replay protocol, or event-sourced client model;
- change session transcript, task, activity, search, or mutation APIs except where a mutation returns navigation revision metadata;
- virtualize rail rendering;
- change project grouping, tier classification, attention ranking, pinning, or archive semantics; or
- make HTTP caches shared across users or hubs.

## Chosen Architecture

Use **revisioned, resource-scoped pull**.

HTTP remains authoritative. AppWire announces which bounded HTTP resources became stale. The client conditionally revalidates those resources. This model avoids both extremes:

- hardening the existing monolith would still transfer the full tree after every real change;
- a normalized delta stream would require patch ordering, replay retention, gap recovery, tombstones, and a more failure-prone client reducer.

The design introduces a small manifest, bounded project resources, one server-side `NavigationService`, one typed invalidation, and one client-side resource revalidator.

## Resource Model

### Wire conventions

All arrays are present and non-null. Optional scalar fields are omitted when
unknown. `generation_id` is a non-empty opaque string. Revisions and AppWire
sequences are nonnegative integers no greater than JavaScript's
`Number.MAX_SAFE_INTEGER`. The server returns the existing JSON error envelope
for 4xx and 5xx responses.

Collection queries accept a nonnegative `offset` and a `limit` from 1 through
the endpoint's hard maximum. Offsets are unsigned 32-bit integers. Implementations
must index an immutable slice or indexed query directly rather than scan to the
offset; an offset at or beyond the collection length returns 200 with an empty
page and `remaining: 0`. Invalid enum, offset, limit, or extra pagination
combinations return 400. Collection order is stable:

- Live and Needs You retain the current authoritative tree order;
- pin sections sort case-insensitively by name, then ID;
- project catalogs retain the current project-bucket order; and
- project tiers retain the current most-recent-first order.

### Navigation manifest

`GET /api/navigation` returns `NavigationManifest`:

```text
NavigationManifest {
  generation_id: string
  revision: number
  sources: Source[]
  attentionSummary: AttentionSummary
  sections: {
    live: ResourceDescriptor
    needs_you: ResourceDescriptor
    pin_sections: ResourceDescriptor
  }
  catalogs: {
    projects: ResourceDescriptor
    archived_projects: ResourceDescriptor
    test_runs: ResourceDescriptor
  }
}

ResourceDescriptor { count: number }
Source { id: string, label: string, kind: string, online: bool }
AttentionSummary { needsYou: number, error: number, working: number }
```

`generation_id` identifies one running server generation. `revision` is the
manifest's monotonic revision within that generation. The manifest contains no
session rows, project summaries, or pin-section descriptors. Hidden sessions
therefore cannot enlarge it. A hub accepts at most 64 configured sources; startup
rejects a larger source set. These rules make the manifest itself bounded.

### Global section resources

Global rows use bounded resources:

- `GET /api/navigation/sections/live`;
- `GET /api/navigation/sections/needs-you`; and
- `GET /api/navigation/pin-sections/{id}`.

Each returns:

```text
NavigationSectionResource {
  generation_id: string
  revision: number
  sessions: NavigationSessionSummary[]
  remaining: number
  truncated: bool
}
```

The first two endpoints and each pin section allow `offset` and `limit`, with a
hard maximum of 50 top-level rows per response. Rows remain reachable through
additional pages. A session shared by Live and Needs You can appear in both
bounded resources; the client does not maintain a cross-resource entity graph.

Pin-section descriptors use
`GET /api/navigation/pin-sections?offset=<n>&limit=<n>`. The endpoint returns
generation, revision, up to 100 `{id, name, count}` descriptors, and `remaining`:

```text
NavigationPinSectionCatalog {
  generation_id: string
  revision: number
  pin_sections: PinSectionDescriptor[]
  remaining: number
}

PinSectionDescriptor { id: string, name: string, count: number }
```

The manifest's pin-section count tells the client whether to load this catalog.

`NavigationSessionSummary` contains only navigation data that current consumers
use:

- ref, host ID, and session ID;
- title and project label;
- state, kind, live, ask-pending, and dormant facts;
- branch, cluster count, favorite, rename, and subagent-overflow facts when present;
- updated timestamp; and
- recursive children.

Its exact shape is:

```text
NavigationSessionSummary {
  ref: string
  host_id: string
  session_id: string
  title: string
  project: string
  state: "errored" | "awaiting" | "active" | "warning" | "idle" | "ended" | "notLoaded"
  kind: "session" | "subagent" | "fork" | "cluster"
  branch?: string
  cluster_count?: number
  favorite?: bool
  rename?: bool
  live: bool
  ask_pending?: bool
  dormant?: bool
  updated_at?: RFC3339 string
  more_subagents?: number
  omitted_descendants?: number
  children: NavigationSessionSummary[]
}
```

Clients derive `row_id`, pin-section membership, tier, and display age from the
owning resource. The new wire contract omits the unused `model` field and the
server-formatted `age` field.

Every response that carries `NavigationSessionSummary` applies the same hard
bounds: 50 top-level rows per page, 50 children per row, 2,000 total nodes, 32
levels, and 2 MiB of uncompressed JSON. The encoder stops before a row or branch
would cross a node, depth, or byte bound, sets resource-level `truncated: true`,
and records `omitted_descendants` on the cut parent when known. Top-level rows
not emitted remain reachable through the next page.

The projection also enforces wire string bounds before byte accounting: titles
remain capped at 200 runes; project/source/pin labels and branch names at 512
runes; refs and opaque IDs at 1,024 bytes; and working directories at 4,096
bytes. Display strings truncate rune-safely with an ellipsis. An identity that
exceeds its validated bound is malformed input and produces an explicit row or
resource error; it is never truncated into a different identity.

The manifest has a 256 KiB uncompressed ceiling. Pin-descriptor and project-
catalog pages have a 512 KiB uncompressed ceiling. With the source, row, and
string caps these ceilings are defensive; a paged collection stops before the
next descriptor would cross its ceiling and includes that descriptor in
`remaining`. A manifest that violates its ceiling is an internal invariant
failure and returns 500 rather than a partial authority document.

### Project catalogs

Project headers use three bounded catalog resources:

- `GET /api/navigation/catalogs/projects`;
- `GET /api/navigation/catalogs/archived-projects`; and
- `GET /api/navigation/catalogs/test-runs`.

Each catalog accepts `offset` and `limit`, with a hard maximum of 100 summaries
per response, and returns:

```text
NavigationProjectCatalog {
  generation_id: string
  revision: number
  projects: NavigationProjectSummary[]
  remaining: number
}
```

`NavigationProjectSummary` has this exact shape:

```text
NavigationProjectSummary {
  key: string
  name: string
  working_dir?: string
  rollup_state?: string
  rollup_live?: number
  rollup_attn?: number
  default_expanded?: bool
  more_current?: number
  more_recent?: number
  more_archived?: number
  worktrees?: number
  is_archived?: bool
  favorite?: bool
  session_count: number
}
```

It preserves:

- key and name;
- working directory where currently exposed;
- rollup state and counts;
- default expansion;
- tier overflow counts;
- worktree count;
- archived, favorite, and test-run placement; and
- authoritative session count.

Project summaries do not carry project-resource revisions. AppWire project
targets invalidate loaded project representations directly; reconnect and event
gap recovery conditionally revalidate them.

### Project resource

`GET /api/navigation/projects/{key}` returns one `NavigationProjectResource`:

```text
NavigationProjectResource {
  generation_id: string
  revision: number
  key: string
  current: NavigationTier
  recent: NavigationTier
  archived: NavigationTier
  truncated: bool
}

NavigationTier {
  sessions: NavigationSessionSummary[]
  remaining: number
}
```

Each tier returns the first bounded page. Every root and page request enforces a
maximum of 50 top-level rows per tier and the common recursive-node, depth,
string, and 2 MiB byte bounds defined above. A cut branch sets `truncated` and
reports omitted descendants under the common contract.

Existing top-level pagination moves under the project resource:

`GET /api/navigation/projects/{key}?tier=<tier>&offset=<n>&limit=<n>`

`tier` is exactly `current`, `recent`, or `archived`; offset is nonnegative; and
limit is 1 through 50. A page response contains one `NavigationTier`, not all
three tiers, and has this envelope:

```text
NavigationProjectPage {
  generation_id: string
  revision: number
  key: string
  tier: "current" | "recent" | "archived"
  offset: number
  sessions: NavigationSessionSummary[]
  remaining: number
  truncated: bool
}
```

A project response owns its row tree. The client does not merge project rows
into a global entity graph. This deliberate simplification avoids entity
lifetime, garbage collection, and cross-resource merge rules. A globally
visible session may exist once in a bounded global section and once in its
loaded project, but duplication is bounded and resource ownership remains
explicit.

### Session location

The current client scans every loaded project row to find a deep-linked session's top-level owner. The new API must make that relationship explicit.

`GET /api/navigation/sessions/{ref}` returns:

```text
NavigationSessionLocation {
  generation_id: string
  revision: number
  ref: string
  top_level_ref: string
  project_key?: string
  top_level: bool
}
```

`NavigationService` builds the location index with the immutable core tree. The
endpoint carries generation and revision metadata but is read on
demand and is not a long-lived client resource. A missing session returns 404.
This endpoint avoids both a complete session-to-project map in the manifest and
the existing session-detail path's full live-thread read.

## Revision Model

Revisions are comparable only within a `generation_id`.

- `generation_id` is an opaque value created when the hub starts.
- `sequence` is a JSON-safe nonnegative integer that increases for every navigation invalidation in that generation.
- each resource revision is a JSON-safe nonnegative integer that increases only when that resource's semantic content changes.
- validators include the generation and resource revision, so a hub restart cannot collide with an earlier cached representation.

The client resets revision comparisons and conditionally revalidates loaded resources when `generation_id` changes.

`sequence` belongs only to the ordered AppWire stream and initialize response.
It is not part of an HTTP representation, response header, cache key, or ETag.
HTTP resources carry only generation and resource revision. This separation
keeps unrelated invalidations from changing cached response metadata.

The server event is:

```text
evener/navigation/invalidated {
  generation_id: string
  sequence: number
  targets: [
    { kind: "manifest", revision: 44 },
    { kind: "section", section: "live", revision: 12 },
    { kind: "pin_catalog", revision: 9 },
    { kind: "pin_section", section_id: "opaque-id", revision: 8 },
    { kind: "catalog", catalog: "projects", revision: 21 },
    { kind: "project", project_key: "opaque-key", revision: 18 },
    { kind: "all_loaded_projects" }
  ]
}
```

The first AppWire initialize response and each reconnect response carry the
current navigation `generation_id`, `sequence`, and capability version. AppWire
preserves event order. A sequence gap causes conditional revalidation of the
manifest and every loaded section, pin-descriptor page, catalog page, and
project representation.
Reconnect performs the same recovery, so the server needs no replay log.

Section and pin-catalog targets apply to every loaded page of that collection.
Catalog targets apply to every loaded page of that project catalog. Project
targets apply to the root and every loaded tier page for that key. All those
representations share the target's semantic revision but retain distinct ETags.
When a producer cannot
identify the affected project safely, it emits `all_loaded_projects`, which has
no revision. The client conditionally revalidates every loaded project
representation and accepts either a 304 or its current revision. Collapsed
projects remain unloaded.

## Server Architecture

### `NavigationService`

One service owns navigation state and presentation. It provides:

1. a composite semantic generation;
2. immutable core tree snapshots;
3. per-resource revisions;
4. cached manifest and project objects;
5. cached encoded JSON;
6. cached gzip representations;
7. weak semantic ETags;
8. bounded LRU eviction for project/page representations; and
9. typed invalidation publication.

Each `WebServer` owns exactly one `NavigationService`. The service and every
cache it owns are scoped to that server's state root, source registry, stores,
and auth guard. No cache is package-global or shared across WebServer instances,
users, hubs, or auth scopes.

HTTP handlers ask the service for an encoded resource. They do not copy metadata, resolve projects, query decision stores, rebuild rows, or encode JSON on a cache hit.

The cache lookup occurs before expensive snapshot assembly. A representation
key contains resource identity, server generation, and that resource's semantic
revision. It does not contain every source generation.

Stores that lack a cheap revision must expose one. After durable commit, a
store compares the new content fingerprint with its prior fingerprint. It
advances its source revision and emits a change set only when semantic content
changed; a successful no-op write advances neither.

The invalidator maps source changes to resource revisions through this
dependency matrix:

The shared **session projection fingerprint** covers every
`NavigationSessionSummary` field and recursive membership: identity, title,
project label, state, kind, branch, cluster count, favorite, rename, live,
ask-pending, dormant, updated timestamp, subagent/descendant overflow, and child
fingerprints. The core snapshot maintains reverse membership from a session ref
to each section and project representation that currently contains it. A
projection-fingerprint change advances every containing resource.

| Resource | Dependencies |
|---|---|
| Manifest | sources, attention totals, global-section counts, pin-section count, project-catalog counts |
| Pin catalog | pin definitions, names, order, and member counts |
| Live section | membership/order plus every contained session projection fingerprint |
| Needs You section | eligibility/membership/order, archive decisions, plus every contained session projection fingerprint |
| Pin section | pin membership/order plus every contained session projection fingerprint |
| Project catalog | every `NavigationProjectSummary` field, membership, placement, and order |
| Project and pages | tier membership/order plus every contained session projection fingerprint and tier clock |
| Session location | session identity, lineage, top-level status, and project membership |

A source change advances only dependent resources. An unknown scope widens to
the applicable catalog/section and loaded-project targets; it never forces an
unrelated resource cache miss merely because a global store counter advanced.

### Build and publication

The service single-flights each representation key. Concurrent misses join one
snapshot, build, encode, and compression operation.

On the owning cache miss, the service:

1. captures one immutable input snapshot;
2. resolves each distinct project identity once;
3. builds the core tree once;
4. derives resource projections from that tree;
5. encodes and compresses each requested resource once; and
6. rereads the relevant source and resource revisions;
7. discards and retries from a fresh snapshot if any dependency changed; and
8. publishes the complete cache entry atomically only when the captured
   revisions still match.

Retry is bounded by the request context, not a fixed attempt count. If churn
prevents a coherent capture before the context ends, the request returns 503
and preserves the last-good entry. An invalidation that arrives during a build
therefore cannot publish stale data under the new revision.

A failed build or serialization does not replace the last-good entry. The request returns 5xx, and clients retain stale data with an error state.

Project, section, and catalog resources may encode lazily. Their representation
identity includes the canonical endpoint kind, validated key or section,
canonical tier when present, offset, and limit. Their ETag includes that
identity, generation, and semantic resource revision. The cache retains only
the current generation and evicts least-recently-used entries after 256
representations or 64 MiB of combined estimated object, encoded, and compressed
bytes, whichever limit it reaches first. These defaults are named constants and
observable through cache metrics.

### Time-based classification

The current cache's 30-second bucket changes relative age and tier calculations only when another request arrives. The new resource model must not poll merely to advance time.

Rows carry timestamps; the browser derives display age. The service schedules
the next actual 24-hour or 14-day tier boundary. At that boundary it advances
the affected project revision. It advances an affected catalog only when tier
overflow, placement, order, or another catalog summary field changes. The
manifest advances only if that catalog change alters a manifest count. The
scheduler always targets the earliest known boundary and resets when inputs
change.

### Change scoping

One invalidator receives typed change sets from roster, past index, remote snapshots, archive, favorite, pins, and time classification.

A change set identifies:

- affected manifest descriptors, global sections, and catalogs;
- affected old and new project keys;
- affected pin sections; and
- whether the project scope is unknown.

The invalidator compares the affected resource's semantic fingerprint, advances
each changed resource once, and emits one event. A project change invalidates a
catalog only when membership, placement, order, or header data changed. It does
not invalidate the manifest because the manifest contains only catalog counts,
not project summaries or resource revisions. A section, pin, or catalog change
invalidates the manifest only when a count stored in the manifest changed.

Existing `thread/started`, `thread/closed`, and
`evener/attention/changed` messages retain their own consumers, but they no
longer invalidate navigation on the client. The empty `evener/tree/changed`
notification retires after migration.

## HTTP Semantics

Navigation responses use:

- `Cache-Control: private, no-cache`;
- `Vary: Accept-Encoding`;
- a weak ETag derived from resource ID, generation ID, and resource revision;
- `Content-Encoding: gzip` when the client accepts gzip;
- an accurate `Content-Length` for cached representations; and
- `X-Evener-Navigation-Generation` and `X-Evener-Navigation-Revision` headers
  matching the JSON body.

A matching `If-None-Match` returns 304 with no body. A 304 still carries
generation, revision, cache, ETag, and variation headers. Event sequence never
appears in an HTTP representation or 304.

`generated_at`, if retained for diagnosis, does not affect the ETag or semantic revision.

Authentication remains the existing hub auth guard. The router URL-decodes each
project key, pin-section ID, or session ref exactly once, rejects malformed or
noncanonical encodings with 400, and validates the decoded identity against the
same immutable authorized snapshot used to build the response. A well-formed
identity absent from that snapshot returns 404. Arbitrary keys cannot select a
filesystem path or bypass project membership.

Responses are private because titles, project paths, and session state can be
sensitive. Access logs record only route class, status, duration, and byte
counts. Metrics, diagnostics, and ordinary access logs must not contain raw URL
paths, query strings, project keys, pin IDs, titles, prompts, refs, or filesystem
paths.

## Client Architecture

### Store shape

Replace the singleton `TreeResponse` authority with:

- manifest data and manifest resource state;
- global-section and pin-section pages;
- project-catalog pages;
- project resources keyed by project key;
- project detail/page state;
- expansion state;
- last-good values; and
- one resource revalidator.

Each resource state contains:

```text
ResourceState<T> {
  generation_id
  revision
  target_revision
  force_token
  value
  status
  error
}
```

`status` distinguishes initial loading, ready, refreshing, and stale-error states. An error never erases `value`.

`force_token` is a client-local counter used only for revisionless recovery
targets such as `all_loaded_projects`, reconnect, and sequence gaps. Advancing it
requires one conditional revalidation; either a 304 or a valid 200 satisfies the
token.

### Revalidator

The generic revalidator owns all navigation fetches.

- At most one request per resource may run at once.
- A typed invalidation raises `target_revision` for every loaded representation
  covered by its target.
- If an event arrives during a request, the revalidator performs at most one trailing request unless the completed response already satisfies the target.
- A response commits only when its generation matches and its revision satisfies the current target.
- An obsolete request is aborted when possible; a completed obsolete response is discarded.
- Mutation responses and AppWire events enter the same target-revision path.
- Unknown resource IDs and revision regressions record protocol errors and trigger manifest recovery.

This replaces `refreshGeneration`, uncancelled overlapping whole-tree requests, feature-owned refresh calls, and notification-specific fetch timers.

### Initial load and expansion

On boot, the client:

1. connects AppWire;
2. conditionally fetches the manifest;
3. fetches the first Live, Needs You, pin-descriptor, and active-project
   catalog pages;
4. fetches each discovered nonempty pin section and any other visible project
   catalog pages;
5. renders each resource as it arrives;
6. combines saved expansion overrides with server defaults; and
7. fetches expanded project resources with a concurrency limit of four.

Collapsed projects issue no project request. Expanding one project loads that resource. Collapsing it may retain its bounded last-good cache entry until LRU eviction.

### Live updates

On `evener/navigation/invalidated`, the client raises targets only for named resources. It does not use a time-based debounce. Resource targets naturally coalesce.

A successful mutation returns:

```text
navigation {
  generation_id
  targets
}
```

The client feeds this metadata to the revalidator. When the matching AppWire event arrives, the resource target is already satisfied or in flight, so it causes no duplicate request.

`evener/attention/changed` updates the dedicated alert, title, and badge state directly from its existing payload. It does not fetch navigation. The navigation invalidation independently updates Needs You membership or project rollups when those resources change.

### Reconnect and gaps

After reconnect, the client conditionally revalidates the manifest and every
loaded section, catalog page, and project representation. If the server
generation changed, it clears revision comparisons but keeps last-good values
until replacements arrive. The initialize response seeds the new generation
and current AppWire sequence.

A sequence gap follows the same path. Recovery never guesses or applies a partial delta.

## Failure Behavior

### Manifest failure

A failed initial load shows the existing navigation error/retry state. A failed refresh keeps the last-good manifest and marks it stale. Global navigation remains usable where its existing data permits.

### Project failure

A failed project request affects only that project. The expanded row shows an inline stale/error state and retry action while retaining old rows. Other projects and global sections remain usable.

A project 404 removes its cached representations and conditionally revalidates
the loaded page of its last-known catalog. If the catalog is unknown or the
project may have moved, it revalidates loaded pages of all three project
catalogs. It then revalidates the manifest if returned catalog counts changed.
The client removes the project header only from an authoritative catalog
response; it does not silently present an empty project as authoritative.

Section and catalog failures follow the same resource-local rule. A failed next
page preserves prior pages and leaves an explicit retry row at the failed
boundary.

### Protocol failure

An unknown resource, impossible revision regression, generation mismatch inside one response, or contradictory 304 is a protocol error. The client records the condition and conditionally revalidates the manifest. It does not mutate state speculatively.

### Server failure

Build, store-read, serialization, or compression failures return a specific 5xx response and preserve last-good cached data. The server must not convert a failed input read into an empty authoritative resource.

## Observability

Measure the new boundary without logging sensitive values.

Server metrics or structured diagnostics must include:

- request count by resource class and status;
- uncompressed and transferred bytes;
- 200 and 304 counts;
- object, encoded, and compressed cache hits and misses;
- snapshot, project-resolution, tree-build, projection, encoding, and compression durations;
- rows emitted by resource class;
- cache entries, bytes, and evictions;
- invalidations emitted by resource class; and
- invalidations widened to `all_loaded_projects`.

Client diagnostics must include:

- invalidations received;
- resource targets coalesced;
- requests started, aborted, trailed, and completed;
- 304 responses;
- stale responses discarded;
- reconnect and sequence-gap revalidations; and
- resources in stale-error state.

Diagnostics categorize resources as manifest, section, catalog, project,
location, or page. They do not record project keys, pin IDs, session refs,
titles, prompts, raw URLs, query strings, or paths.

## Compatibility and Retirement

The bundled UI and server ship together, but `hubapi.Client.Tree` exposes `/api/tree`. Migration therefore uses a temporary adapter.

During migration:

- `/api/tree` projects the legacy response from `NavigationService`'s immutable core snapshot;
- the adapter has ETag and gzip support;
- the adapter does not retain a separate snapshot builder, cache, or invalidator;
- the bundled UI stops calling it; and
- `hubapi` gains new manifest and project methods.

Initialize advertises `navigationResources: 1`. Capability absence is the only
condition that permits the bundled UI to use `/api/tree`. A server that
advertises version 1 but returns an invalid resource response produces the
resource's stale/error state; the client must not silently fall back and mask a
protocol defect. An unsupported future capability version fails explicitly.

The migration release supports the legacy UI path and `/api/tree` adapter. The
next release removes the legacy UI path after browser and integration tests show
zero fallback use against a version-1 server. That release also removes the
server adapter unless the repository's published API policy requires waiting
for a named breaking release; in that case `/api/tree` remains only as the thin
adapter, is marked deprecated, and has an explicit removal version. It may not
block removal of the old builder, store, notifications, or refresh policy.

If public compatibility requires retaining `Client.Tree` after the server
endpoint retires, that client method may assemble the legacy return type from
the new bounded resources. The server must not retain the monolithic endpoint
as a second architecture.

## Rollout

### Phase 1: baseline and service

- Add current-path request, byte, duration, allocation, and row-count baselines.
- Add store revisions and `NavigationService`.
- Add new endpoints, HTTP validators, gzip, caches, and invalidation types.
- Keep the existing UI unchanged.

### Phase 2: client migration

- Advertise navigation resources through the initialize capability response.
- Add the resource store and revalidator.
- Migrate rail rendering, attention consumers, deep links, titles, session menus, and mutation reconciliation.
- Fall back to the legacy endpoint only when the server lacks the capability.

### Phase 3: default and verify

- Enable the new client path by default.
- Compare live and deterministic metrics with the baseline.
- Verify browser behavior and request counts under activity, mutation, reconnect, and failure.

### Phase 4: retirement

- Remove legacy refresh triggers and the old frontend tree store.
- Remove `evener/tree/changed`.
- Migrate `hubapi` consumers.
- Remove temporary capability fallback in the release after the migration
  release.
- Remove the `/api/tree` adapter in that release or the explicitly named
  breaking release required by published compatibility policy.

## Testing

Before changing tests, follow `docs/developing-evener/testing.md`.

### Server unit tests

Cover:

- revision advancement and no-op stability;
- producer content fingerprints and no-op writes;
- exact and wildcard invalidation scopes;
- the per-resource dependency matrix and unrelated-source changes;
- generation changes;
- immutable snapshot publication;
- one snapshot and project-resolution pass per semantic revision;
- concurrent cache-miss single-flight;
- invalidation during build and compare-before-publish retry;
- object, encoded, and compressed cache hits;
- LRU entry and byte bounds;
- ETag, `If-None-Match`, 304, gzip, and required headers;
- canonical resource identity, distinct page ETags, pagination validation, and
  hard row/depth/node caps;
- two WebServer instances with isolated caches and auth scopes;
- malformed, noncanonical, absent, and unauthorized project/pin/session keys;
- access-log redaction;
- exact time-boundary invalidation;
- last-good behavior after store, build, encoding, and compression failures; and
- concurrent readers during invalidation and eviction.

### Frontend unit tests

Cover:

- initial manifest load and expansion hydration;
- bounded section and catalog paging;
- one in-flight request per resource;
- target coalescing and one trailing request;
- abort and stale-response rejection;
- generation replacement;
- sequence-gap recovery;
- mutation and notification deduplication;
- exact target fan-out across loaded pages and `all_loaded_projects`;
- 304 handling;
- manifest and project stale errors;
- project 404 recovery;
- attention updates without navigation fetches;
- stale client detachment; and
- deep-link owner resolution without a complete project tree.

### Integration and browser tests

Use a scripted source at the AppWire boundary and real Evener code below it. Prove that:

- one status change emits scoped invalidation and updates exactly the affected
  section/project resources, plus the manifest only when a displayed total or
  descriptor count changed;
- an unrelated collapsed project causes no request;
- a mutation response and its AppWire event cause one revalidation;
- reconnect and an injected sequence gap recover through conditional HTTP;
- saved and default expansions hydrate correctly;
- Live, Needs You, pins, projects, test runs, archived projects, pagination, titles, menus, and deep links preserve current behavior; and
- failures remain isolated and retriable.

Run the frontend formatter on touched `src/` files, then run the repository's required frontend, Go, lint, vet, and browser gates.

### Performance fixtures

Commit a fixed legacy baseline fixture with:

- 20 non-Git project directories;
- 50 top-level sessions per project and no children;
- IDs `session-%03d-%03d`;
- names formed from the ID, one space, and seven repetitions of
  `representative-title-data-`;
- a fixed UTC clock with sessions spaced one minute apart inside the Current
  window; and
- no live rows, remote sources, favorites, pins, archive decisions, or saved
  expansions.

The legacy benchmark warms `/api/tree` once, then runs 20 iterations per sample
and five samples through the real HTTP handler with no `Accept-Encoding`. It
records the median uncompressed response bytes and `B/op`. The probe that
motivated this design produced about 574 KB and 23 MiB per request, but the
checked-in fixed-clock baseline—not those conversational measurements—is the
acceptance reference.

Run the new path with the same fixture. **Mandatory hydration** means the
manifest, first Live and Needs You pages, all pin-descriptor pages required to
discover nonempty pins, all nonempty pin-section first pages, and the first
active-project catalog page. It excludes project resources because this fixture
has no default or saved expansions. Run mandatory hydration once with identity
encoding and once with gzip.

Run a second **expanded hydration** variant with the first four projects marked
as saved expansions. It includes those four root project resources and runs with
gzip. Record:

- manifest, section, pin-catalog, project-catalog, and project bytes before and
  after gzip;
- cache-hit and cache-miss duration;
- allocations;
- project-resolution and tree-build call counts; and
- request counts for boot, one change, one mutation, reconnect, and idle time.

The warm allocation comparison measures a gzip-accepting manifest request after
its object, JSON, and gzip caches are populated. Performance assertions use
stable structural counts and the checked-in relative budgets, not host-specific
absolute latency.

## Acceptance Criteria

On the observed dataset and the fixed deterministic fixture:

1. After initial hydration, an idle UI issues zero navigation HTTP requests unless AppWire reconnects or semantic navigation state changes.
2. One semantic change causes at most one request for each affected loaded
   representation: manifest, section page, catalog page, project root, or
   project tier page.
3. A mutation and its matching AppWire notification do not duplicate a resource request.
4. No more than one request per resource is in flight.
5. Adding or changing sessions in an existing collapsed project adds no session
   row to the manifest or catalogs. Any affected bounded project-summary scalar
   may change.
6. Fixed-fixture mandatory hydration uncompressed JSON bytes are no more than
   15% of the checked-in legacy uncompressed JSON-byte baseline. This proves the
   resource split independently of compression.
7. Fixed-fixture mandatory hydration gzip-transferred body bytes are no more
   than 10% of the checked-in legacy transferred-body baseline. Expanded
   hydration with four saved projects is no more than 35% of that legacy
   transferred-body baseline. Live diagnostics report mandatory,
   default-expanded, and saved-expanded totals separately against the observed
   429 KB request; arbitrary user-saved expansion is reported, not hidden inside
   the mandatory budget.
8. Matching validators return 304 with an empty body.
9. A representation-cache hit performs no metadata copy, remote snapshot clone, project resolution, decision-store query, tree build, projection, JSON encoding, or compression.
10. Across fixed-fixture mandatory or expanded hydration, one semantic revision
    performs exactly one core-tree build and one distinct-project resolution
    pass. Concurrent misses join that work. Cache hits perform zero additional
    builds or resolution passes.
11. The fixed-fixture warm manifest request uses no more than 20% of the
   checked-in legacy `B/op` baseline, an allocation reduction of at least 80%.
12. Deep links, Live, Needs You, pins, project tiers, pagination, titles, menus, mutations, reconnect, and stale-error behavior retain current semantics.
13. Logs and metrics expose no title, prompt, ref, or path values.
14. Every collection enforces its documented row, recursive-node, depth,
    string, offset, and byte bounds and keeps overflow explicit.
15. The standard lint, vet, test, frontend, and browser gates pass.

## Risks and Mitigations

### Too many small requests

Global sections, catalog pages, and persisted expansions could cause a request
burst after boot. Use the concurrency limit of four, skip empty sections, and
fetch project resources only for expanded projects. Measure aggregate
transferred bytes and request count, not manifest size alone.

### Incorrect change scoping

A missed project key could leave one resource stale. Producers emit
`all_loaded_projects` when uncertain. Event-sequence gap recovery and reconnect
revalidation provide independent repair paths.

### Cache memory growth

Encoded project pages can multiply by revision, page, and encoding. Keep only current-generation entries and enforce entry and byte bounds with observable eviction.

### Temporary dual behavior

A fallback can become permanent. The compatibility adapter must reuse `NavigationService`, and its deletion remains an explicit rollout phase and completion condition.

### New consistency machinery

Resource revisions add state. Keep all comparison and fetch scheduling inside one revalidator and all revision assignment inside one server service. Feature components render resource state but never implement their own refresh policy.
