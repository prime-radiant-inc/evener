# Native recovery UI integration brief

Status: design-only audit. No source, branch, or issue state was changed.

Audited refs

- Runtime-read bde874c38536bf8bc84ffce0a4bd667735761a6f.
- Producer metadata 1349fcbd3193d6f126f95b3631de56a9dddb8baf.
- Foundation integration 1f3203bd73ec29cc5196f670748c638a13955235.
- Web oracle in cmd/evener-hub/frontend/src/panes/session/composer/recovery and composer/queue.
- Native consumer in mobile-native/src/screens.tsx, draftDocument.ts,
  draftRepository.ts, and ImageAttachments.tsx.

The producer ref is based on A2 rather than runtime-read bde874, so it is not a
clean linear prerequisite. Combine the changes deliberately before
implementation. Do not treat a diff from bde874 to 1349 as a proposed deletion
of runtime-read behavior.

## End state

The existing native Review status modal in ConversationScreen becomes the
route-scoped recovery surface. It shows every durable record for the exact
conversation target, ordered by intentSequence:

| Stored record | User-visible state | Automatic action |
| --- | --- | --- |
| outbox state submitting | Sending / waiting for delivery | None |
| optimistic state accepted | Accepted, waiting for transcript reflection | None |
| outbox state blockedUnknown | Delivery unconfirmed | Never replay blindly |
| recoveryKind rejected | Rejected, with recoveryReason when present | None |
| recoveryKind orphaned | Needs review; hub outcome is not represented | None |

The panel may offer Restore to draft for rejected recovery rows and Dismiss
for rejected or orphaned rows. It must not offer a generic retry for blocked-unknown rows.
Reconciliation remains the only path that can establish whether a blocked record
is dispatchable; a later dispatch still goes through the dispatcher. Rows stay
visible until their durable state changes.

This is a route-scoped surface, not a new global inbox. A cold start shows
records when that target's screen opens and performs its target read. An
app-wide recovery inbox would require a separate native target-index decision:
bde874 listTargetRefs unions outbox and optimistic rows but does not enumerate
recovery-only targets.

## Target and lifecycle fences

The route already supplies hubId and raw ref. Every native mutation storage
call must use:

    nativeMutationTargetKey(hubId, ref) // JSON.stringify([hubId, ref])

The raw ref remains the wire payload ref. The composite key is the storage
scope, subscription value, and discard fence. Never pass the raw ref to
readTargetRecords, listRecovery, or discardRecovery.

The consumer captures the composite key, client binding, and a local screen
generation token before an asynchronous read, then ignores the result if the
route, target registration, or generation changed. A late read from target A
must not replace target B's projection or draft. The existing runtime lease
rules provide the authoritative-read fence; the recovery UI needs the same
route fence around its projection and actions.

Startup and registration belong to an effect-owned host lifecycle. The UI must
not construct a runtime or register a target during render. Subscribe, start,
register, and initial-read in the committed effect; remove the subscription and
registration in cleanup. The screen consumes the runtime only after setup has
completed.

## Runtime action required by the UI

bde874 already has native-only listRecovery(targetRef) and
discardRecovery(clientMutationId, targetRef) on SQLite, and
readTargetRecords(targetRef) returns outbox, optimistic, and recovery
snapshots. The public host/runtime seam still needs one narrow discard wrapper.
The UI must not call the storage object directly.

That wrapper must:

1. take the exact composite target key and client mutation ID;
2. invoke the storage delete with both values;
3. notify storage listeners with that composite key only when the delete
   changed a row;
4. produce no notification for a wrong target, an already-discarded row, or a
   failed delete.

The current bde874 storage delete uses both SQL predicates but emits no
notification. Add the wrapper in the first UI/runtime action slice so Dismiss
cannot leave a stale consumer. The regression test should use real SQLite:
insert identical IDs under target A and B, discard A, assert only A's listener
fires and B remains; repeat with a wrong target and assert no listener fires.

## Projection and automatic refresh

Add a small native-only projection module, or keep pure functions next to the
panel, that:

- filters every record by the exact composite target key;
- combines outbox, optimistic, and recovery rows;
- preserves composerText, attachment metadata, recovery kind, reason, and the
  payload's structured input;
- sorts by intentSequence, with a stable ID tie-breaker if a fixture needs
  one;
- labels the required states without deriving a status from rendered prose.

The panel subscribes to the runtime storage listener and refreshes only when the
emitted key equals its captured composite key. It reads the target once after
committed setup, after the screen's authoritative conversation read, and on
the existing focus/foreground lifecycle where the host already rehydrates.
Those reads are automatic; there is no user-facing refresh-to-latest control.
A status panel must not depend on pulling the transcript before a pending row
resolves.

Discard calls the wrapper and then lets the listener/read projection converge.
Do not optimistically remove a row before the durable delete succeeds.

## Exact recovery content

The producer ref keeps the editing source beside the translated wire input:

- composerText is the exact text, including [image N] anchors.
- attachments stores marker, name, media type, and presentationId.
- payload.input is the structured transport input and already carries the
  image data. The producer runtime test stores and reads an image item with
  data: "AQIDBAU=" from the real SQLite outbox row.

The recovery converter must use composerText when present. It must never
reconstruct a draft from translated marker prose. Filter payload.input for
image items and pair those image bytes with the durable attachment metadata in
the same attachment order that the submit boundary wrote. The durable marker
is the composer placement and must be preserved even when the marker numbers
are out of text or image order. The payload image's name and media type are
preferred when present, with the durable metadata supplying the recorded
values. Do not derive a marker from array position.

Native DraftRecord currently has only draft, unconfirmed, images, and
unconfirmedImages. DraftDocument has no selected-skill field, and the current
native composer passes no skillNames argument to buildComposerInput. Current
native composer inputs are therefore text and images. Do not add an unsupported
skill draft field in this panel. If a record contains structured payload.input
items of {type: skill, name}, retain and show their canonical names in the
record preview; do not convert them into prose or silently drop them from a
future structured resend.

### Restore and discard transaction

Restore is routine native draft integration; it does not need a new attachment
storage schema or a product decision about image identity:

1. Require a loaded, non-submitting DraftDocument whose current draft text and
   current images are empty. Preserve a non-empty user draft and leave the
   recovery row available; do not merge or overwrite it implicitly.
2. Capture screen generation, target key, recovery ID, and current draft
   revision before any awaited operation.
3. Read composerText and the actual image items from payload.input. Pair each
   image with its recorded attachment metadata, preserving its marker,
   mediaType, name, and data. Create fresh native DraftImageData IDs as needed;
   presentationId is durable provenance and need not be an existing DraftImage
   primary key.
4. Write one draft record transaction containing exact composerText and all
   resolved image references/data. Reuse DraftRepository.write savepoint
   behavior rather than calling replaceDraft and addImage in a loop.
5. Revalidate target, generation, and draft revision. If the user edited the
   draft or the route changed while the save was pending, keep the recovery row
   and do not delete it.
6. Call the exact-target discard wrapper only after the durable draft write
   succeeds. If discard fails, retain the draft and recovery row for later
   retry; never clear the row first.
7. Refresh the projection from storage. A concurrent discard that already
   removed the same ID is resolved by the subsequent target read, not by
   assuming a stale callback owns the row.

The old transport uncertainty card can keep DraftDocument.restore(). Mutation
recovery needs a separate save-aware adapter or method because its record shape
and target fence differ. Orphaned rows are copy/dismiss only: they do not
claim a reconstructable composer payload and must not silently gain Restore.

## Exact files and seams

- mobile-native/src/nativeMutationRuntime.ts: expose the exact-target discard
  wrapper and successful-delete notification; retain readTargetRecords,
  subscribeStorage, and lease behavior.
- mobile-native/src/mutationOutboxStorage.ts: retain native-only listRecovery
  and scoped delete. Do not widen the shared MutationOutboxStorage port.
- mobile-native/src/nativeMutationHost.ts: use the committed host lifecycle
  from the foundation branch to start, stop, read, and subscribe; do not
  initialize SQLite or register targets during render.
- New mobile-native/src/nativeMutationRecovery.ts: pure projection,
  exact-target filtering, recovery draft conversion from composerText plus
  payload image bytes, and action result types.
- New mobile-native/src/MutationRecoveryPanel.tsx, or an equivalently small
  native-only component: render the five row states and explicit
  rejected/orphaned actions. Rejected may Restore or Dismiss; orphaned is
  Copy or Dismiss only. Keep image preview plumbing in ImageAttachments or
  the recovered DraftImageData adapter.
- mobile-native/src/screens.tsx: wire the panel into the existing recoveryOpen
  modal, pass route target key and current DraftDocument, and own effect-based
  subscription/read generation.
- mobile-native/src/draftDocument.ts and draftRepository.ts: add only the
  narrow transactional mutation-recovery restore seam. Fresh native image IDs
  are acceptable when the recovered bytes come from payload.input.
- mobile/src/state/conversationMutation.ts and the native submit caller:
  1349 defines ConversationComposerSnapshot, but native currently does not pass
  it. Bind exact source text at the submit boundary in the producer/activation
  slice. The existing translated input already supplies the durable image
  bytes.

## Bounded implementation slices

Each slice is a complete user-visible behavior, not a storage primitive left
without a consumer. Keep production diffs in the 80–150 line range; split
further rather than crossing the 400-line hard ceiling.

### Slice A — status projection and automatic native panel (80–150 production lines)

Implement the native-only projection and existing Review status panel for
submitting, accepted/optimistic, blockedUnknown, rejected, and orphaned rows.
Wire effect-owned target reads and exact-key subscription refresh. Include the
runtime discard wrapper so Dismiss produces a successful-delete notification
only for the exact target. Dismiss is available for rejected/orphaned rows;
blockedUnknown has no replay action. Rejected rows expose Restore; orphaned
rows expose Copy and Dismiss only. Use structured row fields and exact
composerText, payload image counts/previews, and skill metadata for the
preview.

Tests: real SQLite records for all states, composite target isolation,
intent-sequence ordering, wrong-target discard with no notification, payload
image bytes surviving the row transition, and a subscription-driven update
without a refresh button.

### Slice B — durable exact restore (80–150 production lines)

Add the small DraftDocument/repository restore seam and wire Restore to draft
for empty documents. Decode exact composerText and payload image data,
preserving metadata markers, then write fresh native image references and the
draft through one transaction. Discard only after the save and
current-target/revision checks. Keep the recovery row on save or discard
failure. Do not expose Restore for orphaned records.

Tests: exact marker text and image marker/name/media-type/data pairing, empty
draft requirement, current-draft preservation, save failure retaining the row,
stale-route/draft edit preventing discard, and successful discard causing the
target-only subscription update.

### Slice C — producer caller binding and recovery completeness (80–150 production lines)

Pass the composer snapshot at the native send, steer, and queue boundary so
exact source text survives; retain the existing translated payload input,
including image bytes, for reconstruction. Preserve canonical skill items in
the structured payload even though the current native editor has no skill chip
state. Add a native integration test that observes the actual SQLite outbox
row and later recovery projection; do not fabricate a server receipt. Retain
no-blind-replay behavior for blockedUnknown rows.

## Validation and falsification

Use real SQLite with a deterministic fake client for native tests. Do not assert
large generated strings or implementation-shaped output.

Minimum falsification cases:

- Before the discard wrapper, a correct SQLite delete changes no storage
  listener; the new test must fail at that missing notification, then pass
  after the wrapper.
- Same mutation ID under target A and target B: restoring or discarding A never
  changes B. A read of target A whose authoritative IDs omit an unrelated
  target's ID must leave that unrelated target blocked.
- A deferred target-A read delivered after navigation to target B is ignored.
- A recovered text with multiple markers uses exact composerText; image bytes
  from payload.input are paired with durable marker metadata even when marker
  numbers are out of order.
- A draft save exception leaves the recovery row and current draft untouched.
  A user edit during an awaited save prevents stale recovery discard.
- A rejected row displays its actual recovery reason and offers Restore/Dismiss;
  an orphaned row remains copyable and dismissible without automatic resend or
  Restore.
- A subscription event updates the panel projection; no user-facing latest
  refresh is needed.
- A cold-start route read displays recovery rows already in SQLite. App-wide
  target discovery is out of scope for this route-scoped surface.

## Settled scope

- Recovery is shown in the existing route-scoped Review status modal. A global
  inbox and recovery-only target enumeration are out of scope for this slice.
- Current native composer inputs remain text and images. Structured skill items
  remain data in the mutation payload and are shown in recovery previews; no
  new native skill draft representation is introduced.
- Rejected recovery can restore exact text and images into an empty draft after
  durable save checks. Orphaned recovery is Copy/Dismiss only. BlockedUnknown
  never receives blind replay.


No source, test, refs, branch, push, PR, or issue state was changed by this
audit.

