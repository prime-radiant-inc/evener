# Task 10 Implementation Report

## RED evidence

Added `identifier_audit_test.go` first with the injected-fixture test. The required focused command failed before the detector existed:

```text
go test . -run 'Test.*Identifier.*Audit' -count=1
./identifier_audit_test.go:11:2: undefined: writeIdentifierAuditFixture
./identifier_audit_test.go:16:19: undefined: identifierAuditFindings
./identifier_audit_test.go:26:19: undefined: identifierAuditFindings
FAIL
```

The fixture seam is a temporary directory containing `func ProjectSlug(path string) string`; the completed detector rejects it without modifying a production file.

## GREEN changes

- Added a root production-source audit that excludes `identifier`, generated Go, tests, docs, and SDD material.
- The audit rejects ULID generation/imports, duplicate `ProjectID`/`ProjectSlug`/`projectSlug` declarations outside `identifier`, and unreviewed `crypto/sha256` imports.
- Added an explicit reviewed SHA-256 allowlist for unrelated integrity, cache, image, PKCE, HMAC, and request-fingerprint uses.
- Added path-specific guards for the removed worktree and Hub project-hash implementations.
- Updated identifier/storage documentation for readable canonical project IDs under `serf/projects/<project-id>`, linked-worktree aggregation, distinct-clone separation, 22-character UUIDv7 base62 session IDs, clean-break manual old-state removal, and installation ID as the sole automatic legacy replacement.
- Updated stale transcript/testing scenario references and comments.
- Fixed the remaining `cmd/serf-doctor` `BucketHash` reference to the Task 9 `ProjectID` field; this was found by the install build and is required for the clean-break reader migration to compile.

## Exact commands and results

| Command | Result |
|---|---|
| `go test . -run 'Test.*Identifier.*Audit' -count=1` | PASS |
| `make lint-docs` | PASS: `serf-docscheck: all exported package-level declarations in the library packages are documented` |
| `rg -n --glob '*.go' --glob '!**/*_test.go' 'ulid\\.(Make|New)|oklog/ulid|func (ProjectID|ProjectSlug|projectSlug)\\(' .` | Only legitimate `identifier/project.go:118:func ProjectID(...)`; no ULID generation/imports or duplicate project definitions outside `identifier` |
| `go work sync` | BLOCKED by sandbox/network: module-cache lock operation not permitted, then unavailable network to `proxy.golang.org` |
| `(cd identifier && go mod tidy)` / `(cd agent && go mod tidy)` / `(cd llm && go mod tidy)` / `go mod tidy` | The exact chained tidy sequence stopped at `go work sync`; no manifest dependency changes were accepted. Existing ULID dependencies remain required by tests. |
| `go test . -run '^TestInstallHomeGeneratedHome$' -count=1` | FAIL in sandbox: `listen tcp 127.0.0.1:0: bind: operation not permitted` |
| `go test . -count=1` | Same sandbox bind failure after the install build; the initial run also exposed the fixed `cmd/serf-doctor` `BucketHash` compile reference. |
| `go test ./identifier -count=1` | PASS |
| `go test ./cmd/serf-doctor -run '^$' -count=1` | PASS (compile-only) |
| `go test ./cmd/serf-doctor -count=1` | FAIL on pre-existing invalid fixture ID `01CMDTESTSESSIONXXXXXXXXXXX`; not caused by Task 10 |
| `git diff --check` | PASS |

## Changed files

- `identifier_audit_test.go`
- `README.md`
- `docs/agentic-testing.md`
- `docs/performance-profiling.md`
- `docs/serf-hub-remote-operations.md`
- `docs/tools/transcripts.md`
- `test/scenarios/transcript-find-catalog-read-markdown.md`
- `test/scenarios/transcript-find-scope-all-projects.md`
- `test/scenarios/transcript-multi-session-create-find-read.md`
- `agent/runtime_dir.go`
- `cmd/serf-doctor/main.go`
- `cmdutil/statedir.go`
- `identifier/project.go`
- `cmd/serf-doctor/main_test.go`
- `go.work`
- `identifier/go.sum`

The pre-existing `.superpowers/sdd/task-1-report.md` modification was not edited, staged, or committed.

## Self-review

Reviewed the complete diff from base `fe91559ba9f05e2a5b635477949fa68d19827e9d`, excluding the pre-existing Task 1 report. The audit is AST/parser-backed for imports, scans only production Go, has a deterministic injected fixture, and uses an explicit allowlist rather than banning unrelated cryptographic hashing. Documentation consistently uses `project-id` and no longer describes project buckets as fixed-width hashes or sessions as ULIDs. No module manifest or sum changes were retained.

## Concerns

- Full install/root tests cannot complete in this sandbox because localhost bind is denied. The changed doctor package compiles, and the focused identifier audit/package tests pass.
- The exact module tidy sequence cannot complete because the environment cannot write the Go module cache and has no network. No unrelated dependency upgrades were accepted.
- Historical SDD plans/specs retain historical hash/ULID examples; published operational/testing documentation was updated, while historical records were intentionally left unchanged.

## Follow-up fixes and verification

The parent follow-up exposed two clean-break verification gaps. First, `go.work`
now carries the minimal additional exact replacement:

```text
replace primeradiant.com/serf/auth v0.1.0 => ./auth
```

This matches `llm/go.mod` while preserving the existing `auth v0.0.0` replacement.
`go work sync` succeeds with the corrected workspace. The full required sequence
was rerun verbatim:

```bash
go work sync && (cd identifier && go mod tidy) && (cd agent && go mod tidy) && (cd llm && go mod tidy) && go mod tidy
```

It remains environment-blocked after `go work sync`: the Go tool cannot write
`/Users/jesse/go/pkg/mod/cache/download/.../*.lock` and then cannot resolve the
local module path through `proxy.golang.org` because network is disabled. The
workspace replacement itself is therefore verified; no dependency upgrades or
ULID removals were made. The only tidy artifact retained is the expected
`identifier/go.sum` for its existing `github.com/google/uuid v1.6.0` dependency.
The `go.work` ordering change is the normalizer output from `go work sync`.

Second, `go test ./cmd/serf-doctor -count=1` initially failed on the stale local
fixture ID `01CMDTESTSESSIONXXXXXXXXXXX`. The fixture migration was test-first:
the unfiltered package command failed with `invalid session id`, then all root,
child, grandchild, and observer fixture IDs were replaced with deterministic
22-character base62 encodings of valid UUIDv7 values, and the legacy
`00aa00bb00cc00dd` project bucket was replaced with
`project-test-0123456789`. The unfiltered doctor package now passes.

Final follow-up commands:

```text
go test . -run 'Test.*Identifier.*Audit' -count=1    PASS
make lint-docs                                      PASS
go test ./identifier -count=1                      PASS
go test ./cmd/serf-doctor -count=1                 PASS
git diff --check                                    PASS
```

The root/install listener tests remain blocked by the sandbox's
`listen tcp 127.0.0.1:0: bind: operation not permitted` restriction.

## Independent-review fixes

- Reworked `identifier_audit_test.go` to inspect Go ASTs for ULID imports/calls,
  duplicate project declarations, and SHA-256 calls. Reviewed SHA uses are
  allowlisted by file and function, while argument expressions containing
  project/path identity data are rejected. Added fixtures for legitimate and
  project-path hashes, comment/string false positives, and nested
  `cmd/identifier` scanning.
- Removed current operational hash/ULID/26-character claims from production
  comments, README, and scenario docs. Historical SDD/design documents and
  generic fuzzing hashes remain intentionally unchanged.
- Broader operational search found and corrected the remaining current selector
  and forensic-output examples (`proj:<hash>` and `bucket_hash`). The remaining
  `ULID`/`hash` matches are generic fuzzing/sanitization terminology or preserved
  historical design text, not current identifier-format claims.
- Recorded that the original RED evidence is the exact command output already
  included above; it is not represented by an intermediate commit and was not
  reconstructed or rewritten.

Verification for this wave:

```text
go test . -run 'Test.*Identifier.*Audit' -count=1    PASS
make lint-docs                                      PASS
go test ./cmd/serf-hub/internal/hubcore -count=1     BLOCKED: httptest listener bind denied
go test ./cmd/serf-hub/internal/hubcore -run '^Test' -count=1 PASS
required forbidden production search                   PASS (only identifier/project.go ProjectID)
broad operational stale search                         PASS (only intentional generic/history survivors)
git diff --check                                      PASS
```

## Commit hash(es)

- Implementation commit: `3878f6e9a7be2876f06e95b98a405bc733a17352` — `docs: document unified identifier formats`
- Follow-up fix/report commit: recorded after the follow-up verification commit.
