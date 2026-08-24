package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestReadFileVisionRequest_ExactTranscriptionMode(t *testing.T) {
	t.Parallel()

	const descriptionSentinel = "VISION_DESCRIPTION_SENTINEL_7F3289"
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant(descriptionSentinel)}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	dir := t.TempDir()
	profile := NewOpenAIProfile("vision-test")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	registry := tool.NewRegistry()
	if err := registerFileTools(registry, &toolDeps{readGuard: readGuard{trackRead: func(string) {}}}); err != nil {
		t.Fatalf("registerFileTools: %v", err)
	}
	call := llm.ToolCallData{
		ID:        "call-image",
		Name:      "read_file",
		Arguments: []byte(`{"file_path":"picture.png","purpose":"Transcribe the rendered text exactly, including every case and symbol."}`),
	}
	result := registry.ExecuteCall(context.Background(), &w3sub_readFileEnv{
		output: "[image: picture.png]\n" + base64.StdEncoding.EncodeToString(validPNGFixture(t)),
	}, call)
	if result.IsError {
		t.Fatalf("read_file result = %+v", result)
	}
	if !requestsLiteralTranscription(result.ImagePurpose) {
		t.Fatalf("read_file purpose selected ordinary description mode")
	}
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tool.ExecResult{result}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("vision requests = %d, want 1", len(requests))
	}
	req := requests[0]
	if len(req.Messages) != 1 || req.Messages[0].Role != llm.RoleUser {
		t.Fatalf("vision messages = %#v, want one user message", req.Messages)
	}
	parts := req.Messages[0].Content
	if len(parts) != 2 || parts[0].Kind != llm.ContentText || parts[0].Text == "" {
		t.Fatalf("vision content = %#v, want non-empty text followed by media", parts)
	}
	image := parts[1]
	if image.Kind != llm.ContentImage || image.Image == nil || image.Document != nil {
		t.Fatalf("vision media part = %#v, want image", image)
	}
	if !bytes.Equal(image.Image.Data, result.ImageData) || image.Image.MediaType != result.ImageMediaType || image.Image.Detail != "original" {
		t.Fatalf("vision image = %#v, want read_file image payload", image.Image)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("vision tools = %#v, want none", req.Tools)
	}
	if req.ReasoningEffort == nil {
		t.Fatal("vision request omitted reasoning effort for a reasoning profile")
	}
	if got, want := *req.ReasoningEffort, llm.ClampReasoningEffort(*req.ReasoningEffort, profile.ReasoningEffortLevels()); got != want {
		t.Fatalf("vision reasoning effort = %q, want a profile-supported level", got)
	}
	steered := sess.drainSteering()
	if len(steered) != 1 {
		t.Fatalf("steering messages = %d, want 1", len(steered))
	}
	if steered[0].Kind != events.SteeringKindImageDescription {
		t.Fatalf("steering kind = %q, want %q", steered[0].Kind, events.SteeringKindImageDescription)
	}
	if !strings.Contains(steered[0].Text, descriptionSentinel) {
		t.Fatalf("vision description sentinel was not delivered: %q", steered[0].Text)
	}
}

func TestDescribeImage_OrdinaryPurposeUsesDescriptionMode(t *testing.T) {
	t.Parallel()

	const (
		purposeSentinel     = "VISION_PURPOSE_SENTINEL_ORDINARY"
		descriptionSentinel = "VISION_DESCRIPTION_SENTINEL_ORDINARY"
	)
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant(descriptionSentinel)}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile("vision-test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	if got := sess.describeImage(context.Background(), tool.ExecResult{
		ImageData:      []byte("png"),
		ImageMediaType: "image/png",
		ImagePurpose:   purposeSentinel,
	}); got != descriptionSentinel {
		t.Fatalf("description = %q, want opaque provider result", got)
	}
	if requestsLiteralTranscription(purposeSentinel) {
		t.Fatal("opaque ordinary purpose selected exact-transcription mode")
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("vision requests = %d, want 1", len(requests))
	}
	parts := requests[0].Messages[0].Content
	if len(parts) != 2 || parts[0].Kind != llm.ContentText || parts[1].Kind != llm.ContentImage {
		t.Fatalf("ordinary vision content = %#v, want text and image parts", parts)
	}
}

func TestReadFileSchema_IsPresentInComposedModelRequest(t *testing.T) {
	t.Parallel()

	sess := newSession(t, withoutGitSnapshot())
	req := sess.buildModelRequest(sess.profile, "system", []llm.Message{llm.User("inspect an image")}, sess.allToolDefinitions(0), "")
	for _, def := range req.Tools {
		if def.Name != "read_file" {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("read_file parameters have no properties: %#v", def.Parameters)
		}
		purpose, ok := properties["purpose"].(map[string]any)
		if !ok {
			t.Fatalf("read_file parameters have no purpose property: %#v", properties)
		}
		if got := purpose["type"]; got != "string" {
			t.Fatalf("read_file purpose type = %#v, want string", got)
		}
		return
	}
	t.Fatal("composed model request did not include read_file")
}
