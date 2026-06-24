# Responses Continuation Phase 5A Proof

## Scope

Phase 5A adds attempt-group identity and the minimum matching transcript/API/raw-log fields for session-owned provider attempts.

This phase keeps the existing single-attempt path only. Adapter-callable attempt recording, endpoint fallback records, retry classification, same-endpoint full-history retry, and production continuation enablement remain out of scope.

## Substrate Recheck

- `singleAttemptRequestMetadata` is the existing Phase 1A stamping boundary. Phase 5A reuses it to allocate the attempt group id and keep `AttemptIndex=1`.
- `callModelWithFallback` still wraps the provider dispatch in `llm.WithAPILogAttemptContext`, so the same metadata reaches `llm.APILogger`.
- `logAPICall` still writes the session-owned transcript `api_call` after dispatch.
- `llm.APILogger` writes raw response bodies through `writeRawResponse` and stream-error raw bodies through `apiLogStream.logError -> writeRawError`.
- Phase 0A's terminal-attempt `final_attempt_count` shape is preserved for the single terminal attempt record.

## RED Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./agent -run 'TestAPILogEntry_AttemptFieldsRoundTrip|TestAPILogger.*Attempt|TestTranscriptContinuationMetadataRoundTrips|TestSession_SingleAttemptMetadataRecorded|TestSingleAttemptRequestMetadataKeepsAttemptCountersOffRequest' -count=1 -v
```

Result before implementation: failed to build because `AttemptGroupID` was absent from `APILogEntry`, `APILogContext`, `APIRawLogEntry`, `transcript.APICall`, and `ModelAttemptMetadata`, and raw entries had no attempt fields.

## GREEN Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./agent -run 'TestAPILogEntry_AttemptFieldsRoundTrip|TestAPILogger.*Attempt|TestTranscriptContinuationMetadataRoundTrips|TestSession_SingleAttemptMetadataRecorded|TestSingleAttemptRequestMetadataKeepsAttemptCountersOffRequest' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- Session-owned single attempts allocate a stable `ag_` attempt group id at the existing Phase 1A stamping boundary.
- Transcript `api_call` records persist `attempt_group_id`, `attempt_index`, `attempt_count`, `final_attempt_count`, `history_mode`, and existing redacted handle fields.
- `llm.APILogger` `api.jsonl` records round-trip `attempt_group_id` with the existing attempt fields.
- Raw HTTP log records carry matching attempt identity, attempt counts, final attempt count, and history mode.
- Stream errors carrying raw HTTP bodies preserve the same attempt metadata in `api-raw.jsonl`; this covers continuation-style rejection delivery after stream setup.
- Production registry entries remain unchanged; no live provider calls are made.

## Remaining Work

- Phase 5B adds adapter-callable attempt recording for endpoint fallback.
- Phase 6 and later add fallback cloning, continuation rejection classification, retry ordering, and complete real-session delta behavior.
