# Delegate Budget Truthfulness Design

Date: 2026-07-15
Status: Approved
Tracker: #20

## Purpose

Serf must never present an exhausted delegate turn as successful completion. A
delegate approaching its lifetime limit also needs enough warning to report its
state before the final turn is consumed.

This design corrects budget accounting and terminal-state truthfulness. It does
not add an orchestration policy.

## Current Behavior

- Delegates default to `MaxTurns = 500` when the caller does not provide a
  positive override.
- `MaxTurns` counts accepted user inputs across the session lifetime.
- `MaxToolRoundsPerInput` separately limits model/tool rounds inside one input.
- Reaching either limit emits `TURN_LIMIT`, transitions the session to idle, and
  returns without an error.
- Job storage has terminal states for completed, failed, cancelled, and stopped,
  but no exhausted state.

The nil-return/idle behavior lets callers mistake budget exhaustion for a normal
turn boundary or successful child result.

## Decisions

### Keep the existing delegate default

The default lifetime budget remains 500 turns. Serf will not guess a higher
number without observed distribution data. Callers may still supply a positive
override.

Serf keeps these budgets distinct:

| Budget | Meaning | Exhaustion effect |
|---|---|---|
| `MaxTurns` | Accepted external inputs over the session lifetime | Current job is exhausted; delegate becomes non-resumable |
| `MaxToolRoundsPerInput` | Model/tool rounds within one input | Current job is exhausted; delegate remains resumable |
| Goal iteration/round limits | Autonomous goal continuation work | Existing goal semantics remain unchanged by this spec |

### Five-turn warning

When a bounded session has exactly five lifetime turns remaining, Serf injects
one steering message before the model request:

> You have 5 turns remaining in this session. Report your current status and
> evidence to your parent soon, and ask for direction if the task cannot be
> completed safely within the remaining budget.

The warning:

- is injected once per session lifetime;
- does not itself consume a turn;
- is persisted or otherwise derivable so restore cannot duplicate it;
- applies to root and child sessions with a positive `MaxTurns`, although its
  parent-report wording is included only when the session has a parent;
- is emitted through the normal steering/transcript path so it is auditable.

If a session is restored or configured after the threshold has already passed,
Serf injects the warning once at the next accepted turn rather than omitting it.

### Exhausted is a terminal job outcome

Add `exhausted` as a durable terminal job status. It is distinct from:

- `completed`: the agent delivered the requested result;
- `failed`: execution failed for a reason other than a configured budget;
- `stopped` or `cancelled`: an external actor ended execution.

Both lifetime-turn and tool-round exhaustion finish the active job as
`exhausted`. They must not be mapped to completed, idle-success, or an empty
successful result.

For lifetime exhaustion, the delegate handle becomes non-resumable with reason
`turn_budget_exhausted`. For per-input tool-round exhaustion, the delegate
returns to idle/resumable after its exhausted job is durably recorded.

The parent receives a terminal notification that names the exhausted budget,
configured limit, and whether the delegate remains resumable.

### Preserve partial evidence

An exhausted job retains the assistant text, tool evidence, transcript
reference, and progress accumulated before the limit. Exhaustion changes the
outcome classification; it does not discard the handoff material.

### Parent-visible contract

All Serf surfaces that expose job state use the same status:

- durable job-store records;
- `job_status` and `job_list`;
- delegate tool results and terminal notifications;
- AppWire projections and Hub/TUI renderers;
- transcript lifecycle events.

No surface may infer success from a nil Go error when the durable outcome is
exhausted.

## Error and Crash Behavior

- The exhausted terminal record is persisted before notification delivery.
- A crash after persistence but before delivery replays the pending exhausted
  notification without rerunning the child turn.
- A failure to persist the terminal record is a job failure, not an exhausted or
  completed outcome.
- Repeated notification delivery remains deduplicated by the existing terminal
  generation rules.

## Testing

Use scripted providers and real Serf session/job plumbing.

Cover:

- warning at five remaining, exactly once;
- warning after restore without duplication;
- warning does not increment `MaxTurns` accounting;
- lifetime exhaustion produces terminal `exhausted` and a non-resumable child;
- tool-round exhaustion produces terminal `exhausted` and a resumable child;
- partial text/evidence remains readable;
- job store, tools, notifications, AppWire, and transcript projections agree;
- crash/replay preserves the exhausted outcome;
- ordinary completed, failed, stopped, and unlimited sessions do not change.

## Scope Lock

This spec does not:

- change the 500-turn delegate default;
- change goal iteration limits or goal continuation behavior;
- change provider retry limits;
- add Superpowers behavior or configuration;
- add an automatic escalation/reslicing policy;
- treat exhaustion as success for compatibility.
