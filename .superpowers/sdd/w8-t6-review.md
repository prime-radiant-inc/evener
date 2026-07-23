# Wave 8 — T6 adversarial review

**Scope:** single-pane mode + `/thread/{ref}` + `openBeside`/`popOutPane` + read-only transcript pane.
**Worktree:** `webui-w8-singlepane` @ `ad93b0137` (base `e3b9c188c`).
**Reviewer verdict: FIX_ROUND** — the shipped code is correct, complete for scope, and fully gated;
the fix round is for one **unflagged latent integration hazard** (Important) plus report/recommendation
corrections. No code rewrite is required of the stream; the Important item needs a controller-level
chokepoint touch T6 structurally cannot make, so it must be surfaced + scheduled for T8.

## Gate summary (re-run by me, from `cmd/serf-hub/frontend`, real exit codes)

- `npx tsc --noEmit` → **exit 0**, clean.
- `npx vitest run` (bare) → **exit 0**, **226 files / 3240 tests, all pass** — matches the report exactly.
  - Baseline 223/3217 → 226/3240 = **+3 files / +23 tests**, fully reconciled:
    +13 hand-authored new-file (Transcript.test.tsx 5, transcript/index.test.ts 3, chromeStrip.test.ts 5)
    + 5 in paneActions.test.ts (2→7) + **1** auto (requireclass-contract for `Transcript.tsx`'s css import)
    + **4** auto (token-contract, 2 generated tests × the 2 new css files) = 23. Honest.
- `npm run lint` (Biome ci) → **exit 0**, 632 files, no fixes.
- `npm run build` → **exit 0**; `dist/PLACEHOLDER` restored; **tree clean**. Single-pane rules confirmed
  in the built bundle (`[data-single-pane] .dv-tabs-and-actions-container,[data-single-pane] button[aria-label=Sessions]{display:none}`).
- **No Go changes. No chokepoint edits** (AppShell/DockHost/paneRegistry/reducer/tokens.css/index.html/routing/Session/widgets-barrel all untouched — verified by name-status).
- Module-pair clean: `shell/singlePane.ts` (file) + `shell/singlePane/{global.css,chromeStrip.test.ts}` (dir),
  **no `shell/singlePane/index.ts`** → no ambiguous module resolution (file wins over dir; dispatch delta satisfied).

## Named probes (one line each)

- **P1 popout:** Inert today — **zero `popOutPane` callers** (only comment refs). Hazard real: `addPopoutGroup`
  defaults to same-origin `/popout.html` (`dockview-core.js:16060`), which serf-hub does not serve → SPA
  fallback boots a second app. **A frontend-only override IS supported** (`addPopoutGroup(item, {popoutUrl})`
  `component.api.d.ts:580`; global `popoutUrl` `options.d.ts:192`; resolved `options?.popoutUrl ?? this.options?.popoutUrl ?? '/popout.html'`)
  → recommend the frontend override (about:blank or a same-origin shell), **not** a Go route (escalation-only).
- **P2(a):** `getDockviewApi` is minimal (3 LOC + comment), genuinely needed by both actions, and **collision-free**
  — catalog/transcript/doc sibling worktrees do **not** touch `workspace.ts` (verified). Out-of-manifest but justified.
- **P2(b):** w8-doc (T5 @ `ee5d09823`) **does NOT modify `openDoc.test.ts`** → **no merge conflict**; T6's +3
  `.mockImplementation(()=>{})` applies cleanly on top of T5 and passes against T5's real `openDoc.ts` (its
  `import "./index"` registers "doc"; the stub prevents the real `openBeside` from executing). Nothing to reconcile on that file.
- **P3 read-only pane:** Genuinely read-only — `useTranscript` exposes only `model`+`loadOlder` (no send/steer);
  the escalation Approve/Deny card is **architecturally not a registered item renderer** ("structurally can't"
  per its own header) so TurnBlock can't render it. Net **mutation-verified** (injected composer → test fails).
  **BUT** the pane shares the doc pane's unregistered-pane-restore **whole-layout-discard** exposure (see Important #1).
- **P4 chrome-strip:** Solid. `.dv-tabs-and-actions-container` is a **real** dockview class (151 refs in
  `dockview.css`); tokens-only (`display:none`, zero color literals); `aria-label="Sessions"` is the real hook
  (`TreeDrawer.tsx:52` `<IconButton label="Sessions">`) and test-locked. Net **mutation-verified** — both
  unscoping and outright rule-deletion bite; rules confirmed in the built bundle.
- **P5 `/thread/{ref}`:** Wired in `routing.ts` (T1, untouched) → session pane; marker set on entry and
  **cleared on nav-away** reactively via `singlePane = isSinglePaneRoute(pathname)` (`AppShell.tsx:194,234`,
  untouched by T6); `paneToURL` returns `null` for transcript. Mobile hide coherent but CSS-rule-level only (Minor #6).
- **P6 gates:** All green, counts exact + reconciled, tree clean, no Go/chokepoint edits (above).

## Findings

### Important

1. **Transcript pane inherits the doc pane's "unregistered pane in a restored layout discards the WHOLE
   layout" exposure — unflagged.** `"transcript"` is registered only lazily, via `paneActions.ts`'s
   `import "../panes/transcript"`. **AppShell eagerly registers only `welcome/session/settings/spawn`**
   (`AppShell.tsx:23-26`); `paneActions.ts` is imported only by producers, so **at shell boot "transcript"
   is unregistered**. DockHost persists the dockview layout (`DockHost.tsx:128-133`) and restores it at boot;
   a restored panel whose type isn't registered throws in `readPanelParams` → `isRegistered` → `paneFor`
   (`workspace.ts:123-129`), caught by `restoreLayout` → `dockviewApi.clear()` + `panes:[]`
   (`workspace.ts:208-230`) — the user loses their **entire** workspace, not just the transcript panel.
   **Latent today** (no live producer opens a transcript pane — `subagentModule.openTranscript` still opens
   a `"session"` pane, and no `openBeside({type:"transcript"})` caller exists), so it is not currently
   triggerable; it **activates** the moment a producer is wired (the pane's whole purpose). T6 **cannot** fix
   this within its manifest (it needs an AppShell chokepoint or a `workspace.ts` restore-semantics change),
   but the report should have flagged it exactly as it flagged the popout gap.
   **Fix-round shape (tradeoffs):**
   - *Preferred — eager registration.* Add `import "../panes/transcript"` (+ `"../panes/doc"`) to AppShell's
     boot imports, alongside the existing four. A controller/T1 chokepoint one-line-append, consistent with
     the established pattern; the heavy components stay `lazy()` so boot cost is negligible; closes doc AND
     transcript in one move.
   - *Alternative — prune only unregistered panels* in `restoreLayout`. More resilient to any future /
     version-skew pane type, but heavier, changes controller-owned `workspace.ts` semantics (the current
     whole-discard is a deliberate version-skew choice, `workspace.ts:118-122`), and silently drops the
     panel with no user signal.

### Minor

2. **P1 recommendation mis-prioritized.** The report leads with "a Go-served `/popout.html` (recommended)";
   dockview supports a **frontend-only `popoutUrl` override** (cited above), which per the resolution
   preference order should lead. The Go route is the escalation-only fallback, not the default. (Code is
   inert with no caller, so this is a recommendation correction, not a code defect.)

3. **`openDoc.test.ts` reconciliation framing is inaccurate.** The report says "T5 should preserve that
   isolation when it rewrites this file." T5 (@ `ee5d09823`) **does not rewrite `openDoc.test.ts`** — there is
   no conflict and nothing for T5 to preserve. T6's change applies cleanly and passes against T5's real
   `openDoc.ts`. Harmless, but the stated merge concern does not exist.

4. **`workspace.ts` `getDockviewApi` is outside T6's stated manifest** (`shell/singlePane/** + panes/transcript/**
   + shell/paneActions.ts`). It is genuinely needed, minimal (3 LOC), not a chokepoint, and collision-free
   (verified no sibling worktree touches `workspace.ts`). Acceptable expansion; recorded for the serial-merge log.

5. **RED-first TDD not independently evidenced.** Each deliverable commit bundles impl + tests
   (`20ce27a20`, `9ca346e1f`, `c99865711`); neither the report nor history shows a failing-test-first step.
   The *outcome* is sound — I mutation-verified the chrome-strip (unscope + delete) and read-only (injected
   composer) nets — but the RED-first claim itself is not verifiable from the artifacts.

6. **Mobile chrome-strip hide is verified only at the CSS-rule level.** `chromeStrip.test.ts` reads
   `global.css` off disk (jsdom leaves CSS unprocessed — documented); no test mounts StackHost on a `/thread`
   route to assert `button[aria-label="Sessions"]` is actually hidden. The selector matches the real hook, but
   the end-to-end hide is unproven until the T8 live mobile proof.

## Conscious divergences reviewed and accepted

Residual `.content` gutter (chokepoint-hashed class, out of reach); read-only pane omits the live
flow-overlay; §3 max-3-pane cap + auto-open-observer not ported (dockview manages space) — all consistent
with the plan's §Ambiguities #4 and the T6 scope note, and correctly recorded for the T8 sweep.
