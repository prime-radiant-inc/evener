# /simplify review — PR #1900 (D6 p6a: draft read-outcome classification, head 9723dbc1d)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M pr-1808-review..pr-1900-review` (this piece's own diff, stacked on #1808):
`appwire-client/typescript/{draftCheckpointPort.ts,keybindingsStore.ts,index.ts,testing/draftStorage.ts}` and
`mobile-native/src/{nativePreferenceDrafts.ts,draftBackend.testkit.ts}` plus the three test files. Line numbers
are at this PR's head.

Two things this piece gets right and I am not second-guessing: `canonicalJson` is genuinely new (`git grep -E
'canonicalJson|stableStringify|canonicalize|sortKeys'` over `appwire-client/typescript
cmd/evener-hub/frontend/src mobile-native/src` finds no pre-existing stable-stringify to reuse), and pointing
`testing/draftStorage.ts` and `draftBackend.testkit.ts` at it closes the real test/production divergence p9's
panel raised. The `stored === null` / `!store.has(key)` guards added alongside are the right shape.

## Must fix before merge

None.

## Follow-up

### 1. `keybindingsStore.ts:402-413` — `discardStoredKeybindingDraft` is a wrapper whose only content is a default argument nothing in production uses

The wrapper exists solely to change `discardStoredDraft`'s `isReadable` default from `() => false` to
`isReadableKeybindingDraft`. The one production caller passes the decoder explicitly anyway
(`mobile-native/src/NativePreferencesProvider.tsx:133`, `discardStoredKeybindingDraft(storage,
isReadableKeybindingDraft)`), so the default is exercised only by its own test
(`keybindingsStore.test.ts:916-926`).

- Cost: two exported functions, two doc blocks and two package-root exports for one operation, plus a
  `DiscardStoredDraftResult | "storageUnavailable"` return type re-spelled a third time. The generic's
  `() => false` default is itself the footgun the wrapper's comment describes — it silently deletes a readable
  record — and it survives this PR untouched for any future caller.
- Simpler form: delete the wrapper, make `isReadable` a required parameter of `discardStoredDraft`
  (`draftCheckpointPort.ts:89-91`), and have the provider call `discardStoredDraft(storage,
  isReadableKeybindingDraft)` — `isReadableKeybindingDraft` is already exported from the root for
  `readDraftOutcome`'s sake. Removes ~12 lines and one export, and makes the dangerous default impossible
  instead of merely discouraged.
- Severity: follow-up.

### 2. `draftCheckpointPort.ts:73` + `:91` — the storage-failure member is bolted onto the named outcome type at every use site instead of being in it

`DiscardStoredDraftResult` is `"removed" | "absent" | "refused"` and every signature that carries it writes
`DiscardStoredDraftResult | "storageUnavailable"`. On this tree that union is hand-spelled at
`draftCheckpointPort.ts:91`, `keybindingsStore.ts:411`, and (in the stacked pieces)
`nativePreferenceDrafts.ts:253` and `NativePreferencesProvider.tsx:64` and `:127` — the last adding `| null` on
top. The same pattern repeats for `DraftReadOutcome | "storageUnavailable"`
(`nativePreferenceDrafts.ts:154` here, plus two more sites in p6b).

- Cost: five hand-written spellings of one type across three modules and two packages; a caller that forgets the
  extra member type-errors in a way that reads as a missing case rather than a missing state, and the
  exhaustiveness of `switch`/`if` chains over the outcome is only as good as whoever re-typed it last.
- Simpler form: `"storageUnavailable"` IS an outcome of the call, so put it in the named type — either
  `DiscardStoredDraftResult = "removed" | "absent" | "refused" | "storageUnavailable"`, or keep the narrow type
  and add `export type DiscardStoredDraftOutcome = DiscardStoredDraftResult | "storageUnavailable"` and use it
  everywhere (same for `DraftReadOutcome`). Every signature then names one type.
- Severity: follow-up. This is the highest-leverage item here because p6b and p6c each re-spell it again.

### 3. `nativePreferenceDrafts.ts:136-163` — the read classification lives in mobile-native while the discard classification lives in the package, and they classify the same load

`discardStoredDraft` (`draftCheckpointPort.ts:89-107`) does: `load()` → absent? → `isReadable(value)`? → act.
`classifyDraftRead`/`readDraftOutcome` (`nativePreferenceDrafts.ts:138-163`, `DraftReadOutcome` at `:136`) do: `load()` → absent? →
`isReadable(value)`? → report. That is one state machine — "what did this port's load name" — written twice, on
two sides of the package boundary, with two vocabularies for the same three states (`absent`/`refused` vs
`absent`/`readable`/`unreadable`).

- Cost: the native app owns half of a package-level concept. `readDraftOutcome`'s signature is
  `Pick<KeybindingDraftStorage, "load">` and its body touches nothing native — no Storage, no React, no
  `parseDraftBytes` — so there is nothing keeping it in mobile-native except where it was written. The web will
  need the same classification the day it grows a real draft port, and will not find it.
- Simpler form: move `DraftReadOutcome`, `classifyDraftRead` and `readDraftOutcome` into
  `draftCheckpointPort.ts` beside `discardStoredDraft`, and express the discard on top of the classification
  (`const outcome = classifyDraftRead(value, isReadable); if (outcome === "readable") return "refused";`). One
  classification shared by load and discard, one place to add a fourth state.
- Severity: follow-up.

### 4. `keybindingsStore.ts:149` + `:1089` + `:1291` — `draftUnreadable` and `storageUnavailable` are two booleans encoding three states, and two gates carry the correction

An unreadable record sets both flags true (`restoreDraft`, `:715`), so every gate that means "the port is dead"
has to spell `storageUnavailable && !draftUnreadable` — `refreshOverrides` (`:1089`) and `assertDiscardable`
(`:1291`) both do. Nothing enforces the invariant that `draftUnreadable` implies `storageUnavailable`.

- Cost: 2^2 states for a 3-state fact, the illegal fourth state (`draftUnreadable` without
  `storageUnavailable`) representable and silently meaningful, and the `&& !draftUnreadable` correction to
  remember at every future gate. `mobile-native/src/nativePreferences.ts` (in p6c) then mirrors both booleans
  into `PreferenceState<T>`, doubling the surface.
- Simpler form: one field — `draftFailure: null | "port" | "record"` — with the two booleans, if hosts need
  them, derived at the snapshot boundary. Gates become `draftFailure === "port"`. This changes the published
  snapshot shape, so it is a deliberate follow-up rather than a quick edit; I would file it rather than land it
  in this stack.
- Severity: follow-up.

### 5. `nativePreferenceDrafts.ts:29-44` and `:49-63` — the two tagged-marker predicates are copy-paste with variation

`isStoredNullRecord` and `isUnparseableDraftBytes` are the same five-clause shape (`typeof === "object"`,
`!== null`, `Object.keys(value).length === N`, `kind === K`, plus one field check).

- Cost: ~20 lines where ~10 would do, and the exact-shape rule (the "only key" argument, spelled out in a
  12-line comment on the first) has to be re-derived when a third marker arrives.
- Simpler form: one `isTaggedRecord(value, kind, extraKeys: readonly string[])` returning a boolean, with the
  two predicates as one-liners over it, and the shape argument documented once.
- Also: `STORED_NULL_RECORD` (`:28`) is exported but has no consumer outside its own module — `git grep
  STORED_NULL_RECORD pr-1904-review` finds only `nativePreferenceDrafts.ts` itself (not even the tests). Drop
  the `export`.
- Severity: follow-up.

### 6. `keybindingsStore.test.ts:892-926` — three of the four new discard tests re-run `draftCheckpointPort.test.ts`'s own table

`draftCheckpointPort.test.ts:178-320` already covers removed / absent / refused / the CAS-refusal re-read / all
three `storageUnavailable` paths over the same `memoryDraftStorage` double. The store suite's "removes an
unreadable record with no store at all" (`:892`), "reports absent when nothing is stored" (`:901`) and "refuses a
record a concurrent writer replaced with a valid one" (`:907`, which passes the decoder explicitly) assert the
generic's behaviour, not the store's.

- Cost: ~25 duplicated test lines that will diverge from the port suite's table the first time an outcome is
  added.
- Simpler form: keep only `:916` ("refuses a readable record even when the caller passes no isReadable of its
  own") — the one case that proves this file's actual contribution, the keybinding-specific default — and delete
  the other three. (If finding #1 is taken, `:916` goes too, along with the default it tests.)
- Severity: follow-up.

## Efficiency

`matchesStoredBytes` (`nativePreferenceDrafts.ts:94-98`) parses the stored bytes and then re-serializes both
sides through `canonicalJson`, so each CAS compare is one parse plus two serializes of small objects. That is
inherent to a byte-aware compare and not worth changing. No double port reads are introduced by this piece on a
path that did not have one — `discardStoredDraft`'s second `load()` (`:103`) only runs after a failed CAS and is
the point of the fix. The real read-count growth is in p6b, noted there.

## Verdict

**Follow-up only — 6 findings, 0 must-fix.** Nothing duplicates a pre-existing helper (there was no
canonical-JSON and no storage-error classification on the tree before this piece) and nothing is dead beyond one
surplus `export`. The substantive three are all about where the classification lives and how it is typed:
`"storageUnavailable"` is hand-appended to the named outcome type at five sites across two packages and p6b/p6c
add three more (#2); the read classification (`classifyDraftRead`/`readDraftOutcome`) sits in mobile-native
although it touches nothing native and is the same load-then-classify state machine `discardStoredDraft` already
runs inside the package (#3); and `draftUnreadable` beside `storageUnavailable` makes three states out of two
booleans, with the `&& !draftUnreadable` correction now carried at two gates and mirrored into the native
snapshot type in p6c (#4). The other three are cheap: a wrapper function that exists only for a default its one
production caller does not use (#1), two marker predicates that are one parameterized predicate (#5), and three
new store-suite tests that re-run the port suite's own table (#6). #2 is the one I would fix before the stack
grows a fourth re-spelling.
