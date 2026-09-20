# /simplify review — PR #1808 (D6 pieces 3-5 folded: the draft-checkpoint port, head d421d5489)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M origin/main...pr-1808-review`: `appwire-client/typescript/{draftCheckpointPort.ts,
keybindingsStore.ts,readyGenerationFence.ts,index.ts,tsconfig.build.json,testing/draftStorage.ts,
testing/keybindingDraftStorage.ts(deleted)}` plus `mobile-native/src/{nativePreferenceDrafts.ts,
NativePreferencesProvider.tsx,draftBackend.testkit.ts}` and the five touched native test files. Line numbers are
at this PR's head.

Two consolidations in this diff are the right call and I have nothing to add to them: `testing/draftStorage.ts`
replaces the keybinding-specific `testing/keybindingDraftStorage.ts` with one generic double, and
`draftBackend.testkit.ts` collapses the per-test native backend fakes into one. Checked for a pre-existing
byte-compare/identity helper to reuse (`git grep -E 'canonicalJson|stableStringify|sortKeys'` over
`appwire-client/typescript cmd/evener-hub/frontend/src mobile-native/src`): none exists on this tree, so the
`JSON.stringify` compares here are not duplicating anything (p6a replaces them with `canonicalJson`).

One scoping fact worth stating because the PR description says otherwise ("the web and native port adapters"):
there is no web adapter in this diff. `cmd/evener-hub/frontend/src/stores/keybindings.ts:49` builds
`createKeybindingsStore({client, registry, characterKeyTriggers})` with no `drafts`, so the web section runs on
`keybindingsStore.ts:80`'s `memoryDraftStorage()` and every CAS refusal path added here is unreachable from the
web. `mobile-native/src/NativePreferencesProvider.tsx` is the only real `DraftPort` implementation in the repo.
That is fine for a lift, but it means the ~550 production lines here have exactly one consumer, and the second
writer the compare-and-swap defends against only comes into existence in p6b (the store-free discard path).

## Must fix before merge

None.

## Follow-up

### 1. `draftCheckpointPort.ts:141-168` (`save`) and `:195-204` (`replaceClassified`) are the same operation written twice

Both decode, `storage.replaceIf(classification.raw, decoded)`, then reset `classification` and `rawFrom` in the
same order. They differ in exactly one thing: what happens when nothing is classified or the store was
classified absent — `save` calls `insertIfAbsent`, `replaceClassified` returns false. `discardClassified`
(`:185-190`) stands in the same relation to `removeIf` (`:169-176`).

- Cost: a 4-mutator public interface (`DraftRepository`, `:57-78`) where two of the four are a one-branch
  variation of another, each with its own 8-14 line doc block; the post-write bookkeeping (`classification =
  { raw: decoded }; rawFrom.set(...)`) is written twice and has to stay in step by hand.
- Simpler form: one writer, `write(checkpoint, whenAbsent: "insert" | "refuse")` — `save` is
  `write(c, "insert")`, `replaceClassified` is `write(next, "refuse")` — with a single post-write block. Drops
  ~12 lines of body and one doc block, and there is then one place to change if the identity bookkeeping ever
  grows a third field.
- Not proposed: collapsing `removeIf(checkpoint)` into `discardClassified()`. They genuinely differ (a
  `restoreDraft` between the write and the removal can move the classification onto another writer's record,
  which `removeIf`'s `rawFrom` identity correctly refuses and `discardClassified` would delete), so `rawFrom`
  (`:120`, one read site at `:170`) has to stay.
- Severity: follow-up.

### 2. `readyGenerationFence.ts:47`/`:99` — `lostHub` composes two predicates the fence already exposes publicly

`lostHub: (generation, stillClaimed) => stillClaimed && isCurrent(generation)` reads no private state: both
`isCurrent` (`:24`) and `writeToken` (`:22`) are already on the interface, and the single call site
(`keybindingsStore.ts:754`) supplies `stillClaimed` as `token === fence.writeToken`. So the method is
`fence.isCurrent(generation) && token === fence.writeToken` spelled as a fence method.

- Cost: the shared fence's interface grows a store-specific concept (a 10-line doc block about `saving` and the
  draft editor, in a module whose header says "the store keeps its own state publication ... above this"), and
  `readyGenerationFence.test.ts` gains no case for it — `git grep lostHub` finds the declaration, the
  implementation and one caller, nothing else.
- Simpler form: delete the interface member and the implementation line; `settleLostHubWrite`
  (`keybindingsStore.ts:670`) already exists and is where that doc paragraph belongs, as
  `if (token === fence.writeToken && fence.isCurrent(generation) && getState().saving)`. -13 lines from the
  shared module, no behaviour change.
- Severity: follow-up.

### 3. `keybindingsStore.ts:947` (`settledWrite`) and `:1260` (`settleWrite`) — one-character-apart names, and two encodings of "re-mark the classified checkpoint settled"

`settledWrite` re-marks via `drafts.replaceClassified({ id: drafts.createId(), baseRevision: draft.revision,
rules: draft.rules, writeUncertain: false })` (`:956-961`, fresh id, rebuilt from in-memory state);
`settleWrite`'s `"remark"` branch re-marks via `drafts.replaceClassified({ ...checkpoint, writeUncertain: false })`
(`:1273`, same id). Both then handle a refusal by adopting `restoreDraft(...)`, and both wrap the call in a
try/catch that publishes a draft-storage message.

- Cost: a reader has to hold two nearly-identical settle routines whose names differ by one letter and whose id
  handling silently disagrees; the divergence is justified only by a comment ("settledWrite has no checkpoint
  reference to reuse one from"), which is a statement about the call site, not about the operation. Any future
  change to what "settled" means on disk has to be made in both.
- Simpler form: one `remarkSettled(checkpoint: KeybindingDraftCheckpoint | null): Partial<Fields>` that mints an
  id only for the null case and owns the try/catch plus the refusal adoption; `settledWrite` calls it with null,
  `settleWrite`'s remark branch with its checkpoint. ~15 lines saved and the id rule stated once. Rename the
  read-side one (`settleUncertainWriteOnRead`, say) regardless — the current pair is a trap in review.
- Severity: follow-up.

### 4. `keybindingsStore.ts:714` and `:774` — three mechanisms now attach fields to one publish

`applyHubOverrides(payload, extra, settle?)` takes `extra` (published whether or not the payload applies) and
`settle` (a thunk whose fields publish only after a successful reconcile), and `applyHubOverridesSettling(payload,
settled)` (`:774-782`) wraps it to guarantee `settled` publishes even when the reconciler throws. `extra` and
`settled` are the same concept under two names — "fields that publish regardless" — reached by two different
entry points.

- Cost: two overlapping parameters and a wrapper in a 70-line region, with the invariant ("which of the three
  survives a throw, an ignore, and a stale guard") documented across three doc blocks instead of one.
- Simpler form: keep the one entry point, `applyHubOverrides(payload, always, onApplied?)`, and have it own the
  try/catch that `applyHubOverridesSettling` currently adds (returning the thrown error as it already does).
  The read path passes `always = {hubLoading: false}`, the write paths pass their `settled`. -9 lines, one doc
  block, and the three-way rule stated once.
- Severity: follow-up.

### 5. `keybindingsStore.ts:968, 1011, 1228, 1274, 1276, 1443` — the "adopt what is on disk" step is spelled out six times in three plumbing shapes

"Re-read and adopt whatever is actually stored" appears as `return restoreDraft({loaded: true, revision:
payload.revision})` (settledWrite's CAS refusal, `:968`), `setState(restoreDraft(getState()))` (refreshOverrides'
retry `:1011`, persistDraft's refusal `:1228`, discardDraft's refusal `:1443`) and `refused =
restoreDraft(getState())` (settleWrite, `:1274` and `:1276`).

- Cost: six sites to keep in step, and the two argument forms (`getState()` vs a payload-derived `{loaded,
  revision}`) are a real semantic difference that nothing names, so a reader must re-derive each time why
  settledWrite passes the refresh's revision and the others pass current state.
- Simpler form: one `adoptStoredDraft(confirmed = getState())` beside `restoreDraft`, with the "why the revision
  argument differs" comment on it once. Small (~6 lines net) but it turns six judgement calls into one.
- Severity: follow-up.

### 6. `keybindingsStore.test.ts:857-870` re-runs `draftCheckpointPort.test.ts:178-195` through the port's own import

`keybindingsStore.test.ts:2` imports `discardStoredDraft as discardStoredKeybindingDraft` **from
draftCheckpointPort** and then re-runs the generic module's own two scenarios ("removes an unreadable record with
no store at all" / "reports false when nothing is stored") over the same `memoryDraftStorage` double. Nothing
keybinding-specific is exercised — the alias is the only difference; the one real delta is that these two also
assert the returned boolean, which `draftCheckpointPort.test.ts:179,188` could assert itself in a line.

- Cost: 14 duplicated test lines in the wrong file, and a reader of the store's suite is led to believe the
  store has a discard path under test that it does not.
- Simpler form: delete both cases here; `draftCheckpointPort.test.ts` already covers them. (p6a gives this
  export a real keybinding-specific behaviour — the `isReadableKeybindingDraft` default — and a test for it
  belongs in the store's suite then, not now.)
- Severity: follow-up.

### 7. `keybindingsStore.ts:80` and `testing/draftStorage.ts:35` — two exported-ish `memoryDraftStorage` with opposite semantics

The store's internal fallback returns `insertIfAbsent/removeIf/replaceIf: () => true` over no storage at all;
the test double of the same name is a real in-memory store whose CAS methods return honest verdicts.

- Cost: `memoryDraftStorage` means "always succeeds, stores nothing" in one file and "stores and compares" in
  another, and the keybindings suite imports both concepts. One grep now answers ambiguously.
- Simpler form: rename the store's fallback to what it is — `ephemeralDraftStorage()` — which also matches the
  language its own comment already uses ("the ephemeral fallback", `keybindingsStore.test.ts:572,580`). Rename only.
- Severity: follow-up.

## Efficiency

Nothing material. `createDraftRepository.save/removeIf/replaceClassified` each re-run `decode` on a checkpoint
this build just constructed (`draftCheckpointPort.ts:142, 170, 197`), and the native backend then stringifies it
again; for a rules array of tens of entries on a per-edit path that is noise, and the normalization is the point
of the decode-both-directions contract. No repeated port reads were introduced on any path in this PR — the
double-read cases all arrive in p6a/p6b.

## Verdict

**Follow-up only — 7 findings, 0 must-fix.** Nothing here duplicates an existing helper (I checked for a
canonical-JSON/stable-stringify helper and a storage-error classification; neither exists on this tree) and
nothing is left dead. The seven are all internal-shape cleanups of code that behaves correctly: the repository's
`save`/`replaceClassified` and `removeIf`/`discardClassified` are one operation each written twice (#1), the
shared fence grew a `lostHub` method that composes two of its own public predicates for a single caller and has
no test of its own (#2), the two settle routines are named one letter apart and re-mark the checkpoint by two
different id rules (#3), one publish now has three ways to attach fields to it (#4), the CAS-refusal adoption is
spelled six times in three shapes (#5), 14 test lines in the store's suite re-run the port suite's own scenarios
through the port's own import (#6), and `memoryDraftStorage` names two opposite things (#7). #1, #3 and
#4 are the ones I would actually spend a round on later; the rest are renames and deletions. Worth recording for
the D6 owner rather than for this PR: the port has exactly one implementation (native) and the web section still
runs on the no-op fallback, so this whole mechanism is load-bearing only once p6b's store-free writer lands.
