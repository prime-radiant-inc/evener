# Task 7 Report: Settled tool calls compose to one line

## Implementation
Implemented the collapsed-row one-line composition for ToolRow in `cmd/serf-hub/frontend/src/panes/session/transcript/ToolRow.tsx`:
- added the load-bearing grammar comment update describing collapsed one-line composition
- added `separator` to the CLASS map
- computed `oneLine = hasPurpose && hasSummary && !expanded`
- rendered `data-oneline="true"` on both the `div` and `summary` return paths when collapsed with both purpose and summary
- inserted the em-dash separator between purpose and summary for collapsed rows
- added `title` fallbacks on the purpose and summary spans for the full text

Updated `cmd/serf-hub/frontend/src/panes/session/transcript/toolcallitem.module.css`:
- replaced the old stacked-row-only explanatory comments with the one-line composition comment
- added `.row[data-oneline="true"]` nowrap behavior
- added ellipsis-clamping for `.purpose` and `.summary` under one-line rows
- added `.separator` styled with `--ink-low`

Added grammar coverage in `cmd/serf-hub/frontend/src/panes/session/transcript/toolRowGrammar.test.tsx`:
- collapsed purpose+summary row composes one line
- expanded row keeps stacked grammar
- purpose-less rows remain unchanged

## TDD evidence
### RED
Before implementation, the new grammar test failed as expected:
- `expect(row.dataset.oneline).toBe("true")` received `undefined`

### GREEN
After implementation:
- `npx vitest run src/panes/session/transcript/toolRowGrammar.test.tsx`
  - 32 tests passed
- `npx vitest run src/panes/session/transcript/toolRowGrammar.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx`
  - 91 tests passed
- `npm run overflowguard`
  - PASS at 390px, 700px, 1024px, and 1400px

## Files changed
- `cmd/serf-hub/frontend/src/panes/session/transcript/ToolRow.tsx`
- `cmd/serf-hub/frontend/src/panes/session/transcript/toolcallitem.module.css`
- `cmd/serf-hub/frontend/src/panes/session/transcript/toolRowGrammar.test.tsx`

## Commit
- Commit message: `transcript: settled tool calls compose to one line (task 7)`
- Commit SHA: `080d418d6`

## Self-review
- The requested collapsed-row behavior is implemented exactly for purpose+summary rows.
- Expanded rows remain stacked; purpose-less rows are unaffected.
- No new colors, type sizes, or motion were introduced.
- Required suites and overflow guard passed.
- Git status should be clean after commit.

## Concerns
- None observed during verification.
