

## Fix report — AppShell mobile viewport height lint regression

### Diagnosis
- Biome was failing `lint/suspicious/noDuplicateProperties` on `cmd/serf-hub/frontend/src/shell/AppShell.module.css` because the shell rule used consecutive declarations:
  - `height: 100vh;`
  - `height: 100dvh;`
- This preserved the intended browser fallback ordering, but Biome still treats the second declaration as a duplicate property.
- The existing contract test in `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx` only asserted the two declarations were present in order; it did not model a valid lint-safe structure.
- I checked the frontend Biome config and existing CSS patterns; there was no narrow suppression already in use for this case, so the cleanest fix was a structural CSS feature query.

### Fix
- Replaced the duplicate `height` declarations with:
  - `height: 100vh;` on `.shell`
  - an `@supports (height: 100dvh)` block that overrides `.shell { height: 100dvh; }`
- Updated the AppShell contract test to assert the fallback remains on `.shell` and the dynamic viewport override lives inside the feature query.

### Commands and results
- Reproduced the lint failure:
  - `cd cmd/serf-hub/frontend && npx biome lint src/shell/AppShell.module.css`
  - Result: `lint/suspicious/noDuplicateProperties` on consecutive `height` declarations.
- Focused test run:
  - `cd cmd/serf-hub/frontend && npm test -- src/shell/AppShell.test.tsx`
  - Result: `45 tests passed`.
- Frontend lint:
  - `cd cmd/serf-hub/frontend && npm run lint`
  - Result: `Checked 804 files ... No fixes applied.`
- Browser guard:
  - `cd cmd/serf-hub/frontend && npm run layoutguard -- mobile-shell-viewport-height`
  - Result: `ERROR - chrome devtools endpoint never came up` (environment/tooling issue, not a CSS regression result).

### Commit
- Code fix commit: `1c7c2491c` — `fix(frontend): gate mobile viewport height override`

### Self-review
- The CSS fallback contract is preserved: `100vh` remains the baseline, and `100dvh` still applies only where supported.
- The change is narrow and idiomatic; it avoids global lint suppression.
- The contract test now matches the actual structure that Biome accepts.
- Remaining concern: the layoutguard browser check could not be completed in this environment because Chrome never exposed a DevTools endpoint.

## Final review fix wave — browser guard contract and unsandboxed verification

### What changed
- Updated `cmd/serf-hub/frontend/scripts/layoutguard/cases/mobile-shell-viewport-height/assert.mjs` so the CSS-contract parser now distinguishes:
  - the base `.shell` rule, which must contain `height: 100vh` and must not contain `height: 100dvh`
  - the `@supports (height: 100dvh)` block, which must contain its own `.shell` override with `height: 100dvh`
- Diagnostics are now specific and mutation-falsifiable:
  - missing base fallback
  - misplaced `100dvh` in the base rule
  - missing `@supports (height: 100dvh)` block
  - missing `.shell` override inside the `@supports` block
  - missing `height: 100dvh` inside the override rule
- Strengthened `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx` so it asserts the exact structure/order instead of broad string presence:
  - the base `.shell` rule contains `height: 100vh`
  - the base `.shell` rule does not contain `height: 100dvh`
  - the `@supports (height: 100dvh)` block appears after the base `.shell` rule
  - the nested `.shell` override inside the `@supports` block contains `height: 100dvh`
  - the nested override does not contain `height: 100vh`
- Adjusted the focused test regex to satisfy Biome's regex lint rule (`noAdjacentSpacesInRegex`) while preserving the same structural assertion.

### Unsandboxed verification commands and exact results
- Targeted browser guard:
  - Command:
    ```bash
    cd cmd/serf-hub/frontend && npm run layoutguard -- mobile-shell-viewport-height
    ```
  - Exact output:
    ```text
    > serf-hub-frontend@1.0.0 layoutguard
    > node scripts/layoutguard/run.mjs mobile-shell-viewport-height

    mobile-shell-viewport-height ... PASS - session and non-session mobile shell fixtures stay within the visible viewport and .shell keeps its 100vh base fallback plus @supports(100dvh) override contract
    ```
  - Exit code: `0`
- Full layoutguard suite:
  - Command:
    ```bash
    cd cmd/serf-hub/frontend && npm run layoutguard
    ```
  - Exact output:
    ```text
    > serf-hub-frontend@1.0.0 layoutguard
    > node scripts/layoutguard/run.mjs

    491q-dockoverlay-escape ... PASS - popover clears the middle panel by 365.0px horizontally
    askdock-mobile-tall-crush ... PASS - ask dock geometry holds at phone width
    edhz-attachment-tile-single-image ... PASS - one image fills the 80x80 tile exactly in both states, nothing inside it is clipped, and pending and settled are the same box
    hk8v-remove-button-focus-visible ... PASS - remove button is opacity 0 at rest, 1 on tile hover, and 1 under :focus-visible with a 2px solid rgb(129, 180, 232) ring
    mobile-shell-viewport-height ... PASS - session and non-session mobile shell fixtures stay within the visible viewport and .shell keeps its 100vh base fallback plus @supports(100dvh) override contract
    p6g8-formrow-overlap ... PASS - trigger fits inside .cfgDir with 0.0px to spare
    rdry-toolrow-contrast ... PASS - demoted line clears AA in both themes (dark 6.50:1, light 7.05:1)
    xak9-diffline-overflow ... PASS - content fits inside .root with 9.0px to spare and truncates with a visible ellipsis
    zscn-theme-flip-dark-surface ... PASS - nested dark and light panes resolve their own surface tokens under a light root
    ```
  - Exit code: `0`
- Focused AppShell source test:
  - Command:
    ```bash
    cd cmd/serf-hub/frontend && npx vitest run src/shell/AppShell.test.tsx -t "shared shell follows the visible viewport"
    ```
  - Exact output summary:
    ```text
    RUN  v4.1.10 /Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/webui-workspace-shell/cmd/serf-hub/frontend

    ✓ src/shell/AppShell.test.tsx (45 tests | 44 skipped)

     Test Files  1 passed (1)
          Tests  1 passed | 44 skipped (45)
    ```
  - Exit code: `0`
- Frontend lint:
  - Command:
    ```bash
    cd cmd/serf-hub/frontend && npm run lint
    ```
  - Exact output summary:
    ```text
    > serf-hub-frontend@1.0.0 lint
    > biome ci src

    Checked 804 files in 364ms. No fixes applied.
    ```
  - Exit code: `0`

### Final review fix commit
- `9aa81b4241ad` — `test(webui): tighten mobile viewport contracts`
