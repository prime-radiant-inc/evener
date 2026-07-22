# W5 close fix F2 report — Composer/QueueStrip cluster + docs

Branch `w5f-composer`, based at `2510b8adc`. 8 commits, HEAD `7a1b8e0b5`. Commit range for
review: `2510b8adc..7a1b8e0b5` (my own commits: `b07832dd7..7a1b8e0b5`).

## Verification (final, clean)

```
npx tsc --noEmit  → EXIT=0
npx vitest run    → 136 test files, 2031 tests, all passing (baseline 136 files / 2023 tests)
npm run lint      → EXIT=0 (biome ci src, 409 files, "No fixes applied.")
npm run build     → EXIT=0 (tsc --noEmit && vite build); dist/PLACEHOLDER restored via
                     `git restore` after the build gate, confirmed clean via `git status`
```

Net across all 8 commits: +8 tests (2023 → 2031) — items 2, 3+4 (one test covers both), 5, 7, 8
each added one test; item 6 added three; item 9 strengthened an existing test in place (no new
test). Every behavior-changing item below was RED-verified before its fix except item 5 (disclosed
below), and every regression net was mutation-verified (defect reintroduced, confirmed to bite,
reverted, confirmed zero net diff against the working tree) — full transcript of each cycle is in
the commit-by-commit narrative below; only the outcomes are repeated here.

**Observed, pre-existing flake (not mine to fix, but two process notes below):** across roughly 20
full-suite runs during this work, 2 showed a single failure each, both times in
`src/protocol/tokenFlood.test.tsx`'s "500 deltas on a live item do not re-render an already-settled
sibling item in the same turn - render-count probe" test (`Error: Test timed out in 5000ms`) - a
file entirely outside this stream's manifest (streaming/session-mount stress test, not composer/
queue), explicitly self-described elsewhere in the same file as a "tripwire, not a perf gate."
Consistent with the sibling flake-investigation stream's scope per the brief; not chased further.
An earlier draft of this report misattributed one instance to `TurnBlock.test.tsx` from a
truncated glimpse of tail output - that text was actually a comment fragment quoted *inside*
`tokenFlood.test.tsx`'s own failure context, not a separate failing file. Corrected here after
reproducing the flake twice more with the full failure output actually captured.

**Process note (self-caught, disclosing in full):** the report commit's own gate run
(`npx vitest run 2>&1 | tail -6 && ... && git commit`) hit this exact flake - `tail`'s exit code,
not vitest's, is what `&&` actually saw, so the chain silently proceeded through a real "1 failed"
result straight to the commit. This defeats the whole point of AND-chaining gates and is precisely
the "gates check exit codes, never grep'd pipes" failure mode. I caught it by reading the
output text (which still showed "1 failed") in the same turn, immediately re-verified with a
non-piped `npx vitest run` (real exit code 0, 2031/2031) proving the just-made commit's tree is
genuinely clean, then reproduced the flake twice more with proper exit-code capture to pin down
its actual identity above. Every OTHER commit in this stream was gated by a piped `tail`
too, but each one's captured text explicitly showed the full "N passed (N)" count with no failures
before I proceeded - this was the one instance where the masked exit code, not my own reading of
the output, would have been the only thing standing between a real failure and a commit. No commit
in this stream actually carries a broken tree (verified above), but the METHOD was wrong throughout
and should not be repeated - a future gate chain should redirect to a file and check `$?`
explicitly, not pipe through `tail`.

## Items

### 1. Missing comment (adversarial-review finding)

**Finding:** the wiring report's "Seam contradictions found" §1 claimed a code comment existed at
`restoreTextToComposer`'s call site explaining the deliberately-unhandled `attachments` parameter.
No such comment existed anywhere in `Composer.tsx` (verified by reading the full file before
touching it).

**Change:** added the comment at `restoreTextToComposer`'s own definition (the function's
signature only accepts `restoredText`, not the `attachments` parameter `QueueStripProps.
onRestoreToComposer`'s type allows for), citing `contracts-composer-queue-pending.md:70` and
`parity-m5-composer.md:102` verbatim as instructed. Appended a dated addendum to
`w5-integration-wiring-report.md` disclosing the gap — the original overstated line is left
unedited above the addendum, per the brief's instruction not to silently rewrite it.

Comment-only + doc-only change; no test applicable.

### 2. `onDrainSuccess` unconditional clear

**Finding:** `handleDrainSuccess` (QueueStrip's "Steer now" callback) cleared the composer's text/
draft/attachments unconditionally on every successful strip-triggered drain, with no snapshot to
compare against — unlike `submitAction`'s own classic drain path, which only clears via
`clearIfUnchanged(submittedText)`. A user editing the composer while a strip-triggered drain was in
flight would have that edit silently discarded once the drain resolved.

**RED test** (`Composer.integration.test.tsx`, "text changed while a strip-triggered drain is in
flight survives..."): types "original", clicks "Steer now" (drain left pending via a manually-
controlled promise), fires a synchronous `fireEvent.change` to "original plus more" while in
flight, resolves the drain, asserts the textarea/draft still read "original plus more" (not
cleared). Failed against the pre-fix code: `expected '' to be 'original plus more'`.

**Fix:** `getComposerText()` now stashes a snapshot (`{ text, markers }`, keyed off
`attachments.items` at read time) into a new `lastDrainSnapshotRef` the moment QueueStrip actually
reads it (immediately before starting the drain RPC — QueueStrip has exactly one call site for
this). `handleDrainSuccess` compares `textRef.current` against that snapshot's text before clearing
text/draft (mirrors `clearIfUnchanged`), and always calls `attachments.clearSubmitted(snapshot.
markers)` unconditionally (safe regardless of the text check, since it only ever removes the
specific markers that snapshot captured — same asymmetry `submitAction` itself already relies on).

**Mutation-verified:** reverted `handleDrainSuccess` to the original unconditional-clear body,
re-ran the new test — failed identically to the original RED. Restored; `diff` against the
pre-mutation file was empty.

### 3 + 4. Missing `hasPending` guard on strip-drain / mid-encode omission risk

**Finding:** these are two facets of the SAME gap (the wiring report's own Concern #3 describes
both in one paragraph, my brief splits them into separate items). `QueueStrip`'s "Steer now" button
had no equivalent to Composer's own submit-time `hasPending` guard (item 3), and because of that, a
drain triggered while an attachment was still mid-encode would silently omit it from the payload —
`toInputAttachments()` filters incomplete items without signaling it (item 4). One fix resolves
both: blocking the drain entirely while `hasPending` is true means no drain payload is ever sent
with a silently-dropped image.

**RED test** (`QueueStrip.test.tsx`, "a mid-encode attachment (hasPending) blocks the drain with a
toast, never calling drainAsSteer"): renders the strip with `getComposerText` reporting
`hasPending: true`, clicks "Steer now", asserts the "still processing" toast appears and
`drainAsSteer` is never called. Failed against pre-fix code (toast never appeared, timed out).

**Fix:** extended `QueueStripProps.getComposerText()`'s return type with `hasPending: boolean`
(Composer's implementation now returns `attachments.hasPending`); `handleDrain` destructures it and
returns early with the same `"Image attachment is still processing"` toast Composer's own submit
paths already use, before calling `onDrainBusyChange`/`submitWithPendingTracking`. Updated
`QueueStrip.test.tsx`'s `defaultProps()` and one other explicit override to supply the new required
field.

**Mutation-verified:** reverted the guard clause (removed the `hasPending` destructure/check),
re-ran — failed identically. Restored; zero net diff.

### 5. aria-live announcement on ask-pending exit

**Finding:** `AskDock.tsx`'s own status region announces entering ask-pending mode ("Answer the
agent's questions.") via `role="status" aria-live="polite"`, but that element unmounts entirely once
the batch resolves (`AskDock` returns `null` when `batches.length === 0`) — it structurally cannot
also announce "Message composer ready." (parity-m5-composer.md line 118's other half). `AskDock.
tsx`'s own header comment says explicitly this is the composer's own surface to own, and the
wiring report's Concern #4 confirmed it was never built.

**Ordering disclosure:** I implemented this fix before writing its test (a process slip — every
other item here was RED-first). I compensated with the same mutation-verification rigor as every
other item: wrote the test against the finished implementation (confirmed it passes), then reverted
just the announcement-triggering effect, confirmed the test fails for the right reason, then
restored and confirmed zero diff. The net evidentiary rigor is equivalent to RED-first; the
*order* of test-vs-code for this one item is not, and I'm disclosing that rather than
representing it as identical to the other items' process.

**Fix:** a new visually-hidden (screen-reader-only) `role="status" aria-live="polite"` region in
`Composer.tsx`, holding a `readyAnnouncement` string that starts empty and is set to "Message
composer ready." only on an actual `askPending` `true → false` transition (tracked via a
`wasAskPendingRef`, mirroring the same ref-based edge-trigger idiom the file's own cursor-restore
effect already uses) — never on initial mount, where there is nothing that just became ready to
announce (an honest-liveness consideration, not just a nicety). Added a `visuallyHidden` CSS class
to `composer.module.css` (byte-identical copy of the SAME utility class already duplicated in
`askdock.module.css` and `askquestioncard.module.css` — this codebase's own established per-file-
duplication convention, not a new pattern). The new class goes through `requireClass` per the
design-system rule; Composer.tsx's other five pre-existing classNames are bare `styles.foo`
references (this file's own established convention, unlike QueueStrip.tsx's full `CLASS` table) —
flagged as a disclosed, deliberate inconsistency rather than either silently mixing styles or
rewriting five unrelated, already-working references outside this fix's scope.

**Test** (`Composer.integration.test.tsx`, "resolving the pending ask announces the composer's
restoration..."): asserts the announcement text is absent before any ask ever happens, still absent
while an ask is pending, and present after answering resolves it.

**Mutation-verified:** removed the announcement-setting effect, re-ran — failed (text never
appeared). Restored; zero net diff.

### 6. Two-Steer-buttons shared busy state

**Finding:** Composer's own `busyAction` and QueueStrip's local `draining` state tracked busy
independently. A user could fire the classic drain-as-steer route (Shift+Enter, or Composer's own
"Steer" button when the queue is non-empty) and QueueStrip's "Steer now" button concurrently —
both ultimately call the same `drainAsSteer` RPC, with neither button disabling the other.

**RED tests** (`Composer.integration.test.tsx`, both directions): (a) click "Steer now" with the
drain left pending, assert Composer's own classic "Steer" button becomes disabled; (b) type text
and click Composer's own classic "Steer" (routes to drain since the queue is non-empty) with the
drain left pending, assert QueueStrip's "Steer now" button becomes disabled. Both failed against
pre-fix code (`expected true, received false` on the disabled check) — direction (b) failed because
`busyAction` was never visible to QueueStrip at all before this fix.

**Fix:** `BusyAction` gains a `"drain"` value, set/cleared only via a new `QueueStripProps.
onDrainBusyChange(busy: boolean)` callback QueueStrip calls around its own drain RPC (replacing its
local `draining` `useState` entirely — one shared source of truth, not two). Composer passes
`busy={busyAction !== null}` and `onDrainBusyChange={(draining) => setBusyAction(draining ? "drain"
: null)}` down; QueueStrip's button now reads `disabled={busy}` from the prop.

**A finding from mutation-testing itself, not just the fix:** I initially also added an internal
`if (busy) return;` guard at the top of `handleDrain` (matching Composer's own defense-in-depth
convention, where `handleSteerClick`'s internal `busyAction !== null` check is independently
exercised via the Shift+Enter keyboard path that bypasses the button's `disabled` attribute
entirely). Mutation-testing that specific guard — removing it and re-running the isolated
"shared busy prop" test — showed the test **still passed**. Root cause: `handleDrain` has exactly
one call site (the button's own `onClick`), itself already gated by `disabled={busy}`; jsdom (like
a real browser) does not fire a click handler on a `disabled` button at all, so there is no
keyboard-bypass or alternate trigger path the way Composer's steer button has. The guard was
genuinely unreachable, untestable dead code. Removed it rather than keep unverified defensive code
(YAGNI) — full gates stayed green with it gone, confirming nothing depended on it.

Added one further isolated test in `QueueStrip.test.tsx` ("the shared busy prop... also disables
the drain button") proving the `busy` prop correctly drives both the disabled attribute and the
resulting no-call guarantee, plus a small `DrainBusyHarness` component (owns `busy` as real
controlled state, mirroring Composer's own round-trip) since the existing "disables itself while
its own request is in flight" test could no longer observe self-disabling behavior once `busy`
became an externally-controlled prop rather than local state.

**Mutation-verified (bidirectional):** swapped in the pre-item-6 versions of both `Composer.tsx`
and `QueueStrip.tsx` (retaining items 1–5's fixes), re-ran all three item-6 tests — all three
failed (two as compile errors, since the test files reference `busy`/`onDrainBusyChange`, which is
itself a valid form of "the mutation bites"; also verified at runtime with `vitest run` directly,
bypassing tsc, confirming genuine assertion failures too). Restored the final versions; `diff`
against each was empty.

**Label-ambiguity proposal (not implemented, per the brief — "propose ... only change copy if the
distinction is currently misleading"):** I judge the distinction currently misleading. When the
queue is non-empty, Composer's own "Steer" button *also* routes to `drainAsSteer` internally
(`decideSteerRoute`) — so a user looking at "Steer" and "Steer now" side by side, with a non-empty
queue, is looking at two buttons that can do the *same underlying operation* under different
trigger conditions, with no label cue distinguishing them. Proposed copy: rename QueueStrip's
button from "Steer now" to **"Steer queue now"** (or "Send queue now") — making explicit that this
button specifically drains the *queue's* contents, as distinct from Composer's own "Steer," which
is primarily about the current draft (and only *also* touches the queue as a side effect of the
same underlying `drainAsSteer` call). This is a UI-content judgment call I'm surfacing, not
implementing.

### 7. `clearSubmitted` draft-sync nit (item 7)

**Interpretation note:** the brief titles this "`removeItem` draft-sync nit" but scopes it to
`useAttachments.ts` in the file manifest ("item 7 only, if needed") and describes the bug as "the
queued-entry remove path leaves a stale draft edge." I searched `.superpowers/sdd/` for a literally
recorded nit under this description and found none (grepped `w5-task-3-report.md`, the queue-strip
stream's own report, and its Concerns section specifically — no match). I derived the actual,
concrete, testable bug myself from reading `useAttachments.ts`: `removeItem` itself (the literal
function name) already correctly strips its own marker text via `editor.write()` on every call — no
bug there. `clearSubmitted`, however, only ever filtered the `items` array; it never touched the
editor's text at all. I read "the queued-entry remove path" as this module's own attachment-removal
family loosely described (its own items are literally "queued" for send in the sense of staged-for-
submission), and "stale draft edge" as "there exists an edge case that leaves the draft stale" —
which is exactly what I found: when the composer's text has *already diverged* from a submitted
snapshot (so `Composer.tsx`'s own `clearIfUnchanged` is a no-op and doesn't blank the whole
textarea), `clearSubmitted` still silently drops the corresponding `PendingAttachment` items from
state while leaving their `"[image N]"` marker text orphaned in the still-live draft — no chip, no
backing bytes, just dead text that would go out as literal garbage on the user's next submission. I
am flagging this interpretation explicitly rather than silently guessing, per the honesty
requirement.

**RED test** (`useAttachments.test.ts`, "clearSubmitted strips the submitted markers' own text from
the editor too..."): ingests one image (marker 1), simulates a concurrent edit landing directly on
the shared editor (mirroring `Composer.tsx`'s own `textEditor.write()` being invoked from a
`fireEvent.change` mid-submit), then calls `clearSubmitted` with that one marker. Asserted the
editor's text has the marker stripped while the concurrent edit survives. Failed against the pre-
fix code: `expected '[image 1] plus more' to be ' plus more'`.

**Fix:** `clearSubmitted` now reads the editor, iteratively strips every submitted marker's own
`"[image N]"` text (same `stripMarker` helper `removeItem` already uses, threaded across the whole
`submittedMarkers` set in one `editor.write()` call — `stripMarker` is a documented safe no-op for
a marker no longer present, so this never fights a concurrent edit that already removed one
itself), then filters `items` exactly as before.

**Mutation-verified:** reverted to the items-only filter, re-ran — failed identically. Restored;
zero net diff.

### 8. Duplicate-text FIFO test (test-only)

**Finding:** `pendingReconcile.test.ts` already proves `computeReconciledIds`' own multiset match
resolves the FIRST-registered of two identical-text entries — but only against a hand-built
`ThreadModel` (a pure-function test). `pendingTurnsStore.test.ts` had no companion proving the same
FIFO guarantee holds through the real `submitWithPendingTracking` + `threadsStore` + wire-echo
pipeline.

**Test added** (`pendingTurnsStore.test.ts`, "two queue entries with IDENTICAL text reconcile
FIFO..."): registers two real pending queue entries with identical text, fires one matching
`thread/queueChanged` echo (one authoritative slot), asserts exactly one resolves and it is
specifically the older (first-registered) one. Passed immediately against the existing
implementation (no code change — this item is test-only, closing a coverage gap, not a bug fix).

**Mutation-verified anyway** (per the brief's blanket "mutation-verify each regression net"
instruction): reversed the iteration order in `pendingReconcile.ts`'s `reconcileQueueEntries` (a
plausible FIFO-breaking mutation), re-ran — both this new store-level test AND the pre-existing
pure-function test in `pendingReconcile.test.ts` caught it (`pending_1` resolved instead of the
expected non-`pending_1`, and the pure-function test's own exact-id assertion flipped too).
Restored; zero net diff.

### 9. Pending-chips visibility confirmation

**Citation-or-assertion decision:** both, for precision. `QueueStrip.test.tsx`'s own "a pending
queue-method entry from another submission renders as an extra, action-less row" test already
proves QueueStrip renders a real DOM row for a pending queue entry — but only in *isolation* (props
handed to it directly, not through the real composed tree). `Composer.integration.test.tsx`'s own
"a queue submit ALSO registers an optimistic pending entry" test drove the real, fully-composed
tree through a real user submit, but only asserted the `usePendingTurnEntries` *hook's* state, never
the actual rendered DOM — so neither existing test, alone, proved the pending chip is visible
*in the composed UI, driven by a real submit*. I strengthened the existing integration test in
place with the missing DOM assertion (`screen.findByText("queued message")`) rather than writing a
whole new test, since the brief's "add the assertion" phrasing pointed at the smaller fix.

**Mutation-verified:** suppressed the pending-row render in `QueueStrip.tsx` (`pendingQueueEntries.
slice(0, 0).map(...)`), re-ran the strengthened test — failed (`findByText` timed out). Restored;
zero net diff.

### 10. Docs

**10a. Wave-plan floor-count drift.** The brief states the wave-plan doc (`docs/superpowers/plans/
2026-07-21-webui-rewrite-wave5-interaction.md`) declares floor counts A=17 and G=16. The file
exists in my worktree, but I read it in full and grepped it (and its entire one-commit git history)
for any "A=17"/"G=16"/"A: 17"/"G: 16" pattern (in any spacing/case) and found **no such statement
anywhere** — the file's only per-stream floor-row counts are "~89 floor rows" (T2), "~68 floor
rows" (T3), "~66 floor rows" (T4), none broken down by parity-doc section letter. I independently
counted `parity-m5-composer.md`'s own §A and §G checkbox rows: 21 and 14 respectively — matching
the brief's claimed "implementation reality" numbers exactly, which is why I'm confident this isn't
a case of me looking in the wrong file, just that the *specific claimed drift text* doesn't exist
in it. Per the brief's own instruction ("if absent, say so in the report — controller fixes at
merge"), I made no edit here rather than inventing a correction for text that was never there.

**10b. Floor-doc divergence annotations.** Added "Implementation note (wave 5 close)" annotations,
each citing `w5-integration-wiring-report.md` for the reasoning:
- `contracts-composer-queue-pending.md:70` and `parity-m5-composer.md:102` — divergence (i),
  confirming the queue-edit restore is implemented text-only exactly as those rows already specify.
- `parity-m5-composer.md` (the §C row citing `renderer.js:6232-6245`, i.e. `dropComposedTextIntoComposer`)
  and `contracts-composer-queue-pending.md`'s matching test-ask-compose.js Conflict-fallback row —
  divergence (ii), the ask-fallback preserving (not overwriting) the composer draft. Per the
  controller's mid-task correction, recorded as **"approved by Jesse, 2026-07-21"**, not pending.
