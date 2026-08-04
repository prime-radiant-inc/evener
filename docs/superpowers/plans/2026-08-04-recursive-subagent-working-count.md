# Recursive Subagent Working Count Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show `1 subagent working` or `N subagents working` for active descendants in the web rail, counting recursively.

**Architecture:** Keep the change in the frontend rail. Add a pure recursive helper beside the existing rail descendant helpers, then compose its result into `activityGloss` without changing the API or wire types. Use the existing `active` state as the definition of working and preserve branch/project suffixes.

**Tech Stack:** React, TypeScript, Vitest, Testing Library, Biome, Make.

## Global Constraints

- Count all recursive descendants, never the session row itself.
- Count only descendants whose wire state is exactly `active`.
- Preserve existing activity text when the count is zero.
- Use singular `1 subagent working` and plural `N subagents working`.
- Follow `docs/testing.md` and run `make test-web`; run `make test-web-browser` when Chrome is available.
- Run `npx biome check --write` on touched frontend files before frontend gates.

---

### Task 1: Add the recursive working-count helper and tests

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railNodes.ts`
- Test: `cmd/serf-hub/frontend/src/shell/rail/railNodes.test.ts`

**Interfaces:**
- Consumes: `ApiTreeNode.children` and `ApiTreeNode.state`.
- Produces: exported `workingDescendantCount(node: ApiTreeNode): number`.

- [ ] **Step 1: Write failing tests**

Add a `describe("workingDescendantCount", ...)` block using the existing `apiNode` fixture/helper in `railNodes.test.ts` (or add the smallest equivalent fixture if that file does not expose one):

```ts
it("counts active descendants recursively and excludes non-active nodes", () => {
  const node = apiNode({
    state: "active",
    children: [
      apiNode({ state: "active", children: [apiNode({ state: "active" })] }),
      apiNode({ state: "idle" }),
      apiNode({ state: "awaiting" }),
    ],
  });

  expect(workingDescendantCount(node)).toBe(2);
});

it("returns zero when there are no active descendants", () => {
  expect(workingDescendantCount(apiNode({ state: "active", children: [apiNode({ state: "idle" })] }))).toBe(0);
});
```

Import `workingDescendantCount` from `./railNodes`.

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
cd cmd/serf-hub/frontend && npx vitest run src/shell/rail/railNodes.test.ts
```

Expected: FAIL because `workingDescendantCount` is not exported yet.

- [ ] **Step 3: Implement the minimal helper**

In `railNodes.ts`, add:

```ts
export function workingDescendantCount(node: ApiTreeNode): number {
  return node.children.reduce(
    (count, child) => count + (child.state === "active" ? 1 : 0) + workingDescendantCount(child),
    0,
  );
}
```

- [ ] **Step 4: Run the focused test and verify it passes**

Run the same Vitest command. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/shell/rail/railNodes.ts cmd/serf-hub/frontend/src/shell/rail/railNodes.test.ts
git commit -m "feat(webui): count working subagent descendants"
```

### Task 2: Render the count in the rail activity text

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx`
- Test: `cmd/serf-hub/frontend/src/shell/rail/RailRow.test.tsx`

**Interfaces:**
- Consumes: `workingDescendantCount(node)` from `railNodes.ts`.
- Produces: `activityGloss(session)` returns the existing state/branch text, with the working-descendant label when applicable.

- [ ] **Step 1: Write failing rendering tests**

Extend the existing `activityGloss` tests and row-render tests with these assertions:

```ts
it("reports one recursive working subagent", () => {
  expect(
    activityGloss(
      apiNode({
        state: "active",
        children: [apiNode({ state: "idle", children: [apiNode({ state: "active" })] })],
      }),
    ),
  ).toBe("1 subagent working");
});

it("reports multiple working subagents with plural wording", () => {
  expect(
    activityGloss(apiNode({ children: [apiNode({ state: "active" }), apiNode({ state: "active" })] })),
  ).toBe("2 subagents working");
});
```

Add a render assertion with a working parent and two active descendants to verify `screen.getByTestId("rail-row-activity")` contains `2 subagents working` and preserves a branch suffix such as ` · fix/thing`.

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
cd cmd/serf-hub/frontend && npx vitest run src/shell/rail/RailRow.test.tsx
```

Expected: FAIL because `activityGloss` still returns `working` or does not count descendants.

- [ ] **Step 3: Implement the minimal presentation change**

Import `workingDescendantCount` into `RailRow.tsx`. Change `activityGloss` to compute the recursive count and use:

```ts
const workingCount = workingDescendantCount(session);
const activity = workingCount === 0
  ? humanizeState(session.state, session.ask_pending === true)
  : `${workingCount} subagent${workingCount === 1 ? "" : "s"} working`;
const parts = [activity];
```

Keep the existing branch append unchanged. Ensure the existing signal/second-line behavior remains intact for active-descendant-only rows by updating the activity-line eligibility only as needed for the new text; do not alter quiet rows with no activity or descendants.

- [ ] **Step 4: Run focused tests and verify they pass**

Run:

```bash
cd cmd/serf-hub/frontend && npx vitest run src/shell/rail/railNodes.test.ts src/shell/rail/RailRow.test.tsx
```

Expected: PASS.

- [ ] **Step 5: Format touched frontend files**

Run:

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/shell/rail/railNodes.ts src/shell/rail/railNodes.test.ts src/shell/rail/RailRow.tsx src/shell/rail/RailRow.test.tsx
```

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx cmd/serf-hub/frontend/src/shell/rail/RailRow.test.tsx cmd/serf-hub/frontend/src/shell/rail/railNodes.ts cmd/serf-hub/frontend/src/shell/rail/railNodes.test.ts
 git commit -m "feat(webui): show working subagent count"
```

### Task 3: Run the frontend verification gates

**Files:**
- No additional files expected.

**Interfaces:**
- Consumes: the completed rail helper and rendering change.
- Produces: verified frontend tests and a clean working tree apart from intentional commits.

- [ ] **Step 1: Run the canonical frontend gate**

```bash
make test-web
```

Expected: PASS with unit, typecheck, and Biome checks successful.

- [ ] **Step 2: Run the browser gate when available**

```bash
make test-web-browser
```

Expected: PASS on a Chrome-capable host. If unavailable, record the explicit environment failure rather than hiding it.

- [ ] **Step 3: Inspect final repository state**

```bash
git status --short
git log -3 --oneline
```

Expected: no uncommitted implementation changes and commits for the design plus feature.

- [ ] **Step 4: Report exact verification results**

Include the commands run, pass/fail status, any environment limitation, changed files, and commit hashes.
