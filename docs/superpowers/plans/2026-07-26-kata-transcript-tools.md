# Transcript Cluster, Disclosure Isolation, and Raw-State Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Each implementation step is tracked with a checkbox and must be verified before it is marked complete.

**Goal:** Complete exactly Katas d6fp, sbt9, and qvvb in this worktree with deterministic behavior tests, minimal frontend changes, and no changes under `src/shell/rail/**`.

**Architecture:** Keep `TurnBlock` responsible for adjacency-based tool-run classification and rendering a cluster wrapper above the existing one-call `ToolCallItem` renderer. Reuse the established session-scoped key convention from `turnScopeKey` for all disclosure state. Treat `ItemModel.raw` as an optional direct producer state value: add structured rendering only for the stable, high-value `job_list` state and retain output-based bodies where raw state duplicates output or has no shared useful shape.

**Tech Stack:** React 19, TypeScript, Zustand disclosure store, Vitest/Testing Library, Vite, Biome, existing Go tool producers and transcript reducer.

## Global Constraints

- Do not close any Kata or merge branches.
- Keep all changes within d6fp, sbt9, and qvvb; open a linked Kata for any unrelated real defect rather than silently expanding scope.
- Do not touch `src/shell/rail/**`.
- Preserve the one-rendered-row-per-call `ToolCallItem` contract.
- Keep default tests deterministic and provider-independent.
- Do not add viewport or scroll awareness to transcript grouping. Existing dynamic `VirtualList` measurement must observe cluster expand/collapse height changes.
- Commit the plan before implementation and commit coherent implementation milestones with detailed messages.

## 1. d6fp — classify and render settled adjacent tool runs

- [ ] Add a pure grouping helper beside the transcript tool renderers, copying the structure of `messages/systemGrouping.ts`. It will derive contiguous runs from the current `turn.items` on every call, use a threshold of three, treat `ask_user` as its own conversational boundary, and exclude failures from runs. Failure detection must include the existing generic error/status rules and descriptor-specific failure rules such as non-zero shell exits.
- [ ] Add behavior tests for fresh adjacency classification, the one/two-item threshold, non-adjacent calls, `ask_user` boundaries, generic failures, descriptor-specific failures, running calls, and runs that are the final activity in a turn. These tests must prove only settled runs of at least three calls that are not final activity are groupable.
- [ ] Add a cluster renderer above `ToolCallItem`. It must render a single expandable summary for a qualifying run while mapping every member through the existing `ToolCallItem`, preserving one call row per member when expanded. The summary must report the exact run count and use the highest-consequence member according to the landed `consequenceRank` implementation.
- [ ] Add renderer behavior tests proving a qualifying run collapses behind one summary, preserves all member rows when opened, names the highest-consequence member, and leaves the final, running, failed, short, and `ask_user` cases ungrouped.
- [ ] Inspect the existing dynamic `VirtualList` path and add only the minimal documentation or test needed to establish that native cluster disclosure changes are measured by the existing dynamic row measurement. Do not introduce scroll or viewport state.

## 2. sbt9 — isolate disclosure state by session and item

- [ ] Add a sibling `itemScopeKey(sessionRef, itemId)` helper next to `turnScopeKey` in `subagentModuleStore.ts`, using the established NUL-separated session/item convention.
- [ ] Thread `sessionRef` through the smallest existing props path and replace bare item disclosure keys in `ToolCallItem`, `SystemNoticeItem`, `ThinkBlock`, and `SteeringItem`. Keep the SteeringItem change limited to key/plumbing/test behavior; do not alter its styling or other behavior.
- [ ] Update memoization comparison only as needed so a renderer cannot retain disclosure UI from one session when the same item object/id is rendered under another session.
- [ ] Add real component behavior tests for each of the four renderers: render the same item id in two sessions, expand one session, and prove the other remains closed. Add a helper-level key test if useful, but do not substitute mock-only assertions for renderer behavior.

## 3. qvvb — correct raw-state documentation and use stable job_list state

- [ ] Correct the false comments in `tools/helpers.ts`, `tools/bodies.tsx`, and `tools/jobTools.tsx` in present-tense domain terms. State that the reducer preserves `item.raw` directly from producer `StateResult.State`, with no wrapper key, and distinguish structured state from the text output each body intentionally renders.
- [ ] Add a reducer/renderer regression test that establishes the direct raw shape boundary where it is not already covered.
- [ ] Verify the Go producers for the job tool states and add a safe structured `job_list` body using the stable direct raw fields (`jobs`, identifiers, type/status/phase/description, and totals). Preserve a text-output fallback for missing or malformed raw state.
- [ ] Add behavior tests proving `job_list` uses direct structured raw state when present and falls back to its formatted output when absent. Keep `job_status`, `job_stop`, and `delegate_send` output-driven where raw state duplicates or does not improve their existing actionable summaries, and document/test that rationale.
- [ ] Do not modify `shellTool.tsx`, `useSkillTool.tsx`, or `webTools.tsx`; their existing raw-state comments are accurate.

## 4. Verification and handoff

- [ ] Run focused tests after each red/green implementation slice.
- [ ] Run the full relevant frontend suite, typecheck, lint, production build, layoutguard, overflowguard, and `git diff --check`.
- [ ] Inspect the complete `base..HEAD` diff against every requirement, confirm no forbidden or unrelated files changed, and confirm the worktree is clean.
- [ ] Add substantive ready-for-controller-review comments to d6fp, sbt9, and qvvb only after all verification passes. Do not close them.
