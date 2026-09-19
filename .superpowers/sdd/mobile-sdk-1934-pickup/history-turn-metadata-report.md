# History turn metadata preservation

## Scope

- Worktree: `/Users/jesse/.codex/worktrees/turn-history-metadata/evener`
- Branch: `codex/turn-history-metadata`
- Exact base: `032c030251ac562b807f68ae700e4e36ee50f850`
- Implementation head before this report commit: `20c647652f6befd4da451a7b9be9528373b353c3`
- Implementation commit: `20c647652f6befd4da451a7b9be9528373b353c3`
- Production change: `appwire-client/typescript/reducer.ts`, 6 additions / 2 deletions
- Test change: `appwire-client/typescript/reducer.test.ts`, 134 additions / 0 deletions

## Change

`mergeToolCallsByCallId` now retains untouched empty turns even when an unrelated
tool result makes the result map nonempty. A turn emptied after its result item
is folded into a call is retained only when it carries one of the private
canonical fields `startedAt`, `completedAt`, `durationMs`, `usage`, `cost`, or
`error`. Metadata-free id/status-only result carriers continue to collapse, so
empty turn separators are not reintroduced. No values are transferred or
aggregated between turns, and call/result field precedence and identity logic
are unchanged.

## TDD evidence

The new `mergeOlderItemPage` tests were run red before the production change:

- older empty metadata turn was reduced to `["fresh-result"]`
- fresh empty metadata turn was reduced to `["older-result"]`
- metadata-bearing consumed result turn was reduced to `["fresh-call"]`

After the minimal predicate/list change, all four new cases passed, including
the legacy metadata-free collapse case.

## Verification

- `NODE_DISABLE_COMPILE_CACHE=1 ./node_modules/.bin/vitest run ../../../appwire-client/typescript/reducer.test.ts --config vite.config.ts`: **243 passed**.
- `NODE_DISABLE_COMPILE_CACHE=1 npm run build` from `appwire-client/typescript`: **passed**.
- `NODE_DISABLE_COMPILE_CACHE=1 npm run qualification` from `appwire-client/typescript`: **passed** (`qualified @evener/appwire-client@0.1.0`). The worktree had no package install, so the run used a temporary isolated dependency directory containing only the existing TypeScript, tinykeys, and ws packages; it did not modify a lockfile or shared install.
- `make lint-package-imports`: **passed**.
- `make lint-biome`: **passed**.
- Touched-file `biome check --write` and `git diff --check`: **passed**.
- Frontend `./node_modules/.bin/tsc --noEmit --incremental false -p tsconfig.json`: **blocked by three pre-existing base errors** in `reducer.test.ts` at lines 223, 224, and 2695 (the existing `toolModelTurn` `InputItem`/duplicate-id construction and an existing string `completedAt` fixture). The added tests introduce no type errors.
- Native checks were not run because this change is confined to the AppWire TypeScript reducer and its tests.

No push, PR, merge, issue, comment, or edits outside this worktree were made.
