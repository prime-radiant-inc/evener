# Responses Continuation Phase 3A Proof

## Scope

Phase 3A completes the pure planner-helper boundary for OpenAI Responses continuation. Earlier auth-scope work already populated sanitized `AuthScopeIdentity` on OpenAI adapters; this phase adds planner input/result types, `llm.Client.PlanResponsesContinuation`, and an OpenAI adapter planner method that passes only sanitized auth scope plus hashed org/project identifiers into the pure helper.

Runtime continuation remains disabled. This phase does not compute request fingerprints, does not compute storage-scope fingerprints, does not send `previous_response_id`, does not change OpenAI `store:false`, and does not add session anchor selection or persistence.

## Evidence

- `GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestPlanResponsesContinuation|TestResponsesContinuationPlanInputDoesNotExposeRawScopeFields|TestClient_PlanResponsesContinuation' -count=1 -v`
- `GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation' -count=1 -v`
- `git diff --check`

## Contracts Proven

- The pure planner input exposes sanitized `AuthScopeIdentity` and hash fields, and does not expose raw credential/token fields or raw org/project identifiers.
- The pure planner helper copies endpoint family, sanitized auth scope, and hashed org/project identifiers while leaving request fingerprint and storage-scope fields zero for later phases.
- `llm.Client.PlanResponsesContinuation` resolves explicit/default providers and dispatches to the adapter planner without invoking completion, streaming, middleware, or provider network paths.
- Unsupported or unknown planner providers return configuration errors.
- The OpenAI adapter maps public Responses and Codex backend paths to distinct endpoint families.
- The OpenAI adapter planner result contains sanitized auth scope and hashed org/project identifiers, and omits raw API keys, bearer tokens, account/workspace identifiers, org IDs, and project IDs.
