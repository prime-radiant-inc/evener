# Mobile Root Overscroll Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Disable mobile browser pull-to-refresh, rubber-band page bounce, and root scroll chaining while preserving scrolling inside WebUI panes.

**Architecture:** Extend the existing mobile-only document lock in `global.css` with `overscroll-behavior: none` on `html`. Pin the selector, breakpoint, value, and existing `html, body` overflow lock in a deterministic Node-environment Vitest source test; then verify the existing full-shell browser guard and frontend toolchain.

**Tech Stack:** CSS, TypeScript 6, Vitest 4, headless Chrome layoutguard, Vite 8, Biome.

## Global Constraints

- Apply `overscroll-behavior: none` only at `@media (max-width: 899px)`.
- Apply the declaration to `html`, not to pane scroll containers.
- Preserve `html, body { overflow: hidden; }` on mobile.
- Preserve pane-owned scrolling.
- Leave desktop overscroll behavior unchanged.
- Keep the shared shell's `100vh` fallback and `100dvh` override unchanged.
- Default tests must remain deterministic and require no network, provider credentials, or ambient machine state.

## File Structure

- Modify `cmd/serf-hub/frontend/src/styles/global.css`: own the mobile document-level overscroll policy beside the existing document lock.
- Create `cmd/serf-hub/frontend/src/styles/mobile-root-overscroll.test.ts`: pin the authored CSS contract in a Node-environment Vitest test.

---

### Task 1: Add and Verify Mobile Root Overscroll Containment

**Files:**
- Modify: `cmd/serf-hub/frontend/src/styles/global.css:91-99`
- Create: `cmd/serf-hub/frontend/src/styles/mobile-root-overscroll.test.ts`

**Interfaces:**
- Consumes: the existing mobile `html, body { overflow: hidden; }` document-lock rule and pane-owned scroll containers.
- Produces: mobile-only `html { overscroll-behavior: none; }` without changing desktop or pane scrolling.

- [ ] **Step 1: Write the failing CSS contract test**

Create `mobile-root-overscroll.test.ts`:

```ts
// @vitest-environment node

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const STYLES_DIR = dirname(fileURLToPath(import.meta.url));
const GLOBAL_CSS = readFileSync(join(STYLES_DIR, "global.css"), "utf8");

test("mobile: html suppresses root overscroll while panes retain scroll ownership", () => {
  const mobile = GLOBAL_CSS.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  expect(mobile).not.toBeNull();

  const htmlRule = mobile![1]!.match(/html \{([^}]*)\}/);
  expect(htmlRule).not.toBeNull();
  expect(htmlRule![1]).toContain("overscroll-behavior: none");

  const documentLock = mobile![1]!.match(/html,\s*\n\s*body \{([^}]*)\}/);
  expect(documentLock).not.toBeNull();
  expect(documentLock![1]).toContain("overflow: hidden");
});

test("desktop: root overscroll containment is not declared outside the mobile query", () => {
  const beforeMobile = GLOBAL_CSS.split("@media (max-width: 899px)", 1)[0]!;
  expect(beforeMobile).not.toContain("overscroll-behavior");
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npx vitest run src/styles/mobile-root-overscroll.test.ts
```

Expected: FAIL because the mobile query has no standalone `html` rule with `overscroll-behavior: none`.

- [ ] **Step 3: Add the minimal mobile root rule**

Change the mobile block in `global.css` to:

```css
@media (max-width: 899px) {
  html {
    overscroll-behavior: none;
  }

  html,
  body {
    overflow: hidden;
  }
}
```

Update the existing comment to state that the document lock and overscroll containment keep pull-to-refresh, page bounce, and root scroll chaining from moving the app shell while panes retain their own scroll containers.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
cd cmd/serf-hub/frontend
npx vitest run src/styles/mobile-root-overscroll.test.ts
```

Expected: both tests PASS.

- [ ] **Step 5: Mutation-check the contract**

Temporarily change `overscroll-behavior: none` to `overscroll-behavior: contain`, rerun:

```bash
cd cmd/serf-hub/frontend
npx vitest run src/styles/mobile-root-overscroll.test.ts
```

Expected: FAIL naming the missing `overscroll-behavior: none` contract. Restore `none` immediately and rerun to PASS.

- [ ] **Step 6: Run integration verification**

Run:

```bash
cd cmd/serf-hub/frontend
npm run layoutguard -- mobile-shell-viewport-height
npm run lint
npm run build
```

Expected: every command exits 0. The browser guard confirms the complete mobile shell remains viewport-contained; lint and build confirm the new authored CSS is valid and bundled.

- [ ] **Step 7: Review and commit only the overscroll files**

Run:

```bash
git diff --check
git diff -- \
  cmd/serf-hub/frontend/src/styles/global.css \
  cmd/serf-hub/frontend/src/styles/mobile-root-overscroll.test.ts
git add \
  cmd/serf-hub/frontend/src/styles/global.css \
  cmd/serf-hub/frontend/src/styles/mobile-root-overscroll.test.ts
git commit -m "fix(webui): contain mobile root overscroll"
```

Confirm unrelated concurrent workspace changes remain unstaged and untouched.
