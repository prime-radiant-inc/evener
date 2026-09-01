# Verbosity = Expansion: Unified Tool-Call Disclosure

## Problem

The webui has five verbosity levels (chat, intent, tools, activity, full) that
control how much detail tool calls show. The current implementation has
overlapping mechanisms that don't compose cleanly:

1. **`ProjectedIntentGroup`** — groups intent entries across turns into a "N
   actions" header at chat/intent levels.
2. **`IntentToolCallRow`** (added in the previous PR) — wraps each intent entry
   as a separate disclosure component, creating a parallel rendering path to
   the normal `ToolCallItem → ToolRow`.
3. **`ToolCallCluster`** ("N steps") — groups consecutive same-tool calls at the
   tools level, introducing a grouping that doesn't exist at the intent level.
4. **`ToolCallItem → ToolRow`** — the actual tool-call row with a single
   disclosure (intent + summary visible collapsed, body visible expanded).

The result: icons don't show next to intents, the expansion format is
inconsistent between levels, "steps" handling is inconsistent, and chat view's
action handling is broken. The user described it as "too many similar things
going on here."

## Design

### Core principle

**One component tree** (`ToolCallItem → ToolRow`) renders every tool call at
every verbosity level. Verbosity controls only the **default expansion depth**,
not what renders. The user can always drill all the way down by clicking.

### Three disclosure levels

Each tool-call row has three levels of expansion:

| Level | What's visible | Default at |
|-------|---------------|------------|
| 0 | Intent line + icon (collapsed) | chat, intent |
| 1 | Intent line + summary line | tools, activity |
| 2 | Intent line + summary line + body | full |

The "N actions" group header (chat/intent) is one more level above the rows:
chat keeps it closed, intent opens it.

### Two-level disclosure in ToolRow

`ToolRow` currently has one disclosure toggle: clicking the trigger opens the
body (which includes the summary). This changes to two independent toggles:

1. **Summary disclosure** (`summaryOpen`): controls whether the summary line is
   visible. When closed, only the intent line shows. When open, the summary
   line appears below the intent.
2. **Body disclosure** (`expanded`): controls whether the body is visible. When
   closed, only the intent + summary show. When open, the body appears below
   the summary.

#### Disclosure keys

Both disclosures use the existing `disclosureStore` with distinct keys scoped
to the session:

- Summary: `summary:${item.id}` (new key)
- Body: `${item.id}` (the existing key, unchanged)

To make the summary disclosure participate in the Full-level open baseline
(like the body disclosure does), the projector's `eligibleDisclosureIds` list
is extended to include `summary:${item.id}` alongside each bare `item.id`. This
ensures the `beginDisclosureBaseline` open-boundary logic clears stale manual
closes on summary disclosures when entering Full — the same mechanism that
already clears stale body closes.

The `intent-row:${item.id}` key namespace from the previous PR's
`IntentToolCallRow` is abandoned. Since `IntentToolCallRow` shipped only
recently and reader state is per-session (not persisted), this is acceptable:
readers will simply see the new default expansion on their next render. No
migration is needed.

#### Accessibility contract

The two-level disclosure adds a second interactive trigger to each row. The
a11y contract:

- The **intent line** is a `<button>` with `aria-expanded={summaryOpen}` and
  `aria-controls={summaryBodyId}` (a new `useId` for the summary region). Its
  accessible name is the intent text (same as today).
- The **summary line** is a `<button>` with `aria-expanded={expanded}` and
  `aria-controls={bodyId}` (the existing body id). Its accessible name is the
  summary text.
- When `summaryOpen` is false, the summary `<button>` is not rendered (no
  phantom trigger). The intent button's chevron is the only interactive
  element.
- When `summaryOpen` is true, the intent button's chevron rotates down and the
  summary button appears below it with its own chevron for the body.
- The existing `triggerLabel` logic (for rows without intent) is extended:
  when `hasIntent && summaryOpen`, the summary button gets the summary text as
  its label; when `hasIntent && !summaryOpen`, the intent button is the sole
  trigger.

For rows without an intent (tool calls with no description), the summary line
is always visible — there's no level 0 to collapse to. The body disclosure
works as before.

#### `summaryHiddenWhenExpanded` is retained

The `summaryHiddenWhenExpanded` descriptor flag is **kept**, not removed.
Shell uses it to hide the raw one-line command when the body is open (since the
body renders the command pretty-printed). With the two-level disclosure, this
flag controls whether the summary line is rendered when the body is open:
- `summaryHiddenWhenExpanded: true` (shell): when body is open, the summary
  line is hidden — the body's pretty-printed block is the single representation.
- `summaryHiddenWhenExpanded: false` (every other tool): the summary line
  stays visible when the body is open.

This preserves the existing dedup contract and its tests. The `summaryOpen`
disclosure and `summaryHiddenWhenExpanded` are independent: `summaryOpen`
controls whether the summary is visible when the body is closed; the flag
controls whether it stays visible when the body is open.

### Verbosity → default expansion mapping

The `ContentVector` already carries `toolIntent`, `toolCalls`, `reasoning`,
and `expandByDefault`. The rendering layer reads the config's content vector
to determine the default disclosure state:

| Level | Group header | Summary default | Body default |
|-------|-------------|-----------------|--------------|
| chat | closed | closed | closed |
| intent | open | closed | closed |
| tools | (no header) | open | closed |
| activity | (no header) | open | closed |
| full | (no header) | open | open |

The `expandDetailsByDefault(config)` function (already exists in
`renderContext.tsx`) returns `contentVectorForConfig(config).expandByDefault` —
true for `full`, false for all others including `activity`.

A new function `summaryOpenByDefault(config)` is added to `config.ts` (next to
`presetContent` and `CONTENT_VECTORS`, co-locating verbosity policy). It returns
`contentVectorForConfig(config).toolCalls` — true when the content vector
includes tool calls (`tools`, `activity`, `full`, and any custom vector with
`toolCalls: true`). This derivation works for both `preset` and `custom`
content selections without adding a new `ContentVector` field, because
`summaryOpen` means "the tool-call summary line is visible," which is exactly
what `toolCalls: true` already gates in the projector.

The existing `autoDefault` (failed rows, autoExpand descriptors) still
overrides the body default.

### Removing ToolCallCluster

`ToolCallCluster` and its grouping logic (`toolGrouping.ts`,
`shouldGroup`, `toolRunFor`, `ToolRun`) are removed. Every tool call renders as
its own `ToolCallItem → ToolRow` row at every level. This eliminates the
inconsistent "N steps" grouping that broke the progressive expansion contract.

**Tradeoff**: removing cluster grouping is a density regression at the
`tools`/`activity` levels for long runs of same-tool calls (e.g. 5 consecutive
`read_file` calls now render as 5 separate rows instead of one "5 steps"
cluster). This is an intentional design decision: the progressive expansion
contract requires that every level shows the same set of rows, and clustering
hides rows that are visible at the level below. The density tradeoff is
acceptable because the "N actions" header at chat/intent already provides
grouping, and the drill-down path at tools level lets the reader collapse
rows individually.

### Removing IntentToolCallRow

`IntentToolCallRow` (added in the previous PR) is removed. The `intent`
ProjectedEntry still carries the `item` field (added in the previous PR), but
`ProjectedIntentGroup` now renders `ToolCallItem` rows directly — the same
component used at every other level. The `hideIntent` prop is removed since the
two-level disclosure in `ToolRow` handles the intent/summary/body split
natively.

### Projector changes

The projector continues to produce `intent` entries at chat/intent levels and
`item` entries at tools/activity/full levels. The `intent` entry's `item` field
(added in the previous PR) is retained so `ProjectedIntentGroup` can render
`ToolCallItem` rows.

The `eligibleDisclosureIds` list is extended: for each eligible `commandExecution`
item, both `item.id` (the body key) and `summary:${item.id}` (the summary key)
are pushed. This makes both disclosures participate in the Full-level open
baseline. The `eligibleDisclosure` function is updated to return both ids, or
the projection loop pushes the summary key alongside the body key.

No other projector changes needed — the projector's job is visibility (what
appears), and the rendering layer's job is expansion (how much is open).

**Note on `critical` entries**: The projector routes intent-less tool calls
(`missingIntent`) to `critical` at tool-call levels, not `item`. These render
through `ToolCallItem` with `projectedSummary` set to the projector's neutral
summary. The two-level disclosure applies the same way: `summaryOpen` controls
the summary line, `expanded` controls the body. For `critical` entries, the
intent line is absent (no `item.description`), so the summary is always visible
(level 1 by default). No special handling needed.

### ProjectedIntentGroup changes

`ProjectedIntentGroup` renders `ToolCallItem` rows inside the "N actions"
disclosure. Each `ToolCallItem` receives the render context, session ref, and
thread — the same props it gets at the tools level. The config level
determines the default disclosure depth via `summaryOpenByDefault` and
`expandDetailsByDefault`.

For `intent` entries, `ToolCallItem` derives the intent from `item.description`
(as it does for `item` entries at tools level) and the summary from the
descriptor's `summary()` (as it does for `item` entries). No `projectedSummary`
is passed — the existing `useProjectedSummary` guard in `ToolCallItem` only
applies when `statedIntent === undefined`, and for `intent` entries the item
has a description (the rationale). The descriptor's summary is the correct
tool-call summary text, same as at tools level.

For the `ACTION_SUMMARY_UNAVAILABLE` fallback case (blank-intent items): the
intent line is absent (`statedIntentOf` returns undefined for blank
descriptions), so the summary line is always visible (level 1 by default).
`projectedSummary` is set to `entry.rationale` ("Action summary unavailable")
so the summary shows the neutral text instead of a re-derived one. This is the
existing `critical` path behavior, preserved.

### Anchor/focus behavior

`ProjectedIntentGroup` wraps each `ToolCallItem` row in a div that applies
`projectedEntryAnchor(entry, viewAnchorIndex)` — the same anchor attributes
(`data-view-anchor-id`, `data-view-anchor-source-index`, etc.) that
`IntentToolCallRow` applied in the previous PR. This preserves scroll-anchor
resolution and focus-fallback behavior across verbosity transitions. The
wrapper div uses the same `className` as `ToolCallItem`'s `.call` wrapper so
layout is consistent with the tools-level rendering.

## Files changed

### Modified

- **`config.ts`** — Add `summaryOpenByDefault(config)` function, co-located
  with `presetContent` and `CONTENT_VECTORS`. Returns
  `contentVectorForConfig(config).toolCalls`.

- **`ToolRow.tsx`** — Add `summaryOpen` and `onToggleSummary` props. Split the
  single disclosure into two: summary visibility (intent → summary) and body
  visibility (summary → body). The summary line is conditionally rendered based
  on `summaryOpen`. When `hasIntent && !summaryOpen`, only the intent line
  shows with a right-pointing chevron. When `summaryOpen`, the summary line
  appears with its own chevron for the body. Add `aria-expanded`/`aria-controls`
  wiring for the summary trigger (using a new `useId` for the summary region).

- **`ToolCallItem.tsx`** — Compute `summaryOpen` from the config level (via
  `summaryOpenByDefault`) and the disclosure store (key `summary:${item.id}`).
  Pass `summaryOpen` and `onToggleSummary` to `ToolRow`. Remove `hideIntent`
  (no longer needed — the two-level disclosure replaces it). Keep
  `summaryHiddenWhenExpanded` handling: when `expanded &&
  descriptor.summaryHiddenWhenExpanded`, the summary line is hidden even if
  `summaryOpen` is true (the body's pretty-printed block is the single copy).

- **`TurnBlock.tsx`** — Remove `IntentToolCallRow`. `ProjectedIntentGroup`
  renders `ToolCallItem` rows directly, wrapped in a div with
  `projectedEntryAnchor` for anchor attributes. Thread `sessionRef`,
  `renderContext`, `thread`, and `viewAnchorIndex`. Remove the `ToolCallCluster`
  import and the clustering branch in the renderedEntries loop. Remove the
  orphaned `itemScopeKey` import.

- **`TranscriptBody.tsx`** — No changes needed (already threads props to
  `ProjectedIntentGroup`).

- **`renderContext.tsx`** — Export `summaryOpenByDefault` from `config.ts`
  (re-exported for convenience, same as `expandDetailsByDefault`).

- **`projector.ts`** — Extend `eligibleDisclosureIds` to include
  `summary:${item.id}` for each eligible `commandExecution` item, alongside the
  existing bare `item.id`.

- **`session.module.css`** — Remove `.intentRow` and `.intentRowBody` (from the
  previous PR). The intent rows now use the same `.call` / `.row` CSS as every
  other tool-call row, inside `.intentGroupItems`. Verify layout parity: the
  `.intentGroupItems` flex column already stacks rows with `gap: var(--space-1)`,
  matching `.call`'s own padding.

### Deleted

- **`ToolCallCluster.tsx`** — Removed entirely.
- **`toolGrouping.ts`** — Removed entirely (`ToolRun`, `toolRunFor`,
  `shouldGroup`).
- **`toolGrouping.test.ts`** — Removed (tests the deleted module).
- **`ToolCallCluster.test.tsx`** — Removed (tests the deleted component).

### Tests

- **`TurnBlock.test.tsx`** — Rewrite tests that assumed `IntentToolCallRow` or
  `ToolCallCluster` grouping. Add tests for the 3-level drill-down (intent →
  summary → body). Update tests that assert `data-testid="intent-tool-call-row"`
  or `data-open` to use `ToolCallItem`'s testids instead.
- **`toolRowGrammar.test.tsx`** — Update for the two-level disclosure. Keep
  `summaryHiddenWhenExpanded` tests (shell dedup is preserved).
- **`ToolCallItem.test.tsx`** — Update for `summaryOpen` default. Keep
  `summaryHiddenWhenExpanded` tests.
- **`TranscriptBody.test.tsx`** — Remove `ToolCallCluster` import (line 16)
  and cluster fixtures (`cluster_1/2/3`). Update tests that depend on cluster
  rendering.
- **`projector.test.ts`** — Update `eligibleDisclosureIds` assertions to
  include `summary:${item.id}` entries.
- **`ToolCallCluster.test.tsx`** — Deleted (component removed).
- **`toolGrouping.test.ts`** — Deleted (module removed).

## What stays the same

- The projector's visibility logic (chat/intent → `intent` entries,
  tools/activity/full → `item` entries).
- The `disclosureStore` mechanism (scoped keys, baselines, explicit choices).
- The `ProjectedIntentGroup` "N actions" header.
- The `ToolRow` two-line grammar (intent + summary) — just with independent
  disclosure for each line.
- The `autoExpand` / failure force-open behavior.
- The `summaryHiddenWhenExpanded` dedup contract (shell).
- The cross-turn intent grouping in `TranscriptBody`.
- All anchor/focus/scroll behavior (the `data-view-anchor-id` attributes,
  threaded through `ProjectedIntentGroup`'s wrapper divs).

## Simplicity gains

- **One component** renders tool calls at every level (no `IntentToolCallRow`).
- **One grouping mechanism** (ProjectedIntentGroup) instead of two
  (ProjectedIntentGroup + ToolCallCluster).
- **One disclosure pattern** (two-level: summary + body) instead of the
  ad-hoc IntentToolCallRow wrapping.
- **Fewer files** — removes `ToolCallCluster.tsx`, `toolGrouping.ts`, and
  their tests.

## Tradeoffs

- **Density regression at tools level**: removing `ToolCallCluster` means long
  same-tool runs render as individual rows instead of one "N steps" cluster.
  This is intentional — progressive expansion requires the same rows at every
  level.
- **Disclosure state migration**: the `intent-row:${item.id}` key namespace from
  the previous PR is abandoned. Reader state is per-session and not persisted,
  so no migration is needed. Readers will see the new default expansion on
  their next render.
