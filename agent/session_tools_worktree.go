package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/identifier"
)

// worktreeGitTimeoutMS bounds each git lifecycle subprocess the manage_worktree
// tool forks. `git worktree add` checks out a fresh tree and `git status` can
// take real time on a large repo, so the ceiling is generous.
const worktreeGitTimeoutMS = 300_000

// worktreeTestSeams provides per-session failure injection at filesystem and
// sandbox boundaries that cannot be made to fail reliably under a temp dir.
// It lives in cfg.testOnly and is zero in production, so the wrappers below
// preserve the normal direct calls.
type worktreeTestSeams struct {
	useControlPolicy func(*execenv.LocalExecutionEnvironment, string) error
	enterWorktree    func(string, bool) error
	writeSidecar     func(string, string, worktree.Sidecar) error
	deleteSidecar    func(string, string) error
	updateSidecar    func(string, string, func(*worktree.Sidecar)) error
}

func (s *Session) useWorktreeControlPolicy(env *execenv.LocalExecutionEnvironment, root string) error {
	if hook := s.cfg.testOnly.worktree.useControlPolicy; hook != nil {
		return hook(env, root)
	}
	return env.UseControlPolicy(root)
}

func (s *Session) writeWorktreeSidecar(dir, name string, sidecar worktree.Sidecar) error {
	if hook := s.cfg.testOnly.worktree.writeSidecar; hook != nil {
		return hook(dir, name, sidecar)
	}
	return worktree.WriteSidecarExcl(dir, name, sidecar)
}

func (s *Session) deleteWorktreeSidecar(dir, name string) error {
	if hook := s.cfg.testOnly.worktree.deleteSidecar; hook != nil {
		return hook(dir, name)
	}
	return worktree.DeleteSidecar(dir, name)
}

func (s *Session) updateWorktreeSidecar(dir, name string, mutate func(*worktree.Sidecar)) error {
	if hook := s.cfg.testOnly.worktree.updateSidecar; hook != nil {
		return hook(dir, name, mutate)
	}
	return worktree.UpdateSidecar(dir, name, mutate)
}

// worktreeState is the snapshot worktreeGuard.state() returns (spec §7): the
// current env, the saved restore env, the resolved canonical Project, the
// derived worktree root, and the managed worktree path the session currently
// occupies (empty when none). env and restoreEnv are nil when the session env
// is not a LocalExecutionEnvironment.
type worktreeState struct {
	env             *execenv.LocalExecutionEnvironment
	restoreEnv      *execenv.LocalExecutionEnvironment
	project         identifier.Project
	mainRepoRoot    string
	worktreeRoot    string
	currentWorktree string
	err             error
}

// worktreeAnchorGoneError reports that the session's recorded working directory
// — the anchor every relative path resolves against — has been deleted out from
// under a running session (issue #4671). It names the dead path and the recovery
// so no surface leaks the bare resolve-project lstat, and no surface silently
// recreates the directory.
type worktreeAnchorGoneError struct{ path string }

func (e *worktreeAnchorGoneError) Error() string {
	return fmt.Sprintf(
		"session working directory %s no longer exists; re-anchor the session to an existing directory (for example the main checkout) or run git worktree operations from the main checkout",
		e.path)
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

// WorktreeAdoptResult is what a successful adopt returns: the adopted
// worktree's path, the managed name it now answers to (its path relative to
// the managed root), its branch, and the base commit recorded in the sidecar
// adopt wrote.
type WorktreeAdoptResult struct {
	Path    string
	Name    string
	Branch  string
	BaseSHA string
}

// WorktreeListEntry is one worktree in the manage_worktree list result (spec
// §5 list step 3): path, branch, current occupancy, lock state, prunable
// annotation, and disposal-relevant staleness state pulled from the metadata
// sidecar plus cheap git queries. HasMetadata is false for a sidecar-less
// worktree; the metadata-derived fields (CreatorSession, DelegateID,
// CreatedAt, AgeSeconds, BaseSHA, MergeTarget, AheadCommits, Merged,
// MergedArm, MergeTargetUnknown) are then zero values rather than an error —
// list surfaces provenance-unknown entries instead of refusing them.
//
// DirtyUnknown and AheadUnknown mark the two reads whose zero value is an
// affirmative "this lane holds nothing" — the answer that makes abandoning it
// look safe. When their git read fails they are set rather than left to
// degrade into "clean, 0 ahead". Merged has no such flag: its zero value is
// "not merged", which errs toward keeping the lane.
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
	DirtyUnknown       bool
	BaseSHA            string
	AheadCommits       int
	AheadUnknown       bool
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
	Removed []WorktreePruneEntry
	Skipped []WorktreePruneEntry
	// Unlocked names delegate lanes the orphan arm released without
	// collecting: their parent provably cannot act as the disposer, and the
	// lane holds work the ordinary gates kept. The lane stays, unlocked and
	// visible to the merge and force paths (#5471).
	Unlocked           []WorktreePruneEntry
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
	// enterWorktree swaps the session env into path, saving the prior env the
	// first time (spec §7 enterWorktree()). managed records whether path is a
	// evener-managed worktree, persisted via SessionMeta.WorktreeManaged (spec §7
	// "Persistence and resume"). It returns an error when the sandbox policy
	// cannot be re-anchored to path, in which case the session stays in its prior
	// (confined) env rather than running unconfined in the new worktree.
	enterWorktree func(path string, managed bool) error
	// exitWorktree restores the saved pre-worktree env and clears it, returning
	// the restored root and ok=false when no restore env was saved (spec §7
	// exitWorktree()), or an error when the swap was refused because the
	// session is closing.
	exitWorktree func() (restoredRoot string, ok bool, err error)
	// beginOp admits a whole manage_worktree call on the session's close fence,
	// labelled for a fence warning. ok is false when the session is already
	// closing, in which case the operation runs unfenced — there is nothing left
	// to fence it against. A true return MUST be paired with endOp.
	beginOp func(label string) (envWorkID, bool)
	// endOp releases an admission obtained from beginOp.
	endOp func(envWorkID)
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
	// adoptOp runs the adopt operation: convert an unmanaged worktree under the
	// managed root into a managed one by writing its metadata sidecar. It is
	// the ONLY way anything becomes managed after the fact — nothing in list,
	// remove or prune ever adopts (spec §6 "Metadata sidecar").
	adoptOp func(ctx context.Context, path string) (WorktreeAdoptResult, error)
	// exitOp runs the exit operation (spec §4 exit): the operation-level
	// choreography (leave-unlock, restore, idempotent restore-relock) layered
	// on top of the exitWorktree env-restore primitive above.
	exitOp func(ctx context.Context) (WorktreeExitResult, error)
	// removeOp runs the remove operation (spec §5 remove, all eleven steps).
	// force overrides provenance/merge gating; forceDirty separately overrides
	// the uncommitted-work refusal so forcing past an ownership/merge gate
	// cannot collaterally discard an uncommitted edit.
	removeOp func(ctx context.Context, name string, force, forceDirty, deleteBranch bool) (WorktreeRemoveResult, error)
	// listOp runs the list operation (spec §5 list, all three steps).
	listOp func(ctx context.Context) ([]WorktreeListEntry, error)
	// pruneOp runs the prune operation (spec §5 prune, all three sweeps).
	// orphanedDelegateDays arms the orphan reclaim arm (#5471) when > 0.
	pruneOp func(ctx context.Context, orphanedDelegateDays float64) (WorktreePruneResult, error)
	// disposeOp runs the dispose operation (spec §P1): retire a delegate's
	// isolation worktree lane by id. force overrides an unmerged-commits refusal;
	// forceDirty separately overrides an uncommitted-changes refusal (orthogonal,
	// like remove).
	disposeOp func(ctx context.Context, id string, force, forceDirty bool) (WorktreeDisposeResult, error)
	// disposeOnly reports whether this session is served the dispose-only
	// manage_worktree surface (delegate-lane disposal spec §P1 "Availability"):
	// a worktree-isolated coordinator that may only dispose its own delegates'
	// lanes. When true the handler refuses every operation except dispose. The
	// registry can add or remove only whole tools, not individual operations, so
	// the dispose-only schema variant is paired with this in-handler gate.
	disposeOnly func() bool
}

// worktreeDisposeOnlySurface reports whether this session is served the
// dispose-only manage_worktree surface. It backs the handler gate seam
// (worktreeGuard.disposeOnly). The flag is write-once during session init,
// before any tool call runs, so no lock is needed.
func (s *Session) worktreeDisposeOnlySurface() bool {
	return s.worktreeDisposeOnly
}

// serveDisposeOnlyWorktreeTool swaps the full manage_worktree tool for its
// dispose-only variant (spec §P1 "Availability"), preserving the same tool name
// and executor. The executor gates non-dispose operations via the
// worktreeDisposeOnly flag this method sets. Called from initSessionState for a
// worktree-isolated coordinator, on both the spawn and restore paths.
func (s *Session) serveDisposeOnlyWorktreeTool() {
	existing := s.reg.Get("manage_worktree")
	if existing == nil {
		return
	}
	s.worktreeDisposeOnly = true
	t := *existing
	t.Definition = tool.DefManageWorktreeDisposeOnly()
	// Drop the copied full schema: Register compiles one for the narrowed
	// parameters.
	t.Schema = nil
	t.Execute = nil
	_ = s.reg.Register(t)
}

// errWorktreeOpWhileClosing is what the manage_worktree dispatch, and the
// isolation step of a delegate spawned with isolation:"worktree", return when
// the session began closing before the work could be admitted on the close
// fence. It is the operation-level sibling of errSwapWhileClosing: unadmitted
// work cannot be waited for, and the close may already be past its join, so the
// only safe answer is to run nothing at all.
var errWorktreeOpWhileClosing = errors.New("the session is closing; operation refused")

// worktreeOpLabel names a manage_worktree call for a close-fence warning: the
// operation, and the target it acts on when the arguments carry one. The
// argument names are the schema's own (name for create/switch/remove, path for
// switch-by-path and adopt, id for dispose), so an unrecognized shape degrades
// to the bare operation rather than guessing.
func worktreeOpLabel(operation string, args map[string]any) string {
	label := "manage_worktree " + operation
	for _, key := range []string{"name", "path", "id"} {
		if target, ok := args[key].(string); ok && strings.TrimSpace(target) != "" {
			return label + " " + strings.TrimSpace(target)
		}
	}
	return label
}

// registerWorktreeTool registers the manage_worktree lifecycle tool (spec §2)
// directly on the registry, mirroring registerGoalTools/registerTaskTools:
// registry-only, not part of any provider profile's own tool definitions.
// Register with ReadOnly unset (false) — even the list operation is part of a
// stateful lifecycle tool and must serialize with env-changing operations.
func registerWorktreeTool(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefManageWorktree(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env // the guard reads/writes the session env under s.mu; the passed env is currentEnv()
			operation, _ := args["operation"].(string)
			// Dispose-only surface (spec §P1 "Availability"): a worktree-isolated
			// coordinator gets the dispose-only schema variant, but the registry can
			// only add or remove whole tools — so the handler enforces the same
			// restriction, refusing every op except dispose with the isolation
			// rationale (a session inside a lane that could create/switch/remove
			// worktrees would be able to force-remove sibling lanes the parent
			// created; see rootOnlyWorktreeTools).
			if operation != "dispose" && deps.worktreeGuard.disposeOnly != nil && deps.worktreeGuard.disposeOnly() {
				return nil, fmt.Errorf("manage_worktree %s: refused — you run inside your own isolation worktree lane, so you may only dispose your own delegates' lanes (operation=dispose); creating, switching, listing, removing, or pruning worktrees could disturb the sibling lanes your parent created", operation)
			}
			// Admit the WHOLE operation on the close fence, here, at the one
			// entry every operation passes through (worktreeGuard is wired only
			// from newToolDeps, and the sole direct call to an op function —
			// remove's cascade into dispose — is itself inside a dispatched
			// operation). Every arm runs git on the session's execution
			// environment and most of them move lanes and locks around, so a
			// close must wait for the operation rather than reap the process
			// table under its git or unlock a lane it is still moving. Only some
			// operations swap the environment, so the swap's own admission
			// covers only those, and it does not begin until after an op's core
			// has already run; this one spans the call. The swap and rollback
			// admissions nest inside it and carry the finer labels a fence
			// warning wants once the operation is past its core.
			//
			// A REFUSED admission refuses the operation. Every other admission
			// in this file proceeds unfenced when it cannot be admitted, because
			// each of those is an undo the session already owes and dropping it
			// would leave residue; this one is work that has not started, and
			// close may already be past its join, so running it would lock
			// lanes and write sidecars against an environment being reaped and
			// stores being closed. Refusing costs a tool call that a closing
			// session was never going to complete anyway.
			if deps.worktreeGuard.beginOp != nil {
				admission, ok := deps.worktreeGuard.beginOp(worktreeOpLabel(operation, args))
				if !ok {
					return nil, fmt.Errorf("manage_worktree %s: %w", operation, errWorktreeOpWhileClosing)
				}
				defer deps.worktreeGuard.endOp(admission)
			}
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
			case "adopt":
				path, _ := args["path"].(string)
				if strings.TrimSpace(path) == "" {
					return nil, errors.New("manage_worktree adopt: path is required")
				}
				res, err := deps.worktreeGuard.adoptOp(ctx, path)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"status":   "adopted",
					"path":     res.Path,
					"name":     res.Name,
					"branch":   res.Branch,
					"base_sha": res.BaseSHA,
					"message": fmt.Sprintf(
						"Adopted the worktree at %s as managed worktree %q (branch %s). It is now switchable by name and subject to remove and prune like any worktree evener created.",
						res.Path, res.Name, res.Branch),
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
				forceDirty, _ := args["force_dirty"].(bool)
				deleteBranch, _ := args["delete_branch"].(bool)
				res, err := deps.worktreeGuard.removeOp(ctx, name, force, forceDirty, deleteBranch)
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
				managed, unmanaged := partitionWorktreeListEntries(entries)
				out := make([]map[string]any, len(managed))
				for i, e := range managed {
					out[i] = worktreeListEntryToMap(e)
				}
				strays := make([]map[string]any, len(unmanaged))
				for i, e := range unmanaged {
					strays[i] = worktreeUnmanagedEntryToMap(e)
				}
				return map[string]any{
					"status":    "listed",
					"entries":   out,
					"unmanaged": strays,
					"message":   worktreeListSummary(managed, unmanaged),
				}, nil
			case "prune":
				orphanedDays, _ := args["orphaned_delegate_days"].(float64)
				res, err := deps.worktreeGuard.pruneOp(ctx, orphanedDays)
				if err != nil {
					return nil, err
				}
				removed := worktreePruneEntryMaps(res.Removed)
				skipped := worktreePruneEntryMaps(res.Skipped)
				unlocked := worktreePruneEntryMaps(res.Unlocked)
				msg := fmt.Sprintf("Removed %d, skipped %d.", len(removed), len(skipped))
				if len(unlocked) > 0 {
					msg += fmt.Sprintf(" Unlocked %d orphaned delegate lane(s), kept: their work is not disposable.", len(unlocked))
				}
				if res.RegistryPruned {
					msg += " Git worktree registry pruned."
				} else if res.RegistrySkipReason != "" {
					msg += " Registry hygiene sweep skipped: " + res.RegistrySkipReason
				}
				return map[string]any{
					"status":               "pruned",
					"removed":              removed,
					"skipped":              skipped,
					"unlocked":             unlocked,
					"registry_pruned":      res.RegistryPruned,
					"registry_skip_reason": res.RegistrySkipReason,
					"message":              msg,
				}, nil
			case "dispose":
				id, _ := args["id"].(string)
				force, _ := args["force"].(bool)
				forceDirty, _ := args["force_dirty"].(bool)
				res, err := deps.worktreeGuard.disposeOp(ctx, id, force, forceDirty)
				if err != nil {
					return nil, err
				}
				status := "disposed"
				if res.AlreadyDisposed {
					status = "already_disposed"
				}
				return map[string]any{
					"status":  status,
					"id":      res.DelegateID,
					"path":    res.LanePath,
					"branch":  res.Branch,
					"message": res.Message,
				}, nil
			default:
				return nil, fmt.Errorf("manage_worktree: unknown operation %q", operation)
			}
		},
	})
}
