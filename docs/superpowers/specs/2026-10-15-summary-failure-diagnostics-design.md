# Safe summary failure diagnostics

## Goal

Fix [summary failures discard actionable details](https://github.com/prime-radiant-inc/evener/issues/4655). When conversation summarization fails, Jesse should be able to identify the failing model route and failure category without enabling full provider-body logging or exposing conversation contents.

This change diagnoses failures. It does not claim to fix the unknown provider failure that prompted the ticket.

## Scope and non-goals

Cover every path that emits `summarizerFailureWarning`: automatic compaction, explicit compaction, checkpoint-pred's summary phase and the session-log strategy. Include failures from partner-message condensation that propagate into those paths.

Preserve existing model selection, refusal learning, retry counts, fallback eligibility, cancellation, warning visibility, and history/checkpoint retention. A successful fallback still completes normally without a summary-failure warning. Keep predictive-checkpoint recovery's informational treatment unchanged.

Do not enable full API logging, call live providers in tests, restart a session, deploy a binary, add a new log store, or redesign warning widgets. The existing generic provider-error classifier remains unchanged for unrelated callers.

## Safe diagnostic data

Produce one bounded, typed diagnostic for each emitted summary failure:

- stable warning code identifying a summary failure;
- configured provider instance and model for the route actually selected, including an internally rerouted session model;
- terminal failure category, distinguishing provider error kinds, timeout, cancellation, configuration, local input budget rejection, rejected summary output and unknown failure;
- HTTP status when supplied by a typed provider error;
- retryability when known from a typed failure, with unknown distinct from permanent;
- a controlled local validation reason for empty, malformed, output-limited or uncondensable summary results;
- safe prior-route failure details when a cheap-model refusal and failed session-model fallback are joined, so a cheap refusal cannot be presented as the terminal model's failure.

Route information comes from request routing, never a provider's message or response body. No diagnostic labels a configured-but-skipped route as attempted. A failure before any model is selected carries no invented provider/model identity. A local input-budget rejection is identified as pre-dispatch rather than a provider rejection.

Only authored route identifiers, controlled enum values, validated numeric fields and booleans enter this record. Bound and encode identifier strings so a model name cannot inject lines or unbounded log data. Omit arbitrary provider error codes, raw `err.Error()` output, raw finish reasons, URLs, headers, bodies, prompts, summary text and credentials. Preserve error unwrapping/classification for existing callers.

The implementation may choose the smallest typed error/result representation that satisfies these rules. It must not infer the terminal failure from a joined error's aggregate severity or add a second retry/routing owner.

## User-visible behavior

Reuse the existing warning message, source, title and hint fields. The message identifies the actual route, category and available HTTP status. The title and advice distinguish failures such as rate limits, spent allowance, authentication/access, timeout, server failure, local context budget and rejected output. Unknown failures say that the cause is unknown; they do not diagnose credentials or API endpoint compatibility.

Advice identifies the fast/cheap model when that is the failed route. It must not promise that switching the main model repairs an independently configured summary model. It describes retained history/checkpoint behavior without claiming a failed summary succeeded.

The same warning travels through the existing CLI, TUI, AppWire, browser and native warning surfaces and Notification hook path. No new widget, action, visibility level or attention policy is introduced. Structured fields are available to the runtime and its log; clients that render the existing message/title/hint need no new renderer.

## Durable evidence and ownership

Use the existing process diagnostic logger and private daemon stderr log, independent of the opt-in API logger. Record the session identity, summary-failure code and safe structured diagnostic once when the session publishes the warning. A delegate's record names that delegate's session even when it shares the root daemon's log.

The implementation choice is to reuse the daemon launcher's existing private stderr file, `run/logs/daemon-<session-id>.log`, rather than extend the API log or create another store. The launcher remains responsible for that file and its retention lifecycle: it retains at most the latest 1 MiB at launch, with no continuous size cap during a run. This is best-effort process logging, not a new fsync-backed audit guarantee. No second file owner or per-request body capture is introduced. Plain CLI execution writes the same safe diagnostic to stderr; persistence outside a daemon remains the invoking caller's responsibility.

Logging is best-effort and must not turn a summary failure into a turn failure, suppress the warning, replace retained history, or block later successful compaction when the sink is unavailable. This change does not add cooldown or deduplication: later genuinely failed compactions can still emit warnings and their compact records.

## Preservation and recovery

A failed summary preserves the same useful state as the current implementation: existing history or the deterministic checkpoint already produced by that operation. The authoritative conversation transcript is untouched by diagnostic logging. The next eligible compaction retains its current opportunity to run; when the provider succeeds, a real summary is installed and normal model work continues. No new sticky failure state or manual recovery step is added.

## Verification

Read `docs/developing-evener/testing.md` before changing tests. Use deterministic scripted providers or an in-process HTTP fixture at the LLM boundary, exercising real routing, context management and session publication below that boundary. Assertions use typed fields, request counts, selected routes, persisted data and state transitions, not wording.

1. Red-first producer tests: typed HTTP failures, timeouts/cancellation, configuration and local budget failures, and each local summary validation reason produce correct safe details. Include an opaque canary in provider message, raw body and error-code fields and prove it never reaches any warning field or diagnostic log.
2. Routing tests: cheap-only failure, no configured cheap model, unavailable/refusal-latched cheap model, cheap refusal followed by a different session-model failure, and cheap validation rejection followed by fallback failure. Verify exact provider call order and distinguish selected routes from actual dispatch. Preserve existing refusal learning and retry behavior.
3. All summary-warning entry points retain the privacy guarantee, including partner-message condensation. Preserve the predictive checkpoint's existing successful deterministic recovery and informational code.
4. Session-level journey with full API logging off: drive real compaction failure, collect its published warning, read the private diagnostic log through the production logging sink, and verify session identity, code, route, kind, status and retryability. Feed the emitted event through the real shared projection and client warning adapters; verify safe route/category data survives. Exercise existing browser/native warning rendering with that producer-shaped payload, without hardcoding production copy.
5. Recovery journey: the same session next receives a valid summary. Verify real summary installation and continued model work, preserved human messages/transcript, and no new failure record on successful compaction or successful fallback. Explicitly test that an unavailable diagnostic sink does not change the session outcome.
6. Run focused context-manager, cheap-model, diagnostic, event, session and projection tests, plus affected client typecheck/rendering checks. Run the relevant race checks and changed-package lint. Leave full repository gates to CI, per repository policy.

Update the owning product documentation and the S09/S21 subsystem rows with the diagnostic owner, safe data boundary, log location, affected surfaces and unchanged recovery behavior.

## Baseline

The isolated lane starts from `26960d52c0f9d0f01187321d2bdf312d8d0c01c6`. The focused baseline command `go test ./agent/internal/contextmgr ./agent/internal/cheapmodel ./agent/diagnostic ./agent/events` passes. The unrelated main-checkout file `mobile-native/scripts/e2e-transcript.mts` is outside this change.
