# Verbosity = Expansion: Unified Tool-Call Disclosure

## Problem

The webui has four verbosity levels (chat, intent, tools, full/activity) that
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
| 1 | Intent line + summary line | tools |
| 2 | Intent line + summary line + body | full/activity |

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

Both use the existing `disclosureStore` with distinct keys:
- Summary: `summary:${item.id}` (scoped to the session)
- Body: `${item.id}` (the existing key, unchanged)

`ToolRow` receives two new props: `summaryOpen` and `onToggleSummary`. The
existing `expanded` and `onToggle` continue to control the body. When
`summaryOpen` is false, the summary line is hidden and the intent line's
chevron points right. When true, the summary line appears with its own chevron
for the body.

For rows without an intent (tool calls with no description), the summary line
is always visible — there's no level 0 to collapse to. The body disclosure
works as before.

### Verbosity → default expansion mapping

The `ContentVector` already carries `toolIntent`, `toolCalls`, `reasoning`,
and `expandByDefault`. The rendering layer reads the config's content level to
determine the default disclosure state:

| Level | Group header | Summary default | Body default |
|-------|-------------|-----------------|--------------|
| chat | closed | closed | closed |
| intent | open | closed | closed |
| tools | (no header) | open | closed |
| full | (no header) | open | open |

The `expandDetailsByDefault(config)` function (already exists) returns true
for full. A new function `summaryOpenByDefault(config)` returns true for
tools and above. The existing `autoDefault` (failed rows, autoExpand
descriptors) still overrides the body default.

### Removing ToolCallCluster

`ToolCallCluster` and its grouping logic (`toolGrouping.ts`,
`shouldGroup`, `toolRunFor`, `ToolRun`) are removed. Every tool call renders as
its own `ToolCallItem → ToolRow` row at every level. This eliminates the
inconsistent "N steps" grouping that broke the progressive expansion contract.

### Removing IntentToolCallRow

`IntentToolCallRow` (added in the previous PR) is removed. The `intent`
ProjectedEntry still carries the `item` field (added in the previous PR), but
`ProjectedIntentGroup` now renders `ToolCallItem` rows directly — the same
component used at every other level. The `hideIntent` prop is removed since the
two-level disclosure in `ToolRow` handles the intent/summary/body split
natively.

### Projector changes

The projector continues to produce `intent` entries at chat/intent levels and
`item` entries at tools/full levels. The `intent` entry's `item` field (added
in the previous PR) is retained so `ProjectedIntentGroup` can render
`ToolCallItem` rows.

No other projector changes needed — the projector's job is visibility (what
appears), and the rendering layer's job is expansion (how much is open).

### ProjectedIntentGroup changes

`ProjectedIntentGroup` renders `ToolCallItem` rows inside the "N actions"
disclosure. Each `ToolCallItem` receives the render context, session ref, and
thread — the same props it gets at the tools level. The config level
determines the default disclosure depth via `summaryOpenByDefault` and
`expandDetailsByDefault`.

For `intent` entries, the `ToolCallItem` receives `projectedSummary` set to the
intent's `rationale` field (so the summary text matches the projected intent,
not a re-derived one). The `item.description` (the original intent) serves as
the intent line.

## Files changed

### Modified

- **`ToolRow.tsx`** — Add `summaryOpen` and `onToggleSummary` props. Split the
  single disclosure into two: summary visibility (intent → summary) and body
  visibility (summary → body). The summary line is conditionally rendered based
  on `summaryOpen`. When `hasIntent && !summaryOpen`, only the intent line
  shows with a right-pointing chevron. When `summaryOpen`, the summary line
  appears with its own chevron for the body.

- **`ToolCallItem.tsx`** — Compute `summaryOpen` from the config level (via
  `summaryOpenByDefault`) and the disclosure store. Pass `summaryOpen` and
  `onToggleSummary` to `ToolRow`. Remove `hideIntent` (no longer needed — the
  two-level disclosure replaces it). The `summaryHiddenWhenExpanded` descriptor
  flag is removed: with the two-level disclosure, the summary line is an
  independent disclosure level that stays visible when the body opens. For
  shell (the one descriptor that used this flag), the summary line (the raw
  one-line command) stays visible above the body (the pretty-printed block) —
  redundant but not wrong, and consistent with every other tool.

- **`TurnBlock.tsx`** — Remove `IntentToolCallRow`. `ProjectedIntentGroup`
  renders `ToolCallItem` rows directly. Thread `sessionRef`, `renderContext`,
  `thread`, and `viewAnchorIndex` (already done in the previous PR). Remove the
  `ToolCallCluster` import and the clustering branch in the renderedEntries
  loop.

- **`TranscriptBody.tsx`** — No changes needed (already threads props to
  `ProjectedIntentGroup`).

- **`renderContext.tsx`** — Add `summaryOpenByDefault(config)` function.

- **`config.ts`** — No changes needed (the content vectors are already correct;
  `toolIntent`/`toolCalls` control visibility, the rendering layer controls
  expansion).

- **`session.module.css`** — Remove `.intentRow` and `.intentRowBody` (from the
  previous PR). The intent rows now use the same `.call` / `.row` CSS as every
  other tool-call row.

### Deleted

- **`ToolCallCluster.tsx`** — Removed entirely.
- **`toolGrouping.ts`** — Removed entirely (`ToolRun`, `toolRunFor`,
  `shouldGroup`).

### Tests

- **`TurnBlock.test.tsx`** — Update tests that assumed `ToolCallCluster` grouping
  or `IntentToolCallRow`. Add tests for the 3-level drill-down.
- **`ToolCallCluster.test.tsx`** — Deleted (component removed).
- **`toolRowGrammar.test.tsx`** — Update for the two-level disclosure.
- **`ToolCallItem.test.tsx`** — Update for `summaryOpen` default.
- **`projector.test.ts`** — No changes (projector unchanged).

## What stays the same

- The projector's visibility logic (chat/intent → `intent` entries,
  tools/full → `item` entries).
- The `disclosureStore` mechanism (scoped keys, baselines, explicit choices).
- The `ProjectedIntentGroup` "N actions" header.
- The `ToolRow` two-line grammar (intent + summary) — just with independent
  disclosure for each line.
- The `autoExpand` / failure force-open behavior.
- The cross-turn intent grouping in `TranscriptBody`.
- All anchor/focus/scroll behavior (the `data-view-anchor-id` attributes).

## Simplicity gains

- **One component** renders tool calls at every level (no `IntentToolCallRow`).
- **One grouping mechanism** (ProjectedIntentGroup) instead of two
  (ProjectedIntentGroup + ToolCallCluster).
- **One disclosure pattern** (two-level: summary + body) instead of the
  ad-hoc IntentToolCallRow wrapping.
- **Fewer files** — removes `ToolCallCluster.tsx` and `toolGrouping.ts`.
