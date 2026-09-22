# Steering ghost at the live edge — design

Status: design for review. Direction A ("ghost at the live edge") picked by
Jesse over B (held queue) and C (boundary beacon) on 2026-09-20; mockups and
the mechanism writeup live in
`docs/web-ui/mockups/2026-09-20-steering-almost-delivered/` (README.md +
`idea-a-ghost-at-live-edge.html`). Branch: `wip/steering-almost-delivered-ux`.

## Problem

A steer accepted mid-run is delivered to the model only at the next injection
boundary (end of the current stream + tool round:
`injectPostToolSteering` → `injectDrainedSteering` →
`consumeSteeringMessage`, which emits `evener/steering/injected`). During that
window — potentially minutes — the web UI shows a dim chip that does not say
what it waits for (direct steers read "Steering", drains read "Draining"
with the composer text only — never the drained rows' content), or nothing
at all (promote: the queue row vanishes on `thread/queueChanged`; the client
does own a durable record, but `pendingMethod` has no promote mapping, so
that record renders nothing). This spec makes the held message visible, in
place, until it is delivered.

## Decision

The held steering message renders in the transcript, as the message it will
become, in a provisional register — dashed accent edge, reduced opacity, and a
caption in the timestamp slot reading `Delivers when this step finishes ·
held 42s`. This reverses the recorded ruling that kept optimistic items out
of the transcript (`PendingChips.tsx`'s header), in the same shape the AskDock
already blessed: a named trailing row the session pane owns, rendered after
the turn rows — never a fake item inside a turn's wire data.

## Design

### 1. Mount point — one virtualized trailing row

`TranscriptBody`'s `trailingRow` slot (the AskDock precedent: state lives
outside the virtual list, the list's end-anchoring surfaces it to a reader at
the bottom without yanking one scrolled up). Session.tsx composes a single
trailing row, id `live-edge`:

```
trailingRow =
  (askPending || heldSteers.length > 0)
    ? { id: "live-edge", content: <>{askPending && <AskDock/>}{heldSteers.length > 0 && <HeldSteerStack …/>}</> }
    : undefined
```

AskDock keeps its current row position and semantics; the ghost stack renders
below it when both exist. Rendered only while the session is live (not for
`notLoaded`/read-only surfaces).

Companion change, mandatory: `Session.tsx`'s `renderedRowCount` — the count
every end-targeted scroll path reads (currently
`renderRows.length + (askPending ? 1 : 0)`) — derives from the row it hands
the list: hoist the trailingRow into a const and count
`renderRows.length + (trailingRow !== undefined ? 1 : 0)` (the form
TranscriptBody itself uses), so the count and the row are one predicate and
cannot drift. Its own comment warns that an uncounted trailing row "lands one
row short, leaving the answering surface below the viewport"; without this,
jump-to-bottom and append-follow leave the ghost partially below the fold —
the feature's payload, invisible in exactly the documented failure mode.

Second companion change, same reasoning that produced the AskDock's signals:
a ghost appearing while the reader is scrolled away must fire the new-content
pill, but the pill's edge detector keys off turn and item shape primitives and
never sees a trailing row appear. Session.tsx therefore feeds the pill one
explicit signal — a `heldEpoch` — mirroring `askDockActivationEpoch` (the
ask's needs-you boolean has no held analogue; the epoch alone is the edge).
As with that epoch, arrival is the only edge:
`heldEpoch` bumps when held steering appears (a new id joins the stack) and
never on removal — departures are surfaced by the announce-once region below,
not by a "new content" bump. (So a canceled or rejected departure — which
moves none of the content-changed effect's primitives — reaches a
scrolled-away reader through the announcement alone; a failed delivery does
flip the failed-turn primitive, firing the pill for the turn's failure.) This
matters most for another client's steer, which appears with no local action
to draw the eye.

### 2. HeldSteerStack component

New `panes/session/transcript/messages/HeldSteerStack.tsx` (+ module.css).
Reads `usePendingTurnEntries(ref)` and renders every steer-family entry —
method `steer`, `drain`, or `promote` (see §3) — except the terminal states
that own QueueStrip rows (`blockedUnknown`, `canceled`; PendingChips filters
the same states today), one per row, each reusing the user-message structure
via `UserMessageView`. Stack order: §4.

- **`UserMessageView` gains one optional prop**: `provisional?: string`. Its
  presence does both provisional jobs at once — it renders in the timestamp
  slot instead of the item time, and it applies the provisional class from
  `usermessageitem.module.css` (dashed `--accent` bubble edge, the stack at
  `opacity: 0.78`, and the dashed left railmark from mockup A). No other
  change to that component; absent prop renders exactly today's output, so
  delivered messages are untouched. Every caption arm produces a string, so
  caption presence and provisional styling are the same bit — one prop, no
  representable-but-impossible state. Tokens only — no new colors, so no
  token-contract allowlist work.
- `opensExchange` is always false: a held steer joins work under way.
- **Scope — every client's held steering renders.** All of it lands in this
  shared transcript, so the stack shows `pendingTurnEntries` for the session,
  not only this client's own: this client's ghosts appear instantly, another
  tab of this origin too (shared storage), a remote client's at the next
  hydrate — §4 scopes the asymmetry and its timestamp consequences.
- **Ghost body, all methods — the chips' own composition, one helper.** The
  body is what the pending chips it replaces showed: the
  `queueEntryPreviewText(entry.text, entry.imageCount)` preview line plus
  `skillMarkers(entry.skillNames)` when the entry carries skills — extracted
  as one entry-body helper both surfaces call (the chips keep it for send),
  rather than a second copy of PendingChips' composition. An image-only
  promote reads the daemon's own placeholder (`[image]` / `[N images]`,
  carried by `queue.preview` and passed by `handlePromote` when the row's
  text is empty); a skill-only steer or drain (empty composer, a skill
  chosen) reads the skill marker instead of an empty bubble. One label is
  new UI copy this spec defines, because the composition yields a blank for
  it and nothing in the code produces one today: an empty-composer drain
  shows `[queued messages]` (the entry is blank until §3's first-hydrate
  refinement). HeldSteerStack owns that fallback label —
  `queueEntryPreviewText` itself stays untouched (it is a matching key
  between queue rows and pending rows, and must keep returning "" for
  contentless input). The ghost body wraps like the delivered message it
  will become — no truncation; the row width bounds it. Stated limitation:
  a ghost cannot render images — not because the data is missing (the
  durable record and the wire's `pendingMutations` entry both carry the
  staged image bytes) but because the entry the stack renders is a preview:
  `inputPreview` keeps only text, image count, and skill names. Widening
  the entry model to carry image data is out of scope here, so an
  image-bearing steer shows its text or placeholder plus the count, and
  the image itself first appears on delivery.
- **Non-visual register.** The caption is real text in the row, read in flow
  by assistive tech; the dashed edge and opacity are decoration only.
  Appearance, delivery, and a departure without delivery (rejected, canceled,
  blockedUnknown, or failed — §5) are announced exactly once each through a
  live region outside the virtual list (the `AskDockAnnouncements` pattern),
  never on the held-timer's cadence. Follow-up surfaces: departures that
  leave a durable row point at its QueueStrip home; §5 names the one
  departure that leaves none.

### 3. Foundation — a pending artifact for every steering path

- **Direct steer** — already exists (optimistic record, method `steer`).
- **Drain** — already exists (`QueueStrip.handleDrain` wraps
  `submitWithPendingTracking`, method `drain`). The ghost shows the composer
  text first, then refines: the daemon combines the drained queue rows'
  inputs with the composer's (`combineClientMutationInputs`) and puts the
  combined input on the steering it reports (`addPendingSteering`), so the
  next hydrate's `pendingMutations[].input` carries the full combined text
  and `authoritativeEntry` previews it. The drain ghost's text settles to
  that authoritative combined text at the first hydrate after the drain —
  the refinement is the display getting more accurate, not a visual flip.
- **Promote** — the client already owns the identity: `promoteQueuedAsSteer`
  goes through `enqueueMutation`, which writes a durable outbox record with
  the mutation id the daemon carries onto the pending steering
  (`clientMutationPromote` → `addPendingSteering`). Two changes make it
  renderable:
  1. `pendingEntries.ts`: `pendingMethod` maps `turn/promoteQueuedAsSteer` to a
     new `PendingMethod` `"promote"` (its own method, so labels and tests stay
     honest; not folded into `steer`).
  2. `QueueStrip.handlePromote` passes the row's full text (it already holds
     `fullText`/`entrySkillNames` at the press site, plus the row's daemon
     preview for image-only rows) into the `optimisticDisplay` input, so the
     ghost has content to show. The display payload is hardcoded inside the
     `promoteQueuedAsSteer` store action in `stores/threads.ts` today, so this
     step widens that action's signature (display text + preview fallback) —
     `stores/threads.ts` is in the file list, and its interface/test callers
     follow.

### 4. Caption state table

| Entry state | Session state | Caption |
|---|---|---|
| `submitting` | turn running | `Joining this turn · 0s` |
| `submitting` | no turn (dispatch straddled the turn boundary, stalled, or an idle drain of a parked queue) | `Delivers with the next turn · 0s` |
| `accepted` | turn running | `Delivers when this step finishes · held 42s` |
| `accepted` | no turn (turn ended while held; the daemon opens the steering-carrier turn on the next run — a Stop parks steering behind the `SteeringHeld` gate until a user-initiated run clears it) | `Delivers with the next turn · held 42s` |

Steering entries never enter the `claimed` state: the daemon keeps a client
steer `accepted` until its transcript append lands
(`TestClientMutation_SteerStaysAcceptedUntilDurableAppend` pins it, and a
legacy normalizer rewrites historical "claimed" steers back to "accepted"),
so the arms key off the entry's method family and the session's turn
liveness, never off a `claimed` state. Both arms read the thread status type,
never `model.activeTurnId`: the projector closes one turn row before opening
the next, so the id goes false between inline turns while the run (and the
status) continues — a caption keyed off the id would flip to "Delivers with
the next turn" at exactly the inline boundary where a drained steering
carrier delivers (#1330, the same rule `submitRouting`'s predicate already
follows: status alone, never the id). The turn-running arm is status
`active` (the package's `isTurnActive` predicate); the no-turn arms are
everything else.

One timer rule for all four arms: the count is elapsed time from `createdAt`,
formatted with the package's exported `formatElapsed` (the same format the
delegate clocks read — `42s`, `1m05s`), ticked against the transcript's
existing `SessionNowContext` cadence — 3 s granularity, accepted: the caption
reads the same clock the liveness line does, no per-second machinery
re-renders the virtualized row, and it is never an `aria-live` region that
ticks. The submitting arms show the same ticking count the accepted arms do
(at press it reads `0s`, and a stalled dispatch reads honestly); the count is
omitted entirely when no `createdAt` is known — never `NaN`, never a false
`0s`.

`createdAt` survival across hydrate: a published hydrate settles the outbox
record — `settleApplied` deletes it from durable storage — and replaces it
with the authoritative `pendingMutations` entry, which carries no timestamp,
so no replace-time lookup can inherit anything; the record is already gone.
The carrier is the mechanism `fromThisClient` already uses:
`pendingTurnsStore`'s `submittedHere` set is written at
`recordSubmittedHere` and never pruned, so it survives the settle. It
becomes a never-pruned id → `createdAt` map, written at the same
`recordSubmittedHere` site. Scope: the map is in-memory
page-session state (the store is deliberately framework-free, no storage),
so it carries `createdAt` across hydrates but not across reloads. Like the
set it extends, the map grows with every own mutation id this page session —
sends and queues included, read only for steer-family entries — bounded by
page lifetime, the accepted cost of keeping `recordSubmittedHere`'s
"everything this client submitted" invariant uniform.
`createdAt` is known while (a) the durable record exists — which includes a
reload before the first post-acceptance hydrate, where the durable read
re-discovers the record with its `createdAt` — or (b) the map holds the id
(post-settle, same page session). A reload after the settle loses it: the
caption omits the held count entirely — never `NaN`, never a false `0s` —
and the entry falls into the unknown-`createdAt` bucket below.

Stack order: entries with a known `createdAt` sort first, ascending; entries
without one render after them. This client's entries carry one while the
record or the map holds it (above) — not after a post-settle reload. Another
tab of this origin shares the durable outbox, so its entries carry their
`createdAt` too and sort with the known bucket; a remote client's entries
arrive only through `pendingMutations`, keep no client-side timestamp, and
take the array order of the current hydrate — the daemon sorts
`pendingMutations` lexicographically by mutation id, which is itself no
submission order, so their relative order is stable within a snapshot and
not guaranteed across snapshots. One home for the rule, shared by every
consumer: `reconcilePendingEntries`' own sort becomes this known-first rule —
today it sorts unknown-`createdAt` entries first, so queue rows and chips
inherit the new ordering along with the ghost, accepted. The stable sort
keeps array order for equal `createdAt`: a same-millisecond double-submit
keeps submission order in-session and hydrate order otherwise — a corner
the spec accepts rather than widening the entry model (a wire timestamp or
sequence on `PendingMutation` would pin it and is a non-goal here). The
reload-mid-hold pins live in Testing.

### 5. Settle semantics — unchanged, and the race, documented

The ghost disappears when the entry leaves `usePendingTurnEntries`. The
delivered path settles it two ways at once: reflection in the transcript
(reconcilePendingEntries' `reflectedMutationIds`) and the identity settle
(`handleNotification` → `dispatcher.reconcileIdentities`) on
`evener/steering/injected`, which fires with the same `clientMutationId` in
the same frame the reducer appends the item. Four non-delivery departures also
end a ghost, none of them through those paths:

- **Rejected at acceptance** (interrupt fence, empty input, queue-revision
  conflict): the record moves to the recovery store, out of the projection
  (which reads outbox + optimistic only), with no injected event and no
  reflection. The recovery row's QueueStrip home is the follow-up surface.
- **Canceled by Stop** (a still-`submitting` steer): the canceled row's
  QueueStrip home, same silent leave.
- **Blocked as delivery-uncertain** (an unknown send outcome with
  `persistenceUnavailable` or a blocked retry: `markUnknown` →
  `blockedUnknown`): the blockedUnknown row's QueueStrip home, same silent
  leave — the stack's exclusion already covers it.
- **Failed at delivery** (a skill-bearing steer whose selection fails
  preparation: `recordFailedSteeringSelection` retires the execution before
  any injected event, and the only wire trace is the error path's bare
  failed-Turn stamp, which carries no items): after the settle, the id is
  absent from the next hydrate and the ghost vanishes with it; before the
  settle, the optimistic record has no authoritative surface left to settle
  it, and the ghost persists indefinitely promising delivery. That stuck
  record is a pre-existing defect (today a pending chip sticks beside the
  composer the same way); the ghost inherits it, and fixing it needs a
  daemon-side failure notification — see Non-goals. In both the vanished and
  the stuck case there is no QueueStrip row: the departure announcement is
  the message's only trace, and the failed turn's error surface is the
  explanation.

Each non-delivery departure is announced once (§2); the delivered path needs
no departure announcement because the delivered item replaces the ghost in
place.

Known race, documented without repair in this spec: the reducer drops the
item when `activeTurnId` is already gone (turn settled between injection and
delivery), while the identity settle still drops the ghost. In this window
the steer is already consumed daemon-side — `steeringLanded` precedes the
injected emit, so no steering-carrier turn follows. Nothing client-side
recovers the message mid-session:

- The settled turn's live completion frame cannot carry it — every live
  settle site emits a bare `Turn{ID, Status[, Error]}` stamp with `Items`
  nil (`internal/appprojector/appwire_projection.go`; the reducer's own
  comment records the same).
- The next hydrate cannot carry it — the server's snapshot drops the
  injected item when the turn has no active turn id, and the transcript
  file is projected only at identity prepare, never on a read.

The message reappears only when the daemon next prepares an identity —
reattach or restart re-runs the transcript-file projection, which does
carry the appended item. That window is unbounded; nothing schedules a
re-prepare mid-session. Two daemon-side fixes exist — project the injected
item into the settled turn, or re-prepare the identity when an injected
steer lands with no active turn — and both exceed this spec's
no-daemon-changes scope. **Open decision for Jesse:** accept and document
(the default here; the race needs the turn to end inside the exact window
between injection and delivery), or say the word and the daemon-side
recovery becomes its own spec.

We do NOT withhold identity settlement for steer-family methods: the
uncertain-send/ recovery machinery settles on positive evidence, and the
injected event is that evidence — weakening it trades a narrow visual race
for real recovery regressions.

### 6. PendingChips

The chip strip keeps `send` only. Steer/drain/promote chips are removed — the
ghost is the single surface for held steering (one surface, no double-read).
Blocked/canceled durable rows keep their QueueStrip homes, unchanged. Known
window, accepted: on a `notLoaded` surface the removed chips leave nothing —
today the strip is the only held-steer surface there, and the ghost is
live-gated. (Before the first hydrate there is no footer at all, so nothing
renders today either; the coverage this change gives up is the
notLoaded-model window.) The window ends when the session loads.

## Non-goals

- Wire/protocol changes; daemon changes. Concretely: no timestamp on the
  wire's `PendingMutation` (cross-client stack order stays approximate, §4),
  no daemon-side recovery for the §5 race (open decision there), and no
  daemon-side failure notification for the §5 pre-settle failed-delivery
  stuck record (see §5 for the pre-existing-defect note).
- TUI and mobile-native parity (separate follow-ups).
- Cross-fade/morph animation between ghost and delivered item (the swap is
  positionally continuous — the ghost was the last row, the items land above
  it; no transition machinery).
- C's beacon wording beyond what the caption already borrows.

## Testing

Per `docs/developing-evener/testing.md` (TDD; deterministic; no live
provider). Component and unit tests, all vitest:

- `HeldSteerStack.test.tsx` — renders steer/drain/promote entries in order
  (known-`createdAt` ascending, unknown-`createdAt` after — the shared sort's
  own rule, §4); excludes `queue`/`send`; caption per
  state table (including both no-turn arms — accepted and submitting — the
  `[image]` placeholder, and the `[queued messages]` fallback the stack itself
  adds for a blank composed body); a skill-only entry renders the skill marker,
  not an empty bubble; the drain ghost's text refines to the authoritative
  combined input at the next hydrate; excludes `blockedUnknown`/`canceled`;
  disappears when the transcript reflects the id; a failed-delivery vanish
  (post-settle hydrate lacks the id) unmounts the ghost with one departure
  announcement; renders under the AskDock when both are present; another
  client's authoritative entry renders.
- `UserMessageItem.test.tsx` — `provisional` renders in the meta slot;
  absent prop renders exactly the current output (regression pin).
- `pendingEntries.test.ts` (package) — promote maps to `promote`; promote
  display input flows into the entry preview;
  `queueEntryPreviewText`/`skillMarkers` composition for skill-only and
  image-bearing entries; the id → `createdAt` carrier survives the hydrate
  settle within a page session (the durable record is gone; map reads
  still resolve) and `queueEntryPreviewText` still returns "" for
  contentless input (matching-key pin); `reconcilePendingEntries`' sort
  places known-`createdAt` entries first, ascending (the shared §4
  ordering).
- `pendingTurns.test.ts` (package) — `recordSubmittedHere` writes the id →
  `createdAt` map entry; the map is never pruned by the settle that deletes
  the durable record.
- `QueueStrip.test.tsx` — promote passes the row's text into the display
  (preview placeholder when the row's text is empty).
- `PendingChips.test.tsx` — steer/drain no longer chip; promote chips
  nothing today (`pendingMethod` has no promote mapping) and must stay
  chip-free once §3's mapping lands; send still does.
- `Session`/transcript integration — trailing row composition when
  `askPending`, when held steers exist, and when neither; `renderedRowCount`
  counts the ghost-only row so end-targeted scrolls land on it
  (steers-without-ask geometry, the one-row-short regression); a
  reload-mid-hold pass pins the degraded caption (no held count after a
  post-settle reload) and the unknown-`createdAt`-last rule, and the
  pre-settle reload's re-discovered `createdAt`; the new-content pill fires
  on ghost appearance for a scrolled-away reader (`heldEpoch` arrival bump)
  and never on a removal or the timer's cadence; the announce-once live
  region fires on held-then-delivered and held-then-departed transitions
  and stays silent on the timer's cadence (a11y assertions). Scope the
  `[queued messages]` query to the stack: QueueStrip's own tests query
  `/queued messages/i` against its section heading, and a Session-level test
  mounts both surfaces.

Gates: `make test-web` (typecheck + unit + Biome), `make test-web-browser` on
Chrome-capable hosts for geometry, `make lint`, `make vet`.

## Files (expected)

- `appwire-client/typescript/state/mutation/pendingEntries.ts` — promote map;
  the shared entry-body helper (§2) extracted here (chips and stack both
  call it); `reconcilePendingEntries`' sort becomes the §4 known-first
  ordering (queue rows and chips inherit it), and it reads the id →
  `createdAt` carrier into authoritative entries (the post-settle §4 join);
  its `submittedHere` parameter widens from a set to the map
- `appwire-client/typescript/state/mutation/pendingTurns.ts` —
  `submittedHere` set becomes the never-pruned id → `createdAt` map, written
  at `recordSubmittedHere`
- `cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts`
  — singleton state sites (durable-read scan, singleton reset) updated for
  the map type; the test-wipe reset re-initializes the map
- `cmd/evener-hub/frontend/src/stores/threads.ts` — `promoteQueuedAsSteer`
  signature widened for the display input (payload is hardcoded there today)
- `cmd/evener-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx` —
  promote display input (text + preview fallback)
- `cmd/evener-hub/frontend/src/panes/session/Session.tsx` — trailing row
  composition + the derived `renderedRowCount` + the `heldEpoch` pill signal
- `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerStack.tsx`
  + `.module.css` — new; owns the `[queued messages]` fallback label
- `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx`
  — `provisional` prop (caption text and provisional styling, one bit)
- `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerAnnouncements.tsx`
  — new; the announce-once live region outside the virtual list
  (AskDockAnnouncements pattern; §2). If extracting the announce-once
  primitive out of AskDock is clean at implementation time, prefer that —
  the pattern has no shared form today
- `cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.tsx` —
  send-only
- the matching test files
