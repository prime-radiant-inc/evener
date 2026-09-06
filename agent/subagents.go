package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
)

const delegateSalvagedDraftNote = "partial draft salvaged in the child transcript — resume it with delegate_send rather than re-dispatching"

// SubagentStatus tracks the lifecycle of a sub-agent.
type SubagentStatus string

const (
	// SubagentRunning indicates the sub-agent is currently executing a run.
	SubagentRunning SubagentStatus = "running"
	// SubagentCompleted indicates the sub-agent's run finished without error.
	SubagentCompleted SubagentStatus = "completed"
	// SubagentFailed indicates the sub-agent's run finished with an error.
	SubagentFailed SubagentStatus = "failed"
	// SubagentCancelled indicates the sub-agent's run was cancelled.
	SubagentCancelled SubagentStatus = "cancelled"
	// SubagentExhausted indicates the sub-agent stopped at a configured budget.
	SubagentExhausted SubagentStatus = "exhausted"
)

// subagentResult is the structured output from a completed sub-agent.
type subagentResult struct {
	AgentID       string         `json:"agent_id"`
	Status        SubagentStatus `json:"status"`
	Closed        bool           `json:"closed"`
	Output        string         `json:"output"`
	Success       bool           `json:"success"`
	TurnsUsed     int            `json:"turns_used"`
	TranscriptRef string         `json:"transcript_ref,omitempty"`
}

// defaultSubagentInstructions is the role-specific prompt for default subagents
// (no agent_type). Appended after the common subagent base prompt.
const defaultSubagentInstructions = `You are a general-purpose subagent. Do the work yourself using the tools
available in this session.
Do NOT try to spawn further subagents.

Your job is to complete the task and report your findings.`

const defaultDelegatingSubagentInstructions = `You are a general-purpose subagent. Do the work yourself using the tools
available in this session.

You may delegate scoped subwork when it reduces context, parallelizes independent
work, or gives you a focused verifier. You remain responsible for inspecting the
delegate's result before relying on it.

Your job is to complete the task and report your findings.`

var rootOnlyJobPresenceTools = []string{"delegate"}

// rootOnlyWorktreeTools are worktree lifecycle tools reserved for the root
// session. Delegates receive worktree isolation via delegate(isolation:"worktree"),
// which the parent-side harness manages; no child flow needs to call
// manage_worktree, and a child that could would be able to force-remove
// sibling worktrees the parent created.
var rootOnlyWorktreeTools = []string{"manage_worktree"}

type subagent struct {
	id   string
	sess *Session
	emit func(events.EventKind, events.EventData)

	mu                    sync.Mutex
	running               bool
	status                SubagentStatus
	turnsUsed             int
	done                  chan struct{}
	result                string
	err                   error
	resultConsumed        bool // true after the first wait returns this run's result
	endEmitted            bool
	runProvenance         *provenance.Causal        // immutable causal provenance for the completed run result
	runStructured         any                       // communicate structured result captured before this run releases its finalization gate
	runStructuredCaptured bool                      // runStructured was captured, including an authoritative nil result
	nudgeEnabled          bool                      // true for default subagents that should be nudged to communicate
	cancel                context.CancelFunc        // cancels the current run's context
	cancelRequested       bool                      // set by parent stop so finalize maps a context.Canceled run to cancelled
	settlementClaimed     bool                      // cancellation admission closes after the run's final pre-settlement check
	agentType             string                    // plugin agent type name; empty for default subagents
	createdAt             time.Time                 // set once at spawn; never reset on resume
	startedAt             time.Time                 // set at spawn; re-stamped at each idle-resume
	endedAt               *time.Time                // set at run finalize; cleared to nil at idle-resume
	stableDescriptor      *delegatestore.Descriptor // immutable committed identity/config for stable terminal evidence
	closed                bool                      // session torn down; record retained as terminal history
	closeTimedOut         bool                      // session-close wait exceeded its bound; close not confirmed
	driving               bool                      // a drive-down notification turn (§3) is in flight on this idle child
	fatalRunGated         bool                      // terminal run error freezes automatic drives until an explicit resume
	finalizing            bool                      // the run accepts no input while owned work drains and terminal state/notify ownership are handed off
	// disposeGated freezes a quiescent, retained TERMINAL child while a dispose op
	// (spec §P1 step 4) evaluates and evicts it: no wake-edge drive may launch and
	// no delegate_send may resume the child while it is set. Guarded by sub.mu; set
	// only after re-verifying !running && !driving under the same hold, so a drive
	// or resume that raced ahead wins and the gate is refused. Reversed on every
	// pre-eviction dispose refusal/failure exit.
	disposeGated bool
}

type preparedSubagentRun struct {
	sub                *subagent
	input              string
	runCtx             context.Context
	runCancel          context.CancelFunc
	parentSessionID    string
	originToolCallID   string
	originItemID       string
	task               string
	agentType          string
	requestedModel     string
	resolvedAgentName  string
	reasoningEffort    string
	frozenRolePrompt   string
	frozenTaskPrompt   string
	frozenToolNames    []string
	frozenSkillNames   []string
	frozenSkillBodies  []string
	workingDir         string
	localEnvPolicy     string
	sandboxSnapshot    *delegatestore.SandboxSnapshot
	isolation          string
	resultSchema       map[string]any
	explicitToolGrants []string
	// treeSlot is the tree-counter reservation claimed by prepareSubagentRun for
	// this spawn's running delegate turn (spec §4). Ownership transfers to the
	// delegate runningJob at attach; if the prepared run is discarded before
	// attach (an error path, or the legacy in-process spawn that mints no
	// delegate job), the reservation is released so the slot is not leaked.
	treeSlot *treeReservation
}

// disposeUnadoptedScratch drops every per-session scratch directory env
// provisioned — the sandbox-owned one and the one an unsandboxed environment
// mints on its first command — releasing each lease with its directory. Every
// caller is a path that provisioned an environment and then failed before any
// session adopted it: both releases belong to a session's own teardown, so
// without this nothing ever runs them and each failure leaves a directory and a
// live lease behind. A no-op for an environment with no scratch to drop,
// including one that is not local. It must run only on an environment built for
// the failed thing, never on a shared parent's, whose scratch the parent is
// still working in.
func disposeUnadoptedScratch(env execenv.ExecutionEnvironment) {
	if local, ok := env.(*execenv.LocalExecutionEnvironment); ok {
		local.DisposeUnadoptedScratch()
	}
}

// childScratchDisposition says what becomes of the scratch a child's owned
// environment provisioned when the child is torn down.
type childScratchDisposition int

const (
	// retainChildScratch releases the leases and keeps the directories: the
	// child finished something a human may still want to inspect, the handoff
	// every normal session teardown makes.
	retainChildScratch childScratchDisposition = iota
	// disposeChildScratch drops the directories with their leases: the child
	// was never adopted or is being discarded, so no one is left to hand
	// anything to.
	disposeChildScratch
)

// teardownChildSession closes a child session and settles what it owned. It is
// the one teardown every child takes — the parent's own close, the eviction of
// a retained terminal child, the stable controller's reclamation of a retained
// runtime, the disposal of a child that never became a tracked delegate, and
// the disposal of an isolation lane — so every path makes the same two
// decisions.
//
// Invariant: a session runs Cleanup only on an environment whose process table
// it constructed, at its own close, and never on a child's. A child's
// environment is the parent's own (a delegate with neither a working dir nor a
// box of its own), a WithWorkingDirectory clone built for it at spawn, or — a
// delegate on the parent's own environment can still build one mid-life by
// entering a worktree — a clone the child built for itself later. Every clone
// shares the process table it was cloned from by pointer, so Cleanup on one
// signals that table's live owner. A child's own processes end without it: its
// job manager stops its shells and cancellation ends its tool commands at
// close, and whatever survives is reaped when the table's owner closes. What a
// child owns outright is its clone's scratch — the sandbox-provisioned dir, the
// one an unsandboxed clone minted on its first command, and the one a
// shared-environment child minted after entering a worktree — and that is
// released here, both kinds together, per scratch: retained on a handoff,
// disposed when the child is dropped. The parent's own environment is left
// untouched in every respect: the parent is still working in it. Which
// environment (if any) a teardown settles is Session.environmentOwnedAtTeardown's
// decision, so a teardown reaching a child no parent bookkeeping names still
// settles it correctly.
func teardownChildSession(ctx context.Context, sess *Session, scratch childScratchDisposition) {
	if sess == nil {
		return
	}
	sess.close(ctx, false)
	// Every entry is a clone the child built for itself by entering or switching
	// worktrees and then swapped away from: no child close runs the cleanupEnv
	// block that drains sess.abandonedEnvs, so this is the only teardown that
	// reaches them. Retaining releases their leases without touching any process
	// table — the parent's own object can never be in this list
	// (recordAbandonedEnvironmentLocked excludes it by construction).
	sess.retainAbandonedEnvironmentScratch()
	releaseOwnedChildEnvironment(sess.environmentOwnedAtTeardown(), scratch)
}

// environmentOwnedAtTeardown returns the environment this session's own
// teardown settles, or nil when the session is still holding the one its live
// parent works in. Ownership is not frozen at spawn: a child handed its
// parent's own environment builds one of its own the moment it enters a
// worktree, and that clone's scratch is the child's — nothing else will ever
// reach it. The parent's object is the one thing this never names.
func (s *Session) environmentOwnedAtTeardown() execenv.ExecutionEnvironment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ownsEnv {
		return s.env
	}
	if s.parentSharedEnv == nil || s.env == s.parentSharedEnv {
		return nil
	}
	return s.env
}

// releaseOwnedChildEnvironment is teardownChildSession's environment step on
// its own, for the one child close that is not a close(): a restore candidate
// nothing adopted is discarded by discardRestoredCandidate, which settles the
// candidate's own resources and then makes exactly this decision for its env.
// env is nil when the child is still holding its parent's own environment —
// there is nothing for this teardown to settle.
func releaseOwnedChildEnvironment(env execenv.ExecutionEnvironment, scratch childScratchDisposition) {
	if env == nil {
		return
	}
	if scratch == disposeChildScratch {
		disposeUnadoptedScratch(env)
		return
	}
	if local, ok := env.(*execenv.LocalExecutionEnvironment); ok {
		local.RetainSessionScratch()
	}
}

// recordEnvironmentOwnership records whether env was built FOR this child
// (ownsFresh) or is the live parent's own object it was handed instead. A
// child that does not own env keeps a reference to it in parentSharedEnv so a
// later teardown — including one that finds env swapped for a clone the child
// built for itself mid-life — knows which environment, if either, is its own
// to settle. Both call sites write this before the session is published
// anywhere else reachable, so no lock is needed.
func (s *Session) recordEnvironmentOwnership(env execenv.ExecutionEnvironment, ownsFresh bool) {
	s.ownsEnv = ownsFresh
	if !ownsFresh {
		s.parentSharedEnv = env
	}
}

// disposeUnadoptedSubagentSession tears down a child that never became a
// tracked/adopted delegate: the create-path twin of discardRestoredCandidate.
// No owner is left to hand anything to, so its owned scratch goes with it.
func disposeUnadoptedSubagentSession(sess *Session) {
	teardownChildSession(context.Background(), sess, disposeChildScratch)
}

func (p *preparedSubagentRun) disposeUnadopted() {
	if p == nil || p.sub == nil {
		return
	}
	disposeUnadoptedSubagentSession(p.sub.sess)
}

func hasString(items []string, want string) bool {
	return slices.Contains(items, want)
}

func appendUniqueStrings(items []string, extras ...string) []string {
	for _, extra := range extras {
		if extra == "" || hasString(items, extra) {
			continue
		}
		items = append(items, extra)
	}
	return items
}

func ensureRecoveryReader(names []string, reg *tool.Registry) []string {
	if reg != nil && reg.RequiresOutputRecovery(names) {
		return appendUniqueStrings(append([]string(nil), names...), "read_transcript")
	}
	return names
}

func removeStrings(items, removals []string) []string {
	if len(removals) == 0 {
		return append([]string(nil), items...)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if hasString(removals, item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func rootOnlySubagentTools() []string {
	all := appendUniqueStrings(append([]string(nil), rootOnlyJobPresenceTools...), rootOnlyJobControlTools...)
	return appendUniqueStrings(all, rootOnlyWorktreeTools...)
}

func isRootOnlyJobPresenceTool(name string) bool {
	return hasString(rootOnlyJobPresenceTools, name)
}

func isRootOnlySubagentTool(name string) bool {
	return hasString(rootOnlySubagentTools(), name)
}

// protectedGrantTools lists tools that grant_tools may never add to a
// subagent, independent of rootOnlySubagentTools. That list's removal is
// allowance-gated (a coordinator with delegation_allowance keeps its
// members), but ask_user's exclusion from every subagent is unconditional
// (spec §7 point 1) — and the grant loop otherwise consults the PARENT's own
// registry, where ask_user is legitimately registered on an interactive root
// session. Without this check the loop has no reason to reject it.
func protectedGrantTools() []string {
	return []string{"ask_user"}
}

func isProtectedGrantTool(name string) bool {
	return hasString(protectedGrantTools(), name)
}

func removeRootOnlySubagentTools(items []string) []string {
	return removeStrings(items, rootOnlySubagentTools())
}

func baseSubagentToolPolicy(agent *plugin.Agent, canDelegate bool) (allTools bool, allowed []string, denied []string) {
	switch {
	case agent != nil && agent.AllTools:
		return true, nil, nil
	case agent != nil && len(agent.Tools) > 0:
		allowed = append([]string(nil), agent.Tools...)
		allowed = appendUniqueStrings(allowed, "task_list")
		// compact_context is context hygiene, not a capability an agent type
		// opts into: a child that cannot compact can only wait for the
		// automatic compaction to run unsteered. The untyped surface already
		// keeps it (deny-list path), so listing tools: must not take it away.
		allowed = appendUniqueStrings(allowed, "compact_context")
		// Root-only job and delegation tools in a typed role's list are
		// allowance-gated: granted, the role keeps them and gains job_watch
		// to supervise its delegates; a leaf loses them, on every spawn
		// path, exactly as the untyped surface does.
		if canDelegate {
			if hasString(allowed, "delegate") {
				allowed = appendUniqueStrings(allowed, "job_watch")
			}
		} else {
			allowed = removeRootOnlySubagentTools(allowed)
		}
		return false, allowed, nil
	default:
		if canDelegate {
			return false, nil, nil // untyped child with allowance: no deny-list → gets delegate+job_watch on default surface
		}
		return false, nil, rootOnlySubagentTools()
	}
}

// subagentToolScopeIsReadOnly reports whether an explicitly tool-scoped agent
// has any direct workspace mutation capability. Shell is intentionally not in
// this list: the bundled explorer/reviewer/verifier roles need shell for
// inspection, but a shell is still a write-capable process unless the child's
// execution environment supplies a kernel boundary. The boundary is therefore
// derived from the structured tool scope, never from role prose.
func subagentToolScopeIsReadOnly(allTools bool, allowed []string) bool {
	if allTools || len(allowed) == 0 {
		return false
	}
	for _, name := range allowed {
		switch name {
		case "write_file", "edit_file", "apply_patch", "manage_worktree":
			return false
		}
	}
	return true
}

func frozenSubagentToolNames(allTools bool, allowed, denied []string) []string {
	switch {
	case allTools:
		return []string{"*"}
	case len(allowed) > 0:
		return append([]string(nil), allowed...)
	case len(denied) > 0:
		return nil
	default:
		return nil
	}
}

func stableDelegateToolNameCeiling(reg *tool.Registry, resultToolName string, allTools bool, allowed, denied []string, canDelegate, watchParent bool, isolation string) []string {
	if reg == nil {
		return nil
	}
	registered := reg.RegisteredNames()
	selected := make(map[string]bool, len(registered))
	switch {
	case allTools:
		maps.Copy(selected, registered)
	case len(allowed) > 0:
		allowed = ensureRecoveryReader(allowed, reg)
		for _, name := range allowed {
			if registered[name] {
				selected[name] = true
			}
		}
	default:
		maps.Copy(selected, registered)
		for _, name := range denied {
			delete(selected, name)
		}
	}
	if watchParent && registered["job_watch"] {
		selected["job_watch"] = true
	}
	if registered[resultToolName] {
		selected[resultToolName] = true
	}
	for _, name := range protectedGrantTools() {
		delete(selected, name)
	}
	if !canDelegate {
		for _, name := range rootOnlySubagentTools() {
			if watchParent && name == "job_watch" {
				continue
			}
			delete(selected, name)
		}
	}
	if isolation == "worktree" && !canDelegate {
		delete(selected, "manage_worktree")
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func frozenStableDelegateSandboxMatches(env execenv.ExecutionEnvironment, want *delegatestore.SandboxSnapshot) bool {
	got := sandboxSnapshotFromEnv(env)
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	if got.Mode != want.Mode || got.WriteBlocked != want.WriteBlocked || !slices.Equal(got.DenylistAdd, want.DenylistAdd) || !slices.Equal(got.DenylistRemove, want.DenylistRemove) || !slices.Equal(got.ExtraWritableRoots, want.ExtraWritableRoots) || !slices.Equal(got.ExtraReadRoots, want.ExtraReadRoots) {
		return false
	}
	if got.Network == nil || want.Network == nil {
		return got.Network == nil && want.Network == nil
	}
	return *got.Network == *want.Network
}

func localEnvPolicyName(env execenv.ExecutionEnvironment) string {
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		return ""
	}
	switch le.EnvPolicy {
	case execenv.EnvPolicyAll:
		return "all"
	case execenv.EnvPolicyNone:
		return "none"
	case execenv.EnvPolicyCoreOnly:
		return "core_only"
	default:
		return "default"
	}
}

func localEnvPolicyFromName(name string) (execenv.EnvVarPolicy, bool) {
	switch strings.TrimSpace(name) {
	case "all":
		return execenv.EnvPolicyAll, true
	case "none":
		return execenv.EnvPolicyNone, true
	case "core_only":
		return execenv.EnvPolicyCoreOnly, true
	case "default":
		return execenv.EnvPolicyDefault, true
	default:
		return execenv.EnvPolicyDefault, false
	}
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return cloneShallowMap(in)
	}
	var out map[string]any
	// json.Marshal only returns syntactically valid JSON. Unmarshal into the
	// matching generic shape therefore cannot fail here.
	_ = json.Unmarshal(b, &out)
	return out
}

func cloneShallowMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

func subagentNeedsCommunicateNudge(agent *plugin.Agent) bool {
	if agent == nil {
		return true
	}
	return agent.PluginName == "builtin" && agent.Name == "subagent"
}

func restoreFrozenSkillBodies(skillNames, skillBodies []string) ([]string, error) {
	if len(skillNames) == 0 {
		if len(skillBodies) != 0 {
			return nil, errors.New("restore frozen skills: descriptor has skill bodies without skill names")
		}
		return nil, nil
	}
	if len(skillBodies) == 0 {
		return nil, errors.New("restore frozen skills: descriptor missing frozen skill bodies")
	}
	if len(skillBodies) != len(skillNames) {
		return nil, fmt.Errorf("restore frozen skills: descriptor has %d skill bodies for %d skill names", len(skillBodies), len(skillNames))
	}
	bodies := make([]string, 0, len(skillBodies))
	for i, body := range skillBodies {
		if strings.TrimSpace(body) == "" {
			return nil, fmt.Errorf("restore frozen skill %q: skill body unavailable", skillNames[i])
		}
		bodies = append(bodies, body)
	}
	return bodies, nil
}

// liveShellsUnderTree collects the handles of every live background-shell job
// rooted at or under path, across THIS session and every retained descendant
// session in its subagent tree (spec §P1 step 3). It exists because each
// session's jobManager.liveShellHandles() only sees its own running map: a shell
// launched inside a grandchild lives in the grandchild's manager and is
// invisible to a parent-only scan. The disposal op consumes this to distinguish
// a lane with genuine running work from one held open only by retained, idle
// delegate children (which liveWorkUnder labels honestly).
//
// The walk follows treeHasOutstandingWork's leaf-lock discipline: it enumerates
// direct children via liveDirectSubagents (which releases each sub.mu before
// returning), then descends into each child's Session, so no sub.mu (or
// jobManager mutex) is ever held across the recursion.
func (s *Session) liveShellsUnderTree(path string) []string {
	target := canonicalOrClean(path)
	var out []string
	s.collectLiveShellsUnderTree(target, &out)
	return out
}

func (s *Session) collectLiveShellsUnderTree(target string, out *[]string) {
	if s.jobManager != nil {
		for _, h := range s.jobManager.liveShellHandles() {
			if pathEqualOrUnder(canonicalOrClean(h.dir), target) {
				*out = append(*out, h.handle)
			}
		}
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.sess.collectLiveShellsUnderTree(target, out)
	}
}

// sharedWorkspaceDelegateWarning returns a non-blocking advisory when a new
// non-isolated delegate would join a running non-isolated sibling already
// working in this session's directory. The caller's isolation choice stays
// authoritative: this inspects no files, takes no locks beyond the
// manager-then-leaf discipline liveDirectSubagents already follows, creates no
// worktree, and never blocks the spawn.
//
// A delegate's working directory is not a caller-supplied parameter: a delegate
// either shares its parent's current directory or gets its own worktree lane, so
// the only comparison is the parent's live working directory against each live
// child's — which is also what catches a child that re-rooted itself.
func (s *Session) sharedWorkspaceDelegateWarning(requestedIsolation string) string {
	if s == nil || s.subagents == nil || strings.TrimSpace(requestedIsolation) != "" {
		return ""
	}
	parentEnv := s.currentEnv()
	if parentEnv == nil {
		return ""
	}
	parentDir := parentEnv.WorkingDirectory()
	target := canonicalOrClean(parentDir)
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		active := sub.running || sub.driving
		isolation := ""
		if sub.stableDescriptor != nil {
			isolation = sub.stableDescriptor.Isolation
		}
		sub.mu.Unlock()
		if !active || strings.TrimSpace(isolation) != "" {
			continue
		}
		env := sub.sess.currentEnv()
		if env != nil && canonicalOrClean(env.WorkingDirectory()) == target {
			// Name the directory the way the caller knows it, not its canonical
			// form: the comparison resolves symlinks, the advisory does not.
			return fmt.Sprintf(
				"shared workspace %q already has a running delegate; this delegate will still launch, "+
					"but consider isolation=\"worktree\" to avoid file, report, branch, and Git-state collisions",
				parentDir,
			)
		}
	}
	return ""
}

func (s *Session) spawnAgent(ctx context.Context, task, model, workingDir string, maxTurns int, agentType string, reasoningEffort string, parentTasks []taskpkg.TaskTemplate, grantTools []string) (any, error) {
	selection, err := s.selectSubagentModel(ctx, model, agentType)
	if err != nil {
		return "", err
	}
	if selection.warning != nil {
		s.emitDiagnosticWarning(*selection.warning)
	}
	prepared, err := s.prepareSubagentRunWithModelSelection(
		ctx, task, workingDir, maxTurns, agentType, reasoningEffort,
		parentTasks, grantTools, selection,
	)
	if err != nil {
		return "", err
	}
	// The legacy in-process spawn mints no delegate job, so no runningJob takes
	// ownership of the slot prepareSubagentRun reserved and no finalize/abandon
	// path can release it. The tree counter bounds delegate turns (spec §4); this
	// in-process turn is not one, so return the slot rather than leak it.
	releasePreparedTreeSlot(prepared)
	if hook := s.cfg.testOnly.subagentAfterPrepare; hook != nil {
		hook(s)
	}
	if err := s.trackAndLaunchPreparedSubagent(prepared); err != nil {
		prepared.disposeUnadopted()
		return "", err
	}

	b, _ := json.Marshal(map[string]any{"agent_id": prepared.sub.id, "status": string(SubagentRunning)})
	return string(b), nil
}

func (s *Session) prepareSubagentRun(ctx context.Context, task, model, workingDir string, maxTurns int, agentType string, reasoningEffort string, parentTasks []taskpkg.TaskTemplate, grantTools []string) (*preparedSubagentRun, error) {
	selection, err := s.selectSubagentModel(ctx, model, agentType)
	if err != nil {
		return nil, err
	}
	return s.prepareSubagentRunWithModelSelection(
		ctx, task, workingDir, maxTurns, agentType, reasoningEffort,
		parentTasks, grantTools, selection,
	)
}

func (s *Session) prepareSubagentRunWithModelSelection(
	ctx context.Context,
	task, workingDir string,
	maxTurns int,
	agentType, reasoningEffort string,
	parentTasks []taskpkg.TaskTemplate,
	grantTools []string,
	selection subagentModelSelection,
) (*preparedSubagentRun, error) {
	return s.prepareSubagentRunFromSelection(
		ctx, task, workingDir, maxTurns, agentType, reasoningEffort,
		parentTasks, grantTools, selection, nil, nil,
	)
}

func (s *Session) prepareStableDelegateRun(ctx context.Context, descriptor delegatestore.Descriptor, watchParent bool, selection subagentModelSelection, inheritedContext []transcript.Entry) (*preparedSubagentRun, error) {
	if selection.profile == nil || selection.profile.ID() != descriptor.ResolvedProfileID || selection.profile.Model() != descriptor.ResolvedModel {
		actual := "<nil>"
		if selection.profile != nil {
			actual = selection.profile.ID() + "/" + selection.profile.Model()
		}
		return nil, fmt.Errorf("committed delegate profile %s/%s is unavailable from frozen selection %s", descriptor.ResolvedProfileID, descriptor.ResolvedModel, actual)
	}
	if len(descriptor.ResultSchema) > 0 {
		var resultSchema map[string]any
		if err := json.Unmarshal(descriptor.ResultSchema, &resultSchema); err != nil {
			return nil, fmt.Errorf("decode committed delegate result schema: %w", err)
		}
		if len(resultSchema) > 0 {
			ctx = context.WithValue(ctx, ctxCommunicateOutputSchema, resultSchema)
		}
	}
	if watchParent {
		ctx = context.WithValue(ctx, ctxWatchParent, true)
	}
	selection.agent = nil
	return s.prepareSubagentRunFromSelection(
		ctx, descriptor.Task, descriptor.WorkingDir, 0, descriptor.AgentType, descriptor.Config.ReasoningEffort,
		nil, nil, selection, &descriptor, inheritedContext,
	)
}

// subagentConfigFromFrozenDescriptor rebuilds a stable delegate's SessionConfig
// from its frozen descriptor snapshot, re-taking every process-scoped field
// from the LIVE parent. The snapshot answers "what was this delegate asked to
// be"; anything about the process now hosting it — paths, credentials, clocks,
// test wiring, process lifetime — belongs to the restorer, and inheriting it
// from the freeze-time snapshot would carry one process's answer into another
// (a one-shot run's descriptor restored under serve, or the reverse).
func subagentConfigFromFrozenDescriptor(frozenConfig schema.ConfigSnapshot, parentCfg SessionConfig) SessionConfig {
	subCfg := configFromSnapshot(frozenConfig.Clone())
	subCfg.Project = parentCfg.Project
	subCfg.LifetimeContext = parentCfg.LifetimeContext
	subCfg.LLMRetryPolicy = parentCfg.LLMRetryPolicy
	subCfg.LLMSleep = parentCfg.LLMSleep
	subCfg.clock = parentCfg.clock
	subCfg.StateDir = parentCfg.StateDir
	subCfg.AcquireSessionOwnership = parentCfg.AcquireSessionOwnership
	subCfg.ExportATIFPath = parentCfg.ExportATIFPath
	subCfg.ExportATIFProviderHandles = parentCfg.ExportATIFProviderHandles
	subCfg.ResolveProfile = parentCfg.ResolveProfile
	subCfg.testOnly = parentCfg.testOnly
	subCfg.TurnEndsProcess = parentCfg.TurnEndsProcess
	subCfg.ForceRealIO = parentCfg.ForceRealIO
	subCfg.spawn.descendantEvent = parentCfg.spawn.descendantEvent
	subCfg.spawn.driveCounter = parentCfg.spawn.driveCounter
	subCfg.spawn.treeCounter = parentCfg.spawn.treeCounter
	subCfg.spawn.jobActivityClock = parentCfg.spawn.jobActivityClock
	return subCfg
}

func (s *Session) prepareSubagentRunFromSelection(
	ctx context.Context,
	task, workingDir string,
	maxTurns int,
	agentType, reasoningEffort string,
	parentTasks []taskpkg.TaskTemplate,
	grantTools []string,
	selection subagentModelSelection,
	frozen *delegatestore.Descriptor,
	inheritedContext []transcript.Entry,
) (*preparedSubagentRun, error) {
	s.mu.Lock()
	depth := s.depth
	parentCfg := s.cfg
	subscriberCount := s.subscriberCountFn
	s.mu.Unlock()
	agentType = strings.TrimSpace(agentType)
	agent := selection.agent
	subProfile := selection.profile

	subCfg := parentCfg
	if frozen != nil {
		subCfg = subagentConfigFromFrozenDescriptor(frozen.Config, parentCfg)
	}
	subCfg.artifactStore = s.artifactStore
	subCfg.MCPConfigFiles = nil
	subCfg.MCPInline = nil
	subCfg.spawn.sessionID = ""
	subCfg.spawn.parentJobActivity = nil
	subCfg.spawn.parentDelegateID = ""
	subCfg.spawn.delegateController = s.delegateController
	subCfg.spawn.delegateRootSessionID = s.delegateRootSessionID
	subCfg.spawn.owningDelegateID = ""
	subCfg.spawn.subscriberCount = subscriberCount
	subCfg.spawn.forwardJobEvent = nil
	subCfg.spawn.parentWatchGranted = false
	if frozen != nil {
		subCfg.spawn.rolePromptOverride = ""
		subCfg.spawn.activatedSkillBodies = nil
		subCfg.spawn.allowedToolNames = nil
		subCfg.spawn.deniedToolNames = nil
		subCfg.spawn.communicateOutputSchema = nil
		subCfg.spawn.isolation = frozen.Isolation
		subCfg.spawn.delegationAllowance = frozen.DelegationAllowance
	}
	subCfg.spawn.parentSessionID = s.id
	subCfg.spawn.subagentTask = task
	subCfg.spawn.inheritedContext = inheritedContext
	subCfg.spawn.depth = depth + 1
	subCfg.spawn.parentSteer = s.SteerWithProvenance
	subCfg.spawn.parentSystemNotification = s.routeSystemNotification
	if subCfg.ShareTasksWithChildren {
		ownerSessionID := parentCfg.spawn.sharedTaskStoreOwnerSessionID
		sharedStore := s.getOrCreateTaskStore()
		if frozen != nil {
			ownerSessionID = frozen.SharedTaskStoreOwnerSessionID
			var err error
			sharedStore, err = s.resolveStableSharedTaskStore(ownerSessionID)
			if err != nil {
				return nil, err
			}
		} else if ownerSessionID == "" {
			ownerSessionID = s.id
		}
		subCfg.spawn.sharedTaskStore = sharedStore
		subCfg.spawn.sharedTaskStoreOwnerSessionID = ownerSessionID
	} else {
		subCfg.spawn.sharedTaskStore = nil
		subCfg.spawn.sharedTaskStoreOwnerSessionID = ""
	}
	if callID, ok := ctx.Value(ctxToolCallID).(string); ok {
		subCfg.spawn.parentToolCallID = callID
	}
	if itemID, ok := ctx.Value(ctxToolItemID).(string); ok {
		subCfg.spawn.parentItemID = itemID
	}
	if delegateID, ok := ctx.Value(ctxParentDelegateID).(string); ok {
		subCfg.spawn.parentDelegateID = delegateID
		subCfg.spawn.owningDelegateID = delegateID
		if s.jobManager != nil {
			subCfg.spawn.forwardJobEvent = s.jobManager.forwardEvent
		}
	}
	if childSessionID, ok := ctx.Value(delegateChildSessionIDContextKey{}).(string); ok {
		subCfg.spawn.sessionID = childSessionID
	}
	if isolation, ok := ctx.Value(ctxIsolation).(string); ok {
		subCfg.spawn.isolation = isolation
	}
	// The granted delegation_allowance (validated by createDelegate against this
	// session's own allowance) shapes the child's grant capability. The delegate
	// path always sets it (0 = leaf); other spawn paths leave it at the inherited
	// default.
	if grantedAllowance, ok := ctx.Value(ctxDelegationAllowance).(int); ok {
		subCfg.spawn.delegationAllowance = grantedAllowance
	}
	if watchParent, ok := ctx.Value(ctxWatchParent).(bool); ok && watchParent {
		subCfg.spawn.parentWatchGranted = true
	}
	childCanDelegate := subCfg.spawn.delegationAllowance > 0
	if schema, ok := ctx.Value(ctxCommunicateOutputSchema).(map[string]any); ok && len(schema) > 0 {
		subCfg.spawn.communicateOutputSchema = schema
	}
	if frozen == nil {
		if maxTurns > 0 {
			subCfg.MaxTurns = maxTurns
		} else {
			subCfg.MaxTurns = 500
		}
		if reasoningEffort = strings.TrimSpace(reasoningEffort); reasoningEffort != "" {
			subCfg.ReasoningEffort = reasoningEffort
		}
	}
	canonicalGrantTools := s.canonicalizeToolNames(grantTools)

	// Determine agent name and role prompt for the subagent.
	// Named agents use their own SystemPrompt; unnamed agents get the "subagent" persona.
	var agentName string
	var rolePrompt string
	if frozen != nil {
		agentName = frozen.Config.AgentName
		rolePrompt = frozen.FrozenRolePrompt
	} else if agent != nil && strings.TrimSpace(agent.SystemPrompt) != "" {
		agentName = agent.Name
		rolePrompt = agent.SystemPrompt
	} else if agent == nil && childCanDelegate {
		agentName = "subagent"
		rolePrompt = defaultDelegatingSubagentInstructions
	} else if subagentAgent, ok := s.pluginAgents["subagent"]; ok {
		agentName = "subagent"
		rolePrompt = subagentAgent.SystemPrompt
	} else {
		agentName = "subagent"
		rolePrompt = defaultSubagentInstructions
	}
	subCfg.AgentName = agentName // ensure subagent gets its own tasks, not parent's

	if frozen != nil {
		subCfg.spawn.rolePromptOverride = rolePrompt
	} else if (agent != nil && strings.TrimSpace(agent.SystemPrompt) != "" && agent.PluginName != "builtin") ||
		(agent == nil && childCanDelegate) {
		subCfg.spawn.rolePromptOverride = rolePrompt
	}
	var activatedSkillNames []string
	var activatedSkillBodies []string
	if frozen != nil {
		var err error
		activatedSkillBodies, err = restoreFrozenSkillBodies(frozen.FrozenSkillNames, frozen.FrozenSkillBodies)
		if err != nil {
			return nil, err
		}
		activatedSkillNames = append([]string(nil), frozen.FrozenSkillNames...)
		subCfg.spawn.activatedSkillBodies = append([]string(nil), activatedSkillBodies...)
	} else if agent != nil && len(agent.Skills) > 0 {
		for _, skillName := range agent.Skills {
			body, err := skill.ResolveSkillContent(s.skills, skillName)
			if fault := s.subagentPrepareFault("skill_resolve"); fault != nil {
				err = fault
			}
			if err != nil {
				continue
			}
			if strings.TrimSpace(body) != "" {
				subCfg.spawn.activatedSkillBodies = append(subCfg.spawn.activatedSkillBodies, body)
				activatedSkillNames = append(activatedSkillNames, skillName)
				activatedSkillBodies = append(activatedSkillBodies, body)
			}
		}
	}

	var allTools bool
	var allowedTools, deniedTools []string
	if frozen != nil {
		allowedTools = append([]string(nil), frozen.ToolNameCeiling...)
		subCfg.spawn.toolNameCeiling = append([]string(nil), allowedTools...)
	} else {
		// The policy follows the CHILD's granted allowance, not this session's:
		// a leaf spawned by a coordinator must not inherit the coordinator's
		// job-supervision tools.
		allTools, allowedTools, deniedTools = baseSubagentToolPolicy(agent, childCanDelegate)
		if subCfg.spawn.parentWatchGranted && !allTools {
			if len(allowedTools) > 0 {
				allowedTools = appendUniqueStrings(allowedTools, "job_watch")
			} else {
				deniedTools = removeStrings(deniedTools, []string{"job_watch"})
			}
		}
	}
	if frozen == nil && len(canonicalGrantTools) > 0 {
		currentTools := s.reg.RegisteredNames()
		for _, toolName := range canonicalGrantTools {
			if isProtectedGrantTool(toolName) {
				return nil, fmt.Errorf("%s is root-only and cannot be granted to subagents", toolName)
			}
			if isRootOnlySubagentTool(toolName) {
				return nil, fmt.Errorf("cannot grant tool %q via grant_tools: delegation tools are enabled by the delegate tool's delegation_allowance parameter, not grant_tools", toolName)
			}
			baseHasTool := allTools ||
				hasString(allowedTools, toolName) ||
				(len(allowedTools) == 0 && !hasString(deniedTools, toolName))
			if baseHasTool {
				continue
			}
			if !currentTools[toolName] {
				return nil, fmt.Errorf("cannot grant tool %q: it is not currently callable in this session", toolName)
			}
			if len(allowedTools) > 0 {
				allowedTools = appendUniqueStrings(allowedTools, toolName)
			}
		}
	}
	if frozen == nil && !allTools {
		allowedTools = ensureRecoveryReader(allowedTools, s.reg)
	}

	if frozen != nil {
		// The descriptor's ceiling was captured from the effective parent registry
		// before CommitStart. NewSession intersects it with the complete registry the
		// child can build, including intrinsic recovery tools, before caching tools.
	} else if allTools {
		// Leave the registry unrestricted for explicit all-tools agents.
	} else if len(allowedTools) > 0 {
		subCfg.spawn.allowedToolNames = append([]string(nil), allowedTools...)
	} else {
		subCfg.spawn.deniedToolNames = append([]string(nil), deniedTools...)
	}

	var reqSandbox *sandbox.SandboxPolicy
	if v, ok := ctx.Value(ctxDelegateSandboxPolicy).(*sandbox.SandboxPolicy); ok {
		reqSandbox = v
	}
	if reqSandbox == nil && subagentToolScopeIsReadOnly(allTools, allowedTools) {
		var sandboxErr error
		reqSandbox, sandboxErr = s.readOnlyDelegateSandbox()
		if sandboxErr != nil {
			return nil, fmt.Errorf("read-only delegate sandbox: %w", sandboxErr)
		}
	}
	preparedEnv, hasPreparedEnv := ctx.Value(delegatePreparedEnvironmentContextKey{}).(delegatePreparedEnvironment)
	subEnv := preparedEnv.env
	ownsFreshEnv := preparedEnv.ownsFresh
	if !hasPreparedEnv {
		var err error
		subEnv, ownsFreshEnv, err = s.prepareSubagentEnvironment(workingDir, reqSandbox)
		if err != nil {
			return nil, err
		}
	}
	if frozen != nil {
		if subEnv == nil || subEnv.WorkingDirectory() != frozen.WorkingDir || localEnvPolicyName(subEnv) != frozen.LocalEnvPolicy || !frozenStableDelegateSandboxMatches(subEnv, frozen.Sandbox) {
			actualWorkingDir := ""
			if subEnv != nil {
				actualWorkingDir = subEnv.WorkingDirectory()
			}
			return nil, fmt.Errorf("committed delegate environment is unavailable: cwd %q/%q policy %q/%q", actualWorkingDir, frozen.WorkingDir, localEnvPolicyName(subEnv), frozen.LocalEnvPolicy)
		}
	}

	if schema := subCfg.spawn.communicateOutputSchema; len(schema) > 0 {
		subProfile = provider.WithCommunicateOutputSchema(subProfile, schema)
	}

	// Each child gets its own client when a factory is injected (the fuzz
	// harness's per-child adapter seam); production leaves it nil and shares the
	// parent's client.
	childClient := s.client
	if factory := parentCfg.testOnly.childClientFactory; factory != nil {
		childClient = factory()
	}
	var subSess *Session
	var err error
	if fault := s.subagentPrepareFault("new_session"); fault != nil {
		err = fault
	} else {
		subSess, err = NewSession(childClient, subProfile, subEnv, subCfg)
	}
	if err != nil {
		// A fresh environment that failed before session adoption never reaches the
		// session cleanup path, so dispose every scratch it provisioned: the
		// sandbox-owned one AND the one an unsandboxed environment mints on its
		// first command, which the construction above reaches through its own git
		// snapshot. A prepared environment belongs to whoever prepared it.
		if ownsFreshEnv && !hasPreparedEnv {
			disposeUnadoptedScratch(subEnv)
		}
		return nil, err
	}
	// The child owns a fresh env iff we re-rooted to a lane and/or enforced a
	// per-delegate sandbox; otherwise subEnv is the shared parent env.
	subSess.recordEnvironmentOwnership(subEnv, ownsFreshEnv)
	disposeUnadopted := func() {
		disposeUnadoptedSubagentSession(subSess)
	}
	if len(canonicalGrantTools) > 0 {
		var missing []string
		for _, toolName := range canonicalGrantTools {
			if subSess.reg.Get(toolName) == nil {
				missing = append(missing, toolName)
			}
		}
		if len(missing) > 0 {
			disposeUnadopted()
			return nil, fmt.Errorf("cannot grant tool(s) to spawned subagent: %s", strings.Join(missing, ", "))
		}
	}

	// Populate default tasks from agent definition + parent tasks.
	var defaultTasks []taskpkg.TaskTemplate
	if frozen != nil {
		defaultTasks = append(defaultTasks, frozen.TaskTemplates...)
	} else if agent != nil {
		defaultTasks = agent.Tasks
	}
	if len(defaultTasks) > 0 {
		subStore := subSess.getOrCreateTaskStore()
		populateErr := subStore.MutateAndPublish(func(epoch, revision uint64) error {
			err := subStore.PopulateFromTemplates(defaultTasks, parentTasks)
			if fault := s.subagentPrepareFault("task_populate"); fault != nil {
				err = fault
			}
			if err != nil {
				return err
			}
			summary := taskpkg.Summarize(subStore.View())
			subSess.emit(events.EventTaskUpdated, taskUpdatedData(summary, subSess.taskStoreOwnerSessionID(), epoch, revision))
			return nil
		})
		if err := populateErr; err != nil {
			// Non-fatal: surface as a warning so the spawn still proceeds but the
			// failure is observable instead of silently swallowed.
			s.emit(events.EventWarning, warningDataFromError("failed to populate subagent tasks from templates", err))
		}
		// Inject the first task's prompt as a steering message.
		if current, ok := subStore.CurrentInProgress(); ok {
			subSess.SteerKind(formatCurrentTaskSteering(current, subSess.canInstructTool("task_list")), events.SteeringKindCurrentTask)
		}
	}

	// Enforce the retained-terminal cap before tracking. Done AFTER NewSession so
	// the child is fully built, but a failure must not leak it: dispose the created
	// session and any unadopted fresh environment before returning.
	// On success, GC-evicted records are closed here, OUTSIDE the manager mutex.
	if !preparedEnv.stableController {
		var evicted []*subagent
		if reserve := s.cfg.testOnly.subagentReserveSlot; reserve != nil {
			evicted, err = reserve(s)
		} else {
			evicted, err = s.subagents.reserveSlot()
		}
		if err != nil {
			disposeUnadopted()
			return nil, err
		}
		for _, ev := range evicted {
			teardownChildSession(context.Background(), ev.sess, retainChildScratch)
		}
	}

	// Reserve a tree-counter slot for this spawn's running delegate turn (spec §4).
	// At capacity the spawn does not launch: dispose the freshly built child and
	// any unadopted environment (the same cleanup as the retained-cap path), then
	// surface the tree_at_capacity error to the tool call. Ownership of the reservation
	// transfers to the delegate runningJob at attach; an unattached prepared run
	// releases it (releasePreparedTreeSlot).
	var treeSlot *treeReservation
	var ok bool
	if !preparedEnv.stableController {
		if reserve := s.cfg.testOnly.subagentReserveTreeSlot; reserve != nil {
			treeSlot, ok = reserve(s)
		} else {
			treeSlot, ok = s.reserveTreeSlot(slotKindJob)
		}
		if !ok {
			disposeUnadopted()
			return nil, s.treeCapacityErrorFor()
		}
	}

	now := s.sclock().Now()
	nudgeEnabled := subagentNeedsCommunicateNudge(agent)
	if frozen != nil {
		nudgeEnabled = frozen.Config.AgentName == "subagent"
	}
	sub := &subagent{
		id:           subSess.id,
		sess:         subSess,
		emit:         s.emit,
		running:      true,
		status:       SubagentRunning,
		done:         make(chan struct{}),
		nudgeEnabled: nudgeEnabled,
		agentType:    agentType,
		createdAt:    now,
		startedAt:    now,
	}
	if frozen != nil {
		descriptor := cloneDelegateStartDescriptor(*frozen)
		sub.stableDescriptor = &descriptor
	}

	// Drive-down wake (spec §3): a child's notify must reach its parent, which
	// drives the child's own drain loop for a notification turn. This is the
	// parent-side analog of serve.go's SetNotifyFunc on the root — the child's
	// jm.wake (= subSess.notify) fires whenever the child arms a job notification
	// (enqueueJobNotificationAndNotify), so the parent learns of undelivered
	// attention on an idle child and drives it. The drive is launched only when
	// the child is live and idle (driveSubagentNotificationTurn's guard) and not
	// stop-gated (a deliberately stopped child is never resurrected by a wake for
	// pre-stop attention — spec §3 stop-gating).
	subSess.SetNotifyFunc(func() { s.driveChildIfNotStopGated(sub) })

	// Subagent execution must outlive the parent tool-call context.
	// The parent may stop waiting, finish its input, or time out while the
	// child keeps running. Child cancellation is handled by subSess.Close(),
	// including when the parent session closes. The per-run context lets
	// parent stops interrupt this run without destroying the child session.
	runCtx, runCancel := context.WithCancel(s.sessionCtx)
	sub.mu.Lock()
	sub.cancel = runCancel
	sub.cancelRequested = false
	sub.mu.Unlock()
	prepared := &preparedSubagentRun{
		sub:                sub,
		input:              task,
		runCtx:             runCtx,
		runCancel:          runCancel,
		parentSessionID:    subCfg.spawn.parentSessionID,
		originToolCallID:   subCfg.spawn.parentToolCallID,
		originItemID:       subCfg.spawn.parentItemID,
		task:               task,
		agentType:          agentType,
		requestedModel:     selection.requestedModel,
		resolvedAgentName:  agentName,
		reasoningEffort:    reasoningEffort,
		frozenRolePrompt:   rolePrompt,
		frozenToolNames:    frozenSubagentToolNames(allTools, allowedTools, deniedTools),
		frozenSkillNames:   append([]string(nil), activatedSkillNames...),
		frozenSkillBodies:  append([]string(nil), activatedSkillBodies...),
		workingDir:         subEnv.WorkingDirectory(),
		localEnvPolicy:     localEnvPolicyName(subEnv),
		sandboxSnapshot:    sandboxSnapshotFromEnv(subEnv),
		isolation:          subCfg.spawn.isolation,
		resultSchema:       cloneMap(subCfg.spawn.communicateOutputSchema),
		explicitToolGrants: append([]string(nil), canonicalGrantTools...),
		treeSlot:           treeSlot,
	}
	if frozen != nil {
		prepared.task = frozen.Task
		prepared.agentType = frozen.AgentType
		prepared.requestedModel = frozen.RequestedModel
		prepared.resolvedAgentName = frozen.Config.AgentName
		prepared.reasoningEffort = frozen.Config.ReasoningEffort
		prepared.frozenRolePrompt = frozen.FrozenRolePrompt
		prepared.frozenToolNames = append([]string(nil), frozen.ToolNameCeiling...)
		prepared.frozenSkillNames = append([]string(nil), frozen.FrozenSkillNames...)
		prepared.frozenSkillBodies = append([]string(nil), frozen.FrozenSkillBodies...)
		prepared.workingDir = frozen.WorkingDir
		prepared.localEnvPolicy = frozen.LocalEnvPolicy
		prepared.isolation = frozen.Isolation
		prepared.resultSchema = cloneMap(subCfg.spawn.communicateOutputSchema)
	} else if agent != nil && len(agent.Tasks) > 0 {
		if current, ok := subSess.getOrCreateTaskStore().CurrentInProgress(); ok {
			prepared.frozenTaskPrompt = current.Prompt
		}
	}
	return prepared, nil
}

func (s *Session) trackAndLaunchPreparedSubagent(prepared *preparedSubagentRun) error {
	if prepared == nil || prepared.sub == nil {
		return errors.New("subagent run is not prepared")
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		prepared.runCancel()
		return errors.New("session is closed")
	}
	s.subagents.track(prepared.sub)
	s.sendersWG.Add(1)
	s.mu.Unlock()

	s.launchSubagentRun(prepared.runCtx, prepared.sub, prepared.runCancel, prepared.input, s.activeCausalProvenance())
	return nil
}

func (s *Session) noteParentJobActivity(phase string) {
	if s == nil || s.cfg.spawn.parentJobActivity == nil {
		return
	}
	parentID := s.cfg.spawn.parentDelegateID
	if parentID != "" {
		s.cfg.spawn.parentJobActivity(parentID, phase)
	}
}

// markSalvagedTurnPersisted records that Component 3 settlement just appended
// a salvaged assistant turn to this session's transcript, stamping the round
// it happened in (totalRounds). Called only from persistSalvagedTurn, on the
// success path, so it can never stamp a draft that never actually reached
// the transcript. Overwritten on every subsequent salvage — see
// hasSalvageFromFinalRound for why the round stamp matters.
func (s *Session) markSalvagedTurnPersisted() {
	s.mu.Lock()
	s.salvagedTurnRound = s.totalRounds
	s.mu.Unlock()
}

// hasSalvageFromFinalRound reports whether this session's transcript holds a
// Component 3 salvaged turn from the LAST round the session ever ran — the
// round whose failure actually ended it, not some earlier settlement the
// session ran past on its way to a later, unrelated failure. A delegating
// parent calls this on a failed child's session at finalize time to decide
// whether the failed delegate result should point at resuming the draft
// (delegate_send).
//
// The round scope matters: a session can salvage a transient stall on round
// 2, run several more rounds, and finally die of context length on round 8 —
// a class Component 3 deliberately excludes from salvage/steering, because
// appending more input to an already-overflowing history makes it worse.
// Recommending delegate_send off the stale round-2 salvage would defeat that
// exclusion by a side door. Comparing salvagedTurnRound to the CURRENT
// totalRounds (read after the child has fully stopped, so both are stable)
// is exact: equal means nothing ran after the salvage, so it explains why
// the session stopped; unequal means the salvage is stale.
func (s *Session) hasSalvageFromFinalRound() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.salvagedTurnRound != 0 && s.salvagedTurnRound == s.totalRounds
}

func (s *Session) sendInput(ctx context.Context, agentID string, input string) (any, error) {
	_ = ctx
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}
	if _, err := s.startOrSteerSubagentRun(sub, input); err != nil {
		return "", err
	}
	return "ok", nil
}

func (s *Session) startOrSteerSubagentRun(sub *subagent, input string) (bool, error) {
	sub.mu.Lock()
	if sub.finalizing || (sub.fatalRunGated && (sub.running || sub.driving)) {
		sub.mu.Unlock()
		return false, fmt.Errorf("target_busy: delegate %q is completing its current run; retry", sub.id)
	}
	if sub.running || sub.driving {
		subSess := sub.sess
		sub.mu.Unlock()
		// Inject as steering message into the in-flight session. A mid-drive child
		// (sub.driving) is in flight just like a running one: the drive turn absorbs
		// the steered input at its tool-round boundary (spec §3, A7 steer-into-drive)
		// rather than launching a second concurrent run.
		subSess.SteerKind(input, events.SteeringKindAgentMessage)
		return false, nil
	}

	// Agent is idle — start a new ProcessInput round. Enroll the resumed run in
	// sendersWG under s.mu (gated on closing) so it joins the same teardown
	// barrier as the initial spawn.
	runCtx, runCancel := context.WithCancel(context.Background())
	resumeTime := s.sclock().Now()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		sub.mu.Unlock()
		runCancel()
		return false, errors.New("session is closed")
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()

	sub.fatalRunGated = false
	resetSubagentForRunLocked(sub, runCancel, resumeTime)
	sub.mu.Unlock()

	s.launchSubagentRun(runCtx, sub, runCancel, input, s.activeCausalProvenance())
	return true, nil
}

// trySetDisposeGate arms the dispose gate on a quiescent retained child, but only
// after re-verifying under sub.mu that no run or drive turn is live. It returns
// false (gate NOT set) when the child is running or driving — a drive/resume that
// raced the dispose op wins, and the caller must refuse the dispose. On success
// the child is frozen: driveSubagentNotificationTurn and the retained delegate_send
// path refuse until clearDisposeGate reverses it.
func (a *subagent) trySetDisposeGate() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running || a.driving {
		return false
	}
	a.disposeGated = true
	return true
}

// clearDisposeGate reverses trySetDisposeGate. The dispose op calls it on every
// pre-eviction refusal/failure exit so a child that survives disposal is drivable
// and resumable again.
func (a *subagent) clearDisposeGate() {
	a.mu.Lock()
	a.disposeGated = false
	a.mu.Unlock()
}

// driveTurnTimeout bounds a single drive-down notification turn: a hung child
// turn is cancelled and its drive slot freed rather than pinned until parent
// close. A var so tests can shrink it.
var driveTurnTimeout = 5 * time.Minute

// driveRedriveMinInterval paces the post-turn re-drive: when attention remains
// after a drive turn, the next drive of the same child waits this long instead
// of launching immediately, so a child whose attention never drains cannot
// hot-loop its budget slot. A var so tests can shrink it.
var driveRedriveMinInterval = 1 * time.Second

// driveSubagentNotificationTurn launches ONE EntryNotification turn on a live,
// idle direct child whose own loop has undelivered attention (spec §3 drive-down:
// a parent never renders a non-owned job's notification; it runs the child whose
// own loop delivers). It is the parent-side analog of serve.go's notify→
// EntryNotification wake, but driven internally by the parent at its loop
// boundaries. NO delegate job record is minted and the child's terminal subagent
// record is left intact — this is the child processing its OWN notification queue
// (acceptNotificationInput drains it), not new tasked work or a delegate resume.
//
// Concurrency discipline (T11-T13): the live/idle guard reads sub.closed and
// sub.running under sub.mu; a child that is closing, mid-run, or already being
// driven is skipped so no run goroutine is launched on a torn-down or active
// child. The driving flag makes the launch idempotent — exactly one drive turn is
// in flight per child at a time (the EntryNotification drain loop self-services
// every queued notification within that single turn, so one launch suffices). No
// session or jobManager lock is held while ProcessInputKind runs.
// driveSubagentNotificationTurn returns true when it launches a drive turn
// (a successful handoff) and false when the live/idle guard skips the child
// (closed, mid-run, or already being driven). The caller uses the handoff result
// to settle the parent's forwarded drive signal (spec §3 settle).
func (s *Session) driveSubagentNotificationTurn(sub *subagent) bool {
	if sub == nil {
		return false
	}
	sub.mu.Lock()
	if sub.sess == nil || sub.closed || sub.running || sub.driving || sub.disposeGated || sub.fatalRunGated || sub.finalizing {
		sub.mu.Unlock()
		return false
	}
	// Reserve a drive-budget slot for the drive turn: a drive launches a running
	// delegate turn (the child's EntryNotification render) and holds the slot for
	// its duration. Drive turns budget separately from spawns (driveCounter), so
	// notification maintenance never starves user fan-out. At capacity the drive
	// does not launch — return false, the not-launched signal the caller already
	// honors (no settle), so the child's durable ledger stays queued and the next
	// loop boundary retries (spec §3).
	treeSlot, ok := s.reserveDriveSlot()
	if !ok {
		sub.mu.Unlock()
		return false
	}
	// The drive ctx is parented to the session's lifetime ctx (cancelled by
	// cancelFunc at the very start of close(), before child closes and before
	// sendersWG.Wait) — NOT to context.Background(): the drive goroutine holds
	// s.sendersWG, so a drive whose ctx nothing cancels on teardown parks
	// session Close until driveTurnTimeout (5m) whenever the child turn or the
	// paced re-drive wait below is blocked on something close cannot otherwise
	// reach (e.g. a frozen session clock: sclock().After never fires). Close
	// cancelling sessionCtx both interrupts the child's ProcessInputKind turn
	// and fires the driveCtx.Done() arm of the re-drive select, so sendersWG
	// drains promptly and the Close comment "already cancelled above" is true
	// again. The ctx is still independent of the LAUNCHING turn's ctx:
	// sessionCtx outlives every turn and dies only at Close.
	driveCtx, driveCancel := context.WithTimeout(s.sessionCtx, driveTurnTimeout)
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		sub.mu.Unlock()
		treeSlot.release()
		driveCancel()
		return false
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()
	sub.driving = true
	childSess := sub.sess
	sub.mu.Unlock()

	go func() {
		defer s.sendersWG.Done()
		defer driveCancel()
		// The re-check defer is registered BEFORE treeSlot.release so LIFO runs
		// the release first: the paced re-drive wait below holds no slot, and
		// the re-drive can claim one even at drive budget 1.
		defer func() {
			sub.mu.Lock()
			sub.driving = false
			sub.mu.Unlock()
			s.redriveChildIfAttentionRemains(driveCtx, sub, childSess)
		}()
		defer treeSlot.release()
		_, err := childSess.ProcessInputKind(driveCtx, "", nil, EntryNotification)
		if err != nil {
			if cleanupErr := sub.gateFatalRun(err); cleanupErr != nil {
				childSess.emit(events.EventWarning, warningDataFromError("delegate owned-work cleanup incomplete", cleanupErr))
			}
		}
	}()
	return true
}

// redriveChildIfAttentionRemains is the post-turn re-drive check (spec §3): a
// notify that fired while driving==true was dropped at the live/idle guard, so
// a notification landing after the child's final in-turn peek would otherwise
// strand (the parent is idle and there is no autonomous poll). It runs AFTER
// sub.driving is cleared so the inner guard passes, and AFTER the drive slot
// is released so the wait holds no budget. It is idempotent and
// self-terminating: it re-drives only when real attention remains, each
// re-drive is a fresh goroutine, and it stops when
// peek==0 && !hasPendingWatchSends. The re-drive is paced: it waits
// driveRedriveMinInterval (or drive cancel) before launching so a child whose
// attention never drains cannot hot-loop its budget slot.
func (s *Session) redriveChildIfAttentionRemains(driveCtx context.Context, sub *subagent, childSess *Session) {
	if sub == nil || childSess == nil {
		return
	}
	if sub.fatalRunGatedSnapshot() {
		return
	}
	stopGated := s.childStopGated(childSess.id)
	if hook := s.cfg.testOnly.subagentStopGated; hook != nil {
		if stopped, handled := hook(s, childSess.id); handled {
			stopGated = stopped
		}
	}
	if stopGated {
		return
	}
	if childSess.peekNotifications() > 0 || (childSess.jobManager != nil && childSess.jobManager.hasPendingWatchSends()) {
		select {
		case <-s.sclock().After(driveRedriveMinInterval):
		case <-driveCtx.Done():
			return
		}
		if s.driveSubagentNotificationTurn(sub) {
			s.settleDrivenChildForwardedPendings(childSess.id)
		}
	}
}

func (a *subagent) gateFatalRun(err error) error {
	if a == nil || err == nil {
		return nil
	}
	a.mu.Lock()
	a.fatalRunGated = true
	a.mu.Unlock()
	// A failed child turn must not leave owned managed work alive. Preserve the
	// retained run's error/state even if stopping races with a job-manager error.
	if a.sess != nil {
		_, cleanupErr := a.sess.stopDelegateSubtreeAndWait(a.sess)
		return cleanupErr
	}
	return nil
}

func (a *subagent) gateFatalRunError(err error) error {
	if cleanupErr := a.gateFatalRun(err); cleanupErr != nil {
		return errors.Join(err, fmt.Errorf("stop delegate-owned work: %w", cleanupErr))
	}
	return err
}

func (s *Session) subagentPrepareFault(point string) error {
	if hook := s.cfg.testOnly.subagentPrepareFault; hook != nil {
		return hook(point)
	}
	return nil
}

func resetSubagentForRunLocked(sub *subagent, cancel context.CancelFunc, startedAt time.Time) {
	sub.done = make(chan struct{})
	sub.running = true
	sub.finalizing = false
	sub.status = SubagentRunning
	sub.result = ""
	sub.err = nil
	sub.resultConsumed = false
	sub.endEmitted = false
	sub.runProvenance = nil
	sub.runStructured = nil
	sub.runStructuredCaptured = false
	sub.cancel = cancel
	sub.cancelRequested = false
	sub.settlementClaimed = false
	sub.startedAt = startedAt
	sub.endedAt = nil
	sub.closed = false
	sub.closeTimedOut = false
}

func (s *Session) launchSubagentRun(runCtx context.Context, sub *subagent, runCancel context.CancelFunc, input string, inputProvenance *provenance.Causal) {
	// Resume runs should also be independent of the caller's wait context.
	go func() {
		defer s.sendersWG.Done()
		defer runCancel()
		sub.run(runCtx, input, inputProvenance)
	}()
}

func (s *Session) cancelAgent(agentID string) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}
	sub.mu.Lock()
	if !sub.running {
		sub.mu.Unlock()
		return "", fmt.Errorf("agent %s is not running", agentID)
	}
	if sub.settlementClaimed {
		sub.mu.Unlock()
		return "", fmt.Errorf("agent %s is completing its current run", agentID)
	}
	sub.cancelRequested = true
	cancel := sub.cancel
	done := sub.done
	sub.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
	case <-s.sclock().After(5 * time.Second):
		return "", fmt.Errorf("timed out cancelling subagent %s", agentID)
	}
	sub.mu.Lock()
	result := sub.resultSnapshotLocked()
	sub.resultConsumed = true
	sub.mu.Unlock()
	b, _ := json.Marshal(result)
	return string(b), nil
}

func (s *Session) getSub(agentID string) *subagent {
	return s.subagents.get(agentID)
}

func (a *subagent) fatalRunGatedSnapshot() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fatalRunGated
}

func (s *Session) childFatalRunGated(childSessionID string) bool {
	if s == nil || s.subagents == nil || childSessionID == "" {
		return false
	}
	sub := s.getSub(childSessionID)
	return sub != nil && sub.fatalRunGatedSnapshot()
}

// communicateNudge returns the message sent to a subagent that stops without
// calling the result tool. Sent at most once.
func communicateNudge(toolName string) string {
	return "You stopped without calling " + toolName + ". " +
		"You MUST call " + toolName + " with end_turn=true and a message summarizing your complete findings " +
		"before stopping. The parent agent receives ONLY the " + toolName + " message — " +
		"it cannot see anything else you did. Report your results now."
}

func (a *subagent) run(ctx context.Context, input string, inputProvenance *provenance.Causal) {
	kind := EntryUserInput
	if _, attention := ctx.Value(delegateAttentionRunContextKey{}).(struct{}); attention {
		kind = EntryDelegateAttention
	}
	lease, stableRun := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease)
	nudgeAvailable := true
	hookAvailable := true
	var res string
	var err error
	var restoreParentDriveNotify func()
	var finish delegateFinish
	var noActionClaim *delegateSettlementClaim
	iteration := 0
	for {
		iteration++
		if observer := a.sess.cfg.testOnly.subagentRunIteration; observer != nil {
			observer(a, iteration)
		}
		res, err = a.sess.processInputKindWithProvenance(ctx, input, nil, kind, inputProvenance)
		_, budgetExhausted := budgetExhaustionFromError(err)
		a.mu.Lock()
		cancelRequested := a.cancelRequested
		a.mu.Unlock()
		settlementMode := delegateSettlementModeForRun(err, cancelRequested)
		if stableRun && a.sess.delegateController != nil {
			boundary, boundaryErr := a.sess.delegateController.SupervisionBoundary(lease, settlementMode)
			if boundaryErr != nil && !errors.Is(boundaryErr, errDelegateTargetBusy) {
				err = errors.Join(err, boundaryErr)
			}
			switch boundary {
			case delegateSupervisionContinue:
				input = "Continue with the newly received steering before settling."
				kind = EntryContinuation
				continue
			case delegateSupervisionSuppress:
				cancelRequested = true
			}
		}
		var needsNudge bool
		if stableRun && a.sess.delegateController != nil {
			decision, decisionErr := a.sess.delegateController.completionDecision(lease)
			if decisionErr != nil {
				err = errors.Join(err, decisionErr)
				needsNudge = false
			} else {
				needsNudge = decision == delegateCompletionNeedsNudge
			}
		} else {
			needsNudge = !a.sess.Communicated()
		}
		shouldNudge := nudgeAvailable && !cancelRequested &&
			!budgetExhausted &&
			a.nudgeEnabled &&
			needsNudge &&
			(err == nil || errors.Is(err, errBareTextWithoutResultTool) || errors.Is(err, errEmptyResponseExhausted))
		if shouldNudge {
			nudgeAvailable = false
			res, err = a.sess.processInputWithProvenance(ctx, communicateNudge(a.sess.resultToolName()), nil, a.followUpProvenance(inputProvenance))
			_, budgetExhausted = budgetExhaustionFromError(err)
		}
		if hookAvailable && !cancelRequested && !budgetExhausted {
			hookAvailable = false
			res, err = a.runSubagentStopHook(ctx, res, err, a.followUpProvenance(inputProvenance))
		}
		restoreParentDriveNotify = nil
		if err == nil {
			res, restoreParentDriveNotify, err = a.drainForFinalization(ctx, res)
		}
		if stableRun && a.sess.delegateController != nil && nudgeAvailable && !cancelRequested && !budgetExhausted && a.nudgeEnabled &&
			(err == nil || errors.Is(err, errBareTextWithoutResultTool) || errors.Is(err, errEmptyResponseExhausted)) {
			decision, decisionErr := a.sess.delegateController.completionDecision(lease)
			if decisionErr != nil {
				err = errors.Join(err, decisionErr)
			} else if decision == delegateCompletionNeedsNudge {
				nudgeAvailable = false
				if restoreParentDriveNotify != nil {
					a.sess.SetNotifyFunc(restoreParentDriveNotify)
					restoreParentDriveNotify = nil
				}
				a.mu.Lock()
				a.finalizing = false
				a.mu.Unlock()
				res, err = a.sess.processInputWithProvenance(ctx, communicateNudge(a.sess.resultToolName()), nil, a.followUpProvenance(inputProvenance))
				a.mu.Lock()
				cancelRequested = a.cancelRequested
				a.mu.Unlock()
				settlementMode = delegateSettlementModeForRun(err, cancelRequested)
				boundary, boundaryErr := a.sess.delegateController.SupervisionBoundary(lease, settlementMode)
				if boundaryErr != nil && !errors.Is(boundaryErr, errDelegateTargetBusy) {
					err = errors.Join(err, boundaryErr)
				}
				switch boundary {
				case delegateSupervisionContinue:
					input = "Continue with the newly received steering before settling."
					kind = EntryContinuation
					continue
				case delegateSupervisionSuppress:
				}
				if err == nil {
					res, restoreParentDriveNotify, err = a.drainForFinalization(ctx, res)
				}
			}
		}
		if observer := a.sess.cfg.testOnly.subagentBeforeSettlement; observer != nil {
			observer(a)
		}
		a.mu.Lock()
		cancelRequested = a.cancelRequested
		if stableRun && a.sess.delegateController != nil {
			a.settlementClaimed = true
		}
		a.mu.Unlock()
		if cancelRequested && err == nil {
			err = context.Canceled
		}
		settlementMode = delegateSettlementModeForRun(err, cancelRequested)
		if !stableRun || a.sess.delegateController == nil {
			if err != nil {
				err = a.gateFatalRunError(err)
			}
			finish = a.stableDelegateFinish(res, err)
			break
		}
		settlementClaim, continueRun, settleErr := a.sess.delegateController.BeginRunFinalization(lease, settlementMode, err)
		if settleErr != nil {
			if !errors.Is(settleErr, errDelegateTargetBusy) {
				err = errors.Join(err, settleErr)
			}
			if settlementMode == delegateSettlementTerminal {
				if stableDelegateFatalRun(err) {
					err = a.gateFatalRunError(err)
				}
			} else if err != nil {
				err = a.gateFatalRunError(err)
			}
			finish = a.stableDelegateFinish(res, err)
			break
		}
		if continueRun {
			if restoreParentDriveNotify != nil {
				a.sess.SetNotifyFunc(restoreParentDriveNotify)
			}
			a.mu.Lock()
			a.finalizing = false
			a.settlementClaimed = false
			a.mu.Unlock()
			input = "Continue with the newly received steering before settling."
			kind = EntryContinuation
			continue
		}
		<-settlementClaim.ready
		attentionPlans, attentionErr := a.sess.delegateController.AttentionResolutionsForFinalization(settlementClaim)
		if executeErr := a.sess.executeDelegateMutationPlans(attentionPlans); attentionErr == nil {
			attentionErr = executeErr
		}
		if attentionErr != nil {
			err = errors.Join(err, attentionErr)
		}
		if settlementClaim.mode == delegateSettlementTerminal {
			if stableDelegateFatalRun(err) {
				err = a.gateFatalRunError(err)
			}
			finish = a.stableDelegateFinish(res, err)
			break
		}
		if attentionErr != nil {
			if err != nil {
				err = a.gateFatalRunError(err)
			}
			finish = a.stableDelegateFinish(res, err)
			break
		}
		if err != nil {
			err = a.gateFatalRunError(err)
		}
		finish = a.stableDelegateFinish(res, err)
		decision, decisionErr := a.sess.delegateController.completionDecision(lease)
		if decisionErr != nil {
			err = errors.Join(err, decisionErr)
		} else if err == nil && decision == delegateCompletionFinishNoAction {
			prepared, prepareErr := a.sess.delegateController.prepareNoAction(settlementClaim, finish)
			if prepareErr != nil {
				err = errors.Join(err, prepareErr)
			} else if prepared {
				noActionClaim = settlementClaim
				break
			}
		}
		plans, settleErr := a.sess.delegateController.CompleteSettlement(settlementClaim, finish.packet)
		if executeErr := a.sess.executeDelegateMutationPlans(plans); settleErr == nil {
			settleErr = executeErr
		}
		if settleErr != nil {
			if !errors.Is(settleErr, errDelegateTargetBusy) {
				err = errors.Join(err, settleErr)
			}
			if err != nil {
				err = a.gateFatalRunError(err)
			}
			finish = a.stableDelegateFinish(res, err)
		}
		break
	}
	a.sess.mu.Lock()
	turns := a.sess.turns
	a.sess.mu.Unlock()

	runProvenance := a.followUpProvenance(inputProvenance)
	runStructured := a.sess.CommunicateStructured()
	finalizeTime := finish.endedAt
	if finalizeTime.IsZero() {
		finalizeTime = a.sess.sclock().Now()
	}
	a.mu.Lock()
	a.finalizing = true
	a.result = res
	a.err = err
	a.runProvenance = provenance.Clone(runProvenance)
	a.runStructured = runStructured
	a.runStructuredCaptured = true
	a.running = false
	a.turnsUsed = turns
	a.endedAt = &finalizeTime
	runEnd := classifyRunEnd(err, a.cancelRequested)
	a.status = runEnd.status
	// The payload is non-nil exactly when the run published Exhausted
	// (classifier contract), so its presence alone is the overwrite decision.
	if runEnd.exhaustion != nil {
		a.err = runEnd.exhaustion
	}
	done := a.done
	a.endEmitted = true
	a.mu.Unlock()
	if hook := a.sess.cfg.testOnly.subagentAfterFinalStatePublish; hook != nil {
		hook(a)
	}
	if stableRun && a.sess.delegateController != nil {
		finish.endedAt = finalizeTime
		var plans delegateMutationPlans
		var finishErr error
		if noActionClaim != nil {
			plans, finishErr = a.sess.delegateController.FinishNoAction(noActionClaim)
		} else {
			plans, finishErr = a.sess.delegateController.FinishGeneration(lease, finish)
		}
		if executeErr := a.sess.executeDelegateMutationPlans(plans); finishErr == nil {
			finishErr = executeErr
		}
		if finishErr != nil {
			a.sess.emit(events.EventWarning, warningDataFromError("delegate generation settlement incomplete", finishErr))
		}
	}
	if restoreParentDriveNotify != nil {
		a.sess.SetNotifyFunc(restoreParentDriveNotify)
	}
	a.mu.Lock()
	a.finalizing = false
	a.mu.Unlock()
	if restoreParentDriveNotify != nil && a.sess.peekNotifications() > 0 {
		a.sess.notify()
	}
	if stableRun {
		if pending, pendingErr := a.sess.pendingDelegateAttentionIDs(); pendingErr != nil {
			a.sess.emit(events.EventWarning, warningDataFromError("inspect remaining delegate attention", pendingErr))
		} else if len(pending) != 0 {
			if armErr := a.sess.armDelegateAttention(pending[0]); armErr != nil {
				a.sess.emit(events.EventWarning, warningDataFromError("rearm remaining delegate attention", armErr))
			}
		}
	}

	if done != nil {
		close(done)
	}
	if stableRun && a.sess.delegateController != nil {
		if reportErr := a.sess.delegateController.ReportFinalizationQuiesced(lease, a.sess); reportErr != nil {
			a.sess.emit(events.EventWarning, warningDataFromError("delegate finalization quiescence report failed", reportErr))
		}
	}
}

func (a *subagent) drainForFinalization(ctx context.Context, result string) (string, func(), error) {
	a.sess.mu.Lock()
	parentDriveNotify := a.sess.notifyFunc
	a.sess.mu.Unlock()
	a.mu.Lock()
	a.finalizing = true
	a.mu.Unlock()
	drained, err := a.sess.DrainJobTree(ctx)
	if err != nil {
		return result, parentDriveNotify, err
	}
	if drained != "" {
		result = drained
	}
	return result, parentDriveNotify, nil
}

// runEndClass is the shared classification of a delegate run's terminal error.
type runEndClass struct {
	mode   delegateSettlementMode
	fatal  bool
	status SubagentStatus
	// exhaustion is non-nil exactly when status == SubagentExhausted; its
	// presence is the caller's decision to replace the run error, so a
	// cancelled run whose error also carries a budget component never sees it.
	exhaustion *budgetExhaustionError
}

// classifyRunEnd maps a run error plus the local cancel request onto the
// settlement-mode, fatality, and status projections in one pattern-match over
// the run-end taxonomy. It is pure: no locking, I/O, Session, or controller
// access. Load-bearing pins:
//
//   - Settlement mode is terminal when cancelRequested regardless of err,
//     while SubagentCancelled additionally requires errors.Is(err,
//     context.Canceled): "this runtime can no longer settle ordinarily" vs
//     "the user stopped this run".
//   - Budget exhaustion is tested BEFORE the bare-text/empty-response
//     sentinels (so Join(bareText, exhaustion) settles terminally), and a
//     joined exhaustion+Canceled under cancel still publishes Cancelled with
//     no exhaustion payload — the error is kept verbatim there.
//   - context.Canceled is never fatal even when cancelRequested is false —
//     a host interrupt, not a user stop.
//   - errors.Is/errors.As semantics are preserved for wrapped and joined
//     (errors.Join) error values.
func classifyRunEnd(err error, cancelRequested bool) runEndClass {
	exhaustion, budgetExhausted := budgetExhaustionFromError(err)
	ordinary := !budgetExhausted && (err == nil ||
		errors.Is(err, errBareTextWithoutResultTool) || errors.Is(err, errEmptyResponseExhausted))
	nonFatal := ordinary || budgetExhausted || errors.Is(err, context.Canceled)
	var cls runEndClass
	if !cancelRequested && ordinary {
		cls.mode = delegateSettlementOrdinary
	} else {
		cls.mode = delegateSettlementTerminal
	}
	cls.fatal = !nonFatal
	switch {
	case cancelRequested && errors.Is(err, context.Canceled):
		cls.status = SubagentCancelled
	case budgetExhausted:
		cls.status = SubagentExhausted
		cls.exhaustion = exhaustion
	case err != nil:
		cls.status = SubagentFailed
	default:
		cls.status = SubagentCompleted
	}
	return cls
}

func delegateSettlementModeForRun(err error, cancelRequested bool) delegateSettlementMode {
	return classifyRunEnd(err, cancelRequested).mode
}

func stableDelegateFatalRun(err error) bool {
	return classifyRunEnd(err, false).fatal
}

func stableDelegateFinish(sess *Session, result string, runErr error) delegateFinish {
	inputs := delegateTerminalRunInputs{session: sess, result: result, runErr: runErr}
	if sess != nil {
		inputs.communicated = sess.Communicated()
		inputs.structuredResult, inputs.structuredResultPresent = sess.communicateStructuredResult()
	}
	finish := stableDelegateFinishFromRun(inputs)
	return finish
}

func (a *subagent) stableDelegateFinish(result string, runErr error) delegateFinish {
	if a == nil || a.sess == nil {
		return stableDelegateFinish(nil, result, runErr)
	}
	endedAt := a.sess.sclock().Now()
	a.mu.Lock()
	startedAt := a.startedAt
	var descriptor delegatestore.Descriptor
	if a.stableDescriptor != nil {
		descriptor = cloneDelegateStartDescriptor(*a.stableDescriptor)
	}
	a.mu.Unlock()
	structuredResult, structuredResultPresent := a.sess.communicateStructuredResult()
	inputs := delegateTerminalRunInputs{
		session:                 a.sess,
		result:                  result,
		runErr:                  runErr,
		communicated:            a.sess.Communicated(),
		structuredResult:        structuredResult,
		structuredResultPresent: structuredResultPresent,
		descriptor:              descriptor,
		startedAt:               startedAt,
		endedAt:                 endedAt,
		latestActivityAt:        endedAt,
		usage:                   cumulativeUsageSnapshot(a.sess.CumulativeUsageSnapshot()),
	}
	reporter := a.sess
	if controller := a.sess.delegateController; controller != nil {
		controller.mu.Lock()
		for _, live := range controller.live {
			if live != nil && live.runtime == a.sess && !live.activityAt.IsZero() {
				inputs.latestActivityAt = live.activityAt
				break
			}
		}
		if controller.rootRuntime != nil {
			reporter = controller.rootRuntime
		}
		controller.mu.Unlock()
	}
	inputs.worktree = reporter.stableDelegateWorktreeReport(descriptor)
	// SessionScratchDir is a reporting-only accessor (never provisions), so this
	// is safe to read even on an externally cancelled run — it costs nothing and
	// carries no side effect, matching the worktree report above.
	if le, ok := a.sess.currentEnv().(*execenv.LocalExecutionEnvironment); ok {
		inputs.scratchPath = le.SessionScratchDir()
	}
	finish := stableDelegateFinishFromRun(inputs)
	if finish.outcome == delegatestore.OutcomeFailed && a.sess.hasSalvageFromFinalRound() && finish.packet != nil {
		finish.packet.Warnings = appendUniqueStrings(finish.packet.Warnings, delegateSalvagedDraftNote)
	}
	return finish
}

// delegateTerminalRunInputs is the immutable run evidence used to construct a
// stable delegate's terminal result. The packet builder consumes this snapshot
// after the run has finished all provider, transcript, and worktree activity.
type delegateTerminalRunInputs struct {
	session                 *Session
	result                  string
	runErr                  error
	communicated            bool
	structuredResult        any
	structuredResultPresent bool
	descriptor              delegatestore.Descriptor
	startedAt               time.Time
	endedAt                 time.Time
	latestActivityAt        time.Time
	usage                   schema.CumulativeUsage
	warnings                []string
	worktree                *delegateWorktreeReport
	scratchPath             string
}

type delegateTerminalPacketMetadata struct {
	Outcome           delegatestore.OutcomeStatus     `json:"outcome,omitempty"`
	Reason            string                          `json:"reason,omitempty"`
	Task              string                          `json:"task,omitempty"`
	Description       string                          `json:"description,omitempty"`
	AgentType         string                          `json:"agent_type,omitempty"`
	Tools             []string                        `json:"tools,omitempty"`
	RequestedModel    string                          `json:"requested_model,omitempty"`
	ResolvedProfileID string                          `json:"resolved_profile_id,omitempty"`
	ResolvedModel     string                          `json:"resolved_model,omitempty"`
	ReasoningEffort   string                          `json:"reasoning_effort,omitempty"`
	RunStartedAt      string                          `json:"run_started_at,omitempty"`
	RunEndedAt        string                          `json:"run_ended_at,omitempty"`
	LatestActivityAt  string                          `json:"latest_activity_at,omitempty"`
	CumulativeUsage   *schema.CumulativeUsage         `json:"cumulative_usage,omitempty"`
	Worktree          *delegateTerminalWorktreeReport `json:"worktree,omitempty"`
	// ScratchPath is the delegate's absolute per-session scratch directory
	// (SessionScratchDir), reported for the same reason Worktree is: it is
	// partial evidence a parent needs to recover after an externally cancelled
	// run (kata tpb0), retained on disk regardless of outcome. Empty when the
	// delegate never provisioned one (unsandboxed and no tool spawned yet).
	ScratchPath         string                         `json:"scratch_path,omitempty"`
	ExhaustionBudget    delegatestore.ExhaustionBudget `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit     int                            `json:"exhaustion_limit,omitempty"`
	ExhaustionResumable *bool                          `json:"resumable,omitempty"`
}

type delegateTerminalWorktreeReport struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
	Ahead   int    `json:"ahead"`
	Dirty   bool   `json:"dirty"`
}

// stableDelegateFinishFromRun constructs the one immutable packet used by
// settlement, crash replay, and owner delivery.
func stableDelegateFinishFromRun(inputs delegateTerminalRunInputs) delegateFinish {
	message := strings.TrimSpace(inputs.result)
	if message == "" && inputs.runErr != nil {
		message = inputs.runErr.Error()
	}
	rawMessage, _ := json.Marshal(message)
	packet := delegatestore.TerminalPacket{
		Kind:     delegatestore.PacketTerminalError,
		Message:  rawMessage,
		Warnings: append([]string(nil), inputs.warnings...),
	}
	finish := delegateFinish{
		outcome:     delegatestore.OutcomeFailed,
		disposition: delegatestore.DispositionTerminalError,
		reason:      "failed",
		packet:      &packet,
		endedAt:     inputs.endedAt,
	}
	metadata := delegateTerminalMetadataFromRun(inputs)
	if inputs.runErr == nil && inputs.communicated {
		packet.Kind = delegatestore.PacketReported
		finish.outcome = delegatestore.OutcomeCompleted
		finish.disposition = delegatestore.DispositionReported
		finish.reason = ""
		captureDelegateStructuredResult(&packet, inputs)
	} else if exhaustion, exhausted := budgetExhaustionFromError(inputs.runErr); exhausted {
		finish.outcome = delegatestore.OutcomeExhausted
		finish.reason = exhaustion.reason()
		finish.exhaustionBudget = delegatestore.ExhaustionBudget(exhaustion.Budget)
		finish.exhaustionLimit = exhaustion.Limit
		resumable := exhaustion.Resumable
		finish.exhaustionResumable = &resumable
		metadata.ExhaustionBudget = finish.exhaustionBudget
		metadata.ExhaustionLimit = exhaustion.Limit
		metadata.ExhaustionResumable = &resumable
	} else if errors.Is(inputs.runErr, context.Canceled) {
		finish.outcome = delegatestore.OutcomeCancelled
		finish.reason = "cancelled"
	}
	metadata.Outcome = finish.outcome
	metadata.Reason = finish.reason
	if raw, err := json.Marshal(metadata); err == nil && string(raw) != "{}" {
		packet.Metadata = raw
	}
	return finish
}

func captureDelegateStructuredResult(packet *delegatestore.TerminalPacket, inputs delegateTerminalRunInputs) {
	if packet == nil {
		return
	}
	if !inputs.structuredResultPresent {
		if len(inputs.descriptor.ResultSchema) != 0 {
			valid := false
			packet.StructuredResultValid = &valid
			packet.StructuredResultReason = structuredResultReasonSchemaResultMissing
		}
		return
	}
	raw, err := json.Marshal(inputs.structuredResult)
	if err != nil {
		valid := false
		packet.StructuredResultValid = &valid
		packet.StructuredResultReason = structuredResultReasonSchemaCaptureFailed
		return
	}
	if len(raw) > delegatestore.MaxTerminalStructuredResultBytes {
		valid := false
		packet.StructuredResultValid = &valid
		packet.StructuredResultReason = structuredResultReasonSchemaResultTooLarge
		return
	}
	packet.StructuredResult = append(json.RawMessage(nil), raw...)
	valid := true
	packet.StructuredResultValid = &valid
	if len(inputs.descriptor.ResultSchema) == 0 {
		return
	}
	var schemaValue any
	var structuredValue any
	if err := json.Unmarshal(inputs.descriptor.ResultSchema, &schemaValue); err != nil || json.Unmarshal(raw, &structuredValue) != nil || validateStructuredResult(structuredValue, schemaValue) != nil {
		valid = false
		packet.StructuredResultValid = &valid
		packet.StructuredResultReason = structuredResultReasonSchemaValidationFailed
	}
}

func delegateTerminalMetadataFromRun(inputs delegateTerminalRunInputs) delegateTerminalPacketMetadata {
	metadata := delegateTerminalPacketMetadata{
		Task:              inputs.descriptor.Task,
		Description:       inputs.descriptor.Description,
		AgentType:         inputs.descriptor.AgentType,
		Tools:             append([]string(nil), inputs.descriptor.ToolNameCeiling...),
		RequestedModel:    inputs.descriptor.RequestedModel,
		ResolvedProfileID: inputs.descriptor.ResolvedProfileID,
		ResolvedModel:     inputs.descriptor.ResolvedModel,
		ReasoningEffort:   inputs.descriptor.Config.ReasoningEffort,
	}
	if !inputs.startedAt.IsZero() {
		metadata.RunStartedAt = inputs.startedAt.UTC().Format(time.RFC3339Nano)
	}
	if !inputs.endedAt.IsZero() {
		metadata.RunEndedAt = inputs.endedAt.UTC().Format(time.RFC3339Nano)
	}
	if !inputs.latestActivityAt.IsZero() {
		metadata.LatestActivityAt = inputs.latestActivityAt.UTC().Format(time.RFC3339Nano)
	}
	if inputs.usage != (schema.CumulativeUsage{}) {
		usage := inputs.usage
		metadata.CumulativeUsage = &usage
	}
	if inputs.worktree != nil {
		metadata.Worktree = &delegateTerminalWorktreeReport{
			Path:    inputs.worktree.Path,
			Branch:  inputs.worktree.Branch,
			HeadSHA: inputs.worktree.HeadSHA,
			Ahead:   inputs.worktree.Ahead,
			Dirty:   inputs.worktree.Dirty,
		}
	}
	metadata.ScratchPath = inputs.scratchPath
	return metadata
}

func (a *subagent) runSubagentStopHook(ctx context.Context, res string, err error, inputProvenance *provenance.Causal) (string, error) {
	if a.sess == nil || a.sess.hookRunner == nil {
		return res, err
	}
	input := a.sess.hookInput(plugin.HookSubagentStop)
	if err != nil {
		input.Reason = err.Error()
	} else {
		input.Reason = "complete"
	}
	stopResult := a.sess.hookRunner.RunSubagentStop(a.sess.apiLogContext(ctx), input)
	for _, m := range stopResult.ModelContext {
		a.sess.deliverHookContext(m)
	}
	for _, m := range stopResult.UserMessages {
		a.sess.deliverHookUserMessage(m)
	}
	if stopResult.Blocked || len(stopResult.ModelContext) != 0 || len(stopResult.UserMessages) != 0 {
		if lease, stableRun := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease); stableRun && a.sess.delegateController != nil {
			if escalationErr := a.sess.delegateController.escalateCompletionRequirement(lease); escalationErr != nil {
				return res, errors.Join(err, escalationErr)
			}
		}
	}
	if !stopResult.Blocked {
		return res, err
	}
	reason := strings.TrimSpace(stopResult.BlockReason)
	if reason == "" {
		reason = "SubagentStop hook blocked completion. Continue and address the hook feedback before stopping."
	}
	return a.sess.processInputWithProvenance(ctx, reason, nil, inputProvenance)
}

func (a *subagent) followUpProvenance(inputProvenance *provenance.Causal) *provenance.Causal {
	if a == nil || a.sess == nil {
		return provenance.Clone(inputProvenance)
	}
	return provenance.Union(inputProvenance, a.sess.activeCausalProvenance(), a.sess.completedCausalProvenance())
}

func (a *subagent) resultSnapshotLocked() subagentResult {
	output := a.result
	if strings.TrimSpace(output) == "" && a.err != nil {
		output = a.err.Error()
	}
	return subagentResult{
		AgentID:       a.id,
		Status:        a.status,
		Closed:        a.closed,
		Output:        output,
		Success:       a.status == SubagentCompleted,
		TurnsUsed:     a.turnsUsed,
		TranscriptRef: encodeRef("", a.sess.ID()),
	}
}
