# Responses Continuation Phase 1A Proof

Date: 2026-06-24
Scope: OpenAI Responses continuation Phase 1A-schema and Phase 1A-attempt

## Schema Compatibility

Checkable line: continuation schema fields are optional and old transcripts/API logs without them remain readable.

Evidence:
- `agent/transcript_test.go:TestTranscriptContinuationMetadataRoundTrips`
- `agent/transcript_test.go:TestTranscriptWriter_AppendAPICallWritesValidLine`
- `agent/transcript_test.go:TestReadTranscriptFull_ParsesAllLineTypes`

Verdict: old records remain readable and are non-anchorable by default because anchor-eligibility fields are absent.

## Request And API Log Control Plane

Checkable line: `llm.Request` carries continuation control-plane metadata out-of-band, and `BuildAPILogRequest` projects only structured/redacted provider-state metadata.

Evidence:
- `llm/apilog_test.go:TestBuildAPILogRequest_IncludesContinuationMetadata`
- `llm/apilog_test.go:TestAPILogEntry_AttemptFieldsRoundTrip`
- `llm/apilog_test.go:TestAPILoggerWritesJSONL`
- `llm/apilog_test.go:TestAPILoggerWrapStreamWritesRawLogOnFinish`

Verdict: Phase 1A adds schema and projection only; it does not generate hashes, add attempt groups, or select `responses_delta`.

## Single-Attempt Metadata

Checkable line: ordinary single-attempt model calls stamp `AttemptIndex=1`, `AttemptCount=1`, `FinalAttemptCount=1`, `HistoryMode=full_history`, and assistant turns receive response metadata from the successful final attempt.

Evidence:
- `agent/session_test.go:TestSession_SingleAttemptMetadataRecorded`
- `agent/session_test.go:TestAssistantTurn_CapturesUsageAndResponseID`
- `agent/session_test.go:TestSession_TranscriptAPICallRecordsFullToolDefinitions`

Append-assistant audit:
- `agent/session_lifecycle.go` receives `ModelAttemptMetadata` from `callModelWithFallback` and passes it to `emitAssistantResponse`.
- `agent/session_model_call.go:emitAssistantResponse` is the only session path that calls `appendAssistantTurn`.
- `agent/session.go:appendAssistantTurn` requires `ModelAttemptMetadata`.

Verdict: `attempt_group_id`, adapter fallback records, retry/fallback classifiers, HMAC generation, and real `responses_delta` attempts remain in later phases.
