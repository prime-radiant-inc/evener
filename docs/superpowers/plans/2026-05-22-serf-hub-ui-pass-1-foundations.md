# Serf Hub UI Pass 1 — Foundations (tokens + fonts + CSP) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the full token foundation (spacing, type, leading, radius, motion, z-index, tap-min, noise, fonts, new theme tokens) to `style.css`, load Hanken Grotesk + JetBrains Mono via Google Fonts, allow those font hosts in CSP, and refresh light-mode palette values — while preserving today's pixel-exact rendering through legacy aliases.

**Architecture:** Additive-only token migration. Every new token lives alongside the existing color tokens at the top of `cmd/serf-hub/assets/style.css`. Legacy aliases (`--pad`, `--panel-2`, `--accent-2`, `--panel`, `--border`, `--muted`, `--tool`, `--user`, `--error`) keep their values so unmigrated rules below the token block keep rendering identically. New theme tokens (`--surface-secondary`, `--accent-secondary`, `--btn-primary-text`) get defined in all four theme variants. Fonts load from `fonts.googleapis.com` / `fonts.gstatic.com`; CSP is updated to allow these origins via a new `font-src` directive and an extension to `style-src`.

**Tech Stack:** CSS custom properties on `:root` and `[data-theme="*"]` blocks; HTML `<link rel="preconnect|stylesheet">` in `templates/app.html`; Go `net/http` middleware in `cmd/serf-hub/security.go`; Go `testing` for CSP assertions in `cmd/serf-hub/security_test.go`. Build via `make build-hub`. Tests via `go test ./cmd/serf-hub/`.

---

## Files touched in this pass

- **Modify:** `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css` — add token block extensions to `:root`, `@media (prefers-color-scheme: light)`, `[data-theme="dark"]`, `[data-theme="light"]`; add `prefers-reduced-motion` global rule.
- **Modify:** `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/templates/app.html` — add Google Fonts preconnect + stylesheet link in `<head>`.
- **Modify:** `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/security.go` — extend `style-src`, add `font-src`.
- **Modify:** `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/security_test.go` — assert new `style-src` value and new `font-src` directive.

No new files are created in this pass. No JavaScript changes. No template changes other than `app.html`.

---

## Task 1: Add spacing, type, leading scale tokens to `:root`

**Files:**
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css:4-34`

This task extends the default `:root` block (currently lines 4–34) by inserting the spacing scale (`--space-0` … `--space-9`), the type-size scale (`--text-2xs` … `--text-2xl`), and the leading scale (`--leading-tight/-snug/-normal/-relaxed`) immediately after the existing legacy alias block. The legacy `--pad: 12px` alias stays — it remains the source of truth for unmigrated rules until those rules are migrated in Pass 3.

- [ ] **Step 1: Verify current state of the `:root` block**

Run: `sed -n '4,34p' /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: the block ends with `  --pad: 12px;` on line 33 and a closing `}` on line 34. If different, stop and re-read the file before editing.

- [ ] **Step 2: Replace the closing `}` of `:root` with the new tokens + closing brace**

Replace the exact text `  --pad: 12px;\n}` (lines 33–34) with the following block. Use the Edit tool with `old_string` `  --pad: 12px;\n}\n` and the new content below.

```css
  --pad: 12px;

  /* Spacing scale — Pass 1. Layout values (padding, margin, gap, top/right/bottom/left)
     use these tokens. 1px hairlines stay literal. See design language §1.3. */
  --space-0: 0;
  --space-1: 2px;
  --space-2: 4px;
  --space-3: 8px;
  --space-4: 12px;
  --space-5: 16px;
  --space-6: 24px;
  --space-7: 32px;
  --space-8: 48px;
  --space-9: 64px;

  /* Type scale — Pass 1. See design language §1.2. */
  --text-2xs: 10px;
  --text-xs:  11px;
  --text-sm:  12px;
  --text-base: 13px;
  --text-md:  14px;
  --text-lg:  16px;
  --text-xl:  18px;
  --text-2xl: 22px;

  /* Leading scale — Pass 1. --leading-relaxed is reserved. */
  --leading-tight:   1.3;
  --leading-snug:    1.5;
  --leading-normal:  1.6;
  --leading-relaxed: 1.7;
}
```

- [ ] **Step 3: Verify the tokens parse**

Run: `head -70 /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css | tail -45`

Expected: every new token line is present, indentation matches the surrounding two-space style, the final `}` closes the `:root` block exactly once.

- [ ] **Step 4: Build to confirm no syntax breakage in CSS-adjacent assets**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds, produces `./serf-hub` binary. The Go build does not parse CSS, but a successful build is the prerequisite for the manual verify at the end of the pass.

- [ ] **Step 5: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && \
git add cmd/serf-hub/assets/style.css && \
git commit -m "ui: add spacing, type, leading scale tokens to :root

First step of Pass 1 token foundation. Adds --space-0..9, --text-2xs..2xl,
and --leading-tight/-snug/-normal/-relaxed alongside the existing color
tokens. Legacy --pad: 12px alias preserved so unmigrated rules keep working.
No visual changes — token block is additive only.

Refs: docs/superpowers/specs/2026-05-22-serf-hub-design-language.md §1.2 §1.3
Refs: docs/superpowers/specs/2026-05-22-serf-hub-responsive-ui-design.md §Pass 1"
```

---

## Task 2: Add radius, motion, z-index, tap-min, noise tokens to `:root`

**Files:**
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css` — extend the `:root` block again with the remaining structural tokens.

This task adds radius (`--radius-sm/-md/-lg/-xl/-pill/-full`), motion (`--motion-fast/-base/-slow` and the semantic `--pulse-cycle`/`--flash-cycle`), z-index (`--z-sticky` through `--z-toast`), `--tap-min` (with desktop default), and the paper-grain `--noise` SVG data URI at 5% white opacity per design language §1.9. These all go inside the same `:root` block, after the leading scale that Task 1 added.

- [ ] **Step 1: Locate the insertion point**

The `:root` block from Task 1 ends with `--leading-relaxed: 1.7;` followed by `}`. Insert the new tokens immediately before that closing `}`.

- [ ] **Step 2: Extend `:root` with the new tokens**

Use the Edit tool. `old_string`:

```css
  --leading-relaxed: 1.7;
}
```

`new_string`:

```css
  --leading-relaxed: 1.7;

  /* Radius scale — Pass 1. See design language §1.4. */
  --radius-sm:   3px;
  --radius-md:   4px;
  --radius-lg:   6px;
  --radius-xl:   8px;
  --radius-pill: 14px;
  --radius-full: 50%;

  /* Motion — three durations, one easing. See design language §1.5. */
  --motion-fast: 100ms ease;
  --motion-base: 160ms ease;
  --motion-slow: 240ms ease;
  --pulse-cycle: 1400ms ease-in-out;
  --flash-cycle: 2000ms ease-out;

  /* Z-index scale — see design language §1.6. */
  --z-sticky:        10;
  --z-fixed-action: 100;
  --z-dropdown:     200;
  --z-overlay:      800;
  --z-drawer:       900;
  --z-modal:       1000;
  --z-toast:       1100;

  /* Touch target floor — design language §1.8. Desktop default; phone
     media query in Pass 3 overrides to 44px. */
  --tap-min: 32px;

  /* Paper-grain texture — design language §1.9. 5% white opacity.
     Applied via ::after pseudo on raised surfaces in later passes. */
  --noise: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='220' height='220'><filter id='n'><feTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='1' stitchTiles='stitch'/><feColorMatrix values='0 0 0 0 1, 0 0 0 0 1, 0 0 0 0 1, 0 0 0 0.05 0'/></filter><rect width='100%25' height='100%25' filter='url(%23n)'/></svg>");
}
```

- [ ] **Step 3: Verify the additions**

Run: `grep -n "^  --radius-pill:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: one match, line number near the top of the file, value `14px;`.

Run: `grep -n "^  --z-modal:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: one match with value `1000;`.

Run: `grep -c "^  --noise:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `1`.

- [ ] **Step 4: Build to confirm**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds.

- [ ] **Step 5: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && \
git add cmd/serf-hub/assets/style.css && \
git commit -m "ui: add radius, motion, z-index, tap-min, noise tokens to :root

Second step of Pass 1 token foundation. Adds --radius-sm/-md/-lg/-xl/-pill/-full,
--motion-fast/-base/-slow plus --pulse-cycle/--flash-cycle semantic tokens,
--z-sticky..--z-toast scale, --tap-min (32px desktop default), and the --noise
paper-grain SVG data URI at 5% white opacity per design language §1.9. No
visual changes; tokens are additive and not yet referenced by any rule.

Refs: docs/superpowers/specs/2026-05-22-serf-hub-design-language.md §1.4 §1.5 §1.6 §1.8 §1.9"
```

---

## Task 3: Add font tokens + prefers-reduced-motion global rule

**Files:**
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css` — add `--font-sans` / `--font-mono` to `:root`; insert the `prefers-reduced-motion` global rule after the four theme blocks.

Two changes in this task:

1. Font-family tokens get added inside `:root`. Body still uses the system stack font-family literal — Pass 2 will migrate `body` to `var(--font-sans)`. Defining the tokens early means later passes can reference them without churning the token block.
2. The `prefers-reduced-motion` rule is the **first** of the two `!important` declarations allowed in the codebase (design language §1.5). It goes immediately after the four theme blocks (before `* { box-sizing: border-box; }` on what is currently line 112) so it cascades to every element from the start of the rule cascade.

- [ ] **Step 1: Add font tokens to `:root`**

Use the Edit tool. `old_string`:

```css
  --noise: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='220' height='220'><filter id='n'><feTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='1' stitchTiles='stitch'/><feColorMatrix values='0 0 0 0 1, 0 0 0 0 1, 0 0 0 0 1, 0 0 0 0.05 0'/></filter><rect width='100%25' height='100%25' filter='url(%23n)'/></svg>");
}
```

`new_string`:

```css
  --noise: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='220' height='220'><filter id='n'><feTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='1' stitchTiles='stitch'/><feColorMatrix values='0 0 0 0 1, 0 0 0 0 1, 0 0 0 0 1, 0 0 0 0.05 0'/></filter><rect width='100%25' height='100%25' filter='url(%23n)'/></svg>");

  /* Font families — design language §1.2. Loaded via Google Fonts link in app.html.
     body migrates to var(--font-sans) in Pass 2. */
  --font-sans: 'Hanken Grotesk', -apple-system, BlinkMacSystemFont, "Segoe UI", "Helvetica Neue", Arial, sans-serif;
  --font-mono: 'JetBrains Mono', ui-monospace, "SFMono-Regular", Menlo, Monaco, Consolas, monospace;
}
```

- [ ] **Step 2: Verify font tokens land in :root only (not duplicated into theme overrides)**

Run: `grep -n "^  --font-sans:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: exactly one match.

Run: `grep -n "^  --font-mono:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: exactly one match.

- [ ] **Step 3: Locate the insertion point for the prefers-reduced-motion rule**

The `[data-theme="light"]` block ends just before the line `* { box-sizing: border-box; }`. Today that closing `}` and the `*` rule are at lines 111–112. After Task 1 and Task 2 they will be later in the file (the added tokens lengthened the file). The reduced-motion rule goes between the close of `[data-theme="light"]` and `* { box-sizing: border-box; }`.

Run: `grep -n "^\* { box-sizing:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: one match. Note the line number for context; you do not need it for the Edit because the unique `old_string` anchors it.

- [ ] **Step 4: Insert the reduced-motion rule**

Use the Edit tool. `old_string`:

```css
* { box-sizing: border-box; }
```

`new_string`:

```css
/* Reduced-motion override — design language §1.5. This and the !important
   declarations inside it are one of the two !important uses allowed in the
   codebase. The second is documented when introduced. */
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 1ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 1ms !important;
  }
}

* { box-sizing: border-box; }
```

- [ ] **Step 5: Verify the reduced-motion rule is present once**

Run: `grep -c "prefers-reduced-motion: reduce" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `1`.

Run: `grep -c "!important" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `3` — the three `!important` lines inside the new rule (`animation-duration`, `animation-iteration-count`, `transition-duration`). These are the only `!important` uses allowed in the codebase per design language §1.5.

- [ ] **Step 6: Build**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds.

- [ ] **Step 7: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && \
git add cmd/serf-hub/assets/style.css && \
git commit -m "ui: add font tokens and prefers-reduced-motion rule

Adds --font-sans (Hanken Grotesk + system fallback) and --font-mono
(JetBrains Mono + ui-monospace fallback) tokens. Body still uses the
system stack literal — Pass 2 migrates body to var(--font-sans).

Adds the global prefers-reduced-motion rule per design language §1.5;
its three !important declarations are one of the two !important uses
allowed in the codebase.

Refs: docs/superpowers/specs/2026-05-22-serf-hub-design-language.md §1.2 §1.5"
```

---

## Task 4: Refresh `[data-theme="light"]` palette per design language §1.1

**Files:**
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css` — replace literal hex values inside `[data-theme="light"]` and `@media (prefers-color-scheme: light)` blocks to match the design-language §1.1 table.

The design language §1.1 table specifies these dark→light pairings; today's code uses slightly different light values. Update both light-palette blocks (`@media (prefers-color-scheme: light)` and `[data-theme="light"]`) so they mirror each other and match the spec exactly.

| Token | Today's light | New light (design language §1.1) |
| --- | --- | --- |
| `--bg-raised` | `#f0f0f1` | `#f1f1f2` |
| `--text-muted` | `#6a6a76` | `#5e5e6a` |
| `--text-dim` | `#a0a0a8` | `#8a8a92` |
| `--rule` | `#e0e0e3` | `#dadadc` |
| `--accent` | `#3b6fc9` | `#2e58b8` |
| `--state-awaiting` | `#c43755` | `#b62a48` |
| `--state-processing` | `#3b6fc9` | `#2e58b8` |
| `--state-warning` | `#a06f1e` | `#8a5a14` |
| `--state-idle` | `#3f7a1e` | `#336a14` |
| `--state-ended` | `#c0c0c8` | `#7a7a82` |
| `--state-subagent` | `#7449c7` | `#5e35b6` |
| `--panel-2` (legacy alias) | `#e8e8ea` | `#e6e6e8` |
| `--accent-2` (legacy alias) | `#7449c7` | `#5e35b6` |

`--bg` (`#fafafa`) and `--text` (`#16161e`) match the spec already and are not changed.

- [ ] **Step 1: Update `@media (prefers-color-scheme: light)` block**

Use the Edit tool. `old_string`:

```css
@media (prefers-color-scheme: light) {
  :root {
    --bg: #fafafa;
    --bg-raised: #f0f0f1;
    --text: #16161e;
    --text-muted: #6a6a76;
    --text-dim: #a0a0a8;
    --rule: #e0e0e3;
    --accent: #3b6fc9;
    --state-awaiting: #c43755;
    --state-processing: #3b6fc9;
    --state-warning: #a06f1e;
    --state-idle: #3f7a1e;
    --state-ended: #c0c0c8;
    --state-subagent: #7449c7;

    --panel: var(--bg-raised);
    --panel-2: #e8e8ea;
    --border: var(--rule);
    --muted: var(--text-muted);
    --accent-2: #7449c7;
    --tool: var(--state-warning);
    --user: var(--state-idle);
    --error: var(--state-awaiting);
  }
}
```

`new_string`:

```css
@media (prefers-color-scheme: light) {
  :root {
    --bg: #fafafa;
    --bg-raised: #f1f1f2;
    --text: #16161e;
    --text-muted: #5e5e6a;
    --text-dim: #8a8a92;
    --rule: #dadadc;
    --accent: #2e58b8;
    --state-awaiting: #b62a48;
    --state-processing: #2e58b8;
    --state-warning: #8a5a14;
    --state-idle: #336a14;
    --state-ended: #7a7a82;
    --state-subagent: #5e35b6;

    --panel: var(--bg-raised);
    --panel-2: #e6e6e8;
    --border: var(--rule);
    --muted: var(--text-muted);
    --accent-2: #5e35b6;
    --tool: var(--state-warning);
    --user: var(--state-idle);
    --error: var(--state-awaiting);
  }
}
```

- [ ] **Step 2: Update `[data-theme="light"]` block**

Use the Edit tool. `old_string`:

```css
:root[data-theme="light"] {
  /* Force light. */
  --bg: #fafafa;
  --bg-raised: #f0f0f1;
  --text: #16161e;
  --text-muted: #6a6a76;
  --text-dim: #a0a0a8;
  --rule: #e0e0e3;
  --accent: #3b6fc9;
  --state-awaiting: #c43755;
  --state-processing: #3b6fc9;
  --state-warning: #a06f1e;
  --state-idle: #3f7a1e;
  --state-ended: #c0c0c8;
  --state-subagent: #7449c7;
  --panel: var(--bg-raised);
  --panel-2: #e8e8ea;
  --border: var(--rule);
  --muted: var(--text-muted);
  --accent-2: #7449c7;
  --tool: var(--state-warning);
  --user: var(--state-idle);
  --error: var(--state-awaiting);
}
```

`new_string`:

```css
:root[data-theme="light"] {
  /* Force light — values mirror @media (prefers-color-scheme: light) above. */
  --bg: #fafafa;
  --bg-raised: #f1f1f2;
  --text: #16161e;
  --text-muted: #5e5e6a;
  --text-dim: #8a8a92;
  --rule: #dadadc;
  --accent: #2e58b8;
  --state-awaiting: #b62a48;
  --state-processing: #2e58b8;
  --state-warning: #8a5a14;
  --state-idle: #336a14;
  --state-ended: #7a7a82;
  --state-subagent: #5e35b6;
  --panel: var(--bg-raised);
  --panel-2: #e6e6e8;
  --border: var(--rule);
  --muted: var(--text-muted);
  --accent-2: #5e35b6;
  --tool: var(--state-warning);
  --user: var(--state-idle);
  --error: var(--state-awaiting);
}
```

- [ ] **Step 3: Verify the two light blocks are byte-equivalent for the shared tokens**

Run:
```
diff <(grep -E "^    --(bg|bg-raised|text|text-muted|text-dim|rule|accent|state-|panel|border|muted|tool|user|error)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css | sed -n '12,33p' | sed 's/^  //') <(grep -E "^  --(bg|bg-raised|text|text-muted|text-dim|rule|accent|state-|panel|border|muted|tool|user|error)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css | sed -n '23,44p')
```

If that diff is too fragile, instead spot-check by running:

Run: `grep -n "#2e58b8" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: at least four matches — `--accent` and `--state-processing` in each of the two light blocks.

Run: `grep -n "#7a7a82" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: exactly two matches — `--state-ended` in each light block.

Run: `grep -c "#3b6fc9" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `0` — the old light accent is fully replaced.

Run: `grep -c "#c43755" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `0` — the old light awaiting is fully replaced.

- [ ] **Step 4: Build**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds.

- [ ] **Step 5: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && \
git add cmd/serf-hub/assets/style.css && \
git commit -m "ui: refresh light palette per design language §1.1

Aligns both light-mode blocks (@media prefers-color-scheme: light and
:root[data-theme=light]) to the design-language §1.1 token table.
Key changes: --accent moves from #3b6fc9 to a darker #2e58b8 (more
contrast on cream); state colors desaturate to their higher-contrast
light variants (--state-ended #c0c0c8 -> #7a7a82, --state-awaiting
#c43755 -> #b62a48, etc.); --rule, --bg-raised, --text-muted/-dim
shift to spec values; legacy --panel-2 and --accent-2 follow.

The light blocks now mirror each other exactly. Dark palette is
unchanged.

Refs: docs/superpowers/specs/2026-05-22-serf-hub-design-language.md §1.1"
```

---

## Task 5: Add `--surface-secondary` + `--accent-secondary` tokens to all four theme blocks

**Files:**
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css` — add the two new canonical tokens to `:root`, `@media (prefers-color-scheme: light)`, `[data-theme="dark"]`, and `[data-theme="light"]`. Re-point the legacy `--panel-2` and `--accent-2` aliases to reference the new tokens.

`--surface-secondary` is the "inset surface within a panel" and replaces the dark-mode hex literal that `--panel-2` carries today. `--accent-secondary` is the "subagent / secondary highlight" and replaces today's `--accent-2` literal. Both tokens must exist in every theme variant. The legacy aliases stay defined (so unmigrated rules keep working) but are re-pointed via `var()` so changing the canonical token automatically updates them.

Dark values: `--surface-secondary: #1c1c24` (matches today's dark `--panel-2`), `--accent-secondary: #bb9af7` (matches today's dark `--accent-2`).

Light values: `--surface-secondary: #e6e6e8`, `--accent-secondary: #5e35b6` (the light values now in `--panel-2` / `--accent-2` after Task 4).

- [ ] **Step 1: Add the two tokens to the default `:root` block**

Use the Edit tool. `old_string`:

```css
  --state-subagent: #bb9af7;
  --diagnostic-provider: var(--state-awaiting);
```

`new_string`:

```css
  --state-subagent: #bb9af7;
  --surface-secondary: #1c1c24;
  --accent-secondary: #bb9af7;
  --diagnostic-provider: var(--state-awaiting);
```

- [ ] **Step 2: Re-point the legacy aliases in the default `:root` block**

Use the Edit tool. `old_string`:

```css
  --panel: var(--bg-raised);
  --panel-2: #1c1c24;
  --border: var(--rule);
  --muted: var(--text-muted);
  --accent-2: #bb9af7;
  --tool: var(--state-warning);
  --user: var(--state-idle);
  --error: var(--state-awaiting);
  --pad: 12px;
```

`new_string`:

```css
  --panel: var(--bg-raised);
  --panel-2: var(--surface-secondary);
  --border: var(--rule);
  --muted: var(--text-muted);
  --accent-2: var(--accent-secondary);
  --tool: var(--state-warning);
  --user: var(--state-idle);
  --error: var(--state-awaiting);
  --pad: 12px;
```

- [ ] **Step 3: Add the two tokens + re-point aliases in `@media (prefers-color-scheme: light)`**

Use the Edit tool. `old_string`:

```css
    --state-subagent: #5e35b6;

    --panel: var(--bg-raised);
    --panel-2: #e6e6e8;
    --border: var(--rule);
    --muted: var(--text-muted);
    --accent-2: #5e35b6;
    --tool: var(--state-warning);
    --user: var(--state-idle);
    --error: var(--state-awaiting);
  }
}
```

`new_string`:

```css
    --state-subagent: #5e35b6;
    --surface-secondary: #e6e6e8;
    --accent-secondary: #5e35b6;

    --panel: var(--bg-raised);
    --panel-2: var(--surface-secondary);
    --border: var(--rule);
    --muted: var(--text-muted);
    --accent-2: var(--accent-secondary);
    --tool: var(--state-warning);
    --user: var(--state-idle);
    --error: var(--state-awaiting);
  }
}
```

- [ ] **Step 4: Add the two tokens + re-point aliases in `[data-theme="dark"]`**

Use the Edit tool. `old_string`:

```css
  --state-subagent: #bb9af7;
  --panel: var(--bg-raised);
  --panel-2: #1c1c24;
  --border: var(--rule);
  --muted: var(--text-muted);
  --accent-2: #bb9af7;
  --tool: var(--state-warning);
  --user: var(--state-idle);
  --error: var(--state-awaiting);
}
```

`new_string`:

```css
  --state-subagent: #bb9af7;
  --surface-secondary: #1c1c24;
  --accent-secondary: #bb9af7;
  --panel: var(--bg-raised);
  --panel-2: var(--surface-secondary);
  --border: var(--rule);
  --muted: var(--text-muted);
  --accent-2: var(--accent-secondary);
  --tool: var(--state-warning);
  --user: var(--state-idle);
  --error: var(--state-awaiting);
}
```

- [ ] **Step 5: Add the two tokens + re-point aliases in `[data-theme="light"]`**

Use the Edit tool. `old_string`:

```css
  --state-subagent: #5e35b6;
  --panel: var(--bg-raised);
  --panel-2: #e6e6e8;
  --border: var(--rule);
  --muted: var(--text-muted);
  --accent-2: #5e35b6;
  --tool: var(--state-warning);
  --user: var(--state-idle);
  --error: var(--state-awaiting);
}
```

`new_string`:

```css
  --state-subagent: #5e35b6;
  --surface-secondary: #e6e6e8;
  --accent-secondary: #5e35b6;
  --panel: var(--bg-raised);
  --panel-2: var(--surface-secondary);
  --border: var(--rule);
  --muted: var(--text-muted);
  --accent-2: var(--accent-secondary);
  --tool: var(--state-warning);
  --user: var(--state-idle);
  --error: var(--state-awaiting);
}
```

- [ ] **Step 6: Verify counts**

Run: `grep -c "^  --surface-secondary:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `2` (the two top-level blocks `:root` and `[data-theme="dark"]`/`[data-theme="light"]`; the `@media` block's indent is 4 spaces).

Run: `grep -c "  --surface-secondary:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `4` — once per theme variant.

Run: `grep -c "  --accent-secondary:" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `4` — once per theme variant.

Run: `grep -c "  --panel-2: var(--surface-secondary)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `4`.

Run: `grep -c "  --accent-2: var(--accent-secondary)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `4`.

Run: `grep -n "panel-2: #" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: no matches (every `--panel-2` is now `var(--surface-secondary)`).

Run: `grep -n "accent-2: #" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: no matches.

- [ ] **Step 7: Build**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds.

- [ ] **Step 8: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && \
git add cmd/serf-hub/assets/style.css && \
git commit -m "ui: add --surface-secondary and --accent-secondary to all four theme blocks

Pulls the inset-surface and subagent-highlight values into the theme
abstraction. --surface-secondary replaces the dark-mode hex literal that
--panel-2 carried; --accent-secondary replaces --accent-2's literal.
Both tokens exist in :root, @media (prefers-color-scheme: light),
[data-theme=dark], and [data-theme=light]. The legacy --panel-2 and
--accent-2 aliases are retained but now reference the new tokens via
var(), so unmigrated rules keep working without literals.

Refs: docs/superpowers/specs/2026-05-22-serf-hub-design-language.md §1.1"
```

---

## Task 6: Add `--btn-primary-text` token to dark + light theme blocks

**Files:**
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css` — add `--btn-primary-text` to all four theme blocks per design language §1.1 + §3.1.

`--btn-primary-text` is the foreground color of `.btn-primary`. In dark mode the primary button is `--accent` (`#7aa2f7`) with near-black text (`var(--bg)` = `#0a0a0e`) — pairing carries WCAG-AA. In light mode the primary button is `--accent` (`#2e58b8`) with cream text (`#fafafa`) — same logic, inverted. Defining it as a token keeps the button rule theme-agnostic when introduced in Pass 4.

- [ ] **Step 1: Add to default `:root` (dark)**

Use the Edit tool. `old_string`:

```css
  --surface-secondary: #1c1c24;
  --accent-secondary: #bb9af7;
  --diagnostic-provider: var(--state-awaiting);
```

`new_string`:

```css
  --surface-secondary: #1c1c24;
  --accent-secondary: #bb9af7;
  --btn-primary-text: var(--bg);
  --diagnostic-provider: var(--state-awaiting);
```

- [ ] **Step 2: Add to `@media (prefers-color-scheme: light)`**

Use the Edit tool. `old_string`:

```css
    --surface-secondary: #e6e6e8;
    --accent-secondary: #5e35b6;

    --panel: var(--bg-raised);
```

`new_string`:

```css
    --surface-secondary: #e6e6e8;
    --accent-secondary: #5e35b6;
    --btn-primary-text: #fafafa;

    --panel: var(--bg-raised);
```

- [ ] **Step 3: Add to `[data-theme="dark"]`**

Use the Edit tool. `old_string`:

```css
  --surface-secondary: #1c1c24;
  --accent-secondary: #bb9af7;
  --panel: var(--bg-raised);
  --panel-2: var(--surface-secondary);
```

`new_string`:

```css
  --surface-secondary: #1c1c24;
  --accent-secondary: #bb9af7;
  --btn-primary-text: var(--bg);
  --panel: var(--bg-raised);
  --panel-2: var(--surface-secondary);
```

- [ ] **Step 4: Add to `[data-theme="light"]`**

Use the Edit tool. `old_string`:

```css
  --surface-secondary: #e6e6e8;
  --accent-secondary: #5e35b6;
  --panel: var(--bg-raised);
  --panel-2: var(--surface-secondary);
```

`new_string`:

```css
  --surface-secondary: #e6e6e8;
  --accent-secondary: #5e35b6;
  --btn-primary-text: #fafafa;
  --panel: var(--bg-raised);
  --panel-2: var(--surface-secondary);
```

- [ ] **Step 5: Verify counts**

Run: `grep -c "btn-primary-text" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: `4` — one per theme block.

Run: `grep -n "btn-primary-text: var(--bg)" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: two matches (default `:root` and `[data-theme="dark"]`).

Run: `grep -n "btn-primary-text: #fafafa" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/assets/style.css`

Expected: two matches (`@media (prefers-color-scheme: light)` and `[data-theme="light"]`).

- [ ] **Step 6: Build**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds.

- [ ] **Step 7: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && \
git add cmd/serf-hub/assets/style.css && \
git commit -m "ui: add --btn-primary-text token to all four theme blocks

Foreground color for .btn-primary, defined per theme so WCAG-AA contrast
holds in both. Dark: var(--bg) (#0a0a0e on #7aa2f7 ~7.2:1). Light:
#fafafa (#fafafa on #2e58b8). The .btn-primary rule introduced in Pass 4
will reference this token; defining it now keeps later passes theme-agnostic.

Refs: docs/superpowers/specs/2026-05-22-serf-hub-design-language.md §1.1 §3.1"
```

---

## Task 7: Add Google Fonts preconnect + stylesheet link to `app.html`

**Files:**
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/templates/app.html:5-17`

Load Hanken Grotesk (sans, weights 100–900 italic + roman) and JetBrains Mono (mono, weights 100–800 italic + roman) from Google Fonts. Preconnect to both `fonts.googleapis.com` (the CSS host) and `fonts.gstatic.com` (the WOFF2 host) so the browser starts the TCP+TLS handshake in parallel with the stylesheet download.

The new links go inside `<head>`, after the existing `<link rel="icon">` (line 8) and before the existing inline theme-restore `<script>` (lines 9–16). Order is: icon, preconnect googleapis, preconnect gstatic (crossorigin), fonts stylesheet, theme script, app stylesheet. Theme script must keep running before the app stylesheet so the `data-theme` attribute is present before any CSS evaluates `[data-theme="*"]` selectors.

- [ ] **Step 1: Insert the font preconnect + stylesheet links**

Use the Edit tool. `old_string`:

```html
  <link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><circle cx='50' cy='50' r='40' fill='%237aa2f7'/></svg>">
  <script>
```

`new_string`:

```html
  <link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><circle cx='50' cy='50' r='40' fill='%237aa2f7'/></svg>">
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Hanken+Grotesk:ital,wght@0,100..900;1,100..900&family=JetBrains+Mono:ital,wght@0,100..800;1,100..800&display=swap" rel="stylesheet">
  <script>
```

- [ ] **Step 2: Verify the insertion**

Run: `grep -c "fonts.googleapis.com" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/templates/app.html`

Expected: `2` — one preconnect, one stylesheet href.

Run: `grep -c "fonts.gstatic.com" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/templates/app.html`

Expected: `1` — the crossorigin preconnect.

Run: `grep -n "rel=\"preconnect\"" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/templates/app.html`

Expected: two lines, adjacent.

- [ ] **Step 3: Build**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds. Templates are embedded via Go's `embed` package; a successful build confirms the template parses.

- [ ] **Step 4: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && \
git add cmd/serf-hub/templates/app.html && \
git commit -m "ui: load Hanken Grotesk and JetBrains Mono via Google Fonts

Adds preconnect to fonts.googleapis.com + fonts.gstatic.com (the latter
crossorigin per Google's recommendation) and a single stylesheet link
that loads both faces with full variable-weight ranges (sans 100..900,
mono 100..800, both ital + roman). display=swap so text renders in the
fallback while WOFF2s download.

The CSP currently blocks this — the next commit updates style-src and
adds font-src. Until that commit lands, the fonts will fail to load but
the system fallback in --font-sans / --font-mono keeps the UI legible.

Refs: docs/superpowers/specs/2026-05-22-serf-hub-design-language.md §1.2"
```

---

## Task 8: Update CSP to allow fonts.googleapis.com (style) and fonts.gstatic.com (font); update test

**Files:**
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/security.go:17-29`
- Modify: `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/security_test.go:25-37`

Two directive changes:

1. `style-src 'self' 'unsafe-inline'` → `style-src 'self' 'unsafe-inline' https://fonts.googleapis.com` (so the browser can fetch the Google Fonts CSS).
2. Add a new directive `font-src 'self' https://fonts.gstatic.com` (so the browser can fetch the WOFF2 files referenced by that CSS).

The test currently asserts four substrings appear in the CSP header. Add two more: the new full `style-src` value and the new `font-src` directive.

We follow TDD: update the test first to assert the new state, watch it fail, then update the middleware to satisfy it.

- [ ] **Step 1: Update `security_test.go` to assert the new directives (failing test)**

Use the Edit tool on `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/security_test.go`. `old_string`:

```go
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self' 'unsafe-inline'",
		// img-src must include data: (transcript-inline base64 thumbnails),
		// blob: (composer-attachments reencodeToPng pipeline; kata 1pgw), and
		// https: (URL-backed AppWire replay images).
		"img-src 'self' data: blob: https:",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got: %s", want, csp)
		}
	}
}
```

`new_string`:

```go
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self' 'unsafe-inline'",
		// style-src allows fonts.googleapis.com so the Google Fonts CSS
		// (loaded by app.html for Hanken Grotesk + JetBrains Mono) can be
		// fetched. 'unsafe-inline' remains because settings partials and
		// app.html use inline <style>/style attributes for data-driven
		// values (e.g., context-bar fill width).
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		// font-src allows fonts.gstatic.com so the WOFF2 font files
		// referenced by the Google Fonts CSS can be downloaded.
		"font-src 'self' https://fonts.gstatic.com",
		// img-src must include data: (transcript-inline base64 thumbnails),
		// blob: (composer-attachments reencodeToPng pipeline; kata 1pgw), and
		// https: (URL-backed AppWire replay images).
		"img-src 'self' data: blob: https:",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got: %s", want, csp)
		}
	}
}
```

- [ ] **Step 2: Run the test — expect failure**

Run: `cd /home/jesse/git/prime-radiant/serf && go test -run TestCSPMiddleware_SetsStrictDefault ./cmd/serf-hub/`

Expected: FAIL with two `CSP missing ...` errors — one for the new `style-src` value (the existing CSP has the shorter form without `https://fonts.googleapis.com`) and one for the new `font-src` directive. The exact assertion message will look like:

```
--- FAIL: TestCSPMiddleware_SetsStrictDefault (0.00s)
    security_test.go:38: CSP missing "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com"; got: default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'
    security_test.go:38: CSP missing "font-src 'self' https://fonts.gstatic.com"; got: default-src 'self'; ...
```

If the test passes, the middleware was already updated or the test edit didn't land — re-check before continuing.

- [ ] **Step 3: Update `security.go` to satisfy the test**

Use the Edit tool on `/home/jesse/git/prime-radiant/serf/cmd/serf-hub/security.go`. `old_string`:

```go
// CSPMiddleware sets a Content-Security-Policy that limits resource origins to
// same-origin. Inline scripts are allowed because several templates (app.html,
// settings partials, credentials) use inline IIFEs for page initialisation;
// migrating them all to asset files is tracked separately.
//
// `img-src` allows `https:` for remote AppWire replay images, and `blob:` so
// the composer-attachments helper
// (cmd/serf-hub/assets/composer-attachments.js:reencodeToPng) can decode a
// pasted / dropped / picked image by loading a `URL.createObjectURL(blob)`
// reference into an `Image` element before re-encoding to PNG (kata 1pgw —
// without `blob:` here every image attachment surface renders "Not an image"
// because the Image element refuses the blob URL).
func CSPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob: https:; "+
				"connect-src 'self'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
```

`new_string`:

```go
// CSPMiddleware sets a Content-Security-Policy that limits resource origins to
// same-origin. Inline scripts are allowed because several templates (app.html,
// settings partials, credentials) use inline IIFEs for page initialisation;
// migrating them all to asset files is tracked separately.
//
// `img-src` allows `https:` for remote AppWire replay images, and `blob:` so
// the composer-attachments helper
// (cmd/serf-hub/assets/composer-attachments.js:reencodeToPng) can decode a
// pasted / dropped / picked image by loading a `URL.createObjectURL(blob)`
// reference into an `Image` element before re-encoding to PNG (kata 1pgw —
// without `blob:` here every image attachment surface renders "Not an image"
// because the Image element refuses the blob URL).
//
// `style-src` allows `https://fonts.googleapis.com` so app.html can load the
// Google Fonts CSS that pulls in Hanken Grotesk + JetBrains Mono. The CSS
// itself references WOFF2 files on `fonts.gstatic.com`, which is allowed via
// the `font-src` directive below. See design language §1.2.
func CSPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"img-src 'self' data: blob: https:; "+
				"connect-src 'self'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Run the test — expect pass**

Run: `cd /home/jesse/git/prime-radiant/serf && go test -run TestCSPMiddleware_SetsStrictDefault ./cmd/serf-hub/`

Expected: PASS. Output ends with `ok  	github.com/...cmd/serf-hub	<elapsed>`.

- [ ] **Step 5: Run the full package test suite to confirm nothing else regressed**

Run: `cd /home/jesse/git/prime-radiant/serf && go test -count=1 ./cmd/serf-hub/`

Expected: PASS. All existing tests in the package still pass.

- [ ] **Step 6: Build**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds.

- [ ] **Step 7: Commit**

```bash
cd /home/jesse/git/prime-radiant/serf && \
git add cmd/serf-hub/security.go cmd/serf-hub/security_test.go && \
git commit -m "ui: allow Google Fonts in CSP (style-src + font-src)

Extends style-src to allow fonts.googleapis.com so app.html can load the
Hanken Grotesk + JetBrains Mono stylesheet. Adds a new font-src directive
allowing fonts.gstatic.com so the WOFF2 font files referenced by that
stylesheet can be downloaded.

security_test.go updated to assert both directives. All other CSP
directives (default-src, script-src, img-src, connect-src, base-uri,
form-action, frame-ancestors) are unchanged.

Refs: docs/superpowers/specs/2026-05-22-serf-hub-design-language.md §1.2
Refs: docs/superpowers/specs/2026-05-22-serf-hub-responsive-ui-design.md §Pass 1"
```

---

## Task 9: Manual verify — build, run, eyeball the UI, confirm fonts load and no CSP violations

**Files:** none modified in this task.

Pass 1 is visually invariant — no token added in Tasks 1–6 is yet referenced by any rule (the rules below the token block still use legacy aliases like `--panel-2`, `--pad`, `--accent-2`, all of which now resolve via `var()` to the new tokens but carry the same final values). The light palette is the one visible change: cream/blue light mode is intentionally darker-accent than today's, per design language §1.1. Fonts are loaded but `body` still uses the system stack literal until Pass 2.

The acceptance bar for this pass is:

1. The hub starts and renders.
2. The dark theme is pixel-identical to pre-merge.
3. The light theme reflects the spec's intended values (visibly different from today, matching design language §1.1).
4. The browser console shows zero CSP violations.
5. The Network panel shows the Google Fonts CSS (`fonts.googleapis.com`) and at least one WOFF2 (`fonts.gstatic.com`) downloading successfully (HTTP 200).

- [ ] **Step 1: Build once more, fresh, to confirm a clean tree**

Run: `cd /home/jesse/git/prime-radiant/serf && make build-hub`

Expected: build succeeds in a few seconds; `./serf-hub` exists.

- [ ] **Step 2: Run the hub locally in the background**

Run: `cd /home/jesse/git/prime-radiant/serf && ./serf-hub` in a separate terminal (or background it with `&`). Note the address it logs (typically `127.0.0.1:9180`).

Expected: log output includes a line like `serving on 127.0.0.1:9180` (or similar; the exact format is in `cmd/serf-hub/main.go`). No panic, no startup error.

- [ ] **Step 3: Open the hub URL in Chrome (or Firefox) with DevTools open**

Open the address from Step 2 in a browser. Open DevTools (`F12` or `Cmd+Option+I`). Switch to the Console tab.

Expected: zero `Refused to load ...` errors. Zero `Content Security Policy` violations. If any appear, the CSP changes in Task 8 are incomplete — re-check the directive strings.

- [ ] **Step 4: Confirm Google Fonts load successfully**

Switch DevTools to the Network tab. Filter by "fonts". Refresh the page.

Expected:
- One request to `https://fonts.googleapis.com/css2?family=Hanken+Grotesk:...` returns HTTP 200 with content-type `text/css`.
- At least one request to `https://fonts.gstatic.com/s/hankengrotesk/...woff2` returns HTTP 200 with content-type `font/woff2`.
- At least one request to `https://fonts.gstatic.com/s/jetbrainsmono/...woff2` returns HTTP 200 with content-type `font/woff2`.

If any of these fails, the CSP isn't allowing the request. Re-read the Console for the exact rejection and adjust the relevant directive.

- [ ] **Step 5: Visual check — dark theme**

In DevTools, force dark mode: `Rendering` panel → `Emulate CSS media feature prefers-color-scheme` → `dark`. (Or set `localStorage["serf-hub.theme"] = "dark"` in the Console and refresh.)

Click through:
- The default workspace (or `/new` if no session exists).
- A spawn form if reachable (`/new`).
- The settings page (`/settings`).
- The search palette (`Cmd+K` or `Ctrl+K`).

Expected: every surface looks the same as it did pre-merge. Dark backgrounds, the same accent blue (`#7aa2f7`), the same Tokyo Night state colors. No color shifts. No layout shifts. Body text still renders in the system font (San Francisco / Segoe UI / Liberation Sans) because Pass 1 hasn't migrated `body` to `var(--font-sans)` yet.

- [ ] **Step 6: Visual check — light theme**

Set `data-theme="light"` via Console: `document.documentElement.setAttribute("data-theme", "light"); localStorage.setItem("serf-hub.theme", "light");` and refresh. (Or use DevTools' `Emulate CSS media feature prefers-color-scheme: light` and clear any `data-theme` override.)

Click through the same surfaces.

Expected: light theme renders with the new design language §1.1 palette — a darker accent blue (`#2e58b8`) and noticeably darker state colors than the previous light theme. This is the one intended visual change in Pass 1. Compare against the spec's §1.1 table: `--accent: #2e58b8`, `--state-ended: #7a7a82`, `--state-awaiting: #b62a48`, `--state-warning: #8a5a14`, `--state-idle: #336a14`, `--state-subagent: #5e35b6`. All surfaces still legible; contrast against the cream `--bg` (`#fafafa`) is stronger than before.

- [ ] **Step 7: Check `prefers-reduced-motion` works**

In DevTools `Rendering` panel: `Emulate CSS media feature prefers-reduced-motion` → `reduce`. Refresh.

If there are any animated elements visible (sidebar loading dots, `htmx` swap animations, the running-indicator pulse on a live session), they should snap rather than animate.

Expected: any animations cap to 1ms (effectively instant). No visible degradation of layout.

Reset: switch the emulation back to `no-preference` for the rest of the verification.

- [ ] **Step 8: Stop the hub**

In the terminal running `./serf-hub`, send `Ctrl+C`. Or if it was backgrounded, `kill %1` (or whichever job number).

Expected: clean shutdown, prompt returns.

- [ ] **Step 9: Run the full Go test suite once more for safety**

Run: `cd /home/jesse/git/prime-radiant/serf && go test -count=1 ./...`

Expected: `ok` for every package. If any package fails — particularly anything in `cmd/serf-hub/...` — investigate. The Pass 1 changes are CSS, an HTML template, and the security middleware; no other package should be affected.

- [ ] **Step 10: No commit — verification step only**

This task is the sign-off for Pass 1. If Steps 1–9 all pass, Pass 1 is complete. No files were modified in this task, so no commit. The eight previous commits constitute the Pass 1 PR.

If any step revealed an issue, fix it inline (re-edit the offending file from the relevant earlier task), build again, and re-run the verification. Don't amend prior commits — make a follow-up commit on the same branch.

---

## Self-Review

Spec coverage check (from the user's Pass 1 scope brief):

- All new tokens (`--space-*`, `--text-*`, `--leading-*`, `--radius-*`, `--motion-*`, `--z-*`, `--noise`, `--tap-min`, `--font-sans`, `--font-mono`, `--btn-primary-text`) — covered by Tasks 1, 2, 3, 6.
- Legacy aliases preserved — Task 1 retains `--pad: 12px`; Task 5 retains `--panel-2` and `--accent-2` (pointing them at new tokens).
- Google Fonts preconnect + stylesheet — Task 7.
- CSP `style-src` extended + `font-src` added — Task 8.
- CSP test updated — Task 8 (TDD: test first, then implementation).
- `--surface-secondary` + `--accent-secondary` in all four theme blocks; legacy aliases re-pointed — Task 5.
- Light palette refresh (`--accent: #2e58b8`, `--state-ended: #7a7a82`, plus the rest of design language §1.1 table) — Task 4.
- `--noise` SVG data URI at 5% white opacity — Task 2 (the `feColorMatrix` row `0 0 0 0.05 0` = alpha 0.05 white).
- `prefers-reduced-motion` rule — Task 3 (one of the two `!important` uses allowed; documented in the rule's preceding comment).
- Build + manual verify (visually invariant in dark, light updated to spec, no CSP violations, fonts download) — Task 9.

Type and reference consistency:

- Every CSS custom property name used in commit messages and step text matches what is defined in the code blocks: `--space-N`, `--text-{2xs,xs,sm,base,md,lg,xl,2xl}`, `--leading-{tight,snug,normal,relaxed}`, `--radius-{sm,md,lg,xl,pill,full}`, `--motion-{fast,base,slow}`, `--pulse-cycle`, `--flash-cycle`, `--z-{sticky,fixed-action,dropdown,overlay,drawer,modal,toast}`, `--tap-min`, `--noise`, `--font-sans`, `--font-mono`, `--surface-secondary`, `--accent-secondary`, `--btn-primary-text`.
- Hex values match the design language §1.1 table exactly: dark `--accent: #7aa2f7`; light `--accent: #2e58b8`; light `--state-ended: #7a7a82`; etc.
- The `--noise` SVG is the byte-exact string from design language §1.9 with `0.05` alpha in the color matrix.
- CSP directive strings match exactly between `security.go` and the assertions in `security_test.go`.
- Task 7 inserts the font links before the existing inline theme script so the theme `data-theme` attribute is set before any CSS evaluates — preserves today's contract that theme override happens pre-paint.

No placeholders. Every step has full code blocks or exact commands with expected output.
