# Section 4: Tool Execution Environment - Audit Findings

## Summary
8 gaps found (1 Important, 5 Minor, 2 Info)

## Findings

### GAP-4.01: No Windows shell support (cmd.exe /c)
- **Spec requirement:** "Use the platform's default shell (`/bin/bash -c` on Linux/macOS, `cmd.exe /c` on Windows)" (Section 4.2, line 767)
- **Current state:** `ExecCommand` always uses `/bin/bash -c` (env_local.go line 424). There is no conditional for Windows to use `cmd.exe /c`. The `Platform()` method does return `"windows"` for runtime.GOOS == "windows", but the shell selection ignores it.
- **Severity:** Minor (serf does not currently target Windows, and `syscall.Setpgid`/`syscall.Kill` would also fail on Windows)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (lines 69-78, 424)

### GAP-4.02: GrepOptions type not defined
- **Spec requirement:** `grep(pattern: String, path: String, options: GrepOptions) -> String` (Section 4.1, line 734)
- **Current state:** The interface uses expanded parameters instead of a `GrepOptions` struct: `Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int) (string, error)`. The spec references `GrepOptions` but never defines its fields as a RECORD, so the code's approach is a reasonable interpretation. However, the signatures don't match literally.
- **Severity:** Info (spec itself doesn't define GrepOptions; the code provides equivalent functionality with explicit parameters)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env.go` (line 36), `/Users/jesse/prime-radiant/serf/coding-agent-loop-spec.md` (line 734)

### GAP-4.03: EditFile method on interface not in spec
- **Spec requirement:** The ExecutionEnvironment interface (Section 4.1, lines 718-757) defines: `read_file`, `write_file`, `file_exists`, `list_directory`, `exec_command`, `grep`, `glob`, `initialize`, `cleanup`, `working_directory`, `platform`, `os_version`. No `edit_file` method is specified.
- **Current state:** The Go interface includes `EditFile(path string, oldString string, newString string, replaceAll bool) (string, error)`. This is an additional method beyond the spec. All implementations of `ExecutionEnvironment` must implement it.
- **Severity:** Minor (additive deviation; does not break spec contract, but expands the interface surface)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env.go` (line 33)

### GAP-4.04: Env var deny patterns broader than spec (Contains vs suffix match)
- **Spec requirement:** "exclude variables matching: `*_API_KEY`, `*_SECRET`, `*_TOKEN`, `*_PASSWORD`, `*_CREDENTIAL` (case-insensitive)" (Section 4.2, line 773). These are suffix-glob patterns (e.g., `*_API_KEY` matches vars ending with `_API_KEY`).
- **Current state:** The `filteredEnv` deny function uses `strings.Contains(uk, "API_KEY")` etc. (env_local.go line 557). This is broader than suffix matching: it would also deny vars like `API_KEY_VERSION`, `TOKEN_ENDPOINT`, or `SECRET_PATH` where the sensitive substring appears as a prefix or infix rather than a suffix. The code is more conservative (filters more aggressively), which is arguably safer but does not match the spec's glob patterns precisely.
- **Severity:** Minor (more restrictive than spec, so it errs on the safe side; unlikely to cause practical issues)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (lines 554-561)

### GAP-4.05: No LoggingExecutionEnvironment wrapper
- **Spec requirement:** Section 4.4 (lines 820-826) describes a `LoggingExecutionEnvironment(inner: ExecutionEnvironment)` wrapper that logs command execution and exit codes.
- **Current state:** No logging wrapper implementation exists anywhere in the codebase. The spec frames this as a composability demonstration, not a hard requirement ("Execution environments can be wrapped for cross-cutting concerns").
- **Severity:** Info (spec presents this as an example of composability, not a required implementation)
- **Files checked:** Searched entire codebase for `LoggingExecution`, `LoggingEnv`, `logging.*wrapper` -- no matches outside spec

### GAP-4.06: No ReadOnlyExecutionEnvironment wrapper
- **Spec requirement:** Section 4.4 (lines 828-834) describes a `ReadOnlyExecutionEnvironment(inner: ExecutionEnvironment)` wrapper that rejects write operations.
- **Current state:** No read-only wrapper implementation exists anywhere in the codebase. Like GAP-4.05, the spec frames this as a composability example.
- **Severity:** Minor (could be useful for subagent sandboxing; the spec presents it as a pattern not a requirement, but it has practical value)
- **Files checked:** Searched entire codebase for `ReadOnlyExecution`, `ReadOnlyEnv`, `read.?only.*wrapper` -- no matches outside spec

### GAP-4.07: Missing test coverage for TOKEN, PASSWORD, CREDENTIAL env var exclusion
- **Spec requirement:** "exclude variables matching: `*_API_KEY`, `*_SECRET`, `*_TOKEN`, `*_PASSWORD`, `*_CREDENTIAL`" (Section 4.2, line 773)
- **Current state:** The deny function in `filteredEnv` correctly checks all five patterns (API_KEY, SECRET, TOKEN, PASSWORD, CREDENTIAL) at line 557. However, test coverage only verifies `MY_API_KEY` and `MY_SECRET` exclusion (env_local_test.go lines 67-86). There are no tests for `*_TOKEN`, `*_PASSWORD`, or `*_CREDENTIAL` patterns.
- **Severity:** Minor (implementation is correct; test coverage is incomplete for 3 of 5 denial patterns)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env_local_test.go` (TestFilteredEnv_ExcludesSensitiveVars), `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (filteredEnv)

### GAP-4.08: Default filteredEnv allow-list missing language toolchain vars
- **Spec requirement:** "Always include: PATH, HOME, USER, SHELL, LANG, TERM, TMPDIR, language-specific paths (GOPATH, CARGO_HOME, NVM_DIR, etc.)" (Section 4.2, line 774)
- **Current state:** The `filteredEnv` function (default policy) has an allow-list at lines 562-572 that includes `PATH`, `HOME`, `USER`, `SHELL`, `LANG`, `TERM`, `TMPDIR`, `GOPATH`, `GOMODCACHE` but omits `CARGO_HOME`, `NVM_DIR`, `RUSTUP_HOME`, and `PYENV_ROOT`. These vars ARE present in the `EnvPolicyCoreOnly` allow-list (lines 531-537). In practice, this is not a functional gap because the default policy inherits all non-sensitive vars anyway -- these vars would pass through the non-deny path. The allow-list in the default policy serves as a safety net in case the deny function accidentally matches them, and since none of these contain sensitive substrings, they are correctly inherited. However, the allow-lists are inconsistent between the two policies.
- **Severity:** Minor (functionally correct due to default pass-through behavior, but the allow-lists should be consistent for maintainability)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (lines 531-537, 562-572)

## Fully Implemented (Verified)

### ExecutionEnvironment Interface (Section 4.1)
- **read_file(path, offset, limit):** Implemented as `ReadFile(path string, offsetLine *int, limitLines *int) (string, error)`. Supports offset/limit, line numbers, image base64 encoding, binary detection. (`env.go` line 30, `env_local.go` lines 82-120)
- **write_file(path, content):** Implemented as `WriteFile(path string, content string) (string, error)`. Creates parent directories automatically. (`env.go` line 31, `env_local.go` lines 122-131)
- **file_exists(path):** Implemented as `FileExists(path string) bool`. Uses `os.Stat`. (`env.go` line 33, `env_local.go` lines 220-223)
- **list_directory(path, depth):** Implemented as `ListDirectory(path string, depth int) ([]DirEntry, error)`. Recursive walk with configurable depth, returns sorted entries with size for files. (`env.go` line 37, `env_local.go` lines 225-265)
- **exec_command(command, timeout_ms, working_dir, env_vars):** Fully implemented with all spec requirements. (`env.go` line 39, `env_local.go` lines 411-489)
- **grep(pattern, path, ...):** Implemented with ripgrep preference and native Go regex fallback. (`env.go` line 36, `env_local.go` lines 297-409)
- **glob(pattern, path):** Implemented using `doublestar` library for filesystem globbing. Results sorted by modification time. (`env.go` line 35, `env_local.go` lines 267-295)
- **initialize() / cleanup():** Both implemented. Cleanup terminates running processes with SIGTERM -> 2s wait -> SIGKILL. (`env.go` lines 22-24, `env_local.go` lines 43-65)
- **working_directory():** Returns `RootDir`. (`env.go` line 26, `env_local.go` line 67)
- **platform():** Returns "darwin", "linux", or "windows" based on `runtime.GOOS`. (`env.go` line 27, `env_local.go` lines 69-78)
- **os_version():** Returns `GOOS/GOARCH` string. (`env.go` line 28, `env_local.go` line 80)

### ExecResult Record (Section 4.1)
- All five fields implemented with correct types and JSON tags: `Stdout`, `Stderr`, `ExitCode`, `TimedOut`, `DurationMS`. (`env.go` lines 5-11)

### DirEntry Record (Section 4.1)
- All three fields implemented: `Name`, `IsDir`, `Size` (with `omitempty` for optional Size). (`env.go` lines 13-17)

### LocalExecutionEnvironment (Section 4.2)
- **Direct filesystem access:** All file operations use `os` package directly. Paths resolved relative to `working_directory()` via `resolve()` helper. (`env_local.go` lines 491-500)
- **New process group:** `Setpgid: true` on `SysProcAttr`. (`env_local.go` line 426)
- **Shell: /bin/bash -c:** Used on Linux/macOS. (`env_local.go` line 424)
- **Timeout enforcement:** SIGTERM to process group -> wait 2 seconds -> SIGKILL. Correctly sends signals to negative PID (process group). (`env_local.go` lines 456-469, 502-514)
- **Stdout/stderr captured separately:** Via separate `bytes.Buffer` instances. Combined in the shell tool handler in `session.go`. (`env_local.go` lines 429-431, `session.go` lines 1170-1182)
- **Wall-clock duration recorded:** Via `time.Since(start).Milliseconds()`. (`env_local.go` line 487)
- **Env var filtering (default):** Excludes `*API_KEY*`, `*SECRET*`, `*TOKEN*`, `*PASSWORD*`, `*CREDENTIAL*` (case-insensitive). Core vars always included. (`env_local.go` lines 554-596)
- **Env var policy:** Four policies implemented: `EnvPolicyDefault`, `EnvPolicyAll`, `EnvPolicyNone`, `EnvPolicyCoreOnly`. Matches spec's "inherit all, inherit none, inherit core only" options. (`env_local.go` lines 24-31, 516-552)
- **Search with ripgrep fallback:** `Grep` checks `exec.LookPath("rg")` first, falls back to `grepNative` which uses Go's `regexp` package. (`env_local.go` lines 297-409)
- **Filesystem globbing:** Uses `doublestar.Glob` library. (`env_local.go` lines 267-295)

### Alternative Environments (Section 4.3)
- Spec explicitly states "These are not required implementations" (line 781). None exist in the codebase. This is compliant.

### Test Coverage
- Timeout and SIGKILL escalation tested (`TestLocalExecutionEnvironment_ExecCommand_TimesOutAndKillsProcessGroup`, `TestExecCommand_SIGTERM_ThenSIGKILL_Escalation`)
- Context cancellation tested (`TestLocalExecutionEnvironment_ExecCommand_ContextCancel_KillsProcessGroup`)
- All four env var policies tested (`TestEnvVarPolicy_All`, `TestEnvVarPolicy_Default_FiltersSensitive`, `TestEnvVarPolicy_CoreOnly`, `TestEnvVarPolicy_InheritNone`)
- Language toolchain paths in CoreOnly tested (`TestEnvVarPolicy_CoreOnly_IncludesLanguageToolchainPaths`)
- Cleanup process termination tested (`TestCleanup_TerminatesRunningProcesses`)
- File read/write/edit tested (`TestLocalExecutionEnvironment_ReadWriteEditFile`)
- Image handling tested (`TestReadFile_ImageReturnsBase64`, `TestReadFile_NonImageBinaryStillErrors`)
- Fuzzy edit matching tested (`TestEditFile_FuzzyMatchWhitespace`, `TestEditFile_FuzzyMatch_CompletelyWrongString_StillFails`)
- Directory listing depth tested (`TestLocalExecutionEnvironment_ListDirectory_Depth`)
- Native grep fallback tested (`TestGrep_FallbackWithoutRipgrep`, `TestGrepNative_CaseInsensitiveAndGlob`, `TestGrepNative_SkipsHiddenDirs`, `TestGrepNative_SkipsBinaryFiles`, `TestGrepNative_MaxResults`, `TestGrepNative_InvalidRegex`)
- Non-login shell verified (`TestExecCommand_UsesNonLoginShell`)
- Initialize/Cleanup lifecycle tested (`TestLocalExecutionEnvironment_InitializeCleanup`)
