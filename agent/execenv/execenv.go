package execenv

import (
	"context"
	"io"
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
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

// StreamingExecutor is an optional capability: a long-running command whose
// output streams to out as it arrives, returning a handle to wait on and signal.
// It is separate from ExecutionEnvironment so existing implementers (incl. test
// fakes) are unaffected; the job runtime type-asserts for it.
type StreamingExecutor interface {
	StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*StreamHandle, error)
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
	Glob(pattern string, basePath string) ([]string, error)
	// Grep searches files for pattern and returns matches formatted per
	// outputMode ("content" (default), "files_with_matches", or "count").
	Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error)
	// ListDirectory lists entries under path, recursing up to depth levels.
	ListDirectory(path string, depth int) ([]DirEntry, error)

	// ExecCommand runs a shell command with the given timeout (ms), working
	// directory, and extra environment variables, returning its result.
	ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
}
