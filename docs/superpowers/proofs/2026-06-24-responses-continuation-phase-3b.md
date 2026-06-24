# Responses Continuation Phase 3B Proof

## Scope

Implemented adapter-owned request fingerprinting for OpenAI Responses continuation planning.

The OpenAI planner now:

- builds the real Responses request body through `buildRequestBody`;
- computes a `cont-req-v1` request fingerprint from the provider-visible body;
- excludes `previous_response_id`, `conversation`, and `store` from the request fingerprint;
- preserves storage-scope fields as empty/false until Phase 4A.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation_.*Fingerprint' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run '^TestOpenAIResponsesContinuationFingerprint_' -count=1 -v
```

Both focused suites passed after implementation.

## Contracts Proven

- Equivalent OpenAI Responses request bodies produce stable fingerprints despite Go map insertion order.
- Provider-visible request-shape changes alter the fingerprint:
  - instructions/message content;
  - tool definitions;
  - tool choice;
  - reasoning effort;
  - provider options;
  - prompt-cache key and retention controls.
- Continuation handles and storage forcing fields do not alter request fingerprints:
  - `previous_response_id`;
  - `conversation`;
  - `store`.
- Codex fingerprints are computed after Codex-specific request filtering.
- Production system prompt rendering with fixed environment data produces stable fingerprints through `llm.Client.PlanResponsesContinuation`.
- Changing the production prompt `Today` value changes the fingerprint.

## Deferred

- Storage-scope fingerprinting and storage policy labels remain deferred to Phase 4A.
- Anchor selection, continuation persistence, and runtime `responses_delta` selection remain deferred.
- The planner still does not send `previous_response_id`.
- OpenAI request storage remains unchanged; the adapter still defaults to `store:false` on real requests.
