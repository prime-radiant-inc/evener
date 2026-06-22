package main

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func requireLiveOpenAI(t *testing.T) {
	t.Helper()
	if os.Getenv("SERF_LIVE_TESTS") != "1" {
		t.Skip("set SERF_LIVE_TESTS=1 to run live model tests")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
}

type scriptedProvider struct {
	name string

	mu       sync.Mutex
	requests []llm.Request
	steps    []func(llm.Request) llm.Response
	i        int
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, req)
	if p.i >= len(p.steps) {
		return scriptedCommunicate("done"), nil
	}
	resp := p.steps[p.i](req)
	p.i++
	if resp.Provider == "" {
		resp.Provider = p.name
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	if resp.Finish.Reason == "" {
		resp.Finish = llm.FinishReason{Reason: llm.FinishReasonStop}
	}
	return resp, nil
}

func (p *scriptedProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (p *scriptedProvider) Requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request{}, p.requests...)
}

func installRunScriptedProvider(t *testing.T, adapter *scriptedProvider) {
	t.Helper()
	oldLoadClient := runLoadClient
	runLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		client := llm.NewClient()
		client.Register(adapter)
		return client, scriptedProviderConfig(adapter.Name()), true, nil
	}
	t.Cleanup(func() {
		runLoadClient = oldLoadClient
	})
}

func installServeScriptedProvider(t *testing.T, adapter *scriptedProvider) {
	t.Helper()
	oldLoadClient := serveLoadClient
	serveLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		client := llm.NewClient()
		client.Register(adapter)
		return client, scriptedProviderConfig(adapter.Name()), true, nil
	}
	t.Cleanup(func() {
		serveLoadClient = oldLoadClient
	})
}

func scriptedProviderConfig(name string) providercfg.Config {
	return providercfg.Config{
		Default: name,
		Instances: []providercfg.InstanceConfig{
			{Name: name, Type: "openai"},
		},
	}
}

func scriptedCommunicate(message string) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":  message,
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	return scriptedToolCalls(llm.ToolCallData{
		ID:        "communicate_test_call",
		Name:      "communicate",
		Arguments: args,
		Type:      "function",
	})
}

func scriptedToolCalls(calls ...llm.ToolCallData) llm.Response {
	parts := make([]llm.ContentPart, len(calls))
	for i := range calls {
		parts[i] = llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &calls[i]}
	}
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: parts},
		Finish:  llm.FinishReason{Reason: llm.FinishReasonToolCalls},
	}
}

func scriptedWriteFileCall(id, path, content string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"file_path": path, "content": content})
	return llm.ToolCallData{ID: id, Name: "write_file", Arguments: args, Type: "function"}
}

func scriptedUpdateGoalCall(id, status string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"status": status})
	return llm.ToolCallData{ID: id, Name: "update_goal", Arguments: args, Type: "function"}
}

func waitForFileContent(path string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data), true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return "", false
}
