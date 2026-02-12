# Section 4: Tool Execution Environment -- Spec Compliance Audit

**Spec reference:** `coding-agent-loop-spec.md`, lines 711-836
**Code under audit:** `internal/agent/env.go`, `internal/agent/env_local.go`, `internal/agent/session.go` (registerCoreTools), `internal/agent/profile.go` (tool definitions)
**Date:** 2026-02-11

---

## Summary

Section 4 of the spec defines the `ExecutionEnvironment` interface, the `LocalExecutionEnvironment` required implementation, alternative environment extension points, and composable wrappers. The codebase provides a faithful implementation of the core requirements with a few deviations, some practical and some that should be addressed.

**Total gaps found: 10**
- PASS (conformant): 6
- MINOR (deviation, low risk): 3
- GAP (spec violation to address): 1

---

## Conformant Items

### PASS-4.01: ExecResult Record
**Status:** PASS
**Evidence:** `internal/agent/env.go:5-11`
```go
type ExecResult struct {
    Stdout     string `json:"stdout"`
    Stderr     string `json:"stderr"`
    ExitCode   int    `json:"exit_code"`
    TimedOut   bool   `json:"timed_out"`
    DurationMS int64  `json:"duration_ms"`
}
```
**Description:** All five spec fields (`stdout`, `stderr`, `exit_code`, `timed_out`, `duration_ms`) are present with correct semantics. `DurationMS` uses `int64` vs the spec's `Integer`, which is a safe Go idiom for millisecond durations.

### PASS-4.02: DirEntry Record
**Status:** PASS
**Evidence:** `internal/agent/env.go:13-17`
```go
type DirEntry struct {
    Name  string `json:"name"`
    IsDir bool   `json:"is_dir"`
    Size  int64  `json:"size,omitempty"`
}
```
**Description:** All three spec fields present. `Size` uses `int64` with `omitempty` to approximate the spec's `Integer | None` nullable semantics. Directories correctly omit size (set at zero, omitted from JSON). Files populate size via `info.Size()`.

### PASS-4.03: Process Group Isolation and Timeout Escalation (SIGTERM -> SIGKILL)
**Status:** PASS
**Evidence:** `internal/agent/env_local.go:475,505-518,551-563`
```go
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
// ... on timeout:
terminateProcessGroup(cmd.Process.Pid)
select {
case <-done:
case <-time.After(2 * time.Second):
    killProcessGroup(cmd.Process.Pid)
}
```
**Description:** Spawns in a new process group (`Setpgid: true`). On timeout, sends SIGTERM to the process group, waits 2 seconds, then SIGKILL. Exactly matches spec: "on timeout, send SIGTERM to the process group, wait 2 seconds, then SIGKILL." Comprehensive test coverage in `env_local_test.go` (timeout test, SIGTERM-then-SIGKILL escalation test, cleanup test).

### PASS-4.04: Environment Variable Filtering
**Status:** PASS
**Evidence:** `internal/agent/env_local.go:23-31,565-649`
**Description:** All spec requirements met:
- **Deny patterns (case-insensitive):** `*_API_KEY`, `*_SECRET`, `*_TOKEN`, `*_PASSWORD`, `*_CREDENTIAL` -- all present in the `deny()` function using `strings.ToUpper` + `strings.Contains`.
- **Always-include list:** `PATH`, `HOME`, `USER`, `SHELL`, `LANG`, `TERM`, `TMPDIR`, `GOPATH`, `CARGO_HOME`, `NVM_DIR` plus extras (`GOMODCACHE`, `RUSTUP_HOME`, `PYENV_ROOT`).
- **Customizable policy:** Four policies (`EnvPolicyDefault`, `EnvPolicyAll`, `EnvPolicyNone`, `EnvPolicyCoreOnly`) mapping to the spec's "inherit all, inherit none, inherit core only."
- Thorough test coverage for all policies in `env_local_test.go`.

### PASS-4.05: Search Operations -- ripgrep with Fallback
**Status:** PASS
**Evidence:** `internal/agent/env_local.go:310-458`
**Description:** `Grep` checks for ripgrep via `exec.LookPath("rg")` and falls back to `grepNative` (language-native regex search). `Glob` uses `doublestar.Glob` for filesystem globbing. Matches spec: "Use ripgrep for grep if available, fall back to language-native regex search. Use filesystem globbing for glob."

### PASS-4.06: Wall-Clock Duration Recording
**Status:** PASS
**Evidence:** `internal/agent/env_local.go:472,536`
```go
start := time.Now()
// ...
DurationMS: time.Since(start).Milliseconds(),
```
**Description:** Records wall-clock duration from start to completion/timeout. Matches spec: "Record wall-clock duration."

---

## Gaps

### GAP-4.01: Interface Contains `EditFile` Not in Spec
**Status:** MINOR
**Evidence:** `internal/agent/env.go:32`
```go
EditFile(path string, oldString string, newString string, replaceAll bool) (string, error)
```
**Description:** The `ExecutionEnvironment` interface includes `EditFile`, which is not part of the spec's interface definition (Section 4.1). This makes the interface harder for alternative implementations to satisfy since they must implement a method the spec doesn't require. However, `EditFile` is a practical necessity for the edit_file tool and is a reasonable extension.
**Recommendation:** Consider whether `EditFile` should be a separate, optional interface (e.g., `EditableEnvironment`) to keep the core interface spec-compliant. Low priority.

### GAP-4.02: `WriteFile` Returns String Instead of Void
**Status:** MINOR
**Evidence:** `internal/agent/env.go:31`
```go
WriteFile(path string, content string) (string, error)
```
Spec: `write_file(path: String, content: String) -> void`
**Description:** The Go interface returns `(string, error)` where the spec says `void`. The returned string is a human-readable success message (e.g., "wrote 42 bytes to foo.txt"). This is a practical Go idiom, not a behavioral deviation -- the operation still writes the file and has no meaningful return value in the spec sense.
**Recommendation:** No action needed. This is a reasonable Go adaptation.

### GAP-4.03: `Initialize` Returns Error Instead of Void
**Status:** MINOR
**Evidence:** `internal/agent/env.go:22`
```go
Initialize() error
```
Spec: `initialize() -> void`
**Description:** Returning an error from `Initialize` is a practical necessity in Go (and any language without exceptions). The `LocalExecutionEnvironment` implementation returns nil, but alternative implementations (Docker, K8s, SSH) would certainly need error reporting during initialization.
**Recommendation:** No action needed. This is a necessary Go adaptation.

### GAP-4.04: `Grep` Signature Expands GrepOptions Inline Instead of Using Options Struct
**Status:** MINOR -- ACCEPTABLE DEVIATION
**Evidence:** `internal/agent/env.go:36`
```go
Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error)
```
Spec: `grep(pattern: String, path: String, options: GrepOptions) -> String`
**Description:** The spec references a `GrepOptions` type but never defines it as a `RECORD`. The implementation expands the options as individual parameters. This is a stylistic choice that trades an options struct for explicit parameters. Adding or changing options in the future would be harder (requires interface signature change), but the current set of options covers all practical needs.
**Recommendation:** Consider introducing a `GrepOptions` struct to match the spec's intent and improve extensibility. Low priority since the spec itself never defines the record.

### GAP-4.05: `ExecCommand` Has Extra `context.Context` Parameter
**Status:** MINOR -- ACCEPTABLE DEVIATION
**Evidence:** `internal/agent/env.go:39`
```go
ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
```
Spec: `exec_command(command, timeout_ms, working_dir, env_vars) -> ExecResult`
**Description:** The Go interface adds a `context.Context` parameter, which is standard Go practice for cancellable operations. This is necessary for abort signal propagation (when the session's context is cancelled, running commands must be terminated). Alternative implementations would naturally need this too. The spec's `timeout_ms` parameter is supplementary to context cancellation.
**Recommendation:** No action needed. This is idiomatic Go and enables critical abort functionality.

### GAP-4.06: Shell Tool Definition Missing `working_dir` and `env_vars` Parameters
**Status:** GAP
**Evidence:** `internal/agent/profile.go:424-438` (tool definition) and `internal/agent/session.go:1246` (tool handler)
```go
// Tool definition only exposes command, timeout_ms, description:
"properties": map[string]any{
    "command":     map[string]any{"type": "string"},
    "timeout_ms":  map[string]any{"type": "integer"},
    "description": map[string]any{"type": "string"},
},

// Handler always passes empty working_dir and nil envVars:
res, err := env.ExecCommand(ctx, cmd, timeout, "", nil)
```
Spec interface: `exec_command(command, timeout_ms, working_dir, env_vars) -> ExecResult`
**Description:** The `ExecutionEnvironment.ExecCommand` interface correctly accepts `workingDir` and `envVars`, but the shell tool exposed to the LLM model does not include these parameters. The handler hardcodes them to `""` and `nil`. While the spec's `exec_command` interface signature includes these, the tool definition exposed to the model is a different concern -- the shell tool intentionally restricts what the model can control. The `working_dir` parameter is arguably a security concern (model shouldn't run commands outside the project root), and `env_vars` could be used to bypass env filtering.
**Recommendation:** This is an intentional design choice for security and simplicity. The underlying `ExecutionEnvironment` interface correctly supports these parameters for programmatic use. Document this as an intentional restriction. The interface itself is conformant; only the tool-level exposure is restricted.

### GAP-4.07: No Windows Shell Support (`cmd.exe /c`)
**Status:** GAP
**Evidence:** `internal/agent/env_local.go:473`
```go
cmd := exec.Command("/bin/bash", "-c", command)
```
Spec: "Use the platform's default shell (`/bin/bash -c` on Linux/macOS, `cmd.exe /c` on Windows)"
**Description:** The implementation unconditionally uses `/bin/bash -c` regardless of platform. While `Platform()` can return `"windows"`, the command execution always uses `/bin/bash`. There are no build-tag-specific files (`env_local_windows.go`) or runtime platform checks before selecting the shell.
**Recommendation:** Add platform-conditional shell selection. On Windows, use `cmd.exe /c`. This could be a simple runtime check:
```go
if runtime.GOOS == "windows" {
    cmd = exec.Command("cmd.exe", "/c", command)
} else {
    cmd = exec.Command("/bin/bash", "-c", command)
}
```
Note: The `syscall.SysProcAttr{Setpgid: true}` and `syscall.Kill(-pid, ...)` calls are also Unix-specific and would need Windows equivalents (`CREATE_NEW_PROCESS_GROUP`, `GenerateConsoleCtrlEvent` or `TerminateProcess`).

### GAP-4.08: `Platform()` Does Not Return "wasm"
**Status:** MINOR
**Evidence:** `internal/agent/env_local.go:82-91`
```go
func (e *LocalExecutionEnvironment) Platform() string {
    switch runtime.GOOS {
    case "darwin":
        return "darwin"
    case "windows":
        return "windows"
    default:
        return "linux"
    }
}
```
Spec: `platform() -> String -- "darwin", "linux", "windows", "wasm"`
**Description:** The spec lists "wasm" as a valid platform value, but `Platform()` never returns "wasm" -- it falls through to "linux" for any unrecognized GOOS (including `js`). However, WASM is listed under Section 4.3 as an extension point ("not required implementations"), and the `LocalExecutionEnvironment` would never actually run in WASM. A `WASMExecutionEnvironment` would implement its own `Platform()`.
**Recommendation:** No action needed for `LocalExecutionEnvironment`. If a WASM environment is ever implemented, it would return "wasm" from its own `Platform()` method.

### GAP-4.09: `ExecCommand` Returns `error` in Addition to `ExecResult`
**Status:** MINOR -- ACCEPTABLE DEVIATION
**Evidence:** `internal/agent/env.go:39`
```go
ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
```
Spec: `exec_command(...) -> ExecResult`
**Description:** The spec shows `ExecResult` as the sole return. The Go interface also returns `error`. The `error` is returned for Go-level failures (e.g., command not found, process start failure) that don't produce a meaningful `ExecResult`. Non-zero exit codes are still reported via `ExecResult.ExitCode`, not the error return. This is standard Go practice.
**Recommendation:** No action needed.

### GAP-4.10: Composability Pattern Not Explicitly Supported
**Status:** MINOR
**Evidence:** Interface is a Go `interface`, which inherently supports wrapping/composition.
**Description:** Section 4.4 describes composable wrappers (LoggingExecutionEnvironment, ReadOnlyExecutionEnvironment). The codebase doesn't implement any wrappers, but the Go interface design naturally supports this pattern -- any struct that embeds or wraps an `ExecutionEnvironment` and delegates calls can implement the decorator pattern. The `WithWorkingDirectory` method on `LocalExecutionEnvironment` is a simple example of composition.
**Recommendation:** No action needed. The spec says "can be wrapped" -- the interface supports this. Actual wrapper implementations are not required.

---

## Test Coverage Assessment

The test file `internal/agent/env_local_test.go` provides comprehensive coverage:
- Timeout and SIGTERM->SIGKILL escalation (3 tests)
- Context cancellation (1 test)
- Environment variable filtering for all 4 policies (7 tests)
- File operations: read, write, edit, fuzzy edit (5 tests)
- Image/binary detection (2 tests)
- Directory listing with depth (1 test)
- Grep native fallback, case sensitivity, glob filter, hidden dirs, binary skip, max results, invalid regex, output modes (10 tests)
- Shell non-login mode (1 test)
- Cleanup process termination (1 test)
- Initialize/Cleanup lifecycle (1 test)

**Missing test coverage:**
- No test for Windows shell selection (but Windows is not currently supported, see GAP-4.07)
- No test for `ExecCommand` with non-empty `workingDir` parameter
- No test for `ExecCommand` with `envVars` parameter (extra vars passed through)
- No wrapper/composition test (but this is structural, not behavioral)
