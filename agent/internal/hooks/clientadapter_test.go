package hooks

import (
	"context"
	"testing"

	"primeradiant.com/serf/llm"
)

// --- clientAdapter tests ---

func TestClientAdapter_SatisfiesPromptHookClient(t *testing.T) {
	// Verify that clientAdapter implements promptHookClient at compile time.
	var _ promptHookClient = clientAdapter{}
}

// --- clientAdapter ---

func TestClientAdapter_Generate(t *testing.T) {
	// Create a mock llm.Client using a fake adapter
	client := llm.NewClient()
	adapter := clientAdapter{client}

	// We can't easily test a real call without a provider, but we can verify
	// that the adapter method exists and the type assertions work.
	_ = adapter

	// Verify the interface is satisfied (compile-time check above is sufficient,
	// but let's also check at runtime).
	var iface promptHookClient = adapter
	_ = iface
}

func TestClientAdapter_Generate_DelegatesToComplete(t *testing.T) {
	// This test verifies that clientAdapter.Generate delegates to Client.Complete.
	// We need a provider that the Client can dispatch to.
	calls := 0
	fakeAdapter := &fakeProviderAdapter{
		name: "fake",
		completeFn: func(ctx context.Context, req llm.Request) (llm.Response, error) {
			calls++
			return llm.Response{
				Message: llm.Message{
					Role:    llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hello from fake"}},
				},
			}, nil
		},
	}

	client := llm.NewClient()
	client.Register(fakeAdapter)

	ca := clientAdapter{client}
	resp, err := ca.Generate(context.Background(), llm.Request{
		Model:    "any-model",
		Provider: "fake",
		Messages: []llm.Message{llm.User("test")},
	})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 Complete call, got %d", calls)
	}
	if resp.Message.Text() != "hello from fake" {
		t.Errorf("response text = %q, want %q", resp.Message.Text(), "hello from fake")
	}
}

// fakeProviderAdapter is a minimal llm.ProviderAdapter for testing.
type fakeProviderAdapter struct {
	name       string
	completeFn func(ctx context.Context, req llm.Request) (llm.Response, error)
}

func (f *fakeProviderAdapter) Name() string { return f.name }
func (f *fakeProviderAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return f.completeFn(ctx, req)
}
func (f *fakeProviderAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, nil
}
