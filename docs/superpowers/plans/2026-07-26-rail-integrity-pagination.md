# Rail Integrity and Pagination Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make project deletion honest while pending, make capped rail sessions fetchable, expose subagent overage, and reject favorite decisions for non-pinnable session ids.

**Architecture:** Preserve the existing memoized navigation tree as the source of ordering and tier classification. Add an explicit per-tier page projection over the uncapped tier slices retained by hubcore, then merge pages into the frontend tree by tier. Keep deletion and page loading state in the existing rail pending/store paths, and validate favorite session ids against real top-level tree nodes before touching the decision store.

**Tech Stack:** Go `hubcore` and `cmd/serf-hub` HTTP handlers/tests; `hubapi` JSON wire structs; React/TypeScript rail, Zustand tree store, Vitest and Testing Library.

## Global Constraints

- Default tests remain deterministic and use real Serf code below scripted/external boundaries.
- Preserve per-tier recency ordering and the existing 50-row sidebar cap; pagination only reveals rows already classified and ordered by hubcore.
- Do not hand-edit generated appwire code; run generation/drift checks after the wire changes.
- Do not merge controller changes or close katas; comment on each owned kata with its commit and focused test evidence when ready.
- Do not sweep existing junk favorite rows in this batch; create a related kata if the store contract does not make that cleanup safely scoped.

---

### Task 1: Surface subagent overage in the tree wire and inactive fold

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Test: `cmd/serf-hub/internal/hubcore/tree_test.go`
- Modify: `hubapi/types.go`
- Test: `hubapi/types_test.go`
- Modify: `cmd/serf-hub/web_api_tree.go`
- Test: `cmd/serf-hub/web_api_tree_test.go`
- Modify: `cmd/serf-hub/frontend/src/stores/tree.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railNodes.ts`
- Test: `cmd/serf-hub/frontend/src/shell/rail/railNodes.test.ts`

**Interfaces:**
- Add `MoreSubagents int` to hubcore and hubapi `TreeNode`, serialized as `more_subagents` when nonzero.
- Set it to the number removed by the existing 50-child cap, while retaining the kept child order.
- Copy it through `apiTreeNode` recursively and add it to the frontend wire mirror.
- Make the inactive fold count visible inactive children plus the server-reported omitted-child count, and put an explicit `OverflowRailNode` after retained inactive rows so expansion explains the omitted portion.

- [ ] **Step 1: Write the failing Go tree and projection tests.** Build a parent with 60 subagents, assert 50 children and 10 overage in hubcore, then assert `more_subagents:10` survives `/api/tree` JSON.
- [ ] **Step 2: Run the focused Go tests and confirm the new assertions fail because the field is absent/zero.**
- [ ] **Step 3: Write the failing rail-node test.** Feed a parent with one retained inactive child and `more_subagents:10`; assert the fold count is 11 and its expanded children are the retained session followed by an `OverflowRailNode` with count 10.
- [ ] **Step 4: Run the focused Vitest test and confirm the expected count fails.**
- [ ] **Step 5: Implement the smallest hubcore, wire, and rail changes.**
- [ ] **Step 6: Run focused Go and Vitest tests; run `go generate ./appwire/...` and the relevant drift check.**
- [ ] **Step 7: Commit with a detailed `t4fa`-named message.**

### Task 2: Add server-side tier paging and make `+N older` fetch rows

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Test: `cmd/serf-hub/internal/hubcore/tree_test.go`
- Modify: `hubapi/types.go`
- Modify: `cmd/serf-hub/web_api.go` or the existing tree route registration in `cmd/serf-hub/web.go` only if needed
- Modify: `cmd/serf-hub/web_api_tree.go`
- Test: `cmd/serf-hub/web_api_tree_test.go`
- Modify: `cmd/serf-hub/frontend/src/stores/tree.ts`
- Test: `cmd/serf-hub/frontend/src/stores/tree.test.ts` or the existing tree store test file
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railNodes.ts`
- Test: `cmd/serf-hub/frontend/src/shell/rail/railNodes.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx`
- Test: `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx`

**Interfaces:**
- Retain the uncapped Current/Recent/Archived slices behind a hubcore page method that returns a deterministic offset/limit page and remaining count.
- Extend `GET /api/tree/project` with validated `tier`, `offset`, and `limit` query parameters for page requests; keep the existing no-page query behavior unchanged.
- Return a structured project-page response containing the project key, requested tier, offset, sessions, and remaining count.
- Add a tree-store page loader that merges returned sessions into the matching project at the correct tier boundary.
- Give `OverflowRailNode` the project key, tier, and offset needed to request its next page; activating it fetches the page and leaves a smaller overflow node until no rows remain.

- [ ] **Step 1: Write failing hubcore and HTTP tests for ordered pages, invalid paging inputs, and remaining counts.**
- [ ] **Step 2: Run those tests and confirm they fail because capped rows are unavailable and the route has no page contract.**
- [ ] **Step 3: Write failing frontend tests for merging a page between Current/Recent boundaries and activating `+N older` to reveal returned rows.**
- [ ] **Step 4: Run the focused Vitest tests and confirm the fetch/merge behavior fails.**
- [ ] **Step 5: Implement the bounded page projection, route validation, store merge, and clickable overflow row.**
- [ ] **Step 6: Run focused Go and frontend tests, including a multi-page fixture proving ordering and deterministic remaining counts.**
- [ ] **Step 7: Commit with a detailed `1fgd`-named message.**

### Task 3: Reconcile optimistic project deletion with partial success

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railPending.ts`
- Test: `cmd/serf-hub/frontend/src/shell/rail/railPending.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx`
- Test: `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx`

**Interfaces:**
- Add one deletion pending operation that hides the project and carries the server response's deleted/skipped identity sets for reconciliation.
- Start the operation before `POST /api/project/delete`; keep it projected through the awaited refresh; drop it after refresh so skipped projects/sessions are admitted from the server tree.
- Preserve the existing warning toast for nonempty `skipped[]` and the existing error path for failed deletion.

- [ ] **Step 1: Write a pending-layer test proving the project disappears while deletion is in flight and a refreshed tree with skipped sessions is not mutated by the overlay after reconciliation.**
- [ ] **Step 2: Run the focused Vitest test and confirm it fails because delete has no pending operation.**
- [ ] **Step 3: Extend the rail integration test with a held POST and a partial-success response; assert optimistic disappearance before release and honest skipped-row reappearance after refresh.**
- [ ] **Step 4: Run the focused integration test and confirm the pre-fix behavior fails.**
- [ ] **Step 5: Implement the deletion pending operation and response-aware refresh path without changing the Go handler contract.**
- [ ] **Step 6: Run the focused pending and Rail tests, including the 409/error path.**
- [ ] **Step 7: Commit with a detailed `hhpe`-named message.**

### Task 4: Validate favorite session ids before writing decisions

**Files:**
- Modify: `cmd/serf-hub/web_api_favorite.go`
- Test: `cmd/serf-hub/web_api_favorite_test.go`
- Test: `cmd/serf-hub/web_api_tree_test.go` only if a shared tree fixture contract needs coverage

**Interfaces:**
- For `kind:"session"`, normalize only for comparison to the tree's wire ref and accept ids matching a real top-level `kind:"session"` node across the memoized project tiers.
- Reject synthetic cluster refs, nested/subagent refs, and unknown refs before `FavoriteStore.Set` is called.
- Leave project-kind favorite behavior unchanged.

- [ ] **Step 1: Write endpoint/store tests for a real top-level success and cluster, nested, and unknown rejection with an unchanged decision store.**
- [ ] **Step 2: Run the focused tests and confirm the invalid cases currently write decisions.**
- [ ] **Step 3: Inspect the existing decision rows and document that sweeping legacy junk is not safely part of this request; create one related kata after searching for duplicates.**
- [ ] **Step 4: Implement the pre-write top-level validation.**
- [ ] **Step 5: Run focused endpoint/store tests, including project-kind coverage.**
- [ ] **Step 6: Commit with a detailed `ndr0`-named message.**

### Task 5: Cumulative verification and handoff

- [ ] **Step 1: Review the cumulative diff and run `git diff --check`.**
- [ ] **Step 2: Run focused red/green Go and frontend tests, generation/drift checks, frontend typecheck, lint, production build, overflowguard, `make build-runtime`, and the requested Go/full gates as feasible in this worktree.**
- [ ] **Step 3: Self-review for ordering/cap preservation, archive/session routing regressions, generated-file drift, and unrelated changes.**
- [ ] **Step 4: Commit any verification-only fixes separately with detailed intent.**
- [ ] **Step 5: Add substantive comments to `hhpe`, `1fgd`, `t4fa`, and `ndr0` naming the ready commit(s) and test evidence; do not merge or close them.**
