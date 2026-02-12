package llm

import (
	"context"
	"os"
	"testing"
	"time"
)

type envFakeAdapter struct {
	name string
}

func (a *envFakeAdapter) Name() string { return a.name }
func (a *envFakeAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	return Response{Provider: a.name, Model: req.Model, Message: Assistant("ok")}, nil
}
func (a *envFakeAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = ctx
	_ = req
	return nil, nil
}

func TestNewFromEnv_UsesRegisteredFactories(t *testing.T) {
	// Isolate global registry.
	envFactoriesMu.Lock()
	saved := append([]EnvAdapterFactory{}, envFactories...)
	envFactories = nil
	envFactoriesMu.Unlock()
	t.Cleanup(func() {
		envFactoriesMu.Lock()
		envFactories = saved
		envFactoriesMu.Unlock()
	})

	t.Setenv("TEST_LLM_ENABLED", "1")
	RegisterEnvAdapterFactory(func() (ProviderAdapter, bool, error) {
		if os.Getenv("TEST_LLM_ENABLED") == "" {
			return nil, false, nil
		}
		return &envFakeAdapter{name: "openai"}, true, nil
	})

	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := c.Complete(ctx, Request{Model: "m", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "openai" {
		t.Fatalf("provider: got %q want %q", resp.Provider, "openai")
	}
}

func TestDefaultClient_LazyInitializationFromEnv(t *testing.T) {
	// Isolate global registry.
	envFactoriesMu.Lock()
	savedFactories := append([]EnvAdapterFactory{}, envFactories...)
	envFactories = nil
	envFactoriesMu.Unlock()
	t.Cleanup(func() {
		envFactoriesMu.Lock()
		envFactories = savedFactories
		envFactoriesMu.Unlock()
	})

	// Reset default client state.
	defaultClientMu.Lock()
	savedInit := defaultClientInit
	savedClient := defaultClient
	savedErr := defaultClientErr
	defaultClientInit = false
	defaultClient = nil
	defaultClientErr = nil
	defaultClientMu.Unlock()
	t.Cleanup(func() {
		defaultClientMu.Lock()
		defaultClientInit = savedInit
		defaultClient = savedClient
		defaultClientErr = savedErr
		defaultClientMu.Unlock()
	})

	t.Setenv("TEST_LLM_ENABLED", "1")
	RegisterEnvAdapterFactory(func() (ProviderAdapter, bool, error) {
		if os.Getenv("TEST_LLM_ENABLED") == "" {
			return nil, false, nil
		}
		return &envFakeAdapter{name: "openai"}, true, nil
	})

	c, err := DefaultClient()
	if err != nil {
		t.Fatalf("DefaultClient: %v", err)
	}
	if c == nil {
		t.Fatalf("expected non-nil client")
	}
}

func TestSetDefaultClient_OverridesLazyInit(t *testing.T) {
	// Reset default client state.
	defaultClientMu.Lock()
	savedInit := defaultClientInit
	savedClient := defaultClient
	savedErr := defaultClientErr
	defaultClientInit = false
	defaultClient = nil
	defaultClientErr = nil
	defaultClientMu.Unlock()
	t.Cleanup(func() {
		defaultClientMu.Lock()
		defaultClientInit = savedInit
		defaultClient = savedClient
		defaultClientErr = savedErr
		defaultClientMu.Unlock()
	})

	explicit := NewClient()
	explicit.Register(&envFakeAdapter{name: "openai"})
	SetDefaultClient(explicit)

	got, err := DefaultClient()
	if err != nil {
		t.Fatalf("DefaultClient: %v", err)
	}
	if got != explicit {
		t.Fatalf("expected DefaultClient to return explicitly set instance")
	}
}
