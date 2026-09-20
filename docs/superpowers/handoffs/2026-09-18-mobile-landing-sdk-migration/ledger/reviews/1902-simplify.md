# /simplify review — PR #1902 (D6 p6b: native offline probe and store-free discard, head 654f9e4ea)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M pr-1900-review..pr-1902-review` (this piece's own diff, stacked on #1900):
`appwire-client/typescript/{keybindingsStore.ts,index.ts}` and
`mobile-native/src/{NativePreferencesProvider.tsx,nativePreferenceDrafts.ts}` plus the two test files. Line
numbers are at this PR's head.

`rawStringDraftBackend` is the right move and the best thing in this piece: the production backend object that
used to live inline in `NativePreferencesProvider.tsx` becomes one function tests can drive over a Map, so the
parse/compare logic is proved rather than mirrored. Findings #1 and #2 below are both "finish that move".

## Must fix before merge

### 1. `keybindingsStore.ts:428-436` — `decodeKeybindingDraftFields` re-implements the mapping `restoreDraft` already does at `:709-712`

`restoreDraft` builds `draft = checkpoint ? { version: 1, revision: checkpoint.baseRevision, rules:
checkpoint.rules } : null` and `writeUncertain: checkpoint?.writeUncertain ?? false`. The new exported
`decodeKeybindingDraftFields` builds the identical pair from a decoded checkpoint, in the same file, 280 lines
away, and its own doc block says so ("the same draft/writeUncertain fields restoreDraft publishes").

- Cost: the checkpoint→published-fields mapping now exists twice in one module. The next field added to
  `KeybindingDraftCheckpoint` that the UI reads (or a change to how `revision` is derived from `baseRevision`)
  has to be made in both, and the offline path silently keeps publishing the old shape if it is not — which is
  precisely the class of bug this piece's fourth finding (stale `draft`/`writeUncertain` on the offline path) was
  fixing.
- Simpler form: one private `draftFieldsFrom(checkpoint: KeybindingDraftCheckpoint | null)` returning
  `{draft, writeUncertain}`; `restoreDraft` spreads it (`...draftFieldsFrom(drafts.load())`, keeping its own
  `staleDraft` line, which needs `draft` — bind it first), and `decodeKeybindingDraftFields` becomes
  `try { return draftFieldsFrom(draftCheckpoint(value)); } catch { return draftFieldsFrom(null); }`. ~8 lines,
  no behaviour change, and the mapping has one home.
- Severity: must-fix-before-merge (the diff copies existing logic in the same file rather than extracting it).

## Follow-up

### 2. `mobile-native/src/draftBackend.testkit.ts:17-51` is now a parallel reimplementation of `rawStringDraftBackend`, and the file's own tests use both fakes side by side

`rawStringDraftBackend` (`nativePreferenceDrafts.ts:152-184`) exists so tests exercise production's
parse/compare. But `fakeDraftBackend` still hand-rolls `get/set/insertIfAbsent/delete/deleteIf/replaceIf` with
its own `canonicalJson` compares, and `nativePreferenceDrafts.test.ts` keeps both: the first two
`nativeKeybindingDrafts` cases (`:46-62`) drive `fakeDraftBackend` and prove the fake's own boolean, while the
key-order and unparseable cases right below them (`:64`, `:80`) drive `rawBytesBackend()` (`:34-43`) and prove
production's; the `insertIfAbsent`/`replaceIf` pairs at `:132-170` go back to the fake.

- Cost: two fakes for one interface in one test file, one of which re-implements the compare the PR's own
  comment calls out as the thing not to re-implement; a CAS rule fixed in `rawStringDraftBackend` leaves the
  `fakeDraftBackend` consumers (`nativePreferenceDrafts.test.ts`, `nativePreferences.test.ts:342`,
  `preferenceDraftRepository.test.ts`, `navigationActionRepository.test.ts`) still asserting the old rule.
- Simpler form: move `rawBytesBackend()` (`:34-43`) into `draftBackend.testkit.ts` and rebuild
  `fakeDraftBackend` on it —
  `rawStringDraftBackend(mapRawStorage(), idSeq)` with `store` replaced by (or kept as a JSON-encoding view
  over) the raw Map. The four consumer files poke `b.store.set(key, object)`, so this is a mechanical
  `JSON.stringify` pass across them, not a redesign — which is why it is follow-up rather than must-fix. -30
  lines of duplicated compare logic, and every existing native draft test starts running through production code.
- Severity: follow-up.

### 3. `nativePreferenceDrafts.ts:242-244` and `:253-255` — two exported one-expression predicates whose tests restate the expression

`draftUnreadableAfterDiscard(current) => current === "unreadable"` and
`clearsErrorAfterOfflineDiscard(outcome) => outcome !== null`, with 8 test lines
(`nativePreferenceDrafts.test.ts:412-445`) that enumerate the three/five members of the input union against the
same comparison.

- Cost: two cross-module exports and two `describe` blocks that cannot fail for any reason other than someone
  editing the operator; a reader at the call sites
  (`NativePreferencesProvider.tsx:159`, `KeybindingPreferencesScreen.tsx:225`) has to open another module to
  learn that `draftUnreadable` means `current === "unreadable"`.
- Simpler form: inline both comparisons at their call sites and keep the paragraph-long comments (which are the
  real content) there. The genuine testability argument — the provider and the screen cannot be rendered by
  this package's vitest — does not apply to a comparison with no branches.
- Severity: follow-up.

### 4. `nativePreferenceDrafts.ts:210-231` — `readDraftOutcome` is now a projection of `readDraftOutcomeWithValue`, and both are exported and separately tested

`readDraftOutcome` is `readDraftOutcomeWithValue(storage, isReadable).outcome`, with one production caller
(`NativePreferencesProvider.tsx:110`, the cold probe). `classifyDraftRead` (`:193`) is likewise exported with no
consumer but `readDraftOutcomeWithValue` and its own tests, and
`nativePreferenceDrafts.test.ts:255` explicitly asserts that the two read functions agree.

- Cost: three exported entry points to one read, two of which are one-line projections of the third, plus three
  `describe` blocks over the same 4-case table (absent / readable / unreadable / storageUnavailable) at
  `:212`, `:230`, `:251`.
- Simpler form: export `readDraftOutcomeWithValue` only; the probe destructures `{ outcome }`. Unexport
  `classifyDraftRead`. One table-driven suite over the surviving function. Roughly -20 lines of source and test.
- Severity: follow-up.

### 5. `NativePreferencesProvider.tsx:87-94` + `:158-191` — the unreadable classification is published twice and kept in step by hand

The provider holds `offlineDraftUnreadable` state AND writes the same value into
`bound.snapshot.keybindings.draftUnreadable` (plus `storageUnavailable: false`, `error: null`) through an 18-line
`setBound` patch. p6c then has to add `offlineAwareDraftUnreadable` to decide which copy wins.

- Cost: one fact in two places with a hand-written tiebreak in a third file; the `setBound` patch also writes
  fields the model owns, behind the model's back, so a subsequent model publish silently reverts them. The
  probe's own state is three parallel values (`offlineDraftUnreadable`, `offlineStorageUnavailable`,
  `probedHubId`) that are only ever meaningful together.
- Simpler form: resolve once at the provider and expose one value on the context —
  `keybindingsDraftUnreadable` (live snapshot while connected, probe otherwise) — and collapse the three probe
  values into one `useState<{hubId, unreadable, storageUnavailable}>`. p6c's `offlineAwareDraftUnreadable` and
  its three tests then disappear; `setBound` keeps only the `draft`/`writeUncertain` publish it genuinely needs.
  This spans p6b and p6c, so it is a follow-up on the pair rather than an edit to either.
- Severity: follow-up.

## Efficiency

### 6. `NativePreferencesProvider.tsx:127-193` — one tap on "Discard unreadable draft" makes three to four synchronous SQLite reads of the same key

`discardStoredKeybindingDraft` loads (`getItemSync` + `JSON.parse`), decodes it via `isReadableKeybindingDraft`,
then `removeIf` → `deleteIf` → `matchesStoredBytes` reads and parses the same key again, and on a CAS refusal
re-reads a third time; `readDraftOutcomeWithValue` (`:147`) then reads a fourth time and
`decodeKeybindingDraftFields` (`:173`) decodes once more.

- Cost: 3-4 `Storage.getItemSync` calls and as many `JSON.parse`/decode passes on the UI thread for one tap, on
  a record that is at most a few KB. Small in absolute terms; worth naming because the count is invisible at the
  call site (each helper reads "once") and the probe effect re-reads on every connectivity change
  (`[hubId, state]`).
- Simpler form: if it ever matters, have the discard take the bytes it already read as the identity it removes
  and return the post-removal read from inside one guard — i.e. one `discardAndReclassify(storage, isReadable)`
  in the package that owns the whole load/remove/re-read sequence, which is also where finding #3 of the p6a
  review wants the classification to live. No change advised now.
- Severity: follow-up.

## Verdict

**Fix before merge — 6 findings, 1 must-fix.** The must-fix is small and mechanical:
`decodeKeybindingDraftFields` copies `restoreDraft`'s checkpoint→`{draft, writeUncertain}` mapping into a second
place in the same file instead of both calling one `draftFieldsFrom`, which is exactly the duplication that would
make the offline path drift from the live one (#1, ~8 lines). Everything else is follow-up. Two of them are
"finish the move this PR started": `rawStringDraftBackend` was added so tests stop re-implementing the
production compare, yet `fakeDraftBackend` still does, and the same test file now drives both fakes (#2); and
the provider publishes the unreadable classification twice, once as its own state and once patched into the
model's snapshot, which is what forces p6c to invent a tiebreak (#5). The rest are surface: two exported
one-comparison predicates with tests that restate the comparison (#3), three exported entry points and three
duplicate test tables for one port read (#4), and a discard tap that reads the same key three or four times
through helpers that each look like a single read (#6).
