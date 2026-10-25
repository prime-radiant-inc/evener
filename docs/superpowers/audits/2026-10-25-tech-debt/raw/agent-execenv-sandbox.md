# Tech-debt audit: agent/execenv and agent/sandbox

Slice id: `agent-execenv-sandbox`. Audited at `a6c0b9c561` (origin/main). Read-only; no tests were run.

## Overview and inventory

| Package | Non-test files | Non-test LOC | Test files | Test LOC |
|---|---|---|---|---|
| agent/execenv | 46 | 12,572 | 132 | 33,325 |
| agent/sandbox (incl. sandboxtest) | 35 | 8,041 | 47 | 15,502 |

Largest files: `execenv/local.go` 3,647 lines (123 top-level funcs, 84 methods on one struct, a 32-field struct), `securepath_browse.go` 1,096, `securepath_fdops_unix.go` 1,009, `gitignore.go` 934, `search_patterns.go` 902, `sandbox/resolve.go` 1,356, `probe.go` 791, `env_floor.go` 646, `gitdir.go` 640.

Shape of the slice:

- `execenv` has exactly one production implementation of `ExecutionEnvironment` (`LocalExecutionEnvironment`), plus two test doubles in `agent/internal/agenttest`. Around 12 "optional capability" interfaces sit on top of it, each with a type-assert fallback in `agent/session_tools_*.go`.
- Every file tool has up to three arms: sandboxed (`sandboxFS`), unsandboxed-scratch (`scratchSandboxFor`) and plain OS (afero). Search (grep/glob/list_dir) has an extra budgeted-versus-legacy axis and a ripgrep-versus-native axis, so the same behavior is implemented several times and kept equal by parity comments and tests.
- `sandbox` is a policy resolver plus bwrap/seatbelt argv builders plus session scratch lifecycle, and also ships ~800 lines of test-harness code in the production package (`contract*.go`).
- Test code is 2.7x (execenv) and 1.9x (sandbox) the production code, with about 34 package-level mutable test seams in production code.
- History/milestone narration is pervasive: 110 matches in execenv and 53 in sandbox for patterns like `M2`, `#4568 round 2`, `kata 31gh`, `legacy`, `byte-identical to before`.

Product-doc anchors touched by fixes: `docs/product/subsystems.md` row S14 (execenv/sandbox) and S13 (scratch), `docs/sandboxing.md`, `docs/developing-evener/environment.md`. Each fix below that changes ownership or file layout must update those in the same change. None of the fixes below change user-visible behavior; they are refactors guarded by the existing behavior tests.

## Findings

### agent-execenv-sandbox-1 (high) local.go is a 3,647-line god file around a 32-field god struct

Locations: `agent/execenv/local.go:70-247` (struct), `:275-1100` (sandbox-layer cache plus three scratch regimes), `:1326-1560` (OS and login-shell probes), `:1570-2060` (read/edit/fuzzy match), `:2066-2905` (list/glob/grep), `:2909-3260` (exec/stream/detach), `:3410-3647` (path resolve and env filtering).

Evidence:

- `LocalExecutionEnvironment` carries `sbMu/sbfs/sbfsScratch/scratchFS/scratchFSRoot/retiredFS`, `ownedSessionTmp`, `scratchRoot/scratchSession`, `unsandboxedScratch(+Failed)`, `unsandboxedTmp(+Failed)`, `detachedSpawned`, `scratchMovedOut`, `sandboxGrant`, six instance-local test seams, and a `scratchMu` whose doc says it guards "both per-session scratch fields" while it actually guards five. Lock-order rules ("sbMu then scratchMu", never across another env's scratchMu) live in prose comments.
- Scratch lifecycle alone is ~800 lines of local.go: `sessionScratchPath`, `wrapperlessScratchDir`, `scratchSandboxFor`, `unsandboxedTmpDir`, `unsandboxedScratchDir`, `newSessionScratch`, `settleSessionScratch`, `retainUnsandboxedScratch`, `RetainSessionScratch`, `AdoptSessionScratch`, `DisposeSandboxScratch`, `DisposeUnsandboxedScratch`, `endTmpContainerLocked`.
- Doc comments longer than the code they describe (for example `newSessionScratch` carries a 12-line deadlock post-mortem).

Impact: every change to a file tool, the exec path or scratch ownership touches the same file and the same lock set; reviewers cannot see what a mutex guards; the file is slow to diff and merge across lanes.

Fix plan (behavior-preserving, mechanical, then structural):

1. Mechanical split by file within package `execenv`, no signature changes: `local_scratch.go`, `local_exec.go` (ExecCommand/Stream/Detach), `local_files.go` (read/write/edit), `local_search.go` (list/glob/grep), `local_probe.go` (Platform/OSVersion/LoginShellPATH), `local_env.go` (env filtering, venv PATH). `local.go` keeps the struct and constructors. Zero risk because the package compiles identically.
2. Extract a `sessionScratchOwner` struct (owned scratch, unsandboxed scratch, tmp container, identity, `detachedSpawned`, `scratchMu`) with the Adopt/Settle/Retain methods on it, and embed one pointer in `LocalExecutionEnvironment`. Existing scratch lifecycle fuzzers (`sandbox_lifecycle_test.go`, scratch-move tests) are the guard; run them in CI.
3. Update the S13/S14 rows of `docs/product/subsystems.md` links if they point at `local.go` line ranges (they point at directories today, so likely no change).

Size: step 1 moves ~3,600 lines with ~0 net change; step 2 touches ~600 lines (net -100). Risk: low for step 1, medium for step 2 (lock ordering); keep step 2 a separate PR.

### agent-execenv-sandbox-2 (high) Every file operation repeats a three-arm dispatch, and WriteFile duplicates WriteFileRaw

Locations: `local.go:1591-1640` (ReadFileAndBytesCtx), `:1742-1783` (WriteFile), `:1804-1883` (EditFileWithCtx), `:2066-2073` (FileExists), `mutator.go:261-278` (ReadFileRawCtx), `:280-308` (WriteFileRaw), `:310-338` (RemovePath), `:346-392` (RenamePath).

Evidence: each of these eight methods begins with the same sequence: `if sfs := e.sandbox(); sfs != nil { defer release; requireLiveSandboxRootForRelative; sfs.op(...) }`, then `abs := e.resolve(path); if sfs := e.scratchSandboxFor(abs); sfs != nil { ... }`, then `requireLiveWorkingDirForRelative; resolveWrite; afero op`. `WriteFile` and `WriteFileRaw` are the same function except for the tool name, the perm (0o644 vs argument) and the returned message. `EditFileWithCtx` has to track `sandboxArm := sfs != nil` separately because the scratch arm reuses the `sfs` variable, which makes its control flow hard to follow (`local.go:1805-1832`).

Impact: a policy fix (for example, the dead-working-dir check) must be made in eight places; a miss is a silent confinement gap on one arm. Reviewers already compensate with "mirror" comments.

Fix plan: add one private `func (e *LocalExecutionEnvironment) fileTarget(tool, path string, write bool) (layer *sandboxFS, abs string, err error)` that returns the chosen layer (sandbox, scratch, or nil for the OS arm) and the resolved absolute path after applying the right `require*` checks. Rewrite the eight methods as `layer, abs, err := e.fileTarget(...)` followed by two branches (layer or afero). Make `WriteFile` call `WriteFileRaw(path, data, 0o644)` and format the message. Guard with the existing sandbox and scratch tests (`securepath_test.go`, `sandbox_lifecycle_test.go`, apply_patch tests). Add one table test that asserts each of the eight operations is denied identically in a read-only sandbox, so a future miss is red.

Size: about -250 lines net across local.go and mutator.go. Risk: medium (security-relevant dispatch), so do it behind the table test first.

### agent-execenv-sandbox-3 (high) grep and glob walks are implemented twice, line for line

Locations: `local.go:2721-2905` (`grepNativeSkipping`, off arm) versus `securepath_browse_fdops_unix.go:188-323` (`sandboxFS.grepNative`) and `:325-364` (`grepNamedFile`); `local.go:2212-2287` (`GlobWithBudget` off arm) versus `securepath_browse_fdops_unix.go:118-186` (`sandboxFS.glob`).

Evidence:

- Both grep walks contain the same ~90-line closure: cancellation check, `globBudgetError` pass-through with the same 8-line "this guard is only reachable when a directory grows between passes" comment (and a different test name in each), directory skip rules (dot prefix, `ignores.matches`, `walkSkipsPseudoDir`), `budget.listing(true)` with a copied 6-line comment, hidden/ignored file skip, `grepFileSelected`, read, `errGrepFileTooLarge` accounting, `a.feed`, `finishWalk`. The only differences are the fs.FS handed to the walk, the extra `s.underMasked(abs)` skip and the single-file arm.
- The single-file "named file larger than cap" handling exists twice with different shapes (`local.go:2840-2866` and `securepath_browse_fdops_unix.go:339-362`).
- The glob bodies match each other down to the `seen` map, `excluded` counter and `sortPathsByMtimeDesc` call; only the `underMasked` filter and the fs construction differ.
- Stub variants for non-unix exist in `securepath_other.go:115-121`.

Impact: any change to ignore, budget or cap semantics must be made twice and tested twice (the repo's own tests literally contain `TestGrepWalk…` and `TestSandboxedGrepWalk…` pairs). Drift is already visible (the single-file arm is handled differently in each).

Fix plan: introduce `type searchWalk struct { fsys fs.FS; base string; masked func(abs string) bool; ... }` and one `grepWalk(ctx, w searchWalk, a *grepAccum, ...)` and one `globWalk(...)`. The off arm builds `searchWalk` with `boundedDirFS` and no mask; the sandbox arm builds it with `secureDirFS` and `underMasked`. Delete `grepNativeSkipping`'s body and `sandboxFS.grepNative`'s body. Keep the platform-specific parts (opening the base fd) outside the shared walk. Guard: the existing pairs of tests become one parameterized test that runs both arms; the budget-growth-between-passes tests keep their stubs (`grepWalk` and `secureBrowseWalkDir` vars would collapse to one).

Size: about -300 lines. Risk: medium; the sandbox arm is a security boundary, so land it after finding 2's table test and keep the symlink-refusal tests as the oracle.

### agent-execenv-sandbox-4 (medium) list_dir has four walkers and three copies of the DirEntry builder

Locations: `local.go:2089-2150` (`listDirectory`, off legacy), `listdir.go:223-295` (`listDirWalk`, off budgeted), `securepath_fdops_unix.go:771-784` and `:786-888` (`walkDirFd` and `walkDirFdBudgeted`, which itself carries a `budget == nil` legacy branch).

Evidence: the 12-line block that builds a `DirEntry` (IsSymlink, Size, ModTime, IsRegular, IsExec) is repeated in `local.go:2111-2125`, `listdir.go:262-282` and `securepath_fdops_unix.go:838-856`. `execenv.go:260-265` and `listdir.go:17` state that the unbudgeted "legacy" walk exists for environments without `DirListBudgeter` (test doubles); production callers `agent/memory_page.go:284` and `ListDirectory` consumers could use the budgeted walk with an unlimited budget.

Impact: sort order, dot filtering and entry fields must be kept equal across four code paths by tests (comments such as "as in the legacy walk" appear in six places).

Fix plan: make `ListDirectory`/`ListVisibleDirectory` call `listDirWalk`/`walkDirFdBudgeted` with an unlimited `DirListBudget` (needs a `NewUnlimitedDirListBudget`, or `nil` handled once at the top), delete `listDirectory`'s closure walk, delete `walkDirFd` and the `budget == nil` branch, and extract `newDirEntry(ent, info)`. Behavior-preserving; the existing listing tests (59 uses of `ListDirectory` in tests) are the guard.

Size: about -150 lines. Risk: low to medium (sorted-order parity).

### agent-execenv-sandbox-5 (medium) ripgrep arm: ~700 lines whose job is to imitate the native arm

Locations: `local.go:2331-2379` (`buildRipgrepArgs*`), `:2398-2547` (rg branch of `GrepSkipping`), `:2549-2612` (`grepRgOversizeCount`, `grepRgListFiles`, `nulCounter`), `:2617-2713` (`grepTargetsOneFile`, `ripgrepOutputLines`, `ripgrepPathSeparator`), `rg_capture.go` (393 lines).

Evidence: `ripgrepOutputLines` re-sorts rg's parallel output to "the native walk's order" and re-inserts `--` separators (issue refs #3259, #3284); the oversize note shells out to `rg --files` twice to rebuild a count the native walk gets for free; `GrepSkipping` routes around rg for sandboxed sessions, for any `skip` predicate, for pseudo-fs roots (`grepTakesNativeArm`) and when rg is absent, so rg is only the fast path for unsandboxed, unskipped, ordinary-directory searches. `buildRipgrepArgs` (the one-filter wrapper) has no non-test caller.

Impact: every native grep rule (caps, notes, ordering, ignore handling) needs an rg-side equivalent, and parity bugs have already needed fixes. Repo rule: delete before adding.

Fix plan (needs Jesse's decision because it removes an implementation): benchmark native grep on a large tree in CI-style conditions. If acceptable, delete the rg arm, `rg_capture.go` and the listed helpers, keeping `findExecutable` only if other callers need it. If rg must stay, move the arm into `grep_rg.go`, delete `buildRipgrepArgs` and share one `grepNotes(...)` builder with `finishWalk` so note text is composed once.

Size: -700 lines if deleted, -50 if kept. Risk: performance regression on very large trees; verify in CI before deleting.

### agent-execenv-sandbox-6 (high) Optional-capability interfaces and Ctx/non-Ctx method families for a single implementation

Locations: `execenv.go:76-265` (interfaces), `local.go:1570-1600` (read variants), `:1785-1810` (edit variants), `:2194-2300` (glob variants), `:2381-2398` (grep variants), `agent/session_tools_shell.go:229-295`, `agent/session_tools_file.go:48-56,99-103`, `agent/internal/tool/apply_patch.go:51-60`.

Evidence:

- Interfaces: `DetachedExecutor`, `DetachSupportReporter`, `StreamingExecutor`, `ArgvExecutor`, `CommandWorkingDirectoryBoundary`, `RootBoundary`, `FileMutator`, `ContextualFileReader`, `ContextualFileMutator`, `GlobExcluder`, `GlobBudgeter`, `DirListBudgeter`. Each doc says "separate so existing implementers (test fakes) are unaffected".
- Only `*LocalExecutionEnvironment` implements them in production. Non-test `func (…) ReadFileCtx|GlobWithBudget|ListDirectoryWithBudget|GlobWithExclusions|ExecArgv|StreamCommand|DetachCommand` definitions exist only in `local.go`, `mutator.go` and `agenttest/env_deny.go`.
- Method families: ReadFile / ReadFileCtx / ReadFileAndBytes / ReadFileAndBytesCtx; EditFile / EditFileWith / EditFileCtx / EditFileWithCtx; Glob / GlobWithExclusions / GlobWithBudget; Grep / GrepSkipping / grepNative / grepNativeSkipping. `ReadFileAndBytes` (non-ctx) has no non-test caller at all, and `EditFileWith` (non-ctx) is called only by `EditFile`.
- The glob tool has a three-way `switch g := env.(type)` (`session_tools_shell.go:283-295`) and list_dir a two-way one whose default arm is "legacy whole-tree walk" for test doubles.

Impact: callers carry fallback branches that production never takes; each new capability adds an interface, a type switch and a fake. Test doubles drive the shape of the production API.

Fix plan: put the Ctx and budgeted forms on the `ExecutionEnvironment` interface (ReadFile(ctx, ...), Glob(ctx, ..., budget), ListDirectory(ctx, ..., budget), Grep(ctx, ...)) and delete the optional interfaces and their type switches. Give `agenttest.FakeEnv`/`DenyEnv` the new methods (they already return stubs). Delete non-ctx wrappers whose only caller is the next wrapper. Do it one family at a time (read, edit, glob, list, exec) so each PR is mechanical. This is an interface change touching `agent/` callers; raise with Jesse before starting since it changes the public execenv API. Update `docs/product/subsystems.md` S14 if it names the optional capabilities (it does not today).

Size: about -400 lines in execenv and agent. Risk: medium (wide caller set; compiler-guided).

### agent-execenv-sandbox-7 (medium) Path containment ("is p under dir") is re-implemented about ten times

Locations in slice: `execenv/securepath.go:497-506` (`pathUnder`, whose comment says "mirrors sandbox.pathUnder, kept local so execenv does not reach into an unexported helper"), `execenv/local.go:3491-3498` (`pathWithinRoot`), `execenv/project_resolver.go:302-309` (`pathContains`), `execenv/securepath_browse.go:631` (`pseudoRootCovers`) and `:736`, `execenv/gitpath.go:150` and `:289-298`, `execenv/open_beneath_root_unix.go:49`; `sandbox/gitdir.go:626-641` (`pathUnder` + `hasParentPrefix`), `sandbox/session_scratch.go:213-224` (`pathWithin`), `sandbox/bwrap.go:405-413` (`isUnderAnyRoot`) vs `sandbox/gitdir.go:589-597` (`underAnyRoot`), `sandbox/resolve.go:293-300` (`insideGitDir`), `sandbox/resolve.go:1116-1128` (`underByIdentity`). Outside the slice: `agent/session_tools_worktree.go:1685`, `internal/plugins/marketplaces.go:1449,1459`, `cmd/evener-hub/internal/fspaths/paths.go:124`, `cmd/evener-fuzzregistry/main.go:466`, `tools/tool-fluency/.../review.go:299`.

Evidence: the variants differ in subtle ways (Rel-based versus `HasPrefix(root+sep)`; with or without Clean; with or without empty-root guard; with or without symlink resolution), so they can disagree on the same input. `underAnyRoot` and `isUnderAnyRoot` are the same function except for a `r != ""` guard, and both do `p == r || pathUnder(p, r)` even though `pathUnder` already returns true for equality.

Impact: security-relevant predicate with divergent edge-case behavior (empty root, trailing separator, unclean paths); every fix must be hunted down.

Fix plan: create one tiny shared home, for example `execsupport/pathutil` (a module already exists for cross-package support: `execsupport/go.mod`), exporting `Under(p, dir string) bool` (clean, Rel-based, equality counts) and `UnderAny(p string, roots []string) bool`. Replace the in-slice copies first (sandbox and execenv both import it, removing the "kept local" excuse), then the out-of-slice copies in later PRs. Keep behavior-specific variants (`pathContains(resolveSymlinks)`, `underByIdentity`) as thin callers. Test strategy: one table test for edge cases (empty, equal, sibling-prefix `/a/bc` vs `/a/b`, trailing slash, `..`), then run the existing sandbox and securepath suites in CI.

Size: about -120 lines. Risk: low to medium; confirm each replaced call site's empty-root handling explicitly.

### agent-execenv-sandbox-8 (medium) Five different "resolve symlinks, tolerate missing tail" helpers

Locations: `execenv/gitpath.go:275-283` (`resolveClean`), `execenv/local.go:3521-3540` (`resolveSymlinksBestEffort`, iterative walk-up), `sandbox/gitdir.go:132-138` (`resolveCleanPath`), `sandbox/seatbelt.go:70-81` (`canonicalizeLongestPrefix`, recursive) wrapped by `sandbox/resolve.go:1014-1016` (`resolveExistingPrefix`), `identifier/git.go:85-90` (`resolveClean`), plus `agent/session_tools_worktree.go:1816` (`canonicalOrClean`). `sandbox/rootguard.go:152-190` adds `CanonicalPath`/`CanonicalDir`.

Evidence: `execenv.resolveClean`, `sandbox.resolveCleanPath` and `identifier.resolveClean` are byte-identical 6-line functions. `resolveSymlinksBestEffort` and `canonicalizeLongestPrefix` solve the same "longest existing prefix + re-append tail" problem with different loops. The gitpath copy is labeled "Compatibility wrapper retaining the old local normalization seam".

Impact: three independent places to fix when symlink or Data-volume spelling rules change; the macOS firmlink handling is only in the sandbox variant.

Fix plan: put `ResolveClean` and `ResolveExistingPrefix` next to `Under` in the shared helper from finding 7; have `sandbox.resolveExistingPrefix` keep the Data-volume strip as a thin caller. Delete the five copies. The seatbelt injected-eval seam stays (it exists so Linux tests can exercise the darwin walk).

Size: about -60 lines. Risk: low.

### agent-execenv-sandbox-9 (high) Git worktree/pointer parsing and validation duplicated across execenv, identifier and sandbox

Locations: `execenv/project_resolver.go:118-227` versus `identifier/git.go:92-190`; `execenv/gitpath.go:192-220`; `sandbox/gitdir.go:143-250`.

Evidence:

- `isSubmoduleGitDirShape` is byte-identical at `project_resolver.go:218-228` and `identifier/git.go:180-190`. `pointerTarget` and `validateSubmodulePointer` are identical (`project_resolver.go:207-216,189-205` versus `identifier/git.go:127-136,162-178`). `validateLinkedPointer` equals `validateLinkedWorktreePointer` modulo statement order. `localStructuralMainRoot` mirrors `mainCheckoutLocal` (same `.git` walk, same pointer branches), differing in explicit stat-error handling and `resolveClean`.
- `structuralWorktreeRoot` (`gitpath.go:192-220`), `hasGitEntryAncestor` (`:222-241`), `envHasGitEntryAncestor` (`project_resolver.go:104-116`) and `sandbox.findGitEntry` (`gitdir.go:229-250`) are four more "walk up to the nearest .git" loops. `sandbox.gitDirUnderModules` re-implements the `.git/modules` test a third time.
- The `identifier` package comment-less copy and the execenv copy diverge in one behavior (execenv returns `handled=false` for submodules and falls to git; identifier calls `gitBinaryMainRootLocal` directly).

Impact: three implementations of the same classification (main checkout, linked worktree, submodule) with different failure behavior; a git layout fix has to be rediscovered in each.

Fix plan: make `identifier` the single owner of `.git` pointer classification: export `ClassifyGitEntry(dir) (kind, gitDir, commonDir, err)` and `NearestGitEntry(cwd)`. Have `execenv.localStructuralMainRoot`, `execenv.structuralWorktreeRoot` and `sandbox.ClassifyWorkspace/findGitEntry` call it and add only their own policy (protected paths in sandbox, `handled`/definitive semantics in execenv). Delete the duplicated validators and `isSubmoduleGitDirShape` from execenv. Guard: existing `gitpath_mainroot_test.go`, project-resolver fuzzers and sandbox `ClassifyWorkspace` tests, plus one shared fixture table (main, linked, submodule, nested submodule, symlinked `.git`, malformed pointer).

Size: about -250 lines. Risk: medium (fail-closed behavior in sandbox classification must stay identical; sandbox returns errors for unknown shapes, execenv tolerates some).

### agent-execenv-sandbox-10 (low) Test-only pass-through wrappers and "compatibility" shims

Locations and evidence:

- `execenv/gitpath.go:240-273`: `mainRootFromGitdirPointer`, `parseGitdirPointer`, `mainRootCandidateFromCommonDir` each one-line wrappers labeled "Compatibility wrapper around the shared identifier helper"; no non-test caller (grep of non-test files finds only the definitions).
- `execenv/gitpath.go:313` `var splitPathComponents = strings.Split`.
- `local.go:3542-3544,3594-3596` `filteredEnvWithPolicy` and `filteredEnv` (5 and 6 test uses, 0 production callers); `local.go:2331-2337` `buildRipgrepArgs` and `:2715-2719` `grepNative` (test-only); `:360-373` `allocatedSessionScratchPath` and `:434-436` `SessionScratchDir` both just call `sessionScratchPath`.
- `local.go:3647` `ShellEscapeArgs` is a one-line alias of `shellquote.Args` with a 20-line comment that says the name "is kept for its callers" (one non-test caller: `rungit.go:62`).
- `local.go:2300-2314`: the long `Grep` doc comment is attached to `resolveGrepDir` while `Grep` (line 2381) has its own shorter comment.

Impact: dead surface that tests keep alive; comments mislead.

Fix plan: delete the wrappers, point the few tests at the real functions (`identifier.*`, `filteredEnvWithSource(…, os.Environ())`), inline `allocatedSessionScratchPath`, delete `ShellEscapeArgs` in favor of `shellquote.Args`, and fix the misplaced doc comment. Per the repo's no-new-compat rule, deleting is the default.

Size: about -90 lines. Risk: low.

### agent-execenv-sandbox-11 (medium) About 34 mutable package-level test seams in production code, several wrapping `strings.Split`

Locations: `local.go:1360-1394` (`execLookPath, execCommandContext, execCommand, runtimeGOOS, osVersionOutput, shellStat, grepReadFile, grepWalk, listReadDir, streamBeforeSignalOnce, streamAfterTimer, streamOutputCopyStart, wrapperWithPolicy, splitEditLines, venvCandidateDirs, loginShellPATHOutput`), `securepath.go:28` (`securePathRel = filepath.Rel`), `securepath_browse.go:1054` (`sortPathStat`), `securepath_browse_fdops_unix.go:22`, `securepath_fdops_unix.go:415`, `gitignore.go:290`, `gitpath.go:313`, `search_patterns.go` (`SetMaxGlobMatchesForTesting`), `sandbox/bwrap.go:14-15,435`, `sandbox/gitdir.go:15-16` (`gitRel = filepath.Rel`), `sandbox/resolve.go:11` (`resolveAbs = filepath.Abs`), `sandbox/env_floor.go:417`, `sandbox/probe.go:240`, `sandbox/backend.go:31`.

Evidence: `splitEditLines = strings.Split` is assigned in `runtime_edges_fuzz_test.go:112-115` to return `nil` so a branch of `nearestFileRegion` can be reached; `securePathRel`, `gitRel`, `resolveAbs` and `splitPathComponents` each have exactly two production references (the declaration and one call) and two test assignments. `streamBeforeSignalOnce` and `streamAfterTimer` are hooks called from inside `StreamCommand`'s production closure. The struct also carries six instance-local seams (`commandFactory`, `lookPath`, …) with a comment saying instance seams avoid "global swaps in deterministic fuzzers"; both styles coexist.

Impact: tests that stub a stdlib function to reach an impossible error branch are coverage-driven, not behavior-driven (`docs/developing-evener/testing.md`). Package globals force serial tests and race with parallel ones.

Fix plan: delete the seams that wrap pure stdlib calls (`splitEditLines`, `securePathRel`, `gitRel`, `resolveAbs`, `splitPathComponents`, `listReadDir`, `shellStat`) together with the tests that only exist to reach the unreachable branch (and remove those branches if they can never fire). Convert the remaining time/race hooks (`streamBeforeSignalOnce`, `streamAfterTimer`, `beforeGoEnvFileOpen`, `scratchSandboxForAfterRootRead`) to instance fields, matching the existing instance-seam style, or keep and document only those that gate a real race test. Flag any test that asserts against a stubbed return to Jesse per the mock-testing rule.

Size: about -200 lines (seams, branches and tests). Risk: low; coverage may drop only for branches that cannot execute, which should be deleted rather than covered.

### agent-execenv-sandbox-12 (medium) Narrating, milestone and issue-number comments are stale or wrong

Locations (examples): `sandbox/denial.go:11-28` ("It carries no live behavior in M1 — no code path produces one yet", but `execenv/securepath.go:242` and `agent/session_tools_web.go:29` construct it); `execenv/local.go:858-870` ("production does not reach it yet … the --sandbox flag stays feature-gated"); `local.go:91-104` ("M2 consults it…", "M3 attaches it only in tests/integration… until M5"); `sandbox/resolve.go:116` and `:134-136` ("carried inert … until a backend consumes it", "M2 satisfies"); `sandbox/contract.go:22-30,164,310,345` (M1/M2/M3/M6); `sandbox/probe.go:211,323`; `execenv/audit.go:22` ("M5, when --sandbox goes live"); `execenv/execenv.go:20` ("#4568 round 2"); `local.go:1285,3643` and others ("kata 31gh", "pre-consolidation rendering"). Counts: 110 history-marker matches in execenv, 53 in sandbox.

Evidence: docs say sandboxing is live (`docs/product/subsystems.md` S14: "sandboxing defaults off", escalation flows implemented); the code comments still describe a pre-launch state. `LocalExecutionEnvironment.Sandbox` comment promises "exactly today's behavior".

Impact: comments contradict the code and the product docs; a new reader cannot tell which are true. Repo rule: comments describe what code does and why, not history.

Fix plan: one comment-only pass per package: delete milestone labels, issue/round numbers and "today/before/legacy" phrasing; keep the invariant the comment protects. Do it in the same PRs as findings 1-3 for the files they touch so we do not add a separate whitespace-only churn PR. Gate: no code change, so `make lint` plus targeted `go vet ./agent/execenv ./agent/sandbox` is enough.

Size: about 300 comment lines edited or removed. Risk: none.

### agent-execenv-sandbox-13 (medium) Three process launchers duplicate setup and two terminate state machines

Locations: `local.go:2946-3058` (`execPreparedCommandStdout`), `:3060-3172` (`StreamCommand`), `:3181-3251` (`DetachCommand`).

Evidence: all three do `resolveCommandWorkingDir`, `injectLocalVenvPath(e.commandEnvironment(env), []string{dir, e.RootDir})`, then `commandRuntimeConfig{...}`, `cmd.Configure`, `cmd.Start`, `cmd.PID`. Two of them also do `lookPathInEnv(args[0], env)` into `config.ExecutablePath` with identical code (`:2965-2969`, `:3235-3239`). The graceful-then-kill ladder exists as an inline select in `execPreparedCommandStdout` (`:3006-3030`) and as a closure with `doneOnce`/`signalOnce` in `StreamCommand` (`:3107-3149`). `StreamCommand` guards `ctx != nil` three times, and `DetachCommand` once (`:3188`); `ExecCommand` does not, so nil ctx is only half-supported.

Impact: a change to how env or cwd is prepared (for example the scratch/TMPDIR rules in `DetachCommand`) must be mirrored; the two kill ladders can drift (one already retains `bufferedOutputDrainer`, the other does not).

Fix plan: extract `prepare(workingDir, envVars) (dir string, env []string, err error)` and `startTracked(cmd, config)`; move the ladder into a `commandRuntime` helper `terminateGracefully(done <-chan struct{}, grace)` used by both. Drop nil-ctx guards (context.Context must not be nil; a nil ctx would already panic in `ExecCommand`). Guard with the existing process-runtime fuzzers (`process_runtime_program_fuzz_test.go`) in CI.

Size: about -120 lines. Risk: medium (process lifecycle); keep the hooks used by the race tests.

### agent-execenv-sandbox-14 (medium) gitignore matching carries a 380-line regexp-cost estimator the code itself calls best-effort

Locations: `gitignore.go:316-600` (`compiledIgnoreRules`, `ignoreLineExpansion`, `classifyIgnoreLine`, `ignoreClassEnd`, `ignorePropertyEnd`, `countUnicodeClasses`, `countedRepetitionFactor`, `validRepetitionBound`, `repetitionBound`), `securepath_browse.go:918` (`readIgnoreFileBytes`).

Evidence: the doc on `classifyIgnoreLine` (`:355-372`) says "This estimate is best-effort, not a proof of the budget it feeds … produced by a second parser that has to agree byte-for-byte with go-gitignore's rewrite plus Go's regexp/syntax, and there is no fixed point short of reimplementing that parser, so a later construct can still slip past. The follow-up is to charge the real compiled program size instead; see issue #1971."

Impact: ~300 lines of fragile parsing (hard-coded `globStarUnits = 5` "measured about five instructions") defend against pathological `.gitignore` files; correctness depends on staying in sync with a third-party library's internals.

Fix plan: do what the comment says: compile each rule with `regexp/syntax` + `syntax.Compile` (or a bounded `regexp.Compile` under a per-rule size cap) and charge `len(prog.Inst)`; delete the estimator and its constants. If `go-gitignore` compiles internally and cannot expose the program, replace `sabhiram/go-gitignore` with a small matcher that translates rules to a segment matcher (no regexp). Needs a design decision from Jesse (new matcher versus cost via compile). Guard with the existing fuzz targets for ignore rules.

Size: -250 to -300 lines. Risk: medium (must not regress the memory-exhaustion protection; keep the adversarial fixtures).

### agent-execenv-sandbox-15 (low) Dead production audit sink

Locations: `execenv/audit.go:1-52`, `execenv/securepath.go:217`.

Evidence: `auditSink` is a package var that is only assigned in tests (`securepath_test.go:291`, `securepath_edge_program_fuzz_test.go:106-109`); no non-test code references `AuditRecord`, `auditSink` or `auditDenial` beyond the definition and the single call in `sandboxFS.deny`. The comment says "Production wiring (M5, when --sandbox goes live) points it at the real logger"; M5 has shipped without wiring it.

Impact: dead feature with exported type, redaction logic and tests; extra work on every denial.

Fix plan: delete `audit.go`, the call in `deny`, and the tests asserting audit records. If audit logging is wanted, it is a product decision to raise with Jesse, not a leftover hook.

Size: -52 lines plus tests. Risk: none.

### agent-execenv-sandbox-16 (low) Environment filtering repeats one loop four times, and Scrub duplicates it again

Locations: `local.go:3546-3592` (`filteredEnvWithSource` with three inline loops), `:3603-3626` (`filteredEnvFrom`), `sandbox/secret_env.go:23-33` (`ScrubSecretEnv`).

Evidence: each branch does "iterate inherited, skip keys overridden by extra, keep allowed, then append extra". `EnvPolicyAll`, `CoreOnly` and `Default` differ only in the allow predicate on inherited and whether extra is filtered by `IsSecretEnvName`. `filteredEnvFrom` is `ScrubSecretEnv` plus the override rule. `inheritedEnv`/`filteredEnv` hold two additional entry points (`filteredEnv`, `filteredEnvWithPolicy`) that production never calls.

Fix plan: one `mergeEnv(inherited []string, extra map[string]string, allowInherited, allowExtra func(name string) bool) []string`, with the policy switch only choosing predicates. Delete `filteredEnv*` variants that have no production caller. Guard with the existing env policy tests (`local_test.go`, six `filteredEnvWithPolicy` uses).

Size: about -45 lines. Risk: low.

### agent-execenv-sandbox-17 (low) Dead "legacy marker" branch in Cleanup

Locations: `local.go:73`, `:1286-1316`.

Evidence: `runningPIDs` is only written with `commandRuntime` values (`local.go:2981,3096`). The "legacy marker value falls back to the historical PID process-group signals" branch, with `procgroup.Terminate/Kill(pid)`, is only reachable from tests that store `struct{}{}` or a string key (`local_test.go:1543`, `process_runtime_program_fuzz_test.go:567`, `runtime_edges_fuzz_test.go:90`).

Fix plan: type the map (`map[int]commandRuntime` with a mutex, or `sync.Map` with a typed accessor), delete the legacy branch and the tests that store foreign values. Violates the repo rule on backward compatibility without approval otherwise.

Size: about -40 lines plus tests. Risk: low.

### agent-execenv-sandbox-18 (medium) Test-harness code ships in the production sandbox package

Locations: `sandbox/contract.go` (500 lines), `contract_git.go`, `contract_git_evenerfuzz.go`, `sandbox/sandboxtest/sandboxtest.go` (a package that already exists for this purpose).

Evidence: `contract.go:9-13` defines a `TestingT` interface "so package sandbox stays free of a testing dependency in the production binaries (cmd/evener) that import it", i.e. the harness is knowingly linked into production binaries. Consumers are only `sandbox/contract_test.go` and `execenv/sandbox_escape_test.go`. `sandbox/sandboxtest` already imports `testing` and exists for the same reason.

Impact: ~560 lines of test fixtures (golden floor matrix, git worktree materializer) compiled into every binary; two parallel test-support locations.

Fix plan: move `ContractCases`, `AssertResolve`, `ReRootCases`, `AssertReRoot`, `MaterializeWorkspace` and the git harness into `sandbox/sandboxtest` using `*testing.T`; export the two unexported helpers they need (`pathUnder` via the shared helper from finding 7, `resolveCleanPath` via finding 8). Update the two consumers' imports. No product-doc change.

Size: ~560 lines moved, ~40 removed (the TestingT shim). Risk: low.

### agent-execenv-sandbox-19 (low) Two scratch-container types with the same lifecycle, and Go env file location computed twice

Locations: `sandbox/session_tmp.go:93-255` (`SessionTmp`) versus `sandbox/session_scratch.go:37-90,228-256` (`SessionScratch`); `sandbox/probe.go:544-558` (`goEnvFile`) versus `sandbox/env_floor.go:503-523` (`goEnvFileFor`); `bwrap.go:405` versus `gitdir.go:590` (see finding 7).

Evidence:

- Both scratch types hold a `scratchLease`, implement `Retain`, `HasLease`, and a removal that refuses paths outside `sessionScratchPrefix`, and both call `removeTree`. `SessionTmp.Remove` re-implements the namespace refusal that `SessionScratch.Cleanup` has. `session_scratch.go:165-170` reuses `worldTempBaseCandidates`/`validWorldTempBase` from `session_tmp.go`. The execenv struct carries both types as separate fields with separate "failed" flags (`local.go:232-241`).
- `goEnvFileFor` says it is "mirroring goEnvFile and os.UserConfigDir for goos": two functions that must stay equal to answer "which go env file will `go` read".

Impact: two lifecycles and two sticky-failure flags for one concept ("a directory owned by this session for its life"); the go env location can drift between the probe and the per-spawn floor.

Fix plan: factor a small `leasedDir` (path, lease, base, `retain`, `remove`) used by both types; collapse `goEnvFile(system)` into `goEnvFileFor(vars, goos)` by passing the probe's environment as the vars map. Guard with `session_scratch_test.go`, `session_tmp_test.go`, `host_temp_bases_test.go`.

Size: about -80 lines. Risk: low to medium (lease semantics).

### agent-execenv-sandbox-20 (medium) resolve.go mixes policy resolution with 350 lines of path-spelling and carve-out helpers

Locations: `sandbox/resolve.go:388-546` (`Resolve`), `:921-992` (`scopesFor`), versus `:1014-1356` (`resolveExistingPrefix`, `withResolvedMasks`, `filterMasked`, `withEvenerConfigMask`, `sameDir`, `sameFile`, `underByIdentity`, `overlapsMaskByIdentity`, `onDiskSpelling`, `unmaskedContentRoots`, `CarveOutIntact`, `CarveOutResolvesTo`, `maskOverlapsCarveOut`, `carveOutEscapes`).

Evidence: the file is 1,356 lines; the last quarter has no dependence on the resolver's flow, only on `ResolvedPolicy.MaskedPaths` and spelling helpers. `rootguard.go` (208 lines) holds a second canonicalization family (`CanonicalPath`, `CanonicalDir`, `StripDataVolumeAlias`) and `disk_spelling_*.go` a third.

Impact: hard to find which spelling rule applies; the same-file/under-by-identity helpers sit beside unrelated resolution code.

Fix plan: mechanically split into `resolve.go` (Resolve, scopesFor, backend/cache choice), `resolve_masks.go` (mask and carve-out logic) and `pathspell.go` (spelling helpers, merged with `disk_spelling_*.go` and the helpers from findings 7-8). No signature changes.

Size: ~0 net (move). Risk: none.

### agent-execenv-sandbox-21 (low) Hand-rolled signal-name table

Locations: `execenv/process_signal_unix.go:22-61`.

Evidence: a 30-entry map literal is allocated on every `signalName` call, to produce names `golang.org/x/sys/unix.SignalName` already returns (`x/sys@v0.48.0/unix/syscall_unix.go:64`; `execenv` already imports `x/sys/unix` in `stream_pipe_unix.go`).

Fix plan: `if n := unix.SignalName(sig); n != "" { return n }; return fmt.Sprintf("SIG%d", sig)`. Verify names match for the signals tests assert (SIGTERM, SIGKILL, SIGSEGV) before deleting the map.

Size: -35 lines. Risk: low.

### agent-execenv-sandbox-22 (low) afero injection exists only as a test seam

Locations: `local.go:76,566-569,1118-1125`, `read_capped.go:9,43-100`, `mutator.go:303`.

Evidence: production never calls `SetFs` (0 non-test callers; 75 test calls). `readFileCappedCtx` carries a 20-line comment about not asserting `*os.File` because "SetFs accepts OS-backed wrappers (fault.FS, BasePathFs)" and about in-memory filesystems whose `FileInfo` carries no type bits. The sandboxed arms use raw fds, not afero, so tests that go through `SetFs` do not cover the confined path.

Impact: an indirection and a dependency that production does not need; admission code bends to support in-memory fakes, which are mock-style tests of a filesystem.

Fix plan: raise with Jesse first, since 75 tests use it. If agreed, replace with real temp dirs (`t.TempDir()`) and delete `SetFs`, `filesystem()` and the afero branches; fault injection moves to the few tests that really need it via an instance fs seam local to `readFileCappedCtx`.

Size: about -150 lines including test churn. Risk: medium (broad test edit).

### agent-execenv-sandbox-23 (low) Launch probes are over-built for `uname -rs` and a login-shell PATH

Locations: `local.go:1329-1568` (`Platform`, `OSVersion`, `LoginShellPATH`, `probeCapture`, `newProbeCapture`, `lastNonEmptyLine`, `lastNonEmptyLineAt`, `probeTrustedAnswer`, `resolveOSVersion`).

Evidence: ~240 lines, including a head/tail-trust protocol over `cappedOutputBuffer` for two commands whose answers are one short line, and a 20-line doc comment on `probeTrustedAnswer`. `Platform()` is a three-case switch that returns `runtimeGOOS` for two cases and `"linux"` otherwise.

Fix plan: keep the login-shell timeout and `orphanpipe` protection; replace the capture/trust protocol with `io.LimitReader` over stdout and take the last non-empty line; drop `probeCapture` and `lastNonEmptyLineAt`. Move to `local_probe.go` (finding 1) first.

Size: about -120 lines. Risk: low to medium (the #4557 OOM guard must stay; LimitReader satisfies it).

## Recommended fix sequence

1. Comment-only cleanup of stale milestone text (finding 12) and dead-code deletions that need no design: audit sink (15), legacy marker (17), test-only wrappers (10), signalName (21). Net -250 lines, no behavior change.
2. Shared path helpers (7, 8) in `execsupport/pathutil`; replace in-slice copies, then the `identifier` and out-of-slice copies in follow-ups.
3. Git pointer classification consolidation (9), with its fixture table.
4. Mechanical `local.go` and `resolve.go` file splits (1 step 1, 20).
5. File-operation dispatch table test, then the `fileTarget` refactor (2).
6. Search consolidation: shared grep/glob walk (3), list_dir collapse (4); then the rg decision (5) with Jesse.
7. Interface flattening (6) one family at a time, together with seam removal (11); ask Jesse before starting.
8. Launcher/env/scratch cleanups: launchers (13), env merge (16), scratch owner extraction (1 step 2), scratch types (19).
9. Move contract harness to `sandboxtest` (18).
10. Decisions that need Jesse: gitignore matcher approach (14), afero removal (22), probe simplification (23).

Each step ships as its own PR with evergreen docs updated where file layout or ownership claims change (S13/S14 rows in `docs/product/subsystems.md`, `docs/sandboxing.md` if it names moved symbols). Run targeted package tests in CI; do not run the full suite locally.
