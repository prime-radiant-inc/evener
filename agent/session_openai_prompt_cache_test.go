package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestOpenAIPromptCacheDefaults_RequestCapture(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_, err = sess.ProcessInput(context.Background(), "run", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("requests: got 0 want at least 1")
	}
	req := reqs[0]
	if strings.TrimSpace(req.PromptCacheKey) == "" {
		t.Fatal("PromptCacheKey is empty")
	}
	if got, want := req.PromptCacheRetention, "24h"; got != want {
		t.Fatalf("PromptCacheRetention = %q, want %q", got, want)
	}
}

func Test_openAIModelSupports24hPromptCache(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "gpt-5.5", want: true},
		{model: "gpt-5.4-mini", want: true},
		{model: "gpt-4.1", want: true},
		{model: "gpt-4o-mini", want: false},
		{model: "gpt-50", want: false},
		{model: "gpt-5whatever", want: false},
		{model: "gpt-4.10", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := openAIModelSupports24hPromptCache(tt.model); got != tt.want {
				t.Fatalf("openAIModelSupports24hPromptCache(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestOpenAIPromptCacheDefaults_FallbackUnsupportedModelClearsRetention(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	permErr := llm.ErrorFromHTTPStatus("openai", 403, "denied", nil, nil)
	f := &modelTrackingAdapter{
		name: "openai",
		respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "gpt-5.5":
				return llm.Response{}, permErr
			case "gpt-4o-mini":
				return finalResponse("fallback answered"), nil
			default:
				t.Fatalf("unexpected model %q", req.Model)
				return llm.Response{}, nil
			}
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.5"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	if _, err := sess.ProcessInput(context.Background(), "run", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d want 2", len(reqs))
	}
	if got, want := reqs[0].PromptCacheRetention, "24h"; got != want {
		t.Fatalf("primary PromptCacheRetention = %q, want %q", got, want)
	}
	if got := reqs[1].PromptCacheRetention; got != "" {
		t.Fatalf("fallback PromptCacheRetention = %q, want empty", got)
	}
	if got := reqs[1].PromptCacheKey; got != "" {
		t.Fatalf("fallback PromptCacheKey = %q, want empty", got)
	}
}

func TestOpenAIPromptCacheDefaults_PreserveExplicitRequestValues(t *testing.T) {
	sess := &Session{id: "session-123", profile: NewOpenAIProfile("gpt-5.5")}
	req := llm.Request{
		Model:                "gpt-5.5",
		Provider:             "openai",
		PromptCacheKey:       "explicit-key",
		PromptCacheRetention: "1h",
	}

	sess.applyModelRequestMetadata(&req)

	if got, want := req.PromptCacheKey, "explicit-key"; got != want {
		t.Fatalf("PromptCacheKey = %q, want %q", got, want)
	}
	if got, want := req.PromptCacheRetention, "1h"; got != want {
		t.Fatalf("PromptCacheRetention = %q, want %q", got, want)
	}
}
