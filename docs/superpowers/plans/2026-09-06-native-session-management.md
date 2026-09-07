# Native iOS session management

Continue the accepted delivery plan with existing AppWire v4 operations and
native menus. iOS is the v1 delivery target; preserve Android sources.

## Contracts and boundaries

- Ordinary fork uses `thread/fork` with `ref`, selected `sourceTurnId`, and
  optional edited input/label/model fields. Its response contains `thread` and
  optional `originalInput`, with no navigation receipt. Navigate to the returned
  child only after acknowledgement. Preserve the existing aside workflow.
- Session deletion uses `evener/session/delete { ref }`. Its response contains
  `deleted`, `skipped`, and `navigation`, with no `ok` field. Explain skipped
  live sessions and confirm actual deletion before leaving the conversation.
- Pin assignment/unpin and section rename/delete return `ok`, `changed`, and a
  navigation receipt. Reuse `NavigationActions` and `NavigationPages`; do not
  create another organization controller. A section deletion removes the section
  while keeping sessions.

Source authority: generated protocol types and web rail actions under
`cmd/evener-hub/frontend/src/`. Server responses remain authoritative; do not
optimistically remove or repin rows. Never replay a mutation after a lost reply.

## Implemented controller slice

`NavigationActions` now offers assignPin, unpin, renamePinSection and
deletePinSection alongside archive/favorite. It serializes mutations, blocks
further writes while uncertain, and terminates pending state when scope changes.
A read-only reconcile callback clears uncertainty only after successful current
navigation refresh. `ProjectsScreen` wires existing refresh controls to this
callback and checks the page model's error state.

Ten deterministic tests cover payloads, acknowledged no-ops, delayed mutation
receipts and refreshes, disposal/current-scope fencing, failed reconciliation,
and no replay. The integrated native gate passed 402 tests plus typecheck during
this slice. These checks do not qualify the missing pin UI or durable mutation
recovery across model disposal.

## Remaining implementation

- [x] Add native pin catalog/section destinations and accessible menus for
  assigning an existing section, naming a new section, unpinning, renaming and
  deleting a section. Consume the shared controller; preserve paging/reveal.
- [x] Add selected-turn ordinary fork with editable input and truthful uncertain
  outcomes. Reuse conversation services for returned child identity.
- [x] Add ended-session deletion with confirmation and server skipped results.
  Refresh from its navigation receipt without assuming an `ok` field.
- [ ] Preserve unfinished edits and uncertain mutation targets across screen/hub
  replacement and process death; current controller lifetime fencing alone
  does not establish that requirement.
- [ ] Run deterministic response and lifecycle tests for these UI integrations.
- [ ] On the owned scripted hub, execute fork, delete, pin/unpin, and section
  create/rename/delete from iOS and independently read resulting server state.
- [ ] Qualify lost replies, overlapping hub/session identities, large text,
  VoiceOver and iPad. Record actual outcomes rather than inferring acceptance
  from controller tests.

## Organization foundation checkpoint (2026-09-07)

Native navigation now supports pin catalog paging and invalidation, validates
cached responses/tombstones before accepting them, and uses resource-specific
receipt revisions. The shared organization controller can persist a pending
operation and acknowledged receipt through an injected synchronous journal,
then pass that exact target to a replacement model's reconciliation callback.
A pure pin assignment editor is present but has no registered destination.

The native gate passed 457 tests plus TypeScript. The independent organization
SDK recipe passed nine contracts and outside-checkout package qualification.
A direct owned-hub create/pin/no-op/rename/unpin/delete cycle passed and restored
the fixture's original unpinned state. See
[organization evidence](../../design/mobile/organization-evidence.md).

These are foundation and API results. Production journal wiring, persistent pin
proposals, target-specific readback, native routes/menus and their UI/lifecycle
acceptance remain open; the remaining implementation checklist above is not
complete.

## iOS assignment checkpoint (2026-09-07)

`c9466f954` registers the pin assignment screen and native session-menu action,
wires per-hub durable operation recovery, stores per-session proposals, restores
the route after restart, and reads current assignment plus catalog before clearing
recovery. Maximum text size exposed and corrected fixed headers hiding the form;
all content now scrolls and action rows wrap.

The final Release app restored a saved proposal without sending it, then created
and pinned a section and unpinned the session. An earlier same-controller journey
assigned an existing section. Separate reads confirmed server state. External
deletion exposed a hub empty-graph delta comparison bug, corrected in `c9fb42abc`;
the final iOS Refresh then succeeded. The owned fixture was restored.

Native validation is now 477 tests plus TypeScript; frontend gates, the full hub
test package and the runtime build passed. Evidence is appended to
[organization evidence](../../design/mobile/organization-evidence.md#ios-pin-assignment-checkpoint-2026-09-07).

The next management slice is native catalog/section browsing and rename/delete.
The broader checklist stays open until those destinations, ordinary fork, session
deletion and lifecycle/accessibility acceptance are implemented and exercised.

### Catalog and section management implementation

1. Share the existing pin navigation/recovery binding between assignment, catalog
   and section management screens. Require a read in the current focus/connection
   scope before enabling edits; cached observations remain visibly last-known.
2. Add a paged pinned-section catalog from Sessions and a section session list
   using the existing navigation tree/list rendering. Each section exposes Manage.
3. Add a section editor with a synchronously saved rename proposal scoped by
   hub and section. Restore its route and raw name after restart. Confirm section
   deletion with current name/member count and explain that sessions are retained.
   Both mutations use the existing journal and fresh target/catalog readback.
4. Verify catalog lookup beyond the first page, absent sections, stale confirmation,
   restart recovery, rename/delete without replay, and the owned iOS workflow.
   Keep full VoiceOver/iPad and the broader release audit separate until observed.

### Catalog and management checkpoint

`f08473f48` implements the four steps above. The final Release restored a name
proposal and route after termination without writing to the hub, saved and
confirmed the rename, rejected a stale delete confirmation, and canceled then
confirmed deletion at maximum text size while retaining the session/history.
The same final build passed native assignment and member browsing, then removed
cached rows after external section deletion and Refresh. The owned fixture was
restored to an empty catalog and unpinned state.

A notification-gap readback regression found during the UI journey is corrected
using the existing receipt-aware refresh. The two new regressions failed first;
492 native tests, TypeScript and touched Biome checks now pass. See the
[management evidence](../../design/mobile/organization-evidence.md#ios-pinned-section-management-checkpoint-2026-09-07)
for the exact Release bundle and scope of the observed results.

The next management work is ordinary selected-turn fork and ended-session
deletion. Durable archive/favorite recovery and full lifecycle/accessibility
qualification remain part of the unfinished overall checklist.

### Ordinary fork implementation

1. Preserve the user message's typed `transcriptEntryIndex` through the shared
   projection. It is the RPC divergence position; turn IDs and row positions
   must never be parsed or substituted. Offer the action only for a positive
   safe entry index when the source advertises fork capability.
2. Add a separate ordinary-fork action to the existing conversation service.
   Use `thread/fork` with `deferInput: true`, matching the web workflow: copy
   history before the selected message and prepare its text in the child's
   editable composer. Validate acknowledged child identity; preserve aside.
3. Persist a hub/parent-scoped checkpoint before creating a fork and retain
   acknowledged child identity/input before opening it. Restore the source route,
   prepare the child draft without replacing newer edits, and never replay an
   unknown request. An uncertain outcome offers session-list inspection and an
   explicit choice to permit a new request. Fence actions by source instance,
   current connection/focus, and the exact saved checkpoint.
4. Test typed divergence, deferred payload/capabilities, draft and checkpoint
   failures, late acknowledgement, scope replacement and no replay. Exercise
   the real iOS fork, child draft/restart and preserved parent history against
   the owned scripted hub before recording acceptance.

### Ordinary fork checkpoint (2026-09-07)

`ff3da051b` implements the selected-message fork, source preview, durable child
handoff and route restoration. The owned iOS journey exposed two integration
defects: live daemon capabilities hid the hub-owned operation, and returning
from the child left the parent's subscription replaced. `0b2d95109` corrects
the narrow hub capability overlay; `be164c5ad` rereads the parent on focus.

The final Release created a child with exactly the preceding source history,
restored its edited 513-character unsent draft after termination, and received
external parent updates after Back. Native validation passes 510 tests plus
TypeScript; shared mobile tests/checks, full hub tests and scoped hub lint pass.
See [fork evidence](../../design/mobile/fork-evidence.md) for source/artifact
identities, screenshots, deterministic fault coverage and limits.

Ended-session deletion is next. Durable archive/favorite recovery and the full
lifecycle/accessibility matrix remain open. The existing fork protocol cannot
atomically reject a concurrent source replacement after the native preflight
read; this limitation remains recorded rather than treated as resolved.

### Ended-session deletion implementation

1. Add a scrollable review destination from a stopped local session's menu.
   Persist that destination, but never a confirmation or an automatically replayed
   request. Recheck the session identity, name and stopped state after the final
   native confirmation and before dispatch. The server still owns admission.
2. Extend the existing per-hub navigation action journal with the exact delete
   target and a validated deleted/missing/skipped acknowledgement. Response IDs
   are bare session IDs. Save late acknowledgements even after screen disposal;
   conditional journal updates must preserve newer operations.
3. Converge each receipt target against its own resource revision. Check the
   exact session through a non-subscribing metadata read, fenced by fresh hub
   navigation generations. Only the exact missing-thread wire rejection proves
   absence; transient failures never do. This also works after a hub restart
   has discarded an old location tombstone. A pending project-deletion fence
   remains an error, because its targetDeleted outcome also covers cleanup
   still in progress.
4. Keep unknown-present requests unresolved. A separate explicit allowance may
   clear only that exact unknown checkpoint and sends nothing; another deletion
   still requires confirmation. A skipped response keeps the session and shows
   its reason. Navigate to Sessions only after absence is confirmed and the
   destination is saved. Keep local unsent drafts and unrelated sessions.
5. Run deterministic controller/repository/readback/route checks, then exercise
   cancellation, stale confirmation, process restoration and actual deletion on
   a newly owned iOS fixture. Preserve the earlier fork acceptance fixtures.

### Ended-session deletion checkpoint (2026-09-07)

`29ac49032` implements the review, exact target confirmation, durable outcome
recovery and navigation. The iOS journey verified cancellation, stale-title
rejection, restored review without dispatch, reachable controls at maximum text
size, and actual deletion with the local draft retained. The journey also exposed
a stale external rename in Sessions; `d2188a67d` adds focused, coalesced navigation
invalidation refreshes while preserving the active search and visible rows.

The final Release separately passed live filtered rename, review/draft restart,
deletion and return-to-Sessions restart. Independent reads verified both deleted
fixtures are absent, original reader/fork histories remain complete, and all
owned drafts remain intact. Final native validation passes 540 tests plus
TypeScript and touched Biome. See
[deletion evidence](../../design/mobile/session-deletion-evidence.md) for exact
artifacts, screenshots and limits.

Next is durable archive/favorite recovery in Projects. The remaining lifecycle,
multi-hub and accessibility matrix is still open, including conservative recovery
when concurrent project deletion has fenced the target but not finished cleanup.

### Durable archive and favorite recovery implementation

1. Reuse the per-hub organization journal in Projects and project session lists.
   Save intent before dispatch, retain late receipts, and never replay a saved
   request. Reject old alert callbacks when their list or screen has changed.
2. Read the exact project through paginated project catalogs or the exact local
   session through its location. Check receipt revisions against their own
   resources and fence the reads by hub generation and current screen identity.
   A missing row, failed read, or unrelated journal operation cannot settle it.
3. Archive placement is effective state, not proof of an explicit archive
   decision. An unknown archive stays unresolved even when placement matches.
   Show its current title and placement; an explicit continuation rereads that
   same state and clears only the exact checkpoint without sending a mutation.
   A known receipt with conflicting current state uses the same review path.
4. Put recovery controls in the scrolling list header, block new organization
   writes while recovery is pending, and restore project/filter destinations
   after termination. Offer session archive only for local top-level sessions.
5. Cover late replies, paging, independent resource revisions, restarts,
   conflicting state, route restoration and no replay deterministically. Then
   qualify archive/favorite and restart recovery in the owned iOS Release.

### Archive and favorite recovery checkpoint (2026-09-07)

`60611f595` adds the durable journal integration, exact-target readback, explicit
read-only continuation and restored project filters/tiers. `ef1cdf93e` separates
an unresolved request from a failed read in the recovery UI. Final Release bundle:
`7f14a22312fa38916cb64b7700a10788a6cd6ef1d4cbf40c6fb952f78182f7a2`.

The final iOS artifact passed project pin/unpin/archive/unarchive, local session
archive/unarchive, exact project/filter/tier restoration, stale Alert rejection,
and unknown-intent continuation at maximum text size without a hub write. The
unknown device intent was seeded at the storage boundary; transport/storage
faults remain deterministic tests. Independent reads confirm retained histories,
unchanged drafts, restored visible fixture organization and an empty journal.
Native verification passes 553 tests in 66 files plus TypeScript and touched
Biome. See [organization evidence](../../design/mobile/organization-evidence.md).

Native keybindings and the broader lifecycle/accessibility qualification remain
open. Archive placement cannot prove an unknown explicit decision was persisted;
missing targets and unrelated recovery operations remain conservatively blocked.
The whole iOS v1 goal remains active, with Android qualification deferred.
