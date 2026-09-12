# Web UI typography, spacing and balance — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the web hub read well: a real type ramp, a bounded reading measure, a four-step vertical rhythm, sentence-case pane headings, disciplined faces and figures, an AA `--ink-low`, mobile fixes, folded tool-call runs, and contract tests so none of it fragments again.

**Architecture:** Every value change lands in `styles/tokens.css` and is consumed through `var(--name)`, so the existing token contract carries it; component stylesheets change only where they consumed a literal, an undefined token, or a decision this plan revises. New behaviour (tool-run folding, the transcript-width preference, the type specimen route) follows the repo's existing patterns exactly: zustand prefs mirrored onto `<body data-*>`, `disclosureStore` for persisted open state, `/dev/*` routes gated by `import.meta.env.DEV`, layoutguard cases for geometry.

**Tech Stack:** React 19, CSS Modules, vitest + Testing Library (jsdom), Biome, layoutguard (headless Chrome via `scripts/layoutguard`).

**Spec:** `docs/web-ui/typography-spacing-critique-2026-09-06.md` (findings + R1–R10). `docs/web-ui/design-system.md` is the standing design law and gets updated in Task 10.

## Global Constraints

- All paths below are relative to `cmd/evener-hub/frontend/` unless they start with `docs/`. Run frontend commands from that directory.
- No colour literal outside `src/styles/tokens.css`; no raw `z-index`; focus rings only via `var(--focus-ring)` (`src/styles/token-contract.test.ts` enforces all three).
- After this plan: no `font-size: <n>px` outside `tokens.css`; `letter-spacing` only via `--tracking-display` / `--tracking-eyebrow`; every `text-transform: uppercase` rule also declares `font-size: var(--font-size-caption)` and `letter-spacing: var(--tracking-eyebrow)` (Task 9 enforces).
- Spacing only via `--space-*` / `--rhythm-*` tokens (existing convention; do not add raw px).
- Sentence case for all UI copy. Eyebrows are at most two words.
- Before every commit: `npx biome check --write <touched files under src/>`; then `npx vitest run <touched test files>`. Before the PR: `make test-web` and `make test-web-browser` (Chrome is installed on this host) from the repo root.
- Never `git add -A` (the frontend's `node_modules` is an untracked symlink here). Add files by path.
- Tests: no test may assert mocked behaviour; jsdom cannot evaluate CSS, so geometry assertions go in layoutguard cases and token/value assertions read the stylesheets off disk (the `token-contract.test.ts` pattern).
- Commit after each task with a conventional subject (`fix(webui): …`, `feat(webui): …`, `docs(web-ui): …`), no attribution lines.

---

### Task 1: Token hygiene — undefined tokens, literal sizes, the eyebrow tracking token, an AA `--ink-low`

**Files:**
- Modify: `src/styles/tokens.css`
- Modify: `src/panes/session/composer/composer.module.css:65,128`
- Modify: `src/panes/session/composer/currentwork.module.css:47-55,72,81`
- Modify: `src/panes/spawn/MobileSettingRows.module.css:57`
- Modify: `src/panes/session/transcript/messages/steeringitem.module.css:69-74` and `src/panes/session/transcript/messages/SteeringItem.tsx` (chevron)
- Modify: `src/panes/session/transcript/tools/delegateStatus.module.css`
- Modify: `src/panes/session/chrome/activitypanel.module.css:127,190,201,214`
- Modify: `src/panes/spawn/spawn.module.css:332` and `src/widgets/directorypicker/directorypicker.module.css:202`
- Test: `src/styles/token-contract.test.ts` (add the `--ink-low` AA check)

**Interfaces:**
- Produces tokens later tasks consume: `--tracking-eyebrow: 0.06em` (replaces `--tracking-micro`, which is deleted), `--line-height-ui: 1.4`, `--font-size-display`, `--rhythm-line/-item/-group/-exchange`, `--session-measure`. Task 4 sets the ramp values; this task only adds the new names with today's values so nothing moves yet.

- [ ] **Step 1: Write the failing `--ink-low` contrast test**

Append to `src/styles/token-contract.test.ts`, right after the existing test `"the four -ink companions clear 4.5:1 on their theme's lightest text grounds"`:

```ts
// --ink-low is documented as placeholder/disabled/timestamp ink, but 96
// text rules set it as `color` (docs/web-ui/typography-spacing-critique-
// 2026-09-06.md finding 7). It therefore has to clear AA on the two grounds
// text actually sits on in each theme, the same bar the -ink companions meet.
test("--ink-low clears 4.5:1 on the page and pane surfaces in both themes", () => {
  const themes = [
    { name: "dark", block: extractBlock(TOKENS_CSS, /(?:^|\n):root(?:\s*,\s*\[data-theme="dark"\])?\s*\{/) },
    { name: "light", block: extractBlock(TOKENS_CSS, /\[data-theme="light"\][^{]*\{/) },
  ];
  for (const theme of themes) {
    const inkLow = declaredToken(theme.block, "--ink-low");
    expect(inkLow, `${theme.name} declares --ink-low`).toBeDefined();
    for (const groundName of ["--surface-0", "--surface-1"]) {
      const ground = declaredToken(theme.block, groundName);
      expect(ground, `${theme.name} declares ${groundName}`).toBeDefined();
      if (!inkLow || !ground) continue;
      const ratio = contrastRatio(parseHexColor(inkLow), parseHexColor(ground));
      expect(ratio, `${theme.name} --ink-low on ${groundName}`).toBeGreaterThanOrEqual(4.5);
    }
  }
});
```

- [ ] **Step 2: Run it and watch it fail**

Run: `npx vitest run src/styles/token-contract.test.ts -t "ink-low"`
Expected: FAIL, dark ratio ≈3.08 on `--surface-1`.

- [ ] **Step 3: Change the two `--ink-low` values and add the new token names**

In `src/styles/tokens.css`:

- Dark block: `--ink-low: #8A8E95; /* placeholders, disabled, timestamps — 4.7:1 on --surface-1, 5.4:1 on --surface-0 */`
- Light block: `--ink-low: #6F737A; /* 4.8:1 on white, 4.6:1 on --surface-0; also clears the diff-marker 3:1 floor */`
- Replace `--tracking-micro: 0.08em;` with `--tracking-eyebrow: 0.06em; /* the ONE uppercase eyebrow tracking: caption size, ≤2 words */`
- After `--line-height-title: 1.3;` add `--line-height-ui: 1.4; /* dense chrome rows: rail, status row, tables */`
- In the `body` ramp block add `--font-size-display: calc(28px * var(--font-scale)); /* welcome hero only */` (Task 4 sets the rest of the ramp).
- After the `--space-9` line add:

```css
  /* Vertical rhythm, named by what sits on either side (critique R3). */
  --rhythm-line: var(--space-1); /* 4: inside one item (intent → call) */
  --rhythm-item: var(--space-2); /* 8: between items in a run */
  --rhythm-group: var(--space-4); /* 16: run → next speaker header; above a turn footer */
  --rhythm-exchange: var(--space-5); /* 24: above a user message */
```

- [ ] **Step 4: Fix the four undefined-token sites**

- `composer.module.css:65` `border-radius: var(--radius-sm);` → `border-radius: var(--radius-chip);`
- `currentwork.module.css:81` same replacement.
- `currentwork.module.css:72` `text-decoration-color: var(--edge-hi);` → `text-decoration-color: var(--edge-strong);`
- `MobileSettingRows.module.css:57` `font-size: var(--font-size-title);` → `font-size: var(--font-size-pane-title);`

- [ ] **Step 5: Replace every literal font-size with a token**

- `composer.module.css:128` `font-size: 11px;` → `font-size: var(--font-size-caption);`
- `activitypanel.module.css` lines 127, 190, 201, 214: `11px` → `var(--font-size-caption)`.
- `delegateStatus.module.css`: `.headId` 11.5px → `var(--font-size-caption)`; `.meta` 11px → caption; `.eyebrow` 11px → caption and `letter-spacing: var(--tracking-micro)` → `var(--tracking-eyebrow)`, `color: var(--ink-low)` → `var(--ink-mid)`; `.mandateBody` / `.diagnosticBody` 13px → `var(--font-size-ui)` and `line-height: 1.5` → `var(--line-height-body)`; `.envRow` 12px → caption, `line-height: 1.5` → `var(--line-height-body)`; `.envK` 11px → caption; `.envV` 12px → caption; `.envVmono` 11.5px → caption; `.toolChip` 10.5px → caption; `.metaSeg { gap: 5px }` → `gap: var(--space-1)`; `.metaSep { margin-right: 2px }` → delete the margin; `.toolChip { padding: 1px 6px }` → `padding: 0 var(--space-2)`.
- `spawn.module.css:332` and `directorypicker.module.css:202`: the phone `font-size: 16px` rules become `font-size: var(--font-size-body);` (Task 4 makes body 16px on phones, which is what these literals were for).
- `steeringitem.module.css` `.chevron { font-size: 9px }`: delete the rule's `font-size`; in `SteeringItem.tsx` render the shared `Chevron` widget (`import { Chevron } from "../../../../widgets"`, `<span className={CLASS.chevron} data-open={open}><Chevron direction="right" /></span>`) exactly the way `ToolRow.tsx` does, and rotate the `svg` (`.chevron[data-open="true"] > svg { transform: rotate(90deg) }`). Read `ToolRow.tsx`'s chevron block first and mirror it.
- Replace the remaining `--tracking-micro` consumers with `--tracking-eyebrow`: `currentwork.module.css:.label`, `widgets/inspectorcard/inspectorcard.module.css`, `widgets/difftable/difftable.module.css`, `widgets/recommendationcard/recommendationcard.module.css`, `widgets/panescaffold/panescaffold.module.css` (Task 5 restyles this rule; for now just rename the token), `widgets/table/table.module.css`. `grep -rn 'tracking-micro' src` must return nothing.

- [ ] **Step 6: Run the contract, the touched component tests, and Biome**

Run: `npx biome check --write src/styles src/panes/session/composer src/panes/session/transcript src/panes/spawn src/widgets && npx vitest run src/styles src/panes/session/transcript/messages src/panes/session/composer src/widgets/inspectorcard src/widgets/table src/widgets/difftable src/widgets/recommendationcard`
Expected: PASS, including the new `--ink-low` test.

- [ ] **Step 7: Commit**

```bash
git add src/styles/tokens.css src/styles/token-contract.test.ts src/panes/session/composer/composer.module.css src/panes/session/composer/currentwork.module.css src/panes/spawn/MobileSettingRows.module.css src/panes/session/transcript/messages/steeringitem.module.css src/panes/session/transcript/messages/SteeringItem.tsx src/panes/session/transcript/tools/delegateStatus.module.css src/panes/session/chrome/activitypanel.module.css src/panes/spawn/spawn.module.css src/widgets/directorypicker/directorypicker.module.css src/widgets/inspectorcard/inspectorcard.module.css src/widgets/difftable/difftable.module.css src/widgets/recommendationcard/recommendationcard.module.css src/widgets/panescaffold/panescaffold.module.css src/widgets/table/table.module.css
git commit -m "fix(webui): token hygiene - undefined tokens, literal sizes, AA ink-low, eyebrow tracking"
```

---

### Task 2: Rail icons as SVG, and the four copy fixes

**Files:**
- Create: `src/shell/rail/railIcons.tsx`
- Modify: `src/shell/rail/Rail.tsx:1405-1432` (icons), `:1454` (empty-state copy)
- Modify: `src/panes/spawn/Spawn.tsx:860` (`mobileTitle`), `:916-919` (intro), the `PromptCard`/`Textarea` placeholder near `:924`
- Test: `src/shell/rail/Rail.test.tsx` (or the nearest existing rail test file — `ls src/shell/rail/*.test.tsx`), `src/panes/spawn/Spawn.test.tsx:503,556`

**Interfaces:**
- Produces `GearIcon`, `SearchIcon`, `SidebarIcon`: `() => JSX.Element`, 16×16 `viewBox`, `stroke="currentColor"`, `strokeWidth="1.5"`, `aria-hidden="true"` — the same grammar as `TreeDrawer.tsx`'s `SessionsIcon` and `widgets/chevron`.

- [ ] **Step 1: Write the failing tests**

In the rail test file add:

```tsx
test("the header icon buttons draw SVG icons, not text glyphs outside the subsetted font", () => {
  renderRail(); // use the file's existing render helper
  for (const label of ["Settings", "Search"]) {
    const button = screen.getByRole("button", { name: label });
    expect(button.querySelector("svg")).toBeTruthy();
    expect(button.textContent?.trim()).toBe("");
  }
});

test("the rail's empty state does not send people to the command line", () => {
  renderRailWithNoSessions();
  expect(screen.queryByText(/command line/i)).toBeNull();
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});
```

In `Spawn.test.tsx` change line 503 to `expect(screen.getByTestId("pane-title-mobile").textContent).toBe("New session");` and line 556 to `.toBe("New session")`. Add:

```tsx
test("the prompt placeholder does not repeat the heading", async () => {
  // reuse the mobile render setup of the test at ~line 490
  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  expect(prompt.placeholder).toBe("Describe the task…");
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `npx vitest run src/shell/rail src/panes/spawn/Spawn.test.tsx`
Expected: FAIL on the four new/changed assertions.

- [ ] **Step 3: Implement**

`src/shell/rail/railIcons.tsx`:

```tsx
// The rail header's three icons, drawn on the app's 16x16 icon grid
// (widgets/chevron, openbutton's OpenIcon, TreeDrawer's SessionsIcon).
// They replace the text glyphs ⚙ ⌕ ☰: global.css subsets Inter to Latin,
// so those code points came from whatever system fallback had them.
const ICON = { viewBox: "0 0 16 16", width: 16, height: 16, "aria-hidden": true } as const;
const STROKE = { fill: "none", stroke: "currentColor", strokeWidth: 1.5, strokeLinecap: "round", strokeLinejoin: "round" } as const;

export function GearIcon() {
  return (
    <svg {...ICON}>
      <circle cx="8" cy="8" r="2.25" {...STROKE} />
      <path d="M8 1.75v2M8 12.25v2M1.75 8h2M12.25 8h2M3.6 3.6l1.4 1.4M11 11l1.4 1.4M3.6 12.4 5 11M11 5l1.4-1.4" {...STROKE} />
    </svg>
  );
}

export function SearchIcon() {
  return (
    <svg {...ICON}>
      <circle cx="7" cy="7" r="4.25" {...STROKE} />
      <path d="M10.2 10.2 14 14" {...STROKE} />
    </svg>
  );
}

export function SidebarIcon() {
  return (
    <svg {...ICON}>
      <rect x="2" y="3" width="12" height="10" rx="1.5" {...STROKE} />
      <path d="M6 3v10" {...STROKE} />
    </svg>
  );
}
```

In `Rail.tsx`: import the three, replace `icon={<span aria-hidden="true">⚙</span>}` → `icon={<GearIcon />}`, `⌕` → `<SearchIcon />`, `☰` → `<SidebarIcon />`. Change line 1454's hint to `hint="Start one with the button above."`. The `.brand button { font-size: var(--font-size-pane-title) }` rule in `Rail.module.css` sized the text glyphs; delete it (SVG icons size themselves).

In `Spawn.tsx`: `mobileTitle="New session"` and `title="New session"`; textarea `placeholder="Describe the task…"` (find the `placeholder=` prop inside the spawn `PromptCard`'s `Textarea`).

- [ ] **Step 4: Run tests + Biome**

Run: `npx biome check --write src/shell/rail src/panes/spawn && npx vitest run src/shell/rail src/panes/spawn`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/shell/rail/railIcons.tsx src/shell/rail/Rail.tsx src/shell/rail/Rail.module.css src/shell/rail/*.test.tsx src/panes/spawn/Spawn.tsx src/panes/spawn/Spawn.test.tsx
git commit -m "fix(webui): SVG rail icons; sentence-case spawn titles; empty-state and placeholder copy"
```

---

### Task 3: Faces and figures — sans for chrome, tabular figures, mono only for machine text

**Files:**
- Modify: `src/panes/session/chrome/modelswitch.module.css:.value`
- Modify: `src/panes/session/chrome/statusrow.module.css:.mono`
- Modify: `src/shell/rail/RailRow.module.css:.time`
- Modify: `src/panes/session/transcript/messages/turnseparator.module.css:.row`
- Modify: `src/panes/spawn/spawn.module.css:.branch`
- Test: `src/styles/faces.test.ts` (new, reads CSS off disk like `token-contract.test.ts`)

- [ ] **Step 1: Write the failing test**

`src/styles/faces.test.ts`:

```ts
// Principle 7 (docs/web-ui/decisions.md): mono is for machine text only.
// These five rules style chrome a person reads constantly (the model chip,
// status figures, rail ages, turn footers, the branch line) and must stay
// on the sans face with tabular figures, never JetBrains Mono.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url)));
const rule = (path: string, selector: string): string => {
  const css = readFileSync(join(SRC, path), "utf8");
  const match = new RegExp(`\\${selector}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) throw new Error(`${path}: no rule ${selector}`);
  return match[1]!;
};

test.each([
  ["panes/session/chrome/modelswitch.module.css", ".value"],
  ["panes/session/chrome/statusrow.module.css", ".figure"],
  ["shell/rail/RailRow.module.css", ".time"],
  ["panes/session/transcript/messages/turnseparator.module.css", ".row"],
])("%s %s is sans with tabular figures", (path, selector) => {
  const body = rule(path, selector);
  expect(body).not.toMatch(/--font-mono/);
  expect(body).toMatch(/font-variant-numeric:\s*tabular-nums/);
});
```

- [ ] **Step 2: Run it, watch it fail**

Run: `npx vitest run src/styles/faces.test.ts`
Expected: FAIL (`.figure` missing; mono present).

- [ ] **Step 3: Implement**

- `modelswitch.module.css .value`: `font-family: var(--font-mono)` → `font-family: var(--font-sans); font-variant-numeric: tabular-nums;`. Update the rule's comment: the id is an identifier but it is the row's most-read label, so it takes the UI face; the qualified id stays in the Details sheet in mono.
- `statusrow.module.css`: rename `.mono` to `.figure` with `font-family: var(--font-sans); font-variant-numeric: tabular-nums;` and update `StatusRow.tsx` (`grep -n 'styles.mono\|CLASS.mono' src/panes/session/chrome/StatusRow.tsx`) and any test that references the class name.
- `RailRow.module.css .time`: drop `font-family: var(--font-mono)`, add `font-variant-numeric: tabular-nums;`.
- `turnseparator.module.css .row`: add `font-variant-numeric: tabular-nums;`.
- `spawn.module.css .branch`: keep mono (a branch name is machine text) — no change; listed only so the implementer does not "fix" it.

- [ ] **Step 4: Run tests + Biome, commit**

Run: `npx biome check --write src/panes/session/chrome src/shell/rail src/panes/session/transcript/messages src/styles && npx vitest run src/styles/faces.test.ts src/panes/session/chrome src/shell/rail`
Expected: PASS.

```bash
git add src/styles/faces.test.ts src/panes/session/chrome/modelswitch.module.css src/panes/session/chrome/statusrow.module.css src/panes/session/chrome/StatusRow.tsx src/shell/rail/RailRow.module.css src/panes/session/transcript/messages/turnseparator.module.css
git commit -m "fix(webui): sans + tabular figures for model, status, rail ages and turn footers"
```

---

### Task 4: The ramp, the measure, the transcript-width preference, and the viewport

**Files:**
- Modify: `src/styles/tokens.css` (body ramp block, phone block)
- Modify: `src/widgets/textarea/textarea.module.css:17`
- Modify: `src/panes/session/transcript/turnblock.module.css:15-28`
- Modify: `src/panes/session/session.module.css:.coldStart,.measure`
- Modify: `src/panes/session/transcript/messages/agentmessageitem.module.css:.bubble,.opener,.column`
- Modify: `src/panes/session/transcript/messages/usermessageitem.module.css:.message,.content,.body`
- Modify: `src/stores/prefs.ts`, `src/panes/settings/sections/theme.tsx`
- Modify: `index.html` (viewport meta + comment)
- Test: `src/styles/viewport-pin.test.ts` (rewrite), `src/stores/prefs.test.ts`, `src/panes/settings/sections/theme.test.tsx`, `src/styles/measure.test.ts` (new), layoutguard case `scripts/layoutguard/cases/transcript-measure/`

**Interfaces:**
- Produces pref `transcriptMeasure: "reading" | "wide"` (`TranscriptMeasurePref`), setter `setTranscriptMeasure`, localStorage key `evener.prefs.transcriptMeasure`, document mirror `<body data-transcript-measure>`; CSS `--session-measure` declared on `body` (44rem) and overridden to 64rem by `body[data-transcript-measure="wide"]`.

- [ ] **Step 1: Write the failing tests**

Rewrite `src/styles/viewport-pin.test.ts` (keep the file name so the guard's history stays findable):

```ts
// @vitest-environment node

// The viewport meta used to pin zoom (maximum-scale=1, user-scalable=no) to
// stop iOS Safari auto-zooming into the 13px composer. That disabled
// pinch-zoom for the whole app (WCAG 1.4.4). Every editable field is now
// 16px on phones (tokens.css's phone block sets --font-size-body: 16px and
// widgets/textarea takes it), so the lock is gone and must not come back.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const STYLES_DIR = dirname(fileURLToPath(import.meta.url));
const FRONTEND_ROOT = dirname(dirname(STYLES_DIR));
const INDEX_HTML = readFileSync(join(FRONTEND_ROOT, "index.html"), "utf8");

function viewportContent(): string {
  const match = /<meta name="viewport" content="([^"]*)"/.exec(INDEX_HTML);
  if (!match) throw new Error("viewport-pin test: could not locate the viewport meta in index.html");
  return match[1]!;
}

test("the viewport meta never disables zoom", () => {
  const content = viewportContent();
  expect(content).toContain("width=device-width");
  expect(content).toContain("initial-scale=1");
  expect(content).not.toContain("maximum-scale");
  expect(content).not.toContain("user-scalable");
});

test("the viewport meta uses viewport-fit=cover so safe-area insets are nonzero", () => {
  expect(viewportContent()).toContain("viewport-fit=cover");
});

test("the viewport meta resizes content around the on-screen keyboard", () => {
  expect(viewportContent()).toContain("interactive-widget=resizes-content");
});
```

`src/styles/measure.test.ts` (new, node environment, same off-disk reading):

```ts
// @vitest-environment node
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url)));
const read = (p: string) => readFileSync(join(SRC, p), "utf8");

test("the ramp is 12/13/15/18/22/28 with 16px body on phones", () => {
  const tokens = read("styles/tokens.css");
  for (const [name, px] of [["caption", 12], ["ui", 13], ["body", 15], ["pane-title", 18], ["page-title", 22], ["display", 28]] as const) {
    expect(tokens).toMatch(new RegExp(`--font-size-${name}: calc\\(${px}px \\* var\\(--font-scale\\)\\);`));
  }
  const phone = tokens.slice(tokens.indexOf("@media (max-width: 899px)"));
  expect(phone).toMatch(/--font-size-body: calc\(16px \* var\(--font-scale\)\);/);
});

test("the reading measure is one body-level token with a wide override", () => {
  const tokens = read("styles/tokens.css");
  expect(tokens).toMatch(/--session-measure: 44rem;/);
  expect(tokens).toMatch(/body\[data-transcript-measure="wide"\]\s*\{\s*--session-measure: 64rem;/);
  expect(read("panes/session/transcript/turnblock.module.css")).not.toMatch(/--session-measure:\s*\d/);
  expect(read("panes/session/session.module.css")).not.toMatch(/76rem/);
});

test("prose is bounded by the column, not a percentage of the pane", () => {
  expect(read("panes/session/transcript/messages/agentmessageitem.module.css")).not.toMatch(/max-width:\s*92%/);
  expect(read("panes/session/transcript/messages/usermessageitem.module.css")).not.toMatch(/max-width:\s*92%/);
});

test("the composer field takes the body size", () => {
  expect(read("widgets/textarea/textarea.module.css")).toMatch(/\.textarea\s*\{[^}]*font-size: var\(--font-size-body\)/);
});
```

In `src/stores/prefs.test.ts`, inside the `describe("corrupted/unrecognized localStorage values …")` block add:

```ts
  test("an unrecognized transcriptMeasure falls back to reading", () => {
    localStorage.setItem(KEY("transcriptMeasure"), "huge");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().transcriptMeasure).toBe("reading");
  });
```

and a new describe:

```ts
describe("transcriptMeasure", () => {
  test("defaults to reading, persists under evener.prefs.transcriptMeasure, and mirrors onto body", () => {
    expect(prefsStore.getState().transcriptMeasure).toBe("reading");
    expect(document.body.dataset.transcriptMeasure).toBe("reading");
    prefsStore.getState().setTranscriptMeasure("wide");
    expect(localStorage.getItem("evener.prefs.transcriptMeasure")).toBe("wide");
    expect(document.body.dataset.transcriptMeasure).toBe("wide");
  });
});
```

In `theme.test.tsx` add a describe mirroring the "Font size" one:

```tsx
describe("Transcript width", () => {
  test("defaults to Reading and updates the pref plus document.body.dataset.transcriptMeasure", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "Reading" }).getAttribute("aria-checked")).toBe("true");
    await user.click(screen.getByRole("radio", { name: "Wide" }));
    expect(prefsStore.getState().transcriptMeasure).toBe("wide");
    expect(document.body.dataset.transcriptMeasure).toBe("wide");
  });
});
```

(Also add `delete document.body.dataset.transcriptMeasure;` beside the existing `delete document.body.dataset.fontSize;` in that file's reset hook.)

- [ ] **Step 2: Run them, watch them fail**

Run: `npx vitest run src/styles/viewport-pin.test.ts src/styles/measure.test.ts src/stores/prefs.test.ts src/panes/settings/sections/theme.test.tsx`
Expected: FAIL on every new assertion.

- [ ] **Step 3: tokens.css**

Replace the `body { --font-scale … }` block with:

```css
body {
  --font-scale: 1;
  --font-size-caption: calc(12px * var(--font-scale)); /* timestamps, meta */
  --font-size-ui: calc(13px * var(--font-scale)); /* dense chrome: rail rows, status row, tables, chips, controls */
  --font-size-body: calc(15px * var(--font-scale)); /* prose, composer, forms, settings values */
  --font-size-pane-title: calc(18px * var(--font-scale)); /* pane/sheet/dialog titles: sentence case, semibold */
  --font-size-page-title: calc(22px * var(--font-scale)); /* section headings, the spawn heading */
  --font-size-display: calc(28px * var(--font-scale)); /* welcome hero only */
  /* The conversation column and everything aligned to it (composer, cold
   * start). 44rem = 704px: ~90 characters at 15px, room for a 100-column
   * code block. Declared on body so the Theme preference below can widen it. */
  --session-measure: 44rem;
}

body[data-transcript-measure="wide"] {
  --session-measure: 64rem;
}
```

Root block line-heights: `--line-height-body: 1.6; /* 15px → 24px, on the 4px grid */`, `--line-height-title: 1.25;`, keep `--line-height-ui: 1.4` from Task 1. In the phone block change `--line-height-body: calc(1.5 * var(--density-scale));` to `calc(1.6 * var(--density-scale))` and add `--font-size-body: calc(16px * var(--font-scale)); /* iOS zooms into any field under 16px; body is the field size */`.

- [ ] **Step 4: Consumers**

- `widgets/textarea/textarea.module.css` `.textarea`: `font-size: var(--font-size-ui)` → `var(--font-size-body)`.
- `turnblock.module.css`: delete the `--session-measure: 76rem;` declaration and its comment paragraph; `.turn` keeps `max-width: var(--session-measure); margin-inline: auto;`.
- `session.module.css`: `.coldStart` and `.measure` `max-width: 76rem` → `max-width: var(--session-measure)`; delete the "consolidate to a token" comment.
- `agentmessageitem.module.css` `.bubble`: `max-width: 92%` → `max-width: 100%`. Below 700px make the opener a grid so prose spans the pane:

```css
@media (max-width: 699px) {
  .opener {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    column-gap: var(--speaker-gap);
    row-gap: var(--rhythm-line);
  }
  /* The column dissolves so its header sits beside the avatar and its
   * bubble drops to a full-width second row. */
  .column {
    display: contents;
  }
  .opener .bubble {
    grid-column: 1 / -1;
  }
}
```

- `usermessageitem.module.css`: `.body` `max-width: 92%` → `100%`; the same phone grid on `.message` with `.content { display: contents }` and `.message .body { grid-column: 1 / -1 }` (the `.actions` inside `.header` keep working; `.header` is a grid item now).
- `index.html`: viewport content becomes `width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content`; rewrite the comment: fields are 16px on phones so zoom is not locked.

- [ ] **Step 5: The preference**

`prefs.ts`: add `export type TranscriptMeasurePref = "reading" | "wide";`, state field `transcriptMeasure: TranscriptMeasurePref;`, setter `setTranscriptMeasure(value: TranscriptMeasurePref): void;`, `const TRANSCRIPT_MEASURE_VALUES: readonly TranscriptMeasurePref[] = ["reading", "wide"];`, `function applyTranscriptMeasure(value) { document.body.dataset.transcriptMeasure = value; }`, read in `loadInitialState` via `readEnum("transcriptMeasure", TRANSCRIPT_MEASURE_VALUES, "reading")` + `applyTranscriptMeasure(...)`, add `"setTranscriptMeasure"` to the `Omit` list, and the setter body `writeRaw("transcriptMeasure", value); applyTranscriptMeasure(value); set({ transcriptMeasure: value });`. Update the file's top comment sentence that lists document-mirrored prefs.

`theme.tsx`: add

```tsx
const TRANSCRIPT_MEASURE_OPTIONS: RadioGroupOption[] = [
  { value: "reading", label: "Reading" },
  { value: "wide", label: "Wide" },
];
```

and a fourth row after Font size:

```tsx
      <div className={CLASS.row}>
        <RadioGroup
          label="Transcript width"
          value={transcriptMeasure}
          options={TRANSCRIPT_MEASURE_OPTIONS}
          onChange={(value) => prefsStore.getState().setTranscriptMeasure(value as "reading" | "wide")}
        />
        <p className={CLASS.help}>Reading keeps lines near 90 characters. Wide uses more of the window.</p>
      </div>
```

with `const transcriptMeasure = usePrefsStore((s) => s.transcriptMeasure);`. Update the intro copy to "Theme, density, font size and transcript width are saved per-browser."

- [ ] **Step 6: The layoutguard case**

Create `scripts/layoutguard/cases/transcript-measure/` with:

`case.json`
```json
{
  "name": "transcript-measure",
  "description": "transcript: a plain agent paragraph never runs past 100 characters per line at 1440 and 1920, and the conversation column is centred at both widths",
  "cssFiles": [
    "styles/global.css",
    "styles/tokens.css",
    "panes/session/transcript/turnblock.module.css",
    "panes/session/transcript/messages/agentmessageitem.module.css",
    "widgets/markdown/markdown.module.css"
  ],
  "widthMatrix": [{ "width": 1440 }, { "width": 1920 }],
  "mutation": {
    "declaration": "tokens.css: body { --session-measure: 44rem } (set to 76rem - the paragraph runs ~150 chars/line at 1440)",
    "verified": "2026-09-06",
    "expect": "fail"
  }
}
```

`harness.html`: body sets `data-font-size="m" data-transcript-measure="reading"`; a `.turn` div containing the opener markup (`div.message.opener > span.avatar + div.column > div.header + div.bubble > div.root > p#prose`) with a 600-character plain paragraph; `window.measure()` returns `{ lines, chars, columnLeft, columnRight, bodyWidth }` where lines = distinct `Range.getClientRects()` tops of `#prose` (the same technique used in the critique), and uses the `__LAYOUTGUARD_WIDTH_MATRIX__` hook the way `cases/compact-session-footer/harness.html` does (read that harness first and copy its matrix wiring verbatim).

`assert.mjs`: for each width, `charsPerLine = chars / lines`; fail if `> 100`; fail if `Math.abs(columnLeft - (bodyWidth - columnRight)) > 1` (not centred). Pass reason reports both numbers per width.

Run: `npm run layoutguard -- transcript-measure` (Chrome required). Then verify the mutation once by hand (set 76rem, expect fail, revert).

- [ ] **Step 7: Run everything touched**

Run: `npx biome check --write src index.html scripts/layoutguard/cases/transcript-measure && npx vitest run src/styles src/stores src/panes/settings src/panes/session src/widgets/textarea && npm run layoutguard -- transcript-measure`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add index.html src/styles/tokens.css src/styles/viewport-pin.test.ts src/styles/measure.test.ts src/widgets/textarea/textarea.module.css src/panes/session/transcript/turnblock.module.css src/panes/session/session.module.css src/panes/session/transcript/messages/agentmessageitem.module.css src/panes/session/transcript/messages/usermessageitem.module.css src/stores/prefs.ts src/stores/prefs.test.ts src/panes/settings/sections/theme.tsx src/panes/settings/sections/theme.test.tsx scripts/layoutguard/cases/transcript-measure
git commit -m "feat(webui): 15/16px body ramp, 44rem reading measure with wide preference, zoom no longer locked"
```

---

### Task 5: Rhythm, pane headings, and one eyebrow

**Files:**
- Modify: `src/widgets/panescaffold/panescaffold.module.css:.header,.title,.body,.footer`
- Modify: `src/panes/session/transcript/messages/agentmessageitem.module.css:.message,.header,.name,.meta`
- Modify: `src/panes/session/transcript/messages/usermessageitem.module.css:.message,.name,.time`
- Modify: `src/panes/session/transcript/toolcallitem.module.css:.call`
- Modify: `src/panes/session/transcript/messages/turnseparator.module.css:.row`
- Modify: `src/panes/session/transcript/messages/thinkblock.module.css:.label,.summary`
- Modify: `src/panes/session/transcript/flow/livenessline.module.css:.line`
- Modify: `src/widgets/tree/tree.module.css:.row`, `src/shell/rail/Rail.module.css:.header,.section,.sectionTitle,.sectionDisclosure`, `src/shell/rail/RailRow.module.css:.activity,.time`
- Modify every uppercase site so its rule declares `font-size: var(--font-size-caption); letter-spacing: var(--tracking-eyebrow); color: var(--ink-mid)` (the 21 rules listed in the critique's finding 4: `Rail.module.css` ×2, `commandpalette.module.css .sectionHeader`, `cheatsheet.module.css .groupTitle`, `settings.module.css .clusterHeader`, `LaunchConfigForm.module.css .groupHeader`, `currentwork.module.css .label`, `CredentialsSection.module.css .groupHeader`, `rawitemview.module.css .type`, `askuser.module.css .detail,.recommended`, `subagentmodule.module.css .section,.sectionLabel`, `delegateStatus.module.css .eyebrow`, `taskspanel.module.css .groupHead`, `detailspanel.module.css .sectionTitle`, `difftable.module.css .headerCell`, `recommendationcard.module.css .eyebrow`, `modelCatalog.module.css .groupRow`, `pathfield.module.css .groupPath,.groupLabel`, `table.module.css .headerCell`)
- Modify: `src/panes/settings/sections.ts:30` ("Agents & models" → "Models") and its tests `sections.test.ts:39,45,66`, `SettingsNav.test.tsx:71`
- Modify: `src/panes/session/transcript/tools/askuser.module.css .detail` (three-word uppercase label: drop the transform, keep caption + ink-mid)
- Test: `src/styles/rhythm.test.ts` (new)

- [ ] **Step 1: Write the failing test**

`src/styles/rhythm.test.ts` (node env, off-disk reads as in Task 4):

```ts
test("pane titles are sentence-case headings, not micro-labels", () => {
  const css = read("widgets/panescaffold/panescaffold.module.css");
  const title = /\.title\s*\{([^}]*)\}/.exec(css)![1]!;
  expect(title).not.toMatch(/text-transform/);
  expect(title).toMatch(/font-size: var\(--font-size-pane-title\)/);
  expect(title).toMatch(/color: var\(--ink-hi\)/);
});

test("every uppercase rule is a complete caption-size eyebrow", () => {
  for (const path of walkCss()) {
    const css = read(path).replace(/\/\*[\s\S]*?\*\//g, "");
    for (const block of css.matchAll(/\{([^}]*)\}/g)) {
      const body = block[1]!;
      if (!/text-transform:\s*uppercase/.test(body)) continue;
      expect(body, `${path}: uppercase rule lacks caption size`).toMatch(/font-size: var\(--font-size-caption\)/);
      expect(body, `${path}: uppercase rule lacks eyebrow tracking`).toMatch(/letter-spacing: var\(--tracking-eyebrow\)/);
    }
  }
});

test("exchange boundaries and runs use the rhythm tokens", () => {
  expect(read("panes/session/transcript/messages/usermessageitem.module.css")).toMatch(/\.message\s*\{[^}]*margin-top: var\(--rhythm-exchange\)/);
  expect(read("panes/session/transcript/toolcallitem.module.css")).toMatch(/\.call\s*\{[^}]*padding: var\(--rhythm-item\) 0/);
  expect(read("widgets/panescaffold/panescaffold.module.css")).toMatch(/\.body\s*\{[^}]*padding: var\(--space-5\)/);
});
```

(`walkCss()` = the same recursive reader `token-contract.test.ts` uses; copy the eight lines rather than exporting from a test file.)

- [ ] **Step 2: Run it, watch it fail**

Run: `npx vitest run src/styles/rhythm.test.ts`
Expected: FAIL.

- [ ] **Step 3: PaneScaffold**

```css
.header {
  flex: none;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-5);
  background: var(--surface-inset);
  border-bottom: 1px solid var(--edge);
}

/* A pane's title is its page heading (critique R4): sentence case, the
 * pane-title step, semibold, full ink. The uppercase micro-label stays for
 * containers INSIDE a page (InspectorCard, RecommendationCard, Table). */
.title {
  flex: 1 1 auto;
  min-width: 0;
  margin: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  color: var(--ink-hi);
  font-family: var(--font-sans);
  font-size: var(--font-size-pane-title);
  font-weight: var(--font-weight-semibold);
  letter-spacing: var(--tracking-display);
  line-height: var(--line-height-title);
}
```

`.body { padding: var(--space-5); padding-bottom: calc(var(--space-5) + max(0px, env(safe-area-inset-bottom) - var(--keyboard-inset, 0px))); }` and in the 899px block `.body { padding: var(--space-4); padding-bottom: calc(var(--space-4) + …same…); }`. `.footer` padding `var(--space-3) var(--space-5)` on desktop, `var(--space-3) var(--space-4)` in the phone block.

- [ ] **Step 4: Transcript rhythm**

- `agentmessageitem.module.css .message { padding: var(--rhythm-line) 0; }`; `.name { font-size: var(--font-size-body); font-weight: var(--font-weight-semibold); }`; `.meta { font-size: var(--font-size-ui); color: var(--ink-mid); }`; `.header { margin-bottom: var(--rhythm-line); }`.
- `usermessageitem.module.css .message { padding: var(--rhythm-line) 0; margin-top: var(--rhythm-exchange); }` and add `.message:first-child { margin-top: 0; }`; `.name` semibold; `.time { font-size: var(--font-size-ui); color: var(--ink-mid); }`. Update the header comment: the 2026-07-30 "no dead air" ruling is revised by the critique (R3): with a bounded measure, the exchange step is a paragraph break.
- `toolcallitem.module.css .call { padding: var(--rhythm-item) 0; }`; `.intent` and `.summary` `font-size: var(--font-size-ui)` stay; `.demoted` stays `--ink-mid`.
- `turnseparator.module.css .row { padding: var(--rhythm-group) 0 var(--rhythm-line); }`.
- `thinkblock.module.css .label, .summary { color: var(--ink-mid); }` (they were `--ink-low`).
- `livenessline.module.css .line { color: var(--ink-mid); }`.

- [ ] **Step 5: Rail rhythm**

- `tree.module.css .row { min-height: 32px; }`.
- `Rail.module.css .header { padding: var(--space-4) var(--space-4); gap: var(--space-3); }`; `.section { padding: var(--space-3) 0; }`; `.sectionTitle` and `.sectionDisclosure`: `color: var(--ink-mid); font-size: var(--font-size-caption); letter-spacing: var(--tracking-eyebrow);` (replacing `0.04em` and `--ink-low`).
- `RailRow.module.css .activity { color: var(--ink-mid); }` for the neutral case (the three hue classes still override); `.time { color: var(--ink-mid); }`.

- [ ] **Step 6: The eyebrow sweep**

For each of the 21 uppercase rules: replace any `letter-spacing: 0.02em|0.04em|0.05em|var(--tracking-micro)` with `var(--tracking-eyebrow)`; ensure `font-size: var(--font-size-caption)`; replace `color: var(--ink-low)` with `var(--ink-mid)`. `askuser.module.css .detail` labels a sentence — delete its `text-transform` and `letter-spacing` and keep caption + `--ink-mid`. Rename the settings cluster: `sections.ts:30` label `"Models"`, and update the three test expectations that spell "Agents & models".

- [ ] **Step 7: Run, Biome, commit**

Run: `npx biome check --write src && npx vitest run src/styles src/widgets/panescaffold src/panes/settings src/panes/session/transcript src/shell/rail src/widgets/table src/widgets/difftable src/widgets/recommendationcard src/widgets/inspectorcard src/widgets/pathfield src/widgets/modelCatalog src/shell/palette src/shell/cheatsheet`
Expected: PASS.

```bash
git add src/styles/rhythm.test.ts src/widgets/panescaffold/panescaffold.module.css src/panes/session/transcript src/shell/rail src/widgets/tree/tree.module.css src/panes/settings src/shell/palette/commandpalette.module.css src/shell/cheatsheet/cheatsheet.module.css src/widgets/table/table.module.css src/widgets/difftable/difftable.module.css src/widgets/recommendationcard/recommendationcard.module.css src/widgets/inspectorcard/inspectorcard.module.css src/widgets/pathfield/pathfield.module.css src/widgets/modelCatalog/modelCatalog.module.css src/panes/session/composer/currentwork.module.css src/panes/session/chrome
git commit -m "feat(webui): four-step rhythm, sentence-case pane headings, one eyebrow recipe"
```

---

### Task 6: Desktop balance and the phone drawer

**Files:**
- Modify: `src/panes/spawn/spawn.module.css:.form,.mobilePromptIntro,.mobilePromptHeading,.mobilePromptSubtitle` and `src/panes/spawn/Spawn.tsx:916-919`
- Modify: `src/panes/settings/settings.module.css:.content`
- Modify: `src/panes/welcome/welcome.module.css`, `src/panes/welcome/WelcomeContent.tsx`, `src/widgets/emptystate/emptystate.module.css:.title`
- Modify: the drawer host — `grep -rn 'TreeDrawer\|"Sessions"' src/shell/mobile/StackHost.tsx` finds the `Sheet` that mounts `<Rail scrollOwner="parent" …/>`; the Sheet body's padding plus the rail's `--surface-canvas` fill is the inset box. Add a `flush` prop to `Sheet`? No — keep the widget; instead give the drawer's rail wrapper a class that zeroes the sheet body padding for this one consumer (`src/shell/mobile/treedrawer.module.css .drawerBody { margin: calc(-1 * var(--space-4)); }` is the smallest change; confirm against `dialog.module.css .body { padding: var(--space-4) }`).
- Test: `src/panes/spawn/Spawn.test.tsx`, `src/panes/welcome/Welcome.test.tsx`

- [ ] **Step 1: Failing tests**

`Spawn.test.tsx`: in the desktop render test (~line 550) add `expect(screen.getByRole("heading", { name: "What should the agent do?" })).toBeTruthy();` (today the heading exists in the DOM but is display:none on desktop; the assertion is about the rename below, so make it `getByRole("heading", { name: "What should the agent do?", level: 2 })`).

`Welcome.test.tsx`: add `expect(screen.getByRole("heading", { name: "No session open" }).className).toMatch(/display/);` — the empty-state title on the welcome pane takes the display size via a new `EmptyState` prop `size="display"`.

- [ ] **Step 2: Run, watch fail**

Run: `npx vitest run src/panes/spawn/Spawn.test.tsx src/panes/welcome`

- [ ] **Step 3: Spawn on desktop**

`spawn.module.css`: `.form { margin-inline: auto; max-width: var(--session-measure); gap: var(--space-5); }`; rename `.mobilePromptIntro` → `.promptIntro` (shown at every width: delete the `display: none` and the phone `display: block`), `.mobilePromptHeading` → `.promptHeading { font-size: var(--font-size-page-title); letter-spacing: var(--tracking-display); }`, `.mobilePromptSubtitle` → `.promptSubtitle`. Update the `CLASS` map and JSX in `Spawn.tsx` (`h3` → `h2`). Delete the `PaneScaffold` `title`'s duplicate by keeping `title="New session"` (the heading is the page's; the pane title stays for the top bar / tab).

- [ ] **Step 4: Settings and welcome**

`settings.module.css .content { max-width: var(--session-measure); }`.

`emptystate/index.tsx`: add `size?: "default" | "display"`; `.titleDisplay { font-size: var(--font-size-display); }` in `emptystate.module.css`; gallery section `src/dev/gallery-sections/emptystate.tsx` shows both. `Welcome.tsx` passes `size="display"`. In `WelcomeContent.tsx` render the chord hints as a two-column definition list (`<dl className={CLASS.hints}><div><dt><KeyHint …/></dt><dd>{desc}</dd></div>…</dl>`) with `.hints { display: grid; grid-template-columns: auto 1fr; column-gap: var(--space-3); row-gap: var(--space-2); }` — keep the `hints`/`hintRow`/`hintFooter` class names so `welcome.overflow.test.tsx` still resolves them.

- [ ] **Step 5: The drawer**

Apply the flush wrapper from the Files list; verify with `npm run shellguard` (Chrome) that the drawer's tap targets still pass.

- [ ] **Step 6: Run, Biome, commit**

Run: `npx biome check --write src/panes/spawn src/panes/settings src/panes/welcome src/widgets/emptystate src/shell/mobile src/dev && npx vitest run src/panes/spawn src/panes/settings src/panes/welcome src/widgets/emptystate src/shell/mobile src/dev`

```bash
git add src/panes/spawn src/panes/settings/settings.module.css src/panes/welcome src/widgets/emptystate src/dev/gallery-sections/emptystate.tsx src/shell/mobile
git commit -m "feat(webui): centred spawn form with its heading on desktop, bounded settings, display-size welcome, flush drawer"
```

---

### Task 7: Fold tool-call runs; agent prose as a document

**Files:**
- Modify: `src/panes/session/transcript/toolRenderers.ts` (`fold?: "never" | "consequential"` on `ToolRendererDescriptor`)
- Modify: `src/panes/session/transcript/tools/editTools.tsx`, `shellTool.tsx`, `worktreeTool.tsx` (`fold: "consequential"`); `subagentModule.tsx`, `askUser.tsx`, `taskCard.tsx`, `jobTools.tsx`, `useSkillTool.tsx` (`fold: "never"`)
- Create: `src/panes/session/transcript/toolRuns.ts`, `src/panes/session/transcript/ToolRunGroup.tsx`, `src/panes/session/transcript/toolrungroup.module.css`
- Modify: `src/panes/session/transcript/TurnBlock.tsx:216-268`
- Modify: `src/panes/session/transcript/messages/agentmessageitem.module.css:.bubble,.continuation`
- Test: `src/panes/session/transcript/toolRuns.test.ts`, `src/panes/session/transcript/TurnBlock.test.tsx`

**Interfaces:**
- `foldToolRuns(entries: readonly ProjectedEntry[], opts: { turnSettled: boolean; descriptorFor: (toolName: string) => ToolRendererDescriptor }): (ProjectedEntry | ToolRun)[]` where `ToolRun = { kind: "run"; id: string; entries: Extract<ProjectedEntry, { kind: "item" }>[] }`.
- `runLabel(run: ToolRun, descriptorFor): string` → `"{n} steps · {summary}"` where summary is the last entry whose descriptor has `fold === "consequential"`, else the last entry.

- [ ] **Step 1: Failing unit tests for the fold**

`toolRuns.test.ts`:

```ts
import { describe, expect, test } from "vitest";
import type { ItemModel } from "../../../protocol/model";
import type { ProjectedEntry } from "../../../transcriptDisplay/projector";
import { foldToolRuns, runLabel } from "./toolRuns";

const tool = (id: string, toolName: string, status = "completed"): Extract<ProjectedEntry, { kind: "item" }> => ({
  kind: "item", id, turnId: "t1", sourceIndex: 0, isMessage: false,
  item: { id, turnId: "t1", type: "commandExecution", text: "", toolName, status } as ItemModel,
});
const descriptorFor = (name: string) => ({
  match: name,
  summary: () => (name === "write_file" ? "Wrote foo.py" : `Read ${name}`),
  fold: name === "write_file" ? ("consequential" as const) : name === "delegate" ? ("never" as const) : undefined,
});

test("three settled quiet calls fold into one run", () => {
  const out = foldToolRuns([tool("a", "read_file"), tool("b", "read_file"), tool("c", "grep")], { turnSettled: true, descriptorFor });
  expect(out).toHaveLength(1);
  expect(out[0]).toMatchObject({ kind: "run", entries: [{ id: "a" }, { id: "b" }, { id: "c" }] });
});

test("two calls do not fold", () => {
  const out = foldToolRuns([tool("a", "read_file"), tool("b", "read_file")], { turnSettled: true, descriptorFor });
  expect(out.map((e) => e.kind)).toEqual(["item", "item"]);
});

test("a failed call breaks the run and stays visible", () => {
  const out = foldToolRuns([tool("a", "read_file"), tool("b", "read_file", "failed"), tool("c", "read_file"), tool("d", "read_file")], { turnSettled: true, descriptorFor });
  expect(out.map((e) => e.kind)).toEqual(["item", "item", "item", "item"]);
});

test("a live turn never folds", () => {
  const out = foldToolRuns([tool("a", "read_file"), tool("b", "read_file"), tool("c", "read_file")], { turnSettled: false, descriptorFor });
  expect(out.map((e) => e.kind)).toEqual(["item", "item", "item"]);
});

test("a never-fold tool splits the run", () => {
  const out = foldToolRuns([tool("a", "read_file"), tool("b", "delegate"), tool("c", "read_file"), tool("d", "read_file"), tool("e", "read_file")], { turnSettled: true, descriptorFor });
  expect(out.map((e) => e.kind)).toEqual(["item", "item", "run"]);
});

test("the label names the consequential step", () => {
  const [run] = foldToolRuns([tool("a", "read_file"), tool("b", "write_file"), tool("c", "read_file")], { turnSettled: true, descriptorFor });
  expect(runLabel(run as never, descriptorFor)).toBe("3 steps · Wrote foo.py");
});
```

- [ ] **Step 2: Run, watch fail** — `npx vitest run src/panes/session/transcript/toolRuns.test.ts` (module missing).

- [ ] **Step 3: `toolRuns.ts`**

```ts
import type { ProjectedEntry } from "../../../transcriptDisplay/projector";
import type { ToolRendererDescriptor } from "./toolRenderers";

export type ToolItemEntry = Extract<ProjectedEntry, { kind: "item" }>;
export interface ToolRun { kind: "run"; id: string; entries: ToolItemEntry[] }
export interface FoldOptions { turnSettled: boolean; descriptorFor: (toolName: string) => Pick<ToolRendererDescriptor, "summary" | "fold" | "failed" | "autoExpand">; }

const MIN_RUN = 3;

function foldable(entry: ProjectedEntry, opts: FoldOptions): entry is ToolItemEntry {
  if (entry.kind !== "item" || entry.item.type !== "commandExecution") return false;
  const { item } = entry;
  if (item.status !== "completed") return false;
  const d = opts.descriptorFor(item.toolName ?? "");
  if (d.fold === "never") return false;
  if (d.failed?.(item) || d.autoExpand?.(item)) return false;
  return true;
}

export function foldToolRuns(entries: readonly ProjectedEntry[], opts: FoldOptions): (ProjectedEntry | ToolRun)[] {
  if (!opts.turnSettled) return [...entries];
  const out: (ProjectedEntry | ToolRun)[] = [];
  let run: ToolItemEntry[] = [];
  const flush = () => {
    if (run.length >= MIN_RUN) out.push({ kind: "run", id: `run:${run[0]!.id}`, entries: run });
    else out.push(...run);
    run = [];
  };
  for (const entry of entries) {
    if (foldable(entry, opts)) run.push(entry);
    else { flush(); out.push(entry); }
  }
  flush();
  return out;
}

export function runLabel(run: ToolRun, descriptorFor: FoldOptions["descriptorFor"]): string {
  const named = [...run.entries].reverse().find((e) => descriptorFor(e.item.toolName ?? "").fold === "consequential") ?? run.entries[run.entries.length - 1]!;
  return `${run.entries.length} steps · ${descriptorFor(named.item.toolName ?? "").summary(named.item)}`;
}
```

Add `fold?: "never" | "consequential";` to `ToolRendererDescriptor` with a comment: `never` = a card a reader must always see (delegates, asks, task lists, skills, jobs); `consequential` = a mutating step that names a folded run (edits, shell, worktree); unset = quiet (reads, searches, web).

- [ ] **Step 4: `ToolRunGroup.tsx` and its CSS**

A `<details data-testid="tool-run">` + `<summary className={CLASS.summary}>` with the run label and a `Chevron` (same idiom as `ProjectedIntentGroup` in `TurnBlock.tsx`, including `disclosureScopeForSession`, `scopedDisclosureId(scope, run.id)`, `isDisclosureOpen`, `toggleDisclosure`, and `expandDetailsByDefault(config)` as the fallback). The body renders each entry through `ToolCallItem` exactly as the non-folded path does (copy the `<ItemRenderer …/>` props block from `TurnBlock.tsx:238-253`; the component is `ToolCallItem`). CSS: `.summary { display: inline-flex; align-items: center; gap: var(--space-2); padding: var(--rhythm-line) 0; color: var(--ink-mid); font-family: var(--font-sans); font-size: var(--font-size-ui); cursor: pointer; list-style: none; }`, `.summary::-webkit-details-marker { display: none }`, `.summary:hover { background: color-mix(in oklab, var(--ink-mid) 8%, transparent); border-radius: var(--radius-control); }`, `.summary:focus-visible { outline: var(--focus-ring); outline-offset: 2px; }`, `.body { display: flex; flex-direction: column; }`.

- [ ] **Step 5: Wire into `TurnBlock.tsx`**

Replace the `for` loop's input: `const laidOut = foldToolRuns(projectedTurn.entries, { turnSettled: sourceTurn.status !== "inProgress", descriptorFor: (name) => toolRendererFor(name) });` and iterate `laidOut`; when `entry.kind === "run"`, push `<div key={entry.id} className={CLASS.runContent} data-testid="run-content"><ToolRunGroup run={entry} …same props as items… /></div>`. Check `TurnModel.status`'s literal values in `protocol/model.ts` and use the in-progress one.

`TurnBlock.test.tsx` additions: a settled turn with three `read_file` items renders one `tool-run` and zero `tool-row` until the summary is clicked, then three `tool-row`; a turn with a failed middle item renders three `tool-row` and no `tool-run`.

- [ ] **Step 6: Agent prose as a document**

`agentmessageitem.module.css .bubble`: remove `background`, `border-radius`, and horizontal padding; keep `padding: var(--rhythm-line) 0; max-width: 100%; width: 100%;`. Delete `.continuation`'s radius rule (keep the class for the TSX). Rewrite the header comment: the user's words keep the accent wash; the agent's answer is the document (critique R9). Keep `usermessageitem.module.css .body` as is.

- [ ] **Step 7: Run, Biome, commit**

Run: `npx biome check --write src/panes/session/transcript && npx vitest run src/panes/session/transcript src/dev`

```bash
git add src/panes/session/transcript
git commit -m "feat(webui): fold settled tool-call runs into one row; agent prose reads as a document"
```

---

### Task 8: The type specimen route

**Files:**
- Create: `src/dev/TypeSpecimen.tsx`, `src/dev/typespecimen.module.css`, `src/dev/TypeSpecimen.test.tsx`
- Modify: `src/App.tsx` (route `/dev/type`, DEV-gated like the others)
- Modify: `docs/web-ui/README.md` (mention `/dev/type` beside `/dev/widgets`)

- [ ] **Step 1: Failing test** — `TypeSpecimen.test.tsx`: renders, shows one row per ramp step (`getByText("caption 12")` … `getByText("display 28")`), shows the four rhythm steps and a paragraph at each of the two measures, both themes via `ThemeFlip`.
- [ ] **Step 2: Run, watch fail.**
- [ ] **Step 3: Implement** — a page listing: every `--font-size-*` token rendered in itself with its name and px; the three line-heights; the eyebrow recipe; `--rhythm-*` as labelled spacer bars; a 600-character paragraph inside a `.turn`-width column at `reading` and `wide`; wrapped in `ThemeFlip`. `App.tsx`: `const TypeSpecimen = import.meta.env.DEV ? lazy(() => import("./dev/TypeSpecimen")) : null;` and the `/dev/type` branch.
- [ ] **Step 4: Run, Biome, commit** — `git commit -m "feat(webui): /dev/type specimen route"`.

---

### Task 9: Enforce it — token-contract extensions

**Files:**
- Modify: `src/styles/token-contract.test.ts`

- [ ] **Step 1: Add four checks with poison tests, in the file's own style (a `violations(cssText)` helper, one `test.each` over `OTHER_STYLESHEETS`, then hand-written snippets proving catch and non-catch):**

1. **Undefined tokens.** Collect every `--name` declared in any stylesheet; for every `var(--name)` with no fallback (`var(--x)` not followed by `,`), fail if not declared. Exempt `--dv-*` (dockview) and the runtime-set names `--keyboard-inset --rail-width --tap-min --fill --markdown-ink --prose-font-size --prose-ink --density-scale --font-scale --textarea-min-lines --sheet-inline-size`. Poison: `var(--radius-sm)` caught; `var(--radius-sm, 4px)` allowed.
2. **No literal font-size.** `font-size:\s*\d` outside `tokens.css` fails. Poison: `font-size: 11px` caught; `font-size: var(--font-size-ui)`, `font-size: 0.86em`, `font-size: inherit` allowed (em is relative and legal for inline code).
3. **Tracking via tokens.** `letter-spacing:` must be `var(--tracking-display)`, `var(--tracking-eyebrow)`, `inherit`, or `normal`.
4. **Uppercase = eyebrow recipe.** Every rule block with `text-transform: uppercase` also declares `font-size: var(--font-size-caption)` and `letter-spacing: var(--tracking-eyebrow)` (this supersedes the interim copy in `rhythm.test.ts`; delete that test there once this one exists).

- [ ] **Step 2: Run** — `npx vitest run src/styles` — expect PASS on the real tree (Tasks 1 and 5 already cleaned it) and the poison snippets.
- [ ] **Step 3: Commit** — `git commit -m "test(webui): contract-test undefined tokens, literal sizes, tracking and eyebrow recipe"`.

---

### Task 10: Docs and the design law

**Files:**
- Modify: `docs/web-ui/design-system.md` §1 Type line, §2 Type / Space & shape paragraphs (ramp 12/13/15/18/22/28, line-heights 1.4/1.6/1.25, `--tracking-eyebrow` replaces `--tracking-micro`, `--rhythm-*`, `--session-measure` + the Transcript width preference, pane titles are headings, micro-label stays for cards), §6 copy rules (eyebrow recipe, ≤2 words, enforced), §4 list item 7–10 (the new contract checks)
- Modify: `docs/web-ui/decisions.md`: a `## 2026-09-06 typography, measure and rhythm` entry recording: the ramp; the 44rem measure and why (149 chars/line measured); the exchange step reversing the 2026-07-30 "no dead air" ruling; agent prose as a document (partially reversing 2026-07-30 bubbles); tool-run folding landing principle 2 / topic 06 Alt A; pane titles as headings (superseding the micro-label port for panes); zoom lock removed.
- Modify: `docs/web-ui/typography-spacing-critique-2026-09-06.md` status line → "implemented by `docs/superpowers/plans/2026-09-06-webui-typography-spacing.md`".

- [ ] **Step 1: Edit the three docs as above.**
- [ ] **Step 2: Commit** — `git commit -m "docs(web-ui): record the 2026-09-06 typography, measure and rhythm decisions"`.

---

### Task 11: Gates, visual check, PR

- [ ] **Step 1:** From the repo root: `make test-web` then `make test-web-browser`. Fix anything red (the guards measure real geometry at 390/700/1024/1400; a tap-target or overflow regression from the larger body size shows up here).
- [ ] **Step 2:** Visual pass with the dev server (see `/Users/jesse/.claude/projects/-Users-jesse-git-prime-radiant-evener/memory/webui-screenshot-recipe.md`): `/new`, a session, `/settings`, `/dev/type` at 1440 and 375, both themes. Measure the agent paragraph again: expect ≤ 100 characters per line at 1440 and ≥ 36 on the phone.
- [ ] **Step 3:** `git push -u origin claude/evener-webui-typography-spacing-3d4fd4` and `gh pr create` with the critique as the description's source: what was measured, what changed (R1–R10 mapped to commits), the two reversed decisions, and how to review (`/dev/type`, Settings → Theme → Transcript width).
