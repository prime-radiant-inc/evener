package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// minWaitTimeoutMS is the minimum timeout for the wait tool, preventing the model
// from burning rounds with rapid 1-second retries.
const minWaitTimeoutMS = 120_000 // 2 minutes

// SubAgentStatus tracks the lifecycle of a sub-agent.
type SubAgentStatus string

const (
	SubAgentRunning   SubAgentStatus = "running"
	SubAgentCompleted SubAgentStatus = "completed"
	SubAgentFailed    SubAgentStatus = "failed"
)

// SubAgentResult is the structured output from a completed sub-agent.
type SubAgentResult struct {
	Status     SubAgentStatus `json:"status"`
	Output     string         `json:"output"`
	Success    bool           `json:"success"`
	TurnsUsed  int            `json:"turns_used"`
	Transcript string         `json:"transcript,omitempty"`
}

// defaultSubagentInstructions is the role-specific prompt for default subagents
// (no agent_type). Appended after the common subagent base prompt.
const defaultSubagentInstructions = `You are a general-purpose subagent. Do the work yourself using the tools
available in this session.
Do NOT try to spawn further subagents.

Your job is to complete the task and report your findings.`

var rootOnlyAgentManagementTools = []string{"spawn_agent", "resume_agent", "wait", "close_agent"}

type subagent struct {
	id     string
	sess   *Session
	parent *Session

	mu             sync.Mutex
	running        bool
	status         SubAgentStatus
	turnsUsed      int
	done           chan struct{}
	result         string
	err            error
	resultConsumed bool // true after the first wait returns this run's result
	endEmitted     bool
	nudgeEnabled   bool // true for default subagents that should be nudged to communicate
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

func isRootOnlyAgentManagementTool(name string) bool {
	return hasString(rootOnlyAgentManagementTools, name)
}

func removeRootOnlyAgentManagementTools(items []string) []string {
	return removeStrings(items, rootOnlyAgentManagementTools)
}

func agentUsesRootOnlyManagementTools(agent PluginAgent) bool {
	for _, tool := range agent.Tools {
		if isRootOnlyAgentManagementTool(tool) {
			return true
		}
	}
	return false
}

func baseSubagentToolPolicy(agent *PluginAgent) (allTools bool, allowed []string, denied []string) {
	switch {
	case agent != nil && agent.AllTools:
		return true, nil, nil
	case agent != nil && len(agent.Tools) > 0:
		allowed = append([]string(nil), agent.Tools...)
		allowed = appendUniqueStrings(allowed, "task_list")
		return false, allowed, nil
	default:
		return false, nil, append([]string(nil), rootOnlyAgentManagementTools...)
	}
}

func subagentNeedsCommunicateNudge(agent *PluginAgent) bool {
	if agent == nil {
		return true
	}
	return agent.PluginName == "builtin" && agent.Name == "subagent"
}

func (s *Session) spawnAgent(ctx context.Context, task, model, workingDir string, maxTurns int, agentType string, reasoningEffort string, parentTasks []TaskTemplate, grantTools []string) (any, error) {
	s.mu.Lock()
	depth := s.depth
	maxDepth := s.cfg.MaxSubagentDepth
	s.mu.Unlock()
	if depth > 0 {
		return "", fmt.Errorf("subagent management is top-level only")
	}
	if depth >= maxDepth {
		return "", fmt.Errorf("subagent depth limit reached")
	}

	// Look up plugin agent configuration when agent_type is specified.
	var agent *PluginAgent
	if agentType = strings.TrimSpace(agentType); agentType != "" {
		a, ok := s.pluginAgents[agentType]
		if !ok {
			return "", fmt.Errorf("unknown plugin agent type: %s", agentType)
		}
		if agentUsesRootOnlyManagementTools(a) {
			return "", fmt.Errorf("agent_type %q is top-level only: it requires root-only agent-management tools", agentType)
		}
		agent = &a
	}

	subProfile := s.profile
	if model = strings.TrimSpace(model); model != "" {
		resolved, crossProvider, err := s.resolveProfileForRef(model)
		if err != nil {
			return "", fmt.Errorf("model override %q: %w", model, err)
		}
		if crossProvider {
			resolved = preserveBaseOverrides(resolved, s.profile)
		}
		subProfile = resolved
	}
	// Plugin agent model takes precedence (unless "inherit" or empty).
	if agent != nil && agent.Model != "inherit" && agent.Model != "" {
		resolved, crossProvider, err := s.resolveProfileForRef(agent.Model)
		if err != nil {
			return "", fmt.Errorf("agent model %q: %w", agent.Model, err)
		}
		if crossProvider {
			resolved = preserveBaseOverrides(resolved, s.profile)
		}
		subProfile = resolved
	}

	subCfg := s.cfg
	subCfg.MCPConfigFiles = nil
	subCfg.MCPInline = nil
	subCfg.ParentSessionID = s.id
	subCfg.SubagentTask = task
	subCfg.Depth = depth + 1
	if s.cfg.ShareTasksWithChildren {
		subCfg.SharedTaskStore = s.getOrCreateTaskStore()
	} else {
		subCfg.SharedTaskStore = nil
	}
	if callID, ok := ctx.Value(ctxToolCallID).(string); ok {
		subCfg.ParentToolCallID = callID
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
	} else if subagentAgent, ok := s.pluginAgents["subagent"]; ok {
		agentName = "subagent"
		rolePrompt = subagentAgent.SystemPrompt
	} else {
		agentName = "subagent"
		rolePrompt = defaultSubagentInstructions
	}
	subCfg.AgentName = agentName // ensure subagent gets its own tasks, not parent's

	if agent != nil && strings.TrimSpace(agent.SystemPrompt) != "" && agent.PluginName != "builtin" {
		subCfg.RolePromptOverride = rolePrompt
	}
	if agent != nil && len(agent.Skills) > 0 {
		for _, skillName := range agent.Skills {
			body, err := ResolveSkillContent(s.skills, skillName)
			if err != nil {
				continue
			}
			if strings.TrimSpace(body) != "" {
				subCfg.ActivatedSkillBodies = append(subCfg.ActivatedSkillBodies, body)
			}
		}
	}

	allTools, allowedTools, deniedTools := baseSubagentToolPolicy(agent)
	if len(canonicalGrantTools) > 0 {
		currentTools := s.reg.RegisteredNames()
		for _, toolName := range canonicalGrantTools {
			if isRootOnlyAgentManagementTool(toolName) {
				return "", fmt.Errorf("cannot grant tool %q: subagent-management tools are top-level only", toolName)
			}
			baseHasTool := allTools ||
				hasString(allowedTools, toolName) ||
				(len(allowedTools) == 0 && !hasString(deniedTools, toolName))
			if baseHasTool {
				continue
			}
			if !currentTools[toolName] {
				return "", fmt.Errorf("cannot grant tool %q: it is not currently callable in this session", toolName)
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
		subCfg.AllowedToolNames = append([]string(nil), allowedTools...)
	} else {
		subCfg.DeniedToolNames = append([]string(nil), deniedTools...)
	}

	subEnv := s.env
	if workingDir = strings.TrimSpace(workingDir); workingDir != "" {
		if le, ok := s.env.(*LocalExecutionEnvironment); ok {
			subEnv = le.WithWorkingDirectory(workingDir)
		} else {
			return "", fmt.Errorf("execution environment does not support working_dir override")
		}
	}

	subSess, err := NewSession(s.client, subProfile, subEnv, subCfg)
	if err != nil {
		return "", err
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
			return "", fmt.Errorf("cannot grant tool(s) to spawned subagent: %s", strings.Join(missing, ", "))
		}
	}

	// Populate default tasks from agent definition + parent tasks.
	if agent != nil && len(agent.Tasks) > 0 {
		subStore := subSess.getOrCreateTaskStore()
		if err := subStore.PopulateFromTemplates(agent.Tasks, parentTasks); err != nil {
			// Log but don't fail the spawn.
			_ = err
		}
		// Inject the first task's prompt as a steering message.
		if current, ok := subStore.CurrentInProgress(); ok {
			subSess.Steer(formatCurrentTaskSteering(current))
		}
	}

	sub := &subagent{
		parent:       s,
		id:           subSess.id,
		sess:         subSess,
		running:      true,
		status:       SubAgentRunning,
		done:         make(chan struct{}),
		nudgeEnabled: subagentNeedsCommunicateNudge(agent),
	}

	s.mu.Lock()
	s.subagents[sub.id] = sub
	s.mu.Unlock()

	// Subagent execution must outlive the parent tool-call context.
	// The parent may stop waiting, finish its input, or time out while the
	// child keeps running. Child cancellation is handled by subSess.Close(),
	// including when the parent session closes.
	go sub.run(context.Background(), task)

	s.emit(EventSubagentStart, SubagentStartData{
		AgentID: sub.id,
		Task:    task,
	})

	b, _ := json.Marshal(map[string]any{"agent_id": sub.id, "status": string(SubAgentRunning)})
	return string(b), nil
}

func (s *Session) sendInput(ctx context.Context, agentID string, input string) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}
	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()

	if running {
		// Inject as steering message into the running session.
		sub.sess.Steer(input)
		return "ok", nil
	}

	// Agent is idle — start a new ProcessInput round.
	sub.mu.Lock()
	sub.done = make(chan struct{})
	sub.running = true
	sub.status = SubAgentRunning
	sub.result = ""
	sub.err = nil
	sub.resultConsumed = false
	sub.endEmitted = false
	sub.mu.Unlock()

	s.emit(EventSubagentStart, SubagentStartData{
		AgentID: sub.id,
		Task:    input,
	})

	// Resume runs should also be independent of the caller's wait context.
	go sub.run(context.Background(), input)
	return "ok", nil
}

func (s *Session) waitAgent(ctx context.Context, agentID string, timeoutMS int) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}

	sub.mu.Lock()
	if sub.resultConsumed && !sub.running {
		sub.mu.Unlock()
		return "", fmt.Errorf("agent %s already completed and results already consumed; use resume_agent to resume or close_agent to clean up", agentID)
	}
	sub.mu.Unlock()

	done := sub.done
	if done == nil {
		return "", fmt.Errorf("agent has no active run")
	}
	if timeoutMS <= 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-done:
		}
	} else {
		t := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-done:
		case <-t.C:
			return "", fmt.Errorf("wait timeout")
		}
	}
	sub.mu.Lock()
	result := sub.resultSnapshotLocked()
	sub.resultConsumed = true
	sub.mu.Unlock()

	b, _ := json.Marshal(result)
	return string(b), nil
}

func (s *Session) closeAgent(agentID string) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}

	sub.sess.Close()

	// Wait for the goroutine to finish after cancellation.
	done := sub.done
	if done == nil {
		s.mu.Lock()
		delete(s.subagents, agentID)
		s.mu.Unlock()

		sub.mu.Lock()
		result := sub.resultSnapshotLocked()
		sub.mu.Unlock()

		b, _ := json.Marshal(result)
		return string(b), nil
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return "", fmt.Errorf("timed out closing subagent %s", agentID)
	}

	sub.mu.Lock()
	result := sub.resultSnapshotLocked()
	sub.mu.Unlock()

	s.mu.Lock()
	delete(s.subagents, agentID)
	s.mu.Unlock()

	b, _ := json.Marshal(result)
	return string(b), nil
}

func (s *Session) getSub(agentID string) *subagent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subagents[agentID]
}

// communicateNudge returns the message sent to a subagent that stops without
// calling the result tool. Sent at most once.
func communicateNudge(toolName string) string {
	return "You stopped without calling " + toolName + ". " +
		"You MUST call " + toolName + " with await_reply=false and a message summarizing your complete findings " +
		"before stopping. The parent agent receives ONLY the " + toolName + " message — " +
		"it cannot see anything else you did. Report your results now."
}

func (a *subagent) run(ctx context.Context, input string) {
	res, err := a.sess.ProcessInput(ctx, input, nil)

	// Auto-nudge: if a default subagent stops without calling communicate,
	// send one reminder and let it try again. This covers both empty stops
	// and repeated bare-text responses that exhausted the session-level retry
	// loop before the subagent had a chance to report back.
	shouldNudge := a.nudgeEnabled &&
		!a.sess.Communicated() &&
		(err == nil || errors.Is(err, errBareTextWithoutResultTool) || errors.Is(err, errEmptyResponseExhausted))
	if shouldNudge {
		res, err = a.sess.ProcessInput(ctx, communicateNudge(a.sess.resultToolName()), nil)
	}
	res, err = a.runSubagentStopHook(ctx, res, err)

	a.sess.mu.Lock()
	turns := a.sess.turns
	a.sess.mu.Unlock()

	a.mu.Lock()
	a.result = res
	a.err = err
	a.running = false
	a.turnsUsed = turns
	if err != nil {
		a.status = SubAgentFailed
	} else {
		a.status = SubAgentCompleted
	}
	done := a.done
	emitEnd := !a.endEmitted
	a.endEmitted = true
	status := a.status
	turnsUsed := a.turnsUsed
	parent := a.parent
	a.mu.Unlock()

	if done != nil {
		close(done)
	}
	if emitEnd && parent != nil {
		parent.emit(EventSubagentEnd, SubagentEndData{
			AgentID:   a.id,
			Status:    string(status),
			TurnsUsed: turnsUsed,
		})
	}
}

func (a *subagent) runSubagentStopHook(ctx context.Context, res string, err error) (string, error) {
	if a.sess == nil || a.sess.hookRunner == nil {
		return res, err
	}
	input := a.sess.hookInput(HookSubagentStop)
	if err != nil {
		input.Reason = err.Error()
	} else {
		input.Reason = "complete"
	}
	stopResult := a.sess.hookRunner.RunSubagentStop(ctx, input)
	for _, msg := range stopResult.SystemMessages {
		a.sess.Steer(msg)
	}
	if !stopResult.Blocked {
		return res, err
	}
	reason := strings.TrimSpace(stopResult.BlockReason)
	if reason == "" {
		reason = "SubagentStop hook blocked completion. Continue and address the hook feedback before stopping."
	}
	return a.sess.ProcessInput(ctx, reason, nil)
}

func (a *subagent) resultSnapshotLocked() SubAgentResult {
	output := a.result
	if strings.TrimSpace(output) == "" && a.err != nil {
		output = a.err.Error()
	}
	return SubAgentResult{
		Status:     a.status,
		Output:     output,
		Success:    a.err == nil,
		TurnsUsed:  a.turnsUsed,
		Transcript: a.sess.TranscriptPath(),
	}
}
