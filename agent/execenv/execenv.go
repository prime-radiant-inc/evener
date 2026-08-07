package execenv

import (
	"context"
	"io"
	"os"
)

// ExecResult holds the outcome of a command executed in an ExecutionEnvironment.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
}

// DirEntry describes a single entry returned when listing a directory.
type DirEntry struct {
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink,omitempty"`
	IsExec    bool   `json:"is_exec,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

// StreamingExecutor is an optional capability: a long-running command whose
// output streams to out as it arrives, returning a handle to wait on and signal.
// It is separate from ExecutionEnvironment so existing implementers (incl. test
// fakes) are unaffected; the job runtime type-asserts for it.
type StreamingExecutor interface {
	StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*StreamHandle, error)
}

// ArgvExecutor is an optional capability: running a program directly from
// explicit argv, without the platform shell ExecCommand forks through. Callers
// that already have structured argv (RunGit, mainly) use it to skip the shell
// fork/exec ExecCommand pays for every call and to avoid building a shell
// command line — which would otherwise be a string-interpolation injection
// surface — out of caller-supplied arguments. It is separate from
// ExecutionEnvironment, like StreamingExecutor, so existing implementers
// (test fakes, remote environments) are unaffected; RunGit type-asserts for it
// and falls back to ExecCommand's shell wrapper when it is absent.
type ArgvExecutor interface {
	ExecArgv(ctx context.Context, name string, args []string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
}

// FileMutator is an optional execution-environment capability: raw,
// policy-checked file mutations used by apply_patch, which needs to read, write,
// remove, and rename files directly rather than through the formatted read/write
// tools. Like StreamingExecutor it is separate from ExecutionEnvironment so other
// implementers are unaffected; apply_patch type-asserts for it.
//
// When the environment carries an ENFORCED sandbox policy, each method resolves
// its path through the race-safe fd-anchored layer (symlink-refusing,
// root/denylist-checked; writes are atomic temp+renameat). Otherwise it confines
// to the working root exactly as the other off-mode file tools do. Paths may be
// relative to the working directory or absolute under it.
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

// StreamHandle is a running streamed process. Wait blocks until exit and returns
// the exit code; Signal terminates the process group (SIGTERM then SIGKILL).
type StreamHandle struct {
	Pid    int
	Wait   func() (exitCode int, err error)
	Signal func()
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
	Glob(pattern string, basePath string, includeIgnored ...bool) ([]string, error)
	// Grep searches files for pattern and returns matches formatted per
	// outputMode ("content" (default), "files_with_matches", or "count").
	// contextLines, when given (0-10), includes that many lines of context
	// before/after each match; omitted or non-positive means no context.
	Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string, contextLines ...int) (string, error)
	// ListDirectory lists entries under path, recursing up to depth levels.
	ListDirectory(path string, depth int) ([]DirEntry, error)

	// ExecCommand runs a shell command with the given timeout (ms), working
	// directory, and extra environment variables, returning its result.
	ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
}

// GlobExcluder is an optional capability an ExecutionEnvironment's Glob may
// additionally implement: it reports, alongside the matches, how many
// candidate matches were dropped by the default dotfile/gitignore exclusion.
// Callers use this to tell a genuinely empty result apart from one that was
// silently emptied out entirely by filtering (rather than widening the
// ExecutionEnvironment interface's Glob signature itself, which every
// implementation — including test doubles with no exclusion logic — would
// otherwise have to grow a meaningless extra return value for).
type GlobExcluder interface {
	// GlobWithExclusions behaves like Glob but also returns the number of
	// candidate matches dropped by the default dotfile/gitignore exclusion
	// (always 0 when includeIgnored is true).
	GlobWithExclusions(pattern, basePath string, includeIgnored bool) (matches []string, excluded int, err error)
}
