package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/llm"
)

// erroringAdapter fails every Complete call, exercising the LLM-error arm of
// callers that wrap the provider error.
type erroringAdapter struct{ name string }

func (a *erroringAdapter) Name() string { return a.name }
func (a *erroringAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("provider boom")
}
func (a *erroringAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func TestW2Tail_nameSession_Guards(t *testing.T) {
	profile := NewOpenAIProfile("gpt-5.2")

	if _, err := nameSession(context.Background(), nil, profile, sessionNameSourcePrompt, "text"); err == nil {
		t.Errorf("nil client should error")
	}
	if _, err := nameSession(context.Background(), llm.NewClient(), nil, sessionNameSourcePrompt, "text"); err == nil {
		t.Errorf("nil profile should error")
	}
}

func TestW2Tail_nameSession_LLMErrorWrapped(t *testing.T) {
	client := llm.NewClient()
	client.Register(&erroringAdapter{name: "openai"})
	_, err := nameSession(context.Background(), client, NewOpenAIProfile("gpt-5.2"), sessionNameSourcePrompt, "do a thing")
	if err == nil {
		t.Fatalf("expected wrapped LLM error")
	}
}

func TestW2Tail_nameSession_EmptyNameAfterSanitize(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant(`{"name":"   "}`)}
			},
		},
	})
	_, err := nameSession(context.Background(), client, NewOpenAIProfile("gpt-5.2"), sessionNameSourceCompaction, "summary text")
	if err == nil {
		t.Fatalf("expected empty-name error after sanitize")
	}
}
