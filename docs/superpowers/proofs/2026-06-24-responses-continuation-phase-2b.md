# Responses Continuation Phase 2B Proof

## Scope

Phase 2B wires `SERF_OPENAI_RESPONSES_CONTINUATION` through the envvars registry, direct `serf` and `serf serve` env help, CLI/serve env fallback resolution, hub launch-setting metadata, and `docs/environment.md`.

Runtime continuation remains disabled. This phase does not send `previous_response_id`, does not change OpenAI `store:false`, and does not add planner/storage eligibility behavior.

## Evidence

- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run 'TestResolveOpenAIResponsesContinuation|TestPrintRunEnvVars_IncludesOpenAIResponsesContinuation|TestPrintServeEnvVars_IncludesOpenAIResponsesContinuation' -count=1 -v`
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/launchconfig -run '^TestLaunchOptionSchema_OpenAIResponsesContinuation$' -count=1 -v`
- `GOCACHE=/tmp/serf-gocache go test . -run 'TestSupportedEnvVarsAreDocumented|TestSupportedEnvVarsUseRegistryRows' -count=1 -v`
- `git diff --check`

## Contracts Proven

- `SERF_OPENAI_RESPONSES_CONTINUATION` is a public envvars registry row and is documented in `docs/environment.md`.
- Direct `serf` and `serf serve` use the same precedence: non-empty `--openai-responses-continuation` wins, otherwise the env var is used, and values are trimmed.
- Direct `serf` and `serf serve` help env lists include `SERF_OPENAI_RESPONSES_CONTINUATION`.
- The hub launch schema advertises `SERF_OPENAI_RESPONSES_CONTINUATION` as the env fallback for `openai_responses_continuation`.
- The documented/user-visible text describes values, default `off`, launch override behavior, resume layering, and future provider-side retention/cost implications.
