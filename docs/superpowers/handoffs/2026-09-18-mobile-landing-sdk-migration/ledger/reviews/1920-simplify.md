# /simplify — PR #1920 (B3a: phone adopts turn-summed session usage totals)

Head reviewed: `d40bf549bf3273d4154dae3eb668da692cc089b2`, own diff vs `origin/main`
(`de36efc18`). Quality only — reuse, simplification, efficiency, altitude. Correctness is
RoboRev's. Nothing below changes what the phone displays (Jesse's ruling 6 stands: the phone shows
the same summed session figure the web does).

---

## 1. Reuse: the shared seam is right, and there is nothing left to lift — no finding

`tokenUnitLabel` (`appwire-client/typescript/threadUsage.ts:60-62`) called from the web's
`DetailsPanel.tsx:131` and the native footer (`transcriptPresentation.ts:54-55`) is the whole of
what the two surfaces can share, and the PR found it. Checked the rest of the overlap:

- `sessionTokens` / `turnUsageTokens` / `SessionTokens` were already in the package and already
  shared; `detailsAccounting.ts` re-exports rather than re-implements.
- The web's Usage section renders only the derived ↑/↓ pair, through `formatTokenCount` in one
  `DetailRow` (`DetailsPanel.tsx:183-188`), with **no** Cached or Total row anywhere
  (`git grep cacheReadTokens|totalTokens` over `cmd/evener-hub/frontend/src` hits only
  `EntityRef.tsx` and `turnMeta.ts`, both per-turn/per-job). So `usageRows` — four rows, native
  labels, `toLocaleString` — has one caller by nature, not by neglect. Leaving it in
  `mobile-native` is the right altitude.
- The label's tests moved with the label, to `threadUsage.test.ts`. Correct home.

Nearest duplication, deliberately left alone: `mobile-native/src/ActivityDelegateDetails.tsx:218-236`
hand-builds the same four labelled token rows ("Input tokens" / "Cached input tokens" / …) for a
*delegate's* cumulative usage, and `cmd/evener-hub/frontend/src/panes/session/transcript/EntityRef.tsx:73`
is a third variant via `formatUsagePair`. Different domain objects (a delegate, a job card),
different widgets, untouched by this PR. Journal it; folding them into `usageRows` here would be
scope creep on a row that is already at round 6.

---

## 2. `accountingFor` decides one config flag three times and builds its result through a presence ternary — follow-up

`mobile-native/src/transcriptPresentation.ts:222-241`:

```ts
const tokens = config.advanced.tokenCounts ? sessionTokens(conversation) : null;
const cacheReadTokens = config.advanced.tokenCounts ? noZero(conversation.usage?.cacheReadTokens) : undefined;
const totalTokens = config.advanced.tokenCounts ? noZero(conversation.usage?.totalTokens) : undefined;
return {
  usage:
    tokens || cacheReadTokens !== undefined || totalTokens !== undefined
      ? {
          ...(tokens ?? {}),
          ...(cacheReadTokens !== undefined ? { cacheReadTokens } : {}),
          ...(totalTokens !== undefined ? { totalTokens } : {}),
        }
      : null,
  cost: config.advanced.estimatedCost ? (conversation.cost ?? null) : null,
};
```

**Cost.** Nineteen lines where six do the work. `config.advanced.tokenCounts` means "token counts
are hidden" once, but the decision is re-taken per field, so a fourth field needs a fourth ternary
and a fourth conjunct in the presence test — three edits to add one row. The conditional spreads
exist to avoid writing `undefined`-valued keys, but `mobile-native` does not enable
`exactOptionalPropertyTypes` (not in `mobile-native/tsconfig.json`, not in `tsconfig.check.json`,
not in `expo/tsconfig.base.json`) and every `EvenerUsage` field is optional
(`types.gen.ts:783-788`), so a plain literal type-checks.

**Simpler form.**

```ts
const cost = config.advanced.estimatedCost ? (conversation.cost ?? null) : null;
if (!config.advanced.tokenCounts) return { usage: null, cost };
return {
  usage: {
    ...(sessionTokens(conversation) ?? {}),
    cacheReadTokens: noZero(conversation.usage?.cacheReadTokens),
    totalTokens: noZero(conversation.usage?.totalTokens),
  },
  cost,
};
```

**Caveat, stated plainly.** This drops the `usage: null` spelling of "token counts are on, but
there is no data", which the only consumer already renders identically to an empty object:
`usageRows(null)` and `usageRows({})` both return `[]` (`:52-64`), and `TranscriptUsage`'s
`rows.length === 0 && cost === null` guard (`TranscriptUsage.tsx:6-7`) is the sole read site
(`SessionAccounting["usage"]` has no other consumer in `mobile-native` — verified by grep). The one
test that asserts the internal spelling, "treats a zero cacheReadTokens/totalTokens the same as an
absent one" (`transcriptPresentation.test.ts`), would assert
`usageRows(result.usage!.usage)).toEqual([])` instead — which is the thing a reader of that test
actually cares about. If the lane would rather keep `null` as a load-bearing sentinel, take the
flag-read collapse alone (`if (!config.advanced.tokenCounts) return { usage: null, cost };`) and
leave the presence ternary; that half is free.

---

## 3. Flattening `SessionTokens` into the cumulative fields is what creates the scope-leak hazard the comments guard by hand — follow-up (altitude)

`transcriptPresentation.ts:34-39`:

```ts
usage:
  | (Partial<SessionTokens> & Pick<EvenerUsage, "cacheReadTokens" | "totalTokens">)
  | null;
```

`Partial<SessionTokens>` makes `inputTokens`, `outputTokens` **and `scope`** independently
optional, so the type says nothing about the one invariant that matters here: the derived pair and
its scope travel together, and `scope` describes *only* that pair. The PR then defends that
invariant in prose — eight comment lines at `:26-32`, five more at `:47-51` — plus two dedicated
tests ("keeps the cumulative breakdown's scope independent of a turn-summed loaded result", "labels
Input/Output with the derived pair's own scope and Cached/Total plainly…").

**Cost.** An invariant held by comment and test rather than by construction. Any future reader with
a `usage` object in hand can write `tokenUnitLabel(usage.scope)` for the Total row and nothing
stops them; that is the exact mistake round 6 already had to find once.

**Simpler form.** Nest the derived pair instead of merging it in:

```ts
export interface SessionAccounting {
  usage: {
    derived: SessionTokens | null;   // input/output plus the scope that describes them
    cacheReadTokens?: number;        // whole-session; carries no scope
    totalTokens?: number;
  } | null;
  cost: string | null;
}
```

`usageRows` then reads `tokenUnitLabel(usage.derived?.scope)` for Input/Output and
`tokenUnitLabel("session")` for Cached/Total, and a scope leak stops being expressible. `Partial<>`
disappears, `accountingFor`'s spread-of-a-maybe-object (`...(tokens ?? {})`) becomes
`derived: sessionTokens(conversation)`, and the two prose blocks shrink to one line each. Composes
with finding 2; together they are roughly −25 lines across the file.

---

## 4. `usageRows`'s tuple-plus-hand-written-predicate filter — follow-up (marginal)

`transcriptPresentation.ts:56-63`:

```ts
const candidates: [UsageRow["label"], number | undefined, string][] = [ … ];
return candidates
  .filter((row): row is [UsageRow["label"], number, string] => row[1] !== undefined)
  .map(([label, value, unit]) => ({ label, value, unit }));
```

**Cost.** Three positional tuple slots the reader has to hold, and a hand-written type predicate
that restates the filter it guards — nothing checks the two agree, and a widened predicate would
narrow a value that is still `undefined`. Small, but this is four rows of output.

**Simpler form.**

```ts
const rows: UsageRow[] = [];
const add = (label: UsageRow["label"], value: number | undefined, unit: string) => {
  if (value !== undefined) rows.push({ label, value, unit });
};
add("Input", usage.inputTokens, derivedUnit);
add("Output", usage.outputTokens, derivedUnit);
add("Cached", usage.cacheReadTokens, cumulativeUnit);
add("Total", usage.totalTokens, cumulativeUnit);
return rows;
```

Same length, no tuples, no predicate. Take it or leave it — I would not spend a round on this one
alone, but it falls out for free if finding 3 is taken.

---

## 5. `tokenUnitLabel(undefined)` to mean "a whole-session figure" — follow-up

`transcriptPresentation.ts:55`: `const cumulativeUnit = tokenUnitLabel(undefined);`. The
`| undefined` arm of `tokenUnitLabel` exists for the web's `tokens?.scope` (`DetailsPanel.tsx:131`),
where "no figure at all" is the caller's situation. Here the situation is the opposite — a figure
that is definitively whole-session — and passing `undefined` makes a reader open `threadUsage.ts` to
learn that `undefined` and `"session"` map to the same string.

**Simpler form.** `tokenUnitLabel("session")`. One word, states the intent, and it stays correct if
a third scope is ever added while `undefined` keeps meaning "unknown".

---

## 6. Efficiency — nothing to flag

`sessionTokens` is one pass over `model.turns` per projection; `projectNativeTranscript` calls
`accountingFor` once and the call site is memoized on the conversation identity
(`screens.tsx:1256-1263`), so the scan runs when the conversation object changes, which it already
had to. `usageRows` builds four entries per footer render. `noZero` is two comparisons. No repeated
scans, no identity comparisons in loops.

`noZero` itself (`:218-220`) is a new three-line helper; checked for an existing one to reuse and
there is none (`sessionTokens` applies the same Go-zero-means-absent rule inline at
`threadUsage.ts:32`, on a pair rather than a scalar). Keeping it local and named is the right call,
and its comment earns its five lines by naming the wire rule.

---

## Verdict

**Follow-up only — 0 must-fix, 4 follow-ups.** This is the cleaner of the two B3 rows. The reuse
question is answered correctly: `tokenUnitLabel` is the only platform-independent piece of the
phone's new footer, it went into `threadUsage.ts` with its tests, the web now calls it instead of
carrying the ternary inline, and `usageRows` stayed native because the web renders no Cached/Total
rows to share. No duplicated helper, no dead code, no efficiency problem — the projection is
memoized and every scan is single-pass. The four follow-ups are all in one 25-line region of
`transcriptPresentation.ts` and compose into one small PR: `accountingFor` re-decides
`config.advanced.tokenCounts` three times and assembles its result through a presence ternary with
two conditional spreads that `mobile-native`'s tsconfig does not require (finding 2, −13 lines);
flattening `Partial<SessionTokens>` in beside `cacheReadTokens`/`totalTokens` is what makes the
"scope must not leak onto the cumulative rows" invariant a matter of comments and tests rather than
of construction, where a nested `derived: SessionTokens | null` would make the leak inexpressible
(finding 3, the one with real weight); plus a tuple-and-predicate filter and a
`tokenUnitLabel(undefined)` that both read as puzzles for no gain. None of it blocks the merge.
