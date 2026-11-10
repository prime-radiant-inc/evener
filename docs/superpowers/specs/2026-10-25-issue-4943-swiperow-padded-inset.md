# Issue #4943: SwipeRow padded call sites and the left edge band

**Status:** Approved after two PAR rounds; implemented and verified.
**Base:** ad886c6e89 (origin/main, 2026-10-25)

## Scope and acceptance

Issue #4943 is the follow-up to #2703/PR #4944. `SwipeRow` now measures the
screen's 24pt left edge band from the row's own frame plus its `edgeInset`, but
three call sites render the row inside a horizontally padded container and pass
no inset. Their pans start 24pt inside their own frame, which is 24pt past the
container's pad, so they refuse swipes that begin between screen x=24 and the
row's frame plus 24.

Acceptance: a pan on the Session transcript's ghost rows (16pt gutter), the
Queue sheet's message rows (16pt pad) and the Notes & links sheet's link rows
(20pt pad) begins at screen x=24; unpadded rows keep exactly
`hitSlop.left = -24`; the Board's depth-0, depth-1 and depth-2 behavior, every
swipe direction and every swipe threshold are unchanged.

Authorized scope:
`mobile-native/src/session/transcriptLayout.ts` (new, one constant),
`mobile-native/src/session/GhostBubble.tsx`,
`mobile-native/src/session/GhostRows.tsx`,
`mobile-native/src/screens.tsx` (the transcript's gutter constant only),
`mobile-native/src/QueueSheet.tsx`,
`mobile-native/src/session/NotesSheet.tsx`, their tests, and this spec.
`SwipeRow.tsx`, `swipeEdge.ts` and the Board already carry the arithmetic this
issue uses; they need no change.

## The defect

`SwipeRow` applies `edgeBandHitSlop(edgeInset)` to both its release tracker and
`ReanimatedSwipeable`. With the default inset it is `{ left: -24 }`, measured
inward from the row's own left edge. On current main the three padded call sites
leave the default in place:

| Call site | Container pad | Today's slop | Pan begins at screen | Dead strip |
|---|---|---|---|---|
| Session transcript ghost (`GhostBubble.tsx:177`) | `screens.tsx` FlatList `padding: 16` | `-24` | 40 | [24, 40) |
| Queue sheet rows (`QueueSheet.tsx:67,71`) | ScrollView `padding: 16` | `-24` | 40 | [24, 40) |
| Notes & links link rows (`NotesSheet.tsx:92,453`) | ScrollView `paddingHorizontal: 20` | `-24` | 44 | [24, 44) |

The screen's left 24 points must stay with the system's back gesture, so the
correct trim is `inset - 24`: `-8` for a 16pt pad, `-4` for a 20pt pad. The
`startsInEdgeZone(pageX)` guard remains the second defense for any future
caller that cannot supply its inset. The transcript's `SafeAreaView` and the
two full-width formSheet routes report a zero left inset on the pinned portrait
iPhone-only app (`app.config.js:23,28`), so their content edge is the screen's;
a future orientation or tablet target would need the safe-area or container
edge folded in.

## Chosen fix: thread each container's horizontal inset

`SwipeRow`'s existing `edgeInset` prop and `edgeBandHitSlop` are the right
arithmetic. This issue supplies the inset at the three padded call sites and
keeps one source of truth for each pad:

- `session/transcriptLayout.ts` exports `TRANSCRIPT_GUTTER_PT = 16`, the
  transcript's horizontal gutter. Both `screens.tsx` (the list's
  `paddingHorizontal`) and `GhostRows.tsx` (each ghost row's `edgeInset`)
  import it, so the pad and the band cannot drift apart.
- `GhostBubble` gains a required `edgeInset` and forwards it to `SwipeRow`.
  Both of its production callers are padded and must state their inset; a new
  caller cannot silently fall back to `{ left: -24 }`.
- `QueueSheet.tsx` names its content pad `QUEUE_SHEET_GUTTER_PT = 16`. The
  ScrollView keeps `padding: QUEUE_SHEET_GUTTER_PT` (all four sides, as today),
  and each `GhostBubble` gets that value as `edgeInset`.
- `NotesSheet.tsx` names its content pad `NOTES_SHEET_GUTTER_PT = 20`. The
  ScrollView keeps `paddingHorizontal: NOTES_SHEET_GUTTER_PT`, and each link
  row's `SwipeRow` gets that value as `edgeInset`.

Resulting effective slop:

| Row | Inset | `hitSlop` | Pan begins at |
|---|---|---|---|
| Plain `SwipeRow` / Board depth 0 | 0 | `{ left: -24 }` | 24 |
| Transcript ghost | 16 | `{ left: -8 }` | 24 |
| Queue sheet row | 16 | `{ left: -8 }` | 24 |
| Notes & links link row | 20 | `{ left: -4 }` | 24 |
| Board depth 1 | 16 | `{ left: -8 }` | 24 |
| Board depth 2+ | 32+ | `undefined` | frame |

Option considered and rejected: measure the row's window x inside `SwipeRow`.
That adds an asynchronous native measurement and rebuilds the release tracker
when the measurement lands, for a value every padded container already knows
statically. The #2703 contract is an explicit `edgeInset`; this issue completes
its call sites.

## Non-goals and preservation

No row restyle, layout change, new swipe action, direction or threshold change.
No change to `SwipeRow`, `swipeEdge`, the Board, or the `pageX` guard. The
transcript's `padding: 16` becomes `paddingHorizontal: TRANSCRIPT_GUTTER_PT`;
`paddingTop` and `paddingBottom` already override the other sides, so geometry
is unchanged. The Queue sheet keeps its all-sides `padding: 16`. The hit slop
now excludes the screen's band at every padded site, so no pan begins inside
it; the unchanged `startsInEdgeZone` guard stays the second defense for a
future caller that cannot supply its inset. The issue's "a sheet has no system
back gesture to protect" explains why the strip outside the band is waste
there; it does not change `SwipeRow`'s product rule that the screen's left 24
points never act on any row.

Simulator-only limit: vitest proves the props and the effective slop, not
UIKit's arbitration between a row's pan and the system back gesture. No
simulator check is claimed here.

## Verification

Red-first, before the fix:

1. `mobile-native/src/session/GhostRows.test.tsx` — a transcript ghost row's
   swipeable and release tracker carry `{ left: -8 }`. Fails today with
   `{ left: -24 }`.
2. `mobile-native/src/QueueSheet.test.tsx` — the ScrollView's
   `contentContainerStyle.padding` is 16, and every message row's swipeable and
   tracker carry `{ left: -8 }`. The slop fails today with `{ left: -24 }`.
3. `mobile-native/src/session/NotesSheet.test.tsx` — the ScrollView's
   `contentContainerStyle.paddingHorizontal` is 20, and every link row's
   swipeable and tracker carry `{ left: -4 }`. The slop fails today with
   `{ left: -24 }`.
4. `mobile-native/src/ConversationScreen.send.test.tsx` — the transcript list's
   `contentContainerStyle.paddingHorizontal` is 16 (today the key is `padding`,
   so the new key is `undefined`), and its queued ghost's swipeable and tracker
   carry `{ left: -8 }` (today `{ left: -24 }`). That suite gets both gesture
   mocks from `conversationPlatform.testkit.tsx:42-47`, so the inert
   `ReanimatedSwipeable` host and the tracker's `.config` are present.

Existing coverage stays green: `SwipeRow.test.tsx` keeps the unpadded
`{ left: -24 }` case and the `edgeInset` cases, and `BoardScreen.test.tsx` keeps
depth 1 at `{ left: -8 }` and depth 2 at `undefined`.

After the fix, from `mobile-native` (the runner applies the native worker
budget):

```sh
sh ../scripts/native/run-tests.sh src/board/SwipeRow.test.tsx \
  src/board/swipeEdge.test.ts src/board/BoardScreen.test.tsx \
  src/session/GhostRows.test.tsx src/QueueSheet.test.tsx \
  src/session/NotesSheet.test.tsx src/ConversationScreen.send.test.tsx
npm run check
npm run check:scripts
npx biome ci src/session/transcriptLayout.ts src/session/GhostBubble.tsx \
  src/session/GhostRows.tsx src/screens.tsx src/QueueSheet.tsx \
  src/session/NotesSheet.tsx src/session/GhostRows.test.tsx \
  src/QueueSheet.test.tsx src/session/NotesSheet.test.tsx \
  src/ConversationScreen.send.test.tsx
```

From the repository root, `make native-preflight` proves the native install and
`make test-native-bundle` proves Metro still resolves the real iOS entry graph
with the new module. `npm run check` is this repository's native TypeScript gate
(`tsc --noEmit --project tsconfig.check.json`); `mobile-native` has no
`typecheck` script. `make build` builds the Go runtime and embedded web UI and
does not read `mobile-native`, so it does not participate in this change.

## Review and delivery

Freeze this spec for two independent competing PAR reviewers. Resolve
legitimate findings. Then implement red-first, PAR the base/head diff, run
simplify-code, commit one change, run branch RoboRev, push a PR against `main`,
and hand Jesse the PR URL, exact head and settle command. Do not merge.
