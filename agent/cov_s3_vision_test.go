package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestVisionPromptContractIsUnconditional(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("vision")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("vision")} },
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()

	purposes := []string{
		"Describe the layout and visible controls.",
		"Transcribe the rendered text exactly, including punctuation.",
	}
	var suffix string
	for _, purpose := range purposes {
		if got := sess.describeImage(context.Background(), tool.ExecResult{ImageData: []byte("image"), ImageIntent: purpose}); got != "vision" {
			t.Fatalf("vision response = %q", got)
		}
		requests := adapter.Requests()
		request := requests[len(requests)-1]
		if len(request.Messages) != 1 || len(request.Messages[0].Content) != 2 {
			t.Fatalf("vision request content shape = %#v", request.Messages)
		}
		prompt := request.Messages[0].Content[0].Text
		wantPrefix := strings.TrimSpace(purpose) + "\n\n"
		if !strings.HasPrefix(prompt, wantPrefix) {
			t.Fatalf("prompt does not preserve purpose: %q", prompt)
		}
		gotSuffix := strings.TrimPrefix(prompt, wantPrefix)
		if gotSuffix != visionRequestContract {
			t.Fatalf("prompt contract = %q, want one shared contract", gotSuffix)
		}
		if strings.Count(prompt, visionRequestContract) != 1 {
			t.Fatalf("prompt contains %d contract copies, want one", strings.Count(prompt, visionRequestContract))
		}
		if suffix == "" {
			suffix = gotSuffix
		} else if gotSuffix != suffix {
			t.Fatal("description and transcription purposes received different contracts")
		}
	}
}

// s3cov_visionSession wires a session whose fake adapter records the last vision
// request and returns the scripted response.
func s3cov_visionSession(t *testing.T, cfg SessionConfig, step func(req llm.Request) llm.Response) *Session {
	t.Helper()
	dir := t.TempDir()
	if cfg.StateDir == "" {
		cfg.StateDir = dir
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{step}})
	profile := NewOpenAIProfile("m")
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()
	return sess
}

func TestS3Cov_DescribeImage_EmptyData(t *testing.T) {
	t.Parallel()
	called := false
	sess := s3cov_visionSession(t, SessionConfig{}, func(req llm.Request) llm.Response {
		called = true
		return llm.Response{Message: llm.Assistant("x")}
	})
	if got := sess.describeImage(context.Background(), tool.ExecResult{}); got != "" {
		t.Fatalf("expected empty for no image data, got %q", got)
	}
	if called {
		t.Fatal("no API call should be made for empty image data")
	}
}

func TestS3Cov_DescribeImage_ExplorerSkips(t *testing.T) {
	t.Parallel()
	called := false
	sess := s3cov_visionSession(t, SessionConfig{AgentName: "explorer"}, func(req llm.Request) llm.Response {
		called = true
		return llm.Response{Message: llm.Assistant("x")}
	})
	got := sess.describeImage(context.Background(), tool.ExecResult{ImageData: []byte("png")})
	if got != "" || called {
		t.Fatalf("explorer should skip vision; got=%q called=%v", got, called)
	}
}

func TestS3Cov_DescribeImage_PDFDocumentPart(t *testing.T) {
	t.Parallel()
	var sawDocument bool
	sess := s3cov_visionSession(t, SessionConfig{}, func(req llm.Request) llm.Response {
		for _, m := range req.Messages {
			for _, p := range m.Content {
				if p.Kind == llm.ContentDocument && p.Document != nil {
					sawDocument = true
				}
			}
		}
		return llm.Response{Message: llm.Assistant("a pdf about cats")}
	})
	got := sess.describeImage(context.Background(), tool.ExecResult{
		ImageData:      []byte("%PDF-1.4 fake"),
		ImageMediaType: "application/pdf",
	})
	if got != "a pdf about cats" {
		t.Fatalf("description = %q", got)
	}
	if !sawDocument {
		t.Fatal("PDF media type should be sent as a ContentDocument part")
	}
}

func TestS3Cov_DescribeImage_ErrorReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&errAdapter{name: "openai", err: errors.New("vision boom")})
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()
	got := sess.describeImage(context.Background(), tool.ExecResult{
		ImageData:      []byte("png"),
		ImageMediaType: "image/png",
	})
	if got != "" {
		t.Fatalf("expected empty on API error, got %q", got)
	}
}
