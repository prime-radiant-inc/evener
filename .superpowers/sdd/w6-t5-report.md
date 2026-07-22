# Wave 6 — T5 (display application + rail host) report

**Status:** DONE_WITH_CONCERNS
**Branch:** `w6-display` (worktree `webui-w6-display`), off `835c17e2b`
**Commit range:** `255666700..3a77d5d51` (5 commits)
**Tests:** 196 files / 2935 passed (baseline 194/2898 → +2 files, +37 tests); tsc + Biome + build all exit 0, `dist/PLACEHOLDER` restored, tree clean.

## What shipped

Two deliverables from the T5 section: (a) the tokens.css font-size / phone-density
application gates, and (b) the `sidebarMode` consumer (the Wave-7 radio's real
consumer) — RailHost + railController + a `useSidebarMode` hook.

| Commit | Content |
|---|---|
| `255666700` | **tokens.css font/density gates** + `display-gates.test.ts`. Type ramp moved from `:root` to `<body>` and scaled through `--font-scale` (0.9/1.0/1.1/1.25 for s/m/l/xl); `body[data-phone-density="comfortable"]` opens vertical rhythm via `--density-scale` on `--line-height-body`, gated inside `@media (max-width:900px)`. |
| `35918e642` | **`useSidebarMode` hook** — resolves the pref + the 1200px desktop breakpoint into `{ mode, collapsed }`. pane=expanded, rail=collapsed, auto=responsive@1200px. SSR/jsdom-safe (defaults wide). |
| `341e35d3b` | **`railController` reveal seam (PIN-A producer)** — `revealSessionInRail(ref)` now dispatches to a RailHost-registered handler (was a T1 no-op stub); no-op-safe, last-registered-wins. |
| `bb752456d` | **Rail reveal** — `revealTarget`/`onRevealConsumed` props: un-collapses the target session's project (`projectNodeIdForSessionRef`, new pure railNodes helper) and scrolls its row (`data-session-ref` on RailRow) into view (`block:"center"`); waits for tree load, one-shot expand, consumes on success/give-up. |
| `3a77d5d51` | **RailHost build-out** — restores the locked `railSlot?: never` seam. pane/auto/rail resolution, the top-left ☰ chip → overlay drawer (Sheet), the needs-you attention badge on the chip, a global **⌘B** cycling rail→pane→auto (PIN-D; desktop only), reveal-first drawer-open coordination, drawer auto-close on navigation. Rail's own boolean collapse (`serf.rail.collapsed.v1`, sliver, `readCollapsed`/`persistCollapsed`) is **removed** — the rail mode subsumes it; Rail's "Hide sidebar" button is now an `onHide` callback RailHost wires to `setSidebarMode("rail")`. Mobile renders the plain rail (modes are desktop-only). |

## Cross-stream pin compliance

- **PIN-A** — `revealSessionInRail(ref: string): void` produced in `shell/rail/railController.ts` with the shape T3 codes against: expand the session's project section, scroll into view `block:"center"`, reveal-first (opens the overlay drawer) when collapsed, no-op-safe when no rail is mounted. T3's `/project` reviewer should read `railController.ts` (dispatch) + `RailHost.tsx` (drawer/reveal coordination) + `Rail.tsx` (expand+scroll effect).
- **PIN-D** — ⌘B is a separate `window` keydown listener in RailHost, disjoint from AppShell's ⌘K; ⌘K untouched. ⌘B gated to `!alt && !shift` + `preventDefault`, desktop-only.
- **Display-pref pins** consumed exactly: `data-font-size` (s/m/l/xl, default m), `data-phone-density` (compact/comfortable, default compact), `sidebarMode` (auto/pane/rail, default auto). `sidebarMode` semantics match the shipped Wave-7 help copy verbatim.
- **tokens.css quiet-window exclusive** — only T5 touched it.

## Design decisions worth review

1. **Type ramp lives on `<body>`, not `:root`.** A custom property's `var()` resolves against its declaring element, and the `data-font-size` attribute lands on `<body>`, so the ramp must be declared there for the scale to apply (the legacy did the same — `style.css:134`). No JS reads `--font-size-*` off `documentElement` (verified: only `focusscope/tabbable.ts` uses getComputedStyle, unrelated); token-contract color-parity is unaffected (font sizes aren't color tokens). One descriptive comment in `token-contract.test.ts` (~L451, not my manifest) still says non-color tokens live "only in the dark block" — now slightly inaccurate, not an enforced assertion.
2. **Density lever = line-height.** The plan scopes density to tokens.css and calls for a "row-spacing multiplier". Line-height is the one global row-rhythm lever reachable from tokens.css alone; `--density-scale` (1.0/1.25) is exposed as a reusable token for future per-component row-padding. Applied only ≤900px.
3. **Rail's boolean collapse removed, not kept alongside.** "The rail mode subsumes it" — keeping both would give two competing collapse mechanisms with two different collapsed presentations (sliver vs ☰-drawer). The old `serf.rail.collapsed.v1` key is now inert (no migration; a previously-collapsed sidebar reverts to `sidebarMode`'s default `auto`).

## Concerns / follow-ups

1. **Overlay drawer side.** The ☰ chip is top-left but the drawer opens from the right (`Sheet side="right"`). `Sheet` only offers `right`/`bottom`, and `widgets/sheet/**` is outside my manifest, so a left-anchored drawer needs a widget change. Behavior is correct; the slide direction is a design-review/widget follow-up.
2. **Phone-density copy mismatch.** The gate is `≤900px` (plan-directed, and consistent with `useIsMobile`'s 899px). The frozen Wave-7 help copy (`theme.tsx:92`) says "phone (≤767px)". Copy nit in a frozen file — flag for the close task.
3. **Visible-rendering proof is deferred to T6.** jsdom resolves neither `var()` nor `calc()`, so the font/density gates are pinned structurally off-disk (`display-gates.test.ts`) rather than by computed rendering. T6's live proof should confirm font-size + density visibly change and ⌘B cycles all three modes.
4. **Mobile `/project` reveal is best-effort.** RailHost (mobile) only mounts inside StackHost's `TreeDrawer` while it's open; a reveal with the drawer closed no-ops (RailHost can't open StackHost's drawer). Desktop reveal is fully wired (reveal-first opens the drawer). Sidebar modes are "Desktop only" by design, so this is consistent.
5. **Stale doc-comments in `prefs.ts`** (frozen, Wave-7): lines ~53/103-104/117 still cite Rail's now-removed `readCollapsed`/`persistCollapsed`/`serf.rail.collapsed.v1` as a best-effort-localStorage "precedent". Cosmetic doc-drift; `prefs.ts` is out of my manifest.

## Gates / nets

- Per-commit gate chain AND-chained with real exit codes: `tsc --noEmit` → `vitest run` (bare, count verified up) → Biome `ci` → `build` + restore placeholder. All five commits green.
- Mutation-verified nets (mutate → RED → revert): useSidebarMode auto threshold (5 fail), RailHost ⌘B cycle map (1 fail), tokens.css `--font-scale` value (1 fail), Rail reveal `block:"center"` (1 fail).
- jsdom stubs followed established precedents: matchMedia per-file stub (useIsMobile pattern, at 1200px), MemoryStorage, and a `scrollIntoView` assignment (jsdom defines none — `vi.spyOn` can't wrap it).

## Manifest

Touched only `src/styles/tokens.css` (+ new `src/styles/display-gates.test.ts`) and `shell/rail/**` (RailHost, Rail, railController, railNodes, RailRow, useSidebarMode, + their tests, Rail.module.css). No chokepoint edits — AppShell already mounts `<RailHost/>` at both sites (T1). No push, no merge.
