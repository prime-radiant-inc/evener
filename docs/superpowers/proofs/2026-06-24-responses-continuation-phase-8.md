# Responses Continuation Phase 8 Proof

## Scope

Phase 8 adds one same-Responses-endpoint full-history retry for continuation-specific rejection before configured model fallback.

This phase uses package-local session tests with a test-only delta-shaped request. It does not enable broad real-session `responses_delta` selection, add storage-quota demotion, or make live provider calls.

## Substrate Recheck

- Phase 7 surfaces continuation-specific OpenAI Responses rejections instead of sending them to Chat Completions fallback.
- `callModelWithFallback` is the existing session harness that owns configured model fallback ordering.
- `llm.Request.FullHistoryFallbackMessages` is the paired full-history message set used when a delta-shaped request must replay safely.
- Model fallback must not send `previous_response_id` or continuation metadata when the model changes.

## RED Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestFallbackChain_Continuation|TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry' -count=1 -v
```

Initial result: failed.

- `TestFallbackChain_ContinuationRejectionRetriesFullHistoryBeforeModelFallback` returned the original `previous_response_not_found` error instead of retrying full history.
- `TestFallbackChain_ContinuationRecoveryFailureThenModelFallback` skipped same-model recovery and did not produce the expected configured fallback result.
- `TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry` passed, proving the guard scenario was already non-regressing.

## GREEN Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestFallbackChain_Continuation|TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry|TestFallbackChain_PermanentErrorTriesNextModel|TestFallbackChain_EndpointFallbackErrorTriesNextModel' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- A `responses_delta` request with `previous_response_id` and a `previous_response_not_found` provider error retries the same provider/model once as `full_history_fallback`.
- The same-model retry clears `PreviousResponseID`, `ConversationID`, continuation metadata, and the fallback message sidecar.
- The same-model retry uses `FullHistoryFallbackMessages`, not the delta-only message set.
- If same-model full-history recovery fails, configured model fallback runs afterward.
- Configured model fallback from a delta-shaped request uses full-history messages and clears continuation state when the model changes.
- Generic non-continuation permanent errors skip same-model full-history recovery and keep using the configured model fallback path.
- Existing permanent-error and endpoint-fallback model fallback tests still pass.

## Remaining Work

- Phase 9 must repeat this retry through real session anchor selection and `responses_delta`.
- Phase 9 must cover real-path item-kind gating, `call_id` linkage validation, intervening non-anchorable assistant handling, media/provider-hosted/reasoning gating, and full-history sanitizer replay coverage.
- Runtime continuation registry entries remain disabled until later rollout proof phases.
