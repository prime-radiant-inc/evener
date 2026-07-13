# Mobile spawn form — design spec

Status: **approved** (Treatment A with auto-expanding textarea).  
Scope: the `/new` session creation form in `cmd/serf-hub`.  
Date: 2026-07-12.  
Companion: [`docs/web-ui/mockups/22-mobile-spawn-treatments.html`](../../web-ui/mockups/22-mobile-spawn-treatments.html).

---

## Problem

The mobile spawn form is a shrunken desktop layout, not a touch-native surface. Config chips sit above the prompt, their labels are 10–11px monospace, the `advanced` toggle uses the retired ALL-CAPS-mono style, and the attach/spawn labels are hard to read at arm’s length. The result is hard to scan, hard to tap, and out of step with the design system.

## Selected direction

**Treatment A — Tuned Single Screen.** Keep the form on one page, fix the hierarchy, make every config option a full-width tappable row, and pin the primary action to the bottom of the screen. The prompt textarea auto-expands as the user types so it never fights the viewport.

## Why this direction

- One screen is fastest for power users who already know the model, project, and branch defaults.
- Settings-style rows are a familiar mobile pattern and leave room for large, readable labels.
- A fixed bottom action keeps the spawn button within thumb reach on tall phones.
- Auto-expanding textarea keeps the growing prompt visible without a manual resize handle or a giant empty field.

## Detailed rules

### 1. Page structure (top to bottom)

1. **Header:** identity only — title `new` and the `Details` overflow. No model chip here.
2. **Prompt section:** heading `What should the agent do?`, subtitle `Leave blank to start a dormant session.`, auto-expanding textarea.
3. **Config rows:** full-width rows, in the order **Harness**, **Model**, **Working directory**, **Branch**, **Reasoning effort**, **Access mode**. Display the current value as the row’s right-hand text. (The working-directory row may be labeled “Project” if that matches the rest of the hub.)
4. **Advanced options:** a quiet text link `Advanced options` below the config rows. No ALL-CAPS, no `<details>` summary, no monospace.
5. **Recent prompts:** if present, full-width rows below the form, two-line clamp, sans labels.
6. **Bottom action band:** attach button left, `Spawn` primary button right.

### 2. Mobile form rows

- **Min-height:** `48px` (≥ `--tap-min` 44px plus visual padding).
- **Label:** sans, sentence case, `--text-base` minimum, left-aligned.
- **Value:** sans, `--text-base` or `--text-md`, right-aligned, truncated with ellipsis.
- **Caret:** at the far right; small, but the whole row is the hit target.
- **Separator:** 1px `--line` hairline between rows. No boxed containers around individual rows.
- **No** ALL-CAPS, monospace, letter-spaced labels, or tinted chip backgrounds for settings rows.
- Tapping anywhere on the row opens the picker.

### 3. Auto-expanding textarea

- **Min-height:** `96px` (about 4 lines).
- **Max-height:** `40vh` or `8` lines, whichever is smaller.
- **Font-size:** `16px` so iOS never auto-zooms on focus.
- **Behavior:** grows and shrinks with content via JS; no manual resize handle on mobile.
- **Background:** `--surface` or `--bg-raised`, with a 1px `--line` border and `--accent` focus border.
- Respect `prefers-reduced-motion` by animating height only when reduced motion is off.

### 4. Bottom action band

- **Position:** fixed to the bottom of the viewport, above `env(safe-area-inset-bottom)`.
- **Height:** primary button `52px` tall; secondary attach button at least `44px`.
- **Background:** `--bg-raised`, with a 1px `--line` top border.
- **Primary action:** `Spawn`, accent background, high contrast label.
- **Attach button:** real `<button>`, icon or icon + label, at least `44px` tall. No tiny keyboard-hint labels on mobile.
- The band does not float with a shadow; it is a surface, not an overlay.

### 5. Pickers

- On mobile, chip pickers open as **bottom sheets** (the existing pattern).
- Sheet rows are `48px` minimum, sans labels, value left, selection state right.
- Group headings inside the sheet are `--text-xs`, sans, `--text-muted` — not ALL-CAPS mono.

### 6. Advanced options

- Use a plain text link `Advanced options` rather than the existing `<details>` summary.
- When expanded, render per-launch overrides as the same `settings-table` style used elsewhere in the hub (`design-system.md` §6).

### 7. Recent prompts

- Each row is at least `44px` tall.
- Text is sans, `--text-base`, two-line clamp.
- No leading icon, no metadata, no border box — just a quiet row that fills on tap.

## Style guide updates

Add a new section to `docs/web-ui/design-system.md` (§11) covering mobile form rows, auto-expanding text inputs, the bottom action band, and mobile picker sheets. Clarify §8 Mobile that editable fields remain at least `16px` and touch targets at least `44px`.

## Non-goals

- Do not redesign the empty/cold-start session state.
- Do not change the live-session composer.
- Do not change the model, harness, or launch-config API.
- Do not add new color or type tokens; reuse the existing system.

## Implementation notes

- Update `cmd/serf-hub/templates/partials/spawn.html` to use row markup instead of the chip pile.
- Update `cmd/serf-hub/assets/style.css` mobile rules (`≤767px`) for `.spawn-form`, `.spawn-row`, `.spawn-actions`, and `.spawn-input`.
- Add a small JS behavior in `cmd/serf-hub/assets/spawn.js` for auto-expanding textarea.
- Keep the existing desktop chip layout unchanged; this spec targets mobile only.
- Add or update `jstest/test-spawn.js` to assert row heights, label casing, and textarea expansion.
