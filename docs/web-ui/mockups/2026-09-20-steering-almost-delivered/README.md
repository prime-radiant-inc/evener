# Steering "almost delivered" — three UX directions

Status: mockups for review, not an implemented change. Branch `wip/steering-almost-delivered-ux`.

## The problem, precisely

When the user steers mid-run, the daemon accepts the message immediately but
delivers it to the model only at the next injection boundary — the end of the
current model stream plus the current tool round
(`injectPostToolSteering` → `injectDrainedSteering` → `consumeSteeringMessage`,
which then emits `evener/steering/injected`). That window can be minutes long
on a slow tool call or a long thinking block.

What the UI shows during that window has real gaps:

- **Direct steer** (composer Steer button): a dimmed `Steering` chip beside
  the composer (`PendingChips`, optimistic record state `accepted`). It exists,
  but reads as "sending in progress," not "waiting for a boundary," and is easy
  to miss when the eye is on the transcript.
- **Promote** (`turn/promoteQueuedAsSteer` on a queued row): the queue row
  vanishes (`thread/queueChanged`) and — while the client does own the
  durable record, `pendingMethod` has no promote mapping, so the record
  renders nothing, and the daemon's authoritative `pendingMutations`
  snapshot is only written at hydrate — *nothing* shows the message again
  until `evener/steering/injected` fires. Total vanish, potentially minutes.
- **Drain** (`turn/drainAsSteer`): a dim "Draining" chip with the composer
  text only — the drained rows' content never shows, and the chip says
  nothing about what it waits for.
- **Handoff races**: the injected notification settles the chip and appends the
  transcript item in the same frame, but the reducer drops the item when
  `activeTurnId` is already gone (comment in `appwire-client/typescript/reducer.ts`),
  so the settle and the render can come apart on turn boundaries.

What the frontend already receives (all three ideas build only on this — no
wire changes):

1. The pending entry itself: method `steer`/`drain`, state
   `submitting` → `accepted`, with text and skill markers.
   (`createdAt` rides the client's own durable record; the daemon's
   authoritative `pendingMutations` entry carries no timestamp — the
   chosen direction's spec §4 carries the timer's full story. Steering
   never enters `claimed` — the daemon keeps a client steer
   `accepted` until its transcript append lands; `claimed` belongs to the
   start/queue methods only.)
2. The active turn's live items — the running tool call's name and status, the
   streaming thinking/text — i.e. *the thing the steer is queued behind*.
3. `evener/steering/injected` with `clientMutationId` — the delivery moment.
4. Reflection evidence: the injected item lands in the transcript with its
   `clientMutationId`, which is what settles any pending artifact on fact,
   not guesswork.

## Shared foundation (required by all three)

Every path that puts a message into steering — direct steer, promote, drain
(a recovery resend rides the same steer record) — must produce a pending
artifact this client owns, settled
only by transcript reflection. That closes the true vanish (promote/drain) and
gives all three directions the same input. It is frontend-only work: seed the
local pending record from the action's own knowledge; the daemon already
carries the ids needed to settle.

---

## Idea A — Ghost at the live edge

**Thesis:** show the message itself, exactly where it will land, in a
"not-yet-real" register.

The held steer renders *in the transcript*, directly below the currently
running tool call — the precise slot the daemon will upsert into
(`item_steering_live_<turn>_<n>` is appended to the active turn's items). It is
the exact component it will become — avatar, "You" header, serif accent
bubble — but dashed accent edge, reduced opacity, and where the timestamp
would go: `Delivers when this step finishes · held 42s`. On
`evener/steering/injected` the ghost unmounts and the real item renders in
the same place, in the same shape — a plain swap, no transition machinery.
The message never moves.

- Rendered as a live-edge element under the turn block (not inside the
  virtualizer's item list), respecting the documented constraint that kept
  optimistic items out of the transcript (`PendingChips.tsx` header records
  that call). This direction reverses that ruling deliberately: the vanish
  complaint is exactly the cost of that choice.
- Multiple held steers stack — this client's in submission order (another
  client's order is approximate; the spec's §4 says why) — so the user sees
  the queue *as* messages.

**Tradeoffs.** Strongest: zero ambiguity, nothing to learn — the message is
simply visibly queued behind the running step. Weakest: it reverses a
recorded presentation decision, and an optimistic element in the reading
surface must be unmistakably provisional (dashed + caption) or it reads as
already-delivered. Reload is a non-issue: the hydrate re-reports the held
steer in `pendingMutations`, so the ghost re-renders — with its held timer if
the reload landed before the first hydrate settled the durable record;
without it only after a post-settle reload (which wipes the in-memory
carrier along with everything else). It settles on reflection.

## Idea B — Held at the boundary (an explicit steering queue)

**Thesis:** keep the chips' location, make them say what they mean.

The chip strip becomes a small queue of held rows. Each row: status word
`Held`, reason `delivers when this step finishes` (or `…when this response
ends`, during a thinking block), a mono `held 0:42` counter (the same 3 s
cadence the liveness line ticks at), queue position `1st` / `2nd`, and an
ellipsized preview. (The row-level ✕ in the mockup is schematic: no wire
method can withdraw accepted steering today, so a real dismiss would be new
protocol work, outside this direction's frontend-only premise.) Every
steering-producing action seeds a row (the shared foundation), so promote and
drain become visible here too. The composer's Steer button grows a held-count
badge so the queue is discoverable even when the footer is out of view.

**Tradeoffs.** Cheapest to ship — an extension of an existing component, and
it keeps the transcript clean (the documented constraint stands). But it shows
the *status* of the message where the user isn't necessarily looking; it never
shows where the message will land; a badge is mitigation, not a fix, for the
scrolled-up case.

## Idea C — Boundary beacon

**Thesis:** annotate the blocker instead of the message.

The running tool call itself carries the waiting state: beneath it, a quiet
pill — `◇ 2 messages steer this turn when this finishes` — with the steering
diamond, a count, and an expand affordance. Expanding lists the held messages
with their texts and held times. At the boundary the pill dissolves in the
same frame the injected message appears beneath it: the causal chain
(running step → waiting messages → delivered) is visible as one motion. If the
turn ends without a boundary, the beacon migrates to the liveness line
(`Steering next turn: 1 message`) until the carrier turn opens.

**Tradeoffs.** Most explanatory — it teaches the boundary rule at the exact
place and moment it matters, and it's the least ink in the transcript. But the
message text is one interaction away (hover/click), so it's the least literal
answer to "show the message"; and it introduces a new adornment family on tool
items that must stay quiet enough not to read as a result or an error.

---

## Recommendation

**A**, on top of the shared foundation. Jesse's complaint is that the message
*vanishes* — the fix should put the message where the eye already is, in the
place it will actually appear. B is the safe minimum and would be worth
shipping on its own if the transcript ghost is ruled out; C's beacon wording
(`…when this finishes`) is worth stealing into A's caption and B's reason line
either way.

## Files

- `idea-a-ghost-at-live-edge.html`
- `idea-b-held-queue.html`
- `idea-c-boundary-beacon.html`

Each is a self-contained 1440×900 dark frame of the *same* session state (two
steers held behind a running `rg` tool call, 42 and 7 seconds in), rendered with
the product's real tokens, plus a three-frame storyboard of the lifecycle:
pressed → held → delivered.
