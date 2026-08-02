# Mobile Viewport Height Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the complete mobile WebUI shell, including the strip beneath the session composer, inside iOS Safari’s visible viewport.

**Architecture:** Correct the shared shell height once in `AppShell.module.css`, preserving `100vh` as a fallback and overriding it with `100dvh`. Test the CSS contract in the existing shell test and verify the full height chain in a real Chrome layoutguard fixture that mirrors `AppShell → StackHost → PaneScaffold → footer` for both session and non-session panes.

**Tech Stack:** React 19, TypeScript 6, CSS Modules, Vitest 4, Vite 8, headless Chrome layoutguard.

## Global Constraints

- Keep `height: 100vh` before `height: 100dvh` for fallback ordering.
- Do not add document-level overflow clipping.
- Keep `StackHost`, `PaneScaffold`, and composer geometry unchanged.
- Preserve internal pane-body scrolling and desktop behavior.
- Default tests must remain deterministic and require no network, provider credentials, or ambient machine state.

## File Structure

- Modify `cmd/serf-hub/frontend/src/shell/AppShell.module.css`: own the shared viewport-height correction.
- Modify `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx`: pin fallback and override ordering in a deterministic source-level test.
- Create `cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height/case.json`: define a mobile browser case and loaded styles.
- Create `cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height/harness.html`: reproduce the complete shared height chain with session and non-session fixtures.
- Create `cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height/assert.mjs`: assert shell, pane, footer, and document bottoms stay within the viewport.

---

### Task 1: Pin and Implement the Shared Shell Height

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx:1134-1148`
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.module.css:1-6`

**Interfaces:**
- Consumes: the existing `.shell` CSS-module class rendered by `AppShell.tsx`.
- Produces: an ordered CSS contract: `height: 100vh;` followed by `height: 100dvh;`.

- [ ] **Step 1: Add the failing CSS contract test**

Append beside the existing mobile full-bleed CSS test:

```ts
test("mobile: the shared shell follows the visible viewport while retaining a vh fallback", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "AppShell.module.css"), "utf8");
  const shellRule = css.match(/\.shell \{([^}]*)\}/);
  expect(shellRule).not.toBeNull();

  const fallback = shellRule![1]!.indexOf("height: 100vh");
  const dynamic = shellRule![1]!.indexOf("height: 100dvh");
  expect(fallback).toBeGreaterThanOrEqual(0);
  expect(dynamic).toBeGreaterThan(fallback);
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npx vitest run src/shell/AppShell.test.tsx -t "shared shell follows the visible viewport"
```

Expected: FAIL because `.shell` has no `height: 100dvh` declaration.

- [ ] **Step 3: Add the minimal shell-height override**

Change `.shell` to:

```css
.shell {
  display: flex;
  flex-direction: column;
  height: 100vh;
  height: 100dvh;
  background: var(--surface-0);
}
```

Add a short comment explaining that `100vh` is the fallback and `100dvh` follows mobile browser chrome.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
cd cmd/serf-hub/frontend
npx vitest run src/shell/AppShell.test.tsx -t "shared shell follows the visible viewport"
```

Expected: PASS.

- [ ] **Step 5: Run the full AppShell test file**

Run:

```bash
cd cmd/serf-hub/frontend
npx vitest run src/shell/AppShell.test.tsx
```

Expected: PASS with no failed tests.

### Task 2: Add a Full-Height Browser Regression Guard

**Files:**
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height/case.json`
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height/harness.html`
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height/assert.mjs`

**Interfaces:**
- Consumes: the `.shell` and `.content` rules from `AppShell.module.css`, `.host`/`.body` rules from `StackHost.module.css`, and `.pane`/`.body`/`.footer` rules from `panescaffold.module.css`.
- Produces: layoutguard assertions over `[data-shell]`, `[data-pane]`, `[data-pane-body]`, `[data-footer]`, and the document root for session and non-session fixtures.

- [ ] **Step 1: Create the mobile browser case metadata**

Use the same JSON fields and stylesheet-loading pattern as `scripts/layoutguard/cases/askdock-mobile-tall-crush/case.json`. Set a 390 px mobile width and a fixed test height, and load `styles/global.css`, `styles/tokens.css`, `shell/AppShell.module.css`, `shell/mobile/StackHost.module.css`, and `widgets/panescaffold/panescaffold.module.css`.

- [ ] **Step 2: Create the complete height-chain harness**

Render two fixtures in `harness.html`, with only one visible at a time if the layoutguard case format supports phases:

```html
<div data-shell class="shell">
  <main class="content">
    <section class="host">
      <div class="body">
        <article data-pane class="pane">
          <div data-pane-body class="body">Scrollable pane content</div>
          <footer data-footer class="footer">
            <label>Message <textarea rows="2"></textarea></label>
          </footer>
        </article>
      </div>
    </section>
  </main>
</div>
```

The non-session fixture omits the footer but keeps the same shell/host/pane chain. Use the class aliases required by the layoutguard CSS-module resolver rather than inventing replacement geometry.

- [ ] **Step 3: Write assertions that name the overflowing element**

In `assert.mjs`, read `window.innerHeight`, `document.documentElement.scrollHeight`, and each marked element’s `getBoundingClientRect()`. Fail when:

```js
const tolerance = 1;
if (document.documentElement.scrollHeight > window.innerHeight + tolerance) {
  failures.push(`document is ${document.documentElement.scrollHeight}px tall inside a ${window.innerHeight}px viewport`);
}
for (const [name, element] of [
  ["shell", document.querySelector("[data-shell]")],
  ["pane", document.querySelector("[data-pane]")],
  ["footer", document.querySelector("[data-footer]")],
]) {
  if (!element) continue;
  const box = element.getBoundingClientRect();
  if (box.bottom > window.innerHeight + tolerance) {
    failures.push(`${name} escapes the viewport by ${(box.bottom - window.innerHeight).toFixed(1)}px`);
  }
}
```

Return the result shape required by the existing layoutguard runner.

- [ ] **Step 4: Mutation-check the guard**

Temporarily remove `height: 100dvh` from `AppShell.module.css`, run:

```bash
cd cmd/serf-hub/frontend
npm run layoutguard -- mobile-shell-viewport-height
```

Expected: FAIL naming the document, shell, pane, or footer bottom. Restore `height: 100dvh` immediately.

- [ ] **Step 5: Run the guard and verify GREEN**

Run:

```bash
cd cmd/serf-hub/frontend
npm run layoutguard -- mobile-shell-viewport-height
```

Expected: PASS for both session and non-session fixtures.

### Task 3: Verify, Review, Commit, Rebuild, and Restart

**Files:**
- Verify only; no additional source files unless review identifies a defect.

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: a committed, built WebUI and a restarted hub process using the repository’s existing operational command.

- [ ] **Step 1: Run frontend verification**

```bash
cd cmd/serf-hub/frontend
npm test
npm run layoutguard
npm run spawnguard
npm run lint
npm run build
```

Expected: every command exits 0. Read and fix all warnings or failures.

- [ ] **Step 2: Review the diff**

```bash
git diff --check
git diff -- cmd/serf-hub/frontend/src/shell/AppShell.module.css \
  cmd/serf-hub/frontend/src/shell/AppShell.test.tsx \
  cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height
```

Confirm the change stays within the approved scope and does not alter composer, StackHost, PaneScaffold, or document overflow rules.

- [ ] **Step 3: Commit only implementation files**

```bash
git add \
  cmd/serf-hub/frontend/src/shell/AppShell.module.css \
  cmd/serf-hub/frontend/src/shell/AppShell.test.tsx \
  cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height
git commit -m "fix(webui): fit mobile shell to visible viewport"
```

- [ ] **Step 4: Discover the existing hub lifecycle without guessing**

Inspect running processes and repository documentation or scripts:

```bash
pgrep -af 'serf-hub|cmd/serf-hub'
rg -n "restart.*hub|serf-hub.*restart|launchctl|systemctl" README.md docs Makefile scripts cmd -g '!**/node_modules/**'
```

Use the established lifecycle mechanism. Do not kill unrelated processes or invent a new service configuration.

- [ ] **Step 5: Rebuild and restart the hub**

Run the repository’s canonical build target:

```bash
make build-hub
```

Then run the exact restart command identified in Step 4. If no managed hub is running, report that fact instead of starting an unrequested duplicate.

- [ ] **Step 6: Verify the restarted hub**

Confirm the expected hub process is running and serving its established endpoint. Record the build command, restart command, process identity, and health-check result for the final report.
