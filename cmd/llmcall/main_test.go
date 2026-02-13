package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

type fakeAdapter struct {
	name  string
	resp  llm.Response
	check func(req llm.Request)
}

func (a *fakeAdapter) Name() string { return a.name }

func (a *fakeAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	if a.check != nil {
		a.check(req)
	}
	if a.resp.Message.Role == "" {
		a.resp.Model = req.Model
		a.resp.Provider = a.name
		a.resp.Message = llm.Assistant("OK")
		a.resp.Finish = llm.FinishReason{Reason: llm.FinishReasonStop}
	}
	return a.resp, nil
}

func (a *fakeAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, fmt.Errorf("not implemented")
}

func TestRunLLMCall_DefaultsToNoSystemPrompt(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		check: func(req llm.Request) {
			if len(req.Messages) < 1 {
				t.Fatalf("expected at least 1 message")
			}
			if req.Messages[0].Role != llm.RoleUser {
				t.Fatalf("expected first message role=user, got %q", req.Messages[0].Role)
			}
		},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt:   "hello",
		provider: "fake",
		model:    "m1",
		format:   "text",
		stdout:   &stdout,
		stderr:   &stderr,
		client:   c,
	})
	if err != nil {
		t.Fatalf("runLLMCall: %v", err)
	}
}

func TestRunLLMCall_SetsToolChoiceNoneAndNoTools(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		check: func(req llm.Request) {
			if req.ToolChoice == nil || req.ToolChoice.Mode != "none" {
				t.Fatalf("expected tool_choice.mode=none, got %#v", req.ToolChoice)
			}
			if len(req.Tools) != 0 {
				t.Fatalf("expected no tools, got %d", len(req.Tools))
			}
		},
		resp: llm.Response{
			Model:    "m1",
			Provider: "fake",
			Message:  llm.Assistant("PONG"),
			Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt:   "Reply with PONG",
		provider: "fake",
		model:    "m1",
		format:   "text",
		stdout:   &stdout,
		stderr:   &stderr,
		client:   c,
	})
	if err != nil {
		t.Fatalf("runLLMCall: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != "PONG" {
		t.Fatalf("expected PONG, got %q", stdout.String())
	}
}

func TestRunLLMCall_ErrorsOnToolCalls(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		resp: llm.Response{
			Model:    "m1",
			Provider: "fake",
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{
						Kind: llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{
							ID:        "t1",
							Name:      "write_file",
							Arguments: json.RawMessage(`{"file_path":"x","content":"y"}`),
							Type:      "function",
						},
					},
				},
			},
			Finish: llm.FinishReason{Reason: llm.FinishReasonToolCalls},
		},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt:   "cause tool calls",
		provider: "fake",
		model:    "m1",
		format:   "text",
		stdout:   &stdout,
		stderr:   &stderr,
		client:   c,
	})
	if err == nil {
		t.Fatal("expected error when tool calls are returned")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "tool calls") {
		t.Fatalf("expected tool-call error, got: %v", err)
	}
}

func TestRunLLMCall_JSONFormat_ParsesAndPrints(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		check: func(req llm.Request) {
			if req.ResponseFormat == nil || req.ResponseFormat.Type != "json" {
				t.Fatalf("expected response_format.type=json, got %#v", req.ResponseFormat)
			}
		},
		resp: llm.Response{
			Model:    "m1",
			Provider: "fake",
			Message:  llm.Assistant(`{"a": 1}`),
			Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt:   "Return JSON",
		provider: "fake",
		model:    "m1",
		format:   "json",
		stdout:   &stdout,
		stderr:   &stderr,
		client:   c,
	})
	if err != nil {
		t.Fatalf("runLLMCall: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != `{"a":1}` {
		t.Fatalf("expected compact JSON, got %q", stdout.String())
	}
}

func TestRunLLMCall_Schema_ValidatesAndPrints(t *testing.T) {
	schema := `{
  "type": "object",
  "properties": { "ok": { "type": "boolean" } },
  "required": ["ok"],
  "additionalProperties": false
}`
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		check: func(req llm.Request) {
			if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_schema" {
				t.Fatalf("expected response_format.type=json_schema, got %#v", req.ResponseFormat)
			}
			if req.ResponseFormat.JSONSchema == nil {
				t.Fatalf("expected json_schema in response_format, got %#v", req.ResponseFormat)
			}
		},
		resp: llm.Response{
			Model:    "m1",
			Provider: "fake",
			Message:  llm.Assistant(`{"ok": true}`),
			Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		},
	})

	var stdout, stderr bytes.Buffer
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt:   "Return JSON matching schema",
		provider: "fake",
		model:    "m1",
		schema:   schemaPath,
		stdout:   &stdout,
		stderr:   &stderr,
		client:   c,
	})
	if err != nil {
		t.Fatalf("runLLMCall: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != `{"ok":true}` {
		t.Fatalf("expected JSON output, got %q", stdout.String())
	}
}
