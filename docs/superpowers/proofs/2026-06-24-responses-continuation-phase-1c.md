# Responses Continuation Phase 1C Proof

## Scope

Phase 1C adds sanitized OpenAI auth-scope identity construction for future continuation storage-scope planning.

Runtime continuation remains disabled. This phase does not send `previous_response_id`, does not send continuation-owned `store:true`, and does not persist auth scope to transcripts or API logs.

## Evidence

- `GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -run 'TestContinuation|TestNewForInstance_ContinuationAuthScope|TestNewFromEnv_ContinuationAuthScope|TestNewFromEnv_ReadsOrgAndProjectID|TestInstanceFactory_EnvTunables_APIKeyPath' -count=1 -v`
- `git diff --check`

## Contracts Proven

- API-key adapters keep the raw API key only on the existing transport field and expose a versioned credential HMAC for continuation planning.
- OpenAI org/project identifiers keep their raw header fields and expose separate versioned HMACs for continuation planning.
- OAuth/Codex adapters keep the raw bearer token only on the existing transport field and expose versioned credential/account/workspace HMACs from stable auth-record identifiers.
- Missing `ContinuationHasher` leaves auth-scope fields empty, so default construction behavior remains unchanged until a later runtime phase supplies the hasher.
