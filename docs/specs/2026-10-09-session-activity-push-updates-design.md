# Session activity push updates (design)

**Status:** Proposed. Awaiting Jesse's approval and execution choice. No product
code is changed by this document.

**Goal:** While the Activity sidebar (or the native subagent tree) is open, a
delegate field update must cost no delegates-collection read. The pushed
`evener/delegate/updated` frame already carries the row's mutable fields; this
spec adds the one field it lacks (the settled report preview) and stops
re-reading the collection for a field change.

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
- The frame's bounded prose rules match the read's exactly (same code point cap,
  same ellipsis, same truncation flag), or the push and read drift.
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

`appwire.FeatureSet` gains `DelegateReportPreview bool
\`json:"delegateReportPreview,omitempty"\`` so a client can gate on a source that
does not produce the field (see Compatibility).

Regenerate the TypeScript (`appwire-client/typescript/types.gen.ts`) and the
protocol catalog (`docs/appwire-protocol.md`) with `make generate`.

## Producer

**Projection.** `appwireDelegateInfo(data events.DelegateUpdatedData)` fills the
two fields from `data.PacketKind`/`data.Message` when the snapshot is settled
with a reported packet, using the same trim, byte bound
(`activityMaxReportPreviewBytes`) and proof-of-truncation rule as
`agent/session_activity_delegates.go`. Extract that bounding into one shared,
exported helper in the agent package and call it from both the read projection
and the projector, so the two cannot drift. `mergeAppwireDelegateInfo` orders
the preview by `projectionRevision` like every other field.

**Invalidation.** `emitStableDelegateUpdate` (via `emitSessionActivityChanged`)
currently names `delegates` for every row update. It must name `delegates` only
when the listed membership changed (a delegate entered the owner's listed set,
and a removal if one exists — see Membership), and always name `summary` when a
count can move. A field-only update then pushes only `evener/delegate/updated`
(plus a `summary` invalidation), and the client updates the row without reading
the collection.

Optional follow-on (separate ruling): narrow `summary` to count-moving changes
only, so a pure `latestActivityAt` update invalidates nothing.

## Consumers

**Shared store** (`SessionActivityStore`, used by web and native):

1. Handle `evener/delegate/updated` (existing `ref` filter): merge into
   `state.delegates.rows` by `delegateId` when the scope matches —
   `session` accepts only `delegate.ownerSessionId === context.sessionId`,
   `subtree` accepts the thread's own subtree. Keep `ownerRef`, `rootRef`,
   `childRef`, and `name` from the loaded row; take every mutable field,
   including the reported preview, from the frame.
2. Order a merge by `runGeneration`, then `projectionRevision` (a per-delegate
   applied-revision map, cleared on session replacement and dispose). Skip a
   frame that is not newer. On a generation increase, clear the prior run's
   preview.
3. If the frame names a delegate the loaded rows do not contain and the
   collection is observed, request a root read (new membership).
4. On `evener/thread/activity/changed`, refresh `summary`, `jobs`, and `watches`
   as today, but do not refresh `delegates`. `evener/thread/resync` still
   refreshes every observed resource.
5. Publish nothing when a merge changes no mapped field.

**Web.** The Agents tab, the Activity sheet, the transcript entity view, the
status bar, and the session chrome consume the store unchanged; the merge makes
rows update without a re-read. `ActivityPageBoundary` already presents nothing
for a background refresh.

**Native.** The subagent tree consumes the same store rows; its report text now
arrives on the frame. Its tests emit the pushed frame instead of a bare
invalidation (five tests today).

**TUI.** `cmd/evener-tui/hub_notifications.go` already consumes
`evener/delegate/updated`; it gains the preview field with no contract change.

## Membership

The client stops reading `delegates` on an invalidation, so a row can only
disappear through a read. The durable delegate read model appears append-only
(no removal event or delete path; `deriveSessionActivityDelegateKeys` derives
from every non-nil stored row), so within an epoch the listed set only grows,
and a source replacement or session change already restarts the walk through the
stale-cursor/epoch path. The first implementation task must **prove** this with
a journal/store test — a delegate record survives every lifecycle transition,
and a page's set is monotonic within an epoch. If any removal path exists, the
producer must signal it in the membership invalidation before the client rule
changes (the client then reads on membership, so no new removal rule is needed).

## Compatibility

The capability gate keeps an un-upgraded source working. Advertise
`delegateReportPreview` from the daemon and hub; the shared store enables the
no-read merge only when its client reports the source's support. Without it
(older daemon, remote host that does not advertise), the store keeps today's
behavior — read `delegates` on the invalidation — so reports still land. Task 1
verifies the hub relays a source's feature set per source; if it cannot, the
fallback is to leave the read in place for such sources and accept the work.

## Test and acceptance obligations

- **Parity.** For the same settled generation, the `delegate/updated` preview
  equals the delegates-list `reportPreview` (including the 4096 cap, the
  ellipsis case, and a resumed run).
- **Merge.** A field update patches a loaded row and causes zero
  `evener/thread/delegates/list` reads; a repeated frame publishes nothing; a
  stale `projectionRevision` cannot regress a row; a resume clears the prior
  report; a `session` store ignores another owner's delegate.
- **Membership.** An unknown delegate triggers exactly one read; a membership
  invalidation reads; resync, reconnect, observe and paging still read.
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
| Stale row from an out-of-order frame | `runGeneration` + `projectionRevision` map. |
| Missed removal | Task 1 proves append-only; otherwise the producer signals membership. |
| Old source with no preview | Feature gate keeps the read. |
| Removed read hides a source error | Recovery paths (resync, reconnect, stale cursor) unchanged; the collection still reads on observe and paging. |

## Open questions for Jesse

1. Membership: accept the append-only proof plus the existing recovery paths, or
   require an explicit removal signal in the invalidation regardless?
2. Scope: web + native + shared store together (recommended), or web first
   behind the feature gate?
3. Also narrow the `summary` invalidation to count-moving changes, or keep it
   for every delegate change?

## File map

| Unit | Files |
| --- | --- |
| Wire + generated | `appwire/types.go`, `appwire/session_activity.go`, `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md` |
| Producer | `internal/appprojector/appwire_projection.go`, `agent/session_activity_delegates.go` (shared bound), `agent/session_activity_events.go`, `agent/delegate_runtime.go` |
| Shared store | `appwire-client/typescript/sessionActivityStore.ts`, `.test.ts` |
| Web | `cmd/evener-hub/frontend/src/shell/activitybar/ActivityPageBoundary.tsx` (done), `activityApi.test.tsx` |
| Native | `mobile-native/src/subagentRows.test.tsx`, `src/subagents/SubagentsScreen.test.tsx`, `src/ConversationScreen.send.test.tsx` |
| Docs | `docs/product/session-activity.md`, `docs/design/session-activity-api.md`, `docs/product/subsystems.md` if ownership moves |
