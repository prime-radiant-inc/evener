# Native Worktree Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the native worktree tools spec (rev 9) — `manage_worktree` tool with six operations, git-native occupancy locks, disposal primitives, session resume, and delegate worktree isolation — in one delivery.

**Architecture:** A pure decision core (`agent/internal/worktree`) holds every unit-testable/fuzzable rule (name validation, projectid, sidecar codec, lock markers, porcelain parsing, the lock state table, merge predicates). The `agent` package wires it to session state (env swap, tool handlers, delegate lifecycle, close hooks). Detection lives in `agent/execenv` + a new `internal/gitpath` for local-FS callers. Integration tests run **real git** (2.43 on this machine) against `t.TempDir()` repos.

**Tech Stack:** Go 1.25, real `git` in tests, afero only where the codebase already uses it, native Go fuzzing (`go test -fuzz`), table-driven tests.

**THE NORMATIVE SPEC** is `docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md` (rev 9, in this worktree). Every task below names its governing sections. Where this plan and the spec disagree, **the spec wins** — report the conflict, don't improvise. The spec's §5 "Lock state machine (normative summary)" table is the single most important artifact: Task 9 implements it verbatim.

## Global Constraints

- Git floor: `git worktree add --lock --reason` support (git ≥ 2.33), preflight-checked once per session; clear error when too old; **no degraded mode** (spec §3 step 6).
- Name regex: `^[A-Za-z0-9_][A-Za-z0-9_./-]*$`, max 100 bytes, plus explicit rejects: `..` anywhere, leading `-`, trailing `/`, `@{` (spec §2). Git is the final validator via `git check-ref-format --branch <name>`.
- Lock markers: `serf:<session-id>` and `serf:dlg:<delegate-id>:<parent-session-id>`. Reasonless or unparseable reasons are **foreign**. Lock-taking is idempotent per spec §5 (unlocked→lock, own→adopt, foreign→refuse). `git worktree lock` on an already-locked tree is FATAL even with an identical reason — never call it blind.
- All base/HEAD resolution via `git -C <activeRoot> rev-parse --verify --quiet <ref>^{commit}`; the resolved SHA is ALWAYS passed explicitly to `git worktree add` (spec §2 "Base resolution").
- Creation is atomic: `git worktree add --lock --reason "serf:<sid>" -b <name> -- <path> <sha>` (spec §3 step 6).
- Branch deletion NEVER uses `git branch -d`'s built-in check; serf's merge gate first, then `-D` (spec §5 remove step 9).
- Merged predicate: two arms (ancestry `git merge-base --is-ancestor <tip> <target-tip>`; patch-equivalence `git cherry <target-tip> <tip> <base_sha>` all-`-`), target = recorded `merge_target` local or remote-tracking tip, NEVER HEAD (spec §5 prune sweep 1).
- Sidecar: flat `.meta/<name with / → %2F>.json`, written O_EXCL BEFORE `worktree add`, mtime-judged grace in reconciliation (spec §3 step 5, §6).
- `s.mu` is NEVER held across a git subprocess; env swap pre-warms `GitRootOrEmpty` outside the lock (spec §7).
- The agent package must not import `cmdutil` (spec §6).
- Shell-escape every interpolated token via a shared helper; never hand-build command strings (spec §2).
- Serf conventions: gofmt-clean, `golangci-lint run` clean, `make test-race` green. Test output pristine. NEVER write tests that test mocks; integration tests use real git. TDD: failing test first, then code, then green, then commit.
- Commit style: conventional (`feat(worktree): …`, `test(worktree): …`), each ending with the Claude-Session trailer used on this branch (copy from `git log -1 --format=%B`).
- Gate command for every task: `gofmt -l . | grep -v vendor` (must be empty), `go vet ./...`, `golangci-lint run <changed packages>`, `go test -race -count=1 <changed packages>`.

## File Map

| Path | Role |
|---|---|
| `agent/execenv/gitpath.go` (modify) | `ResolveMainRepoRoot` + kind-keyed cache |
| `internal/gitpath/gitpath.go` (create) | `ResolveMainRepoRootLocal` for hub/CLI (no execenv dep) |
| `cmd/serf/run.go`, `cmd/serf/serve.go` (modify) | RuntimeDir keyed off main root |
| `agent/internal/worktree/name.go` (create) | name validation, projectid, %2F encoding |
| `agent/internal/worktree/sidecar.go` (create) | sidecar type + O_EXCL codec + grace |
| `agent/internal/worktree/marker.go` (create) | lock marker format/parse |
| `agent/internal/worktree/porcelain.go` (create) | `git worktree list --porcelain` parser (C-unquote) |
| `agent/internal/worktree/lockstate.go` (create) | THE state table as a pure `Decide` function |
| `agent/internal/worktree/predicates.go` (create) | unchanged / merged(2-arm) / adopted, over injected `runGit` |
| `agent/internal/worktree/preflight.go` (create) | git version parse + floor check |
| `agent/session_tools_worktree.go` (create) | six operations, `worktreeGuard`, control env |
| `agent/internal/tool/definitions.go` (modify) | `DefManageWorktree()` |
| `agent/session.go`, `agent/session_tools.go`, etc. (modify) | `currentEnv()` accessor conversions, swap helper |
| `agent/internal/schema` SessionMeta (modify) | worktree persistence fields |
| `agent/session_init.go` (modify) | resume re-entry + init-inside locking |
| `cmd/serf-hub/internal/hubcore/tree.go`, `cmd/serf-hub/app_threadlifecycle.go` (modify) | restore-root migration |
| `agent/internal/jobstore` (modify) | shell-job workdir, delegate `Isolation`, disposed flag |
| `agent/job_delegate.go`, `agent/subagents.go` (modify) | isolation spawn/restore/revival |
| `agent/session_lifecycle.go` (modify) | close-unlock + disposal hook |
| `docs/worktrees.md` (create) | user-facing doc |

---

## Phase 0 — Detection

### Task 1: `ResolveMainRepoRoot` in execenv

**Files:** Modify `agent/execenv/gitpath.go`; Test `agent/execenv/gitpath_mainroot_test.go` (new); Fuzz in `agent/execenv/gitpath_fuzz_test.go` (new).
**Spec:** §1 entire (all 6 algorithm steps + cache rules). Read it before writing code.
**Interfaces (Produces):**
```go
// ResolveMainRepoRoot returns the main repository root for cwd, resolving
// linked-worktree .git pointer files structurally and falling back to the
// git binary. Returns "" when unresolvable.
func ResolveMainRepoRoot(env ExecutionEnvironment, cwd string) string
// pure core, fuzzable: parses "gitdir: <path>" content; returns the main
// root iff the pointer matches the .git/worktrees/<id> shape.
func mainRootFromGitdirPointer(pointerContent, ancestorDir string) (string, bool)
```
- [ ] Step 1: failing tests. Real-git fixtures via `t.TempDir()`: (a) main repo → its root; (b) linked worktree (`git worktree add`) → MAIN root, not worktree dir; (c) launched in a repo *subdirectory* whose env `RootDir` is the subdir (structural walk must use `os`, not env FS); (d) `--git-common-dir` absolute-output handling (worktree case; assert no `/wt/abs/...` mangling); (e) submodule → submodule working-tree root via the sanity-check fallback (`git submodule add`; two submodules of one super get different roots); (f) pointer-file resolution WITHOUT git binary (structural path: run with `PATH` shimmed to hide git); (g) not a repo → "". Also cache: same env asks worktree-root (GitRootOrEmpty) and main-root for one cwd → different answers, both cached (assert one fork each via a counting PATH shim).
- [ ] Step 2: run, confirm failures.
- [ ] Step 3: implement per spec §1 exactly (structural primary, `--git-common-dir` fallback with `filepath.IsAbs` branch + candidate sanity check + submodule `--show-toplevel` recovery). Second cache field keyed by lookup kind.
- [ ] Step 4: green + gate.
- [ ] Step 5: fuzz target `FuzzMainRootFromGitdirPointer(f *testing.F)` — seed with real pointer contents, `gitdir: ../..`, absolute, `worktrees/x`, `modules/x`, garbage, NULs; property: never panics, returned root (when ok) has the `worktrees` shape stripped. Run `go test -run=xxx -fuzz=FuzzMainRootFromGitdirPointer -fuzztime=30s ./agent/execenv`.
- [ ] Step 6: commit `feat(execenv): ResolveMainRepoRoot with structural linked-worktree resolution`.

### Task 2: `internal/gitpath` local resolver

**Files:** Create `internal/gitpath/gitpath.go`, `internal/gitpath/gitpath_test.go`.
**Spec:** §1 "Active content root vs stable identity root".
**Interfaces (Produces):** `func ResolveMainRepoRootLocal(cwd string) string` — same algorithm, direct `os` + `os/exec` (no execenv dependency; hub/CLI callers).
- [ ] Failing tests (same fixture matrix as Task 1 a-e,g; share fixture helpers via a small `gitpathtest` helper in the test file — do NOT import agent/execenv).
- [ ] Implement (extract the shared pure core if trivially shareable via `agent/execenv` export is NOT allowed — `internal/gitpath` must not import `agent/execenv`; duplicate the ~40-line core with a comment cross-referencing Task 1, or move the pure core to `internal/gitpath` and have execenv wrap it — prefer the latter: `agent/execenv` MAY import `internal/gitpath`).
- [ ] Green + gate + commit `feat(gitpath): local-filesystem main-root resolver`.

### Task 3: RuntimeDir + hub launch keying

**Files:** Modify `cmd/serf/run.go` (~line 101), `cmd/serf/serve.go` (equivalent RuntimeDir call ~line 152), hub launch trust resolver (find via `grep -rn "RuntimeDir\|trust" cmd/serf-hub/internal/`); Tests beside each.
**Spec:** §1 "Runtime state keying at launch" + hub dual-root paragraph.
- [ ] Failing test: origin-less repo, launch cwd inside a linked worktree → same state dir as launching from the main root (drive the run.go state-dir derivation function; extract it to a testable helper if currently inline).
- [ ] Implement: pass `gitpath.ResolveMainRepoRootLocal(cfg.workDir)` (fallback `cfg.workDir`) as RuntimeDir's workDir arg in run.go + serve.go. Hub: trust/meta keyed off stable root, config content read from active root.
- [ ] Green + gate + commit `feat(cli): key runtime state off the resolved main repo root`.

## Phase 1 — env-swap discipline

### Task 4: `currentEnv()` accessor + read audit

**Files:** Modify `agent/session.go` (accessor + mu-discipline comment), plus every file in spec §7's read list; Test `agent/session_env_race_test.go` (new).
**Spec:** §7 "`currentEnv()` accessor" — re-grep `s\.env\b` at implementation time; convert whole functions, not just listed lines.
**Interfaces (Produces):** `func (s *Session) currentEnv() execenv.ExecutionEnvironment` (mirrors `currentProfile()`, session.go:455-459).
- [ ] Failing race test: `-race` run where one goroutine swaps env via a test hook while others exercise tool dispatch/status events/child creation (mirror `session_close_race_test.go` patterns; use `agent/testkit_test.go` helpers).
- [ ] Implement accessor; convert ALL unlocked reads (grep fresh); update the mu comment.
- [ ] Green (`go test -race ./agent/...`) + gate + commit `refactor(agent): lock-disciplined currentEnv accessor`.

### Task 5: `swapEnvAndRefresh` helper

**Files:** Modify `agent/session.go` or new `agent/session_env_swap.go`; Test `agent/session_env_swap_test.go`.
**Spec:** §7 "envInfo + system-prompt refresh" — the two-step (outside-mu compute + pre-warm; under-mu assign + cache rebuilds) is normative, including the `GitRootOrEmpty(newEnv, newWD)` pre-warm.
**Interfaces (Produces):**
```go
// swapEnvAndRefresh computes envInfo + git snapshot + prewarms the git-root
// cache against next OUTSIDE s.mu, then atomically installs env+envInfo and
// rebuilds tool/prompt caches under s.mu.
func (s *Session) swapEnvAndRefresh(next *execenv.LocalExecutionEnvironment)
```
- [ ] Failing tests: (a) after swap, envInfo.WorkingDir/GitBranch/etc reflect the new root and the system prompt cache contains the new dir; (b) **no git fork while s.mu is held**: PATH-shim `git` with a script that signals a channel; hold a test hook that records `s.mu` state (add an internal test-only hook or assert via mutex TryLock from the shim's signal handler — simplest robust design: the shim writes a marker file; the test wraps s.mu in a guard counter via an exported-for-test setter); include the FIRST post-swap prompt render in the window.
- [ ] Implement per spec steps 1-2 verbatim.
- [ ] Green + gate + commit `feat(agent): atomic env swap with outside-lock git snapshot`.

## Phase 2 — worktree core (`agent/internal/worktree`)

### Task 6: names, projectid, encoding

**Files:** Create `agent/internal/worktree/name.go`, `name_test.go`, `name_fuzz_test.go`.
**Spec:** §2 "name validation", §6 "projectid", §6 sidecar encoding paragraph.
**Interfaces (Produces):**
```go
func ValidateName(name string) error            // regex + explicit rejects + 100-byte cap; git check-ref-format is the caller's job (needs git)
func ProjectID(canonicalAbsRoot string) string  // <safe-basename>-<sha256hex[:16]>
func EncodeSidecarName(name string) string      // "/" -> "%2F", + ".json" appended by caller? NO: returns encoded name WITHOUT extension
func DecodeSidecarName(encoded string) (string, bool)
```
- [ ] Failing table tests: accepts `feature/foo`, `dlg_01H…`, `my_feature`, `a.json/b`; rejects `..`, `-x`, `a/`, `a@{1}`, `a b`, 101-byte, empty, leading `.`; projectid: same basename different roots differ, 48-byte basename truncation, unsafe chars sanitized, fixed length; encode/decode round-trips all legal names, `a` vs `a.json/b` encodings differ and neither prefixes the other's file name.
- [ ] Implement.
- [ ] Fuzz: `FuzzValidateName` (never panics; accepted names also pass a `git check-ref-format --branch` oracle when git available — use `testing.Short()` guard), `FuzzSidecarNameRoundtrip` (encode→decode == identity for accepted names; encoded contains no `/`).
- [ ] Green + gate + commit `feat(worktree): name validation, projectid, sidecar encoding`.

### Task 7: sidecar codec

**Files:** Create `agent/internal/worktree/sidecar.go`, `sidecar_test.go`, `sidecar_fuzz_test.go`.
**Spec:** §6 "Metadata sidecar" (fields verbatim), §3 step 5 (O_EXCL), §5 sweep 2 (mtime grace).
**Interfaces (Produces):**
```go
type Sidecar struct {
    Name             string `json:"name"`
    Branch           string `json:"branch"`
    BaseSHA          string `json:"base_sha"`
    MergeTarget      string `json:"merge_target,omitempty"`
    OriginalRoot     string `json:"original_root"`
    CreatorSession   string `json:"creator_session"`
    DelegateID       string `json:"delegate_id,omitempty"`
    WorktreeRemoved  bool   `json:"worktree_removed,omitempty"`
    TipSHAAtRemoval  string `json:"tip_sha_at_removal,omitempty"`
    CreatedAt        string `json:"created_at"`
}
func WriteSidecarExcl(metaDir, name string, sc Sidecar) error  // O_CREATE|O_EXCL|O_WRONLY
func ReadSidecar(metaDir, name string) (Sidecar, error)
func UpdateSidecar(metaDir, name string, mutate func(*Sidecar)) error
func DeleteSidecar(metaDir, name string) error
func ListSidecars(metaDir string) ([]Sidecar, error)
func SidecarAge(metaDir, name string) (time.Duration, error)   // file mtime, for the grace
const ReconcileGrace = 15 * time.Minute
```
- [ ] Failing tests: O_EXCL loser errors with `os.IsExist`; round-trip all fields; Update preserves unknown-field-free JSON; List skips non-.json noise; mtime age.
- [ ] Implement. Fuzz: `FuzzSidecarJSONRoundtrip` (marshal/unmarshal identity), `FuzzReadSidecar` (arbitrary bytes never panic).
- [ ] Green + gate + commit `feat(worktree): O_EXCL sidecar codec with mtime grace`.

### Task 8: markers + porcelain parser

**Files:** Create `agent/internal/worktree/marker.go`, `porcelain.go` + tests + fuzz.
**Spec:** §5 markers, §5 list (C-quoting note), §5 "reasonless is foreign".
**Interfaces (Produces):**
```go
type Marker struct { SessionID, DelegateID string } // DelegateID=="" for session markers
func FormatSessionMarker(sid string) string                 // "serf:<sid>"
func FormatDelegateMarker(dlgID, parentSID string) string   // "serf:dlg:<dlg>:<sid>"
func ParseMarker(reason string) (Marker, bool)              // false => foreign (incl. empty)
type PorcelainEntry struct { Path, Head, Branch string; Bare, Detached bool; Locked bool; LockReason string; Prunable bool; PrunableReason string }
func ParsePorcelain(out string) []PorcelainEntry            // handles C-quoted reason payloads
func CUnquote(s string) string
```
- [ ] Failing tests: marker round-trips; `serf:` alone, `serf:dlg:x` (missing part), empty, `random text` → foreign; porcelain fixtures captured from REAL git in the test (`git worktree list --porcelain` on a temp repo with a locked-with-spaces-and-newline reason, a prunable entry (mv the dir away), detached and branch entries) — assert exact parse.
- [ ] Implement. Fuzz: `FuzzParsePorcelain` (never panics, entries' Path non-empty), `FuzzParseMarker`, `FuzzCUnquote` (round-trip against a Go-quoting oracle for printable strings).
- [ ] Green + gate + commit `feat(worktree): lock markers and porcelain parsing`.

### Task 9: THE lock state table

**Files:** Create `agent/internal/worktree/lockstate.go`, `lockstate_test.go`, `lockstate_fuzz_test.go`.
**Spec:** §5 "Lock state machine (normative summary)" — implement the table VERBATIM, one row per event. This is the heart of four review rounds; the test file enumerates EVERY cell.
**Interfaces (Produces):**
```go
type LockState int    // Unlocked, OwnSession, OwnDelegate, Foreign
type LockEvent int    // EvCreate, EvLeave, EvEnter, EvEnterCurrent, EvRestoreLand, EvInitInside, EvResumeReenter, EvRemoveTarget, EvRemoveCurrent, EvDelegateCreate, EvDelegateRevive, EvDisposeUnchanged, EvDisposeChanged, EvPruneCandidate
type LockAction int   // ActNone, ActLock, ActAdopt, ActUnlock, ActUnlockProceed, ActRefuse, ActWarnCoOccupy, ActRefuseToRestoreRoot, ActSkip, ActAtomicAddLock
func Decide(ev LockEvent, st LockState) LockAction
func ClassifyReason(reason string, ownSID, ownDlgID string) LockState
```
- [ ] Failing test: a table-driven test with one case per cell of the spec table (≈ 15 events × up to 3 states — write them ALL out with a comment citing the spec row). `Decide` must be total: unknown combos return a defined `ActRefuse` (fail safe), never panic.
- [ ] Implement as a switch — boring, explicit, greppable.
- [ ] Fuzz: `FuzzDecideTotal` (any int pair → no panic, result in valid range) and `FuzzClassifyReason`.
- [ ] Green + gate + commit `feat(worktree): normative lock state table as pure decision core`.

### Task 10: predicates over injected git

**Files:** Create `agent/internal/worktree/predicates.go`, `predicates_test.go` (REAL git).
**Spec:** §5 prune sweep 1 "disposable" (both arms, remote-tracking targets, unknown-target), sweep 2 adopted rule, §5 remove step 9.
**Interfaces (Produces):**
```go
type GitRunner func(args ...string) (stdout string, err error) // rooted at main repo
func Unchanged(run GitRunner, wtPath, baseSHA string) (bool, error)      // clean status AND tip==base
func CleanTree(run GitRunner, wtPath string) (bool, []string, error)     // status --porcelain=v1 --untracked-files=all; returns offending lines
func Merged(run GitRunner, tipSHA, mergeTarget, baseSHA string) (MergedResult, error)
type MergedResult struct { Merged bool; Arm string /* "ancestry"|"cherry"|"" */; TargetRef string; TargetUnknown bool }
func Adopted(tipSHA, baseSHA, tipAtRemoval string) bool  // tip ∉ {base, tipAtRemoval}; pure
```
- [ ] Failing REAL-git tests, one fixture repo each: true merge → ancestry arm; fast-forward → ancestry; **rebase-merge → cherry arm**; **single-commit squash → cherry arm**; **multi-commit squash → NOT merged**; detached-HEAD main root reviewing the tip → NOT merged (the rev-6 destruction case); target parked behind but `refs/remotes/origin/<target>` ahead → merged via remote-tracking tip; target branch deleted → TargetUnknown; `Adopted` truth table incl. reset-back-to-base → false.
- [ ] Implement (`git cherry <target-tip> <tip> <base>` all-lines-`-` check; try `refs/heads/<t>` then each `refs/remotes/*/<t>` via `git for-each-ref`).
- [ ] Green + gate + commit `feat(worktree): unchanged/merged/adopted predicates with real-git tests`.

### Task 11: git preflight

**Files:** Create `agent/internal/worktree/preflight.go` + test + fuzz.
**Spec:** §3 step 6, §8 preflight error row.
**Interfaces (Produces):** `func CheckGitVersion(run GitRunner) error` (parses `git version x.y.z...`, floor 2.33, memoize per session at the caller); `func parseGitVersion(s string) (major, minor int, ok bool)`.
- [ ] Failing tests: `git version 2.43.0` ok; `2.32.9` → error naming 2.33; `git version 2.33.0.windows.1` ok; garbage → error. Fuzz `FuzzParseGitVersion`.
- [ ] Implement + green + gate + commit `feat(worktree): git version preflight (>=2.33)`.

## Phase 3 — the tool

### Task 12: definition + registration

**Files:** Modify `agent/internal/tool/definitions.go` (`DefManageWorktree()`); Create `agent/session_tools_worktree.go` skeleton with `registerWorktreeTool(reg, deps)` + wire into the registry init (find `registerXTools` call sites); Tests: visibility in `ToolDefinitions()` as registry-only non-read-only (mirror existing registry-tool tests in `agent/session_init_registry_test.go`).
**Spec:** §2 Registration + Definition (args table verbatim) + description usage-policy paragraph.
- [ ] Failing test → implement definition (operation enum + per-op args, description carrying the "not for ordinary branch work" policy) → green + gate + commit `feat(agent): manage_worktree tool definition and registration`.

### Task 13: `worktreeGuard` + `create`

**Files:** Extend `agent/session_tools_worktree.go`; Test `agent/session_tools_worktree_create_test.go` (REAL git).
**Spec:** §3 all 9 steps + §2 control env + base resolution; §7 worktreeGuard method list.
**Interfaces (Produces):**
```go
type worktreeGuard struct { /* closures over Session, per spec §7 */ }
// state() worktreeState; controlEnv(mainRoot) ; enterWorktree(path) ; exitWorktree() ; liveWorkUnder(path) []string
func (s *Session) worktreeCreate(name, baseRef string) (WorktreeResult, error)
type WorktreeResult struct { Path, Branch, BaseSHA, MainRoot string }
```
- [ ] Failing REAL-git integration tests (drive through the session tool surface via `agenttest` fake-LLM session where practical, plus direct handler tests): worktree exists + `.git` pointer file; env swapped (subsequent read_file resolves inside); sidecar written BEFORE add (crash-sim: force add failure via pre-existing D/F branch `feature` vs `feature/foo` → error AND sidecar cleaned same-call); atomic lock present with own marker right after create (porcelain assert); base = ACTIVE worktree HEAD when created from inside another worktree (fixture with divergent tips); explicit `base_ref` (branch/tag/SHA/remote-tracking, and rejects `-x`, whitespace, nonexistent); create-away unlocks the old worktree; branch-exists error suggests switch only when managed worktree exists; non-local env → clear error; preflight too-old git → clear error (PATH-shim an old `git version` reporter).
- [ ] Implement per §3 exactly.
- [ ] Green + gate + commit `feat(agent): manage_worktree create with atomic lock and sidecar-first ordering`.

### Task 14: `switch` + `exit`

**Files:** Extend handler; Test `agent/session_tools_worktree_switch_test.go` (REAL git).
**Spec:** §4 entire (by-name steps 1-6 incl. no-op rule and lock-target-first; by-path steps 1-4 incl. managed-path reroute; exit steps 1-6 incl. restore-relock).
- [ ] Failing tests: switch between two managed worktrees (lock moved: old unlocked, new locked; envInfo refreshed; saved restore env untouched); switch-to-current → no-op, lock KEPT; foreign-locked target → refuse naming reason; by-path to a registered sibling worktree (created manually via `git worktree add` outside managed dir) → entered, NO lock mutation; by-path with symlinked spelling → canonicalized accept; unregistered path → reject; by-path resolving inside managed dir → full by-name choreography (locked after entry); exit → env restored, saved env cleared, worktree+branch+sidecar intact, unlocked; exit outside worktree → clear error, no side effects; exit restoring into a managed launch-root worktree → idempotent relock (foreign → warn-and-continue, assert warning surfaced); create→exit→switch round-trip re-locks.
- [ ] Implement.
- [ ] Green + gate + commit `feat(agent): manage_worktree switch/exit with lock choreography`.

### Task 15: `remove`

**Files:** Extend handler; Test `agent/session_tools_worktree_remove_test.go` (REAL git).
**Spec:** §5 remove steps 1-11 verbatim.
- [ ] Failing tests: clean remove; dirty without force → error LISTING files, env unchanged; force removes dirty; delete_branch on merged branch → `-D` after gate passes; on UNMERGED branch → refusal with evidence, branch survives, sidecar kept with `worktree_removed:true` + `tip_sha_at_removal`; detached-HEAD-review fixture → serf gate refuses where `-d` would succeed (assert `-d` never invoked via PATH-shim recording git argv); branch checked out elsewhere → git refusal surfaced with location; foreign lock → refuse regardless of force; own-marker crash residue (lock a non-current managed wt with own marker manually) → auto-unlock + proceed; remove-current → restore + relock rules; remove-current with no safe restore env → refusal; live-work guard: background shell job with workdir under target (needs Task 20's field — use a stub recorder here and mark the cross-task wire-up ⚠️ for Task 20 to complete); cross-creator sidecar → refuse without force.
- [ ] Implement.
- [ ] Green + gate + commit `feat(agent): manage_worktree remove with merge-gate branch deletion`.

### Task 16: `list` + `prune`

**Files:** Extend handler; Test `agent/session_tools_worktree_prune_test.go` (REAL git, the densest file).
**Spec:** §5 list steps 1-3; §5 prune sweeps 1-3 + skip taxonomy + report shape.
- [ ] Failing tests: list does NOT run `git worktree prune` (mv a non-managed worktree dir away; after list it's still registered); staleness fields (age/dirty/ahead/merged-per-target/creator/lock/prunable) correct on a 3-worktree fixture; prefix-collision filtering (`projectidA` vs `projectidAB`); symlinked worktreeRoot canonicalization; prune sweep 1: collects unlocked+clean+unchanged, collects unlocked+clean+merged (ancestry AND cherry fixtures), skips locked/dirty/live/sidecar-less/in-grace with reasons; prune sweep 2: stale sidecar (no wt no branch) deleted post-grace, fresh sidecar survives (grace), adopted branch (tip moved) → sidecar dropped + branch kept + reported, reset-to-base branch → collected, checked-out branch → skip `checked out at <path>`, unmerged residue kept; sweep 3: repo-wide git prune runs ONLY when all prunable entries are managed (fixture: non-managed prunable → skipped + reported, still registered after).
- [ ] Implement.
- [ ] Green + gate + commit `feat(agent): manage_worktree list and three-sweep prune`.

### Task 17: error surfaces + arg fuzz + ordering

**Files:** Extend handler + `agent/session_tools_worktree_errors_test.go`; extend the existing `FuzzToolArgsValidate` table (find it: `agent/tool_args_fuzz_test.go`).
**Spec:** §8 every bullet = one test; §2 serialization paragraph.
- [ ] Failing tests: every §8 row asserted verbatim (error text contains the named elements); same-response ordering (read-only call before manage_worktree sees old env, after sees new — drive via agenttest scripted parallel tool-call response).
- [ ] Extend fuzz table with `manage_worktree` args (operation × name/path/base_ref/force/delete_branch).
- [ ] Green + gate + commit `test(agent): worktree error surfaces and arg fuzzing`.

## Phase 4 — persistence, resume, hub

### Task 18: SessionMeta + resume re-entry + init-inside lock

**Files:** Modify schema SessionMeta (find via `grep -rn "EnvInfo" agent/internal/schema/` → session meta struct), `agent/session_state.go`, `agent/session_init.go`; Tests beside.
**Spec:** §7 "Persistence and resume" entire (including managed flag, restore root, idempotent lock rule on re-entry, foreign → restore root + notice, gone → notice) + §5 init-inside row.
**Interfaces (Produces):** SessionMeta gains `WorktreePath string`, `WorktreeManaged bool`, `WorktreeRestoreRoot string` (json tags snake_case matching existing style).
- [ ] Failing tests: meta round-trip; RestoreSessionFromMeta with active managed worktree → env rooted there, restore env recorded, lock taken (unlocked case) / adopted (same-id stale case) / foreign → restore root + notice event; path-entered non-managed → re-entered, NO lock; worktree gone → restore root + notice; init with launch cwd inside a managed worktree → lock taken (foreign → warning surfaced, continues).
- [ ] Implement (re-enter BEFORE initSessionState per spec).
- [ ] Green + gate + commit `feat(agent): worktree persistence and resume re-entry`.

### Task 19: hub consumer migration

**Files:** Modify `cmd/serf-hub/internal/hubcore/tree.go` (~325-336), `cmd/serf-hub/app_threadlifecycle.go` (~246), `cmd/serf-hub/spawn.go` (~245); Tests beside each.
**Spec:** §7 "Hub consumers of the persisted working dir must migrate".
- [ ] Failing tests: sidebar grouping uses restore root when worktree active (meta fixture with WorktreePath set → grouped under restore-root basename, not `dlg_01H…`); spawn prefill = restore root; resume args `--dir` = restore root (re-entry handles the rest).
- [ ] Implement + green + gate + commit `feat(hub): read worktree-aware session meta via restore root`.

## Phase 5 — delegate isolation

### Task 20: shell-job workdir + `liveWorkUnder`

**Files:** Modify jobstore record (+ shell job creation site `agent/job_shell.go` / `agent/session_tools_shell.go`) to record launch workdir; implement `worktreeGuard.liveWorkUnder`; wire Task 15's guard fully; Tests.
**Spec:** §5 remove step 4 ("New plumbing" sentence), §7 guard method.
- [ ] Failing tests: background shell job records env.WorkingDirectory() at launch; liveWorkUnder returns it for equal/below paths; remove refuses while such a job lives even after the session switched elsewhere; delegate WorkingDir + subagent envs enumerated too.
- [ ] Implement + green + gate + commit `feat(agent): live-work guard over recorded launch workdirs`.

### Task 21: isolation spawn/restore/revival

**Files:** Modify `agent/internal/tool/definitions.go` (DefDelegate gains `isolation` enum), `agent/internal/jobstore` restore descriptor (+`Isolation string` + fold event), `agent/job_delegate.go` (creation path ~187, revival ~444-487, restore env ~841-861), `agent/subagents.go` (deny plumbing); Tests.
**Spec:** §9 Tool surface + Lifecycle steps 1-3 + step 2's named new plumbing (deny survives restore AND all-tools types) + §7 revival re-lock paragraph.
- [ ] Failing tests: delegate with isolation → managed worktree named `<delegate_id>`, sidecar has delegate_id + parent sid, `serf:dlg:` lock atomic at add, child env rooted at lane, restore descriptor WorkingDir = lane + Isolation set; `manage_worktree` absent from child's tools at spawn, for an all-tools agent type, AND after restore; second job via delegate_send runs in the same lane; per-job result carries path/branch/ahead/dirty; revival on a kept (unlocked) lane re-locks; revival on a foreign-locked lane refuses with clear error; parent switch into live lane refused (foreign dlg lock).
- [ ] Implement + green + gate + commit `feat(agent): delegate worktree isolation with restore-safe deny`.

### Task 22: disposal + close-unlock + resumability stat

**Files:** Modify `agent/session_lifecycle.go` (close hook between children-close and closeStoreOnly), `agent/job_delegate.go` (`assessDelegateResumability` gains disposed-flag + WorkingDir stat), jobstore (disposed event + fold); Tests `agent/session_worktree_close_test.go`.
**Spec:** §9 step 4 (evaluate → unlock → non-force remove → mark → branch+sidecar; dirty refusal ⇒ re-lock+keep), step 5 (both defenses), step 6; §5 close-unlock of own worktree.
- [ ] Failing tests: unchanged lane at close → removed (wt+branch+sidecar+lock gone) and descriptor marked disposed AFTER removal; changed lane → unlocked, kept, still resumable, close output lists it; killed-mid-job dirty lane → kept; close inside own managed worktree → unlocked on disk; parent close→resume→delegate_send to disposed lane → clear refusal; crash-sim between remove and mark (delete lane dir manually, leave descriptor) → `assessDelegateResumability` refuses on the stat; store-closed ordering (disposal mark append succeeds — assert disposal runs before closeStoreOnly); disposal remove hitting a racing dirty write → downgrades to keep + re-lock.
- [ ] Implement + green + gate + commit `feat(agent): close-time lane disposal and occupancy unlock`.

## Phase 6 — polish, coverage, docs

### Task 23: coverage push to ~100% + fuzz corpus

**Files:** wherever gaps are.
- [ ] Run `go test -count=1 -coverprofile=/tmp/wt.cov ./agent/... ./internal/gitpath/... && go tool cover -func=/tmp/wt.cov | grep -E "worktree|gitpath"` — every new file ≥95%, pure cores 100%. Close every gap with a REAL test (no hollow tests; if a branch is unreachable, delete it or document why).
- [ ] Run each fuzz target 60s (`make fuzz` conventions — check `FUZZTIME`); fix anything found; commit seeds under `testdata/fuzz/` where valuable.
- [ ] Commit `test(worktree): coverage to target + fuzz corpus`.

### Task 24: docs + skill note

**Files:** Create `docs/worktrees.md` (user-facing: operations, lock semantics, disposal, delegate isolation, limitations — distilled from spec §§2-9,11, NOT a copy); modify `docs/architecture.md` cross-link if a tools list exists there; append a short "native tool" note to the serf-side skill surface IF `docs/skills/` has a worktree skill (check; the cross-repo superpowers skill update is out of scope for this branch — record it in the final report instead).
- [ ] Write, lint prose, commit `docs(worktrees): user-facing worktree tool documentation`.

### Task 25: final whole-branch review + gate

- [ ] `make test-race` full repo; `golangci-lint run ./...`; `gofmt -l` empty; `make fuzz FUZZTIME=30s` (or repo equivalent) green.
- [ ] Dispatch the final whole-branch reviewer (superpowers requesting-code-review template, most capable model) with `scripts/review-package $(git merge-base origin/main HEAD) HEAD`.
- [ ] Fix Critical/Important via ONE fix subagent; re-review; record Minor triage.
- [ ] Commit fixes; final ledger update.

## Self-review notes (per writing-plans)

- Spec coverage: §1→T1-3; §2→T6,12,13; §3→T13; §4→T14; §5→T8,9,10,15,16,22; §6→T6,7; §7→T4,5,18,19; §8→T17; §9→T20-22; §10 mapped across all task tests; §11 documented in T24; §12 phases = this plan's phases.
- The one §10 item not covered above — "Root semantics" (docs/skills/MCP from active root while placement keys off stable root) — is asserted inside T1 (e) + T13 (env-swap assertions) via the GitRootOrEmpty-vs-ResolveMainRepoRoot distinction; T13's test list includes a project-docs-read-from-worktree assertion. T13 implementer: add it.
- Type consistency: `GitRunner` defined T10, consumed T11,13,15,16,22. `Marker`/`ParseMarker` T8 → T9's `ClassifyReason` wraps it. `Sidecar` T7 → T13,15,16,21,22. `Decide` T9 → every handler task.
- Live model e2e (spec §12 phase 6) is intentionally NOT a task here: no provider credentials should be assumed overnight; the final report must list it as the one remaining validation step.
