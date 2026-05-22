# Serf Hub UI Pass 2 — Typography Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Switch every selector in `cmd/serf-hub/assets/style.css` that sets `font-size`, `font-family`, or `line-height` to use the typography tokens introduced in Pass 1. After this pass ships, all type in the hub is one of `Hanken Grotesk` (sans, inherited from `body`) or `JetBrains Mono` (explicit `var(--font-mono)`), sized from the `--text-*` scale, with leading from `--leading-*`.

**Scope:** CSS only. No template, JS, or Go changes. Every literal font-size, system-font stack, or numeric line-height becomes a token. The single visual side effect is the actual font swap (system sans → Hanken; `ui-monospace` → JetBrains Mono) and a handful of tiny size rounds (`14.5` → `14`, `12.5` → `12`, `11.5` → `12`, `10.5` → `10`, `9` → `10` where the glyph allows).

**Non-goals:** spacing, radius, motion, z-index migration (Pass 3); button/chip/status refactor (Pass 4); structural template changes; new components. We do not touch any `:focus-visible` rules or rewrite legacy class names.

**Depends on:** Pass 1 — tokens listed below must already exist on `:root` and the Google Fonts link must already be loaded in `app.html`.

**Tech stack:** CSS, manual visual verification in `serf-hub` running locally on `127.0.0.1:9180`.

---

## Reference — Design Language §1.2 voice-by-surface cheatsheet

The boundary between sans and mono is **what the text means**, not which surface it sits on.

- **Sans (Hanken Grotesk)** — prose, UI controls, headings, button labels, navigation, session titles. The human reading the log.
- **Mono (JetBrains Mono)** — code, file paths, model identifiers, branch names, timestamps, kbd hints, key-value metadata, status labels (small-caps), section labels (uppercase + tracked). The log itself.

Type scale (Pass 1 tokens):

| Token | px | Default leading | Typical use |
| --- | --- | --- | --- |
| `--text-2xs` | 10 | `--leading-tight` | Section labels (uppercase, tracked), tiny metadata |
| `--text-xs` | 11 | `--leading-tight` | Row metadata, timestamps, hints, kbd labels, help text |
| `--text-sm` | 12 | `--leading-snug` | Tool calls, diff body, status bar, captions, picker options |
| `--text-base` | 13 | `--leading-snug` | UI body — buttons, settings labels, sidebar titles |
| `--text-md` | 14 | `--leading-normal` | Conversation body (user pill + assistant) |
| `--text-lg` | 16 | `--leading-snug` | Workspace title, page-header h1 |
| `--text-xl` | 18 | `--leading-snug` | Settings h2 |
| `--text-2xl` | 22 | `--leading-snug` | Spawn-prompt heading |

Leading tokens: `--leading-tight: 1.3`, `--leading-snug: 1.5`, `--leading-normal: 1.6`, `--leading-relaxed: 1.7` (reserved).

Voice-by-surface map (paste this on the side of your monitor):

| Surface element | Voice | Token |
| --- | --- | --- |
| Workspace title | sans | `--text-lg / 600` |
| Conversation user pill + assistant body | sans | `--text-md / 400` |
| Tool call (verb + target) | mono | `--text-sm / 400` |
| Diff body | mono | `--text-sm / 400` |
| Inline code in prose (`<code>`) | mono | `0.85em / 400` |
| Sidebar session title | sans | `--text-base / 400` |
| Sidebar meta line (project · age) | mono | `--text-2xs / 350` |
| Sidebar section header (LIVE, PROJECTS) | mono | `--text-2xs / 500` uppercase + `0.16em` |
| Project header name | mono | `--text-2xs / 500` uppercase + `0.14em` |
| Composer textarea | sans | `--text-md / 400` |
| Composer status row (cwd, branch, ctx, cost) | mono | `--text-xs / 400` |
| Status badge text | mono | `--text-xs / 500` uppercase + `0.08em` |
| Spawn prompt question | sans | `--text-2xl / 600 / -0.018em` |
| Spawn chip key | mono | `--text-2xs / 400` |
| Spawn chip value | mono | `--text-xs / 400` |
| Settings table dt (label) | sans | `--text-base / 500` |
| Settings table dd (value) | mono | `--text-sm / 400` |
| Settings table help | sans | `--text-xs / 350` |
| Button label | sans | `--text-sm / 500` (or `--text-base / 600` for primary) |
| kbd hint | mono | `--text-xs / 600` |

**Size-rounding rules for literals already in the file:**

| Literal | Token | Notes |
| --- | --- | --- |
| 9 | `--text-2xs` (10) | Carets / decorative — leave literal where 10px is visually too heavy (`.chip-caret`, `.sp-*-caret`) |
| 10 | `--text-2xs` | |
| 10.5 | `--text-2xs` | Diagnostic badge |
| 11 | `--text-xs` | |
| 11.5 | `--text-sm` (12) | Round up — Hanken at 11.5 is fragile |
| 12 | `--text-sm` | |
| 12.5 | `--text-sm` | Round down to 12 |
| 13 | `--text-base` | |
| 14 | `--text-md` | |
| 14.5 | `--text-md` | Round down |
| 15 | `--text-lg` (16) | Round up |
| 16 | `--text-lg` | |
| 18 | `--text-xl` | |
| 22 | `--text-2xl` | |

**Rule of thumb when adding `font-family`:**
- Elements that already declare `ui-monospace, "SFMono-Regular", monospace` — replace with `var(--font-mono)`. Pure substitution.
- Elements with no `font-family` declaration that carry **mono content** by role (paths, code, models, timestamps, status text, kbd, picker options, sidebar meta, status-badge text) — add `font-family: var(--font-mono)`.
- Everything else — no declaration. Sans inherits from `body`.

**Conversation body sits at `--text-md` (14) initially.** After Pass 2 ships, A/B 13 vs 14 in the live app and pick.

---

## File structure

- Modify `cmd/serf-hub/assets/style.css` — every section migrates one task at a time. No new files.

---

## Task 1: Switch body font-family to sans; add `.mono` utility

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` lines 112–119 (`body` rule) and 248 (`body.app` rule). Append `.mono` utility class immediately after the `body.app` rule (around line 249).

- [ ] **Step 1 — read context.** Confirm lines 112–119 and 248 still match the before-block below. Also confirm `--font-sans` and `--font-mono` are defined on `:root` from Pass 1 (search for `--font-sans:` in the file). If they are missing, stop and re-run Pass 1.

- [ ] **Step 2 — apply migration.**

  **Before** (lines 112–119):
  ```css
  * { box-sizing: border-box; }
  body {
    background: var(--bg);
    color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Helvetica Neue", Arial, sans-serif;
    margin: 0;
    font-size: 14px;
  }
  ```

  **After:**
  ```css
  * { box-sizing: border-box; }
  body {
    background: var(--bg);
    color: var(--text);
    font-family: var(--font-sans);
    margin: 0;
    font-size: var(--text-md);
  }
  ```

  **Before** (line 248):
  ```css
  body.app { display: flex; background: var(--bg); color: var(--text); font-family: ui-sans-serif, -apple-system, "Helvetica Neue", sans-serif; overflow: hidden; }
  ```

  **After:**
  ```css
  body.app { display: flex; background: var(--bg); color: var(--text); font-family: var(--font-sans); overflow: hidden; }
  ```

  **Append `.mono` utility immediately after the `body.app` rule:**
  ```css
  /* Mono utility — for one-off mono spans that don't have a dedicated class. */
  .mono { font-family: var(--font-mono); }
  ```

- [ ] **Step 3 — verify visually.** Build the hub (`make build-hub`) and run it locally (`./serf-hub`). Open `http://127.0.0.1:9180`. Body prose should now render in Hanken Grotesk. Anything that explicitly sets `font-family: ui-monospace, ...` will still be in the system mono (we migrate those in later tasks). Open dev tools, inspect `<body>`, confirm computed `font-family` resolves to `"Hanken Grotesk", -apple-system, ...`.

- [ ] **Step 4 — commit.**
  ```
  ui: switch body font-family to --font-sans; add .mono utility

  Pass 2 of the serf-hub typography migration. Body now inherits Hanken
  Grotesk; .mono is the escape hatch for spans that need explicit mono
  without a dedicated class.
  ```

---

## Task 2: Migrate page-header and workspace-header surfaces

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` lines 120–147 (page-header + section.panel h2), 251–252 (`sidebar-loading`, `workspace-empty`), 333–358 (workspace header + meta + actions + rule-dot + status-pill + conversation rule).

- [ ] **Step 1 — read context.** Re-read lines 120–147 and 333–358. Note `.conversation` at line 357 has `font-size: 15px; line-height: 1.7;` — that's the conversation body. Per spec it sits at `--text-md / --leading-normal`. `.workspace-title .title` is 15px sans — becomes `--text-lg`. `.workspace-meta` is 11.5px (mono cluster per spec) — becomes `--text-xs` + mono.

- [ ] **Step 2 — apply migration.**

  **Before** (lines 128, 147):
  ```css
  .page-header h1 { font-size: 16px; margin: 0; font-weight: 500; }
  …
  section.panel h2 { margin-top: 0; font-size: 14px; color: var(--muted); font-weight: 500; text-transform: uppercase; letter-spacing: 0.05em; }
  ```

  **After:**
  ```css
  .page-header h1 { font-size: var(--text-lg); margin: 0; font-weight: 500; }
  …
  section.panel h2 { margin-top: 0; font-family: var(--font-mono); font-size: var(--text-md); color: var(--muted); font-weight: 500; text-transform: uppercase; letter-spacing: 0.05em; }
  ```

  **Before** (lines 251–252):
  ```css
  .sidebar-loading { padding: 24px; color: var(--text-muted); font-size: 12px; }
  .workspace-empty { padding: 64px; color: var(--text-muted); text-align: center; }
  ```

  **After:**
  ```css
  .sidebar-loading { padding: 24px; color: var(--text-muted); font-size: var(--text-sm); }
  .workspace-empty { padding: 64px; color: var(--text-muted); text-align: center; }
  ```

  **Before** (lines 333–358):
  ```css
  .workspace-header { padding: 12px 24px 10px; border-bottom: 1px solid var(--rule); }
  .workspace-title-row { display: flex; align-items: center; gap: 12px; }
  .workspace-title { display: flex; align-items: baseline; gap: 8px; flex: 1; min-width: 0; }
  .workspace-title .title { font-weight: 500; font-size: 15px; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .workspace-meta { margin-top: 3px; display: flex; align-items: baseline; gap: 8px; color: var(--text-muted); font-size: 11.5px; }
  .workspace-actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
  .panel-toggle { display: inline-flex; align-items: center; gap: 5px; padding: 4px 10px; background: transparent; border: 1px solid transparent; border-radius: 4px; color: var(--text-muted); font-size: 12px; font-family: inherit; cursor: pointer; transition: background 0.1s, color 0.1s, border-color 0.1s; }
  .panel-toggle:hover { color: var(--text); background: var(--bg-raised); border-color: var(--rule); }
  .panel-toggle.active { color: var(--text); background: var(--bg-raised); border-color: var(--rule); }
  .panel-toggle-icon { font-size: 13px; line-height: 1; }
  .panel-toggle-label { font-size: 12px; }
  .header-action { display: inline-flex; align-items: center; padding: 4px 8px; background: transparent; border: none; border-radius: 4px; color: var(--text-muted); font-size: 12px; font-family: inherit; cursor: pointer; transition: background 0.1s, color 0.1s; }
  .header-action:hover:not([disabled]) { color: var(--text); background: var(--bg-raised); }
  .header-action-danger { color: var(--state-awaiting); }
  .header-action-danger:hover:not([disabled]) { color: var(--state-awaiting); background: rgba(247, 118, 142, 0.1); }
  .header-action[disabled] { opacity: 0.4; cursor: not-allowed; }
  .header-action-danger[disabled] { color: var(--text-muted); }
  .workspace-meta .branch,
  .workspace-meta .source-label { font-family: ui-monospace, "SFMono-Regular", monospace; }
  .rule-dot { color: var(--text-dim); }
  .status-pill { display: inline-flex; align-items: baseline; gap: 6px; }
  .conversation { flex: 1; min-height: 0; padding: 32px 64px; overflow-y: auto; font-size: 15px; line-height: 1.7; color: var(--text); }
  ```

  **After:**
  ```css
  .workspace-header { padding: 12px 24px 10px; border-bottom: 1px solid var(--rule); }
  .workspace-title-row { display: flex; align-items: center; gap: 12px; }
  .workspace-title { display: flex; align-items: baseline; gap: 8px; flex: 1; min-width: 0; }
  .workspace-title .title { font-weight: 600; font-size: var(--text-lg); color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .workspace-meta { margin-top: 3px; display: flex; align-items: baseline; gap: 8px; color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
  .workspace-actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
  .panel-toggle { display: inline-flex; align-items: center; gap: 5px; padding: 4px 10px; background: transparent; border: 1px solid transparent; border-radius: 4px; color: var(--text-muted); font-size: var(--text-sm); font-family: inherit; cursor: pointer; transition: background 0.1s, color 0.1s, border-color 0.1s; }
  .panel-toggle:hover { color: var(--text); background: var(--bg-raised); border-color: var(--rule); }
  .panel-toggle.active { color: var(--text); background: var(--bg-raised); border-color: var(--rule); }
  .panel-toggle-icon { font-size: var(--text-base); line-height: 1; }
  .panel-toggle-label { font-size: var(--text-sm); }
  .header-action { display: inline-flex; align-items: center; padding: 4px 8px; background: transparent; border: none; border-radius: 4px; color: var(--text-muted); font-size: var(--text-sm); font-family: inherit; cursor: pointer; transition: background 0.1s, color 0.1s; }
  .header-action:hover:not([disabled]) { color: var(--text); background: var(--bg-raised); }
  .header-action-danger { color: var(--state-awaiting); }
  .header-action-danger:hover:not([disabled]) { color: var(--state-awaiting); background: rgba(247, 118, 142, 0.1); }
  .header-action[disabled] { opacity: 0.4; cursor: not-allowed; }
  .header-action-danger[disabled] { color: var(--text-muted); }
  .workspace-meta .branch,
  .workspace-meta .source-label { font-family: var(--font-mono); }
  .rule-dot { color: var(--text-dim); }
  .status-pill { display: inline-flex; align-items: baseline; gap: 6px; font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 500; letter-spacing: 0.08em; text-transform: uppercase; }
  .conversation { flex: 1; min-height: 0; padding: 32px 64px; overflow-y: auto; font-size: var(--text-md); line-height: var(--leading-normal); color: var(--text); }
  ```

  Notes:
  - `.workspace-title .title` weight bumps from `500` to `600` per the cheatsheet (`--text-lg / 600`).
  - `.workspace-meta` becomes a mono cluster (was implicit because every child was mono); declared at the parent so future children inherit.
  - `.status-pill` gains the typographic small-caps treatment so existing markup gets the new look immediately. Pass 4 replaces the class name with `.status-badge` and removes the rgba background tints on the variants further down; here we just give the bare `.status-pill` the new identity at the base layer.

- [ ] **Step 3 — verify visually.** Reload `http://127.0.0.1:9180`. Open a session: the workspace title is Hanken at 16/600; meta line (src · branch · turn count) is mono at 11. The status pill above the input strip is still a colored rounded thing — fine, we recolor in Pass 4. The conversation body looks slightly smaller (15 → 14) and the line-height tightens 1.7 → 1.6 — verify it still reads.

- [ ] **Step 4 — commit.**
  ```
  ui: migrate header + workspace-header typography to tokens

  Workspace title, meta cluster, panel toggle, header actions, status pill,
  conversation body now use the --text-* and --font-* tokens introduced in
  Pass 1.
  ```

---

## Task 3: Migrate sidebar selectors

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` lines 255–331 (sidebar root, sidebar-header, sidebar-action, section/project headers, rows, dots, meta, fork-original-banner).

- [ ] **Step 1 — read context.** Re-read lines 255–331. Note: today there is no `.sb-row`; today's classes are `.session-row`, `.subagent-row`, `.fork-row`, `.live-row`. Pass 5 introduces `.sb-row`. In Pass 2 we just migrate the existing class typography. The sidebar root sets `font-size: 12px` and everything under it inherits — many child rules don't restate font-size. Per cheatsheet, **session title is sans `--text-base` (13)**, and meta lines are mono `--text-2xs`. We surface this by setting the body type on the row itself and explicit mono on meta.

- [ ] **Step 2 — apply migration.**

  **Before** (lines 255–331):
  ```css
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
  .subagent-row .row-title { flex: 1; }
  .fork-row { color: var(--text-muted); }
  .fork-row .row-title { flex: 1; }
  .fork-glyph { font-family: ui-monospace, "SFMono-Regular", monospace; color: var(--state-ended); width: 6px; }
  .fork-row[data-state="active"] .fork-glyph { color: var(--state-processing); }
  .fork-row[data-state="awaiting"] .fork-glyph { color: var(--state-awaiting); }
  .fork-row[data-state="warning"] .fork-glyph { color: var(--state-warning); }
  .fork-row[data-state="idle"] .fork-glyph { color: var(--state-idle); }
  .live-row .row-title { flex: 1; }
  .status-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--state-ended); flex-shrink: 0; }
  …
  .row-meta { color: var(--text-muted); font-size: 11px; }
  .row-age { color: var(--text-muted); font-size: 11px; margin-left: auto; }
  …
  .project-new-btn { color: var(--text-dim); text-decoration: none; padding: 4px 6px; font-size: 14px; line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; }
  …
  .project-gear-btn { color: var(--text-dim); text-decoration: none; padding: 4px 5px; font-size: 11px; line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; opacity: 0; }
  …
  .fork-original-banner { font-size: 11px; color: var(--text-muted); margin-bottom: 4px; padding: 0; }
  ```

  **After:**
  ```css
  .sidebar { padding: 20px 0; font-size: var(--text-base); color: var(--text); }
  .sidebar-header { padding: 0 20px 14px; display: flex; gap: 18px; }
  .sidebar-action { color: var(--text-muted); text-decoration: none; cursor: pointer; }
  .sidebar-action:hover { color: var(--text); }
  .sidebar-action kbd { color: var(--text-dim); font-family: var(--font-mono); font-size: var(--text-2xs); margin-left: 4px; }
  .sidebar-section { margin-bottom: 4px; }
  .sidebar-section-header,
  .project-header { padding: 14px 20px 4px; color: var(--text-dim); font-family: var(--font-mono); font-size: var(--text-2xs); font-weight: 500; letter-spacing: 0.14em; text-transform: uppercase; display: flex; align-items: baseline; gap: 12px; }
  .sidebar-section-header { letter-spacing: 0.16em; }
  .sidebar-section-header .row-meta,
  .project-header .row-meta { margin-left: auto; letter-spacing: 0; }
  .session-row,
  .subagent-row,
  .fork-row,
  .live-row { display: flex; align-items: baseline; padding: 5px 20px; gap: 9px; color: var(--text); text-decoration: none; cursor: pointer; }
  .session-row { font-weight: 500; }
  .session-row .row-title { flex: 1; }
  .subagent-row { padding-left: 48px; font-size: var(--text-sm); }
  .subagent-row .row-title { flex: 1; }
  .fork-row { color: var(--text-muted); }
  .fork-row .row-title { flex: 1; }
  .fork-glyph { font-family: var(--font-mono); color: var(--state-ended); width: 6px; }
  .fork-row[data-state="active"] .fork-glyph { color: var(--state-processing); }
  .fork-row[data-state="awaiting"] .fork-glyph { color: var(--state-awaiting); }
  .fork-row[data-state="warning"] .fork-glyph { color: var(--state-warning); }
  .fork-row[data-state="idle"] .fork-glyph { color: var(--state-idle); }
  .live-row .row-title { flex: 1; }
  .status-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--state-ended); flex-shrink: 0; }
  …
  .row-meta { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-2xs); letter-spacing: 0.02em; }
  .row-age { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-2xs); margin-left: auto; }
  …
  .project-new-btn { color: var(--text-dim); text-decoration: none; padding: 4px 6px; font-size: var(--text-md); line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; }
  …
  .project-gear-btn { color: var(--text-dim); text-decoration: none; padding: 4px 5px; font-size: var(--text-xs); line-height: 1; cursor: pointer; transition: color 0.1s, background 0.1s; border-radius: 4px; opacity: 0; }
  …
  .fork-original-banner { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-muted); margin-bottom: 4px; padding: 0; }
  ```

  Notes:
  - Sidebar base font-size moves from 12 to `--text-base` (13) so session titles render at the cheatsheet size. Meta and section headers restate their own (smaller) sizes.
  - `.subagent-row` gets explicit `--text-sm` per cheatsheet "sub" treatment.
  - Section + project headers gain `font-family: var(--font-mono)` and tighter tracking; the `--text-2xs` size (10) keeps them quiet.
  - `.row-meta` and `.row-age` get explicit mono — they carry mono content (project · age, timestamps) but didn't say so today.
  - The `…` ranges between rules are unchanged — preserve `.status-dot[data-state="*"]`, `.project-chevron`, `.project-folder`, `.project-name`, `.project-section.collapsed`, `.project-rollup-dot` rules as-is. They have no font properties.

- [ ] **Step 3 — verify visually.** Reload the hub. Sidebar session titles are visibly Hanken at 13. Section headers (LIVE / PROJECTS) and project rows are JetBrains Mono uppercase at 10 — they should read like tiny stamps. Meta lines (project · age) are mono and quiet. Click around between sessions; titles and metadata both render correctly. Verify dark + light themes both look right.

- [ ] **Step 4 — commit.**
  ```
  ui: migrate sidebar typography to tokens

  Session titles sans 13; section + project headers and meta lines mono 10
  with tracked uppercase. Pass 2 prep for the Pass 5 sidebar restructure.
  ```

---

## Task 4: Migrate conversation tier — user pill, assistant body, system-line, steering, banner, diagnostic

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` lines 446–466 (user pill, user message, image lightbox, assistant message + code + pre), 517–571 (subagent-reference + diagnostic + banner + system-line + task-system-detail), 578–585 (steering).

- [ ] **Step 1 — read context.** Re-read lines 446–466 and 517–585. The user pill is sans 14.5 / 1.55 — rounds to `--text-md / --leading-snug`. The assistant body is sans 13 / 1.6 — becomes `--text-base / --leading-normal`, per cheatsheet conversation body. (Note: cheatsheet says `--text-md` for both, but assistant message also has `max-width: 680px`. We bump to `--text-md` to match the user pill and the A/B planned in §137 of the design language; if the live experiment lands on 13, a follow-up moves it back.)

- [ ] **Step 2 — apply migration.**

  **Before** (lines 446–466):
  ```css
  .user-message .pill { max-width: 62%; padding: 8px 14px; background: var(--bg-raised); border-radius: 14px; font-size: 14.5px; color: var(--text); line-height: 1.55; }
  .user-message-actions { position: absolute; right: 0; top: 4px; display: none; gap: 14px; font-size: 11px; color: var(--text-muted); }
  …
  .user-image-name { display: block; padding: 4px 8px; font-size: 11px; color: var(--text-muted); font-family: ui-monospace, "SFMono-Regular", monospace; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 220px; text-align: left; }
  .user-message-text { white-space: pre-wrap; word-wrap: break-word; }
  …
  .image-lightbox-caption { color: #fff; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; opacity: 0.75; }
  .assistant-message { margin-bottom: 24px; max-width: 680px; font-size: 13px; line-height: 1.6; color: var(--text); }
  .assistant-message code { background: var(--bg-raised); padding: 1px 6px; border-radius: 3px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12.5px; color: var(--text); }
  .assistant-message pre { background: var(--bg-raised); padding: 12px 16px; border-radius: 6px; overflow-x: auto; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; }
  ```

  **After:**
  ```css
  .user-message .pill { max-width: 62%; padding: 8px 14px; background: var(--bg-raised); border-radius: 14px; font-size: var(--text-md); color: var(--text); line-height: var(--leading-snug); }
  .user-message-actions { position: absolute; right: 0; top: 4px; display: none; gap: 14px; font-size: var(--text-xs); color: var(--text-muted); }
  …
  .user-image-name { display: block; padding: 4px 8px; font-size: var(--text-xs); color: var(--text-muted); font-family: var(--font-mono); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 220px; text-align: left; }
  .user-message-text { white-space: pre-wrap; word-wrap: break-word; }
  …
  .image-lightbox-caption { color: #fff; font-family: var(--font-mono); font-size: var(--text-sm); opacity: 0.75; }
  .assistant-message { margin-bottom: 24px; max-width: 680px; font-size: var(--text-md); line-height: var(--leading-normal); color: var(--text); }
  .assistant-message code { background: var(--bg-raised); padding: 1px 6px; border-radius: 3px; font-family: var(--font-mono); font-size: 0.85em; color: var(--text); }
  .assistant-message pre { background: var(--bg-raised); padding: 12px 16px; border-radius: 6px; overflow-x: auto; font-family: var(--font-mono); font-size: var(--text-sm); }
  ```

  **Before** (lines 517–571):
  ```css
  .subagent-reference { margin: 0 0 12px 22px; font-size: 12.5px; color: var(--text-muted); cursor: pointer; }
  .subagent-reference .verb { color: var(--state-subagent); font-family: ui-monospace, "SFMono-Regular", monospace; }
  .subagent-reference:hover { color: var(--text); }
  .diagnostic {
    --diagnostic-accent: var(--state-warning);
    margin: 0 0 18px 28px;
    max-width: 720px;
    padding: 10px 12px 11px;
    background: var(--bg-raised);
    border: 1px solid var(--rule);
    border-left: 3px solid var(--diagnostic-accent);
    border-radius: 6px;
    color: var(--text);
    font-size: 12.5px;
    line-height: 1.55;
  }
  .diagnostic-source-provider { --diagnostic-accent: var(--diagnostic-provider); }
  .diagnostic-source-serf { --diagnostic-accent: var(--diagnostic-serf); }
  .diagnostic-source-hub { --diagnostic-accent: var(--diagnostic-hub); }
  .diagnostic-source-ui { --diagnostic-accent: var(--diagnostic-ui); }
  .diagnostic-header { display: flex; align-items: baseline; gap: 9px; margin-bottom: 5px; flex-wrap: wrap; }
  .diagnostic-badge {
    display: inline-flex;
    align-items: center;
    min-height: 18px;
    padding: 1px 6px;
    border: 1px solid var(--diagnostic-accent);
    border-radius: 4px;
    color: var(--diagnostic-accent);
    font-family: ui-monospace, "SFMono-Regular", monospace;
    font-size: 10.5px;
    line-height: 1.4;
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
  …
  .task-system-detail-title { color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
  …
  .task-system-detail dt { color: var(--text-dim); font-family: ui-monospace, "SFMono-Regular", monospace; }
  ```

  **After:**
  ```css
  .subagent-reference { margin: 0 0 12px 22px; font-size: var(--text-sm); color: var(--text-muted); cursor: pointer; }
  .subagent-reference .verb { color: var(--state-subagent); font-family: var(--font-mono); }
  .subagent-reference:hover { color: var(--text); }
  .diagnostic {
    --diagnostic-accent: var(--state-warning);
    margin: 0 0 18px 28px;
    max-width: 720px;
    padding: 10px 12px 11px;
    background: var(--bg-raised);
    border: 1px solid var(--rule);
    border-left: 3px solid var(--diagnostic-accent);
    border-radius: 6px;
    color: var(--text);
    font-size: var(--text-sm);
    line-height: var(--leading-snug);
  }
  .diagnostic-source-provider { --diagnostic-accent: var(--diagnostic-provider); }
  .diagnostic-source-serf { --diagnostic-accent: var(--diagnostic-serf); }
  .diagnostic-source-hub { --diagnostic-accent: var(--diagnostic-hub); }
  .diagnostic-source-ui { --diagnostic-accent: var(--diagnostic-ui); }
  .diagnostic-header { display: flex; align-items: baseline; gap: 9px; margin-bottom: 5px; flex-wrap: wrap; }
  .diagnostic-badge {
    display: inline-flex;
    align-items: center;
    min-height: 18px;
    padding: 1px 6px;
    border: 1px solid var(--diagnostic-accent);
    border-radius: 4px;
    color: var(--diagnostic-accent);
    font-family: var(--font-mono);
    font-size: var(--text-2xs);
    line-height: var(--leading-tight);
  }
  .diagnostic-title { color: var(--text); font-weight: 500; }
  .diagnostic-message { color: var(--text); word-break: break-word; }
  .diagnostic-hint { margin-top: 5px; color: var(--text-muted); word-break: break-word; }

  .banner { margin: 0 0 18px 28px; padding: 6px 12px; font-size: var(--text-sm); border-radius: 4px; }
  .banner.error { color: var(--state-awaiting); border-left: 2px solid var(--state-awaiting); padding-left: 10px; }
  .banner.warning { color: var(--state-warning); border-left: 2px solid var(--state-warning); padding-left: 10px; }
  .banner.note { color: var(--text-muted); border-left: 2px solid var(--rule); padding-left: 10px; }

  .system-line { margin: 8px 0 8px 28px; padding: 4px 0; font-size: var(--text-sm); color: var(--text-muted); font-style: italic; line-height: var(--leading-snug); }
  .task-system-icon { display: inline-block; width: 14px; margin-right: 6px; color: var(--text); font-style: normal; font-family: var(--font-mono); }
  …
  .task-system-detail-title { color: var(--text); font-family: var(--font-mono); font-size: var(--text-xs); }
  …
  .task-system-detail dt { color: var(--text-dim); font-family: var(--font-mono); }
  ```

  **Before** (lines 578–585):
  ```css
  .steering { margin: 18px 0; padding: 0; font-size: 11.5px; color: var(--text-muted); }
  .steering > summary { cursor: pointer; list-style: none; user-select: none; display: flex; align-items: center; gap: 12px; padding: 0; }
  .steering > summary::-webkit-details-marker { display: none; }
  .steering > summary::before, .steering > summary::after { content: ""; flex: 1; height: 1px; background: var(--rule); }
  .steering > summary:hover { color: var(--text); }
  .steering .steering-verb { color: var(--state-warning); font-family: ui-monospace, "SFMono-Regular", monospace; letter-spacing: 0.02em; }
  .steering .steering-detail { color: var(--text); }
  .steering .steering-body { margin: 10px 0 0; padding: 10px 12px; background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11.5px; line-height: 1.55; color: var(--text-muted); white-space: pre-wrap; word-break: break-word; }
  ```

  **After:**
  ```css
  .steering { margin: 18px 0; padding: 0; font-size: var(--text-sm); color: var(--text-muted); }
  .steering > summary { cursor: pointer; list-style: none; user-select: none; display: flex; align-items: center; gap: 12px; padding: 0; }
  .steering > summary::-webkit-details-marker { display: none; }
  .steering > summary::before, .steering > summary::after { content: ""; flex: 1; height: 1px; background: var(--rule); }
  .steering > summary:hover { color: var(--text); }
  .steering .steering-verb { color: var(--state-warning); font-family: var(--font-mono); letter-spacing: 0.02em; }
  .steering .steering-detail { color: var(--text); }
  .steering .steering-body { margin: 10px 0 0; padding: 10px 12px; background: var(--bg); border-radius: 4px; font-family: var(--font-mono); font-size: var(--text-sm); line-height: var(--leading-snug); color: var(--text-muted); white-space: pre-wrap; word-break: break-word; }
  ```

  Notes:
  - Inline code in `.assistant-message code` switches from a fixed 12.5px to `0.85em` so it follows whatever the live experiment lands at for the body.
  - The `…` placeholders preserve the unchanged `.task-system-details`, `.task-system-detail-item`, `.task-system-detail`, `.task-system-detail dd`, `.system-line-pointer` rules between explicit edits.

- [ ] **Step 3 — verify visually.** Open a session with a few user/assistant turns plus a diagnostic and a banner. User pill reads at Hanken 14; assistant body matches (slight size bump from 13 to 14, slight leading loosen from 1.6 to 1.6 — wait, both are 1.6, no change there). Inline `<code>` in assistant text is now JetBrains Mono and proportional to body (0.85em). Diagnostic and banner read tight at 12. Steering verb is mono.

- [ ] **Step 4 — commit.**
  ```
  ui: migrate conversation, diagnostic, banner, system-line typography

  User pill + assistant body land at --text-md sans; tool-adjacent text
  (diagnostic, system-line, steering) lands at --text-sm mono.
  ```

---

## Task 5: Migrate tool-call cluster — tool-call, diff-body, shell-output, cheap-tool-*, task-list-body, fetch/search-body

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` lines 468–515.

- [ ] **Step 1 — read context.** Re-read lines 468–515. Every selector in this block carries mono content — tool calls, diffs, shell output, task ids, search bodies. Most explicitly declare `font-family: ui-monospace, ...`; some inherit from parent but the meaning is mono. Per cheatsheet, all of this is `--text-sm` mono (tool calls + diff body) with mono substitution.

- [ ] **Step 2 — apply migration.**

  **Before** (lines 468–515):
  ```css
  .tool-call { margin: 0 0 6px 0; font-size: 12.5px; color: var(--text-muted); display: flex; column-gap: 8px; row-gap: 1px; flex-wrap: wrap; align-items: baseline; }
  .tool-call-cluster { margin-bottom: 12px; }
  .tool-call-cluster .tool-call:last-child { margin-bottom: 0; }
  .tool-call .tool-status { display: inline-block; width: 12px; flex: 0 0 12px; margin-left: -20px; text-align: center; font-size: 11px; line-height: 1; font-family: ui-monospace, "SFMono-Regular", monospace; }
  .tool-call .tool-status-pending { color: var(--text-dim); }
  .tool-call .tool-status-good { color: var(--text-muted); }
  .tool-call .tool-status-bad { color: var(--state-awaiting); }
  .tool-call .verb { color: var(--text-muted); font-family: ui-monospace, "SFMono-Regular", monospace; }
  .tool-call .target { color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; word-break: break-all; }
  .tool-call .sep { color: var(--text-dim); }
  .tool-call .result-detail { color: var(--text-muted); }
  .tool-call .tool-meta { margin-left: auto; color: var(--text-dim); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; white-space: nowrap; }
  .tool-call .result { color: var(--text-muted); }
  .tool-call .result-good { color: var(--state-idle); }
  .tool-call .result-bad { color: var(--state-awaiting); }
  .tool-call .diff-body,
  .tool-call .tool-body {
    flex: 0 0 100%;
    margin: 0;
  }
  .diff-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.5; color: var(--text-muted); white-space: pre-wrap; }
  .diff-body .add { color: var(--state-idle); }
  .diff-body .del { color: var(--state-awaiting); }
  .diff-body .hunk { color: var(--text-dim); }

  /* Shared style for tool-body elements: diff, shell output, task list, etc. */
  .tool-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-size: 12px; color: var(--text-muted); }
  .tool-body summary { cursor: pointer; color: var(--text-muted); padding: 0; user-select: none; }
  .tool-body summary:hover { color: var(--text); }
  .shell-output { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text); white-space: pre-wrap; }
  .cheap-tool-args { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text-muted); white-space: pre-wrap; }
  .cheap-tool-output { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; line-height: 1.4; color: var(--text); white-space: pre-wrap; }
  .read-tool-purpose { margin: 0 0 3px; color: var(--text-muted); font-style: italic; }
  .read-tool-body .cheap-tool-output { margin-top: 0; }
  .output-preview-body { display: flex; flex-direction: column; }
  .tool-output-more { margin-top: 0; display: flex; flex-direction: column; }
  .tool-output-more summary { cursor: pointer; color: var(--text-muted); padding: 0; user-select: none; order: 2; }
  .tool-output-more summary:hover { color: var(--text); }
  .tool-output-rest { margin-top: 0; order: 1; }
  .task-list-body { list-style: none; margin: 0 0 12px 36px; padding: 6px 0 6px 10px; border-left: 1px solid var(--rule); display: flex; flex-direction: column; gap: 3px; }
  .task-list-body .task-row { padding: 2px 0; background: transparent; border: none; font-size: 12px; display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
  .task-list-body .task-id { color: var(--text-muted); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
  .task-list-body .task-arrow { color: var(--text-dim); }
  .task-list-body .task-status-label { color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
  .task-list-body .task-notes { color: var(--text-muted); font-style: italic; font-size: 11px; }
  .task-list-body .task-type { color: var(--text-muted); font-size: 10px; text-transform: uppercase; letter-spacing: 0.04em; }
  .fetch-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-size: 12px; color: var(--text-muted); font-style: italic; }
  .search-body { list-style: none; margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-size: 12px; color: var(--text); }
  .search-body li { padding: 2px 0; }
  ```

  **After:**
  ```css
  .tool-call { margin: 0 0 6px 0; font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-muted); display: flex; column-gap: 8px; row-gap: 1px; flex-wrap: wrap; align-items: baseline; }
  .tool-call-cluster { margin-bottom: 12px; }
  .tool-call-cluster .tool-call:last-child { margin-bottom: 0; }
  .tool-call .tool-status { display: inline-block; width: 12px; flex: 0 0 12px; margin-left: -20px; text-align: center; font-size: var(--text-xs); line-height: 1; font-family: var(--font-mono); }
  .tool-call .tool-status-pending { color: var(--text-dim); }
  .tool-call .tool-status-good { color: var(--text-muted); }
  .tool-call .tool-status-bad { color: var(--state-awaiting); }
  .tool-call .verb { color: var(--text-muted); font-family: var(--font-mono); }
  .tool-call .target { color: var(--text); font-family: var(--font-mono); word-break: break-all; }
  .tool-call .sep { color: var(--text-dim); }
  .tool-call .result-detail { color: var(--text-muted); }
  .tool-call .tool-meta { margin-left: auto; color: var(--text-dim); font-family: var(--font-mono); font-size: var(--text-xs); white-space: nowrap; }
  .tool-call .result { color: var(--text-muted); }
  .tool-call .result-good { color: var(--state-idle); }
  .tool-call .result-bad { color: var(--state-awaiting); }
  .tool-call .diff-body,
  .tool-call .tool-body {
    flex: 0 0 100%;
    margin: 0;
  }
  .diff-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-family: var(--font-mono); font-size: var(--text-sm); line-height: var(--leading-snug); color: var(--text-muted); white-space: pre-wrap; }
  .diff-body .add { color: var(--state-idle); }
  .diff-body .del { color: var(--state-awaiting); }
  .diff-body .hunk { color: var(--text-dim); }

  /* Shared style for tool-body elements: diff, shell output, task list, etc. */
  .tool-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-muted); }
  .tool-body summary { cursor: pointer; color: var(--text-muted); padding: 0; user-select: none; }
  .tool-body summary:hover { color: var(--text); }
  .shell-output { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: var(--font-mono); font-size: var(--text-sm); line-height: var(--leading-tight); color: var(--text); white-space: pre-wrap; }
  .cheap-tool-args { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: var(--font-mono); font-size: var(--text-sm); line-height: var(--leading-tight); color: var(--text-muted); white-space: pre-wrap; }
  .cheap-tool-output { margin: 4px 0 0; padding: 6px 8px; background: var(--bg); border-radius: 4px; font-family: var(--font-mono); font-size: var(--text-sm); line-height: var(--leading-tight); color: var(--text); white-space: pre-wrap; }
  .read-tool-purpose { margin: 0 0 3px; color: var(--text-muted); font-style: italic; }
  .read-tool-body .cheap-tool-output { margin-top: 0; }
  .output-preview-body { display: flex; flex-direction: column; }
  .tool-output-more { margin-top: 0; display: flex; flex-direction: column; }
  .tool-output-more summary { cursor: pointer; color: var(--text-muted); padding: 0; user-select: none; order: 2; }
  .tool-output-more summary:hover { color: var(--text); }
  .tool-output-rest { margin-top: 0; order: 1; }
  .task-list-body { list-style: none; margin: 0 0 12px 36px; padding: 6px 0 6px 10px; border-left: 1px solid var(--rule); display: flex; flex-direction: column; gap: 3px; }
  .task-list-body .task-row { padding: 2px 0; background: transparent; border: none; font-family: var(--font-mono); font-size: var(--text-sm); display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
  .task-list-body .task-id { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
  .task-list-body .task-arrow { color: var(--text-dim); }
  .task-list-body .task-status-label { color: var(--text); font-family: var(--font-mono); font-size: var(--text-xs); }
  .task-list-body .task-notes { color: var(--text-muted); font-style: italic; font-size: var(--text-xs); }
  .task-list-body .task-type { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-2xs); text-transform: uppercase; letter-spacing: 0.04em; }
  .fetch-body { margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-muted); font-style: italic; }
  .search-body { list-style: none; margin: 0 0 12px 36px; padding-left: 10px; border-left: 1px solid var(--rule); font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text); }
  .search-body li { padding: 2px 0; }
  ```

  Notes:
  - `.tool-call` itself gains `font-family: var(--font-mono)` so unflagged children (`.sep`, `.result`, `.result-detail`) inherit mono instead of falling back to sans. Children that already declared mono keep their declaration for explicitness.
  - `.shell-output`, `.cheap-tool-*` use `--leading-tight` (1.3) instead of the old 1.4 since these are dense pre-wrap blocks — close enough that no one will notice but it lands on a token.

- [ ] **Step 3 — verify visually.** Spawn a session that triggers a tool call (e.g., "ls a directory") and one with a diff (a small file edit). Tool call lines and diff body both render in JetBrains Mono at 12. Task lists are aligned, mono, with task ids at 11. Shell output reads tight and clean.

- [ ] **Step 4 — commit.**
  ```
  ui: migrate tool-call, diff, shell-output typography to mono tokens

  Every tool-adjacent surface now renders in JetBrains Mono at --text-sm
  with --leading-snug for diffs and --leading-tight for dense pre-wrap.
  ```

---

## Task 6: Migrate composer — input-card, message-input, queue-preview, attachments, input-controls, input-status

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` lines 367–437 (queue-preview, attachment-chip, composer-attachment*, input-card, message-input, input-controls, running-indicator, input-btn*, input-chip, input-status, generic .context/.cost).

- [ ] **Step 1 — read context.** Re-read lines 367–437. Per cheatsheet, composer textarea is **sans `--text-md / 400`** (you're writing prose), and the bottom status row (cwd · branch · ctx · cost) is **mono `--text-xs / 400`**. Buttons inherit `font: inherit` from body, plus the explicit `font-size: 11.5px` rounding up to `--text-sm`.

- [ ] **Step 2 — apply migration.**

  **Before** (lines 367–437):
  ```css
  .queue-preview { padding: 8px 10px; margin-bottom: 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
  .queue-preview-header { display: flex; align-items: baseline; gap: 12px; color: var(--text-muted); margin-bottom: 4px; }
  .queue-preview-label { font-weight: 500; color: var(--text); }
  .queue-preview-label [data-queue-depth] { font-family: ui-monospace, "SFMono-Regular", monospace; }
  .queue-preview-hint { flex: 1; }
  .queue-preview-hint kbd { font-family: ui-monospace, "SFMono-Regular", monospace; padding: 0 3px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 3px; }
  .queue-preview-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 3px; }
  .queue-preview-item { display: flex; gap: 6px; align-items: baseline; padding: 3px 6px; background: var(--bg-raised); border-radius: 3px; }
  .queue-preview-item .qp-idx { color: var(--text-dim); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 10px; }
  .queue-preview-item .qp-text { color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  .attachment-chip { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
  …
  .composer-attachment { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; color: var(--text); }
  .composer-attachment-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 240px; }
  .composer-attachment-remove { cursor: pointer; color: var(--text-muted); padding: 0 2px; border: none; background: transparent; font-size: 13px; line-height: 1; }
  …
  .composer-attachment-error { color: var(--state-danger, #c8553d); font-size: 11px; padding: 4px 0; }
  …
  .message-input { width: 100%; min-height: 36px; max-height: 50vh; background: transparent; border: none; resize: none; color: var(--text); font: inherit; outline: none; line-height: 1.5; overflow-y: auto; }

  .input-controls { display: flex; align-items: center; gap: 8px; padding: 8px 0 0; flex-wrap: wrap; }
  .controls-spacer { flex: 1; }
  .running-indicator { display: inline-flex; align-items: center; gap: 6px; color: var(--text); font-size: 12px; white-space: nowrap; }
  .running-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--state-processing); box-shadow: 0 0 0 4px rgba(122,162,247,0.12); }
  .input-btn { display: inline-flex; align-items: center; gap: 5px; padding: 4px 12px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; color: var(--text); font: inherit; font-size: 11.5px; cursor: pointer; }
  …
  .input-btn-primary kbd { background: rgba(0,0,0,0.2); border: 1px solid rgba(0,0,0,0.3); color: inherit; font-family: ui-monospace, "SFMono-Regular", monospace; padding: 0 4px; border-radius: 3px; }

  .input-chip { font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
  .chip-caret { color: var(--text-muted); margin-left: 2px; }

  .input-status { display: flex; align-items: center; gap: 14px; padding: 10px 0 0; margin-top: 6px; border-top: 1px solid var(--rule); font-size: 11px; color: var(--text-muted); flex-wrap: wrap; }
  .input-status .cwd, .input-status .branch { font-family: ui-monospace, "SFMono-Regular", monospace; }
  …
  .input-status .context-numbers { font-family: ui-monospace, "SFMono-Regular", monospace; }
  .input-status .cost { font-family: ui-monospace, "SFMono-Regular", monospace; }

  /* Generic context/cost styling — kept for status_bar.html and other surfaces. */
  .context { display: inline-flex; align-items: center; gap: 8px; }
  .context-bar { display: inline-block; width: 80px; height: 2px; background: var(--rule); vertical-align: middle; overflow: hidden; }
  .context-fill { display: block; height: 100%; background: var(--accent); }
  .context-numbers { font-family: ui-monospace, "SFMono-Regular", monospace; }
  .cost { font-family: ui-monospace, "SFMono-Regular", monospace; }
  ```

  **After:**
  ```css
  .queue-preview { padding: 8px 10px; margin-bottom: 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: var(--text-xs); }
  .queue-preview-header { display: flex; align-items: baseline; gap: 12px; color: var(--text-muted); margin-bottom: 4px; }
  .queue-preview-label { font-weight: 500; color: var(--text); }
  .queue-preview-label [data-queue-depth] { font-family: var(--font-mono); }
  .queue-preview-hint { flex: 1; }
  .queue-preview-hint kbd { font-family: var(--font-mono); font-weight: 600; padding: 0 3px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 3px; }
  .queue-preview-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 3px; }
  .queue-preview-item { display: flex; gap: 6px; align-items: baseline; padding: 3px 6px; background: var(--bg-raised); border-radius: 3px; }
  .queue-preview-item .qp-idx { color: var(--text-dim); font-family: var(--font-mono); font-size: var(--text-2xs); }
  .queue-preview-item .qp-text { color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  .attachment-chip { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-family: var(--font-mono); font-size: var(--text-xs); }
  …
  .composer-attachment { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text); }
  .composer-attachment-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 240px; }
  .composer-attachment-remove { cursor: pointer; color: var(--text-muted); padding: 0 2px; border: none; background: transparent; font-size: var(--text-base); line-height: 1; }
  …
  .composer-attachment-error { color: var(--state-danger, #c8553d); font-size: var(--text-xs); padding: 4px 0; }
  …
  .message-input { width: 100%; min-height: 36px; max-height: 50vh; background: transparent; border: none; resize: none; color: var(--text); font: inherit; font-family: var(--font-sans); font-size: var(--text-md); outline: none; line-height: var(--leading-snug); overflow-y: auto; }

  .input-controls { display: flex; align-items: center; gap: 8px; padding: 8px 0 0; flex-wrap: wrap; }
  .controls-spacer { flex: 1; }
  .running-indicator { display: inline-flex; align-items: center; gap: 6px; color: var(--text); font-size: var(--text-sm); white-space: nowrap; }
  .running-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--state-processing); box-shadow: 0 0 0 4px rgba(122,162,247,0.12); }
  .input-btn { display: inline-flex; align-items: center; gap: 5px; padding: 4px 12px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; color: var(--text); font: inherit; font-size: var(--text-sm); cursor: pointer; }
  …
  .input-btn-primary kbd { background: rgba(0,0,0,0.2); border: 1px solid rgba(0,0,0,0.3); color: inherit; font-family: var(--font-mono); padding: 0 4px; border-radius: 3px; }

  .input-chip { font-family: var(--font-mono); font-size: var(--text-xs); }
  .chip-caret { color: var(--text-muted); margin-left: 2px; }

  .input-status { display: flex; align-items: center; gap: 14px; padding: 10px 0 0; margin-top: 6px; border-top: 1px solid var(--rule); font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-muted); flex-wrap: wrap; }
  .input-status .cwd, .input-status .branch { font-family: var(--font-mono); }
  …
  .input-status .context-numbers { font-family: var(--font-mono); }
  .input-status .cost { font-family: var(--font-mono); }

  /* Generic context/cost styling — kept for status_bar.html and other surfaces. */
  .context { display: inline-flex; align-items: center; gap: 8px; }
  .context-bar { display: inline-block; width: 80px; height: 2px; background: var(--rule); vertical-align: middle; overflow: hidden; }
  .context-fill { display: block; height: 100%; background: var(--accent); }
  .context-numbers { font-family: var(--font-mono); }
  .cost { font-family: var(--font-mono); }
  ```

  Notes:
  - `.message-input` declares `font-family: var(--font-sans)` explicitly even though body inherits — this is the one place the cheatsheet is emphatic ("you're writing prose").
  - `.input-status` declares mono at the parent so all chips inherit; child rules keep their own mono declarations for clarity but no longer rely on cascade alone.
  - `.queue-preview-hint kbd` gains `font-weight: 600` per kbd cheatsheet row.
  - `.composer-attachment-remove` font-size 13 maps to `--text-base` (it's the × glyph; the literal was sizing the click target).
  - `…` ranges preserve `.input-attachments`, `.composer-attachments`, `.input-card`, `.input-card.drop-active`, `.input-btn:hover`, `.input-btn-ghost`, `.input-btn-stop`, `.input-btn-stop:hover`, `.input-btn-stop[disabled]`, `.input-btn-primary`, `.input-btn-primary:hover`, `.input-status .cwd { max-width: ... }`, `.input-status .status-spacer`, `.input-status .context`, `.input-status .context-bar`, `.input-status .context-fill`, and `.attachment-chip .att-thumb / .att-remove / .att-remove:hover` — all of which are unchanged.

- [ ] **Step 3 — verify visually.** Open a session. Type into the composer: text is Hanken at 14. The status row at the bottom (cwd · branch · ctx · cost) is JetBrains Mono at 11. The Send button label is sans at 12. Attachment chips and queue preview chips look quiet and mono.

- [ ] **Step 4 — commit.**
  ```
  ui: migrate composer typography — sans textarea, mono status row

  Message textarea declares sans 14 explicitly. Status row, chips, queue
  preview, and attachments land at mono --text-xs.
  ```

---

## Task 7: Migrate spawn surface — spawn-prompt, spawn-subtitle, chips, spawn-input, spawn-advanced, spawn-recent

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` lines 599–663 (spawn-pane, spawn-form, spawn-prompt, spawn-subtitle, spawn-chips, chip + chip-label + chip-value + chip-caret + chip-mode + chip-picker + chip-picker-option, spawn-input-wrap, spawn-input, spawn-attach-row, spawn-attach-btn, spawn-advanced + descendants, spawn-actions, spawn-btn, spawn-recent + descendants, fork-confirm kbd).

- [ ] **Step 1 — read context.** Re-read lines 599–663. Spawn prompt is the largest text in the app per cheatsheet — `--text-2xl / 600 / -0.018em`. Chips are mono key + value composition. Spawn input is sans (writing prose). Advanced section is mono labels.

- [ ] **Step 2 — apply migration.**

  **Before** (lines 599–663):
  ```css
  .spawn-pane { padding: 48px 64px; max-width: 880px; margin: 0 auto; width: 100%; flex: 1; overflow-y: auto; min-height: 0; }
  .spawn-form { display: flex; flex-direction: column; gap: 14px; }
  .spawn-prompt { font-size: 22px; color: var(--text); font-weight: 500; margin: 0; }
  .spawn-subtitle { color: var(--text-muted); font-size: 13px; margin: 0; }
  .spawn-chips { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 4px; }
  .chip { display: inline-flex; align-items: baseline; gap: 8px; padding: 5px 11px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 14px; color: var(--text); font-size: 12px; cursor: pointer; font: inherit; }
  .chip-label { color: var(--text-muted); }
  .chip-value { color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .chip-caret { color: var(--text-muted); font-size: 9px; }
  .chip-mode { background: rgba(224, 175, 104, 0.12); color: var(--state-warning); }
  .chip-picker { background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 6px; padding: 4px 0; min-width: 220px; box-shadow: 0 8px 24px rgba(0,0,0,0.2); z-index: 50; }
  .chip-picker-option { padding: 6px 12px; cursor: pointer; color: var(--text); font-size: 12px; font-family: ui-monospace, "SFMono-Regular", monospace; }
  .chip-picker-option:hover { background: var(--rule); }
  .spawn-input-wrap { display: block; }
  .spawn-input { width: 100%; box-sizing: border-box; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 8px; padding: 18px; min-height: 160px; color: var(--text); font: inherit; font-size: 14px; line-height: 1.5; outline: none; resize: vertical; }
  .spawn-input:focus { border-color: var(--accent); }
  …
  .spawn-attach-row { display: flex; gap: 8px; }
  .spawn-attach-btn { display: inline-flex; align-items: center; gap: 4px; padding: 4px 10px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 4px; color: var(--text); font: inherit; font-size: 13px; cursor: pointer; }
  …
  .spawn-form .diagnostic { margin: 0; max-width: none; }
  .spawn-advanced { color: var(--text-muted); font-size: 12px; }
  .spawn-advanced summary { cursor: pointer; padding: 5px 0; }
  .spawn-advanced-body { display: flex; flex-direction: column; gap: 14px; padding: 12px 0; }
  .spawn-advanced-note { margin: 0 0 4px; }
  .spawn-advanced-group { border: 1px solid var(--rule); border-radius: 4px; padding: 10px 12px; display: flex; flex-direction: column; gap: 12px; align-items: stretch; }
  .spawn-advanced-group legend { padding: 0 6px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; font-size: 10px; }
  .spawn-advanced-body label { display: flex; flex-direction: column; gap: 4px; font-size: 11px; color: var(--text-muted); }
  .spawn-advanced-body input,
  .spawn-advanced-body textarea,
  .spawn-advanced-body select,
  .spawn-advanced-body button { background: transparent; border: 1px solid var(--rule); border-radius: 4px; padding: 4px 8px; color: var(--text); font: inherit; font-size: 12px; outline: none; cursor: pointer; }
  …
  .spawn-advanced-textarea { width: 100%; min-height: 132px; line-height: 1.45; resize: vertical; cursor: text; }
  …
  .spawn-advanced-field-label { font-size: 11px; color: var(--text-muted); }
  .spawn-advanced-env-fallback { color: var(--text-dim); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
  …
  .spawn-advanced-list li { display: flex; align-items: center; justify-content: space-between; gap: 8px; border: 1px solid var(--rule); border-radius: 4px; padding: 4px 8px; color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
  …
  .spawn-advanced-chips li { display: inline-flex; align-items: center; gap: 4px; border: 1px solid var(--rule); border-radius: 12px; padding: 2px 8px; font-size: 11px; }
  …
  .spawn-btn { margin-left: auto; padding: 7px 18px; background: var(--accent); color: var(--bg); border: none; border-radius: 4px; font-weight: 500; font-size: 13px; cursor: pointer; font: inherit; }
  .spawn-btn kbd { font-family: ui-monospace, "SFMono-Regular", monospace; opacity: 0.7; margin-left: 4px; }
  .spawn-recent { margin-top: 32px; }
  .spawn-recent-header { color: var(--text-muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 8px; }
  .spawn-recent-row { display: block; padding: 6px 10px; border-radius: 4px; color: var(--text); cursor: pointer; font-size: 12px; text-decoration: none; }
  .spawn-recent-row:hover { background: var(--bg-raised); }
  .fork-confirm kbd { font-family: ui-monospace, "SFMono-Regular", monospace; opacity: 0.7; margin-left: 4px; }
  ```

  **After:**
  ```css
  .spawn-pane { padding: 48px 64px; max-width: 880px; margin: 0 auto; width: 100%; flex: 1; overflow-y: auto; min-height: 0; }
  .spawn-form { display: flex; flex-direction: column; gap: 14px; }
  .spawn-prompt { font-size: var(--text-2xl); color: var(--text); font-weight: 600; letter-spacing: -0.018em; margin: 0; }
  .spawn-subtitle { color: var(--text-muted); font-size: var(--text-base); margin: 0; }
  .spawn-chips { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 4px; }
  .chip { display: inline-flex; align-items: baseline; gap: 8px; padding: 5px 11px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 14px; color: var(--text); font-size: var(--text-sm); cursor: pointer; font: inherit; }
  .chip-label { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-2xs); }
  .chip-value { color: var(--text); font-family: var(--font-mono); font-size: var(--text-xs); max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .chip-caret { color: var(--text-muted); font-size: 9px; }
  .chip-mode { background: rgba(224, 175, 104, 0.12); color: var(--state-warning); }
  .chip-picker { background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 6px; padding: 4px 0; min-width: 220px; box-shadow: 0 8px 24px rgba(0,0,0,0.2); z-index: 50; }
  .chip-picker-option { padding: 6px 12px; cursor: pointer; color: var(--text); font-family: var(--font-mono); font-size: var(--text-sm); }
  .chip-picker-option:hover { background: var(--rule); }
  .spawn-input-wrap { display: block; }
  .spawn-input { width: 100%; box-sizing: border-box; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 8px; padding: 18px; min-height: 160px; color: var(--text); font: inherit; font-family: var(--font-sans); font-size: var(--text-md); line-height: var(--leading-snug); outline: none; resize: vertical; }
  .spawn-input:focus { border-color: var(--accent); }
  …
  .spawn-attach-row { display: flex; gap: 8px; }
  .spawn-attach-btn { display: inline-flex; align-items: center; gap: 4px; padding: 4px 10px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 4px; color: var(--text); font: inherit; font-size: var(--text-base); cursor: pointer; }
  …
  .spawn-form .diagnostic { margin: 0; max-width: none; }
  .spawn-advanced { color: var(--text-muted); font-size: var(--text-sm); }
  .spawn-advanced summary { cursor: pointer; padding: 5px 0; font-family: var(--font-mono); }
  .spawn-advanced-body { display: flex; flex-direction: column; gap: 14px; padding: 12px 0; }
  .spawn-advanced-note { margin: 0 0 4px; }
  .spawn-advanced-group { border: 1px solid var(--rule); border-radius: 4px; padding: 10px 12px; display: flex; flex-direction: column; gap: 12px; align-items: stretch; }
  .spawn-advanced-group legend { padding: 0 6px; color: var(--text-muted); font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.04em; font-size: var(--text-2xs); }
  .spawn-advanced-body label { display: flex; flex-direction: column; gap: 4px; font-size: var(--text-xs); color: var(--text-muted); }
  .spawn-advanced-body input,
  .spawn-advanced-body textarea,
  .spawn-advanced-body select,
  .spawn-advanced-body button { background: transparent; border: 1px solid var(--rule); border-radius: 4px; padding: 4px 8px; color: var(--text); font: inherit; font-family: var(--font-mono); font-size: var(--text-sm); outline: none; cursor: pointer; }
  …
  .spawn-advanced-textarea { width: 100%; min-height: 132px; line-height: var(--leading-snug); resize: vertical; cursor: text; }
  …
  .spawn-advanced-field-label { font-size: var(--text-xs); color: var(--text-muted); }
  .spawn-advanced-env-fallback { color: var(--text-dim); font-family: var(--font-mono); font-size: var(--text-xs); }
  …
  .spawn-advanced-list li { display: flex; align-items: center; justify-content: space-between; gap: 8px; border: 1px solid var(--rule); border-radius: 4px; padding: 4px 8px; color: var(--text); font-family: var(--font-mono); font-size: var(--text-xs); }
  …
  .spawn-advanced-chips li { display: inline-flex; align-items: center; gap: 4px; border: 1px solid var(--rule); border-radius: 12px; padding: 2px 8px; font-family: var(--font-mono); font-size: var(--text-xs); }
  …
  .spawn-btn { margin-left: auto; padding: 7px 18px; background: var(--accent); color: var(--bg); border: none; border-radius: 4px; font-weight: 600; font-size: var(--text-base); cursor: pointer; font: inherit; }
  .spawn-btn kbd { font-family: var(--font-mono); font-weight: 600; opacity: 0.7; margin-left: 4px; }
  .spawn-recent { margin-top: 32px; }
  .spawn-recent-header { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-2xs); font-weight: 500; text-transform: uppercase; letter-spacing: 0.16em; margin-bottom: 8px; }
  .spawn-recent-row { display: block; padding: 6px 10px; border-radius: 4px; color: var(--text); cursor: pointer; font-size: var(--text-sm); text-decoration: none; }
  .spawn-recent-row:hover { background: var(--bg-raised); }
  .fork-confirm kbd { font-family: var(--font-mono); font-weight: 600; opacity: 0.7; margin-left: 4px; }
  ```

  Notes:
  - `.spawn-prompt` weight bumps 500 → 600 with `-0.018em` tracking per cheatsheet.
  - `.chip-label` and `.chip-value` decouple from `.chip` font-size — key is `--text-2xs`, value is `--text-xs`, both mono. The `.chip` parent's `font-size: --text-sm` lands on `.chip-caret` and the caret literal `9px` stays where it is (carets are decorative; the design language §1.2 explicitly allows `9` to stay literal for tiny carets).
  - `.spawn-input` declares sans explicitly (same logic as `.message-input`).
  - `.spawn-recent-header` adopts the section-header treatment per cheatsheet — mono uppercase tracked at `--text-2xs`.
  - `.spawn-btn` weight bumps to 600 per primary button cheatsheet row (`--text-base / 600`).
  - `…` ranges preserve `.spawn-attach-btn:hover`, `.spawn-attach-btn:focus-visible`, `.spawn-advanced-body button[type="button"]`, `.spawn-advanced-row`, `.spawn-advanced-model`, `.spawn-advanced-radio + label`, `.launch-radio-composite` + descendants, `.launch-radio-option-body`, `.spawn-advanced-list-control`, `.spawn-advanced-add-controls`, `.spawn-advanced-list { ... display: flex }`, `.spawn-advanced-list li button`, `.spawn-advanced-chips { ... }`, `.spawn-advanced-chips li button`, `.spawn-advanced-actions`, `.spawn-actions` rules — none of which set type properties.

- [ ] **Step 3 — verify visually.** Visit `/new`. The big "What should we build?" prompt is Hanken 22 bold with subtle negative tracking. Chips above the textarea have small mono keys and slightly-larger mono values. The textarea itself is sans 14. The advanced details summary shows mono label. Submit button reads sans 13/600.

- [ ] **Step 4 — commit.**
  ```
  ui: migrate spawn surface typography to tokens

  Spawn prompt lands at --text-2xl/600 with -0.018em tracking; chips
  separate mono key (2xs) and value (xs); textarea declares sans 14
  explicitly; advanced section uses mono labels.
  ```

---

## Task 8: Migrate settings, credentials, fork dialog, search palette, pickers, panels, optimistic

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` lines 587–596 (fork-dialog* — note: lines re-anchor after Tasks 2–7; rely on selector context, not line numbers, when applying), 666–727 (chip-picker-wide, search palette + descendants, search-hit), 729–810 (settings page + descendants + status-pill variants + settings-launch-form + sp-* pickers + settings-add-row + settings-items-list), 813–823 (title-action, details-panel + descendants), 826–864 (conversation-empty, tasks-* panel), 989–1002 (credentials), 1011–1029 (optimistic-failed-reason, optimistic-retry). Skip the responsive `@media (max-width: 767px)` block — it gets re-checked in Task 9.

- [ ] **Step 1 — read context.** Re-read each block. Note: `.status-pill` variants under `.settings-row-title` should retain their colored backgrounds for Pass 2 (Pass 4 strips backgrounds and replaces the class with `.status-badge`); we just migrate type. Per cheatsheet, settings dt is sans 13/500, dd is mono 12/400, help is sans 11/350. Search palette result rows are sans body; cmd ids are mono 11.

- [ ] **Step 2 — apply migration.**

  **Before** (fork-dialog, lines 587–596):
  ```css
  .fork-dialog-title { font-weight: 500; color: var(--text); font-size: 13px; margin-bottom: 6px; }
  .fork-dialog-body { color: var(--text-muted); font-size: 12.5px; line-height: 1.55; margin-bottom: 10px; }
  .fork-dialog-label { color: var(--text-muted); font-size: 12px; }
  .fork-dialog-input { background: transparent; border: 1px solid var(--rule); border-radius: 3px; padding: 4px 8px; color: var(--text); font: inherit; font-size: 12px; width: 220px; margin-left: 8px; outline: none; }
  …
  .fork-dialog-actions { display: flex; gap: 14px; align-items: center; margin-top: 10px; font-size: 12px; }
  ```

  **After:**
  ```css
  .fork-dialog-title { font-weight: 500; color: var(--text); font-size: var(--text-base); margin-bottom: 6px; }
  .fork-dialog-body { color: var(--text-muted); font-size: var(--text-sm); line-height: var(--leading-snug); margin-bottom: 10px; }
  .fork-dialog-label { color: var(--text-muted); font-size: var(--text-sm); }
  .fork-dialog-input { background: transparent; border: 1px solid var(--rule); border-radius: 3px; padding: 4px 8px; color: var(--text); font: inherit; font-family: var(--font-mono); font-size: var(--text-sm); width: 220px; margin-left: 8px; outline: none; }
  …
  .fork-dialog-actions { display: flex; gap: 14px; align-items: center; margin-top: 10px; font-size: var(--text-sm); }
  ```

  **Before** (model + dir picker, lines 666–686):
  ```css
  .chip-picker-search { background: transparent; border: none; border-bottom: 1px solid var(--rule); padding: 8px 12px; color: var(--text); font: inherit; font-size: 13px; outline: none; }
  …
  .chip-picker-provider { padding: 6px 12px; cursor: pointer; color: var(--text-muted); font-size: 12px; }
  …
  .chip-picker-model-name { color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; }
  .chip-picker-model-meta { color: var(--text-muted); font-size: 11px; margin-top: 2px; }
  …
  .chip-picker-dir-row { display: flex; align-items: center; gap: 8px; padding: 6px 12px; cursor: pointer; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; }
  …
  .chip-picker-dir-tag { color: var(--state-warning); font-size: 10px; padding: 1px 6px; border: 1px solid var(--state-warning); border-radius: 8px; flex-shrink: 0; }
  .chip-picker-empty { padding: 24px; color: var(--text-muted); text-align: center; font-size: 12px; }
  ```

  **After:**
  ```css
  .chip-picker-search { background: transparent; border: none; border-bottom: 1px solid var(--rule); padding: 8px 12px; color: var(--text); font: inherit; font-size: var(--text-base); outline: none; }
  …
  .chip-picker-provider { padding: 6px 12px; cursor: pointer; color: var(--text-muted); font-size: var(--text-sm); }
  …
  .chip-picker-model-name { color: var(--text); font-family: var(--font-mono); font-size: var(--text-sm); }
  .chip-picker-model-meta { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); margin-top: 2px; }
  …
  .chip-picker-dir-row { display: flex; align-items: center; gap: 8px; padding: 6px 12px; cursor: pointer; font-family: var(--font-mono); font-size: var(--text-sm); }
  …
  .chip-picker-dir-tag { color: var(--state-warning); font-family: var(--font-mono); font-size: var(--text-2xs); padding: 1px 6px; border: 1px solid var(--state-warning); border-radius: 8px; flex-shrink: 0; }
  .chip-picker-empty { padding: 24px; color: var(--text-muted); text-align: center; font-size: var(--text-sm); }
  ```

  **Before** (search palette, lines 694–727):
  ```css
  #search-input { flex: 1; background: transparent; border: none; color: var(--text); font: inherit; font-size: 14px; outline: none; }
  .search-dialog-hint { color: var(--text-muted); font-size: 11px; }
  …
  .search-section-header { padding: 4px 16px; color: var(--text-muted); font-size: 10px; text-transform: uppercase; letter-spacing: 0.05em; margin-top: 6px; }
  …
  .search-project { color: var(--text-muted); font-size: 11px; }
  .search-age { color: var(--text-muted); font-size: 11px; }
  .search-empty { padding: 24px; color: var(--text-muted); text-align: center; font-size: 12px; }
  .palette-error { margin: 6px 12px 4px; padding: 8px 12px; border: 1px solid var(--state-error, #f7768e); border-radius: 4px; background: rgba(247, 118, 142, 0.12); color: var(--state-error, #f7768e); font-size: 12px; }
  .search-dialog-footer { padding: 8px 16px; border-top: 1px solid var(--rule); display: flex; gap: 18px; font-size: 11px; color: var(--text-muted); }
  …
  .search-row-command .search-cmd-id { color: var(--text-muted); font-size: 11px; font-family: var(--font-mono, ui-monospace, monospace); }
  .search-row-command .search-cmd-hint { color: var(--text-muted); font-size: 11px; }
  .search-row-argitem .search-cmd-hint { color: var(--text-muted); font-size: 11px; }
  .search-cmd-pill { display: inline-flex; align-items: center; gap: 6px; padding: 2px 6px 2px 8px; background: rgba(122, 162, 247, 0.15); color: var(--text); border-radius: 12px; font-size: 12px; line-height: 1; }
  …
  .search-cmd-pill-back { background: transparent; border: none; color: var(--text-muted); cursor: pointer; padding: 0 2px; font-size: 14px; line-height: 1; }
  …
  .search-empty code { font-family: var(--font-mono, ui-monospace, monospace); background: rgba(122, 162, 247, 0.12); padding: 0 4px; border-radius: 3px; color: var(--text); }
  .search-help-row { display: flex; align-items: center; gap: 12px; padding: 6px 16px; font-size: 12px; }
  .search-help-keys { color: var(--text); font-family: var(--font-mono, ui-monospace, monospace); flex-shrink: 0; min-width: 110px; }
  .search-help-desc { color: var(--text-muted); }
  .search-snippet { font-size: 12px; color: var(--text-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  ```

  **After:**
  ```css
  #search-input { flex: 1; background: transparent; border: none; color: var(--text); font: inherit; font-size: var(--text-md); outline: none; }
  .search-dialog-hint { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
  …
  .search-section-header { padding: 4px 16px; color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-2xs); font-weight: 500; text-transform: uppercase; letter-spacing: 0.16em; margin-top: 6px; }
  …
  .search-project { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
  .search-age { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
  .search-empty { padding: 24px; color: var(--text-muted); text-align: center; font-size: var(--text-sm); }
  .palette-error { margin: 6px 12px 4px; padding: 8px 12px; border: 1px solid var(--state-error, #f7768e); border-radius: 4px; background: rgba(247, 118, 142, 0.12); color: var(--state-error, #f7768e); font-size: var(--text-sm); }
  .search-dialog-footer { padding: 8px 16px; border-top: 1px solid var(--rule); display: flex; gap: 18px; font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-muted); }
  …
  .search-row-command .search-cmd-id { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
  .search-row-command .search-cmd-hint { color: var(--text-muted); font-size: var(--text-xs); }
  .search-row-argitem .search-cmd-hint { color: var(--text-muted); font-size: var(--text-xs); }
  .search-cmd-pill { display: inline-flex; align-items: center; gap: 6px; padding: 2px 6px 2px 8px; background: rgba(122, 162, 247, 0.15); color: var(--text); border-radius: 12px; font-family: var(--font-mono); font-size: var(--text-sm); line-height: 1; }
  …
  .search-cmd-pill-back { background: transparent; border: none; color: var(--text-muted); cursor: pointer; padding: 0 2px; font-size: var(--text-md); line-height: 1; }
  …
  .search-empty code { font-family: var(--font-mono); background: rgba(122, 162, 247, 0.12); padding: 0 4px; border-radius: 3px; color: var(--text); }
  .search-help-row { display: flex; align-items: center; gap: 12px; padding: 6px 16px; font-size: var(--text-sm); }
  .search-help-keys { color: var(--text); font-family: var(--font-mono); font-weight: 600; flex-shrink: 0; min-width: 110px; }
  .search-help-desc { color: var(--text-muted); }
  .search-snippet { font-size: var(--text-sm); color: var(--text-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  ```

  **Before** (settings, lines 732–810):
  ```css
  .settings-nav-section { padding: 14px 20px 4px; color: var(--text-dim); font-size: 10px; letter-spacing: 0.12em; text-transform: uppercase; }
  .settings-nav-link { display: block; padding: 6px 20px; color: var(--text-muted); text-decoration: none; font-size: 13px; }
  …
  .settings-h2 { margin: 0 0 12px; font-size: 18px; font-weight: 500; color: var(--text); }
  .settings-help { color: var(--text-muted); font-size: 13px; max-width: 560px; margin: 0 0 24px; line-height: 1.6; }
  .settings-help code { background: var(--bg-raised); padding: 1px 6px; border-radius: 3px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; }
  .settings-h3 { margin: 28px 0 10px; font-size: 13px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.06em; }
  .settings-form { display: flex; flex-direction: column; gap: 12px; max-width: 480px; }
  .settings-radio,
  .settings-toggle { display: flex; align-items: center; gap: 10px; color: var(--text); font-size: 13px; cursor: pointer; }
  .settings-list { display: grid; grid-template-columns: 200px 1fr; gap: 8px 24px; max-width: 720px; }
  .settings-list dt { color: var(--text-muted); font-size: 13px; }
  .settings-list dd { margin: 0; color: var(--text); font-size: 13px; }
  .settings-list code { font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; color: var(--text); }
  …
  .settings-row-title { font-weight: 500; color: var(--text); font-size: 14px; font-family: ui-monospace, "SFMono-Regular", monospace; }
  .settings-row-meta { color: var(--text-muted); font-size: 12px; margin-top: 2px; }
  …
  .model-chip { background: var(--bg-raised); padding: 2px 8px; border-radius: 10px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; color: var(--text); }
  .settings-row-version { color: var(--text-muted); font-size: 11px; margin-left: 8px; font-family: ui-monospace, "SFMono-Regular", monospace; font-weight: 400; }
  …
  .settings-row-cmd { font-family: ui-monospace, "SFMono-Regular", monospace; word-break: break-all; }
  .settings-row-desc { line-height: 1.5; max-width: 640px; }
  .settings-error { color: var(--state-awaiting); }
  .settings-row-title .status-pill { margin-left: 8px; padding: 1px 8px; border-radius: 10px; font-size: 11px; font-family: ui-monospace, "SFMono-Regular", monospace; font-weight: 400; }
  …
  /* Settings launch-form field rows */
  .settings-launch-form { display: flex; flex-direction: column; gap: 10px; max-width: 480px; margin-bottom: 24px; }
  .settings-launch-form label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text-muted); }
  .settings-launch-form label > input[type=text],
  .settings-launch-form label > input[type=number],
  .settings-launch-form label > select,
  .settings-launch-form textarea,
  .settings-launch-form .launch-radio-option input[type=text] { font-size: 13px; color: var(--text); background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 4px; padding: 6px 8px; outline: none; }
  …
  .settings-launch-form label.checkbox-label { flex-direction: row; align-items: center; gap: 8px; color: var(--text); font-size: 13px; }
  …
  .settings-launch-textarea { width: 100%; min-height: 132px; line-height: 1.45; resize: vertical; cursor: text; }

  /* Model + dir picker widgets used in settings pages */
  …
  .sp-dir-btn { display: inline-flex; align-items: baseline; gap: 6px; padding: 5px 10px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 14px; color: var(--text); font-size: 12px; cursor: pointer; font: inherit; white-space: nowrap; }
  …
  .sp-dir-display { font-family: ui-monospace, "SFMono-Regular", monospace; color: var(--text); max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .sp-dir-caret { color: var(--text-muted); font-size: 9px; }
  .sp-clear-btn { background: none; border: none; color: var(--text-muted); font-size: 13px; padding: 0 4px; cursor: pointer; line-height: 1; }
  …
  .settings-items-list li { display: flex; align-items: center; gap: 8px; padding: 4px 0; border-bottom: 1px solid var(--rule); font-size: 13px; }
  .settings-items-list li:last-child { border-bottom: none; }
  .settings-items-list li code { font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 12px; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .settings-items-list li button { flex-shrink: 0; font-size: 11px; padding: 2px 7px; }
  ```

  **After:**
  ```css
  .settings-nav-section { padding: 14px 20px 4px; color: var(--text-dim); font-family: var(--font-mono); font-size: var(--text-2xs); font-weight: 500; letter-spacing: 0.16em; text-transform: uppercase; }
  .settings-nav-link { display: block; padding: 6px 20px; color: var(--text-muted); text-decoration: none; font-size: var(--text-base); }
  …
  .settings-h2 { margin: 0 0 12px; font-size: var(--text-xl); font-weight: 500; color: var(--text); }
  .settings-help { color: var(--text-muted); font-size: var(--text-xs); font-weight: 350; max-width: 560px; margin: 0 0 24px; line-height: var(--leading-snug); }
  .settings-help code { background: var(--bg-raised); padding: 1px 6px; border-radius: 3px; font-family: var(--font-mono); font-size: 0.85em; }
  .settings-h3 { margin: 28px 0 10px; font-family: var(--font-mono); font-size: var(--text-2xs); font-weight: 500; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.16em; }
  .settings-form { display: flex; flex-direction: column; gap: 12px; max-width: 480px; }
  .settings-radio,
  .settings-toggle { display: flex; align-items: center; gap: 10px; color: var(--text); font-size: var(--text-base); cursor: pointer; }
  .settings-list { display: grid; grid-template-columns: 200px 1fr; gap: 8px 24px; max-width: 720px; }
  .settings-list dt { color: var(--text); font-size: var(--text-base); font-weight: 500; }
  .settings-list dd { margin: 0; color: var(--text); font-family: var(--font-mono); font-size: var(--text-sm); }
  .settings-list code { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text); }
  …
  .settings-row-title { font-weight: 500; color: var(--text); font-family: var(--font-mono); font-size: var(--text-md); }
  .settings-row-meta { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-sm); margin-top: 2px; }
  …
  .model-chip { background: var(--bg-raised); padding: 2px 8px; border-radius: 10px; font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text); }
  .settings-row-version { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 400; margin-left: 8px; }
  …
  .settings-row-cmd { font-family: var(--font-mono); word-break: break-all; }
  .settings-row-desc { line-height: var(--leading-snug); max-width: 640px; }
  .settings-error { color: var(--state-awaiting); }
  .settings-row-title .status-pill { margin-left: 8px; padding: 1px 8px; border-radius: 10px; font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 500; letter-spacing: 0.08em; text-transform: uppercase; }
  …
  /* Settings launch-form field rows */
  .settings-launch-form { display: flex; flex-direction: column; gap: 10px; max-width: 480px; margin-bottom: 24px; }
  .settings-launch-form label { display: flex; flex-direction: column; gap: 4px; font-size: var(--text-sm); color: var(--text-muted); }
  .settings-launch-form label > input[type=text],
  .settings-launch-form label > input[type=number],
  .settings-launch-form label > select,
  .settings-launch-form textarea,
  .settings-launch-form .launch-radio-option input[type=text] { font-family: var(--font-mono); font-size: var(--text-base); color: var(--text); background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 4px; padding: 6px 8px; outline: none; }
  …
  .settings-launch-form label.checkbox-label { flex-direction: row; align-items: center; gap: 8px; color: var(--text); font-size: var(--text-base); }
  …
  .settings-launch-textarea { width: 100%; min-height: 132px; line-height: var(--leading-snug); resize: vertical; cursor: text; }

  /* Model + dir picker widgets used in settings pages */
  …
  .sp-dir-btn { display: inline-flex; align-items: baseline; gap: 6px; padding: 5px 10px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 14px; color: var(--text); font-size: var(--text-sm); cursor: pointer; font: inherit; white-space: nowrap; }
  …
  .sp-dir-display { font-family: var(--font-mono); color: var(--text); max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .sp-dir-caret { color: var(--text-muted); font-size: 9px; }
  .sp-clear-btn { background: none; border: none; color: var(--text-muted); font-size: var(--text-base); padding: 0 4px; cursor: pointer; line-height: 1; }
  …
  .settings-items-list li { display: flex; align-items: center; gap: 8px; padding: 4px 0; border-bottom: 1px solid var(--rule); font-size: var(--text-base); }
  .settings-items-list li:last-child { border-bottom: none; }
  .settings-items-list li code { font-family: var(--font-mono); font-size: var(--text-sm); flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .settings-items-list li button { flex-shrink: 0; font-size: var(--text-xs); padding: 2px 7px; }
  ```

  Notes:
  - `.settings-list dt` color changes from `--text-muted` to `--text` and gains `font-weight: 500` per cheatsheet ("settings table dt: sans / `--text-base / 500`").
  - `.settings-h3` is the section subhead in mono uppercase tracked (`--text-2xs / 500 / 0.16em`).
  - `.settings-row-title .status-pill` adopts the typographic small-caps treatment matching `.status-pill` from Task 2. The colored background variants (`.status-pill.status-running` etc., lines 760–766) stay untouched — Pass 4 removes their backgrounds.
  - `.sp-model-btn` is grouped with `.sp-dir-btn` at line 792–793 (shared rule). Apply both selectors together (same font-size literal); selector listed singly in the before/after for brevity. Similarly for `.sp-model-display` / `.sp-dir-display` and `.sp-model-caret` / `.sp-dir-caret`.

  **Before** (title-action, details-panel, conversation-empty, tasks panel, lines 813–864):
  ```css
  .title-action { background: transparent; border: none; color: var(--text-muted); font-size: 13px; cursor: pointer; padding: 0 4px; }
  …
  .details-panel-header { display: flex; align-items: baseline; justify-content: space-between; padding-bottom: 12px; border-bottom: 1px solid var(--rule); color: var(--text); font-weight: 500; font-size: 13px; }
  .details-panel-close { color: var(--text-muted); font-size: 11px; }
  .details-loading { color: var(--text-muted); padding: 12px 0; font-size: 12px; }
  .details-list { display: grid; grid-template-columns: 110px 1fr; gap: 6px 12px; margin: 12px 0 0; font-size: 12px; }
  .details-list dt { color: var(--text-muted); }
  .details-list dd { margin: 0; color: var(--text); font-family: ui-monospace, "SFMono-Regular", monospace; word-break: break-all; }
  /* Conversation empty state */
  .conversation-empty { color: var(--text-muted); font-size: 13px; padding: 32px 0; font-style: italic; }
  /* Task list panel */
  .tasks-summary { color: var(--text-muted); font-size: 11px; margin: 12px 0 6px; }
  .tasks-empty { color: var(--text-muted); font-size: 12px; padding: 12px 0; font-style: italic; }
  .tasks-list { list-style: none; margin: 8px 0 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .task-row { display: grid; grid-template-columns: 14px 28px auto 1fr auto; gap: 6px; align-items: baseline; padding: 6px 8px; border-radius: 4px; font-size: 12px; line-height: 1.4; background: var(--bg); border: 1px solid var(--rule); }
  .task-row .task-icon { color: var(--text-muted); font-size: 11px; }
  .task-row .task-id { color: var(--text-muted); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
  .task-row .task-type { color: var(--text-muted); font-size: 10px; text-transform: uppercase; letter-spacing: 0.04em; }
  …
  .task-row .task-deps { color: var(--text-muted); font-size: 10px; font-family: ui-monospace, "SFMono-Regular", monospace; }
  …
  .tasks-list .task-row-chevron { color: var(--text-dim); font-size: 14px; transition: transform 0.15s; transform: rotate(0deg); }
  …
  .tasks-list .task-detail { margin: 0; padding: 4px 12px 10px 38px; display: grid; grid-template-columns: 80px 1fr; gap: 4px 10px; font-size: 11px; border-top: 1px dashed var(--rule); }
  …
  .tasks-list .task-prompt { font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; line-height: 1.5; white-space: pre-wrap; color: var(--text); }
  .tasks-list .task-type-pill { display: inline-block; padding: 1px 6px; font-size: 10px; text-transform: uppercase; letter-spacing: 0.04em; background: var(--bg); border: 1px solid var(--rule); border-radius: 3px; }
  …
  .tasks-list .task-note { display: grid; grid-template-columns: 16px 1fr; gap: 4px; align-items: baseline; font-size: 11px; color: var(--text-muted); }
  .tasks-list .task-note-num { color: var(--text-dim); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 10px; }
  …
  .panel-toggle-badge { display: inline-flex; align-items: center; padding: 1px 6px; margin-left: 4px; font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 10px; color: var(--text-muted); background: var(--bg); border: 1px solid var(--rule); border-radius: 3px; }
  ```

  **After:**
  ```css
  .title-action { background: transparent; border: none; color: var(--text-muted); font-size: var(--text-base); cursor: pointer; padding: 0 4px; }
  …
  .details-panel-header { display: flex; align-items: baseline; justify-content: space-between; padding-bottom: 12px; border-bottom: 1px solid var(--rule); color: var(--text); font-weight: 500; font-size: var(--text-base); }
  .details-panel-close { color: var(--text-muted); font-size: var(--text-xs); }
  .details-loading { color: var(--text-muted); padding: 12px 0; font-size: var(--text-sm); }
  .details-list { display: grid; grid-template-columns: 110px 1fr; gap: 6px 12px; margin: 12px 0 0; font-size: var(--text-sm); }
  .details-list dt { color: var(--text-muted); font-family: var(--font-mono); }
  .details-list dd { margin: 0; color: var(--text); font-family: var(--font-mono); word-break: break-all; }
  /* Conversation empty state */
  .conversation-empty { color: var(--text-muted); font-size: var(--text-base); padding: 32px 0; font-style: italic; }
  /* Task list panel */
  .tasks-summary { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); margin: 12px 0 6px; }
  .tasks-empty { color: var(--text-muted); font-size: var(--text-sm); padding: 12px 0; font-style: italic; }
  .tasks-list { list-style: none; margin: 8px 0 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .task-row { display: grid; grid-template-columns: 14px 28px auto 1fr auto; gap: 6px; align-items: baseline; padding: 6px 8px; border-radius: 4px; font-size: var(--text-sm); line-height: var(--leading-tight); background: var(--bg); border: 1px solid var(--rule); }
  .task-row .task-icon { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
  .task-row .task-id { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); }
  .task-row .task-type { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-2xs); text-transform: uppercase; letter-spacing: 0.04em; }
  …
  .task-row .task-deps { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-2xs); }
  …
  .tasks-list .task-row-chevron { color: var(--text-dim); font-size: var(--text-md); transition: transform 0.15s; transform: rotate(0deg); }
  …
  .tasks-list .task-detail { margin: 0; padding: 4px 12px 10px 38px; display: grid; grid-template-columns: 80px 1fr; gap: 4px 10px; font-size: var(--text-xs); border-top: 1px dashed var(--rule); }
  …
  .tasks-list .task-prompt { font-family: var(--font-mono); font-size: var(--text-xs); line-height: var(--leading-snug); white-space: pre-wrap; color: var(--text); }
  .tasks-list .task-type-pill { display: inline-block; padding: 1px 6px; font-family: var(--font-mono); font-size: var(--text-2xs); text-transform: uppercase; letter-spacing: 0.04em; background: var(--bg); border: 1px solid var(--rule); border-radius: 3px; }
  …
  .tasks-list .task-note { display: grid; grid-template-columns: 16px 1fr; gap: 4px; align-items: baseline; font-size: var(--text-xs); color: var(--text-muted); }
  .tasks-list .task-note-num { color: var(--text-dim); font-family: var(--font-mono); font-size: var(--text-2xs); }
  …
  .panel-toggle-badge { display: inline-flex; align-items: center; padding: 1px 6px; margin-left: 4px; font-family: var(--font-mono); font-size: var(--text-2xs); color: var(--text-muted); background: var(--bg); border: 1px solid var(--rule); border-radius: 3px; }
  ```

  **Before** (credentials, lines 989–1002):
  ```css
  .credentials-row-status { color: var(--text-muted, #888); font-size: 12px; }
  …
  .credentials-editor-label { font-size: 12px; color: var(--text-muted, #888); }
  …
  .credentials-editor-input { padding: 6px 8px; font-family: inherit; font-size: 13px; }
  .credentials-editor-error { margin: 0; color: var(--state-awaiting, var(--error, #c43755)); font-size: 12px; }
  ```

  **After:**
  ```css
  .credentials-row-status { color: var(--text-muted, #888); font-family: var(--font-mono); font-size: var(--text-sm); }
  …
  .credentials-editor-label { font-size: var(--text-sm); color: var(--text-muted, #888); }
  …
  .credentials-editor-input { padding: 6px 8px; font-family: var(--font-mono); font-size: var(--text-base); }
  .credentials-editor-error { margin: 0; color: var(--state-awaiting, var(--error, #c43755)); font-size: var(--text-sm); }
  ```

  **Before** (optimistic, lines 1011–1029):
  ```css
  .optimistic-failed-reason {
    font-size: 11px;
    color: var(--state-error, #f7768e);
    margin-top: 4px;
  }

  .optimistic-retry {
    font-size: 11px;
    color: var(--text-muted);
    cursor: pointer;
    margin-left: 8px;
    user-select: none;
  }
  ```

  **After:**
  ```css
  .optimistic-failed-reason {
    font-size: var(--text-xs);
    color: var(--state-error, #f7768e);
    margin-top: 4px;
  }

  .optimistic-retry {
    font-size: var(--text-xs);
    color: var(--text-muted);
    cursor: pointer;
    margin-left: 8px;
    user-select: none;
  }
  ```

  Notes:
  - `.chip-caret`, `.sp-model-caret`, `.sp-dir-caret` keep their `font-size: 9px` literal — design language explicitly allows this for "tiny carets."
  - `.image-lightbox-caption` was already migrated in Task 4.
  - `.assistant-message code`, `.settings-help code`, `.search-empty code` use `0.85em` rather than `--text-sm` so they scale with body — matches the cheatsheet "Inline code in prose" row.
  - All `.status-pill.status-*` variant background rules (lines 760–766) are NOT touched — Pass 4 removes them.

- [ ] **Step 3 — verify visually.** Click through `/settings/general`, `/settings/providers`, `/settings/plugins`, `/settings/skills`, `/settings/mcp`, `/settings/serf-launch`. Every label is sans 13/500; every value is mono 12. Help text is sans 11 light. Section subheads (settings-h3) read as small-caps mono. Open the search palette with ⌘K: results list is sans body; cmd ids are mono. Open details panel and tasks panel from the workspace header: keys mono, values mono, task ids mono. Visit credentials page: rows lay out in mono.

- [ ] **Step 4 — commit.**
  ```
  ui: migrate settings, palette, panels, credentials typography

  Settings dt/dd land on the cheatsheet (sans 13/500, mono 12/400). Search
  palette section headers and cmd-id metadata gain explicit mono. Details
  panel, tasks panel, credentials, optimistic retry chrome all use tokens.
  ```

---

## Task 9: Replace remaining line-height literals; sweep the responsive media query

- [ ] **Files:** Modify `cmd/serf-hub/assets/style.css` — scan for every remaining numeric `line-height:` value (Tasks 2–8 handled the conversation cluster, diagnostic, banner, system-line, steering, message-input, spawn-input, spawn-advanced-textarea, search-help, task-row, task-prompt). Then sweep the `@media (max-width: 767px)` block at lines 875–986 for any font-size literals.

- [ ] **Step 1 — read context.** Run `grep -n "line-height:" cmd/serf-hub/assets/style.css` and `grep -n "font-size:" cmd/serf-hub/assets/style.css | grep -v "var(--text"`. Cross-check each hit against the changes already applied. Expected remaining literals:
  - `.settings-help { ... line-height: 1.6; }` — already updated in Task 8 to `--leading-snug`. (1.5 is closer than 1.6; the cheatsheet implies tight help text.)
  - `.settings-row-desc { line-height: 1.5; ... }` — updated in Task 8 to `--leading-snug`.
  - `.shell-output / .cheap-tool-args / .cheap-tool-output { line-height: 1.4; }` — updated in Task 5 to `--leading-tight` (1.3).
  - `.task-row { line-height: 1.4; }` — updated in Task 8 to `--leading-tight`.
  - `.spawn-advanced-textarea { line-height: 1.45; }` — updated in Task 7 to `--leading-snug`.
  - `.settings-launch-textarea { line-height: 1.45; }` — updated in Task 8 to `--leading-snug`.
  - `.tasks-list .task-prompt { line-height: 1.5; }` — updated in Task 8 to `--leading-snug`.
  - `.message-input { line-height: 1.5; }` — updated in Task 6 to `--leading-snug`.
  - `.spawn-input { line-height: 1.5; }` — updated in Task 7 to `--leading-snug`.
  - `.diff-body { line-height: 1.5; }` — updated in Task 5 to `--leading-snug`.
  - **Untouched, still literal**: any `line-height: 1` on icon-only / single-line buttons (`.panel-toggle-icon`, `.composer-attachment-remove`, `.project-new-btn`, `.project-gear-btn`, `.search-cmd-pill`, `.search-cmd-pill-back`, `.sp-clear-btn`, `.tasks-list .task-row-details > summary` line 1 height, `.diagnostic-badge` 1.4). `line-height: 1` and `line-height: 1.4` on icon-aligned chrome stay literal — they're optical alignment values, not text rhythm.
  - **Untouched mobile media query**: lines 922–924 — `.header-action, .panel-toggle { font-size: 11px; }` and `.workspace-meta { font-size: 11px; }`. Migrate these. Line 912 `font-size: 18px` on `#mobile-hamburger` becomes `--text-xl`. Line 974 `line-height: 1.3` on `.search-cmd-pill` (phone wrap) becomes `--leading-tight`.

- [ ] **Step 2 — apply migration.**

  Within the `@media (max-width: 767px)` block, apply these line-by-line edits:

  **Before** (line 912):
  ```css
    font-size: 18px;
  ```

  **After:**
  ```css
    font-size: var(--text-xl);
  ```

  **Before** (lines 921–924):
  ```css
    .header-action,
    .panel-toggle { padding: 4px 8px; font-size: 11px; }
    .panel-toggle-icon { display: none; }
    .workspace-meta { font-size: 11px; }
  ```

  **After:**
  ```css
    .header-action,
    .panel-toggle { padding: 4px 8px; font-size: var(--text-xs); }
    .panel-toggle-icon { display: none; }
    .workspace-meta { font-size: var(--text-xs); }
  ```

  **Before** (line 974):
  ```css
      line-height: 1.3;
  ```

  **After:**
  ```css
      line-height: var(--leading-tight);
  ```

  Then run the sweep:

  ```bash
  grep -nE "font-size:\s*[0-9]" cmd/serf-hub/assets/style.css | grep -v "0\.85em" | grep -v "9px"
  grep -nE "line-height:\s*1\.(4|45|5|55|6|7)" cmd/serf-hub/assets/style.css
  ```

  Both must come back empty except for:
  - `.chip-caret`, `.sp-model-caret`, `.sp-dir-caret` — `font-size: 9px` (intentional tiny carets).
  - Icon-aligned `line-height: 1` declarations (intentional optical alignment).
  - `.diagnostic-badge { line-height: ... }` — Task 4 set it to `--leading-tight`, confirm.

  If any other hit appears, migrate it in this commit.

- [ ] **Step 3 — verify visually.** Resize the browser to phone width (≤767px) via dev tools. Workspace header chrome reads at 11; status pill text size is right; meta line wraps cleanly. Open the search palette on phone: cmd pills wrap with the right leading. Open the workspace at desktop again: nothing has shifted.

- [ ] **Step 4 — commit.**
  ```
  ui: finish typography token sweep — remaining line-heights and mobile

  Replaces leftover numeric line-height values with --leading-* tokens.
  Mobile media-query font-sizes migrate to tokens. Intentional literals
  (9px carets, 1.0 icon line-heights) documented in the plan.
  ```

---

## Task 10: Build, manual verify across every surface, document exceptions

- [ ] **Files:** No code changes unless verification surfaces a regression. If anything needs adjusting, repeat one of Tasks 2–9.

- [ ] **Step 1 — build.** Run `make build-hub` from the repo root. Confirm the binary builds clean. Run `make test` to make sure no Go test cares about CSS bytes. Run `make lint-naming` to confirm namingcheck still passes.

- [ ] **Step 2 — visual sweep.** Launch `./serf-hub` against a working hub config and walk every surface in both themes:

  | Surface | Check |
  | --- | --- |
  | `/` (workspace empty) | Empty body reads in sans; sidebar live rows have mono meta |
  | `/s/<id>` (open session) | Workspace title 16/sans, meta cluster 11/mono, conversation body 14/sans, tool calls 12/mono, diff 12/mono, status pill mono-uppercase, composer textarea sans 14, status row 11/mono |
  | `/new` | Spawn prompt 22/sans/600 with neg tracking, chips show mono key 10 + mono value 11, textarea sans 14, advanced summary mono, recent header mono uppercase |
  | `/settings/general` | dt sans 13/500, dd mono 12, help sans 11 light, settings-h2 sans 18 |
  | `/settings/theme` | radios + toggle sans 13 |
  | `/settings/providers` | provider rows; status pill text now mono small-caps (bg still colored — Pass 4 strips bg) |
  | `/settings/serf-launch` | label sans 12 muted, input mono 13 |
  | `/settings/plugins` + `/settings/skills` + `/settings/mcp` | row titles mono 14, meta mono 12 |
  | `/credentials` | row name sans, status mono 12, editor input mono 13 |
  | `⌘K` palette | search input sans 14, section headers mono 10 uppercase tracked, cmd ids mono 11, help row mono 12 |
  | Details panel (open from header) | label muted mono, value mono 12 |
  | Tasks panel | task rows mono 12, ids mono 11, type pills mono 10 uppercase |
  | Diagnostic in conversation | badge mono 10, body sans 12, hint muted |
  | Banner | mono-free; just sans 12 with state color |
  | Phone width 390 × 844 | header actions 11, meta line 11, search palette full-screen with right type |

  In each surface, toggle `data-theme="light"` and `data-theme="dark"` via dev tools and confirm both render correctly (no theme regressions — Pass 2 didn't change colors but the fonts may render slightly differently against a cream background).

- [ ] **Step 3 — document intentional exceptions.** Append a short comment block to the bottom of `style.css` listing each selector that intentionally keeps a non-token type literal, so Pass 3+ reviewers don't try to "fix" them:

  ```css
  /* ── Pass 2 typography exceptions ────────────────────────────────────────
     Selectors deliberately keeping a non-token font-size or line-height:

       .chip-caret              font-size: 9px       (caret glyph, too tiny for --text-2xs)
       .sp-model-caret          font-size: 9px       (same)
       .sp-dir-caret            font-size: 9px       (same)
       .assistant-message code  font-size: 0.85em    (relative to body so A/B works)
       .settings-help code      font-size: 0.85em    (same)
       .search-empty code       font-size: 0.85em    (same)
       *                        line-height: 1       (icon-aligned chrome; optical)
       .diagnostic-badge        line-height: 1.3 via --leading-tight (already token)

     Everything else uses --text-* / --leading-* tokens. If you find another
     literal, file it as a Pass 3 cleanup; do not add to this list without
     calling it out in the design-language doc.
     ──────────────────────────────────────────────────────────────────────── */
  ```

- [ ] **Step 4 — commit.**
  ```
  ui: pass 2 — verify type migration across surfaces, document exceptions

  Closes Pass 2 of the serf-hub UI overhaul. All font-size, font-family,
  and line-height declarations in style.css now reference tokens, except
  for the documented exceptions (9px carets, 0.85em inline code, line-
  height: 1 on icon-aligned chrome).

  Next: start the A/B between --text-md (14) and --text-base (13) on
  .user-message .pill + .assistant-message in the live app, pick the
  winner, update the design-language doc, and ship as a follow-up.
  ```

---

## Open follow-ups (post-Pass 2)

- **Conversation body A/B (open question in spec line 387–388).** Run 14 vs 13 in the live app for at least a day. If 13 wins, downshift `.user-message .pill` and `.assistant-message` to `--text-base` and bump tool calls from `--text-sm` to `--text-xs`. Update §1.2 of the design language and §spec §Pass 2 to reflect the decision.
- **Hanken at 11px (open question in implementation spec known-issues).** Test on physical iOS + Android. If Hanken dissolves below 12px on certain DPRs, raise Compact phone density body from 11 to 12 in Pass 5's phone density section. Pass 2 sets 11px (`--text-xs`) for several surfaces (`.input-status`, `.workspace-meta`, mobile `.header-action`); they're acceptable on desktop but verify on phone hardware.
