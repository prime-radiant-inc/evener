# Session activity push updates (design)

**Status:** Proposed. Awaiting Jesse's approval and execution choice. No product
code is changed by this document.

**Goal:** While the Activity sidebar (or the native subagent tree) is open, a
delegate field update must cost no delegates-collection read. The pushed
`evener/delegate/updated` frame already carries the row's mutable fields and the
raw report packet (`packetKind`/`message`); it lacks only the *bounded*
`reportPreview` the read projects, so this spec adds that projection (instead of
duplicating the server's 4096-code-point rule in each client) and stops
re-reading the collection for a field change.

**Status note:** this revision folds in the findings of the adversarial reviews of
commits 6342f22da8, a784dd40f7, and 99504f04ff.

**Related:** [session activity product guide](../product/session-activity.md),
[API design](session-activity-api.md),
[client](../../appwire-client/typescript/sessionActivityStore.ts),
[delegate projection](../../internal/appprojector/appwire_projection.go),
[read projection](../../agent/session_activity_delegates.go).

## Problem and evidence

The Agents tab re-reads the session summary, the whole displayed delegates page,
and the subtree summary on every `evener/thread/activity/changed
{resources:[summary,delegates]}` push, which the daemon emits once per delegate
update. On a live session (73 delegates, 48-row first page) that measured about
once per second and was visible as the "Load more subagents" paging control
relabelling to "Loading subagents…". The label half is fixed
(`ActivityPageBoundary` now reports only a page it started). The read half is
not removable today because the pushed frame omits `reportPreview`, so a settled
run's report only reaches the row through a collection read. Proof: removing the
read broke the native suite's settled-report, stop, and list-shrink tests.

## Constraints

- The activity reads stay the authority for collection membership and counts. A
  push may update a row in place; it may not invent one.
- Counts are never reconstructed from loaded rows; the summary read stays.
- Every bounded field matches the read's exactly (same prose-bound helper, same
  ellipsis, same truncation flag) — the report preview and `task`, `description`,
  `reason`, `error`, `notResumableReason`, `model` — or the push and read drift.
- Additive wire change only: new fields are `omitempty`; no existing field
  changes meaning, and no new read method is introduced.
- Web, native, and the shared store migrate together; the phone/web visible
  contracts are unchanged.
- Keep the existing recovery owners: observe, paging, resync, reconnect, stale
  cursor, source replacement.

## Wire contract

`appwire.EvenerDelegateInfo` (`appwire/types.go`, shared by
`evener/delegate/updated` and thread diagnostics) gains two fields, with the
exact semantics of `appwire.SessionDelegate`:

```go
// ReportPreview is the settled current run's reported text, capped at 4096
// Unicode code points including an ellipsis when truncated.
ReportPreview          string `json:"reportPreview,omitempty"`
ReportPreviewTruncated bool   `json:"reportPreviewTruncated,omitempty"`
```

Absent while the run is open, for an error-only or unreported outcome, and for a
prior generation once a new run has started. `name` stays read-only: it is
immutable per delegate and already on the loaded row.

`appwire.EvenerDelegateInfo` is also the thread/read roster row, which omits
report payloads by contract. `SlimDelegateForRoster` (`appwire/delegate_roster.go`)
must clear `ReportPreview` and `ReportPreviewTruncated` alongside
`PacketKind`/`Message`/`StructuredResult`, and a roster test must assert the
fields never appear there.

The activity row `appwire.SessionDelegate` gains `ProjectionRevision uint64` (JSON
`projectionRevision`) so a collection read can seed the merge ordering (see
Consumers); the read projection must actually fill it from the same aggregate
revision the frame carries. It is an ordering key, not display data.

`appwire.EvenerDelegateInfo` gains the delegate's **logical owner** identity — a
session id (`LogicalOwnerSessionID`) — the value the activity read lists a
session's own delegates by. This is required, not optional: a delegate's
`ownerSessionId` is always the physical root
(`agent/delegate_tree_start.go`), while the read's logical owner is the parent
delegate's `childSessionId` (else `ownerSessionId`,
`sessionActivityDelegateOwner`). Without it a `session`-scoped store cannot tell
its own children from a descendant's.

The capability is authored by the response producer, not derived by the hub. A
daemon that emits the frame sets `reportPreview: true` on the
`SessionActivityContext` it returns, and the local agent projection that loads a
retained session sets it too. The hub passes the field through unchanged: it
must not resolve capability from a handshake, because the client-facing
connection `FeatureSet` is the hub's own hardcoded constant
(`cmd/evener-hub/app_rpc.go`), a remote "source" is itself a hub whose set is its
own, and a local daemon's features are not read by the hub. An older producer
omits the field and the client keeps the read (see Compatibility). Carry it as a
field on `SessionActivityContext` (a per-response capability), not on
`appwire.FeatureSet`.

Regenerate the TypeScript (`appwire-client/typescript/types.gen.ts`) and the
protocol catalog (`docs/appwire-protocol.md`) with `make generate`.

## Producer

**Projection.** `appwireDelegateInfo(data events.DelegateUpdatedData)` fills the
two fields from `data.PacketKind`/`data.Message` when the snapshot is settled
with a reported packet, and applies the same trim, byte bound
(`activityMaxReportPreviewBytes`) and proof-of-truncation rule as
`agent/session_activity_delegates.go`. Put that bounding in a **leaf** package
both `agent` and `internal/appprojector` can import (the precedent is
`agent/sandbox`): the projector cannot import package `agent`, because package
`agent`'s own tests import the projector and `make test` would fail with an
import cycle. Bound the frame's other prose fields the same way the read does —
`task`, `description`, `reason`, `error`, `notResumableReason`, `model`, and the
`worktree` strings — so a merge can never widen a row the read bounds.
`appwireDelegateInfo` sets `LogicalOwnerSessionID` from
`data.AncestorSessionIDs[0]` (nearest ancestor) when present, else
`data.OwnerSessionID` — the same result as `sessionActivityDelegateOwner`, and
derivable from the event without the durable tree (the runtime already sets the
ancestry). `mergeAppwireDelegateInfo` orders by `projectionRevision` then
`latestActivityAt` like every other field.

**Invalidation.** Unchanged. The daemon keeps emitting
`evener/thread/activity/changed {summary, delegates}` for every delegate change —
an un-upgraded client still needs it — and the upgraded client simply does not
act on `delegates`. The `summary` invalidation also stays on every delegate
change for now: the page read is the cost worth removing, while narrowing
`summary` to count-moving changes needs daemon-side count deltas and risks
under-invalidating a count. Revisit only against a measured win.

## Consumers

**Shared store** (`SessionActivityStore`, used by web and native):

1. Handle `evener/delegate/updated` (existing `ref` filter): merge into
   `state.delegates.rows` by `delegateId`. Scope by the frame's
   `logicalOwnerSessionId`, never `ownerSessionId` (always the physical root): a
   `session` store accepts only `logicalOwnerSessionId === context.sessionId`,
   and `subtree` accepts the thread's own subtree. Keep `ownerRef`, `rootRef`,
   `childRef`, and `name` from the loaded row; take the value fields from the
   frame, bounded by the same helper as the read. A frame for an unknown delegate
   never adds a row — step 3 reads instead — so a push updates in place and never
   invents one.
2. Join contributions exactly as `mergeAppwireDelegateInfo` does — the snapshot
   fields come from the contribution with the strictly greater
   `projectionRevision` (a lower one never replaces them), while
   `latestActivityAt` is the **maximum** of the two, independent of revision (a
   lower-revision contribution may advance it; a higher-revision one must not
   move it backward). A single "keep the newer contribution" order is wrong: it
   loses that crossed case and can regress activity time. Hold each delegate's
   applied `projectionRevision`/`latestActivityAt` in a per-delegate map; a
   collection read joins the same way, so a read landing after a newer frame
   cannot clobber it and a frame in the gap is not skipped. Clear the prior run's
   preview when `runGeneration` increases.
3. If step 1 accepted the frame (scope matched) and it names a delegate the
   loaded rows do not contain, and the collection is observed, request a root
   read at most once per delegate ID (a seen-unknown set). A frame the scope
   check dropped (a descendant-owned delegate on a `session` store) never reads.
   A root read reconciles only to the displayed boundary, so a delegate beyond
   the loaded extent can never be adopted by that read; without the per-ID bound,
   every later frame for it would re-read. A genuinely new delegate sorts into
   the first page the read returns and is adopted. Clear the seen-unknown set on
   session replacement and dispose so it cannot grow for the store's lifetime.
   Keep the latest frame per unknown delegate id (bounded the same way) and apply
   it once a read admits the row, so a settle that arrives while a later-page
   read is in flight is not dropped: the admitted row takes the buffered frame by
   the same join, and an older page cannot leave a running row behind.
4. On `evener/thread/activity/changed`, refresh `summary`, `jobs`, and `watches`
   as today, but do not refresh `delegates`. `evener/thread/resync` still
   refreshes every observed resource.
5. Publish nothing when a merge changes no mapped field.
6. Treat a response whose context `epoch` differs from the loaded one as a source
   replacement: clear the applied map and the seen-unknown set — their authority
   is epoch-scoped, because a replacement journal reconstructs
   `projectionRevision` from 1, so an old epoch's higher revision would otherwise
   win forever — record the new epoch, and read the collection afresh. Extending
   the existing epoch comparison (`sessionActivityStore.ts:428`, non-root and only
   when `read.epoch` is set, assigned at `:473`) to a root read must skip while
   the stored epoch is unset — otherwise the first read of every store mismatches
   `undefined !== epoch` and loops — and record the new epoch before restarting.
   The invalidation carries no epoch, so a response is the only place to see it.

**Web.** The Agents tab, the Activity sheet, the transcript entity view, the
status bar, and the session chrome consume the store unchanged; the merge makes
rows update without a re-read. `ActivityPageBoundary` already presents nothing
for a background refresh.

**Native.** The subagent tree consumes the same store rows; its report text now
arrives on the frame. Its update and stop tests emit the pushed frame instead of
a bare invalidation. The **list-shrink** case cannot: a removal has no frame
(that is the membership ruling), so that test must trigger a still-reading owner
instead — `evener/thread/resync`, a reconnect, or a re-observe. The test
fixture's compile-time exhaustiveness guard
(`mobile-native/src/subagents/sessionActivityTestUtils.ts`) must be extended for
the new row field, or `make test-native`/`test-web` typecheck fails.

**TUI.** `cmd/evener-tui/hub_notifications.go` already consumes
`evener/delegate/updated`; it gains the preview field with no contract change.

## Membership

A loaded row can change two ways: its fields (an in-place patch) or its
membership (the row appears or disappears). The pushed frame covers fields and
creates; it cannot cover a removal, because no "this delegate went away" signal
exists. Once the client stops reading `delegates` on an invalidation, the only
thing that deletes a loaded row is a read — so a removal the client cannot
observe would leave a stale row until the next recovery read. Whether that is
possible is the pivot of this design.

Evidence that it is not: the durable delegate journal is append-only. Its nine
event kinds (`agent/internal/delegatestore/event.go`) are create, run-start,
terminal-prepared, run-finished, resumability-closed, subtree-stop requested and
completed, delivery-acknowledged, and attention-changed — none removes a
delegate. The activity keys derive from every non-nil stored row
(`deriveSessionActivityDelegateKeys`). `captureDelegateCandidates` leaves a row
out for reasons that are not removals: out-of-scope ownership (never listed for
this scope), the walk's creation-admission high-water mark (a delegate created
after an incremental walk's boundary appears on the next fresh root read), and
the response byte budget (which reports `complete:false`). Only a fresh root read
establishes the full current set; retirement deletes a child's artifacts
directory, not its durable record (a retained delegate still lists with no live
runtime). Within one source epoch, a fresh root read's set therefore only grows.

It shrinks only on a source replacement (a new epoch) or a session replacement.
Note: `acceptContext` resets the store only when the resolved `sessionId`
changes, not on an epoch change, so an epoch change is *not* a store reset. The
client must treat a changed epoch on any response as a recovery signal and read
the collection afresh (the store's existing stale-epoch restart already does this
for paged rows). Journal recovery truncates an uncommitted trailing batch, never
a served row.

Decision (ruled by Jesse, 2026-10-09): take the append-only model. Delegates
never get rehomed; they only end. Creates are caught because the new delegate's
frame names an ID the loaded rows do not hold, so the store reads once for it
(bounded by the per-ID seen-unknown set); removals do not occur. Pin the
invariant with a behavioral test over the public read: a delegate created and
driven through run/stop/resume/terminal never leaves the listed set within an
epoch, and the client reconciles absence only through a read. If a removal path
is ever added, the producer signals membership (a `delegates` invalidation the
client still reads) before the client rule changes; the client then needs no new
removal rule.

## Compatibility

The gate keeps an un-upgraded source working, and it is authored by the
producer. The capability rides on `SessionActivityContext`, set by the daemon
that produced the rows (or the local agent projection for a retained session) —
never resolved by the hub: the client-facing connection `FeatureSet` is the hub's
own constant (`cmd/evener-hub/app_rpc.go`), a remote "source" is itself a hub
whose set is its own, and a local daemon's features are not read by the hub. The
store enables the no-read merge only for a context that advertises
`reportPreview` **and** whose `availability` is `live`; otherwise it keeps
today's behavior — read `delegates` on the invalidation — so reports still land.
The `live` requirement matters: invalidations reach every ancestor target, but a
delegate frame is emitted only through live ancestor runtimes
(`delegate_update_routing_test.go` pins that a released ancestor gets none), so a
`retained` context would otherwise suppress the read and never see the frames.
Prefer extending frame routing to subscribed retained targets; until then, gate
on `availability`. A negative-path test must pin the fallback.

## Test and acceptance obligations

- **Parity.** For the same settled generation, the `delegate/updated` preview
  equals the delegates-list `reportPreview` (including the 4096 cap, the
  ellipsis case, and a resumed run).
- **Merge.** A field update patches a loaded row and causes zero
  `evener/thread/delegates/list` reads; a repeated frame publishes nothing; a
  stale `projectionRevision` cannot regress a row; a resume clears the prior
  report; a `session` store ignores another owner's delegate.
- **Membership.** A field update for a delegate *beyond* the loaded extent
  triggers at most one read, not one per frame; an unknown delegate in an
  authoritative complete collection triggers one read and is adopted; a
  `delegates` invalidation alone triggers none; resync, reconnect, observe and
  paging still read; an epoch change forces a read; and a behavioral test drives
  a delegate through run/stop/resume/terminal and asserts it never leaves the
  listed set and the client reconciles absence only through a read.
- **Logical owner.** A `session`-scoped store at a non-root subagent accepts its
  own direct children's frames and ignores a deeper descendant's; a root session
  store ignores a descendant-owned frame (no read for it). This is the case the
  current `ownerSessionId` check gets wrong.
- **Read revision.** The served `SessionDelegate.ProjectionRevision` is populated
  and non-zero, so a read seeds the merge order.
- **Helper home.** The shared prose-bound helper lives in a leaf package both
  `agent` and `internal/appprojector` import; `make test` compiles with no import
  cycle.
- **Ordering.** Mirrors `mergeAppwireDelegateInfo`: snapshot fields follow the
  greater revision, `latestActivityAt` is the independent maximum, in both
  directions (a lower-revision later-activity frame advances only the timestamp;
  a higher-revision frame does not move it backward); a read joins the same way
  and cannot clobber a newer frame or skip one in the gap.
- **Epoch replacement.** An epoch change clears the applied map and seen-unknown
  set, and a replacement source returning the same delegate id with a *lower*
  revision and a different status/report wins over the old entries.
- **Epoch guard.** Extending the epoch comparison to root reads neither loops on
  the first read nor misses a real epoch change, and records the new epoch.
- **Native shrink.** The list-shrink case recovers through resync, reconnect, or
  a re-observe rather than a frame; the native fixture's exhaustiveness guard
  covers the new row field and typechecks.
- **Roster.** `SlimDelegateForRoster` strips the preview fields; a thread/read
  roster row never carries them.
- **Capability.** A live context advertising `reportPreview` merges without
  reading the collection on an invalidation; a context that does not advertise
  it, or whose `availability` is `retained`, keeps reading, so the report still
  lands.
- **Retained delivery.** A real subtree stop with an observed *retained* ancestor
  shows its descendants reaching stopping through the read path.
- **Later-page race.** A settled frame for an unloaded delegate that arrives
  during a later-page read is applied once the row is admitted; the older page
  does not leave a running row.
- **Counts.** A count-moving update still refreshes the summary; the Agents
  count never derives from loaded rows.
- **Web.** Rows update from a frame with no collection read; the boundary test
  stays green.
- **Native.** The five migrated tests pass; the full native suite is green.
- **Live evidence.** With the Agents tab open on a session with a running
  delegate, a delegate field update produces no `evener/thread/delegates/list`
  request, and a settle shows the report from the frame.
- Gates: `make lint`, `make vet`, `make test`, and the native gate.
- Regenerate `agent/testdata/subagentwire` (`make fuzz-goldens`) if the recorded
  producer frames change.

## Risks

| Risk | Guard |
| --- | --- |
| Push/read preview drift | Shared bounding helper; parity test. |
| Wrong scope filter | scope by the frame's logical owner, never `ownerSessionId` (always the root). |
| Shared helper import cycle | leaf package importable by both `agent` and `internal/appprojector`. |
| Root-epoch guard loops | skip while the stored epoch is unset; record the new epoch before restart. |
| Native fixture typecheck | extend the exhaustiveness guard for the new row field. |
| Read clobbers a newer frame | each read row joins the order; keep the newer of read and applied. |
| Stale row from an out-of-order frame | `mergeAppwireDelegateInfo`'s join: greater revision for fields, independent maximum for activity time. |
| Epoch replacement outlives the old map | clear the applied map and seen-unknown set on an epoch change. |
| Later-page frame loops | the per-ID seen-unknown set bounds it to one read per ID; step 1's scope check gates it. |
| Report preview in the roster | `SlimDelegateForRoster` clears it; roster test. |
| Missed removal | Append-only invariant pinned by a behavioral test; a future removal path must signal membership. |
| Old source with no preview | producer-authored context capability; the read stays unless the source advertises. |
| Retained ancestor: invalidations but no frames | gate the no-read on a live context, or extend frame routing to retained targets. |
| Later-page frame dropped after its one read | buffer the latest unknown-id frame until a read admits the row. |
| Removed read hides a source error | Recovery paths (resync, reconnect, stale cursor, epoch change on any response) unchanged; the collection still reads on observe and paging. |

## Open questions for Jesse

All resolved 2026-10-09.

1. **Membership:** ruled — take the append-only model (delegates never get
   rehomed; they only end).
2. **Scope:** decided — web + native + shared store together.
3. **Summary invalidation:** decided — keep it on every delegate change; narrow
   only against a measured win.

## File map

| Unit | Files |
| --- | --- |
| Wire + generated | `appwire/types.go`, `appwire/session_activity.go`, `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md` |
| Roster slimming | `appwire/delegate_roster.go`, `server/appwire_runtime.go` |
| Capability plumbing | `SessionActivityContext` field, set by the producing daemon (`agent/session_activity*.go`) and the retained loader; the hub passes it through (`cmd/evener-hub/app_session_activity.go`, `internal/appsource`) |
| Producer | `internal/appprojector/appwire_projection.go`, `agent/session_activity_delegates.go`, and one leaf package for the shared prose-bound helper |
| Shared store | `appwire-client/typescript/sessionActivityStore.ts`, `.test.ts` |
| Web | `cmd/evener-hub/frontend/src/shell/activitybar/ActivityPageBoundary.tsx` (done), `activityApi.test.tsx` |
| Native | `mobile-native/src/subagentRows.test.tsx`, `src/subagents/SubagentsScreen.test.tsx`, `src/ConversationScreen.send.test.tsx`, `src/subagents/sessionActivityTestUtils.ts` (exhaustiveness guard) |
| Docs | `docs/product/session-activity.md`, `docs/design/session-activity-api.md`, `docs/product/subsystems.md` if ownership moves |
