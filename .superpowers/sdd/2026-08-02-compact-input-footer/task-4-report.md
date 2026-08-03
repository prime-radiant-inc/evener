# Task 4 Report: compact session footer final verification

## Status

**DONE_WITH_CONCERNS**

All required functional, browser-layout, real-session overflow, canonical frontend, and repository-state checks passed after one verification-driven correction. The correction is committed separately as `d55d5d325 fix(webui): use semantic compact footer statuses`.

The concern is execution-environment-specific: the fixed Serf sandbox denies both normal Chrome loopback CDP startup and Vite's loopback listener. As permitted by the brief, browser checks used a temporary pipe/headless-shell workaround. All temporary drivers, build products, interposer files, profiles, and shell residue were removed, and the repository is clean.

## 1. Owning component tests

From `cmd/serf-hub/frontend`, with:

```bash
export PATH="/opt/homebrew/bin:$PATH"
npm test -- \
  src/panes/session/chrome/StatusRow.test.tsx \
  src/panes/session/chrome/ModelSwitch.test.tsx \
  src/panes/session/chrome/SessionChrome.test.tsx \
  src/panes/session/chrome/GoalControl.test.tsx
```

Initial result: exit 0, with no warnings or unhandled errors:

```text
Test Files  4 passed (4)
Tests       90 passed (90)
FOCUSED_TEST_EXIT=0
```

After the verification-driven correction, the exact command was run again and also exited 0:

```text
StatusRow.test.tsx      37 tests passed
SessionChrome.test.tsx  17 tests passed
ModelSwitch.test.tsx    21 tests passed
GoalControl.test.tsx    15 tests passed
Test Files              4 passed (4)
Tests                   90 passed (90)
POST_FIX_FOCUSED_EXIT=0
```

## 2. Browser guards

### Normal launch evidence

The normal repository layoutguard was attempted first:

```bash
cd cmd/serf-hub/frontend
npm run layoutguard
```

It failed exactly as documented for this sandbox, exit 1. All 11 cases reported:

```text
ERROR - chrome devtools endpoint never came up
NORMAL_LAYOUTGUARD_EXIT=1
```

The exact overflowguard command was also attempted normally:

```bash
node scripts/overflowguard/run.mjs 320 360 400 479 480 559 560 900
```

It exited 2 before browser measurement because the sandbox denied the Vite listener:

```text
Error: listen EPERM: operation not permitted 127.0.0.1:<random-port>
vite dev server never came up
NORMAL_OVERFLOWGUARD_EXIT=2
```

These are sandbox launch failures, not product or guard failures.

### Temporary verified workaround

Following Task 3's documented workaround, verification temporarily used:

- cached real `chrome-headless-shell`, reporting `HeadlessChrome/147.0.7727.15`;
- CDP `--remote-debugging-pipe`, avoiding the denied loopback CDP port;
- `--single-process --no-sandbox`, required by the outer sandbox;
- a scratch-only DYLD interposer replacing the failing `IONotificationPortGetRunLoopSource` call with a valid inert `CFRunLoopSource`;
- for overflowguard only, a Vite production build of the real `overflowharness-entry.tsx` React tree into scratch, loaded with `file://` and `--allow-file-access-from-files`, because the sandbox denies Vite's loopback listener;
- a scratch-only overflow adapter preserving the repository runner's disclosure, exception-safety, clipping, and horizontal-scroller assertions.

The workaround probe returned the real browser version through CDP:

```json
{"protocolVersion":"1.3","product":"HeadlessChrome/147.0.7727.15","revision":"@6b5a1b80ccc1e8a4967901d8e58fc2e162cdf050"}
```

### Full layoutguard

The complete `npm run layoutguard` suite then exited 0. All 11 cases passed:

```text
491q-dockoverlay-escape ... PASS
askdock-mobile-tall-crush ... PASS
compact-session-footer ... PASS - footer stays on one line and follows every compression threshold
edhz-attachment-tile-single-image ... PASS
hk8v-remove-button-focus-visible ... PASS
mobile-shell-viewport-height ... PASS
p6g8-formrow-overlap ... PASS
rdry-toolrow-contrast ... PASS
session-ask-pane-allocation ... PASS
xak9-diffline-overflow ... PASS
zscn-theme-flip-dark-surface ... PASS
PIPE_LAYOUTGUARD_EXIT=0
```

### Real-session overflowguard width sweep

The real built React session harness and reducer were measured at every specified width. Every width reported zero horizontal scroll containers:

```text
320px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally
360px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally
400px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally
479px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally
480px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally
559px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally
560px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally
900px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally
PIPE_OVERFLOWGUARD_EXIT=0
```

The runner explicitly reported intentional clipped/non-scroll elements rather than treating them as failures. Depending on width, these were the clamped notification text spans, a clipped summary, and 1px screen-reader-only spans. No footer or pane horizontal scroller was present.

### Browser workaround cleanup

After the checks:

- the repository `scripts/layoutguard/cdp.mjs` was restored from an exact saved copy;
- `scripts/overflowguard/run.mjs` and `vite.config.ts` were never changed;
- the temporary Vite config was removed;
- scratch build output, pipe drivers, interposer source/dylib, and temporary Chrome profiles were removed;
- an empty shell heredoc artifact (`sh-thd-1785670601`) was inspected and removed.

Cleanup proof:

```text
TEMP_BROWSER_WORKAROUND_REMOVED=1
GUARD_DRIVERS_RESTORED=1
```

`git diff --exit-code` for the guard drivers/config printed nothing, and `git status --short` was empty after cleanup.

## 3. Canonical frontend gate and correction

From the repository root:

```bash
export PATH="/opt/homebrew/bin:$PATH"
make test-web
```

### Initial result

The initial canonical gate exited 2:

```text
PASS  web-typecheck
PASS  web-test
FAIL  web-lint
MAKE_TEST_WEB_EXIT=2
```

Biome found four owning-file issues:

1. formatting in `StatusRow.test.tsx`;
2. formatting in `StatusRow.tsx`;
3. `role="meter"` on a `span` should use semantic `<meter>`;
4. `aria-label` was unsupported on the roleless queue `span`.

### Test-first correction

Before implementation, tests were changed to require:

- a native `METER` element;
- native `value` and `max` attributes;
- a named `status` role for the queue readout.

The red run behaved as expected:

```text
Test Files  1 failed (1)
Tests       3 failed | 34 passed (37)
SEMANTIC_RED_TEST_EXIT=1
```

The implementation correction then:

- changed the context wrapper from `<span role="meter" aria-valuenow/aria-valuemax>` to native `<meter min/value/max>`;
- added `role="status"` to the queue wrapper so its accessible label is valid;
- applied Biome formatting to the two owning files.

The focused green run passed:

```text
Test Files  1 passed (1)
Tests       37 passed (37)
SEMANTIC_GREEN_TEST_EXIT=0
```

The exact four owning suites then passed 90/90, as recorded above.

### Final canonical result

The repository-root gate was rerun and exited 0:

```text
PASS  web-typecheck
PASS  web-test
PASS  web-lint
POST_FIX_MAKE_TEST_WEB_EXIT=0
```

## 4. Verification-driven correction commit

Committed separately:

```text
d55d5d325 fix(webui): use semantic compact footer statuses
```

Only owning files were staged and committed:

```text
cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx
cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.tsx
```

The commit command emitted this sandbox warning:

```text
error: Unable to create '.../.git/packed-refs.lock': Operation not permitted
```

Despite that non-fatal maintenance warning, the commit itself succeeded and `git log`, `git show`, and `git diff-tree` all confirm commit `d55d5d325` with exactly the two files above.

## 5. Final repository inspection

Commands:

```bash
git diff --check
git status --short
git log -8 --oneline
```

Results:

```text
GIT_DIFF_CHECK_EXIT=0
GIT_STATUS_EXIT=0
```

`git status --short` printed nothing: the worktree and index are clean. No unrelated existing modifications were present, staged, or changed.

Recent commit order:

```text
d55d5d325 fix(webui): use semantic compact footer statuses
127a9a821 test(webui): pressure compact footer goal layout
4d13cd638 test(webui): guard compact session footer layout
ef45f120c feat(webui): keep session footer on one line
b9aa9bdc9 test(webui): cover footer token and lifecycle cases
4a4f84d64 refactor(webui): simplify session footer facts
0acdb540f docs(webui): plan compact input footer
7723c694e docs(webui): clarify compact footer constraints
```

Thus the three requested implementation commits are present after the two approved design/refactor commits, followed by the separate verification correction.

A residue scan found no repository-local:

- temporary dylibs;
- Chrome profiles;
- temporary Vite config;
- shell heredoc files;
- screenshots;
- `.orig` or `.rej` files.

## Concerns

1. Normal Chrome CDP and Vite loopback launch remain unavailable under this fixed sandbox. Browser checks passed only through the documented temporary pipe/headless-shell approach. This is an environment concern, not a product failure.
2. The overflow workaround necessarily replaced the denied Vite HTTP listener with a production-built `file://` copy of the same real React harness and reducer. It preserved the repository guard's substantive assertions and used a real browser, but it was not the unmodified `run.mjs` process path.
3. The correction commit produced a non-fatal `packed-refs.lock` permission warning from Git's post-commit maintenance. The commit and final repository state are valid and clean.

## Fix round 1: native meter fallback structure and retained browser evidence

### Status and commit

Review findings were addressed in a separate correction commit:

```text
471af7047 fix(webui): separate footer meter semantics
```

The commit owns exactly:

```text
cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.tsx
cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx
cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/harness.html
cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/assert.mjs
```

No evidence or report file was staged; `.superpowers/` is repository-ignored.

### Root cause and correction

The preceding correction placed the custom themed gauge and compact percentage inside a native `<meter>`. Children of native `<meter>` are fallback content, so conforming browser rendering can replace rather than display those children.

The final structure now has:

1. one native `<meter>` carrying the exact accessible label, `min`, `value`, and `max`, using the existing `statusrow.module.css` `srOnly` recipe;
2. an immediately following ordinary `<span className={context}>` visual sibling with `aria-hidden="true"`;
3. the themed 64px gauge and compact percentage as visual descendants of that ordinary wrapper, never native-meter fallback content.

The static compact footer harness now mirrors that exact production sibling structure at all eight widths. Its assertions additionally require an empty visually-hidden native `METER`, an immediately following aria-hidden `SPAN`, and no visual fallback children.

### Test-first evidence

The owning test was changed first to require:

- one native `METER` with the `srOnly` class;
- exact `value="64000"`, `max="128000"`, and accessible label;
- an ordinary `SPAN` visual context wrapper with `aria-hidden="true"`;
- the native meter outside and immediately before the visual wrapper;
- themed gauge and percentage descendants relying on the wrapper's aria hiding.

The RED run failed on the old nested structure as required:

```text
Test Files  1 failed (1)
Tests       1 failed | 36 passed (37)
AssertionError: expected meter.classList.contains(srOnly) false to be true
FIX_ROUND_STRUCTURE_RED_EXIT=1
```

After the correction, the GREEN run passed:

```text
Test Files  1 passed (1)
Tests       37 passed (37)
FIX_ROUND_STRUCTURE_GREEN_EXIT=0
```

The complete owning test command was rerun:

```text
StatusRow.test.tsx      37 passed
SessionChrome.test.tsx  17 passed
ModelSwitch.test.tsx    21 passed
GoalControl.test.tsx    15 passed
Test Files              4 passed (4)
Tests                   90 passed (90)
FIX_ROUND_FOCUSED_EXIT=0
```

### Full browser verification

The complete layoutguard suite passed after both production and static-harness corrections:

```text
491q-dockoverlay-escape ... PASS
askdock-mobile-tall-crush ... PASS
compact-session-footer ... PASS - footer stays on one line and follows every compression threshold
edhz-attachment-tile-single-image ... PASS
hk8v-remove-button-focus-visible ... PASS
mobile-shell-viewport-height ... PASS
p6g8-formrow-overlap ... PASS
rdry-toolrow-contrast ... PASS
session-ask-pane-allocation ... PASS
xak9-diffline-overflow ... PASS
zscn-theme-flip-dark-surface ... PASS
LAYOUTGUARD_EXIT=0
```

Raw compact-footer measurements prove the structure at every requested static width:

```text
320, 360, 400, 479, 480, 559, 560, 900px:
semanticTag=METER
semanticChildren=0
semanticBox=1px by 1px
semanticNextIsVisual=true
visualTag=SPAN
visualAriaHidden=true
```

The real-session overflowguard passed all specified widths:

```text
320px scrollers=0 disclosures=2 exceptionFound=2 exceptionThrew=true
360px scrollers=0 disclosures=2 exceptionFound=2 exceptionThrew=true
400px scrollers=0 disclosures=2 exceptionFound=2 exceptionThrew=true
479px scrollers=0 disclosures=2 exceptionFound=2 exceptionThrew=true
480px scrollers=0 disclosures=2 exceptionFound=2 exceptionThrew=true
559px scrollers=0 disclosures=2 exceptionFound=2 exceptionThrew=true
560px scrollers=0 disclosures=2 exceptionFound=2 exceptionThrew=true
900px scrollers=0 disclosures=2 exceptionFound=2 exceptionThrew=true
OVERFLOWGUARD_EXIT=0
```

### Retained inspectable browser evidence

The exact workaround sources, commands, raw logs, and measurements remain available for review under:

```text
.superpowers/sdd/2026-08-02-compact-input-footer/task-4-browser-evidence/
```

Files:

```text
cdp-pipe.mjs                         exact temporary CDP pipe driver
viewport.mjs                         exact driver dependency copied from the repository
iokit-runloop-shim.c                 exact DYLD interposer source
overflow-pipe-run.mjs                exact overflow assertion adapter
vite-overflow.config.mjs             exact temporary Vite build config
run-browser-verification.sh          exact compile/build/guard/cleanup commands
environment.log                      paths, versions, widths, HEAD, SHA-256 hashes
chrome-stderr.log                    raw HeadlessChrome stderr from all launches
layoutguard.log                      full layoutguard output
layoutguard-measurements.jsonl       raw CDP values for every layoutguard case
compact-footer-measurement-summary.jsonl
                                      eight concise compact-footer measurements
overflow-build.log                   raw Vite real-harness build output
overflowguard.log                    full eight-width guard output
overflow-cdp-measurements.jsonl      raw CDP values for overflow launches
overflow-measurements.jsonl          raw width-keyed guard result objects
overflow-measurement-summary.txt     concise eight-width overflow facts
cleanup.log                          pre-clean status and cleanup result markers
```

Recorded environment:

```text
Node v26.0.0
npm 11.12.1
Google Chrome for Testing 147.0.7727.15
browser product through CDP: HeadlessChrome/147.0.7727.15
```

The retained source hashes are in `environment.log`. Raw measurement JSONL includes browser-version responses and exact returned measurement objects.

Cleanup markers in `cleanup.log`:

```text
SCRIPT_EXIT=0
TRANSIENT_VITE_CONFIG_REMOVED=1
TRANSIENT_RUN_ROOT_REMOVED=1
```

The repository `scripts/layoutguard/cdp.mjs` was restored byte-for-byte. Repository `scripts/overflowguard/run.mjs` and `vite.config.ts` have no diff. The transient Vite config, compiled interposer, browser profiles, built harness, and all other scratch run output were removed. Only the explicitly requested evidence package remains.

### Canonical gate after all corrections

From the repository root with the required Homebrew path:

```text
PASS  web-typecheck
PASS  web-test
PASS  web-lint
FIX_ROUND_MAKE_TEST_WEB_EXIT=0
```

### Fix-round concerns

- Normal loopback Chrome CDP and Vite listen remain denied by the fixed sandbox. The retained evidence package makes the temporary pipe/headless-shell method and all raw results independently inspectable.
- Git again emitted the non-fatal `packed-refs.lock` permission warning during commit maintenance. Commit `471af7047` succeeded and owns only the four intended files.

## Fix round 2: mount and assert context in the real React overflow tree

### Status and commit

The remaining review finding is resolved in:

```text
f1c895f12 test(webui): seed overflow footer context
```

The commit owns exactly one repository file:

```text
cmd/serf-hub/frontend/src/dev/overflowharness-entry.tsx
```

The retained adapter, commands, and evidence remain ignored and uncommitted under `.superpowers/`.

### Root cause and test boundary

The real-session overflow sweep rendered `Session` through the real reducer, but its inline `ThreadReadResponse` snapshot did not provide context data. `hydrateThread` therefore produced `contextWindow: 0`, and `StatusRow` correctly omitted the entire context subtree. The earlier sweep proved general containment but never exercised the corrected native meter or its visual sibling.

There is no owning deterministic unit test or exported fixture for the inline snapshot in `src/dev/overflowharness-entry.tsx`; searches found no test importing or reading this dev entry. jsdom also cannot execute the container-query visibility contract. Therefore the retained real-browser guard is the earliest executable RED for this harness boundary. The existing reducer suite remains the deterministic check that the actual wire fields hydrate correctly.

Generated `SerfThread` and `hydrateThread` confirm the exact camelCase wire fields under `thread.serf`:

```text
contextUsed?: number
contextWindow?: number
contextPressure?: number
```

The harness now seeds:

```text
contextUsed: 64_000
contextWindow: 128_000
contextPressure: 0.5
```

TypeScript accepted the snapshot (`SEEDED_HARNESS_TYPECHECK_EXIT=0`), and the reducer suite passed 111/111.

### Real-tree assertion contract

The retained `overflow-pipe-run.mjs` now inspects the actual React `StatusRow` under `#oh-pane`, not static stand-ins. At every requested width it requires:

- exactly one `[data-testid="status-row"]`;
- exactly one native `METER` within that row;
- exact `value="64000"`, `max="128000"`, and label `Context: 64k of 128k tokens used, 50 percent`;
- the meter is a direct child of the real status row;
- its immediate next element sibling is exactly one visual context wrapper;
- the visual wrapper is a `SPAN` with `aria-hidden="true"`;
- the actual `[data-testid="session-chrome-body"]` computes `container-type: inline-size`;
- the 64px visual meter is shown and percentage hidden when actual body width is at least 400px;
- the percentage is shown as `50%` and visual meter hidden below 400px;
- the existing disclosure, exception-safety, and zero-horizontal-scroller contracts still pass.

### Browser RED before harness seed

All round-1 evidence was preserved unchanged under:

```text
.superpowers/sdd/2026-08-02-compact-input-footer/task-4-browser-evidence/round-1/
```

Before adding context data, the extended adapter ran at all eight widths. Full layoutguard passed, then the real React overflow sweep failed at every width because the context subtree was absent:

```text
320px  bodyClientWidth=213 meterCount=0 visualCount=0
360px  bodyClientWidth=253 meterCount=0 visualCount=0
400px  bodyClientWidth=293 meterCount=0 visualCount=0
479px  bodyClientWidth=372 meterCount=0 visualCount=0
480px  bodyClientWidth=373 meterCount=0 visualCount=0
559px  bodyClientWidth=452 meterCount=0 visualCount=0
560px  bodyClientWidth=453 meterCount=0 visualCount=0
900px  bodyClientWidth=587 meterCount=0 visualCount=0
OVERFLOWGUARD_EXIT=1
ROUND2_BROWSER_RED_EXIT=1
```

Each failure logged `FAIL - real context structure` with the full measured object. Raw round-2 RED evidence is retained under:

```text
.superpowers/sdd/2026-08-02-compact-input-footer/task-4-browser-evidence/round-2-red/
```

That directory contains the exact source copies, environment, Chrome stderr, layoutguard/build/overflow logs, CDP JSONL, width-keyed measurement JSONL, and cleanup markers. Its cleanup log records:

```text
SCRIPT_EXIT=1
TRANSIENT_VITE_CONFIG_REMOVED=1
TRANSIENT_RUN_ROOT_REMOVED=1
```

### Browser GREEN after harness seed

After seeding the real wire snapshot, the full layoutguard suite passed all 11 cases, including `compact-session-footer`:

```text
LAYOUTGUARD_EXIT=0
```

The real React overflow sweep then passed every specified pane width. The guard records actual body widths, which are smaller than the outer pane because the real trailing controls consume space. This means the real 400px body threshold is crossed between the 480px and 559px pane fixtures:

```text
320px ... context PASS - one native meter + aria-hidden sibling; 213px body shows compact percentage
360px ... context PASS - one native meter + aria-hidden sibling; 253px body shows compact percentage
400px ... context PASS - one native meter + aria-hidden sibling; 293px body shows compact percentage
479px ... context PASS - one native meter + aria-hidden sibling; 372px body shows compact percentage
480px ... context PASS - one native meter + aria-hidden sibling; 373px body shows compact percentage
559px ... context PASS - one native meter + aria-hidden sibling; 452px body shows wide meter
560px ... context PASS - one native meter + aria-hidden sibling; 453px body shows wide meter
900px ... context PASS - one native meter + aria-hidden sibling; 587px body shows wide meter
OVERFLOWGUARD_EXIT=0
ROUND2_BROWSER_GREEN_EXIT=0
```

Raw JSONL confirms at every width:

```text
statusRowCount=1
meterCount=1
meterTag=METER
meterValue=64000
meterMax=128000
meterLabel="Context: 64k of 128k tokens used, 50 percent"
meterParentIsStatusRow=true
meterNextIsVisual=true
visualCount=1
visualTag=SPAN
visualAriaHidden=true
bodyContainerType=inline-size
scrollers=0
```

The refreshed round-2 GREEN sources/logs/measurements are the top-level files in:

```text
.superpowers/sdd/2026-08-02-compact-input-footer/task-4-browser-evidence/
```

`environment.log` identifies `RUN_LABEL=round-2-green`, the exact source hashes, Node v26.0.0, npm 11.12.1, and Chrome for Testing 147.0.7727.15. Cleanup records:

```text
SCRIPT_EXIT=0
TRANSIENT_VITE_CONFIG_REMOVED=1
TRANSIENT_RUN_ROOT_REMOVED=1
```

Repository `scripts/layoutguard/cdp.mjs`, `scripts/overflowguard/run.mjs`, and `vite.config.ts` have no diff. The transient Vite config, compiled interposer, browser profiles, build directory, and run root were removed.

### Remaining gates

Targeted reducer verification:

```text
Test Files  1 passed (1)
Tests       111 passed (111)
REDUCER_TEST_EXIT=0
```

Focused footer suites:

```text
StatusRow.test.tsx      37 passed
SessionChrome.test.tsx  17 passed
ModelSwitch.test.tsx    21 passed
GoalControl.test.tsx    15 passed
Test Files              4 passed (4)
Tests                   90 passed (90)
ROUND2_FOCUSED_EXIT=0
```

Canonical repository-root gate:

```text
PASS  web-typecheck
PASS  web-test
PASS  web-lint
ROUND2_MAKE_TEST_WEB_EXIT=0
```

### Fix-round 2 concerns

- The fixed sandbox still denies normal loopback Chrome CDP and Vite listen. The same retained pipe/headless-shell method was necessary; round-1, round-2 RED, and round-2 GREEN evidence are all independently inspectable.
- Outer pane width is not the container-query width in the real tree. The retained measurements correctly assert variants against actual `session-chrome-body.clientWidth`, and demonstrate both sides of the 400px threshold.
- Git emitted the known non-fatal `packed-refs.lock` permission warning during the successful commit. Commit `f1c895f12` contains only the overflow harness snapshot.

## Final review fix wave

### Status and commit

**DONE**. All important and minor final-review findings were fixed in the one permitted wave.

```text
69cc02392 fix(webui): preserve compact footer facts
```

The commit contains the 11 tracked production/test/guard files. The detailed report and retained browser evidence stay under repository-ignored `.superpowers/`, matching the established workflow.

### Findings resolved

1. **Full-row clipping/model width:** the full row now reserves enough status width for fixed facts plus the model's 72px preference before the lower-priority goal consumes space. The goal is bounded, shrinkable, and ellipsized. The guard now rejects `status.scrollWidth > clientWidth`, zero-width or sub-72px full-row model triggers, and context/work/queue/goal boxes escaping their containers.
2. **Focus containment:** the goal and chrome trailing buttons use inset focus shadows with no outside outline. Layoutguard forces `:focus-visible` on real goal/action stand-ins and measures the settled cascade.
3. **Exact context counts:** the native meter's accessible label and visual wrapper tooltip expose exact unrounded used/max counts plus the integer percentage. Visible meter/percentage formats remain unchanged.
4. **Live cost regression:** the representative lifecycle test now asserts both `status-row-cost` and the seeded `~$1.23` text are absent.
5. **Stale summary:** `SessionChrome.tsx` now describes cadence/model/effort/context/live work/queue/goal.
6. **Real React pressure:** the real reducer fixture now includes supported effort, exact measured context, queue depth 12, active work, and a long goal; overflow checks require effort/context/queue visibility, the full queue accessible label, no internal status clipping, and a nonzero model width.

### Strict TDD evidence

Tests and guard assertions were strengthened before production code.

Component RED:

```bash
cd cmd/serf-hub/frontend
PATH=/opt/homebrew/bin:$PATH npm test -- src/panes/session/chrome/StatusRow.test.tsx
```

Result: exit 1; **5 failed, 32 passed**. The failures showed rounded `12k/128k` and `64k/128k` labels where exact `12345/128001` and `64000/128000` were required.

Browser-layout RED: normal Chrome could not start in the fixed sandbox (`chrome devtools endpoint never came up`; direct system Chrome abort 134, cached shell segfault 139). The strengthened assertions were therefore replayed against the retained pre-fix real-browser measurement. Result: exit 1, naming the intended defects:

```text
560px: status facts are internally clipped
560px: model visible value has zero width
560px: model did not receive its 72px preferred width
focused goal ring is not containment-safe
focused session-actions ring is not containment-safe
```

The retained pre-fix 560px input was status 76/204 and model 0px.

Narrow GREEN after implementation:

```text
StatusRow.test.tsx: 37 passed (37), exit 0
```

### Focused and canonical verification

Five focused owning suites:

```bash
npm test -- \
  src/panes/session/chrome/StatusRow.test.tsx \
  src/panes/session/chrome/ModelSwitch.test.tsx \
  src/panes/session/chrome/SessionChrome.test.tsx \
  src/panes/session/chrome/GoalControl.test.tsx \
  src/panes/session/chrome/SessionActionsMenu.test.tsx
```

Result: **5 files passed, 109 tests passed**, exit 0.

No reducer production was changed. The canonical web suite subsequently exercised the complete frontend test set.

The first `make test-web` run passed typecheck/tests and failed only Biome formatting in `StatusRow.tsx`. The named file was formatted with the repository Biome binary, then the exact gate was rerun:

```text
PASS  web-typecheck
PASS  web-test
PASS  web-lint
```

### Browser GREEN and retained evidence

Because normal loopback CDP/Vite remain denied by the fixed sandbox, the established retained pipe/headless-shell workflow was used. Full `npm run layoutguard` passed all 11 cases, including:

```text
compact-session-footer ... PASS - footer stays on one line and follows every compression threshold
LAYOUTGUARD_EXIT=0
```

The real built React/real reducer sweep passed at every requested outer pane width:

```text
320 360 400 479 480 559 560 900px: PASS
OVERFLOWGUARD_EXIT=0
```

It exercised exact `67345/100001` context, 67%, supported high effort, queue depth 12, active work, and a long goal. No horizontal scroller or internally clipped status row was reported.

Final static 560px evidence:

```text
body client/scroll:   560 / 560
status client/scroll: 380 / 380
model trigger:        125.1875px (value 105px, ellipsis)
context/work/queue:   fully contained
long goal:            168px, bounded and contained
focused goal:         2px inset accent shadow, outline none
focused actions:      2px inset accent shadow, outline none
```

Final browser evidence and exact driver/log/measurement copies are retained at:

```text
.superpowers/sdd/2026-08-02-compact-input-footer/final-review-browser-evidence/
```

### Changed tracked files

```text
cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/assert.mjs
cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/case.json
cmd/serf-hub/frontend/scripts/layoutguard/cases/compact-session-footer/harness.html
cmd/serf-hub/frontend/scripts/overflowguard/run.mjs
cmd/serf-hub/frontend/src/dev/overflowharness-entry.tsx
cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx
cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx
cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.tsx
cmd/serf-hub/frontend/src/panes/session/chrome/goalcontrol.module.css
cmd/serf-hub/frontend/src/panes/session/chrome/sessionchrome.module.css
cmd/serf-hub/frontend/src/panes/session/chrome/statusrow.module.css
```

### Remaining concerns

None in the product or tests. Environment note only: normal Chrome CDP and Vite loopback launch are unavailable in this fixed sandbox, so the already-documented real-browser pipe/file workflow was required. Git emitted the known non-fatal `packed-refs.lock` maintenance warning; commit `69cc02392` succeeded and was verified.
