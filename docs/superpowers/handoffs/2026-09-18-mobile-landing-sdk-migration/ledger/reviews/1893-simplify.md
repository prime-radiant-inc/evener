# /simplify review — PR #1893 (prunedForStringify is a bounded lazy walk, head 8342a7331)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M pr-1892-review..pr-1893-review`: `appwire-client/typescript/reducer.ts` (+`reducer.test.ts`).
Line numbers are at this PR's head.

## Must fix before merge

### 1. `reducer.ts:1059` — `Array.isArray(value) ||` is a dead disjunct

```ts
return Array.isArray(value) || (typeof value === "object" && value !== null) ? "…" : value;
```

`Array.isArray(v)` being true implies `typeof v === "object" && v !== null` (true for cross-realm arrays and for
a Proxy over an array too — which this PR's own Proxy test exercises). The first operand can never change the
result.

- Cost: a reader has to work out whether the two clauses are catching different shapes — the natural reading is
  that arrays are somehow *not* objects here, which is exactly the confusion the line below (:1066-1067, where
  `Array.isArray` is checked first because the order genuinely matters) can seed.
- Simpler form: `return typeof value === "object" && value !== null ? "…" : value;` — one-token deletion,
  provably identical output for every input.
- Severity: must-fix-before-merge (dead code).

## Follow-up

### 2. `reducer.ts:1030` — `RAW_WARNING_FRAME_MAX_FIELD_CHARS = RAW_WARNING_FRAME_MAX_CHARS` is an alias with no independent value

The new constant is defined as the old one and never diverges from it, but the comment block above it presents
the whole group as "generous bounds for the prune" — so a reader has to check whether the two 2000s are
coincidence or coupling.

- Cost: either the per-field prune bound and the final frame bound are the same number on purpose (then the alias
  is noise) or they will diverge (then the alias should carry its own literal and its own reason). Today it is
  neither, and the three sites that read it (:1054, :1055, :1082) look independently tunable but are not.
- Simpler form: pick one. Either delete the alias and use `RAW_WARNING_FRAME_MAX_CHARS` at all three sites, or
  give it its own literal (e.g. `= 500`, since a pruned field only ever has to survive into a 2000-code-point
  envelope alongside up to 49 siblings) and say why in one line.
- Severity: follow-up.

### 3. `reducer.test.ts:4656,4695,4726,4768,4804` — five more copies of the 9-line hydrate preamble, and a third copy of the `JSON.stringify` spy

Every new test opens with the identical `testHydrate()` + `turn/started` `applyNotification` block, and three of
them (:4656, :4726, :4804) build the same `vi.spyOn(JSON, "stringify")` recorder verbatim. The warning-fold
cluster in this file is now nine tests deep (:4591 onward) with the same preamble in each.

- Cost: ~70 duplicated lines in the new diff alone; the next required field on `turn/started` is a nine-site
  edit in one cluster, and the three stringify spies can drift into recording different things while all still
  passing. The file already establishes the local-helper convention for precisely this (`hydrateWithShaImage`
  at :2190, `withActiveRetry` at :6172).
- Simpler form: two local helpers above the cluster — `warningTurnModel(): ThreadModel` (hydrate + `turn/started`,
  returning the model) and `recordStringifyOutputLengths(): { lengths: number[]; restore: () => void }` — then
  each test is its `params` plus its assertions, 6-10 lines instead of 30. Pure scaffolding move, no assertion
  changes.
- Severity: follow-up. Test-only, and part of the duplication predates this PR.

### 4. `reducer.ts:1054-1056` and `:1082` (altitude, lands on #1894) — the prune hand-rolls the string bound that #1894 then names

The prune truncates a string value (`value.slice(0, MAX) + "…"`) and a property name (`key.slice(0, MAX) + "…"`)
with two inline copies of the same expression. #1894, 40 lines below in the same file, introduces
`boundedCodePoints` to do exactly this job surrogate-safely.

- Cost: three encodings of "bound one wire string" in one file, two of which can cut a surrogate pair in half —
  the specific failure the sibling test at :4617 exists to prevent for the outer frame, left open one level in.
- Simpler form: see #1894 finding 1 — one `boundedPrefix(s, maxCodePoints)` primitive, with `boundedCodePoints`
  and both prune sites as its three callers.
- Severity: follow-up here; tracked as must-fix on #1894, where the helper it duplicates actually appears. Fixing
  it on this PR alone would just move the work.

## Verdict

**Fix before merge — 4 findings, 1 must-fix.** The mechanism is the right one and it is well scoped: a single
recursive prune with four caps, a counted `for...in` instead of `Object.entries`, and a Proxy test that actually
proves the walk is lazy rather than asserting on the output. The one must-fix is a one-token deletion of a
redundant `Array.isArray` disjunct at :1059 — zero risk, provably behaviour-identical, and worth doing before
merge only because leaving it invites the next reader to believe arrays are handled specially there. The other
three are real but cheap-to-review follow-ups: the `MAX_FIELD_CHARS` alias that is not actually a separate knob,
the nine-deep repetition of the hydrate preamble in the test cluster, and the string-bound duplication that
#1894 is the right place to close.
