# Responses Continuation Phase 2A Proof

## Scope

Phase 2A adds launch-time configuration for `openai_responses_continuation` with the supported values `off` and `auto`.

The setting now flows through direct `serf`, `serf serve`, hub launch config, appwire, TUI launch settings, and session config snapshots.

Runtime continuation remains disabled. This phase does not send `previous_response_id`, does not change OpenAI `store:false`, and does not add planner/storage eligibility behavior.

## Evidence

- `GOCACHE=/tmp/serf-gocache go test ./agent -run '^TestConfigSnapshot_ConverterFidelity$' -count=1 -v`
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run '^$' -count=1`
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/launchconfig -run '^(TestLaunchOptionSchema|TestMerge_ScalarPrecedence|TestLayerTOMLRoundTrip|TestFromWire|TestToWire|TestToArgs_AllFields)$' -count=1 -v`
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf-tui/internal/launchconfig -run '^(TestApplyEdit_NewSchemaFields|TestLaunchSettingsPanel_UsesSchemaRowsWhenAvailable)$' -count=1 -v`
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf-tui/internal/launchconfig -run '^TestSchemaRows' -count=1 -v`
- `GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestRestoreSessionFromMetaWithConfig_LayersOpenAIResponsesContinuation|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory' -count=1 -v`
- `git diff --check`

## Contracts Proven

- `agent.SessionConfig` and `schema.ConfigSnapshot` preserve `openai_responses_continuation` without dropping or reordering the persisted config projection.
- Direct CLI and `serf serve` compile with `--openai-responses-continuation`.
- Hub launch config merges, serializes, deserializes, and projects the setting to `--openai-responses-continuation auto`.
- TUI launch settings display and edit the setting through the schema-driven rows.
- Resumed sessions layer launch-time restore overrides over persisted snapshots, including persisted `off` overridden to `auto` and persisted `auto` overridden to `off`.
- Configured `auto` still sends full-history OpenAI Responses requests with no `previous_response_id` and explicit `store:false` while the default endpoint support registry is disabled.
- Default empty value remains equivalent to off until later runtime phases consume it.
