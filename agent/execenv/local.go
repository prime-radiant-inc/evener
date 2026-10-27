package execenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/execsupport/procgroup"
)

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

	// commandFactory, inheritedEnv, lookPath, sandboxTmpBase, terminationGrace,
	// and outputCaptureCap are instance-local test seams. Nil/empty values retain
	// production os/exec, os.Environ, exec.LookPath, os.TempDir, terminateGrace,
	// and maxCapturedOutputBytes behavior. Keeping them on the environment avoids
	// global swaps in deterministic fuzzers.
	commandFactory   commandRuntimeFactory
	inheritedEnv     func() []string
	lookPath         func(string) (string, error)
	sandboxTmpBase   string
	terminationGrace *time.Duration
	outputCaptureCap *int

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

	// sbMu guards the lazily-built fd-anchored file-tool layers. sbfs is the policy
	// enforcement layer, built on first file-tool use from a file-tool-confined
	// Sandbox policy and cached (its root fds are captured at build so a later
	// root swap cannot redirect resolution). It folds in the session scratch
	// path current at build, recorded in sbfsScratch; when the effective path
	// has changed since, sandbox() rebuilds, the way scratchSandboxFor re-keys
	// on scratchFSRoot. A scratch move (AdoptSessionScratch) also retires the
	// source's layers outright, whether or not its path reads differently: a
	// wrapper reports the same session tmp before and after, and the source no
	// longer owns the scratch either way. It stays nil for plain off / a nil
	// policy.
	sbMu        sync.Mutex
	sbfs        *sandboxFS
	sbfsScratch string
	// scratchFS is the cached fd-anchored layer for an unsandboxed session's
	// explicitly allocated scratch root. Keeping its root fd while the root is
	// current gives the late-bound grant the same root-swap defense as policy
	// roots; it is retired with sbfs (closed once drained) when the root moves,
	// at policy replacement, and at environment teardown.
	scratchFS     *sandboxFS
	scratchFSRoot string
	// retiredFS holds layers taken out of service that an operation still held
	// at the time: a layer's close is not safe against a file-tool operation
	// still using it (sandboxFS.close), so a retired layer closes itself once
	// its last operation completes (sandboxFS.retire, release). The list is
	// bookkeeping — pruned of closed layers at each retirement — and is
	// therefore bounded by the operations in flight, not by the number of
	// rebuilds. Teardown retires every layer the same way, so a file tool in
	// flight across Cleanup completes against its root and closes it after.
	retiredFS []*sandboxFS

	// sandboxReRootErr records a fail-closed re-root refusal from the
	// WithWorkingDirectory that produced this env: when re-anchoring Sandbox/Wrapper
	// to the new worktree failed (a mode+net the host cannot satisfy at that target),
	// Sandbox and Wrapper are left nil and this holds the typed refusal so the caller
	// (a delegate/subagent spawn, a worktree switch) can surface it rather than
	// silently running the child unconfined. nil on every successful or off re-root.
	sandboxReRootErr error

	// ownedSessionTmp, when non-nil, is a per-session/per-lane scratch dir this env
	// owns. The session's end settles it (EndSessionScratch: a named one is kept,
	// a disposable one removed); DisposeSessionScratch removes it outright.
	// It is provisioned at sandbox construction (EnableSandbox) and deliberately
	// NOT copied by WithWorkingDirectory — a re-rooted clone shares the wrapper's
	// tmp path but must never dispose the owner's dir out from under it. nil for
	// off and for a fresh clone; a clone owns one only once a session that swaps
	// onto it moves ownership there (AdoptSessionScratch).
	ownedSessionTmp *sandbox.SessionScratch

	// scratchRoot and scratchSession name the session this env's scratch belongs
	// to (SetScratchIdentity). When set, a scratch this env mints is that
	// session's directory in its root's tree, which outlives the session's end
	// and is reopened on resume; when empty it is a disposable one. Guarded by
	// scratchMu, and deliberately not copied by either clone path: a clone that
	// mints a scratch of its own must not reopen its original's directory. A
	// session swapping onto a clone carries it there with AdoptSessionScratch.
	scratchRoot, scratchSession string

	// sandboxGrant, when non-empty, is a single per-invocation granted path (M7
	// escalation approve), threaded onto a short-lived clone by
	// WithSandboxInvocationGrant. The clone's lazily-built sandboxFS widens
	// root-containment for EXACTLY this one path; masking, git-protection, and
	// symlink refusal are unaffected. It is never set on a durable env and never
	// copied by WithWorkingDirectory, so the grant cannot outlive the one re-dispatch.
	sandboxGrant string

	// LoginPATH, when non-empty, overrides the inherited process PATH for the
	// PATH variable ONLY in commandEnvironment — every other variable still
	// flows through the existing EnvPolicy filter untouched. Callers that
	// construct a session/daemon env set it from LoginShellPATH() (kata 31gh):
	// a daemon launched from a context that skips the shell rc chain (macOS
	// launchd, a GUI app, systemd) inherits a PATH lacking developer tool
	// directories like /opt/homebrew/bin, which only the login shell's rc chain
	// adds. Empty means "use the inherited process PATH unchanged" (the probe
	// was never run, failed, or timed out). It is a property of the PROCESS's
	// launch context, not of a working directory, so both clone paths
	// (WithWorkingDirectory, WithSandboxInvocationGrant) carry it: a delegate
	// in its own worktree or a switched managed worktree must not silently
	// revert to the launchd/GUI PATH this override exists to replace.
	LoginPATH string

	// scratchMu guards both per-session scratch fields, ownedSessionTmp above
	// and unsandboxedScratch below: a session's environment swap moves them
	// between environment objects (AdoptSessionScratch) while a child sharing
	// one of those objects may be rendering its scratch path (SessionScratchDir)
	// or minting one. It is never held across a subprocess or another env's
	// scratchMu.
	scratchMu sync.Mutex
	// unsandboxedScratch is a per-session scratch dir this UNSANDBOXED env
	// provisions on first spawn, so commandEnvironment can export
	// EVENER_SCRATCH_DIR per docs/developing-evener/environment.md's contract,
	// which carries no sandbox-only caveat (TMPDIR follows the shape-dependent
	// rule below). It reuses the sandbox scratch location convention
	// (sandbox.NewSessionScratch) but is entirely separate from ownedSessionTmp,
	// which only a sandboxed env owns — a sandboxed spawn's scratch vars come
	// from sandbox.ApplyEnvFloor instead, so this is never consulted when
	// Wrapper is non-nil. Provisioning is best-effort: a failure disables the
	// export rather than blocking the spawn (unsandboxedScratchFailed is sticky
	// so a broken base is not re-probed on every spawn). Not copied by
	// WithWorkingDirectory or WithSandboxInvocationGrant, matching
	// ownedSessionTmp — a re-rooted clone provisions its own on first use unless
	// a session swapping onto it moves the original's there first
	// (AdoptSessionScratch).
	unsandboxedScratch       *sandbox.SessionScratch
	unsandboxedScratchFailed bool

	// unsandboxedTmp is the world-usable session temp container this UNSANDBOXED
	// env provisions on first spawn when its file tools are UNCONFINED, and whose
	// leaf is exported as TMPDIR (unsandboxedTmpDir). TMPDIR must be a directory a
	// child that becomes another uid can write, and this env's private 0700
	// scratch is not one (#495); the container lives in a world-usable host temp
	// instead, under evener's session-scratch prefix so the crashed-scratch sweep
	// still reaps it. It is never consulted for a confined or sandboxed spawn,
	// which keeps TMPDIR on the scratch (tmpDirNamesScratch). Provisioning is
	// best-effort exactly as the scratch is (unsandboxedTmpFailed is sticky), and
	// it is NOT copied by either clone path nor moved by AdoptSessionScratch: a
	// container is disposable host-temp residue rather than session data, so each
	// env mints its own and no retention binding has to model it.
	unsandboxedTmp       *sandbox.SessionTmp
	unsandboxedTmpFailed bool

	// detachedSpawned records that DetachCommand started a process from this env.
	// Such a process outlives the session on purpose and keeps the TMPDIR it was
	// spawned with, so the session's end keeps that container and leaves it to
	// the crashed-scratch sweep. Guarded by scratchMu.
	detachedSpawned bool

	// scratchMovedOut, when non-nil, runs on the SOURCE of a scratch move
	// (AdoptSessionScratch) at the point its two fields have been taken and the
	// target has not yet installed them — the interval in which a command
	// spawned on this env finds it owning nothing and mints a fresh scratch. It
	// is an instance-local test seam, set through
	// ObserveScratchMoveWindowForTesting, so a test can drive that interleaving
	// deterministically instead of racing for it. Guarded by scratchMu like the
	// two fields above; nil in production, and not copied by either clone path.
	scratchMovedOut func()
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

// lookup memoizes compute's answer for cwd, but ONLY when compute calls it
// definitive. A resolution that never reached a verdict says nothing about cwd,
// and caching it would make this environment treat cwd as non-git for the rest
// of its life, long after the request whose cancellation caused it is gone.
//
// Definitive is not the same as successful, and the distinction matters in both
// directions. A negative answer git itself gave ("not a repository", reported as
// the process's non-zero exit) and an absence the filesystem actually reported
// are definitive and cached like any other answer — without that, a directory
// whose answer can never change re-forks git on every resolution. A cancelled or
// expired context, a git that could not be run, and a stat that failed for any
// reason other than absence are not.
func (c *gitRootCache) lookup(cwd string, compute func() (root string, definitive bool)) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[cwd]; ok {
		return v
	}
	v, definitive := compute()
	if definitive {
		c.m[cwd] = v
	}
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
	return filteredEnvWithSource(e.EnvPolicy, e.overlaySessionEnv(extra), inherited)
}

// overlaySessionEnv layers this environment's developer-PATH override
// (LoginPATH) and, for an unsandboxed session, its always-on scratch vars
// onto extra, without mutating the caller's map. A caller-supplied value in
// extra always wins — this only fills in what the caller left unset. A
// sandboxed env (Wrapper != nil) never gets scratch vars here: those come
// from sandbox.ApplyEnvFloor at the spawn site instead, so sandboxed
// behavior is byte-identical to before.
//
// EVENER_SCRATCH_DIR always names this env's private session scratch. TMPDIR
// names the same directory only while this env's file tools are confined; an
// unsandboxed env with UNCONFINED file tools gets a world-usable session temp
// container instead (unsandboxedTmpDir), because that is the shape whose spawned
// children may become another uid and a 0700 scratch is unwritable by it (#495).
// The amended decision record — docs/superpowers/specs/2026-07-15-session-scratch-and-orchestration-posture-design.md,
// "Environment" — is the normative statement of that rule.
func (e *LocalExecutionEnvironment) overlaySessionEnv(extra map[string]string) map[string]string {
	overlay := map[string]string{}
	if e.LoginPATH != "" {
		overlay[envvars.Path.Name] = e.LoginPATH
	}
	if e.Wrapper == nil {
		// EVENER_SCRATCH_DIR and TMPDIR are provisioned INDEPENDENTLY. They answer
		// different needs, so one failing must not suppress the other: a host that
		// cannot serve a private scratch can still serve a world-usable temp, and
		// inheriting an ambient TMPDIR — which may itself be a private directory — is
		// exactly the failure #495 is about. Each failure leaves only its OWN
		// variable unset (inherited) rather than re-pointing it at the other.
		scratch := e.unsandboxedScratchDir()
		if scratch != "" {
			overlay[envvars.EVENERScratchDir.Name] = scratch
		}
		tmpDir := ""
		if e.tmpDirNamesScratch() {
			// The confined shape's temp IS its scratch, so an unprovisioned scratch
			// leaves TMPDIR inherited (today's behavior) rather than inventing a path
			// the file tools cannot write.
			tmpDir = scratch
		} else {
			tmpDir = e.unsandboxedTmpDir()
		}
		if tmpDir != "" {
			overlay[envvars.TmpDir.Name] = tmpDir
		}
	}
	if len(overlay) == 0 {
		return extra
	}
	// caller-supplied extra stays authoritative
	maps.Copy(overlay, extra)
	return overlay
}

func (e *LocalExecutionEnvironment) findExecutable(name string) (string, error) {
	if e.lookPath != nil {
		return e.lookPath(name)
	}
	return execLookPath(name)
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
// set. The owned session tmp is NOT copied: only the env that owns it disposes it,
// and ownership moves only explicitly (AdoptSessionScratch).
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
		outputCaptureCap: e.outputCaptureCap,
		LoginPATH:        e.LoginPATH,
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

// outputCaptureCapBytes is the per-stream capture bound for buffered commands:
// the seam's value when a test sets one, else maxCapturedOutputBytes.
func (e *LocalExecutionEnvironment) outputCaptureCapBytes() int {
	if e.outputCaptureCap != nil && *e.outputCaptureCap > 0 {
		return *e.outputCaptureCap
	}
	return maxCapturedOutputBytes
}

// Cleanup terminates any tracked processes by sending SIGTERM to each process
// group, waiting two seconds for graceful shutdown, then sending SIGKILL to
// every tracked process group (a no-op for those that already exited).
func (e *LocalExecutionEnvironment) Cleanup() {
	// Retire the cached file-tool layers: their root fds close now, or once an
	// operation still mid-flight on one completes. Independent of the process
	// teardown below.
	e.invalidateSandboxFS()

	// Release the leases of the per-session scratch dirs this env provisioned —
	// the one it owns from EnableSandbox and the one an unsandboxed env lazily
	// minted via unsandboxedScratchDir — only AFTER the SIGTERM/grace/SIGKILL
	// sequence below: tracked children may still be writing into them. Cleanup
	// does not settle them: a session's close does that last, after its
	// SessionEnd hooks and MCP servers (which still use TMPDIR) are done, with
	// EndSessionScratch. A disposable scratch no session ends is left to
	// sweepCrashedSessionScratch's 24h reclaim instead of holding its lease for
	// the daemon's uptime. A clone owns the sandbox one only if a session moved
	// ownership onto it (AdoptSessionScratch), so a clone's Cleanup never
	// releases a tmp some other env still owns.
	defer e.RetainSessionScratch()

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
			procgroup.Terminate(process.pid)
		}
	}
	// Wait for graceful shutdown, then SIGKILL survivors.
	time.Sleep(e.terminationGraceDuration())
	for _, process := range processes {
		if process.runtime != nil {
			process.runtime.Kill()
		} else {
			procgroup.Kill(process.pid)
		}
	}
}

// WorkingDirectory returns the environment's root directory.
func (e *LocalExecutionEnvironment) WorkingDirectory() string { return e.RootDir }

// ReadFile reads the file at path (resolved relative to RootDir) and returns
// its contents. Image and PDF files are returned as base64-encoded data with a
// descriptive header. Files containing a NUL byte are rejected as binary. For
// text files, lines are returned numbered, starting at offsetLine (default 1)
// and limited to limitLines lines (default 2000); CRLF line endings are
// normalized to LF.
func (e *LocalExecutionEnvironment) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return e.ReadFileCtx(context.Background(), path, offsetLine, limitLines)
}

// ReadFileCtx is ReadFile with the caller's context threaded to the read, so a
// cancelled call ends a read parked on a stream instead of waiting it out.
func (e *LocalExecutionEnvironment) ReadFileCtx(ctx context.Context, path string, offsetLine *int, limitLines *int) (string, error) {
	out, _, err := e.ReadFileAndBytesCtx(ctx, path, offsetLine, limitLines)
	return out, err
}

// ReadFileAndBytesCtx is ReadFileCtx that also returns the whole file's bytes
// as this read loaded them, for a caller that must record exactly what the
// read saw. The bytes are the complete file even when offset and limit select
// only some of its lines; on error they are nil.
func (e *LocalExecutionEnvironment) ReadFileAndBytesCtx(ctx context.Context, path string, offsetLine *int, limitLines *int) (string, []byte, error) {
	t, err := e.fileTarget("read_file", path, targetRead)
	if err != nil {
		return "", nil, err
	}
	defer t.release()
	sfs, abs := t.sfs, t.abs
	var b []byte
	if sfs != nil {
		// Sandboxed or explicitly granted scratch: race-safe fd read
		// (symlink-refusing, root/denylist-checked).
		// The image/PDF/binary/line-numbering contract below is applied identically
		// to the returned bytes, so the output is unchanged from the off path.
		b, err = sfs.readFileCtx(ctx, "read_file", abs)
	} else {
		b, err = readFileCappedCtx(ctx, e.filesystem(), abs)
	}
	if err != nil {
		// Best-effort "did you mean" suggestion for a genuine missing-file miss
		// (never for a permission error or a directory-not-a-file error). Skipped
		// when sandboxed: the diagnostic listing walks plain os.Stat/os.ReadDir
		// outside the confined resolver, and could name a masked or denied
		// directory's contents that the sandbox would otherwise keep the model
		// from seeing. Off/unsandboxed is the default mode, so this still covers
		// the common case; sandboxed sessions just get the raw ENOENT.
		if sfs == nil && errors.Is(err, fs.ErrNotExist) {
			if suggestion := readFileNotFoundSuggestion(abs); suggestion != "" {
				return "", nil, fmt.Errorf("%w. %s", err, suggestion)
			}
		}
		return "", nil, err
	}
	// Image files: return base64-encoded data instead of erroring on binary.
	if format := detectImageFormat(path, b); format != "" {
		encoded := base64.StdEncoding.EncodeToString(b)
		return fmt.Sprintf("[image: %s, %d bytes, base64 data follows]\n%s", format, len(b), encoded), b, nil
	}
	// Document files (PDF): return base64-encoded data for vision/content pipeline.
	if format := detectDocumentFormat(path, b); format != "" {
		encoded := base64.StdEncoding.EncodeToString(b)
		return fmt.Sprintf("[document: %s, %d bytes, base64 data follows]\n%s", format, len(b), encoded), b, nil
	}
	// Basic binary detection.
	if bytes.IndexByte(b, 0) >= 0 {
		return "", nil, fmt.Errorf("binary file (NUL byte): %s", path)
	}
	return NumberLines(string(b), offsetLine, limitLines), b, nil
}

// eachLine visits the '\n'-separated segments of text in order — including
// the final (possibly empty) segment after a trailing newline, exactly the
// segments strings.Split(text, "\n") yields — without materializing them all:
// a line-heavy file at the whole-file read cap would otherwise hold ~16 bytes
// of slice header per line at once (issue #4556). The walk stops once visit
// returns true.
func eachLine(text string, visit func(line string) bool) {
	for line := range strings.SplitSeq(text, "\n") {
		if visit(line) {
			return
		}
	}
}

// NumberLines presents text as ReadFile presents a text file: CRLF
// normalized to LF, lines numbered from offsetLine (default 1), at most
// limitLines lines (default 2000), and "" past the end. The window is
// extracted by scanning segments rather than splitting the file, so a
// line-heavy file at the whole-file read cap does not allocate per line for
// every line in it (issue #4556).
func NumberLines(text string, offsetLine, limitLines *int) string {
	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := 2000
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	// Skip to line start. Running out of newlines first means start is past
	// the final segment.
	for i := 1; i < start; i++ {
		idx := strings.IndexByte(text, '\n')
		if idx < 0 {
			return ""
		}
		text = text[idx+1:]
	}
	remaining := limit
	var out strings.Builder
	eachLine(text, func(line string) bool {
		fmt.Fprintf(&out, "%4d\t%s\n", start, line)
		start++
		remaining--
		return remaining == 0
	})
	return out.String()
}

// readFileNotFoundSuggestion renders a "did you mean" hint for a read_file
// ENOENT on absPath, or "" if it can't find anything worth suggesting.
//
// It walks up absPath's ancestors (starting at its own parent) until it finds
// one that exists as a directory, then fuzzy-matches absPath's basename
// against that directory's direct children. This single algorithm covers two
// distinct misses with the same code path: an ordinary typo in a file that
// lives right next to where the model looked (the walk terminates
// immediately, since the parent already exists), and a "doubled path
// segment" miss like ".../worktrees/x/worktrees/x/file.go" where the real
// file is at ".../worktrees/x/file.go" (the walk climbs past the fictional
// nested worktrees/x before landing on a real directory whose listing
// contains an exact match).
func readFileNotFoundSuggestion(absPath string) string {
	dir := filepath.Dir(absPath)
	for {
		info, err := os.Stat(dir)
		if err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // walked to the filesystem root without finding a real directory
		}
		dir = parent
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	matches := repair.TopMatches(filepath.Base(absPath), names, 3)
	if len(matches) == 0 {
		return ""
	}
	full := make([]string, len(matches))
	for i, m := range matches {
		full[i] = filepath.Join(dir, m)
	}
	if len(full) == 1 {
		return fmt.Sprintf("Did you mean %s?", full[0])
	}
	return fmt.Sprintf("Did you mean one of: %s?", strings.Join(full, ", "))
}

// WriteFile writes content to the file at path, creating any missing parent
// directories within the active sandbox policy. It returns a
// human-readable summary of the bytes written.
func (e *LocalExecutionEnvironment) WriteFile(path string, content string) (string, error) {
	if err := e.writeFileAs("write_file", path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

// EditFile replaces occurrences of oldString with newString in the file at
// path, subject to the active sandbox policy. If oldString is not
// found exactly, a whitespace-normalized fuzzy match is attempted. Unless
// replaceAll is true, oldString must match exactly once. It returns a summary
// of the number of replacements made.
func (e *LocalExecutionEnvironment) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return e.EditFileWithCtx(context.Background(), path, oldString, newString, replaceAll, nil)
}

// EditFileCtx is EditFile with the caller's context threaded to its read, so a
// cancelled edit ends a read parked on a stream. It is the interface-visible
// entry of ContextualFileReader.
func (e *LocalExecutionEnvironment) EditFileCtx(ctx context.Context, path string, oldString string, newString string, replaceAll bool) (string, error) {
	return e.EditFileWithCtx(ctx, path, oldString, newString, replaceAll, nil)
}

// EditFileWithCtx is EditFileCtx, except that finish, when not nil, turns the
// edited bytes into the bytes written, inside the same read and write.
func (e *LocalExecutionEnvironment) EditFileWithCtx(ctx context.Context, path string, oldString string, newString string, replaceAll bool, finish func(edited []byte) []byte) (string, error) {
	t, err := e.fileTarget("edit_file", path, targetWrite)
	if err != nil {
		return "", err
	}
	defer t.release()
	var b []byte
	if t.sfs != nil {
		// Deny an edit in a non-writable location up front (read-only mode, outside
		// the writable roots, or a masked/git-protected surface) before reading, so
		// the model gets a clean write denial rather than a match/not-found result.
		if derr := t.sfs.checkWritable("edit_file", t.abs); derr != nil {
			return "", derr
		}
		b, err = t.sfs.readFileCtx(ctx, "edit_file", t.abs)
	} else {
		b, err = readFileCappedCtx(ctx, e.filesystem(), t.abs)
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
	edited := []byte(s)
	if finish != nil {
		edited = finish(edited)
	}
	if t.sfs != nil {
		if werr := t.sfs.writeFile("edit_file", t.abs, edited, 0o644); werr != nil {
			return "", werr
		}
	} else if werr := afero.WriteFile(e.filesystem(), t.abs, edited, 0o644); werr != nil {
		return "", werr
	}
	plural := "s"
	if n == 1 {
		plural = ""
	}
	return fmt.Sprintf("edited %s: %d replacement%s%s", path, n, plural, fuzzyNote), nil
}

// detectImageFormat checks file extension and magic bytes to identify image files.
// Returns the format name (e.g. "png", "jpeg") or "" if not an image.
func detectImageFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	imageExts := map[string]string{
		".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
		".gif": "gif", ".webp": "webp", ".bmp": "bmp",
		".tif": "tiff", ".tiff": "tiff",
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
	if len(data) >= 2 && string(data[:2]) == "BM" {
		return "bmp"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "webp"
	}
	if len(data) >= 4 && (string(data[:4]) == "II*\x00" || string(data[:4]) == "MM\x00*") {
		return "tiff"
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
		defer sfs.release()
		return sfs.exists("file_exists", e.resolve(path))
	}
	_, err := os.Stat(e.resolve(path))
	return err == nil
}

// KernelWrapper returns the sandbox kernel wrapper for this environment, or nil
// when the session is not sandboxed. Spawn sites that do not flow through this
// environment (the stdio MCP manager, the hook runner) read it to confine their
// own child processes under the same policy.
func (e *LocalExecutionEnvironment) KernelWrapper() *sandbox.Wrapper { return e.Wrapper }

// wrapForSandbox applies kernel confinement to cmd when this environment is
// sandboxed and always empties ExtraFiles so a spawned process inherits no evener
// fds beyond stdio — not the live LLM-API connection, not a credential fd, not an
// agent socket. dir is the resolved working directory, used as the sandbox chdir.
// When Wrapper is nil it does nothing beyond the (already-default) fd hygiene, so
// a non-sandboxed spawn stays byte-identical to before.
func (e *LocalExecutionEnvironment) wrapForSandbox(cmd *exec.Cmd, dir string) {
	wrapCommandForSandbox(cmd, e.Wrapper, dir)
}
