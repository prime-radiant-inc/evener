# Serf-Hub Design System

> Reference for engineers and designers extending the serf-hub UI.
> Captures the system AS BUILT after Pass 1-8 of the responsive UI overhaul (May 2026).
> Source of truth: `cmd/serf-hub/assets/style.css` + the partials under `cmd/serf-hub/templates/`.

The hub UI is **dense by default, with room to breathe at the touchpoints**. The aesthetic is *workshop log* — two type voices (Hanken Grotesk + JetBrains Mono), the Tokyo Night palette, paper-grain texture, and quiet motion. Color is functional (status, severity, accent); type carries identity. Add new surfaces by composing existing primitives; do not invent ad-hoc class names or use raw pixel values outside the token scale.

---

## 1. Foundations

All tokens live at the top of `style.css` under `:root`, with theme overrides via `@media (prefers-color-scheme: light)` and `:root[data-theme="light"|"dark"]`.

### 1.1 Tokens

#### Spacing scale

| Token | Value | Typical use |
| --- | --- | --- |
| `--space-0` | 0 | intentional zero |
| `--space-1` | 2px | tight badge inset, hairline offsets |
| `--space-2` | 4px | inline gap, small padding, chip gutter |
| `--space-3` | 8px | default sibling gap |
| `--space-4` | 12px | panel padding, settings-row vertical pad |
| `--space-5` | 16px | section padding, large gap |
| `--space-6` | 24px | major section spacing, header padding |
| `--space-7` | 32px | hero/empty padding, conversation horizontal pad |
| `--space-8` | 48px | spawn form padding, wide gutter |
| `--space-9` | 64px | wide-screen conversation gutter |

Layout values (padding, margin, gap, top/right/bottom/left) MUST use `--space-N`. 1px hairlines stay literal. Negative offsets use `calc(0 - var(--space-N))`.

#### Type scale

| Token | Value | Default leading | Typical use |
| --- | --- | --- | --- |
| `--text-2xs` | 10px | `--leading-tight` | Section labels (uppercase, tracked), tiny metadata |
| `--text-xs` | 11px | `--leading-tight` | Row metadata, timestamps, hints, kbd labels, help text |
| `--text-sm` | 12px | `--leading-snug` | Tool calls, diff body, status bar, captions, picker options |
| `--text-base` | 13px | `--leading-snug` | UI body — buttons, settings labels, sidebar titles |
| `--text-md` | 14px | `--leading-snug` | Conversation body (user pill + assistant), composer textarea |
| `--text-lg` | 16px | `--leading-snug` | Workspace title, empty-state title, page-header h1 |
| `--text-xl` | 18px | `--leading-snug` | Settings h2 |
| `--text-2xl` | 22px | `--leading-snug` | Spawn-prompt heading (largest surface) |

#### Leading scale

```css
--leading-tight:   1.3;
--leading-snug:    1.5;
--leading-normal:  1.6;
--leading-relaxed: 1.7;  /* reserved */
```

#### Radius scale

| Token | Value | Use |
| --- | --- | --- |
| `--radius-sm` | 3px | inline code, small inset tags |
| `--radius-md` | 4px | default — buttons, inputs, panels, banners |
| `--radius-lg` | 6px | cards, dialogs, input-card, dropdown pickers |
| `--radius-xl` | 8px | spawn input, image cards, search dialog |
| `--radius-pill` | 14px | pills (chips, message pills, status pills, btn-chip) |
| `--radius-full` | 50% | status dots, running indicator |

#### Motion

```css
--motion-fast: 100ms ease;   /* color / opacity / bg flips */
--motion-base: 160ms ease;   /* drawer slide, accordion, dropdown reveal, toast in/out */
--motion-slow: 240ms ease;   /* full-shell transitions */

--pulse-cycle: 1400ms ease-in-out;  /* optimistic-pending, running dot, skeleton shimmer */
--flash-cycle: 2000ms ease-out;     /* search-hit flash */
```

A universal `prefers-reduced-motion: reduce` rule clamps every animation and transition to 1ms. Those `!important` declarations are the only two `!important` uses in the codebase and are documented at the rule.

#### Z-index

| Token | Value | Use |
| --- | --- | --- |
| `--z-sticky` | 10 | sticky element inside a scroll container |
| `--z-fixed-action` | 100 | fixed-position action surface, connection banner, details panel |
| `--z-dropdown` | 200 | dropdown picker, popover |
| `--z-overlay` | 800 | backdrop scrim |
| `--z-drawer` | 900 | slide-over panel, mobile sidebar |
| `--z-modal` | 1000 | modal dialog (search palette) |
| `--z-toast` | 1100 | toast region (above modal) |

#### Touch target / accessibility

`--tap-min: 32px` desktop default. Phone media query overrides to 44px. Every interactive control meets `--tap-min` via `min-height` (or `padding` for in-prose links). Compact composer chips with a visible chrome smaller than `--tap-min` extend their hit-box via padding.

#### Texture

```css
--noise: url("data:image/svg+xml;utf8,<svg ...feTurbulence baseFrequency='0.85' .../><feColorMatrix .../></svg>");
```

Inline SVG fractal noise sampled to 5% white opacity. Applied via `::after` pseudo with `mix-blend-mode: overlay` and ~0.5 opacity on raised surfaces (`.input-card`, `.diagnostic`, `.fork-dialog`, user pill, workspace shell). Skipped on text-heavy surfaces — transcript body and settings content — so the grain does not fight readability.

#### Font tokens

```css
--font-sans: 'Hanken Grotesk', -apple-system, BlinkMacSystemFont, "Segoe UI", "Helvetica Neue", Arial, sans-serif;
--font-mono: 'JetBrains Mono', ui-monospace, "SFMono-Regular", Menlo, Monaco, Consolas, monospace;
```

Both faces are loaded from Google Fonts; the CSP allows `fonts.googleapis.com` and `fonts.gstatic.com`. Vendoring WOFF2 files into `cmd/serf-hub/assets/fonts/` is the documented alternate path for offline operation.

### 1.2 Typography

Two voices, one boundary: **the meaning of the text, not its surface.**

- **Sans (Hanken Grotesk):** prose, UI controls, headings, button labels, navigation, session titles, settings labels (dt). The human reading the log.
- **Mono (JetBrains Mono):** code, file paths, model identifiers, branch names, timestamps, key-value metadata, kbd hints, status labels (small-caps), section labels (uppercase + tracked), settings values (dd). The log itself.

#### Voice-by-surface cheatsheet

| Surface | Voice | Token |
| --- | --- | --- |
| Workspace title | sans | `--text-lg / 600` |
| Conversation user pill + assistant body | sans | `--text-md / 400` |
| Tool call (verb + target) | mono | `--text-sm / 400` |
| Diff body | mono | `--text-sm / 400` |
| Inline `<code>` in prose | mono | `0.85em / 400` |
| Sidebar session title | sans | `--text-base / 400` |
| Sidebar meta (project · age) | mono | `--text-2xs / 350` |
| Sidebar section header (LIVE, PROJECTS) | mono | `--text-2xs / 500` uppercase + `0.16em` |
| Project header name | mono | `--text-2xs / 500` uppercase + `0.14em` |
| Composer textarea | sans | `--text-md / 400` |
| Composer status row | mono | `--text-xs / 400` |
| Status badge | mono | `--text-xs / 500` uppercase + `0.08em` |
| Spawn prompt question | sans | `--text-2xl / 600 / -0.018em` |
| Spawn chip key | mono | `--text-2xs / 400` |
| Spawn chip value | mono | `--text-xs / 400` |
| Settings dt (label) | sans | `--text-base / 500` |
| Settings dd (value) | mono | `--text-sm / 400` |
| Settings help | sans | `--text-xs / 350` |
| Button label | sans | `--text-sm / 500` (primary uses base / 600) |
| kbd hint | mono | `--text-xs / 600` |

Inline code in prose uses `font-size: 0.85em` (not a fixed px) so it scales with the surrounding text. Documented exceptions where a literal stays: `.chip-caret`, `.sp-model-caret`, `.sp-dir-caret` (`9px` glyph carets), and `line-height: 1` on icon-aligned chrome.

### 1.3 Color

Token-driven. Light and dark are mirrors, not subsets — every token is defined in all four blocks (`:root`, `:root[data-theme="dark"]`, `@media (prefers-color-scheme: light)`, `:root[data-theme="light"]`). Light mode intentionally darkens accents because cream backgrounds need more contrast than near-black.

#### Surface + text tokens

| Token | Dark | Light | Role |
| --- | --- | --- | --- |
| `--bg` | `#0a0a0e` | `#fafafa` | Page background |
| `--bg-raised` | `#16161e` | `#f1f1f2` | Elevated surface (panels, cards, hover targets) |
| `--surface-secondary` | `#1c1c24` | `#e6e6e8` | Inset surface within a panel |
| `--text` | `#ececf0` | `#16161e` | Primary text |
| `--text-muted` | `#7a7a86` | `#5e5e6a` | Secondary text, captions, mono metadata |
| `--text-dim` | `#5a5a64` | `#8a8a92` | Tertiary text, icon glyphs, off-state controls |
| `--rule` | `#1a1a20` | `#dadadc` | Borders, dividers, hairlines |
| `--rule-soft` | `color-mix(rule 50%, transparent)` | same | Inner hairlines inside lists |
| `--accent` | `#7aa2f7` | `#2e58b8` | Primary action, links, focus rings |
| `--accent-secondary` | `#bb9af7` | `#5e35b6` | Subagent / secondary highlight |

#### State colors

Drive status dots, live-row tints, banners, badges, and the `data-state` attribute system:

| Token | Dark | Light | Role |
| --- | --- | --- | --- |
| `--state-awaiting` | `#f7768e` | `#b62a48` | Awaiting human · errors |
| `--state-processing` | `#7aa2f7` | `#2e58b8` | Active · in-flight work |
| `--state-warning` | `#e0af68` | `#8a5a14` | Warning · attention |
| `--state-idle` | `#9ece6a` | `#336a14` | Idle · success · completion |
| `--state-ended` | `#3a3a44` | `#7a7a82` | Closed · ended · neutral |
| `--state-subagent` | `#bb9af7` | `#5e35b6` | Subagent / fork glyph |

#### Button primary text

```css
:root, [data-theme="dark"]  { --btn-primary-text: var(--bg);   /* near-black on light blue accent */ }
[data-theme="light"]        { --btn-primary-text: #fafafa;     /* cream on dark blue accent */ }
```

Dark `--accent` over `#fafafa` only reaches 2.41:1 (fails WCAG-AA). Inverting to `--bg` reaches ~7.2:1.

#### Diagnostic source aliases

```css
--diagnostic-provider: var(--state-awaiting);
--diagnostic-serf:     var(--state-warning);
--diagnostic-hub:      var(--state-processing);
--diagnostic-ui:       var(--state-subagent);
```

A `.diagnostic` card's accent comes from `var(--diagnostic-accent)` — set by a class like `.diagnostic-source-hub`. Adding a new source means aliasing a state color, not a hex literal.

#### Legacy aliases (still defined)

`--panel`, `--panel-2`, `--border`, `--muted`, `--accent-2`, `--tool`, `--user`, `--error`, `--pad` are still defined in every theme block. They alias the canonical tokens. New rules MUST use canonical names. The aliases survive for safe migration of any rule not yet rewritten; deleting them is a cleanup task once a final audit confirms zero references.

### 1.4 Themes

`document.documentElement.dataset.theme` ∈ `light | dark | (unset)`, managed by `theme.js` via `localStorage["serf-hub.theme"]`. Unset falls back to `prefers-color-scheme`.

The **four-block theme pattern**: every color token is defined in (1) `:root` (dark default), (2) `@media (prefers-color-scheme: light) :root`, (3) `:root[data-theme="dark"]` (force dark over media query), (4) `:root[data-theme="light"]` (force light over media query). Adding a new color token means touching all four blocks. There is no inheritance shortcut — explicit duplication keeps the cascade legible.

Light-theme sidebar row tints use a higher mix percentage (12% vs 5%) because 5% is invisible against `#fafafa`. The override lives in dedicated `[data-theme="light"]` selectors right after the theme blocks.

### 1.5 Motion + reduced motion

Three durations (`--motion-fast/-base/-slow`) plus two semantic cycles (`--pulse-cycle`, `--flash-cycle`). Every transition or animation uses one of these tokens — no literal `ms` values outside the token definitions.

Reduced motion is the universal kill switch:

```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 1ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 1ms !important;
  }
}
```

Components whose meaning depends on animation get non-motion fallbacks declared inside their own `@media (prefers-reduced-motion: reduce)` block:

```css
@media (prefers-reduced-motion: reduce) {
  .optimistic-pending {
    border-left: 2px dashed var(--accent);
    padding-left: var(--space-2);
  }
  .status-dot[data-pulse] {
    box-shadow: 0 0 0 2px color-mix(in srgb, currentColor 30%, transparent);
  }
}
```

---

## 2. Components

### 2.1 Buttons

Six variants on a `.btn` base. All inherit min-height `--tap-min`, sans `--text-sm / 500`, `--radius-md`, and motion-token transitions.

```css
.btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-4);
  min-height: var(--tap-min);
  font: inherit;
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  font-weight: 500;
  color: var(--text);
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background var(--motion-fast), color var(--motion-fast),
              border-color var(--motion-fast), filter var(--motion-fast);
}
.btn[disabled], .btn:disabled { opacity: 0.45; cursor: not-allowed; }
```

| Variant | Background | Color | Border | Hover | Use |
| --- | --- | --- | --- | --- | --- |
| `.btn-primary` | `--accent` | `--btn-primary-text` | `--accent` | `filter: brightness(1.08)` | Send · spawn · save · fork confirm |
| `.btn-secondary` | `--bg-raised` | `--text` | `--rule` | bg → `--surface-secondary` | Common controls |
| `.btn-ghost` | transparent | `--text-muted` | transparent | bg `--bg-raised`, color `--text` | Header actions · cancel · ⋯ |
| `.btn-danger` | `color-mix(awaiting 5%, transparent)` | `--state-awaiting` | `color-mix(awaiting 40%, transparent)` | bg+border brighten | Stop · shutdown · destructive |
| `.btn-icon` | transparent | `--text-muted` | transparent | bg `--bg-raised`, border `--rule` | Single glyph (≥32px desktop, ≥44px phone) |
| `.btn-chip` | `--bg-raised` | `--text` | `--rule` (pill radius) | border `--accent` | Spawn picker triggers (`key`+`val` mono spans inside) |

`:active` drops surface one step and translates 0.5px down (`transform: translateY(0.5px)`). `.btn-primary:active` uses `filter: brightness(0.95)`. Disabled `.btn-danger` reverts to neutral muted (not "active red") so accidental tap surfaces feel safer.

`.btn-ghost[data-active]` is the toggled state for panel toggles (tasks, details). Active = `--bg-raised` background + `--rule` border. The `data-active` attribute makes it CSS-only — no class-flipping in JS.

`.btn-chip[data-mode]` (e.g. access_mode chip on spawn) tints with `color-mix(state-warning 12%, bg-raised)` to communicate a non-default mode.

`.btn-primary kbd` renders an inline kbd glyph with a translucent `--btn-primary-text` background — works in both themes because the kbd inherits the parent's resolved button-text color.

### 2.2 Status indicators

Two shapes, both driven by `[data-state]`. There is **no `.status-pill` background-fill component** — status is communicated typographically via small-caps mono in the state color.

**`.status-dot`** — 6×6 circle, `--radius-full`. Default background `--state-ended`; `[data-state]` overrides.

```css
.status-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--state-ended); }
.status-dot[data-state="active"]    { background: var(--state-processing); }
.status-dot[data-state="awaiting"]  { background: var(--state-awaiting); }
.status-dot[data-state="errored"]   { background: var(--state-awaiting); }
.status-dot[data-state="warning"]   { background: var(--state-warning); }
.status-dot[data-state="idle"]      { background: var(--state-idle); }
.status-dot[data-state="closed"],
.status-dot[data-state="notLoaded"] { background: var(--state-ended); }
.status-dot.subagent                { background: var(--state-subagent); }
.status-dot[data-pulse]             { animation: status-dot-pulse var(--pulse-cycle) ease-in-out infinite; }
```

`[data-pulse]` is added by `renderer.js` (`applyStatusDotPulse`) when `data-state` ∈ `active | awaiting | errored`. The reduced-motion fallback replaces the animation with a `box-shadow` halo so the pulse signal does not vanish.

**`.status-badge`** — typographic. No background fill. Just a dot + small-caps mono label in the state color.

```css
.status-badge {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  font-weight: 500;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--text-muted);
}
.status-badge[data-state="active"]    { color: var(--state-processing); }
.status-badge[data-state="awaiting"]  { color: var(--state-awaiting); }
.status-badge[data-state="errored"]   { color: var(--state-awaiting); }
.status-badge[data-state="warning"]   { color: var(--state-warning); }
.status-badge[data-state="idle"]      { color: var(--state-idle); }
.status-badge[data-state="ended"]     { color: var(--text-muted); }
.status-badge[data-state="closed"]    { color: var(--text-muted); }
.status-badge[data-state="notLoaded"] { color: var(--text-muted); }
```

#### `data-state` values

| Value | Meaning | Color | Pulse |
| --- | --- | --- | --- |
| `active` | Turn in flight | `--state-processing` | yes |
| `awaiting` | Awaiting human input | `--state-awaiting` | yes |
| `errored` | Provider/serf/hub error | `--state-awaiting` | yes |
| `warning` | Soft problem | `--state-warning` | — |
| `idle` | Sitting quiet | `--state-idle` | — |
| `ended` | Closed, no daemon | `--state-ended` | — |
| `closed` | Past session, no live daemon | `--state-ended` | — |
| `notLoaded` | Past-session result not yet hydrated | `--state-ended` | — |
| (empty) | Unknown | `--state-ended` | — |

Provider/MCP/credentials sub-pages keep their legacy state class names (`.status-running`, `.status-available`, `.status-error`, `.status-missing`, `.status-unreachable`, `.status-stopped`, `.status-unknown`, `.status-absent`, `.status-file`, `.status-env`, `.status-oauth`, `.status-none`); they alias onto the same color logic so existing `status-${activeSource}` template emission keeps working.

**`.fork-glyph`** — monospace `⎇` glyph, color follows `[data-state]` like the dot.

### 2.3 Settings tables

The unified primitive for label → value settings. Read-only and editable share one row; what changes is what lives in `<dd>`.

```html
<dl class="settings-table">
  <div class="row">
    <dt>Hub address</dt>
    <dd>127.0.0.1:9180</dd>
  </div>
  <div class="row editable">
    <dt>Title bar count</dt>
    <dd>
      <label class="val-toggle">
        <input type="checkbox" checked>
        <span class="state">ON</span>
      </label>
    </dd>
    <p class="help">Number of awaiting sessions shown in the tab title.</p>
  </div>
  <div class="row">
    <dt>Access mode</dt>
    <dd>
      <div class="val-radio-group">
        <label class="val-radio"><input type="radio" name="access"> read-only</label>
        <label class="val-radio"><input type="radio" name="access" checked> normal</label>
        <label class="val-radio"><input type="radio" name="access"> full-auto</label>
      </div>
    </dd>
  </div>
</dl>
```

Geometry: each `.row` is a 2-column grid (`160px 1fr`), baseline-aligned, with `--rule` hairline borders. `.help` lives at `grid-column: 1 / -1` so it spans both columns on the next grid row. `max-width: 760px` keeps lines comfortably scannable. `.row.editable` adds `cursor: pointer` and a 2% text-tint hover.

#### Value-cell variants

| Variant | Use |
| --- | --- |
| plain `<dd>` content | Read-only mono text |
| `.val-text` (with `.dim` subspans) | Read-only with annotation |
| `.val-input` | Text input (mono) — focus ring on `--accent`, `[aria-invalid="true"]` flips to `--state-awaiting` |
| `.val-select` | Dropdown (same shape as `.val-input`) |
| `.val-radio-group` + `.val-radio` | Inline segmented radios (pill-shaped, `[data-checked]` or `:has(input:checked)` accent fill) |
| `.val-toggle` | Custom-styled checkbox with sliding thumb + ON/OFF mono `.state` pill |
| `<span class="status-badge" data-state="…">` | Stateful read-only |

#### Section-header row

For `LaunchConfigControls.render()` to embed fieldset legends inside the flat table primitive:

```css
.settings-table .row.section-header {
  grid-template-columns: 1fr;
  padding: var(--space-5) 0 var(--space-2);
  border-bottom: none;
}
.settings-table .row.section-header dd {
  grid-column: 1 / -1;
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  font-weight: 500;
  letter-spacing: 0.14em;
  text-transform: uppercase;
  color: var(--text-muted);
}
.settings-table .row.section-header + .row { border-top: 1px solid var(--rule); }
```

The trailing rule re-establishes the top hairline on the next regular row (which the section-header row drops).

### 2.4 Settings collections

For dynamic add/remove lists — Plugins, Skills, MCP servers (×2: inline + config files), Credentials. The shape is an unbounded sequence rather than a fixed set of labeled fields.

```html
<section class="settings-collection">
  <header class="settings-collection-head">
    <h3>Skill directories</h3>
    <span class="settings-collection-count">5 entries</span>
  </header>
  <ul class="settings-collection-list" role="list">
    <li class="settings-collection-row">
      <div>
        <div class="row-text">~/.claude/skills</div>
        <div class="row-meta">8 skills · user</div>
      </div>
      <div class="row-actions">
        <button class="btn-icon" aria-label="Remove">×</button>
      </div>
    </li>
  </ul>
  <form class="settings-collection-add">
    <input class="val-input" placeholder="add a skill directory…">
    <button class="btn btn-secondary" type="submit">＋ Add</button>
  </form>
</section>

<p class="settings-collection-empty">No directories configured.</p>
```

Each row is a 2-column grid (`1fr auto`) with the body left, actions right. Row hairline is `--rule-soft` (inner) rather than `--rule` (outer). Remove buttons are **persistent** — no hover-only opacity reveal — so touch users see them. Sub-pages may render multiple collections (MCP has Inline + Config-file collections in one pane).

### 2.5 Sidebar rows

The `.sb-row` is a 2-column grid (`dot-col + text-col`) with a 2-line title wrap and a mono meta line below. Titles like "refactor auth middleware to use the new session token store" stay fully readable.

```html
<a class="sb-row" data-state="awaiting" href="/s/abc123">
  <div class="dot-col"><span class="status-dot" data-state="awaiting" data-pulse></span></div>
  <div class="text-col">
    <div class="title">add input validation to signup handler</div>
    <div class="meta"><span>prime-radiant/serf</span><span class="sep">·</span><span>2m</span></div>
  </div>
</a>
```

```css
.sb-row {
  display: grid;
  grid-template-columns: 10px 1fr;
  gap: var(--space-3);
  padding: 5px var(--space-4);
  border-left: 2px solid transparent;
  color: var(--text);
  text-decoration: none;
  cursor: pointer;
  transition: background var(--motion-fast);
}
.sb-row .title { font-size: var(--text-base); line-height: 1.35; -webkit-line-clamp: 2; -webkit-box-orient: vertical; display: -webkit-box; overflow: hidden; word-break: break-word; }
.sb-row .meta  { margin-top: 2px; font-family: var(--font-mono); font-size: var(--text-2xs); color: var(--text-muted); letter-spacing: 0.02em; line-height: 1.2; display: flex; align-items: baseline; gap: var(--space-2); }

.sb-row[data-state="awaiting"] { background: color-mix(in srgb, var(--state-awaiting) 5%, transparent); border-left-color: var(--state-awaiting); }
.sb-row[data-state="active"]   { background: color-mix(in srgb, var(--state-processing) 5%, transparent); border-left-color: var(--state-processing); }
.sb-row[data-state="warning"]  { background: color-mix(in srgb, var(--state-warning)  5%, transparent); border-left-color: var(--state-warning); }
.sb-row[data-active] { background: color-mix(in srgb, var(--accent) 10%, transparent); border-left-color: var(--accent); }
```

#### Modifiers

- `.sb-row.sub` — subagent indent, `padding-left: var(--space-7)`; title drops to `--text-sm` + muted.
- `.sb-row.fork` — fork variant, title in muted. Uses `.fork-glyph` instead of `.status-dot` in `dot-col`.
- `[data-active]` — set by `sidebar.js` on the row whose `href` matches the currently-rendered workspace URL (`htmx:afterSwap` hook).

#### Rail mode

`body.app[data-sidebar-rail]` collapses the sidebar to 56px: hides text columns, section headers, project name/count/folder/gear/new, and centers `.sb-row` content. Toggled by the `.sidebar-rail-toggle` button at the top of the sidebar header and persisted to `localStorage["serf-hub.sidebar.rail"]`. `⌘B / Ctrl+B` shortcut.

A container query (`@container sidebar (max-width: 80px)`) provides the same collapse independent of the body attribute, in case a parent ever shrinks the sidebar structurally.

### 2.6 Workspace header

```
[mobile: ☰] [title · meta · meta · status-badge] [tasks · details · ⋯]
```

- `.header-hamburger` lives inline at the leftmost slot of `.workspace-title-row`, displayed only at `max-width: 767px`. Partials that ship without a header (empty state, settings) consequently lose the hamburger affordance — tap-out, `⌘B`, and the search palette remain available.
- `.workspace-title .title` truncates with ellipsis when narrow.
- `.workspace-meta` is a mono `flex; gap: var(--space-4); flex-wrap: wrap` cluster. **No `.rule-dot` separators** — gap + muted color carries the work with less noise.
- `.status-badge` anchors the right edge of the meta cluster.
- `.workspace-actions` clusters action buttons at the right; on phone the `panel-toggle-icon` text collapses to icon-only.

### 2.7 Conversation tier

The conversation pane sits directly on `--bg`, uses `--text-md` body / `--leading-snug`, and declares a custom property `--tool-indent: 36px` that drives every tool-call left margin. Container query at 599px drops `--tool-indent` to 20px and tightens padding.

```css
.conversation {
  flex: 1;
  min-height: 0;
  padding: var(--space-5) var(--space-6);
  overflow-y: auto;
  font-size: var(--text-md);
  line-height: var(--leading-snug);
  color: var(--text);
  container-type: inline-size;
  container-name: conversation;
  --tool-indent: 36px;
}
@container conversation (max-width: 599px) {
  .conversation { --tool-indent: 20px; padding: var(--space-3); }
}
```

| Element | Class | Shape |
| --- | --- | --- |
| User pill | `.user-message .pill` | `max-width: min(62%, 540px)`, `--bg-raised`, `--radius-pill`, `--text-md`. Phone bumps max-width to 90%. |
| User actions | `.user-message-actions` | Hover-only (mouse) above the pill; the wrapper reserves 22px above the pill for hit-zone. |
| Assistant body | `.assistant-message` | `max-width: 680px`, `--text-md / --leading-snug`. Inline `code` at `--bg-raised / 0.85em`. `pre` at `--bg-raised / --radius-lg / --text-sm`. |
| Tool call | `.tool-call` | Mono `--text-sm` flex cluster: `.tool-status` (12px pending/good/bad glyph), `.verb`, `.target`, `.result`, `.tool-meta` (right-aligned). |
| Tool body | `.tool-body`, `.shell-output`, `.cheap-tool-args`, `.cheap-tool-output` | `surface-inset`-style block at `var(--tool-indent)` left margin, mono `--text-sm / --leading-tight`. |
| Diff body | `.diff-body` | Inset surface, mono `--text-sm`, `white-space: pre`, `overflow-x: auto`. `.add` / `.del` / `.hunk` color variants. |
| Subagent ref | `.subagent-reference` | `var(--state-subagent)` verb, dimmed text, clickable. |
| Diagnostic | `.diagnostic` | Raised surface, `border-left: 3px solid var(--diagnostic-accent)`, `.diagnostic-source-{provider,serf,hub,ui}` switches the accent. Inner `.diagnostic-header` + `.diagnostic-badge` + `.diagnostic-title` + `.diagnostic-message` + `.diagnostic-hint`. |
| Banner | `.banner` + `.error / .warning / .note` | Single-line, left-border colored, `--text-sm`. |
| System line | `.system-line` | `--text-sm`, muted, italic, indented `--space-7`. For task transitions, full-list pointers. |
| Steering | `.steering` (a `<details>`) | Full-width divider with summary centered between rules; `.steering-verb` in `--state-warning` mono. |
| Fork dialog | `.fork-dialog` | Inline raised surface, `max-width: 480px; margin-left: auto`. |

### 2.8 Composer

Three zones in `.input-controls`:

- `.controls-left` — file picker + advanced controls.
- `.controls-center` — running indicator (active turn pulse).
- `.controls-right` — Send (primary), Stop (danger), Send-as-steer (ghost). **Three separate buttons, not a split.**

Above the controls sits `.composer-attachments` — a single rail consolidating paste/drag-drop/file-picker chips, owned by `SerfComposerAttachments`. Hides itself when empty (`:empty { display: none }`).

Below the controls sits `.input-status` — one mono `--text-xs` line with `.status-item` clusters:

```css
.input-status { display: flex; align-items: center; gap: var(--space-5); padding: var(--space-3) 0 0; margin-top: var(--space-2); border-top: 1px solid var(--rule); font-family: var(--font-mono); font-size: var(--text-xs); flex-wrap: wrap; }
.input-status .status-item  { display: inline-flex; align-items: center; gap: var(--space-2); white-space: nowrap; }
.input-status .status-key   { color: var(--text-dim); }
.input-status .status-value { color: var(--text); }
.input-status .cwd .status-value { max-width: 260px; overflow: hidden; text-overflow: ellipsis; }
.input-status .context-bar  { width: 80px; height: 3px; background: var(--bg); border-radius: var(--radius-sm); overflow: hidden; }
.input-status .context-fill { background: var(--state-processing); }
```

The `.queue-preview` block hovers above the textarea while a turn is in flight, showing messages that will become fresh user turns. A `?` glyph (`.queue-preview-help`) is the kbd hint; clicking it expands the explanation.

`.input-card` is the textarea wrapper. `.drop-active` (dragenter) adds a dashed accent outline; no recolor, so dropping into the existing composer keeps its border style.

### 2.9 Search palette

`<dialog id="search-dialog">` opened by `⌘K / Ctrl+K`. Native `<dialog>` gives focus trap + Esc handling for free; `SerfFocusTrap` is wired in for consistency.

- `.search-dialog-inner` — 560px centered raised surface on desktop, full-screen sheet on phone (margin 0, height 100vh, border-radius 0).
- `.search-dialog-header` — search icon + `#search-input` (transparent, `--text-md`, no border) + hint kbd. Sticky on phone.
- `.search-results` — `max-height: 400px; overflow-y: auto`. Sections separated by `.search-section-header` (uppercase mono `--text-2xs`).
- `.search-row` — flex with `.status-dot` + `.search-title` + `.search-project` (mono `--text-xs`) + `.search-age`. `:focus-visible` uses `outline-offset: -2px` so the ring stays inside the rounded row.
- `.search-help-row` — keyboard shortcut help with mono `.search-help-keys`.
- `.search-hit` — applied to a destination element when the palette jumps to a result; outlines with `--state-warning`, runs a `--flash-cycle` animation.

Phone variant scales `.search-row` to `min-height: 48px` and lets `.search-cmd-pill` wrap.

### 2.10 Toasts

`#toast-region` lives at body level (rendered by `app.html`) with `aria-live="polite"`, positioned **top-center** at `--z-toast`.

```css
#toast-region {
  position: fixed;
  top: var(--space-4);
  left: 50%;
  transform: translateX(-50%);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  z-index: var(--z-toast);
  pointer-events: none;
  max-width: min(560px, calc(100vw - var(--space-4) * 2));
}
.toast {
  pointer-events: auto;
  padding: var(--space-3) var(--space-4);
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  color: var(--text);
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  box-shadow: 0 8px 24px rgba(0,0,0,0.3);
  animation: toast-enter var(--motion-base) both;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 240px;
}
.toast.toast-success { border-left: 3px solid var(--state-idle); }
.toast.toast-error   { border-left: 3px solid var(--state-awaiting); }
.toast.toast-info    { border-left: 3px solid var(--accent); }
.toast.toast-dismissing { animation: toast-exit var(--motion-base) both; }
```

API:

```js
const handle = window.SerfToast.show(message, kind, opts);
// kind ∈ "success" | "error" | "info" (unknown kinds become "info")
// opts.timeout: number ms; default 3000; 0 disables auto-dismiss
window.SerfToast.dismiss(handle);             // no-op for unknown handle
window.SerfToast.success(msg, opts);
window.SerfToast.error(msg, opts);
window.SerfToast.info(msg, opts);
```

`role="alert"` for `error` kind, `role="status"` otherwise. Built-in trigger surfaces: copy session ID, model change, session shutdown, settings saved, theme change, attachment rejected, connection lost/restored, htmx error.

`.connection-banner` is a persistent sticky banner paired with the `connection-lost` toast — transient toast alone is insufficient when the whole UI is stale.

### 2.11 Skeletons

```css
.skeleton {
  display: block;
  border-radius: var(--radius-sm);
  background: var(--bg-raised);
  height: 12px;
  min-width: 40px;
  margin: var(--space-2) 0;
}
[data-loading] .skeleton {
  background: linear-gradient(90deg, var(--bg-raised) 0%, var(--surface-secondary) 50%, var(--bg-raised) 100%);
  background-size: 200% 100%;
  animation: skeleton-shimmer var(--pulse-cycle) infinite linear;
}
[data-loading] .skeleton-row {
  display: grid;
  grid-template-columns: 6px 1fr 40px;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
}
[data-loading] .skeleton-row .skeleton.dot   { height: 6px; min-width: 6px; border-radius: 50%; }
[data-loading] .skeleton-row .skeleton.title { height: 12px; width: 70%; }
[data-loading] .skeleton-row .skeleton.meta  { height: 10px; width: 32px; }
```

Without `[data-loading]` on an ancestor, `.skeleton` is an invisible spacer (height preserves layout so the eventual swap does not jump). The `data-loading` attribute is set by `skeleton.js` on `htmx:beforeRequest` and cleared on `htmx:afterSwap` / `htmx:responseError` / `htmx:sendError` / `htmx:swapError`.

`.skeleton-turn` is a transcript-turn-shaped variant used during session swap. Reduced-motion respects the universal kill switch.

Note: `.skeleton-row` is laid out only inside `[data-loading]`. Outside that context (e.g. on the initial sidebar render template), the rows have no `display: grid` rule, so the bare spans render as inline elements until the swap finishes — see §6.

### 2.12 Empty states

```css
.empty-state {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  align-items: center;
  text-align: center;
  padding: var(--space-6) var(--space-4);
  color: var(--text-muted);
  max-width: 480px;
  margin: 0 auto;
}
.empty-state-title { font-family: var(--font-sans); font-size: var(--text-lg); font-weight: 500; color: var(--text); }
.empty-state-body  { font-family: var(--font-sans); font-size: var(--text-sm); color: var(--text-muted); line-height: 1.55; }
.empty-state-actions { display: flex; flex-wrap: wrap; gap: var(--space-2); justify-content: center; margin-top: var(--space-2); }
```

Per-surface variants tune padding and alignment:

| Variant | Padding | Align |
| --- | --- | --- |
| `.empty-state-workspace` | `padding-top: var(--space-8)` | centered |
| `.empty-state-conversation` | `var(--space-4) 0` | left-aligned |
| `.empty-state-sidebar` | `var(--space-4) var(--space-3)` | centered, smaller title (`--text-sm`) |
| `.empty-state-search` | `var(--space-4)` | centered |
| `.empty-state-tasks` | `var(--space-3)` | left-aligned |
| `.empty-state-picker` | `var(--space-3)` | left-aligned |

---

## 3. Patterns

### 3.1 Focus management

Universal `:where(:focus-visible)` ring with **zero specificity** so per-component overrides win:

```css
:where(:focus-visible) {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
```

Rounded controls where `outline-offset: 1px` would visually escape the radius use `outline-offset: -2px`. Explicit list:

```css
.search-row:focus-visible,
.sb-row:focus-visible,
.settings-nav-link:focus-visible,
.row-icon-btn:focus-visible,
.spawn-recent-row:focus-visible,
.tasks-list .task-row-details > summary:focus-visible,
.search-cmd-pill-back:focus-visible,
.composer-attachment-remove:focus-visible,
.attachment-chip .att-remove:focus-visible,
.chip-picker-option:focus-visible,
.chip-picker-model:focus-visible,
.chip-picker-provider:focus-visible,
.chip-picker-dir-row:focus-visible {
  outline-offset: -2px;
}
```

#### Focus trap (slide-overs)

`window.SerfFocusTrap.activate(panelEl, returnFocusTo)` and `.deactivate(handle)`:

1. Captures `document.activeElement` (or explicit trigger) as restore target.
2. Applies `inert` to every root-level sibling of `panelEl`.
3. Binds a Tab handler that cycles focus inside `panelEl`.
4. Focuses the first focusable child (or `panelEl` itself with a temporary `tabindex="-1"`).

On deactivate: removes `inert`, unbinds handler, restores focus to the trigger if it is still in the DOM.

Wired into: mobile sidebar drawer (`#sidebar`), tasks/details slide-overs, the search palette. Multiple handles can be active concurrently and torn down in any order. The trap handle is stashed on the panel as `__trapHandle` so opens and closes can locate it.

### 3.2 Optimistic UI

Owned by `pending.js` (`window.SerfAppwirePending`).

```css
.optimistic-pending { animation: optimistic-pulse var(--pulse-cycle) infinite; }
.optimistic-failed  { border-left: 2px solid var(--state-awaiting); padding-left: var(--space-3); }
.optimistic-failed-reason { font-size: var(--text-xs); color: var(--state-awaiting); margin-top: var(--space-2); }
.optimistic-retry { font-size: var(--text-xs); color: var(--text-muted); cursor: pointer; margin-left: var(--space-3); }
@keyframes optimistic-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.65; } }
```

`[data-pending-id]` carries the pending registry handle. Participating components today: user-message, steering, queue-preview-item. New components that need optimistic state apply `.optimistic-pending` while in flight, swap to `.optimistic-failed` + `.optimistic-failed-reason` on rejection, and clear both on reconcile.

Reduced-motion fallback replaces the opacity pulse with a dashed accent left border so the pending signal survives.

### 3.3 Status pulse / live updates

`.status-dot[data-pulse]` runs the 1400ms `--pulse-cycle` opacity animation. The attribute is added/removed by `renderer.js`'s `applyStatusDotPulse(root)`, called on:
- htmx swaps that touch the sidebar.
- AppWire notifications that change state.
- The active turn changing.

The reduced-motion fallback replaces the animation with a `box-shadow` halo on the dot so the pulse signal does not disappear.

### 3.4 Responsive breakpoints

Two viewport breakpoints — phone and wide. Tablet portrait inherits desktop.

| Name | Condition | Behavior |
| --- | --- | --- |
| Phone | `max-width: 767px` | Off-canvas sidebar, full-screen drawers, stacked composer controls, larger touch targets (`--tap-min: 44px`) |
| Phone landscape | `max-width: 767px and max-height: 500px` | Compresses vertical chrome |
| Default | `min-width: 768px` | Two-pane shell with inline controls |
| Wide | (no explicit breakpoint, max-width caps used per-surface) | Conversation pane caps at ~920px; sidebar may use rail mode |

Container queries handle surface-level responsiveness — used on `.conversation` (`@container conversation (max-width: 599px)` tightens tool-indent + padding) and `#sidebar` (`@container sidebar (max-width: 80px)` hides text columns). Media queries are reserved for shell-level decisions (sidebar drawer vs pane, hamburger placement, settings nav-as-page).

**Phone settings navigation** is nav-as-page: at `max-width: 767px`, `body[data-settings-pane="nav"]` shows the nav and hides the content; `body[data-settings-pane="content"]` flips it and shows the back chevron in the sub-page header. URL routing is unchanged.

### 3.5 Drag-and-drop, paste, file picker

`SerfComposerAttachments` (composer-attachments.js) is the shared helper used by both the session workspace composer (`renderer.js`) and the spawn form (`spawn.js`).

- A single `.composer-attachments` rail per surface renders chips.
- Drop targets carry `.drop-active` (set on dragenter) which adds a dashed accent outline.
- Rejected attachments fire a `SerfToast.error("Attachment rejected", ...)` plus an inline `.composer-attachment-error` banner.

---

## 4. JavaScript helpers

### 4.1 Module list

Files in `cmd/serf-hub/assets/`:

| File | Role |
| --- | --- |
| `focus-trap.js` | `SerfFocusTrap.activate/deactivate` — slide-over focus management |
| `toast.js` | `SerfToast.show/dismiss/success/error/info` — top-center notifications |
| `skeleton.js` | Toggles `data-loading` on htmx swap targets so `.skeleton` shimmers during requests |
| `chip-overflow.js` | Caps visible `.chip` children at 4 (most-recently-modified) in `[data-chip-overflow-host]` containers, with a `+N` overflow chip |
| `sidebar.js` | Project collapse/expand persisted to `localStorage["serf-hub.sidebar.expanded.<key>"]`, rail-toggle, `data-active` row marker, first-paint stagger |
| `search.js` | `SerfSearch.open/close/openWith` — search palette UI |
| `settings.js` | Settings page interactivity via body-level event delegation (survives htmx swaps); also exposes `SerfSectionLabels` |
| `settings-pickers.js` | `sp-model-btn` / `sp-dir-btn` / `sp-clear-btn` picker widgets used inside settings cells |
| `renderer.js` | `SerfRenderer` — conversation transcript rendering, status-dot pulse application, banner append |
| `spawn.js` | `SerfSpawn` — spawn form interaction |
| `pending.js` | `SerfAppwirePending.create({...})` — optimistic-rendering registry |
| `appwire.js` | `SerfAppwire` — RPC wrapper, AppWire notification subscription, connection-loss callbacks |
| `composer-attachments.js` | `SerfComposerAttachments` — paste/drag-drop/file-picker gesture handling |
| `diagnostics.js` | `SerfDiagnostics.classify/render` — diagnostic card rendering |
| `notifications.js` | Browser notifications, title-bar awaiting count, favicon updates |
| `launchconfig.js` | `LaunchConfigControls.render(el, opts)` — schema-driven launch option form (emits settings-table row markup) |
| `theme.js` | Theme persistence via `localStorage["serf-hub.theme"]`, density persistence, applies `data-theme` to root |

### 4.2 Global API surface

| Global | Source | Exposes |
| --- | --- | --- |
| `window.SerfToast` | toast.js | `show`, `dismiss`, `success`, `error`, `info` |
| `window.SerfFocusTrap` | focus-trap.js | `activate(panel, returnFocusTo) → handle`, `deactivate(handle)` |
| `window.SerfAppwire` | appwire.js | `startThread`, `readThread`, `startTurn`, `queueTurn`, `steer`, `drainAsSteer`, `forkThread`, `setModel`, `listModels`, `completeDirs`, `tasks`, `search`, `action`, `refForSession`, `eventsFromNotification`, `eventsFromThread`, `activeTurnIDFromThread`, `setPendingRegistry`, `onNotification`, `onConnectionLost` |
| `window.SerfAppwirePending` | pending.js | `create(opts)` → registry with `register/fail/tryReconcile` |
| `window.SerfRenderer` | renderer.js | `SerfRenderer` constructor + `appendBanner`, `applyStatusDotPulse(root)`, `activeTurnId` property |
| `window.SerfSearch` | search.js | `open`, `close`, `openWith`, `Nav` |
| `window.SerfSpawn` | spawn.js | spawn form mount API |
| `window.SerfComposerAttachments` | composer-attachments.js | `attachComposerImageHandlers`, `attachComposerDropHandlers`, `attachComposerFilePickerHandlers`, `renderAttachmentChips`, `resetMarkerCounter` |
| `window.SerfDiagnostics` | diagnostics.js | `classify(payload)`, `render(payload, actions)` |
| `window.SerfSectionLabels` | settings.js | Shared section-label map |
| `window.LaunchConfigControls` | launchconfig.js | `render(el, opts)` — emits settings-table row markup for launch options |

---

## 5. Conventions

### 5.1 Class naming

BEM-ish but loose: `.btn` base + `.btn-primary` variant; `.sb-row` base + `.sb-row.sub` modifier. `data-*` attributes carry state (`data-state`, `data-active`, `data-pulse`, `data-sidebar-rail`, `data-sidebar-open`, `data-phone-density`, `data-theme`, `data-settings-pane`, `data-loading`, `data-mode`, `data-pending-id`).

The naming linter (`cmd/serf-namingcheck`) enforces kebab-case classes and camelCase/snake_case `data-*` attributes.

Legacy token aliases (`--pad`, `--panel`, `--panel-2`, `--border`, `--muted`, `--accent-2`, `--tool`, `--user`, `--error`) and a handful of legacy class aliases (status-pill state classes in providers) are still defined for migration safety. They reference the canonical tokens, so removing them is a delete-only operation once every reference is audited out.

### 5.2 When to use a token vs a literal

| Property | Use token | Literal OK when |
| --- | --- | --- |
| `padding`, `margin`, `gap`, top/right/bottom/left | `--space-*` always | 1px hairlines |
| `font-size` | `--text-*` always | 9px micro carets (`.chip-caret`, `.sp-*-caret`); `0.85em` inline code (relative) |
| `line-height` | `--leading-*` mostly | `1` for icon-aligned chrome (optical); the documented `1.35` on sb-row title is a tracked exception |
| `border-radius` | `--radius-*` always | never literal |
| `transition-duration`, `animation-duration` | `--motion-*` / `--pulse-cycle` / `--flash-cycle` | never literal |
| `z-index` | `--z-*` always | never literal |
| color | `--bg`, `--text`, `--accent`, `--state-*`, etc. | `rgba(0,0,0,0.x)` for box-shadows and the modal scrim — the abstraction is "drop shadow", not "neutral foreground" |

### 5.3 The 0.85em rule for inline code

Inline `<code>` in prose surfaces uses `font-size: 0.85em`, not a fixed px:

```css
.assistant-message code { background: var(--bg-raised); padding: 1px var(--space-3); border-radius: var(--radius-sm); font-family: var(--font-mono); font-size: 0.85em; }
.settings-help code     { background: var(--bg-raised); padding: 1px var(--space-3); border-radius: var(--radius-sm); font-family: var(--font-mono); font-size: 0.85em; }
```

`0.85em` is relative to the surrounding text, so if the conversation body or settings help shifts size, inline code follows automatically. Block-level `<pre>` does NOT use 0.85em — it uses `--text-sm` so it has its own anchor.

### 5.4 Mono-by-content

Mono is content-driven, not surface-driven. Decide voice from what the text is, not where it lives:

- **Mono:** paths, code, identifiers, branch names, timestamps, metadata, status labels, kbd, section labels, model identifiers, settings values, providers status.
- **Sans:** prose, UI labels, headings, button labels, settings labels, navigation, session titles, empty-state titles, settings help text.

A settings row's `<dt>` is sans because it is a label; the `<dd>` is mono because it is a value. A status badge is mono because the value (`ACTIVE`, `AWAITING`) is a machine identifier rendered for human eyes.

### 5.5 No pill status fills

Status is communicated typographically (small-caps mono + state color) not via background pills. The legacy `.status-pill` (which used `rgba(...)` background tints) is removed; `.status-badge` is the canonical form. Live sidebar rows still carry a 5% tinted background (12% in light theme) because the **row** is the surface, not the badge — the tint is row-level surface communication, not pill chrome.

---

## 6. Known follow-ups (as of 2026-05-23)

These were surfaced during the browser audit at the end of Pass 8. They are real but non-blocking.

- **`.skeleton-row` outside `[data-loading]`.** The sidebar partial renders five `.skeleton-row` placeholders in the Live section when no live sessions exist. Without `[data-loading]` on an ancestor the grid layout rule does not apply and the bare spans render as inline blocks. Either gate them inside a `[data-loading]` wrapper or add a `:not([data-loading]) .skeleton-row { display: none }` rule. `.skeleton-turn` already has the `:not([data-loading])` guard; `.skeleton-row` does not.

- **`.sep · separators still present.** `partials/sidebar.html` lines 29, 115, 129 still render `<span class="sep">·</span>` inside `.sb-row .meta`, and `.tool-call .sep` still styles the same separator in tool calls. Pass 8 Task 24 removed `.rule-dot` but missed `.sep`. Resolve by removing the spans and relying on the `gap: var(--space-2)` already on `.sb-row .meta`. The CSS rules (`.sb-row .meta .sep` and `.tool-call .sep`) can be deleted after the templates are updated.

- **Legacy token aliases retained.** `--pad`, `--panel`, `--panel-2`, `--border`, `--muted`, `--accent-2`, `--tool`, `--user`, `--error` are still defined in all four theme blocks. The aliases reference canonical tokens so they are harmless; they exist as a safety net for any selector not audited. A final sweep + delete is pending.

- **Legacy status class aliases.** Provider status pills still carry class names like `.status-running`, `.status-available`, `.status-error`, `.status-missing`. They alias onto the canonical state colors via `.status-badge.status-running { color: var(--state-idle) }`. Migrating providers/MCP/credentials emission to a single `data-state` attribute is a follow-up.

- **`settings-list` (legacy primitive) still defined.** A few read-only legacy panels reference it. Migrating remaining call sites onto `.settings-table` is pending.

- **The details panel re-activate-after-fetch path is dead code.** The slide-over panel logic includes a branch for re-activating the focus trap after an async fetch that no longer happens. Safe to delete during the next pass on `renderer.js`.

- **Sidebar density toggle (Comfortable/Compact) is phone-only.** Power users on desktop with 30+ rows would benefit from the same toggle. Add `body[data-sidebar-density="compact|comfortable"]` mirroring the phone setting.

- **Spawn-advanced bespoke classes.** `launchconfig.js` still emits `spawn-advanced-row`, `settings-launch-row`, `spawn-advanced-radio`, etc. — about 50 lines of CSS targets these names. Renaming to `settings-table` row classes is a follow-up; the JS API contract does not change, only the markup it emits.

- **`.composer-attachment-error` raw hex fallback.** The rule includes `color: var(--state-danger, #c8553d)`. `--state-danger` is not a defined token (the canonical is `--state-awaiting`); the fallback hex should be removed and the var swapped.

---

## 7. Adding new surfaces

When adding a new pane, sub-page, or component:

1. **Pick the primitive.**
   - Label → value, fixed set of rows? `.settings-table`.
   - Dynamic add/remove list? `.settings-collection`.
   - Conversation-tier annotation? `.tool-call` / `.tool-body` / `.diagnostic` / `.banner` / `.system-line` / `.steering`.
   - Sidebar entry? `.sb-row`.
   - Header? `.workspace-header` pattern.
   - Modal? `<dialog>` + focus trap. Slide-over? `drawer-right` + `SerfFocusTrap`.

2. **Follow the voice cheatsheet (§1.2).** A label is sans; a value is mono. A section header is uppercase tracked mono. A button label is sans `--text-sm / 500`.

3. **Use existing button variants** (`.btn-primary/-secondary/-ghost/-danger/-icon/-chip`). Do not invent a new variant unless the existing six all wrong-shape the use case.

4. **Use existing status indicators** (`.status-badge`, `.status-dot`). For new sources, alias a state color (do not introduce a new color token).

5. **Touch targets** ≥ `--tap-min`. Test at `prefers-reduced-motion: reduce`, `prefers-color-scheme: light`, and on a 390×844 phone viewport.

6. **Avoid:** ad-hoc class names, raw pixels outside the token scale, mono-vs-sans confusion, hover-only opacity affordances (touch users see nothing), inline `style="..."` on templates (forbidden except for data-driven values like `.context-fill width:{{.X}}%`).

---

## 8. Open principles for future evolution

- **Density by default, room to breathe at touchpoints.** Desktop is dense — workshop log, not chat app. Phone scales up touch targets and chrome.

- **Two-voice typography is the design's spine.** Hanken Grotesk + JetBrains Mono. Do not break the voice rule to make a one-off look "cleaner" — it will erode the visual identity. If a value reads as prose, it stops being a value.

- **Color tokens are theme-agnostic.** CSS rules use `var(--…)`. Hex literals are reserved for the four theme blocks and for non-semantic effects (drop shadow `rgba`, modal scrim).

- **Container queries beat media queries for surface-level decisions.** Use media queries for shell-level decisions (sidebar drawer vs pane). Use container queries for individual surfaces that adapt to their own container width.

- **New components compose existing primitives.** A new dialog is a `<dialog>` + focus trap + an existing surface variant inside. A new settings page is a `.settings-table` or `.settings-collection`. Bespoke markup is the last resort.

- **State is data, not class.** Prefer `data-state="awaiting"` over `.status-awaiting`. CSS selectors targeting `[data-state]` keep state changes diff-friendly and lint-checkable.

- **Status is typography first.** Pulses, dots, and tints support the typographic label; they do not replace it. If a user cannot read the state name (small-caps mono in the state color), the surface is broken.

- **Motion is quiet.** The longest non-loop animation is 240ms. Loops (pulse, shimmer) run at 1400-2000ms and at sub-100% opacity so they read as "alive", not "blinking". Reduced motion is respected universally with explicit non-motion fallbacks for any signal whose meaning depends on the animation.
