package llm

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/llm/providercfg"
)

// setupFakeInstanceFactories replaces the instance factory registry with fakes
// that return configFakeAdapter instances. Restores the original on test cleanup.
// Returns a fake for each type that the tests exercise.
func setupFakeInstanceFactories(t *testing.T) {
	t.Helper()
	instanceFactoriesMu.Lock()
	saved := make(map[instanceFactoryKey]InstanceAdapterFactory, len(instanceFactories))
	for k, v := range instanceFactories {
		saved[k] = v
	}
	// Replace with fakes that never make network calls.
	instanceFactories = map[instanceFactoryKey]InstanceAdapterFactory{
		{typ: "openai", apiStyle: "responses"}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "openai", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "openai", apiStyle: "chat-completions"}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "anthropic", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "google", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "gemini", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "kimi", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "glm", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "openrouter", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "minimax", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "openrouter-anthropic", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
		{typ: "ollama", apiStyle: ""}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
	}
	instanceFactoriesMu.Unlock()
	t.Cleanup(func() {
		instanceFactoriesMu.Lock()
		instanceFactories = saved
		instanceFactoriesMu.Unlock()
	})
}

func TestNewFromProviders_RegistersAllInstances(t *testing.T) {
	setupFakeInstanceFactories(t)

	cfg := providercfg.Config{
		Default: "work",
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai", APIStyle: "responses", APIKey: "key-work"},
			{Name: "work2", Type: "openai", APIStyle: "responses", APIKey: "key-work2"},
			{Name: "kimi-corp", Type: "kimi", APIKey: "key-kimi"},
		},
	}

	c, err := NewFromProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}

	names := c.ProviderNames()
	wantNames := []string{"work", "work2", "kimi-corp"}
	for _, w := range wantNames {
		found := false
		for _, n := range names {
			if n == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ProviderNames() missing %q; got %v", w, names)
		}
	}
	if len(names) != len(wantNames) {
		t.Errorf("ProviderNames() len = %d, want %d; got %v", len(names), len(wantNames), names)
	}
}

func TestNewFromProviders_DefaultIsSet(t *testing.T) {
	setupFakeInstanceFactories(t)

	cfg := providercfg.Config{
		Default: "work",
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai", APIStyle: "responses", APIKey: "key-work"},
			{Name: "work2", Type: "openai", APIStyle: "responses", APIKey: "key-work2"},
			{Name: "kimi-corp", Type: "kimi", APIKey: "key-kimi"},
		},
	}

	c, err := NewFromProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}

	if got := c.DefaultProvider(); got != "work" {
		t.Errorf("DefaultProvider() = %q, want %q", got, "work")
	}
}

func TestNewFromProviders_BehaviorTagsAreSet(t *testing.T) {
	setupFakeInstanceFactories(t)

	cfg := providercfg.Config{
		Default: "work",
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai", APIStyle: "responses", APIKey: "key-work"},
			{Name: "kimi-corp", Type: "kimi", APIKey: "key-kimi"},
		},
	}

	c, err := NewFromProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}

	if got := c.behaviorTagFor("work"); got != "openai" {
		t.Errorf("behaviorTagFor(work) = %q, want %q", got, "openai")
	}
	if got := c.behaviorTagFor("kimi-corp"); got != "kimi" {
		t.Errorf("behaviorTagFor(kimi-corp) = %q, want %q", got, "kimi")
	}
}

func TestNewFromProviders_RoutingReachesCorrectAdapter(t *testing.T) {
	setupFakeInstanceFactories(t)

	cfg := providercfg.Config{
		Default: "work",
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai", APIStyle: "responses", APIKey: "key-work"},
			{Name: "kimi-corp", Type: "kimi", APIKey: "key-kimi"},
		},
	}

	c, err := NewFromProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Routing "work" via default (no Provider set).
	resp, err := c.Complete(ctx, Request{Model: "gpt-4o", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Complete via default: %v", err)
	}
	if resp.Provider != "work" {
		t.Errorf("default routing: provider = %q, want %q", resp.Provider, "work")
	}

	// Explicit routing to kimi-corp.
	resp, err = c.Complete(ctx, Request{Provider: "kimi-corp", Model: "kimi-k2", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Complete via kimi-corp: %v", err)
	}
	if resp.Provider != "kimi-corp" {
		t.Errorf("kimi-corp routing: provider = %q, want %q", resp.Provider, "kimi-corp")
	}
}

func TestNewFromProviders_ChatCompletionsStyleIsOpenAICompat(t *testing.T) {
	setupFakeInstanceFactories(t)

	cfg := providercfg.Config{
		Default: "my-compat",
		Instances: []providercfg.InstanceConfig{
			{Name: "my-compat", Type: "openai", APIStyle: "chat-completions",
				BaseURL: "https://example.com/v1", APIKey: "key-compat"},
		},
	}

	c, err := NewFromProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}

	if got := c.behaviorTagFor("my-compat"); got != "openai-compatible" {
		t.Errorf("behaviorTagFor(my-compat) = %q, want %q", got, "openai-compatible")
	}

	names := c.ProviderNames()
	found := false
	for _, n := range names {
		if n == "my-compat" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("my-compat not registered; names = %v", names)
	}
}

func TestNewFromProviders_UnknownTypeErrors(t *testing.T) {
	setupFakeInstanceFactories(t)

	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "bad", Type: "does-not-exist", APIKey: "key"},
		},
	}

	_, err := NewFromProviders(cfg)
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
}
