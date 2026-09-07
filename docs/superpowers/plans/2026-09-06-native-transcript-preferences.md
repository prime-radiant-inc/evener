# Native transcript display preferences

## Scope

Implement the iOS native reader's mobile transcript display preferences from
the existing `TranscriptDisplayConfigV1` contract. Android remains deferred.
The settings must affect presentation while preserving the existing reader's
critical context, stable item identity, paging, live updates, and reader
restoration behavior.

## Current source boundary

The native screen renders `groupTimeline(conversation?.items ?? [])` in
`mobile-native/src/screens.tsx:958-959`, then passes every row to
`TimelineItem` at `screens.tsx:1625-1638`. The service creates that
`MobileConversation` by calling `projectThread(thread)` in
`mobile/src/services/conversation.ts:545` and `:579`.

`mobile/src/state/conversation.ts` explicitly stores only the
`MobileConversation` projection (its file-level comment says the store never
holds the raw wire thread). `loadOlder` projects each fragment before merging
it. Keep this single store and project from its unfiltered items; adding a
second raw-wire store or changing the paging canonical source is out of scope.

## Required projection shape

Keep the one existing `MobileConversation` store and add a pure native
presentation transform over its unfiltered `items` before `groupTimeline`.
Changing a config revision reruns this transform only; it must not issue a
transcript read, mutate a cursor, or create a second canonical source.

The shared mobile projection already preserves `MobileTimelineItem.family`,
activity `state` and `detail`, notice `family`/`tone`, stable IDs and
`position`, and conversation-level `MobileUsage`. Do not duplicate these
fields or invent raw-wire retention. The transform must preserve stable source
identity for surviving rows and attachments.

There is one concrete granularity issue: `clusterActivities` in
`mobile/src/conversation/project.ts:459-489` collapses a same-family run to
the first row and carries only its state. Later activities' IDs, details,
positions, and attachments are no longer independent presentation units.
Either move clustering after the native presentation transform, or add a
typed per-member list to the existing activity item and make the transform
consume it. Do not introduce a second raw list. Tests must prove that changing
from a compact to detailed config recovers every member of a run.

The caller supplies the confirmed mobile configuration once available and
retains the last confirmed config while loading or after a write error. Do not
invent a new default: use the server's current mobile default returned by
`evener/settings/transcriptDisplay/get`.

## Config semantics

The source contract is `cmd/evener-hub/frontend/src/transcriptDisplay/config.ts`
and the server reference implementation is
`cmd/evener-hub/frontend/src/transcriptDisplay/projector.ts`.

Content selection is applied before clustering:

* `toolCalls` controls ordinary command execution rows.
* `toolIntent` produces the typed intent proxy when full calls are hidden.
* `reasoning` controls routine reasoning rows.
* `expandByDefault` initializes disclosure state only; it must not change
  disclosure IDs or turn a user-expanded row into a new identity.

Always retain user and assistant messages, questions, approvals, failures,
warnings, active/in-progress items, and interruption/terminal context. A
custom vector that disables both tool intent and calls must still preserve a
failed or active tool as a critical row with its neutral summary. Attachments
remain adjacent to the source item that produced them and retain their source
identity even when the companion activity row is filtered.

Apply advanced visibility by typed event kind, matching the server rules at
`projector.ts:198-213`:

* `roundTimings` controls `round_timings` events.
* `systemEvents` controls other routine system events.
* `promptEvents` controls `system_prompt` and `prompt_loaded` events.
* `hookExits` supports `none`, `successful`, and `all`; failed hook exits
  remain visible as critical context even when routine hook events are hidden.

The current native `groupTimeline` hardcodes selected non-warning notices into
collapsed details. Keep grouping after source projection, and derive its
membership from the same typed visibility decision. Never discard the source
row merely because the current config hides it.

## Advanced metadata required

`MobileTimelineItem` currently exposes activity `durationMs` and `exitCode`,
but does not expose turn-level metrics. To honor every advanced field, retain
and project these source values:

* `roundTimings`: existing activity `detail.durationMs` and typed system-event
  text where the generated item provides it. Verify the generated
  `round_timings` shape before adding a field; do not infer timing from render
  time or labels.
* `tokenCounts`: existing `MobileConversation.usage` fields
  (`inputTokens`, `outputTokens`, `cacheReadTokens`, `totalTokens`) already
  projected by `projectUsage`; render them conditionally and preserve absent
  values as unavailable.
* `estimatedCost`: existing `MobileConversation.usage.cost`, preserving the
  server's display string.
* `systemEvents`, `promptEvents`, and `hookExits`: existing notice
  `family`/`tone` plus activity `detail.exitCode`, `detail.error`, and source
  text. Add a typed field only if inspection confirms a generated value is
  currently dropped; never synthesize one from labels.

The native renderer shows advanced metadata only when its config flag is
enabled and source data is present.

## Preference lifecycle

`mobile-native/src/nativePreferences.ts:34-35` already tracks the mobile
config, revision, draft, saving, conflict, write uncertainty, and errors.
Wire its confirmed config into the conversation projection and subscribe to
the existing change notification. A newer confirmed revision triggers a
pure re-projection of retained unfiltered source; it does not reset the FlatList,
cursor, disclosure identity, or reader anchor. While a write is uncertain,
keep the last confirmed rendering and explain the uncertainty in the settings
surface. If settings support is unavailable, do not claim the reader honors
editable transcript settings.

## Reader and paging invariants

Stable row identity remains the source transcript key plus `{entry,item}`
position already carried by `MobileTimelineItem`; configuration changes may
change visibility but must not invent new IDs for surviving rows. Reader
anchors resolve against the newly projected rows and retain the same semantic
item and within-item offset. If the anchor is currently hidden by config, the
controller must retain it and resolve it when a later config makes it visible;
it must not jump to the first or last loaded row.

Paging and live events continue using the existing service/store merge of
unfiltered projected items, then the native presentation transform runs.
`hasEarlierItems`/`hasLaterItems`, cursors,
replacement instance checks, and stale-cursor recovery remain properties of
the raw conversation scope and are independent of whether the current config
renders a row. Reprojection must never make a hidden page appear exhausted.

## Bounded implementation sequence

1. Preserve the existing unfiltered `MobileConversation.items`; move or
   augment activity clustering so per-member identity/details remain available
   to presentation filtering without a second store.
2. Add a pure native presentation transform parameterized by the confirmed
   mobile config. Preserve critical rows, attachments, source positions, and
   disclosure IDs.
3. Add only verified missing typed advanced metadata to the shared mobile
   model, then add conditional native rendering.
4. Connect preference revision changes to the pure transform and preserve reader
   state during the update.
5. Add deterministic tests for each preset, a non-preset custom vector,
   advanced flags, unknown/failed events, hidden-item restoration from the
   existing unfiltered items, paging/live merge, scope replacement, and reader
   anchor continuity. Add a focused
   native check for the preference-to-projection wiring; device qualification
   remains a separate iOS acceptance step.

## Acceptance evidence

The implementation is ready for review only when tests show that every
representable config field changes the native presentation, hidden source
items remain available for later re-projection, critical context is never
lost, and config changes do not alter paging boundaries, stable identities,
or the reader's semantic position. The settings UI must describe the actual
support state and must not imply that a saved value changes the reader before
this path is wired.

## Implementation checkpoint

The shared projection now retains typed activity members, source descriptions
for tool intent, and system notice event kind/hook exit status. Failed tool,
reasoning and unknown activities remain individual rows. The canonical store
and compact presentation are unchanged; the native preference transform and
editor are still pending.

The focused projection suite passed 78 tests, including red-to-green checks for
distinct member descriptions and separate unknown failures. Native integration
passed 402 tests plus typecheck. Independent review found no remaining scoped
projection issue. A broader shared mobile run exposed stale v3 fixture handshakes
and obsolete turn-count request expectations; those test fixtures are being
updated to the current v4 fragment contract separately.

Do not mark preference-to-reader acceptance complete from this metadata work.
