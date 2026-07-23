# Wave 8 T7 — adversarial task review

**Verdict:** APPROVED
**Range reviewed:** `e3b9c188c..20f9703f6` (branch `w8-polish`), 6 commits + report.
**Reviewer stance:** spec compliance AND code quality; every claim re-verified against code; all
gates re-run; both mutation nets re-executed live; PIN obligations enumerated.

## Gate summary (re-run by the reviewer, from `cmd/serf-hub/frontend`)

`npx tsc --noEmit` exit 0 → `npx vitest run` exit 0 (**225 files / 3233 tests, 0 failures** — matches
the report exactly) → `npm run lint` (biome ci, 627 files) exit 0 → `npm run build` exit 0 →
`git restore dist/PLACEHOLDER` → tree clean. All AND-chain gates green.

## P0 — PIN / obligation enumeration (met / overridden / unmet)

Every obligation naming T7 across the plan's Binding constraints, Locked cross-stream value pins,
Schedule-W8 triage, and PIN-D. **No UNMET obligation.**

| # | Obligation (source) | Verdict |
|---|---|---|
| 1 | Chokepoints controller-owned; never edit `index.html` (Binding) | MET — `index.html` untouched; theme-color one-liner correctly deferred + flagged |
| 2 | `reducer.ts` not edited (Binding) | MET — untouched |
| 3 | `tokens.css` untouched; PWA re-brand READS brand hex, hardcodes into manifest (Binding) | MET — manifest set to `#0e1116` (= tokens `--surface-0` dark); tokens.css untouched |
| 4 | Design-system binds: widgets-only, tokens-only CSS, color-is-attention, a11y names, sentence case (Binding) | MET — `dirListSetting.module.css` uses only tokens; StatusDot is a widget; count copy sentence-case; a11y names present |
| 5 | Wire-truth / failures via `useToasts` (Binding) | MET — busy handlers preserve existing try/catch→toast |
| 6 | Honest exit-code gates (Binding) | MET — re-verified all four |
| 7 | Auth: connection-error hint on `/rpc` WS 401 (pin: Auth exempt-path; manifest: `stores/connection.ts`) | MET — pre-exists in `auth.ts`+`ConnectionBanner.tsx` (predates base), with `ConnectionBanner.test.tsx` 401 coverage present at base. Plan's `stores/connection.ts` citation was a speculative edit-site guess; the real, complete home is elsewhere |
| 8 | PWA icon paths stay verbatim (auth-exempt) (pin) | MET — 4 icons untouched |
| 9 | PWA `background_color`/`theme_color` re-brand to tokens brand bg (pin) | MET — manifest `#0e1116` |
| 10 | `index.html` theme-color meta re-synced TOGETHER with manifest (pin) | OVERRIDDEN — dispatch delta makes `index.html` a standing off-limits chokepoint; correctly flagged as a controller one-liner (`content="#0a0a0e"`→`content="#0e1116"`). Manifest half done; meta half is a controller follow-up, not a T7 miss |
| 11 | Triage #9 dir-list "N entries" count header | MET — built, tested, mutation-proven |
| 12 | Triage #10 withBusy on Refresh + Installed row actions | MET — per-row-per-action keyed; no sibling over-disable; mutation-proven |
| 13 | Triage #11 Installed plugin status dot | MET — honest CadenceState mapping |
| 14 | Triage #12 `/settings/providers`→`/credentials` (T1's redirect) + T7 jsdom fold-in | MET — the redirect is T1's; T7's fold-in regression net built + mutation-proven |
| 15 | `?cwd=`-in-settings-shell cosmetic sub-item | ACCEPTED DIVERGENCE — fix needs a chokepoint (`Settings.tsx`/`AppShell.tsx`) edit; SPA single-shell is the intentional replacement; recorded for T8 ratification. Legitimate, consistent with the plan's §3 dockview-divergence posture |
| 16 | PIN-D: T7 owns `panes/settings/**` MINUS `fields.tsx` (+ dispatch delta: also minus `fields.test.tsx`/`fields.module.css`) | OVERRIDDEN + HONORED — all three T2 files untouched |
| 17 | Verify double-serve / token-injection §4.1/§4.2 (server-side) | DEFERRED to T8 per the plan's own text — correct |

The one obligation a factual-only pass could have mis-scored is #7 (the "ONE real frontend item").
It is genuinely satisfied, not waved away: `auth.ts`'s entire design targets the exact
indistinguishable-401 case the floor §5.3:813-819 names (a plain `fetch("/")` probe because the WS
handshake 401 is opaque to JS), and `ConnectionBanner.tsx` surfaces `SIGN_IN_PROMPT_MESSAGE` on
`closedReason==="auth"`. Commits `d9e577030`/`ec9948d44`/`551443c8f` are all ancestors of base
`e3b9c188c`; `ConnectionBanner.test.tsx`'s `describe('state "closed" - unauthenticated (401)')`
block exists at base. Real, tested, pre-existing.

## Named probes — one-line outcomes

- **P0 (pins):** No unmet obligation; table above. #10 index.html-meta is an OVERRIDDEN controller
  follow-up (correctly flagged), #15 `?cwd=` is an accepted architecture divergence for T8.
- **P1 (busy keying):** PASS — three separate `Set<string>` (toggle/autoUpgrade/upgrade) keyed by
  `plugin@marketplace`; two rows or two actions stay independent. Live mutation (`upgradeBusy.size > 0`)
  failed exactly the row-isolation net (1 fail / 21 pass), restored to zero diff. Convention matches
  the codebase (plain `disabled` on Button; ConfirmDialog keeps its own `busy` prop).
- **P2 (StatusDot honesty):** PASS — StatusDot's real label set is `Idle/Working/Needs you/Failed/
  Ended` over `CadenceState` (no "warning"); mapping is `broken→"failed"`, `!enabled→"ended"`, else
  `"idle"` — no invented state. "Ended" for a merely-disabled plugin is the floor §12e word itself,
  is neutral-family (no false attention), and is backstopped by the visible "disabled" chip. The
  priority test genuinely proves broken>disabled (its broken row is also `enabled:false`).
- **P3 (providers-redirect test):** PASS — composes the REAL `urlToPane` + REAL `Settings` render
  (asserts the real "Providers & credentials" heading + `aria-current="page"` nav, same labels as the
  pre-existing `AppShell.test.tsx`). Live mutation (disable the intercept) failed the net with the
  exact pre-fix defect (`section:"providers"` fall-through) at the real `urlToPane` call; restored to
  zero diff.
- **P4 (PWA re-brand):** PASS — manifest `background_color`/`theme_color` = `#0e1116` = tokens
  `--surface-0` dark (line 18, the first match the test's regex takes; light `#F5F7FA` is line 108).
  The regression test reads BOTH files off disk (`readFileSync`), no hardcoded copies. index.html:8 is
  currently `content="#0a0a0e"`; the report's flagged one-liner →`#0e1116` is correct and is a
  chokepoint the stream correctly did not touch.
- **P5 (dir count header):** PASS — copy is plain sentence-case ("2 entries"/"1 entry"/"0 entries");
  pluralization honest (`length===1?"entry":"entries"`, 0→"entries"); count is a sibling `<span>` of
  the heading (not folded into the accessible heading name); mirrors the live
  `MarketplacesSection.tsx:151` markup verbatim.
- **P6 (gates + manifest):** PASS — gates green (above). 11 changed files all inside `panes/settings/**`
  or the plan-named `assets/manifest.webmanifest`, except one new test in `src/styles/` (see Minor).
  `fields.tsx`/`fields.test.tsx`/`fields.module.css` untouched; no chokepoint, no `reducer.ts`, no
  `tokens.css`, no Go touched (verified by targeted `git diff --name-only`).
- **P7 (verified-left-as-is claims):** PASS — the `/rpc` 401 hint genuinely pre-exists with its own
  coverage (see P0 #7). The `?cwd=` settings-shell divergence is accurately characterized as the
  wave7 cosmetic ruling; the fix would require a chokepoint edit, so recording it as an
  architecture-driven divergence is honest.

## TDD RED + mutation credibility

RED evidence and mutation proofs in the report are credible against the diff and independently
reproduced for the two probed nets (P1 row-isolation, P3 redirect). Test-count arithmetic is
MEASURED-accurate: final 225/3233 confirmed by a full run; the two new files
(`providersRedirect.test.tsx`, `pwa-manifest-colors.test.ts`) are absent at base (+2 files); the +16
new tests (dir-list 5, Marketplaces 3, Installed 6, +2 new-file singletons) reconcile to 3217+16.
No test asserts mocked behavior — the redirect test drives the real router + pane, the manifest test
reads real files, busy/status tests drive the real components through the fake client.

## Findings

**Critical:** none.

**Important:** none.

**Minor:**
1. `src/styles/pwa-manifest-colors.test.ts` sits outside T7's declared `panes/settings/**` manifest.
   It follows the established `src/styles/` contract-test convention (co-located with
   `token-contract.test.ts`/`requireclass-contract.test.ts`, which read `tokens.css` off disk the
   same way) and collides with no sibling stream (none owns `src/styles/**`, and `tokens.css` itself
   is untouched). The report's boundary concerns flagged the `manifest.webmanifest` out-of-`frontend/`
   edit but did not call out this test-file placement — a disclosure gap, not a risk.
2. The PWA-colors regression test guards manifest↔`tokens.css` but not `index.html`'s theme-color
   meta↔`tokens.css`. Once the controller lands the flagged `index.html` one-liner, that meta tag has
   no drift guard. Inherent to `index.html` being a chokepoint the stream can't edit; a read-only
   assertion over `index.html` could have closed it. Non-blocking.

## Note to controller (not a T7 defect)

To fully satisfy pin #10 ("re-synced TOGETHER"), apply the flagged chokepoint one-liner
`cmd/serf-hub/frontend/index.html:8` `content="#0a0a0e"` → `content="#0e1116"`. Correct against the
current file and tokens. Until then manifest (`#0e1116`) and meta (`#0a0a0e`) are briefly out of sync
— cosmetic (both near-black), process-correctness only.
