package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/identifier"
)

// worktreeCreateCoreResult carries worktreeCreateCore's outputs; a struct
// rather than four positional strings so callers can't transpose them.
type worktreeCreateCoreResult struct {
	Path, Branch, BaseSHA, MainRoot string
	// MetaDir is the project metadata dir the sidecar was written to. A nested
	// name puts the lane under a subdirectory of the project dir while the
	// sidecar stays here, so a rollback must not derive it from Path.
	MetaDir string
	Project identifier.Project
	Run     worktree.GitRunner
	// Done disposes the control environment Run is bound to (its scratch and
	// lease); the caller runs it once it is finished with Run.
	Done func()
}

// worktreeCreateCore is the shared git-level portion of managed-worktree
// creation (spec §3 steps 1-6): resolve the main repo root, validate the name,
// resolve the base commit from active's active root, reject a pre-existing
// branch, write the metadata sidecar BEFORE `git worktree add` (crash-safe
// ordering), and atomically add+lock the worktree with lockReason. It is
// reused by worktreeCreate (a session's own managed worktree, EvCreate +
// session marker) and createDelegateWorktree (a parent-side isolation lane,
// EvDelegateCreate + evener:dlg: marker — spec §9 lifecycle step 1: "reuse the
// create plumbing"). ev selects the Decide row gating the atomic add: Decide
// is total and both EvCreate and EvDelegateCreate map Unlocked to
// ActAtomicAddLock, so a mismatch is an internal-error guard, never
// user-reachable. sidecarMutate lets the caller stamp extra sidecar fields
// (delegate_id) before the write; nil for a session's own create. errPrefix
// names the caller in error text. It neither leaves nor enters any env — the
// caller decides what, if anything, to do with the returned path; the
// returned run is the control-env GitRunner (rooted at mainRoot) so a caller
// that leaves/enters an env (worktreeCreate) does not have to rebuild it.
//
// branch names the git branch cut for the lane and may differ from name: a
// delegate isolation lane's directory stays keyed to its delegate id while its
// branch carries a mnemonic name (the delegate-lane branch-names design). The
// branch is ref-format-validated and existence-checked exactly as the name's
// branch was when the two were one string; the name keeps its own validation
// because it remains the directory component and the sidecar key.
func (s *Session) worktreeCreateCore(ctx context.Context, active *execenv.LocalExecutionEnvironment, name, branch, baseRef string, ev worktree.LockEvent, lockReason, errPrefix string, sidecarMutate func(*worktree.Sidecar)) (worktreeCreateCoreResult, error) {
	activeRoot := active.WorkingDirectory()

	// Step 1: resolve the shared canonical Project through linked-worktree
	// pointers. This is deliberately the only identity resolution in the create
	// operation; every path below is derived from this value.
	project, err := resolveWorktreeProject(active, activeRoot)
	if err != nil {
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: resolve project: %w", errPrefix, err)
	}
	if !projectIsGitCheckout(project) {
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: not in a git repository", errPrefix)
	}
	mr := project.CanonicalPath

	// Control env rooted at the main repo root; every lifecycle git command runs
	// through it (spec §2 "Git control environment").
	controlEnv, cerr := s.worktreeControlEnv(mr)
	if cerr != nil {
		return worktreeCreateCoreResult{}, cerr
	}
	run := s.newWorktreeGitRunner(ctx, controlEnv)
	// The control env's lifetime is the caller's on success (Done) and this
	// function's on every failure below.
	handedOff := false
	defer func() {
		if !handedOff {
			endEnvironmentScratch(controlEnv)
		}
	}()

	// Step 3: worktree path under <worktreeRoot>/<Project.ID>/<name>.
	worktreeRoot, err := s.worktreeRootForProject(s.currentStateDir(), project)
	if err != nil {
		return worktreeCreateCoreResult{}, err
	}
	projectDir := filepath.Join(worktreeRoot, project.ID)
	worktreePath := filepath.Join(projectDir, filepath.FromSlash(name))
	metaDir := metaDirForProject(projectDir)

	// Step 4: validate name (regex, pure — spec §8: "name fails validation ->
	// error before any git call") BEFORE the git version preflight (spec §3
	// step 6, memoized once per session) and check-ref-format, so an invalid
	// name never reaches a git subprocess; resolve the base to a SHA from the
	// active root, and reject a pre-existing branch.
	if verr := worktree.ValidateName(name); verr != nil {
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: %w", errPrefix, verr)
	}
	if verr := s.ensureWorktreeGitVersion(run); verr != nil {
		return worktreeCreateCoreResult{}, verr
	}
	if _, verr := run("check-ref-format", "--branch", branch); verr != nil {
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: %q is not a valid git branch name", errPrefix, branch)
	}

	baseSHA, err := resolveBaseFromActiveRoot(run, activeRoot, baseRef)
	if err != nil {
		return worktreeCreateCoreResult{}, err
	}

	if branchExists(run, branch) {
		msg := fmt.Sprintf("%s: branch %q already exists", errPrefix, branch)
		if managedWorktreeExists(worktreePath) {
			msg += "; use manage_worktree switch to enter its worktree"
		}
		return worktreeCreateCoreResult{}, errors.New(msg)
	}

	// merge_target is the branch checked out at the active root at creation
	// time; empty when the active root is on a detached HEAD (spec §6).
	mergeTarget := branchAtRoot(run, activeRoot)

	// Step 5: MkdirAll the sidecar parent and write the sidecar FIRST, O_EXCL.
	if mkErr := os.MkdirAll(metaDir, 0o755); mkErr != nil {
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: create metadata dir: %w", errPrefix, mkErr)
	}

	// A branch claim is identity: refuse a branch an existing sidecar claims,
	// even a stale one whose branch is already gone from git (the residue a
	// failed sidecar delete can leave after a branch collection). Otherwise a
	// stale sidecar and a new lane would both claim the branch, and prune
	// sweep 2 would judge each claim by its own — possibly outdated —
	// metadata, deleting the new lane's branch. A stale claim self-heals:
	// sweep 2 removes it ("no worktree, no branch") on its next run. A
	// sidecar under this create's own name is the name collision the O_EXCL
	// sidecar write that follows already refuses with its own message. A
	// refusal implies the claiming sidecar exists, so the metadata dir above
	// was not created by this create.
	if claims, cErr := worktree.ListSidecars(metaDir); cErr == nil {
		for _, c := range claims {
			if c.Name != name && c.BranchOrName() == branch {
				return worktreeCreateCoreResult{}, fmt.Errorf("%s: branch %q is claimed by worktree %q; run manage_worktree prune or pick another branch", errPrefix, branch, c.Name)
			}
		}
	} else if !os.IsNotExist(cErr) {
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: listing worktree claims: %w", errPrefix, cErr)
	}
	sc := worktree.Sidecar{
		Name:           name,
		Branch:         branch,
		BaseSHA:        baseSHA,
		MergeTarget:    mergeTarget,
		OriginalRoot:   project.CanonicalPath,
		CreatorSession: s.id,
		CreatedAt:      s.sclock().Now().UTC().Format(time.RFC3339),
	}
	if sidecarMutate != nil {
		sidecarMutate(&sc)
	}
	if werr := s.writeWorktreeSidecar(metaDir, name, sc); werr != nil {
		if os.IsExist(werr) {
			// The name is occupied. Either a live concurrent create won the
			// race, or a create died between its O_EXCL open and its write,
			// leaving a stale sidecar ListSidecars hides. Only the stale,
			// undecodable case is named — a fresh undecodable file may be the
			// winner mid-write, and directing repair at it would point at a
			// live reservation. The file is never removed here: malformed
			// metadata does not authorize a destructive cleanup.
			if corrErr := worktree.CorruptStaleReservation(metaDir, name, worktree.ReconcileGrace); corrErr != nil {
				return worktreeCreateCoreResult{}, fmt.Errorf("%s: worktree %q is reserved by a corrupt sidecar (%w); remove that file to repair", errPrefix, name, corrErr)
			}
			return worktreeCreateCoreResult{}, fmt.Errorf("%s: a worktree named %q is already being created", errPrefix, name)
		}
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: write sidecar: %w", errPrefix, werr)
	}

	// Step 6: MkdirAll the worktree parent (nested for slash names), then create
	// AND lock in one atomic command (spec §3 step 6; §9 step 1; Decide gates
	// the action).
	if worktree.Decide(ev, worktree.Unlocked) != worktree.ActAtomicAddLock {
		// Coverage note: unreachable with the current two call sites. Both
		// worktreeCreate and createDelegateWorktree pass ev as a compile-time
		// constant (EvCreate / EvDelegateCreate respectively), and Decide's
		// table maps BOTH of those to ActAtomicAddLock for Unlocked (the only
		// state reachable here — mr/name/branch have already been validated
		// not to exist), proven exhaustively by
		// agent/internal/worktree/lockstate_test.go's TestDecideEveryCell.
		// Kept as a guard against a future caller passing an unexpected
		// LockEvent rather than trusting every call site forever.
		_ = s.deleteWorktreeSidecar(metaDir, name)
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: internal error: create is not an atomic-add-lock event", errPrefix)
	}
	if mkErr := os.MkdirAll(filepath.Dir(worktreePath), 0o755); mkErr != nil {
		_ = s.deleteWorktreeSidecar(metaDir, name)
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: create worktree parent dir: %w", errPrefix, mkErr)
	}
	if _, addErr := run("worktree", "add", "--lock", "--reason", lockReason, "-b", branch, "--", worktreePath, baseSHA); addErr != nil {
		// Step 6 crash-safety: a failed add (e.g. a refs/heads D/F conflict)
		// must delete the just-written sidecar in the same call, or the name
		// becomes uncreatable until a post-grace prune (spec §3 step 6).
		_ = s.deleteWorktreeSidecar(metaDir, name)
		return worktreeCreateCoreResult{}, fmt.Errorf("%s: git worktree add failed: %w", errPrefix, addErr)
	}

	handedOff = true
	return worktreeCreateCoreResult{
		Path:     worktreePath,
		Branch:   branch,
		BaseSHA:  baseSHA,
		MainRoot: project.CanonicalPath,
		MetaDir:  metaDir,
		Project:  project,
		Run:      run,
		Done:     func() { endEnvironmentScratch(controlEnv) },
	}, nil
}

// worktreeCreate performs the create operation (spec §3, all nine steps in
// order): the shared worktreeCreateCore (steps 1-6), then leaves any worktree
// the session was already inside and finally enters the new one.
func (s *Session) worktreeCreate(ctx context.Context, name, baseRef string) (WorktreeResult, error) {
	// create resolves both its project and its base SHA from the active root
	// (spec §2) without going through worktreeStateSnapshot, so a dead anchor is
	// checked here: refusing names the dead path instead of leaking the lstat.
	if err := s.worktreeAnchorGone(); err != nil {
		return WorktreeResult{}, err
	}
	// Snapshot the active env once. Base resolution is against the ACTIVE root
	// (the worktree if the session is in one, else the original root), while
	// placement keys off the stable main repo root (spec §2).
	active, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return WorktreeResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	marker := worktree.FormatSessionMarker(s.id)
	res, err := s.worktreeCreateCore(ctx, active, name, name, baseRef, worktree.EvCreate, marker, "manage_worktree create", nil)
	if err != nil {
		return WorktreeResult{}, err
	}
	defer res.Done()
	// Steps 1-6 added and locked the lane and wrote its sidecar for a session
	// that, past any failure below, will never enter it; every such failure
	// takes them back. The rollback runs on its own bounded control context,
	// not the request's: the close that refuses the swap has already cancelled
	// the request context res.Run is bound to, and a rollback through it would
	// fail silently.
	entered := false
	// The rollback runs after the swap has already left the close fence, and its
	// git forks on the process table a close reaps. Take an admission of its own
	// so a close that refused the swap waits for the undo it caused instead of
	// tearing the environment down mid-rollback. It has to be taken HERE, not
	// where the rollback starts: between the swap releasing its own admission
	// and this defer running there is a window a close fits through, and an
	// admission asked for inside it would be refused. So the admission spans the
	// whole operation and is named for it, and renames itself if a rollback does
	// start — a create held mid-enter is not a rollback and must not read as one.
	createAdmission, createFenced := s.beginEnvWork("create " + res.Path)
	defer func() {
		if createFenced {
			defer s.endEnvWork(createAdmission)
		}
		if entered {
			return
		}
		if createFenced {
			s.relabelEnvWork(createAdmission, "create rollback for "+res.Path)
		}
		run, done, err := s.worktreeCleanupRun(res.MainRoot)
		if err != nil {
			s.emit(events.WarningData{Message: fmt.Sprintf("rolling back worktree %s after a failed create failed: %v", res.Path, err)})
			return
		}
		defer done()
		if rbErr := s.rollbackFreshWorktree(run, res.Path, res.Branch, res.MetaDir, name); rbErr != nil {
			s.emit(events.WarningData{Message: fmt.Sprintf("rolling back worktree %s after a failed create failed: %v", res.Path, rbErr)})
		}
	}()

	// Step 7: creating a new worktree from inside a managed one is a LEAVE of
	// the old one — unlock it via Decide (spec §3 step 7; §5 EvLeave).
	if err := s.leaveCurrentWorktree(res.Run); err != nil {
		return WorktreeResult{}, err
	}

	// Step 8: enter the new worktree (env swap + refresh, saving the prior env).
	// A sandbox re-root the host cannot satisfy fails closed here rather than
	// entering the new worktree unconfined; a swap the session's close refused
	// fails the same way. Both roll the lane back above.
	if err := s.enterWorktree(res.Path, true); err != nil {
		return WorktreeResult{}, err
	}
	entered = true

	// Step 9: report the path, branch, base SHA, and main repo root.
	return WorktreeResult{
		Path:     res.Path,
		Branch:   res.Branch,
		BaseSHA:  res.BaseSHA,
		MainRoot: res.MainRoot,
	}, nil
}

// createDelegateWorktree performs the parent-side git-level portion of the
// native worktree tools spec §9 lifecycle step 1: create the delegate's
// isolation lane as a managed worktree named for the delegate id, branched
// from the parent's active HEAD, locked with the delegate's evener:dlg: marker.
// branch is the git branch cut for the lane, and this function owns its
// empty-means-id default: callers pass the descriptor's raw value, and an empty
// branch names the branch with the delegate id (the delegate-lane branch-names
// design — the directory, sidecar, and all addressing stay id-keyed).
// It does not touch the parent's own env at all — the caller (createDelegate)
// roots the CHILD env at the returned path via prepareSubagentRun's
// workingDir override. Only valid for a local execution environment; errors
// clearly otherwise (spec §9 "Tool surface").
func (s *Session) createDelegateWorktree(ctx context.Context, delegateID, branch string) (path, laneBranch, baseSHA, mainRoot string, project identifier.Project, err error) {
	active, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return "", "", "", "", identifier.Project{}, errors.New(`delegate isolation:"worktree" requires a local execution environment`)
	}
	if branch == "" {
		branch = delegateID
	}
	lockReason := worktree.FormatDelegateMarker(delegateID, s.id)
	res, err := s.worktreeCreateCore(ctx, active, delegateID, branch, "", worktree.EvDelegateCreate, lockReason, `delegate isolation:"worktree"`, func(sc *worktree.Sidecar) {
		sc.DelegateID = delegateID
	})
	if err != nil {
		return "", "", "", "", identifier.Project{}, err
	}
	res.Done()
	return res.Path, res.Branch, res.BaseSHA, res.MainRoot, res.Project, nil
}

// rollbackFreshDelegateWorktree best-effort removes a just-created, still-empty
// isolation lane when delegate creation fails after createDelegateWorktree
// succeeded but before the delegate durably exists (a prepareSubagentRun,
// attach, or launch failure). The lane provably has zero commits beyond its
// base (it was just cut), so this is always safe — never a "changed lane"
// disposal decision. Errors are swallowed: a leaked locked-but-orphaned lane
// is a `manage_worktree list`-visible annoyance an operator can clean up by
// hand, never data loss, and failing the caller's already-failed spawn on a
// cleanup error would only obscure the original error.
// branch is the git branch the lane was cut on, so the rollback deletes the
// branch git actually holds while the sidecar stays keyed to the id.
func (s *Session) rollbackFreshDelegateWorktree(delegateID, branch, lanePath string, project identifier.Project) {
	if project.ID == "" || project.CanonicalPath == "" {
		return
	}
	worktreeRoot, err := s.worktreeRootForProject(s.currentStateDir(), project)
	if err != nil {
		return
	}
	// Best-effort: a control env the host cannot confine or a control policy it
	// cannot satisfy skips the rollback rather than running it unscoped.
	run, done, err := s.worktreeCleanupRun(project.CanonicalPath)
	if err != nil {
		return
	}
	defer done()
	// Swallowed here, not at the core: this rollback runs on a delegate-create
	// path whose own failure is the one worth reporting.
	_ = s.rollbackFreshWorktree(run, lanePath, branch, metaDirForProject(filepath.Join(worktreeRoot, project.ID)), delegateID)
}

// rollbackFreshWorktree takes back a just-created, still-empty managed
// worktree: its lock, the worktree itself, the branch cut for it, and its
// metadata sidecar under metaDir. It is the git-level core
// rollbackFreshDelegateWorktree wraps for an isolation lane and worktreeCreate
// uses on every failure after its core added the lane.
//
// Every step is attempted regardless of what the ones before it did: a lock
// that would not come off must not stop the worktree removal that makes the
// lock moot. What it could not take back is JOINED AND RETURNED rather than
// swallowed — the caller decides. A returned error never displaces the failure
// that triggered the rollback (rollbackFreshDelegateWorktree's rationale); the
// point is that residue this leaves is reported somewhere rather than silent.
func (s *Session) rollbackFreshWorktree(run worktree.GitRunner, lanePath, branch, metaDir, sidecarName string) error {
	var errs []error
	if _, err := run("worktree", "unlock", lanePath); err != nil {
		errs = append(errs, fmt.Errorf("unlocking the lane: %w", err))
	}
	if _, err := run("worktree", "remove", "--force", "--", lanePath); err != nil {
		errs = append(errs, fmt.Errorf("removing the lane: %w", err))
	}
	if _, err := run("branch", "-D", branch); err != nil {
		errs = append(errs, fmt.Errorf("deleting branch %s: %w", branch, err))
	}
	if err := s.deleteWorktreeSidecar(metaDir, sidecarName); err != nil {
		errs = append(errs, fmt.Errorf("deleting the sidecar: %w", err))
	}
	return errors.Join(errs...)
}
