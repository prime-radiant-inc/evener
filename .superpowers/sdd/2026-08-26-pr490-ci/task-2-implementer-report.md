Summary

Fixed the AppShell Mod-J regression so a settled empty `needs_you` page is treated as terminal, while a truly cold `needs_you` section still issues its initial request.

Files changed

- `cmd/evener-hub/frontend/src/shell/AppShell.tsx`
- `.superpowers/sdd/2026-08-26-pr490-ci/task-2-implementer-report.md`

Root cause

After the PR 490 rebase, Mod-J still used `selectNextSectionOffset("needs_you", state)` after an empty page had already settled with `remaining === 0`. That advanced the computed page key past the loaded empty page, so the next keydown saw no resource at the new page key and treated the section as requestable again. The fix adds a narrow settled-page check: when there are no visible needs-you refs and the latest settled needs-you page exists and reports `remaining === 0`, Mod-J stops instead of re-requesting.

Tests/commands and results

- `npx vitest run src/shell/AppShell.test.tsx -t 'v1 Mod-J does not re-request an empty needs-you page' --maxWorkers=4`
  - before fix: failed with `expected "vi.fn()" to be called 1 times, but got 2 times`
  - after fix: passed (`1 passed | 73 skipped`)
- `npx vitest run src/shell/AppShell.test.tsx -t 'Mod-J' --maxWorkers=4`
  - passed (`5 passed | 69 skipped`)
- `npx biome check --write src/shell/AppShell.tsx`
  - passed (`Checked 1 file ... No fixes applied.`)

Commit SHA

`33f6214ca45835cf8248de1d99d9c19790a51070`

Limitations

Verification stayed at the narrow frontend scope requested here. I did not run broader frontend or repository gates such as `make test-web`, `make lint`, or `make merge-approval-gate`.
