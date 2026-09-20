# /simplify review — PR #1901 (D25e C5: submission lifecycle into the package, head 99812f46d)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's (passed clean).
Reviewed `git diff -M origin/main...pr-1901-review`: `appwire-client/typescript/state/mutation/{submission.ts,
submission.test.ts,testing.ts,index.ts}`, `tsconfig.build.json`, and
`cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts`. Line numbers are at this PR's
head. Compared against the C4 lifts now on main (`commitFeed.ts`, `projection.ts`, `projectionWork.ts`) and
`outbox.ts`'s port conventions.

The lift itself is the right shape: positional `(store, fence, …, refresh)` ports match
`wireMutationCommitFeed(store, fence, feed, refresh)` exactly, `refresh: (ref?: string) => void` is that
sibling's own port type, the module names no host global, and it does not re-express any fence sequence —
`projection.ts` exposes `epoch()` and nothing more (`git grep '\.epoch()'` finds only this module and
`projection.test.ts`), so there is no existing epoch-guard helper to reuse and nothing in `pendingTurns.ts`
expresses begin/settle/end ordering.

## Must fix before merge

### 1. `appwire-client/typescript/state/mutation/testing.ts:75` — the new `overrides` parameter has no caller

`git grep testPendingTurnsStore pr-1901-review` returns eleven call sites (5 in `submission.test.ts`, 4 in
`commitFeed.test.ts`, the declaration): every one of them is `testPendingTurnsStore()` with no argument. The
parameter, its `PendingTurnsStoreDeps` import and its three-line doc comment are added dead in this PR.

- Cost: an exported test-helper knob nothing exercises, so nothing proves that spreading a `Partial` over the
  three ports is even the contract the comment claims ("replaces one dependency wholesale"). The PR description
  says the new tests needed it "to control the draft port's revision/text/skillNames"; they don't use it.
- Simpler form: either delete the parameter and the type import (a 6-line revert to main's signature), or — the
  better option — actually use it for the case the suite cannot currently express. Today
  `submission.test.ts:79-95` ("settles the draft and reports what it decided") only reaches
  `{clearedDraft: true}` because the no-op port returns revision `0` and `{text: "", skillNames: []}`, which is
  why that one test passes `text: ""` in a suite that otherwise sends `"hi"`. A second case with
  `testPendingTurnsStore({ draft: { readDraftRevision: () => 7, readComposerDraft: () => ({ text: "edited",
  skillNames: [] }), clearDraft: () => undefined } })` asserts `onCommitted` reports
  `{clearedDraft: false, draftUnchanged: false}` — the report's other branch, which nothing in this PR covers —
  and makes the parameter load-bearing. ~10 lines.
- Severity: must-fix-before-merge (dead code introduced by the diff). Either direction closes it.

### 2. `appwire-client/typescript/state/mutation/submission.ts:9-15` and `:67-71` — `MutationSubmissionOptions` re-declares `SubmittedDraft`, then rebuilds it field by field

`SubmittedDraft` (`pendingTurns.ts:118`, exported from `state/mutation/index.ts`) is exactly
`{ draftRevisionAtStart: number; text: string; skillNames: readonly string[] }` — the three fields
`MutationSubmissionOptions` declares alongside `ref`/`onFailure`, and its doc comment already says it is "the
submitted values `settleSubmittedDraft` compares the current draft against". The new module then re-assembles
that literal at the call it hands to `settleSubmittedDraft`.

- Cost: two declarations of one shape in the same directory, plus a five-line object reconstruction whose only
  job is to copy three fields across. A fourth compared field (the store's `selectionsUnchanged` check already
  hints at one) has to be added to `SubmittedDraft`, to `MutationSubmissionOptions` and to the literal, and the
  compiler will not point at the third.
- Simpler form:
  ```ts
  export interface MutationSubmissionOptions extends SubmittedDraft {
    ref: string;
    onFailure: (error: unknown) => void;
  }
  ```
  and `const settled = store.settleSubmittedDraft(opts.ref, opts);` — excess properties are fine on a variable,
  and `settleSubmittedDraft` reads only the three. 6 lines become 2, no behaviour change. If the options shape
  should stay independent of the store's for native's sake, then at minimum drop the literal and pass `opts`
  through; the reconstruction is the half that must go.
- Severity: must-fix-before-merge (duplicates an existing exported declaration in the same directory; the repo
  rule is extract-and-share rather than copy). ~4 lines.

## Follow-up

### 3. `submission.ts:21-24`, `:72` — `MutationSubmissionCommitted` is `settleSubmittedDraft`'s return shape with one field renamed

`settleSubmittedDraft` returns `{ cleared, draftUnchanged }`; the new interface is the same pair with `cleared`
→ `clearedDraft`, so `:72` rebuilds the object purely to rename one key, and the web adapter destructures the
new names back out at `pendingTurnsStore.ts:200`.

- Cost: a third name for one decision (`cleared` in the store, `clearedDraft` on the wire out, `clearStoredDraft`
  in main's old inline code), and one more object per submission that carries no information the store's own
  result didn't.
- Simpler form: keep the store's field name (`cleared: boolean; draftUnchanged: boolean`, or
  `ReturnType<PendingTurnsStore["settleSubmittedDraft"]>`) and call `onCommitted?.(settled)`. One line instead of
  five. `clearedDraft` does read better at a bare call site, so this is a judgement call — the reconstruction,
  not the name, is what costs.
- Severity: follow-up, low.

### 4. `submission.ts:47` — `track`'s type re-declares `MutationProjectionWorkTracker["track"]`

`projectionWork.ts:25` already declares `track<T>(work: Promise<T>): Promise<T>` as the tracker's method, and
the web binds exactly that (`pendingTurnsStore.ts:90-92` wraps `projectionWorkTracker.track`). The new port
spells the same generic signature out again.

- Cost: two encodings of one port, so a change to the tracker's contract (a label argument, a cancel handle)
  type-checks on one side only. Naming the existing type also tells a reader of `submission.ts` which package
  capability this parameter is for, which the bare signature does not.
- Simpler form: `import type { MutationProjectionWorkTracker } from "./projectionWork";` and
  `track: MutationProjectionWorkTracker["track"],`. Structurally identical today, so nothing at any call site
  changes.
- Severity: follow-up, low.

### 5. `cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts:199` and `:135-137` — the same fire-and-forget refresh adapter written twice

`(ref) => void refreshPendingTurnsProjection(ref)` is built inline for the commit feed at `:135` and again, per
submission, at `:199`.

- Cost: two encodings of "this is the `(ref?: string) => void` the package's cores take", and one closure
  allocated per send. If the `void`-wrapping convention changes (or the refresh grows an argument), one site
  gets updated.
- Simpler form: one module-level `const refreshTarget: (ref?: string) => void = (ref) => void
  refreshPendingTurnsProjection(ref);` beside `trackProjectionWork` at `:90`, passed at both sites.
- Severity: follow-up, low.

### 6. `submission.ts:44-52` — six required positional parameters, four of which never vary per call

On the web, `store`, `fence`, `track` and `refresh` are module-level singletons; only `opts`, `perform` and
`onCommitted` differ between submissions. The parameter list interleaves the two kinds (`opts` sits between
`track` and `perform`), and this directory's convention for binding long-lived ports once is a factory or wire
function — `createPendingTurnsStore(deps)`, `createMutationProjectionWorkTracker(ports)`,
`wireMutationCommitFeed(store, fence, feed, refresh)`.

- Cost: the one call site (`pendingTurnsStore.ts:187-217`) is a 7-argument call a reader has to count through,
  and native's binding will repeat all four singletons at its own call site too.
- Simpler form: `createSubmissionRunner(store, fence, track, refresh)` returning
  `(opts, perform, onCommitted?) => Promise<void>`, built once at module scope next to the `wireMutationCommitFeed`
  call. The adapter body then reads `return submitSubmission({…}, perform, ({clearedDraft, draftUnchanged}) => …)`.
  Don't do it if native's binding turns out to need a per-call fence or tracker.
- Severity: follow-up. Optional; the current shape is not wrong, just the least sibling-like of the lifts.

### 7. `pendingTurnsStore.ts:141` — `SubmitWithPendingTrackingOptions.method` is a required field nothing reads (pre-existing)

`git grep -n method` over this file finds `method` only as the declaration at `:141` and as
`pendingTurnEntries`/`usePendingTurnEntries`'s unrelated filter parameter — main is the same. The PR description
justifies keeping it ("`method`, `attachments`, `recoveryId` stay web-only — Composer-specific fields the core
never needed"), which holds for the other two (`:209` reads both) but not for `method`.

- Cost: every caller fills in a value that is thrown away — `Composer.tsx:1180`, `QueueStrip.tsx:301`, and four
  `pendingTurnsStore.test.ts` / `QueueStrip.test.tsx` call sites — and the next reader of the newly-shrunk
  adapter has to re-derive that the core drops it deliberately rather than by mistake.
- Simpler form: delete the field and the six call-site properties. Purely subtractive.
- Severity: follow-up (pre-existing, and it touches call sites outside this diff), but this is the PR that
  reshapes the interface's contract, so it belongs in its follow-up.

## Checked, not findings

- **`onCommitted` as a port, not a returned value** (the altitude question): the port is right. The listeners
  must run inside the tracked window and before `refresh`, which is what the module's own kata-3p22 comment
  defends — a returned report would fire them after `track`'s `finally` has already removed the work from the
  in-flight set, so a settle round could see zero outstanding with the composer's draft UI and recovery tray not
  yet notified, and the rejection path (where the caller's promise rejects) would never deliver it at all.
- **Ordering change in the adapter**: `draftRevisionAtStart: readDraftRevision(opts.ref)` (`:193`) is now
  evaluated before `beginSubmission` instead of after it (main read it between `epoch` and `track`). Both are
  synchronous with no await between, so there is no observable difference.
- **The two `epoch === fence.epoch()` guards** (`:66`, `:75`) are separate decisions (settle vs. release), not a
  duplicated helper; `projection.ts` exposes no guard of its own to reuse.
- **Package tests overlapping the web adapter's** (5 new core tests vs. `pendingTurnsStore.test.ts`): that is the
  established pattern for these lifts (`commitFeed.test.ts` alongside the web's), not duplication.
- **`qualify-package.mjs`**: no sibling core (`wireMutationCommitFeed`, the fence, the work tracker) has a smoke
  block either, so leaving `submitWithPendingTracking` out is consistent.

## Verdict

**Fix before merge — 7 findings, 2 must-fix.** The lift itself is a genuine net simplification: the web adapter
loses 57 lines of nesting for 34 of argument passing, the sequencing now has one home with real package tests
behind it, the port list matches `wireMutationCommitFeed`'s positional `(store, fence, …, refresh)` convention,
and nothing about the draft rule was generalized on the way through — `settleSubmittedDraft` still owns it. Two
things should not land as they are. `testing.ts:75`'s new `overrides` parameter has no caller anywhere in the
repo, which makes it dead code added by this diff; the fix worth making is to use it for the
`{clearedDraft: false, draftUnchanged: false}` branch `submission.test.ts` never reaches (the passing draft test
works only because the no-op port happens to return revision `0` and empty text, which is why it sends `""`).
And `MutationSubmissionOptions` re-declares the exported `SubmittedDraft` field for field and then rebuilds that
literal at `:67-71`, where `extends SubmittedDraft` plus `settleSubmittedDraft(opts.ref, opts)` says the same
thing in two lines. The remaining five are cheap follow-ups: one renamed copy of the store's return shape (#3),
a re-declared tracker port type (#4), the refresh arrow written twice in the web file (#5), the six-positional
signature where this directory would normally bind the singletons once in a factory (#6), and the dead required
`method` field this PR's own description mis-justifies (#7).
