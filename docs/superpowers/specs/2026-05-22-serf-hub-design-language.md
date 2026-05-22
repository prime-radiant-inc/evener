# Serf Hub Design Language — Workshop Log

**Status:** Draft 2 · 2026-05-22
**Companion spec:** [2026-05-22-serf-hub-responsive-ui-design.md](./2026-05-22-serf-hub-responsive-ui-design.md)

This document defines the design language of the serf-hub web interface. It is the authoritative source for tokens, typography, components, patterns, state conventions, and responsive behavior. Every rule documented here either exists today or has a planned migration path described in the companion implementation spec.

The hub UI is **dense by default, with room to breathe at the touchpoints**. The aesthetic is *workshop log* — the structured page of someone who keeps records. Two type voices, one palette, a fine paper-grain texture, and short, quiet motion. Color is functional (status, severity, accent); type carries identity. The interface should feel like a tool whose maker cared about typography, not a chat app with a dark theme.

---

## 1 · Foundations

### 1.1 Theme & color

Tokens are declared on `:root` for dark by default; `@media (prefers-color-scheme: light)` provides the light palette; `:root[data-theme="light"|"dark"]` allows user override via `theme.js`. Every token must exist in all four variants; light/dark are mirrors, not subsets.

| Token | Role | Dark | Light |
| --- | --- | --- | --- |
| `--bg` | Page background | `#0a0a0e` | `#fafafa` |
| `--bg-raised` | Elevated surface (panels, cards, hover targets) | `#16161e` | `#f1f1f2` |
| `--surface-secondary` | Inset surface within a panel | `#1c1c24` | `#e6e6e8` |
| `--text` | Primary text | `#ececf0` | `#16161e` |
| `--text-muted` | Secondary text, captions, mono metadata | `#7a7a86` | `#5e5e6a` |
| `--text-dim` | Tertiary text, icon glyphs, off-state controls | `#5a5a64` | `#8a8a92` |
| `--rule` | Borders, dividers, hairlines | `#1a1a20` | `#dadadc` |
| `--rule-soft` | Inner hairlines inside lists | `#14141a` | `#e6e6e8` |
| `--accent` | Primary action, links, focus rings | `#7aa2f7` | `#2e58b8` |
| `--accent-secondary` | Subagent / secondary highlight | `#bb9af7` | `#5e35b6` |

**Light mode is not just inverted.** The light accent (`#2e58b8`) is intentionally darker than the dark accent (`#7aa2f7`) — light needs more contrast against a cream background than dark needs against near-black. Same logic applies to state colors: dark uses Tokyo Night's bright variants; light uses their desaturated, higher-contrast counterparts.

**State colors** drive status dots, live-row backgrounds, banners, badges, and the `data-state` attribute system:

| Token | Role | Dark | Light |
| --- | --- | --- | --- |
| `--state-awaiting` | Awaiting human · errors | `#f7768e` | `#b62a48` |
| `--state-processing` | Active · in-flight work | `#7aa2f7` | `#2e58b8` |
| `--state-warning` | Warning · attention | `#e0af68` | `#8a5a14` |
| `--state-idle` | Idle · success · completion | `#9ece6a` | `#336a14` |
| `--state-ended` | Closed · ended · neutral | `#3a3a44` | `#7a7a82` |
| `--state-subagent` | Subagent / fork glyph | `#bb9af7` | `#5e35b6` |

**Primary-button text token** — a third surface-specific token that resolves per theme so the WCAG-AA contrast holds in both:

```css
:root, [data-theme="dark"]  { --btn-primary-text: var(--bg);    /* near-black on light blue accent */ }
[data-theme="light"]         { --btn-primary-text: #fafafa;     /* cream on dark blue accent */ }
```

Dark `--accent` (`#7aa2f7`) carries `#fafafa` at only 2.41:1 — fails WCAG-AA. Inverting to `--bg` (`#0a0a0e`) on the same background gives ~7.2:1.

**Diagnostic source colors** alias state colors via custom properties; they exist so diagnostic cards can swap accent based on origin without redefining the layout:

```css
--diagnostic-provider: var(--state-awaiting);
--diagnostic-serf:     var(--state-warning);
--diagnostic-hub:      var(--state-processing);
--diagnostic-ui:       var(--state-subagent);
```

Legacy aliases (`--panel`, `--panel-2`, `--border`, `--muted`, `--accent-2`, `--tool`, `--user`, `--error`, `--pad`) stay defined during migration so unmigrated rules keep working. New rules MUST use canonical names. Legacy aliases are deleted after migration.

**Token discipline rules:**
- Never set a literal hex value in a rule. Use a token.
- Never reach across the theme abstraction — no rule may use a dark-mode hex literally to override a light-mode token.
- `--accent-secondary` and `--surface-secondary` exist precisely because today's `--panel-2` and `--accent-2` are dark-mode hex literals; they get pulled into the theme abstraction during migration.

### 1.2 Typography — two voices

The hub speaks in two voices. The boundary is the **meaning of the text**, not its surface.

**Sans — Hanken Grotesk** carries prose, UI controls, headings, button labels, navigation, and session titles. It's the human reading the log.

**Mono — JetBrains Mono** carries code, file paths, model identifiers, branch names, timestamps, key-value metadata, kbd hints, status labels (small-caps), and section labels (uppercase + tracked). It's the log itself.

Both faces are open-source, loaded via Google Fonts, and variable-weight (100–900 sans, 100–800 mono). They get preconnected in `<head>`:

```html
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Hanken+Grotesk:ital,wght@0,100..900;1,100..900&family=JetBrains+Mono:ital,wght@0,100..800;1,100..800&display=swap" rel="stylesheet">
```

Tokens:

```css
--font-sans: 'Hanken Grotesk', -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
--font-mono: 'JetBrains Mono', ui-monospace, "SFMono-Regular", monospace;
```

**Type scale** (one source of truth; no `.5px` half-values, no scatter):

| Token | px | Default leading | Typical use |
| --- | --- | --- | --- |
| `--text-2xs` | 10 | `--leading-tight` | Section labels (uppercase, tracked), tiny metadata |
| `--text-xs` | 11 | `--leading-tight` | Row metadata, timestamps, hints, kbd labels, help text |
| `--text-sm` | 12 | `--leading-snug` | Tool calls, diff body, status bar, captions, picker options |
| `--text-base` | 13 | `--leading-snug` | UI body — buttons, settings labels, sidebar titles |
| `--text-md` | 14 | `--leading-normal` | Conversation body (user pill + assistant) |
| `--text-lg` | 16 | `--leading-snug` | Workspace title, page-header h1 |
| `--text-xl` | 18 | `--leading-snug` | Settings h2 |
| `--text-2xl` | 22 | `--leading-snug` | Spawn-prompt heading (largest surface) |

```css
--leading-tight:   1.3;
--leading-snug:    1.5;
--leading-normal:  1.6;
--leading-relaxed: 1.7;  /* reserved */
```

**Voice-by-surface, quick reference:**

| Surface element | Voice | Token |
| --- | --- | --- |
| Workspace title | sans | `--text-lg / 600` |
| Conversation user pill + assistant body | sans | `--text-md / 400` |
| Tool call (verb + target) | mono | `--text-sm / 400` |
| Diff body | mono | `--text-sm / 400` |
| Inline code in prose (`<code>`) | mono | `0.85em / 400` |
| Sidebar session title | sans | `--text-base / 400` |
| Sidebar meta line (project · age) | mono | `--text-2xs / 350` |
| Sidebar section header (LIVE, PROJECTS) | mono | `--text-2xs / 500` uppercase + `0.16em` letter-spacing |
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

**Body size A/B during implementation:** conversation body sits at `--text-md` (14). If 14 feels loose in long sessions, drop to 13 and bump tool calls to 11. The decision is made in the live app, not the spec. Whichever wins becomes the implementation default.

### 1.3 Spacing scale

| Token | px | Common use |
| --- | --- | --- |
| `--space-0` | 0 | intentional zero |
| `--space-1` | 2 | tight badge inset |
| `--space-2` | 4 | inline gap, small padding, chip gutter |
| `--space-3` | 8 | default sibling gap |
| `--space-4` | 12 | panel padding, settings-row vertical pad |
| `--space-5` | 16 | section padding, large gap |
| `--space-6` | 24 | major section spacing, header padding |
| `--space-7` | 32 | hero/empty padding, conversation horizontal pad |
| `--space-8` | 48 | spawn form padding, wide gutter |
| `--space-9` | 64 | wide-screen conversation gutter |

Legacy `--pad: 12px` keeps its current value during migration so unmigrated rules don't shift. After migration the alias is removed.

**Rules:**
- Layout values (padding, margin, gap, top/right/bottom/left) MUST use `--space-N`.
- 1px values (rule lines, hairline offsets) stay literal — the scale starts at 2.
- Negative offsets use `calc(0 - var(--space-N))`.

### 1.4 Radius

| Token | px | Use |
| --- | --- | --- |
| `--radius-sm` | 3 | inline code, small inset tags |
| `--radius-md` | 4 | default — buttons, inputs, panels, banners |
| `--radius-lg` | 6 | cards, dialogs, input-card, dropdown pickers |
| `--radius-xl` | 8 | spawn input, image cards, search dialog |
| `--radius-pill` | 14 | pills (chips, message pills, status pills, btn-chip) |
| `--radius-full` | 50% | status dots, running indicator |

Today's stray `10px` and `12px` radii collapse to `--radius-pill` during migration (visually indistinguishable at status-pill sizes); `2px` collapses to `--radius-sm`.

### 1.5 Motion

Three durations, one easing, one reduced-motion override.

```css
--motion-fast: 100ms ease;   /* color / opacity / bg flips */
--motion-base: 160ms ease;   /* drawer slide, accordion, dropdown reveal */
--motion-slow: 240ms ease;   /* full-shell transitions */

--pulse-cycle: 1400ms ease-in-out;  /* optimistic-pending, running dot */
--flash-cycle: 2000ms ease-out;     /* search-hit flash */
```

```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 1ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 1ms !important;
  }
}
```

That `!important` and the rule forcing it are the only two `!important` declarations allowed in the codebase. Document them as such.

**Stagger on first paint:** the sidebar's first render uses a 30ms stagger across live rows so the list typesets rather than slams in. After load, individual swaps don't stagger — only first paint earns the choreography.

### 1.6 Elevation & z-index

| Token | Value | Use |
| --- | --- | --- |
| `--z-sticky` | 10 | sticky element inside a scroll container (e.g., palette header) |
| `--z-fixed-action` | 100 | fixed-position action surface (rare) |
| `--z-dropdown` | 200 | dropdown picker, popover |
| `--z-overlay` | 800 | backdrop scrim |
| `--z-drawer` | 900 | slide-over panel |
| `--z-modal` | 1000 | modal dialog (search palette, fork confirm) |
| `--z-toast` | 1100 | toast region (above modal) |

Today's hardcoded `1, 50, 100, 150, 199, 200` collapse to this scale during migration.

### 1.7 Breakpoints, container queries, density

Two viewport breakpoints — phone and wide. Tablet portrait inherits desktop (user decision).

| Name | Condition | Behavior |
| --- | --- | --- |
| Phone | `max-width: 767px` | Off-canvas sidebar, full-screen drawers, stacked controls, larger touch targets |
| Phone landscape | `max-width: 767px and max-height: 500px` | Compresses vertical chrome (header, status bar) to reclaim conversation height |
| Default | `min-width: 768px` | Two-pane shell with inline controls — the canonical layout |
| Wide | `min-width: 1440px` | Soft cap on conversation width, optional sidebar rail |

Prefer **container queries** over media queries for surface-level decisions. Container queries apply on `#sidebar`, `#workspace`, `.settings-pane`, `.spawn-pane`, `.conversation`. Media queries are reserved for shell-level decisions (sidebar drawer vs pane, hamburger placement).

**Phone density toggle** is a user setting (`Theme → Phone density`):

| Setting | Body text | Tool call text | Row vertical rhythm |
| --- | --- | --- | --- |
| Compact (default) | `--text-xs` (11) | 10px | tight |
| Comfortable | `--text-sm` (12) | `--text-xs` (11) | one space-step looser |

Stored as `body[data-phone-density="compact|comfortable"]`. CSS branches on the attribute under the phone media query.

### 1.8 Touch targets

- `--tap-min: 44px` on phone, `--tap-min: 32px` on desktop.
- Every interactive control MUST meet `--tap-min` on phone via padding or `min-height`.
- Exception: in-prose text links inside the conversation body; they get `padding: var(--space-1) var(--space-2)` and a hit-box-extending `::before` pseudo if needed.

### 1.9 Texture

Raised surfaces carry a fine paper-grain overlay — an inline SVG noise filter sampled to ~5% white opacity. Subtle on dark, almost invisible on light. The texture is in a CSS custom property so any surface that wants the feel just references it:

```css
--noise: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='220' height='220'><filter id='n'><feTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='1' stitchTiles='stitch'/><feColorMatrix values='0 0 0 0 1, 0 0 0 0 1, 0 0 0 0 1, 0 0 0 0.05 0'/></filter><rect width='100%25' height='100%25' filter='url(%23n)'/></svg>");
```

Applied via `::after` pseudo-element with `mix-blend-mode: overlay` and `opacity: 0.5`. Used on: `.input-card`, `.diagnostic`, `.fork-dialog`, the user pill, the workspace shell. Skipped on text-heavy surfaces (conversation transcript, settings content) so it doesn't fight readability.

---

## 2 · Layout primitives

### 2.1 App shell

```
<body class="app" [data-theme] [data-sidebar-open] [data-sidebar-rail] [data-phone-density]>
  <aside id="sidebar">              ← off-canvas drawer on phone; 260px pane / 56px rail on desktop
  <main id="workspace">             ← session view, spawn, settings, credentials
  <dialog id="search-dialog">       ← ⌘K palette
  <div id="toast-region" aria-live="polite"> ← new, top-center
</body>
```

Mobile collapses the flex shell to `display: block` and floats the sidebar. The hamburger moves **into the workspace header** as the leftmost element on phone — partials no longer need to remember hamburger padding.

**Body state attributes:**

| Attribute | Values | Set by | Purpose |
| --- | --- | --- | --- |
| `data-theme` | `light`, `dark`, absent | `theme.js` ← `localStorage["serf-hub.theme"]` | Override `prefers-color-scheme` |
| `data-sidebar-open` | present / absent | `sidebar.js` on hamburger tap | Open off-canvas drawer (mobile) |
| `data-sidebar-rail` | present / absent | rail-toggle button, persisted | Collapse to 56px rail (desktop) |
| `data-phone-density` | `compact`, `comfortable` | Theme settings, persisted | Phone type-scale variant |

### 2.2 Surface

A **surface** is a rectangular region with a background, optional border, and content padding. Every panel in the UI is a surface variant.

| Variant | Background | Border | Radius | Padding | Texture |
| --- | --- | --- | --- | --- | --- |
| `surface` (default) | `--bg-raised` | `1px solid --rule` | `--radius-lg` | `--space-4` | optional noise |
| `surface-inset` | `--surface-secondary` | none | `--radius-md` | `--space-3` | none |
| `surface-flat` | transparent | `1px solid --rule` | `--radius-md` | `--space-3` | none |

Existing classes that ARE surfaces (refactored to variants during migration):

- `.input-card`, `.diagnostic`, `.fork-dialog`, `.search-dialog-inner`, `.chip-picker`, `.credentials-editor`, `section.panel`, `.user-msg .pill` → `surface`
- `.shell-output`, `.cheap-tool-args`, `.cheap-tool-output`, `.diff-body` → `surface-inset`

The conversation transcript body sits directly on `--bg` (no surface).

### 2.3 Stack & cluster

Two utility spacing patterns used inside surfaces.

- **Stack** — vertical rhythm. `display: flex; flex-direction: column; gap: var(--space-N)`.
- **Cluster** — inline group that wraps. `display: flex; align-items: center; gap: var(--space-N); flex-wrap: wrap`.

---

## 3 · Components

### 3.1 Button

Six variants. All inherit:

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
  border-radius: var(--radius-md);
  border: 1px solid transparent;
  cursor: pointer;
  transition: background var(--motion-fast), color var(--motion-fast), border-color var(--motion-fast);
}
.btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.btn[disabled] { opacity: 0.45; cursor: not-allowed; }
```

| Variant | Background | Color | Border | Hover | Use |
| --- | --- | --- | --- | --- | --- |
| `.btn-primary` | `--accent` | `--btn-primary-text` (`var(--bg)` in dark, `#fafafa` in light) | `--accent` | `filter: brightness(1.08)` | Send · spawn · fork confirm · save |
| `.btn-secondary` | `--bg-raised` | `--text` | `--rule` | bg: `--surface-secondary` | Common controls |
| `.btn-ghost` | transparent | `--text-muted` | transparent | bg: `--bg-raised`; color: `--text` | Header actions · cancel · ⋯ |
| `.btn-danger` | `rgba(awaiting, 0.05)` | `--state-awaiting` | `rgba(awaiting, 0.4)` | bg + border brighten | Stop · shutdown · destructive |
| `.btn-icon` | transparent | `--text-muted` | transparent | bg: `--bg-raised`; border: `--rule` | Single glyph (≥32px desktop, ≥44px phone) |
| `.btn-chip` | `--bg-raised` | `--text` | `--rule` | border: `--accent` | Spawn picker triggers (pill-shaped) |

The chip variant uses `border-radius: var(--radius-pill)` and a `<span class="key">` (mono `--text-2xs`) + `<span class="val">` (mono `--text-xs`) for its label+value composition.

Pressed state (`:active`) drops surface one step (e.g., `--bg-raised` → `--surface-secondary`).

**Legacy class migration:**

| Today | New |
| --- | --- |
| `.input-btn` | `.btn-secondary` |
| `.input-btn-primary` | `.btn-primary` |
| `.input-btn-stop` | `.btn-danger` |
| `.input-btn-ghost` | `.btn-ghost` |
| `.header-action` | `.btn-ghost` (small) |
| `.header-action-danger` | `.btn-danger` (ghost size) |
| `.spawn-btn` | `.btn-primary` |
| `.spawn-attach-btn` | `.btn-secondary` (chip-shaped) |
| `.fork-confirm` | `.btn-primary` |
| `.fork-cancel` | `.btn-ghost` |
| `.title-action` | `.btn-icon` |
| `.panel-toggle` | `.btn-ghost` + `[data-active]` |
| `.chip` (spawn) | `.btn-chip` |

### 3.2 Chip · Pill · Badge

Three closely-related shapes, distinguished by role:

| Component | Padding | Shape | Use | Examples |
| --- | --- | --- | --- | --- |
| **Chip** | `var(--space-2) var(--space-4)` | `--radius-pill` | Picker trigger or filter (interactive) | Spawn launch chips, model picker, branch picker |
| **Pill** | `var(--space-1) var(--space-3)` | `--radius-pill` | Read-only label (presentational) | `.model-chip` (settings) |
| **Badge** | `var(--space-1) var(--space-2)` | `--radius-sm` | Inline count / type tag (presentational) | `.panel-toggle-badge`, `.task-type-pill` |

Chip is a button. Pill and badge are spans.

### 3.3 Status indicators

**`.status-dot`** — 6×6 circle, `--radius-full`. Default `--state-ended`; `[data-state]` overrides.

```css
.status-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--state-ended); }
.status-dot[data-state="active"]    { background: var(--state-processing); }
.status-dot[data-state="awaiting"]  { background: var(--state-awaiting); }
.status-dot[data-state="warning"]   { background: var(--state-warning); }
.status-dot[data-state="idle"]      { background: var(--state-idle); }
.status-dot.subagent                { background: var(--state-subagent); }
.status-dot[data-pulse]             { animation: pulse 2s ease-in-out infinite; }
```

`[data-pulse]` is added by JS for `active` and `awaiting` states so the dot breathes peripherally.

**`.fork-glyph`** — monospace `⎇` glyph. Same `[data-state]` color matrix as the dot.

**`.status-badge`** — typographic. **No pill background, no rounded fill.** Just a dot + small-caps mono label in the state color.

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
}
.status-badge[data-state="active"]   { color: var(--state-processing); }
.status-badge[data-state="awaiting"] { color: var(--state-awaiting); }
.status-badge[data-state="warning"]  { color: var(--state-warning); }
.status-badge[data-state="idle"]     { color: var(--state-idle); }
.status-badge[data-state="ended"]    { color: var(--text-muted); }
```

This replaces today's `.status-pill` which uses `rgba(...)` background tints — those read as generic SaaS pills. The typographic treatment carries the same information with less visual cargo.

### 3.4 Row

Two row primitives live in the codebase: **sidebar row** and **generic row** (used in settings collections, palette results, recent prompts, etc.).

**Sidebar row** has a richer structure than the generic row because it carries a title that wants to wrap and metadata that wants to stay compact:

```html
<a class="sb-row" data-state="awaiting" [data-active]>
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
  cursor: pointer;
  transition: background var(--motion-fast);
}
.sb-row:hover { background: var(--bg-raised); }
.sb-row .title {
  font-size: var(--text-base);
  color: var(--text);
  line-height: 1.35;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.sb-row .meta {
  margin-top: 2px;
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  color: var(--text-muted);
  letter-spacing: 0.02em;
  line-height: 1.2;
}
.sb-row[data-state="awaiting"] {
  background: color-mix(in srgb, var(--state-awaiting) 5%, transparent);
  border-left-color: var(--state-awaiting);
}
.sb-row[data-state="active"] {
  background: color-mix(in srgb, var(--state-processing) 5%, transparent);
  border-left-color: var(--state-processing);
}
.sb-row[data-active] {
  background: color-mix(in srgb, var(--accent) 10%, transparent);
  border-left-color: var(--accent);
}
.sb-row.sub  { padding-left: var(--space-7); }
.sb-row.sub  .title { font-size: var(--text-sm); color: var(--text-muted); }
.sb-row.fork .title { color: var(--text-muted); }
```

The 2-line title wrap + mono meta line is a key design choice — titles like "refactor auth middleware to use the new session token store" stay fully readable, not truncated to one cramped line.

**Generic row** is simpler — a horizontal flex with `min-height: var(--tap-min)` and consistent padding. Used in palette results, recent prompts, settings collections.

### 3.5 Input controls

**Text input / textarea / select** share a base. The `.input` class collapses today's scattered field styles.

```css
.input {
  width: 100%;
  padding: var(--space-2) var(--space-3);
  font: inherit;
  font-family: var(--font-mono);  /* values default to mono */
  font-size: var(--text-sm);
  color: var(--text);
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  outline: none;
  transition: border-color var(--motion-fast);
}
.input:focus-visible { border-color: var(--accent); }
.input[aria-invalid="true"] { border-color: var(--state-awaiting); }
```

Three size variants (`-sm`, `-md` default, `-lg`).

`.message-input` and `.spawn-input` stay custom — they're textareas inside surface containers (`.input-card`, the spawn page) and use `--font-sans` (you're writing prose, not commands). Default-mono inputs are for paths, models, tokens, and other machine values.

### 3.6 Picker

Two picker shapes:

- **Inline picker** (`.chip-picker`) — opens below a chip trigger. Single column of options.
- **Two-pane picker** (`.chip-picker-wide`) — model picker pattern: providers left, models right. 520px desktop, full-screen sheet on phone.

Both use the `surface` variant with `--z-dropdown`. Optional search input at top, filterable results, keyboard nav (arrow keys, enter), Esc to close. The settings model/dir picker (`SettingsPickers.init()`) uses these same shapes — consolidate to one component.

### 3.7 Card / panel

Existing card-shaped surfaces refactor onto the `surface` variant:

- `.input-card` — composer input area. `surface` + `[data-drop-active]` attribute (replaces `.drop-active` class) to flip outline on dragover.
- `.diagnostic` — diagnostic block. `surface` with `border-left: 3px solid var(--diagnostic-accent)`.
- `.fork-dialog` — `surface`, `max-width: 480px`, `margin-left: auto`, inline in conversation.
- `.credentials-editor` — `surface-inset`.

### 3.8 Dialog & slide-over

Three overlay patterns:

- **Modal dialog** (`.dialog`) — centered, backdrop scrim, focus-trapped. Uses `<dialog>` element. Examples: search palette. `--z-modal`.
- **Right slide-over** (`.drawer-right`) — slides in from right, full height, max 360px desktop. Examples: details panel, tasks panel. Single-at-a-time on desktop (enforced by JS); full-screen overlay on phone with sticky header + close button. `--z-drawer`.
- **Left off-canvas** (`.drawer-left`) — slides in from left, phone-only. Sidebar in mobile. `--z-drawer` with `--z-overlay` scrim.

All three: close via Esc, backdrop tap, or explicit close button. Trap focus on open, return focus to the trigger on close. Apply `inert` to the rest of the document when modal.

### 3.9 Toast

`#toast-region` lives at body level with `aria-live="polite"`, positioned **top-center** (user decision — bottom-center fights the composer + keyboard on phone). Toasts insert as children, slide in from above, auto-dismiss after 3s.

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
}
.toast.toast-success { border-left: 3px solid var(--state-idle); }
.toast.toast-error   { border-left: 3px solid var(--state-awaiting); }
.toast.toast-info    { border-left: 3px solid var(--accent); }
@keyframes toast-enter {
  from { opacity: 0; transform: translateY(-8px); }
  to   { opacity: 1; transform: translateY(0); }
}
```

API: `window.SerfToast.show(message, kind, opts)` — kind ∈ `success | error | info`. `opts.timeout` overrides default 3s.

Initial trigger set: copy session ID, model change, session shutdown, settings saved, credential set/cleared, htmx error.

### 3.10 Banner & diagnostic

Two inline-in-conversation notices:

- **`.banner`** — single-line, left-border colored. Variants: `.banner-error`, `.banner-warning`, `.banner-note`.
- **`.diagnostic`** — multi-line, structured (header + message + hint), source-tagged via `--diagnostic-accent`. Heavy weight, used for actionable problems.

---

## 4 · Patterns

### 4.1 Workspace header

Standard header layout used by workspace, spawn, settings, and credentials:

```
[mobile: ☰] [title  · meta · meta · status-badge] [tasks · details · ⋯]
```

- Hamburger appears at left on phone (≤767px) only.
- Title truncates with ellipsis when narrow.
- Meta items (`src`, `branch`, `turn-count`) are a mono cluster with `gap: var(--space-4)`; today's `.rule-dot` separators are dropped — gap + muted color carries the same work with less noise.
- Status badge anchors the right edge of the meta cluster.
- Action buttons cluster at the right; when phone width forces a wrap, secondary actions consolidate into a `⋯` overflow menu.

### 4.2 Settings — the settings-table primitive

**One row primitive for everything that takes label → value shape.** Read-only and editable share the same row — what changes is what lives in `<dd>`.

```html
<dl class="settings-table">
  <div class="row">
    <dt>Hub address</dt>
    <dd>127.0.0.1:9180</dd>
  </div>
  <div class="row">
    <dt>Past index</dt>
    <dd>~/.serf/index.db <span class="val-text dim">48 MB</span></dd>
    <p class="help">SQLite database of past session metadata. Search results in <code>⌘K</code> come from here.</p>
  </div>
  <div class="row editable">
    <dt>Title bar count</dt>
    <dd><label class="val-toggle on"><input type="checkbox" checked><span class="state">ON</span></label></dd>
    <p class="help">Number of awaiting sessions shown in the tab title.</p>
  </div>
  <div class="row">
    <dt>Access mode</dt>
    <dd>
      <div class="val-radio-group">
        <label class="val-radio"><input type="radio" name="access"> read-only</label>
        <label class="val-radio" data-checked><input type="radio" name="access" checked> normal</label>
        <label class="val-radio"><input type="radio" name="access"> full-auto</label>
      </div>
    </dd>
    <p class="help">Controls writes outside <code>cwd</code> and network reach.</p>
  </div>
</dl>
```

Geometry:

```css
.settings-table { margin: 0; max-width: 760px; border-top: 1px solid var(--rule); }
.settings-table .row {
  display: grid;
  grid-template-columns: 160px 1fr;
  column-gap: var(--space-4);
  row-gap: var(--space-2);
  align-items: baseline;
  padding: var(--space-4) 0;
  border-bottom: 1px solid var(--rule);
}
.settings-table dt {
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-base);
  font-weight: 500;
  color: var(--text);
}
.settings-table dd {
  margin: 0;
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text);
}
.settings-table .help {
  grid-column: 1 / -1;        /* spans both columns on grid-row 2 */
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-xs);
  color: var(--text-muted);
  font-weight: 350;
  line-height: 1.55;
  max-width: 620px;
}
.settings-table .row.editable { cursor: pointer; }
.settings-table .row.editable:hover {
  background: color-mix(in srgb, var(--text) 2%, transparent);
}
```

**Value cells** (what lives in `<dd>`):

| Variant | Shape | Use |
| --- | --- | --- |
| Plain mono text | `<dd>~/.serf/run</dd>` | Read-only values |
| `.val-text` (with `.dim` subspans) | `<dd>•••• <span class="val-text"><span class="dim">2d ago</span></span></dd>` | Read-only with annotation |
| `.status-badge` | `<dd><span class="status-badge" data-state="available">…</span></dd>` | Stateful read-only |
| `.val-input` | `<dd><input class="val-input" value="..."></dd>` | Text input (mono) |
| `.val-select` | `<dd><select class="val-select">…</select></dd>` | Dropdown |
| `.val-radio-group` | `<dd><div class="val-radio-group">…</div></dd>` | Inline segmented radios |
| `.val-toggle` | `<dd><label class="val-toggle on">…</label></dd>` | Checkbox + ON/OFF mono pill |

This unifies what was today's `.settings-list` (read-only definition list), `.settings-form` (form), `.settings-launch-form` (variant form), and the inline-JS rendered Providers/Plugins/Skills/MCP pages — every "key → value" page collapses to this primitive. `LaunchConfigControls.render()` keeps its JS contract; it emits row markup instead of bespoke field markup.

**The other settings primitive — collection.** For dynamic lists with add/remove (Plugins, Skills, MCP servers, Credentials), where the shape is an unbounded sequence rather than a fixed set of labeled fields:

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
      <button class="btn-icon" aria-label="Remove">×</button>
    </li>
    …
  </ul>
  <form class="settings-collection-add">
    <input class="val-input" placeholder="add a skill directory…">
    <button class="btn btn-secondary" type="submit">＋ Add</button>
  </form>
</section>
```

The `×` remove button is **persistent** (no hover-only reveal), so touch users see it. Sub-pages can render multiple collections (e.g., MCP servers has "Inline servers" + "Config files").

**Sub-page taxonomy after migration:**

| Sub-page | Primitive(s) |
| --- | --- |
| General | settings-table (read-only) |
| Theme | settings-table (radios) |
| Notifications | settings-table (toggles) |
| Providers | settings-table (status-badge) |
| Agents | settings-table (read-only) |
| Serf launch | settings-table (mixed inputs, schema-driven) |
| Codex launch | settings-table (read-only) |
| In-repo config | settings-table + custom trust controls (documented one-off) |
| Plugins | settings-collection |
| Skills | settings-collection |
| MCP servers | settings-collection × 2 (inline + config files) |
| Hub | settings-table (read-only) |
| Storage | settings-table (read-only) |
| Project | settings-table (mixed inputs, schema-driven) |
| Credentials | settings-collection (provider rows with action buttons) |

### 4.3 Slide-over / drawer

Right drawers (`details`, `tasks`) follow `drawer-right`. On phone, full-screen with slide-in transition (`var(--motion-base)`), sticky header + explicit close button. On desktop, 360px wide, single-at-a-time (opening one closes the other); no backdrop on desktop.

Left drawer (sidebar mobile) follows `drawer-left`. Off-canvas at `transform: translateX(-100%)`; `[data-sidebar-open]` on body slides it in. Scrim active.

### 4.4 Modal dialog

Centered with backdrop. Search palette is canonical. Implementation uses `<dialog>` element so focus trapping and Esc handling come free. Phone variant goes full-screen (`width: 100%; height: 100vh; border-radius: 0`).

Fork-confirm is rendered inline in the conversation (not modal) as a `.fork-dialog` surface with `max-width: 480px; margin-left: auto`.

### 4.5 Optimistic rendering

Existing classes (`.optimistic-pending`, `.optimistic-failed`, `.optimistic-failed-reason`, `.optimistic-retry`) stay. They migrate to use motion tokens and the new state colors. `pending.js` keeps its JS contract.

```css
.optimistic-pending { animation: pulse var(--pulse-cycle) infinite; }
.optimistic-failed  { border-left: 2px solid var(--state-awaiting); padding-left: var(--space-2); }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.65; } }
```

### 4.6 Loading & skeleton

`.skeleton` utility — a shimmering rectangle in `--bg-raised` → `--surface-secondary` → `--bg-raised`, animated by `--pulse-cycle`. Used in:

- Sidebar on first load (5 skeleton rows in Live, 3 per project section)
- Workspace meta + first 1–2 transcript turns during session swap
- Settings sub-page content during htmx swap

htmx swap targets get `data-loading` set during the request; CSS shows skeletons when `data-loading` is present.

### 4.7 Empty states

```html
<div class="empty-state">
  <p class="empty-state-title">No messages yet</p>
  <p class="empty-state-body">Type below to start the conversation.</p>
  <div class="empty-state-actions">
    <a class="btn btn-secondary" href="/new">＋ New session</a>
    <button class="btn btn-ghost" data-search-trigger>⌘K search</button>
  </div>
</div>
```

Applies to: workspace empty (no session selected), conversation empty (new session, no messages), sidebar empty (no projects), search empty, tasks empty, settings sub-page empty.

### 4.8 Sidebar — sections & projects

Sidebar is a vertical stack of sections. Top-level structure:

```
[hamburger-or-rail-toggle ＋new search ⌘K        ⚙]
LIVE 3
  ● add input validation to signup handler        ← awaiting (red dot, red border-left tint)
    prime-radiant/serf · 2m
  ● refactor auth middleware to use the…         ← active (blue dot pulse)
    prime-radiant/serf · 14m
  ● tune token usage in llm-proxy retry path     ← idle
    llm-proxy · 1h
▸ PRIME-RADIANT / SERF  6  ⚙ ＋
  ● add input validation to signup handler
    feat/signup-validation · 2m
    ↳ explore existing handler tests             ← subagent (purple dot, indented)
       subagent · explore · 3m
    ⎇ add input validation (alt approach)        ← fork (mono glyph, dimmed)
       fork of: signup-validation · 5m
▸ PRIME-RADIANT / HUB  2  ●                       ← rollup dot when project has live work
▸ LLM-PROXY  4
▸ SERF-TUI  1
```

Project headers use mono `--text-2xs` uppercase with `letter-spacing: 0.14em`. The ⚙ (project settings) and ＋ (new session in project) buttons are **persistent at `--text-dim`** — no hover-only reveal — and brighten to `--text-muted` on row hover.

Rollup dot on collapsed project headers shows the highest-priority state across the project's children (omits `ended`). Hidden when the project has no live children.

---

## 5 · State conventions

### 5.1 `data-state` values

Session state machine (carried on `.sb-row`, conversation root, status-dot, status-badge, fork-glyph). Sourced from `web.go:stateLabel` and `notifications.js:STATE_PRIORITY` — every value emitted by either must have a documented color and component treatment.

| Value | Meaning | Color | Pulse |
| --- | --- | --- | --- |
| `active` | Turn in flight, working | `--state-processing` | yes |
| `awaiting` | Awaiting human input | `--state-awaiting` | yes |
| `errored` | Provider/serf/hub error during turn | `--state-awaiting` | yes |
| `warning` | Soft problem, recoverable | `--state-warning` | — |
| `idle` | Sitting quiet, last result complete | `--state-idle` | — |
| `ended` | Closed, no daemon | `--state-ended` | — |
| `closed` | Past session, no live daemon | `--state-ended` | — |
| `notLoaded` | Past-session result not yet hydrated | `--state-ended` | — |
| (empty) | Unknown | `--state-ended` | — |

The same `[data-state]` selectors apply to `errored`, `closed`, `notLoaded`:

```css
.status-dot[data-state="errored"], .status-badge[data-state="errored"] { color: var(--state-awaiting); /* same as awaiting */ }
.status-dot[data-state="closed"], .status-dot[data-state="notLoaded"]  { background: var(--state-ended); }
```

Reserved for future expansion: `paused`, `thinking`. The `subagent` role currently uses a class; promote to `data-state="subagent"` for consistency.

### 5.2 Capability flags

Composer button visibility is **template-gated** by `{{if .Capabilities.X}}` in `workspace.html` — buttons not currently capable are not in the DOM. The `data-capability-{name}` attribute on a rendered button carries its *enabled vs pending* state for CSS (spinner, disabled styling). It does not encode visibility, because `renderer.js:syncTurnActionControls` reads attributes via `querySelector` and tolerates a null result when a capability is absent.

| Attribute | Button (when rendered) | `true` | `pending` |
| --- | --- | --- | --- |
| `data-capability-send` | Send | enabled | shown with spinner |
| `data-capability-queue` | (same Send button, queue mode) | accepts queue | shown with spinner |
| `data-capability-steer` | Send as steer | enabled | shown with spinner |
| `data-capability-interrupt` | Stop | enabled | shown with spinner |

**Send, Stop, and Send-as-steer are three separate buttons** — not a split-button. The risk of accidentally clicking stop when reaching for send is too high.

### 5.3 Optimistic states

`[data-pending-id]` carries the pending registry handle. `.optimistic-pending` and `.optimistic-failed` classes are managed by `pending.js`. Components that participate (user-message, steering, queue-preview-item) are listed in the design system. New components that need optimistic state follow the same pattern.

### 5.4 Theme & density

- `document.documentElement.dataset.theme` ∈ `light | dark | (unset)` — managed by `theme.js`.
- `document.body.dataset.phoneDensity` ∈ `compact | comfortable` — managed by settings.js. Default `compact`.

---

## 6 · Accessibility conventions

### 6.1 Focus rings

Every interactive element MUST have `:focus-visible`:

```css
:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
```

For rounded controls where the offset escapes the radius, use `outline-offset: -2px` (already done for `.search-row`).

Applies to: buttons, links, inputs, sidebar rows, chips, picker options, summary elements, project chevron, hamburger, drawer-close buttons, palette results, toasts.

### 6.2 ARIA & live regions

- `#toast-region` carries `aria-live="polite"`.
- Each form's status (`<span class="settings-form-status">`) carries `aria-live="polite"`.
- Sidebar nav: `aria-label="Sessions"`.
- Search dialog: `role="combobox"` on input, `role="listbox"` on results.
- Modal/drawer open applies `inert` to the rest of the document.
- Status dots that today rely on color get `aria-label` describing the state.

### 6.3 Keyboard navigation

| Key | Context | Action |
| --- | --- | --- |
| Tab / Shift+Tab | Any | Standard focus traversal |
| Enter / Space | Button, row, chip | Activate |
| Esc | Modal, drawer, picker | Close |
| ⌘K / Ctrl+K | Any | Open search palette |
| ⌘B / Ctrl+B | Desktop | Toggle sidebar rail |
| Arrow up/down | Search results, picker options | Navigate |
| ⌘↵ / Ctrl+↵ | Composer textarea, spawn input | Send / spawn |
| ⇧↵ | Composer textarea | Send as steer |
| Esc | Composer (with text) | Clear input |

Project chevrons today are click-only (a11y gap). They become `<button>` elements with keyboard activation.

### 6.4 Touch targets

See §1.8. Enforced via component padding/min-height. Implementation spec includes a checklist for every interactive control.

### 6.5 Semantic upgrade

- Settings definition lists use `<dl>` not `<ul>` (correct semantic for label-value).
- The settings-table row `<div>` wrappers are permitted by HTML5.2+.

---

## 7 · Responsive behavior

### 7.1 Adaptation table

| Surface | Phone | Desktop | Wide (≥1440) |
| --- | --- | --- | --- |
| App shell | sidebar off-canvas, hamburger in header | sidebar 260px pane | sidebar 260px or 56px rail |
| Conversation | padding `--space-3`, no max-width | padding `--space-5 --space-6`, max-width 920px | max-width 920px |
| Composer | full-width, controls wrap | inline horizontal controls | inline |
| Slide-over (details/tasks) | full-screen overlay, sticky header | 360px right drawer, single-at-a-time | 360px right drawer |
| Spawn form | padding `--space-4`, chips wrap | padding `--space-7 --space-8`, chips horizontal | max-width 880px |
| Settings | nav-as-page with back chevron | two-pane (200px nav + content) | two-pane |
| Search palette | full-screen | 560px centered | 560px centered |
| Modal pickers | full-screen sheet | inline dropdown | inline dropdown |
| Settings-table | 1-column stack (dt above dd, help below) | 2-column 160px / 1fr + help spans | same |
| Phone density | compact (default) or comfortable (opt-in) | n/a | n/a |

### 7.2 Container queries

Per surface where layout depends on container width (not viewport), use container queries:

```css
@container workspace (max-width: 600px) {
  .conversation { padding: var(--space-3); }
  .workspace-header .meta { display: none; }
}

@container sidebar (max-width: 80px) {
  .sb-row .text-col,
  .sb-row .age,
  .sb-section-head .count,
  .sb-project-head .name,
  .sb-project-head .count { display: none; }
  .sb-row { justify-content: center; }
}
```

---

## 8 · Implementation notes

- All token additions go in `style.css` `:root` blocks. Legacy aliases stay until migration completes.
- Components live in `style.css`. No separate stylesheets — the file is large but readable, and HTMX swap targets benefit from a single cascade source.
- Inline `style="..."` in templates is forbidden except for data-driven values (e.g., `.context-fill width:{{.X}}%`). Today's two `style="margin:0"` overrides are removed.
- Naming follows existing conventions: kebab-case classes, camelCase/snake_case `data-*` attributes per the lint rules in `cmd/serf-namingcheck`.
- New components are added to this document **before** being implemented. This document is checked in alongside the implementation.
- Both fonts are loaded from Google Fonts. If offline operation is needed, vendor the WOFF2 files into `cmd/serf-hub/assets/fonts/` and add `@font-face` declarations to `style.css` — the Google import is then a fallback.

---

## 9 · Glossary

- **Surface** — a rectangular region with a background, optional border, and content padding. The atomic visual primitive.
- **Cluster** — a horizontal flex group with `gap` and `flex-wrap`. A spacing pattern, not a component.
- **Stack** — a vertical flex column with `gap`. A spacing pattern.
- **Sidebar row** — interactive horizontal strip in the sidebar: dot-column + text-column (title + mono meta). 2-line title wrap.
- **Settings-table** — the unified primitive for read-only and editable label-value settings. Rows are 2-column grids; help spans both columns.
- **Settings-collection** — the primitive for dynamic add/remove lists.
- **Chip** — small interactive control shaped like a pill. Used in pickers and spawn.
- **Pill** — presentational pill-shaped label. Read-only.
- **Badge** — small inline label, less rounded than a pill. Counts and type tags.
- **Status badge** — typographic mono small-caps in state color, **no background fill**.
- **Drawer** — slide-over panel anchored to an edge.
- **Modal** — centered overlay with focus trap and backdrop.
- **Skeleton** — shimmering placeholder during loading.
- **Toast** — top-center auto-dismissing notification.
- **Token** — CSS custom property used in place of a literal value.
- **Variant** — a named modifier of a base component (e.g., `.btn-primary`).
- **Workshop Log** — the aesthetic name: two voices (Hanken Grotesk + JetBrains Mono), unchanged palette, paper-grain texture, quiet motion.
