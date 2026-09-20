# /simplify review — PR #1894 (warning strings are bounded before any copy, head f5c751ea4)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M pr-1893-review..pr-1894-review`: `appwire-client/typescript/reducer.ts` (+`reducer.test.ts`).
Line numbers are at this PR's head.

## Must fix before merge

### 1. `reducer.ts:1054-1056` and `:1082` — `prunedForStringify` hand-rolls the truncation that this PR's own `boundedCodePoints` is

This PR adds `boundedCodePoints` at :1102 ("truncates a string to RAW_WARNING_FRAME_MAX_CHARS code points,
safely"). Forty lines above it, `prunedForStringify` — added one PR earlier, same file, same author, same
purpose — does the same job twice by hand:

```ts
value.length > RAW_WARNING_FRAME_MAX_FIELD_CHARS ? `${value.slice(0, RAW_WARNING_FRAME_MAX_FIELD_CHARS)}…` : value   // :1054
key.length   > RAW_WARNING_FRAME_MAX_FIELD_CHARS ? `${key.slice(0, RAW_WARNING_FRAME_MAX_FIELD_CHARS)}…`   : key     // :1082
```

- Cost: three encodings of "bound one wire string" in one 130-line region, and the two inline copies are the
  *unsafe* encoding — a plain UTF-16 `slice` can cut a surrogate pair in half, which is the exact failure the
  test at `reducer.test.ts:4617` exists to prevent for the outer frame. The lone surrogates the prune can emit
  then ride through `JSON.stringify` as `\ud83d` escapes. Any future change to the bound (units, marker, the
  fast path) has to be made in three places.
- Simpler form: extract the surrogate-safe truncation once, parameterized on the cap, and make all three sites
  callers:
  ```ts
  function boundedPrefix(s: string, maxCodePoints: number): string { /* today's boundedCodePoints body, cap as a param */ }
  const boundedCodePoints = (s: string) => boundedPrefix(s, RAW_WARNING_FRAME_MAX_CHARS);
  // prune: `${boundedPrefix(value, RAW_WARNING_FRAME_MAX_FIELD_CHARS)}…`  (and the same for `key`)
  ```
  ~10 lines net deletion. Output is byte-identical for every BMP string (i.e. every real frame); the only delta
  is that an astral string now truncates on a code-point boundary instead of splitting a pair, which is the
  direction the file's own surrogate test already asserts. Keeping the caps as separate constants means the
  prune's per-field budget does not silently double.
- Severity: must-fix-before-merge (duplicates a helper introduced in the same file by the same stack; the
  repo rule is extract-and-share rather than copy).

## Follow-up

### 2. `reducer.ts:1019` + `:1151,1158-1160` — the fold answers "is there text" by allocating a bounded copy, then allocates the same copy again

`hasWarningText` now returns `boundedCodePoints(value).trim() !== ""` — a `slice` + `Array.from` + `join` just to
answer a yes/no. `foldWarningParams` then calls `boundedCodePoints` on the same value a second time for each of
title, hint and source, and wraps `text` in a third `boundedCodePoints` at :1151 even though the
`rawWarningFrame` branch already returned a bounded string from :1127.

- Cost: for an oversized frame, six bounded walks (two per field) where three would do, plus one redundant
  re-bound of an already-bounded frame. The exported predicate is also called on already-bounded model values by
  three consumers — `WarningItem.tsx:39-40`, `transcriptProjector.ts:168`, `mobile/src/conversation/project.ts:549-550`
  (the latter twice, once via `.filter(hasWarningText)`) — so each of those now pays a 4000-unit slice plus a
  2000-element array per call whenever a value is over the bound. Absolute cost is small (the `s.length <= MAX`
  fast path covers every real frame); the shape is what is off: a predicate that allocates.
- Simpler form: one function that does the walk once and answers both questions —
  `function boundedWarningText(value: unknown): string | undefined` (bound, trim-check, `undefined` when blank),
  with `hasWarningText(value) = boundedWarningText(value) !== undefined` kept as the exported type predicate for
  the three consumers, and `title: boundedWarningText(params.title)` etc. at :1158-1160. Then drop either the
  `boundedCodePoints` at :1151 or the one at :1127 (one call covers both branches — the comment at :1148-1150
  already says so). ~8 lines shorter and one bounded walk per field.
- Severity: follow-up.

### 3. `reducer.ts:998,1000,1003` — `typeof x === "string" &&` in front of a type predicate that already checks it

`hasWarningText` is `(value: unknown) => value is string`, so all three guards in `warningMessage` are
`typeof x === "string" && (typeof x === "string" && …)`.

- Cost: three lines that read as if the predicate needed a pre-narrowing it explicitly does not, which is the
  opposite of the reason `hasWarningText` was made a predicate ("so a caller narrows `unknown` in one step
  instead of repeating the typeof/trim check", :1012-1014). The `typeof` half also silently hides that
  `hasWarningText` now does real work, making the redundancy easy to keep copying.
- Simpler form: `if (hasWarningText(params.message)) return params.message;` and the same at the other two sites —
  the narrowing is identical and tsc is happy.
- Severity: follow-up.

### 4. `reducer.test.ts:4628,4667,4906,4932` — `const MAX_CHARS = 2000; // mirrors reducer.ts's RAW_WARNING_FRAME_MAX_CHARS` four times, plus three more copies of the hydrate preamble

The mirrored constant is now declared in four separate tests, each with the same comment admitting it is a copy
of an unexported constant; the three new tests also repeat the 9-line `testHydrate()` + `turn/started` preamble
(see #1893 finding 3 — the cluster is twelve tests deep after this PR).

- Cost: a change to `RAW_WARNING_FRAME_MAX_CHARS` leaves four hand-copied 2000s that keep passing by luck or fail
  in four places with no pointer to each other.
- Simpler form: export `RAW_WARNING_FRAME_MAX_CHARS` from `reducer.ts` and import it in the test (it is already
  the number three exported functions' behaviour is defined by), or failing that hoist one `const MAX_CHARS`
  above the cluster. Bundle with #1893's `warningTurnModel()` helper so the whole cluster shrinks at once.
- Severity: follow-up. Test-only.

## Verdict

**Fix before merge — 4 findings, 1 must-fix.** The change is doing the right thing at the right altitude:
bounding at the fold so every string the model can carry is bounded once, in one place, instead of each consumer
re-bounding, and the `trim`-spy and `Array.from`-spy tests pin the *allocation* rather than just the output,
which is the only way this class of finding stays closed. The must-fix is that the PR names the primitive
(`boundedCodePoints`) while leaving two hand-rolled, surrogate-unsafe copies of it forty lines above in the
prune the previous PR added — three encodings of one rule in one region, which is the exact drift the stack has
already paid for twice. The three follow-ups are all in the same small area (a predicate that allocates a copy
to answer yes/no and then allocates it again, three redundant `typeof` guards, and a constant hand-mirrored in
four tests) and are cheap to land together afterwards.
