# Session activity APIs

## Intent and status

This is the target contract for session activity. Implementation and verification
are tracked in the [implementation plan](../superpowers/plans/session-activity-api.md).
The [product guide](../product/README.md) describes verified behavior; this design
must not be cited as evidence that an unfinished interface is available.

A person can inspect the agents, shell jobs, and watches associated with the
session they are reading, move into a child, and return without reconstructing
context or repairing a connection. A program can do the same through typed
AppWire reads. The primary navigation rail lists sessions. Opening navigation
does not enumerate the work trees belonging to every session.

The useful activity sidebar, embedded Tasks tab, and focus behavior are retained.
The API contract is independent of those particular views. No permission,
approval, diagnostic-authority, provider, or runtime-restart changes are included.

## Ownership and architecture

The root delegate controller owns stable delegate identities and their parent
graph. Shell-job stores and managers own shell processes. Watch journals and
managers own registrations, deliveries, and endings. The read layer projects
those authorities; it does not introduce another lifecycle controller or a new
persistent activity database.

The read layer is implemented in the agent package. Daemon handlers resolve the
requested session, including descendants, and call it with the request context.
The hub routes local and remote references and serves retained local sessions
through the same projections. Navigation consumes small summaries. Clients read
the selected session through a shared AppWire state owner.

Receiver-owned watches belong to the receiver even when a descendant manager
physically observes their source. Delegate records retain their identity when
their child runtime is absent. Fork ancestry is distinct from delegate ancestry.
Reads must not resume sessions, start providers, repair journals by writing them,
or create work.

Three approaches were considered:

- Putting subagents back into the navigation tree would repair the immediate
  missing-data symptom but restore global traversal and couple clients to rail
  structure.
- Reusing the existing recursive `evener/jobs/list` response unchanged would
  preserve useful ownership logic, but retain whole-tree refresh cost, an opaque
  response type, and the missing watch collection.
- Separate, typed session reads reuse the domain projections while giving each
  collection an explicit scope, bounded pagination, and independent recovery.
  This is the selected approach.

## Public methods

All four methods are available through daemon and hub AppWire. They use the
existing source-qualified `ref` convention. A missing ref is invalid; it must
never silently select the daemon's root. `scope` is `session` or `subtree`, with
`session` as the default. `session` selects resources logically owned by the
addressed session; `subtree` additionally selects resources owned by its delegate
descendants. Neither includes fork originals or unrelated sessions.

| Method | Params | Response |
| --- | --- | --- |
| `evener/thread/activity/read` | `SessionActivityReadParams { ref, scope }` | `SessionActivitySummary` |
| `evener/thread/delegates/list` | `SessionActivityListParams { ref, scope, cursor?, limit? }` | `SessionDelegatesResponse` |
| `evener/thread/jobs/list` | `SessionActivityListParams` | `SessionJobsResponse` |
| `evener/thread/watches/list` | `SessionActivityListParams` | `SessionWatchesResponse` |

Use these Go names and their generated TypeScript equivalents. Scope is a named
string type `SessionActivityScope`, with `SessionActivityScopeSession` and
`SessionActivityScopeSubtree` constants. Reject unknown scopes, negative limits,
malformed cursors, and cursors belonging to another resource or scope through
structured AppWire errors. Default page limit is 50; maximum is 200. A larger
positive limit is clamped to 200.

Every response carries `context: SessionActivityContext`:

- `ref`: the canonical public routing reference for the addressed session;
- `sessionId`: the resolved session identity, distinct from a stable workspace
  routing alias;
- `rootRef`: the root controller's public session reference;
- `parentRef` and `delegateId`, when the addressed session is a delegate child;
- `ancestors: SessionActivityAncestor[]`, ordered root to immediate parent, each
  with `ref`, `sessionId`, optional `delegateId`, and `title`;
- `epoch`: an opaque identity for the current read-source incarnation; and
- `availability`: `live` or `retained`.

Context is returned without loading sibling collections. An unavailable source
returns the existing typed unavailable/transport error, not an empty retained
response. Remote refs are never satisfied from a coincidentally named local file.

`SessionActivitySummary` carries `context`, `scope`, `delegates`, `jobs`, and
`watches`. Each summary is `SessionActivityCounts { known, total, active, failed,
completed }`. Unknown totals use `known: false`; their numeric fields are not
claims. Counts describe the declared scope and retained data, never just the
downloaded pages. Watch `active` means armed. Summaries use cheap domain state or
an existing warm derived index; a cold summary does not recursively load journals
just to fill a badge. It may return unknown counts.

Each list response carries `context`, `scope`, `page: SessionActivityPage`, and
one typed array: `delegates`, `jobs`, or `watches`. Arrays are always present,
including known-empty arrays. The page has `nextCursor?`, `complete`, and
`issues: SessionActivityIssue[]`. An issue identifies `ref` and a stable `code`
for an unavailable branch or incomplete retained source. Empty, complete, and
issue-free means an authoritative empty result. An incomplete read never means
the session has no work.

`SessionDelegate` is a compact stable resource, with `delegateId`, `ownerRef`,
`rootRef`, `childRef`, `parentDelegateId?`, `description`, `task`, `type`,
`lifecycle`, `phase`, `status`, `outcome?`, `terminal`, `resumable`,
`notResumableReason?`, `model?`, `reasoningEffort?`, `runStartedAt?`, `runEndedAt?`,
`latestActivityAt?`, `usage?`, and `worktree?`. Use existing domain projections and
classifiers for these values. `childRef` remains addressable when retained child
history exists. A missing child runtime does not erase its delegate. No nested
`child` tree or activation-job identity is included. Optional usage and worktree
data are included only when already available without transcript or filesystem
walks. The existing delegate stop API remains the mutation authority.

Jobs reuse the typed `JobActivityJob` row, restricted to shell jobs. Their
`ownerRef` is the logical session owner. Job output stays behind its existing
bounded output API rather than being included in list pages.

`SessionWatch` carries `ownerRef`, `receiverRef`, `state` (`armed`, `ended`, or
`unknown`), and `watch: EvenerWatchInfo`. Preserve stable watch IDs, cadence,
delivery counts, and recorded end reasons. Use `unknown` when retained evidence
does not establish whether a registration is still armed. Recent history is a
bounded retained collection, not a promise of an unlimited durable audit log.

## Pagination, cost, and consistency

Collections use a stable order by creation instant and stable resource ID,
newest first. Cursors are opaque and bound to resolved session identity, epoch,
resource, scope, an initial creation-order high-water mark, and the last emitted
key. They contain no filesystem path supplied by the client. They are not list
offsets and are not invalidated by an ordinary status or output update.

Pagination is a live view over the initial membership high-water mark: fields
describe the state read for that page; newly created resources appear on a fresh
root read; resources genuinely removed from retained state can disappear. It is
not a transaction across jobs, watches, and delegates. Within an epoch, clients
merge by stable resource identity. They never infer recency by comparing opaque
epochs, and they never mix pages from different epochs or root refresh requests.

A list call emits at most 200 rows and 256 KiB of encoded response. Cap free-text
fields using the existing activity projection limits; do not omit an entire
resource because its task or description is large. If the byte limit reduces a
page, continuation starts after the last emitted item. Every retained row remains
reachable. Unknown or unavailable branches are represented explicitly.

Cold retained projection work must be cancelable and resumable. Reuse the
existing bounded journal scanners and incremental fold/index mechanisms. A call
may process at most 2,000 records/work units and 4 MiB of journal input before
yielding a continuation. Index-building progress may yield an empty incomplete
page; the cursor must advance. It must not publish a partially folded shell job
as a complete current outcome. Warm reads use the derived index and bounded row
projection. Cache loss or journal replacement produces a typed stale-cursor
result and transparent client restart, never a destructive journal repair.

The input byte budget counts newly read journal bytes. Decode a stored batch one
event at a time so a large batch cannot bypass the work budget by allocating all
of its events before paging them. A single event is an atomic decoding unit,
bounded by its store's existing record-size limit; cancellation is checked before
and after that decode. These are input and work bounds, not a hard wall-clock or
CPU-time guarantee. Large individual events may accumulate across input pages;
they are not rejected merely because they exceed one page's input budget.

In-memory controller membership inspection is allowed; recursive visits to all
descendant runtimes and unbounded journal reads are not a summary implementation.
Do not allocate or sort a full historical recursive activity tree to answer a
single collection page. Derived indexes are disposable and read-only with
respect to durable authorities.

Request cancellation reaches session resolution, disk scans, and projection.
No request performs provider calls or starts an ended session. Existing public
APIs keep their existing behavior; no compatibility fallback or dual-read path
is added to new consumers. The new, unmerged navigation `subagents` resource is
retired once its consumers move.

## Changes and client recovery

Use the existing thread subscription machinery. A shared owner acquires a
lightweight thread subscription (`thread/read`, `includeTurns: false`,
`subscribe: true`, without replacing another subscriber) when required, and
shares it with the connection's established thread ownership. Do not unsubscribe
another mounted transcript or replace its subscription to follow activity.

Add `evener/thread/activity/changed`, with `SessionActivityChangedParams { ref,
threadId, sessionId, resources }`; `threadId` preserves the existing subscription
routing identity, while `sessionId` identifies the resolved session. Resources
is a typed list of `summary`, `delegates`,
`jobs`, or `watches`. Feed it from real domain changes and the existing event
bridge. Watch registration, clearing, delivery, and ending must refresh the
receiver's view. An event is a scoped invalidation, not a second lifecycle fold.
Existing delegate/job events may also be consumed, with coalescing. Subscription
gaps and reconnects revalidate only observed session resources.

The shared TypeScript owner is `SessionActivityStore`, exported from
`@evener/appwire-client`. It is framework-free. Its public surface is:

```ts
new SessionActivityStore(client, ref, { scope?, clock? })
activity.getSnapshot()
activity.subscribe(listener)
activity.start()
activity.observe(resource)    // acquire collection demand; returns its release
activity.load(resource)       // delegates | jobs | watches
activity.loadMore(resource)
activity.refresh(resource?)  // omitted means all observed resources + summary
activity.dispose()
```

`start()` observes the summary; opening a collection uses `observe` and releases
that demand when it closes. `load` is a one-shot read; it does not retain demand.
Only observed resources acquire data and refresh. Snapshot state separates
loading, retained data, completeness, transient failure, and authoritative
unavailability. It exposes typed arrays and context rather than navigation rows.
Clock/timer dependencies are injectable for deterministic tests.

Coalesce concurrent requests per resource and keep at most one in flight for
that resource. Retain known data during recovery. Retry transient failures while
observed, with delays of 1, 2, 4, 8, 16, then 30 seconds, capped at 30 seconds
without an attempt limit. Connection-ready and explicit refresh can wake a
pending retry. Do not spin on a permanently missing resource or unsupported
method. Dispose cancels timers and ignores late results. Scope/client changes
cannot graft data from the former owner into the new session.

Automatically page on collection demand and near the visible list boundary;
clients never require a user to repair pagination or reconnect. An explicit
disclosure can reveal additional retained history, but is not an error recovery
control. Status counts are independent of page loading. A stale cursor restarts
that collection, retaining useful displayed rows until the new result is ready.

## Client integration and navigation

The browser status bar reads the selected session's summary and context. The
Agents, Jobs, and Watches tabs acquire their corresponding collection. They
render domain records and drill through the returned public references. Tasks
continue to use the existing task owner and unfold in the sidebar.

The native subagent tree uses the same session activity owner with subtree scope.
Its existing views may use a pure adapter from flat domain rows into their
rendering model; that adapter owns no networking, retries, or lifecycle state.
Preserve native shell-job detail/output behavior and delegate stop semantics.

Navigation stays roots-only. Remove the unmerged `subagents` resource, its global
fingerprints, and the new child-derived needs-you count. Remove job/watch detail
arrays from navigation once all consumers are migrated; retain small cached
summaries where useful. Existing human-attention policy is unchanged. Navigation
locations continue to answer rail placement, not the full activity hierarchy.

## Acceptance evidence

- A real scripted session with child and grandchild delegates appears through
  the public endpoints although navigation has no subagent rows.
- Parent, child, unrelated-session, stable-workspace, and remote references
  resolve to the correct logical owner; a forged cursor cannot cross scopes.
- Parent-owned watches physically held by children appear once on the receiver.
- A retained delegate with no child runtime remains visible; ended local reads
  do not start a daemon; an offline remote does not borrow local state.
- More than 200 rows, large prose, creation during paging, ordinary status
  changes, deletion, cache loss, and journal replacement preserve progress and
  explicit completeness without duplicate identities.
- A cold journal larger than a read budget progresses across canceled/retried
  calls; warm reads do not reread the complete journal. Summary reads do not
  enumerate historical collections.
- Disconnect, missed notification, stale cursor, pre-initialization mount, and
  scope switches retain useful state and recover without user repair controls.
- Browser and native use the same state owner. Status counts remain truthful
  before, during, and after collection paging. Breadcrumbs work without sibling
  pages. Browser Tasks stays embedded, and the layout guard exercises that view.

Use deterministic providers, clocks, stores, and transports at external
boundaries, with real Evener producers and routers below them. Update generated
contracts, client package surfaces, owning product documentation, and subsystem
ownership together. Do not present focused tests as live device qualification.
