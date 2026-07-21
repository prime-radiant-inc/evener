# Wave 5 integration wiring report — T6

Branch `w5-interaction`, starting tip `4eb55d47d` (the four merged streams:
composer core, queue strip, ask dock, session chrome). 5 commits, HEAD
`095877f6d`. This is threading work, not design work — every seam below was
specified by its own stream's report; my job was wiring `Composer.tsx` to
actually use them and closing the gaps each stream flagged as "the
integrator's job."

## Commits

| Commit | Unit |
|---|---|
| `262d3548a` | T6(1): barrel-export Dropzone widget |
| `0fd7db825` | T6(2): wire QueueStrip into Composer's T3 slot |
| `aff0208b2` | T6(3): pending-tracking uniformity across send/steer/queue/drain |
| `8e765aeac` | T6(4): wire AskDock into Composer's T4 slot |
| `095877f6d` | T6(5): full-tree integration sweep, retire the T3/T4 slot placeholder |

Commit range for review: `4eb55d47d..095877f6d`.

## Verification (re-run clean on final HEAD)

```
npx tsc --noEmit  → EXIT=0
npx vitest run    → EXIT=0 (136 test files, 2023 tests); on-disk `find` count
                     also 136 (no silent exclusion)
npm run lint      → EXIT=0 (biome ci src, 409 files, "No fixes applied.")
npm run build     → EXIT=0 (tsc --noEmit && vite build); dist/PLACEHOLDER
                     restored via `git restore` after every build gate,
                     confirmed via `git status` before every commit
```

Baseline before this work: 135 files / 2009 tests (matches the task brief
exactly). Net across all 5 commits: +1 file (`Composer.integration.test.tsx`),
+15 tests in that file, −1 test retired from `Composer.test.tsx` (see T6(5)),
net tests 2009 → 2023.

## What each commit did

**T6(1) — barrel lines.** Added the `Dropzone`/`DropzoneProps` export pair to
`widgets/index.ts` per the composer stream's documented lines, and
consolidated `Composer.tsx`'s own Dropzone import into the same barrel import
it already uses for every other widget (it was previously importing Dropzone
alone from `../../../widgets/dropzone`, a leftover from before the barrel
export existed). No new test: this is a pure re-export of an already-fully-
tested widget (`dropzone.test.tsx` + `Composer.test.tsx`'s own drag-drop
cases) through an additional import path — there is no new behavior for a
test to pin, and the TDD skill's own guidance treats a refactor like this as
protected by *existing* tests staying green, not as needing a new one
invented for the occasion. Verified by the full gate, not a dedicated test.

**T6(2) — QueueStrip into the T3 slot.** Mounted `<QueueStrip ref
getComposerText onRestoreToComposer onDrainSuccess />`. `getComposerText`
reads `textRef.current` (the synchronously-updated live-text mirror the
composer addendum calls out, not the `text` state closure) plus
`attachments.toInputAttachments()`. `onRestoreToComposer` → a new
`restoreTextToComposer` helper that ports `renderer.js:6823-6837`'s merge
byte-for-byte: existing text right-trimmed then kept, incoming text appended
after a blank line, `textEditor.write()` for the draft-sync + cursor-to-end,
then `.focus()`. `onDrainSuccess` → a new `handleDrainSuccess` that clears
text/draft/attachments unconditionally (the callback carries no snapshot to
check "unchanged since," unlike this component's own `clearIfUnchanged` —
see Concerns).

Caught a real regression here: `Composer.test.tsx`'s `steerButton()` test
helper (`getByRole("button", { name: /^steer\b/i })`) started matching TWO
buttons once a test's queue is non-empty — this component's own "Steer"
button and QueueStrip's own "Steer now" button, exactly the button-naming
overlap the queue stream's own report flagged as "worth a second look at
integration time." Fixed with a negative-lookahead regex
(`/^steer(?!\s*now\b)/i`), documented in place.

**T6(3) — pending-tracking uniformity.** `submitAction`'s send/queue/steer/
drain calls now go through `submitWithPendingTracking`, exactly mirroring
QueueStrip's own `handleDrain` — the wave's own beyond-parity decision that
optimistic pending apply uniformly across all four methods. Reconciled the
toast-vs-bookkeeping split per `pendingTurnsStore.ts`'s own documented
contract: the toast now lives entirely inside `onFailure`, and the outer
`catch` only re-runs the `queuedDrainPartial` state-clearing, never a second
toast — a failure surfaces exactly once regardless of which branch it takes.
Before this reconciliation both sides pushed a toast for the same failure.

Caught a real "test passes for the wrong reason" trap while writing this:
the first version of the send-failure and queuedDrainPartial-failure tests
asserted "0 pending entries" as their only pending-state check, which is
trivially true when no entry was ever registered at all — not proof of
removal-on-failure. Strengthened both to defer the RPC rejection via a
manually-controlled promise (matching `Composer.test.tsx`'s own in-flight
idiom) so the test asserts 1 entry while in flight, *then* 0 after — a test
that cannot pass without both registration and removal actually happening.

**T6(4) — AskDock into the T4 slot.** Mounted `<AskDock ref
onFallbackToComposer={restoreTextToComposer} />` above the queue strip.
`useAskDockPending(ref)` gates `hidden`/`inert` on both the chips row and the
input-card div (mirrors `renderer.js:5711-5736`'s `setComposerAskMode`, which
applies the same pair to the composer surface *and* the textarea
specifically). Legacy's additional submit-time `pendingAsk` guard
(`renderer.js:6479-6482`) is architecturally moot here and deliberately not
ported: it defends against the ask dock's own inputs living *inside* the
same `<form>` as the composer (an Enter keypress there could implicit-submit
the stale draft) — in this rewrite `AskDock` is a sibling of Composer's
`<form>`, never nested inside it, so there is no shared form to submit.

Caught a real test-fixture bug (not an implementation bug) here: the first
version of all three new ask-dock tests failed because this file's own
default `testThread` fixture (built for the queue/steer tests) pre-seeds an
already-open `turn_1`, and firing a real `turn/started` for that same id
(needed to get an acked `ask_user` item onto the model) appended a *second*,
colliding `turn_1` to `model.turns` — `reducer.ts`'s own `turn/started` case
unconditionally appends, no dedup. Fixed with an idle/no-turn override
matching `AskDock.test.tsx`'s own `hydrateWithOneAsk` convention.

**T6(5) — full-tree sweep.** Added the two scenarios not yet covered by
tasks 2-4's own tests: a DOM-order assertion that the ask dock renders above
the queue strip when both are visible simultaneously (temporarily swapped
the JSX to confirm this test actually fails on wrong order, then reverted —
substituting for "watch it fail" on a scenario whose underlying wiring
already existed from T6(2)/T6(4)), and a fully *live* queue round-trip (type
→ click Queue → `turn/queue` resolves → `thread/queueChanged` wire echo →
strip renders the real row → Edit restores it → `cancelQueued` fires with
`expectedEntryId`) — task 2's own tests exercised edit/cancel against a
pre-hydrated queue; this is the first test to drive the whole thing through
an actual queue *action*. Retired `Composer.test.tsx`'s "reserves marked
slots for T3/T4" test (it scraped `Composer.tsx`'s own source text for the
literal strings "T4: ask dock"/"T3: queue strip", written when there was
nothing else in the DOM to query) in favor of the new DOM-order test, now
that both slots render real content; removed its now-unused
`node:fs`/`node:path`/`node:url` imports.

All three integration-sweep scenarios named in the task brief (queue+edit+
cancel, ask+pending+answer, send-failure+one-toast+pending-fails) are
covered — built incrementally across commits T6(2)-T6(5) as each seam
landed, rather than written as one batch at the end; each was verified red
against its own missing wiring before being made green.

## Seam contradictions found

1. **`onRestoreToComposer`'s `attachments` parameter cannot be honestly
   "merged into pending-attachment state," and parity itself says it
   shouldn't be.** The task brief's own wording for scope item 2 says
   `onRestoreToComposer` should merge restored attachments into
   pending-attachment state. Two independent things I found while reading
   contradict implementing that literally: (a) `contracts-composer-queue-
   pending.md` line 70 and `parity-m5-composer.md` line 102 both say a
   queued entry's edit is a text-only recompose — dropped image attachments
   are surfaced as their own warning toast (QueueStrip's own
   `reportRemovedImages`), never restored to the composer; (b) mechanically,
   `useAttachments.ts`'s public API has no method to inject an
   already-encoded `InputAttachment` (only `ingestFiles(File[])`, which
   re-runs the whole canvas-PNG-encode pipeline from raw browser `File`
   objects) — there is no marker-minting or items-state-setter exposed
   outside the hook, and extending it for this would mean adding real surface
   to a module outside this integration's manifest for a parameter the queue
   stream's own report already confirms its one real call site
   (`QueueStrip.tsx`'s `handleEdit`) never populates. I implemented the
   `text` half in full (merge-after-blank-line, ported byte-for-byte from
   legacy) and left `attachments` an accepted-but-unused parameter, with a
   code comment at `restoreTextToComposer`'s call site citing both of the
   above. I did not stop and ask before proceeding since the correct
   behavior (don't restore images here) was independently confirmed by
   parity, not just inferred — but flagging it here as asked, since the
   brief's own wording and parity disagree.

2. **`onFallbackToComposer`'s "preserve current draft" instruction has no
   legacy citation — legacy's actual behavior is the opposite.** The task
   brief says the ask-dock fallback should preserve any current draft text.
   Legacy's own equivalent function, `dropComposedTextIntoComposer`
   (`renderer.js:6238-6245`), unconditionally overwrites (`ta.value = text`),
   discarding whatever was typed — a real, deliberate divergence from
   `restoreTextToComposer`'s (the OTHER legacy function, used by queue-edit)
   append-after-blank-line merge. I followed the brief's instruction (reused
   the queue-edit merge behavior for both seams) rather than porting the
   overwrite, since this rewrite's architecture gives the divergence a real
   justification legacy never had to reckon with: AskDock only hides/inerts
   the composer's input row, it never clears Composer's own `text` state, so
   a user's pre-ask draft survives underneath the whole time — overwriting
   it on a Conflict fallback would silently discard work the legacy
   composer never actually had sitting around to lose (its own DOM-morph
   world had different lifecycle guarantees). Documented at length in
   `restoreTextToComposer`'s own doc comment, and flagging it here since it
   is a considered choice diverging from the one legacy citation I could
   find, not a citation itself.

## Concerns

1. **Two independent "Steer"-labeled buttons is a real, user-visible
   surface, not just a test-selector nuisance.** QueueStrip's own report
   already flagged this as worth a second look; I hit it directly fixing
   `steerButton()`'s test collision. A session with both a non-empty queue
   and an active turn now shows a "Steer" button (this component, classic
   steer/drain-as-steer routing) *and* a "Steer now" button (QueueStrip,
   drain-as-steer only) doing overlapping things under different
   conditions. Not fixed here — no stream's contract calls for unifying
   them, and doing so unilaterally would be designing, not threading — but
   worth Jesse's eyes on whether that's the intended end-state UI.

2. **`onDrainSuccess`'s unconditional clear is a narrow, real asymmetry with
   this component's own drain path.** `submitAction`'s classic drain (Shift+
   Enter with a non-empty queue) only clears the composer if the text is
   *unchanged* since submit (`clearIfUnchanged`). QueueStrip's own "Steer
   now" button has no such guard available to it — `onDrainSuccess` takes no
   arguments, so there is no submitted-text snapshot to compare against. A
   user who edits the composer (text or a new attachment) while a
   QueueStrip-triggered drain is still in flight will have that edit
   silently cleared once the drain resolves, unlike the identical race
   through this component's own steer button. Fixable only by changing
   `QueueStripProps.onDrainSuccess`'s signature (outside this manifest), so
   flagged rather than fixed.

3. **`getComposerText` has no `hasPending` (mid-encode attachment) guard.**
   This component's own submit paths block send/steer/queue/drain with a
   toast while any attachment is still mid-encode. QueueStrip's "Steer now"
   button has no equivalent check before calling `getComposerText()`, so a
   drain triggered mid-encode would silently omit the not-yet-encoded image
   from the drained payload (`toInputAttachments()` filters incomplete items
   without signaling it). Low real-world likelihood (a narrow timing
   window), and the guard would belong in `QueueStrip.tsx`, outside this
   integration's manifest — flagged, not fixed.

4. **No "Message composer ready." status announcement for exiting ask-
   pending mode.** `AskDock.tsx`'s own header comment says explicitly "that
   is the composer's own surface to show/hide, and T2 owns it," naming a
   legacy behavior (`renderer.js:5731-5735`'s `composerModeStatusEl`
   toggling between "Answer the agent's questions."/"Message composer
   ready.") that this integration does not implement. I did not build it:
   no stream's contract (including my own task brief) specifies props,
   placement, or test expectations for it, and inventing an aria-live region
   unilaterally felt like designing a new surface rather than threading an
   existing one. `hidden`+`inert` on the input row itself is implemented and
   tested; the accessibility announcement half of the parity floor-row
   (`contracts-composer-queue-pending.md` line 184 area, `parity-m5-
   composer.md` line 118) is a real, verified gap, not an oversight.

5. **`dist/webassets/*` hashed filenames differ from this branch's tip on
   every build** (expected — content-hashed by vite, harmless) but
   `dist/PLACEHOLDER` is the one file actually tracked in git; restored via
   `git restore` after every build gate and confirmed via `git status`
   before every commit, per instructions.

No blocking issues. The four merged streams' own seam documentation was
internally consistent with what their code actually exposes in every case I
checked except the two flagged above (both resolved in favor of parity/the
brief's explicit instruction, with reasoning left in code comments at the
point of divergence).
