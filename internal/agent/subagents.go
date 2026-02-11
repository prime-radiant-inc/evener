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

func (s *Session) spawnAgent(ctx context.Context, task, model, workingDir string, maxTurns int) (any, error) {
	s.mu.Lock()
	depth := s.depth
	maxDepth := s.cfg.MaxSubagentDepth
	s.mu.Unlock()
	if depth >= maxDepth {
		return "", fmt.Errorf("subagent depth limit reached")
	}

	subProfile := s.profile
	if model = strings.TrimSpace(model); model != "" {
		subProfile = s.profile.WithModel(model)
	}

	subCfg := s.cfg
	if maxTurns > 0 {
		subCfg.MaxTurns = maxTurns
	} else if subCfg.MaxTurns <= 0 {
		subCfg.MaxTurns = 50
	}

	subEnv := s.env
	if workingDir = strings.TrimSpace(workingDir); workingDir != "" {
		subEnv = NewLocalExecutionEnvironment(workingDir)
	}

	subSess, err := NewSession(s.client, subProfile, subEnv, subCfg)
	if err != nil {
		return "", err
	}
	subSess.depth = depth + 1

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

	b, _ := json.Marshal(map[string]any{"agent_id": sub.id, "status": string(SubAgentRunning)})
	return string(b), nil
}

func (s *Session) sendInput(ctx context.Context, agentID string, input string) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}
	sub.mu.Lock()
	if sub.running {
		sub.mu.Unlock()
		return "", fmt.Errorf("agent is already running")
	}
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
	defer sub.mu.Unlock()

	result := SubAgentResult{
		Output:    sub.result,
		Success:   sub.err == nil,
		TurnsUsed: sub.turnsUsed,
	}
	b, _ := json.Marshal(result)
	if sub.err != nil {
		return string(b), sub.err
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
	return "closed", nil
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

	a.mu.Lock()
	a.result = res
	a.err = err
	a.running = false
	a.turnsUsed = a.sess.turns
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
