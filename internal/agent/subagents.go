package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type subagent struct {
	id   string
	sess *Session

	mu      sync.Mutex
	running bool
	done    chan struct{}
	result  string
	err     error
}

func (s *Session) spawnAgent(ctx context.Context, task string) (any, error) {
	s.mu.Lock()
	depth := s.depth
	maxDepth := s.cfg.MaxSubagentDepth
	s.mu.Unlock()
	if depth >= maxDepth {
		return "", fmt.Errorf("subagent depth limit reached")
	}

	subProfile := s.profile
	subSess, err := NewSession(s.client, subProfile, s.env, s.cfg)
	if err != nil {
		return "", err
	}
	subSess.depth = depth + 1

	sub := &subagent{
		id:   subSess.id,
		sess: subSess,
		done: make(chan struct{}),
	}

	s.mu.Lock()
	s.subagents[sub.id] = sub
	s.mu.Unlock()

	go sub.run(ctx, task)

	b, _ := json.Marshal(map[string]any{"agent_id": sub.id})
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
	if sub.err != nil {
		return sub.result, sub.err
	}
	return sub.result, nil
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
	a.mu.Unlock()

	res, err := a.sess.ProcessInput(ctx, input)

	a.mu.Lock()
	a.result = res
	a.err = err
	a.running = false
	if a.done != nil {
		close(a.done)
	}
	a.mu.Unlock()
}
