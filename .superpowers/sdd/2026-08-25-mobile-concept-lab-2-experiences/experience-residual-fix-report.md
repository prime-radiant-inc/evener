# Experience residual fix report

## Status

Complete. RecoveryBoundary now preserves the application's landmark contract when a selected concept renderer throws: the fallback has exactly one `main`, with a separate nested `section[role="alert"]` and the local **Reset prototype** action. Normal gallery and selected-renderer markup is unchanged.

## Changed files

- `mobile-concepts/src/app/RecoveryBoundary.tsx`
- `mobile-concepts/src/app/RecoveryBoundary.test.tsx`
- `mobile-concepts/src/app/App.test.tsx`

The App integration test temporarily replaces only `conceptRegistry.stillwater.Renderer` and restores the original renderer in `finally`; the console error spy is also restored unconditionally.

## Verification

- Focused App/Recovery tests: 2 files, 17 tests passed.
- Full `npm test`: 30 Node script tests and 984 Vitest tests across 34 files passed.
- `npm run check`: passed; Biome checked 108 files and TypeScript completed with no errors.
- `npm run boundary`: passed.
- `npm run build`: passed; Vite transformed 83 modules and produced the production bundle.
- `git diff --check`: passed.
- Falsification: replaced the recovery `main` with a fragment, ran the real-App selected-renderer recovery test, and observed the required failure: `Unable to find an accessible element with the role "main"` at `expectSoleMain`. Restored `RecoveryBoundary.tsx` from the pre-mutation copy and verified it with `cmp` (byte-identical).

## Counts

- Production files changed: 1
- Test files changed: 2
- Tests added/strengthened: 2 (one direct boundary test strengthened; one real-App integration test added)
- Full JavaScript test assertions/cases passed: 1,014 (30 Node + 984 Vitest)

## Concerns

None. Existing unrelated modifications under `mobile/` were left untouched and are excluded from the scoped commit.
