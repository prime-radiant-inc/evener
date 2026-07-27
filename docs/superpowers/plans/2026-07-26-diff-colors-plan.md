# Dedicated Diff Colors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give DiffBlock quiet, non-semantic add/remove backgrounds in both themes and lock the decision into the token contract and design-system documentation.

**Architecture:** Add only `--diff-add-bg` and `--diff-del-bg` to each theme. Keep DiffBlock's existing neutral `--ink-hi` content and `--ink-low` marker foregrounds, with `+`/`−` remaining the independent meaning channel; no semantic status token is exposed or reused.

**Tech Stack:** CSS custom properties, React/CSS Modules, Vitest, the existing frontend contrast math, Biome, TypeScript, Vite, and the layout/overflow guards.

## Global Constraints

- Dedicated diff colors are syntax/domain notation, not status.
- No `--alive`, `--danger`, broader fifth status family, `rail/**`, or `Steering*` changes.
- Tests must assert structured token/behavior contracts and remain deterministic.
- Do not close `9jew`; leave a ready-for-review evidence comment when Linear is available.

### Task 1: Lock the failing contracts

**Files:**
- Modify: `cmd/serf-hub/frontend/src/widgets/diffblock/diffblock.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/styles/token-contract.test.ts`

- [ ] Add a DiffBlock behavior assertion that preserves `+` and `−` as explicit marker text.
- [ ] Add token-contract assertions for the dedicated pair, theme parity, semantic-token absence from DiffBlock, grayscale luminance separation, and contrast of real content/marker inks against both diff backgrounds and `--surface-0`.
- [ ] Run the focused tests and confirm they fail for the missing tokens/semantic implementation.

### Task 2: Implement the token decision

**Files:**
- Modify: `cmd/serf-hub/frontend/src/styles/tokens.css`
- Modify: `cmd/serf-hub/frontend/src/widgets/diffblock/diffblock.module.css`
- Modify: `cmd/serf-hub/frontend/src/widgets/diffblock/index.tsx`

- [ ] Add `#131A1B`/`#1D1519` to the dark theme and `#E9F3EC`/`#F5E7EB` to the light theme as the two domain tokens.
- [ ] Replace DiffBlock's semantic background references and stale comments with the dedicated pair; keep the existing glyph channel and neutral foregrounds.
- [ ] Run the focused tests and confirm they pass.

### Task 3: Make the ruling authoritative

**Files:**
- Modify: `docs/web-ui/design-system.md` section 2, section 3 DiffBlock note, and section 4 ruling.

- [ ] Record Jesse's 2026-07-26 ruling, explicitly superseding mockup 19 as an implementation authority and defining diff colors as syntax/domain notation rather than status.
- [ ] Update token counts and DiffBlock wording so the document has no remaining semantic-color contradiction.

### Task 4: Verify and hand off

- [ ] Run focused DiffBlock/token-contract tests, the full frontend test suite, typecheck, lint, build, layoutguard, and overflowguard.
- [ ] Re-read the requirements, inspect the diff/status, and commit implementation/documentation changes in reviewable commits.
- [ ] Append the commit and exact contrast/test evidence to `9jew` without closing it, then leave the worktree clean and report `HEAD`.
