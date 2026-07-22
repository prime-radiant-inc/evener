# Wave 5 Task 3 report — queue strip + optimistic pending

Branch `w5-queue`, off the wave chokepoint `e299f4803` (T1). 3 commits, HEAD `3cd42d14f`. Full
suite green (111 test files, 1640 tests), `tsc --noEmit` clean, `biome ci src` clean, `npm run
build` clean. Manifest respected exactly: `git diff --stat e299f4803..HEAD` touches only
`cmd/serf-hub/frontend/src/panes/session/composer/queue/**` (10 files, +2265/-0) — `Composer.tsx`,
`Session.tsx`, `stores/threads.ts`, and the widgets barrel are all untouched.

## Commits

| Commit | Unit |
|---|---|
| `296e4a7b7` | T3(A): optimistic-pending state model — `queueDisplay.ts`, `pendingReconcile.ts`, `pendingTurnsStore.ts` |
| `8f0cf0d21` | T3(B): `QueueStrip.tsx` component + CSS + public barrel |
| `3cd42d14f` | T3(C): row re-keying test coverage + `queueEntryPreviewText` barrel export |

## Design: three-layer split

- **`queueDisplay.ts`** (pure): `normalizeText`/`imagePlaceholder`/`queueEntryPreviewText`/
  `truncateForDisplay`, ported verbatim from the legacy registry's own `pending.js` since these
  exact strings are a *matching key*, not just a display choice.
- **`pendingReconcile.ts`** (pure): `computeReconciledIds(entries, model, priorItemIds)` — given
  the pending entries for one ref, that ref's current `ThreadModel`, and the item-id baseline from
  the *previous* model, returns which entries a diff confirms. No notification pattern-matching at
  all: per the wave's binding constraint ("subscribe to the threads store's model, never the
  client directly"), this diffs consecutive `ThreadModel` snapshots. Tested in isolation against
  hand-built `ThreadModel` fixtures (25 tests) — legitimate here since `ThreadModel` is this
  codebase's own internal contract, not a wire shape; wire-truth is instead proven by
  `pendingTurnsStore.test.ts`'s FakeClient-driven integration tests, which push real wire
  notifications through the real `hydrateThread`/`applyNotification` pipeline.
- **`pendingTurnsStore.ts`** (impure glue): a zustand store for pending entries, a 10s timeout
  reaper, and `submitWithPendingTracking` as the one public entry point. Wires reconciliation via
  `threadsStore.subscribe()` at module load (mirroring `threads.ts`'s own `connectionStore.subscribe`
  pattern) — permanent, never re-subscribed, so it works regardless of whether any component ever
  mounts `QueueStrip` (e.g. a plain send's pending entry still gets reconciled/reaped even when the
  queue is empty and the strip renders `null`).

## Deliberate departures from the legacy behavior (all sanctioned by the wave plan or documented as my own call)

1. **All four methods (send/steer/queue/drain) register pending entries uniformly** — the wave
   plan's own beyond-parity fix; legacy's `turn/start` never did.
2. **Declarative store, not a DOM-chip registry** — the wave's own decided design. A concrete
   consequence: legacy's "failed chip + inline Retry link, left visible until the user acts"
   is replaced by *toast-and-remove* (no lingering failed state at all) — `onFailure` fires once
   (immediate rejection or the 10s timeout), the entry is dropped, and there is no retry
   affordance this wave. Nothing in my task brief calls for retry, and the wave's own failure
   convention ("no new banner systems") reads as pointing at exactly this simplification, but it's
   a real, visible-to-Jesse behavior change from the legacy client, so flagging it explicitly here
   rather than letting it hide in a diff.
3. **`onFailure` receives the raw error, not a pre-formatted string.** Discovered mid-build: the
   drain affordance needs to distinguish a `queuedDrainPartial` `WireError` from any other failure
   (parity §A, "drain failed after queueing" vs "drain failed"), which is impossible once the
   error's been collapsed to a string. Refactored before this became load-bearing in more than one
   place; the timeout reaper synthesizes a plain `Error("The server didn't confirm this message in
   time.")` so the shape is uniform either way.
4. **Queue-method reconciliation prefers `QueueState.texts` (full untruncated text) over
   `QueueState.preview`** (server-truncated to the first line) when both are present — a
   deliberate improvement over the legacy JS, which only ever had the truncated array to match
   against and would silently fail to reconcile a multi-line queued message.
5. **A steering-item echo tries an exact-text `steer` match before falling back to FIFO-any
   `drain`** — my own considered ordering (legacy's two methods are reconciled via two independent,
   unordered `tryReconcile` calls against the same event; unifying them into one diff pass forced a
   choice I didn't have a citation for).
6. **All three row actions (not just edit+cancel) disable together during an in-flight request on
   the same row**, and **all three degrade together (disabled + explaining tooltip) when the
   daemon reports no `ids` array at all** — parity only names edit+cancel and only names the
   no-`texts` degradation for edit specifically; I generalized both to all three actions since an
   entry-id-less row has no way to safely identify itself for ANY of the three, and allowing
   promote to race an in-flight cancel/edit on the same row seemed like unnecessary risk for zero
   benefit. Flagging as a judgment call, not a parity citation.

## Integration seam (exact signatures T2/T6 need)

```ts
// from cmd/serf-hub/frontend/src/panes/session/composer/queue/index.ts

export interface QueueStripProps {
  ref: string;
  getComposerText(): { text: string; attachments?: InputAttachment[] };
  onRestoreToComposer(text: string, attachments?: InputAttachment[]): void;
  onDrainSuccess(): void;
}
export function QueueStrip(props: QueueStripProps): ReactNode;

export interface SubmitWithPendingTrackingOptions {
  ref: string;
  method: "send" | "steer" | "queue" | "drain";
  text: string;
  attachments?: InputAttachment[];
  onFailure: (error: unknown) => void; // raw error/rejection value - format it yourself
}
export function submitWithPendingTracking(
  opts: SubmitWithPendingTrackingOptions,
  perform: () => Promise<void>,
): Promise<void>;

export function usePendingTurnEntries(ref: string, method?: PendingMethod): PendingTurnEntry[];
export function queueEntryPreviewText(text: string, imageCount: number): string;
```

Notes for whoever wires this in (T6, or T2 if it lands first):

- **`getComposerText`/`onRestoreToComposer`/`onDrainSuccess` are the three props `<QueueStrip
  ref={ref} .../>` needs.** `onRestoreToComposer`'s `attachments` parameter is never populated by
  this module (`cancelQueued`'s wire response returns only a `removedImages` *count*, never
  attachment bytes — parity is explicit that edit is a text-only recompose) — kept in the
  signature per my own brief's instruction, for symmetry with a general "restore to composer"
  concept the integration may reuse elsewhere.
- **`onRestoreToComposer`'s own "never clobber existing composer text, append after a blank line"
  behavior (parity §B) is the CALLBACK IMPLEMENTATION's job, not something `QueueStrip` does** —
  `QueueStrip` only ever calls `onRestoreToComposer(fullText)`; T2's Composer owns merging that
  into whatever's already typed.
- **T2's own send/steer submissions should wrap their `threadsStore.send`/`.steer` calls in
  `submitWithPendingTracking`** the same way `QueueStrip`'s own drain handler wraps
  `drainAsSteer` — that's what makes optimistic pending genuinely uniform across all four methods
  rather than just the two (queue/drain) this stream itself initiates.
- **No presentational chip exists for send/steer/drain pending entries** — their natural render
  location (the transcript/conversation pane) is outside this manifest. `usePendingTurnEntries` +
  the now-exported `queueEntryPreviewText` give whoever builds that surface the same state/label
  computation `QueueStrip` uses for its own queue-method rows, without re-deriving it.
- **Promote/cancel deliberately do NOT go through `submitWithPendingTracking`** — matches parity's
  "no local mirror" for either; only queue/drain (and, per the wave decision, send/steer) get
  optimistic entries.

## Parity/contract coverage

Covered directly (queue strip, §B + contracts §Queue): wire-only rendering (no local mutation),
depth-0-unless-pending visibility, 140-char client truncation on top of the wire's own first-line
truncation, entryId-carrying rows, promote/edit/cancel with expectedEntryId, edit's
restore-then-cancel ordering (verified via an explicit call-order assertion, not just an end
state), edit disabled for image-only/no-texts, cancel's shared "images weren't restored" warning,
per-row in-flight locking, row re-keying after a queue shift (added explicitly in T3(C) — a named
contract row I hadn't locked down with its own test until I re-audited against the docs), and the
drain affordance's `queuedDrainPartial`-vs-generic split.

Covered directly (pending, §E + contracts §Pending): uniform registration across all 4 methods,
10s timeout, queue-method multiset reconciliation (one-for-one, FIFO on ties), drain's
FIFO-any-text-match, a successful RPC response *not* itself reconciling the entry (only a wire
echo or the timeout does).

Explicitly not applicable to this manifest (verified, not just skipped): the REST-fallback
snake_case/camelCase normalization row (this rewrite has no REST fallback at all — AppWire-only,
so it's trivially satisfied); `queueText`'s own empty-text-with-attachments validation and the
classic steer-vs-drain button branching (both are the *submission* path, T2's `Composer.tsx`, not
this stream's queue-strip-and-pending manifest); the legacy DOM-chip rendering mechanics and its
retry-link affordance (superseded by the wave's own declarative-store decision, see above).

## TDD evidence

Every file: test written first, run to confirm red (import-resolution failure or missing export),
implementation added until green, then `tsc --noEmit` re-checked before moving on. Two places TDD
surfaced a real design gap rather than a typo:

- **The `onFailure: (message: string) => void` → `(error: unknown) => void` refactor** (T3(A),
  before its own commit) — caught while designing the drain handler's `queuedDrainPartial` split,
  not by a failing test per se, but by noticing the planned implementation had no way to satisfy a
  requirement I'd already written a test contract for. Fixed at the store-API level before any
  other caller depended on the string shape.
- **`fake.on("turn/queue", () => { throw "raw string failure" })` routed through
  `threadsStore.getState().queue(...)` came back as an `Error` object, not the raw string** — a
  real "verify, don't assume" catch: `threads.ts`'s own `mapConflict` normalizes *any* non-`Error`
  rejection into `new Error(String(err))` before it ever reaches this module, so a "does a
  non-Error value pass through unchanged" test driven through the real store was testing
  `mapConflict`, not my own code. Fixed by isolating that one test with a synthetic `perform`
  (`() => Promise.reject("raw string failure")`) instead of a real threadsStore action.

One tooling gotcha, not a design bug: this project has no jest-dom matcher setup
(`vite.config.ts`'s `test.setupFiles: []`), so `toBeDisabled()` isn't a real Chai property here —
every other test file in the tree checks `(el as HTMLButtonElement).disabled` directly. Caught by
running the suite (not by reading configs first) and fixed by matching the established convention
with a small local `isDisabled()` helper, same as `sandboxEscalation.test.tsx`/
`button.test.tsx`/etc. already do.

## Files

Created (all new, all inside the exclusive manifest):
- `queueDisplay.ts` + `.test.ts` (39 / 83 lines)
- `pendingReconcile.ts` + `.test.ts` (204 / 281 lines)
- `pendingTurnsStore.ts` + `.test.ts` (237 / 362 lines)
- `QueueStrip.tsx` + `.test.tsx` + `queuestrip.module.css` (292 / 682 / 68 lines)
- `index.ts` (17 lines, the public barrel)

## Concerns

- **No visual chip exists yet for send/steer/drain pending entries** — by design (outside this
  manifest), but it means until T6 (or T2) builds one, a plain send/steer's optimistic-pending
  state is tracked and reconciled but invisible to the user. Worth confirming at the wave-close
  parity sweep that this is an accepted gap for this wave, not an oversight.
- **The drain-as-steer affordance is its own button ("Steer now") inside the queue strip**, separate
  from whatever button T2's composer core renders for the classic steer/drain kata branch (parity
  §A). I could not find anything in my brief or the locked interfaces requiring these to be the
  *same* DOM button, and the wave plan's own T3 bullet lists "drain-as-steer affordance" as this
  stream's scope distinctly from T2's "send-vs-steer-vs-queue routing" — but this is a real
  UX-surface judgment call (two buttons that both call `drainAsSteer` under different conditions)
  worth a second look at integration time, not something I want to assert is definitely right.
- **The "actions unavailable when `ids` is missing" degradation (generalized from parity's
  edit-only, no-`texts` rule) has no citation** — a defensive design choice on my part, documented
  in code and above, not verified against any legacy behavior (the legacy renderer's own
  promote/cancel never had an `ids`-missing code path to observe, since it always trusted
  `state.ids[idx]` to exist).

## Verification

```
npx tsc --noEmit  → EXIT=0 (no output), re-checked after every unit
npx vitest run    → EXIT=0 (111 test files, 1640 tests); on-disk `find` count also 111 (no silent
                     exclusion)
npm run lint      → EXIT=0 (biome ci src, 347 files, "No fixes applied.")
npm run build     → EXIT=0 (tsc --noEmit && vite build); dist/PLACEHOLDER restored via
                     `git restore` after each build gate
```
