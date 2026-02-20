package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

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

// defaultSubagentInstructions is appended to the system prompt for default
// subagents (no plugin agent_type). It overrides the parent's delegation
// directives which would tell the subagent to spawn further subagents it
// cannot create.
const defaultSubagentInstructions = `You are a subagent. Do the work yourself using the tools available to you:
glob, grep, read_file, shell, edit_file, write_file, apply_patch. Do NOT try to spawn
further subagents.

Your job is to complete the task and report your findings.

CRITICAL: When done, call communicate(result) with a message that contains the COMPLETE,
DETAILED results of your work. The parent agent receives ONLY this message — it cannot
see anything else you did. If you explored files, include the file contents or relevant
excerpts. If you ran commands, include the full output. If you found something, describe
it with specifics (file paths, line numbers, code, data).

BAD: communicate(result, message="Survey complete. Found Python project with tests.")
GOOD: communicate(result, message="Project structure:\n/app/main.py (150 lines) - Flask
web app with routes for /api/users and /api/items\n/app/models.py (80 lines) - SQLAlchemy
models: User(id, name, email), Item(id, title, price)\n...")

Always attempt the task. Never refuse or ask for clarification.`

type subagent struct {
	id   string
	sess *Session

	mu        sync.Mutex
	running   bool
	status    SubAgentStatus
	turnsUsed int
	done      chan struct{}
	result    string
	err       error
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
		subCfg.MaxTurns = 50
	}
	// Apply system prompt override.
	if agent != nil && strings.TrimSpace(agent.SystemPrompt) != "" {
		// Plugin agents get their custom system prompt.
		subCfg.UserInstructionOverride = agent.SystemPrompt
	} else {
		// Default subagents get focused instructions that override the
		// parent's delegation directives (which would tell them to spawn
		// further subagents they cannot create due to depth limits).
		subCfg.UserInstructionOverride = defaultSubagentInstructions
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

	// Restrict subagent tools to the plugin agent's allow list.
	if agent != nil && len(agent.Tools) > 0 {
		allowed := make(map[string]bool, len(agent.Tools))
		for _, t := range agent.Tools {
			allowed[t] = true
		}
		subSess.reg.Restrict(allowed)
	}

	sub := &subagent{
		id:     subSess.id,
		sess:   subSess,
		status: SubAgentRunning,
		done:   make(chan struct{}),
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
	sub.mu.Unlock()

	go sub.run(ctx, input)
	return "ok", nil
}

func (s *Session) waitAgent(ctx context.Context, agentID string, timeoutMS int) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}
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

func (a *subagent) run(ctx context.Context, input string) {
	a.mu.Lock()
	a.running = true
	a.status = SubAgentRunning
	a.mu.Unlock()

	res, err := a.sess.ProcessInput(ctx, input)

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
