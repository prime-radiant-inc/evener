package execenv

import (
	"context"
	"errors"
	"io"
	"os"
)

// ErrDetachUnsupported reports that an environment cannot disown commands.
var ErrDetachUnsupported = errors.New("detached commands are not supported by this execution environment")

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

// RootBoundary is an optional execution-environment capability: validating
// that an already-resolved absolute path stays under the environment's
// sandbox root. It is separate from ExecutionEnvironment, like
// StreamingExecutor, so other implementers (incl. test fakes) are unaffected;
// the shell tool type-asserts for it to validate a model-chosen `cwd` before
// spawning a process there.
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
	// (always 0 when includeIgnored is true). A listing that would have been
	// truncated by the match cap comes back as an error instead of a short
	// list: this entry point has no budget of its own to report truncation
	// through, so a caller that wants the partial list and a way to tell it
	// was cut short uses GlobBudgeter instead.
	GlobWithExclusions(ctx context.Context, pattern, basePath string, includeIgnored bool) (matches []string, excluded int, err error)
}

// GlobBudgeter is an optional capability an ExecutionEnvironment's Glob may
// additionally implement, modelled on GlobExcluder for the same reason: it
// exists so a caller that wants a truncated listing rather than the refusal
// GlobExcluder gives does not have to widen Glob's own signature, which every
// implementation — including test doubles with no budget accounting — would
// otherwise have to grow a meaningless extra parameter for. The caller
// supplies the budget and reads what the call had to cut off it once the
// call returns.
type GlobBudgeter interface {
	GlobWithBudget(ctx context.Context, pattern, basePath string, includeIgnored bool, budget *GlobBudget) (matches []string, excluded int, err error)
}

// maxListDirWalkListings and maxListDirWalkEntries cap how much work one
// ListDirectoryBudget walk may spend no matter how large a page its caller
// asked for: the number of directories it reads and the total entries it
// accumulates across them. They mirror GlobBudget's listing and entry bounds,
// and matter for the same reason — a model-controlled depth over a huge tree
// costs unbounded work even when the requested page is tiny.
var (
	maxListDirWalkListings = 200_000
	maxListDirWalkEntries  = 200_000
)

// ListDirBudget bounds one ListDirectoryBudget walk's work. Callers supply it
// and read afterwards whether the walk had to stop early; its fields stay
// unexported for the same reason GlobBudget's do. A budget belongs to one call
// and must not be shared across goroutines.
type ListDirBudget struct {
	maxListings int
	maxEntries  int
	listings    int
	entries     int
	truncated   bool
}

// NewListDirBudget constructs a budget for one list_dir call. maxEntries is the
// number of entries the caller still needs — the requested page (offset+limit)
// plus one, so the walk can tell a complete listing from a truncated one. It is
// clamped up to at least one and down to maxListDirWalkEntries so neither a
// caller's tiny page nor a huge one can drive the walk past the hard ceiling.
func NewListDirBudget(maxEntries int) *ListDirBudget {
	if maxEntries < 1 {
		maxEntries = 1
	}
	if maxEntries > maxListDirWalkEntries {
		maxEntries = maxListDirWalkEntries
	}
	return &ListDirBudget{maxEntries: maxEntries, maxListings: maxListDirWalkListings}
}

// Truncated reports whether the walk stopped at its budget rather than
// exhausting the subtree. The entries it returned are then a prefix of the full
// listing, with more beyond, so their count is a floor rather than a total.
func (b *ListDirBudget) Truncated() bool { return b.truncated }

// chargeListing records one directory the walk is about to read, reporting
// false once the listing budget is spent. A non-positive cap is unlimited, so
// the zero ListDirBudget an internal caller uses is unbounded.
func (b *ListDirBudget) chargeListing() bool {
	if b.maxListings > 0 && b.listings >= b.maxListings {
		b.truncated = true
		return false
	}
	b.listings++
	return true
}

// chargeEntry records one entry the walk is about to retain, reporting false
// once the entry budget is spent. A non-positive cap is unlimited.
func (b *ListDirBudget) chargeEntry() bool {
	if b.maxEntries > 0 && b.entries >= b.maxEntries {
		b.truncated = true
		return false
	}
	b.entries++
	return true
}

// DirBudgeter is an optional capability an ExecutionEnvironment may implement,
// modelled on GlobBudgeter for the same reason: ListDirectory's signature is
// shared by every implementation, including test doubles with no budget
// accounting, so a caller that needs to cancel and bound a directory walk uses
// this instead. The caller supplies the budget and reads what the walk had to
// cut off it through ListDirBudget.Truncated afterwards.
type DirBudgeter interface {
	ListDirectoryBudget(ctx context.Context, path string, depth int, budget *ListDirBudget) ([]DirEntry, error)
}
