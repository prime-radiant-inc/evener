# Open Affordance Unification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every "open into another surface" affordance in the web UI is the one reusable `OpenButton` icon form — big touch target that never changes row height, placed immediately beside the item it opens, tooltip exactly "Open".

**Architecture:** Converge `widgets/openbutton` on a single button form (icon-only, specific aria-label, `title="Open"`), wrap it in a layout shell whose negative `margin-block` gives the hit area back to the line box (28px desktop / `--tap-min` phones of touch target, 1em of leading), then fix each call site's placement so the control rides immediately after the item's text — never sprung right by `flex:1`/`margin-left:auto`/`justify-content:space-between`, never on the far side of a disclosure chevron.

**Tech Stack:** React 19 + TypeScript, CSS modules, Vitest + Testing Library, Biome, layoutguard (browser geometry guards).

**Spec:** the user request (quoted below) + the inventory in this file. The inventory is the spec: every site it names has a task here.

> "across the web ui, we have a bunch of different treatments of 'open in a side panel' some with text and some with just an icon button. sometimes it's placed in the right rail. sometimes it's close to inline. it also blows out the row height, making rows with and without inconsistent. the touch target should be big, but it shouldn't fuck with the leading. the button should always be right next to the item it's going to open. not in the gutter. not on the far side of a disclosure arrow. it should only show 'Open' in the tooltip. it should use the same reusable open button component everywhere we use it."

## Global Constraints

- All frontend work is under `cmd/evener-hub/frontend/`; all paths below are relative to it unless absolute.
- Before the gate, run `npx biome check --write` on every touched file under `src/` (AGENTS.md). Biome's enforced scope is `src/` only — do NOT run it on `scripts/layoutguard` harness HTML.
- The canonical gates: `make test-web` (unit + typecheck + Biome) and, on this Chrome-capable host, `make test-web-browser`. The layoutguard suite runs via `npm run layoutguard` from `cmd/evener-hub/frontend`.
- Tests must stay deterministic (AGENTS.md): no network, no provider credentials.
- Tooltip text is exactly `Open` (title attribute). Accessible names (aria-label) stay specific — "Open transcript", "Open beside: src/a.ts", "Open subagent" — because many open controls share a screen.
- Touch target: 28×28px desktop (IconButton `sm`), `--tap-min` (44px) inside the existing `@media (max-width: 899px)` phone block. Layout contribution: 1em tall, always.
- Do not change `widgets/iconbutton` or `widgets/button` themselves; the geometry fix lives in `openbutton.module.css`.

## Inventory (the spec's site list)

Verified by read-through of the sources (2026-09-03, base fdc499055f36):

| # | Site | Opens | Current treatment | Current placement | Defect |
|---|------|-------|-------------------|-------------------|--------|
| 1 | `src/panes/session/chrome/ActivityTree.tsx:534` (dense row) | child transcript pane beside | OpenButton iconOnly xs (24px) | after name span; `.denseName { flex: 1 }` pushes it rightward; before meta | sprung away from the item; 24px box inflates the 2px-padded row (44px on phones) |
| 2 | `src/panes/session/chrome/ActivityTranscriptAction.tsx` | — | iconOnly sm, label "Open transcript beside" | **dead code** — no non-test consumer | delete |
| 3 | `src/panes/session/transcript/ToolCallItem.tsx:164-167` → `fileOpenBeside.tsx:75-83` | file in doc pane beside | iconOnly sm (28px), tooltip "Open beside: \<path\>" | read_file: inline mid-summary (good); others: end of demoted summary line | 28px box is the line's tallest item (`toolcallitem.module.css:322-328` documents the workaround); tooltip not "Open" |
| 4 | `src/panes/session/transcript/ToolCallItem.tsx:181-185` (delegate_send/delegate rows; descriptors `jobTools.tsx:336`, `subagentModule.tsx:387-395`) | child transcript pane beside | **word form** ("open ⤢", Button sm fixed 28px, nowrap), no title | intent-less rows: control rides inside `.summaryLine` immediately after the summary text (already adjacent; the disclosure chevron is the overlay trigger at the row's right edge); intent-only rows: `.intentTrailing` sprung to the line's far end by `.row[data-intent-trailing] .trigger { flex: 1 1 0 }` — a column of whitespace between the intent (whose inline chevron ends it) and the control | text treatment; far-end placement on intent-only rows (the "far side of a disclosure arrow" case: the control sits past the intent's chevron, pushed away). Note: `ToolRow.tsx`'s `content` fragment (:420-431) orders trailing after `chevron`, but that fragment only renders in the non-expandable branch where `chevron` is always null — a dead ordering, not the live defect |
| 5 | `src/panes/session/transcript/tools/delegateStatus.tsx:382` (card footer) | delegate transcript pane beside | iconOnly xs | leading item of expanded-card footer | only the geometry/tooltip defaults (gets both free from Task 1) |
| 6 | `src/panes/session/transcript/messages/NotificationCard.tsx:190-194` | subagent/job transcript pane beside | **word form**, aria "Open subagent", no title | `.action { margin-left: auto }` springs it to the far right of the disclosure head | text treatment; far-right placement; 28px Button on a baseline text head |
| 7 | `src/panes/settings/sections/agents.tsx:64` | agent file in external editor | anchor form "open in editor ⤢" (external — stays an anchor) | `agents.module.css .row { justify-content: space-between }` puts it at the row's far right | placement only |
| 8 | `src/dev/gallery-sections/openbutton.tsx` | — | gallery of all four forms | — | must show the converged forms |

Explicitly out of scope (different action or already-correct bespoke control; do not touch):

- `src/shell/PopoutHeaderAction.tsx` — "Pop out" opens a native OS window, not a side panel; it already reuses the `OpenIcon` glyph.
- `src/panes/session/composer/AttachmentTile.tsx` — the whole 80×80 thumbnail opens a modal lightbox `Dialog`, not a side panel.
- `src/panes/session/composer/CurrentWork.tsx:45` — the task text itself is the control (already adjacent to its item, no row-height cost) and it opens the Tasks Sheet. Flag to the user as a possible follow-up, do not change here.
- `src/panes/session/transcript/tools/subagentModule.tsx:271-281` — "Show/Hide recent activity" chevron is a disclosure toggle, not an open-out control.

## The two mechanisms every task relies on

**M1 — Touch target without leading (Task 1).** `OpenButton`'s button form renders a wrapper span around the existing `IconButton size="sm"`:

```
layout box:   ┌─ span.inline ─ height:28px, margin-block:min(0px,(1em-28px)/2) ─┐  → outer height 1em
hit area:     │   <button> 28×28 (44×44 inside the phone media query)          │  → full box, visually centered
              └─────────────────────────────────────────────────────────────────┘
```

The negative `margin-block` is symmetric, so the glyph does not move; the flex/inline line sees a 1em item. `min(0px, …)` means a font larger than 28px never earns a positive margin. On phones the same rule uses `--tap-min` (44px, declared in `styles/tokens.css:365` inside the phone media block) and `iconbutton.module.css`'s existing media rule grows the button to fill.

**M2 — Clip relaxation (Task 4).** The only container that clips the overhang is `toolcallitem.module.css`'s `.clamped` (`overflow: hidden` on the collapsed summary line). It becomes `overflow: clip; overflow-clip-margin: 16px;` — the children (`.clampedHead`/`.clampedTail`) keep their own `overflow:hidden`+ellipsis, so horizontal text clipping is unchanged while the button's hit area paints and hit-tests through the clip margin. VERIFY in Task 6 with `make test-web-browser` that the button's top/bottom overhang is clickable inside a collapsed tool row; if hit-testing through `overflow-clip-margin` fails in Chrome, the fallback is keeping `overflow:hidden` and accepting a 1em-tall hit strip horizontally extended (do not silently ship that fallback — report it).

**M3 — Adjacency without wrap regression (Task 4).** The intent-only row's trigger today is `flex: 1 1 0` — basis 0 so a long intent wraps inside the trigger (the `delegate-open-widget-inline` layoutguard case pins this), but grow 1 springs the control to the line's end. The fix keeps the wrap behavior and drops the spring: `flex: 0 1 auto` plus `max-width: calc(100% - var(--tap-min, 28px) - var(--space-2))`. Flex line-breaking is decided on **hypothetical main sizes — the base size clamped by max-width** — so a long intent's trigger still leaves room for the control on line 1, and a short intent's control sits immediately after the text. `var(--tap-min, 28px)` resolves to 44px on phones, 28px elsewhere.

---

### Task 1: OpenButton converges on one button form

**Files:**
- Modify: `src/widgets/openbutton/index.tsx` (whole file)
- Modify: `src/widgets/openbutton/openbutton.module.css` (whole file)
- Test: `src/widgets/openbutton/openbutton.test.tsx` (rewrite)
- Modify: `src/dev/gallery-sections/openbutton.tsx`

**Interfaces:**
- Produces (consumed by Tasks 2-5):
  ```ts
  export interface OpenButtonProps {
    label?: string;   // accessible name; button form falls back to "Open"
    title?: string;   // tooltip; defaults to "Open"
    onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
    tabIndex?: number;
    href?: string;    // anchor form (external target); ignores onClick
    word?: string;    // anchor form's visible words; default "open"
  }
  export function OpenButton(props: OpenButtonProps): JSX.Element;
  export function OpenIcon({ size }: { size?: number }): JSX.Element; // unchanged
  ```
  GONE: `iconOnly`, `size`, and the visible-word `<Button>` form. TypeScript will flag every missed consumer at typecheck — that is the safety net.
- The CSS class `styles.inline` is the layout shell (M1).

- [ ] **Step 1: Write the failing tests**

Replace `src/widgets/openbutton/openbutton.test.tsx` with:

```tsx
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { OpenButton, OpenIcon } from ".";

afterEach(cleanup);

// The repo's CSS-source test idiom (difftable.test.tsx, select.test.tsx):
// jsdom has no layout, so geometry contracts are pinned by reading the
// stylesheet's own source.
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "openbutton.module.css"), "utf8");

test("the button form is icon-only: named by label, tooltip defaults to the one word 'Open'", () => {
  const onClick = vi.fn();
  const { container } = render(<OpenButton label="Open transcript" onClick={onClick} />);
  const button = screen.getByRole("button", { name: "Open transcript" });
  expect(button.textContent).toBe("");
  expect(button.getAttribute("title")).toBe("Open");
  expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  fireEvent.click(button);
  expect(onClick).toHaveBeenCalledTimes(1);
});

test("the accessible name falls back to 'Open' when no label is given", () => {
  render(<OpenButton onClick={() => {}} />);
  expect(screen.getByRole("button", { name: "Open" })).toBeTruthy();
});

test("a title override wins over the 'Open' default", () => {
  render(<OpenButton label="Open transcript" title="Open the child transcript" onClick={() => {}} />);
  expect(screen.getByRole("button", { name: "Open transcript" }).getAttribute("title")).toBe(
    "Open the child transcript",
  );
});

test("a click never reaches the enclosing row - the affordance rides disclosures", () => {
  const onParentClick = vi.fn();
  render(
    // biome-ignore lint/a11y/useKeyWithClickEvents: a stand-in disclosure row, not the component under test
    // biome-ignore lint/a11y/noStaticElementInteractions: same
    <div onClick={onParentClick}>
      <OpenButton label="Open subagent" onClick={() => {}} />
    </div>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Open subagent" }));
  expect(onParentClick).not.toHaveBeenCalled();
});

test("the button form rides in a 1em layout shell: the hit size never reaches the line box", () => {
  // M1: the wrapper is the full hit size; the negative margin-block hands
  // exactly one text line back to layout, clamped at 0 so a large font never
  // earns a positive margin.
  expect(css).toMatch(/\.inline\s*\{[^}]*height:\s*28px/);
  expect(css).toMatch(/\.inline\s*\{[^}]*margin-block:\s*min\(0px,\s*calc\(\(1em - 28px\) \/ 2\)\)/);
  const media = /@media\s*\(max-width:\s*899px\)\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(media, "openbutton.module.css must have a max-width:899px block").not.toBeNull();
  expect(media![1]).toMatch(/\.inline\s*\{[^}]*height:\s*var\(--tap-min\)/);
  expect(media![1]).toMatch(/margin-block:\s*min\(0px,\s*calc\(\(1em - var\(--tap-min\)\) \/ 2\)\)/);
});

test("the anchor form renders a real link to an external target, glyph following the words", () => {
  const onParentClick = vi.fn();
  const { container } = render(
    // biome-ignore lint/a11y/useKeyWithClickEvents: a stand-in disclosure row, not the component under test
    // biome-ignore lint/a11y/noStaticElementInteractions: same
    <div onClick={onParentClick}>
      <OpenButton href="vscode://file/src/agents/foo.md" word="open in editor" />
    </div>,
  );
  const link = screen.getByRole("link", { name: "open in editor" });
  expect(link.getAttribute("href")).toBe("vscode://file/src/agents/foo.md");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toContain("noopener");
  expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  fireEvent.click(link);
  expect(onParentClick).not.toHaveBeenCalled();
});

test("tabIndex forwards to the underlying control (dense tree rows take -1)", () => {
  render(<OpenButton label="Open transcript" tabIndex={-1} onClick={() => {}} />);
  expect(screen.getByRole("button", { name: "Open transcript" }).tabIndex).toBe(-1);
});

test("OpenIcon renders the box-arrow glyph on the 16px grid", () => {
  const { container } = render(<OpenIcon />);
  const svg = container.querySelector("svg");
  expect(svg?.getAttribute("viewBox")).toBe("0 0 16 16");
  expect(svg?.getAttribute("aria-hidden")).toBe("true");
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/openbutton/openbutton.test.tsx`
Expected: FAIL — "tooltip defaults to the one word 'Open'" gets "Open transcript"; the CSS-shell test finds no `.inline` rule; the no-label fallback finds no button named "Open".

- [ ] **Step 3: Rewrite the component**

Replace `src/widgets/openbutton/index.tsx` with:

```tsx
// The standard "open out of this surface" affordance: the box-arrow OpenIcon
// glyph, alone (the button form) or after words (the anchor form, an external
// target). Every place the UI opens something outside the current surface -
// a child transcript pane, a file doc pane, an external editor - routes
// through this one component, so a rendering change lands once.
//
// ONE treatment: icon-only, everywhere. The accessible name stays specific
// ("Open transcript", "Open beside: src/a.ts") because many open controls
// share a screen; the TOOLTIP is the one word "Open" everywhere.
//
// The touch target never reaches the line box: the .inline wrapper is the
// full hit size (28px, --tap-min on phones - the IconButton sm inside fills
// it) and its negative margin-block hands exactly 1em back to layout, so a
// row with the affordance is the height of a row without it.
//
// The affordance always rides inside something clickable (a disclosure head,
// a tool row's summary line, an activity-tree row), so it owns
// stopPropagation: a click here must never also toggle the enclosing row.
import type { MouseEvent } from "react";
import { IconButton } from "../iconbutton";
import { requireClass } from "../internal/requireClass";
import styles from "./openbutton.module.css";

const CLASS = {
  link: requireClass(styles.link, "openbutton.module.css", "link"),
  inline: requireClass(styles.inline, "openbutton.module.css", "inline"),
};

// The traditional "open out of the box" glyph - a box with its top-right
// corner open and an arrow leaving through it - in the app's 16x16 stroke
// grammar, currentColor so it inherits the Button/IconButton variant colour.
// Defaults to this control's 14px; the pane header's pop-out action
// (shell/PopoutHeaderAction.tsx) renders it at 16px.
export function OpenIcon({ size = 14 }: { size?: number }) {
  return (
    <svg viewBox="0 0 16 16" width={size} height={size} aria-hidden="true">
      <path
        d="M12.5 8.5V12.5H3.5V3.5H7.5"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
      <path
        d="M8 8L13 3M9.5 3H13V6.5"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

export interface OpenButtonProps {
  /** Accessible name. Stay specific ("Open transcript", "Open beside:
   * src/a.ts") - many open controls share a screen. Defaults to "Open". */
  label?: string;
  /** Hover text; the one word every open affordance shows. */
  title?: string;
  /** Click handler for the button form. An href anchor navigates instead
   * and only stopPropagates. */
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  /** Forwards to the underlying control: activity-tree rows are their own
   * tab stop, so their nested open glyph takes -1 there. */
  tabIndex?: number;
  /** An external target renders an <a> (new tab, no opener access) instead
   * of a <button> - the settings "open in editor" case. The anchor names
   * itself from its visible words and ignores onClick and title. */
  href?: string;
  /** The visible words the glyph follows (anchor form only). */
  word?: string;
}

export function OpenButton({ label, title = "Open", onClick, tabIndex, href, word = "open" }: OpenButtonProps) {
  if (href !== undefined) {
    return (
      <a
        className={CLASS.link}
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        aria-label={label}
        tabIndex={tabIndex}
        onClick={(event) => event.stopPropagation()}
      >
        {word}
        <OpenIcon />
      </a>
    );
  }
  function handleClick(event: MouseEvent<HTMLButtonElement>) {
    event.stopPropagation();
    onClick?.(event);
  }
  return (
    <span className={CLASS.inline}>
      <IconButton
        label={label ?? "Open"}
        title={title}
        tabIndex={tabIndex}
        icon={<OpenIcon />}
        variant="quiet"
        size="sm"
        onClick={handleClick}
      />
    </span>
  );
}
```

Replace `src/widgets/openbutton/openbutton.module.css` with:

```css
/* The anchor form of the open affordance (an external target, e.g. settings'
 * "open in editor"): an accent text link with the glyph riding after the
 * words, one inline-flex row so the 14px icon centers on the caption line. */
.link {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  color: var(--accent);
  font-size: var(--font-size-caption);
}

.link:focus-visible {
  outline: var(--focus-ring);
  outline-offset: 2px;
  border-radius: var(--radius-control);
}

/* The button form's layout shell: the touch target WITHOUT the leading. The
 * box is the full hit size (28px - IconButton sm fills it) and the negative
 * margin-block hands exactly one text line back to layout, so a row with the
 * affordance is the height of a row without it. The margin is symmetric, so
 * the glyph does not move; min(0px, ...) means a font taller than the hit
 * size never earns a positive margin. The one container that would clip the
 * overhang - toolcallitem.module.css's .clamped - relaxes its clip with
 * overflow-clip-margin (see there). */
.inline {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  height: 28px;
  margin-block: min(0px, calc((1em - 28px) / 2));
  vertical-align: middle;
}

/* Phone tap floor (tokens.css's --tap-min, decision 4 of
 * 2026-07-30-mobile-session-layout-design.md): iconbutton.module.css's own
 * phone block grows the button to fill, this shell grows with it, and the
 * leading is still exactly one line. */
@media (max-width: 899px) {
  .inline {
    height: var(--tap-min);
    margin-block: min(0px, calc((1em - var(--tap-min)) / 2));
  }
}
```

- [ ] **Step 4: Update the gallery fixture**

Replace the `<ThemeFlip>` contents of `src/dev/gallery-sections/openbutton.tsx` (and the note) with:

```tsx
      <p className={styles.note}>
        The one open-out affordance (transcript panes, file docs): icon-only, specific accessible name, tooltip
        "Open", a 28px (phone: tap-min) touch target that never reaches the line box. The anchor form is reserved
        for external targets.
      </p>
      <ThemeFlip>
        <div className={styles.row}>
          <p className={styles.rowLabel}>button</p>
          <OpenButton label="Open transcript" onClick={() => {}} />
          <OpenButton label="Open subagent" onClick={() => {}} />
          <OpenButton label="Open beside: src/sheet.test.tsx" onClick={() => {}} />
        </div>
        <div className={styles.row}>
          <p className={styles.rowLabel}>anchor</p>
          <OpenButton href="editor://open?path=/plugins/reviewer.md" word="open in editor" />
        </div>
      </ThemeFlip>
```

- [ ] **Step 5: Run the tests**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/openbutton/openbutton.test.tsx`
Expected: PASS (8 tests). The typecheck will fail on `iconOnly`/`size` consumers until Task 2 — that is expected; do not "fix" it by keeping the old props.

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/src/widgets/openbutton cmd/evener-hub/frontend/src/dev/gallery-sections/openbutton.tsx
git commit -m "refactor(web): converge OpenButton on one icon form with a leading-free touch target"
```

---

### Task 2: Every consumer takes the one form; dead wrapper deleted

**Files:**
- Modify: `src/panes/session/transcript/openTranscript.tsx:52-74`
- Test: `src/panes/session/transcript/openTranscript.test.tsx:91-108`
- Modify: `src/panes/session/transcript/fileOpenBeside.tsx:75-83`
- Test: `src/panes/session/transcript/fileOpenBeside.test.tsx:83-104`
- Modify: `src/panes/session/transcript/messages/NotificationCard.tsx:190-194`
- Modify: `src/panes/session/transcript/messages/notificationcard.module.css:39-42`
- Modify: `src/panes/session/transcript/tools/delegateStatus.tsx:382`
- Modify: `src/panes/session/chrome/ActivityTree.tsx:534` and comments at `:105-109`
- Delete: `src/panes/session/chrome/ActivityTranscriptAction.tsx`
- Delete: `src/panes/session/chrome/ActivityTranscriptAction.test.tsx`

**Interfaces:**
- Consumes: `OpenButtonProps` from Task 1.
- Produces: `OpenTranscriptButton({ transcriptRef, parentRef?, label?, tabIndex? })` — the `iconOnly` prop is gone; every render is the icon form. All call sites keep working by role-name queries (aria-labels unchanged).

- [ ] **Step 1: Write the failing tests**

In `src/panes/session/transcript/openTranscript.test.tsx`, replace the two tests at lines 91-108 with:

```tsx
test("OpenTranscriptButton renders the glyph with no visible label, tooltip 'Open', and opens on click", () => {
  render(<OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" />);

  const button = screen.getByRole("button", { name: "Open transcript" });
  // Icon-only: the accessible name comes from aria-label, not visible text.
  expect(button.textContent).toBe("");
  expect(button.getAttribute("title")).toBe("Open");

  fireEvent.click(button);
  expect(transcriptPanes("local:child")).toHaveLength(1);
  expect(transcriptPanes("local:child")[0]?.params).toEqual({ ref: "local:child", parentRef: "local:owner" });
});
```

In `src/panes/session/transcript/fileOpenBeside.test.tsx`, find the test asserting the tooltip equals the full label (the `title` assertion around lines 83-104) and change the expectation to:

```tsx
  // The tooltip is the one word everywhere; the PATH stays in the aria-label.
  expect(button.getAttribute("title")).toBe("Open");
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/openTranscript.test.tsx src/panes/session/transcript/fileOpenBeside.test.tsx`
Expected: FAIL — `openTranscript.test.tsx` no longer compiles against the old assertions (visible "open" text gone) and `fileOpenBeside` still titles the full label.

- [ ] **Step 3: Update the consumers**

`src/panes/session/transcript/openTranscript.tsx` — replace `OpenTranscriptButton` (lines 52-74) with:

```tsx
export function OpenTranscriptButton({
  transcriptRef,
  parentRef,
  label = "Open transcript",
  tabIndex,
}: {
  transcriptRef: string;
  parentRef?: string;
  label?: string;
  tabIndex?: number;
}) {
  return <OpenButton label={label} tabIndex={tabIndex} onClick={() => openTranscript(transcriptRef, parentRef)} />;
}
```

`src/panes/session/transcript/fileOpenBeside.tsx` — lines 75-83 become (the aria-label keeps the path; the comment above it stays true):

```tsx
  const name = `Open beside: ${docParams.path}`;
  return <OpenButton label={name} onClick={() => paneActions.openBeside({ type: "doc", params: docParams })} />;
```

`src/panes/session/transcript/messages/NotificationCard.tsx` — lines 190-194 become (no `.action` wrapper; the control rides immediately after the secondary text, adjacent to the item it opens):

```tsx
        {transcriptRef && (
          <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} label="Open subagent" />
        )}
```

Also in `NotificationCard.tsx`, delete the `action` entry from the `CLASS` map (line 47: `action: requireClass(styles.action, "notificationcard.module.css", "action"),`) — `requireClass` throws at module load if the CSS rule is gone but the entry stays.

`src/panes/session/transcript/messages/notificationcard.module.css` — delete the now-unused rule:

```css
.action {
  margin-left: auto;
  display: inline-flex;
}
```

`src/panes/session/transcript/tools/delegateStatus.tsx:382` — drop the `iconOnly` prop:

```tsx
        {transcriptRef && <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} />}
```

`src/panes/session/chrome/ActivityTree.tsx:534` — drop the `iconOnly` prop:

```tsx
          {target && <OpenTranscriptButton transcriptRef={target} parentRef={row.parentRef} tabIndex={-1} />}
```

`src/panes/session/chrome/ActivityTree.tsx:105-109` — the comment references the dead component; replace it with:

```tsx
// transcriptTarget mirrors OpenTranscriptButton's own gate: the ref is
// trimmed and a row with no ref gets no transcript action at all. There is
// deliberately no `job:<id>` fallback - the backend populates
// transcriptRef.
```

Delete the dead wrapper and its test:

```bash
git rm cmd/evener-hub/frontend/src/panes/session/chrome/ActivityTranscriptAction.tsx \
       cmd/evener-hub/frontend/src/panes/session/chrome/ActivityTranscriptAction.test.tsx
```

(Before deleting, confirm deadness yourself: `rg -l "ActivityTranscriptAction" cmd/evener-hub/frontend/src` must list only these two files plus the comment you just rewrote.)

- [ ] **Step 4: Run the affected test files**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/openTranscript.test.tsx src/panes/session/transcript/fileOpenBeside.test.tsx src/panes/session/transcript/messages/NotificationCard.test.tsx src/panes/session/transcript/messages/SteeringItem.test.tsx src/panes/session/transcript/tools/subagentModule.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/transcript/TranscriptBody.test.tsx`
Expected: PASS. Every query is by accessible name ("Open transcript", "Open subagent", /open beside/i), which Task 1 kept; only visible-text and title assertions needed updating.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes
git commit -m "refactor(web): route every transcript/doc open affordance through the one icon form"
```

---

### Task 3: Activity tree — the control hugs the name

**Files:**
- Modify: `src/panes/session/chrome/activitypanel.module.css` (`.denseName` at :77-83, `.denseMeta` at :107-113)
- Test: `src/panes/session/chrome/ActivityTree.test.tsx`

**Interfaces:**
- Consumes: Task 2's prop-less `OpenTranscriptButton` call at `ActivityTree.tsx:534` (already immediately after the name span in JSX — this task removes the CSS that pushed it away).

- [ ] **Step 1: Write the failing test**

Add to `src/panes/session/chrome/ActivityTree.test.tsx` (match the file's existing render idiom — its tests at :219/:383/:400 already render rows with transcript refs and query `screen.getByRole("button", { name: "Open transcript" })`):

```tsx
test("the open control's previous sibling is the row's name span - nothing springs it away", () => {
  // render a delegate/job row with a transcriptRef the way the file's
  // existing "Open transcript" tests do, then:
  const button = screen.getByRole("button", { name: "Open transcript" });
  const nameSpan = button.parentElement?.previousElementSibling;
  expect(nameSpan?.textContent).toBe(/* the rendered row's name, e.g. */ "task-name");
});
```

Also add a CSS-source pin (same `readFileSync` idiom as Task 1 — the test file lives in the same directory as `activitypanel.module.css`):

```tsx
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "activitypanel.module.css"), "utf8");

test("dense rows never grow the name over the open control: meta owns the right edge", () => {
  expect(css).toMatch(/\.denseName\s*\{[^}]*flex:\s*0 1 auto/);
  expect(css).toMatch(/\.denseMeta\s*\{[^}]*margin-left:\s*auto/);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/ActivityTree.test.tsx`
Expected: the CSS pin FAILs (`.denseName` is `flex: 1`, `.denseMeta` has no auto margin).

- [ ] **Step 3: Make the CSS change**

In `src/panes/session/chrome/activitypanel.module.css`, change `.denseName` and `.denseMeta`:

```css
.denseName {
  /* flex: 0 1 auto, NOT flex: 1: the name still shrinks and ellipsizes under
   * pressure, but it no longer GROWS - growing pushed the row's open
   * affordance a column of whitespace away from the name it opens. The meta
   * segments own the right edge via their own margin-left:auto. */
  flex: 0 1 auto;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
```

```css
.denseMeta {
  flex: none;
  /* The right edge is the meta's, so the open control stays beside the name. */
  margin-left: auto;
  font-size: var(--font-size-caption);
  color: var(--ink-low);
}
```

(Preserve any declarations already present on `.denseMeta` — the listing showed `flex: none; font-size; color`; add only the margin and the comment.)

- [ ] **Step 4: Run the tests**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/chrome/ActivityPanel.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/chrome
git commit -m "fix(web): keep the activity row's open control beside the row's name"
```

---

### Task 4: Tool rows — adjacency without the wrap regression, and a clip that keeps the hit area

**Files:**
- Modify: `src/panes/session/transcript/toolcallitem.module.css` (`.clamped` :320-332, `[data-intent-trailing]` rule :67-84)
- Modify: `src/panes/session/transcript/ToolRow.tsx` (grammar comment :46-57 only — no JSX change)
- Test: `src/panes/session/transcript/toolRowGrammar.test.tsx`

**Interfaces:**
- Consumes: Task 1's `OpenButton` (the trailing nodes passed into `ToolRow` are unchanged — only the CSS around them changes).
- Produces: the placement contract Task 6's layoutguard update measures: on an intent-only row the control shares the trigger's first line and sits within one column-gap of the trigger box.

**Why no ToolRow JSX change:** verified against the live render paths — `ToolRow.tsx`'s `content` fragment orders trailing after `chevron` (:420-431), but that fragment only renders in the non-expandable branch (:435-441) where `chevron` is always null (:285), so the ordering is dead. The live intent-less path (`summaryContent`, :443-477) already renders the control inside `.summaryLine` immediately after the summary text; the disclosure chevron there is the absolutely-positioned overlay trigger at the row's right edge, not an inline item the control follows. The one live placement defect in this surface is the intent-only row's `.intentTrailing` control sprung to the line's far end by the trigger's `flex: 1 1 0` — pure CSS — plus the `.clamped` clip that would amputate the new hit area.

- [ ] **Step 1: Write the failing tests**

In `src/panes/session/transcript/toolRowGrammar.test.tsx`, add CSS-source pins (the `readFileSync` idiom from Task 1 — the test file and `toolcallitem.module.css` share a directory):

```tsx
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "toolcallitem.module.css"), "utf8");

test("the intent-trailing trigger reserves the control's width instead of growing past it", () => {
  expect(css).toMatch(
    /\.row\[data-intent-trailing="true"\]\s+\.trigger\s*\{[^}]*flex:\s*0 1 auto/,
  );
  expect(css).toMatch(
    /\.row\[data-intent-trailing="true"\]\s+\.trigger\s*\{[^}]*max-width:\s*calc\(100% - var\(--tap-min, 28px\) - var\(--space-2\)\)/,
  );
});

test("the collapsed summary line clips with a margin, so the open control's hit area survives", () => {
  expect(css).toMatch(/\.clamped\s*\{[^}]*overflow:\s*clip/);
  expect(css).toMatch(/\.clamped\s*\{[^}]*overflow-clip-margin:\s*16px/);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/toolRowGrammar.test.tsx`
Expected: FAIL — today the trigger rule is `flex: 1 1 0` with no max-width, and `.clamped` is `overflow: hidden`.

- [ ] **Step 3: Make the CSS changes**

In `src/panes/session/transcript/toolcallitem.module.css`, replace the `.row[data-intent-trailing="true"] .trigger` rule (lines 67-84) with:

```css
/* data-intent-trailing marks an intent-only row whose trailing affordance
 * rides the disclosure line (ToolRow.tsx's grammar). flex: 0 1 auto, NOT
 * flex: 1 1 0: the control belongs immediately AFTER the intent text, and a
 * growing trigger springs it to the line's far end - the defect this rule
 * used to have. The max-width reserves the control's width plus the row's
 * column-gap: flex line-breaking is decided on hypothetical main sizes (the
 * base size CLAMPED by max-width), so a long intent still wraps INSIDE the
 * trigger and the control still shares line 1 (the layoutguard
 * delegate-open-widget-inline case pins exactly this). */
.row[data-intent-trailing="true"] .trigger {
  flex: 0 1 auto;
  min-width: 0;
  max-width: calc(100% - var(--tap-min, 28px) - var(--space-2));
}
```

Replace the `.clamped` rule (lines 320-332) with:

```css
.clamped {
  display: flex;
  /* Center, not the default stretch: keeps the text spans and the trailing
   * affordance on one axis when their heights differ. */
  align-items: center;
  /* clip with a margin, NOT hidden: the open affordance's touch target
   * (openbutton.module.css's .inline) overhangs the line box by design, and
   * a hard clip would amputate its hit area. The head/tail spans keep their
   * own overflow:hidden + ellipsis, so text clipping is unchanged. */
  overflow: clip;
  overflow-clip-margin: 16px;
  white-space: nowrap;
}
```

- [ ] **Step 4: Update the grammar comment**

In `src/panes/session/transcript/ToolRow.tsx`'s header grammar (lines 46-57), replace the affordance paragraph — "affordances are trailing controls ... a sibling flex item AFTER the trigger ..., sprung to the line's end by the trigger's own flex-grow, the same placement the notification card's head gives 'Open subagent' ..." — with the new rule, in the file's own voice:

```
//   - affordances are trailing controls (the open affordance) and they ride
//     immediately AFTER the text they open: inline at the end of the summary
//     when there is one, which - with an intent present - is the demoted
//     second line, not the rationale line. An intent-only row (no summary)
//     trails the control on the intent line instead, the one line it has: a
//     sibling flex item directly AFTER the trigger (never nested inside it -
//     a button inside a button is not valid), kept adjacent by the
//     [data-intent-trailing] trigger's flex:0 1 auto + max-width reservation
//     (toolcallitem.module.css) - never sprung to the line's far end. The one
//     exception: a descriptor whose summary quotes its target verbatim
//     (read_file's openBesideInline) anchors the control mid-summary via
//     trailingAfter - between the file name and the line range it opens.
```

- [ ] **Step 5: Run the tests**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/toolRowGrammar.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx`
Expected: PASS. Then run the layoutguard case that pins the wrap regression:

Run: `cd cmd/evener-hub/frontend && npm run layoutguard -- delegate-open-widget-inline`
Expected: PASS — the control still shares line 1 on the long-intent fixtures. (If the runner's flag syntax differs, read `scripts/layoutguard/run.mjs` for how to select one case.)

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/transcript
git commit -m "fix(web): keep tool-row open controls beside their text without the wrap regression"
```

---

### Task 5: Settings agents — the anchor hugs the agent name

**Files:**
- Modify: `src/panes/settings/sections/agents.module.css` (`.row` :29-37, `.builtin` :44-47)
- Test: `src/panes/settings/sections/agents.test.tsx`

**Interfaces:**
- Consumes: Task 1's anchor form (unchanged visually). This task is placement only.

- [ ] **Step 1: Write the failing test**

Add to `src/panes/settings/sections/agents.test.tsx` (CSS-source pin, the `readFileSync` idiom from Task 1 — the test file and `agents.module.css` share a directory):

```tsx
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "agents.module.css"), "utf8");

test("the open-in-editor anchor rides beside the agent name, not the row's far edge", () => {
  expect(css).not.toMatch(/\.row\s*\{[^}]*justify-content:\s*space-between/);
  expect(css).toMatch(/\.builtin\s*\{[^}]*margin-left:\s*auto/);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/agents.test.tsx`
Expected: FAIL — `.row` still has `justify-content: space-between`.

- [ ] **Step 3: Make the CSS change**

```css
.row {
  display: flex;
  align-items: center;
  /* No space-between: the open anchor belongs beside the agent name it
   * opens; only the "built-in" status keeps the far edge. */
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--edge);
  border-radius: var(--radius-control);
}
```

```css
.builtin {
  margin-left: auto;
  color: var(--ink-low);
  font-size: var(--font-size-caption);
}
```

- [ ] **Step 4: Run the tests**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/agents.test.tsx`
Expected: PASS (including the existing noopener/noreferrer anchor test).

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/settings/sections/agents.module.css cmd/evener-hub/frontend/src/panes/settings/sections/agents.test.tsx
git commit -m "fix(web): place the agents open-in-editor anchor beside the agent name"
```

---

### Task 6: Layoutguard contract update + full gates

**Files:**
- Modify: `scripts/layoutguard/cases/delegate-open-widget-inline/case.json`
- Modify: `scripts/layoutguard/cases/delegate-open-widget-inline/assert.mjs` (add adjacency measurement)
- Possibly: `scripts/layoutguard/cases/delegate-open-widget-inline/harness.html` (expose the measurement)

**Interfaces:**
- Consumes: Task 4's placement contract.

- [ ] **Step 1: Update the case metadata**

Rewrite `case.json`'s `description` and `mutation` for the new mechanism:

```json
{
  "name": "delegate-open-widget-inline",
  "description": "An intent-only tool row (the delegate/subagent card) keeps its 'open' control on the SAME line as the disclosure trigger AND immediately beside the intent text: the trigger is flex:0 1 auto with a max-width reserving the control's width plus the column-gap, so a long intent wraps INSIDE the trigger (hypothetical main sizes are clamped by max-width before line-breaking) while a short intent leaves the control hugging its end. Removing the max-width reservation lets a long intent claim the full line and wraps the control to line 2; restoring flex-grow springs the control to the line's far end, away from the item it opens.",
  "cssFiles": ["styles/global.css", "styles/tokens.css", "panes/session/transcript/toolcallitem.module.css", "widgets/button/button.module.css"],
  "viewport": { "width": 1200, "height": 900, "deviceScaleFactor": 1, "mobile": false },
  "mutation": {
    "declaration": "toolcallitem.module.css: .row[data-intent-trailing=\"true\"] .trigger max-width reservation removed (plain flex:0 1 auto)",
    "verified": "2026-09-03",
    "expect": "fail",
    "note": "Without the clamp the long-intent fixtures' base size is the text's max-content, the control wraps to its own line, and sameLine fails - the same regression the old flex:1 1 0 rule fixed, now guarded against the new mechanism."
  }
}
```

- [ ] **Step 2: Extend the assertion with adjacency**

In `assert.mjs`, keep the `sameLine` invariant and add: for fixtures labeled short-intent, the control's left edge must sit within `(column-gap + 4px)` of the trigger's line-1 right edge (the harness already computes line boxes via `Range.getClientRects()`; extend its per-fixture measurement to return `controlLeftGap = controlRect.left - line1Rect.right` and fail when it exceeds the gap tolerance or is negative beyond the control's own width — i.e. the control drifted away from the text). Read the harness first and wire the measurement through exactly the way `sameLine`/`dropBelowLine1` already flow.

- [ ] **Step 3: Run the guard**

Run: `cd cmd/evener-hub/frontend && npm run layoutguard`
Expected: PASS for every case, including the updated one. (This suite is outside Biome's `src/` scope — do not reformat its harness HTML to satisfy an explicit-path Biome run; see AGENTS.md.)

- [ ] **Step 4: Biome the touched source**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/widgets/openbutton src/dev/gallery-sections/openbutton.tsx src/panes/session/transcript src/panes/session/chrome src/panes/settings/sections`
Expected: no diff remains (or only formatting fixes it applies itself).

- [ ] **Step 5: Full frontend gates**

Run: `make test-web`
Expected: `PASS web-typecheck`, `PASS web-test`, `PASS web-lint`.

Run: `make test-web-browser`
Expected: PASS — this is where the M2 clip-margin hit-area question (see "The two mechanisms") gets its real-browser answer. Verify specifically that a collapsed tool row's open control is clickable across its full 28px box, and that an activity-tree dense row with the control is the same pixel height as one without.

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/scripts/layoutguard/cases/delegate-open-widget-inline
git commit -m "test(web): pin the open control's same-line adjacency in layoutguard"
```

---

## Self-review notes (completed against the inventory)

- **Spec coverage:** inventory rows 1-8 map to Tasks 3, 2, 4, 4, 2, 2, 5, 1 respectively. Out-of-scope sites are named with reasons. The user's four rules each have an enforcement point: one component (Tasks 1-2), big touch target without leading (Task 1 M1 + Task 4 M2 + Task 6 browser check), adjacency/not-gutter/not-far-side-of-arrow (Tasks 3, 4, 5), tooltip "Open" (Task 1, pinned by tests in Tasks 1-2).
- **Type consistency:** `OpenButtonProps` (Task 1) is exactly what Tasks 2-5 consume; `OpenTranscriptButton`'s surviving props (`transcriptRef`, `parentRef`, `label`, `tabIndex`) match every call site listed in the inventory (`ActivityTree.tsx:534`, `delegateStatus.tsx:382`, `NotificationCard.tsx:192`, `ToolCallItem.tsx:184`).
- **Known risk, stated honestly:** M2 relies on pointer events surviving inside `overflow-clip-margin`; Task 6 Step 5 is the decisive check and names the fallback. M3 relies on max-width clamping hypothetical main sizes (css-flexbox §9.3); the layoutguard case is the decisive check and its mutation block pins the regression both ways.
