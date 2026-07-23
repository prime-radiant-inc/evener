# Wave 8 — T6 report: single-pane mode + read-only transcript pane + open-beside/popout

**Status:** DONE_WITH_CONCERNS
**Branch:** `w8-singlepane` (base `e3b9c188c`)
**Commit range:** `20ce27a20..0bcb816bf` (4 commits)
**Tests:** baseline 223 files / 3217 tests → **226 files / 3240 tests, all passing**; tsc clean, Biome ci clean, `npm run build` clean (dist/PLACEHOLDER restored, tree clean). Every commit AND-chain-gated (tsc → vitest bare → lint → build).

## What was built (per deliverable)

1. **`openBeside` / `popOutPane` seam bodies** (`shell/paneActions.ts`, commit `20ce27a20`).
   - `openBeside` routes through `workspaceStore.openPane(type, params, {beside})`. Split-beside = the currently-focused pane; dedup is the store's own same-type+deep-equal-params focus (floor §3.2), so re-opening a pane beside just focuses it. **Mobile degrade** keys off `getDockviewApi() === null` (StackHost registers no api) → plain open, no split hint (the one pane becomes the focused full-screen screen — the "navigate" equivalent, since doc/transcript panes have no URL).
   - `popOutPane` → `api.addPopoutGroup(panel)`; no-op when no host (mobile) or unknown/closed pane id.
   - 7 tests; the openBeside split/dedup/mobile and popOutPane delegate/no-op paths are all covered.

2. **Read-only transcript pane** (`panes/transcript/**`, commit `9ca346e1f`). Self-registers the `"transcript"` type; renders another thread's transcript via the **M4 engine reused read-only** (`useTranscript` + `TurnBlock` + the item/tool renderer barrels), **no composer / no pending chips / no session chrome**. Hydrates through the same refcounted threads store (`thread/read`, no new data path); title falls back to the raw ref. 8 tests (loading / empty / renders-turns / read-only-net / title fallback + registration/title), read-only net mutation-verified.

3. **Single-pane chrome-strip** (`shell/singlePane/global.css` + `singlePane.ts` import, commit `c99865711`). Keyed off the T1 `[data-single-pane]` marker: hides dockview's tab strip (desktop) and the mobile tree-drawer trigger (`button[aria-label="Sessions"]` — the sidebar/search/settings entry point, floor §2.3). A **global** sheet (named `global.css` so the naming contract accepts it) since it targets a library class + an aria-label a hashed module class can't reach — same rationale as `dockview-theme.css`. Wired via a side-effect import in `singlePane.ts` (eagerly reachable from AppShell); confirmed present in the built bundle. **AppShell.tsx / DockHost.tsx untouched.** 5 tests (read off disk; the "every rule scoped under the marker" net is mutation-verified).

## Concerns / flags for the controller

- **`popOutPane` needs a server-served `/popout.html` shell (functional gap for the "verify" step).** Dockview's `addPopoutGroup` `window.open()`s a same-origin `popoutUrl` (default `/popout.html`) and moves the group DOM into it. serf-hub serves **no** `/popout.html`; its SPA fallback would boot a **second full app instance** in the popout window. The frontend enablement is complete and unit-tested, but reliable native popout needs either a minimal same-origin popout shell served by Go (the MW-* precedent — recommended) or a `popoutUrl: "about:blank"` override (cross-browser quirks). Left on dockview's default + documented in-code; **no caller of popOutPane exists yet**, and T8's "pop out a pane" live proof is where this must be closed.
- **Two edits outside the strict T6 manifest** (`shell/singlePane/** + panes/transcript/** + shell/paneActions.ts`), both merge-safe:
  - **`shell/workspace.ts`** (+13): added a `getDockviewApi()` read accessor (the api holder) — `popOutPane` and `openBeside`'s split-detection genuinely need the live api, and workspace.ts privately holds it with no getter. Not a chokepoint; not in any concurrent stream's manifest (verified) → no collision.
  - **`panes/doc/openDoc.test.ts`** (T5's manifest, +3 lines): the T1-shipped delegation test spied on `openBeside` **calling through**, which threw once `openBeside` became real (unregistered "doc" fixture). Added `.mockImplementation(() => {})` to isolate the delegation contract (correct unit hygiene). **T5 should preserve that isolation when it rewrites this file for the real doc pane.**
- **`shell/singlePane.ts`** got one side-effect import line (the CSS anchor). Read as within T6's seam per the dispatch NAMING CONSTRAINT note; the dispatch STOP condition named only AppShell.tsx / DockHost.tsx, both untouched.

## Conscious divergences (for the T8 sweep)

- **Residual content gutter** — full edge-to-edge isn't achieved; AppShell's `.content` padding is a CSS-Module-hashed chokepoint class this stream can't target. The visible single-pane effect (no tab strip / rail / drawer) is delivered; the gutter is cosmetic.
- **Read-only pane omits the live flow-overlay** (new-content pill / scroll-follow) — a live-session affordance; the viewer opens at the latest turn once, then leaves scrolling to the user. Couples only to the engine's stable seams (useTranscript / TurnBlock / LoadOlderRow), deliberately avoiding the T3-volatile scroll internals.
- **§3 dockview-model divergences** (per plan): the **max-3-pane cap** (floor §3.2) and **auto-open-observer** behavior (floor §3.7) are **not ported** — dockview manages space; openBeside enforces no cap. The iframe/postMessage/localStorage mechanism (§3.1/3.3/3.5/3.6) is not ported (dockview-native, same-document — the origin/`isPaneSafeHref` guards are moot with no iframes).
- **Floor §2 (thread single-pane):** chrome suppression = built (rail by T1, tab strip/drawer here). **Fallback-title quirk (cross-cutting #2 / §Ambiguities #3):** honored for free — both the session pane (`model.name || ref`) and this transcript pane keep the raw-ref fallback, and the SPA has no title-blanking poll. **Composer-stays-live §2.5:** T1 routed `/thread/{ref}` to the SESSION pane; not this stream's build. The legacy ThreadDocumentMode **location-telemetry / subagent-parent-banner suppression** (§2.5) is a T4-chrome concern with no stable selector yet — recorded as a divergence (the new single-pane shows the full session pane), not built here.
- **Mobile chrome-strip couples to the `aria-label="Sessions"` accessible name** (the only stable hook on the hashed StackHost top bar). Fragile if TreeDrawer renames the trigger; the chromeStrip test pins the selector.

## Cross-stream notes

- **PIN-A:** `openBeside`/`popOutPane` bodies are landed for T3/T5 reviewers. Transcript registration is guaranteed by a `paneActions.ts` side-effect import of `../panes/transcript` (producers call bare `openBeside({type:"transcript"})` per PIN-A, with no dedicated opener the way doc has `openDocBeside`).
- **PIN-E:** no new global keydown listener added (open-beside/popout are imperative, not chords).
- `shell/singlePane.ts` file + `shell/singlePane/**` dir coexist; no `shell/singlePane/index.ts` created (the `./singlePane` import stays the file).
