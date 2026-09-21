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
window — potentially minutes — the web UI shows either a dim "Steering…" chip
that does not say what it waits for (direct steers), or nothing at all
(promote/drain: the queue row vanishes on `thread/queueChanged` and no pending
artifact this client owns exists for the promoted message). This spec makes
the held message visible, in place, until it is delivered.

## Decision

The held steering message renders in the transcript, as the message it will
become, in a provisional register — dashed accent edge, reduced opacity, and a
caption in the timestamp slot reading `Delivers when this step finishes ·
held 0:42`. This reverses the recorded ruling that kept optimistic items out
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
`renderRows.length + (askPending ? 1 : 0)`) — gains the same
`(askPending || heldSteers.length > 0)` condition. Its own comment warns that
an uncounted trailing row "lands one row short, leaving the answering surface
below the viewport"; without this, jump-to-bottom and append-follow leave the
ghost partially below the fold — the feature's payload, invisible in exactly
the documented failure mode.

Second companion change, same reasoning that produced the AskDock's signals:
a ghost appearing while the reader is scrolled away must fire the new-content
pill, but the pill's edge detector keys off turn and item shape primitives and
never sees a trailing row appear. Session.tsx therefore feeds the pill two
explicit signals — a `heldCount` and a `heldEpoch` — mirroring `askDockPending`
/ `askDockActivationEpoch`. As with that epoch, arrival is the only edge:
`heldEpoch` bumps when held steering appears (a new id joins the stack, or the
count leaves zero) and never on removal — departures are surfaced by the
announce-once region below, not by a "new content" bump. (The content-changed
pill effect keys on item-count/first-turn/failed-turn primitives, so a
canceled or rejected departure — which moves none of them — reaches a
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
via `UserMessageView`. Stack order (§4): entries with a known `createdAt`
first, ascending; entries without one after them, keeping the hydrate's
array order:

- **`UserMessageView` gains one optional prop**: `provisionalMeta?: string`,
  rendered in the timestamp slot instead of the item time. No other change to
  that component; delivered messages render exactly as today.
- The ghost's provisional styling is a `variant="provisional"` of
  `UserMessageView` (one more optional prop, applying a class from
  `usermessageitem.module.css`): dashed `--accent` bubble edge, the stack at
  `opacity: 0.78`, and the dashed left railmark from mockup A. Tokens only —
  no new colors, so no token-contract allowlist work.
- `opensExchange` is always false: a held steer joins work under way.
- **Scope — every client's held steering renders.** All of it lands in this
  shared transcript, so the stack shows `pendingTurnEntries` for the session,
  not only this client's own. Known asymmetry, honestly scoped: this client's
  ghosts appear instantly (its own durable record), and — because the durable
  outbox is shared per origin while the client identity is per tab — so does
  another tab's, carrying its `createdAt` from the shared record. A remote
  client's entry (no shared storage) surfaces only when a hydrate reports it
  through `pendingMutations`, timestampless.
- **Ghost body, all methods — the chips' own composition.** The body is
  what the pending chips it replaces showed: the
  `queueEntryPreviewText(entry.text, entry.imageCount)` preview line plus
  `skillMarkers(entry.skillNames)` when the entry carries skills. An
  image-only promote reads the daemon's own placeholder (`[image]` /
  `[N images]`, carried by `queue.preview` and passed by `handlePromote`
  when the row's text is empty); a skill-only steer or drain (empty
  composer, a skill chosen) reads the skill marker instead of an empty
  bubble. One label is new UI copy this spec defines, because the
  composition yields a blank for it and nothing in the code produces one
  today: an empty-composer drain shows `[queued messages]` (its optimistic
  entry carries only the composer's empty input until the first hydrate
  brings the daemon's combined text). HeldSteerStack owns that fallback
  label — `queueEntryPreviewText` itself stays untouched (it is a matching
  key between queue rows and pending rows, and must keep returning "" for
  contentless input). Stated limitation: a ghost cannot render images —
  the wire's `pendingMutations` entry and the client's durable record carry
  an image count, not image data — so an image-bearing steer shows its text
  or placeholder plus the count, and the image itself first appears on
  delivery.
- **Non-visual register.** The caption is real text in the row, read in flow
  by assistive tech; the dashed edge and opacity are decoration only.
  Appearance, delivery, and a departure without delivery (rejected, canceled,
  blockedUnknown, or failed — §5) are announced exactly once each through a
  live region outside the virtual list (the `AskDockAnnouncements` pattern),
  never on the held-timer's cadence. Follow-up surfaces, honestly: the
  departures that move the record to a terminal row (rejected → recovery,
  canceled, blockedUnknown) point at that QueueStrip row; the failed-at-
  delivery departure leaves no row anywhere (§5) — the announcement is the
  ghost's only trace, and the failed turn's error is the explanation.

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
| `submitting` | turn running | `Joining this turn · 0:00` |
| `submitting` | no turn (dispatch straddled the turn boundary, stalled, or an idle drain of a parked queue) | `Delivers with the next turn · 0:00` |
| `accepted` | turn running | `Delivers when this step finishes · held m:ss` |
| `accepted` | no turn (turn ended while held; the daemon opens the steering-carrier turn on the next run — a Stop parks steering behind the `SteeringHeld` gate until a user-initiated run clears it) | `Delivers with the next turn · held m:ss` |

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
`active`; the no-turn arms are everything else.

`m:ss` is computed from `createdAt` against the transcript's existing
`SessionNowContext` cadence — 3 s granularity, accepted: the caption reads
the same clock the liveness line does, no per-second machinery re-renders the
virtualized row, and it is never an `aria-live` region that ticks.

`createdAt` survival across hydrate: a published hydrate settles the outbox
record — `settleApplied` deletes it from durable storage — and replaces it
with the authoritative `pendingMutations` entry, which carries no timestamp,
so no replace-time lookup can inherit anything; the record is already gone.
The carrier is the mechanism `fromThisClient` already uses:
`pendingTurnsStore`'s `submittedHere` set is written at
`recordSubmittedHere` and never pruned, so it survives the settle. It
becomes a never-pruned id → `createdAt` map, written at the same
`recordSubmittedHere` site. Honest scope: the map is in-memory
page-session state (the store is deliberately framework-free, no storage),
so it carries `createdAt` across hydrates but not across reloads.
`createdAt` is known while (a) the durable record exists — which includes a
reload before the first post-acceptance hydrate, where the durable read
re-discovers the record with its `createdAt` — or (b) the map holds the id
(post-settle, same page session). A reload after the settle loses it: the
caption omits `held m:ss` entirely — never `NaN`, never a false `0:00` —
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
not guaranteed across snapshots. Same-millisecond submissions tie-break by
`intentSequence`: `PendingTurnEntry` gains `intentSequence?: number`,
populated in `outboxEntry` from the durable record (authoritative entries
have none and degrade to the hydrate order above — only a sub-millisecond
double-submit is affected). This entry-model change is listed in Files; a
wire timestamp (or sequence) on `PendingMutation` would pin cross-client
order and is a non-goal here. A reload-mid-hold test pins the degraded
caption (no `held m:ss` after a post-settle reload), the
unknown-`createdAt`-last rule, and the pre-settle reload's re-discovered
`createdAt`.

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
window, accepted: on a not-yet-live surface (app boot before the first
hydrate) neither the removed chips nor the live-gated ghost render, so a
durable held steer is invisible for the seconds until the first hydrate loads
the session — the same window in which the chips were the only surface today.

## Non-goals

- Wire/protocol changes; daemon changes. Concretely: no timestamp on the
  wire's `PendingMutation` (cross-client stack order stays approximate, §4),
  no daemon-side recovery for the §5 race (open decision there), and no
  daemon-side failure notification for the §5 pre-settle failed-delivery
  stuck record (pre-existing; today's chip has the same defect).
- TUI and mobile-native parity (separate follow-ups).
- Cross-fade/morph animation between ghost and delivered item (the swap is
  positionally continuous — the ghost was the last row, the items land above
  it; no transition machinery).
- C's beacon wording beyond what the caption already borrows.

## Testing

Per `docs/developing-evener/testing.md` (TDD; deterministic; no live
provider). Component and unit tests, all vitest:

- `HeldSteerStack.test.tsx` — renders steer/drain/promote entries in order
  (known-`createdAt` ascending, unknown-`createdAt` after, hydrate order
  within, `intentSequence` tie-break); excludes `queue`/`send`; caption per
  state table (including both no-turn arms — accepted and submitting — the
  `[image]` placeholder, and the `[queued messages]` fallback the stack itself
  adds for a blank composed body); a skill-only entry renders the skill marker,
  not an empty bubble; the drain ghost's text refines to the authoritative
  combined input at the next hydrate; excludes `blockedUnknown`/`canceled`;
  disappears when the transcript reflects the id; a failed-delivery vanish
  (post-settle hydrate lacks the id) unmounts the ghost with one departure
  announcement; renders under the AskDock when both are present; another
  client's authoritative entry renders.
- `UserMessageItem.test.tsx` — `provisionalMeta` renders in the meta slot;
  absent prop renders exactly the current output (regression pin).
- `pendingEntries.test.ts` (package) — promote maps to `promote`; promote
  display input flows into the entry preview;
  `queueEntryPreviewText`/`skillMarkers` composition for skill-only and
  image-bearing entries; the id → `createdAt` carrier survives the hydrate
  settle within a page session (the durable record is gone; map reads
  still resolve) and `queueEntryPreviewText` still returns "" for
  contentless input (matching-key pin); `outboxEntry` populates the new
  `intentSequence` field for the §4 tie-break.
- `pendingTurns.test.ts` (package) — `recordSubmittedHere` writes the id →
  `createdAt` map entry; the map is never pruned by the settle that deletes
  the durable record.
- `QueueStrip.test.tsx` — promote passes the row's text into the display
  (preview placeholder when the row's text is empty).
- `PendingChips.test.tsx` — steer/drain/promote no longer chip; send still
  does.
- `Session`/transcript integration — trailing row composition when
  `askPending`, when held steers exist, and when neither; `renderedRowCount`
  counts the ghost-only row so end-targeted scrolls land on it
  (steers-without-ask geometry, the one-row-short regression); a
  reload-mid-hold pass pins the degraded caption (no `held m:ss` after a
  post-settle reload) and the unknown-`createdAt`-last rule, and the
  pre-settle reload's re-discovered `createdAt`; the new-content pill fires
  on ghost appearance for a scrolled-away reader (`heldEpoch` arrival bump)
  and never on a removal or the timer's cadence; the announce-once live
  region fires on held-then-delivered and held-then-departed transitions
  and stays silent on the timer's cadence (a11y assertions).

Gates: `make test-web` (typecheck + unit + Biome), `make test-web-browser` on
Chrome-capable hosts for geometry, `make lint`, `make vet`.

## Files (expected)

- `appwire-client/typescript/state/mutation/pendingEntries.ts` — promote map;
  `queueEntryPreviewText`/`skillMarkers` reuse for the ghost body;
  `PendingTurnEntry` gains `intentSequence?` (populated in `outboxEntry`) for
  the §4 tie-break
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
  composition + `renderedRowCount` condition + `heldCount`/`heldEpoch` pill
  signals
- `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerStack.tsx`
  + `.module.css` — new; owns the `[queued messages]` fallback label
- `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx`
  — `provisionalMeta` prop
- `cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.tsx` —
  send-only
- the matching test files
