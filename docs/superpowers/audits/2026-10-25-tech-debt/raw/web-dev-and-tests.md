# Audit slice: web-dev-and-tests

Scope: `cmd/evener-hub/frontend/src/dev` plus every frontend `*.test.*` file.
Worktree: `.worktrees/debt-audit` (origin/main at a6c0b9c561). Read-only audit; no tests were run.

## Overview and inventory

| Measure | Value |
| --- | --- |
| Files under `src/dev` | 175 (19,331 lines incl. CSS/tsv) |
| Largest dev files | `overflowharness-entry.tsx` 2018, `mockups/mockups-entry.tsx` 1216, `shellguard-entry.tsx` 1043, `transcriptscrollguard-entry.tsx` 871, `spawnguard-entry.tsx` 767, `copyonly.ts` 632 (+685 test) |
| Dev browser entries (`*-entry.tsx`) | 13, each with a root-level `*.html` |
| Frontend test files | 607 (293,460 lines) |
| `it(`/`test(` calls | ~11,100 |
| Test files over 1000 lines | 65 (165,270 lines, 56% of all test code) |
| Test files over 2000 lines | 27 (110,484 lines) |
| Shared test-support homes | 5 conventions: `src/*TestUtils.ts` (root), per-dir `*TestUtils.ts`, `testing/` dirs (6), `src/dev`, and `appwire-client/typescript/testing` |
| `vi.mock(` | 7 (the suite mostly uses the shared `FakeClient` boundary fake, which is good) |
| `.skip/.todo/.only` | 1 |

Overall: test infrastructure discipline is high (act-scope guard, console guard, ended-body guard, copy-oracle ratchet, cssBlock helpers).
The debt is that the shared helpers that exist are bypassed by copy-paste at scale, and that several dev
harnesses outlived the decision they were built for. The biggest levers are (1) deleting dead mockup harnesses,
(2) one shared store/fixture preamble for the ~90 files that re-declare it, and (3) breaking the 27 files over 2000 lines by feature.

Method note: the repo's own `copyonly.ts` inventory could not run (no `node_modules` in the audit worktree), so
copy-assertion numbers below are grep counts, not the audit tool's classification.

---

## web-dev-and-tests-1: Two mockup harnesses for shipped decisions are still in the tree (~2,750 lines)

Severity: medium. Category: dead-code.

Locations
- `src/dev/mockups/mockups-entry.tsx` (1216), `src/dev/mockups/mockups.module.css` (189), `mockups.html` (11), `vite.mockups.config.ts` (27)
- `src/dev/taskcardmockups-entry.tsx` (297), `src/dev/taskcardmockups/variants.tsx` (407), `fixture.ts` (389), `taskcardmockups.module.css` (167), `taskcardmockups.html` (12), `taskcardmockups.vite.config.ts` (13)
- `src/styles/token-contract.test.ts:504-510` and `:580-585` carry a path exception and a test for `dev/taskcardmockups/taskcardmockups.module.css`

Evidence
- `mockups-entry.tsx:1-3`: "Mockups for two proposed changes ... NOT product code and not a guard: this page exists so the design can be reviewed before anything ships." The two proposals (host picker above the working directory; "Organize by" grouping) both shipped: `panes/spawn/Spawn.tsx:233` has the host picker, `shell/rail/Rail.tsx` and `Rail.test.tsx` implement Organize by.
- `taskcardmockups/variants.tsx:17-20`: "the chosen variant is now the production card". The file registers five throwaway tool names (`mock_taskcard_a..e`) to compare layouts that were already decided.
- Neither page is referenced by any script, Makefile target, guard runner, or doc (`grep` over `docs/`, `Makefile`, `scripts/`, `.github` finds nothing). Last meaningful change 2026-10-12 (#4379), a mechanical widget migration.
- Both import production components (`Spawn`, `RailRow`, `TranscriptBody`, `TasksPanelBody`), so every refactor of those must keep the mockups compiling under `npm run typecheck`, which includes `src/dev`.
- `vite.mockups.config.ts` and `taskcardmockups.vite.config.ts` hardcode a personal tailscale hostname (`magic-kingdom`) in one and an env var in the other.

Impact: ~2,750 lines that typecheck and lint on every run, force token-contract exceptions, and add churn to every Spawn/Rail/task-card refactor, for pages nobody can reach from a documented command.

Fix plan
1. Delete the two entry directories, both html files, both vite configs.
2. Remove the `taskcardmockups.module.css` exception and its test (`token-contract.test.ts:504-510`, `:580-585`).
3. Design decisions already live in git history and in `docs/web-ui/mockups/`; no doc update is needed (no doc references the pages), but run `make test-web` to prove nothing imported from `src/dev/taskcardmockups`.

Size: ~-2,750 lines, +0. Risk: low (nothing imports them; `gallery.module.css` and `ThemeFlip` stay because the widget gallery uses them; verify with `tsc`).

---

## web-dev-and-tests-2: `k7harness` is a self-declared throwaway that was committed and has no runner

Severity: low. Category: dead-code.

Locations: `src/dev/k7harness-entry.tsx` (126), `src/dev/k7harness-entry.test.ts` (35), `k7harness.html`, `src/dev/DevHarness.module.css` (mentions it).

Evidence: `k7harness-entry.tsx:1-5`: "throwaway browser-verification harness for kata vybn ... never committed. Deleted before this kata closes." No script under `scripts/`, no Makefile target, and no Go browser test references `k7harness` (grep). Its only consumer is its own test, which imports the entry to check that a store toggles, i.e. a test whose only subject is the dead harness. It also carries one more copy of the `ThreadCapabilities` literal (`:32`).

Fix plan: delete the entry, its test, the html, and the stale mention in `DevHarness.module.css`. Size -170. Risk: none.

---

## web-dev-and-tests-3: `connectFakeClient` and the connection-store reset are re-declared in 88 test files, with four spellings of the reset

Severity: high. Category: duplication.

Locations (examples): `stores/agentsDoc.test.ts:10`, `stores/archivedList.test.ts`, `stores/threads.frameTimes.test.ts:36`, `stores/threads.notificationRouting.test.ts:38`, `panes/settings/HostPicker.test.tsx`, `panes/transcript/Transcript.test.tsx`, `dev/DevHarness.test.tsx:69`.

Evidence (measured)
- `grep -rl "^function connectFakeClient"` finds 88 files. After whitespace normalization, 57 are byte-identical:
  ```
  function connectFakeClient(): FakeClient {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    return fake;
  }
  ```
  The other 31 differ by a `state` parameter or a few lines (10 in `threads.parallelResyncEntry.test.ts`, 6 in `threads.frameTimes.test.ts`, etc.).
- The `beforeEach` reset of the connection store appears 453 times as `connectionStore.setState({...})` in four drifted spellings: `{ state: "idle", serverInfo: undefined, client: null }` x182, `{ client: null, state: "idle" }` x47, `{ state: "idle", serverInfo: undefined, features: undefined, client: null }` x25, `{ state: "idle", client: null }` x12. The spellings differ in whether `features` is cleared, so a test that sets features can leak into the next test of the same file, which is exactly the kind of cross-test leak `testSetup.ts` exists to prevent.
- `stores/connection.ts` exposes no `resetConnectionStoreForTests`, while other stores do (`resetThreadsStoreForTests`, `resetAgentsDocStoreForTests`).

Impact: ~450 lines of boilerplate plus latent inconsistency; adding a connection-state field (it has already grown `features`) means auditing every spelling.

Fix plan
1. Add `resetConnectionStoreForTests()` next to the store (mirrors the existing `*ForTests` reset convention) and `connectReadyFakeClient(state = "ready")` in a shared `src/testing/` module (see finding 13 for the home).
2. Add the connection reset to `testSetup.ts`'s per-test hook, so the 453 per-file resets can be deleted outright (after confirming no test relies on state surviving between tests in a file; run the suite on CI).
3. Mechanical codemod in batches of ~20 files per PR: replace the local function with an import, delete the `setState` line. Behavior-preserving; CI is the test.
4. Add a short entry to `docs/developing-evener/testing.md` naming the helper.

Size: ~-900 lines, +40. Risk: low-medium (tests that depend on a leaked `features`; CI will show).

---

## web-dev-and-tests-4: The thread/capabilities fixture is re-declared in 85 files although the shared package already has `wireThread`

Severity: high. Category: duplication.

Locations
- Shared: `appwire-client/typescript/testing/notifications.ts:13-45` (`CAPABILITIES`, `wireThread`) and, a second copy inside the package, `appwire-client/typescript/testing/tokenFlood.ts:243-257`.
- Web copies: 25 test files define `testThread` (13 byte-identical, e.g. `stores/threads.frameTimes.test.ts:63`, `threads.notificationRouting.test.ts:65`, `threads.foregroundOwnership.test.ts:80`), 25 define `readResponse`, 36 declare `const CAPABILITIES: ThreadCapabilities`, and 85 files in total contain the literal `changeVisionModel: true`.
- Non-test copies: `dev/k7harness-entry.tsx:32`, `dev/transcriptscrollguard-entry.tsx:211`, `dev/overflowharness-entry.tsx:81`, `dev/surface-sections/chrome.tsx:25`, `dev/surface-sections/composer.tsx:29`, `dev/editorial-preview/data.ts:17`, `panes/session/transcript/messages/testing/heldSteerTestUtils.ts:23`, `mobile-native/src/dev/demoSessions.ts`.

Evidence: `notifications.ts:1-3` says its builders exist so "each shape is written once ... rather than a literal copy-pasted into every site", yet only 4 web files import `wireThread`. The `threads.parallel*Entry` family (`parallelResyncEntry`, `parallelWatchedEntry`, `parallelHydrationEntry`, plus `foregroundOwnership`, `frameTimes`, `notificationRouting`) each open with the same ~100-line preamble: `establishMembership`, `flushUntil`, `CAPABILITIES`, `testThread`, `readResponse`, `versionedReadResponse`, `markResponseCut`, `emitAtResponseCut`, `connectFakeClient`. (`stores/threads.parallelResyncEntry.test.ts:53-200` versus `threads.parallelWatchedEntry.test.ts:37-199`.) The fixture literal also hardcodes the capability set in the dev guards, so adding a capability means editing ~85 files or silently letting fixtures drift.

Fix plan
1. Export one `FULL_THREAD_CAPABILITIES` and one `testThread(ref, overrides)` from the package's `testing/` (extend `wireThread`, which already takes overrides; delete the duplicate in `tokenFlood.ts`).
2. Add `readResponse(ref, overrides)` and the entry-test helpers (`establishMembership`, `markResponseCut`, `emitAtResponseCut`, `versionedReadResponse`) to `stores/testing/threadEntryHarness.ts`, next to the existing `entryProductionHarness.ts` (27 lines).
3. Migrate the `threads.*` family first (13 files, largest payoff), then `panes/session`, then dev entries.
4. Mobile-native's `demoSessions.ts` should consume the same export (shared client owns shared derivations).

Size: ~-2,500 lines, +120. Risk: medium (the fixtures differ in small ways: `instanceId`, `mutationStateAuthoritative`, `steer: steerMode`); migrate with overrides, one file family per PR, CI as judge. TDD does not apply (behavior-preserving), but each PR must keep test counts unchanged.

---

## web-dev-and-tests-5: Async and jsdom-environment helpers are reinvented per file next to existing shared ones

Severity: medium. Category: duplication.

Evidence (counts from grep over test files)
- `deferred`: shared at `appwire-client/typescript/testing/deferred.ts:12` and imported by 35 files, yet 17 files define their own (`shell/needsYouActions.test.ts:43`, `stores/threads.test.ts:1564`, `stores/transcriptRepository.resources.test.ts:159`, `panes/spawn/Spawn.test.tsx:490`), and 535 sites build `new Promise<T>((resolve ...` inline.
- `nextMacrotask` shared at `testing/macrotask.ts`, imported by 7 files; meanwhile 15 files define `async function flushUntil(done, maxTurns = 20)`, 5 define `flushMicrotasks`, 6 define `settleCallerContinuations`, plus ~8 more `flush/settle/tick` variants.
- `matchMedia`: `panes/session/testing/mobileViewport.ts` is imported by 10 files; 25 test files touch `matchMedia` (103 occurrences). `installMobileViewport` is redefined in 6 files (`shell/holdhints/HoldHints.test.tsx:42`, `shell/cheatsheet/CheatsheetOverlay.test.tsx:43`, `shell/AppShell.test.tsx:5879`), `stubMatchMedia` in 5. The literal `"(max-width: 899px)"` appears in 57 places in 35 files, mirroring `shell/useIsMobile.ts:3` (`MOBILE_QUERY`) without importing it, so a breakpoint change silently makes the mobile tests desktop tests.
- `ResizeObserver`: `src/resizeObserverTestUtils.ts` has a 13-line `StubResizeObserver`, used by 11 files; 7 more files declare an inline no-op `observe() {}` class; `dev/editorial-preview/fixture.test.tsx:12-19` is one of them.

Impact: dozens of near-identical helpers with slightly different defaults (`maxTurns` 20 vs 5), the shared `mobileViewport` lives under `panes/session/testing` where `shell/*` tests hesitate to import from.

Fix plan
1. `testSetup.ts` installs a no-op ResizeObserver and `scrollIntoView` (it already does the latter) once; delete the 18 per-file stubs.
2. Move `mobileViewport.ts` to the shared testing home, export `MOBILE_QUERY` from `shell/useIsMobile.ts` and use it in the helper.
3. Add `flushUntil` to the shared testing module (alongside `nextMacrotask`); migrate callers.
4. Replace the 17 local `deferred` copies with the package import; leave the 535 inline Promises alone (not worth churn) except where the file already imports the helper.

Size: ~-700 lines, +60. Risk: low.

---

## web-dev-and-tests-6: 58 test files hand-roll CSS parsing although `styles/cssBlock.ts` exists

Severity: medium. Category: duplication.

Locations: `styles/cssBlock.ts` (55 lines: `readModuleCss`, `blockBody`, `topRuleBlock`, `mediaBlock`); local reimplementations in `widgets/card/card.test.tsx:15-35` (`cardCss`, `ruleBody`, `stripCssComments`), `widgets/codeblock/codeblock.test.tsx:15`, `panes/welcome/welcome.overflow.test.tsx:15`, `panes/session/transcript/messages/resultTable.contract.test.ts:23` (`ruleBody`), plus ~50 more.

Evidence: 87 test files read stylesheets with `readFileSync`; only 11 import `cssBlock` (and 2 `cssClassTestUtils`); 83 of the 87 use their own parser. The comment-stripping regex `/\/\*[\s\S]*?\*\//g` occurs 96 times in 58 files, 3 as a function literally named `stripCssComments`. Local helper names found: `ruleBody`, `rulesFor`, `cssRules`, `declarationInRule`, `declarationValues`, `ownDeclarations`, `slotRule`(x4), `declarationsInRule`(x4), `nestedRuleBlock`. `card.test.tsx:28-30` documents why comment stripping matters ("this repo has a precedent of exactly that passing while asserting nothing"), which is the case for one implementation, not 58.

Related concern (decision for Jesse, not part of the mechanical fix): these tests assert on stylesheet text because jsdom cannot cascade (`cssBlock.ts:1-8`). Many assert declarations like `box-shadow: var(--shadow-card)` (`card.test.tsx:55-59`), which restates the CSS rather than testing behavior. The repo already has `make test-web-browser` layout guards for real geometry; some of these text-greps could be dropped rather than migrated.

Fix plan
1. Make `cssBlock.ts` strip comments inside `readModuleCss` (one behavior change, with a unit test in `cssBlock.test.ts`).
2. Migrate callers file by file to `topRuleBlock/mediaBlock/readModuleCss`; delete local parsers.
3. During migration, list tests that only restate a declaration and propose deleting them to Jesse rather than silently dropping.

Size: ~-900 lines. Risk: medium (parsers differ in "last rule" versus "first rule" semantics, as `card.test.tsx:20` notes; add that selector choice to the helper's API).

---

## web-dev-and-tests-7: Copy is still the oracle in thousands of assertions; the ratchet only catches tests named like "shows X"

Severity: high. Category: test-debt.

Evidence
- AGENTS.md and `docs/developing-evener/testing.md:62-90` forbid asserting rendered text equals a hardcoded phrase. Greps over the 605 non-audit test files: `getByText("`/`queryByText("`/`findByText("` x1,825 (969 + 288 + 568), `getByText(/regex/` x152, `textContent).toBe("` x679, `textContent).toContain("` x414, `getByLabelText("` x594.
- Many are fixture round-trips (allowed), but a sample shows real copy pins:
  - `shell/statusbar/StatusBar.test.tsx:414` `expect(...liveness-line").textContent).toBe("Waiting on 3 subagents")`; `panes/session/transcript/flow/LivenessLine.test.tsx:134,333,353,365` same pattern (the allowlist keeps only `liveness.test.ts` as a "design doc pins this string" exception; these four are not covered by it).
  - `panes/session/transcript/messages/NotificationCard.test.tsx:247,265,449,636,1334` `toContain("Delegate completed")`, `"Job completed"`, `"Delegate stopped"`, `"Job cancelled"`, `"Watch delivery failed"`.
  - `panes/session/transcript/TranscriptDetailControl.test.tsx:158,182,193` `toContain("Storage warning")`, `"Hub unavailable"`, `toBe("Hub default status: Same failure")`.
  - `panes/session/transcript/messages/WarningItem.test.tsx:189,209` `toContain("Output allocation reduced for inst/model: requested=100 admitted=50")`, `toBe("Compaction will manage the window.")`.
  - `panes/session/transcript/tools/jobTools.edge.test.tsx:266` `toContain("Started delegate")`; `panes/session/chrome/ActivityWatchDetail.test.tsx:505` `"Waiting on Child checks, matching ready"`.
  - Dev guards check labels too: `dev/overflowharness-entry.tsx:1987-1988` finds menu verbs by `textContent === "Stop"/"Steer"`.
- Heaviest files by literal-text queries: `Spawn.test.tsx` 163, `Composer.test.tsx` 150, `AppShell.test.tsx` 97, `CredentialsSection.test.tsx` 85, `RailRow.test.tsx` 81, `Rail.test.tsx` 79, `Zoom.test.tsx` 78, `taskCard.test.tsx` 62.
- Ratchet weakness: `dev/copyonly/copyonlyAudit.test.ts:1-15` only flags a test whose name says it shows/renders/says and whose every check is a copy claim. A test named "marks the delegate as stopped" that also asserts `toContain("Delegate stopped")` is invisible to it. The allowlist has 10 rows, so the ratchet reports almost nothing while the grep counts above are large.

Impact: every wording change breaks many tests, and the tests would still pass if the underlying state were wrong in the same way the label is.

Fix plan (per AGENTS.md: discuss consequential choices; this is a convention the repo already decided)
1. Extend `copyonly.ts` to report a second tier: a copy literal inside a test that has behavior assertions too, ratcheted per file (count may not grow) rather than failing outright. Add `docs/developing-evener/testing.md` text for the tier.
2. Convert the highest-density files first, using the doc's prescription: assert `data-kind`/`data-state`/role attributes (the components already expose `data-testid`s such as `liveness-line`, `notification-card`) or the structured value that feeds the label (e.g. the delegate outcome enum, the waiting count).
3. Where production lacks a stable hook, add a `data-*` attribute in production in the same PR (ask Jesse before adding widget props).

Size: ~-/+1,500 lines over several PRs. Risk: medium (needs component hooks; do not churn fixture round-trips).

---

## web-dev-and-tests-8: 27 test files exceed 2,000 lines (110k lines); `Spawn.test.tsx` is 11,489 lines with 308 tests and no `describe`

Severity: medium. Category: complexity.

Evidence
- `panes/spawn/Spawn.test.tsx` 11,489 lines, 308 tests, 0 `describe`, 51 helper functions scattered between tests (e.g. `openTeamProvider` at 1509, `openDesktopPluginSelection` 3137, `effortOptionLabels` 4413, `pastePngInto` 5806, `installStalledDecodeStub` 6213, `typeSlashQuery` 6568, `readyClientWithRemoteHome` 8069, `routedHostCalls` 10779). Test names cluster by feature: 21 "a remote ...", 16 "automatic connection ...", 10 "a host ...", slash menu, images, plugin selection, model/effort, drafts. `panes/spawn/` already has satellite files (`AdvancedOptions.test.tsx`, `MobileSettingRows.test.tsx`, `usePluginPreview.test.tsx`), so the single file contradicts the directory's own layout. `Spawn.tsx` itself is 3,279 lines.
- Others: `stores/threads.sessionActions.test.ts` 10,661 (234 tests, 8 describes, 22 helpers), `shell/AppShell.test.tsx` 8,914 (185 tests, 0 describes, 51 helpers; `installMobileViewport` at 5879), `Composer.test.tsx` 7,498 (237 tests), `Session.test.tsx` 7,111 (121 tests), `useTranscriptScroll.test.ts` 4,876, `ProviderConnection.test.tsx` 4,805, `transcriptRepository.resources.test.ts` 4,321, `threads.entryProduction.test.ts` 4,025.
- Costs: the CI test lanes were split and tuned for slowness (memory note 2026-09-22), and a single 11k-line file is one vitest worker's unit of parallelism, so it dominates the critical path. Files this size also exceed what an agent can read in one pass, which is how the 57 identical helpers (finding 3) accumulated.

Fix plan
1. After findings 3-5 land (so the shared preamble is one import), split by feature using the file's existing clusters: `Spawn.remote.test.tsx`, `Spawn.images.test.tsx`, `Spawn.slash.test.tsx`, `Spawn.plugins.test.tsx`, `Spawn.model.test.tsx`, `Spawn.drafts.test.tsx`; shared setup (`readyClient`, `renderSpawn`, `seedSources`, `setupUser`, `settled`) to `panes/spawn/testing/spawnHarness.tsx`.
2. Same treatment for AppShell, Composer, Session, `threads.sessionActions` in descending size.
3. Pure move; test counts before and after must match (`rg -c "^(test|it)\("`).

Size: ~0 net (moves) plus ~-1,500 from shared setup dedupe. Risk: low if done as pure moves; do not combine with logic edits.

---

## web-dev-and-tests-9: `dev/editorial-preview` tests mostly test the fixture's own scripted fake

Severity: medium. Category: test-debt (tests of mocks).

Locations: `dev/editorial-preview/boundary.test.ts` (140), `fixture.test.tsx` (102), subject `fixture.ts` (350) and `data.ts` (298).

Evidence
- `boundary.test.ts` imports only `./fixture` (lines 1-2). Every assertion reads from `createEditorialClient()`, a `FakeClient` subclass loaded with canned answers: "nested transcript ... opens exact distinct ... with truthful ancestry", "distinct child reads and owner projections never derive current lifecycle from receipts", "fixture answer is reflected once, clears attention", "unexpected requests and unknown refs fail loudly; client instances do not share mutations". No production code runs; the tests check that the fake returns the data its author wrote. The file's own assertion `expect(native.length).toBeLessThanOrEqual(8000)` (line ~139) guards the sample against a production constant in a comment, not through production code.
- `fixture.test.tsx`: three of four tests ("the editorial client answers the spawn pane's slash catalog request", "the fixture's scripted handshake reaches the connection store") test the fixture's coverage of calls; only the first drives the real `AppShell`.
- This violates the repo's own rule against tests that test mocked behavior. The preview's real guard is `scripts/editorial-preview-browser.mjs` (342 lines) plus `scripts/editorial-preview.test.mjs` (287).

Fix plan: keep the one AppShell-driven test (it proves the fixture can drive the real shell, which is the reason the fixture exists), plus the `rejectedRequests` check that makes unscripted calls fail; delete `boundary.test.ts` and the two fixture-only tests. If the ancestry/lifecycle claims matter, assert them in the browser guard through the rendered UI. Raise with Jesse before deleting, per the "never throw away without permission" rule.

Size: ~-200 lines. Risk: low.

---

## web-dev-and-tests-10: Browser-guard entries are monoliths with duplicated scaffolding

Severity: medium. Category: complexity.

Locations: `dev/overflowharness-entry.tsx` (2018), `shellguard-entry.tsx` (1043), `transcriptscrollguard-entry.tsx` (871), `spawnguard-entry.tsx` (767), `retirementharness-entry.tsx` (457).

Evidence
- `overflowharness-entry.tsx` started as one bug repro (`:1-9`, "a transcript pane containing a delegation module scrolls sideways") and now exposes 11 window entry points (`:2007-2018`: `measure`, `measureComposerContentWidth`, `dump`, `inspectDetail`, `inspectChatFocus`, `inspectIntentColumn`, `inspectHeldSteerColumn`, `measureMenuVerbs`, `settled`, `verifyItemPaging`, `composerRequests`) and five URL modes (`settings`, `paging`, `intenttail`, `heldtail`, `steer`, lines 60-67) in one module. `measure()` spans lines 1591-1832 (~240 lines); `FIXTURES` spans 396-728 (~330 lines); `inspectDetail` ~285 lines (982-1267). The name "overflow" no longer describes paging, intent-tail, and menu-verb checks.
- Repeated scaffolding across entries: each re-creates `new FakeClient("ready")` (`overflowharness:404`, `transcriptscrollguard:243`, `spawnguard:22`, `shellguard:413`, `mockups:311`), a full `ThreadCapabilities` literal (finding 4), a `Thread` literal (`transcriptscrollguard:228-241` vs `overflowharness:94-130`, same `modelProvider`, `createdAt: 1000`, `cliVersion: "1.0.0"`), the `?theme=` handling (`k7:27-30`, `overflowharness:59,77`), its own wait/frame helpers (`overflowharness:1400 waitFor`, `:1412 twoPaintedFrames`, `transcriptscrollguard:509 nextFrame`, `retirementharness:275 settled`, `spawnguard:440 settleSpawn`), and its own page-error probe (`__panelGuardErrors` at `overflowharness:50-57`, nothing equivalent elsewhere).
- 13 `kata xxxx` ids across `dev/` (e.g. `spawnguard-entry.tsx:189,239,304`) narrate history instead of the invariant.

Fix plan
1. Extract `dev/guardKit.ts` with: `?theme=` application, `nextFrames(n)`, `waitFor(read, harness, label)`, error-probe install, and `mountFakeSession(options)` (FakeClient + connectionStore + capabilities + thread literal). `guardVisibility.ts` (59 lines) is the pattern to extend.
2. Split `overflowharness-entry.tsx` by mode into `settingsgeometry`, `paging`, `intentcolumn` modules imported by the entry, keeping the `window.*` surface and the guard runners in `scripts/overflowguard/run.mjs` unchanged (they are the contract; run `make test-web-browser` on a Chrome-capable host to prove it).
3. Do not touch what the runners call.

Size: ~-600 lines duplicated, +150 shared. Risk: medium (browser guards are only exercised on Chrome hosts; CI has them).

---

## web-dev-and-tests-11: `DevHarness` is a bare, permanent harness that violates the UI rules and narrates its history

Severity: low. Category: slop.

Locations: `dev/DevHarness.tsx` (111), `DevHarness.test.tsx` (274), `DevHarness.module.css`, `App.tsx:20-26,50-55`.

Evidence: header `DevHarness.tsx:1-10` explains it "arrived as Wave 1 scaffolding 'Wave 2 replaces with the real workspace shell'; the waves landed, the replacement did not"; it uses raw `<button>` and `<pre>` (line ~100) against the AGENTS.md "no raw `<button>` outside `src/widgets`" rule; its comment at `:46-55` documents Go JSON-null behavior and "legacy client" (`assets/appwire.js`) that is gone. It is the only web consumer of `thread/list` (per the SDK migration notes). The route `/dev/harness` is DEV-only.

Fix plan: ask Jesse whether the live-hub protocol smoke page is still wanted. If not, delete the file, its test, the `App.tsx` route and lazy import (about -400 lines, plus a small App.tsx edit and the `DevHarness` mentions in `App.test.tsx:144,206` and `AppShell.tsx:489`). If kept, switch to widgets and trim the history comments.

Size: -400 / ~-30. Risk: low.

---

## web-dev-and-tests-12: Test-support lives in five different homes, and production-adjacent code imports it in both directions

Severity: low. Category: inconsistency.

Evidence
- 25 `*TestUtils.ts` files scattered at the src root and next to components (`src/storageTestUtils.ts`, `src/textEntryTestUtils.ts`, `widgets/tooltip/tooltipTestUtils.ts`, `stores/sessionActivityTestUtils.ts`, `panes/zoom/cascadeTestUtils.ts`), 6 `testing/` directories (17 files), `src/dev`, and `appwire-client/typescript/testing`.
- `src/widgets/codeblock/ansi.test.ts:4` imports `../../dev/mulberry32`; `dev/overflowharness-entry.tsx:20` and `dev/shellguard-entry.tsx:49` import `../stores/sessionActivityTestUtils`. The coverage exclude in `vite.config.ts:268-276` lists `storageTestUtils.ts` and `resizeObserverTestUtils.ts` by name and `src/dev/**` by glob, i.e. exclusion is maintained by hand per file.
- `session/testing/mobileViewport.ts` is a cross-feature helper (used by shell tests) sitting in `panes/session`.

Fix plan: adopt one rule in `docs/developing-evener/testing.md`: shared helpers live in `src/testing/` (excluded from coverage by one glob); feature-local helpers stay beside their tests as `testing/` dirs; `src/dev` is only browser entries and gallery. Move `mulberry32` and the cross-feature helpers; leave feature-local `*TestUtils` alone to avoid churn.

Size: ~100 lines of moves and a doc paragraph. Risk: low.

---

## web-dev-and-tests-13: Narrating history comments are pervasive in tests and dev code

Severity: low. Category: slop.

Evidence: 271 `kata xxxx` references across `src`, 13 inside `dev/`. Examples: `buildScripts.test.ts:6-10` tells the story of a stale tsbuildinfo with "(kata af3e)" in both the comment and the assertion message; `transcriptscrollguard-entry.tsx:253-262` "kata read this the hard way once"; `k7harness-entry.tsx:1`. `dev/DevHarness.tsx:7-9` history of Wave 1/Wave 2. The user rules say history belongs in commit messages.

Fix plan: do not sweep. When a file is already being touched by findings 3-10, trim history to the one-line invariant. No standalone PR.

Size: incidental. Risk: none.

---

## web-dev-and-tests-14: Source-text meta tests pin implementation strings rather than behavior

Severity: low. Category: test-debt.

Locations: `src/buildScripts.test.ts` (65), `src/testCleanupHygiene.test.ts` (82), `src/testFlushScopeHygiene.test.ts` (179), `src/testSetup.test.tsx` (32; reads `testSetup.ts` text via `readFileSync`).

Evidence: `buildScripts.test.ts:30-65` asserts `package.json` script strings contain `"--incremental false"`, `"gate_source_helper ../../../scripts/lib/load-aware-workers.sh"`, `"$(vitest_run_args)"`, and `not.toMatch(/--maxWorkers=\d/)`. Its own comment concedes the behavior is "pinned directly in gatebudgets_test.go", so the remaining assertions test a config file's text. The two hygiene tests regex-scan other tests' source (e.g. forbidding per-file `cleanup`), which is useful as a lint but is a repo lint implemented as a unit test.

Fix plan: keep the Go behavior test; delete the `package.json` string assertions in `buildScripts.test.ts` (keep the `--incremental false` check only if a lint rule cannot express it). Move the two hygiene scans to `make lint` (already home of TOML naming and secret scan) only if Jesse wants them to run outside vitest; otherwise leave them.

Size: ~-50. Risk: low.

---

## Recommended fix sequence for this slice

1. Delete dead dev code (findings 1 and 2, and 11 after Jesse's answer). Pure removal; frees ~3,300 lines of typecheck and review surface before any codemod.
2. Shared test kit (findings 3, 4, 5): create `src/testing/` with `resetConnectionStoreForTests`, `connectReadyFakeClient`, `flushUntil`, moved `mobileViewport`; extend the package's `testing/` with one `testThread` and capabilities constant; add the connection reset and a no-op `ResizeObserver` to `testSetup.ts`. Document in `docs/developing-evener/testing.md` (finding 12 rule).
3. Migrate by codemod in family batches, `threads.*` first (13 files share ~100-line preambles), CI as the judge, test-count parity checked per PR.
4. Fold CSS-parsing helpers into `cssBlock.ts` (finding 6), migrating files while touching them; raise the "restates declaration" tests with Jesse.
5. Split the 27 files over 2,000 lines by feature, starting with `Spawn.test.tsx` (finding 8), as pure moves after steps 2-3 shrink their preambles.
6. Delete the fixture-only editorial-preview tests after Jesse's OK (finding 9).
7. Copy-oracle second-tier ratchet and conversion of the densest files (finding 7); in parallel, refactor the guard entries onto `guardKit.ts` (finding 10) verified by `make test-web-browser`.
8. Low-priority cleanups (13, 14) only when touching the files.
