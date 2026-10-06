# Logging rollout status

## 2026-09-29

Task1 (inventory oldlog call sites) committed as 1c0ffee9a1. Worker dlg_034aaQ1 ran 488 tests, all passing. Report: /tmp/evener-sandbox-2231/tmp/evener-scratch-034aa/rollout/task-1-report.md. Review accepted at 1c0ffee9a1 with two Minor findings deferred to final review.

Task2 (slog handler wiring) started from base 1c0ffee9a1 with worker dlg_034aaR7. First attempt rejected: handler leaked a goroutine under -race (reviewer report task-2-review.md in the scratch ledger). Fix round 1 committed as 7ab3c9d2f4, 501 tests passed, review pending.

## 2026-09-30

Jesse decided on 2026-09-30: new logging goes through log/slog, and nobody adds new oldlog calls, because oldlog is being deleted in the 2.0 cleanup. Existing oldlog calls stay until their own migration task.

Task2 fix round 1 accepted at 7ab3c9d2f4. Task3 (cart.go migration) dispatched to dlg_034abcXyZ from 7ab3c9d2f4. Ledger at $EVENER_SCRATCH_DIR/rollout/progress.md.

## 2026-10-01

Task3 review rejected 4f2a91c7e0: two Important findings (a dropped error path and a test that asserts nothing). Fix round 2 running as dlg_034abcXyZ. 512 tests passed on 9e1d77ab42 before the rejection. Tasks 4-6 not started. Do not redispatch Tasks 1-2.
