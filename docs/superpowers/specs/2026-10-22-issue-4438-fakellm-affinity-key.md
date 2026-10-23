# Issue #4438: fakellm affinity-key comments

## Scope and authority

Correct the four comments under `test/e2e/fakellm` that name the retired
`send_session_affinity_headers` providers.toml key. Each must instead name
`session_affinity_headers`, the TOML key on `SessionAffinityHeaders` in
`llm/registry/types.go`.

The implementation base is `16ff3909af79adfadbdca3f450a636814eb3ef9e`.
Jesse authorized this comment-only scope and proceeding after spec PAR.

## Exact sites at the base

1. `test/e2e/fakellm/cmd/main.go:31`: package comment explaining fixture configuration.
2. `test/e2e/fakellm/fakellm.go:76`: `Call.AffinityHeader` documentation.
3. `test/e2e/fakellm/cmd/main_test.go:114`: request helper's affinity-header comment.
4. `test/e2e/fakellm/cmd/main_test.go:843`: fallback test's configuration comment.

Replace only the retired key token at each site. No other comment wording changes.
The only additional changed file is this spec.

## Non-goals and preservation

Do not change executable code, test assertions, configuration schema, fixtures,
provider behavior, header transmission, session naming, fallback behavior, or
product documentation. No client, recovery, or persistence contract changes.
Existing request/header and fallback behavior must remain unchanged.

## Verification and delivery

Before editing, mechanically demonstrate that the retired token exists at the
four sites. After editing, require zero occurrences under `test/e2e/fakellm`
and inspect the diff to confirm exactly four comment-token replacements.
Run `go test ./test/e2e/fakellm/...` and `go vet ./test/e2e/fakellm/...`.
Run the repository entry point `make build`; do not run full local suites.
CI owns the full gate ladder.

Freeze this spec for two independent competing PAR reviewers before implementation.
Then PAR the complete implementation diff, run simplify-code and
roborev-review-branch, commit and push, open a PR against main, and shepherd it.
Admin squash merge only after required checks are green and all RoboRev findings
are low or conclusively refuted. Verify GitHub's merged state and merge SHA.
