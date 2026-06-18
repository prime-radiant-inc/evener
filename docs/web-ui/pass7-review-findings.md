# Pass 7 — Multi-persona UX review of the shipped web hub

Reviewed against `docs/web-ui/design-system.md`, the golden grammar
(`docs/web-ui/mockups/tokens.css` + `TARGETS.md`), and the mockup set. Base:
`origin/main` c0bb3dba (Passes 1–6 + Sidebar IA v2/v2.1 + multi-pane MVP).
Branch: `pass7-ux-review`.

Scope per the brief: conversation/transcript + chrome + visual-grammar surfaces.
The multi-pane code (`panes.js`, `.pane*`, open-beside) and the sidebar
disclosure/archive code (`sidebar.js`, sidebar template, `tree.go`) are owned by
other agents and were **not** touched. Findings in those areas are listed for
awareness only and explicitly DEFERRED.

Severity: **Critical** (broken/inaccessible), **Important** (real grammar drift,
worth doing), **Minor** (polish / debatable).

Each item is marked **IMPLEMENTED** or **DEFERRED** with a reason.

---

## Headline

The shipped transcript grammar is, surprisingly, faithful to the golden: the
thinking block, tool clusters, subagents module, plan block, diff palette,
liveness bands, steering demotion, system-churn coalescing, cold-start, the
new-content pill, and the agent-question/needs-you treatments all match their
mockups and carry the documented comments. Every transcript status color is
glyph-paired (colorblind-safe), and motion is disciplined (one `--pulse-cycle`
cadence + a `prefers-reduced-motion` kill switch).

The drift is concentrated in two places:
1. **The token foundation** — `style.css` keeps a parallel token namespace
   (`--text`/`--bg-raised`/`--rule`) instead of the golden (`--ink`/`--surface`/
   `--line`), still ships the **retired paper-grain `--noise`** and a **retired
   6-radius spread**, and uses `--text-dim` (the "non-text only" hairline ink)
   as readable text in ~37 places. These are all cross-cutting and several touch
   sidebar code, so they are DEFERRED for Jesse's call.
2. **A handful of small a11y gaps** in the conversation + chrome — all clear,
   self-contained wins. These are IMPLEMENTED.

Two claims surfaced during the review were **verified false** and dropped (see
"False alarms" at the bottom) — recording them so they don't get re-raised.

---

## Lens 1 — Scannability

| Sev | Finding | Location | Status |
|---|---|---|---|
| Minor | Conversation-leads hierarchy is correct: agent prose is the hero (`--text-md`, full contrast), user prompt demoted, tools quiet. No change needed. | `style.css:1692` (`.assistant-message`), `:1644` (`.user-message .pill`) | — |
| Minor | Tool clusters collapse-once-scrolled-past as specified; cluster summary leads with the consequential step. No change needed. | `style.css:1817`, `renderer.js` `endCheapCluster` | — |

No scannability regressions found.

## Lens 2 — Visual craft

| Sev | Finding | Location | Status |
|---|---|---|---|
| Important | **Retired paper-grain still ships.** `--noise` SVG is painted on `body::before` and a second `::after` on `.input-card/.diagnostic/.fork-dialog/.banner`. The design system explicitly retired "the imperceptible 5% paper-grain `--noise` texture." | `style.css:116`, `:288–345` | **DEFERRED** — whole-page aesthetic decision; reverses an earlier deliberate "workshop-log" choice. Jesse should confirm before removing. |
| Important | **Retired 6-radius spread.** The token set still defines `--radius-{sm,md,lg,xl,pill,full}` and all are in use; the design system calls for **two only** (`--r 5px` + pill). | `style.css:84–90`; usage spread across the file | **DEFERRED** — cross-cutting (~70 sites); needs a consolidation pass + visual review. |
| Important | **Parallel token namespace.** `style.css` uses `--text/--text-muted/--text-dim/--bg-raised/--rule` rather than the golden `--ink/--ink-2/--ink-3/--surface/--line`. Values mostly match, but the two systems can drift independently and `TARGETS.md`'s meta-finding ("tokens conflate the 4 meanings") traces to this. | `style.css:4–122` | **DEFERRED** — large rename; no behavior change but high churn. Jesse to decide if/when. |
| Minor | Multiple infinite animations exist (`think-breathe`, `status-dot-pulse`, `optimistic-pulse`, `skeleton-shimmer`) but they never co-occur and all share the `--pulse-cycle` cadence (except loading shimmer). Within the spirit of "one gentle breathe." | `style.css:1754,3422,3457,3622,3749` | — (no change) |

## Lens 3 — Accessibility

| Sev | Finding | Location | Status |
|---|---|---|---|
| Critical | **User-message copy/edit actions were clickable `<span>`s** (`onclick` only) — not keyboard-focusable or operable. | `renderer.js` `appendUserMessage` (the actions row) | **IMPLEMENTED** — converted to `<button type="button">`; CSS strips the global button chrome so they stay quiet inline text. (`bf6d4289`) |
| Important | **Icon-only chrome buttons lacked an accessible name.** The copy-session-id (⧉) and attach (＋) buttons had only `title=`, which SRs don't reliably expose as the name. | `templates/partials/workspace.html:20, :80` | **IMPLEMENTED** — added `aria-label` to both; Go test asserts. (`8fc00872`) |
| Important | **Disclosure buttons didn't expose `aria-expanded`.** The thinking block, tool-call clusters, and coalesced system runs are real `<button>`s but never told AT whether they were collapsed/expanded. | `renderer.js` thinking/cluster/system-run toggles | **IMPLEMENTED** — shared `bindDisclosureToggle` helper syncs `aria-expanded` with the target `.open` class at all three sites; jstest drives collapse/expand/recollapse. (`e073543f`) |
| Important | **`--text-dim` used as readable text in ~37 places.** `--text-dim` (#5a5a64 on #0a0a0e ≈ 3.4:1) is the documented "hairlines / non-text only" ink, yet it colors words — most visibly the entire short-tier thinking line + its preview. Below WCAG AA (4.5:1) for normal text. | `style.css` 37 sites; transcript-scoped: `:1773–1774` (`.think.think-tier-short` + `.pv`) | **DEFERRED** — fixing the short-think tier fights the intentional "duration-weighted recede" design (raising contrast un-demotes a quick thought); and most of the 37 sites are sidebar (guardrailed). `TARGETS.md` already flags "retire `--text-dim` as text" as a foundation task. Needs a deliberate contrast pass + Jesse's call on the think-tier tension. |
| Important | **Open-beside control is a `<span role="button" tabindex="0">`** rather than a `<button>`. It is keyboard-operable (Enter/Space handler) so it is functional, but a native button would be more robust. | `renderer.js` subagent-row open-beside builder | **DEFERRED** — guardrailed (multi-pane / open-beside is another agent's surface). |
| Minor | The non-interactive model chip (when `ChangeModel` is false) is a `<span class="btn btn-chip model-chip">` styled like a button. It is not focusable and conveys a static label, so it reads correctly to AT — acceptable, but a `<div>` would be cleaner semantics. | `templates/partials/workspace.html:87` | **DEFERRED** — cosmetic; touches the composer capability branch; low value. |

## Lens 4 — Calm / motion economy

| Sev | Finding | Location | Status |
|---|---|---|---|
| Minor | Motion is disciplined: one breathing cadence, soft cursor, transient enter/exit, and a global `@media (prefers-reduced-motion: reduce)` override that clamps duration/iteration. No blinking. | `style.css:233–239`, `:1754` | — (no change) |
| Minor | The streaming thinking teleprompter uses `direction: rtl` to keep newest words visible in a fixed slot — clever and reflow-free, matches mockup #5 alt A. | `style.css:1743–1748` | — |

No motion regressions found.

## Lens 5 — Edge-case legibility

| Sev | Finding | Location | Status |
|---|---|---|---|
| Minor | Diff lines carry the literal `+`/`-`/`@@` prefix as text (not color-only), with a desaturated palette distinct from error/success — exactly the documented dual-channel. | `renderer-tools.js:32–42`, `style.css:1903–1914` | — (no change) |
| Minor | Server-dropped vs client-collapsed output are honestly distinguished (amber dropped-note vs blue expand affordance). Binary output is detected and stated plainly. | `style.css:1931–1940`, `renderer-tools.js` `looksBinary` | — |
| Minor | Stale "running" subagents demote to a neutral `?` unknown state with an honest "never reported finishing — last seen Ns ago"; a child failure surfaces at the module level. | `style.css:1996`, `renderer.js` subagent refresh | — |

No edge-case regressions found.

## Lens 6 — Wayfinding

| Sev | Finding | Location | Status |
|---|---|---|---|
| Minor | Top bar is identity-only (title + copy + details overflow); the model chip lives in the composer controls (`controls-left`), not duplicated in the top bar — matches the controls-layout rule. | `templates/partials/workspace.html:16–27` (top), `:78–90` (composer) | — (no change) |
| Minor | New-content pill is attention-aware (plain `↓ N new` / amber `↓ ◆ needs you` / red `↓ ✕ error`), each glyph-paired; the needs-you dock keeps a blocking question reachable above the composer. | `style.css:1342–1374`, `renderer.js` pill/dock builders | — |

No wayfinding regressions found.

---

## What I IMPLEMENTED (all gate-green, TDD, small commits)

1. **`bf6d4289`** — user-message copy/edit actions → real `<button>`s
   (keyboard-reachable); CSS reset keeps them quiet inline text.
   Files: `renderer.js`, `style.css`, `jstest/test-renderer-user-actions-a11y.js`.
2. **`8fc00872`** — `aria-label` on the icon-only copy (⧉) and attach (＋)
   buttons. Files: `templates/partials/workspace.html`, `web_test.go`.
3. **`e073543f`** — `aria-expanded` on the thinking / tool-cluster / system-run
   disclosures via a shared `bindDisclosureToggle` helper.
   Files: `renderer.js`, `renderer-format.js`,
   `jstest/test-renderer-disclosure-aria.js`.

## What I DEFERRED for Jesse (with reasons)

- **Remove the retired paper-grain `--noise`** — whole-page aesthetic call that
  reverses an earlier deliberate choice.
- **Consolidate the radius scale to two** — cross-cutting (~70 sites); needs a
  visual review.
- **Migrate `style.css` to the golden token names** (`--ink`/`--surface`/
  `--line`) — large, high-churn rename; no behavior change.
- **Retire `--text-dim` as text / fix sub-AA contrast** — most sites are sidebar
  (guardrailed); the transcript site (short-think tier) is entangled with the
  intentional duration-weighted recede. Needs a deliberate contrast pass.
- **Open-beside `<span role=button>` → `<button>`** — guardrailed (multi-pane).
- **Non-interactive model chip `<span>` → `<div>`** — cosmetic, low value.

## False alarms (verified against the code, dropped — do not re-raise)

- "Model chip is duplicated in the top bar." **False** — the chip is only in the
  composer `controls-left` (`workspace.html:82–89`); the top bar
  (`:16–27`) has title + copy + details only.
- "Diff +/− lines rely on color alone." **False** — `renderDiffLines`
  (`renderer-tools.js:40`) sets `span.textContent = line`, which includes the
  literal `+`/`-`/`@@` prefix, so the sign is a real text channel.
