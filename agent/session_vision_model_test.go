package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func visionImageResult() tool.ExecResult {
	return tool.ExecResult{ImageData: []byte("fake-png"), ImageMediaType: "image/png", ImagePurpose: "describe it"}
}

func TestDescribeImage_OffMakesNoCall(t *testing.T) {
	t.Parallel()
	called := false
	sess := s3cov_visionSession(t, SessionConfig{VisionModel: "off"}, func(req llm.Request) llm.Response {
		called = true
		return llm.Response{Message: llm.Assistant("vision")}
	})
	if got := sess.describeImage(context.Background(), visionImageResult()); got != "" {
		t.Fatalf("off description = %q, want empty", got)
	}
	if called {
		t.Fatal("off made a vision call")
	}
}

func TestResolveVisionRoute(t *testing.T) {
	t.Parallel()
	profile := NewOpenAIProfile("session-model")
	if p, m, off := resolveVisionRoute(profile, ""); p != profile.ID() || m != "session-model" || off {
		t.Fatalf("unset = (%q, %q, %v), want session route", p, m, off)
	}
	if _, _, off := resolveVisionRoute(profile, "OFF"); !off {
		t.Fatal("OFF (case-insensitive) must be the off sentinel")
	}
	if p, m, off := resolveVisionRoute(profile, "anthropic/claude-x"); p != "anthropic" || m != "claude-x" || off {
		t.Fatalf("pinned = (%q, %q, %v)", p, m, off)
	}
	if p, m, off := resolveVisionRoute(profile, "other-model"); p != profile.ID() || m != "other-model" || off {
		t.Fatalf("bare = (%q, %q, %v), want active provider", p, m, off)
	}
	if p, m, off := resolveVisionRoute(profile, "/"); p != profile.ID() || m != "/" || off {
		t.Fatalf("malformed = (%q, %q, %v), want bare fallback", p, m, off)
	}
}

func TestDescribeImage_RoutesToConfiguredProvider(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	openai := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("session-vision")} },
	}}
	anthropic := &fakeAdapter{name: "anthropic", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("routed-vision")} },
	}}
	c := llm.NewClient()
	c.Register(openai)
	c.Register(anthropic)
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, VisionModel: "anthropic/claude-x"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()

	if got := sess.describeImage(context.Background(), visionImageResult()); got != "routed-vision" {
		t.Fatalf("routed description = %q", got)
	}
	if len(openai.Requests()) != 0 {
		t.Fatal("session provider received a vision call despite the pinned route")
	}
	reqs := anthropic.Requests()
	if len(reqs) != 1 || reqs[0].Model != "claude-x" {
		t.Fatalf("anthropic requests = %#v, want one claude-x call", reqs)
	}
}
