# Job Cancellation Presentation

## Status

Approved after competitive adversarial review and simplify-code review. This is a frontend transcript-reader change. Backend lifecycle and persistence semantics remain unchanged.

## Problem

A confirmed parent-driven shell stop is serialized as:

```text
status="cancelled" reason="stopped_by_parent" exit_code="-1"
```

`status` is the logical lifecycle result. `exit_code` is the physical process wait result after termination. `steeringClassify.ts` currently checks every nonzero raw exit before it checks status, so this coherent cancellation renders as a red error:

```text
error  Job cancelled  Run repository lint, vet, and test gates · exit -1 · stopped_by_parent
```

The reader also treats `-1` inconsistently: the compact summary displays it from the raw attribute, while `optionalNonNegativeInteger` rejects it from expanded typed metadata.

## Existing contracts

- `agent/internal/jobstore/record.go` defines `completed`, `failed`, `cancelled`, `stopped`, and `exhausted` as distinct terminal statuses.
- `agent/job_shell.go` gives an accepted stop precedence over timeout, wait error, signal death, and exit code while retaining the physical wait result.
- `docs/job-control.md` makes status the primary branch field. `cancelled` means an intentional, confirmed stop. `stopped` means work did not complete and Evener could not attribute it to normal failure or confirmed cancellation.
- Activity and task surfaces already avoid treating cancellation as failure. They have separate live/backend authority and are outside this change.
- `NotificationCard` reserves amber for attention and red for failure; success and neutral notifications recede without a chip.

## Goals

1. Interpret job-notification lifecycle status before physical exit code.
2. Render explicit `cancelled` as neutral settled non-success.
3. Keep every explicit `stopped` outcome as warning/attention.
4. Keep explicit and compatibility-derived failures red.
5. Parse status authority and signed exit once per job notification, then reuse that analysis for tone, summary, and metadata.
6. Preserve signed exit, reason, and raw text in expanded diagnostics.
7. Preserve historical delegate, watch, concern, and observer-callback behavior.

## Non-goals

- Do not change backend statuses, reasons, signaling, persistence, notification markup, AppWire, generated files, or activity projection.
- Do not add a backend tone or other UI-specific field.
- Do not treat cancellation as success.
- Do not change activity, task, subagent, rail, CSS, or generic steering behavior.
- Do not remove raw evidence.

## Ownership and implementation boundary

Backend status, reason, and exit code remain authoritative facts. Historical transcript compatibility belongs to the transcript parser, not to live activity formatting.

Add a private, React-free analysis inside `steeringClassify.ts`, adjacent to `parseJobNotification`:

```ts
type JobDisposition = "success" | "failure" | "cancelled" | "stopped" | "unknown";

interface JobNotificationAnalysis {
  disposition: JobDisposition;
  exitCode?: number;
}

function analyzeJobNotification(
  attrs: Record<string, string>,
  communicate: CommunicateEnvelope | null,
): JobNotificationAnalysis;
```

The analyzer:

1. trims and lowercases each candidate before selection;
2. selects the first nonempty value from outer `status`, outer `event`, then communicate status;
3. parses `exit_code` once as an exact signed base-10 safe integer;
4. classifies from normalized status plus parsed exit.

`reason` is preserved as metadata and summary copy but does not determine disposition. This follows the status-first contract and keeps every explicit `stopped` outcome distinct from confirmed `cancelled`.

The existing shared `notificationTone` remains unchanged for delegate notifications and durable observer-callback replay. Job-specific authority and disposition mapping stay inside `parseJobNotification`, preventing observer events from masking communicate status.

## Classification contract

| Authoritative status | Parsed exit | Disposition | Tone |
|---|---:|---|---|
| `failed`, `error`, `exhausted`, or historical status containing `fail` | any | `failure` | error |
| `cancelled` | any | `cancelled` | neutral |
| `stopped` | any | `stopped` | warning |
| `completed` or `done` | absent or `0` | `success` | success |
| `completed` or `done` | nonzero | `failure` | error |
| absent or unknown | nonzero | `failure` | error |
| absent or unknown | absent or `0` | `unknown` | neutral |

Explicit `cancelled` and `stopped` always outrank exit code. `cancelled + stopped_by_parent + -1` is neutral cancellation. `stopped + stopped_by_parent + -1` remains warning because status says cancellation was not confirmed.

A malformed exit is absent. It never appears in typed metadata or compact summary. An explicit failure status still produces error tone when its exit attribute is malformed.

## Job-notification presentation

`parseJobNotification` computes one `JobNotificationAnalysis` and uses it for all job-specific consumers.

### Tone

- failure → `error`;
- stopped, watch/watch-send, or communicate concerns → `warning`;
- success → `success`;
- cancelled or unknown → `neutral`.

Watch and concern rules remain unchanged.

### Compact summary

1. Keep decoded description, falling back to a non-generic job type.
2. Show `exit N` only when disposition is failure and the one parsed exit is defined and nonzero.
3. Show reason only for error or warning tones.
4. Confirmed cancellation therefore shows only the description; it omits `exit -1` and `stopped_by_parent`.
5. A stopped timeout shows `run_timeout` but not its termination sentinel.
6. A genuine signal/nonzero failure may show its parsed exit and reason.

The title remains `Job cancelled`.

### Expanded and raw diagnostics

Keep `optionalNonNegativeInteger` for nonnegative counters such as `output_bytes`. Assign `ParsedNotification.exitCode` from the analyzer's signed value. `NotificationMetadata` already accepts negative numbers, so expanded details show `Exit code -1`. Raw notification text remains unchanged.

## Compatibility

- Missing or unknown status retains nonzero-exit failure fallback.
- Historical `completed + nonzero` remains error.
- Blank outer status does not mask a meaningful event or communicate status.
- For job notifications, explicit outer status/event outranks communicate status.
- Delegate notifications retain current shared tone behavior.
- Historical observer callbacks retain communicate-first success detection and success-to-warning coercion.
- Watch, watch-send, concerns, malformed blocks, and success behavior remain unchanged.
- No wire negotiation or migration is required.

## Deterministic tests

Extend `steeringClassify.test.ts` with:

- exact `cancelled/stopped_by_parent/-1` → neutral, description-only summary, typed exit `-1`;
- every explicit `stopped`, including `stopped_by_parent`, `cancelled`, and `run_timeout` → warning without compact sentinel;
- signal/nonzero/wait-style failure → error with parsed exit and reason;
- completed/nonzero and missing-status/nonzero compatibility → error;
- explicit failed + malformed exit → error, no compact exit, no typed exit;
- unknown + malformed exit → neutral;
- whitespace-only status + failed event → error;
- absent outer status + communicate cancelled/stopped → consistent tone and summary;
- explicit outer cancelled + communicate done → outer status wins;
- historical observer callback with communicate done → warning;
- existing watch and concern tones remain warning.

Extend `NotificationCard.test.tsx` using existing Chai-compatible idioms (`getAttribute`, `textContent`, truthiness, and null checks). Assert that a neutral cancelled card:

- has `data-tone="neutral"` and no chip;
- keeps title and description in the collapsed row;
- omits cancellation sentinel and routine reason while collapsed;
- shows Status, Reason, and signed Exit code after expansion;
- preserves the raw block.

## Acceptance criteria

1. The observed confirmed parent cancellation renders neutral with no colored chip.
2. Its collapsed row contains `Job cancelled` and its description, not `exit -1` or `stopped_by_parent`.
3. Expanded metadata and raw disclosure retain status, reason, and signed exit.
4. Every explicit stopped outcome renders warning.
5. Explicit failures, exhaustion, signal failures, fallback nonzero exits, and contradictory completed/nonzero payloads render error.
6. Tone, summary, and metadata consume one normalized analysis and one parsed exit.
7. Delegate, watch, concern, success, observer-callback, and malformed-notification behavior does not regress.
8. Touched frontend files pass Biome, focused tests pass, and `make test-web`, `make lint`, `make vet`, and `make test` exit zero.
9. No backend, protocol, generated, activity, or CSS files change.

## Review resolution

Accepted review corrections: keep `stopped` distinct from `cancelled`; scope outer-first authority to job notifications; normalize candidates before selection; parse and classify once; reject malformed compact exits; use supported test matchers; bootstrap worktree dependencies before `npx`; format before final focused tests and commits.

The proposal to reconcile or change backend activity outcome handling was rejected as outside this defect. The stronger altitude correction was applied instead: activity remains untouched, and historical compatibility analysis is private to the transcript reader.
