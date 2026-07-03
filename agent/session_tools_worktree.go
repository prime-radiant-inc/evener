package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/llm"
)

// worktreeGitTimeoutMS bounds each git lifecycle subprocess the manage_worktree
// tool forks. `git worktree add` checks out a fresh tree and `git status` can
// take real time on a large repo, so the ceiling is generous.
const worktreeGitTimeoutMS = 300_000

// worktreeState is the snapshot worktreeGuard.state() returns (spec §7): the
// current env, the saved restore env, the resolved main repo root, the derived
// worktree root, and the managed worktree path the session currently occupies
// (empty when none). env and restoreEnv are nil when the session env is not a
// LocalExecutionEnvironment.
type worktreeState struct {
	env             *execenv.LocalExecutionEnvironment
	restoreEnv      *execenv.LocalExecutionEnvironment
	mainRepoRoot    string
	worktreeRoot    string
	currentWorktree string
}

// WorktreeResult is what a successful create returns to the handler and, via
// the tool result, to the model (spec §3 step 9): the new worktree's absolute
// path, its branch, the base commit it was cut from, and the resolved main
// repo root.
type WorktreeResult struct {
	Path     string
	Branch   string
	BaseSHA  string
	MainRoot string
}

// WorktreeSwitchResult is what a successful switch returns (spec §4 switch
// by-name step 6 / by-path step 3): the worktree's absolute path and branch,
// and whether entering it was a no-op because the session already occupied it
// (spec §4 switch by-name step 1).
type WorktreeSwitchResult struct {
	Path   string
	Branch string
	NoOp   bool
}

// WorktreeExitResult is what a successful exit returns (spec §4 exit step 6):
// the restored root and the path of the worktree just left. Warning is set
// only when the restore landed the session in a managed worktree that turned
// out to be locked by a foreign owner — exit warns and co-occupies rather
// than refusing, since a restore has to land somewhere (spec §4 exit step 4;
// §5 "Restores follow the same rule").
type WorktreeExitResult struct {
	RestoredRoot string
	LeftPath     string
	Warning      string
}

// worktreeGuard is the toolDeps facade the manage_worktree handler reaches
// session state through (spec §2 registration; §7 method list), mirroring
// taskGuard/goalGuard. Its members are closures over the owning Session so the
// handler never references the concrete *Session type; each forwards to a
// Session method that preserves the session's locking discipline. Tasks 14-16
// (switch/exit/remove/list/prune) share this same guard.
type worktreeGuard struct {
	// state snapshots the current worktree occupancy (spec §7 state()).
	state func() worktreeState
	// controlEnv returns a local env rooted at the main repo root for lifecycle
	// git commands (spec §7 controlEnv()). It errors when the session env is not
	// a LocalExecutionEnvironment.
	controlEnv func(mainRepoRoot string) (execenv.ExecutionEnvironment, error)
	// enterWorktree swaps the session env into path, saving the prior env the
	// first time (spec §7 enterWorktree()).
	enterWorktree func(path string)
	// exitWorktree restores the saved pre-worktree env and clears it, returning
	// the restored root and ok=false when no restore env was saved (spec §7
	// exitWorktree()).
	exitWorktree func() (restoredRoot string, ok bool)
	// liveWorkUnder reports live child/delegate/shell work rooted at or under
	// path (spec §7 liveWorkUnder()); remove/prune use it.
	liveWorkUnder func(path string) []string
	// create runs the create operation (spec §3) and returns its result.
	create func(ctx context.Context, name, baseRef string) (WorktreeResult, error)
	// switchByName runs the switch-by-name operation (spec §4 switch, by name).
	switchByName func(ctx context.Context, name string) (WorktreeSwitchResult, error)
	// switchByPath runs the switch-by-path operation (spec §4 switch, by path),
	// including the managed-directory reroute to switchByName's choreography.
	switchByPath func(ctx context.Context, path string) (WorktreeSwitchResult, error)
	// exitOp runs the exit operation (spec §4 exit): the operation-level
	// choreography (leave-unlock, restore, idempotent restore-relock) layered
	// on top of the exitWorktree env-restore primitive above.
	exitOp func(ctx context.Context) (WorktreeExitResult, error)
}

// registerWorktreeTool registers the manage_worktree lifecycle tool (spec §2)
// directly on the registry, mirroring registerGoalTools/registerTaskTools:
// registry-only, not part of any provider profile's own tool definitions.
// Register with ReadOnly unset (false) — even the list operation is part of a
// stateful lifecycle tool and must serialize with env-changing operations.
//
// Task 13 implements the create arm and the shared worktreeGuard plumbing; the
// remaining operations (list/switch/exit/remove/prune) land in Tasks 14-16 and
// currently return a clear "not yet implemented" error.
func registerWorktreeTool(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefManageWorktree()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env // the guard reads/writes the session env under s.mu; the passed env is currentEnv()
			operation, _ := args["operation"].(string)
			switch operation {
			case "create":
				name, _ := args["name"].(string)
				if strings.TrimSpace(name) == "" {
					return nil, errors.New("manage_worktree create: name is required")
				}
				baseRef, _ := args["base_ref"].(string)
				res, err := deps.worktreeGuard.create(ctx, name, baseRef)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"status":         "created",
					"path":           res.Path,
					"branch":         res.Branch,
					"base_sha":       res.BaseSHA,
					"main_repo_root": res.MainRoot,
					"message": fmt.Sprintf(
						"Created and entered worktree %q at %s (branch %s, base %s). Subsequent tools operate inside it; use manage_worktree exit to return to the main checkout.",
						name, res.Path, res.Branch, shortSHA(res.BaseSHA)),
				}, nil
			case "switch":
				name, hasName := args["name"].(string)
				path, hasPath := args["path"].(string)
				nameSet := hasName && strings.TrimSpace(name) != ""
				pathSet := hasPath && strings.TrimSpace(path) != ""
				if nameSet == pathSet {
					return nil, errors.New("manage_worktree switch: exactly one of name or path is required")
				}
				var (
					res WorktreeSwitchResult
					err error
				)
				if nameSet {
					res, err = deps.worktreeGuard.switchByName(ctx, name)
				} else {
					res, err = deps.worktreeGuard.switchByPath(ctx, path)
				}
				if err != nil {
					return nil, err
				}
				status, msg := "switched", fmt.Sprintf(
					"Entered worktree at %s (branch %s). Subsequent tools operate inside it; use manage_worktree exit to return.",
					res.Path, res.Branch)
				if res.NoOp {
					status, msg = "unchanged", fmt.Sprintf("Already in worktree at %s (branch %s); no-op.", res.Path, res.Branch)
				}
				return map[string]any{
					"status":  status,
					"path":    res.Path,
					"branch":  res.Branch,
					"message": msg,
				}, nil
			case "exit":
				res, err := deps.worktreeGuard.exitOp(ctx)
				if err != nil {
					return nil, err
				}
				msg := fmt.Sprintf("Exited worktree %s; restored to %s.", res.LeftPath, res.RestoredRoot)
				out := map[string]any{
					"status":        "exited",
					"restored_root": res.RestoredRoot,
					"left_path":     res.LeftPath,
				}
				if res.Warning != "" {
					msg += " Warning: " + res.Warning
					out["warning"] = res.Warning
				}
				out["message"] = msg
				return out, nil
			case "list", "remove", "prune":
				return nil, fmt.Errorf("manage_worktree %s: not yet implemented", operation)
			default:
				return nil, fmt.Errorf("manage_worktree: unknown operation %q", operation)
			}
		},
	})
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
// control env, rooted at the main repo root). Every arg is shell-escaped, so a
// worktree name or path can never inject shell syntax (spec §2). A non-zero
// exit is reported as a *gitCmdError carrying the code and stderr.
func gitRunner(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
	return func(args ...string) (string, error) {
		cmd := "git " + execenv.ShellEscapeArgs(args...)
		res, err := env.ExecCommand(ctx, cmd, worktreeGitTimeoutMS, "", nil)
		if res.ExitCode != 0 {
			return res.Stdout, &gitCmdError{code: res.ExitCode, args: args, stderr: strings.TrimSpace(res.Stderr)}
		}
		if err != nil {
			return res.Stdout, err
		}
		return res.Stdout, nil
	}
}

// worktreeControlEnv returns a local env rooted at mainRepoRoot for lifecycle
// git commands (spec §2 "Git control environment", §7 controlEnv()). The
// user-facing s.env may be rooted inside a worktree after create/switch, and
// ExecCommand rejects a workingDir outside its RootDir; WithWorkingDirectory
// re-roots unconditionally and shares PID/fs with the parent, so this control
// env can target the main root without disturbing the confined tool env. Errors
// when the session env is not a LocalExecutionEnvironment (spec §2: env
// swapping and local git worktree management are unsupported otherwise).
func (s *Session) worktreeControlEnv(mainRepoRoot string) (execenv.ExecutionEnvironment, error) {
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return nil, errors.New("manage_worktree requires a local execution environment")
	}
	return local.WithWorkingDirectory(mainRepoRoot), nil
}

// worktreeStateSnapshot implements worktreeGuard.state() (spec §7). It reads the
// mutable occupancy fields under s.mu, then resolves the main repo root and
// worktree root OUTSIDE the lock (ResolveMainRepoRoot may fork git; s.mu must
// never be held across a subprocess — spec §7).
func (s *Session) worktreeStateSnapshot() worktreeState {
	s.mu.Lock()
	local, _ := s.env.(*execenv.LocalExecutionEnvironment)
	restore := s.worktreeRestoreEnv
	current := s.worktreeCurrentPath
	stateDir := s.stateDir
	s.mu.Unlock()

	st := worktreeState{env: local, restoreEnv: restore, currentWorktree: current}
	if local != nil {
		st.mainRepoRoot = execenv.ResolveMainRepoRoot(local, local.WorkingDirectory())
		if st.mainRepoRoot != "" {
			// Canonicalize before deriving worktreeRoot/projectid, matching
			// worktreeCreate's canonicalMain — a symlinked state home or macOS
			// /var → /private/var must not split one repo's managed worktrees
			// across two different worktreeRoot/projectid values.
			if resolved, evErr := filepath.EvalSymlinks(st.mainRepoRoot); evErr == nil {
				st.mainRepoRoot = resolved
			}
			st.worktreeRoot = s.worktreeRootFor(local, stateDir, st.mainRepoRoot)
		}
	}
	return st
}

// worktreeRootFor derives the directory managed worktrees live under (spec §6
// "worktreeRoot derivation"): <stateDir>/worktrees when a runtime state dir
// launched the session, else the agent-owned project state dir for the main
// repo root plus /worktrees. It never imports cmdutil (spec §6).
func (s *Session) worktreeRootFor(env execenv.ExecutionEnvironment, stateDir, mainRepoRoot string) string {
	if stateDir != "" {
		return filepath.Join(stateDir, "worktrees")
	}
	return filepath.Join(RuntimeDir(gitOriginURL(env, mainRepoRoot), mainRepoRoot, ""), "worktrees")
}

// enterWorktree implements worktreeGuard.enterWorktree() (spec §7): swap s.env
// to WithWorkingDirectory(path), saving the prior env the first time (single
// saved env, not a stack — spec §7 "env-restore model") and recording the
// occupied managed worktree path. The swap always uses WithWorkingDirectory so
// PID/fs sharing survives (spec §7 "WithWorkingDirectory correctness"). The env
// swap + refresh runs outside s.mu (swapEnvAndRefresh forks git for the
// snapshot); manage_worktree is serialized in the tool stream, so nothing else
// swaps the env concurrently.
func (s *Session) enterWorktree(path string) {
	s.mu.Lock()
	local, ok := s.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		s.mu.Unlock()
		return
	}
	saveRestore := s.worktreeRestoreEnv == nil
	prior := local
	next := local.WithWorkingDirectory(path)
	s.mu.Unlock()

	s.swapEnvAndRefresh(next)

	s.mu.Lock()
	if saveRestore {
		s.worktreeRestoreEnv = prior
	}
	s.worktreeCurrentPath = path
	s.mu.Unlock()
}

// exitWorktree implements worktreeGuard.exitWorktree() (spec §7): restore the
// saved pre-worktree env and clear it so the next enter saves afresh. It
// returns ok=false when no restore env was saved (the session is not in a
// worktree entered via the tool). This is the env-restore primitive only; the
// operation-level `exit` semantics (own-marker unlock, restore-land idempotent
// lock rule) land with Task 14 and layer on top of this.
func (s *Session) exitWorktree() (string, bool) {
	s.mu.Lock()
	restore := s.worktreeRestoreEnv
	s.mu.Unlock()
	if restore == nil {
		return "", false
	}

	s.swapEnvAndRefresh(restore)

	s.mu.Lock()
	s.worktreeRestoreEnv = nil
	s.worktreeCurrentPath = ""
	root := restore.WorkingDirectory()
	s.mu.Unlock()
	return root, true
}

// liveWorkUnder implements worktreeGuard.liveWorkUnder() (spec §7): live
// child/delegate/shell working directories at or under path, for the remove and
// prune guards (spec §5 remove step 4). The full scan depends on the
// background-shell-job launch-workdir field spec §5 adds and on the
// delegate-restore working-dir cross-check; both land with the remove/prune
// operations (Tasks 15-16), which own this guard's guarded state. Task 13
// ships the shared method so create's guard plumbing and those tasks agree on
// the surface; with no remove/prune caller yet it reports no live work.
func (s *Session) liveWorkUnder(path string) []string {
	_ = path
	return nil
}

// worktreeCreate performs the create operation (spec §3, all nine steps in
// order). It resolves the main repo root, validates the name and base, writes
// the metadata sidecar BEFORE `git worktree add` (crash-safe ordering), creates
// and locks the worktree in one atomic command, leaves any worktree the session
// was already inside, and finally enters the new worktree.
func (s *Session) worktreeCreate(ctx context.Context, name, baseRef string) (WorktreeResult, error) {
	// Snapshot the active env once. Base resolution is against the ACTIVE root
	// (the worktree if the session is in one, else the original root), while
	// placement keys off the stable main repo root (spec §2).
	active, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return WorktreeResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	activeRoot := active.WorkingDirectory()

	// Step 1: resolve the main repo root through linked-worktree pointers.
	mainRoot := execenv.ResolveMainRepoRoot(active, activeRoot)
	if mainRoot == "" {
		return WorktreeResult{}, errors.New("manage_worktree create: not in a git repository")
	}

	// Control env rooted at the main repo root; every lifecycle git command runs
	// through it (spec §2 "Git control environment").
	controlEnv, err := s.worktreeControlEnv(mainRoot)
	if err != nil {
		return WorktreeResult{}, err
	}
	run := gitRunner(ctx, controlEnv)

	// Git version preflight (spec §3 step 6), memoized once per session.
	if err := s.ensureWorktreeGitVersion(run); err != nil {
		return WorktreeResult{}, err
	}

	// Step 2: projectid over the canonical main repo root.
	canonicalMain := mainRoot
	if resolved, evErr := filepath.EvalSymlinks(mainRoot); evErr == nil {
		canonicalMain = resolved
	}
	projectID := worktree.ProjectID(canonicalMain)

	// Step 3: worktree path under <worktreeRoot>/<projectid>/<name>.
	worktreeRoot := s.worktreeRootFor(active, s.currentStateDir(), canonicalMain)
	projectDir := filepath.Join(worktreeRoot, projectID)
	worktreePath := filepath.Join(projectDir, filepath.FromSlash(name))
	metaDir := filepath.Join(projectDir, ".meta")

	// Step 4: validate name (regex + git check-ref-format), resolve the base to
	// a SHA from the active root, and reject a pre-existing branch.
	if err := worktree.ValidateName(name); err != nil {
		return WorktreeResult{}, fmt.Errorf("manage_worktree create: %w", err)
	}
	if _, err := run("check-ref-format", "--branch", name); err != nil {
		return WorktreeResult{}, fmt.Errorf("manage_worktree create: %q is not a valid git branch name", name)
	}

	baseSHA, err := resolveBaseFromActiveRoot(run, activeRoot, baseRef)
	if err != nil {
		return WorktreeResult{}, err
	}

	if branchExists(run, name) {
		msg := fmt.Sprintf("manage_worktree create: branch %q already exists", name)
		if managedWorktreeExists(worktreePath) {
			msg += "; use manage_worktree switch to enter its worktree"
		}
		return WorktreeResult{}, errors.New(msg)
	}

	// merge_target is the branch checked out at the active root at creation
	// time; empty when the active root is on a detached HEAD (spec §6).
	mergeTarget := branchAtRoot(run, activeRoot)

	// Step 5: MkdirAll the sidecar parent and write the sidecar FIRST, O_EXCL.
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return WorktreeResult{}, fmt.Errorf("manage_worktree create: create metadata dir: %w", err)
	}
	sc := worktree.Sidecar{
		Name:           name,
		Branch:         name,
		BaseSHA:        baseSHA,
		MergeTarget:    mergeTarget,
		OriginalRoot:   canonicalMain,
		CreatorSession: s.id,
		CreatedAt:      s.sclock().Now().UTC().Format(time.RFC3339),
	}
	if err := worktree.WriteSidecarExcl(metaDir, name, sc); err != nil {
		if os.IsExist(err) {
			return WorktreeResult{}, fmt.Errorf("manage_worktree create: a worktree named %q is already being created", name)
		}
		return WorktreeResult{}, fmt.Errorf("manage_worktree create: write sidecar: %w", err)
	}

	// Step 6: MkdirAll the worktree parent (nested for slash names), then create
	// AND lock in one atomic command (spec §3 step 6; Decide gates the action).
	if worktree.Decide(worktree.EvCreate, worktree.Unlocked) != worktree.ActAtomicAddLock {
		_ = worktree.DeleteSidecar(metaDir, name)
		return WorktreeResult{}, errors.New("manage_worktree create: internal error: create is not an atomic-add-lock event")
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		_ = worktree.DeleteSidecar(metaDir, name)
		return WorktreeResult{}, fmt.Errorf("manage_worktree create: create worktree parent dir: %w", err)
	}
	marker := worktree.FormatSessionMarker(s.id)
	if _, err := run("worktree", "add", "--lock", "--reason", marker, "-b", name, "--", worktreePath, baseSHA); err != nil {
		// Step 6 crash-safety: a failed add (e.g. a refs/heads D/F conflict)
		// must delete the just-written sidecar in the same call, or the name
		// becomes uncreatable until a post-grace prune (spec §3 step 6).
		_ = worktree.DeleteSidecar(metaDir, name)
		return WorktreeResult{}, fmt.Errorf("manage_worktree create: git worktree add failed: %w", err)
	}

	// Step 7: creating a new worktree from inside a managed one is a LEAVE of
	// the old one — unlock it via Decide (spec §3 step 7; §5 EvLeave).
	if err := s.leaveCurrentWorktree(run); err != nil {
		return WorktreeResult{}, err
	}

	// Step 8: enter the new worktree (env swap + refresh, saving the prior env).
	s.enterWorktree(worktreePath)

	// Step 9: report the path, branch, base SHA, and main repo root.
	return WorktreeResult{
		Path:     worktreePath,
		Branch:   name,
		BaseSHA:  baseSHA,
		MainRoot: canonicalMain,
	}, nil
}

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
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, "")
	}
	if worktree.Decide(worktree.EvLeave, st) == worktree.ActUnlock {
		if _, err := run("worktree", "unlock", oldPath); err != nil {
			return fmt.Errorf("manage_worktree: unlocking the current worktree: %w", err)
		}
	}
	return nil
}

// lockStateOf reports whether the worktree at path is locked and, if so, its
// (C-unquoted) lock reason, by parsing `git worktree list --porcelain` from the
// control env. A path git does not list is reported unlocked (not an error) —
// the caller decides what an unknown path means.
func lockStateOf(run worktree.GitRunner, path string) (locked bool, reason string, err error) {
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return false, "", err
	}
	target := filepath.Clean(path)
	for _, e := range worktree.ParsePorcelain(out) {
		if filepath.Clean(e.Path) == target {
			return e.Locked, e.LockReason, nil
		}
	}
	return false, "", nil
}

// worktreeControlRun builds a GitRunner over the control env rooted at
// mainRepoRoot (spec §2 "Git control environment"), for lifecycle git calls
// that must run outside the (possibly worktree-rooted) user-facing env.
func (s *Session) worktreeControlRun(ctx context.Context, mainRepoRoot string) (worktree.GitRunner, error) {
	controlEnv, err := s.worktreeControlEnv(mainRepoRoot)
	if err != nil {
		return nil, err
	}
	return gitRunner(ctx, controlEnv), nil
}

// isUnderManagedDir reports whether canonicalPath lives strictly under the
// managed worktree directory projectDir (spec §4 switch by-path step 2; spec
// §5 `list` step 2's "not bare HasPrefix" rule — a bare string-prefix compare
// collides when one projectid string prefixes another). projectDir is
// canonicalized here too (spec §5: "canonicalize both sides"); a projectDir
// that does not exist yet canonicalizes to itself, under which nothing can
// resolve, so isUnderManagedDir correctly reports false.
func isUnderManagedDir(canonicalPath, projectDir string) bool {
	canonicalProjectDir := filepath.Clean(projectDir)
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		canonicalProjectDir = filepath.Clean(resolved)
	}
	rel, err := filepath.Rel(canonicalProjectDir, canonicalPath)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// worktreeEnterManaged performs the by-name switch choreography (spec §4
// switch by-name steps 1-6) against an already-resolved managed worktree
// path. switchByPath's managed-directory reroute (step 2) shares it verbatim:
// once a by-path argument resolves inside the managed directory, entering it
// is indistinguishable from a by-name switch.
func (s *Session) worktreeEnterManaged(st worktreeState, run worktree.GitRunner, path string) (WorktreeSwitchResult, error) {
	target := filepath.Clean(path)

	// Step 1: switch-to-current is a no-op — the lock stays exactly as it is.
	// Without this, the ordinary lock-target/unlock-old choreography below
	// would unlock the session's own active worktree out from under it (spec
	// §4 switch step 1).
	if st.currentWorktree != "" && filepath.Clean(st.currentWorktree) == target {
		return WorktreeSwitchResult{Path: target, Branch: branchAtRoot(run, target), NoOp: true}, nil
	}

	// Step 2: the target must exist and be a real worktree (.git pointer file).
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: no worktree at %s", target)
	}
	locked, reason, err := lockStateOf(run, target)
	if err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: inspecting the target lock: %w", err)
	}
	targetState := worktree.Unlocked
	if locked {
		targetState = worktree.ClassifyReason(reason, s.id, "")
	}

	// Step 3: lock the target FIRST, then unlock the current worktree. Order
	// is load-bearing (spec §4 switch step 3): a lost race fails right here,
	// with nothing yet changed, instead of leaving the session unlocked out of
	// its old worktree.
	switch worktree.Decide(worktree.EvEnter, targetState) {
	case worktree.ActLock:
		marker := worktree.FormatSessionMarker(s.id)
		if _, err := run("worktree", "lock", "--reason", marker, target); err != nil {
			return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: locking the target: %w", err)
		}
	case worktree.ActAdopt:
		// Already carries our own marker (crash-resume case); nothing to do.
	default:
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: %s is locked (%s)", target, reason)
	}
	if err := s.leaveCurrentWorktree(run); err != nil {
		return WorktreeSwitchResult{}, err
	}

	// Steps 4-5: swap the env directly to the target (no intermediate
	// restore step) and refresh envInfo + the prompt cache.
	s.enterWorktree(target)

	// Step 6: report the path and branch.
	return WorktreeSwitchResult{Path: target, Branch: branchAtRoot(run, target)}, nil
}

// worktreeSwitchByName performs the switch-by-name operation (spec §4 switch,
// "By name"): resolve the managed path for name and run the shared entry
// choreography.
func (s *Session) worktreeSwitchByName(ctx context.Context, name string) (WorktreeSwitchResult, error) {
	if err := worktree.ValidateName(name); err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: %w", err)
	}
	st := s.worktreeStateSnapshot()
	if st.env == nil {
		return WorktreeSwitchResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreeSwitchResult{}, errors.New("manage_worktree switch: not in a git repository")
	}
	run, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreeSwitchResult{}, err
	}
	path := filepath.Join(st.worktreeRoot, worktree.ProjectID(st.mainRepoRoot), filepath.FromSlash(name))
	return s.worktreeEnterManaged(st, run, path)
}

// worktreeSwitchByPath performs the switch-by-path operation (spec §4 switch,
// "By path"): canonicalize rawPath, require it to be registered to this
// repository, reroute through the managed choreography if it resolves inside
// the managed directory, and otherwise swap the env with no lock mutation at
// all (serf does not manage locks on worktrees it did not create).
func (s *Session) worktreeSwitchByPath(ctx context.Context, rawPath string) (WorktreeSwitchResult, error) {
	st := s.worktreeStateSnapshot()
	if st.env == nil {
		return WorktreeSwitchResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreeSwitchResult{}, errors.New("manage_worktree switch: not in a git repository")
	}

	// Step 1: canonicalize the argument and require a match in `git worktree
	// list --porcelain` — git's own registry is the validator.
	canonicalArg, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: path %q does not exist", rawPath)
	}
	canonicalArg = filepath.Clean(canonicalArg)

	run, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreeSwitchResult{}, err
	}
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: listing worktrees: %w", err)
	}
	var matchedPath, matchedBranch string
	found := false
	for _, e := range worktree.ParsePorcelain(out) {
		cp, evErr := filepath.EvalSymlinks(e.Path)
		if evErr != nil {
			continue // this entry's directory is momentarily absent; not a match we can canonicalize
		}
		if filepath.Clean(cp) == canonicalArg {
			matchedPath, matchedBranch, found = e.Path, strings.TrimPrefix(e.Branch, "refs/heads/"), true
			break
		}
	}
	if !found {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: %q is not a worktree registered to this repository", rawPath)
	}

	// Step 2: reroute through the by-name choreography when the target
	// resolves inside the managed directory (full lock guard + choreography).
	projectDir := filepath.Join(st.worktreeRoot, worktree.ProjectID(st.mainRepoRoot))
	if isUnderManagedDir(canonicalArg, projectDir) {
		return s.worktreeEnterManaged(st, run, matchedPath)
	}

	// Step 3: a genuinely non-managed registered worktree — same env swap, NO
	// lock choreography on the target. If the session is leaving a managed
	// worktree, it is still unlocked on the way out (spec §4 switch by-path
	// step 3).
	if st.currentWorktree != "" && filepath.Clean(st.currentWorktree) == filepath.Clean(matchedPath) {
		return WorktreeSwitchResult{Path: matchedPath, Branch: matchedBranch, NoOp: true}, nil
	}
	if err := s.leaveCurrentWorktree(run); err != nil {
		return WorktreeSwitchResult{}, err
	}
	s.enterWorktree(matchedPath)
	return WorktreeSwitchResult{Path: matchedPath, Branch: matchedBranch}, nil
}

// relockRestoreTarget applies the idempotent EvRestoreLand lock rule to a
// restore target that resolves inside the managed directory (spec §4 exit
// step 4; §5 "Restores follow the same rule"): lock if unlocked, adopt our
// own marker as a no-op, or warn and co-occupy on a foreign lock — a restore
// can never be refused, since the session has to land somewhere. Returns a
// non-empty warning string only for the co-occupy case.
func (s *Session) relockRestoreTarget(run worktree.GitRunner, path string) (string, error) {
	locked, reason, err := lockStateOf(run, path)
	if err != nil {
		return "", fmt.Errorf("manage_worktree exit: inspecting the restore target lock: %w", err)
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, "")
	}
	switch worktree.Decide(worktree.EvRestoreLand, st) {
	case worktree.ActLock:
		marker := worktree.FormatSessionMarker(s.id)
		if _, err := run("worktree", "lock", "--reason", marker, path); err != nil {
			return "", fmt.Errorf("manage_worktree exit: locking the restore target: %w", err)
		}
		return "", nil
	case worktree.ActAdopt:
		return "", nil
	case worktree.ActWarnCoOccupy:
		return fmt.Sprintf("restore target %s is locked by another owner (%s); continuing and co-occupying it", path, reason), nil
	default:
		return "", fmt.Errorf("manage_worktree exit: internal error: unexpected restore-relock action for %s", path)
	}
}

// worktreeExit performs the exit operation (spec §4 "exit", all six steps).
func (s *Session) worktreeExit(ctx context.Context) (WorktreeExitResult, error) {
	st := s.worktreeStateSnapshot()

	// Step 1: not in a worktree entered via this tool → clear, non-destructive
	// error, no side effects.
	if st.restoreEnv == nil {
		return WorktreeExitResult{}, errors.New("manage_worktree exit: not in a worktree")
	}
	leftPath := st.currentWorktree

	var run worktree.GitRunner
	if st.mainRepoRoot != "" {
		r, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
		if err != nil {
			return WorktreeExitResult{}, err
		}
		run = r
		// Step 2: unlock the worktree being left, if managed and locked with
		// this session's own marker (same EvLeave rule as switch-away /
		// create-away).
		if err := s.leaveCurrentWorktree(run); err != nil {
			return WorktreeExitResult{}, err
		}
	}

	// Step 3: restore the saved env, clear it, recompute envInfo, refresh the
	// prompt cache (spec §7 exitWorktree()).
	restoredRoot, ok := s.exitWorktree()
	if !ok {
		return WorktreeExitResult{}, errors.New("manage_worktree exit: not in a worktree")
	}
	result := WorktreeExitResult{RestoredRoot: restoredRoot, LeftPath: leftPath}

	// Step 4: if the restore root is itself a managed worktree (the session
	// was launched inside one before entering others), apply the idempotent
	// lock rule to it.
	if run != nil {
		canonicalRestored := filepath.Clean(restoredRoot)
		if resolved, evErr := filepath.EvalSymlinks(restoredRoot); evErr == nil {
			canonicalRestored = filepath.Clean(resolved)
		}
		projectDir := filepath.Join(st.worktreeRoot, worktree.ProjectID(st.mainRepoRoot))
		if isUnderManagedDir(canonicalRestored, projectDir) {
			warning, err := s.relockRestoreTarget(run, restoredRoot)
			if err != nil {
				return WorktreeExitResult{}, err
			}
			result.Warning = warning
			// The session now occupies this managed worktree again; record it
			// so a later create/switch/close leaves it correctly (spec §3
			// step 7; §5 clean-close unlock).
			s.mu.Lock()
			s.worktreeCurrentPath = restoredRoot
			s.mu.Unlock()
		}
	}

	return result, nil
}

// resolveBaseFromActiveRoot resolves baseRef to an explicit commit SHA against
// activeRoot (spec §2 "Base resolution"). An absent base defaults to HEAD.
// baseRef is trimmed and rejected for whitespace, control characters, or a
// leading '-' before git sees it, so an option-like value can never be
// interpreted as a flag.
func resolveBaseFromActiveRoot(run worktree.GitRunner, activeRoot, baseRef string) (string, error) {
	ref := strings.TrimSpace(baseRef)
	if ref == "" {
		ref = "HEAD"
	}
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("manage_worktree create: base_ref %q must not start with %q", ref, "-")
	}
	if strings.ContainsFunc(ref, func(r rune) bool { return r <= ' ' }) {
		return "", fmt.Errorf("manage_worktree create: base_ref %q must not contain whitespace or control characters", ref)
	}
	out, err := run("-C", activeRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	sha := strings.TrimSpace(out)
	if err != nil || sha == "" {
		return "", fmt.Errorf("manage_worktree create: base_ref %q cannot be resolved to a commit from %s", ref, activeRoot)
	}
	return sha, nil
}

// branchExists reports whether refs/heads/<name> already exists.
func branchExists(run worktree.GitRunner, name string) bool {
	_, err := run("show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// managedWorktreeExists reports whether a managed worktree already lives at
// worktreePath (a directory carrying a .git pointer), so create only suggests
// switch when switch would actually succeed (spec §3 step 4).
func managedWorktreeExists(worktreePath string) bool {
	_, err := os.Stat(filepath.Join(worktreePath, ".git"))
	return err == nil
}

// branchAtRoot returns the branch checked out at root, or "" when root is on a
// detached HEAD (spec §6 merge_target).
func branchAtRoot(run worktree.GitRunner, root string) string {
	out, err := run("-C", root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
