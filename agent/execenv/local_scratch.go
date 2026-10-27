package execenv

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/sandbox"
)

// ObserveScratchMoveWindowForTesting installs fn as this environment's
// scratch-move window observer (scratchMovedOut), returning a restore func. It
// is exported because what the window costs is settled by a SESSION's close,
// which lives in another package and cannot reach the field.
func (e *LocalExecutionEnvironment) ObserveScratchMoveWindowForTesting(fn func()) (restore func()) {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	orig := e.scratchMovedOut
	e.scratchMovedOut = fn
	return func() {
		e.scratchMu.Lock()
		defer e.scratchMu.Unlock()
		e.scratchMovedOut = orig
	}
}

// sandbox returns the environment's fd-anchored enforcement layer, or nil when
// the environment's file tools are unconfined (a nil policy or plain off mode) —
// in which case every file tool keeps its byte-identical afero/os path. The gate
// is FileToolConfined, not Enforced: a write-blocked off policy (a read-only
// delegate on a host with no sandbox backend) has no OS sandbox but still confines
// the file tools, which are then the only thing holding its write boundary. The
// sandboxFS is built lazily and cached, and rebuilt when the session's scratch
// has moved since (AdoptSessionScratch): it folds in the concrete per-session
// scratch directory (sessionScratchPath) so the file tools reach the SAME scratch
// dir a spawned shell command gets via $EVENER_SCRATCH_DIR — regardless of which
// policy-replacement path built it (EnableSandbox, WithWorkingDirectory's
// re-root, UseControlPolicy), since they all funnel through this single lazy
// builder. The layer returned is acquired for the caller's operation; the caller
// releases it when the operation completes (sandboxFS.release).
func (e *LocalExecutionEnvironment) sandbox() *sandboxFS {
	if e.Sandbox == nil || !e.Sandbox.FileToolConfined() {
		return nil
	}
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	scratch := e.sessionScratchPath()
	if e.sbfs != nil && e.sbfsScratch != scratch {
		e.retireFileToolLayerLocked(e.sbfs)
		e.sbfs = nil
	}
	if e.sbfs == nil {
		e.sbfs = newSandboxFS(e.Sandbox, scratch)
		e.sbfs.grant = e.sandboxGrant
		e.sbfsScratch = scratch
	}
	e.sbfs.acquire()
	return e.sbfs
}

// retireFileToolLayerLocked takes a layer out of service (sandboxFS.retire):
// closed now if no operation holds it, otherwise by the release that drains it.
// The bookkeeping list keeps only the layers still open, re-examined on each
// call. The caller holds sbMu, and since a layer is acquired only under sbMu
// while it is current, a retired layer is never acquired again.
func (e *LocalExecutionEnvironment) retireFileToolLayerLocked(layer *sandboxFS) {
	layer.retire()
	retired := e.retiredFS[:0]
	for _, candidate := range append(e.retiredFS, layer) {
		if !candidate.closed.Load() {
			retired = append(retired, candidate)
		}
	}
	e.retiredFS = retired
	if len(e.retiredFS) == 0 {
		e.retiredFS = nil
	}
}

// sessionScratchPath returns the concrete per-session scratch directory this
// env's kernel wrapper already grants spawned processes via $EVENER_SCRATCH_DIR
// and $TMPDIR (agent/sandbox.ApplyEnvFloor), or "" when neither layer has
// one. It reads through Wrapper rather than ownedSessionTmp because a re-rooted
// clone (WithWorkingDirectory) shares the parent's scratch dir via the Wrapper
// without necessarily owning it (ownedSessionTmp is nil on a fresh clone — see
// its doc comment).
//
// A write-blocked policy with no OS sandbox (a read-only delegate on a host with
// no sandbox backend) never builds a wrapper to read through, so it falls back to
// the scratch EnableSandbox provisioned for it, which this env OWNS — the same dir
// overlaySessionEnv exports to its spawned commands, so the model's file tools and
// its shell agree on one writable place. Without it the file tools of a
// write-blocked env would have no writable root at all, breaking WriteBlocked's
// contract that the session scratch stays writable.
func (e *LocalExecutionEnvironment) sessionScratchPath() string {
	if e.Wrapper != nil {
		return e.Wrapper.SessionTmp()
	}
	return e.wrapperlessScratchDir()
}

// wrapperlessScratchDir returns the already-provisioned per-session scratch of an
// env with no kernel wrapper, or "" when it has none. It never provisions one:
// both shapes that have one allocate it eagerly (EnableSandbox for a write-blocked
// policy, the first spawn for an ordinary unsandboxed session), and allocating
// from a read path would make a report or a containment check a filesystem side
// effect.
func (e *LocalExecutionEnvironment) wrapperlessScratchDir() string {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	if tmp := e.ownedSessionTmp; tmp != nil {
		return tmp.Dir
	}
	if e.unsandboxedScratch == nil {
		return ""
	}
	return e.unsandboxedScratch.Dir
}

// allocatedSessionScratchPath returns an already-provisioned scratch root owned
// by this environment. It deliberately does not provision one: checking an
// arbitrary write path must never allocate a new grant as a side effect. Enforced
// environments are handled by sandbox(), whose policy already folds
// Wrapper.SessionTmp() into the fd-anchored roots; this accessor is for the
// otherwise-unconfined environment's late-bound scratch grant.
func (e *LocalExecutionEnvironment) allocatedSessionScratchPath() string {
	return e.sessionScratchPath()
}

// scratchSandboxForAfterRootRead is a test-only seam observing the point in
// scratchSandboxFor between its unlocked read of the scratch root and taking
// sbMu, where a concurrent scratch move can land. Nil in production.
var scratchSandboxForAfterRootRead func()

// scratchSandboxFor returns the cached fd-anchored layer for an already allocated
// unsandboxed scratch root when abs is beneath that root. The normal enforced path
// uses e.sandbox() instead, preserving its full policy (including secret masking
// and git protection). The root fd is opened before the first operation and held
// until environment teardown, so a later path-level symlink/root swap cannot
// redirect the grant.
func (e *LocalExecutionEnvironment) scratchSandboxFor(abs string) *sandboxFS {
	if runtimeGOOS != "linux" && runtimeGOOS != "darwin" {
		return nil
	}
	root := e.allocatedSessionScratchPath()
	if root == "" {
		return nil
	}
	if _, _, ok := containingRoot([]string{root}, filepath.Clean(abs)); !ok {
		return nil
	}
	if scratchSandboxForAfterRootRead != nil {
		scratchSandboxForAfterRootRead()
	}
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	// Re-read the root under the lock (sbMu then scratchMu, the standing
	// order): a scratch move can land between the read above and here, retiring
	// the layer for that root and taking the root away. A layer is installed
	// only for the root current under the lock — never for the one read
	// outside it, which nothing would ever retire.
	if current := e.allocatedSessionScratchPath(); current != root {
		root = current
		if root == "" {
			return nil
		}
		if _, _, ok := containingRoot([]string{root}, filepath.Clean(abs)); !ok {
			return nil
		}
	}
	if e.scratchFS != nil && e.scratchFSRoot == root {
		e.scratchFS.acquire()
		return e.scratchFS
	}
	if e.scratchFS != nil {
		e.retireFileToolLayerLocked(e.scratchFS)
		e.scratchFS = nil
		e.scratchFSRoot = ""
	}
	sfs := newScratchSandboxFS(root)
	if err := pinScratchSandboxRoot(sfs, root); err != nil {
		sfs.close()
		return nil
	}
	e.scratchFS = sfs
	e.scratchFSRoot = root
	sfs.acquire()
	return sfs
}

// SessionScratchDir reports the per-session scratch directory spawned commands
// already receive as $EVENER_SCRATCH_DIR — the sandboxed env's wrapper tmp, the
// write-blocked env's own owned dir, or an unsandboxed env's lazily provisioned
// one — and "" when none has been provisioned. It is NOT the value of $TMPDIR for
// an unsandboxed env with unconfined file tools, which receives a world-usable
// temp container instead (unsandboxedTmpDir). It deliberately never
// provisions one: it is a REPORTING accessor (the session prompt's capability
// preamble), and reporting a path must not create it, nor turn a prompt render
// into a filesystem side effect.
func (e *LocalExecutionEnvironment) SessionScratchDir() string {
	return e.sessionScratchPath()
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
//
// The gate stays Enforced() rather than FileToolConfined() on purpose: the only
// policy that separates the two is the write-blocked off box, which only a DELEGATE
// receives, and escalation refuses a subagent session outright (escalationAllowed),
// so no such env can reach this. Widening the gate would add a branch nothing can
// execute — and one whose clone, sharing no owned scratch, would need its own
// handling. If escalation ever reaches delegates, widen it THEN, with the test that
// can finally exercise it.
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
		outputCaptureCap: e.outputCaptureCap,
		LoginPATH:        e.LoginPATH,
		Sandbox:          e.Sandbox,
		Wrapper:          e.Wrapper,
		sandboxGrant:     filepath.Clean(path),
	}
}

// invalidateSandboxFS retires every cached fd-anchored layer (closed once no
// operation holds it) and drops it so the next file tool rebuilds from the
// current policy and scratch. It MUST run whenever e.Sandbox is replaced
// (EnableSandbox, UseControlPolicy) — a stale sbfs captured the OLD policy's
// root fds and would keep enforcing the OLD roots — and when an environment
// loses its scratch to another (AdoptSessionScratch).
func (e *LocalExecutionEnvironment) invalidateSandboxFS() {
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	e.closeFileToolLayersLocked()
}

// closeFileToolLayersLocked takes every cached fd-anchored layer, current and
// retired, out of service. The caller holds sbMu and is a teardown or a policy
// replacement. A layer no operation holds closes now; one a file tool is still
// mid-operation on (Session.close joins the tool wait group only after the
// environment's Cleanup) closes when that operation completes, so the
// operation never sees a closed root descriptor.
func (e *LocalExecutionEnvironment) closeFileToolLayersLocked() {
	if e.sbfs != nil {
		e.sbfs.retire()
		e.sbfs = nil
		e.sbfsScratch = ""
	}
	if e.scratchFS != nil {
		e.scratchFS.retire()
		e.scratchFS = nil
		e.scratchFSRoot = ""
	}
	for _, retired := range e.retiredFS {
		retired.retire()
	}
	e.retiredFS = nil
}

// tmpDirNamesScratch reports whether this env's TMPDIR still names the session
// scratch. An unsandboxed env whose file tools are confined — the write-blocked
// off policy, a read-only delegate on a host with no sandbox backend — keeps the
// scratch, and deliberately: that scratch is the file tools' ONLY writable root
// (EnableSandbox provisions it eagerly for exactly that reason), so pointing the
// shell's temp elsewhere would break a mktemp-then-write_file workflow. Only an
// unsandboxed env with UNCONFINED file tools, the shape a privilege-dropping
// child actually comes from, gets the world-usable container. A sandboxed spawn
// never reaches overlaySessionEnv at all — sandbox.ApplyEnvFloor owns its
// scratch vars — and keeps TMPDIR on the session temp.
func (e *LocalExecutionEnvironment) tmpDirNamesScratch() bool {
	if !sandbox.SessionTmpSupported {
		// Where the POSIX container cannot exist the shape split buys nothing, so
		// keep the pre-existing session-scratch export instead of handing the child
		// an unrelated inherited TMPDIR.
		return true
	}
	return e.Sandbox != nil && e.Sandbox.FileToolConfined()
}

// unsandboxedTmpDir lazily provisions (once) and returns this env's world-usable
// session temp container leaf — the directory exported as TMPDIR when this env's
// file tools are unconfined — or "" if provisioning failed. Like the scratch it
// is a convenience, never a launch or spawn blocker: a failure leaves TMPDIR
// inherited rather than erroring the command.
func (e *LocalExecutionEnvironment) unsandboxedTmpDir() string {
	e.scratchMu.Lock()
	if tmp := e.unsandboxedTmp; tmp != nil {
		dir := tmp.Dir
		e.scratchMu.Unlock()
		return dir
	}
	if e.unsandboxedTmpFailed {
		e.scratchMu.Unlock()
		return ""
	}
	tmp, err := sandbox.NewSessionTmp()
	if err != nil {
		e.unsandboxedTmpFailed = true
		e.scratchMu.Unlock()
		return ""
	}
	e.unsandboxedTmp = tmp
	e.scratchMu.Unlock()
	return tmp.Dir
}

// unsandboxedScratchDir lazily provisions (once) and returns this env's
// per-session scratch directory, or "" if provisioning failed. A scratch dir is a
// convenience, never a launch or spawn blocker: a failure silently disables the
// EVENER_SCRATCH_DIR export instead of erroring the command.
//
// A write-blocked env already OWNS one (EnableSandbox provisioned it, and its file
// tools treat it as their single writable root), so this returns that rather than
// minting a second: the model's file tools and its shell must name the same
// directory, and only the owned one is disposed with the env.
func (e *LocalExecutionEnvironment) unsandboxedScratchDir() string {
	e.scratchMu.Lock()
	if tmp := e.ownedSessionTmp; tmp != nil {
		e.scratchMu.Unlock()
		return tmp.Dir
	}
	if e.unsandboxedScratch != nil {
		dir := e.unsandboxedScratch.Dir
		e.scratchMu.Unlock()
		return dir
	}
	if e.unsandboxedScratchFailed {
		e.scratchMu.Unlock()
		return ""
	}
	tmp, err := e.newSessionScratch(e.scratchRoot, e.scratchSession)
	if err != nil {
		e.unsandboxedScratchFailed = true
		e.scratchMu.Unlock()
		return ""
	}
	e.unsandboxedScratch = tmp
	e.scratchMu.Unlock()
	return tmp.Dir
}

// newSessionScratch creates a fresh per-session scratch directory for this env,
// anchored at its workspace root so the scratch names the project it belongs to.
//
// The anchor is resolved STRUCTURALLY and must stay that way: minting runs
// inside commandEnvironment's overlay, so spawning anything from here re-enters
// this function through overlaySessionEnv → unsandboxedScratchDir and wedges the
// process. GitRootOrEmpty's `git rev-parse --show-toplevel` fallback did exactly
// that whenever structuralWorktreeRoot missed but a .git ancestor existed — it
// blocked on the scratch mutex its own caller was already holding, and no lock
// reordering fixes it, since gitRootCache.lookup holds its own mutex across the
// same probe. structuralWorktreeRoot answers every intact repo shape (a .git
// directory, a linked-worktree or submodule "gitdir:" pointer); the shapes it
// cannot answer are ones git's discovery rejects too — it stops at the same
// first .git entry — so the probe returned "" there anyway and the anchor fell
// back to RootDir, exactly as it does now.
//
// root and session are the env's scratch name, read by the caller under
// scratchMu (ScratchIdentity, or the fields directly while holding it).
func (e *LocalExecutionEnvironment) newSessionScratch(root, session string) (*sandbox.SessionScratch, error) {
	workspace := SessionScratchWorkspaceRoot(e.RootDir)
	if root != "" && session != "" {
		if s, err := sandbox.OpenSessionScratch(e.sandboxTmpBase, workspace, root, session); err == nil {
			return s, nil
		}
		// A named scratch that cannot be opened (another process holds it, or a
		// planted path refuses) falls back to a disposable one: the session runs,
		// it just does not get its old directory back.
	}
	return sandbox.NewSessionScratch(e.sandboxTmpBase, workspace)
}

// SetScratchIdentity names the session this env's scratch belongs to: rootID is
// the top-level session of the tree, sessionID this session (the root's own ID
// for the root). Call it before the env mints a scratch — before EnableSandbox
// for a sandboxed env — so the scratch is that session's directory in its root's
// tree, kept across the session's ends and reopened on resume.
func (e *LocalExecutionEnvironment) SetScratchIdentity(rootID, sessionID string) {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	e.scratchRoot, e.scratchSession = rootID, sessionID
}

// ScratchIdentity reports the session this env's scratch belongs to, as set by
// SetScratchIdentity; both are empty when it was never named.
func (e *LocalExecutionEnvironment) ScratchIdentity() (rootID, sessionID string) {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	return e.scratchRoot, e.scratchSession
}

// EndSessionScratch settles this env's scratch at a session's end — close, idle
// retirement, a delegate's teardown or idle release. A named scratch
// (SetScratchIdentity) is kept with its regenerable caches pruned and its lease
// released: it goes when the hub archives or deletes the root. A disposable
// scratch is removed. The TMPDIR container is
// removed unless a detached command still uses it. Call it only on an env the
// caller owns.
func (e *LocalExecutionEnvironment) EndSessionScratch() error {
	return e.settleSessionScratch((*sandbox.SessionScratch).End)
}

// settleSessionScratch hands each scratch this env owns to settle, drops its
// ownership, and ends the TMPDIR container.
func (e *LocalExecutionEnvironment) settleSessionScratch(settle func(*sandbox.SessionScratch) error) error {
	e.invalidateSandboxFS()
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	var errs []error
	if tmp := e.ownedSessionTmp; tmp != nil {
		e.ownedSessionTmp = nil
		errs = append(errs, settle(tmp))
	}
	if tmp := e.unsandboxedScratch; tmp != nil {
		e.unsandboxedScratch = nil
		errs = append(errs, settle(tmp))
	}
	errs = append(errs, e.endTmpContainerLocked())
	return errors.Join(errs...)
}

// SessionScratchWorkspaceRoot reports the workspace a session started in dir
// anchors its scratch to: the structural worktree root when dir sits in one, and
// dir itself when it does not. Everything that decides whether a scratch base
// lies inside the workspace has to ask this one function — the allocator above
// and the startup reclaim in cmd/evener both do — or the two disagree about
// which tree a directory belongs to and the reclaim sweeps a base allocation
// refuses to use.
func SessionScratchWorkspaceRoot(dir string) string {
	if root, ok := structuralWorktreeRoot(dir); ok {
		return root
	}
	return dir
}

// retainUnsandboxedScratch releases the leases of the per-session directories
// this env lazily provisioned for an unsandboxed spawn — the private scratch
// (unsandboxedScratchDir) and the world-usable temp container
// (unsandboxedTmpDir) — without removing either directory. It is the
// counterpart to RetainSessionScratch's sandbox release for the unsandboxed case, so a Cleanup
// never leaves either lease held for the rest of the daemon's uptime.
func (e *LocalExecutionEnvironment) retainUnsandboxedScratch() {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	if e.unsandboxedScratch != nil {
		_ = e.unsandboxedScratch.Retain()
	}
	if e.unsandboxedTmp != nil {
		_ = e.unsandboxedTmp.Retain()
	}
}

// RetainSessionScratch releases the lease of every per-session scratch
// directory this env provisioned — the one it owns from EnableSandbox, the one
// an unsandboxed env mints lazily on its first command, and the world-usable temp
// container such an env mints for TMPDIR — without removing the directories.
// Cleanup calls it once the env's processes are gone; the session's end then
// settles the directories (EndSessionScratch).
func (e *LocalExecutionEnvironment) RetainSessionScratch() {
	e.invalidateSandboxFS()
	// Releasing a lease mutates it, and two teardowns can retain the same env
	// at once (a swap rolling back onto the parked restore environment while
	// close retains it), so the release itself runs under scratchMu.
	e.scratchMu.Lock()
	_ = e.ownedSessionTmp.Retain()
	e.scratchMu.Unlock()
	e.retainUnsandboxedScratch()
}

// EnableSandbox provisions this env for a sandboxed session from an already-
// resolved policy: it opens the session's scratch directory (the session's named
// one, or a fresh disposable one) that the env owns, attaches the policy, and — on the bwrap backend —
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
	e.scratchMu.Lock()
	prior := e.ownedSessionTmp
	e.ownedSessionTmp = nil
	_ = prior.Retain()
	e.scratchMu.Unlock()
	if policy == nil || !policy.Enforced() {
		e.Sandbox = policy
		e.Wrapper = nil
		// A write-blocked off policy has no OS sandbox to provision but still confines
		// the file tools, whose only writable root is the session scratch. Provision it
		// here rather than on first use so the scratch path is available to the session
		// prompt that tells the delegate where it may write — a prompt render must not
		// create directories, so it can only report one that already exists. The env
		// OWNS it, exactly as the enforced path below does, so a spawn that fails after
		// this point disposes it (DisposeSandboxScratch) instead of leaking a directory
		// AND the flock lease that keeps the crashed-scratch sweeper away from it.
		//
		// Best-effort: losing the scratch costs the delegate its one writable place,
		// which denies writes — the fail-closed direction — and must not block a spawn.
		if policy != nil && policy.FileToolConfined() {
			if tmp, err := e.newSessionScratch(e.ScratchIdentity()); err == nil {
				e.setOwnedSessionTmp(tmp)
			}
		}
		return nil
	}
	tmp, err := e.newSessionScratch(e.ScratchIdentity())
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
		_ = tmp.End()
		e.Sandbox = nil
		e.Wrapper = nil
		return &sandbox.RefusalError{
			Mode: policy.Mode, Net: policy.Network, RequiredBackend: policy.Backend.String(),
			Reason: fmt.Sprintf("--sandbox %s: no %s backend binary is available to provision kernel confinement", policy.Mode, policy.Backend),
		}
	}
	w, werr := sandbox.NewWrapper(*policy, binPath, tmp.Dir)
	if werr != nil {
		_ = tmp.End()
		e.Sandbox = nil
		e.Wrapper = nil
		return werr
	}
	e.Wrapper = w
	e.Sandbox = policy
	e.setOwnedSessionTmp(tmp)
	return nil
}

func (e *LocalExecutionEnvironment) setOwnedSessionTmp(tmp *sandbox.SessionScratch) {
	e.scratchMu.Lock()
	e.ownedSessionTmp = tmp
	e.scratchMu.Unlock()
}

// DisposeSandboxScratch releases the per-session/per-lane scratch dir and cached
// file-tool fds this env provisioned via EnableSandbox, WITHOUT the process
// teardown Cleanup performs. A spawn path that EnableSandbox'd a FRESH env (a
// re-rooted or cloned one) and then failed before a session adopts it calls this so
// the scratch dir is not leaked — the failed env is never handed to a session that
// would Cleanup it. It must run only on such a freshly-provisioned env, never on the
// shared parent env, whose live children's caches point into ITS scratch dir; a
// fresh re-rooted clone owns nothing of the parent's (WithWorkingDirectory does
// not copy it), so disposing a clone's OWN scratch cannot touch the parent's.
func (e *LocalExecutionEnvironment) DisposeSandboxScratch() {
	e.invalidateSandboxFS()
	// Under scratchMu for the same reason RetainSessionScratch releases under
	// it: a concurrent retain of the same env must not race the lease release
	// inside Cleanup. That holds scratchMu across the RemoveAll, which the
	// field's "never across a subprocess" invariant permits: no command runs
	// here, and Dispose's contract is a fresh environment no session adopted,
	// so nothing else is minting or reading its scratch meanwhile.
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	tmp := e.ownedSessionTmp
	e.ownedSessionTmp = nil
	_ = tmp.Cleanup()
}

// DisposeUnsandboxedScratch releases the lazily minted per-session scratch this
// env provisioned and removes it, WITHOUT the process teardown Cleanup performs.
// The next spawned command lazily mints a replacement.
//
// The session temp container is deliberately NOT touched here: this env keeps
// working, so its TMPDIR must keep naming a live directory for the children it
// already spawned. The env's own end settles the container.
func (e *LocalExecutionEnvironment) DisposeUnsandboxedScratch() {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	if tmp := e.unsandboxedScratch; tmp != nil {
		e.unsandboxedScratch = nil
		_ = tmp.Cleanup()
	}
}

// DisposeSessionScratch removes every per-session scratch directory this env
// owns: the sandbox scratch EnableSandbox provisioned, the scratch an unsandboxed
// env mints on its first command, and the world-usable TMPDIR container. An env
// that started a detached command keeps that container, lease released, because
// the command still uses it; the crashed-scratch sweep reclaims it later. Call it
// only on an environment the caller owns, never on a parent's shared one whose
// live children point into its scratch. Idempotent.
func (e *LocalExecutionEnvironment) DisposeSessionScratch() error {
	return e.settleSessionScratch((*sandbox.SessionScratch).Cleanup)
}

// endTmpContainerLocked removes the world-usable TMPDIR container, or, when a
// detached command still uses it, releases its lease and leaves it for the
// crashed-scratch sweep. The caller holds scratchMu.
func (e *LocalExecutionEnvironment) endTmpContainerLocked() error {
	if e.detachedSpawned {
		if e.unsandboxedTmp != nil {
			return e.unsandboxedTmp.Retain()
		}
		return nil
	}
	tmp := e.unsandboxedTmp
	e.unsandboxedTmp = nil
	return tmp.Remove()
}

// AdoptSessionScratch moves every per-session scratch directory from `from` —
// the one EnableSandbox provisioned and the one an unsandboxed env minted on
// its first command — onto this env, leases included, leaving `from` owning
// none. A session that swaps its environment for a re-rooted clone (a worktree
// enter, exit, or re-entry on resume) keeps working in the scratch the original
// provisioned: the clone's re-rooted kernel wrapper carries the same session
// tmp, and an unsandboxed clone exports whatever it is handed. Exactly one
// environment owns each scratch, and it must be the one the session's own
// teardown reaches, so ownership follows the swap.
//
// A kind this env already owns stays put. Live commands on this env may be
// using it — a child spawned with no working_dir shares its parent's
// environment object, and mints a scratch there while the parent is away on a
// clone — so this env's path stays stable for them and the incoming one is
// retained instead: its lease released, its directory kept for the handoff,
// as with any scratch a session hands off. Nothing is ever dropped unreleased.
func (e *LocalExecutionEnvironment) AdoptSessionScratch(from *LocalExecutionEnvironment) {
	if from == nil || from == e {
		return
	}
	// The two envs' locks are never held together, so swaps in opposite
	// directions cannot deadlock; whatever this env keeps is retained after
	// both are released.
	//
	// Between the two, `from` owns nothing, and a command a child sharing it
	// spawns there mints a scratch this move never sees. That interval needs no
	// bookkeeping of its own: its outcome is the one a spawn landing just AFTER
	// the move produces, which is how an emptied source already behaves. The
	// one thing it does not reproduce is timing, not outcome — the source's
	// invalidateSandboxFS below runs after the interval, so a layer the mint
	// had just built is retired where the later ordering would have kept it,
	// costing one lazy rebuild and nothing else.
	//
	// What accounts for the scratch is the ENVIRONMENT, not the move. A session
	// keeps every environment it swapped away from within its close's reach:
	// worktreeRestoreEnv or abandonedEnvs, and swapEnvAndRefresh writes the
	// install and BOTH records under one s.mu hold, which close reads under the
	// same lock — so a close can never observe an installed target without the
	// source recorded. That record is what releases the lease. A "moving" flag
	// making the mint wait would change no outcome, and it would put a blocking
	// wait in commandEnvironment's overlay, which every spawn on every
	// environment goes through.
	from.scratchMu.Lock()
	owned, unsandboxed := from.ownedSessionTmp, from.unsandboxedScratch
	from.ownedSessionTmp, from.unsandboxedScratch = nil, nil
	window := from.scratchMovedOut
	root, session := from.scratchRoot, from.scratchSession
	from.scratchMu.Unlock()
	if window != nil {
		window()
	}
	e.scratchMu.Lock()
	// The session's name moves with it too, so a scratch first minted on this
	// env is still the session's own directory rather than a disposable one.
	if e.scratchRoot == "" {
		e.scratchRoot, e.scratchSession = root, session
	}
	if e.ownedSessionTmp == nil {
		e.ownedSessionTmp, owned = owned, nil
	}
	if e.unsandboxedScratch == nil {
		e.unsandboxedScratch, unsandboxed = unsandboxed, nil
	}
	e.scratchMu.Unlock()
	// Safe outside scratchMu: both pointers were taken out of from exclusively
	// under from.scratchMu, so no environment references them any more and no
	// concurrent retain can reach them.
	_ = owned.Retain()
	_ = unsandboxed.Retain()
	// The file-tool layers each env built around the scratch it held go through
	// the same drain a rebuild uses. The source's go unconditionally: it no
	// longer owns the scratch whatever root its layers recorded (a wrapper
	// reports the same session tmp before and after the move), and it may never
	// be touched again (a clone discarded on exit), so without this its root
	// descriptors would stay open for the daemon's uptime. The target's go only
	// if its effective path changed; it is the environment the session now
	// works in.
	from.invalidateSandboxFS()
	e.retireStaleFileToolLayers()
}

// retireStaleFileToolLayers retires every cached file-tool layer whose scratch
// root no longer matches this env's effective scratch path — the layers a
// scratch move left behind — so they close once drained instead of waiting
// for a lookup that may never come.
func (e *LocalExecutionEnvironment) retireStaleFileToolLayers() {
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	scratch := e.sessionScratchPath()
	if e.sbfs != nil && e.sbfsScratch != scratch {
		e.retireFileToolLayerLocked(e.sbfs)
		e.sbfs = nil
		e.sbfsScratch = ""
	}
	if e.scratchFS != nil && e.scratchFSRoot != scratch {
		e.retireFileToolLayerLocked(e.scratchFS)
		e.scratchFS = nil
		e.scratchFSRoot = ""
	}
}
