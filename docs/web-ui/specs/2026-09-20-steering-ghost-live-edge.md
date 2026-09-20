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

### 2. HeldSteerStack component

New `panes/session/transcript/messages/HeldSteerStack.tsx` (+ module.css).
Reads `usePendingTurnEntries(ref)` and renders every entry whose method is
`steer`, `drain`, or `promote` (see §3) in `createdAt` order, one per row,
each reusing the user-message structure via `UserMessageView`:

- **`UserMessageView` gains one optional prop**: `provisionalMeta?: string`,
  rendered in the timestamp slot instead of the item time. No other change to
  that component; delivered messages render exactly as today.
- The ghost's provisional styling is a `variant="provisional"` of
  `UserMessageView` (one more optional prop, applying a class from
  `UserMessageItem.module.css`): dashed `--accent` bubble edge, the stack at
  `opacity: 0.78`, and the dashed left railmark from mockup A. Tokens only —
  no new colors, so no token-contract allowlist work.
- `opensExchange` is always false: a held steer joins work under way.

### 3. Foundation — a pending artifact for every steering path

- **Direct steer** — already exists (optimistic record, method `steer`).
- **Drain** — already exists (`QueueStrip.handleDrain` wraps
  `submitWithPendingTracking`, method `drain`). Known limitation, accepted: the
  drain ghost shows the composer text; the drained queue rows' texts are
  combined daemon-side and are not echoed to the client.
- **Promote** — the client already owns the identity: `promoteQueuedAsSteer`
  goes through `enqueueMutation`, which writes a durable outbox record with
  the mutation id the daemon carries onto the pending steering
  (`clientMutationPromote` → `addPendingSteering`). Two changes make it
  renderable:
  1. `pendingEntries.ts`: `pendingMethod` maps `turn/promoteQueuedAsSteer` to a
     new `PendingMethod` `"promote"` (its own method, so labels and tests stay
     honest; not folded into `steer`).
  2. `QueueStrip.handlePromote` passes the row's full text (it already holds
     `fullText`/`entrySkillNames` at the press site) into the `enqueueMutation`
     `optimisticDisplay` input, so the ghost has content to show.

### 4. Caption state table

| Entry state | Session state | Caption |
|---|---|---|
| `submitting` | any | `Joining this turn · 0:00` |
| `accepted` | active turn running | `Delivers when this step finishes · held m:ss` |
| `claimed` (turn ended; daemon claimed the steering-carrier turn) | no active turn | `Delivers with the next turn · held m:ss` |

`m:ss` is computed from `createdAt` against the transcript's existing
`SessionNowContext` cadence (no new timer machinery, and never an
`aria-live` region that ticks). The turn-running arm keys off
`model.activeTurnId`, not status type.

### 5. Settle semantics — unchanged, and the race, documented

The ghost disappears when the entry leaves `usePendingTurnEntries`:
reflection in the transcript (reconcilePendingEntries' `reflectedMutationIds`)
and the identity settle (`handleNotification` →
`dispatcher.reconcileIdentities`) on `evener/steering/injected`, which fires
with the same `clientMutationId` in the same frame the reducer appends the
item.

Known race, accepted: the reducer drops the item when `activeTurnId` is
already gone (turn settled between injection and delivery), while the identity
settle still drops the ghost. The message reappears at the next authoritative
snapshot, which the daemon's steering-carrier turn provokes immediately. We do
NOT withhold identity settlement for steer-family methods: the
uncertain-send/ recovery machinery settles on positive evidence, and the
injected event is that evidence — weakening it trades a narrow visual race for
real recovery regressions.

### 6. PendingChips

The chip strip keeps `send` only. Steer/drain/promote chips are removed — the
ghost is the single surface for held steering (one surface, no double-read).
Blocked/canceled durable rows keep their QueueStrip homes, unchanged.

## Non-goals

- Wire/protocol changes; daemon changes (none needed).
- TUI and mobile-native parity (separate follow-ups).
- Cross-fade/morph animation between ghost and delivered item (the swap is
  positionally continuous — the ghost was the last row, the items land above
  it; no transition machinery).
- The drain combined-text display and the no-active-turn race (§5).
- C's beacon wording beyond what the caption already borrows.

## Testing

Per `docs/developing-evener/testing.md` (TDD; deterministic; no live
provider). Component and unit tests, all vitest:

- `HeldSteerStack.test.tsx` — renders steer/drain/promote entries in order;
  excludes `queue`/`send`; caption per state table; excludes
  `blockedUnknown`/`canceled`; disappears when the transcript reflects the id;
  renders under the AskDock when both are present.
- `UserMessageItem.test.tsx` — `provisionalMeta` renders in the meta slot;
  absent prop renders exactly the current output (regression pin).
- `pendingEntries.test.ts` (package) — promote maps to `promote`; promote
  display input flows into the entry preview.
- `QueueStrip.test.tsx` — promote passes the row's text into the display.
- `PendingChips.test.tsx` — steer/drain/promote no longer chip; send still
  does.
- `Session`/transcript integration — trailing row composition when
  `askPending`, when held steers exist, and when neither.

Gates: `make test-web` (typecheck + unit + Biome), `make test-web-browser` on
Chrome-capable hosts for geometry, `make lint`, `make vet`.

## Files (expected)

- `appwire-client/typescript/state/mutation/pendingEntries.ts` — promote map
- `cmd/evener-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx` —
  promote display input
- `cmd/evener-hub/frontend/src/panes/session/Session.tsx` — trailing row
  composition
- `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerStack.tsx`
  + `.module.css` — new
- `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx`
  — `provisionalMeta` prop
- `cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.tsx` —
  send-only
- the matching test files
