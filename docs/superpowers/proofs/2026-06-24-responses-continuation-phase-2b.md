# Responses Continuation Phase 2B Proof

## Scope

Phase 2B wires `EVENER_OPENAI_RESPONSES_CONTINUATION` through the envvars registry, direct `evener` and `evener serve` env help, CLI/serve env fallback resolution, hub launch-setting metadata, and `docs/environment.md`.

Runtime continuation remains disabled. This phase does not send `previous_response_id`, does not change OpenAI `store:false`, and does not add planner/storage eligibility behavior.

## Evidence

- `GOCACHE=/tmp/evener-gocache go test ./cmd/evener -run 'TestResolveOpenAIResponsesContinuation|TestPrintRunEnvVars_IncludesOpenAIResponsesContinuation|TestPrintServeEnvVars_IncludesOpenAIResponsesContinuation' -count=1 -v`
- `GOCACHE=/tmp/evener-gocache go test ./cmd/evener-hub/internal/launchconfig -run '^TestLaunchOptionSchema_OpenAIResponsesContinuation$' -count=1 -v`
- `GOCACHE=/tmp/evener-gocache go test . -run 'TestSupportedEnvVarsAreDocumented|TestSupportedEnvVarsUseRegistryRows' -count=1 -v`
- `git diff --check`

## Contracts Proven

- `EVENER_OPENAI_RESPONSES_CONTINUATION` is a public envvars registry row and is documented in `docs/environment.md`.
- Direct `evener` and `evener serve` use the same precedence: non-empty `--openai-responses-continuation` wins, otherwise the env var is used, and values are trimmed.
- Direct `evener` and `evener serve` help env lists include `EVENER_OPENAI_RESPONSES_CONTINUATION`.
- The hub launch schema advertises `EVENER_OPENAI_RESPONSES_CONTINUATION` as the env fallback for `openai_responses_continuation`.
- The documented/user-visible text describes values, default `off`, launch override behavior, resume layering, and future provider-side retention/cost implications.
