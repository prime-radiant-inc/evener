package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
)

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

var rootOnlyJobPresenceTools = []string{"delegate", "job_watch"}

type subagent struct {
	id   string
	sess *Session
	emit func(events.EventKind, events.EventData)

	mu              sync.Mutex
	running         bool
	status          SubagentStatus
	turnsUsed       int
	done            chan struct{}
	result          string
	err             error
	resultConsumed  bool // true after the first wait returns this run's result
	endEmitted      bool
	runProvenance   *provenance.Causal // immutable causal provenance for the completed run result
	runFromWatch    bool               // true for a run resumed by job_watch.send; suppresses observer feedback loops
	nudgeEnabled    bool               // true for default subagents that should be nudged to communicate
	cancel          context.CancelFunc // cancels the current run's context
	cancelRequested bool               // set by parent stop so finalize maps a context.Canceled run to cancelled
	agentType       string             // plugin agent type name; empty for default subagents
	createdAt       time.Time          // set once at spawn; never reset on resume
	startedAt       time.Time          // set at spawn; re-stamped at each idle-resume
	endedAt         *time.Time         // set at run finalize; cleared to nil at idle-resume
	closed          bool               // session torn down; record retained as terminal history
	closeTimedOut   bool               // session-close wait exceeded its bound; close not confirmed
	driving         bool               // a drive-down notification turn (§3) is in flight on this idle child
}

type preparedSubagentRun struct {
	sub                *subagent
	input              string
	runCtx             context.Context
	runCancel          context.CancelFunc
	parentSessionID    string
	parentJobID        string
	originToolCallID   string
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
	resultSchema       map[string]any
	explicitToolGrants []string
	// treeSlot is the tree-counter reservation claimed by prepareSubagentRun for
	// this spawn's running delegate turn (spec §4). Ownership transfers to the
	// delegate runningJob at attach; if the prepared run is discarded before
	// attach (an error path, or the legacy in-process spawn that mints no
	// delegate job), the reservation is released so the slot is not leaked.
	treeSlot *treeReservation
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
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
	return appendUniqueStrings(append([]string(nil), rootOnlyJobPresenceTools...), rootOnlyJobControlTools...)
}

func isRootOnlyJobPresenceTool(name string) bool {
	return hasString(rootOnlyJobPresenceTools, name)
}

func isRootOnlySubagentTool(name string) bool {
	return hasString(rootOnlySubagentTools(), name)
}

func removeRootOnlySubagentTools(items []string) []string {
	return removeStrings(items, rootOnlySubagentTools())
}

func agentUsesRootOnlySubagentTools(agent plugin.Agent) bool {
	for _, tool := range agent.Tools {
		if isRootOnlySubagentTool(tool) {
			return true
		}
	}
	return false
}

func baseSubagentToolPolicy(agent *plugin.Agent, canDelegate bool) (allTools bool, allowed []string, denied []string) {
	switch {
	case agent != nil && agent.AllTools:
		return true, nil, nil
	case agent != nil && len(agent.Tools) > 0:
		allowed = append([]string(nil), agent.Tools...)
		allowed = appendUniqueStrings(allowed, "task_list")
		return false, allowed, nil
	default:
		if canDelegate {
			return false, nil, nil // untyped child with allowance: no deny-list → gets delegate+job_watch on default surface
		}
		return false, nil, rootOnlySubagentTools()
	}
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
	if err := json.Unmarshal(b, &out); err != nil {
		return cloneShallowMap(in)
	}
	return out
}

func cloneShallowMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
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

func (s *Session) spawnAgent(ctx context.Context, task, model, workingDir string, maxTurns int, agentType string, reasoningEffort string, parentTasks []taskpkg.TaskTemplate, grantTools []string) (any, error) {
	prepared, err := s.prepareSubagentRun(ctx, task, model, workingDir, maxTurns, agentType, reasoningEffort, parentTasks, grantTools)
	if err != nil {
		return "", err
	}
	// The legacy in-process spawn mints no delegate job, so no runningJob takes
	// ownership of the slot prepareSubagentRun reserved and no finalize/abandon
	// path can release it. The tree counter bounds delegate turns (spec §4); this
	// in-process turn is not one, so return the slot rather than leak it.
	releasePreparedTreeSlot(prepared)
	if err := s.trackAndLaunchPreparedSubagent(prepared); err != nil {
		prepared.sub.sess.Close()
		return "", err
	}

	b, _ := json.Marshal(map[string]any{"agent_id": prepared.sub.id, "status": string(SubagentRunning)})
	return string(b), nil
}

func (s *Session) prepareSubagentRun(ctx context.Context, task, model, workingDir string, maxTurns int, agentType string, reasoningEffort string, parentTasks []taskpkg.TaskTemplate, grantTools []string) (*preparedSubagentRun, error) {
	s.mu.Lock()
	depth := s.depth
	allowance := s.delegationAllowance
	s.mu.Unlock()
	if allowance <= 0 {
		return nil, errors.New("delegation not permitted: your delegation_allowance is 0")
	}

	// Look up plugin agent configuration when agent_type is specified.
	var agent *plugin.Agent
	if agentType = strings.TrimSpace(agentType); agentType != "" {
		a, ok := s.pluginAgents[agentType]
		if !ok {
			return nil, fmt.Errorf("unknown plugin agent type: %s", agentType)
		}
		if agentUsesRootOnlySubagentTools(a) && allowance <= 0 {
			return nil, fmt.Errorf("agent_type %q is top-level only: it requires root-only tools", agentType)
		}
		agent = &a
	}

	sp := s.currentProfile()
	subProfile := sp
	if model = strings.TrimSpace(model); model != "" {
		resolved, crossProvider, err := s.resolveProfileForRef(sp, model)
		if err != nil {
			return nil, fmt.Errorf("model override %q: %w", model, err)
		}
		if crossProvider {
			resolved = resolved.WithCommunicateOverridesFrom(sp)
		}
		subProfile = resolved
	}
	// Plugin agent model takes precedence (unless "inherit" or empty).
	if agent != nil && agent.Model != "inherit" && agent.Model != "" {
		resolved, crossProvider, err := s.resolveProfileForRef(sp, agent.Model)
		if err != nil {
			return nil, fmt.Errorf("agent model %q: %w", agent.Model, err)
		}
		if crossProvider {
			resolved = resolved.WithCommunicateOverridesFrom(sp)
		}
		subProfile = resolved
	}

	s.mu.Lock()
	subCfg := s.cfg
	s.mu.Unlock()
	subCfg.MCPConfigFiles = nil
	subCfg.MCPInline = nil
	subCfg.spawn.parentJobID = ""
	subCfg.spawn.forwardJobEvent = nil
	subCfg.spawn.parentSessionID = s.id
	subCfg.spawn.subagentTask = task
	subCfg.spawn.depth = depth + 1
	subCfg.spawn.parentSteer = s.SteerWithProvenance
	subCfg.spawn.parentSteerDelivered = s.trySteerWithProvenanceAndNotify
	if s.jobManager != nil {
		subCfg.spawn.parentMarkCallerCallbackDelivered = s.jobManager.markWatchOriginCallerCallbackDelivered
	}
	subCfg.spawn.parentGrantedJobRead = s.lookupGrantedJobRead
	if s.cfg.ShareTasksWithChildren {
		subCfg.spawn.sharedTaskStore = s.getOrCreateTaskStore()
	} else {
		subCfg.spawn.sharedTaskStore = nil
	}
	if callID, ok := ctx.Value(ctxToolCallID).(string); ok {
		subCfg.spawn.parentToolCallID = callID
	}
	if parentJobID, ok := ctx.Value(ctxParentJobID).(string); ok && parentJobID != "" {
		subCfg.spawn.parentJobID = parentJobID
		if s.jobManager != nil {
			subCfg.spawn.forwardJobEvent = s.jobManager.forwardEvent
		}
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
		subCfg.spawn.parentInstallWatch = s.installParentSourceWatchForChild
	}
	childCanDelegate := subCfg.spawn.delegationAllowance > 0
	if schema, ok := ctx.Value(ctxCommunicateOutputSchema).(map[string]any); ok && len(schema) > 0 {
		subCfg.spawn.communicateOutputSchema = schema
	}
	if maxTurns > 0 {
		subCfg.MaxTurns = maxTurns
	} else {
		subCfg.MaxTurns = 500
	}
	if reasoningEffort = strings.TrimSpace(reasoningEffort); reasoningEffort != "" {
		subCfg.ReasoningEffort = reasoningEffort
	}
	canonicalGrantTools := s.canonicalizeToolNames(grantTools)

	// Determine agent name and role prompt for the subagent.
	// Named agents use their own SystemPrompt; unnamed agents get the "subagent" persona.
	var agentName string
	var rolePrompt string
	if agent != nil && strings.TrimSpace(agent.SystemPrompt) != "" {
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

	if (agent != nil && strings.TrimSpace(agent.SystemPrompt) != "" && agent.PluginName != "builtin") ||
		(agent == nil && childCanDelegate) {
		subCfg.spawn.rolePromptOverride = rolePrompt
	}
	var activatedSkillNames []string
	var activatedSkillBodies []string
	if agent != nil && len(agent.Skills) > 0 {
		for _, skillName := range agent.Skills {
			body, err := skill.ResolveSkillContent(s.skills, skillName)
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

	allTools, allowedTools, deniedTools := baseSubagentToolPolicy(agent, allowance > 0)
	if subCfg.spawn.parentWatchGranted && !allTools {
		if len(allowedTools) > 0 {
			allowedTools = appendUniqueStrings(allowedTools, "job_watch")
		} else {
			deniedTools = removeStrings(deniedTools, []string{"job_watch"})
		}
	}
	if len(canonicalGrantTools) > 0 {
		currentTools := s.reg.RegisteredNames()
		for _, toolName := range canonicalGrantTools {
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
			} else {
				deniedTools = removeStrings(deniedTools, []string{toolName})
			}
		}
	}

	if allTools {
		// Leave the registry unrestricted for explicit all-tools agents.
	} else if len(allowedTools) > 0 {
		subCfg.spawn.allowedToolNames = append([]string(nil), allowedTools...)
	} else {
		subCfg.spawn.deniedToolNames = append([]string(nil), deniedTools...)
	}

	subEnv := s.env
	if workingDir = strings.TrimSpace(workingDir); workingDir != "" {
		if le, ok := s.env.(*execenv.LocalExecutionEnvironment); ok {
			subEnv = le.WithWorkingDirectory(workingDir)
		} else {
			return nil, errors.New("execution environment does not support working_dir override")
		}
	}

	if schema := subCfg.spawn.communicateOutputSchema; len(schema) > 0 {
		subProfile = provider.WithCommunicateOutputSchema(subProfile, schema)
	}

	subSess, err := NewSession(s.client, subProfile, subEnv, subCfg)
	if err != nil {
		return nil, err
	}
	if len(canonicalGrantTools) > 0 {
		var missing []string
		for _, toolName := range canonicalGrantTools {
			if subSess.reg.Get(toolName) == nil {
				missing = append(missing, toolName)
			}
		}
		if len(missing) > 0 {
			subSess.Close()
			return nil, fmt.Errorf("cannot grant tool(s) to spawned subagent: %s", strings.Join(missing, ", "))
		}
	}

	// Populate default tasks from agent definition + parent tasks.
	if agent != nil && len(agent.Tasks) > 0 {
		subStore := subSess.getOrCreateTaskStore()
		if err := subStore.PopulateFromTemplates(agent.Tasks, parentTasks); err != nil {
			// Non-fatal: surface as a warning so the spawn still proceeds but the
			// failure is observable instead of silently swallowed.
			s.emit(events.EventWarning, warningDataFromError("failed to populate subagent tasks from templates", err))
		}
		// Inject the first task's prompt as a steering message.
		if current, ok := subStore.CurrentInProgress(); ok {
			subSess.Steer(formatCurrentTaskSteering(current))
		}
	}

	// Enforce the retained-terminal cap before tracking. Done AFTER NewSession so
	// the child is fully built, but a failure must not leak it: close the created
	// session (mirroring the created-but-not-tracked cleanup above) before returning.
	// On success, GC-evicted records are closed here, OUTSIDE the manager mutex.
	evicted, err := s.subagents.reserveSlot()
	if err != nil {
		subSess.Close()
		return nil, err
	}
	for _, ev := range evicted {
		ev.sess.Close()
	}

	// Reserve a tree-counter slot for this spawn's running delegate turn (spec §4).
	// At capacity the spawn does not launch: close the freshly built child (the
	// same created-but-not-tracked cleanup as the retained-cap path) and surface
	// the tree_at_capacity error to the tool call. Ownership of the reservation
	// transfers to the delegate runningJob at attach; an unattached prepared run
	// releases it (releasePreparedTreeSlot).
	treeSlot, ok := s.reserveTreeSlot()
	if !ok {
		subSess.Close()
		return nil, errTreeAtCapacity
	}

	now := time.Now()
	sub := &subagent{
		id:           subSess.id,
		sess:         subSess,
		emit:         s.emit,
		running:      true,
		status:       SubagentRunning,
		done:         make(chan struct{}),
		nudgeEnabled: subagentNeedsCommunicateNudge(agent),
		agentType:    agentType,
		createdAt:    now,
		startedAt:    now,
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
	runCtx, runCancel := context.WithCancel(context.Background())
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
		parentJobID:        subCfg.spawn.parentJobID,
		originToolCallID:   subCfg.spawn.parentToolCallID,
		task:               task,
		agentType:          agentType,
		requestedModel:     model,
		resolvedAgentName:  agentName,
		reasoningEffort:    reasoningEffort,
		frozenRolePrompt:   rolePrompt,
		frozenToolNames:    frozenSubagentToolNames(allTools, allowedTools, deniedTools),
		frozenSkillNames:   append([]string(nil), activatedSkillNames...),
		frozenSkillBodies:  append([]string(nil), activatedSkillBodies...),
		workingDir:         subEnv.WorkingDirectory(),
		localEnvPolicy:     localEnvPolicyName(subEnv),
		resultSchema:       cloneMap(subCfg.spawn.communicateOutputSchema),
		explicitToolGrants: append([]string(nil), canonicalGrantTools...),
		treeSlot:           treeSlot,
	}
	if agent != nil && len(agent.Tasks) > 0 {
		if current, ok := subSess.getOrCreateTaskStore().CurrentInProgress(); ok {
			prepared.frozenTaskPrompt = current.Prompt
		}
	}
	return prepared, nil
}

func (s *Session) installParentSourceWatchForChild(observerSessionID string, observerDelegateID string, args watchArgs) (watchResult, error) {
	return watchResult{}, errors.New("parent watch installation is not wired")
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
	if sub.running || sub.driving {
		subSess := sub.sess
		sub.mu.Unlock()
		// Inject as steering message into the in-flight session. A mid-drive child
		// (sub.driving) is in flight just like a running one: the drive turn absorbs
		// the steered input at its tool-round boundary (spec §3, A7 steer-into-drive)
		// rather than launching a second concurrent run.
		subSess.Steer(input)
		return false, nil
	}

	// Agent is idle — start a new ProcessInput round. Enroll the resumed run in
	// sendersWG under s.mu (gated on closing) so it joins the same teardown
	// barrier as the initial spawn.
	runCtx, runCancel := context.WithCancel(context.Background())
	resumeTime := time.Now()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		sub.mu.Unlock()
		runCancel()
		return false, errors.New("session is closed")
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()

	resetSubagentForRunLocked(sub, runCancel, resumeTime)
	sub.mu.Unlock()

	s.launchSubagentRun(runCtx, sub, runCancel, input, s.activeCausalProvenance())
	return true, nil
}

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
	if sub.sess == nil || sub.closed || sub.running || sub.driving {
		sub.mu.Unlock()
		return false
	}
	// Reserve a tree-counter slot for the drive turn (spec §4): a drive launches a
	// running delegate turn (the child's EntryNotification render) and holds the
	// slot for its duration. At capacity the drive does not launch — return false,
	// the not-launched signal the caller already honors (no settle), so the child's
	// durable ledger stays queued and the next loop boundary retries (spec §3).
	treeSlot, ok := s.reserveTreeSlot()
	if !ok {
		sub.mu.Unlock()
		return false
	}
	driveCtx, driveCancel := context.WithCancel(context.Background())
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
		defer treeSlot.release()
		defer func() {
			sub.mu.Lock()
			sub.driving = false
			sub.mu.Unlock()
			// Re-drive if attention arrived during this turn (spec §3): a notify that
			// fired while driving==true was dropped at the live/idle guard, so a
			// notification landing after the child's final peek would otherwise
			// strand (the parent is idle and there is no autonomous poll). The
			// re-check runs AFTER sub.driving is cleared so the inner guard passes.
			// It is idempotent and self-terminating: it re-drives only when real
			// attention remains, each re-drive is a fresh goroutine, and it stops
			// when peek==0 && !hasPendingWatchSends.
			if s.childStopGated(childSess.id) {
				return
			}
			if childSess.peekNotifications() > 0 || (childSess.jobManager != nil && childSess.jobManager.hasPendingWatchSends()) {
				if s.driveSubagentNotificationTurn(sub) {
					s.settleDrivenChildForwardedPendings(childSess.id)
				}
			}
		}()
		_, _ = childSess.ProcessInputKind(driveCtx, "", nil, EntryNotification)
	}()
	return true
}

func resetSubagentForRunLocked(sub *subagent, cancel context.CancelFunc, startedAt time.Time) {
	resetSubagentForRunLockedFromWatch(sub, cancel, startedAt, false)
}

func resetSubagentForRunLockedFromWatch(sub *subagent, cancel context.CancelFunc, startedAt time.Time, fromWatch bool) {
	sub.done = make(chan struct{})
	sub.running = true
	sub.status = SubagentRunning
	sub.result = ""
	sub.err = nil
	sub.resultConsumed = false
	sub.endEmitted = false
	sub.runProvenance = nil
	sub.runFromWatch = fromWatch
	sub.cancel = cancel
	sub.cancelRequested = false
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
	sub.cancelRequested = true
	cancel := sub.cancel
	done := sub.done
	sub.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
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

// communicateNudge returns the message sent to a subagent that stops without
// calling the result tool. Sent at most once.
func communicateNudge(toolName string) string {
	return "You stopped without calling " + toolName + ". " +
		"You MUST call " + toolName + " with end_turn=true and a message summarizing your complete findings " +
		"before stopping. The parent agent receives ONLY the " + toolName + " message — " +
		"it cannot see anything else you did. Report your results now."
}

func (a *subagent) run(ctx context.Context, input string, inputProvenance *provenance.Causal) {
	a.mu.Lock()
	runStartedFromWatch := a.runFromWatch
	a.mu.Unlock()

	kind := EntryUserInput
	if runStartedFromWatch {
		kind = EntryWatchDelivery
	}
	res, err := a.sess.processInputKindWithProvenance(ctx, input, nil, kind, inputProvenance)

	// A requested cancel suppresses both the communicate-nudge and the
	// SubagentStop blocking-continuation: neither should run another turn on the
	// already-cancelled run context. This covers the late-cancel err==nil case
	// the status switch below still treats as completed.
	a.mu.Lock()
	cancelRequested := a.cancelRequested
	a.mu.Unlock()

	// Auto-nudge: if a default subagent stops without calling communicate,
	// send one reminder and let it try again. This covers both empty stops
	// and repeated bare-text responses that exhausted the session-level retry
	// loop before the subagent had a chance to report back.
	shouldNudge := !cancelRequested &&
		!runStartedFromWatch &&
		a.nudgeEnabled &&
		!a.sess.Communicated() &&
		(err == nil || errors.Is(err, errBareTextWithoutResultTool) || errors.Is(err, errEmptyResponseExhausted))
	if shouldNudge {
		res, err = a.sess.processInputWithProvenance(ctx, communicateNudge(a.sess.resultToolName()), nil, a.followUpProvenance(inputProvenance))
	}
	if !cancelRequested {
		res, err = a.runSubagentStopHook(ctx, res, err, a.followUpProvenance(inputProvenance))
	}

	a.sess.mu.Lock()
	turns := a.sess.turns
	a.sess.mu.Unlock()

	runProvenance := a.followUpProvenance(inputProvenance)
	finalizeTime := time.Now()
	a.mu.Lock()
	a.result = res
	a.err = err
	a.runProvenance = provenance.Clone(runProvenance)
	a.running = false
	a.turnsUsed = turns
	a.endedAt = &finalizeTime
	switch {
	case a.cancelRequested && errors.Is(err, context.Canceled):
		a.status = SubagentCancelled
	case err != nil:
		a.status = SubagentFailed
	default:
		a.status = SubagentCompleted
	}
	done := a.done
	a.endEmitted = true
	a.mu.Unlock()

	if done != nil {
		close(done)
	}
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
	stopResult := a.sess.hookRunner.RunSubagentStop(ctx, input)
	for _, m := range stopResult.ModelContext {
		a.sess.deliverHookContext(m)
	}
	for _, m := range stopResult.UserMessages {
		a.sess.deliverHookUserMessage(m)
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
