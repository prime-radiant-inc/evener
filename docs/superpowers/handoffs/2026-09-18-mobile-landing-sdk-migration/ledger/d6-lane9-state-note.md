# D6 lane 9 state note (2026-09-18, worktree sdk-d6-split)

Stopping here per the coordinator's ~600k-token instruction. Everything below is
pushed and green; no rebase/cherry-pick is in progress (verify with
`git status --short` in the worktree - clean - and `ls .git/rebase-merge` on the
worktree's real gitdir, which does not exist).

## Chain order and heads (all pushed, all local == origin)

| # | Branch | PR | Base | New head (full 40) | Round this pass |
|---|---|---|---|---|---|
| 1 | `claude/sdk-d6-p3to5-draft-port` | #1808 (fold) | main | `d421d5489d05abe483380677c6410fee1bc5617b` | round 6 (Jesse: may take a round 7; do not decompose unless round 7 still has real findings) |
| 2 | `claude/sdk-d6-p6a-draft-read-outcome` | #1900 | fold | `9723dbc1d7e2dacef768042e2edd6932493aaade` | round 2 disposition |
| 3 | `claude/sdk-d6-p6b-native-offline-discard` | #1902 | p6a | `654f9e4ea7d7e2173e1ff818653101747447bcf8` | round 2 disposition |
| 4 | `claude/sdk-d6-p6c-screen-offline-recovery` | #1904 | p6b | `3c6436d6fdeeb9f235749b4a9cde0bcb27333048` | restack only, both assigned items already fixed pre-round (cited, no code change) |
| 5 | `claude/sdk-d6-p7-draft-generation-stamp` | #1792 | p6c | `41fc2f2e8408b601c5e3b73fdd5ef360046e3d42` | restack only, own findings were echoes already fixed upstream |
| 6 | `claude/sdk-d6-p8-settings-hub-generation` | #1841 | p7 | `c2afb38362d562370e81de5f559d162ae5ef6d4e` | restack only, panel reviews the stacked diff, no new own finding |
| 7 | `claude/sdk-d6-p9-checkpointed-draft-editor` | #1844 | p8 | `a50ac1a955481ff0f7d97011919f88b7f8cfafff` | restack + carried fold's round-6 createId fix into the extracted primitive (see below) |
| 8 | `claude/sdk-d6-p10-transcript-hub-defaults` | #1845 | p9 | `737b7cebd995baf0402d7a2f82569b4c219281a8` | round 3 (decoder tolerance + superseded-PATCH-resolves fix, on top of round 2's restack + post-apply fix) |

Each PR has a disposition comment posted this pass with its own diff stat and
gate results - read those before re-deriving anything from git log. `git diff
--stat <old-tip>..<new-tip>` for each hop is in those comments (fold: 3 files;
p6a: 9 files/951+/40-; p6b: 6 files/408+/79-; p6c-p8: restack only, ~zero net
diff besides base shift; p9: 8 files/438+/56-; p10 round 2: 9 files/1271+/12-,
round 3: 3 files/71+/12-).

## What each PR still owes, if anything

- **Fold #1808**: round 7 is Jesse's call (may or may not happen). Nothing
  outstanding from round 6's own findings.
- **p6a-p9**: nothing outstanding from the reviews read this pass
  (`1900-a24e7c59d.md`, `1902-4c51a14cb.md`, `1904-568143b05.md`,
  `1841-13c8e8353.md`, `1792-1d7571223.md`) except the three Lows filed as
  **#1918** (not blocking - see Residual below).
- **p10 #1845**: nothing outstanding from `1845-2814cfe52.md` or
  `1845-f7bfc4dbe.md`. A THIRD review round at the new tip (`737b7cebd`) has
  not run yet - next round should confirm no new findings before merge.

## The option-A resolution and where the createId fix lives at each level

Two independent instances of "drafts.createId() runs outside its own error
handler" (a local-write failure - crypto/random-source - escaping uncaught
instead of setting storageUnavailable+draftError):

1. **Fold's own `keybindingsStore.ts:persistDraft`** (the code AS IT EXISTS
   on the fold branch, inline body, no extraction yet): fixed directly in
   fold's round-6 commit (`d421d5489`). `createId()` moved inside the try.
2. **p9's own `checkpointedDraftEditor.ts:persistCheckpointedDraft`** (the
   PRIMITIVE p9's own commit `235ed7d09` extracts persistDraft's body into,
   authored before fold's round-6 fix existed): inherited the bug during
   extraction. When rebasing p9 onto the new fold-descended p8 tip, the
   extraction commit conflicted with fold's fix (both restructure the same
   function). Resolved by taking p9's delegation (`persistDraft` calls
   `persistCheckpointedDraft`) and separately fixing `createId()`-inside-try
   IN THE PRIMITIVE ITSELF (not the inline body, which no longer exists after
   extraction) - same fix, same failing-first test pattern, applied at its
   own home. This is "option A" from the earlier p9/p10 round: the shared
   primitive carries the fix, callers delegate with no behavior change.

**Verification** (per coordinator's ask, done on p10's tip `737b7cebd`):
`git grep -n 'createId()' -- 'appwire-client/typescript/*.ts'` (excluding
`*.test.ts`) shows exactly three non-test call sites, all inside their own
try: `checkpointedDraftEditor.ts:77`, `draftCheckpointPort.ts:181` (the
generic repository's own `save`, unrelated function, already correct),
`keybindingsStore.ts:1090` (`settledWrite`'s own mint, already correct,
unrelated function). Re-run this grep on any future rebase of this range to
catch a regression.

## Production/test parity backend (p6b, item 1)

`mobile-native/src/NativePreferencesProvider.tsx`'s draft backend used to be
an inline object duplicating `parseDraftBytes`/`matchesStoredBytes` logic
that only the TEST fakes (`nativePreferenceDrafts.test.ts`'s old
`rawBytesBackend`) exercised - "the tests were testing the fakes." Fixed by
extracting `nativePreferenceDrafts.ts`'s `rawStringDraftBackend(storage:
RawStringStorage, createId)`, generic over any raw string-keyed storage:

- Production: `const backend = rawStringDraftBackend(Storage, () =>
  Crypto.randomUUID());` in `NativePreferencesProvider.tsx`.
- Tests: `rawBytesBackend()` in `nativePreferenceDrafts.test.ts` now wraps
  the SAME function over a Map-backed `RawStringStorage` fake, instead of a
  parallel reimplementation. Covers stored-null, malformed, and (new this
  pass) key-order-different identity through the backend's own `deleteIf`.

If a future piece touches this backend again, extend
`rawStringDraftBackend` itself, never re-inline logic into the provider -
that's exactly the regression this pass closed.

## Gates cheat-sheet (what actually needs running, and from where)

Web/package (from `cmd/evener-hub/frontend`):
```
npm run typecheck
npx biome ci ../../../appwire-client/typescript
npm run lint
npx vitest run ../../../appwire-client/typescript          # or a targeted subset of touched *.test.ts
```
Package root (from repo root):
```
make lint-package-imports
make test-api-package        # only when index.ts/tsconfig.build.json touched
```
Native (from `mobile-native`):
```
npm run check                # tsc --noEmit
npx vitest run               # full suite is fast (~2-5s), no need to target
```
**Never** run biome/`npx biome` over `mobile-native/` or `mobile/` - no config
there, and running it from the repo root resolves an unrelated
`biome@0.3.3` that silently exits 0. `NativePreferencesProvider.tsx` and
`KeybindingPreferencesScreen.tsx` still have no direct test harness (react-
native import pulled in via `ConnectionProvider.tsx` -> `AppState`; no React
Testing Library in this package) - filed earlier as **#1908**; every
decision in those two files is unit-tested as an extracted pure function
instead (`readDraftOutcomeWithValue`, `keybindingOfflineRecovery.ts`'s three
functions, etc.), the JSX wiring itself is not.

## Residual filed this pass

**#1918** - three Lows from `1900-a24e7c59d.md` (member 1) not actioned in
this restack (Lows don't cost a review round; batched into one follow-up
issue per the merge-on-lows-then-follow-up convention):
1. `draftCheckpointPort.ts:47` `canonicalJson` returns `undefined` (not a
   `string`, contradicting its own declared return type) for a bare
   top-level `JSON.stringify(undefined)` call.
2. `testing/draftStorage.ts:52` `insertIfAbsent` checks `stored !== null`
   only, not `undefined`, inconsistent with `load()`'s absent contract.
3. `keybindingsStore.test.ts:1363` uses a real 200ms `setTimeout` instead of
   a controlled fake request/microtask assertion.

No other residual found worth filing this pass - the two Mediums from
`1902-4c51a14cb.md`'s member 2 review (the same production-backend and
double-load findings) and `1904-568143b05.md`'s two Mediums (same two,
echoed) are the SAME findings already fixed at p6b, not separate residuals.
