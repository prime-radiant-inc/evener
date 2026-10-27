package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/identifier"
)

// currentStateDir reads s.stateDir under s.mu.
func (s *Session) currentStateDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateDir
}

// ensureWorktreeGitVersion runs the once-per-session git-version preflight
// (spec §3 step 6). worktree.CheckGitVersion always re-runs, so this memoizes a
// successful result on the session.
func (s *Session) ensureWorktreeGitVersion(run worktree.GitRunner) error {
	s.mu.Lock()
	done := s.worktreeGitVersionOK
	s.mu.Unlock()
	if done {
		return nil
	}
	if err := worktree.CheckGitVersion(run); err != nil {
		return fmt.Errorf("manage_worktree create: %w", err)
	}
	s.mu.Lock()
	s.worktreeGitVersionOK = true
	s.mu.Unlock()
	return nil
}

// leaveCurrentWorktree unlocks the managed worktree the session currently
// occupies, if any, routing the decision through Decide (spec §5 table row
// "leave old worktree (switch-away, create-away, exit, clean close)" —
// EvLeave). Every caller runs it before swapping s.env away, while
// s.worktreeCurrentPath still names the worktree being left: create's step 7
// (create-away), switch's step 3 (switch-away, after locking the new target
// first), and exit's step 2.
func (s *Session) leaveCurrentWorktree(run worktree.GitRunner) error {
	s.mu.Lock()
	oldPath := s.worktreeCurrentPath
	s.mu.Unlock()
	if oldPath == "" {
		return nil
	}

	locked, reason, err := lockStateOf(run, oldPath)
	if err != nil {
		return fmt.Errorf("manage_worktree: inspecting the current worktree lock: %w", err)
	}
	return s.leaveWorktreeWithLockState(run, oldPath, locked, reason)
}

func (s *Session) leaveCurrentWorktreeFromPorcelain(run worktree.GitRunner, porcelain []worktree.PorcelainEntry) error {
	s.mu.Lock()
	oldPath := s.worktreeCurrentPath
	s.mu.Unlock()
	if oldPath == "" {
		return nil
	}

	locked, reason := lockStateFromPorcelain(porcelain, oldPath)
	return s.leaveWorktreeWithLockState(run, oldPath, locked, reason)
}

func (s *Session) leaveWorktreeWithLockState(run worktree.GitRunner, oldPath string, locked bool, reason string) error {
	if err := s.releaseOwnWorktreeLock(run, oldPath, locked, reason); err != nil {
		return fmt.Errorf("manage_worktree: unlocking the current worktree: %w", err)
	}
	return nil
}

// releaseOwnWorktreeLock applies the EvLeave lock rule to path's observed lock
// state: it releases the lock when the marker is this session's own, and is a
// no-op for an unlocked, foreign, or delegate-marked tree. The error is the raw
// unlock failure, so each caller can name the tree in its own terms — leaving
// the occupied worktree calls it "the current worktree", while the close-time
// sweep names the individual lane it could not release.
func (s *Session) releaseOwnWorktreeLock(run worktree.GitRunner, path string, locked bool, reason string) error {
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, "")
	}
	if worktree.Decide(worktree.EvLeave, st) != worktree.ActUnlock {
		return nil
	}
	_, err := run("worktree", "unlock", path)
	return err
}

// occupiesManagedLane reports whether path is the managed lane this session is
// standing in: the lane create/switch recorded as current, or the lane the
// active environment is rooted in or beneath (a session launched or resumed
// directly inside a managed worktree never went through create/switch). A session legitimately
// holds its own occupancy marker on that lane; the identical marker on any other
// lane is crash residue (spec §5: "a worktree found locked with the session's
// own marker while the session is not occupying it ... is the session's to
// release"). The comparison canonicalizes both sides, so a persisted symlink
// alias of the occupied lane is not mistaken for residue.
func (s *Session) occupiesManagedLane(path string) bool {
	s.mu.Lock()
	current := s.worktreeCurrentPath
	local, _ := s.env.(*execenv.LocalExecutionEnvironment)
	s.mu.Unlock()
	target := canonicalOrClean(path)
	if current != "" && canonicalOrClean(current) == target {
		return true
	}
	return local != nil && identifier.PathUnder(canonicalOrClean(local.WorkingDirectory()), target)
}

// lockStateOf reports whether the worktree at path is locked and, if so, its
// (C-unquoted) lock reason, by parsing `git worktree list --porcelain` from the
// control env. A path git does not list is reported unlocked (not an error) —
// the caller decides what an unknown path means.
func lockStateOf(run worktree.GitRunner, path string) (locked bool, reason string, err error) {
	porcelain, err := readWorktreePorcelain(run)
	if err != nil {
		return false, "", err
	}
	locked, reason = lockStateFromPorcelain(porcelain, path)
	return locked, reason, nil
}

func readWorktreePorcelain(run worktree.GitRunner) ([]worktree.PorcelainEntry, error) {
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return worktree.ParsePorcelain(out), nil
}

func lockStateFromPorcelain(porcelain []worktree.PorcelainEntry, path string) (locked bool, reason string) {
	target := canonicalOrClean(path)
	for _, e := range porcelain {
		if canonicalOrClean(e.Path) == target {
			return e.Locked, e.LockReason
		}
	}
	return false, ""
}

// branchOfCheckout resolves the branch checked out in the worktree at path
// from git's own worktree list — the only authoritative source once the
// metadata sidecar is gone. A bare or detached worktree names no branch.
func branchOfCheckout(porcelain []worktree.PorcelainEntry, path string) (string, bool) {
	target := canonicalOrClean(path)
	for _, e := range porcelain {
		if canonicalOrClean(e.Path) == target && e.Branch != "" {
			return strings.TrimPrefix(e.Branch, "refs/heads/"), true
		}
	}
	return "", false
}

// worktreeControlRun builds a GitRunner over the control env rooted at
// mainRepoRoot (spec §2 "Git control environment"), for lifecycle git calls
// that must run outside the (possibly worktree-rooted) user-facing env.
func (s *Session) worktreeControlRun(ctx context.Context, mainRepoRoot string) (worktree.GitRunner, func(), error) {
	run, _, done, err := s.worktreeControlRunWithFiles(ctx, mainRepoRoot)
	return run, done, err
}

// worktreeControlRunWithFiles is worktreeControlRun that also returns the
// control environment's file layer, for an op that edits the worktree registry's
// files itself and must do so under the control policy's write authority.
func (s *Session) worktreeControlRunWithFiles(ctx context.Context, mainRepoRoot string) (worktree.GitRunner, execenv.FileMutator, func(), error) {
	controlEnv, err := s.worktreeControlEnv(mainRepoRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	files, ok := controlEnv.(execenv.FileMutator)
	if !ok {
		endEnvironmentScratch(controlEnv)
		return nil, nil, nil, errors.New("manage_worktree requires a file-mutating execution environment")
	}
	return s.newWorktreeGitRunner(ctx, controlEnv), files, func() { endEnvironmentScratch(controlEnv) }, nil
}

// worktreeCleanupRun is worktreeControlRun on a context of its own, bounded by
// close-cascade budget — the budget the close disposal pass spends
// (session_worktree_close.go); the close-time cleanup runners themselves are
// deliberately unbudgeted. A rollback of a refused or failed op must not
// inherit the request context, which the close that refused the swap has
// already cancelled. The returned done cancels the context and disposes the
// control environment; the caller runs it when the rollback is over.
func (s *Session) worktreeCleanupRun(mainRepoRoot string) (worktree.GitRunner, func(), error) {
	s.closeCtxMu.RLock()
	closeCtx := s.closeCtx
	s.closeCtxMu.RUnlock()
	budget := closePassBudget(closeCtx)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	run, dispose, err := s.worktreeControlRun(ctx, mainRepoRoot)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return run, func() { cancel(); dispose() }, nil
}

// relPathUnderManagedDir canonicalizes projectDir (spec §5: "canonicalize
// both sides") and, if canonicalPath lives strictly under it, returns its
// path relative to projectDir. ok is false for canonicalPath == projectDir
// itself, anything outside it, or an unresolvable relation — a projectDir
// that does not exist yet canonicalizes to itself, under which nothing can
// resolve, so this correctly reports ok=false rather than panicking or
// guessing. Shared by isUnderManagedDir (spec §4 switch by-path step 2) and
// the list/prune managed-entry enumeration (spec §5 list step 2's "not bare
// HasPrefix" rule — a bare string-prefix compare collides when one Project.ID
// string prefixes another).
func relPathUnderManagedDir(canonicalPath, projectDir string) (rel string, ok bool) {
	canonicalProjectDir := filepath.Clean(projectDir)
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		canonicalProjectDir = filepath.Clean(resolved)
	}
	r, err := filepath.Rel(canonicalProjectDir, canonicalPath)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return r, true
}

// isUnderManagedDir reports whether canonicalPath lives strictly under the
// managed worktree directory projectDir (spec §4 switch by-path step 2; spec
// §5 `list` step 2).
func isUnderManagedDir(canonicalPath, projectDir string) bool {
	_, ok := relPathUnderManagedDir(canonicalPath, projectDir)
	return ok
}

// canonicalOrClean resolves path's symlinks (spec §5 list step 2:
// "canonicalize both sides... macOS /var → /private/var otherwise make
// managed worktrees silently vanish") and falls back to filepath.Clean(path)
// when EvalSymlinks fails — a directory git still has registered but that is
// momentarily absent (the exact case list step 1 must tolerate, "prunable")
// still compares correctly against a canonicalized projectDir, instead of
// being dropped from enumeration just because it cannot be resolved.
func canonicalOrClean(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// managedEntry pairs a porcelain worktree entry with its name (the path
// relative to projectDir, slash-separated regardless of OS — spec §6: names
// may contain "/"), shared by list and prune sweep 1's enumeration of
// registered managed worktrees (spec §5 list step 2 / prune sweep 1).
type managedEntry struct {
	worktree.PorcelainEntry
	Name string
}

// managedPorcelainEntries filters porcelain to entries whose worktree path is
// under projectDir (spec §5 list step 2 / prune sweep 1's "registered managed
// worktrees"), pairing each with its derived name.
func managedPorcelainEntries(porcelain []worktree.PorcelainEntry, projectDir string) []managedEntry {
	var out []managedEntry
	for _, e := range porcelain {
		rel, ok := relPathUnderManagedDir(canonicalOrClean(e.Path), projectDir)
		if !ok {
			continue
		}
		out = append(out, managedEntry{PorcelainEntry: e, Name: filepath.ToSlash(rel)})
	}
	return out
}

// checkoutLocationOf finds which worktree currently has refs/heads/<branch>
// checked out, for reporting prune sweep 2's "checked out at <path>" skip
// (spec §5 sweep 2). ok is false when no worktree has the branch checked out.
func checkoutLocationOf(run worktree.GitRunner, branch string) (path string, ok bool) {
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return "", false
	}
	full := "refs/heads/" + branch
	for _, e := range worktree.ParsePorcelain(out) {
		if e.Branch == full {
			return e.Path, true
		}
	}
	return "", false
}

// shortSHA trims a commit SHA to its first 12 chars for legible messages,
// leaving shorter strings untouched.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// gitCmdError carries a git subprocess's non-zero exit through the
// worktree.GitRunner contract. It satisfies interface{ ExitCode() int } so the
// worktree predicates can distinguish an expected negative result (e.g.
// `merge-base --is-ancestor` exiting 1) from a genuine failure.
type gitCmdError struct {
	code   int
	args   []string
	stderr string
}

func (e *gitCmdError) Error() string {
	msg := fmt.Sprintf("git %s: exit %d", strings.Join(e.args, " "), e.code)
	if e.stderr != "" {
		msg += ": " + e.stderr
	}
	return msg
}

func (e *gitCmdError) ExitCode() int { return e.code }

// gitRunner builds a worktree.GitRunner that executes git through env (the
// control env, rooted at the main repo root). RunGit execs git directly from
// this argv when env supports it (production always does), so a worktree
// name or path can never inject shell syntax (spec §2) — there is no shell
// command line for it to inject into. Environments without that capability
// fall back to RunGit's shell-escaped wrapper, which preserves the same
// guarantee the old escaping did. A non-zero exit is reported as a
// *gitCmdError carrying the code and stderr.
//
// A successful invocation whose capture tripped the per-stream cap is refused
// only where stdout is DECODED as records (see gitOutputIsParsed): a truncated
// porcelain listing or ref set would be parsed as if whole. A mutating
// subcommand's stdout is progress diagnostic nobody reads, so its success is
// preserved even when that output truncated — failing a completed `worktree
// add` on a verbose post-checkout hook would strand the worktree and branch it
// just created.
func gitRunner(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
	return func(args ...string) (string, error) {
		res, err := execenv.RunGit(ctx, env, "", worktreeGitTimeoutMS, args...)
		if res.ExitCode != 0 {
			return res.Stdout, &gitCmdError{code: res.ExitCode, args: args, stderr: strings.TrimSpace(res.Stderr)}
		}
		if err != nil {
			return res.Stdout, err
		}
		// Only a capped STDOUT poisons a parsed command: the parsed stream is
		// stdout, so a stderr cap is the command's diagnostics, not its records.
		if res.StdoutTruncated && gitOutputIsParsed(args) {
			return res.Stdout, fmt.Errorf("git %s: %w", strings.Join(args, " "), execenv.ErrOutputTruncated)
		}
		return res.Stdout, nil
	}
}

// gitWorktreeMutations are the `git worktree` subcommands that change state;
// their stdout is progress diagnostic, not records.
var gitWorktreeMutations = map[string]bool{
	"add": true, "remove": true, "prune": true, "lock": true,
	"unlock": true, "move": true, "repair": true,
}

// gitOutputIsParsed reports whether an invocation's stdout is decoded as
// records, so a truncated capture must be refused rather than read as if whole.
// Only the state-changing subcommands are treated as unparsed; an unknown
// command defaults to parsed, so a new record read added later is refused by
// default rather than silently accepted.
func gitOutputIsParsed(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if args[0] == "worktree" {
		if len(args) < 2 {
			return true
		}
		return !gitWorktreeMutations[args[1]]
	}
	if args[0] == "branch" {
		for _, a := range args[1:] {
			switch a {
			case "-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy":
				return false
			}
		}
	}
	return true
}

// newWorktreeGitRunner returns the lifecycle Git boundary for this session.
// Production uses gitRunner; package-agent tests can inject a scripted runner
// through testConfig to exercise the real orchestration without a subprocess.
// The returned runner memoizes `git worktree list --porcelain` for its own
// lifetime (see cachingWorktreeRunner): a pass that evaluates several lanes
// reads the listing once instead of once per lane, and any command that could
// change the listing drops the memo. Each call here mints a fresh runner, so a
// cached listing never crosses operations.
func (s *Session) newWorktreeGitRunner(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
	if scripted := s.cfg.testOnly.worktreeGitRunner; scripted != nil {
		return cachingWorktreeRunner(scripted(ctx, env))
	}
	return cachingWorktreeRunner(gitRunner(ctx, env))
}

// resolveLaneMainRoot resolves the main repository root of the worktree at
// lanePath through a probe clone rooted there, and disposes the probe: a
// resolution that falls back to the git binary mints a scratch on it. Empty
// when the lane is not part of a repository.
func resolveLaneMainRoot(local *execenv.LocalExecutionEnvironment, lanePath string) string {
	probe := local.WithWorkingDirectory(lanePath)
	defer func() { _ = probe.DisposeSessionScratch() }()
	return execenv.ResolveMainRepoRoot(probe, lanePath)
}

// laneControlEnv builds the control environment a lane-scoped lifecycle step
// runs its git through: a clone rooted at the lane's main repository root,
// resolved through resolveLaneMainRoot. The clone is this step's alone, and
// its first command mints a scratch and takes its lease, so done disposes it
// and the caller runs it on the way out — every caller gets one by
// construction. ok is false when the lane is not part of a repository. The
// caller applies whatever control policy its step needs.
func laneControlEnv(local *execenv.LocalExecutionEnvironment, lanePath string) (controlEnv *execenv.LocalExecutionEnvironment, mainRoot string, done func(), ok bool) {
	mainRoot = resolveLaneMainRoot(local, lanePath)
	if mainRoot == "" {
		return nil, "", nil, false
	}
	controlEnv = local.WithWorkingDirectory(mainRoot)
	return controlEnv, mainRoot, func() { _ = controlEnv.DisposeSessionScratch() }, true
}

// worktreeControlEnv returns a local env rooted at mainRepoRoot for lifecycle
// git commands (spec §2 "Git control environment", §7 controlEnv()). The
// user-facing s.env may be rooted inside a worktree after create/switch, and
// ExecCommand rejects a workingDir outside its RootDir; WithWorkingDirectory
// re-roots unconditionally and shares PID/fs with the parent, so this control
// env can target the main root without disturbing the confined tool env. Errors
// when the session env is not a LocalExecutionEnvironment (spec §2: env
// swapping and local git worktree management are unsupported otherwise).
//
// The control env is a clone built for one op, and its first git command
// mints a scratch dir and takes its lease; nothing references the clone once
// the op returns, so the op disposes it (worktreeControlRun's done, the create
// core's Done) or each op leaves a directory with a held lease behind.
func (s *Session) worktreeControlEnv(mainRepoRoot string) (execenv.ExecutionEnvironment, error) {
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return nil, errors.New("manage_worktree requires a local execution environment")
	}
	control := local.WithWorkingDirectory(mainRepoRoot)
	// Carry the CONTROL sandbox policy (main repo + .git/worktrees registry
	// writable; config/hooks denied) rather than the current worktree's tool
	// policy, so lifecycle git can manage the registry. Fail closed if the host
	// cannot satisfy it — the lifecycle op is refused, not run unscoped.
	if err := control.SandboxReRootError(); err != nil {
		return nil, err
	}
	if err := s.useWorktreeControlPolicy(control, mainRepoRoot); err != nil {
		return nil, err
	}
	return control, nil
}

// worktreeStateSnapshot implements worktreeGuard.state() (spec §7): the session
// occupancy snapshot with the strict anchor policy that refuses a dead anchor
// rather than falling back. See worktreeStateSnapshotWith.
func (s *Session) worktreeStateSnapshot() worktreeState {
	return s.worktreeStateSnapshotWith(false)
}

// worktreeStateSnapshotWith is worktreeStateSnapshot with an explicit
// dead-anchor policy. It reads the mutable occupancy fields under s.mu, then
// resolves the canonical Project and worktree root OUTSIDE the lock (project
// resolution may fork git; s.mu must never be held across a subprocess — spec
// §7). When the session's active working directory is not an existing
// directory, a snapshot that does not allow a fallback carries the named
// worktreeAnchorGoneError, so every caller fails honestly instead of leaking
// the bare resolve-project lstat (issue #4671). allowAnchorFallback is set only
// by `list`, which resolves the project from the live restore root so the lanes
// that still exist are still enumerated. The fallback is deliberately NOT
// global: it would otherwise silently re-enable close-time unlocking, the
// residue sweep, and adopt/remove/prune under a dead anchor.
func (s *Session) worktreeStateSnapshotWith(allowAnchorFallback bool) worktreeState {
	s.mu.Lock()
	local, _ := s.env.(*execenv.LocalExecutionEnvironment)
	restore := s.worktreeRestoreEnv
	current := s.worktreeCurrentPath
	stateDir := s.stateDir
	s.mu.Unlock()

	st := worktreeState{env: local, restoreEnv: restore, currentWorktree: current}
	if local == nil {
		return st
	}
	// The anchor is the session's working directory; resolving project identity
	// from a missing path is the lstat failure this guards against.
	resolveEnv, resolvePath := local, local.WorkingDirectory()
	if !directoryExists(resolvePath) {
		restoreRoot := ""
		if restore != nil {
			restoreRoot = restore.WorkingDirectory()
		}
		if !allowAnchorFallback || restoreRoot == "" || !directoryExists(restoreRoot) {
			st.err = &worktreeAnchorGoneError{path: resolvePath}
			return st
		}
		resolveEnv, resolvePath = restore, restoreRoot
	}
	project, err := resolveWorktreeProject(resolveEnv, resolvePath)
	if err != nil {
		st.err = err
		return st
	}
	st.project = project
	if projectIsGitCheckout(project) {
		st.mainRepoRoot = project.CanonicalPath
		st.worktreeRoot, st.err = s.worktreeRootForProject(stateDir, project)
	}
	return st
}

// worktreeAnchorGone returns the named dead-anchor error when the session's
// active local working directory no longer exists, else nil. create and the
// worktree-isolated delegate spawn both resolve their project and base commit
// from that directory without going through worktreeStateSnapshot, so they
// need the check directly (issue #4671).
func (s *Session) worktreeAnchorGone() error {
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return nil
	}
	anchor := local.WorkingDirectory()
	if directoryExists(anchor) {
		return nil
	}
	return &worktreeAnchorGoneError{path: anchor}
}

// directoryExists reports whether path names an existing directory (following
// symlinks). It is the dead-anchor test shared by the snapshot and the explicit
// create/spawn checks.
func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (st worktreeState) resolutionError(operation string) error {
	if st.err == nil {
		return nil
	}
	return fmt.Errorf("manage_worktree %s: %w", operation, st.err)
}

// worktreeRootFor resolves a project path and derives the directory managed
// worktrees live under (spec §6
// "worktreeRoot derivation"): <stateDir>/worktrees when a runtime state dir
// launched the session, else the agent-owned project state dir for the main
// repo root plus /worktrees. It never imports cmdutil (spec §6).
func (s *Session) worktreeRootFor(env execenv.ExecutionEnvironment, stateDir, mainRepoRoot string) (string, error) {
	project, err := resolveWorktreeProject(env, mainRepoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve managed worktree project: %w", err)
	}
	return s.worktreeRootForProject(stateDir, project)
}

func (s *Session) worktreeRootForProject(stateDir string, project identifier.Project) (string, error) {
	if project.ID == "" || project.CanonicalPath == "" {
		return "", errors.New("resolve managed worktree state: project identity is empty")
	}
	if stateDir != "" {
		return filepath.Join(stateDir, "worktrees"), nil
	}
	projectDir, err := RuntimeDirForProjectWithStateHome(project, "")
	if err != nil {
		return "", err
	}
	return filepath.Join(projectDir, "worktrees"), nil
}

// resolveWorktreeProject is the single project-identity boundary for a
// managed-worktree operation. The shared identifier resolver follows linked
// worktree metadata and returns the canonical main checkout for both the main
// checkout and every linked checkout.
func resolveWorktreeProject(env execenv.ExecutionEnvironment, path string) (identifier.Project, error) {
	return identifier.ResolveProjectWith(path, execenv.NewProjectResolver(env))
}

func projectIsGitCheckout(project identifier.Project) bool {
	if project.CanonicalPath == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(project.CanonicalPath, ".git"))
	return err == nil
}

// enterWorktree implements worktreeGuard.enterWorktree() (spec §7): swap s.env
// to WithWorkingDirectory(path), saving the prior env the first time (single
// saved env, not a stack — spec §7 "env-restore model") and recording the
// occupied worktree path and whether it is evener-managed. The swap always uses
// WithWorkingDirectory so PID/fs sharing survives (spec §7 "WithWorkingDirectory
// correctness"). The env swap + refresh runs outside s.mu (swapEnvAndRefresh
// forks git for the snapshot); manage_worktree is serialized in the tool
// stream, so nothing else swaps the env concurrently.
func (s *Session) enterWorktree(path string, managed bool) error {
	if hook := s.cfg.testOnly.worktree.enterWorktree; hook != nil {
		if err := hook(path, managed); err != nil {
			return err
		}
	}
	s.mu.Lock()
	local, ok := s.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		s.mu.Unlock()
		return nil
	}
	saveRestore := s.worktreeRestoreEnv == nil
	prior := local
	next := local.WithWorkingDirectory(path)
	s.mu.Unlock()

	// Fail closed: when the sandbox policy could not be re-anchored to the new
	// worktree (a mode+net the host cannot satisfy at this target), next carries a
	// nil Sandbox/Wrapper plus the sticky refusal — refuse the switch and leave the
	// session in its prior (confined) env rather than running unconfined in the new
	// worktree. ReRoot's contract is that a worktree switch is refused here.
	if err := next.SandboxReRootError(); err != nil {
		return err
	}

	// The occupancy is recorded under the swap's own install lock hold: the
	// parked environment must be visible to a close the moment next is.
	record := func() {
		if saveRestore {
			s.worktreeRestoreEnv = prior
		}
		s.worktreeCurrentPath = path
		s.worktreeCurrentManaged = managed
	}
	if err := s.swapEnvAndRefresh(next, record); err != nil {
		return err
	}
	if hook := s.cfg.testOnly.enterWorktreeAfterSwap; hook != nil {
		hook()
	}
	return nil
}

// exitWorktree implements worktreeGuard.exitWorktree() (spec §7): restore the
// saved pre-worktree env and clear it so the next enter saves afresh. It
// returns ok=false when no restore env was saved (the session is not in a
// worktree entered via the tool), and an error when the swap was refused
// because the session is closing. This is the env-restore primitive only; the
// operation-level `exit` semantics (own-marker unlock, restore-land idempotent
// lock rule) layer on top of this.
func (s *Session) exitWorktree() (string, bool, error) {
	s.mu.Lock()
	restore := s.worktreeRestoreEnv
	s.mu.Unlock()
	if restore == nil {
		return "", false, nil
	}

	record := func() {
		s.worktreeRestoreEnv = nil
		s.worktreeCurrentPath = ""
		s.worktreeCurrentManaged = false
	}
	if err := s.swapEnvAndRefresh(restore, record); err != nil {
		return "", false, err
	}
	return restore.WorkingDirectory(), true, nil
}

// liveWorkUnder implements worktreeGuard.liveWorkUnder() (spec §7): live
// child/delegate/shell working directories at or under path, for the remove
// and prune guards (spec §5 remove step 4). Shell launch directories come from
// shell JobRecords. Delegate lanes, lifecycle, and child identities come from
// stable controller descriptors; a resident child's current env additionally
// covers a delegate that switched worktrees after launch. It is best-effort
// and read-only: a shell command that `cd`s after launch is invisible to it
// (spec §5 remove step 4). worktreeLiveWorkStub, a test-only seam, takes
// precedence when set (see its doc comment on the Session struct) so unit tests
// can exercise the guard call without spinning up real jobs.
func (s *Session) liveWorkUnder(path string) []string {
	return s.liveWorkUnderExceptIdleDelegate(path, "")
}

// liveWorkUnderExceptIdleDelegate omits one exact stable descriptor only when
// it is idle. The P3 collector uses this after resolving an owned lane's
// sidecar so the lane does not block its own authorized disposal; active work,
// descendants, shells, and every other retained descriptor still block it.
func (s *Session) liveWorkUnderExceptIdleDelegate(path, ignoredDelegateID string) []string {
	s.mu.Lock()
	stub := s.worktreeLiveWorkStub
	s.mu.Unlock()
	if stub != nil {
		return stub(path)
	}

	target := canonicalOrClean(path)
	var live []string

	if s.jobManager != nil {
		for _, h := range s.jobManager.liveShellHandles() {
			if identifier.PathUnder(canonicalOrClean(h.dir), target) {
				live = append(live, h.handle)
			}
		}
	}

	stableByChild := make(map[string]stableDelegateWorktreeSnapshot)
	stableDescriptorUnderTarget := make(map[string]bool)
	if s.delegateController != nil {
		for _, row := range s.delegateController.stableWorktreeSnapshots() {
			childID := strings.TrimSpace(row.descriptor.ChildSessionID)
			lanePath := strings.TrimSpace(row.descriptor.WorkingDir)
			if childID == "" || lanePath == "" {
				continue
			}
			stableByChild[childID] = row
			if !identifier.PathUnder(canonicalOrClean(lanePath), target) {
				continue
			}
			if row.delegateID == ignoredDelegateID && !row.active {
				stableDescriptorUnderTarget[childID] = true
				continue
			}
			label := subagentRetainedIdleLabel
			if row.active {
				label = subagentRunningLabel
			}
			live = append(live, childID+label)
			stableDescriptorUnderTarget[childID] = true
		}
	}

	if s.subagents != nil {
		for _, sub := range s.subagents.directSubagents() {
			child := sub.sess
			env := child.currentEnv()
			if env == nil {
				// Defensive: every *Session's env is set at construction and
				// stays non-nil for its whole life (session_lifecycle.go's
				// close path dereferences it unconditionally, so a nil env
				// would already be broken well before liveWorkUnder could
				// observe it). Kept as a guard against the ExecutionEnvironment
				// interface's zero value rather than assuming every caller of
				// subagents.track upholds the invariant forever.
				continue
			}
			wd := env.WorkingDirectory()
			if wd == "" {
				continue
			}
			if !identifier.PathUnder(canonicalOrClean(wd), target) {
				continue
			}
			if stable, ok := stableByChild[child.id]; ok {
				if stableDescriptorUnderTarget[child.id] {
					continue
				}
				label := subagentRetainedIdleLabel
				if stable.active {
					label = subagentRunningLabel
				}
				live = append(live, child.id+label)
				continue
			}
			// Honest label (spec §P1 step 3): a retained child that is neither
			// running a task nor mid-drive holds no live work of its own — it is
			// idle terminal history the dispose op can reclaim. Labeling it
			// "running" (as this scan once did unconditionally) dead-ends a model
			// trying to remove a clean, merged lane, because it correctly refuses
			// to bypass what looks like a live-work guard.
			sub.mu.Lock()
			active := sub.running || sub.driving
			sub.mu.Unlock()
			if active {
				live = append(live, child.id+subagentRunningLabel)
			} else {
				live = append(live, child.id+subagentRetainedIdleLabel)
			}
		}
	}

	return live
}

// subagentRunningLabel and subagentRetainedIdleLabel are the two live-work
// labels liveWorkUnder attaches to a child session rooted under a target path.
// The retained-idle label is also the signal the remove refusal keys on to
// offer a dispose suggestion (disposeHintForRetainedIdle).
const (
	subagentRunningLabel      = " (subagent, running)"
	subagentRetainedIdleLabel = " (subagent, retained — idle)"
)

// retainedIdleDelegateIDs resolves every live-work blocker in handles to an
// owned, worktree-isolated delegate id, returning ok=false the moment any
// blocker fails to resolve — a running shell, delegate job, or driving child
// (not a retained-idle subagent at all), a subagent with no matching delegate
// record, or a delegate that is not worktree-isolated or not owned by this
// session (a plain/ordinary/shared subagent, or another session's lane, has
// no disposable lane this op may touch). It backs both
// disposeHintForRetainedIdle's refusal-copy suggestion and worktreeRemove's
// force cascade (spec §P1 step 3; the force-cascade addendum), so the two stay
// in lockstep: whatever the hint offers to dispose is exactly what force goes
// on to dispose.
func (s *Session) retainedIdleDelegateIDs(handles []string) ([]string, bool) {
	if len(handles) == 0 || s.delegateController == nil {
		return nil, false
	}
	childIDs := make([]string, 0, len(handles))
	for _, h := range handles {
		id, ok := strings.CutSuffix(h, subagentRetainedIdleLabel)
		if !ok {
			return nil, false
		}
		childIDs = append(childIDs, id)
	}
	owned := s.delegateController.ownedStableWorktreeSnapshots(s)
	childToDelegate := make(map[string]string, len(owned))
	for _, row := range owned {
		if row.descriptor.ChildSessionID != "" && row.delegateID != "" {
			childToDelegate[row.descriptor.ChildSessionID] = row.delegateID
		}
	}
	dlgIDs := make([]string, 0, len(childIDs))
	for _, cid := range childIDs {
		dlg, ok := childToDelegate[cid]
		if !ok {
			return nil, false
		}
		dlgIDs = append(dlgIDs, dlg)
	}
	return dlgIDs, true
}

// disposeHintForRetainedIdle returns a remove-refusal suffix pointing the caller
// at the dispose op (spec §P1 step 3), but ONLY when every live-work blocker in
// handles resolves to an owned, worktree-isolated delegate
// (retainedIdleDelegateIDs). A retained-idle subagent holds no live work of its
// own — it is terminal history the dispose op can reclaim — so naming the
// delegate id and suggesting `manage_worktree op=dispose id=<dlg_…>`, or
// pointing at `force` to have `remove` run that cascade itself, gives the model
// a legitimate way forward instead of an opaque refusal it correctly will not
// bypass. It returns "" (leaving the generic refusal in place) when
// retainedIdleDelegateIDs cannot resolve every blocker.
func (s *Session) disposeHintForRetainedIdle(handles []string) string {
	dlgIDs, ok := s.retainedIdleDelegateIDs(handles)
	if !ok {
		return ""
	}
	suggestions := make([]string, 0, len(dlgIDs))
	for _, dlg := range dlgIDs {
		suggestions = append(suggestions, "manage_worktree op=dispose id="+dlg)
	}
	return "All blockers are retained, idle delegate(s) holding no live work; dispose them to proceed, or pass force to remove to have it dispose them automatically: " + strings.Join(suggestions, "; ")
}

// metaDirForProject returns the sidecar directory for a project's managed
// worktree tree: <projectDir>/.meta.
func metaDirForProject(projectDir string) string { return filepath.Join(projectDir, ".meta") }

// metaDirForLane returns the sidecar directory given a lane's worktree path
// (its parent is the project dir): <dir(lanePath)>/.meta.
func metaDirForLane(lanePath string) string { return filepath.Join(filepath.Dir(lanePath), ".meta") }
