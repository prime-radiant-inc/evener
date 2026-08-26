# Task 6 product fix report

Date: 2026-08-25 PDT

## Scope

Fixed only the two strict accessibility defects documented in `task-6-report.md` lines 167–178:

- Each Stillwater, Constellation, and Field Notes question option now has one stable `aria-describedby` token (`question-${question.id}-option-${option.id}-detail`) resolving to the option's single detail element. The existing `aria-label={option.label}` preserves the exact accessible name and visual copy.
- Stillwater's pushed Back/Close button now derives `data-icon-target`, width, minimum width, height, and minimum height from `${primitives.minimumTarget}px`, yielding `44px` on iOS and `48px` on Android.

No test or shared-action behavior changed.

## RED evidence

From `mobile-concepts`, the required initial command:

```text
npx vitest run src/test/accessibilityContract.test.tsx
Test Files  1 failed (1)
Tests       8 failed | 11 passed (19)
```

The failures were exactly six concept/platform accessible-description cases (expected `Review route and back behavior.`, received empty) and two Stillwater pushed-control hook cases (expected `44px`/`48px`, received `null`).

## GREEN evidence

```text
npx vitest run src/test/accessibilityContract.test.tsx
Test Files  1 passed (1)
Tests       19 passed (19)

npx vitest run src/concepts/stillwater/*.test.tsx src/concepts/constellation/*.test.tsx src/concepts/field-notes/*.test.tsx
Test Files  8 passed (8)
Tests       161 passed (161)

npm test
Node tests  30 passed, 0 failed
Vitest      33 files passed; 967 tests passed

npm run check
Biome checked 107 files with no fixes; TypeScript passed.

npm run boundary
Passed.

npm run build
TypeScript passed; Vite transformed 83 modules and built successfully.

git diff --check
Passed.
```

After falsification restoration, the strict suite was rerun and again passed 19/19.

## Falsification evidence

Saved the green Stillwater `QuestionCard.tsx` and `StillwaterShell.tsx` outside the workspace, then removed one `aria-describedby={detailId}` relationship and the pushed button's `data-icon-target={iconTarget}` hook.

The strict suite failed as required:

```text
Test Files  1 failed (1)
Tests       4 failed | 15 passed (19)
```

The four failures were Stillwater iOS/Android semantic matrix failures with an empty accessible description and Stillwater iOS/Android target-hook failures receiving `null` instead of `44px`/`48px`. Both files were restored from the saved green bytes and verified with `cmp -s`; the restored strict run passed 19/19.

## Commit evidence

Committed with subject:

```text
fix(concepts): expose option descriptions and targets
```

The commit is restricted with `git commit --only` to this report and the four requested production files. No pre-existing mobile workspace changes are included.
