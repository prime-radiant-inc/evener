package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// WorktreeRemoveResult is what a successful remove returns (spec §5 remove
// step 11): the removed path and branch name, whether the branch was
// deleted, and why it was kept when deletion was requested but refused
// (BranchKeptReason, empty when deletion was not requested or it succeeded).
// The worktree removal itself (spec §5 remove step 8) is the primary action
// and always either succeeds or the whole call errors — a delete_branch
// refusal (step 9) is reported here rather than as an error, since step 11
// requires reporting "whether the branch was deleted" as part of a
// confirmation, not failing the call after the worktree is already gone.
// Warning mirrors WorktreeExitResult's: set only when remove-current's
// restore landed in a foreign-locked managed worktree (spec §5 "Restores
// follow the same rule").
type WorktreeRemoveResult struct {
	Path             string
	Branch           string
	BranchDeleted    bool
	BranchKeptReason string
	Warning          string
}

// WorktreeListEntry is one worktree in the manage_worktree list result (spec
// §5 list step 3): path, branch, current occupancy, lock state, prunable
// annotation, and disposal-relevant staleness state pulled from the metadata
// sidecar plus cheap git queries. HasMetadata is false for a sidecar-less
// worktree; the metadata-derived fields (CreatorSession, DelegateID,
// CreatedAt, AgeSeconds, BaseSHA, MergeTarget, AheadCommits, Merged,
// MergedArm, MergeTargetUnknown) are then zero values rather than an error —
// list surfaces provenance-unknown entries instead of refusing them.
type WorktreeListEntry struct {
	Name           string
	Path           string
	Branch         string
	Current        bool
	Locked         bool
	LockReason     string
	Prunable       bool
	PrunableReason string

	HasMetadata    bool
	CreatorSession string
	DelegateID     string
	CreatedAt      string
	AgeSeconds     float64

	Dirty              bool
	BaseSHA            string
	AheadCommits       int
	MergeTarget        string
	Merged             bool
	MergedArm          string
	MergeTargetUnknown bool
}

// WorktreePruneEntry is one worktree or sidecar prune touched or decided to
// leave alone (spec §5 prune's report shape: "Report removed and skipped
// entries with per-entry reasons"). Path is empty for a sweep-2 entry whose
// worktree directory is already gone — only its sidecar and/or branch remain.
// An entry with none of the Removed flags set belongs in the tool dispatch's
// "skipped" bucket; any flag set belongs in "removed" (adopted's
// sidecar-only removal is a "removed" entry with BranchRemoved false).
type WorktreePruneEntry struct {
	Name            string
	Path            string
	WorktreeRemoved bool
	BranchRemoved   bool
	SidecarRemoved  bool
	Reason          string
}

// WorktreePruneResult is the outcome of the prune operation (spec §5 prune,
// all three sweeps): every entry prune removed something from, every entry it
// left alone with a reason, and whether the repo-wide git registry hygiene
// sweep (sweep 3) ran.
type WorktreePruneResult struct {
	Removed            []WorktreePruneEntry
	Skipped            []WorktreePruneEntry
	RegistryPruned     bool
	RegistrySkipReason string
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
	// first time (spec §7 enterWorktree()). managed records whether path is a
	// serf-managed worktree, persisted via SessionMeta.WorktreeManaged (spec §7
	// "Persistence and resume").
	enterWorktree func(path string, managed bool)
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
	// removeOp runs the remove operation (spec §5 remove, all eleven steps).
	removeOp func(ctx context.Context, name string, force, deleteBranch bool) (WorktreeRemoveResult, error)
	// listOp runs the list operation (spec §5 list, all three steps).
	listOp func(ctx context.Context) ([]WorktreeListEntry, error)
	// pruneOp runs the prune operation (spec §5 prune, all three sweeps).
	pruneOp func(ctx context.Context) (WorktreePruneResult, error)
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
			case "remove":
				name, _ := args["name"].(string)
				if strings.TrimSpace(name) == "" {
					return nil, errors.New("manage_worktree remove: name is required")
				}
				force, _ := args["force"].(bool)
				deleteBranch, _ := args["delete_branch"].(bool)
				res, err := deps.worktreeGuard.removeOp(ctx, name, force, deleteBranch)
				if err != nil {
					return nil, err
				}
				msg := fmt.Sprintf("Removed worktree %q at %s.", name, res.Path)
				if deleteBranch {
					if res.BranchDeleted {
						msg += fmt.Sprintf(" Branch %q deleted.", res.Branch)
					} else {
						msg += fmt.Sprintf(" Branch %q NOT deleted: %s", res.Branch, res.BranchKeptReason)
					}
				}
				if res.Warning != "" {
					msg += " Warning: " + res.Warning
				}
				out := map[string]any{
					"status":         "removed",
					"path":           res.Path,
					"branch":         res.Branch,
					"branch_deleted": res.BranchDeleted,
					"message":        msg,
				}
				if res.BranchKeptReason != "" {
					out["branch_kept_reason"] = res.BranchKeptReason
				}
				if res.Warning != "" {
					out["warning"] = res.Warning
				}
				return out, nil
			case "list":
				entries, err := deps.worktreeGuard.listOp(ctx)
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, len(entries))
				for i, e := range entries {
					out[i] = worktreeListEntryToMap(e)
				}
				return map[string]any{
					"status":  "listed",
					"entries": out,
					"message": fmt.Sprintf("%d managed worktree(s).", len(entries)),
				}, nil
			case "prune":
				res, err := deps.worktreeGuard.pruneOp(ctx)
				if err != nil {
					return nil, err
				}
				removed := make([]map[string]any, len(res.Removed))
				for i, e := range res.Removed {
					removed[i] = worktreePruneEntryToMap(e)
				}
				skipped := make([]map[string]any, len(res.Skipped))
				for i, e := range res.Skipped {
					skipped[i] = worktreePruneEntryToMap(e)
				}
				msg := fmt.Sprintf("Removed %d, skipped %d.", len(removed), len(skipped))
				if res.RegistryPruned {
					msg += " Git worktree registry pruned."
				} else if res.RegistrySkipReason != "" {
					msg += " Registry hygiene sweep skipped: " + res.RegistrySkipReason
				}
				return map[string]any{
					"status":               "pruned",
					"removed":              removed,
					"skipped":              skipped,
					"registry_pruned":      res.RegistryPruned,
					"registry_skip_reason": res.RegistrySkipReason,
					"message":              msg,
				}, nil
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
// occupied worktree path and whether it is serf-managed. The swap always uses
// WithWorkingDirectory so PID/fs sharing survives (spec §7 "WithWorkingDirectory
// correctness"). The env swap + refresh runs outside s.mu (swapEnvAndRefresh
// forks git for the snapshot); manage_worktree is serialized in the tool
// stream, so nothing else swaps the env concurrently.
func (s *Session) enterWorktree(path string, managed bool) {
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
	s.worktreeCurrentManaged = managed
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
	s.worktreeCurrentManaged = false
	root := restore.WorkingDirectory()
	s.mu.Unlock()
	return root, true
}

// liveWorkUnder implements worktreeGuard.liveWorkUnder() (spec §7): live
// child/delegate/shell working directories at or under path, for the remove
// and prune guards (spec §5 remove step 4). It scans three sources: running
// background shell jobs (JobRecord.WorkingDir) and running delegate jobs
// (DelegateRestore.WorkingDir) via the job manager, plus every live subagent
// session's current env — the last of which also covers delegates (delegates
// are subagents with a job record layered on top) and, unlike a job record's
// launch-time snapshot, tracks a child that has since switched worktrees
// itself. It is best-effort and read-only: a shell command that `cd`s after
// launch is invisible to it (spec §5 remove step 4). worktreeLiveWorkStub, a
// test-only seam, takes precedence when set (see its doc comment on the
// Session struct) so unit tests can exercise the guard call without spinning
// up real jobs.
func (s *Session) liveWorkUnder(path string) []string {
	s.mu.Lock()
	stub := s.worktreeLiveWorkStub
	s.mu.Unlock()
	if stub != nil {
		return stub(path)
	}

	target := canonicalOrClean(path)
	var live []string

	if s.jobManager != nil {
		for _, h := range s.jobManager.liveWorkHandles() {
			if pathEqualOrUnder(canonicalOrClean(h.dir), target) {
				live = append(live, h.handle)
			}
		}
	}

	if s.subagents != nil {
		for _, child := range s.subagents.sessions() {
			env := child.currentEnv()
			if env == nil {
				continue
			}
			wd := env.WorkingDirectory()
			if wd == "" {
				continue
			}
			if pathEqualOrUnder(canonicalOrClean(wd), target) {
				live = append(live, child.id+" (subagent, running)")
			}
		}
	}

	return live
}

// pathEqualOrUnder reports whether candidate is target itself or nested under
// it, both already canonicalized by the caller (canonicalOrClean). This is
// liveWorkUnder's "equal to path or under it" (spec §7) — the one case
// relPathUnderManagedDir's "strictly under" deliberately excludes, since a
// child rooted exactly at the worktree being removed must also refuse.
func pathEqualOrUnder(candidate, target string) bool {
	if candidate == target {
		return true
	}
	rel, err := filepath.Rel(target, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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

	// Step 4: validate name (regex, pure — spec §8: "name fails validation ->
	// error before any git call") BEFORE the git version preflight (spec §3
	// step 6, memoized once per session) and check-ref-format, so an invalid
	// name never reaches a git subprocess; resolve the base to a SHA from the
	// active root, and reject a pre-existing branch.
	if err := worktree.ValidateName(name); err != nil {
		return WorktreeResult{}, fmt.Errorf("manage_worktree create: %w", err)
	}
	if err := s.ensureWorktreeGitVersion(run); err != nil {
		return WorktreeResult{}, err
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
	s.enterWorktree(worktreePath, true)

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

// relPathUnderManagedDir canonicalizes projectDir (spec §5: "canonicalize
// both sides") and, if canonicalPath lives strictly under it, returns its
// path relative to projectDir. ok is false for canonicalPath == projectDir
// itself, anything outside it, or an unresolvable relation — a projectDir
// that does not exist yet canonicalizes to itself, under which nothing can
// resolve, so this correctly reports ok=false rather than panicking or
// guessing. Shared by isUnderManagedDir (spec §4 switch by-path step 2) and
// the list/prune managed-entry enumeration (spec §5 list step 2's "not bare
// HasPrefix" rule — a bare string-prefix compare collides when one projectid
// string prefixes another).
func relPathUnderManagedDir(canonicalPath, projectDir string) (rel string, ok bool) {
	canonicalProjectDir := filepath.Clean(projectDir)
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		canonicalProjectDir = filepath.Clean(resolved)
	}
	r, err := filepath.Rel(canonicalProjectDir, canonicalPath)
	if err != nil {
		return "", false
	}
	if r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
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

// switchToCurrentNoOp implements the EvEnterCurrent row of the lock state
// machine (spec §4 switch step 1; §5 table "switch to the worktree already
// occupied"; lockstate.go EvEnterCurrent) for a switch whose target is the
// worktree the session already occupies. Only the own-session occupancy
// state may no-op with the lock kept; every other observed state — including
// unlocked or foreign-locked, reachable only if something outside this
// session's own choreography changed the lock between occupying the target
// and this switch call — is refused rather than silently treated as a no-op.
// Decide is total and fails safe here exactly as it does everywhere else in
// the lock state machine.
func (s *Session) switchToCurrentNoOp(run worktree.GitRunner, target string) (WorktreeSwitchResult, error) {
	locked, reason, err := lockStateOf(run, target)
	if err != nil {
		return WorktreeSwitchResult{}, fmt.Errorf("manage_worktree switch: inspecting the current worktree lock: %w", err)
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, "")
	}
	if worktree.Decide(worktree.EvEnterCurrent, st) != worktree.ActNone {
		detail := reason
		if detail == "" {
			detail = "unlocked"
		}
		return WorktreeSwitchResult{}, fmt.Errorf(
			"manage_worktree switch: %s is the worktree already occupied, but it is not locked with this session's own marker (%s); refusing",
			target, detail)
	}
	return WorktreeSwitchResult{Path: target, Branch: branchAtRoot(run, target), NoOp: true}, nil
}

// worktreeEnterManaged performs the by-name switch choreography (spec §4
// switch by-name steps 1-6) against an already-resolved managed worktree
// path. switchByPath's managed-directory reroute (step 2) shares it verbatim:
// once a by-path argument resolves inside the managed directory, entering it
// is indistinguishable from a by-name switch.
func (s *Session) worktreeEnterManaged(st worktreeState, run worktree.GitRunner, path string) (WorktreeSwitchResult, error) {
	target := filepath.Clean(path)

	// Step 1: switch-to-current routes through Decide(EvEnterCurrent, ...) —
	// only an ActNone (own-session marker) may no-op with the lock kept; any
	// other observed state is refused (spec §4 switch step 1; lockstate.go
	// EvEnterCurrent). Without the ActNone no-op, the ordinary
	// lock-target/unlock-old choreography below would unlock the session's
	// own active worktree out from under it.
	if st.currentWorktree != "" && filepath.Clean(st.currentWorktree) == target {
		return s.switchToCurrentNoOp(run, target)
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
	s.enterWorktree(target, true)

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
	// lock choreography on the target (spec §4 by-path step 3: "no lock
	// choreography — serf does not mutate lock state on worktrees it does
	// not manage"). There is no lock decision on this site at all, so a
	// redundant switch back to the worktree already occupied is a plain
	// path-compare no-op rather than a run through Decide(EvEnterCurrent,
	// ...) — that gate exists to protect the managed case's own-session
	// lock, which has no counterpart here. If the session is leaving a
	// managed worktree, it is still unlocked on the way out below.
	if st.currentWorktree != "" && filepath.Clean(st.currentWorktree) == filepath.Clean(matchedPath) {
		return WorktreeSwitchResult{Path: matchedPath, Branch: matchedBranch, NoOp: true}, nil
	}
	if err := s.leaveCurrentWorktree(run); err != nil {
		return WorktreeSwitchResult{}, err
	}
	s.enterWorktree(matchedPath, false)
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
		projectDir := filepath.Join(st.worktreeRoot, worktree.ProjectID(st.mainRepoRoot))
		warning, err := s.applyRestoreLandRelock(run, restoredRoot, projectDir)
		if err != nil {
			return WorktreeExitResult{}, err
		}
		result.Warning = warning
	}

	return result, nil
}

// applyRestoreLandRelock applies the idempotent EvRestoreLand lock rule when
// restoredRoot resolves inside the managed directory rooted at projectDir
// (spec §5 "Restores follow the same rule"): exit's restore root (§4 exit
// step 4) and remove-current's restore (§5 remove step 7) share this exact
// choreography — lock if unlocked, adopt our own marker as a no-op, or warn
// and co-occupy on a foreign lock. It is a no-op (empty warning, nil error)
// when restoredRoot is not under the managed directory. When the rule fires,
// it also records the session's occupancy (worktreeCurrentPath) so a later
// create/switch/close leaves it correctly (spec §3 step 7; §5 clean-close
// unlock).
func (s *Session) applyRestoreLandRelock(run worktree.GitRunner, restoredRoot, projectDir string) (string, error) {
	canonicalRestored := filepath.Clean(restoredRoot)
	if resolved, evErr := filepath.EvalSymlinks(restoredRoot); evErr == nil {
		canonicalRestored = filepath.Clean(resolved)
	}
	if !isUnderManagedDir(canonicalRestored, projectDir) {
		return "", nil
	}
	warning, err := s.relockRestoreTarget(run, restoredRoot)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.worktreeCurrentPath = restoredRoot
	s.worktreeCurrentManaged = true
	s.mu.Unlock()
	return warning, nil
}

// worktreeRemove performs the remove operation (spec §5 remove, all eleven
// steps in order). Removing the worktree directory (step 8) is the primary,
// always-or-error action; a delete_branch refusal (step 9) is reported in the
// result rather than failing the call, since by then the worktree is already
// gone and step 11 requires reporting whether the branch was deleted as part
// of a confirmation.
func (s *Session) worktreeRemove(ctx context.Context, name string, force, deleteBranch bool) (WorktreeRemoveResult, error) {
	// Step 1: resolve the worktree path from name.
	if err := worktree.ValidateName(name); err != nil {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %w", err)
	}
	st := s.worktreeStateSnapshot()
	if st.env == nil {
		return WorktreeRemoveResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreeRemoveResult{}, errors.New("manage_worktree remove: not in a git repository")
	}
	projectDir := filepath.Join(st.worktreeRoot, worktree.ProjectID(st.mainRepoRoot))
	metaDir := filepath.Join(projectDir, ".meta")
	target := filepath.Clean(filepath.Join(projectDir, filepath.FromSlash(name)))

	// Step 2: the target must be under <worktreeRoot>/<projectid>/, canonicalized
	// as in `list` (spec §5 `list` step 2) — never remove an arbitrary path by
	// name. Structurally guaranteed already (ValidateName rejects ".."; target
	// is built by joining under projectDir), but checked explicitly for
	// defense in depth and for symlinked state homes.
	canonicalTarget := target
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		canonicalTarget = filepath.Clean(resolved)
	}
	if !isUnderManagedDir(canonicalTarget, projectDir) {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s is not a managed worktree", name)
	}

	run, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreeRemoveResult{}, err
	}

	// "Currently inside" is judged against the session's ACTIVE root, not the
	// create/switch-tracked worktreeCurrentPath: a session that launched
	// directly inside a managed worktree (never entering it via create/switch)
	// has an empty worktreeCurrentPath even though its env is rooted there,
	// and step 7's "no safe restore env" refusal exists precisely for that
	// case (spec §5 remove step 7's own example).
	activeRoot := filepath.Clean(st.env.WorkingDirectory())
	currentlyInside := activeRoot == target

	// Step 3: lock guard. A foreign lock refuses regardless of force; this
	// session's own marker on a worktree it is not currently in is crash
	// residue — unlocked here and the operation proceeds (spec §5 remove step
	// 3). When the session IS currently inside the target, its own marker is
	// ordinary occupancy and the unlock (if any) happens at the restore, step
	// 7 below — not here (EvRemoveCurrent's own-marker row: "unlock at the
	// restore step").
	locked, reason, err := lockStateOf(run, target)
	if err != nil {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: inspecting the target lock: %w", err)
	}
	lockSt := worktree.Unlocked
	if locked {
		lockSt = worktree.ClassifyReason(reason, s.id, "")
	}
	lockDetail := reason
	if lockDetail == "" {
		lockDetail = "no reason"
	}
	var unlockAtRestore bool
	if currentlyInside {
		switch worktree.Decide(worktree.EvRemoveCurrent, lockSt) {
		case worktree.ActNone:
			// unlocked; nothing to do here
		case worktree.ActUnlock:
			unlockAtRestore = true
		default:
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s is locked (%s); refusing", target, lockDetail)
		}
	} else {
		switch worktree.Decide(worktree.EvRemoveTarget, lockSt) {
		case worktree.ActNone:
			// unlocked; nothing to do here
		case worktree.ActUnlockProceed:
			if _, err := run("worktree", "unlock", target); err != nil {
				return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: unlocking crash-residue lock: %w", err)
			}
		default:
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s is locked (%s); refusing (force does not override a lock)", target, lockDetail)
		}
	}

	// Step 4: live child/job guard (spec §5 remove step 4, "widened"). Best
	// effort: liveWorkUnder scans running shell/delegate jobs and live
	// subagent envs (see its doc comment); a shell command that `cd`s after
	// launch is invisible to it.
	if live := s.liveWorkUnder(target); len(live) > 0 {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: live work under %s: %s", target, strings.Join(live, ", "))
	}

	// Step 5: cross-session ownership guard. A worktree with no sidecar has
	// unknown provenance and is treated as another session's (spec §6
	// "Metadata sidecar": "treated by remove as another session's — refuse
	// without force").
	sc, scErr := worktree.ReadSidecar(metaDir, name)
	hasSidecar := scErr == nil
	if scErr != nil && !os.IsNotExist(scErr) {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: reading metadata: %w", scErr)
	}
	if !force {
		if !hasSidecar {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s has no metadata sidecar (unmanaged provenance); refusing without force", target)
		}
		if sc.CreatorSession != s.id {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s was created by a different session (%s); refusing without force", target, sc.CreatorSession)
		}
	}

	// Step 6: dirtiness preflight (force:false only). Nothing above has
	// mutated s.env or removed anything yet, so a refusal here leaves s.env
	// unchanged (spec §5 remove step 6).
	if !force {
		clean, offending, err := worktree.CleanTree(run, target)
		if err != nil {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: checking for uncommitted changes: %w", err)
		}
		if !clean {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: %s has uncommitted changes (use force to remove anyway):\n%s", target, strings.Join(offending, "\n"))
		}
	}

	// Step 7: if the session is currently in this worktree, unlock it (if
	// step 3 deferred an unlock) and restore s.env via worktreeGuard, applying
	// the idempotent restore-land lock rule to the landing spot when it is
	// itself a managed worktree (spec §5 "Restores follow the same rule";
	// remove-current's restore reuses exit's applyRestoreLandRelock
	// choreography verbatim). No safe restore env — for example, the session
	// started directly inside the worktree being removed — refuses rather
	// than deleting the active root out from under the session.
	var removeWarning string
	if currentlyInside {
		if st.restoreEnv == nil {
			return WorktreeRemoveResult{}, errors.New("manage_worktree remove: no safe restore env for the currently-occupied worktree (it was never entered via manage_worktree); refusing to remove the active root")
		}
		if unlockAtRestore {
			if _, err := run("worktree", "unlock", target); err != nil {
				return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: unlocking before restore: %w", err)
			}
		}
		restoredRoot, ok := s.exitWorktree()
		if !ok {
			return WorktreeRemoveResult{}, errors.New("manage_worktree remove: not in a worktree")
		}
		warning, err := s.applyRestoreLandRelock(run, restoredRoot, projectDir)
		if err != nil {
			return WorktreeRemoveResult{}, err
		}
		removeWarning = warning
	}

	// Step 8: remove the worktree itself. --force is included only when
	// force:true, and only ever covers git's own dirty/untracked refusal —
	// never locks, which step 3 already resolved (spec §5 remove step 8).
	rmArgs := []string{"worktree", "remove"}
	if force {
		rmArgs = append(rmArgs, "--force")
	}
	rmArgs = append(rmArgs, "--", target)
	if _, err := run(rmArgs...); err != nil {
		return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: git worktree remove failed: %w", err)
	}

	result := WorktreeRemoveResult{Path: target, Branch: name, Warning: removeWarning}

	// Step 9: delete_branch, gated by serf's own merge check — never git's
	// `branch -d`, which is HEAD-relative (spec §5 remove step 9: rev-6 review
	// demonstrated `-d` deleting a never-merged branch under a detached HEAD
	// review of that same tip in the main checkout). force bypasses the gate
	// entirely (unconditional -D). A branch checked out in any other worktree
	// cannot be deleted by git at all, gate or no gate — that refusal is
	// surfaced with the checkout location git itself reports.
	if deleteBranch {
		tipOut, tipErr := run("rev-parse", "--verify", "refs/heads/"+name)
		if tipErr != nil {
			result.BranchKeptReason = fmt.Sprintf("branch %q not found: %v", name, tipErr)
		} else {
			tipSHA := strings.TrimSpace(tipOut)
			passesGate := force
			var evidence string
			if !passesGate {
				switch {
				case !hasSidecar:
					evidence = fmt.Sprintf("no metadata recorded for %q; cannot verify merge status without force", name)
				case tipSHA == sc.BaseSHA:
					passesGate = true
				default:
					mr, mErr := worktree.Merged(run, tipSHA, sc.MergeTarget, sc.BaseSHA)
					if mErr != nil {
						return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: checking merge status: %w", mErr)
					}
					switch {
					case mr.Merged:
						passesGate = true
					case mr.TargetUnknown:
						evidence = fmt.Sprintf("branch %q merge target unknown; tip %s not verified merged; re-invoke with force to delete anyway", name, shortSHA(tipSHA))
					default:
						evidence = fmt.Sprintf("branch %q is not merged into %s (tip %s); neither ancestry nor patch-equivalence holds; merge first or re-invoke with force to delete anyway", name, mr.TargetRef, shortSHA(tipSHA))
					}
				}
			}
			if passesGate {
				if _, err := run("branch", "-D", name); err != nil {
					result.BranchKeptReason = fmt.Sprintf("git refused to delete branch %q: %v", name, err)
				} else {
					result.BranchDeleted = true
				}
			} else {
				result.BranchKeptReason = evidence
			}
		}
	}

	// Step 10: sidecar disposition. Gone (deleted here) → delete the sidecar;
	// survives → keep it and mark worktree_removed + tip_sha_at_removal. The
	// mark write is UpdateSidecar's plain truncating write, not atomic — a
	// crash mid-write can leave it torn or unwritten, which sweep 2's
	// reconciliation (spec §5 prune sweep 2) explicitly tolerates via its "or
	// no removal record" branch: a sidecar with no (or a torn) removal record
	// is judged exactly like tip == base_sha for merge-gated collection, so an
	// unmarked sidecar is still handled correctly, just less precisely
	// reported meanwhile.
	if result.BranchDeleted {
		if err := worktree.DeleteSidecar(metaDir, name); err != nil && !os.IsNotExist(err) {
			return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: deleting sidecar: %w", err)
		}
	} else if hasSidecar {
		tipOut, tipErr := run("rev-parse", "--verify", "refs/heads/"+name)
		if tipErr == nil {
			tipSHA := strings.TrimSpace(tipOut)
			if err := worktree.UpdateSidecar(metaDir, name, func(sc *worktree.Sidecar) {
				sc.WorktreeRemoved = true
				sc.TipSHAAtRemoval = tipSHA
			}); err != nil && !os.IsNotExist(err) {
				return WorktreeRemoveResult{}, fmt.Errorf("manage_worktree remove: marking sidecar removed: %w", err)
			}
		}
	}

	// Step 11: report the path and whether the branch was deleted (already
	// carried on result: Path, Branch, BranchDeleted, BranchKeptReason,
	// Warning).
	return result, nil
}

// worktreeListEntryToMap renders a WorktreeListEntry into the tool result
// shape (spec §5 list step 3).
func worktreeListEntryToMap(e WorktreeListEntry) map[string]any {
	return map[string]any{
		"name":                 e.Name,
		"path":                 e.Path,
		"branch":               e.Branch,
		"current":              e.Current,
		"locked":               e.Locked,
		"lock_reason":          e.LockReason,
		"prunable":             e.Prunable,
		"prunable_reason":      e.PrunableReason,
		"has_metadata":         e.HasMetadata,
		"creator_session":      e.CreatorSession,
		"delegate_id":          e.DelegateID,
		"created_at":           e.CreatedAt,
		"age_seconds":          e.AgeSeconds,
		"dirty":                e.Dirty,
		"base_sha":             e.BaseSHA,
		"ahead_commits":        e.AheadCommits,
		"merge_target":         e.MergeTarget,
		"merged":               e.Merged,
		"merged_arm":           e.MergedArm,
		"merge_target_unknown": e.MergeTargetUnknown,
	}
}

// worktreePruneEntryToMap renders a WorktreePruneEntry into the tool result
// shape (spec §5 prune's report shape).
func worktreePruneEntryToMap(e WorktreePruneEntry) map[string]any {
	return map[string]any{
		"name":             e.Name,
		"path":             e.Path,
		"worktree_removed": e.WorktreeRemoved,
		"branch_removed":   e.BranchRemoved,
		"sidecar_removed":  e.SidecarRemoved,
		"reason":           e.Reason,
	}
}

// worktreeList performs the list operation (spec §5 list, all three steps):
// enumerate managed worktrees from `git worktree list --porcelain` (never
// running `git worktree prune`), filter with the same prefix-collision-safe,
// symlink-canonicalized comparison remove/switch use, and report each with
// its lock/prunable state plus disposal-relevant staleness pulled from the
// metadata sidecar and cheap git queries.
func (s *Session) worktreeList(ctx context.Context) ([]WorktreeListEntry, error) {
	st := s.worktreeStateSnapshot()
	if st.env == nil {
		return nil, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return nil, errors.New("manage_worktree list: not in a git repository")
	}
	run, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return nil, err
	}

	// Step 1: `git worktree list --porcelain` — never `git worktree prune`.
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("manage_worktree list: listing worktrees: %w", err)
	}

	// Step 2: filter to serf-managed worktrees, canonicalized comparison.
	projectDir := filepath.Join(st.worktreeRoot, worktree.ProjectID(st.mainRepoRoot))
	metaDir := filepath.Join(projectDir, ".meta")
	managed := managedPorcelainEntries(worktree.ParsePorcelain(out), projectDir)

	activeRoot := filepath.Clean(st.env.WorkingDirectory())
	now := s.sclock().Now()

	// Step 3: build one structured entry per managed worktree.
	entries := make([]WorktreeListEntry, 0, len(managed))
	for _, e := range managed {
		entry := WorktreeListEntry{
			Name:           e.Name,
			Path:           e.Path,
			Branch:         strings.TrimPrefix(e.Branch, "refs/heads/"),
			Current:        filepath.Clean(e.Path) == activeRoot,
			Locked:         e.Locked,
			LockReason:     e.LockReason,
			Prunable:       e.Prunable,
			PrunableReason: e.PrunableReason,
		}

		if sc, scErr := worktree.ReadSidecar(metaDir, e.Name); scErr == nil {
			entry.HasMetadata = true
			entry.CreatorSession = sc.CreatorSession
			entry.DelegateID = sc.DelegateID
			entry.CreatedAt = sc.CreatedAt
			entry.BaseSHA = sc.BaseSHA
			entry.MergeTarget = sc.MergeTarget
			if created, perr := time.Parse(time.RFC3339, sc.CreatedAt); perr == nil {
				entry.AgeSeconds = now.Sub(created).Seconds()
			}
		}

		// Cheap git queries: best-effort, and only meaningful when the
		// worktree directory actually exists (a `prunable` entry's directory
		// is momentarily or permanently absent — spec §5 list step 1).
		if _, statErr := os.Stat(e.Path); statErr == nil {
			if clean, _, cErr := worktree.CleanTree(run, e.Path); cErr == nil {
				entry.Dirty = !clean
			}
			if entry.HasMetadata && entry.BaseSHA != "" {
				if aheadOut, aErr := run("-C", e.Path, "rev-list", "--count", entry.BaseSHA+"..HEAD"); aErr == nil {
					if n, convErr := strconv.Atoi(strings.TrimSpace(aheadOut)); convErr == nil {
						entry.AheadCommits = n
					}
				}
				if tipOut, tErr := run("-C", e.Path, "rev-parse", "HEAD"); tErr == nil {
					tip := strings.TrimSpace(tipOut)
					if mr, mErr := worktree.Merged(run, tip, entry.MergeTarget, entry.BaseSHA); mErr == nil {
						entry.Merged = mr.Merged
						entry.MergedArm = mr.Arm
						entry.MergeTargetUnknown = mr.TargetUnknown
					}
				}
			}
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// worktreePrune performs the prune operation (spec §5 prune, all three
// sweeps in order): registered managed worktrees, sidecar reconciliation,
// then git registry hygiene.
func (s *Session) worktreePrune(ctx context.Context) (WorktreePruneResult, error) {
	st := s.worktreeStateSnapshot()
	if st.env == nil {
		return WorktreePruneResult{}, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return WorktreePruneResult{}, errors.New("manage_worktree prune: not in a git repository")
	}
	run, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return WorktreePruneResult{}, err
	}

	projectDir := filepath.Join(st.worktreeRoot, worktree.ProjectID(st.mainRepoRoot))
	metaDir := filepath.Join(projectDir, ".meta")

	removed1, skipped1, err := s.worktreePruneSweep1(run, projectDir, metaDir)
	if err != nil {
		return WorktreePruneResult{}, err
	}
	removed2, skipped2, err := s.worktreePruneSweep2(run, projectDir, metaDir)
	if err != nil {
		return WorktreePruneResult{}, err
	}

	result := WorktreePruneResult{
		Removed: append(removed1, removed2...),
		Skipped: append(skipped1, skipped2...),
	}

	ran, skipReason, err := worktreePruneSweep3(run, projectDir)
	if err != nil {
		return WorktreePruneResult{}, err
	}
	result.RegistryPruned = ran
	result.RegistrySkipReason = skipReason

	return result, nil
}

// worktreePruneSweep1 disposes of registered managed worktrees (spec §5
// prune sweep 1): a worktree is collected (dir + branch + sidecar removed)
// iff it is unlocked, has no live work under it, has a sidecar, is clean, and
// is disposable (unchanged or merged per the recorded merge_target). Every
// entry that fails one of those tests is reported skipped with the reason;
// any per-entry git query failure is treated as a soft skip rather than
// aborting the whole sweep, so one bad entry cannot block every other
// collectible worktree.
func (s *Session) worktreePruneSweep1(run worktree.GitRunner, projectDir, metaDir string) (removed, skipped []WorktreePruneEntry, err error) {
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, nil, fmt.Errorf("manage_worktree prune: listing worktrees: %w", err)
	}
	managed := managedPorcelainEntries(worktree.ParsePorcelain(out), projectDir)

	for _, e := range managed {
		// Not locked: the occupancy rule, owner-independent (spec §5 sweep 1;
		// EvPruneCandidate skips any locked state regardless of whose marker).
		lockSt := worktree.Unlocked
		if e.Locked {
			lockSt = worktree.ClassifyReason(e.LockReason, s.id, "")
		}
		if worktree.Decide(worktree.EvPruneCandidate, lockSt) == worktree.ActSkip {
			reason := "locked"
			if e.LockReason != "" {
				reason = fmt.Sprintf("locked (%s)", e.LockReason)
			}
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: reason})
			continue
		}

		// No live work under it (belt and braces alongside the lock).
		if live := s.liveWorkUnder(e.Path); len(live) > 0 {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "live work: " + strings.Join(live, ", ")})
			continue
		}

		// Has a sidecar: provenance unknown otherwise, not ours to judge.
		sc, scErr := worktree.ReadSidecar(metaDir, e.Name)
		if scErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "sidecar-less"})
			continue
		}

		// Clean.
		clean, offending, cErr := worktree.CleanTree(run, e.Path)
		if cErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "status check failed: " + cErr.Error()})
			continue
		}
		if !clean {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "dirty: " + strings.Join(offending, ", ")})
			continue
		}

		// Disposable: unchanged (tip == base) or merged (per the recorded
		// merge_target, never HEAD).
		tipOut, tErr := run("-C", e.Path, "rev-parse", "HEAD")
		if tErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "rev-parse HEAD failed: " + tErr.Error()})
			continue
		}
		tip := strings.TrimSpace(tipOut)

		disposable, reasonTag, dErr := disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)
		if dErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: "merge check failed: " + dErr.Error()})
			continue
		}
		if !disposable {
			skipped = append(skipped, WorktreePruneEntry{Name: e.Name, Path: e.Path, Reason: reasonTag})
			continue
		}

		// Collect: remove the worktree, delete the branch (-D, the merge gate
		// above already passed — spec §5 sweep 1's closing note, remove step 9's
		// rationale for never trusting `-d`), delete the sidecar.
		if _, err := run("worktree", "remove", "--", e.Path); err != nil {
			return nil, nil, fmt.Errorf("manage_worktree prune: removing %s: %w", e.Path, err)
		}
		if _, err := run("branch", "-D", e.Name); err != nil {
			return nil, nil, fmt.Errorf("manage_worktree prune: deleting branch %q: %w", e.Name, err)
		}
		if err := worktree.DeleteSidecar(metaDir, e.Name); err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("manage_worktree prune: deleting sidecar for %q: %w", e.Name, err)
		}
		removed = append(removed, WorktreePruneEntry{
			Name: e.Name, Path: e.Path,
			WorktreeRemoved: true, BranchRemoved: true, SidecarRemoved: true,
			Reason: reasonTag,
		})
	}

	return removed, skipped, nil
}

// disposableReason judges whether tip is disposable per spec §5's shared
// unchanged/merged test: tip == baseSHA (unchanged), or merged into
// mergeTarget's recorded tip (never HEAD). reason is "unchanged",
// "merged (ancestry)", "merged (cherry)", "merge target unknown", or
// "unmerged" — always set, whether or not disposable is true, so callers can
// use it directly as a skip reason on the false path.
func disposableReason(run worktree.GitRunner, tip, baseSHA, mergeTarget string) (disposable bool, reason string, err error) {
	if tip == baseSHA {
		return true, "unchanged", nil
	}
	mr, err := worktree.Merged(run, tip, mergeTarget, baseSHA)
	if err != nil {
		return false, "", err
	}
	switch {
	case mr.Merged:
		return true, fmt.Sprintf("merged (%s)", mr.Arm), nil
	case mr.TargetUnknown:
		return false, "merge target unknown", nil
	default:
		return false, "unmerged", nil
	}
}

// worktreePruneSweep2 reconciles metadata sidecars with no matching
// registered worktree (spec §5 prune sweep 2): a sidecar younger than
// worktree.ReconcileGrace (judged by file mtime) is left alone as
// possibly-racing-create residue; older ones are resolved per the branch's
// current state (gone → stale, adopted → sidecar dropped, disposable →
// branch+sidecar deleted, checked out elsewhere → skipped, unmerged →
// kept as residue).
func (s *Session) worktreePruneSweep2(run worktree.GitRunner, projectDir, metaDir string) (removed, skipped []WorktreePruneEntry, err error) {
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, nil, fmt.Errorf("manage_worktree prune: listing worktrees: %w", err)
	}
	registered := make(map[string]bool)
	for _, e := range managedPorcelainEntries(worktree.ParsePorcelain(out), projectDir) {
		registered[e.Name] = true
	}

	sidecars, err := worktree.ListSidecars(metaDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("manage_worktree prune: listing metadata: %w", err)
	}

	for _, sc := range sidecars {
		if registered[sc.Name] {
			continue // a live registered worktree exists; sweep 1's job, not reconciliation's
		}

		age, ageErr := worktree.SidecarAge(metaDir, sc.Name)
		if ageErr == nil && age < worktree.ReconcileGrace {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "in-grace"})
			continue
		}

		if !branchExists(run, sc.Name) {
			if err := worktree.DeleteSidecar(metaDir, sc.Name); err != nil && !os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("manage_worktree prune: deleting stale sidecar %q: %w", sc.Name, err)
			}
			removed = append(removed, WorktreePruneEntry{Name: sc.Name, SidecarRemoved: true, Reason: "stale sidecar (no worktree, no branch)"})
			continue
		}

		tipOut, tErr := run("rev-parse", "--verify", "refs/heads/"+sc.Name)
		if tErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "rev-parse failed: " + tErr.Error()})
			continue
		}
		tip := strings.TrimSpace(tipOut)

		// Adopted: the branch survived a prior branch-kept removal and the
		// user has since built on it (tip is neither the recorded base nor
		// the tip serf recorded at removal time) — serf's claim expires.
		if sc.WorktreeRemoved && worktree.Adopted(tip, sc.BaseSHA, sc.TipSHAAtRemoval) {
			if err := worktree.DeleteSidecar(metaDir, sc.Name); err != nil && !os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("manage_worktree prune: deleting adopted sidecar %q: %w", sc.Name, err)
			}
			removed = append(removed, WorktreePruneEntry{Name: sc.Name, SidecarRemoved: true, Reason: "adopted"})
			continue
		}

		// Not adopted (tip == base_sha, tip == tip_sha_at_removal, or no
		// removal record): eligible for the merge-gated branch deletion.
		disposable, reasonTag, dErr := disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)
		if dErr != nil {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: "merge check failed: " + dErr.Error()})
			continue
		}
		if !disposable {
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: reasonTag})
			continue
		}

		if _, err := run("branch", "-D", sc.Name); err != nil {
			reason := "checked out"
			if loc, ok := checkoutLocationOf(run, sc.Name); ok {
				reason = "checked out at " + loc
			}
			skipped = append(skipped, WorktreePruneEntry{Name: sc.Name, Reason: reason})
			continue
		}
		if err := worktree.DeleteSidecar(metaDir, sc.Name); err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("manage_worktree prune: deleting sidecar %q: %w", sc.Name, err)
		}
		removed = append(removed, WorktreePruneEntry{Name: sc.Name, BranchRemoved: true, SidecarRemoved: true, Reason: reasonTag})
	}

	return removed, skipped, nil
}

// worktreePruneSweep3 runs repo-wide `git worktree prune` registry hygiene
// (spec §5 prune sweep 3) only when every `prunable`-annotated porcelain
// entry is under the managed directory; a non-managed prunable entry (a
// user's own sibling worktree, momentarily or permanently absent) makes this
// deliberately not this tool's call to deregister, and the whole sweep is
// skipped with a reason naming the entry.
func worktreePruneSweep3(run worktree.GitRunner, projectDir string) (ran bool, skipReason string, err error) {
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return false, "", fmt.Errorf("manage_worktree prune: listing worktrees: %w", err)
	}
	for _, e := range worktree.ParsePorcelain(out) {
		if !e.Prunable {
			continue
		}
		if _, ok := relPathUnderManagedDir(canonicalOrClean(e.Path), projectDir); !ok {
			return false, fmt.Sprintf("non-managed prunable entry: %s (%s)", e.Path, e.PrunableReason), nil
		}
	}
	if _, err := run("worktree", "prune"); err != nil {
		return false, "", fmt.Errorf("manage_worktree prune: git worktree prune: %w", err)
	}
	return true, "", nil
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
