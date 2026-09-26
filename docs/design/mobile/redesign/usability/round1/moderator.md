# Round 1 moderator guide (participants never see this)

Success is judged from each task's saved log (task-Tn-log.json), then the participant's claim.

| Task | Success (log) | Partial | Watch for |
|---|---|---|---|
| T1 | `answer` for s-audit | answered via typing (`how: typed`) or opened s-audit without answering | Do they notice the failed session and the notice first? Do they find the dock? Time to first open. |
| T2 | `review_sent` from the hier plan with a comment or note mentioning one host | a plain `send` with the change request | Do they find the plan (Board chip vs session doc chip)? Do they discover long-press to comment, or use the note field? |
| T3 | `artifact_proposal` action send/queue/steer, text contains "layout C" | artifact opened, C chosen, proposal discarded | Do they find the artifact? Understand the proposal sheet? |
| T4 | `start_session` host=paradise-park, project=evener, model=glm-5.3-vision, effort=max, plugins=[go, superpowers] | started with 1-2 fields wrong | Plugin None-then-pick path; finding Max; host-change project note. |
| T5 | `subagent_stop_request` for g-settle | opened g-settle | Chips → Subagents → Failed; understanding "Ask coordinator to stop it". |
| T6 | `send` mode=queue for s-tasklist | `send` mode=steer (wrong semantics) | Do Queue/Steer labels and the hint make sense? |
| T7 | `provider_signin` codex-jesse-fsck.com then `retry` for s-retry with providerOk=true | sign-in without retry, or retry without sign-in | Error → Sign in → Retry chain; notice usage. |
| T8 | `pin` s-retry category=Release and `organize` mode=host | one of the two | Finding pin (swipe vs long-press); finding the Organize-by control. |
| T9 | `search_open` s-gocache; claim mentions GOFLAGS or -count=1 | found session, wrong conclusion | Search discoverability; Archived vs search. |
| T10 | claim mentions risks (badge width / host-first toggle) AND `answer` for s-gateway or a deliberate choice to defer | noticed held alert but ignored | Do held alerts get noticed? Do they get back to the plan? |
| T11 | `approval` decision=allow for s-mirror | opened s-mirror | Finding it; clarity of Allow once. |
| T12 | `detail_level` Tools/Activity/Full for s-pr2138 or `evidence_toggle` open=true | expanded an activity run only | "Intent" chip meaning; discoverability of detail level. |
| T13 | `host_reconnect` or `notice_action` reconnect for paradise-park | opened hub/hosts | Notice banner vs Hub path. |
| T14 | `model_change` model=claude-sonnet-5 effort=high for s-pr2138 | model right, effort wrong | Model chip discoverability; effort control. |

Personas and task orders:

- P1 Orchestrator: T1, T5, T8, T4, T14, T6
- P2 Newcomer: T1, T9, T4, T12, T11, T6
- P3 Commuter: T1, T11, T6, T10, T2, T3
- P4 Editor: T2, T3, T10, T1, T9
- P5 Operator: T7, T13, T5, T14, T8, T12
