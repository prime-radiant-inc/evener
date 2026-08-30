package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func TestOpenAIPromptCacheDefaults_RequestCapture(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	if got, want := req.PromptCacheKey, "evener-session-"+sess.ID(); got != want {
		t.Fatalf("PromptCacheKey = %q, want %q", got, want)
	}
	if got, want := req.PromptCacheRetention, "24h"; got != want {
		t.Fatalf("PromptCacheRetention = %q, want %q", got, want)
	}
}

// TestOpenAIPromptCacheDefaults_FallbackRowDropsRetentionKeepsKey pins the two
// independent Fields gates (spec §7.5): the session puts both prompt-cache
// fields on every request and llm.ShapeRequest keeps whichever the resolved
// row sends. gpt-4o-mini's row carries prompt_cache_key but not
// prompt_cache_retention, so the fallback attempt keeps the session's cache
// key and loses the 24h retention.
func TestOpenAIPromptCacheDefaults_FallbackRowDropsRetentionKeepsKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	permErr := llm.ErrorFromHTTPStatus("openai", 403, "denied", nil, nil)
	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	if got, want := reqs[1].PromptCacheKey, "evener-session-"+sess.ID(); got != want {
		t.Fatalf("fallback PromptCacheKey = %q, want %q", got, want)
	}
}

func TestOpenAIPromptCacheDefaults_PreserveExplicitRequestValues(t *testing.T) {
	t.Parallel()
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

// TestPromptCacheDefaults_StampedForRenamedInstance pins that the session
// stamps the prompt-cache fields from its session id alone: a renamed OpenAI
// instance (id "work") is no more or less eligible than any other, because
// eligibility is the resolved row's decision at dispatch.
func TestPromptCacheDefaults_StampedForRenamedInstance(t *testing.T) {
	t.Parallel()
	renamedProfile := namedOpenAIInstanceProfile("work", "gpt-5.5")
	sess := &Session{id: "sess-abc", profile: renamedProfile}
	req := llm.Request{Model: "gpt-5.5", Provider: renamedProfile.ID()}

	sess.applyModelRequestMetadata(&req)

	if got, want := req.PromptCacheKey, "evener-session-sess-abc"; got != want {
		t.Fatalf("PromptCacheKey = %q, want %q", got, want)
	}
	if got, want := req.PromptCacheRetention, "24h"; got != want {
		t.Fatalf("PromptCacheRetention = %q, want %q", got, want)
	}
}

// TestPromptCacheDefaults_AnthropicRowDropsBothFields is the absence half of
// the two Fields gates: the anthropic row sends neither prompt_cache_key nor
// prompt_cache_retention, so llm.ShapeRequest strips both before the request
// reaches the endpoint — even though the session stamped them.
func TestPromptCacheDefaults_AnthropicRowDropsBothFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "anthropic", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	sess, err := NewSession(c, newAnthropicProfile("claude-opus-4-6"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.ProcessInput(context.Background(), "run", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("requests: got 0 want at least 1")
	}
	if got := reqs[0].PromptCacheKey; got != "" {
		t.Fatalf("PromptCacheKey = %q, want empty — the anthropic row does not send it", got)
	}
	if got := reqs[0].PromptCacheRetention; got != "" {
		t.Fatalf("PromptCacheRetention = %q, want empty — the anthropic row does not send it", got)
	}
}
