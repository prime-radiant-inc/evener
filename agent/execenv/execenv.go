package execenv

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
)

// ErrDetachUnsupported reports that an environment cannot disown commands.
var ErrDetachUnsupported = errors.New("detached commands are not supported by this execution environment")

// ErrOutputTruncated reports that a buffered command's stdout or stderr
// capture tripped the per-stream cap, so that stream's text is the retained
// head and tail around an omission marker, not the complete output. A caller
// that decodes the text as records — a porcelain listing, a status table, a
// log line set — must refuse a truncated capture rather than parse a partial
// stream as if it were whole (#4568 round 2). Grep's ripgrep arm is the one
// exception: it discards the capture and reruns the search natively, so it
// keeps its own special handling.
var ErrOutputTruncated = errors.New("command output truncated at the capture cap")

// CommandStartError is a command's start failure that a shell reports as a
// process exit status rather than as a launch failure: 127 when the shell finds
// no program of that name, 126 when it finds the program and the kernel still
// refuses to run it (permission denied, exec format error, a directory, a `#!`
// interpreter that is missing or not executable).
//
// The argv fast path must not give a caller a different outcome from the shell
// path for the same command, so a start failure with a shell analogue is
// reported the way the shell reports it: ExecCommand returns this error with
// ExecResult.ExitCode already set to Status, exactly as it returns an
// *exec.ExitError with the status of a command that ran and exited nonzero, and
// StreamCommand returns a handle whose Wait reports Status with no error. A
// caller that separates "never started" from "ran and failed" must therefore
// read this type as the latter.
//
// A start failure with no shell analogue — resource exhaustion, an unusable
// name — is never a CommandStartError, so it stays a plain start error.
type CommandStartError struct {
	// Status is the exit status the shell reports for this failure: 126 or 127.
	Status int
	// Err is the start error the process launcher returned. It stays reachable
	// through errors.Is, so a caller can still ask what the kernel refused.
	Err error
}

func (e *CommandStartError) Error() string { return e.Err.Error() }

func (e *CommandStartError) Unwrap() error { return e.Err }

// ExecResult holds the outcome of a command executed in an ExecutionEnvironment.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
	// StdoutTruncated and StderrTruncated report the per-stream capture cap
	// tripping: that stream's text is the retained head and tail around an
	// omission marker, not the full output. A caller that parses the text as
	// records must treat it as corrupted — Grep's ripgrep arm reruns the search
	// natively when it trips, so both grep arms disclose byte truncation the
	// same way.
	StdoutTruncated bool `json:"stdout_truncated,omitempty"`
	StderrTruncated bool `json:"stderr_truncated,omitempty"`
}

// Truncated reports a capture whose per-stream cap tripped: ErrOutputTruncated
// when either stream is truncated, else nil. A record parser checks it before
// decoding the text.
func (r ExecResult) Truncated() error {
	if r.StdoutTruncated || r.StderrTruncated {
		return ErrOutputTruncated
	}
	return nil
}

// DirEntry describes a single entry returned when listing a directory.
type DirEntry struct {
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink,omitempty"`
	IsExec    bool   `json:"is_exec,omitempty"`
	Size      int64  `json:"size,omitempty"`
	// ModTime is the modification time; files only, zero when unknown.
	ModTime time.Time `json:"-"`
	// IsRegular is true for a regular file: not a directory, symlink, FIFO,
	// socket or device.
	IsRegular bool `json:"-"`
}

// DetachedProcess identifies a command disowned by its execution environment.
type DetachedProcess struct {
	PID int `json:"pid"`
	// Done closes when this exact process exits. It is intentionally not part of
	// the tool result: callers use it for lifecycle ownership, not wire output.
	Done <-chan struct{} `json:"-"`
}

// DetachedExecutor is an optional capability for immediately disowned commands.
type DetachedExecutor interface {
	DetachCommand(ctx context.Context, command, workingDir string, envVars map[string]string) (DetachedProcess, error)
}

// DetachSupportReporter is an optional execution-environment capability:
// answering, before anything is launched, whether DetachCommand can actually
// disown a process here. It exists so a caller can tell a model whether
// mode:"detached" is available instead of recommending a call that fails —
// notably under a sandbox, where a provisioned wrapper refuses detach outright.
// Separate from DetachedExecutor, like the other optional capabilities, so
// existing implementers are unaffected; an environment that does not report is
// treated as unable to detach.
type DetachSupportReporter interface {
	DetachSupported() bool
}

// StreamingExecutor is an optional capability: a long-running command whose
// output streams to out as it arrives, returning a handle to wait on and signal.
// It is separate from ExecutionEnvironment so existing implementers (incl. test
// fakes) are unaffected; the job runtime type-asserts for it.
type StreamingExecutor interface {
	StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*StreamHandle, error)
}

// ArgvExecutor is an optional capability: running a program directly from
// explicit argv, with no command string to parse or classify. Callers that
// already have structured argv (RunGit, mainly) use it to avoid building a
// shell command line out of caller-supplied arguments — which would otherwise
// be a string-interpolation injection surface. ExecCommand reaches argv by
// itself for a word list it can classify, so this remains a separate entry
// point for callers that never had a command string. It is separate from
// ExecutionEnvironment, like StreamingExecutor, so existing implementers
// (test fakes, remote environments) are unaffected; RunGit type-asserts for it
// and falls back to ExecCommand's shell wrapper when it is absent.
type ArgvExecutor interface {
	ExecArgv(ctx context.Context, name string, args []string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
}

// CommandWorkingDirectoryBoundary is an optional execution-environment capability
// for checking a resolved command cwd against its active sandbox policy. Off
// sessions permit any cwd; confined sessions retain their workspace boundary.
type CommandWorkingDirectoryBoundary interface {
	EnsureCommandWorkingDirectory(abs string) error
}

// RootBoundary checks that an already-resolved absolute path stays under the
// working root regardless of sandbox mode. It serves callers with a narrower
// path contract, such as session URL attachments.
type RootBoundary interface {
	// EnsureUnderRoot rejects abs if it escapes the sandbox root. abs must
	// already be absolute and filepath.Clean'ed by the caller.
	EnsureUnderRoot(abs string) error
}

// FileMutator is an optional execution-environment capability: raw,
// policy-checked file mutations used by apply_patch, which needs to read, write,
// remove, and rename files directly rather than through the formatted read/write
// tools. Like StreamingExecutor it is separate from ExecutionEnvironment so other
// implementers are unaffected; apply_patch type-asserts for it.
//
// When the environment carries a file-tool-confined policy, each method resolves
// its path through the race-safe fd-anchored layer (symlink-refusing,
// root/denylist-checked; writes are atomic temp+renameat). Unrestricted off
// sessions impose no workspace boundary. Paths may be relative to the working
// directory or absolute.
type FileMutator interface {
	// ReadFileRaw returns the raw bytes of path.
	ReadFileRaw(path string) ([]byte, error)
	// WriteFileRaw writes data to path, creating any missing parent directories.
	WriteFileRaw(path string, data []byte, perm os.FileMode) error
	// RemovePath deletes path (best-effort: a missing target is not an error; an
	// out-of-policy target is a denial).
	RemovePath(path string) error
	// RenamePath moves oldPath to newPath, creating newPath's parents.
	RenamePath(oldPath, newPath string) error
}

// ContextualFileReader is an optional execution-environment capability: the
// formatted file read and edit entries with the caller's context threaded to
// the read, so a cancelled tool call ends a read parked on a stream. It is
// separate from ExecutionEnvironment, like the other optional capabilities, so
// existing implementers (test fakes) are unaffected; the file tools type-assert
// for it and fall back to the ctx-less entries when it is absent.
type ContextualFileReader interface {
	ReadFileCtx(ctx context.Context, path string, offsetLine, limitLines *int) (string, error)
	EditFileCtx(ctx context.Context, path, oldString, newString string, replaceAll bool) (string, error)
}

// FileEdit is one exact-text replacement within a single-file batch edit.
type FileEdit struct {
	OldString  string
	NewString  string
	ReplaceAll bool
}

// BatchFileEditor is an optional execution-environment capability for applying
// multiple non-overlapping replacements to one file atomically in a single read
// and write. Separate from ExecutionEnvironment so existing test doubles remain
// unaffected.
type BatchFileEditor interface {
	EditFileBatchCtx(ctx context.Context, path string, edits []FileEdit) (string, error)
}

// ContextualFileMutator is FileMutator's read entry with the caller's context
// threaded to the read; apply_patch type-asserts for it, falling back to
// ReadFileRaw when absent.
type ContextualFileMutator interface {
	ReadFileRawCtx(ctx context.Context, path string) ([]byte, error)
}

// StreamHandle is a running streamed process. Wait blocks until exit and returns
// the exit code; Signal terminates the process group (SIGTERM then SIGKILL).
// SignalName reports the signal that terminated the process, when the platform
// exposes it; it must be called after Wait returns and may be nil for non-system
// implementations.
type StreamHandle struct {
	Pid        int
	Wait       func() (exitCode int, err error)
	Signal     func()
	SignalName func() string
}

// ExecutionEnvironment abstracts the filesystem and command runner used by tools.
type ExecutionEnvironment interface {
	// Initialize performs any setup required before the environment is used.
	Initialize() error
	// Cleanup releases resources held by the environment.
	Cleanup()

	// WorkingDirectory returns the environment's root/working directory.
	WorkingDirectory() string
	// Platform returns the OS platform identifier (e.g. "darwin", "linux").
	Platform() string
	// OSVersion returns a human-readable OS version string.
	OSVersion() string

	// ReadFile reads a file. offsetLine and limitLines, when non-nil, restrict
	// the result to a 1-based line window.
	ReadFile(path string, offsetLine *int, limitLines *int) (string, error)
	// WriteFile writes content to path, creating or truncating it, and returns
	// a human-readable result message.
	WriteFile(path string, content string) (string, error)
	// EditFile replaces oldString with newString in path; replaceAll replaces
	// every occurrence instead of requiring a unique match.
	EditFile(path string, oldString string, newString string, replaceAll bool) (string, error)
	// FileExists reports whether path exists.
	FileExists(path string) bool

	// Glob returns paths matching the shell-style pattern, rooted at basePath.
	// Dotfiles/dirs (.git, .claude/worktrees/x, ...) and gitignored paths are
	// excluded by default; pass includeIgnored(true) to restore them.
	// A `**` walk over a large tree can run for minutes, so ctx bounds it:
	// cancelling ctx aborts the walk and returns ctx's error.
	Glob(ctx context.Context, pattern string, basePath string, includeIgnored ...bool) ([]string, error)
	// Grep searches files for pattern and returns matches formatted per
	// outputMode ("content" (default), "files_with_matches", or "count").
	// contextLines, when given (0-10), includes that many lines of context
	// before/after each match; omitted or non-positive means no context. The
	// context bounds the filesystem walk and any ripgrep subprocess.
	Grep(ctx context.Context, pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string, contextLines ...int) (string, error)
	// ListDirectory lists entries under path, recursing up to depth levels.
	ListDirectory(path string, depth int) ([]DirEntry, error)

	// ExecCommand runs a shell command with the given timeout (ms), working
	// directory, and extra environment variables, returning its result.
	ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
}

// GlobBudgeter is an optional capability an ExecutionEnvironment's Glob may
// additionally implement: the budgeted glob entry point the glob tool uses,
// which also reports how many candidate matches the default dotfile/gitignore
// exclusion dropped. It stays off Glob's own signature, which every
// implementation — including test doubles with no budget accounting — would
// otherwise have to grow a meaningless extra parameter and return value for.
// The caller supplies the budget and reads what the call had to cut off it
// once the call returns.
type GlobBudgeter interface {
	GlobWithBudget(ctx context.Context, pattern, basePath string, includeIgnored bool, budget *GlobBudget) (matches []string, excluded int, err error)
}

// DirListBudgeter is an optional capability an ExecutionEnvironment's
// ListDirectory may additionally implement, modelled on GlobBudgeter for the
// same reason: it keeps the context-aware, budgeted walk off ListDirectory's
// own signature, which every implementation — including test doubles with no
// budget accounting — would otherwise have to grow a meaningless extra
// parameter for. The caller supplies the budget and reads what the call had
// to cut off it once the call returns; environments without the capability
// keep the legacy whole-tree walk.
type DirListBudgeter interface {
	// ListDirectoryWithBudget lists entries under path, recursing up to
	// depth levels, like ListDirectory — but it stops once the budget's page
	// need is met (see NewDirListBudget) or its bounds refuse, instead of
	// enumerating the whole requested subtree first, and a cancelled ctx
	// aborts the walk.
	ListDirectoryWithBudget(ctx context.Context, path string, depth int, budget *DirListBudget) ([]DirEntry, error)
}
