//go:build darwin || linux

package execenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/spf13/afero"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/envvars"
)

// EnvVarPolicy controls which environment variables are inherited by child processes.
type EnvVarPolicy int

const (
	// EnvPolicyDefault inherits all non-sensitive environment variables (the current default behavior).
	EnvPolicyDefault EnvVarPolicy = iota // Inherit all non-sensitive (current behavior)
	// EnvPolicyAll inherits every environment variable, including sensitive ones.
	EnvPolicyAll // Inherit everything including sensitive
	// EnvPolicyNone starts with a clean environment, passing only explicitly provided vars.
	EnvPolicyNone // Start clean, only explicitly passed vars
	// EnvPolicyCoreOnly inherits only a core set of variables (PATH, HOME, USER, SHELL, LANG, TERM, TMPDIR) plus language toolchain paths.
	EnvPolicyCoreOnly // Only PATH, HOME, USER, SHELL, LANG, TERM, TMPDIR + language paths
)

var coreEnvVars = []envvars.Var{
	envvars.Path,
	envvars.Home,
	envvars.User,
	envvars.Shell,
	envvars.Lang,
	envvars.Term,
	envvars.TmpDir,
	envvars.GoPath,
	envvars.GoModCache,
	envvars.CargoHome,
	envvars.RustupHome,
	envvars.NVMDir,
	envvars.PyenvRoot,
}

// LocalExecutionEnvironment runs commands and file operations on the local
// machine, rooted at RootDir and governed by EnvPolicy. It tracks the PIDs of
// started processes so they can be terminated on cleanup.
type LocalExecutionEnvironment struct {
	RootDir     string        // directory that file operations and commands are rooted at
	EnvPolicy   EnvVarPolicy  // which env vars child processes inherit
	runningPIDs *sync.Map     // pid (int) → commandRuntime; legacy marker entries are tolerated by Cleanup
	gitRoots    *gitRootCache // active working-tree root per cwd (GitRootOrEmpty)
	mainRoots   *gitRootCache // stable main repo root per cwd (ResolveMainRepoRoot)
	fs          afero.Fs      // filesystem backing ReadFile/WriteFile/EditFile; defaults to the OS

	// commandFactory, inheritedEnv, lookPath, sandboxTmpBase, and terminationGrace
	// are instance-local test seams. Nil/empty values retain production os/exec,
	// os.Environ, exec.LookPath, os.TempDir, and terminateGrace behavior. Keeping
	// them on the environment avoids global swaps in deterministic fuzzers.
	commandFactory   commandRuntimeFactory
	inheritedEnv     func() []string
	lookPath         func(string) (string, error)
	sandboxTmpBase   string
	terminationGrace *time.Duration

	// Sandbox is the resolved sandbox policy for this environment, or nil for the
	// default (off) — exactly today's behavior. M2 consults it in the FILE tools
	// (read/write/edit/apply_patch/glob/grep/list_dir) via e.sandbox(); the shell
	// spawn layer (execPreparedCommand/StreamCommand) reads Wrapper for kernel
	// confinement. It rides WithWorkingDirectory like EnvPolicy so a re-rooted
	// child (subagent worktree) inherits it.
	Sandbox *sandbox.ResolvedPolicy

	// Wrapper, when non-nil, kernel-confines every command this environment spawns
	// (shell jobs, rg, and — threaded from here — stdio MCP servers and hook
	// commands): execPreparedCommand and StreamCommand prepend its bwrap invocation
	// to the argv and raise the sandbox env floor. Nil means no kernel confinement,
	// so a non-sandboxed spawn is byte-identical to before. M3 attaches it only in
	// tests/integration (the --sandbox flag stays gated off until M5); it rides
	// WithWorkingDirectory alongside Sandbox.
	Wrapper *sandbox.Wrapper

	// sbMu guards the lazily-built sandboxFS. sbfs is the fd-anchored enforcement
	// layer, built on first file-tool use from an ENFORCED Sandbox policy and cached
	// for the environment's lifetime (its root fds are captured once so a later root
	// swap cannot redirect resolution). It stays nil for off / a nil policy.
	sbMu sync.Mutex
	sbfs *sandboxFS

	// sandboxReRootErr records a fail-closed re-root refusal from the
	// WithWorkingDirectory that produced this env: when re-anchoring Sandbox/Wrapper
	// to the new worktree failed (a mode+net the host cannot satisfy at that target),
	// Sandbox and Wrapper are left nil and this holds the typed refusal so the caller
	// (a delegate/subagent spawn, a worktree switch) can surface it rather than
	// silently running the child unconfined. nil on every successful or off re-root.
	sandboxReRootErr error

	// ownedSessionTmp, when non-nil, is a per-session/per-lane scratch dir this env
	// owns. Normal teardown releases its live lease but retains the directory for
	// the human handoff; DisposeSandboxScratch is the explicit removal operation.
	// It is provisioned at sandbox construction (EnableSandbox) and deliberately
	// NOT copied by WithWorkingDirectory — a re-rooted clone shares the wrapper's
	// tmp path but must never dispose the owner's dir out from under it. nil for off
	// and for re-rooted clones.
	ownedSessionTmp *sandbox.SessionScratch

	// sandboxGrant, when non-empty, is a single per-invocation granted path (M7
	// escalation approve), threaded onto a short-lived clone by
	// WithSandboxInvocationGrant. The clone's lazily-built sandboxFS widens
	// root-containment for EXACTLY this one path; masking, git-protection, and
	// symlink refusal are unaffected. It is never set on a durable env and never
	// copied by WithWorkingDirectory, so the grant cannot outlive the one re-dispatch.
	sandboxGrant string
}

// sandbox returns the environment's fd-anchored enforcement layer, or nil when
// the environment is unsandboxed (a nil policy or off mode) — in which case every
// file tool keeps its byte-identical afero/os path. The sandboxFS is built once
// and cached; it is only ever constructed for an enforced policy. It folds in the
// concrete per-session scratch directory (sessionScratchPath) so the file tools
// reach the SAME scratch dir a spawned shell command gets via $TMPDIR —
// regardless of which policy-replacement path built it (EnableSandbox,
// WithWorkingDirectory's re-root, UseControlPolicy), since they all funnel
// through this single lazy builder.
func (e *LocalExecutionEnvironment) sandbox() *sandboxFS {
	if e.Sandbox == nil || !e.Sandbox.Enforced() {
		return nil
	}
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	if e.sbfs == nil {
		e.sbfs = newSandboxFS(e.Sandbox, e.sessionScratchPath())
		e.sbfs.grant = e.sandboxGrant
	}
	return e.sbfs
}

// sessionScratchPath returns the concrete per-session scratch directory this
// env's kernel wrapper already grants spawned processes via $TMPDIR /
// $SERF_SCRATCH_DIR (agent/sandbox.ApplyEnvFloor), or "" when unsandboxed. It
// reads through Wrapper rather than ownedSessionTmp because a re-rooted clone
// (WithWorkingDirectory) shares the parent's scratch dir via the Wrapper without
// owning it (ownedSessionTmp is nil there — see its doc comment).
func (e *LocalExecutionEnvironment) sessionScratchPath() string {
	if e.Wrapper == nil {
		return ""
	}
	return e.Wrapper.SessionTmp()
}

// WithSandboxInvocationGrant returns a short-lived clone of this env whose file-tool
// enforcement layer additionally permits EXACTLY the one path — the M7 escalation
// approve path re-dispatches a single denied tool call through it. The clone shares
// this env's resolved policy, roots, and working directory unchanged; it only widens
// root-containment for that one leaf (masking, git-protection, and symlink refusal
// still apply). It is discarded after the one re-dispatch, so the grant cannot leak
// to any later call. On an off / non-enforced env the grant is meaningless and the
// env is returned unchanged. The clone never owns the session tmp, so it never
// disposes it.
func (e *LocalExecutionEnvironment) WithSandboxInvocationGrant(path string) ExecutionEnvironment {
	if e.Sandbox == nil || !e.Sandbox.Enforced() || strings.TrimSpace(path) == "" {
		return e
	}
	return &LocalExecutionEnvironment{
		RootDir:          e.RootDir,
		EnvPolicy:        e.EnvPolicy,
		runningPIDs:      e.runningPIDs,
		gitRoots:         e.gitRoots,
		mainRoots:        e.mainRoots,
		fs:               e.fs,
		commandFactory:   e.commandFactory,
		inheritedEnv:     e.inheritedEnv,
		lookPath:         e.lookPath,
		sandboxTmpBase:   e.sandboxTmpBase,
		terminationGrace: e.terminationGrace,
		Sandbox:          e.Sandbox,
		Wrapper:          e.Wrapper,
		sandboxGrant:     filepath.Clean(path),
	}
}

// invalidateSandboxFS closes and drops the cached fd-anchored enforcement layer so
// the next file tool rebuilds it from the current policy. It MUST run whenever
// e.Sandbox is replaced (EnableSandbox, UseControlPolicy); a stale sbfs captured
// the OLD policy's root fds and would keep enforcing the OLD roots.
func (e *LocalExecutionEnvironment) invalidateSandboxFS() {
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	if e.sbfs != nil {
		e.sbfs.close()
		e.sbfs = nil
	}
}

// gitRootCache memoizes git-root lookups per working dir. A session resolves the
// git root ~4 times at init (skills, project docs, mcp config, system prompt);
// without this each forks `git rev-parse`, which is roughly half of session
// construction latency. Keyed by cwd so distinct directories stay correct; the
// root of a fixed cwd is stable for the life of the environment.
type gitRootCache struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *gitRootCache) lookup(cwd string, compute func() string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[cwd]; ok {
		return v
	}
	v := compute()
	c.m[cwd] = v
	return v
}

// NewLocalExecutionEnvironment returns a LocalExecutionEnvironment rooted at
// rootDir with a fresh PID tracker.
func NewLocalExecutionEnvironment(rootDir string) *LocalExecutionEnvironment {
	return &LocalExecutionEnvironment{
		RootDir:     rootDir,
		runningPIDs: &sync.Map{},
		gitRoots:    &gitRootCache{m: map[string]string{}},
		mainRoots:   &gitRootCache{m: map[string]string{}},
		fs:          afero.NewOsFs(),
	}
}

// SetFs overrides the filesystem backing ReadFile/WriteFile/EditFile.
// Production defaults to afero.NewOsFs() (byte-identical to direct os calls);
// tests and fuzzers inject an in-memory or sandboxed filesystem. It returns the
// environment for call chaining. The subprocess paths (ExecCommand,
// StreamCommand, OSVersion) are unaffected — they always use the real OS.
func (e *LocalExecutionEnvironment) SetFs(fs afero.Fs) *LocalExecutionEnvironment {
	e.fs = fs
	return e
}

func (e *LocalExecutionEnvironment) commands() commandRuntimeFactory {
	if e.commandFactory != nil {
		return e.commandFactory
	}
	return systemCommandRuntimeFactory{}
}

func (e *LocalExecutionEnvironment) commandEnvironment(extra map[string]string) []string {
	inherited := os.Environ()
	if e.inheritedEnv != nil {
		inherited = e.inheritedEnv()
	}
	return filteredEnvWithSource(e.EnvPolicy, extra, inherited)
}

func (e *LocalExecutionEnvironment) findExecutable(name string) (string, error) {
	if e.lookPath != nil {
		return e.lookPath(name)
	}
	return execLookPath(name)
}

// EnableSandbox provisions this env for a sandboxed session from an already-
// resolved policy: it creates a fresh per-session scratch directory that the env
// owns and retains at Cleanup, attaches the policy, and — on the bwrap backend —
// builds the kernel wrapper from the policy, the probed bwrap binary, and that
// tmp. It is the single "provision at env construction, retain at session end"
// wiring point the live flag path (M5) and M4's own tests call; the --sandbox flag
// stays feature-gated, so production does not reach it yet.
//
// A nil or non-enforced (off) policy is a no-op beyond carrying the pointer: no
// tmp is provisioned and no wrapper is built, so an off env stays byte-identical
// to today. This live provisioning path builds only the bwrap kernel wrapper; an
// enforced policy on any other backend (Seatbelt) would attach the file-tool layer
// with NO kernel confinement — a half-enforced env whose spawned processes run
// unconfined and whose enforcement line would overstate — so it FAILS CLOSED here
// (the flag is live on Linux/bwrap; the Seatbelt wrapper + macOS validation are
// M6's). On any failure the provisioned tmp is disposed and the env is left
// unsandboxed so a half-wired sandbox never runs.
func (e *LocalExecutionEnvironment) EnableSandbox(policy *sandbox.ResolvedPolicy) error {
	// EnableSandbox establishes the COMPLETE sandbox state, so it always resets any
	// stale re-root error and, on the off path, any policy/wrapper a prior
	// WithWorkingDirectory re-rooted onto this env — a delegate's box must be a pure
	// function of ITS OWN policy, never a parent's leaked one. The policy is being
	// replaced, so drop the cached fd layer, and release (but retain) any tmp a prior
	// call owned so a second EnableSandbox never silently deletes handed-off work.
	e.sandboxReRootErr = nil
	e.invalidateSandboxFS()
	if e.ownedSessionTmp != nil {
		_ = e.ownedSessionTmp.Retain()
		e.ownedSessionTmp = nil
	}
	if policy == nil || !policy.Enforced() {
		e.Sandbox = policy
		e.Wrapper = nil
		return nil
	}
	workspaceRoot := GitRootOrEmpty(e, e.RootDir)
	if workspaceRoot == "" {
		workspaceRoot = e.RootDir
	}
	tmp, err := sandbox.NewSessionScratch(e.sandboxTmpBase, workspaceRoot)
	if err != nil {
		// Leave the env unsandboxed: a half-wired sandbox must never run, and the
		// prior policy/wrapper (torn down above) must not silently persist.
		e.Sandbox = nil
		e.Wrapper = nil
		return err
	}
	// Provision the kernel wrapper for whichever backend resolved — bubblewrap on
	// Linux, sandbox-exec (Seatbelt) on macOS. An enforced policy always resolves to
	// a real backend; one with no usable backend binary fails closed (unconfined
	// spawns would half-enforce and the enforcement line would overstate) rather
	// than run half-wired.
	binPath := policy.HostBinaryPath()
	if binPath == "" {
		_ = tmp.Cleanup()
		e.Sandbox = nil
		e.Wrapper = nil
		return &sandbox.RefusalError{
			Mode: policy.Mode, Net: policy.Network, RequiredBackend: policy.Backend.String(),
			Reason: fmt.Sprintf("--sandbox %s: no %s backend binary is available to provision kernel confinement", policy.Mode, policy.Backend),
		}
	}
	w, werr := sandbox.NewWrapper(*policy, binPath, tmp.Dir)
	if werr != nil {
		_ = tmp.Cleanup()
		e.Sandbox = nil
		e.Wrapper = nil
		return werr
	}
	e.Wrapper = w
	e.Sandbox = policy
	e.ownedSessionTmp = tmp
	return nil
}

// RetainSandboxScratch releases the per-session/per-lane scratch lease and cached
// file-tool fds this env provisioned via EnableSandbox without removing the path.
// Normal session and delegate teardown use this so a parent can inspect or retain
// artifacts after the child exits. DisposeSandboxScratch is the explicit removal
// operation for an allocation that should not survive.
func (e *LocalExecutionEnvironment) RetainSandboxScratch() {
	e.sbMu.Lock()
	if e.sbfs != nil {
		e.sbfs.close()
		e.sbfs = nil
	}
	e.sbMu.Unlock()
	if tmp := e.ownedSessionTmp; tmp != nil {
		_ = tmp.Retain()
	}
}

// DisposeSandboxScratch releases the per-session/per-lane scratch dir and cached
// file-tool fds this env provisioned via EnableSandbox, WITHOUT the process
// teardown Cleanup performs. A spawn path that EnableSandbox'd a FRESH env (a
// re-rooted or cloned one) and then failed before a session adopts it calls this so
// the scratch dir is not leaked — the failed env is never handed to a session that
// would Cleanup it. It must run only on such a freshly-provisioned env, never on the
// shared parent env, whose live children's caches point into ITS scratch dir; a
// re-rooted clone never owns the parent's tmp (WithWorkingDirectory does not copy
// it), so disposing a clone's OWN scratch cannot touch the parent's.
func (e *LocalExecutionEnvironment) DisposeSandboxScratch() {
	e.sbMu.Lock()
	if e.sbfs != nil {
		e.sbfs.close()
		e.sbfs = nil
	}
	e.sbMu.Unlock()
	if tmp := e.ownedSessionTmp; tmp != nil {
		e.ownedSessionTmp = nil
		_ = tmp.Cleanup()
	}
}

// filesystem returns the environment's filesystem, defaulting to the OS
// filesystem when one was never injected (e.g. a zero-value environment).
func (e *LocalExecutionEnvironment) filesystem() afero.Fs {
	if e.fs == nil {
		e.fs = afero.NewOsFs()
	}
	return e.fs
}

// WithWorkingDirectory returns a new LocalExecutionEnvironment that uses the
// given directory as its root but shares PID tracking with the parent.
//
// The sandbox policy and kernel wrapper are RE-ROOTED to dir, not copied: their
// resolved roots are anchored at this env's worktree, so a plain pointer-copy to a
// child at a different worktree (a delegate isolation lane) would confine the
// child to the PARENT's lane — a containment hole. ReRoot re-runs the root+gitdir
// resolution against dir from the policy's retained inputs, so the child is
// confined to ITS worktree with fresh gitdir resolution. A nil policy re-roots to
// nil (off stays byte-identical to before). A re-root the host cannot satisfy is
// captured in sandboxReRootErr (Sandbox/Wrapper left nil) and surfaced via
// SandboxReRootError() — the infallible signature is preserved for the wide caller
// set. The owned session tmp is NOT copied: only the constructing env disposes it.
func (e *LocalExecutionEnvironment) WithWorkingDirectory(dir string) *LocalExecutionEnvironment {
	child := &LocalExecutionEnvironment{
		RootDir:          dir,
		EnvPolicy:        e.EnvPolicy,
		runningPIDs:      e.runningPIDs,
		gitRoots:         &gitRootCache{m: map[string]string{}},
		mainRoots:        &gitRootCache{m: map[string]string{}},
		fs:               e.fs,
		commandFactory:   e.commandFactory,
		inheritedEnv:     e.inheritedEnv,
		lookPath:         e.lookPath,
		sandboxTmpBase:   e.sandboxTmpBase,
		terminationGrace: e.terminationGrace,
	}
	if e.Sandbox != nil {
		if rerooted, err := e.Sandbox.ReRoot(dir); err != nil {
			child.sandboxReRootErr = err
		} else {
			child.Sandbox = rerooted
		}
	}
	if e.Wrapper != nil {
		if rerooted, err := e.Wrapper.ReRoot(dir); err != nil {
			child.sandboxReRootErr = err
		} else {
			child.Wrapper = rerooted
		}
	}
	// Fail closed as a UNIT: if either re-root failed, nil BOTH so the child is
	// never left half-confined (a re-rooted Sandbox alongside a nil Wrapper, or the
	// reverse) — the sticky error is what the caller surfaces. Structurally enforced
	// here rather than relying on both re-roots failing identically.
	if child.sandboxReRootErr != nil {
		child.Sandbox = nil
		child.Wrapper = nil
	}
	return child
}

// SandboxReRootError returns the fail-closed refusal from the WithWorkingDirectory
// that built this env, or nil when the re-root succeeded (or the env is off). A
// delegate/subagent spawn and a managed-worktree switch check it so a policy the
// host cannot re-anchor to the target worktree surfaces as an error rather than a
// silently unconfined child.
func (e *LocalExecutionEnvironment) SandboxReRootError() error { return e.sandboxReRootErr }

// UseControlPolicy replaces this env's sandbox policy (and kernel wrapper) with the
// manage_worktree CONTROL variant anchored at mainRepoRoot — the main repo +
// worktree registry writable, .git/config and hooks write-denied — so a worktree
// lifecycle op (create/switch/remove/lock) manages the registry without carrying
// the current worktree's tool policy. It is meant to run on an env freshly
// produced by WithWorkingDirectory(mainRepoRoot): the re-root establishes the base,
// this narrows it to the control grants. A nil policy (off) is a no-op; a control
// policy the host cannot satisfy is returned as an error so the lifecycle op is
// refused (fail closed) rather than run with the wrong scope.
func (e *LocalExecutionEnvironment) UseControlPolicy(mainRepoRoot string) error {
	if e.Sandbox == nil {
		return nil
	}
	ctrl, err := e.Sandbox.ControlPolicy(mainRepoRoot)
	if err != nil {
		return err
	}
	e.Sandbox = ctrl
	// The policy was replaced, so drop the cached fd layer built from the old one.
	e.invalidateSandboxFS()
	if e.Wrapper != nil && ctrl != nil {
		w, err := applyWrapperPolicy(e.Wrapper, *ctrl)
		if err == nil {
			e.Wrapper = w
		}
		return err
	}
	return nil
}

func applyWrapperPolicy(w *sandbox.Wrapper, policy sandbox.ResolvedPolicy) (*sandbox.Wrapper, error) {
	updated, err := wrapperWithPolicy(w, policy)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Initialize prepares the environment for use.
func (e *LocalExecutionEnvironment) Initialize() error {
	if e.runningPIDs == nil {
		e.runningPIDs = &sync.Map{}
	}
	if e.gitRoots == nil {
		e.gitRoots = &gitRootCache{m: map[string]string{}}
	}
	if e.mainRoots == nil {
		e.mainRoots = &gitRootCache{m: map[string]string{}}
	}
	if e.fs == nil {
		e.fs = afero.NewOsFs()
	}
	return nil
}

// terminateGrace bounds how long a SIGTERM'd process group is given to exit
// before it is escalated to SIGKILL. Production keeps the 2s default; tests
// shrink it so teardown of processes that ignore SIGTERM does not cost real
// seconds.
var terminateGrace = 2 * time.Second

func (e *LocalExecutionEnvironment) terminationGraceDuration() time.Duration {
	if e.terminationGrace != nil {
		return *e.terminationGrace
	}
	return terminateGrace
}

// Cleanup terminates any tracked processes by sending SIGTERM to each process
// group, waiting two seconds for graceful shutdown, then sending SIGKILL to
// every tracked process group (a no-op for those that already exited).
func (e *LocalExecutionEnvironment) Cleanup() {
	// Release any cached sandbox root fds captured by the file-tool enforcement
	// layer. Independent of the process teardown below.
	e.sbMu.Lock()
	if e.sbfs != nil {
		e.sbfs.close()
		e.sbfs = nil
	}
	e.sbMu.Unlock()

	// Retain the per-session/per-lane scratch dir this env owns (provisioned by
	// EnableSandbox) only AFTER the SIGTERM/grace/SIGKILL sequence below: tracked
	// children may still be writing artifacts into it. RetainSandboxScratch releases
	// the live lease and closes any cached file-tool fds, but deliberately leaves the
	// absolute path available for the human handoff. Re-rooted clones never own one,
	// so a clone's Cleanup never retains or removes the owner's tmp.
	defer e.RetainSandboxScratch()

	// Collect running process handles and send SIGTERM. Command execution stores a
	// commandRuntime so scripted runtimes own their teardown too; a legacy marker
	// value falls back to the historical PID process-group signals.
	type trackedProcess struct {
		pid     int
		runtime commandRuntime
	}
	var processes []trackedProcess
	e.runningPIDs.Range(func(key, value any) bool {
		pid, ok := key.(int)
		if !ok {
			return true
		}
		runtime, _ := value.(commandRuntime)
		processes = append(processes, trackedProcess{pid: pid, runtime: runtime})
		return true
	})
	if len(processes) == 0 {
		return
	}
	for _, process := range processes {
		if process.runtime != nil {
			process.runtime.Terminate()
		} else {
			terminateProcessGroup(process.pid)
		}
	}
	// Wait for graceful shutdown, then SIGKILL survivors.
	time.Sleep(e.terminationGraceDuration())
	for _, process := range processes {
		if process.runtime != nil {
			process.runtime.Kill()
		} else {
			killProcessGroup(process.pid)
		}
	}
}

// WorkingDirectory returns the environment's root directory.
func (e *LocalExecutionEnvironment) WorkingDirectory() string { return e.RootDir }

// Platform returns the operating system family as one of "darwin", "windows",
// or "linux" (the default for any other GOOS).
func (e *LocalExecutionEnvironment) Platform() string {
	switch runtimeGOOS {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

var (
	osVersionOnce  sync.Once
	osVersionValue string
)

// OSVersion returns the OS version string. On darwin and linux it shells out to
// "uname -rs"; on windows it runs "ver". If the command fails it falls back to
// "GOOS/GOARCH". The host OS version is constant for the process, so it is
// resolved once and shared rather than forking a subprocess per session.
func (e *LocalExecutionEnvironment) OSVersion() string {
	osVersionOnce.Do(func() { osVersionValue = resolveOSVersion() })
	return osVersionValue
}

// execLookPath, execCommandContext, and execCommand are the exec entry points
// execenv shells out through. Production binds them to the real os/exec
// functions; a test swaps them to force the ripgrep-present/absent branch or
// an OS-probe failure without depending on the host's tools. Byte-identical
// to calling exec.* directly. execCommand (not execCommandContext) backs the
// Argv command-runtime factory specifically: it must not carry its own
// ctx-triggered kill, which would race execPreparedCommand's process-group
// termination — see systemCommandRuntimeFactory.Argv's doc comment.
var (
	execLookPath       = exec.LookPath
	execCommandContext = exec.CommandContext
	execCommand        = exec.Command
	runtimeGOOS        = runtime.GOOS
	osVersionOutput    = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return execCommandContext(ctx, name, args...).Output()
	}
	shellStat              = os.Stat
	grepReadFile           = os.ReadFile
	grepWalk               = filepath.WalkDir
	listReadDir            = os.ReadDir
	streamBeforeSignalOnce = func(func()) {}
	streamAfterTimer       = func(func()) {}
	wrapperWithPolicy      = func(w *sandbox.Wrapper, p sandbox.ResolvedPolicy) (*sandbox.Wrapper, error) { return w.WithPolicy(p) }
	splitEditLines         = strings.Split
	venvCandidateDirs      = func(root, binDir string) []string {
		return []string{filepath.Join(root, ".venv", binDir), filepath.Join(root, "venv", binDir)}
	}
)

func resolveOSVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtimeGOOS {
	case "darwin", "linux":
		out, err := osVersionOutput(ctx, "uname", "-rs")
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	case "windows":
		out, err := osVersionOutput(ctx, "cmd", "/c", "ver")
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return runtimeGOOS + "/" + runtime.GOARCH // fallback
}

// ReadFile reads the file at path (resolved relative to RootDir) and returns
// its contents. Image and PDF files are returned as base64-encoded data with a
// descriptive header. Files containing a NUL byte are rejected as binary. For
// text files, lines are returned numbered, starting at offsetLine (default 1)
// and limited to limitLines lines (default 2000); CRLF line endings are
// normalized to LF.
func (e *LocalExecutionEnvironment) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	abs := e.resolve(path)
	var b []byte
	var err error
	if sfs := e.sandbox(); sfs != nil {
		// Sandboxed: race-safe fd read (symlink-refusing, root/denylist-checked).
		// The image/PDF/binary/line-numbering contract below is applied identically
		// to the returned bytes, so the output is unchanged from the off path.
		b, err = sfs.readFile("read_file", abs)
	} else {
		b, err = afero.ReadFile(e.filesystem(), abs)
	}
	if err != nil {
		return "", err
	}
	// Image files: return base64-encoded data instead of erroring on binary.
	if format := detectImageFormat(path, b); format != "" {
		encoded := base64.StdEncoding.EncodeToString(b)
		return fmt.Sprintf("[image: %s, %d bytes, base64 data follows]\n%s", format, len(b), encoded), nil
	}
	// Document files (PDF): return base64-encoded data for vision/content pipeline.
	if format := detectDocumentFormat(path, b); format != "" {
		encoded := base64.StdEncoding.EncodeToString(b)
		return fmt.Sprintf("[document: %s, %d bytes, base64 data follows]\n%s", format, len(b), encoded), nil
	}
	// Basic binary detection.
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("binary file (NUL byte): %s", path)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(s, "\n")

	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := 2000
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	if start > len(lines) {
		return "", nil
	}
	end := min(start-1+limit, len(lines))
	var out strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&out, "%4d\t%s\n", i, lines[i-1])
	}
	return out.String(), nil
}

// WriteFile writes content to the file at path, creating any missing parent
// directories. The path must resolve to a location under RootDir. It returns a
// human-readable summary of the bytes written.
func (e *LocalExecutionEnvironment) WriteFile(path string, content string) (string, error) {
	if sfs := e.sandbox(); sfs != nil {
		// Sandboxed: atomic temp+renameat beneath a writable-root fd (creating any
		// missing parents beneath the same root); read-only mode / out-of-root /
		// masked / git-protected targets return a typed denial.
		if err := sfs.writeFile("write_file", e.resolve(path), []byte(content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
	}
	abs, err := e.resolveWrite(path)
	if err != nil {
		return "", err
	}
	if err := e.filesystem().MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := afero.WriteFile(e.filesystem(), abs, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

// EditFile replaces occurrences of oldString with newString in the file at
// path, which must resolve to a location under RootDir. If oldString is not
// found exactly, a whitespace-normalized fuzzy match is attempted. Unless
// replaceAll is true, oldString must match exactly once. It returns a summary
// of the number of replacements made.
func (e *LocalExecutionEnvironment) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	sfs := e.sandbox()
	var abs string
	var b []byte
	var err error
	if sfs != nil {
		abs = e.resolve(path)
		// Deny an edit in a non-writable location up front (read-only mode, outside
		// the writable roots, or a masked/git-protected surface) before reading, so
		// the model gets a clean write denial rather than a match/not-found result.
		if derr := sfs.checkWritable("edit_file", abs); derr != nil {
			return "", derr
		}
		b, err = sfs.readFile("edit_file", abs)
	} else {
		abs, err = e.resolveWrite(path)
		if err != nil {
			return "", err
		}
		b, err = afero.ReadFile(e.filesystem(), abs)
	}
	if err != nil {
		return "", err
	}
	s := string(b)
	fuzzyNote := ""
	if !strings.Contains(s, oldString) {
		// Fuzzy fallback: try whitespace-normalized matching.
		match := findFuzzyMatch(s, oldString)
		if match == "" {
			if near := nearestFileRegion(s, oldString); near != "" {
				return "", fmt.Errorf("old_string not found in %s\nnearest text in the file (copy it exactly — your old_string may be a partial line or omit a line):\n%s", path, near)
			}
			return "", fmt.Errorf("old_string not found in %s", path)
		}
		oldString = match
		fuzzyNote = " [NOTE: Matched with whitespace normalization]"
	}
	if !replaceAll && strings.Count(s, oldString) != 1 {
		return "", fmt.Errorf("old_string not unique in %s; use replace_all=true or provide a more specific old_string", path)
	}
	n := strings.Count(s, oldString)
	if replaceAll {
		s = strings.ReplaceAll(s, oldString, newString)
	} else {
		s = strings.Replace(s, oldString, newString, 1)
		n = 1
	}
	if sfs != nil {
		if werr := sfs.writeFile("edit_file", abs, []byte(s), 0o644); werr != nil {
			return "", werr
		}
	} else if werr := afero.WriteFile(e.filesystem(), abs, []byte(s), 0o644); werr != nil {
		return "", werr
	}
	plural := "s"
	if n == 1 {
		plural = ""
	}
	return fmt.Sprintf("edited %s: %d replacement%s%s", path, n, plural, fuzzyNote), nil
}

// findFuzzyMatch scans the file content for a substring that matches
// oldString when whitespace is normalized. Returns the actual substring from
// the file, or "" if no match.
func findFuzzyMatch(content, oldString string) string {
	normOld := normalizeWS(oldString)
	if normOld == "" {
		return ""
	}
	// Scan lines for single-line matches.
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if normalizeWS(line) == normOld {
			return line
		}
	}
	// Multi-line: try sliding window of the same line count.
	oldLines := strings.Split(oldString, "\n")
	wSize := len(oldLines)
	if wSize > 1 && wSize <= len(lines) {
		for i := 0; i <= len(lines)-wSize; i++ {
			candidate := strings.Join(lines[i:i+wSize], "\n")
			if normalizeWS(candidate) == normOld {
				return candidate
			}
		}
	}
	return ""
}

// normalizeWS collapses all whitespace runs to single spaces and trims ends.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// nearestFileRegion returns the file region most similar to oldString's first
// line, verbatim and spanning oldString's line count, so a failed edit_file
// match can show the model the actual bytes at the intended site — surfacing a
// dropped word or an omitted line, the usual causes of a near miss the fuzzy
// matcher cannot rescue. Returns "" when nothing is similar enough to help.
func nearestFileRegion(content, oldString string) string {
	oldLines := splitEditLines(strings.TrimRight(oldString, "\n"), "\n")
	if len(oldLines) == 0 {
		return ""
	}
	target := normalizeWS(oldLines[0])
	if target == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	best, bestScore := -1, 0.0
	for i, line := range lines {
		if score := lineSimilarity(normalizeWS(line), target); score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 || bestScore < 0.5 {
		return ""
	}
	end := min(best+len(oldLines), len(lines))
	return strings.Join(lines[best:end], "\n")
}

// lineSimilarity scores two whitespace-normalized lines in [0,1]: 1.0 when one
// contains the other (the partial-line case), otherwise the Jaccard overlap of
// their space-separated tokens.
func lineSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return 1
	}
	sa, sb := wordSet(a), wordSet(b)
	inter := 0
	for w := range sa {
		if sb[w] {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// wordSet returns the set of space-separated tokens in s.
func wordSet(s string) map[string]bool {
	m := make(map[string]bool)
	for w := range strings.FieldsSeq(s) {
		m[w] = true
	}
	return m
}

// detectImageFormat checks file extension and magic bytes to identify image files.
// Returns the format name (e.g. "png", "jpeg") or "" if not an image.
func detectImageFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	imageExts := map[string]string{
		".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
		".gif": "gif", ".webp": "webp", ".bmp": "bmp",
		".svg": "svg", ".ico": "ico",
	}
	if format, ok := imageExts[ext]; ok {
		return format
	}
	// Check magic bytes for images without recognized extension.
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "jpeg"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "gif"
	}
	return ""
}

// detectDocumentFormat checks file extension and magic bytes to identify document files
// that can be processed natively by the model (e.g. PDFs). Returns the format name or "".
func detectDocumentFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" {
		return "pdf"
	}
	// Check magic bytes: PDF files start with %PDF-
	if len(data) >= 5 && string(data[:5]) == "%PDF-" {
		return "pdf"
	}
	return ""
}

// FileExists reports whether a file or directory exists at path (resolved
// relative to RootDir).
func (e *LocalExecutionEnvironment) FileExists(path string) bool {
	if sfs := e.sandbox(); sfs != nil {
		return sfs.exists("file_exists", e.resolve(path))
	}
	_, err := os.Stat(e.resolve(path))
	return err == nil
}

// ListDirectory returns the entries under path (resolved relative to RootDir),
// recursing up to depth levels (depth <= 0 is treated as 1). Entries are sorted
// by name within each directory, nested names are prefixed with their relative
// path, and file sizes are populated.
func (e *LocalExecutionEnvironment) ListDirectory(path string, depth int) ([]DirEntry, error) {
	if depth <= 0 {
		depth = 1
	}
	if sfs := e.sandbox(); sfs != nil {
		// Sandboxed: fd-anchored recursive walk (each subdir re-opened beneath its
		// parent fd with O_NOFOLLOW; masked entries skipped; symlinks not followed).
		return sfs.listDir("list_dir", e.resolve(path), depth)
	}
	root := e.resolve(path)

	var out []DirEntry
	var walk func(absDir string, relPrefix string, d int) error
	walk = func(absDir string, relPrefix string, d int) error {
		ents, err := listReadDir(absDir)
		if err != nil {
			return err
		}
		sort.SliceStable(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, ent := range ents {
			name := ent.Name()
			relName := name
			if relPrefix != "" {
				relName = filepath.Join(relPrefix, name)
			}
			de := DirEntry{Name: relName, IsDir: ent.IsDir()}
			if ent.Type()&os.ModeSymlink != 0 {
				de.IsSymlink = true
			}
			if !ent.IsDir() {
				if info, err := ent.Info(); err == nil {
					de.Size = info.Size()
					if info.Mode()&0o111 != 0 {
						de.IsExec = true
					}
				}
			}
			out = append(out, de)
			if ent.IsDir() && d > 1 {
				if err := walk(filepath.Join(absDir, name), relName, d-1); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk(root, "", depth); err != nil {
		return nil, err
	}
	return out, nil
}

// Glob returns absolute paths matching pattern (doublestar syntax) under
// basePath, which defaults to RootDir and is resolved relative to RootDir when
// not absolute. Results are sorted by modification time, newest first, with
// ties broken by path.
func (e *LocalExecutionEnvironment) Glob(pattern string, basePath string) ([]string, error) {
	patterns, err := expandSearchPattern(pattern)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSpace(basePath)
	if base == "" {
		base = e.RootDir
	}
	if !filepath.IsAbs(base) {
		base = filepath.Join(e.RootDir, base)
	}
	if sfs := e.sandbox(); sfs != nil {
		// Sandboxed: the base is policy-checked and the walk refuses symlink
		// traversal (no out-of-root match) and drops masked matches.
		return sfs.glob("glob", base, pattern)
	}
	seen := make(map[string]struct{})
	var abs []string
	for _, pattern := range patterns {
		matches, err := doublestar.Glob(os.DirFS(base), pattern)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			path := filepath.Join(base, m)
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			abs = append(abs, path)
		}
	}
	sortPathsByMtimeDesc(abs)
	return abs, nil
}

// Grep searches for pattern under path (defaulting to RootDir), using ripgrep
// when available and falling back to a native Go regex search otherwise.
// globFilter restricts which files are searched, caseInsensitive enables
// case-insensitive matching, and maxResults caps the output (default 100).
// outputMode selects the result format: "files_with_matches", "count", or
// matching lines otherwise.
// resolveGrepDir resolves a grep path argument against the environment root: a
// blank path means the root, and a relative path is joined under it. It is the
// pure directory-resolution core shared by Grep's ripgrep and native-fallback arms.
func resolveGrepDir(path, rootDir string) string {
	dir := strings.TrimSpace(path)
	if dir == "" {
		dir = rootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(rootDir, dir)
	}
	return dir
}

// buildRipgrepArgs builds the ripgrep argument vector for a Grep call. It is the
// pure arg-construction core: a fixed no-heading / no-color prefix, then exactly
// one output-mode flag (files-with-matches / count / line-number), then optional
// case-insensitivity and glob filter, then the trailing pattern and directory.
func buildRipgrepArgs(outputMode string, caseInsensitive bool, globFilter, pattern, dir string) []string {
	filters := []string(nil)
	if strings.TrimSpace(globFilter) != "" {
		filters = []string{globFilter}
	}
	return buildRipgrepArgsWithFilters(outputMode, caseInsensitive, filters, pattern, dir)
}

func buildRipgrepArgsWithFilters(outputMode string, caseInsensitive bool, globFilters []string, pattern, dir string) []string {
	args := []string{"--no-heading", "--color", "never"}
	switch outputMode {
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count")
	default:
		args = append(args, "--line-number")
	}
	if caseInsensitive {
		args = append(args, "-i")
	}
	for _, globFilter := range globFilters {
		if strings.TrimSpace(globFilter) != "" {
			args = append(args, "-g", globFilter)
		}
	}
	args = append(args, pattern, dir)
	return args
}

func (e *LocalExecutionEnvironment) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	globFilters, err := expandGrepFilter(globFilter)
	if err != nil {
		return "", err
	}
	dir := resolveGrepDir(path, e.RootDir)

	if sfs := e.sandbox(); sfs != nil {
		// Sandboxed sessions always use the denylist-aware, symlink-refusing native
		// walk, EVEN when ripgrep is present. The rg subprocess is still UNCONFINED
		// in M2 — only its base is policy-checked, so it would read masked/denylisted
		// descendants under an allowed base (e.g. ~/.ssh when the base is $HOME in
		// read-only). Its kernel wrapping is M3 defense-in-depth, not something to
		// rely on here: correctness over speed for a sandboxed session. grepNative
		// policy-checks the base itself and skips masked subtrees.
		return sfs.grepNative(pattern, dir, globFilter, caseInsensitive, maxResults, outputMode)
	}

	rg, err := e.findExecutable("rg")
	if err != nil {
		// Fallback to native Go regex search when ripgrep is absent
		return e.grepNative(pattern, dir, globFilter, caseInsensitive, maxResults, outputMode)
	}

	args := buildRipgrepArgsWithFilters(outputMode, caseInsensitive, globFilters, pattern, dir)

	ctx := context.Background()
	if maxResults <= 0 {
		maxResults = 100
	}
	res, err := e.ExecCommand(ctx, rg+" "+shellEscapeArgs(args...), 10_000, e.RootDir, nil)
	if err == nil {
		// Best-effort cap: keep first maxResults lines.
		lines := strings.Split(res.Stdout, "\n")
		if len(lines) > maxResults {
			lines = lines[:maxResults]
		}
		return strings.Join(lines, "\n"), nil
	}
	// Exit code 1 means "no matches" for rg.
	if res.ExitCode == 1 {
		return "", nil
	}
	return res.Stdout + res.Stderr, err
}

func (e *LocalExecutionEnvironment) grepNative(pattern, path, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	globFilters, err := expandGrepFilter(globFilter)
	if err != nil {
		return "", err
	}
	a, err := newGrepAccum(pattern, caseInsensitive, maxResults, outputMode)
	if err != nil {
		return "", err
	}

	err = grepWalk(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort grep: skip unreadable entries and keep walking
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != path {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip hidden files
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if len(globFilters) > 0 {
			matched, matchErr := matchesAnyGrepFilter(filepath.Base(p), globFilters)
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return nil
			}
		}
		data, err := grepReadFile(p)
		if err != nil {
			return nil //nolint:nilerr // best-effort grep: skip unreadable files and keep walking
		}
		// Skip binary files
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		relPath, _ := filepath.Rel(path, p)
		if a.feed(relPath, data) {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return a.finish(), nil
}

// ExecCommand runs command through the platform shell in its own process group,
// rooted at workingDir (defaulting to RootDir and required to be under RootDir).
// The environment is built from EnvPolicy plus envVars, with any local
// virtualenv bin directory prepended to PATH. The command is terminated if ctx
// is cancelled or timeoutMS elapses (default 10000ms), escalating from SIGTERM
// to SIGKILL. It returns an ExecResult capturing stdout, stderr, exit code,
// timeout status, and duration.
func (e *LocalExecutionEnvironment) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	return e.execPreparedCommand(ctx, e.commands().Shell(command), timeoutMS, workingDir, envVars)
}

// ExecArgv runs a command directly, without a shell, while preserving the same
// working-directory, environment, process-group, timeout, and result semantics
// as ExecCommand. Use it when the caller already has structured argv.
func (e *LocalExecutionEnvironment) ExecArgv(ctx context.Context, name string, args []string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	cmd := e.commands().Argv(name, args...)
	return e.execPreparedCommand(ctx, cmd, timeoutMS, workingDir, envVars)
}

func (e *LocalExecutionEnvironment) execPreparedCommand(ctx context.Context, cmd commandRuntime, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	if timeoutMS <= 0 {
		timeoutMS = 10_000
	}
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		dir = e.RootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.RootDir, dir)
	}
	if err := e.ensureUnderRoot(dir); err != nil {
		return ExecResult{ExitCode: 127}, fmt.Errorf("working directory %w", err)
	}

	start := time.Now()
	var stdout, stderr bytes.Buffer
	env := injectLocalVenvPath(e.commandEnvironment(envVars), []string{dir, e.RootDir})
	config := commandRuntimeConfig{
		Dir:     dir,
		Env:     env,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Wrapper: e.Wrapper,
	}
	if args := cmd.Args(); len(args) > 0 {
		if resolved, ok := lookPathInEnv(args[0], env); ok {
			config.ExecutablePath = resolved
		}
	}
	cmd.Configure(config)

	if err := cmd.Start(); err != nil {
		return ExecResult{ExitCode: 127}, err
	}
	pid := cmd.PID()
	e.runningPIDs.Store(pid, cmd)
	defer e.runningPIDs.Delete(pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timedOut := false
	interrupted := false
	var waitErr error
	select {
	case <-ctx.Done():
		interrupted = true
		waitErr = ctx.Err()
		timedOut = errors.Is(waitErr, context.DeadlineExceeded)
	case err := <-done:
		waitErr = err
	case <-time.After(time.Duration(timeoutMS) * time.Millisecond):
		interrupted = true
		timedOut = true
		waitErr = context.DeadlineExceeded
	}

	if interrupted {
		cmd.Terminate()
		select {
		case <-done:
			// exited on SIGTERM
		case <-time.After(e.terminationGraceDuration()):
			cmd.Kill()
			// Best-effort: wait a bit for Wait() to return so we don't leak the goroutine.
			select {
			case <-done:
			case <-time.After(e.terminationGraceDuration()):
			}
		}
	}

	exitCode := 0
	if waitErr != nil {
		if code, ok := cmd.ExitCode(waitErr); ok {
			exitCode = code
		} else if timedOut {
			exitCode = 124
		} else if errors.Is(waitErr, context.Canceled) {
			exitCode = 130
		} else {
			exitCode = 1
		}
	}

	return ExecResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		TimedOut:   timedOut,
		DurationMS: time.Since(start).Milliseconds(),
	}, waitErr
}

// StreamCommand runs command through the platform shell in its own process
// group, streaming combined stdout/stderr to out and returning immediately with
// a handle for waiting on or signalling the command.
func (e *LocalExecutionEnvironment) StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*StreamHandle, error) {
	if e.runningPIDs == nil {
		e.runningPIDs = &sync.Map{}
	}
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		dir = e.RootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.RootDir, dir)
	}
	if err := e.ensureUnderRoot(dir); err != nil {
		return nil, fmt.Errorf("working directory %w", err)
	}

	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	cmd := e.commands().Shell(command)
	cmd.Configure(commandRuntimeConfig{
		Dir:     dir,
		Env:     injectLocalVenvPath(e.commandEnvironment(envVars), []string{dir, e.RootDir}),
		Stdout:  out,
		Stderr:  out,
		Wrapper: e.Wrapper,
	})

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.PID()
	e.runningPIDs.Store(pid, cmd)
	detachedStart := false
	if d, ok := ctx.(interface{ DetachAfterStart() }); ok {
		d.DetachAfterStart()
		detachedStart = true
	}

	done := make(chan struct{})
	var doneOnce sync.Once
	var signalOnce sync.Once
	signal := func() {
		select {
		case <-done:
			return
		default:
		}
		streamBeforeSignalOnce(func() { doneOnce.Do(func() { close(done) }) })
		signalOnce.Do(func() {
			select {
			case <-done:
				return
			default:
			}
			cmd.Terminate()
			go func() {
				timer := time.NewTimer(e.terminationGraceDuration())
				defer timer.Stop()
				select {
				case <-done:
					return
				case <-timer.C:
					streamAfterTimer(func() { doneOnce.Do(func() { close(done) }) })
					select {
					case <-done:
						return
					default:
						cmd.Kill()
					}
				}
			}()
		})
	}

	if ctx != nil && !detachedStart {
		go func() {
			select {
			case <-ctx.Done():
				signal()
			case <-done:
			}
		}()
	}

	wait := func() (int, error) {
		defer doneOnce.Do(func() { close(done) })
		defer e.runningPIDs.Delete(pid)
		if err := cmd.Wait(); err != nil {
			if code, ok := cmd.ExitCode(err); ok {
				return code, nil
			}
			return 127, err
		}
		return 0, nil
	}

	return &StreamHandle{
		Pid:    pid,
		Wait:   wait,
		Signal: signal,
	}, nil
}

func lookPathInEnv(name string, env []string) (string, bool) {
	if name == "" || strings.ContainsAny(name, `/\\`) {
		return "", false
	}

	pathPrefix := envvars.Path.Name + "="
	pathValue := ""
	for i := len(env) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(env[i], pathPrefix); ok {
			pathValue = rest
			break
		}
	}
	if pathValue == "" {
		return "", false
	}

	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode().Perm()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}

func injectLocalVenvPath(env []string, roots []string) []string {
	if len(env) == 0 || len(roots) == 0 {
		return env
	}

	seenRoots := map[string]struct{}{}
	var uniqueRoots []string
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		if _, ok := seenRoots[r]; ok {
			continue
		}
		seenRoots[r] = struct{}{}
		uniqueRoots = append(uniqueRoots, r)
	}
	if len(uniqueRoots) == 0 {
		return env
	}

	binDir := "bin"
	if runtimeGOOS == "windows" {
		binDir = "Scripts"
	}

	seenDirs := map[string]struct{}{}
	var prefixDirs []string
	for _, root := range uniqueRoots {
		candidates := venvCandidateDirs(root, binDir)
		for _, cand := range candidates {
			info, err := os.Stat(cand)
			if err != nil || !info.IsDir() {
				continue
			}
			if _, ok := seenDirs[cand]; ok {
				continue
			}
			seenDirs[cand] = struct{}{}
			prefixDirs = append(prefixDirs, cand)
		}
	}
	if len(prefixDirs) == 0 {
		return env
	}

	sep := string(os.PathListSeparator)
	pathPrefix := envvars.Path.Name + "="
	findPath := func(env []string) (int, string) {
		for i, kv := range env {
			if rest, ok := strings.CutPrefix(kv, pathPrefix); ok {
				return i, rest
			}
		}
		return -1, ""
	}

	idx, existing := findPath(env)
	if existing != "" {
		parts := strings.Split(existing, sep)
		existingSet := map[string]struct{}{}
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			existingSet[p] = struct{}{}
		}
		var filteredPrefix []string
		for _, d := range prefixDirs {
			if _, ok := existingSet[d]; ok {
				continue
			}
			filteredPrefix = append(filteredPrefix, d)
		}
		prefixDirs = filteredPrefix
	}
	if len(prefixDirs) == 0 {
		return env
	}

	prefix := strings.Join(prefixDirs, sep)
	var newPath string
	if existing == "" {
		newPath = prefix
	} else {
		newPath = prefix + sep + existing
	}

	if idx >= 0 {
		env[idx] = envvars.Path.Assignment(newPath)
		return env
	}
	return append(env, envvars.Path.Assignment(newPath))
}

// shellCommand returns an *exec.Cmd that runs the given command string through
// the platform shell. POSIX invocations use Bash explicitly because pipefail
// is part of the shell contract; falling back to /bin/sh would silently make
// the command invalid on shells such as dash. If /bin/bash is unavailable,
// resolve Bash through the caller's effective PATH and otherwise leave the
// explicit /bin/bash path in place so command start fails instead of running
// with different pipeline semantics. The command's lifecycle (cancellation,
// timeout) is managed by the caller (ExecCommand) via its own process-group
// SIGTERM->SIGKILL escalation, so CommandContext is deliberately not used here.
func shellCommand(command string) *exec.Cmd {
	if runtimeGOOS == "windows" {
		return exec.Command("cmd.exe", "/c", command) //nolint:noctx // lifecycle managed by ExecCommand's process-group kill
	}
	shell := "/bin/bash"
	if _, err := shellStat(shell); err != nil {
		if resolved, err := execLookPath("bash"); err == nil {
			shell = resolved
		}
	}
	return exec.Command(shell, "-o", "pipefail", "-c", command) //nolint:noctx // lifecycle managed by ExecCommand's process-group kill
}

// KernelWrapper returns the sandbox kernel wrapper for this environment, or nil
// when the session is not sandboxed. Spawn sites that do not flow through this
// environment (the stdio MCP manager, the hook runner) read it to confine their
// own child processes under the same policy.
func (e *LocalExecutionEnvironment) KernelWrapper() *sandbox.Wrapper { return e.Wrapper }

// wrapForSandbox applies kernel confinement to cmd when this environment is
// sandboxed and always empties ExtraFiles so a spawned process inherits no serf
// fds beyond stdio — not the live LLM-API connection, not a credential fd, not an
// agent socket. dir is the resolved working directory, used as the sandbox chdir.
// When Wrapper is nil it does nothing beyond the (already-default) fd hygiene, so
// a non-sandboxed spawn stays byte-identical to before.
func (e *LocalExecutionEnvironment) wrapForSandbox(cmd *exec.Cmd, dir string) {
	wrapCommandForSandbox(cmd, e.Wrapper, dir)
}

func (e *LocalExecutionEnvironment) resolve(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return e.RootDir
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.RootDir, p)
}

// resolveWrite is the boundary-enforcing counterpart to resolve. Unlike
// resolve (which trusts reads), resolveWrite rejects paths that escape the
// working directory so write_file / edit_file can't reach into the
// orchestrator's source tree or anywhere else outside the project.
func (e *LocalExecutionEnvironment) resolveWrite(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", errors.New("empty path")
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(e.RootDir, abs)
	}
	abs = filepath.Clean(abs)
	if err := e.ensureUnderRoot(abs); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return abs, nil
}

// ensureUnderRoot reports whether the cleaned absolute path equals RootDir
// or is a descendant of it. Both paths are resolved through EvalSymlinks
// (best-effort; missing leaves resolve via their parent) so that symlink
// canonicalisation differences like macOS's /var -> /private/var don't
// false-positive as escapes. Callers are responsible for cleaning and
// absolutising the path before calling.
func (e *LocalExecutionEnvironment) ensureUnderRoot(abs string) error {
	abs = resolveSymlinksBestEffort(abs)
	root := resolveSymlinksBestEffort(e.RootDir)
	if abs == root {
		return nil
	}
	if strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return nil
	}
	return fmt.Errorf("is outside working directory %q", e.RootDir)
}

// resolveSymlinksBestEffort resolves symlinks in path. For write targets the
// leaf (and sometimes intermediate dirs) may not exist yet, so this walks up
// the ancestors until it finds one that does, resolves that, and re-joins
// the missing suffix. Returns a cleaned path in all cases.
func resolveSymlinksBestEffort(path string) string {
	path = filepath.Clean(path)
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(r)
	}
	suffix := []string{}
	cur := path
	for {
		parent, base := filepath.Split(cur)
		parent = strings.TrimRight(parent, string(filepath.Separator))
		if parent == "" || parent == cur {
			return path
		}
		suffix = append([]string{base}, suffix...)
		cur = parent
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Clean(filepath.Join(append([]string{r}, suffix...)...))
		}
	}
}

func terminateProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func filteredEnvWithPolicy(policy EnvVarPolicy, extra map[string]string) []string {
	return filteredEnvWithSource(policy, extra, os.Environ())
}

func filteredEnvWithSource(policy EnvVarPolicy, extra map[string]string, inherited []string) []string {
	switch policy {
	case EnvPolicyAll:
		out := append([]string(nil), inherited...)
		for k, v := range extra {
			out = append(out, k+"="+v)
		}
		return out
	case EnvPolicyNone:
		out := make([]string, 0, len(extra))
		for k, v := range extra {
			out = append(out, k+"="+v)
		}
		return out
	case EnvPolicyCoreOnly:
		core := make(map[string]bool, len(coreEnvVars))
		for _, v := range coreEnvVars {
			core[v.Name] = true
		}
		out := []string{}
		for _, kv := range inherited {
			k, _, ok := strings.Cut(kv, "=")
			if ok && core[k] {
				out = append(out, kv)
			}
		}
		for k, v := range extra {
			out = append(out, k+"="+v)
		}
		return out
	default: // EnvPolicyDefault
		return filteredEnvFrom(extra, inherited)
	}
}

func filteredEnv(extra map[string]string) []string {
	return filteredEnvFrom(extra, os.Environ())
}

func filteredEnvFrom(extra map[string]string, inherited []string) []string {
	deny := sandbox.IsSecretEnvName
	out := []string{}
	for _, kv := range inherited {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if deny(k) {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		if deny(k) {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// ShellEscapeArgs joins args into a single shell command string, quoting each
// token so it survives the shell word-splitting ExecCommand performs. It is the
// argv-discipline helper the native worktree tools use to assemble git commands
// (spec §2 "name validation": "Do not hand-build shell command strings"), so a
// worktree name or path can never inject shell metacharacters.
func ShellEscapeArgs(args ...string) string { return shellEscapeArgs(args...) }

func shellEscapeArgs(args ...string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellEscape(a))
	}
	return b.String()
}

func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\'' || r == '\\' || r == '$' || r == '`' || r == '!' || r == '(' || r == ')' || r == ';' || r == '|' || r == '&' || r == '<' || r == '>' || r == '*' || r == '?' || r == '[' || r == ']' || r == '{' || r == '}' || r == '~' || r == '#'
	}) == -1 {
		return s
	}
	// Single-quote escape strategy for bash.
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
