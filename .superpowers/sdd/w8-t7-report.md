# Wave 8 T7 — settings polish + PWA + auth periphery — report

**Status:** DONE_WITH_CONCERNS
**Branch:** `w8-polish`  **Commit range:** `e3b9c188c..535edf78f` (6 commits, off base `e3b9c188c`)
**Gates (final tree):** tsc 0 · vitest 0 (**225 files / 3233 tests**, baseline 223/3217 → +2 files, +16 tests) · biome ci 0 · `npm run build` 0 (`dist/PLACEHOLDER` restored, tree clean).

## Commits

1. `981cc277c` dir-list N-entries count header (`DirListSetting`)
2. `c479652dd` withBusy on Marketplaces Refresh
3. `244e52ec4` withBusy on Installed row actions (Enable/Disable, Auto-upgrade, Upgrade)
4. `449bfa0ad` Installed plugin status dot
5. `0722ad178` jsdom regression net for the `/settings/providers` redirect (FOLD-IN item)
6. `535edf78f` re-brand PWA manifest colors to tokens.css

## Triage items covered

| # | Item | Outcome |
|---|---|---|
| 9 | dir-list "N entries" count header | Built. Commit 1. |
| 10 | `withBusy` on Marketplaces Refresh + Installed Enable/Disable/Auto-upgrade/Upgrade | Built. Commits 2-3. |
| 11 | Installed plugin status dot | Built. Commit 4. |
| 12 sub-item | per-project `?cwd=` page inside settings-nav shell (cosmetic) | Verified, recorded as accepted divergence — no code change (see below). |
| FOLD-IN | jsdom regression test for `/settings/providers` redirect | Built. Commit 5. |
| PWA §4 | re-brand `background_color`/`theme_color` + `index.html` `theme-color` | manifest.webmanifest done (commit 6); `index.html` half is chokepoint-blocked — see Concerns. |
| PWA §4.4 | keep the 4 icon files at their exact paths | Verified unchanged — no edit made, none needed. |
| Auth §5.3/§5.7 | connection-error hint on `/rpc` WS 401 | Verified ALREADY SHIPPED (prior wave) — no code change. |

## RED evidence + mutation proofs (per behavioral regression net)

Every net below was: written RED → run to confirm failure → implemented → run to confirm GREEN →
mutated (defect reintroduced, either in the new code or, for the FOLD-IN net, in the pre-existing
code it locks) → run to confirm the mutation bites → restored → run to confirm GREEN again with a
clean `git diff` (zero net diff from the mutation exercise itself).

1. **Dir-list count header** (`dirListSetting.test.tsx`). RED: 3 of 5 new tests failed
   (`"2 entries"`/`"1 entry"`/`"0 entries"` not found; the 2 "does not show a count" tests passed
   trivially since no count existed at all). Mutation: `showCount = false` → same 3 tests fail
   identically to the original RED. Restored, zero diff, 22/22 green.
2. **Marketplaces Refresh withBusy** (`MarketplacesSection.test.tsx`). RED: 2 of 3 new tests failed
   (own-button-disables, row-isolation; the "re-enables after failure" test passed trivially).
   Mutation: hardcoded `disabled={false}` on the Refresh button → same 2 tests fail. Restored, zero
   diff, 16/16 green.
3. **Installed row-action withBusy** (`InstalledSection.test.tsx`). RED: 4 of 5 new tests failed
   (Disable/Auto-upgrade/Upgrade own-button-disables + cross-row isolation; the 1 "re-enables after
   failure" test passed trivially). Mutation: `runBusy` reduced to a passthrough (`await fn()`, no
   Set mutation) → same 4 tests fail. Restored, zero diff, 21/21 green.
4. **Installed status dot** (`InstalledSection.test.tsx`). RED: new test failed (`getAllByRole("img",
   {name:"Failed"})` etc. found nothing — no `StatusDot` rendered at all). Mutation: `pluginStatus`
   hardcoded to always return `"idle"` → same test fails (no "Failed"/"Ended" dots found). Restored,
   zero diff, 22/22 green.
5. **`/settings/providers` redirect regression net** (`providersRedirect.test.tsx`, the FOLD-IN item).
   This locks EXISTING behavior (T1 already shipped the redirect) rather than new behavior, so the
   test is GREEN on first run, not RED — confirmed via a plain run before any mutation. Mutation:
   temporarily disabled routing.ts's `/settings/providers` intercept locally (`if (false && ...)`,
   never committed) → the test failed, and the failure reproduced the EXACT pre-fix defect
   (`urlToPane` fell through to the generic `/settings/{section}` regex, yielding
   `{section:"providers"}` — an unregistered section id, which is what wave7-report.md's GAP #1
   described as "falls through to PlaceholderSection"). Restored `routing.ts` from a pre-edit backup;
   `git diff` on that file is empty (chokepoint untouched, confirmed by `git status`/`git diff`
   showing zero changes to `shell/routing.ts` in the final tree).
6. **PWA manifest colors** (`pwa-manifest-colors.test.ts`). RED: `expected '#0a0a0e' to be '#0e1116'`
   (reads both `tokens.css` and `manifest.webmanifest` off disk, no hardcoded literal on either
   side). Implemented: changed the 2 manifest fields. Mutation: reverted the manifest values back to
   `#0a0a0e` → test fails identically to the original RED. Restored, zero diff, 1/1 green.

## Wire / source truth receipts

- **`dirListSetting.tsx` count header**: mirrors the EXACT pattern already live in
  `MarketplacesSection.tsx`/`InstalledSection.tsx` (`{n} {n===1?"entry":"entries"}`), which
  `parity-m7-settings.md` §12b/§12e already require and wave7-report.md's GAP #2 flags as the
  asymmetry (§13/§14 have none). Not invented — copied from the sibling sections' own already-shipped
  markup.
- **`withBusy` semantics**: `parity-m7-settings.md` §12f: "`withBusy(btn, fn)` disables the
  triggering button... re-enabling in a finally" — singular "the triggering button", confirmed by
  reading the floor text before choosing a per-row-per-action `Set<string>` keying scheme over a
  single shared boolean (which would have over-disabled sibling buttons).
- **Status dot mapping**: `parity-m7-settings.md` §12e: "Status dot: `warning` if `broken`, else
  `ended` if `!enabled`, else `idle`". Read `widgets/statusdot/index.tsx` and `widgets/cadence/
  index.tsx` first — `CadenceState` is exactly `"idle"|"working"|"needs-you"|"failed"|"ended"`, no
  `"warning"` value exists in the shared widget. Mapped `broken` → `"failed"` (the only failure-family
  state) rather than inventing a state the widget doesn't support; documented as a conscious
  translation in both the code comment and this report, not silently done.
- **`/settings/providers` redirect**: read `shell/routing.ts`'s actual landed `urlToPane` (T1's
  commit) before writing the test — did not invent or guess its shape. Confirmed the redirect target
  is `{type:"settings", params:{section:"credentials"}}`, matching the existing `routing.test.ts`
  pure-function test and T1's own w8-t1-report.md.
- **PWA manifest colors**: read the actual `cmd/serf-hub/assets/manifest.webmanifest` (not the
  frontend tree — confirmed via `find` that no such file exists under `frontend/`; this is the
  Go-embedded asset `web.go:263-267` serves) and the actual `--surface-0` value in `tokens.css`
  (`#0E1116`, "app background — deep neutral ink", dark/default theme block) before changing
  anything. The regression test parses both files live rather than hardcoding either value, so it
  tracks `tokens.css` automatically if the brand background changes again.
- **Auth connection-error hint**: read `src/auth.ts` (`checkAuthStatus`, `SIGN_IN_PROMPT_MESSAGE`) and
  `src/shell/ConnectionBanner.tsx` in full, plus their git history
  (`d9e577030`/`ec9948d44`/`551443c8f`, all well before this wave's base) and existing test coverage
  (`ConnectionBanner.test.tsx`'s `describe('state "closed" - unauthenticated (401)')` block). This is
  the exact floor §5.3/§5.7 behavior already fully built, wired into `AppShell.tsx`, and tested — not
  something this task needed to add.

## Conscious divergences / verify-only items (no code change)

1. **Per-project `?cwd=` page renders inside the settings-nav shell (cosmetic, folded with triage
   #12).** Read `panes/settings/sections/project.tsx` in full. Its own header comment already records
   a prior, deliberate scope decision: `cwd` is read directly from `window.location.search` (not
   threaded through the pane-params system) specifically because extending that shared contract "is
   out of this stream's manifest." The remaining gap — rendering inside the unified settings-nav shell
   rather than the legacy's standalone no-nav page — is a byproduct of this wave's own single-shell
   SPA architecture (every pane renders inside the same `AppShell`); "fixing" it to match the legacy's
   standalone layout would mean suppressing the settings-nav specifically for this one section, which
   requires editing `Settings.tsx` and/or `AppShell.tsx` — both STANDING OFF-LIMITS chokepoints. Given
   it's explicitly tagged "cosmetic" in wave7-report.md's own GAP list, and given the unified-shell
   design is the intentional replacement for the legacy's page-per-feature model, I recorded this as
   an accepted, architecture-driven divergence rather than building anything. No file touched.
2. **Auth connection-error hint.** Already fully shipped by a prior wave (`src/auth.ts` +
   `shell/ConnectionBanner.tsx`, commits `d9e577030`/`ec9948d44`/`551443c8f`, predating this wave's
   base `e3b9c188c`). Verified via source + git log + existing tests; no code change needed or made.
3. **4 PWA icon files stay at their exact paths.** Verified `cmd/serf-hub/assets/icon*.{png,svg}`
   unchanged (not present in any diff) — the auth-exempt path list
   (`hubedge/auth_token.go:109-117`) is unaffected.
4. **Double-served-manifest / token-injection behaviors (floor §4.1/§4.2).** Server-side, unchanged,
   and the plan's own text assigns their verification to T8's close sweep, not T7 — not re-verified
   here beyond reading `web.go`'s cited line ranges during PWA-color research.

## Concerns

1. **`index.html`'s `theme-color` meta is NOT re-synced — chokepoint-blocked, needs a one-line
   controller edit.** The T7 task text says "re-brand `background_color`/`theme_color` + the
   `index.html` `theme-color` meta... (READ, together)", and T1 left the exact hook for this
   (`index.html:8`, `<meta name="theme-color" content="#0a0a0e" />`, with a comment reading "wave-8
   T7 re-syncs both to the new brand background"). But this dispatch's ADDITIONAL DISPATCH
   REQUIREMENTS block lists `index.html` as a STANDING OFF-LIMITS chokepoint with an unconditional
   instruction ("If a task truly requires a chokepoint edit, STOP and return NEEDS_CONTEXT naming the
   exact file and the one-line change you need") that postdates and, being explicitly "binding",
   takes precedence over the plan's own task text where they conflict. I did not edit `index.html`.
   **Exact one-line change needed:** `cmd/serf-hub/frontend/index.html:8` —
   `content="#0a0a0e"` → `content="#0e1116"` (the same value `manifest.webmanifest` now carries,
   commit `535edf78f`). Until this lands, the manifest and the meta tag are briefly out of sync with
   each other (manifest correct, meta tag still the old value) — a real but minor, purely cosmetic gap
   (both colors are near-black; the practical difference is in token-fidelity/process correctness, not
   visible contrast).
2. **`cmd/serf-hub/assets/manifest.webmanifest` and the icon files live outside `cmd/serf-hub/
   frontend/` (the dispatch's stated "Work ONLY here" boundary).** The T7 file manifest in the plan
   explicitly names `assets/manifest.webmanifest` + "the 4 PWA icon assets", which only resolve under
   `cmd/serf-hub/assets/` (no such file exists anywhere under `frontend/`). I treated the plan's
   explicit, unambiguous file citation as authoritative for this one file and edited it directly
   (2-line color value change only); I did not touch the 4 icon files (no edit was called for — "keep
   them at their exact paths" is a don't-rename instruction, not a content change). Flagging this
   interpretation explicitly in case the controller reads "Work ONLY here" more strictly than I did.
3. **`fields.tsx`/`fields.test.tsx`/`fields.module.css` (T2's OWNERSHIP OVERRIDE files), all SIBLING
   STREAMS paths, and all other STANDING OFF-LIMITS chokepoints are untouched** — confirmed via
   `git diff --stat e3b9c188c..HEAD` against the full file list in the dispatch; every changed file is
   listed in the Commits section above and none collides.

## Gate log summary

- `npx tsc --noEmit`: 0 errors, every commit.
- `npx vitest run` (bare): 223/3217 (baseline) → 225/3233 (final), file count and test count both
  strictly increased across the 6 commits, 0 failures at every gate checkpoint.
- `npm run lint` (biome ci): 0 errors at every commit (one intermediate formatting violation in
  `MarketplacesSection.test.tsx` — a too-long single-line object literal — fixed via `npm run format`
  before that commit, not by hand-editing whitespace).
- `npm run build`: succeeds at every commit; `dist/PLACEHOLDER` restored via `git restore` each time,
  tree clean.
