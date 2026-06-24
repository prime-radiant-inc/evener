# Responses Continuation Phase 0A Proof

Date: 2026-06-24
Scope: OpenAI Responses continuation Phase 0A-audits

## Registry Defaults

Checkable line: `DefaultResponsesContinuationSupportRegistry` has production rows for `openai_public` and `openai_codex`, and both rows are disabled no-op defaults.

Evidence:

- `llm/responses_continuation.go` defines `ResponsesEndpointFamilyOpenAIPublic = "openai_public"` and `ResponsesEndpointFamilyOpenAICodex = "openai_codex"`, and `DefaultResponsesContinuationSupportRegistry` returns one row for each family.
- `llm/responses_continuation.go` builds both production rows with `disabledResponsesContinuationSupport`, which only sets `EndpointFamily`; therefore `Enabled=false`, `StorageShapeProven=false`, `ProductionPathProven=false`, `MaxAnchorAgeSeconds=0`, `StorageShapeProofID=""`, and `ProductionPathProofID=""`.
- `llm/responses_continuation_test.go:TestDefaultResponsesContinuationSupportRegistryDisabled` checks both production rows and every disabled/default field.
- `llm/responses_continuation_test.go:TestResponsesContinuationSupportForUnknownFamilyDisabled` checks that unknown endpoint families resolve to disabled support instead of implicitly enabling continuation.
- `llm/responses_continuation_test.go:TestDecideResponsesContinuationRequiresAutoEnabledAndAnchorAge` checks that `auto` still chooses `full_history` when support is disabled or anchor age is unbounded.
- `agent/session_openai_continuation_phase0a_test.go:TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory` proves the runtime no-op path: `auto` plus disabled public OpenAI registry sends a full-history OpenAI Responses request with no `previous_response_id` and `store:false`.

Verdict: Phase 0A registry defaults are production no-op defaults for public OpenAI and Codex OpenAI Responses endpoints.

## Serialization Audit

Checkable line: reservation required: yes

Evidence:

- `agent/session_lifecycle.go:274-317` starts one `ProcessInputKind` drain loop and calls `processOneInput` sequentially inside that invocation.
- `agent/session_lifecycle.go:491-527` marks state as processing and resets per-turn state, but it does not reject a second external `ProcessInputKind` call while the first is in progress.
- `agent/session_lifecycle.go:557-594` prepares one request, dispatches it, then logs one round in order.
- `agent/session_model_call.go:52-126` snapshots `s.history` under `s.mu`, repairs/context-manages it, expands history, and builds the request.
- `agent/session_model_call.go:306-371` calls the primary model and configured fallbacks sequentially using one API-log context for the round.
- `agent/session_queue.go:173-203` queues typed input, and `agent/session_lifecycle.go:361-373` drains queued input after interruption. Queue behavior helps UI callers avoid concurrent turn entry, but it is not a Session API reservation.
- `agent/session.go:49-71` documents the turn loop as the primary mutable-state owner while external callers race under locks. It does not prove `ProcessInputKind` is single-flight.

Verdict: Current code is serialized within one `ProcessInputKind` invocation, and fallback dispatch is sequential. The public Session API does not provide a history-base reservation spanning future anchor selection to dispatch. Phase 4C must add a reservation or single-flight guard before Phase 4D/9 rely on an immutable history base.

## Logging Audit

Checkable line: Phase 5A should use terminal-attempt `final_attempt_count` on the terminal attempt record, not a separate group-summary record.

Evidence:

- `llm/apilog.go:20-24` carries only `SessionID` and `Round` in `APILogContext`.
- `llm/apilog.go:36-85` API/raw log records lack attempt group, attempt index, history mode, and final attempt count fields.
- `llm/apilog.go:145-189` logs one API entry per wrapped complete/stream attempt and raw bodies from the same attempt.
- `llm/apilog.go:222-255` appends API/raw log records and never rewrites prior lines.
- `agent/transcript/transcript.go:60-78` transcript `api_call` records lack attempt metadata.
- `agent/transcript/transcript.go:244-280` appends `api_call` records with monotonic transcript sequence numbers and never rewrites prior lines.
- `agent/session_model_call.go:375-410` writes one transcript `api_call` for the final request/response currently visible to the session.

Chosen Phase 5A shape:

- Future provider attempt records carry `attempt_group_id`, 1-based `attempt_index`, `attempt_count`, and `history_mode`.
- Non-terminal attempts may emit `attempt_count=0` and no `final_attempt_count`.
- The terminal attempt record carries `final_attempt_count=N`.
- Single-attempt groups use `attempt_index=1`, `attempt_count=1`, and `final_attempt_count=1`.
- This matches append-only transcript/API/raw logs and avoids a second group-summary record kind.

Verdict: Phase 5A should attach final group cardinality to the terminal attempt record so append-only logs remain self-contained without rewriting earlier attempt records.
