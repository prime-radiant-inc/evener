# Responses Continuation Phase 11 Proof

Date: 2026-06-24

## Scope

Phase 11 implements ATIF provider-handle export modes for Responses continuation:

- default/redacted ATIF export omits raw provider handles;
- explicit `raw-local` export includes raw response IDs already persisted on local assistant turns;
- transcript API-call metadata contributes request-side continuation hashes and request diagnostics;
- direct CLI, `serf serve`, hub launch config, and TUI launch settings carry `redacted|raw-local`.

Raw `conversation_id` is not emitted because the transcript currently persists only `conversation_id_hash`, not the raw conversation handle. Raw `previous_response_id` is emitted in raw-local only when it can be derived from a matching local assistant `response_id_hash`.

## Verification

Command:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/atif ./agent ./cmd/serf ./cmd/serf-hub/internal/launchconfig ./cmd/serf-tui/internal/launchconfig -run 'Test.*ATIF|TestSchemaRows|TestLaunchSettingsPanel|TestConvertToATIF|TestConvertTranscriptToATIF|TestApplyEdit_NewSchemaFields|TestToArgs_AllFields|TestWire_SystemPromptAndDebugFieldsRoundTrip|TestLaunchOptionSchema_FieldCoverage|TestLaunchOptionSchema_ExportATIFProviderHandles' -count=1 -v && git diff --check
```

Result: PASS.

Notes:

- `agent/internal/atif` ran the ATIF conversion suite, including `TestConvertToATIF_ResponsesProviderHandlesRedacted`, `TestConvertToATIF_ResponsesProviderHandlesRawLocal`, and `TestConvertTranscriptToATIF_ResponsesRequestHandleHashes`.
- `agent` ran `TestExportATIF_WritesFile` and `TestExportATIF_ProviderHandleModes`.
- `cmd/serf-hub/internal/launchconfig` ran argv, schema, and wire round-trip checks for the new mode.
- `cmd/serf-tui/internal/launchconfig` ran schema row, settings panel, and apply-edit checks for the new mode.
- The focused command selected no tests in `cmd/serf`, so direct CLI coverage was rerun separately.

Command:

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run 'Test' -count=1
```

Result: PASS.

Command:

```bash
git diff --check
```

Result: PASS as part of the focused verification command.
