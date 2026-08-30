package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func requireLiveOpenAI(t *testing.T) {
	t.Helper()
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run live model tests")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
}

type scriptedProvider struct {
	name string

	mu         sync.Mutex
	requests   []llm.Request
	steps      []func(llm.Request) llm.Response
	errorSteps []func(llm.Request) (llm.Response, error)
	i          int
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	if response, ok := scriptedSessionNamerResponse(p.name, req); ok {
		return response, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, req)
	if len(p.errorSteps) > 0 {
		if p.i >= len(p.errorSteps) {
			return scriptedCommunicate("done"), nil
		}
		resp, err := p.errorSteps[p.i](req)
		p.i++
		if err != nil {
			return resp, err
		}
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

func scriptedSessionNamerResponse(providerName string, req llm.Request) (llm.Response, bool) {
	if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_schema" || len(req.Tools) != 0 {
		return llm.Response{}, false
	}
	properties, propertiesOK := req.ResponseFormat.JSONSchema["properties"].(map[string]any)
	required, requiredOK := req.ResponseFormat.JSONSchema["required"].([]string)
	if !propertiesOK || len(properties) != 1 || properties["name"] == nil || !requiredOK || len(required) != 1 || required[0] != "name" {
		return llm.Response{}, false
	}
	return llm.Response{
		Provider: providerName,
		Model:    req.Model,
		Message:  llm.Assistant(`{"name":"CLI Test"}`),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
	}, true
}

func (p *scriptedProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (p *scriptedProvider) Requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request{}, p.requests...)
}

// scriptedRegistryClient builds the client the CLI's loader would build: a
// hermetic registry carrying one instance per scripted adapter name, with the
// adapters registered as overrides so nothing reaches a transport.
func scriptedRegistryClient(t *testing.T, adapters ...*scriptedProvider) *llm.Client {
	t.Helper()
	instances := map[string]registry.Provider{}
	for _, a := range adapters {
		instances[a.Name()] = scriptedInstance(a.Name())
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("scripted registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	for _, a := range adapters {
		client.Register(a)
	}
	client.SetDefaultProvider(adapters[0].Name())
	return client
}

// scriptedInstance is the registry entry behind a scripted adapter's name.
func scriptedInstance(name string) registry.Provider {
	if name == "kimi-anthropic" {
		return registry.Provider{Base: "kimi-for-coding", APIKey: "test"}
	}
	return registry.Provider{Base: "openai", APIKey: "test"}
}

func installRunScriptedProvider(t *testing.T, adapter *scriptedProvider) {
	t.Helper()
	oldLoadClient := runLoadClient
	client := scriptedRegistryClient(t, adapter)
	runLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() {
		runLoadClient = oldLoadClient
	})
}

func installServeScriptedProvider(t *testing.T, adapter *scriptedProvider) {
	installServeScriptedProviders(t, adapter)
}

func installServeScriptedProviders(t *testing.T, adapters ...*scriptedProvider) {
	t.Helper()
	if len(adapters) == 0 {
		t.Fatal("installServeScriptedProviders requires at least one adapter")
	}
	oldLoadClient := serveLoadClient
	client := scriptedRegistryClient(t, adapters...)
	serveLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() {
		serveLoadClient = oldLoadClient
	})
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

func scriptedShellCall(id, command, mode string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{
		"command": command,
		"mode":    mode,
	})
	return llm.ToolCallData{ID: id, Name: "shell", Arguments: args, Type: "function"}
}

func scriptedUpdateGoalCall(id, status string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"status": status})
	return llm.ToolCallData{ID: id, Name: "update_goal", Arguments: args, Type: "function"}
}

func waitForFileContent(path string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data), true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return "", false
}

func TestWaitForFileContentWaitsForNonEmptyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gate.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write empty gate file: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(path, []byte("ready"), 0o644)
	}()
	t.Cleanup(func() { <-done })

	got, ok := waitForFileContent(path, time.Second)
	if !ok {
		t.Fatal("waitForFileContent timed out")
	}
	if got != "ready" {
		t.Fatalf("content = %q, want %q", got, "ready")
	}
}
