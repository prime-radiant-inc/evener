# Feature specs

Point-in-time design records for substantial features. Each states the problem,
the chosen design, and the acceptance criteria at the moment it was approved.

These are not current references. Where a spec and a guide disagree, the guide is
right; where a spec records a decision that still holds, the rule belongs in the
owning guide. See [`docs/product/subsystems.md`](../product/subsystems.md) to
find that owner.

| Spec | Subject |
| --- | --- |
| [`2026-06-12-job-control-watch-deadlock-design.md`](2026-06-12-job-control-watch-deadlock-design.md) | Job-control watch deadlock and its resolution. |
| [`2026-10-09-session-activity-push-updates-design.md`](2026-10-09-session-activity-push-updates-design.md) | Push updates for session activity reads. |
| [`2026-10-11-delegate-latest-intent-design.md`](2026-10-11-delegate-latest-intent-design.md) | The latest-intent row a running delegate shows. |
| [`2026-10-12-subagent-status-dots-design.md`](2026-10-12-subagent-status-dots-design.md) | Status-dot vocabulary for subagent rows. |

The larger delegate design set lives in
[`docs/subagent-management/`](../subagent-management/); the long-form working
records live in [`docs/superpowers/`](../superpowers/).

Links inside these specs point at paths and line numbers from when they were
written and are deliberately excluded from `make docs-check`.
