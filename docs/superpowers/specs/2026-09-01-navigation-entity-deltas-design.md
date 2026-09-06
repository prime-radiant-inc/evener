# Navigation Entity Deltas

## Status

Approved for implementation as the next evolution of the existing scoped
AppWire navigation read service. The September 2 redesign removes
per-entity/per-container numeric revisions; resource generation, revision,
ETag, and invalidation sequence remain authoritative. This specification is
independent of PR #757.

## Evidence and problem

The traffic investigation found no repeated delegate-status publication and no
duplicate navigation waves. The observed UI storm came from a coarser boundary:
a legitimate project/current change invalidated a whole recursive navigation
resource, the hub resent that resource, and the browser replaced and
rematerialized thousands of mostly unchanged child rows.

Current behavior is internally coherent but too coarse:

- each logical navigation resource has one revision/fingerprint over its whole
  recursive body;
- a changed descendant changes the project resource fingerprint;
- a non-matching ETag returns the full recursive resource;
- the frontend clones/freezes and replaces every changed response body;
- Rail adapters and tree-node builders recursively allocate new objects;
- stable React keys preserve component continuity but do not prevent selectors,
  row functions, and visible rows from rerunning.

The fix belongs at the end-to-end data boundary, not in delegate publication or
invalidation fan-out.

## Goals

- Add a capability-gated v2 representation to the existing
  `evener/navigation/read` method.
- Represent each bounded resource as resource-scoped keyed entities plus keyed
  ordering/parent containers.
- Return a full normalized snapshot for initial/unknown/evicted/mismatched
  bases, `not_modified` for the exact current base, and one cumulative delta
  from a retained exact older base to current.
- Preserve exact generation, revision, ETag, invalidation sequence, resource
  selector, paging, truncation, and browser fan-out semantics.
- Keep normalized records stateless: entity/container identity and semantic
  content determine keyed changes; records carry no independent revision
  counter.
- Express deletion, reordering, and reparenting without ambiguous index-edit
  transforms.
- Apply snapshots and deltas with structural sharing so unchanged entity and
  container objects preserve `===` identity.
- Memoize navigation selectors, Rail adapters/nodes, and row content so an
  unchanged visible row does not rerender when a sibling changes.
- Make reconnect consume the latest initialize result and correctly fence a hub
  generation change.
- Bound server history and always fall back to a complete snapshot after
  eviction.

## Non-goals

- Do not change `BroadcastAll` invalidation fan-out. Every subscribed browser
  still receives the same committed invalidation.
- Do not add per-browser server state or acknowledgements.
- Do not send index/move edit scripts or require clients to transform concurrent
  order edits.
- Do not globalize entity identity across resource/page representations. The
  same ref may have different truncation or `omitted_descendants` fields.
- Do not remove v1 in the first delivery.
- Do not redesign navigation projection limits, authentication, WebSocket
  transport, or unrelated thread/delegate protocols.
- Do not make default tests depend on network, provider credentials, ambient
  time, or model behavior.

## Version negotiation

Keep the existing navigation capability envelope at version 1 so deployed v1
clients that explicitly reject any other value continue to work. Advertise the
new normalized read representation separately:

```go
type NavigationCapability struct {
    Version      int    `json:"version"` // remains 1
    GenerationID string `json:"generationId"`
    Sequence     uint64 `json:"sequence"`
    ReadVersions []int  `json:"readVersions,omitempty"` // server advertises [1, 2]
}
```

The method remains `evener/navigation/read`; a request opts into normalized v2
with an explicit representation version, so v1 clients cannot accidentally
decode a new shape.

```go
type NavigationReadBase struct {
    GenerationID string `json:"generationId"`
    Revision     uint64 `json:"revision"`
    ETag         string `json:"etag"`
}

type NavigationReadParams struct {
    // Existing resource selector fields remain unchanged.
    RepresentationVersion uint8               `json:"representationVersion,omitempty"`
    Base                  *NavigationReadBase `json:"base,omitempty"`
}
```

Rules:

- omitted/zero or `1` selects the existing recursive v1 representation and
  existing ETag behavior;
- `2` selects normalized v2 and is sent only after the initialize capability
  advertises `readVersions: [1, 2]`;
- `base` is invalid for v1;
- v2 accepts no legacy `etag` field; its validator is inside `base`;
- unsupported versions and structurally incomplete bases are invalid params.

## Response envelope

```go
type NavigationRepresentation string

const (
    NavigationRepresentationSnapshot NavigationRepresentation = "snapshot"
    NavigationRepresentationDelta    NavigationRepresentation = "delta"
)

type NavigationReadResponse struct {
    Status         string                   `json:"status"`
    Representation NavigationRepresentation `json:"representation,omitempty"`
    GenerationID   string                   `json:"generationId"`
    Revision       uint64                   `json:"revision"`
    ETag           string                   `json:"etag"`
    Base           *NavigationReadBase      `json:"base,omitempty"`
    Data           json.RawMessage          `json:"data,omitempty"`
}
```

V2 status rules:

- `not_modified`: exact current base; omit representation, base, and data;
- `ok` + `snapshot`: no base or unusable base; data is a complete normalized
  snapshot;
- `ok` + `delta`: exact older base is retained; echo that exact base and return
  one cumulative delta to the response revision;
- `gone`: the requested whole resource is absent at its current tombstone
  revision; omit data and treat the revision as converged.

Every successful response identifies the authoritative current generation,
resource revision, and ETag. A delta is accepted only when all echoed base
fields exactly equal the client's installed base.

## Normalized representation

Entity keys are scoped to the complete resource key, including resource kind,
project, tier, page offset, and page limit. Wire keys may contain a kind prefix
but must not imply global cross-resource identity.

```go
type NavigationEntityRecord struct {
    Key   string          `json:"key"`
    Kind  string          `json:"kind"`
    Value json.RawMessage `json:"value"`
}

type NavigationContainerOwner struct {
    Kind      string `json:"kind"` // resource_root or entity
    Slot      string `json:"slot,omitempty"`
    EntityKey string `json:"entityKey,omitempty"`
}

type NavigationOrderContainer struct {
    Key      string                   `json:"key"`
    Owner    NavigationContainerOwner `json:"owner"`
    Children []string                 `json:"children"`
}

type NavigationSnapshot struct {
    Metadata   json.RawMessage            `json:"metadata"`
    Entities   []NavigationEntityRecord   `json:"entities"`
    Containers []NavigationOrderContainer `json:"containers"`
}

type NavigationDelta struct {
    Metadata                json.RawMessage            `json:"metadata,omitempty"`
    UpsertedEntities        []NavigationEntityRecord   `json:"upsertedEntities"`
    RemovedEntityKeys       []string                   `json:"removedEntityKeys"`
    UpsertedContainers      []NavigationOrderContainer `json:"upsertedContainers"`
    RemovedContainerKeys    []string                   `json:"removedContainerKeys"`
}
```

Arrays are non-null, including empty delta arrays. Session entity values are
shallow and contain no recursive `children`. Embedded job arrays may stay in
the owning session entity in this slice. Project, pin-section, and other
resource-specific metadata remain complete resource metadata.

A container's `children` array is the complete authoritative order for that
container. Reordering updates one container. Reparenting updates both old and
new parent containers atomically in one resource revision. Removal from a
resource uses removed keys plus changed containers; it does not assert global
entity deletion.

## Projection and semantic changes

The existing pure bounded projectors remain authoritative for fields, limits,
truncation, and order. Add a deterministic normalization pass after bounded
projection. Normalization is a pure function of the complete resource View and
the bounded projected value; it does not require a prior snapshot.

Keyed change rules are content-based:

- a new entity key is upserted;
- an existing entity key is upserted only when its kind or semantic value
  changes;
- an existing container key is upserted only when its owner or complete ordered
  child list changes;
- a removed key disappears from the current snapshot and is named in deltas.

Canonical comparisons use typed values or deterministic JSON generated by the
existing serializer; map iteration order must not create false changes.

The logical resource revision and ETag remain the authority for publication and
conditional reads. Exact resource bases select immutable history; semantic
record comparison determines the delta and frontend object reuse. Entity and
container records carry no numeric revision and are never independent
authority.

## Delta history and fallback

Retain immutable normalized snapshots under the existing bounded navigation
representation cache policy: at most 256 entries or 64 MiB estimated object +
JSON bytes, whichever is reached first. History is server-global per
`NavigationService`, never per browser.

When an exact base `(resource key, generation, revision, ETag)` is retained,
diff that immutable snapshot against current and return one cumulative delta.
Intermediate revisions do not need delivery. If the base is from another
resource/page/generation, is in the future, has an ETag mismatch, or was
evicted, return the current full snapshot. Never approximate a delta from a
nearby revision.

A snapshot fallback is normal convergence, not an error. It must still pass
through the frontend structural-sharing merge so equal keys retain object
identity. The fallback replaces authority only for the requested complete View;
it does not clear, reload, or borrow a baseline from another View.

## Read and invalidation rules

1. Initial v2 read without base returns a snapshot.
2. Exact current base returns `not_modified`.
3. Exact retained older base returns cumulative delta.
4. Unusable or evicted base returns snapshot.
5. A below-target response is not published; the existing one-trailing-read
   behavior reruns from the still-installed base.
6. A late response whose request/base/force token no longer matches is
   discarded.
7. A sequence gap revalidates every loaded resource from its exact installed
   base.
8. `all_loaded_projects` and existing precise target mapping remain unchanged.
9. `BroadcastAll` remains FIFO and exactly once per committed mutation.
10. Project/page bases use the complete `ResourceKey`; project A's base is never
    sent for project B.

## Whole-resource deletion

The service already retains absent resources as tombstone states. V2 returns
`status: "gone"` with current generation, tombstone revision, and ETag. The
client clears the normalized graph for that resource and records the tombstone
base as converged. A later reappearance has a later resource revision and
returns a complete snapshot or retained delta as available. `gone` is a
resource-presence state, not an entity tombstone revision: reappearance is
determined by current presence and validated graph content, never by reviving an
old entity/container record as authority.

This replaces v1's last-good-on-unavailable behavior only for negotiated v2.

## Frontend graph and structural sharing

Each loaded resource owns:

```ts
type NavigationGraph = {
  metadata: Readonly<Record<string, unknown>>;
  entities: ReadonlyMap<string, NavigationEntityRecord>;
  containers: ReadonlyMap<string, NavigationOrderContainer>;
};
```

Before publication, snapshot/delta application validates atomically:

- keys are unique;
- every child key exists;
- each represented child has one parent within the represented graph;
- container owners exist and slots are valid;
- the graph is acyclic;
- all required roots and metadata for the selected resource are present;
- removed keys are not still referenced.

Apply by copying only maps that contain changed keys. Reuse the exact frozen
entity/container object when its kind/value or owner/ordered children are
semantically equal. Apply full snapshots through the same keyed merge rather
than clearing and replacing the maps; generation authority metadata is stored
separately so equal entity values may retain identity across a generation
reset.

Navigation request/loading/error metadata must not force graph consumers to
receive a new graph object. Expose memoized resource graph selectors.

## Rail and Tree rendering

Replace recursive full-resource adaptation with selectors keyed by entity and
container identity. Memoize:

- project/session summary adapters;
- child-container to Rail-node arrays;
- archived/project node builders;
- row content at a stable scalar-prop boundary.

`RailRow` receives a stable node plus explicit visible scalar fields. Stable
`node.id` is not sufficient by itself. Loading/error-only state changes must not
rematerialize navigation nodes. A sibling entity update must preserve every
unchanged entity, node, and row object's identity and must not invoke the
unchanged row's render spy.

## Reconnect correctness

The production Appwire client currently keeps the first `connect()` promise,
while reconnect performs a new initialize handshake. Navigation's `onReady`
callback can therefore call `connect()` and receive stale generation/sequence
capability data.

Change the ready callback contract to carry the initialize result for the exact
ready transition:

```ts
onReady(cb: (initialize: InitializeResponse) => void): () => void
```

The client stores the successful result before publishing `ready`, and every
ready handler receives that result. Navigation consumes the callback argument
and does not call `connect()` from `onReady`.

Reconnect rules:

- same generation and equal sequence: no broad reload;
- same generation and higher sequence: force loaded resources from their bases;
- same generation and lower sequence: protocol error, because sequence cannot
  move backward within a generation;
- different generation: clear authority bases, retain equal graph objects
  provisionally, and request snapshots for loaded resources.

Tests use the real protocol client reconnect harness in addition to store fakes.

## Migration

1. Fix the ready/initialize callback and cover generation-changing reconnect.
2. Add v2 wire types, capability, validation, and generated TypeScript while v1
   remains the default for old clients.
3. Add deterministic stateless normalization and content-based keyed diffs.
4. Retain bounded immutable snapshots and implement cumulative diff/fallback.
5. Add v2 handler responses, including `gone`.
6. Add frontend validation and structural-sharing graph application.
7. Switch the browser navigation store to request v2 and preserve its existing
   target/trailing-read state machine.
8. Memoize Rail/Tree derived identities and row rendering.
9. Keep v1 until explicit caller inventory and compatibility policy authorize
   removal in a separate cleanup.

## Security and privacy

- Resource/entity keys, ETags, and bases are untrusted input and receive the
  existing length and selector validation.
- Errors do not echo raw refs, paths, titles, prompts, keys, or response bodies.
- Metrics record representation kind, counts, bytes, hit/fallback reason, and
  resource revisions only; no content or identity values.
- Deltas do not cross authentication or `WebServer`/`NavigationService`
  boundaries.
- Cache eviction clears immutable history without leaking it to another server
  instance.

## Acceptance criteria

- One changed descendant returns one entity upsert and only the changed parent
  container(s), not an otherwise identical recursive project tree.
- V2 entity/container wire records omit numeric revision fields; strict
  decoders reject that obsolete record shape.
- Pure reorder changes only its complete ordering container.
- Reparent updates both parent containers atomically.
- Exact current base returns `not_modified`; retained old base returns cumulative
  delta; evicted/mismatched base returns a complete snapshot.
- Whole-resource deletion converges through `gone` without preserving stale
  rows.
- Invalid, incomplete, dangling, cyclic, or wrong-base deltas are rejected
  atomically and recover with a full snapshot.
- Unchanged entities retain `===` identity after delta and equal snapshot
  fallback, including generation reset.
- An unchanged visible Rail/Tree row does not rerender when a sibling changes.
- Sequence-gap, one-trailing-read, concurrent invalidation, project switching,
  and stale-response fencing remain correct.
- Reconnect consumes the newest initialize generation/sequence from the real
  client boundary.
- Publisher FIFO and `BroadcastAll` fan-out tests remain unchanged and green.
- Default tests are deterministic; generated-output checks, `make lint`,
  `make vet`, `make test`, `make test-web`, and Chrome-capable browser guards
  pass.
