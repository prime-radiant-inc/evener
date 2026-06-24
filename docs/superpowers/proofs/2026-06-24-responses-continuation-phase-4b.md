# Responses Continuation Phase 4B Proof

## Scope

Phase 4B adds pure session-owned eligibility gates for context boundaries, restored active boundary membership, missing continuation-aware anchor metadata, empty deltas, and `SystemPromptAsUser`.

Runtime continuation remains disabled. This phase does not send `previous_response_id`, does not select anchors from `prepareModelRequest`, does not enable any endpoint-family registry entry, and does not make live provider requests.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationAnchorCandidate' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- Assistant turns before the latest checkpoint/summary boundary are not active anchors.
- Restored histories use the restored checkpoint/summary boundary when evaluating active membership.
- Older assistant turns missing continuation-aware metadata are rejected.
- `SystemPromptAsUser` forces `full_history`.
- Empty deltas force `full_history`.
