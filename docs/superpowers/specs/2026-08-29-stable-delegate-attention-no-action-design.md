# Stable Delegate Attention No-Action Completion

- **Status:** Proposed after simplification and adversarial review
- **Date:** 2026-08-29
- **Incident:** `local:034FCH9eUroasmcr3cK4B7`

## Decision

Complete the existing stable-delegate `completed_no_action` path without redesigning terminal `communicate`, job-drain cuts, or delivery deduplication.

A stable attention generation may finish without an outward result only when:

1. the model loop recorded `delegateCompletionOutcomeAttentionNoAction`;
2. the generation's monotonic completion requirement remains `attention_only`;
3. no terminal `communicate` was accepted during the generation;
4. no run, hook, drain, cancellation, exhaustion, attention-resolution, or stop error won;
5. the controller consumes the exact finalization claim.

The controller then appends `delegate_run_finished` with public outcome `completed`, private disposition `completed_no_action`, no terminal packet, and no delivery ID.

This is the smallest safe fix for the incident. It connects an existing runtime trigger to an existing durable disposition. It does not attempt to repair adjacent terminal-result and notification-cut defects uncovered during review; those require separate designs.

## Incident and origin

The parent received repeated `reported` notifications from the same delegates without an intervening `delegate_send` or user input.

| Delegate | Child session | Parent notification turns | Reports |
|---|---|---:|---:|
| `dlg_034FGnQnCifSmOfBbozBUa` | `034FGnQnCiiFj6orykTDCC` | 600, 605, 611 | 3 |
| `dlg_034FGnPRMrtXnw0whvajkg` | `034FGnPRMrwK1roKzzpQZx` | 616, 626, 630, 634, 638 | 5 |
| `dlg_034FGnRQUAAC3ABolYLIYB` | `034FGnRQUACxGAnUjhSfT8` | 644, 649, 654 | 3, then stop |
| `dlg_034FGnQADF5GRQtFnjJAou` | `034FGnQADF9WGZCCc3BAOI` | 664, 669, 673 | 3, then stop |

One child recorded four stable-owned shell attentions and produced five reports: one task report plus one report for each attention successor. Every successor followed the same path:

1. the controller started `TriggerAttention`;
2. the model returned a bare acknowledgement such as “Report delivered”;
3. `routeNoToolCalls` treated it as a result-tool violation;
4. Evener instructed the model to call `communicate`;
5. that call created another canonical parent report.

The bug came from an incomplete integration:

- `74c8bd4106` introduced durable `completed_no_action` support but explicitly left active `Session` wiring for later;
- `363c2be7d` added autonomous durable-attention generations and rearming;
- the runtime still recognized only reported/error finishes and enforced `communicate` for every successful subagent run.

Tests covered the controller and attention runtime separately: controller tests manually supplied `completed_no_action`, while attention tests scripted `communicate`. No test covered bare attention acknowledgement -> packetless finish -> zero parent delivery. A later fuzz oracle codified bare `EntryDelegateAttention` as a retry, preserving the gap.

## Existing contract

The shipped design already defines the target state:

- `docs/subagent-management/11-delegate-resource-model.md` permits `completed_no_action` only for attention-triggered work consumed without `communicate`.
- `docs/superpowers/specs/2026-08-09-delegate-identity-simplification-design.md` says that state creates no terminal result or owner delivery.
- `delegatestore` validates attention trigger, public `completed`, no packet, and no delivery ID.
- `FinishNoAction` is the controller authority for the packetless disposition;
  general `FinishGeneration` rejects a caller-supplied no-action disposition.

The active runtime never safely selects it. This spec supplies only that missing selection and authority path.

## Scope

### Goals

1. Produce one task report and zero parent reports for bare no-action attention successors.
2. Require positive model-loop evidence for no action.
3. Preserve result enforcement when owner or other report-requiring work enters the generation.
4. Enforce packetless completion through the controller claim.
5. Preserve current stop, error, recovery, delivery, and explicit-`communicate` behavior outside this finding.
6. Add no persisted field, packet type, queue, prompt dependency, or content heuristic.

### Non-goals

- Do not infer attention consumption from transcript visibility.
- Do not coalesce or skip attention generations.
- Do not compare or deduplicate result content.
- Do not alter root `EntryNotification` behavior.
- Do not redefine terminal `communicate` acceptance, packet selection, or job-drain temporal cuts.
- Do not strengthen crash-time operational-evidence guarantees beyond current behavior.
- Do not remove lifecycle updates or watch reactions.

## Adjacent defects found during review

These existing defects are real but outside the duplicate-attention repair:

1. **#569 — stable terminal acceptance is not durably prepared at acceptance.** The evergreen resource model requires `delegate_terminal_prepared` before returning accepted, while current code retains transient `Session.comm` until later settlement. A crash can lose an accepted result.
2. **#570 — same-round terminal communicates are not atomically selected.** Parallel handlers can combine one call's message with another call's structured value.
3. **#571 — terminal-cut disposal can miss delayed queue enqueue.** A durable pre-cut identity may enter the in-memory queue after the first disposal pass.

Track these as separate terminal-lifecycle work. This spec must not worsen them, depend on fixing them, or claim they are solved.

## Invariants

### Durable authority

1. The child transcript remains the authority for attention content and resolution.
2. The delegate journal remains the authority for generation, outcome, and delivery.
3. `FinishNoAction` appends one packetless `delegate_run_finished` and transitions
   the exact claimed generation `running -> idle`.
4. No action never enters `PhaseSettling`.
5. The exact finalization claim remains live until no action commits or another terminal path wins.
6. Crash recovery never invents no action for an unfinished generation.

### Generation evidence

Each active stable generation has one process-local object owned by its controller runtime binding:

```go
type delegateGenerationEvidence struct {
    requirement  delegateCompletionRequirement
    outcome      delegateCompletionOutcome
    terminalSeen bool
    fallback     *delegateFinish
}
```

- `TriggerAttention` starts `attention_only`; every other trigger starts `report_required`.
- The common work-admission boundary escalates monotonically to `report_required` for owner, queued, follow-up, goal, hook, and steering work.
- System-only attention and notification work do not escalate.
- A terminal `communicate` sets `terminalSeen` monotonically. It does not replace the existing terminal packet path.
- `prepareNoAction` stores the ordinary evidence-bearing finish in `fallback`
  before attempting no action. Live recovery may use it; generation release
  clears the object.
- A stale callback must authenticate the exact lease and cannot mutate a later generation.

### Delivery

1. Explicit terminal `communicate` follows the existing reported-result path.
2. Eligible no action creates no delivery for that generation.
3. Existing error, cancellation, exhaustion, and stop paths retain their delivery behavior.
4. Finishing no action may release unrelated older delivery work; it creates no delivery keyed to its own generation.

## Alternatives

### Complete the existing no-action lifecycle — chosen

This reuses the existing trigger, disposition, fold validation, and controller finish branch. The medium-risk change is limited to attention admission/recovery, generation evidence, model-loop outcome, clean-exit enforcement, and claim-bound finish.

### Consume attention in the ordinary generation — rejected

Transcript presence does not prove that a provider request saw or acted on attention. Correct request-bound consumption would need new durable evidence and recovery rules.

### Deduplicate parent reports — rejected

Different generations may legitimately report equal text, and incident messages were not identical. Deduplication would corrupt explicit `communicate` semantics or durable delivery state.

### Prompt the model not to report — rejected

The runtime itself currently forces `communicate`; model wording is not a deterministic lifecycle signal.

## Design

### 1. Maintain one monotonic completion policy

The controller runtime binding owns `delegateGenerationEvidence`; the retained `Session` receives only lease-authenticated access.

Escalate the evidence `requirement` at the common admission boundary before
model projection whenever effective work is owner-authored or report-requiring.
This includes:

- bound delegate steering;
- queued, follow-up, user, steering-carrier, and goal continuation;
- blocked hook continuation;
- unblocked hook model context or user messages accepted for the current generation.

If an unblocked hook emits model context that requires action, force a continuation before finalization. Removing a steer from `live.pendingSteers` or completing a later system notification cannot downgrade the requirement.

### 2. Record an explicit no-action outcome

Do not infer no action from `attentionRun`, nil error, or transcript text.
Record the internal outcome `delegateCompletionOutcomeAttentionNoAction`
through the exact-lease controller evidence method.

Record it only when:

- the active entry is `EntryDelegateAttention`;
- the response is non-empty and has no tool call;
- `evidence.requirement == attention_only`;
- `terminalSeen == false`.

A generic `finishIdle` remains the root-notification outcome. A truly empty attention response uses the existing retry budget.

### 3. Enforce report requirements at every clean exit

No-tool routing may enforce `report_required` immediately, but it is not the authority. Before any clean `ProcessInput`, hook, or drain exit can proceed to finalization, run one shared gate:

- if a terminal `communicate` was seen, use the existing terminal path;
- if outcome is `delegateCompletionOutcomeAttentionNoAction` and requirement
  remains `attention_only`, proceed to packetless finalization;
- otherwise, consume the generation's one recovery nudge and continue the model;
- if that bounded recovery fails or is unavailable, use the existing missing-terminal/abnormal path.

The gate covers no-tool responses, tool-bearing observer handoff, notification yield, goal-controlled round-cap exit, hook continuation, and post-drain exit. A model request that binds owner work sees the escalated requirement in that same request. Re-running after the recovery nudge must preserve SubagentStop ordering and complete drain before finalization.

Any provider, tool, hook, transcript, drain, cancellation, exhaustion, attention-resolution, or stop error disqualifies no action and follows its existing path.

### 4. Resolve attention and select finalization

Retain this order:

1. `BeginRunFinalization` acquires and fences the exact claim while binding the
   sampled run error;
2. join quiet attention admitted before the claim;
3. execute `AttentionResolutionsForFinalization`;
4. snapshot final run error and generation evidence;
5. choose existing terminal/abnormal finish or no action.

No action requires:

- `evidence.outcome == delegateCompletionOutcomeAttentionNoAction`;
- `evidence.requirement == attention_only`;
- `terminalSeen == false`;
- nil run error.

Before calling the controller, build and store the ordinary evidence-bearing `delegateFinish` in `fallback`. This is the same finish the existing stop/error path would receive; the controller does not claim to reconstruct or semantically verify fields it does not own.

### 5. Commit no action through the controller claim

The runtime calls `prepareNoAction(claim, fallback)` and then
`FinishNoAction(claim)`.

Under the controller mutex, validate:

- the exact ordinary finalization claim is live, ready, and bound to the same lease;
- trigger is `TriggerAttention`;
- generation evidence records the explicit outcome, remains `attention_only`, and has not seen terminal communicate;
- phase is running, or stop won;
- no prepared terminal or bound attention resolution remains;
- a structurally valid fallback exists for the same lease.

On the running path, construct the fixed completed/no-action finish internally, append packetless `RunFinished`, and release the claim and generation. On a live stop race, pass the retained fallback through the existing stopped branch.

If the append fails, keep claim, evidence, fallback, and capacity live and
latch the existing stop-only finalization recovery path. Live recovery does not
retry no action directly; it can use the retained fallback when the existing
stop recovery closes the generation. A process crash may lose late
process-only operational evidence and follows existing `runtime_lost`/stopped
recovery; this spec makes no stronger crash guarantee.

Use existing locked event/release helpers. Reject missing, stale, mismatched, or unready claims.

## Durability and recovery

The successful path reuses the existing attention acceptance and `ResumeGeneration` protocol, followed by the explicit outcome, post-resolution selection, and claim-consuming packetless finish.

Crash behavior:

- before attention consumption: attention remains pending;
- consumed but missing `RunStarted`: existing generation-start recovery starts the exact generation;
- open generation without a completed live finalizer: existing runtime-loss recovery applies;
- after packetless `RunFinished`: restore folds idle/completed with no delivery for that generation;
- attention-resolution or no-action append failure: keep live finalization recovery latched;
- terminal communicate: existing prepared/unprepared behavior is unchanged by this spec.

Historical duplicate reports remain immutable.

## Job drain and observer semantics

`DrainJobTree` remains before finalization. It participates in the shared completion-exit gate but otherwise keeps its current terminal-cut and notification behavior. Newly appended attention IDs remain pending for later one-ID generations.

A packetless finish emits the ordinary revisioned delegate lifecycle update. Watches may react to that event; such watch delivery is distinct from an owner result notification.

## Tests

Follow `docs/developing-evener/testing.md`. Use scripted providers and deterministic barriers, not credentials, network, or sleeps.

### Incident regression

Drive one initial reported generation and one stable-owned shell attention whose model response is bare text. Assert:

- one provider request for attention;
- consumed attention;
- public completed/private `completed_no_action`;
- no packet, delivery ID, pending delivery, or second parent report for that generation.

### Completion policy

- steering bound before the first request escalates the requirement;
- late steering and steering bound inside drain escalate in the same request;
- queued/follow-up/user/goal work escalates;
- blocked hook continuation escalates;
- unblocked hook model context escalates and forces continuation;
- system-only notification leaves `attention_only` unchanged;
- monotonic escalation never downgrades.

### Clean-exit enforcement

For any clean exit with neither terminal communicate nor an eligible explicit no-action outcome, assert one bounded recovery nudge at:

- non-empty no-tool response;
- tool-bearing observer handoff;
- notification yield;
- goal round-cap exit;
- hook continuation;
- post-drain exit.

Then assert a final result or the existing missing-terminal/abnormal finish. Empty attention retains its current retry and outer recovery-nudge behavior.

### Controller and stop

- exact claim plus eligible evidence produces packetless no action;
- missing/stale/mismatched/unready claim is rejected;
- prepared terminal, terminalSeen, report-required, or run error disqualifies no action;
- live stop after claim uses retained task/worktree/scratch/usage/timing/warning fallback;
- running no-action never persists fallback;
- append failure retains claim/evidence/fallback/capacity for live recovery;
- crash after failed append follows the explicitly narrowed existing recovery guarantee;
- unrelated older delivery work may replay, but no delivery uses the no-action generation ID.

### Non-regression

- explicit attention `communicate` follows the current reported path exactly once;
- user-triggered no-communicate remains missing-terminal;
- root notification semantics are unchanged;
- lifecycle update and watch reaction remain distinct from owner result delivery.

### Gates

Run focused agent tests, then:

- `make lint`;
- `make vet`;
- `make test`.

Use an isolated clean worktree and preserve unrelated user work.

## Expected code boundaries

- `agent/session_lifecycle.go` and internal result types: explicit outcome and clean-exit gate;
- `agent/subagents.go`: outcome/nudge/finalization flow and hook escalation;
- `agent/delegate_tree_controller.go`, `delegate_tree_steer.go`, and `delegate_tree_finish.go`: generation evidence, monotonic requirement, and `FinishNoAction`;
- nearby deterministic tests;
- `docs/subagent-management/11-delegate-resource-model.md`: packetless `running -> idle` and completion requirement.

Do not change transcript or delegate-journal schemas.

## Compatibility and rollout

- No API, Appwire, frontend, transcript, or journal migration.
- Existing journals already understand `completed_no_action`.
- New behavior affects future attention generations only.
- No feature flag: the old attention runtime violates an existing contract.
- Monitor reported, no-action, and terminal-error counts.

## Acceptance criteria

1. The incident shape yields one task report and no bare-attention duplicate.
2. No action requires explicit outcome, `attention_only`, no terminal seen, nil error, and an exact claim.
3. Every report-requiring work source escalates through one monotonic policy.
4. Every clean exit enforces that policy, including tool-bearing, hook, and drain exits.
5. Explicit terminal communicate behavior is unchanged and never mistaken for no action.
6. Live stop retains existing operational fallback; crash guarantees are not overstated.
7. Error, cancellation, exhaustion, restore, delivery ordering, lifecycle updates, and watches retain existing behavior outside this finding.
8. No new persisted state, packet, queue, prompt dependency, or content heuristic is introduced.
9. Required gates pass in a clean worktree.

## Review record

### Earlier design review

Two reviewers reported seven unique significant issues and three minor ambiguities in two passes. A later source audit disproved the pending-ask finding: `ask_user` is unconditionally excluded from every subagent by `protectedGrantTools`. The ask-related design was removed and that finding was excluded; every other legitimate finding remains incorporated. The corrected earlier score is Reviewer A **5** to Reviewer B **3**, so Reviewer A still earns **5 points**.

### Simplification pass

Four read-only reviewers covered reuse, simplification, efficiency, and architectural altitude. Applied findings consolidated generation evidence, completion policy, alternatives, drain recap, and stale prose. Skipped findings that would broaden finalization refactoring, weaken distinct tests, alter gate execution, or remove required review provenance.

### Post-simplification adversarial competition

| Finding | Reviewer(s) | Decision | Correction |
|---|---|---|---|
| Accepted terminal result was only process-local | A, B | Accepted, high/critical | Removed terminal-result redesign; documented canonical defect separately and made no action depend only on `terminalSeen` |
| Cut revision missed delayed queue enqueue | A | Accepted, high | Removed cumulative-cut redesign from this fix; recorded adjacent defect |
| Stop fallback unavailable or unauthenticated in recovery | A, B | Accepted, high/medium | Bound ordinary fallback to generation evidence, used it only for live recovery, and narrowed crash guarantee |
| Same-round communicate capture was non-atomic | A | Accepted, medium | Removed latest-result redesign and recorded adjacent defect |
| Completion requirement missed hook/tool-bearing clean exits | A, B | Accepted, medium/high | Added hook escalation and one shared clean-exit gate covering every exit shape |
| Pending-ask fence conflicted with owed-generation recovery | B | Rejected, false finding | `ask_user` is excluded from every subagent; removed all ask/owed-generation scope |

Reviewer A found five legitimate significant issues. Reviewer B found three legitimate significant issues and one false finding; only the false finding is excluded. Every legitimate finding remains incorporated. Reviewer A wins **5–3** and earns **5 points**.
