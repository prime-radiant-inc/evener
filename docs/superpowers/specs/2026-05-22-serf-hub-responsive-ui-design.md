# Serf Hub Responsive UI Pass — Implementation Spec

**Status:** Draft 1 · 2026-05-22
**Design language:** [2026-05-22-serf-hub-design-language.md](./2026-05-22-serf-hub-design-language.md)

## Summary

A comprehensive UI/UX pass on the `serf-hub` web interface. Three intertwined goals:

1. **Fully responsive** on desktop (≥768px) and phone (≤767px). Phone landscape and wide (≥1440px) get targeted treatments. Tablet portrait inherits desktop (user decision).
2. **Polished** — every surface gets the design-language treatment: token discipline, typographic identity (Hanken Grotesk + JetBrains Mono), the *Workshop Log* aesthetic. No more density inconsistency, no hover-only affordances on touch, no missing focus rings.
3. **Consolidated** — five settings sub-page layout patterns collapse to two canonical primitives. Eight button variants collapse to six. Hardcoded colors, font sizes, spacings, radii, motion durations, and z-indexes migrate to a token scale.

The hub UI already has a clean structural foundation: 0 `!important`, 0 high-specificity selectors, only 3 inline `style` attributes, and a mature dark/light color token system. This pass builds on those bones.

## Goals

- Phone (320–767px) and phone landscape work as first-class targets — every surface usable, every control reachable, all touch targets ≥44px.
- Desktop polish: typography, motion, and density tuning so the hub feels deliberately made.
- Wide-monitor treatment (≥1440px): conversation max-width cap, optional sidebar rail.
- Light mode with stronger contrast than today's mirror — same tokens, intentional contrast bumps.
- Settings consolidation: 13 sub-pages onto 2 canonical primitives + one one-off (in-repo trust UI).
- Persistent affordances — no hover-only controls. Touch users discover everything mouse users see.
- A11y baseline: `:focus-visible` rings on every interactive element, `aria-live` on status regions, keyboard nav on project chevrons, semantic `<dl>` for definition lists.
- New deliberate feature surfaces: top-center toasts, skeleton loading states, sidebar rail mode (`⌘B`).

## Non-goals

- Functional changes to the AppWire protocol, session state machine, or htmx swap contract.
- New backend endpoints.
- Tablet-specific layout (768–1023px inherits desktop).
- Rewriting `renderer.js` rendering logic — markup conventions are preserved.
- Theming beyond light + dark.

---

## Constraints (preserve these contracts)

The audit produced a list of behavioral contracts the visual pass must not break:

1. **htmx swap targets** — `#sidebar`, `#workspace`, `#settings-content`, `#conversation`, `.workspace-meta`, `.input-status` stay as stable swap targets. Their innerHTML can change; the IDs/classes used as `hx-target` must not.
2. **JS class hooks** — `.optimistic-pending`, `.optimistic-failed`, `.drop-active`, `.collapsed`, `.active`, the project-chevron `[data-open]` attribute, and `data-state` values consumed by `notifications.js` stay defined. Some are renamed via data-attributes during migration (see §6.3) but the **meaning** survives.
3. **Data attributes carrying state** — every `data-*` listed in the templates inventory stays valid input to CSS/JS. We add new ones (`data-active`, `data-pulse`, `data-sidebar-rail`, `data-phone-density`) without removing existing ones.
4. **LaunchConfigControls.render()** API — the function still produces a form's body; the form just uses the `settings-table` row primitive now.
5. **Custom events** — `sidebar:refresh`, `serf-hub:notifications-changed`, `credentials-reload`, `serf/auth/updated`, `serf/launch/updated` keep their names + payloads.
6. **Same-origin guards & CSP** — design changes don't touch the strict CSP. Fonts are loaded from `fonts.googleapis.com` + `fonts.gstatic.com`; if these aren't allowed by the current policy, the CSP allowlist gets updated.
7. **Tokyo Night identity** — dark palette unchanged. Light mode gets stronger contrast but stays in the same family.

---

## Architecture

The pass is a **CSS + templates** migration. No Go changes except:
- A new `cmd/serf-hub/assets/style.css` (the existing file rewritten in passes).
- Template tweaks (sidebar partial restructure, settings partials normalized to two patterns, hamburger moves into header).
- One or two small JS additions (`window.SerfToast`, sidebar rail toggle persistence, body `data-phone-density` from settings).
- CSP allowlist for fonts.googleapis.com / fonts.gstatic.com (if not already permitted).

### File touch map

```
cmd/serf-hub/
  assets/
    style.css                       ← full rewrite (1037 lines → ~1400 lines with new components + tokens)
    fonts.css                       ← (optional) vendored @font-face fallbacks
    fonts/*.woff2                   ← (optional) vendored fonts
    toast.js                        ← new — window.SerfToast.show()
    sidebar.js                      ← + rail toggle, density toggle handling
    theme.js                        ← + density persistence
    settings.js                     ← + form-status aria-live wiring
  templates/
    app.html                        ← + toast-region, font preconnect, rail-toggle button
    partials/
      sidebar.html                  ← row markup restructured (dot-col + text-col)
      workspace.html                ← hamburger moves into header, status pill → badge
      spawn.html                    ← chip + advanced details tweaks
      settings.html                 ← (largely unchanged shell)
      settings/*.html               ← all 14 sub-pages normalized to settings-table or settings-collection
      credentials.html              ← migrated to settings-collection pattern
      input_strip.html              ← (largely unchanged)
```

### Token migration scaffolding

To minimize regression risk, tokens are added to `:root` first (additive) while keeping all legacy aliases working. Each surface migrates in its own pass. When all rules use the new tokens, legacy aliases are deleted in a final cleanup.

The token block at the top of `style.css` grows from ~85 lines (color only) to ~180 lines (color + spacing + type + radius + motion + z + texture + fonts).

---

## Migration order

Eight passes, each independently shippable and reviewable. Earlier passes establish foundations; later passes depend on them.

### Pass 1 — Foundations (tokens + fonts + CSP)

Goal: add the token scale, load fonts, update CSP. No visual changes yet — existing rules still use legacy aliases.

- Add `--space-*`, `--text-*`, `--leading-*`, `--radius-*`, `--motion-*`, `--z-*`, `--noise`, `--tap-min`, `--font-sans`, `--font-mono`, `--btn-primary-text` tokens to `:root` and theme overrides.
- Add Hanken Grotesk + JetBrains Mono `<link>` preconnect + `@import` to `app.html` (or `style.css`).
- **CSP update (required)** — `cmd/serf-hub/security.go` currently sets `default-src 'self'` with explicit `script-src`, `style-src`, `img-src`, `connect-src`, no `font-src`. Add:
  - `style-src 'self' 'unsafe-inline' https://fonts.googleapis.com`
  - `font-src 'self' https://fonts.gstatic.com`
  - Update `cmd/serf-hub/security_test.go` to assert the new directives.
- Add `--surface-secondary` and `--accent-secondary` tokens; old `--panel-2` and `--accent-2` retain values via aliases.
- Refresh light palette tokens (`--accent: #2e58b8`, `--state-ended: #7a7a82`, awaiting/idle/warning state color adjustments).

**Verify:** existing UI renders pixel-equivalent to today. Open dev tools, confirm token-substitution didn't change anything. Run `serf-hub` locally with `make build-hub && ./serf-hub`, click through the workspace, sidebar, spawn, settings, credentials, search palette. Diff against pre-merge screenshots.

### Pass 2 — Typography migration

Goal: switch every selector that sets `font-size` or `font-family` to use tokens. No layout changes yet.

- Switch `body` font-family to `var(--font-sans)`.
- Add `.mono` utility class (sets `font-family: var(--font-mono)`).
- For each selector with a `font-size` literal, replace with the closest `--text-*` token (round 14.5 → 14, 12.5 → 12, 11.5 → 12, 10.5 → 10).
- For tool calls, paths, code, kbd, status text, model names, timestamps, sidebar meta, picker options, status badges — explicitly set `font-family: var(--font-mono)`.
- For everything else, sans inherits from body.
- Replace `line-height: 1.55, 1.6, 1.7` literals with `--leading-*` tokens.

Conversation body sits at `--text-md` initially. After this pass ships, A/B 13px vs 14px in the live app — whichever wins becomes the standard. Update the doc.

**Verify:** scan every surface for type that looks "wrong" (still uses an old size or wrong face). Document any intentional exceptions. Diff screenshots; differences should match the design language type-by-surface table.

### Pass 3 — Spacing, radius, motion, z-index migration

Goal: switch every padding/margin/gap/radius/transition/animation/z-index to tokens.

- Padding/margin/gap: replace literals with `--space-*`. Round 5px → `--space-2` (4) or `--space-3` (8) — pick by intent.
- Border-radius: replace literals with `--radius-*`. Stray `10px` and `12px` round to `--radius-pill`; `2px` rounds to `--radius-sm`.
- Transitions/animations: every duration goes to `--motion-fast/-base/-slow` or the `--pulse-cycle/--flash-cycle` semantic tokens.
- Z-index: replace `1, 50, 100, 150, 199, 200` with `--z-*`.

Add `prefers-reduced-motion` rule (§1.5 of design language). One `!important` allowed (with the rule forcing it).

**Verify:** no visible shifts. Compare pixel-perfect snapshots against pre-merge. Validate reduced-motion by running with `@media (prefers-reduced-motion: reduce)` forced; all animations should cap at 1ms.

### Pass 4 — Buttons, chips, status, focus rings

Goal: collapse the 8 button variants to 6 canonical (`.btn` + 6 modifiers). Replace `.status-pill` with typographic `.status-badge`. Add `:focus-visible` rings universally.

- Introduce `.btn` base class + `.btn-primary/-secondary/-ghost/-danger/-icon/-chip`.
- Migrate each legacy button class to one of the new variants (mapping in design language §3.1). Templates updated.
- Add `:focus-visible` rule with `outline: 2px solid var(--accent); outline-offset: 1px;` — applies to all interactive elements (`button`, `a`, `input`, `textarea`, `select`, `[role="button"]`, `summary`, `.sb-row`, `.search-row`, `.settings-nav-link`, `.chip-picker-option`, `.row-icon-btn`, etc.).
- Replace `.status-pill` definitions with `.status-badge` (mono small-caps in state color, no background). Templates updated to use the new class. Old `.status-pill` definitions stay as a transitional alias if used by tests; remove after migration.
- Add `[data-pulse]` attribute handling on status dots; JS sets it when `data-state` ∈ `active | awaiting`.

**Verify:** every button on every surface has the right variant. Tab through the entire UI with keyboard — every focus state visible. Confirm status badges render the correct color in both themes. No remaining hover-only invisible controls.

### Pass 5 — Sidebar restructure + workspace header + slide-over a11y

Goal: implement the 2-line sidebar row, the rail-mode toggle, the workspace header refactor, AND the slide-over focus-management infrastructure (because slide-overs share the drawer pattern with mobile sidebar).

**Sidebar:**
- Restructure `sidebar.html` partial: each `.sb-row` becomes `dot-col + text-col` with title (2-line wrap via `-webkit-line-clamp`) + mono meta line below.
- **Update `web_test.go`** lines that assert `"session-row"` / `"live-row"` to assert the new `"sb-row"` class with appropriate `data-state` values. Co-applying legacy classes during a transitional period is rejected — it leaves dead CSS selectors. Update tests in lockstep.
- Project headers compact mono with persistent ⚙ + ＋ icons (no hover-only `opacity: 0`).
- Sidebar width 260px.
- Add `data-sidebar-rail` body state + rail-toggle button at top of sidebar. CSS for 56px rail mode (icons + status dots only).
- `⌘B` keyboard shortcut toggles rail (persist to `localStorage["serf-hub.sidebar.rail"]`).
- Mobile drawer behavior preserved; `data-sidebar-open` triggers slide-in.
- Container queries: `@container sidebar (max-width: 80px)` hides text columns.
- **Wire `data-active`**: extend `sidebar.js` to listen for the htmx `htmx:afterSwap` event on `#workspace`. On swap, parse the new URL (`/s/<id>` or `/new`); find the matching `.sb-row[href$="<id>"]`; set `data-active` on it and clear from all others. Add to `cmd/serf-hub/jstest/test-sidebar-active.js`.

**Workspace header:**
- Hamburger renders inside `.workspace-header` (mobile only, hidden on desktop).
- Tasks + details actions consolidate into the overflow `⋯` menu on phone; stay inline on desktop.
- Meta row: drop `.rule-dot` separators, use `gap: var(--space-4)` mono cluster instead.
- Status pill → status badge.

**Slide-over focus management (drawer + sidebar mobile + modal):**
- New helper `cmd/serf-hub/assets/focus-trap.js` (~80 LOC) exposing `window.SerfFocusTrap.activate(el, returnFocusTo)` and `.deactivate()`. On activate: stores `document.activeElement` as restore target; adds `inert` to all root siblings of `el`; binds a Tab-key handler that cycles focus within `el`. On deactivate: restores focus, removes `inert`, unbinds handler.
- `renderer.js` calls `SerfFocusTrap.activate(panelEl, triggerEl)` when opening tasks/details panels. Calls `.deactivate()` on close.
- `sidebar.js` calls the same for mobile sidebar (`#sidebar` activated by the hamburger).
- `search.js` migrates from the existing implicit `<dialog>` focus to the explicit helper for consistency (search palette already uses native `<dialog>` so focus trap is already free — but tested for consistency).
- Adds `cmd/serf-hub/jstest/test-focus-trap.js` covering: open captures activeElement, Tab cycles, Esc restores focus to trigger.
- Per the design language: status dot pulse + indicator pulse animations get an explicit `prefers-reduced-motion` fallback that uses `border-left` or `outline` instead of opacity animation.

**Verify:** sidebar titles like "refactor auth middleware to use the new session token store" are fully readable. Project chevron is keyboard-accessible. Rail mode collapses cleanly. Mobile hamburger no longer overlaps partials. Tab from the search button into the search palette, around the results, and Esc — focus lands back on the search button. Same for tasks/details panels.

**Sequencing note:** Pass 4's `:focus-visible` rule references `.sb-row`, which doesn't exist until this pass. **Ship Pass 5 before Pass 4.** Pass 4's `:focus-visible` selector list updates to match the new class names.

### Pass 6 — Composer + conversation tightening

Goal: declutter the composer, tighten conversation rhythm.

- Conversation: padding `var(--space-7) var(--space-8)` → `var(--space-5) var(--space-6)`. Line-height on `.assistant-msg` `1.7 → --leading-snug (1.5)`. Gaps between turns `24px → var(--space-4)` (12px).
- Tool cluster: indent uses `--tool-indent: 36px` (desktop) / `20px` (phone, via container query).
- Diff body: `surface-inset` variant. `white-space: pre` + `overflow-x: auto` for monospace lines (current `pre-wrap` hurts diff readability).
- Composer: merge `.input-attachments` + `.composer-attachments` into one rail. Queue preview gets a `?` glyph for the kbd hint instead of inline text.
- Composer controls: single row, three zones (left/center/right). Send is `.btn-primary`; stop is `.btn-danger`; steer is `.btn-ghost`. **Three separate buttons** (no split).
- Status row at bottom: cwd · branch · ctx (with bar) · cost — all mono with `--text-dim` keys + `--text` values.

**Verify:** transcript reads tighter without losing breath. Composer at phone width keeps "Send" obvious + reachable. Status row stays single-line until it has to wrap.

### Pass 7 — Settings consolidation

Goal: every settings sub-page migrates to `settings-table` or `settings-collection`. Phone uses nav-as-page navigation.

- New CSS: `.settings-table`, `.settings-table .row`, `.settings-table dt`, `.settings-table dd`, `.settings-table .help`, value-cell variants (`.val-text`, `.val-input`, `.val-select`, `.val-radio-group`, `.val-radio`, `.val-toggle`).
- New CSS: `.settings-collection`, `.settings-collection-list`, `.settings-collection-row`, `.settings-collection-add`.
- Migrate each sub-page (mapping in design language §4.2 sub-page taxonomy).
- `LaunchConfigControls.render()` emits row markup. Reuses existing data attributes.
- Switch `<ul class="settings-list">` instances to `<dl class="settings-table">` (semantic upgrade).
- Phone CSS for settings: when `max-width: 767px`, settings-nav becomes the index page; tapping a link replaces the workspace innerHTML with the sub-page partial + a back chevron in the header. URL routing already supports `/settings/<section>`.
- Search filter at the top of `settings-nav` when ≥12 entries (it has 14 currently).

**Verify:** every sub-page renders without visible glitch in both themes. Theme/Notifications/Providers/Skills are the bellwethers. Confirm `LaunchConfigControls` still saves and validates. Phone settings navigation works.

### Pass 8 — Polish, motion, toast, skeleton, empty states

Goal: the last 10% — interaction polish.

- `#toast-region` added to `app.html`. New `assets/toast.js` exposes `window.SerfToast.show(message, kind, opts)`. JS calls added for: copy session ID (renderer.js), model change (renderer.js / search.js), session shutdown (search.js / workspace.js), settings saved (settings.js), credential set/cleared (credentials inline JS), htmx error (global hook).
- `.skeleton` utility CSS. Applied via `data-loading` attribute that htmx swap handlers set during the request.
- Empty states for: workspace (no session selected), conversation (no messages), sidebar (no projects), search palette (no results), tasks panel, settings sub-pages.
- Stagger animation on first sidebar render — JS adds `.stagger` class to the Live section on first paint.
- `:active` press states across all buttons (surface drops one elevation step).
- Remove inline `style="margin:0"` from `partials/settings/project.html` and `partials/settings/launch-serf.html`; use a utility class.
- Replace the noisy `.rule-dot` `·` separators with `gap` everywhere.

**Verify:** smoke-test every surface end-to-end. Cap context with `--motion-base` / `--motion-fast` everywhere. Confirm prefers-reduced-motion is respected throughout.

---

## Per-surface checklists

The migration spans many surfaces. Each pass touches multiple files; each file has its own concrete to-do list. These are the per-surface acceptance criteria.

### Workspace + composer

- [ ] Header uses `.btn-icon` for hamburger (mobile only) + `.btn-ghost` for tasks/details + overflow `⋯`.
- [ ] Status badge replaces status pill in `.workspace-meta`.
- [ ] Meta row uses `gap` not `.rule-dot`.
- [ ] Conversation max-width caps at 920px on wide screens via container query.
- [ ] Tool-call indent uses `--tool-indent` (36 desktop / 20 phone).
- [ ] Diff body uses `surface-inset` + horizontal scroll for long monospace lines.
- [ ] Composer merges attachment rails into one row.
- [ ] Send + Stop + Send-as-steer are three separate buttons.
- [ ] Composer status row is one mono line (cwd · branch · ctx · cost).
- [ ] User pill `max-width: min(62%, 540px)` so it doesn't get absurdly wide on wide screens.
- [ ] Optimistic-failed retry chip is touch-target sized.

### Sidebar

- [ ] Rows restructured to dot-col + text-col (title 2-line wrap + mono meta).
- [ ] Project headers compact mono with persistent ⚙ + ＋ (no hover-only opacity).
- [ ] Project chevron is a `<button>` (keyboard-accessible).
- [ ] Rail mode (56px) collapsible via `⌘B` and a toggle button; persisted to localStorage.
- [ ] Active session marker via `[data-active]` (left-border accent).
- [ ] First-paint stagger animation on Live section.
- [ ] Phone drawer behavior preserved.
- [ ] Container query for `< 80px` width hides text columns.

### Spawn (/new)

- [ ] Workspace header uses standard pattern.
- [ ] Prompt `--text-2xl` heading.
- [ ] Chips use `.btn-chip` variant (mono key + value).
- [ ] `.spawn-input` uses `--font-sans` (you're writing prose).
- [ ] Advanced section in `<details>` with mono summary.
- [ ] Recent prompts in mono `--text-sm` row list.
- [ ] Phone: chips wrap vertically, advanced section single-column groups.

### Settings shell + sub-pages

- [ ] Settings-nav has top search filter (≥12 entries).
- [ ] Phone: nav-as-page with back chevron in sub-page header.
- [ ] Every sub-page uses `settings-table` or `settings-collection`.
- [ ] In-repo config keeps its custom trust UI but adopts token + button styles.
- [ ] All sub-pages render in both themes without visual glitch.
- [ ] `LaunchConfigControls.render()` emits row markup.

### Search palette (`⌘K`)

- [ ] Result rows use generic row pattern + `.btn-icon` for actions.
- [ ] Keyboard nav has visible focus ring.
- [ ] Footer hint row uses mono kbd glyphs.
- [ ] Phone full-screen behavior preserved.

### Slide-overs (details, tasks)

- [ ] Right drawer 360px desktop; full-screen overlay phone.
- [ ] Sticky header with close button (phone).
- [ ] Single-at-a-time on desktop (JS closes the other when opening one).
- [ ] Focus trapped while open; returned to trigger on close.

### Credentials

- [ ] Migrates to `settings-collection` pattern.
- [ ] Provider rows show status badge + action buttons.
- [ ] Editor uses `surface-inset` variant.

### Inline elements

- [ ] Diagnostic cards `surface` with `border-left: 3px solid var(--diagnostic-accent)` — header includes a badge in the source color.
- [ ] Banners use `.banner` + variant.
- [ ] System lines (task transitions) use `.system-line` with subtle styling.
- [ ] Steering uses `.steering` with full-width divider + mono verb.

---

## Testing & verification

This is a CSS/markup pass. Verification is primarily visual + interactive.

### Functional checks (existing tests must pass)

- `make test` (Go) — no regressions.
- `make lint-naming` — passes (no new violations).
- Existing JS smoke tests (in `cmd/serf-hub/jstest/`) — pass.
- `serf-hub` builds clean.

### Visual verification

Pre-merge screenshot baseline against post-merge:

- Workspace at 1440 × 900 — dark + light.
- Workspace at 1280 × 720 — dark + light.
- Workspace at 1920 × 1080 with rail mode — dark.
- Phone at 390 × 844 — dark + light, both `compact` and `comfortable` density.
- Phone landscape at 844 × 390.
- Spawn form at all three sizes.
- Each settings sub-page at desktop + phone.
- Search palette open at each size.
- Details panel open at each size.

### Interactive checks (per-surface)

- Tab through every interactive control in the workspace — every focus state visible.
- Tab through sidebar — chevron is keyboard-accessible; rows have focus rings.
- Touch-target audit: in dev tools mobile mode, every button/link/chip reports ≥44px on phone.
- `prefers-reduced-motion: reduce` flag set — all animations cap at 1ms.
- `prefers-color-scheme: light` — light palette renders correctly.
- Theme override via `data-theme="light|dark"` — switches without flash.
- Phone density toggle — settings page updates body data-attribute; conversation, sidebar reflow accordingly.
- HTMX swaps continue to work — workspace pane swap on session click, settings sub-page swap, etc.

### Regression checks (the contracts)

- `pending.js` reconciliation still works (verified by spawning a session, sending a message, watching optimistic-pending → confirmed).
- `notifications.js` title-bar count + favicon updates respond to state changes.
- `sidebar.js` collapse-expand persists per project.
- `LaunchConfigControls.render()` produces a working form in `Serf launch` and `Project` settings.
- Search palette finds + jumps to results.
- Composer image attachment via paste + file picker + drag-drop works.

---

## Rollout

The 8 passes ship as a stack of git-spice'd PRs against `main`. Each PR is reviewable independently; later PRs depend on earlier ones for token availability but don't break if reverted.

Suggested PR titles, **in ship order** (not migration-order-section numbering):

1. `ui: add token foundation (spacing, type, motion, z, radius, fonts, CSP)` — Pass 1
2. `ui: migrate every font-size and font-family to tokens` — Pass 2
3. `ui: migrate spacing, radius, motion, z-index to tokens` — Pass 3
4. `ui: restructure sidebar rows; add rail mode, workspace header refactor, slide-over focus trap` — Pass 5 (ships before Pass 4 — see note)
5. `ui: unify buttons, add status badges, add focus rings` — Pass 4
6. `ui: tighten conversation; declutter composer` — Pass 6
7. `ui: consolidate settings sub-pages onto two primitives` — Pass 7
8. `ui: polish — toasts, skeletons, stagger, empty states, reduced motion` — Pass 8

Pass 1 is the only one that must ship before any other. Passes 2–3 unlock the rest. **Pass 5 (sidebar) ships before Pass 4 (buttons + focus rings)** because Pass 4's universal `:focus-visible` selector references `.sb-row`, which doesn't exist until Pass 5. Once Pass 5 ships, Passes 4, 6, 7 can ship in any order relative to each other; 8 is last.

A small `serf-hub` migration banner can be displayed during this work — e.g., a top-of-app `.banner` saying "UI refresh in progress; report issues via …" — but this is optional and the user can decide whether it's worth the noise.

---

## Risks & mitigations

| Risk | Mitigation |
| --- | --- |
| Font loading from Google Fonts is blocked by CSP | Update CSP to allow `fonts.googleapis.com` and `fonts.gstatic.com`; or vendor WOFF2 files into `assets/fonts/` with `@font-face` (documented as alternate path) |
| Light mode contrast bumps shift the look in ways users dislike | Light mode change ships as part of Pass 1 with `data-theme` override available; if regression complaints, revert just the light-mode token block |
| Sidebar 2-line row wrap eats too much vertical room | If average title is short, can be capped via a `data-line-clamp` body attribute defaulting to 2; users with short titles see 1-line behavior |
| `LaunchConfigControls.render()` row migration breaks the schema-driven form | Pass 7 includes side-by-side rendering and explicit regression checks; the JS API contract doesn't change, only the markup it produces |
| HTMX swap target IDs change accidentally | The swap-target IDs are listed in §Constraints; PR diff review checks for accidental ID/class removals |
| Migration leaves orphan tokens or dead rules | Pass 8 includes a CSS sweep to remove unused legacy aliases and orphan rules |
| Removing hover-only opacity affordances changes mouse user experience | Persistent affordances default to `--text-dim` (low-contrast on mouse) and `--text-muted` on row hover — visually similar to today for mouse, but visible at all times for touch |

---

## Open questions

- **Body text size in conversation** — 13 or 14? Decided during Pass 2 by running both in the live app for a day and picking the one that wins.
- **Fonts vendored vs CDN?** Default plan is Google Fonts CDN with preconnect. If we want offline operation or to satisfy a stricter CSP, we vendor WOFF2 files. Decision deferred to engineering during Pass 1.
- **Sidebar rail keyboard shortcut** — currently `⌘B`. Conflicts with browser bookmark sidebar in some contexts. If conflicts emerge, fall back to no keyboard shortcut (toggle button only).
- **Phone density default** — Compact. If telemetry shows users frequently switching to Comfortable, default flips in a future pass. (No telemetry collection added in this pass.)

## Known issues to address during implementation

Adversarial review surfaced these. They are real but not blocking the spec; resolution lives in the pass where the surface is touched.

**LaunchConfigControls form rendering (Pass 7).** The schema-driven launch form today emits `<fieldset>` groups with `<legend>` labels. Flat `settings-table` destroys this grouping. The implementer permits nested section headers in the table primitive (e.g., a row variant whose `<dd>` spans both columns and renders a fieldset header), OR declares Launch defaults a documented second one-off alongside in-repo trust. Decide during Pass 7 after re-reading `launchconfig.js`.

**Picker widgets inside settings-table (Pass 7).** `sp-model-btn`, `sp-dir-btn`, `sp-clear-btn` from `settings-pickers.js` live inside settings cells. Add a `picker` variant to the value-cell variant table, OR keep the picker classes as-is and defer their visual consolidation to a follow-up pass. Decide during Pass 7.

**`launchconfig.js` emits bespoke class names (Pass 7).** `spawn-advanced-row`, `settings-launch-row`, `spawn-advanced-radio`, etc. — about 50 lines of CSS targets these names. Pass 7 must rename the JS class output (and update jstest snapshots) OR layer the new `settings-table` styles on top of these names. The spec's "JS contract doesn't change" promise applies to the API, not the markup it emits.

**Optimistic-pulse + reduced motion (Pass 8).** With `prefers-reduced-motion`, `.optimistic-pending`'s pulse animation runs once at 1ms and the chip looks identical to a confirmed message. Add a non-motion fallback: `@media (prefers-reduced-motion) { .optimistic-pending { border-left: 2px dashed var(--accent); padding-left: var(--space-2); } }`. Same for `.status-dot[data-pulse]` — fall back to an outline or border.

**Tap-min vs phone density (Pass 5 / Pass 6).** Compact phone density wants ~26px tall composer buttons. `--tap-min: 44px` says no. Resolution: `--tap-min` on phone drops to 32px AND every composer control's hit-box extends via padding even when its visible chrome is smaller. The "visible chip + larger hit zone" pattern. Document in the design language §1.8 during Pass 5.

**Universal `:focus-visible` cascade (Pass 4).** Universal rule uses `:where(:focus-visible)` so it has zero specificity; per-component overrides (`.search-row:focus-visible { outline-offset: -2px }`) win.

**Sidebar tint contrast in light mode (Pass 5).** 5% mix is invisible against `#fafafa`. Bump to 10–12% for light theme specifically: `.sb-row[data-state="awaiting"] { background: color-mix(in srgb, var(--state-awaiting) 12%, transparent); }` and let dark inherit a lower percentage via theme-scoped override.

**Status-fidelity disparity (Pass 5 / Pass 6).** Sidebar row uses tint + border + dot; workspace header uses typographic badge only. Pick one fidelity per state moment and apply consistently. Recommendation: give the workspace status badge a subtle tinted background for `awaiting | errored` so it doesn't read as "less alarming" than the sidebar row.

**Hanken Grotesk at 11px on phone Compact (Pass 1 / Pass 2).** Hanken's character can dissolve below 12px on certain DPRs. Test on physical iOS + Android during Pass 1. If 11px loses character, raise Compact body to 12px and tighten leading/letter-spacing instead.

**Sidebar density toggle (post-Pass 5 follow-up).** Phone has Compact/Comfortable. Desktop power users with 30+ rows want the same. Add `body[data-sidebar-density="compact|comfortable"]` mirroring the phone setting, with the same Theme settings row.

**Spawn chip overflow (Pass 6 or follow-up).** When users override 8+ defaults, chips form a 3–4 row wall above the textarea. Cap visible chips at ~4 most-recently-modified + a "+N more" overflow chip.

**Missing toast triggers (Pass 8).** Add `connection-lost` and `connection-restored` (appwire socket events from `appwire.js`) and `attachment-failed` (composer-attachments.js error path). Pair `connection-lost` with a persistent banner since a transient toast won't be enough when the entire UI is stale.

**`--noise` alpha value (Pass 1).** Spec says 5% white opacity; mockups use 4%. Pick a value, propagate to both. Recommendation: 5% (the spec); the mockup undershoots.

**Workspace empty state (Pass 8).** Generic empty-state markup needs a per-surface variant for the workspace-empty case (no session selected) — first-time users need orientation: "spawn first session" / "open `⌘K` search" / brief explanation of what the hub is.

**Subagent row tint (Pass 5).** Subagent rows are indented + dot-colored but have no row-tint. Decide: tint them like other states (consistent), or leave them indented-only (current). Recommendation: leave them indented-only because subagent is structural, not a state condition.

**`:focus-visible` ring on rounded controls.** Use `outline-offset: -2px` on `.search-row`, `.sb-row`, and any other control where the +1px offset escapes the radius. Listed in §6.1 of design language; surface in Pass 4 review checklist.

---

## Success criteria

The pass is done when:

1. Every surface listed in §Per-surface checklists has its checklist completed.
2. Every interactive control has a `:focus-visible` ring.
3. Every interactive control on phone has `min-height ≥ var(--tap-min)` (44px).
4. Phone tested at 390 × 844 (portrait) and 844 × 390 (landscape) — no overflow, all controls reachable.
5. Light + dark renders correctly across all surfaces.
6. `prefers-reduced-motion` is respected throughout.
7. No `font-size`, `padding`, `margin`, `gap`, `border-radius`, `transition-duration`, `animation-duration`, or `z-index` literal in `style.css` outside of token definitions and 1px hairlines.
8. No hover-only opacity affordances remain.
9. Legacy token aliases (`--pad`, `--panel-2`, `--accent-2`, `--tool`, `--user`, `--error`, etc.) deleted after migration.
10. Inline `style="..."` attributes removed from `settings/project.html` and `settings/launch-serf.html`.
11. The audit reports zero `:focus-visible`-missing interactive elements.
12. `make test` and `make lint-naming` pass.

---

## Appendix: inventory snapshot (2026-05-22)

Captured before Pass 1; used for regression diffing.

**CSS:** 1037 lines, 0 `!important`, 0 three-class chains, 3 inline-style attributes. Color tokens: 17 (13 semantic + 4 diagnostic aliases). Layout/spacing tokens: 1 (`--pad`). Font-size literals: 14 distinct values, most-used 12px (51 selectors), 11px (49), 13px (24). Padding/margin/gap literals: 24+ distinct values, most-used 6px gap (26), 4px padding (24). Border-radius: 8 distinct, most-used 4px (29). `:focus-visible` rules: ~3.

**Templates:** 22 HTML files, 60+ CSS class families, 75+ `data-*` attributes, 8 htmx swap targets.

**Settings sub-pages:** 14 files, today using 5 distinct layout patterns (`.settings-list`, `.settings-form`, `.settings-launch-form`, `.settings-rows`, inline-JS custom). Target: 2 patterns (`.settings-table`, `.settings-collection`) + 1 documented one-off (in-repo trust).

**JS files:** 13 (excluding htmx + marked). Existing APIs preserved; one new file (`toast.js`); one new property on body (`data-phone-density`).
