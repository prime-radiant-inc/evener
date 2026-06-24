# Responses Continuation Phase 7 Proof

## Scope

Phase 7 adds OpenAI Responses continuation error classification and applies it before Chat Completions fallback for continuation attempts.

This phase does not add same-Responses-endpoint full-history retry. Phase 8 owns that retry ordering.

## Substrate Recheck

- Phase 0B proved public OpenAI returns `previous_response_not_found` for invalid `previous_response_id` anchors.
- Phase 0B also proved Codex rejects `previous_response_id`, so Codex runtime continuation remains blocked.
- Phase 5B adapter attempt recording still covers fallback-capable OpenAI paths.
- Phase 6 Chat fallback cloning still supplies full-history messages to Chat Completions when fallback is allowed.

## RED Evidence

Reconstructed against the parent commit (`eac5b67f`) with the Phase 7 adapter tests copied into a temporary worktree:

```sh
cp /Users/jesse/git/prime-radiant-inc/serf/.worktrees/subagent-side-view-chrome/llm/providers/openai/adapter_test.go /tmp/serf-phase7-red/llm/providers/openai/adapter_test.go
cd /tmp/serf-phase7-red
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_ClassifyResponsesError|TestAdapter_Stream_Continuation.*Fallback|TestAdapter_Stream_ModelEndpointContinuationFallback' -count=1 -v
```

Initial result: failed to build because `llm.ResponsesErrorClass`, the `ResponsesError*` constants, and `Adapter.ClassifyResponsesError` did not exist.

## GREEN Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_ClassifyResponsesError|TestAdapter_Stream_Continuation.*Fallback|TestAdapter_Stream_ModelEndpointContinuationFallback|TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages|TestAdapter_Stream_Records.*FallbackAttempts' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- `previous_response_not_found` with `PreviousResponseID` classifies as `continuation_rejected`.
- Generic invalid request errors with `PreviousResponseID` do not classify as continuation rejection.
- Model endpoint mismatch errors with `PreviousResponseID` still classify as `model_endpoint` and allow Chat fallback.
- `previous_response_not_found` without `PreviousResponseID` does not classify as continuation rejection.
- Immediate continuation rejection returns the Responses error and does not call Chat Completions.
- Empty Responses streams on continuation attempts return the empty-stream error and do not call Chat Completions.
- Phase 6 full-history Chat fallback cloning still works for fallback-capable immediate and empty-stream paths.
- Phase 5B fallback attempt recording still works for fallback-capable paths.

## Remaining Work

- Phase 8 must add same-Responses-endpoint full-history retry before model fallback.
- Phase 9 must complete broad real-session delta behavior and replay sanitizer coverage.
- Runtime continuation registry entries remain disabled until later rollout phases.
