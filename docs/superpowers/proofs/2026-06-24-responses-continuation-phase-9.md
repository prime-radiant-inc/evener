# Responses Continuation Phase 9 Proof

## Scope

Phase 9 completes deterministic real-session `responses_delta` behavior for fallback-capable OpenAI Responses paths.

This phase keeps the production registry disabled. It does not add live OpenAI tests, token-pressure accounting, raw-local export, or activation defaults for the runtime registry.

## Substrate Recheck

- Phase 4D proved stored-anchor metadata and basic delta shaping.
- Phase 7 classifies continuation-specific OpenAI Responses rejections without sending them to Chat Completions fallback.
- Phase 8 retries continuation rejection once with same-model full history before configured model fallback.
- Phase 9 reuses the existing OpenAI full-history serializer for fallback replay; it does not add a parallel sanitizer.

## RED Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9FallbackCapablePathProducesFullHistoryAnchor|TestSession_OpenAIResponsesContinuationPhase9RealOpenAIAdapterUsesFullHistoryWhenAnchorFingerprintMismatches|TestSession_OpenAIResponsesContinuationPhase9FallbackCapableFakePathCarriesFullHistorySidecar|TestSession_OpenAIResponsesContinuationPhase4DIIConsumesStoredAnchorAsDelta' -count=1 -v
```

Initial result: failed before sidecar attachment because the fallback-capable fake path still used full history instead of `responses_delta` with `FullHistoryFallbackMessages`.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationAnchorCandidate|TestSession_OpenAIResponsesContinuationPhase9.*Gate' -count=1 -v
```

Initial result: failed before delta eligibility gates because orphaned tool results, unsafe media content, and unsupported delta turn kinds could still pass through candidate selection.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9RetryThroughRealAnchorSelection' -count=1 -v
```

Initial result: failed before the real-session retry path had a fallback-capable delta request carrying the full-history sidecar.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9.*Disabled' -count=1 -v
```

Initial result: failed because the same live session selected a second `responses_delta` after an endpoint rejected the first continuation handle for the same provider, model, scope, policy, and stream path.

The sanitizer proof test passed when added after sidecar/retry work, showing no extra sanitizer glue was required.

## GREEN Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase9|TestResponsesContinuationAnchorCandidate|TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay|TestFallbackChain_Continuation' -count=1 -v
```

Result: pass.

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_Stream_Continuation|TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages|TestAdapter_ClassifyResponsesError' -count=1 -v
```

Result: pass.

## Commits

- `265d9a89 feat(agent): enable fallback-capable responses delta sidecar`
- `52ed0aea feat(agent): gate responses continuation delta inputs`
- `977d022d test(agent): prove responses continuation retry real path`
- `882cad19 test(agent): prove responses continuation sanitizer fallback`
- `8fe59545 feat(agent): add session-local responses continuation disablement`

## Contracts Proven

- Real-session fallback-capable OpenAI paths can select `responses_delta` while preserving a full-history fallback sidecar.
- The real OpenAI adapter fixture remains full-history when the helper anchor fingerprint does not match the request planner fingerprint.
- Delta selection rejects unsupported turn kinds, orphaned tool results, unsafe media content, unsafe reasoning/provider-hosted content, and intervening non-anchorable assistant turns.
- Tool-result deltas are eligible only when their `call_id` links to a tool call in the selected anchor assistant turn.
- A continuation rejection after real-session anchor selection retries same-model full history before configured model fallback.
- Full-history fallback replay sanitizes malformed historical OpenAI function-call arguments through the existing serializer and preserves the linked function-call output.
- After endpoint-level continuation rejection, the same live session disables further delta attempts for the same provider, model, endpoint family, storage scope, storage policy, and stream path.
- Disabled state is runtime-only and does not leak to a new session or to a changed storage scope or storage policy.

## Remaining Work

- Phase 10 must add token-pressure accounting for delta and fallback paths.
- Phase 11 must add raw-local export proof.
- Phase 12 must finish rollout activation/defaults.
- Runtime continuation registry entries remain disabled until Phase 12 activation proof.
