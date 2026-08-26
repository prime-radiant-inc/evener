# Task31 / #155 / PR480 residual documentation search disposition

Base: `f3017ce762abe2cbc352c0992718706906b5ff46`  
Date: 2026-08-26

## Current contract applied

Delegate identity is a stable `dlg_...` resource with private run generations.
Delegates are not activation `JobRecord`s and do not expose public `job_id` values,
`job.notification`, or `job:` transcript output. Delegate lifecycle attention is
`<delegate-notification>`; delegate conversation and result history is read via
the delegate session `transcript_ref`. `job_...`, `job.notification`, and `job:`
output are shell-only. The notes added to the historical design files below are
intentional annotations, not shipped API claims.

## Disposition of the 25 requested groups

1. `docs/job-control.md:1098` — corrected “any job” to “any uniquely resolved shell job” and explicitly routed delegate history to its session ref.
2. `docs/subagent-management/07-lifecycle-hooks-claude-compat.md:189-200` — rewrote “Delegate job lifecycle” as stable delegate lifecycle, private runs, typed delegate notification, and session transcript retention.
3. `test/scenarios/INDEX.md:305-306,321-322,363` — removed delegate-as-job/next-job wording; stable resource/private run wording now points to the delegate scenario.
4. `test/scenarios/job-list-and-recovery.md:43-51,69-101` — delegate is D4 (stable `delegate_id`) and excluded from shell JobRecord filters/order; delegate listing is covered by `subagent-list-and-output.md`.

Groups 5–25 are historical or approved design documents containing delegate
lifecycle/handle claims. Each now has a prominent top-level **Current contract /
partial supersession** note. Non-delegate design content remains authoritative;
no dated history was deleted or rewritten wholesale:

5. `2026-06-10-job-control-open-decision-fixes.md` — annotated.
6. `2026-06-11-job-control-surface-ergonomics.md` — annotated.
7. `2026-06-11-job-control-watch-mailbox-design.md` — annotated.
8. `2026-06-12-recursive-subagents-design.md` — annotated.
9. `2026-06-13-max-wait-unification.md` — existing shell-only supersession extended with delegate contract note.
10. `2026-06-18-job-control-handle-split-design.md` — annotated.
11. `2026-06-21-communicate-end-turn-design.md` — annotated.
12. `2026-06-23-job-status-transcript-design.md` — annotated.
13. `2026-06-25-job-notification-renderer-design.md` — annotated.
14. `2026-06-25-subagent-run-rendering-design.md` — annotated.
15. `2026-07-13-codex-timeout-and-status-integrity-design.md` — annotated.
16. `2026-07-15-hub-webui-data-path-corrections-design.md` — annotated.
17. `2026-07-15-job-supervision-surface-cleanup-design.md` — annotated.
18. `2026-07-19-delegate-turn-limits-design.md` — annotated.
19. `2026-07-31-webui-jobs-panel-design.md` — annotated.
20. `2026-08-01-open-local-job-transcript-reads-design.md` — annotated.
21. `2026-08-02-approved-evener-decisions-design.md` — annotated and explicit delegate row fields corrected to stable aggregate fields.
22. `2026-08-03-session-tree-sidebar-design.md` — annotated.
23. `2026-08-06-delegate-send-renderer-design.md` — annotated; removed shipped `started_job_id` display claim.
24. `2026-08-09-one-shot-background-job-drain-design.md` — annotated.
25. `2026-08-09-read-transcript-tools-design.md` — annotated; `job:` wording scoped to shell and delegate results to session refs.

## Explicitly allowed history (not changed)

The clearly marked history named in the request remains untouched: delegate
identity simplification; July 31 read-grant plan/design; observer-autoopen;
June 8 job-control; runtime-contract point-in-time; and dated plans/research.
The unrelated `read_session_transcript` minor/API-log migration is deferred.

## Audit method

The source audit searched the 25 named files plus the two current contract files
for `delegate`, `JobRecord`, `job_id`, `job.notification`, `job:`, `next job`, and
`started_job_id`; every remaining historical hit is either in the explicit
supersession note or in dated design prose covered by that note. Scenario source
citations use named headings/phrases, never line-number citations, per
`test/scenarios/README.md` “Citing a contract”.
