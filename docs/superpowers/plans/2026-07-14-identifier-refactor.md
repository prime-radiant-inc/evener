# Unified Identifier Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every project-identity implementation with one resolving API and replace every Serf-owned ULID payload with a fixed-width, 22-character base62 UUIDv7 payload.

**Architecture:** Add the leaf module `primeradiant.com/serf/identifier`. It owns project canonicalization, local Git main-checkout resolution, bounded project rendering, UUIDv7/base62 encoding, domain constructors, and validators. Root, `agent`, and `llm` depend on it; `agent/execenv` supplies an environment-backed `identifier.Resolver` without creating a reverse dependency.

**Tech Stack:** Go 1.25.6 workspace, `github.com/google/uuid` v1.6.0, SHA-256, Git worktrees, deterministic table/property/fuzz tests.

## Global Constraints

- Read and follow `docs/testing.md`; default tests must be deterministic, offline, and credential-independent.
- Project IDs use only ASCII letters, digits, and hyphens and never exceed 80 bytes.
- Project IDs retain the most specific path tail and end with `-` plus exactly 10 base62 SHA-256-derived characters.
- The project API owns absolute-path, cleaning, symlink, Git main-checkout, and submodule resolution; ordinary callers cannot bypass it.
- A Git main checkout and all its linked worktrees share one project; distinct clones at distinct canonical paths remain distinct.
- Generated payloads are RFC 9562 UUIDv7 values encoded as exactly 22 base62 characters with alphabet `0-9A-Za-z`.
- Existing prefixes remain `job_`, `dlg_`, `dg_`, `watch_`, `wg_`, `wd_`, `ag_`, and `call_`; sessions, installations, and terminal generations remain unprefixed.
- External provider and thread IDs remain opaque.
- This is a clean break: do not migrate, rename, discover through fallback, or delete old hash/ULID state. Replace only an invalid singleton installation ID.
- Keep changes focused; do not preserve duplicate project renderers or direct Serf-owned ULID generation.

## File Structure

### New module

- `identifier/go.mod` — leaf-module declaration and direct UUID dependency.
- `identifier/uuid.go` — fixed-width base62 codec and UUIDv7 payload generation.
- `identifier/uuid_test.go` — fixed vectors, ordering, overflow, version, and variant tests.
- `identifier/uuid_fuzz_test.go` — codec round-trip and rejection properties.
- `identifier/domains.go` — named constructors and exact domain validators.
- `identifier/domains_test.go` — prefix, length, uniqueness, and cross-domain rejection tests.
- `identifier/project.go` — `Project`, `Resolver`, canonicalization pipeline, rendering, hashing, and validation.
- `identifier/project_test.go` — fake-resolver pipeline tests and rendering vectors.
- `identifier/project_integration_test.go` — local symlink, Git, linked-worktree, and submodule fixtures.
- `identifier/project_fuzz_test.go` — safe alphabet, cap, deterministic rendering, and malformed-ID properties.
- `identifier/git.go` — local `Resolver` and reusable Git structural/binary resolution core moved from `internal/gitpath`.
- `identifier/git_test.go` — moved and adapted Git structural/parser tests.

### Existing modules

- `go.work`, `Makefile`, root `go.mod`, `agent/go.mod`, `llm/go.mod`, and corresponding sums — workspace registration and dependency direction.
- `agent/execenv/project_resolver.go` — execution-environment adapter for `identifier.Resolver`.
- `agent/execenv/project_resolver_test.go` — shared resolver-contract and failure tests.
- Runtime/project consumers in `agent/runtime_dir.go`, `cmdutil/statedir.go`, `cmd/serf-hub/spawn.go`, `cmd/serf-hub/internal/launchconfig/paths.go`, `agent/session_tools_worktree.go`, `agent/session_worktree_resume.go`, and `cmd/serf-hub/internal/hubcore/tree.go`.
- Generated-ID consumers in `agent/session_init.go`, `agent/fork.go`, `agent/internal/installid/installation_id.go`, `agent/internal/jobstore/record.go`, `agent/internal/jobstore/notify.go`, `agent/session_model_call.go`, and `llm/providers/google/{adapter.go,response.go}`.
- Clean-break lookup/validation consumers in `agent/transcript_lookup.go`, `agent/session_tools_find.go`, `agent/doctor`, and local hub route/listing paths.
- `internal/gitpath` is deleted after all reusable logic and tests move to `identifier`.
- README/storage/upgrade docs and a repository audit test document and enforce the new contract.

---

### Task 1: Add the Identifier Module and UUIDv7/Base62 Core

**Files:**
- Create: `identifier/go.mod`
- Create: `identifier/uuid.go`
- Create: `identifier/uuid_test.go`
- Create: `identifier/uuid_fuzz_test.go`
- Modify: `go.work`
- Modify: `Makefile:75-81`

**Interfaces:**
- Produces: `EncodeUUID(uuid.UUID) string`
- Produces: `DecodeUUID(string) (uuid.UUID, error)`
- Produces: `ValidateUUIDv7Payload(string) error`
- Produces internally: `newUUIDv7Payload() (string, error)` and `mustNewUUIDv7Payload() string`
- Consumes: `github.com/google/uuid` v1.6.0 only

- [ ] **Step 1: Write fixed-vector tests before module implementation**

Create tests that define the codec contract:

```go
func TestUUIDBase62Vectors(t *testing.T) {
    tests := []struct {
        name string
        raw  uuid.UUID
        want string
    }{
        {"zero", uuid.UUID{}, "0000000000000000000000"},
        {"one", uuid.UUID{15: 1}, "0000000000000000000001"},
        {"max", uuid.UUID{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, "7n42DGM5Tflk9n8mt7Fhc7"},
    }
    for _, tt := range tests {
        if got := EncodeUUID(tt.raw); got != tt.want { t.Errorf("%s: %q", tt.name, got) }
        decoded, err := DecodeUUID(tt.want)
        if err != nil || decoded != tt.raw { t.Errorf("%s: decoded=%x err=%v", tt.name, decoded, err) }
    }
}
```

Also test wrong lengths, `-`, `_`, `!`, and a 22-character base62 value above `2^128-1`; validate known UUIDv7 and reject v4/wrong-variant values. Add a fuzz seed for zero, one, max, and one valid v7 UUID and assert `DecodeUUID(EncodeUUID(x)) == x`.

- [ ] **Step 2: Run the new module test and verify the expected failure**

Run:

```bash
go test ./identifier/... -run 'TestUUIDBase62Vectors|TestDecodeUUIDRejects|TestValidateUUIDv7Payload' -count=1
```

Expected: FAIL because `identifier/go.mod` or codec symbols do not exist.

- [ ] **Step 3: Add workspace/module wiring and the minimal codec**

Create `identifier/go.mod` with Go 1.25.6 and `github.com/google/uuid v1.6.0`. Add `./identifier` to `go.work`, add a workspace replace for `primeradiant.com/serf/identifier v0.0.0`, and add `identifier` to `GO_MODULES` in `Makefile`.

Implement base62 conversion with a 22-byte output initialized to `'0'`, repeated `big.Int.QuoRem`, strict alphabet lookup, strict width, and `BitLen() <= 128`. `ValidateUUIDv7Payload` must decode, then check `Version() == 7` and `Variant() == uuid.RFC4122`. Generate with `uuid.NewV7()`.

- [ ] **Step 4: Run deterministic module tests and bounded fuzz smoke**

Run:

```bash
go test ./identifier/... -count=1
go test ./identifier/... -run '^FuzzUUIDBase62RoundTrip$' -fuzz '^FuzzUUIDBase62RoundTrip$' -fuzztime=2s
```

Expected: PASS; no network or ambient state.

- [ ] **Step 5: Commit the UUID codec foundation**

```bash
git add go.work Makefile identifier/go.mod identifier/uuid.go identifier/uuid_test.go identifier/uuid_fuzz_test.go
git commit -m "feat(identifier): add UUIDv7 base62 codec"
```

### Task 2: Add Named Generated-ID Domains

**Files:**
- Create: `identifier/domains.go`
- Create: `identifier/domains_test.go`

**Interfaces:**
- Consumes: `newUUIDv7Payload() (string, error)` from Task 1
- Produces error-returning constructors: `NewSessionID`, `NewInstallationID`, `NewJobID`, `NewDelegateID`, `NewDelegateGeneration`, `NewWatchID`, `NewWatchGeneration`, `NewWatchDeliveryID`, `NewAgentCallID`, `NewSyntheticCallID`, `NewTerminalGeneration`
- Produces fail-fast wrappers with the same names prefixed by `Must`, for current no-error call sites
- Produces validators matching each constructor, including `ValidateSessionID(string) error` and `ValidateInstallationID(string) error`

- [ ] **Step 1: Write domain shape and rejection tests**

Use a table with constructor, validator, prefix, and total length:

```go
func TestGeneratedIDDomains(t *testing.T) {
    tests := []struct{
        name, prefix string
        newID func() (string, error)
        validate func(string) error
    }{
        {"session", "", NewSessionID, ValidateSessionID},
        {"job", "job_", NewJobID, ValidateJobID},
        {"delegate", "dlg_", NewDelegateID, ValidateDelegateID},
        {"watch", "watch_", NewWatchID, ValidateWatchID},
        {"agent-call", "ag_", NewAgentCallID, ValidateAgentCallID},
        {"synthetic-call", "call_", NewSyntheticCallID, ValidateSyntheticCallID},
    }
    for _, tt := range tests {
        got, err := tt.newID()
        if err != nil { t.Fatal(err) }
        if len(got) != len(tt.prefix)+22 || !strings.HasPrefix(got, tt.prefix) { t.Errorf("%s: %q", tt.name, got) }
        if err := tt.validate(got); err != nil { t.Errorf("%s: %v", tt.name, err) }
    }
}
```

Include `dg_`, `wg_`, `wd_`, installation, and terminal generation. Assert validators reject wrong prefixes, 26-character ULIDs, truncated payloads, and cross-domain IDs.

- [ ] **Step 2: Run the focused tests and verify they fail**

```bash
go test ./identifier/... -run 'TestGeneratedIDDomains|TestGeneratedIDValidatorsRejectWrongDomain' -count=1
```

Expected: FAIL with undefined domain symbols.

- [ ] **Step 3: Implement named constructors and strict validators**

Use one unexported helper:

```go
func newDomainID(prefix string) (string, error) {
    payload, err := newUUIDv7Payload()
    if err != nil { return "", err }
    return prefix + payload, nil
}
```

Validators must require exact prefix and validate only the remaining 22-character v7 payload. Do not expose a public arbitrary-prefix constructor.

- [ ] **Step 4: Run all identifier tests**

```bash
go test ./identifier/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit named ID domains**

```bash
git add identifier/domains.go identifier/domains_test.go
git commit -m "feat(identifier): add generated ID domains"
```

### Task 3: Implement Resolving Project Identity and Move the Git Core

**Files:**
- Create: `identifier/project.go`
- Create: `identifier/project_test.go`
- Create: `identifier/project_fuzz_test.go`
- Create: `identifier/git.go`
- Create: `identifier/git_test.go`
- Create: `identifier/project_integration_test.go`
- Move/adapt tests from: `internal/gitpath/gitpath_test.go`
- Delete after consumers migrate in Task 4: `internal/gitpath/gitpath.go`

**Interfaces:**
- Produces: `type Project struct { ID string; CanonicalPath string }`
- Produces: `type Resolver interface { Abs(string) (string,error); EvalSymlinks(string) (string,error); MainCheckout(string) (string,bool,error) }`
- Produces: `ResolveProject(string) (Project,error)`, `ResolveProjectWith(string, Resolver) (Project,error)`, `ProjectID(string) (string,error)`, `ValidateProjectID(string) error`
- Produces reusable pure Git helpers needed by the execenv adapter: `ParseGitdirPointer`, `MainRootFromGitdirPointer`, `MainRootCandidateFromCommonDir`, and `GitEntryResolvesToCommon`

- [ ] **Step 1: Write fake-resolver pipeline and rendering tests**

Use a recording resolver to assert call order `Abs → EvalSymlinks → MainCheckout → EvalSymlinks`, Git root replacement, non-Git retention, and error propagation. Add stable renderer vectors that check:

```go
if got := project.ID; len(got) > 80 || !regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*-[0-9A-Za-z]{10}$`).MatchString(got) {
    t.Fatalf("unsafe project id %q", got)
}
```

Compute the expected suffix in the test from the approved definition: full SHA-256 digest interpreted big-endian, modulo `62^10`, base62-encoded and left-padded. Include paths whose sanitized tails collide and a path long enough to prove left-side truncation preserves `prime-radiant-serf`.

- [ ] **Step 2: Run the focused project tests and verify they fail**

```bash
go test ./identifier/... -run 'TestResolveProjectWithPipeline|TestProjectIDRendering|TestValidateProjectID' -count=1
```

Expected: FAIL with undefined project APIs.

- [ ] **Step 3: Implement project resolution, rendering, and validation**

Reject empty paths and nil resolvers. Require every resolved path to exist and represent a directory. Never fall back after a resolver reports `isGit=true` with an empty root or error. Render from the final canonical path only. Keep rendering unexported so every public ID call passes through resolution.

Move the local structural Git implementation into `identifier/git.go`. Preserve linked-worktree pointer parsing, submodule fallback to `git rev-parse --show-toplevel`, symlink cleaning, two-second command timeout, and path-containment sanity checks.

- [ ] **Step 4: Add real filesystem and Git integration tests**

Create temp fixtures with `git init`, `git worktree add`, a symlink to the repo, and a submodule fixture using only local repositories. Assert:

- repo root, nested directory, symlink, and linked worktree resolve to the same `Project`;
- a submodule resolves to its own root and differs from its superproject;
- a nonexistent path errors;
- a detected malformed linked-worktree pointer errors rather than hashing the active path.

- [ ] **Step 5: Run identifier tests and bounded project fuzz smoke**

```bash
go test ./identifier/... -count=1
go test ./identifier/... -run '^FuzzProjectIDFormat$' -fuzz '^FuzzProjectIDFormat$' -fuzztime=2s
```

Expected: PASS.

- [ ] **Step 6: Commit project identity core**

```bash
git add identifier/project.go identifier/project_test.go identifier/project_fuzz_test.go identifier/git.go identifier/git_test.go identifier/project_integration_test.go
git commit -m "feat(identifier): resolve canonical project identity"
```

### Task 4: Add the Execution-Environment Resolver Adapter

**Files:**
- Create: `agent/execenv/project_resolver.go`
- Create: `agent/execenv/project_resolver_test.go`
- Modify: `agent/execenv/gitpath.go`
- Modify: `agent/go.mod`
- Modify: `agent/go.sum`
- Modify existing tests in: `agent/execenv/gitpath_mainroot_test.go`, `agent/execenv/gitpath_program_fuzz_test.go`
- Delete: `internal/gitpath/gitpath.go`
- Move/delete corresponding old tests under: `internal/gitpath`

**Interfaces:**
- Consumes: `identifier.Resolver` and pure Git helpers from Task 3
- Produces: `func NewProjectResolver(env ExecutionEnvironment) identifier.Resolver`
- Produces strict internal resolution: `resolveMainRepoRoot(env, cwd) (root string, isGit bool, err error)`

- [ ] **Step 1: Write adapter contract tests first**

Run one shared fixture table against `identifier.ResolveProjectWith(path, NewProjectResolver(env))`. Cover local repo root, linked worktree, submodule, non-Git directory, nonexistent directory, nonzero `git rev-parse`, blank stdout, and containment mismatch. Assert non-Git returns `isGit=false,nil`; detected-but-unresolvable Git returns an error.

- [ ] **Step 2: Run adapter tests and verify they fail**

```bash
(cd agent && go test ./execenv -run 'TestProjectResolver|TestResolveMainRepoRootStrict' -count=1)
```

Expected: FAIL because `NewProjectResolver` does not exist.

- [ ] **Step 3: Implement the adapter without duplicating policy**

Implement `Abs` relative to `env.WorkingDirectory()`. For local execution environments, use host `filepath.EvalSymlinks`; for generic environments, resolve a directory with a fixed `pwd -P` command whose working directory is the path, never interpolating the path into shell text. Implement `MainCheckout` with structural helpers first and environment `git rev-parse` fallback. Return enough status to distinguish non-Git from detected-but-broken Git.

Refactor old `ResolveMainRepoRoot` and `GitRootOrEmpty` wrappers to call the shared helper where compatibility is still required; do not retain parser/candidate implementations in `agent/execenv` or `internal/gitpath`.

- [ ] **Step 4: Run execenv and identifier tests**

```bash
(cd agent && go test ./execenv -count=1)
go test ./identifier/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit adapter and Git-core removal**

```bash
git add agent/go.mod agent/go.sum agent/execenv/project_resolver.go agent/execenv/project_resolver_test.go agent/execenv/gitpath.go agent/execenv/gitpath_mainroot_test.go agent/execenv/gitpath_program_fuzz_test.go internal/gitpath identifier/go.sum
git commit -m "refactor(execenv): share project resolution"
```

### Task 5: Migrate Fresh, Forked, Installation, Job, and Provider IDs

**Files:**
- Modify: `agent/session_init.go`
- Modify: `agent/fork.go`
- Modify: `agent/internal/installid/installation_id.go`
- Modify: `agent/internal/installid/installation_id_fuzz_test.go`
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/notify.go`
- Modify: `agent/internal/jobstore/record_test.go`
- Modify: `agent/internal/jobstore/notify_test.go`
- Modify: `agent/session_model_call.go`
- Modify: `llm/providers/google/adapter.go`
- Modify: `llm/providers/google/response.go`
- Modify relevant Google response/stream tests
- Modify: root `go.mod`, root `go.sum`, `llm/go.mod`, `llm/go.sum`

**Interfaces:**
- Consumes all named constructors/validators from Task 2
- Preserves existing jobstore factory signatures such as `NewJobID() string`
- Changes internal session initialization only as needed to propagate `identifier.NewSessionID()` errors through existing public error returns

- [ ] **Step 1: Update tests to assert the new formats and clean-break installation behavior**

Change jobstore length assertions from `len(prefix)+26` to `len(prefix)+22` and validate with the matching identifier validator. Add tests that fresh and forked sessions pass `ValidateSessionID`. Add installation cases:

```go
func TestLoadOrCreateInstallationID_ReplacesLegacyULID(t *testing.T) {
    // seed installation_id with a 26-char ULID
    // call LoadOrCreateInstallationIDWithFS
    // assert returned/stored value passes ValidateInstallationID and differs from legacy
}
```

Assert the two Google synthetic sites produce `call_` IDs that pass `ValidateSyntheticCallID`; do not validate external IDs received on the wire.

- [ ] **Step 2: Run focused tests and verify old production code fails them**

```bash
(cd agent && go test ./internal/installid ./internal/jobstore -count=1)
(cd agent && go test . -run 'Test.*Session.*ID|Test.*Fork' -count=1)
(cd llm && go test ./providers/google -run 'Test.*Synthetic|Test.*ToolCall' -count=1)
```

Expected: FAIL on old 26-character ULIDs or legacy installation reuse.

- [ ] **Step 3: Replace every production ULID call with a named constructor**

Use error-returning constructors in `NewSession` and fork paths. Preserve jobstore and attempt-group no-error signatures with explicit `identifier.MustNew...` calls. Replace invalid installation contents atomically: write a temporary file in the same directory with mode `0600`, close/sync as the existing filesystem seam permits, then rename; on a race, reread and validate the winner.

Replace both Google synthetic call-ID sites with `identifier.MustNewSyntheticCallID()`. Keep all external IDs untouched.

- [ ] **Step 4: Remove production ULID imports and module dependencies**

Run:

```bash
rg -n --glob '*.go' --glob '!**/*_test.go' 'ulid\.(Make|New)|oklog/ulid' .
```

Expected: no matches. Remove `github.com/oklog/ulid/v2` from root, `agent`, and `llm` when test fixtures no longer import it; use fixed old-format strings in clean-break tests instead of generating legacy IDs.

- [ ] **Step 5: Run focused and module tests**

```bash
(cd agent && go test ./internal/installid ./internal/jobstore -count=1)
(cd agent && go test . -run 'Test.*Session.*ID|Test.*Fork|Test.*AttemptGroup' -count=1)
(cd llm && go test ./providers/google -count=1)
```

Expected: PASS.

- [ ] **Step 6: Commit generated-ID migration**

```bash
git add go.mod go.sum agent/go.mod agent/go.sum llm/go.mod llm/go.sum agent/session_init.go agent/fork.go agent/internal/installid agent/internal/jobstore agent/session_model_call.go llm/providers/google
git commit -m "refactor: use compact generated identifiers"
```

### Task 6: Migrate Runtime State and Launch Configuration Project Paths

**Files:**
- Modify: `agent/runtime_dir.go`
- Modify: `agent/runtime_dir_test.go`
- Modify: `cmdutil/statedir.go`
- Modify: `cmdutil/statedir_test.go`
- Modify: `cmd/serf/run.go`
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf-hub/spawn.go`
- Modify: `cmd/serf-hub/internal/launchconfig/paths.go`
- Modify: `cmd/serf-hub/internal/launchconfig/paths_test.go`
- Modify callers/tests under: `cmd/serf-hub/internal/launchconfig`

**Interfaces:**
- Consumes: `identifier.ResolveProject(path)`
- Produces: `RuntimeDir(workDir, overrideDir string) (identifier.Project, string, error)` or an equivalently explicit result separating `Project` from the state path
- Produces: `PathsFor(stateRoot, cwd string) (Paths, error)` with `Paths.Project` carrying the resolved project if needed by callers

- [ ] **Step 1: Write tests for path-based state identity and error propagation**

Replace origin-URL expectations with canonical-path expectations. Assert two clones with the same origin have different project state directories, while main checkout and linked worktree share one. Assert nonexistent paths return errors. Preserve explicit override behavior without resolving the project only where the caller intentionally bypasses project storage.

- [ ] **Step 2: Run focused tests and verify they fail**

```bash
(cd agent && go test . -run 'TestRuntimeDir' -count=1)
go test ./cmdutil -run 'TestDefaultProjectStateDir|TestResolveStateKeyDir' -count=1
go test ./cmd/serf-hub/internal/launchconfig -run 'TestProject|TestPathsFor' -count=1
```

Expected: FAIL on old origin/hash behavior or unchanged no-error signatures.

- [ ] **Step 3: Refactor runtime state to consume resolved projects**

Remove origin URL from identity decisions. Keep unrelated `shortHash` behavior by moving tool-call dedupe hashing to a clearly named non-project helper if it is still used; do not route non-project hashes through `identifier.ProjectID`.

Make CLI and hub spawn callers resolve once, propagate errors, and pass the resulting state path and canonical project path forward. Do not call `ResolveProject` repeatedly within one launch.

- [ ] **Step 4: Replace launchconfig `ProjectID` and propagate errors**

Delete `launchconfig.ProjectID`. Build `<stateRoot>/projects/<Project.ID>` from `identifier.ResolveProject(cwd)`. Update `PathsFor` callers to handle errors and to use `Project.CanonicalPath` for repo/project config roots when appropriate.

- [ ] **Step 5: Run affected packages**

```bash
(cd agent && go test . -run 'TestRuntimeDir' -count=1)
go test ./cmdutil ./cmd/serf ./cmd/serf-hub/internal/launchconfig ./cmd/serf-hub -run 'Test.*(StateDir|Spawn|Paths|Launch)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit runtime and launchconfig migration**

```bash
git add agent/runtime_dir.go agent/runtime_dir_test.go cmdutil/statedir.go cmdutil/statedir_test.go cmd/serf/run.go cmd/serf/serve.go cmd/serf-hub/spawn.go cmd/serf-hub/internal/launchconfig
git commit -m "refactor: unify project state identity"
```

### Task 7: Migrate Managed Worktree Storage and Resume

**Files:**
- Modify: `agent/internal/worktree/name.go`
- Modify: `agent/internal/worktree/name_test.go`
- Modify: `agent/session_tools_worktree.go`
- Modify: `agent/session_worktree_resume.go`
- Modify worktree tests including: `agent/session_tools_worktree_create_test.go`, `agent/session_tools_worktree_prune_test.go`, `agent/session_tools_worktree_livework_test.go`, `agent/job_delegate_isolation_test.go`

**Interfaces:**
- Consumes: `identifier.ResolveProjectWith(path, execenv.NewProjectResolver(env))`
- Removes: `agent/internal/worktree.ProjectID`
- Preserves unrelated worktree APIs: `ValidateName`, `EncodeSidecarName`, and `DecodeSidecarName`

- [ ] **Step 1: Change worktree tests to use resolved `Project.ID`**

Add a regression fixture that creates a main checkout and linked worktree, invokes managed-worktree operations from each, and asserts both use:

```text
<worktree-root>/<same Project.ID>/<name>
```

Replace every test helper that directly calls `worktree.ProjectID` with the resolving identifier API.

- [ ] **Step 2: Run focused tests and verify they fail**

```bash
(cd agent && go test ./internal/worktree . -run 'Test.*(ProjectID|Worktree|Isolation)' -count=1)
```

Expected: FAIL until production callers stop using the deleted local renderer.

- [ ] **Step 3: Resolve once per worktree operation and carry the `Project` value**

At create, list, switch, remove, prune, and resume entry points, resolve the main project through the environment adapter once. Build every project directory from `Project.ID`; use `Project.CanonicalPath` for containment checks and metadata. Remove hash-specific comments and assumptions.

- [ ] **Step 4: Delete the local project renderer and its old tests**

Remove SHA-256/hex/project-basename code from `agent/internal/worktree/name.go`. Keep only worktree-name and sidecar responsibilities.

- [ ] **Step 5: Run worktree and agent tests**

```bash
(cd agent && go test ./internal/worktree -count=1)
(cd agent && go test . -run 'Test.*(Worktree|Isolation)' -count=1)
```

Expected: PASS.

- [ ] **Step 6: Commit managed-worktree migration**

```bash
git add agent/internal/worktree/name.go agent/internal/worktree/name_test.go agent/session_tools_worktree.go agent/session_worktree_resume.go agent/*worktree*_test.go agent/job_delegate_isolation_test.go
git commit -m "refactor(worktree): use canonical project IDs"
```

### Task 8: Migrate Hub Project Grouping, Archive, Delete, and Spawn Keys

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Modify: `cmd/serf-hub/internal/hubcore/tree_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/coverage_edges_test.go`
- Modify: `cmd/serf-hub/web_api_tree.go`
- Modify: `cmd/serf-hub/web_api_tree_test.go`
- Modify: `cmd/serf-hub/web_api_project_delete.go`
- Modify project archive/delete/spawn tests under `cmd/serf-hub`
- Modify TUI fallback key code only if it still synthesizes project keys: `cmd/serf-tui/hub_types.go`, `cmd/serf-tui/hub_dashboard.go`

**Interfaces:**
- Consumes: `identifier.ResolveProject` and `identifier.Project`
- Removes: `hubcore.ProjectSlug` and `projectSlug`
- Produces tree/project rows whose `Key` is always `Project.ID` and whose `WorkingDir` is `Project.CanonicalPath`

- [ ] **Step 1: Update tree and endpoint tests first**

Assert a main checkout and linked worktree aggregate into one `TreeProject`, symlink aliases aggregate, same-basename clones remain distinct, and every project key passes `identifier.ValidateProjectID`. Assert delete/archive requests validate the recomputed ID against the canonical path. Remove expectations for `<basename>-<8hex>`.

- [ ] **Step 2: Run focused hub tests and verify they fail**

```bash
go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -run 'Test.*(Tree|Project|Archive|Delete|Spawn)' -count=1
```

Expected: FAIL on old `ProjectSlug` output or duplicate grouping.

- [ ] **Step 3: Resolve project identity at hub ingestion boundaries**

Resolve live and past working directories once when building the project map. Pass `identifier.Project` into grouping rather than resolving inside sort/loop helpers. Use `Project.ID` for grouping, archive decisions, deletion keys, and URLs; use `CanonicalPath` for display and spawn/resume prefills.

Pathless external sessions remain in the explicit `no-project` presentation bucket, but `no-project` is not accepted as a local `identifier.ProjectID` for destructive operations.

- [ ] **Step 4: Delete slug generation and TUI fallbacks that can drift**

Remove `ProjectSlug`/`projectSlug`. TUI code may use the server-provided key; if it lacks one, it must treat the row as non-actionable rather than derive a second key from the display name.

- [ ] **Step 5: Run hub and TUI focused tests**

```bash
go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub ./cmd/serf-tui -run 'Test.*(Tree|Project|Archive|Delete|Spawn|Dashboard)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit hub project-key migration**

```bash
git add cmd/serf-hub/internal/hubcore cmd/serf-hub/web_api_tree.go cmd/serf-hub/web_api_tree_test.go cmd/serf-hub/web_api_project_delete.go cmd/serf-hub/*project* cmd/serf-tui/hub_types.go cmd/serf-tui/hub_dashboard.go
git commit -m "refactor(hub): use canonical project keys"
```

### Task 9: Enforce the Clean Break in Local Readers

**Files:**
- Modify: `agent/transcript_lookup.go`
- Modify: `agent/session_tools_find.go`
- Modify: `agent/doctor/selector.go`
- Modify: `agent/doctor/locate.go`
- Modify: `agent/doctor/doctor.go`
- Modify relevant tests in `agent/doctor`, transcript lookup/find tests, and hub past-index tests
- Modify local route validation in `cmd/serf-hub/web.go` and route tests

**Interfaces:**
- Consumes: `ValidateProjectID` and `ValidateSessionID`
- Preserves external ref parsing by validating only refs identified as local Serf refs
- Removes assumptions that project bucket names are exactly 16 hex characters

- [ ] **Step 1: Add old-state fixtures and untouched-file assertions**

Seed:

- `serf/projects/0123456789abcdef/sessions/<26-char-ulid>.transcript.jsonl`;
- a valid new project bucket containing a legacy session filename;
- a legacy project bucket containing a syntactically new session ID;
- valid new project/session state;
- opaque external refs such as `codex:thread_abc`.

Snapshot file contents and modification times. Assert only valid new local state is listed or resumed, legacy local direct lookup returns not-found/invalid-local-ID, all legacy files remain unchanged, and the external ref remains routable through its source-specific path.

- [ ] **Step 2: Run focused lookup/doctor/hub tests and verify they fail**

```bash
(cd agent && go test . ./doctor -run 'Test.*(Legacy|Lookup|Locate|Find|Selector)' -count=1)
go test ./cmd/serf-hub -run 'Test.*(Legacy|LocalRoute|Past)' -count=1
```

Expected: FAIL because old token validation accepts legacy names.

- [ ] **Step 3: Apply strict validation only at local boundaries**

When enumerating `serf/projects/*`, skip directories that fail `ValidateProjectID`. When enumerating local session files or accepting `local:`/`proj:` refs, require `ValidateSessionID`. Keep path-traversal checks. Do not apply these validators to external provider refs, thread IDs, or appwire source-qualified opaque IDs.

- [ ] **Step 4: Update terminology from hash to project ID**

Rename internal fields such as `bucketHash`/`BucketHash` to `projectID`/`ProjectID` where they represent the directory key. Preserve serialized compatibility only where a public JSON field is still needed during the clean-break release; otherwise update field names and tests consistently.

- [ ] **Step 5: Run affected tests**

```bash
(cd agent && go test . ./doctor -count=1)
go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -count=1
```

Expected: PASS and legacy fixtures remain byte-for-byte untouched.

- [ ] **Step 6: Commit clean-break reader enforcement**

```bash
git add agent/transcript_lookup.go agent/session_tools_find.go agent/doctor cmd/serf-hub/web.go cmd/serf-hub/*test.go cmd/serf-hub/internal/hubcore
git commit -m "refactor: enforce new local identifier formats"
```

### Task 10: Add Repository Audits and Update Documentation

**Files:**
- Create or modify: a focused root audit test near existing lint/audit tests, chosen after inspecting current conventions
- Modify: `README.md`
- Modify: `docs/performance-profiling.md`
- Modify: storage/upgrade documentation found by `rg 'project-hash|<hash>|26-character|ULID|ProjectSlug' docs README.md`
- Modify: comments in affected production files

**Interfaces:**
- Consumes the final code layout from Tasks 1–9
- Produces automated enforcement of the single-codepath claim

- [ ] **Step 1: Write audit tests that fail on duplicate implementations**

The audit scans production `.go` files, excluding `identifier`, generated files, and tests, and rejects:

```text
ulid.Make(
ulid.New(
github.com/oklog/ulid
func ProjectID(
func ProjectSlug(
func projectSlug(
```

Outside `identifier`, reject SHA-256 project-path hashing by checking known removed files and forbid imports of `crypto/sha256` in project-identity consumers. Allow unrelated cryptographic hashing through an explicit, reviewed allowlist rather than a blanket ban.

- [ ] **Step 2: Run the audit and verify it catches an injected fixture**

Use the audit test's temp fixture or test seam, not a production-file edit. Run the exact audit test and verify the fixture containing `func ProjectSlug` fails before the detector is complete, then passes after detector implementation.

- [ ] **Step 3: Update docs and comments**

Document:

- readable project IDs under `serf/projects/<project-id>`;
- main/linked-worktree aggregation and distinct-clone behavior;
- 22-character session IDs and retained domain prefixes;
- the clean break and manual removal of inert old state;
- installation ID replacement as the sole automatic legacy replacement.

Remove statements that project buckets are 16-hex hashes or that session IDs are ULIDs.

- [ ] **Step 4: Remove obsolete dependencies and tidy each changed module**

Run:

```bash
go work sync
(cd identifier && go mod tidy)
(cd agent && go mod tidy)
(cd llm && go mod tidy)
go mod tidy
```

Review every `go.mod`/`go.sum` change; do not accept unrelated upgrades.

- [ ] **Step 5: Run audits and documentation lint**

```bash
go test . -run 'Test.*Identifier.*Audit' -count=1
make lint-docs
rg -n --glob '*.go' --glob '!**/*_test.go' 'ulid\.(Make|New)|oklog/ulid|func (ProjectID|ProjectSlug|projectSlug)\(' .
```

Expected: tests/lint PASS and search returns no forbidden production definitions or ULID generation.

- [ ] **Step 6: Commit audits and documentation**

```bash
git add README.md docs Makefile go.work go.mod go.sum agent/go.mod agent/go.sum llm/go.mod llm/go.sum identifier/go.mod identifier/go.sum '**/*audit*test.go'
git commit -m "docs: document unified identifier formats"
```

### Task 11: Full Verification and Adversarial Review

**Files:**
- Modify only files required to fix verified failures or review findings

**Interfaces:**
- Consumes all previous tasks
- Produces a clean worktree and evidence that all acceptance criteria hold

- [ ] **Step 1: Run focused module tests in dependency order**

```bash
go test ./identifier/... -count=1
(cd agent && go test ./execenv ./internal/installid ./internal/jobstore ./internal/worktree . -count=1)
(cd llm && go test ./providers/google/... -count=1)
go test ./cmdutil ./cmd/serf ./cmd/serf-hub/internal/launchconfig ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub ./cmd/serf-tui -count=1
```

Expected: PASS.

- [ ] **Step 2: Run static and repository gates**

```bash
make vet
make lint
make test
```

Expected: every command exits 0. Read and fix every warning or failure; do not mute tests or reduce coverage.

- [ ] **Step 3: Run race tests for the changed concurrency-sensitive packages**

```bash
(cd agent && go test -race ./internal/installid ./internal/jobstore . -count=1)
go test -race ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -count=1
```

Expected: PASS, especially installation replacement and hub indexing.

- [ ] **Step 4: Run final format/single-codepath searches**

```bash
rg -n --glob '*.go' --glob '!**/*_test.go' 'ulid\.(Make|New)|oklog/ulid|func (ProjectID|ProjectSlug|projectSlug)\(' .
git grep -n '16-hex\|project-hash\|26-character ULID' -- README.md docs agent cmd cmdutil
```

Expected: no stale production code; documentation matches the new contract or explicitly describes inert legacy data.

- [ ] **Step 5: Request a fresh spec-compliance and code-quality review**

Give the reviewer the approved spec, this plan, the full commit range from `cbab0b92..HEAD`, and exact verification outputs. Require concrete findings with file/line references. Fix every confirmed issue through a new test-first commit.

- [ ] **Step 6: Verify final repository state and commit any review fixes**

```bash
git status --short --branch
git log --oneline --decorate cbab0b92..HEAD
```

Expected: clean `identifier-refactor` worktree and a linear series of focused commits. If review fixes were needed, commit them without amending earlier commits.
