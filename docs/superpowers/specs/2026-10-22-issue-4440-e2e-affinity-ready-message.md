# Issue #4440: affinity-aware turn-controls ready message

## Scope

Correct only the ready-message guidance in
`scripts/e2e/e2e-webui-turn-controls.sh`. Its generated provider configuration
sets `session_affinity_headers = true`. In
`test/e2e/fakellm/cmd/main.go`, `sessions.begin` prefers `call.AffinityHeader()`
for the logged session name and falls back to `call.ToolCallID()` only when the
header is absent. The harness therefore logs the daemon session id, not a
`call_fakellm_N` tool-call id.

## Corrected message

Replace the stale tool-call-id gloss, numbered-id rationale, and example with:

```text
  Sessions run at once and their rounds interleave. Each round line names its
  daemon session id. To follow just one session, take its daemon session id
  off its first round line and use it in place of DAEMON_SESSION_ID below:
    grep -F 'DAEMON_SESSION_ID' $run/fakellm.log
```

`$run` continues to expand in the existing ready-message heredoc. The uppercase
placeholder is explicitly replaced by the operator with the id on the first
round line. Keep the existing round-watch command above this guidance.

## Non-goals and preservation

Do not change runtime behavior, provider configuration, affinity capability,
round counting, flags, startup, teardown, or fakellm. Do not remove
`session_affinity_headers = true`. No client, ownership, persistence, or recovery
contract changes. Existing logs and operator work remain untouched. Change only
this spec and the script's ready-message paragraph. Add no copy-as-oracle test.

## Verification

- Before editing, read the current paragraph and compare it with
  `sessions.begin` and the affinity-header documentation in fakellm.
- Review the new text against the same implementation and read back the changed
  paragraph. It must identify the daemon session id and must not claim a
  tool-call id names the round or that `-w` disambiguates numbered ids.
- Run `bash -n scripts/e2e/e2e-webui-turn-controls.sh`.
- Review the diff to confirm the provider configuration and all executable
  behavior are unchanged.
- Run `make build`. Leave full suites to CI, with no local e2e startup needed
  for a wording-only correction.
- Run independent competing PAR reviews of this frozen spec and of the
  implementation, simplify, commit, and run branch RoboRev review. Push a PR
  against main, shepherd current-head checks and reviews, and merge only under
  the repository's green-check and settled-finding policy.
