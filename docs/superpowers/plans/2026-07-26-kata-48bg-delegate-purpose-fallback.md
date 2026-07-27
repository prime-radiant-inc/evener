# Delegate Task-Only Purpose Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give production-shaped task-only delegate calls a bounded human top-level purpose while preserving the existing one-disclosure, Mandate, and multi-delegate behavior.

**Architecture:** Keep delegate machine summaries suppressed in `ToolCallItem`. Derive a purpose only at that owner from `item.description` via `statedPurposeOf`, falling back to a normalized, bounded `task` argument preview; leave the full task in `subagentModule`'s Mandate. No Go projection, reasoning item, sibling item, rail, notification, or workspace-routing changes.

**Tech Stack:** React 19, TypeScript, Vitest Testing Library, CSS Modules, Vite/Chrome layout and overflow guards.

## Global Constraints

- Description-bearing delegates keep the existing description as the purpose, including `Testing delegation` winning over the task.
- Malformed arguments, non-string tasks, and blank tasks produce no fabricated purpose.
- The preview is one line of normalized plain text and is bounded with an honest ellipsis; the Mandate remains the full task text.
- The top-level `ToolCallItem` owns the only disclosure; the status dot stays in its left row grammar and the existing quiet `open` plus icon action remains.
- Preserve genuine multi-delegate leader election, aggregation, task rows, and job status behavior.
- Tests remain deterministic and use the real frontend components; do not add provider/network requirements or large snapshots.

---

### Task 1: Add failing task-only and semantic regression contracts

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/toolRowGrammar.test.tsx` only for the smallest missing row/CSS contract

**Interfaces:**
- Consumes: the existing `ItemModel` fixture shape with `argumentsJSON`, `toolName: "delegate"`, and optional `description`.
- Produces: executable contracts for description-present, task-only, malformed/blank, one-disclosure, Mandate, multi-row, status, and prose styling behavior.

- [ ] **Step 1: Write the failing component test.** Add a production-shaped task-only `ToolCallItem` case with no `description`, a task containing markdown punctuation, newlines, and enough text to exceed the preview bound. Assert the top-level `tool-row-purpose` is a single normalized bounded preview, the full original task is present in `subagent-mandate`, the top-level row has exactly one status image and one `details > summary`, and the module has no nested disclosure or large primary transcript button.
- [ ] **Step 2: Write malformed/blank and precedence cases.** Assert malformed JSON, a blank task, and a non-string task keep status visible but render neither purpose nor a generic label. Keep or strengthen the existing description-bearing assertion so `Testing delegation` remains the purpose and the technical delegate summary remains suppressed.
- [ ] **Step 3: Run the focused tests and verify RED.** Run `npm test -- --run src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/tools/subagentModule.test.tsx src/panes/session/transcript/toolRowGrammar.test.tsx` from `cmd/serf-hub/frontend`; expected failure is the missing top-level purpose for task-only delegates, not a fixture or transform error.
- [ ] **Step 4: Preserve the existing multi-row contracts.** Extend the existing aggregate assertion only if needed to name the behavior under protection: one elected module, all genuine child rows, no per-row Mandate duplication, and no nested `details > summary`.
- [ ] **Step 5: Commit the failing-test slice.** Stage only the three frontend test files and commit with a message describing the task-only wire-shape regression and the contracts it exposes.

### Task 2: Implement the smallest delegate-purpose fallback

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentmodule.module.css` only if the Mandate needs an explicit prose/line-break contract
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx` only for that CSS contract

**Interfaces:**
- Consumes: `statedPurposeOf`, `parseArgs`, `str`, and the existing `clip` helper; `rowFromDelegateItem` remains the source of the full Mandate task.
- Produces: a domain-named delegate purpose derivation used only by `ToolCallItem`.

- [ ] **Step 1: Add the minimal implementation.** When `item.toolName === "delegate"`, use `statedPurposeOf(item)` first. If absent, parse `item.argumentsJSON`, read a string `task`, trim/collapse whitespace into one line, and return the existing bounded clip form; return `undefined` for malformed JSON, non-string, or blank task. Pass that derived purpose to both existing `ToolRow` call sites while retaining `summary={isDelegate ? "" : summary}`.
- [ ] **Step 2: Keep the body and aggregate path unchanged.** Do not alter `rowFromDelegateItem`, `DelegateBody`, leader election, status derivation, activity feeds, or the open-transcript action. If newline preservation in the full Mandate is required by the new semantic test, make only the explicit `.mandate` prose/line-break CSS adjustment and add a narrow declaration contract with comments stripped.
- [ ] **Step 3: Run the focused tests and verify GREEN.** Re-run the same ToolCallItem/subagent/ToolRow command until the new cases and the existing suite pass; fix production code rather than weakening expectations.
- [ ] **Step 4: Commit the implementation slice.** Stage only the fallback implementation and any directly required Mandate CSS/test contract, with a concise message explaining why the fallback belongs at the ToolCallItem presentation boundary.

### Task 3: Verify browser-shaped layout and repository quality

**Files:**
- Modify: `cmd/serf-hub/frontend/src/dev/overflowharness-entry.tsx` only if inspection proves the fixture is no longer production-shaped

- [ ] **Step 1: Inspect the overflow fixture after the fix.** Confirm it still has a real `commandExecution` delegate with `argumentsJson: JSON.stringify({ task: TASK, ... })` and no `description`; do not add a synthetic stand-in or unrelated fixture data.
- [ ] **Step 2: Run browser guards.** Run `npm run layoutguard` and `npm run overflowguard`; explicitly cover 390px and 1400px, and ensure the task-only preview remains bounded without horizontal scroll.
- [ ] **Step 3: Run the full frontend suite twice sequentially.** Run `npm test`, then run `npm test` again as a separate command. Also run `npm run typecheck`, `npm run lint`, and `npm run build`.
- [ ] **Step 4: Capture real screenshots.** Start the existing frontend harness and capture the real Session at 390px and 1400px. Check that the summary visibly combines chevron, status dot, and human purpose; the expanded body is a sans/prose Mandate; there is no nested row disclosure; and the quiet `open` plus icon action remains.
- [ ] **Step 5: Open katas for unrelated findings.** Record any unrelated rail, notification, routing, ThinkBlock, User, Agent, or other weirdness in separate katas; do not fix it in this branch.
- [ ] **Step 6: Run final verification and leave the tree clean.** Check `git diff`, `git status --short`, and the final `HEAD`; do not merge or close `48bg`.

