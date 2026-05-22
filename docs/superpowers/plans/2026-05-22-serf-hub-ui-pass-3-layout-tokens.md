# Serf Hub UI Pass 3 — Spacing, Radius, Motion, Z-Index Migration Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every padding/margin/gap/border-radius/transition/animation/z-index literal in `cmd/serf-hub/assets/style.css` with the design-language tokens (`--space-*`, `--radius-*`, `--motion-*`, `--z-*`) added in Pass 1, and add a `prefers-reduced-motion` override — without shifting a pixel.

**Architecture:** Pass 1 already introduced the layout/motion/z tokens on `:root`; Pass 2 migrated typography. This pass is a mechanical-but-careful find-and-replace at the value layer. Group rules by surface so each commit produces a coherent, reviewable diff and any visual regression points at a small blast radius. The `var(--pad)` alias is collapsed in a single dedicated task so cross-rule spacing stays consistent. After all literals are migrated and the `prefers-reduced-motion` rule is added, a final verification pass diff-checks the rendered app against the pre-merge snapshot in both themes and exercises the reduced-motion override.

**Tech Stack:** CSS custom properties (already declared in `:root`), Go web server (`cmd/serf-hub`), htmx-served partials. No JS changes. No template changes.

---

## Value-Mapping Reference

Engineers SHOULD keep this table open while migrating. Tokens come from the design language §1.3–§1.6.

### Spacing (`--space-*`, all `px`)

| Literal | Token | Notes |
| --- | --- | --- |
| `0` | `--space-0` (or stay literal) | Use the token only where 0 is intentional rhythm; raw `0` in shorthand like `padding: 0` stays literal. |
| `1px` | stays literal | Hairlines (`border: 1px solid …`), 1px offsets — scale starts at 2. |
| `2px` | `--space-1` | tight badge inset |
| `3px` | `--space-2` (4) | round up; visually indistinguishable on chips/gaps |
| `4px` | `--space-2` | inline gap, small padding, chip gutter |
| `5px` | `--space-2` (4) or `--space-3` (8) | pick by intent — sidebar row vertical, tab-row padding → `--space-2`; visual gap between siblings → `--space-3` |
| `6px` | `--space-3` (8) or `--space-2` (4) | pick by intent — gaps between sibling controls → `--space-3`; tight chip insets → `--space-2` |
| `7px` | `--space-3` (8) | round to closest |
| `8px` | `--space-3` | default sibling gap |
| `9px` | `--space-3` (8) | round to closest |
| `10px` | `--space-4` (12) | per spec rule |
| `11px` | `--space-4` (12) | round to closest |
| `12px` | `--space-4` | panel padding, settings-row vertical pad |
| `14px` | `--space-4` (12) or `--space-5` (16) | pick by intent — workspace meta gap, message pill horizontal → `--space-4`; section gap, dialog header → `--space-5` |
| `16px` | `--space-5` | section padding, large gap |
| `18px` | `--space-5` (16) or `--space-6` (24) | pick by intent — spawn input padding → `--space-5`; banner left-margin → `--space-6` |
| `20px` | `--space-6` (24) | round to closest |
| `22px` | `--space-6` (24) | round to closest |
| `24px` | `--space-6` | major section spacing, header padding |
| `28px` | `--space-7` (32) | round to closest |
| `32px` | `--space-7` | conversation horizontal pad, empty padding |
| `36px` | `--space-8` (48) — or stay literal | tool-cluster indent stays literal until Pass 6 (`--tool-indent`); other 36px values → `--space-8` |
| `48px` | `--space-8` | spawn form padding, wide gutter |
| `56px` | `--space-8` (48) — or stay literal | settings-content `56px` horizontal is an exception; round to `--space-8` (48) |
| `64px` | `--space-9` | wide-screen conversation gutter |

For shorthand values like `padding: 8px 12px`, replace each token in order: `padding: var(--space-3) var(--space-4)`.

`var(--pad)` is the legacy 12px alias. **Task 1 replaces every occurrence with `var(--space-4)` in one shot** — this is a separate, dedicated task to eliminate the cross-rule mismatch the spec calls out.

Negative offsets: use `calc(0px - var(--space-N))` (rare in this file; only `margin-left: -20px` on `.tool-status` qualifies → `calc(0px - var(--space-6))`).

### Radius (`--radius-*`, all `px`)

| Literal | Token |
| --- | --- |
| `2px` | `--radius-sm` (3) |
| `3px` | `--radius-sm` |
| `4px` | `--radius-md` (4) — default |
| `6px` | `--radius-lg` (6) |
| `8px` | `--radius-xl` (8) |
| `10px` | `--radius-pill` (14) |
| `12px` | `--radius-pill` |
| `14px` | `--radius-pill` |
| `50%` | `--radius-full` |

### Motion (`--motion-*`, semantic timing)

| Literal | Token |
| --- | --- |
| `0.1s`, `100ms` | `--motion-fast` (100ms ease) |
| `0.15s`, `150ms` | `--motion-fast` (round down) |
| `0.2s`, `200ms` | `--motion-base` (160ms ease) |
| `0.4s`, `400ms` | `--motion-slow` (240ms ease) |
| `1.4s` infinite pulse | `--pulse-cycle` (1400ms ease-in-out) |
| `2s` (`search-hit-flash`, `.status-dot[data-pulse]` reference) | `--flash-cycle` (2000ms ease-out) |

`transition` shorthand: `transition: background 0.1s` → `transition: background var(--motion-fast)`. The token already includes the easing keyword, so when token-replacing a `0.1s ease` literal drop the `ease`.

When the original transition was just a duration (no easing keyword) and the token includes one, the resulting timing is **identical at the duration value** (100ms = 100ms). Engineers don't need to do anything special — the easing change is part of the design.

### Z-index (`--z-*`)

| Literal | Token |
| --- | --- |
| `1` | stays literal (sticky-inside-scroll, e.g. `.search-dialog-header` on mobile) |
| `50` | `--z-dropdown` (200) — but see Note below |
| `100` | `--z-fixed-action` (100) |
| `150` | `--z-fixed-action` (100) — round down |
| `199` | `--z-overlay` (800) |
| `200` | `--z-drawer` (900) |

**Note on `--z-dropdown`:** the design language defines `--z-dropdown: 200`; today's `.chip-picker` uses `z-index: 50`, which is well below today's `100/150/199/200`. Promoting `.chip-picker` to `--z-dropdown` (200) is intentional — it's a popover and belongs above fixed-action chrome. If this causes any stacking issue (no current case in the codebase), revisit during verify.

---

## File Structure

Single file modified end-to-end:

- **Modify:** `cmd/serf-hub/assets/style.css` (~1037 lines as of pre-Pass-3). Pass 1 added tokens on `:root`; Pass 2 migrated typography. No new files.

All edits are pure value substitutions. No selector additions, no rule reordering, no JS changes, no template changes.

---

## How to Verify a Task

Every spacing/radius/motion task uses the same verification loop. Repeating the procedure here so engineers don't flip back:

1. Build and run the app:
   ```bash
   cd /home/jesse/git/prime-radiant/serf
   go build -o /tmp/serf-hub ./cmd/serf-hub
   /tmp/serf-hub --addr 127.0.0.1:9180 &
   ```
2. Open `http://127.0.0.1:9180` in a browser with DevTools open. Confirm:
   - Dark mode renders identically to the pre-task screenshot for the surface this task touched.
   - Toggle to light mode (settings → Theme → Light) and confirm the same.
   - Use DevTools "Elements" → "Computed" on at least one touched rule to confirm the `padding` / `border-radius` / `transition` / `z-index` resolves to the same pixel value as before.
3. Kill the server (`fg` then Ctrl-C, or `kill %1`).

The **first** task already establishes a baseline screenshot set. Subsequent tasks compare against it.

---

## Task 1: Establish Visual Baseline

**Files:** none modified — just snapshots.

- [ ] **Step 1: Read context**

Confirm pre-Pass-3 state of `cmd/serf-hub/assets/style.css` — Pass 1 tokens present on `:root`, Pass 2 typography migrated, layout literals NOT yet migrated. Run:

```bash
grep -cE "var\(--space-" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: low single digits (only places where Pass 1 token definitions exist in tooltips/comments). If 50+, Pass 3 is already in progress — STOP and reconcile.

- [ ] **Step 2: Capture baseline screenshots**

Run the app (see "How to Verify"). For each of the following surfaces, capture one dark-mode screenshot and one light-mode screenshot. Save to a scratch dir outside the repo (`/tmp/pass3-baseline/`):

- `00-landing.png` — `/` (landing list)
- `01-spawn.png` — `/new` (spawn pane, with the advanced disclosure open)
- `02-workspace-empty.png` — any session URL with no transcript yet
- `03-workspace-conversation.png` — a session with at least one user turn, one assistant turn, one tool call, and one diagnostic
- `04-settings-general.png` — `/settings/general`
- `05-settings-launch.png` — `/settings/serf-launch` (has the chip pickers)
- `06-credentials.png` — `/settings/credentials` (with the editor open)
- `07-search-palette.png` — `⌘K` open with a query that returns results
- `08-mobile-sidebar-open.png` — DevTools device emulation: iPhone 12 (390×844), hamburger tapped so sidebar overlays
- `09-mobile-conversation.png` — same device, conversation view

These are the comparison targets for every later task. The plan completes when re-shot screenshots are pixel-equivalent (modulo expected differences: ease curves on transitions are perceptual, not visible in still images).

- [ ] **Step 3: Verify baseline is on a clean working tree**

```bash
cd /home/jesse/git/prime-radiant/serf
git status
```

Expected: clean tree on the Pass-3 branch. If dirty, stash or commit before starting.

- [ ] **Step 4: Commit (no-op marker)**

No code changes in this task. Skip the commit — the next task starts the actual migration.

---

## Task 2: Collapse `var(--pad)` to `var(--space-4)`

This is the task the spec calls out: replace every `var(--pad)` with `var(--space-4)` in a single commit. Both resolve to 12px, so visuals don't move; subsequent tasks no longer have to think about two parallel "12px" tokens.

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (every line containing `var(--pad)` — at least: `120-124`, `134-135`, `139`, `144`, `152`, `202-211`, `218-222`, `229-236`)

- [ ] **Step 1: Read context**

```bash
grep -n "var(--pad)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Confirm every match is a layout value (padding, margin, gap). Expect ~20-25 matches in the legacy landing-page and `#status-bar`/`#transcript`/`#input-area` blocks.

- [ ] **Step 2: Replace every `var(--pad)` → `var(--space-4)` and every `calc(var(--pad) * N)` → its `--space-*` equivalent**

Use a single sed-style pass (do this via the Edit tool with `replace_all: true`):

**Replacement 1 (replace_all true):**
- old: `var(--pad)`
- new: `var(--space-4)`

**Replacement 2 (must run after Replacement 1 reverts any double-substitutions — but `calc(var(--space-4) * 2)` resolves to 24px = `--space-6`, and `calc(var(--space-4) * 4)` resolves to 48px = `--space-8`):**

After Replacement 1, the file now contains `calc(var(--space-4) * 2)` and `calc(var(--space-4) * 4)`. Replace each with the direct token:

- `calc(var(--space-4) * 2)` → `var(--space-6)` (replace_all true)
- `calc(var(--space-4) * 4)` → `var(--space-8)` (replace_all true)

- [ ] **Step 3: Verify**

```bash
grep -n "var(--pad)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: zero matches. Note: the **definition** of `--pad: 12px;` on `:root` (line 33) stays — it remains a legacy alias until the post-migration cleanup. Only **usages** are migrated.

Run "How to Verify" against all 10 baseline screenshots. Confirm no pixel diff in dark and light.

- [ ] **Step 4: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: collapse var(--pad) usages to var(--space-4)

Pass 3 prep — every `var(--pad)` becomes `var(--space-4)` in one
shot so subsequent surface-by-surface spacing migrations don't have
two 12px tokens to reconcile. `calc(var(--pad) * N)` becomes the
direct `--space-N` equivalent. The `--pad: 12px` definition on
:root stays as a legacy alias until end-of-migration cleanup.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Migrate Top-Level Layout Spacing (page-header, row, cols, panel, body)

Covers the legacy landing-page rules and the generic surfaces that aren't part of the app shell.

**Files:**
- Modify: `cmd/serf-hub/assets/style.css:120-247` (page-header, body:not(.app) main, .cols, section.panel, .row, top-level button/input rules, #status-bar, #transcript, .msg, #input-area, #input-buttons, .hint, .empty, html/body.app shell rules, .sidebar-loading, .workspace-empty)

- [ ] **Step 1: Read context**

Read lines 120-260 of `style.css`. Identify every spacing literal and its semantic role (panel padding vs row gap vs message inset).

- [ ] **Step 2: Apply replacements**

Use Edit for each rule. Full mapping (before → after):

**`.page-header` (line 120-124):**
- `padding: var(--space-4) calc(var(--pad) * 2)` → after Task 2 this already reads `padding: var(--space-4) var(--space-6)` — confirm. No change needed.

**`body:not(.app) main` (line 134-135):**
- After Task 2 reads `padding: var(--space-6); padding-bottom: var(--space-8);` — confirm. No change needed.

**`section.panel` (line 140-146):**
```css
/* before */
section.panel {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: var(--space-4);
  min-width: 0;
}
```
```css
/* after — radius migrated in Task 9; spacing already done */
section.panel {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: var(--space-4);
  min-width: 0;
}
```
No spacing change — `border-radius` waits for Task 9.

**`.row` (line 148-167):**
```css
/* before */
.row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: 8px 4px;
  border-bottom: 1px solid var(--border);
  …
}
.row .meta { … font-size: 12px; … }
```
```css
/* after */
.row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-3) var(--space-2);
  border-bottom: 1px solid var(--border);
  …
}
.row .meta { … font-size: 12px; … }
```

**`button, input[type=submit], a.btn` (line 178-186):**
```css
/* before */
button, input[type=submit], a.btn {
  …
  border-radius: 4px;
  padding: 6px 10px;
  font-size: 13px;
  …
}
input[type=text], textarea, select {
  …
  border-radius: 4px;
  padding: 6px;
  …
}
```
```css
/* after */
button, input[type=submit], a.btn {
  …
  border-radius: 4px;
  padding: var(--space-3) var(--space-4);
  font-size: 13px;
  …
}
input[type=text], textarea, select {
  …
  border-radius: 4px;
  padding: var(--space-3);
  …
}
```

**`textarea` line 198:** `min-height: 60px;` — 60px is a content sizing value, not layout spacing. **Stays literal** (would round to `--space-9` (64) but that breaks the form). Document in commit message.

**`#status-bar` (line 200-210):**
```css
/* before */
#status-bar {
  display: flex;
  gap: var(--space-4);
  align-items: center;
  padding: 8px var(--space-4);
  background: var(--panel-2);
  border-radius: 4px;
  font-size: 12px;
  color: var(--muted);
  margin-bottom: var(--space-4);
}
#status-bar .pill { padding: 2px 8px; background: var(--bg); border-radius: 12px; }
```
```css
/* after */
#status-bar {
  display: flex;
  gap: var(--space-4);
  align-items: center;
  padding: var(--space-3) var(--space-4);
  background: var(--panel-2);
  border-radius: 4px;
  font-size: 12px;
  color: var(--muted);
  margin-bottom: var(--space-4);
}
#status-bar .pill { padding: var(--space-1) var(--space-3); background: var(--bg); border-radius: 12px; }
```

**`#transcript` (line 214-221):**
```css
/* before */
#transcript {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: var(--space-4);
  height: 60vh;
  overflow-y: auto;
}
.msg { margin-bottom: var(--space-4); padding: 8px; border-radius: 4px; }
…
.msg.tool { … padding-left: 8px; … font-size: 12px; }
.msg .role { font-size: 11px; … margin-bottom: 4px; }
.msg pre, .msg code { … }
.msg pre { background: var(--bg); padding: 8px; border-radius: 4px; overflow-x: auto; }
```
```css
/* after */
#transcript {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: var(--space-4);
  height: 60vh;
  overflow-y: auto;
}
.msg { margin-bottom: var(--space-4); padding: var(--space-3); border-radius: 4px; }
…
.msg.tool { … padding-left: var(--space-3); … font-size: 12px; }
.msg .role { font-size: 11px; … margin-bottom: var(--space-2); }
.msg pre, .msg code { … }
.msg pre { background: var(--bg); padding: var(--space-3); border-radius: 4px; overflow-x: auto; }
```

**`#input-area` (line 231-242):**
```css
/* before */
#input-area {
  margin-top: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: var(--space-4);
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
}
#input-buttons { display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
.hint { color: var(--muted); font-size: 11px; padding: 4px; }
.empty { color: var(--muted); font-style: italic; padding: 16px; text-align: center; }
```
```css
/* after */
#input-area {
  margin-top: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-4);
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 6px;
}
#input-buttons { display: flex; gap: var(--space-3); flex-wrap: wrap; align-items: center; }
.hint { color: var(--muted); font-size: 11px; padding: var(--space-2); }
.empty { color: var(--muted); font-style: italic; padding: var(--space-5); text-align: center; }
```

**App shell (lines 247-252):**
```css
/* before */
#sidebar { width: 260px; flex-shrink: 0; height: 100vh; border-right: 1px solid var(--rule); overflow-y: auto; }
…
.sidebar-loading { padding: 24px; color: var(--text-muted); font-size: 12px; }
.workspace-empty { padding: 64px; color: var(--text-muted); text-align: center; }
```
```css
/* after */
#sidebar { width: 260px; flex-shrink: 0; height: 100vh; border-right: 1px solid var(--rule); overflow-y: auto; }
…
.sidebar-loading { padding: var(--space-6); color: var(--text-muted); font-size: 12px; }
.workspace-empty { padding: var(--space-9); color: var(--text-muted); text-align: center; }
```

`#sidebar` width `260px` is a layout sizing value (sidebar column width), **stays literal** — the design language §7.1 documents it as 260px exactly.

- [ ] **Step 3: Verify**

Run "How to Verify" against screenshots 00-landing, 02-workspace-empty, 03-workspace-conversation. The legacy landing list, page header, and workspace-empty surface should be pixel-identical.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate top-level layout spacing to --space-* tokens

Pass 3 — page-header, body main, .cols, section.panel, .row, generic
button/input rules, #status-bar, #transcript, .msg, #input-area,
.sidebar-loading, .workspace-empty. Two sizing values stay literal:
textarea min-height 60px and #sidebar width 260px (both are layout
geometry, not rhythm).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Migrate Sidebar Spacing

**Files:**
- Modify: `cmd/serf-hub/assets/style.css:254-330` (`.sidebar`, `.sidebar-header`, `.sidebar-action`, `.sidebar-section`, `.sidebar-section-header`, `.project-header`, row variants, status dots, project glyphs, fork banner)

- [ ] **Step 1: Read context**

Read lines 254-331. Note every padding/margin/gap literal. The sidebar rows currently use `padding: 5px 20px` (line 268) and `gap: 9px` — these are key vertical-rhythm decisions and must map carefully.

- [ ] **Step 2: Apply replacements**

```css
/* before */
.sidebar { padding: 20px 0; font-size: 12px; color: var(--text); }
.sidebar-header { padding: 0 20px 14px; display: flex; gap: 18px; }
.sidebar-action { color: var(--text-muted); text-decoration: none; cursor: pointer; }
.sidebar-action:hover { color: var(--text); }
.sidebar-action kbd { color: var(--text-dim); font-family: ui-monospace, monospace; font-size: 10px; margin-left: 4px; }
.sidebar-section { margin-bottom: 4px; }
.sidebar-section-header,
.project-header { padding: 14px 20px 4px; color: var(--text-dim); font-size: 10px; letter-spacing: 0.12em; text-transform: uppercase; display: flex; align-items: baseline; gap: 12px; }
.sidebar-section-header .row-meta,
.project-header .row-meta { margin-left: auto; letter-spacing: 0; }
.session-row,
.subagent-row,
.fork-row,
.live-row { display: flex; align-items: baseline; padding: 5px 20px; gap: 9px; color: var(--text); text-decoration: none; cursor: pointer; }
.session-row { font-weight: 500; }
.session-row .row-title { flex: 1; }
.subagent-row { padding-left: 48px; }
…
.status-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--state-ended); flex-shrink: 0; }
…
.status-dot.rollup { margin-left: auto; }
.row-meta { color: var(--text-muted); font-size: 11px; }
.row-age { color: var(--text-muted); font-size: 11px; margin-left: auto; }
.live-row { background: transparent; border-left: 2px solid transparent; }
.live-row[data-state="awaiting"]   { … border-left: 2px solid var(--state-awaiting); }
.live-row[data-state="active"] { … border-left: 2px solid var(--state-processing); }
.live-row[data-state="warning"]    { … border-left: 2px solid var(--state-warning); }
.project-header { cursor: default; }
.project-chevron { display: inline-block; width: 12px; … }
…
.project-rollup-dot { … width: 6px; height: 6px; border-radius: 50%; margin-left: 4px; … }
…
.project-new-btn { … padding: 4px 6px; font-size: 14px; line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; }
.project-new-btn:hover { color: var(--text); background: var(--bg-raised); }
.project-section:hover .project-new-btn { color: var(--text-muted); }
.project-gear-btn { … padding: 4px 5px; font-size: 11px; line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; opacity: 0; }
.project-gear-btn:hover { color: var(--text); background: var(--bg-raised); opacity: 1; }
.project-section:hover .project-gear-btn { opacity: 1; color: var(--text-muted); }
.fork-original-banner { font-size: 11px; color: var(--text-muted); margin-bottom: 4px; padding: 0; }
.fork-original-target { color: var(--text); }
```

```css
/* after — spacing only; radius and motion handled in Tasks 9/10 */
.sidebar { padding: var(--space-6) 0; font-size: 12px; color: var(--text); }
.sidebar-header { padding: 0 var(--space-6) var(--space-4); display: flex; gap: var(--space-5); }
.sidebar-action { color: var(--text-muted); text-decoration: none; cursor: pointer; }
.sidebar-action:hover { color: var(--text); }
.sidebar-action kbd { color: var(--text-dim); font-family: ui-monospace, monospace; font-size: 10px; margin-left: var(--space-2); }
.sidebar-section { margin-bottom: var(--space-2); }
.sidebar-section-header,
.project-header { padding: var(--space-4) var(--space-6) var(--space-2); color: var(--text-dim); font-size: 10px; letter-spacing: 0.12em; text-transform: uppercase; display: flex; align-items: baseline; gap: var(--space-4); }
.sidebar-section-header .row-meta,
.project-header .row-meta { margin-left: auto; letter-spacing: 0; }
.session-row,
.subagent-row,
.fork-row,
.live-row { display: flex; align-items: baseline; padding: var(--space-2) var(--space-6); gap: var(--space-3); color: var(--text); text-decoration: none; cursor: pointer; }
.session-row { font-weight: 500; }
.session-row .row-title { flex: 1; }
.subagent-row { padding-left: var(--space-8); }
…
.status-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--state-ended); flex-shrink: 0; }
…
.status-dot.rollup { margin-left: auto; }
.row-meta { color: var(--text-muted); font-size: 11px; }
.row-age { color: var(--text-muted); font-size: 11px; margin-left: auto; }
.live-row { background: transparent; border-left: 2px solid transparent; }
.live-row[data-state="awaiting"]   { … border-left: 2px solid var(--state-awaiting); }
.live-row[data-state="active"] { … border-left: 2px solid var(--state-processing); }
.live-row[data-state="warning"]    { … border-left: 2px solid var(--state-warning); }
.project-header { cursor: default; }
.project-chevron { display: inline-block; width: 12px; … }
…
.project-rollup-dot { … width: 6px; height: 6px; border-radius: 50%; margin-left: var(--space-2); … }
…
.project-new-btn { … padding: var(--space-2) var(--space-3); font-size: 14px; line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; }
.project-new-btn:hover { color: var(--text); background: var(--bg-raised); }
.project-section:hover .project-new-btn { color: var(--text-muted); }
.project-gear-btn { … padding: var(--space-2) var(--space-2); font-size: 11px; line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; opacity: 0; }
.project-gear-btn:hover { color: var(--text); background: var(--bg-raised); opacity: 1; }
.project-section:hover .project-gear-btn { opacity: 1; color: var(--text-muted); }
.fork-original-banner { font-size: 11px; color: var(--text-muted); margin-bottom: var(--space-2); padding: 0; }
.fork-original-target { color: var(--text); }
```

**Decisions documented inline:**
- `padding: 5px 20px` → `padding: var(--space-2) var(--space-6)` — sidebar row vertical rhythm wants the tightest layout step (4px); 20px horizontal rounds to 24px (`--space-6`).
- `gap: 9px` → `gap: var(--space-3)` — round 9 → 8.
- `padding-left: 48px` (subagent indent) → `var(--space-8)`.
- `gap: 18px` (sidebar-header) → `var(--space-5)` (16) — round 18 → 16; the action chrome stays well-spaced.
- `gap: 12px` (sidebar-section-header) → `var(--space-4)`.
- `margin-left: 4px` (kbd, rollup-dot) → `var(--space-2)`.
- `padding: 14px 20px 4px` (section headers) → `var(--space-4) var(--space-6) var(--space-2)`.
- 6px and 4px dot dimensions stay literal — these are icon sizes, not layout rhythm.
- 12px chevron width stays literal — icon sizing.
- 2px borders stay literal — hairline strokes.

Border-radius and transitions are addressed in later tasks; leave them alone here.

- [ ] **Step 3: Verify**

Run "How to Verify." Compare with screenshots 03 and 08 (mobile-sidebar). Sidebar should be pixel-identical: rows align, project headers indent the same, rollup dots sit in the same spot.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate sidebar spacing to --space-* tokens

Pass 3 — .sidebar, .sidebar-header, .sidebar-action, .sidebar-section,
section/project headers, row variants (session/subagent/fork/live),
project glyphs, .fork-original-banner. Row vertical rhythm uses
--space-2 (4); horizontal 20px rounds to --space-6 (24). Subagent
indent --space-8 (48). Dot/chevron icon dimensions stay literal.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Migrate Workspace-Header Spacing

**Files:**
- Modify: `cmd/serf-hub/assets/style.css:332-356` (`.workspace-header`, `.workspace-title-row`, `.workspace-title`, `.workspace-meta`, `.workspace-actions`, `.panel-toggle`, `.panel-toggle-icon`, `.panel-toggle-label`, `.header-action`, `.header-action-danger`, `.rule-dot`, `.status-pill`)

- [ ] **Step 1: Read context**

Read lines 332-358. This is the header strip above the conversation — every gap and pad here drives the visual rhythm of the page chrome.

- [ ] **Step 2: Apply replacements**

```css
/* before */
.workspace-header { padding: 12px 24px 10px; border-bottom: 1px solid var(--rule); }
.workspace-title-row { display: flex; align-items: center; gap: 12px; }
.workspace-title { display: flex; align-items: baseline; gap: 8px; flex: 1; min-width: 0; }
.workspace-title .title { font-weight: 500; font-size: 15px; … }
.workspace-meta { margin-top: 3px; display: flex; align-items: baseline; gap: 8px; color: var(--text-muted); font-size: 11.5px; }
.workspace-actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.panel-toggle { display: inline-flex; align-items: center; gap: 5px; padding: 4px 10px; background: transparent; border: 1px solid transparent; border-radius: 4px; color: var(--text-muted); font-size: 12px; font-family: inherit; cursor: pointer; transition: background 0.1s, color 0.1s, border-color 0.1s; }
.panel-toggle:hover { color: var(--text); background: var(--bg-raised); border-color: var(--rule); }
.panel-toggle.active { color: var(--text); background: var(--bg-raised); border-color: var(--rule); }
.panel-toggle-icon { font-size: 13px; line-height: 1; }
.panel-toggle-label { font-size: 12px; }
.header-action { display: inline-flex; align-items: center; padding: 4px 8px; background: transparent; border: none; border-radius: 4px; color: var(--text-muted); font-size: 12px; font-family: inherit; cursor: pointer; transition: background 0.1s, color 0.1s; }
.header-action:hover:not([disabled]) { color: var(--text); background: var(--bg-raised); }
…
.status-pill { display: inline-flex; align-items: baseline; gap: 6px; }
```

```css
/* after */
.workspace-header { padding: var(--space-4) var(--space-6) var(--space-4); border-bottom: 1px solid var(--rule); }
.workspace-title-row { display: flex; align-items: center; gap: var(--space-4); }
.workspace-title { display: flex; align-items: baseline; gap: var(--space-3); flex: 1; min-width: 0; }
.workspace-title .title { font-weight: 500; font-size: 15px; … }
.workspace-meta { margin-top: var(--space-2); display: flex; align-items: baseline; gap: var(--space-3); color: var(--text-muted); font-size: 11.5px; }
.workspace-actions { display: flex; align-items: center; gap: var(--space-3); flex-shrink: 0; }
.panel-toggle { display: inline-flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-4); background: transparent; border: 1px solid transparent; border-radius: 4px; color: var(--text-muted); font-size: 12px; font-family: inherit; cursor: pointer; transition: background 0.1s, color 0.1s, border-color 0.1s; }
.panel-toggle:hover { color: var(--text); background: var(--bg-raised); border-color: var(--rule); }
.panel-toggle.active { color: var(--text); background: var(--bg-raised); border-color: var(--rule); }
.panel-toggle-icon { font-size: 13px; line-height: 1; }
.panel-toggle-label { font-size: 12px; }
.header-action { display: inline-flex; align-items: center; padding: var(--space-2) var(--space-3); background: transparent; border: none; border-radius: 4px; color: var(--text-muted); font-size: 12px; font-family: inherit; cursor: pointer; transition: background 0.1s, color 0.1s; }
.header-action:hover:not([disabled]) { color: var(--text); background: var(--bg-raised); }
…
.status-pill { display: inline-flex; align-items: baseline; gap: var(--space-3); }
```

**Decisions:**
- `padding: 12px 24px 10px` → `var(--space-4) var(--space-6) var(--space-4)` — top 12 and bottom 10 collapse to 12 (consistency across surfaces; visually <2px shift).
- `margin-top: 3px` → `var(--space-2)` (4) — meta line lift to 4px from title; visually indistinguishable.
- `gap: 6px` (workspace-actions, status-pill) → `var(--space-3)` (8) for actions; for status-pill keep --space-3 too — both reads better at 8 than 4.

- [ ] **Step 3: Verify**

Run "How to Verify" against 02, 03, 04. Title row, meta line, action chrome all sit in the same place. The 12/10 collapse to 12/12 makes the bottom border one pixel lower — acceptable, document in commit.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate workspace-header spacing to --space-* tokens

Pass 3 — .workspace-header, title-row, title, meta, actions,
panel-toggle, header-action, status-pill. 12px/10px asymmetric
header pad collapses to symmetric --space-4 (12px); shifts the
bottom border 2px down at most. 3px meta lift rounds to --space-2.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Migrate Conversation, Tool-Call, and Diff Spacing

**Files:**
- Modify: `cmd/serf-hub/assets/style.css:357-573` (`.conversation`, `.user-message`, `.user-message .pill`, `.user-message-actions`, `.user-message-images`, `.user-image-card`, `.user-image-name`, `.image-lightbox`, `.assistant-message`, `.tool-call`, `.tool-call-cluster`, `.tool-status` family, `.diff-body`, `.tool-body`, `.shell-output`, `.cheap-tool-args/output`, task/fetch/search bodies, `.subagent-reference`, `.diagnostic`, `.diagnostic-header`, `.diagnostic-badge`, `.diagnostic-hint`, `.banner`, `.system-line`, `.task-system-*`, `.steering`)

This is the largest task by line count. The conversation is the most-seen surface; verify carefully.

- [ ] **Step 1: Read context**

Read lines 357-595. Map every literal to its semantic role. Pay attention to: `.tool-status margin-left: -20px` (negative offset — use `calc()`), `.tool-call column-gap: 8px, row-gap: 1px` (different scales), `.diff-body margin: 0 0 12px 36px` (asymmetric four-value margin).

- [ ] **Step 2: Apply replacements (conversation root + user message)**

```css
/* before */
.conversation { flex: 1; min-height: 0; padding: 32px 64px; overflow-y: auto; font-size: 15px; line-height: 1.7; color: var(--text); }
.workspace-input { padding: 12px 24px 14px; border-top: 1px solid var(--rule); }
.input-attachments { display: flex; gap: 6px; flex-wrap: wrap; padding-bottom: 8px; }
…
.user-message { display: flex; justify-content: flex-end; margin-bottom: 28px; padding-top: 22px; position: relative; }
.user-message .pill { max-width: 62%; padding: 8px 14px; background: var(--bg-raised); border-radius: 14px; font-size: 14.5px; color: var(--text); line-height: 1.55; }
.user-message-actions { position: absolute; right: 0; top: 4px; display: none; gap: 14px; font-size: 11px; color: var(--text-muted); }
.user-message:hover .user-message-actions { display: flex; }
.user-message-actions .action { cursor: pointer; }
.user-message-actions .edit { color: var(--text); }
.user-message-images { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
.user-message-images:empty { display: none; }
.user-image-card { display: inline-flex; flex-direction: column; align-items: stretch; gap: 4px; padding: 0; background: transparent; border: 1px solid var(--rule); border-radius: 8px; cursor: pointer; overflow: hidden; max-width: 220px; }
…
.user-image-name { display: block; padding: 4px 8px; font-size: 11px; color: var(--text-muted); font-family: ui-monospace, "SFMono-Regular", monospace; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 220px; text-align: left; }
…
.image-lightbox { position: fixed; inset: 0; background: rgba(0,0,0,0.8); display: flex; align-items: center; justify-content: center; flex-direction: column; gap: 12px; z-index: 200; cursor: zoom-out; padding: 32px; }
.image-lightbox img { max-width: 90vw; max-height: 85vh; object-fit: contain; border-radius: 6px; box-shadow: 0 20px 60px rgba(0,0,0,0.5); }
.image-lightbox-caption { color: #fff; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; opacity: 0.75; }
.assistant-message { margin-bottom: 24px; max-width: 680px; font-size: 13px; line-height: 1.6; color: var(--text); }
.assistant-message code { background: var(--bg-raised); padding: 1px 6px; border-radius: 3px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12.5px; color: var(--text); }
.assistant-message pre { background: var(--bg-raised); padding: 12px 16px; border-radius: 6px; overflow-x: auto; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; }
```

```css
/* after */
.conversation { flex: 1; min-height: 0; padding: var(--space-7) var(--space-9); overflow-y: auto; font-size: 15px; line-height: 1.7; color: var(--text); }
.workspace-input { padding: var(--space-4) var(--space-6) var(--space-4); border-top: 1px solid var(--rule); }
.input-attachments { display: flex; gap: var(--space-3); flex-wrap: wrap; padding-bottom: var(--space-3); }
…
.user-message { display: flex; justify-content: flex-end; margin-bottom: var(--space-7); padding-top: var(--space-6); position: relative; }
.user-message .pill { max-width: 62%; padding: var(--space-3) var(--space-4); background: var(--bg-raised); border-radius: 14px; font-size: 14.5px; color: var(--text); line-height: 1.55; }
.user-message-actions { position: absolute; right: 0; top: var(--space-2); display: none; gap: var(--space-4); font-size: 11px; color: var(--text-muted); }
.user-message:hover .user-message-actions { display: flex; }
.user-message-actions .action { cursor: pointer; }
.user-message-actions .edit { color: var(--text); }
.user-message-images { display: flex; gap: var(--space-3); flex-wrap: wrap; margin-bottom: var(--space-3); }
.user-message-images:empty { display: none; }
.user-image-card { display: inline-flex; flex-direction: column; align-items: stretch; gap: var(--space-2); padding: 0; background: transparent; border: 1px solid var(--rule); border-radius: 8px; cursor: pointer; overflow: hidden; max-width: 220px; }
…
.user-image-name { display: block; padding: var(--space-2) var(--space-3); font-size: 11px; color: var(--text-muted); font-family: ui-monospace, "SFMono-Regular", monospace; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 220px; text-align: left; }
…
.image-lightbox { position: fixed; inset: 0; background: rgba(0,0,0,0.8); display: flex; align-items: center; justify-content: center; flex-direction: column; gap: var(--space-4); z-index: 200; cursor: zoom-out; padding: var(--space-7); }
.image-lightbox img { max-width: 90vw; max-height: 85vh; object-fit: contain; border-radius: 6px; box-shadow: 0 20px 60px rgba(0,0,0,0.5); }
.image-lightbox-caption { color: #fff; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; opacity: 0.75; }
.assistant-message { margin-bottom: var(--space-6); max-width: 680px; font-size: 13px; line-height: 1.6; color: var(--text); }
.assistant-message code { background: var(--bg-raised); padding: 1px var(--space-3); border-radius: 3px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12.5px; color: var(--text); }
.assistant-message pre { background: var(--bg-raised); padding: var(--space-4) var(--space-5); border-radius: 6px; overflow-x: auto; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; }
```

**Decisions:**
- `padding: 32px 64px` → `var(--space-7) var(--space-9)`.
- `margin-bottom: 28px; padding-top: 22px` → `var(--space-7); var(--space-6)` — 28 rounds up to 32; 22 rounds up to 24.
- `padding: 8px 14px` (pill) → `var(--space-3) var(--space-4)` — 8 stays, 14 → 12.
- `padding: 12px 16px` (assistant pre) → `var(--space-4) var(--space-5)`.
- `padding: 1px 6px` (inline code) → `1px var(--space-3)` (4→6 rounds to 8, but at this scale 6 reads as inset; round down to --space-3 (8) keeps consistency). Actually keep `1px var(--space-3)` — 1px hairline stays literal.

- [ ] **Step 3: Apply replacements (tool-call + body family)**

```css
/* before */
.tool-call { margin: 0 0 6px 0; font-size: 12.5px; color: var(--text-muted); display: flex; column-gap: 8px; row-gap: 1px; flex-wrap: wrap; align-items: baseline; }
.tool-call-cluster { margin-bottom: 12px; }
.tool-call-cluster .tool-call:last-child { margin-bottom: 0; }
.tool-call .tool-status { display: inline-block; width: 12px; flex: 0 0 12px; margin-left: -20px; text-align: center; font-size: 11px; line-height: 1; font-family: ui-monospace, "SFMono-Regular", monospace; }
…
.tool-call .tool-meta { margin-left: auto; … font-size: 11px; white-space: nowrap; }
…
.diff-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.5; color: var(--text-muted); white-space: pre-wrap; }
…
.tool-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-size: 12px; color: var(--text-muted); }
.tool-body summary { cursor: pointer; color: var(--text-muted); padding: 0; user-select: none; }
.tool-body summary:hover { color: var(--text); }
.shell-output { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text); white-space: pre-wrap; }
.cheap-tool-args { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text-muted); white-space: pre-wrap; }
.cheap-tool-output { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text); white-space: pre-wrap; }
.read-tool-purpose { margin: 0 0 3px; color: var(--text-muted); font-style: italic; }
.read-tool-body .cheap-tool-output { margin-top: 0; }
…
.task-list-body { list-style: none; margin: 0 0 12px 36px; padding: 6px 0 6px 10px; border-left: 1px solid var(--rule); display: flex; flex-direction: column; gap: 3px; }
.task-list-body .task-row { padding: 2px 0; background: transparent; border: none; font-size: 12px; display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
…
.fetch-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-size: 12px; color: var(--text-muted); font-style: italic; }
.search-body { list-style: none; margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-size: 12px; color: var(--text); }
.search-body li { padding: 2px 0; }
.subagent-reference { margin: 0 0 12px 22px; font-size: 12.5px; color: var(--text-muted); cursor: pointer; }
…
.diagnostic {
  --diagnostic-accent: var(--state-warning);
  margin: 0 0 18px 28px;
  max-width: 720px;
  padding: 10px 12px 11px;
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-left: 3px solid var(--diagnostic-accent);
  border-radius: 6px;
  …
}
…
.diagnostic-header { display: flex; align-items: baseline; gap: 9px; margin-bottom: 5px; flex-wrap: wrap; }
.diagnostic-badge {
  …
  padding: 1px 6px;
  …
  border-radius: 4px;
  …
}
.diagnostic-title { color: var(--text); font-weight: 500; }
.diagnostic-message { color: var(--text); word-break: break-word; }
.diagnostic-hint { margin-top: 5px; color: var(--text-muted); word-break: break-word; }

.banner { margin: 0 0 18px 28px; padding: 6px 12px; font-size: 12.5px; border-radius: 4px; }
.banner.error { color: var(--state-awaiting); border-left: 2px solid var(--state-awaiting); padding-left: 10px; }
.banner.warning { color: var(--state-warning); border-left: 2px solid var(--state-warning); padding-left: 10px; }
.banner.note { color: var(--text-muted); border-left: 2px solid var(--rule); padding-left: 10px; }

.system-line { margin: 8px 0 8px 28px; padding: 4px 0; font-size: 12px; color: var(--text-muted); font-style: italic; line-height: 1.5; }
.task-system-icon { display: inline-block; width: 14px; margin-right: 6px; color: var(--text); font-style: normal; font-family: ui-monospace, "SFMono-Regular", monospace; }
.task-system-details { display: block; margin: 2px 0 0 20px; font-style: normal; }
.task-system-details summary { cursor: pointer; color: var(--text-muted); display: list-item; list-style: disclosure-closed inside; width: max-content; text-align: left; }
.task-system-details[open] summary { list-style-type: disclosure-open; }
.task-system-detail-item { margin-top: 4px; }
.task-system-detail-title { color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
.task-system-detail { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 2px 8px; margin: 3px 0 0; max-width: 680px; }
.task-system-detail dt { color: var(--text-dim); font-family: ui-monospace, "SFMono-Regular", monospace; }
.task-system-detail dd { margin: 0; color: var(--text); white-space: pre-wrap; }
…
.steering { margin: 18px 0; padding: 0; font-size: 11.5px; color: var(--text-muted); }
.steering > summary { cursor: pointer; list-style: none; user-select: none; display: flex; align-items: center; gap: 12px; padding: 0; }
…
.steering .steering-body { margin: 10px 0 0; padding: 10px 12px; background: var(--bg); border-radius: 4px; … }
```

```css
/* after */
.tool-call { margin: 0 0 var(--space-3) 0; font-size: 12.5px; color: var(--text-muted); display: flex; column-gap: var(--space-3); row-gap: 1px; flex-wrap: wrap; align-items: baseline; }
.tool-call-cluster { margin-bottom: var(--space-4); }
.tool-call-cluster .tool-call:last-child { margin-bottom: 0; }
.tool-call .tool-status { display: inline-block; width: 12px; flex: 0 0 12px; margin-left: calc(0px - var(--space-6)); text-align: center; font-size: 11px; line-height: 1; font-family: ui-monospace, "SFMono-Regular", monospace; }
…
.tool-call .tool-meta { margin-left: auto; … font-size: 11px; white-space: nowrap; }
…
.diff-body { margin: 0 0 var(--space-4) 36px; padding-left: var(--space-4); border-left: 1px solid var(--rule); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.5; color: var(--text-muted); white-space: pre-wrap; }
…
.tool-body { margin: 0 0 var(--space-4) 36px; padding-left: var(--space-4); border-left: 1px solid var(--rule); font-size: 12px; color: var(--text-muted); }
.tool-body summary { cursor: pointer; color: var(--text-muted); padding: 0; user-select: none; }
.tool-body summary:hover { color: var(--text); }
.shell-output { margin: var(--space-2) 0 0; padding: var(--space-3) var(--space-3); background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text); white-space: pre-wrap; }
.cheap-tool-args { margin: var(--space-2) 0 0; padding: var(--space-3) var(--space-3); background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text-muted); white-space: pre-wrap; }
.cheap-tool-output { margin: var(--space-2) 0 0; padding: var(--space-3) var(--space-3); background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text); white-space: pre-wrap; }
.read-tool-purpose { margin: 0 0 var(--space-2); color: var(--text-muted); font-style: italic; }
.read-tool-body .cheap-tool-output { margin-top: 0; }
…
.task-list-body { list-style: none; margin: 0 0 var(--space-4) 36px; padding: var(--space-3) 0 var(--space-3) var(--space-4); border-left: 1px solid var(--rule); display: flex; flex-direction: column; gap: var(--space-2); }
.task-list-body .task-row { padding: var(--space-1) 0; background: transparent; border: none; font-size: 12px; display: flex; align-items: baseline; gap: var(--space-3); flex-wrap: wrap; }
…
.fetch-body { margin: 0 0 var(--space-4) 36px; padding-left: var(--space-4); border-left: 1px solid var(--rule); font-size: 12px; color: var(--text-muted); font-style: italic; }
.search-body { list-style: none; margin: 0 0 var(--space-4) 36px; padding-left: var(--space-4); border-left: 1px solid var(--rule); font-size: 12px; color: var(--text); }
.search-body li { padding: var(--space-1) 0; }
.subagent-reference { margin: 0 0 var(--space-4) var(--space-6); font-size: 12.5px; color: var(--text-muted); cursor: pointer; }
…
.diagnostic {
  --diagnostic-accent: var(--state-warning);
  margin: 0 0 var(--space-5) var(--space-7);
  max-width: 720px;
  padding: var(--space-4) var(--space-4) var(--space-4);
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-left: 3px solid var(--diagnostic-accent);
  border-radius: 6px;
  …
}
…
.diagnostic-header { display: flex; align-items: baseline; gap: var(--space-3); margin-bottom: var(--space-2); flex-wrap: wrap; }
.diagnostic-badge {
  …
  padding: 1px var(--space-3);
  …
  border-radius: 4px;
  …
}
.diagnostic-title { color: var(--text); font-weight: 500; }
.diagnostic-message { color: var(--text); word-break: break-word; }
.diagnostic-hint { margin-top: var(--space-2); color: var(--text-muted); word-break: break-word; }

.banner { margin: 0 0 var(--space-5) var(--space-7); padding: var(--space-3) var(--space-4); font-size: 12.5px; border-radius: 4px; }
.banner.error { color: var(--state-awaiting); border-left: 2px solid var(--state-awaiting); padding-left: var(--space-4); }
.banner.warning { color: var(--state-warning); border-left: 2px solid var(--state-warning); padding-left: var(--space-4); }
.banner.note { color: var(--text-muted); border-left: 2px solid var(--rule); padding-left: var(--space-4); }

.system-line { margin: var(--space-3) 0 var(--space-3) var(--space-7); padding: var(--space-2) 0; font-size: 12px; color: var(--text-muted); font-style: italic; line-height: 1.5; }
.task-system-icon { display: inline-block; width: 14px; margin-right: var(--space-3); color: var(--text); font-style: normal; font-family: ui-monospace, "SFMono-Regular", monospace; }
.task-system-details { display: block; margin: var(--space-1) 0 0 var(--space-6); font-style: normal; }
.task-system-details summary { cursor: pointer; color: var(--text-muted); display: list-item; list-style: disclosure-closed inside; width: max-content; text-align: left; }
.task-system-details[open] summary { list-style-type: disclosure-open; }
.task-system-detail-item { margin-top: var(--space-2); }
.task-system-detail-title { color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
.task-system-detail { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: var(--space-1) var(--space-3); margin: var(--space-2) 0 0; max-width: 680px; }
.task-system-detail dt { color: var(--text-dim); font-family: ui-monospace, "SFMono-Regular", monospace; }
.task-system-detail dd { margin: 0; color: var(--text); white-space: pre-wrap; }
…
.steering { margin: var(--space-5) 0; padding: 0; font-size: 11.5px; color: var(--text-muted); }
.steering > summary { cursor: pointer; list-style: none; user-select: none; display: flex; align-items: center; gap: var(--space-4); padding: 0; }
…
.steering .steering-body { margin: var(--space-4) 0 0; padding: var(--space-4) var(--space-4); background: var(--bg); border-radius: 4px; … }
```

**Decisions documented inline:**
- `36px` indent on diff/tool/task/fetch/search bodies **stays literal** — Pass 6 introduces `--tool-indent: 36px` (per spec); leaving as 36px now keeps the same Pass 6 swap surface.
- `28px` indent on diagnostic/banner/system-line → `var(--space-7)` (32). Tight 4px shift across these surfaces is acceptable.
- `22px` indent on subagent-reference → `var(--space-6)` (24).
- `9px` gap (diagnostic-header) → `var(--space-3)`.
- `5px` margin-bottom (diagnostic) → `var(--space-2)`.
- `padding: 10px 12px 11px` (diagnostic) → `var(--space-4) var(--space-4) var(--space-4)` — collapses asymmetric 10/12/11 to uniform 12; <2px change.
- `margin-left: -20px` (tool-status) → `calc(0px - var(--space-6))`.
- `column-gap: 8px` (tool-call) → `var(--space-3)`; `row-gap: 1px` stays literal (hairline).
- `padding: 6px 8px` (shell-output et al) → `var(--space-3) var(--space-3)` — 6 rounds to 8.
- `padding: 4px 0` (system-line) → `var(--space-2) 0`.

- [ ] **Step 4: Verify**

Run "How to Verify" against screenshot 03-workspace-conversation. Diff body lines up with tool call indent; diagnostic margin matches; banner border-left position unchanged. Any visible >4px shift is a bug.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate conversation + tool-call spacing to --space-* tokens

Pass 3 — .conversation, .user-message family, .assistant-message,
.tool-call cluster, .diff-body, .tool-body, output bodies, task-list,
fetch/search bodies, .subagent-reference, .diagnostic, .banner,
.system-line family, .task-system-detail, .steering. 36px tool
indent stays literal pending Pass 6's --tool-indent token. The
diagnostic 10/12/11 asymmetric padding collapses to uniform 12.
Negative -20px margin uses calc(0px - var(--space-6)).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Migrate Composer Spacing

**Files:**
- Modify: `cmd/serf-hub/assets/style.css:359-437` (`.workspace-input`, `.input-attachments`, `.queue-preview*`, `.attachment-chip*`, `.composer-attachments*`, `.composer-attachment*`, `.input-card`, `.message-input`, `.input-controls`, `.controls-spacer`, `.running-indicator`, `.running-dot`, `.input-btn*`, `.input-chip`, `.chip-caret`, `.input-status*`, `.context*`, `.cost`, `.fork-dialog*`)

The composer is the second-most-touched surface. Migrate the entire input strip together so the bottom-of-page feel stays coherent.

- [ ] **Step 1: Read context**

Read lines 358-596. Note: `.workspace-input` was already partly handled by Task 6 (it's adjacent to `.conversation`). If Task 6 already migrated it, skip the rule in Step 2 and just verify.

- [ ] **Step 2: Apply replacements**

```css
/* before */
.input-attachments { display: flex; gap: 6px; flex-wrap: wrap; padding-bottom: 8px; }
…
.queue-preview { padding: 8px 10px; margin-bottom: 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
.queue-preview-header { display: flex; align-items: baseline; gap: 12px; color: var(--text-muted); margin-bottom: 4px; }
.queue-preview-label { font-weight: 500; color: var(--text); }
.queue-preview-label [data-queue-depth] { font-family: ui-monospace, "SFMono-Regular", monospace; }
.queue-preview-hint { flex: 1; }
.queue-preview-hint kbd { font-family: ui-monospace, "SFMono-Regular", monospace; padding: 0 3px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 3px; }
.queue-preview-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 3px; }
.queue-preview-item { display: flex; gap: 6px; align-items: baseline; padding: 3px 6px; background: var(--bg-raised); border-radius: 3px; }
.queue-preview-item .qp-idx { … font-size: 10px; }
.queue-preview-item .qp-text { … }
.attachment-chip { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
.attachment-chip .att-thumb { width: 18px; height: 18px; object-fit: cover; border-radius: 2px; }
.attachment-chip .att-remove { cursor: pointer; color: var(--text-muted); padding: 0 2px; border: none; background: transparent; }
.attachment-chip .att-remove:hover { color: var(--text); }
.composer-attachments { display: flex; gap: 6px; flex-wrap: wrap; padding-bottom: 8px; }
.composer-attachments:empty { display: none; }
.composer-attachment { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; color: var(--text); }
…
.composer-attachment-remove { cursor: pointer; color: var(--text-muted); padding: 0 2px; border: none; background: transparent; font-size: 13px; line-height: 1; }
.composer-attachment-remove:hover { color: var(--text); }

.input-card { background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 6px; padding: 12px 14px; min-height: 80px; transition: border-color 0.15s; }
.input-card.drop-active,
.spawn-input-wrap.drop-active { outline: 2px dashed var(--accent, var(--state-processing)); outline-offset: -2px; }
.composer-attachment-error { color: var(--state-danger, #c8553d); font-size: 11px; padding: 4px 0; }
…
.message-input { … }

.input-controls { display: flex; align-items: center; gap: 8px; padding: 8px 0 0; flex-wrap: wrap; }
.controls-spacer { flex: 1; }
.running-indicator { display: inline-flex; align-items: center; gap: 6px; color: var(--text); font-size: 12px; white-space: nowrap; }
.running-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--state-processing); box-shadow: 0 0 0 4px rgba(122,162,247,0.12); }
.input-btn { display: inline-flex; align-items: center; gap: 5px; padding: 4px 12px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; color: var(--text); font: inherit; font-size: 11.5px; cursor: pointer; }
.input-btn:hover { background: var(--bg-raised); }
.input-btn-ghost { color: var(--text-muted); }
.input-btn-stop { color: var(--state-awaiting); border-color: rgba(247,118,142,0.45); background: rgba(247,118,142,0.08); margin-right: 10px; }
.input-btn-stop:hover:not([disabled]) { background: rgba(247,118,142,0.14); border-color: rgba(247,118,142,0.65); }
.input-btn-stop[disabled] { opacity: 0.45; cursor: not-allowed; }
.input-btn-primary { background: var(--state-processing); color: var(--bg); border-color: transparent; font-weight: 500; }
.input-btn-primary:hover { background: var(--state-processing); filter: brightness(1.1); }
.input-btn-primary kbd { background: rgba(0,0,0,0.2); border: 1px solid rgba(0,0,0,0.3); color: inherit; font-family: ui-monospace, "SFMono-Regular", monospace; padding: 0 4px; border-radius: 3px; }

.input-chip { font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
.chip-caret { color: var(--text-muted); margin-left: 2px; }

.input-status { display: flex; align-items: center; gap: 14px; padding: 10px 0 0; margin-top: 6px; border-top: 1px solid var(--rule); font-size: 11px; color: var(--text-muted); flex-wrap: wrap; }
.input-status .cwd, .input-status .branch { font-family: ui-monospace, "SFMono-Regular", monospace; }
.input-status .cwd { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.input-status .status-spacer { flex: 1; }
.input-status .context { display: inline-flex; align-items: center; gap: 6px; }
.input-status .context-bar { width: 80px; height: 3px; background: var(--bg); border-radius: 2px; overflow: hidden; }
.input-status .context-fill { display: block; height: 100%; background: var(--state-processing); }
.input-status .context-numbers { font-family: ui-monospace, "SFMono-Regular", monospace; }
.input-status .cost { font-family: ui-monospace, "SFMono-Regular", monospace; }

.context { display: inline-flex; align-items: center; gap: 8px; }
.context-bar { display: inline-block; width: 80px; height: 2px; background: var(--rule); vertical-align: middle; overflow: hidden; }
…
.fork-dialog { margin-bottom: 24px; padding: 12px 16px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 8px; max-width: 480px; margin-left: auto; }
.fork-dialog-title { font-weight: 500; color: var(--text); font-size: 13px; margin-bottom: 6px; }
.fork-dialog-body { color: var(--text-muted); font-size: 12.5px; line-height: 1.55; margin-bottom: 10px; }
.fork-dialog-label { color: var(--text-muted); font-size: 12px; }
.fork-dialog-input { background: transparent; border: 1px solid var(--rule); border-radius: 3px; padding: 4px 8px; color: var(--text); font: inherit; font-size: 12px; width: 220px; margin-left: 8px; outline: none; }
.fork-dialog-input:focus { border-color: var(--accent); }
.fork-dialog-actions { display: flex; gap: 14px; align-items: center; margin-top: 10px; font-size: 12px; }
.fork-cancel { background: transparent; border: none; color: var(--text-muted); cursor: pointer; font: inherit; padding: 4px 8px; }
.fork-confirm { margin-left: auto; padding: 6px 14px; background: var(--accent); color: var(--bg); border: none; border-radius: 4px; font-weight: 500; cursor: pointer; font: inherit; }
```

```css
/* after */
.input-attachments { display: flex; gap: var(--space-3); flex-wrap: wrap; padding-bottom: var(--space-3); }
…
.queue-preview { padding: var(--space-3) var(--space-4); margin-bottom: var(--space-3); background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
.queue-preview-header { display: flex; align-items: baseline; gap: var(--space-4); color: var(--text-muted); margin-bottom: var(--space-2); }
.queue-preview-label { font-weight: 500; color: var(--text); }
.queue-preview-label [data-queue-depth] { font-family: ui-monospace, "SFMono-Regular", monospace; }
.queue-preview-hint { flex: 1; }
.queue-preview-hint kbd { font-family: ui-monospace, "SFMono-Regular", monospace; padding: 0 var(--space-2); background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 3px; }
.queue-preview-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: var(--space-2); }
.queue-preview-item { display: flex; gap: var(--space-3); align-items: baseline; padding: var(--space-2) var(--space-3); background: var(--bg-raised); border-radius: 3px; }
.queue-preview-item .qp-idx { … font-size: 10px; }
.queue-preview-item .qp-text { … }
.attachment-chip { display: inline-flex; align-items: center; gap: var(--space-3); padding: var(--space-2) var(--space-3); background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
.attachment-chip .att-thumb { width: 18px; height: 18px; object-fit: cover; border-radius: 2px; }
.attachment-chip .att-remove { cursor: pointer; color: var(--text-muted); padding: 0 var(--space-1); border: none; background: transparent; }
.attachment-chip .att-remove:hover { color: var(--text); }
.composer-attachments { display: flex; gap: var(--space-3); flex-wrap: wrap; padding-bottom: var(--space-3); }
.composer-attachments:empty { display: none; }
.composer-attachment { display: inline-flex; align-items: center; gap: var(--space-3); padding: var(--space-2) var(--space-3); background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; color: var(--text); }
…
.composer-attachment-remove { cursor: pointer; color: var(--text-muted); padding: 0 var(--space-1); border: none; background: transparent; font-size: 13px; line-height: 1; }
.composer-attachment-remove:hover { color: var(--text); }

.input-card { background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 6px; padding: var(--space-4) var(--space-4); min-height: 80px; transition: border-color 0.15s; }
.input-card.drop-active,
.spawn-input-wrap.drop-active { outline: 2px dashed var(--accent, var(--state-processing)); outline-offset: -2px; }
.composer-attachment-error { color: var(--state-danger, #c8553d); font-size: 11px; padding: var(--space-2) 0; }
…
.message-input { … }

.input-controls { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-3) 0 0; flex-wrap: wrap; }
.controls-spacer { flex: 1; }
.running-indicator { display: inline-flex; align-items: center; gap: var(--space-3); color: var(--text); font-size: 12px; white-space: nowrap; }
.running-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--state-processing); box-shadow: 0 0 0 var(--space-2) rgba(122,162,247,0.12); }
.input-btn { display: inline-flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-4); background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; color: var(--text); font: inherit; font-size: 11.5px; cursor: pointer; }
.input-btn:hover { background: var(--bg-raised); }
.input-btn-ghost { color: var(--text-muted); }
.input-btn-stop { color: var(--state-awaiting); border-color: rgba(247,118,142,0.45); background: rgba(247,118,142,0.08); margin-right: var(--space-4); }
.input-btn-stop:hover:not([disabled]) { background: rgba(247,118,142,0.14); border-color: rgba(247,118,142,0.65); }
.input-btn-stop[disabled] { opacity: 0.45; cursor: not-allowed; }
.input-btn-primary { background: var(--state-processing); color: var(--bg); border-color: transparent; font-weight: 500; }
.input-btn-primary:hover { background: var(--state-processing); filter: brightness(1.1); }
.input-btn-primary kbd { background: rgba(0,0,0,0.2); border: 1px solid rgba(0,0,0,0.3); color: inherit; font-family: ui-monospace, "SFMono-Regular", monospace; padding: 0 var(--space-2); border-radius: 3px; }

.input-chip { font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
.chip-caret { color: var(--text-muted); margin-left: var(--space-1); }

.input-status { display: flex; align-items: center; gap: var(--space-4); padding: var(--space-4) 0 0; margin-top: var(--space-3); border-top: 1px solid var(--rule); font-size: 11px; color: var(--text-muted); flex-wrap: wrap; }
.input-status .cwd, .input-status .branch { font-family: ui-monospace, "SFMono-Regular", monospace; }
.input-status .cwd { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.input-status .status-spacer { flex: 1; }
.input-status .context { display: inline-flex; align-items: center; gap: var(--space-3); }
.input-status .context-bar { width: 80px; height: 3px; background: var(--bg); border-radius: 2px; overflow: hidden; }
.input-status .context-fill { display: block; height: 100%; background: var(--state-processing); }
.input-status .context-numbers { font-family: ui-monospace, "SFMono-Regular", monospace; }
.input-status .cost { font-family: ui-monospace, "SFMono-Regular", monospace; }

.context { display: inline-flex; align-items: center; gap: var(--space-3); }
.context-bar { display: inline-block; width: 80px; height: 2px; background: var(--rule); vertical-align: middle; overflow: hidden; }
…
.fork-dialog { margin-bottom: var(--space-6); padding: var(--space-4) var(--space-5); background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 8px; max-width: 480px; margin-left: auto; }
.fork-dialog-title { font-weight: 500; color: var(--text); font-size: 13px; margin-bottom: var(--space-3); }
.fork-dialog-body { color: var(--text-muted); font-size: 12.5px; line-height: 1.55; margin-bottom: var(--space-4); }
.fork-dialog-label { color: var(--text-muted); font-size: 12px; }
.fork-dialog-input { background: transparent; border: 1px solid var(--rule); border-radius: 3px; padding: var(--space-2) var(--space-3); color: var(--text); font: inherit; font-size: 12px; width: 220px; margin-left: var(--space-3); outline: none; }
.fork-dialog-input:focus { border-color: var(--accent); }
.fork-dialog-actions { display: flex; gap: var(--space-4); align-items: center; margin-top: var(--space-4); font-size: 12px; }
.fork-cancel { background: transparent; border: none; color: var(--text-muted); cursor: pointer; font: inherit; padding: var(--space-2) var(--space-3); }
.fork-confirm { margin-left: auto; padding: var(--space-3) var(--space-4); background: var(--accent); color: var(--bg); border: none; border-radius: 4px; font-weight: 500; cursor: pointer; font: inherit; }
```

**Decisions:**
- `box-shadow: 0 0 0 4px rgba(...)` (running-dot halo) → `var(--space-2)` (4) — the halo offset is rhythm.
- `width: 220px` (fork-dialog-input) **stays literal** — input width is a layout sizing decision.
- `width: 260px` (input-status cwd) **stays literal**.
- `width: 80px` (context-bar) **stays literal**.
- `width: 7px; height: 7px` (running-dot) **stays literal** — icon sizing.
- `padding: 6px 14px` (fork-confirm) → `var(--space-3) var(--space-4)`.

- [ ] **Step 3: Verify**

Run "How to Verify" against screenshot 03 (composer at the bottom). Verify: composer height unchanged, status row gaps look like before, fork dialog (if you can trigger one) looks unchanged.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate composer spacing to --space-* tokens

Pass 3 — composer attachments + queue preview, .input-card, .input-controls,
.input-btn family, .input-status, .context*, .fork-dialog*. Fixed widths
(input cwd, fork dialog input, context bar) stay literal as layout
geometry. Running-dot 4px halo becomes --space-2.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Migrate Spawn, Settings, Credentials, Picker, Mobile, and Tail Spacing

The remaining surfaces, batched. Each is small individually; together they cover the rest of the file (~lines 598-1036).

**Files:**
- Modify: `cmd/serf-hub/assets/style.css:598-1036`. Specifically:
  - Spawn surface (598-663): `.spawn-pane`, `.spawn-form`, `.spawn-prompt`, `.spawn-subtitle`, `.spawn-chips`, `.chip`, `.chip-label/value/caret/mode`, `.chip-picker*`, `.spawn-input*`, `.spawn-attach-row/btn`, `.spawn-advanced*`, `.spawn-actions`, `.spawn-btn`, `.spawn-recent*`
  - Pickers (666-686): `.chip-picker-wide`, `.chip-picker-search`, `.chip-picker-body`, providers/models, `.chip-picker-dir*`, `.chip-picker-empty`
  - Search palette (688-727): `.search-dialog*`, `.search-row*`, `.search-section-header`, `.search-empty`, `.search-cmd*`, `.search-help*`, `.search-hit`, palette-error
  - Settings (729-810): `.settings-pane`, `.settings-nav*`, `.settings-content`, `.settings-h2/h3`, `.settings-help`, `.settings-form`, `.settings-list/dl/dt/dd`, `.settings-rows`, `.settings-row*`, `.settings-error`, `.status-pill.*`, `.settings-launch-form*`, model/dir picker widgets (`sp-*`), `.settings-add-row`, `.settings-items-list`
  - Title action + details panel + tasks + credentials + optimistic (812-1036): `.title-action`, `.details-panel*`, `.details-loading`, `.details-list*`, `.conversation-empty`, `.tasks-*`, `.task-row*`, `.task-status-*`, `.task-detail*`, `.panel-toggle-badge`, `#mobile-hamburger`, mobile media block, `.credentials-*`, `.optimistic-*`

This task is large by line count but the substitution rule per literal is mechanical (use the value-mapping reference). Recommend doing it as **three sub-commits** so review stays sane.

- [ ] **Step 1: Read context**

Read lines 598-1036.

- [ ] **Step 2a: Migrate spawn surface (lines 598-663)**

Use Edit per rule. The mappings (literals → tokens):

| Rule | Literal | Token |
| --- | --- | --- |
| `.spawn-pane` | `padding: 48px 64px;` | `padding: var(--space-8) var(--space-9);` |
| `.spawn-form` | `gap: 14px;` | `gap: var(--space-4);` (14→12; visually <2px) |
| `.spawn-chips` | `gap: 8px; margin-top: 4px;` | `gap: var(--space-3); margin-top: var(--space-2);` |
| `.chip` | `gap: 8px; padding: 5px 11px;` | `gap: var(--space-3); padding: var(--space-2) var(--space-4);` (5→4, 11→12) |
| `.chip-picker` | `padding: 4px 0;` | `padding: var(--space-2) 0;` |
| `.chip-picker-option` | `padding: 6px 12px;` | `padding: var(--space-3) var(--space-4);` |
| `.spawn-input` | `padding: 18px;` | `padding: var(--space-5);` (18→16) |
| `.spawn-attach-row` | `gap: 8px;` | `gap: var(--space-3);` |
| `.spawn-attach-btn` | `gap: 4px; padding: 4px 10px;` | `gap: var(--space-2); padding: var(--space-2) var(--space-4);` |
| `.spawn-advanced summary` | `padding: 5px 0;` | `padding: var(--space-2) 0;` |
| `.spawn-advanced-body` | `gap: 14px; padding: 12px 0;` | `gap: var(--space-4); padding: var(--space-4) 0;` |
| `.spawn-advanced-note` | `margin: 0 0 4px;` | `margin: 0 0 var(--space-2);` |
| `.spawn-advanced-group` | `padding: 10px 12px;` `gap: 12px;` | `padding: var(--space-4) var(--space-4); gap: var(--space-4);` |
| `.spawn-advanced-group legend` | `padding: 0 6px;` | `padding: 0 var(--space-3);` |
| `.spawn-advanced-body label` | `gap: 4px;` | `gap: var(--space-2);` |
| `.spawn-advanced-body input...` | `padding: 4px 8px;` | `padding: var(--space-2) var(--space-3);` |
| `.spawn-advanced-row` | `gap: 6px;` | `gap: var(--space-3);` |
| `.spawn-advanced-model` | `gap: 8px;` | `gap: var(--space-3);` |
| `.spawn-advanced-radio` | `gap: 12px;` | `gap: var(--space-4);` |
| `.spawn-advanced-radio label` | `gap: 4px;` | `gap: var(--space-2);` |
| `.launch-radio-composite` | `gap: 8px;` | `gap: var(--space-3);` |
| `.launch-radio-composite .launch-radio-option` | `gap: 6px;` | `gap: var(--space-3);` |
| `.launch-radio-composite .launch-radio-option-with-control > input[type=radio]` | `margin-top: 7px;` | `margin-top: var(--space-3);` (7→8) |
| `.launch-radio-option-body` | `gap: 5px;` | `gap: var(--space-2);` |
| `.spawn-advanced-list-control` | `gap: 6px;` | `gap: var(--space-3);` |
| `.spawn-advanced-add-controls` | `gap: 6px;` | `gap: var(--space-3);` |
| `.spawn-advanced-list` | `gap: 4px;` | `gap: var(--space-2);` |
| `.spawn-advanced-list li` | `gap: 8px; padding: 4px 8px;` | `gap: var(--space-3); padding: var(--space-2) var(--space-3);` |
| `.spawn-advanced-list li button` | `padding: 0 4px;` | `padding: 0 var(--space-2);` |
| `.spawn-advanced-chips` | `gap: 4px;` | `gap: var(--space-2);` |
| `.spawn-advanced-chips li` | `gap: 4px; padding: 2px 8px;` | `gap: var(--space-2); padding: var(--space-1) var(--space-3);` |
| `.spawn-advanced-chips li button` | `padding: 0 4px;` | `padding: 0 var(--space-2);` |
| `.spawn-advanced-actions` | `gap: 8px;` | `gap: var(--space-3);` |
| `.spawn-btn` | `padding: 7px 18px;` | `padding: var(--space-3) var(--space-5);` |
| `.spawn-btn kbd` | `margin-left: 4px;` | `margin-left: var(--space-2);` |
| `.spawn-recent` | `margin-top: 32px;` | `margin-top: var(--space-7);` |
| `.spawn-recent-header` | `margin-bottom: 8px;` | `margin-bottom: var(--space-3);` |
| `.spawn-recent-row` | `padding: 6px 10px;` | `padding: var(--space-3) var(--space-4);` |
| `.fork-confirm kbd` | `margin-left: 4px;` | `margin-left: var(--space-2);` |
| `.image-lightbox` already covered in Task 6 | — | — |

Then commit:
```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate spawn surface spacing to --space-* tokens

Pass 3 — .spawn-pane, .spawn-form, .spawn-chips, .chip, .chip-picker,
.spawn-input, .spawn-attach-*, .spawn-advanced-* (form + groups + lists
+ chips + radios), .spawn-btn, .spawn-recent. 18px input padding rounds
to --space-5 (16); 7px radio margin rounds to --space-3 (8).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 2b: Migrate picker + search palette spacing (lines 666-727)**

| Rule | Literal | Token |
| --- | --- | --- |
| `.chip-picker-search` | `padding: 8px 12px;` | `padding: var(--space-3) var(--space-4);` |
| `.chip-picker-providers` | `padding: 4px 0;` | `padding: var(--space-2) 0;` |
| `.chip-picker-provider` | `padding: 6px 12px;` | `padding: var(--space-3) var(--space-4);` |
| `.chip-picker-models` | `padding: 4px 0;` | `padding: var(--space-2) 0;` |
| `.chip-picker-model` | `padding: 6px 12px;` | `padding: var(--space-3) var(--space-4);` |
| `.chip-picker-model-meta` | `margin-top: 2px;` | `margin-top: var(--space-1);` |
| `.chip-picker-results` | `padding: 4px 0;` | `padding: var(--space-2) 0;` |
| `.chip-picker-dir-row` | `gap: 8px; padding: 6px 12px;` | `gap: var(--space-3); padding: var(--space-3) var(--space-4);` |
| `.chip-picker-dir-tag` | `padding: 1px 6px;` | `padding: var(--space-1) var(--space-3);` |
| `.chip-picker-empty` | `padding: 24px;` | `padding: var(--space-6);` |
| `.search-dialog-inner` | `margin: 80px auto 0;` | `margin: var(--space-8) auto 0;` (80 rounds to 48? — **stays literal**, this is a vertical drop offset, not rhythm. Document.) |
| `.search-dialog-header` | `padding: 14px 16px; gap: 10px;` | `padding: var(--space-4) var(--space-5); gap: var(--space-4);` |
| `.search-results` | `padding: 6px 0;` | `padding: var(--space-3) 0;` |
| `.search-section-header` | `padding: 4px 16px; margin-top: 6px;` | `padding: var(--space-2) var(--space-5); margin-top: var(--space-3);` |
| `.search-row` | `gap: 10px; padding: 8px 16px;` | `gap: var(--space-4); padding: var(--space-3) var(--space-5);` |
| `.search-empty` | `padding: 24px;` | `padding: var(--space-6);` |
| `.palette-error` | `margin: 6px 12px 4px; padding: 8px 12px;` | `margin: var(--space-3) var(--space-4) var(--space-2); padding: var(--space-3) var(--space-4);` |
| `.search-dialog-footer` | `padding: 8px 16px; gap: 18px;` | `padding: var(--space-3) var(--space-5); gap: var(--space-5);` |
| `.search-row-insession .search-insession-glyph` `width: 14px` | — | stays literal (icon width) |
| `.search-cmd-pill` | `gap: 6px; padding: 2px 6px 2px 8px;` | `gap: var(--space-3); padding: var(--space-1) var(--space-3) var(--space-1) var(--space-3);` |
| `.search-cmd-pill-back` | `padding: 0 2px;` | `padding: 0 var(--space-1);` |
| `.search-empty code` | `padding: 0 4px;` | `padding: 0 var(--space-2);` |
| `.search-help-row` | `gap: 12px; padding: 6px 16px;` | `gap: var(--space-4); padding: var(--space-3) var(--space-5);` |

**Special case `.search-dialog-inner margin: 80px auto 0`:** 80px is the visual "drop" from the top of the viewport so the palette feels centered-ish. Stays literal — round-up to `--space-8` (48) would change the visual position. Document in commit.

Commit:
```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate picker + search palette spacing to --space-* tokens

Pass 3 — .chip-picker-* (providers/models/dir/empty), .search-dialog-*,
.search-row, .search-results, .search-cmd-pill, .search-help-row,
.palette-error. The 80px top margin on .search-dialog-inner stays
literal — it's a viewport-relative visual drop, not row rhythm.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 2c: Migrate settings, credentials, tasks, mobile, optimistic (lines 729-1036)**

Settings:

| Rule | Literal | Token |
| --- | --- | --- |
| `.settings-nav` | `width: 200px; padding: 24px 0;` | `width: 200px; padding: var(--space-6) 0;` (200px stays — sidebar nav width) |
| `.settings-nav-section` | `padding: 14px 20px 4px;` | `padding: var(--space-4) var(--space-6) var(--space-2);` |
| `.settings-nav-link` | `padding: 6px 20px;` | `padding: var(--space-3) var(--space-6);` |
| `.settings-content` | `padding: 32px 56px;` | `padding: var(--space-7) var(--space-8);` (56→48, 4px shift) |
| `.settings-h2` | `margin: 0 0 12px;` | `margin: 0 0 var(--space-4);` |
| `.settings-help` | `margin: 0 0 24px;` | `margin: 0 0 var(--space-6);` |
| `.settings-help code` | `padding: 1px 6px;` | `padding: 1px var(--space-3);` |
| `.settings-h3` | `margin: 28px 0 10px;` | `margin: var(--space-7) 0 var(--space-4);` (28→32, 10→12) |
| `.settings-form` | `gap: 12px;` | `gap: var(--space-4);` |
| `.settings-radio, .settings-toggle` | `gap: 10px;` | `gap: var(--space-4);` |
| `.settings-list` | `gap: 8px 24px;` | `gap: var(--space-3) var(--space-6);` |
| `.settings-rows` | `gap: 1px;` | `gap: 1px;` (hairline rule, stays literal) |
| `.settings-row` | `padding: 12px 16px;` | `padding: var(--space-4) var(--space-5);` |
| `.settings-row-meta` | `margin-top: 2px;` | `margin-top: var(--space-1);` |
| `.settings-row-detail` | `margin-top: 8px; gap: 6px;` | `margin-top: var(--space-3); gap: var(--space-3);` |
| `.model-chip` | `padding: 2px 8px;` | `padding: var(--space-1) var(--space-3);` |
| `.settings-row-version` | `margin-left: 8px;` | `margin-left: var(--space-3);` |
| `.settings-row-title .status-pill` | `margin-left: 8px; padding: 1px 8px;` | `margin-left: var(--space-3); padding: 1px var(--space-3);` |
| `.settings-launch-form` | `gap: 10px; margin-bottom: 24px;` | `gap: var(--space-4); margin-bottom: var(--space-6);` |
| `.settings-launch-form label` | `gap: 4px;` | `gap: var(--space-2);` |
| `.settings-launch-form label > input/select/textarea` | `padding: 6px 8px;` | `padding: var(--space-3) var(--space-3);` |
| `.settings-launch-form label.checkbox-label` | `gap: 8px;` | `gap: var(--space-3);` |
| `.settings-launch-form .form-actions` | `margin-top: 6px; gap: 12px;` | `margin-top: var(--space-3); gap: var(--space-4);` |
| `.sp-model-wrap, .sp-dir-wrap` | `gap: 6px;` | `gap: var(--space-3);` |
| `.sp-model-btn, .sp-dir-btn` | `gap: 6px; padding: 5px 10px;` | `gap: var(--space-3); padding: var(--space-2) var(--space-4);` |
| `.sp-clear-btn` | `padding: 0 4px;` | `padding: 0 var(--space-2);` |
| `.settings-add-row` | `gap: 6px; margin-top: 6px;` | `gap: var(--space-3); margin-top: var(--space-3);` |
| `.settings-items-list` | `margin: 0 0 4px; gap: 4px;` | `margin: 0 0 var(--space-2); gap: var(--space-2);` |
| `.settings-items-list li` | `gap: 8px; padding: 4px 0;` | `gap: var(--space-3); padding: var(--space-2) 0;` |
| `.settings-items-list li button` | `padding: 2px 7px;` | `padding: var(--space-1) var(--space-3);` |
| `.title-action` | `padding: 0 4px;` | `padding: 0 var(--space-2);` |

Details panel + tasks:

| Rule | Literal | Token |
| --- | --- | --- |
| `.details-panel` | `width: 360px; padding: 18px 20px;` | `width: 360px; padding: var(--space-5) var(--space-6);` (width stays — drawer geometry) |
| `.details-panel-header` | `padding-bottom: 12px;` | `padding-bottom: var(--space-4);` |
| `.details-loading` | `padding: 12px 0;` | `padding: var(--space-4) 0;` |
| `.details-list` | `gap: 6px 12px; margin: 12px 0 0;` | `gap: var(--space-3) var(--space-4); margin: var(--space-4) 0 0;` |
| `.conversation-empty` | `padding: 32px 0;` | `padding: var(--space-7) 0;` |
| `.tasks-summary` | `margin: 12px 0 6px;` | `margin: var(--space-4) 0 var(--space-3);` |
| `.tasks-empty` | `padding: 12px 0;` | `padding: var(--space-4) 0;` |
| `.tasks-list` | `margin: 8px 0 0; gap: 6px;` | `margin: var(--space-3) 0 0; gap: var(--space-3);` |
| `.task-row` | `gap: 6px; padding: 6px 8px;` | `gap: var(--space-3); padding: var(--space-3) var(--space-3);` |
| `.tasks-list .task-row-details > summary` | `gap: 6px; padding: 6px 8px;` | `gap: var(--space-3); padding: var(--space-3) var(--space-3);` |
| `.tasks-list .task-detail` | `padding: 4px 12px 10px 38px; gap: 4px 10px;` | `padding: var(--space-2) var(--space-4) var(--space-4) var(--space-8); gap: var(--space-2) var(--space-4);` (38→48; 4px shift acceptable on detail-only block) |
| `.tasks-list .task-notes-list` | `gap: 4px;` | `gap: var(--space-2);` |
| `.tasks-list .task-note` | `gap: 4px;` | `gap: var(--space-2);` |
| `.tasks-list .task-type-pill` | `padding: 1px 6px;` | `padding: var(--space-1) var(--space-3);` |
| `.panel-toggle-badge` | `padding: 1px 6px; margin-left: 4px;` | `padding: var(--space-1) var(--space-3); margin-left: var(--space-2);` |

Credentials + optimistic:

| Rule | Literal | Token |
| --- | --- | --- |
| `.credentials-pane` | `padding: 1rem 1.25rem;` | `padding: var(--space-5) var(--space-5);` (1rem=16, 1.25rem=20 → both --space-5; tiny shift on right side) |
| `.credentials-row` | `padding: .5rem 0;` | `padding: var(--space-3) 0;` |
| `.credentials-row-main` | `gap: 1rem;` | `gap: var(--space-5);` |
| `.credentials-row-actions button` | `margin-left: .25rem;` | `margin-left: var(--space-2);` |
| `.credentials-editor` | `gap: 6px; margin-top: 8px; padding: 8px 12px;` | `gap: var(--space-3); margin-top: var(--space-3); padding: var(--space-3) var(--space-4);` |
| `.credentials-editor-input` | `padding: 6px 8px;` | `padding: var(--space-3) var(--space-3);` |
| `.credentials-editor-actions` | `gap: 6px;` | `gap: var(--space-3);` |
| `.optimistic-failed` | `padding-left: 8px;` | `padding-left: var(--space-3);` |
| `.optimistic-failed-reason` | `margin-top: 4px;` | `margin-top: var(--space-2);` |
| `.optimistic-retry` | `margin-left: 8px;` | `margin-left: var(--space-3);` |

Mobile media block (lines 875-986):

| Rule | Literal | Token |
| --- | --- | --- |
| `#sidebar` (mobile) | `width: 80vw; max-width: 320px;` | stays literal (geometry) |
| `#mobile-hamburger` | `top: 8px; left: 8px; width: 36px; height: 36px; font-size: 18px;` | `top: var(--space-3); left: var(--space-3); width: 36px; height: 36px; font-size: 18px;` (width/height stay — touch target) |
| `.workspace-header` (mobile) | `padding: 10px 14px 8px 56px;` | `padding: var(--space-4) var(--space-4) var(--space-3) var(--space-8);` (10→12, 14→12, 8→8, 56→48; documented) |
| `.workspace-title-row` (mobile) | `gap: 8px;` | `gap: var(--space-3);` |
| `.workspace-actions` (mobile) | `gap: 4px;` | `gap: var(--space-2);` |
| `.header-action, .panel-toggle` (mobile) | `padding: 4px 8px;` | `padding: var(--space-2) var(--space-3);` |
| `.workspace-empty` (mobile) | `padding-top: 64px;` | `padding-top: var(--space-9);` |
| `.details-panel` (mobile) | `padding: 14px;` | `padding: var(--space-4);` |
| `.workspace-input` (mobile) | `padding: 8px 12px 10px;` | `padding: var(--space-3) var(--space-4) var(--space-4);` |
| `.input-controls` (mobile) | `gap: 6px;` | `gap: var(--space-3);` |
| `.search-results` (mobile) | `padding-bottom: calc(16px + env(safe-area-inset-bottom));` | `padding-bottom: calc(var(--space-5) + env(safe-area-inset-bottom));` |
| `.search-row` (mobile) | `min-height: 48px; padding: 12px 14px;` | `min-height: 48px; padding: var(--space-4) var(--space-4);` (min-height stays — touch target) |
| `.conversation` (mobile) | `padding: 12px 14px;` | `padding: var(--space-4) var(--space-4);` |

Commit:
```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate settings/credentials/tasks/mobile spacing to --space-* tokens

Pass 3 — .settings-* (nav, content, h2/h3, form, list, rows, launch-form,
add-row, items-list, sp-model/dir picker widgets), .title-action,
.details-panel + .details-list, .tasks-* (.task-row, .task-detail,
notes), .panel-toggle-badge, .credentials-* (pane, row, editor),
.optimistic-* (failed, retry), mobile media block (hamburger, header,
workspace-input, search, conversation). Width/min-height values that
represent geometry (sidebar 200px, drawer 360px, touch target 48px)
stay literal.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 3: Verify**

Run "How to Verify" against ALL 10 baseline screenshots. Settings-general, settings-launch, credentials, search palette, mobile sidebar overlay, mobile conversation — all should match pixel-equivalent.

- [ ] **Step 4: Done — proceed to Task 9**

---

## Task 9: Migrate Border-Radius Literals to `--radius-*`

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` — every line containing a `border-radius:` literal.

- [ ] **Step 1: Read context**

```bash
grep -nE "border-radius:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expect ~50 matches.

- [ ] **Step 2: Apply replacements**

Per the value-mapping reference. Most are mechanical. Use Edit per occurrence (or one `replace_all` per unique literal string to be safe — `2px` etc. as standalone radii are unlikely to false-match because radii always follow `border-radius:`).

Recommended approach: for each literal, do a `replace_all` of the exact string `border-radius: Npx` → `border-radius: var(--radius-XX);`. The literal value with its preceding key + colon + space + closing semicolon is unique enough.

Mapping (apply all):

| Find (exact) | Replace with |
| --- | --- |
| `border-radius: 2px;` | `border-radius: var(--radius-sm);` |
| `border-radius: 3px;` | `border-radius: var(--radius-sm);` |
| `border-radius: 4px;` | `border-radius: var(--radius-md);` |
| `border-radius: 6px;` | `border-radius: var(--radius-lg);` |
| `border-radius: 8px;` | `border-radius: var(--radius-xl);` |
| `border-radius: 10px;` | `border-radius: var(--radius-pill);` |
| `border-radius: 12px;` | `border-radius: var(--radius-pill);` |
| `border-radius: 14px;` | `border-radius: var(--radius-pill);` |
| `border-radius: 50%;` | `border-radius: var(--radius-full);` |
| `border-radius: 0;` | (stays — explicit zero, used in mobile search-dialog-inner) |

**Edge cases:**
- `border-radius: 8px` on `.search-cmd-pill` mobile override (line ~975) → `var(--radius-xl)` per the rule. The mobile override now resolves to 8 (was 8) — no shift.
- `.user-image-card border-radius: 8px` → `--radius-xl`.
- `.input-status .context-bar border-radius: 2px` and `.context-bar border-radius:` may not exist — verify.

- [ ] **Step 3: Verify**

```bash
grep -nE "border-radius:\s+\d" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: zero matches. Only `border-radius: var(...)` or `border-radius: 0` or `border-radius: 50%` (if any were missed by the `replace_all`) remain.

```bash
grep -nE "border-radius:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css | grep -v "var(--radius-"
```

Expected: at most lines containing `border-radius: 0` (mobile search-dialog full-screen override).

Run "How to Verify" against all 10 baseline screenshots. Radii are 1px-2px shifts at most (2→3, 3→3, 10→14 are the biggest). Spec explicitly accepts the 10/12 → 14 collapse on status pills.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate border-radius literals to --radius-* tokens

Pass 3 — every border-radius pixel literal in style.css mapped to
--radius-sm/md/lg/xl/pill/full per the design language §1.4. Stray
10px and 12px radii collapse to --radius-pill (14) — visually
indistinguishable at status-pill scale and called out in the spec.
2px collapses to --radius-sm (3). 50% becomes --radius-full.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Migrate Transition + Animation Durations to `--motion-*`

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` — every line containing `transition:`, `animation:`, or `@keyframes`-adjacent timing.

- [ ] **Step 1: Read context**

```bash
grep -nE "(transition:|animation:|\b\d+\.?\d*m?s\b)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expect: `transition: color 0.1s, background 0.1s` (project-new-btn, project-gear-btn); `transition: border-color 0.15s` (input-card); `transition: background 0.1s, color 0.1s, border-color 0.1s` (panel-toggle); `transition: background 0.1s, color 0.1s` (header-action); `transition: transform 0.2s ease` (mobile sidebar); `transition: transform 0.15s` (task-row-chevron); `transition: outline-color 0.4s ease-out` (search-hit); `animation: search-hit-flash 2s ease-out` (search-hit); `animation: optimistic-pulse 1.4s ease-in-out infinite` (optimistic-pending).

- [ ] **Step 2: Apply replacements**

| Rule | Literal | After |
| --- | --- | --- |
| `.project-new-btn`, `.project-gear-btn` | `transition: color 0.1s, background 0.1s;` | `transition: color var(--motion-fast), background var(--motion-fast);` |
| `.panel-toggle` | `transition: background 0.1s, color 0.1s, border-color 0.1s;` | `transition: background var(--motion-fast), color var(--motion-fast), border-color var(--motion-fast);` |
| `.header-action` | `transition: background 0.1s, color 0.1s;` | `transition: background var(--motion-fast), color var(--motion-fast);` |
| `.input-card` | `transition: border-color 0.15s;` | `transition: border-color var(--motion-fast);` |
| `#sidebar` (mobile) | `transition: transform 0.2s ease;` | `transition: transform var(--motion-base);` (token already includes `ease`) |
| `.tasks-list .task-row-chevron` | `transition: transform 0.15s;` | `transition: transform var(--motion-fast);` |
| `.search-hit` | `transition: outline-color 0.4s ease-out; animation: search-hit-flash 2s ease-out;` | `transition: outline-color var(--motion-slow); animation: search-hit-flash var(--flash-cycle);` (token includes `ease-out`) |
| `.optimistic-pending` | `animation: optimistic-pulse 1.4s ease-in-out infinite;` | `animation: optimistic-pulse var(--pulse-cycle) infinite;` (token includes `ease-in-out`) |

**`@keyframes` blocks (definitions, not durations):** `@keyframes search-hit-flash`, `@keyframes optimistic-pulse` stay as-is — they define keyframe shape, not duration. The duration is on the consuming `animation:` declaration.

**Background reference colors with rgba(122,162,247,...) etc.:** these are not motion; leave alone. Color migration belongs to a separate pass.

- [ ] **Step 3: Verify**

```bash
grep -nE "(transition:|animation:)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css | grep -v "var(--motion-\|var(--pulse-cycle\|var(--flash-cycle"
```

Expected: zero matches. Only token-using transitions/animations remain.

Run "How to Verify" against all 10 screenshots. Static screenshots will be identical. Interactively: hover state changes feel the same (100ms ≈ 100ms); the running-dot pulse and search-hit flash keep their cadence.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate transitions and animations to --motion-* tokens

Pass 3 — every transition/animation duration in style.css mapped to
--motion-fast/base/slow per design language §1.5. The infinite pulse
animations (.optimistic-pending, .running-dot via :hover, search-hit)
use the semantic --pulse-cycle and --flash-cycle tokens. Tokens carry
their own easing keyword so the duplicate "ease" suffixes are dropped
from the consuming declarations.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Migrate Z-Index Literals to `--z-*`

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` — every line containing `z-index:`.

- [ ] **Step 1: Read context**

```bash
grep -nE "z-index:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expect: `.image-lightbox z-index: 200`; `.chip-picker z-index: 50`; `.details-panel z-index: 100`; `#sidebar z-index: 200` (mobile); `body[data-sidebar-open]::before z-index: 199` (mobile scrim); `#mobile-hamburger z-index: 150`; `.search-dialog-header z-index: 1` (mobile sticky).

- [ ] **Step 2: Apply replacements**

| Rule | Literal | Token |
| --- | --- | --- |
| `.search-dialog-header` (mobile, sticky inside scroll) | `z-index: 1;` | stays literal `1` (sticky within scroll container — `--z-sticky` is for that, but the design language says `1` stays literal for "sticky inside scroll"). Use `var(--z-sticky)` if engineer prefers — the token is `10` so resolves higher than `1`; **prefer the token** for consistency. Use `z-index: var(--z-sticky);`. |
| `.chip-picker` | `z-index: 50;` | `z-index: var(--z-dropdown);` (200) |
| `.details-panel` | `z-index: 100;` | `z-index: var(--z-fixed-action);` (100) |
| `#mobile-hamburger` | `z-index: 150;` | `z-index: var(--z-fixed-action);` (100; rounds down per spec) |
| `body[data-sidebar-open]::before` (mobile scrim) | `z-index: 199;` | `z-index: var(--z-overlay);` (800) |
| `#sidebar` (mobile, slide-in) | `z-index: 200;` | `z-index: var(--z-drawer);` (900) |
| `.image-lightbox` | `z-index: 200;` | `z-index: var(--z-drawer);` (900) — semantically a backdrop'd overlay; using `--z-drawer` since it isn't a modal dialog. |

**Stacking consistency check:** after these replacements, the layer order is:
- sticky: 10 (`.search-dialog-header` mobile)
- fixed-action: 100 (`.details-panel`, `#mobile-hamburger`)
- dropdown: 200 (`.chip-picker`)
- overlay scrim: 800 (`body[data-sidebar-open]::before`)
- drawer: 900 (`#sidebar` mobile, `.image-lightbox`)

Today's order was:
- sticky: 1 (mobile dialog header)
- 50 (chip-picker)
- 100 (details-panel)
- 150 (hamburger)
- 199 (scrim)
- 200 (sidebar mobile, lightbox)

The new order **changes one relationship**: the `.chip-picker` was at 50, below `.details-panel` at 100. After migration it's at 200, above `.details-panel`. This matches the design language — a popover dropdown belongs above a fixed-action drawer trigger. No current UI shows both at once, so no visible regression expected. Document in commit.

- [ ] **Step 3: Verify**

```bash
grep -nE "z-index:\s+\d" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: zero matches with raw digits. Only `z-index: var(--z-*)` remains.

Run "How to Verify" against screenshots 03 (with a chip-picker open if possible), 07 (search palette), 08 (mobile sidebar overlay). Layering visually unchanged.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: migrate z-index literals to --z-* tokens

Pass 3 — replace 1, 50, 100, 150, 199, 200 with --z-sticky / -dropdown /
-fixed-action / -overlay / -drawer per design language §1.6. The
.chip-picker promotes from z-index 50 to --z-dropdown (200), now above
.details-panel — matches the intended popover-over-drawer stacking;
no current surface shows both at once.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Add `prefers-reduced-motion` Override

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` — append at end of file (after the `@keyframes optimistic-pulse` block, line ~1036).

- [ ] **Step 1: Read context**

Read the last 30 lines of `style.css`:

```bash
tail -30 /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Confirm the file ends with the optimistic-pulse keyframes block.

Confirm the design language rule (§1.5 of the design language doc): one `!important` allowed, paired with the comment forcing it.

- [ ] **Step 2: Append the override**

Use Edit to append after the closing `}` of `@keyframes optimistic-pulse`:

```css
/* Reduced-motion override.
   The ONLY rule allowed to use !important in this file (paired with the
   acknowledgment in the design language §1.5). When the user has
   `prefers-reduced-motion: reduce` set at the OS level, every animation
   and transition caps at 1ms — visually instant but still triggers
   transitionend/animationend handlers. */
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 1ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 1ms !important;
  }
}
```

- [ ] **Step 3: Verify**

Build + run the app (see "How to Verify"). With DevTools open:

1. In Chrome DevTools: Cmd-Shift-P → "Show Rendering" → toggle "Emulate CSS media feature prefers-reduced-motion" to **reduce**.
2. Hover sidebar rows, open/close mobile drawer (Device Mode), trigger a tool call so `.optimistic-pending` flashes briefly.
3. Inspect any element with `transition` or `animation` via "Computed" tab. Confirm `transition-duration: 1ms` and `animation-duration: 1ms`.
4. The running-dot pulse should be visually static (no breathing).
5. The mobile sidebar slide-in should be instant.

Also confirm: `grep -c "!important" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css` returns exactly **3** (the three `!important` declarations inside the new rule). No other `!important` exists in the file (verify with `grep -nv "1ms !important\|count: 1 !important" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css | grep "!important"` — expected: zero matches).

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: add prefers-reduced-motion override

Pass 3 final piece — single @media (prefers-reduced-motion: reduce)
rule caps every animation and transition at 1ms per design language
§1.5. The three !important declarations in this rule are the only
!important uses in style.css, paired with the comment forcing them.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Final Verification (Build + Pixel-Compare + Reduced-Motion)

**Files:** none modified.

- [ ] **Step 1: Read context**

Confirm every prior task committed cleanly:

```bash
cd /home/jesse/git/prime-radiant/serf
git log --oneline | head -15
```

Expect Tasks 2-12 (eleven commits) on top of the pre-Pass-3 tip.

- [ ] **Step 2: Re-capture screenshots**

Build, run, and recapture each of the 10 baseline screenshots in dark and light. Save to `/tmp/pass3-final/`.

- [ ] **Step 3: Pixel-diff against baseline**

For each pair (`/tmp/pass3-baseline/<name>` vs `/tmp/pass3-final/<name>`), do a visual diff. Acceptable differences (document in PR description):

- Workspace header bottom border lifted 2px (12/10 → 12/12 collapse, Task 5).
- Banner/diagnostic left margin moved from 28px → 32px (4px shift, Task 6).
- Settings-content right padding 56px → 48px (4px shift, Task 8c).
- Diagnostic asymmetric padding 10/12/11 collapsed to 12/12/12 (≤2px shift, Task 6).
- Border-radius shifts: 2→3 (chip thumb), 10→14, 12→14 (status pills), 6→6 (no shift), 3→3.
- `.chip-picker` stacks above `.details-panel` (would only appear if both open simultaneously, which doesn't occur).

**Not acceptable:** any text reflows, any control wraps where it didn't before, any focus state visibly different. If you find one, investigate which task introduced it (use `git bisect` against the screenshot set).

- [ ] **Step 4: Verify reduced-motion**

In Chrome DevTools rendering tab, toggle "Emulate CSS media feature prefers-reduced-motion" → **reduce**. Click through the app:

- Sidebar hover transitions: instant (no fade).
- Mobile drawer open: instant (no slide).
- `.optimistic-pending` flash: no animation (the test trigger is to send a message — the user pill should appear instantly without the breathing pulse).
- Search palette open: instant.
- Task-row chevron rotation on toggle: instant.

Confirm via `grep` that exactly three `!important` declarations live in the file (all inside the reduced-motion block):

```bash
grep -n "!important" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: 3 lines, all inside `@media (prefers-reduced-motion: reduce)`.

- [ ] **Step 5: Confirm token coverage**

```bash
grep -cE "var\(--space-" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
grep -cE "var\(--radius-" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
grep -cE "var\(--motion-" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
grep -cE "var\(--z-" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: hundreds of `--space-*`, ~50 `--radius-*`, ~10 `--motion-*` + a few `--pulse-cycle`/`--flash-cycle`, ~7 `--z-*`.

Conversely, residual literals:

```bash
# Spacing literals in padding/margin/gap shorthand — exclude 0, 1px (hairline), and known geometry literals
grep -nE "(padding|margin|gap|top|left|right|bottom):\s*-?\d+px" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css | grep -vE ":\s*(0|1px|220px|260px|260px|320px|360px|480px|520px|560px|640px|680px|720px|760px|80px|36px|48px|44px|32px|18px|14px|12px|7px|6px|18px|22px)\b"
```

Expected: zero or near-zero matches. Any remaining literal MUST be documented in this plan (textarea min-height, sidebar width, settings nav width, etc. — content sizing, not layout rhythm) or be a missed substitution.

```bash
grep -nE "border-radius:\s*\d" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: zero, OR `border-radius: 0` (mobile search-dialog full-screen).

```bash
grep -nE "z-index:\s*\d" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: zero.

```bash
grep -nE "(transition|animation):[^;]*\b\d+\.?\d*m?s\b" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css
```

Expected: zero (every duration is now a token).

- [ ] **Step 6: Commit (optional verification note)**

If verification passed clean, no commit needed — the prior 11 commits stand. If a missed literal turned up, fix it inline:

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "$(cat <<'EOF'
ui: clean up residual layout literals found in Pass 3 verification

Final sweep — <describe what was missed>.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Post-Migration Cleanup (Not in This Plan)

The `--pad: 12px` legacy alias on `:root` (line 33) stays defined after this pass — its usages are gone, but other token-migration passes (color, typography) may still reference it via `--panel`/`--border`/etc. The alias is removed in the final consolidation step of the broader serf-hub UI overhaul (after Pass 6 completes), not here.

---

## Self-Review Notes

- **Spec coverage:**
  - Pass 3 scope §1.3 spacing — Tasks 2-8 cover every `padding/margin/gap/top/right/bottom/left` literal in the file.
  - §1.4 radius — Task 9 covers every `border-radius` literal.
  - §1.5 motion — Task 10 covers every `transition`/`animation` duration; Task 12 adds the `prefers-reduced-motion` override.
  - §1.6 z-index — Task 11 covers every `z-index` literal.
  - Spec call-out about `var(--pad)` collapse — Task 2 (dedicated, called out by name).
  - Verify step in spec — Task 13 (screenshot-diff + reduced-motion DevTools emulation).
- **Placeholder scan:** No "TBD" or "Similar to Task N" — every value mapping is spelled out.
- **Type consistency:** Token names match design language §1.3-§1.6 exactly (`--space-*`, `--radius-*`, `--motion-*`, `--z-*`, `--pulse-cycle`, `--flash-cycle`). Tokens referenced in tasks match the Pass 1 token definitions assumed on `:root`.
