package agent

import (
	"context"
	"encoding/json"
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
	Output    string `json:"output"`
	Success   bool   `json:"success"`
	TurnsUsed int    `json:"turns_used"`
}

// defaultSubagentInstructions is the role-specific prompt for default subagents
// (no agent_type). Appended after the common subagent base prompt.
const defaultSubagentInstructions = `You are a general-purpose subagent. Do the work yourself using the tools
available to you: glob, grep, read_file, shell, edit_file, write_file, apply_patch.
Do NOT try to spawn further subagents.

Your job is to complete the task and report your findings.`

type subagent struct {
	id   string
	sess *Session

	mu             sync.Mutex
	running        bool
	status         SubAgentStatus
	turnsUsed      int
	done           chan struct{}
	result         string
	err            error
	resultConsumed bool // true after first successful wait returns results
	nudgeEnabled   bool // true for default subagents that should be nudged to submit_result
}

func (s *Session) spawnAgent(ctx context.Context, task, model, workingDir string, maxTurns int, agentType string) (any, error) {
	s.mu.Lock()
	depth := s.depth
	maxDepth := s.cfg.MaxSubagentDepth
	s.mu.Unlock()
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
		agent = &a
	}

	subProfile := s.profile
	if model = strings.TrimSpace(model); model != "" {
		subProfile = s.profile.WithModel(model)
	}
	// Plugin agent model takes precedence (unless "inherit" or empty).
	if agent != nil && agent.Model != "inherit" && agent.Model != "" {
		subProfile = s.profile.WithModel(agent.Model)
	}

	subCfg := s.cfg
	subCfg.MCPConfigFiles = nil
	subCfg.MCPInline = nil
	subCfg.ParentSessionID = s.id
	subCfg.SubagentTask = task
	subCfg.Depth = depth + 1
	if callID, ok := ctx.Value(ctxToolCallID).(string); ok {
		subCfg.ParentToolCallID = callID
	}
	if maxTurns > 0 {
		subCfg.MaxTurns = maxTurns
	} else {
		subCfg.MaxTurns = 500
	}
	// Compose subagent system prompt: common base + role-specific instructions.
	// All subagents get the subagent base (submit_result, workflow, non-interactive)
	// followed by their role-specific guidance. This replaces the parent's base.md
	// to avoid inherited delegation/skill instructions the subagent can't follow.
	subBase := SubagentBasePrompt()
	var rolePrompt string
	if agent != nil && strings.TrimSpace(agent.SystemPrompt) != "" {
		rolePrompt = agent.SystemPrompt
	} else {
		rolePrompt = defaultSubagentInstructions
	}
	composed := subBase + "\n\n" + rolePrompt

	// Inject skill content referenced by the plugin agent.
	if agent != nil && len(agent.Skills) > 0 {
		for _, skillName := range agent.Skills {
			body, err := ResolveSkillContent(s.skills, skillName)
			if err != nil {
				continue
			}
			if body != "" {
				composed += "\n\n" + body
			}
		}
	}

	subCfg.BasePromptOverride = composed
	subProfile = subProfile.WithBasePrompt(composed)

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

	// Restrict subagent tools.
	if agent != nil && len(agent.Tools) > 0 {
		// Plugin agents get their explicit allow list.
		allowed := make(map[string]bool, len(agent.Tools))
		for _, t := range agent.Tools {
			allowed[t] = true
		}
		subSess.reg.RestrictKeepingResultTool(allowed, subSess.resultToolName())
	} else {
		// Default subagents cannot delegate further — remove delegation tools.
		subSess.reg.Remove("spawn_agent")
		subSess.reg.Remove("resume_agent")
		subSess.reg.Remove("wait")
		subSess.reg.Remove("close_agent")
	}

	sub := &subagent{
		id:           subSess.id,
		sess:         subSess,
		status:       SubAgentRunning,
		done:         make(chan struct{}),
		nudgeEnabled: agent == nil, // default subagents get nudged to submit_result
	}

	s.mu.Lock()
	s.subagents[sub.id] = sub
	s.mu.Unlock()

	go sub.run(ctx, task)

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
	sub.resultConsumed = false
	sub.mu.Unlock()

	go sub.run(ctx, input)
	return "ok", nil
}

func (s *Session) waitAgent(ctx context.Context, agentID string, timeoutMS int) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}

	// Prevent closed-channel polling: if results were already consumed and
	// the agent is not running, return an error instead of silently returning
	// stale data. This prevents the model from burning rounds re-waiting.
	sub.mu.Lock()
	if sub.resultConsumed && !sub.running {
		sub.mu.Unlock()
		return "", fmt.Errorf("agent %s already completed and results already consumed; use resume_agent to resume or close_agent to clean up", agentID)
	}
	sub.mu.Unlock()

	done := sub.done
	if done == nil {
		return "", fmt.Errorf("agent has no running task")
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
	result := SubAgentResult{
		Output:    sub.result,
		Success:   sub.err == nil,
		TurnsUsed: sub.turnsUsed,
	}
	status := sub.status
	subErr := sub.err
	sub.resultConsumed = true
	sub.mu.Unlock()

	s.emit(EventSubagentEnd, SubagentEndData{
		AgentID:   agentID,
		Status:    string(status),
		TurnsUsed: result.TurnsUsed,
	})

	b, _ := json.Marshal(result)
	if subErr != nil {
		return string(b), subErr
	}
	return string(b), nil
}

func (s *Session) closeAgent(agentID string) (any, error) {
	s.mu.Lock()
	sub := s.subagents[agentID]
	delete(s.subagents, agentID)
	s.mu.Unlock()
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}

	sub.sess.Close()

	// Wait for the goroutine to finish.
	select {
	case <-sub.done:
	case <-time.After(5 * time.Second):
	}

	sub.mu.Lock()
	status := sub.status
	result := sub.result
	turnsUsed := sub.turnsUsed
	sub.mu.Unlock()

	s.emit(EventSubagentEnd, SubagentEndData{
		AgentID:   agentID,
		Status:    string(status),
		TurnsUsed: turnsUsed,
	})

	b, _ := json.Marshal(map[string]any{
		"status":     string(status),
		"output":     result,
		"turns_used": turnsUsed,
	})
	return string(b), nil
}

func (s *Session) getSub(agentID string) *subagent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subagents[agentID]
}

// submitResultNudge returns the message sent to a subagent that stops without
// calling the result tool. Sent at most once.
func submitResultNudge(toolName string) string {
	return "You stopped without calling " + toolName + ". " +
		"You MUST call " + toolName + " with a message summarizing your complete findings " +
		"before stopping. The parent agent receives ONLY the " + toolName + " message — " +
		"it cannot see anything else you did. Report your results now."
}

func (a *subagent) run(ctx context.Context, input string) {
	a.mu.Lock()
	a.running = true
	a.status = SubAgentRunning
	a.mu.Unlock()

	res, err := a.sess.ProcessInput(ctx, input)

	// Auto-nudge: if a default subagent stopped without calling submit_result,
	// send one reminder and let it try again. This addresses the empty-result
	// failure mode where subagents do work but forget to report back.
	if a.nudgeEnabled && err == nil && !a.sess.ResultDelivered() {
		res, err = a.sess.ProcessInput(ctx, submitResultNudge(a.sess.resultToolName()))
	}

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
	if a.done != nil {
		close(a.done)
	}
	a.mu.Unlock()
}
