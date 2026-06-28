package llm

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm/providercfg"
)

// callCountAdapter wraps fakeAdapter and counts Complete calls so routing tests
// can assert that the correct adapter INSTANCE handled each request, not just
// that resp.Provider was stamped with the right name by the client layer.
type callCountAdapter struct {
	fakeAdapter
	mu    sync.Mutex
	calls int
}

func (a *callCountAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return a.fakeAdapter.Complete(ctx, req)
}

func (a *callCountAdapter) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

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
		{typ: "openai", apiStyle: "auto"}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
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
	// Replace ALL factory types with fakes first so no real adapter can be
	// constructed regardless of which factory the SUT selects for each instance.
	setupFakeInstanceFactories(t)

	// Use named adapter instances so we can assert that the CORRECT instance was
	// invoked, not just that resp.Provider was stamped correctly by the client.
	// A mutation that wires the wrong factory under each key produces a call-count
	// mismatch: the wrong adapter's count stays zero while the expected one is 1.
	workAdapter := &callCountAdapter{fakeAdapter: fakeAdapter{name: "work"}}
	kimiAdapter := &callCountAdapter{fakeAdapter: fakeAdapter{name: "kimi-corp"}}

	// Layer tracking adapters on top of the blanket fakes installed above.
	// setupFakeInstanceFactories already registered the cleanup; no extra t.Cleanup needed.
	instanceFactoriesMu.Lock()
	instanceFactories[instanceFactoryKey{typ: "openai", apiStyle: "responses"}] = func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
		if inst.Name == "work" {
			return workAdapter, nil
		}
		return &fakeAdapter{name: inst.Name}, nil
	}
	instanceFactories[instanceFactoryKey{typ: "kimi", apiStyle: ""}] = func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
		if inst.Name == "kimi-corp" {
			return kimiAdapter, nil
		}
		return &fakeAdapter{name: inst.Name}, nil
	}
	instanceFactoriesMu.Unlock()

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
	if n := workAdapter.callCount(); n != 1 {
		t.Errorf("workAdapter.calls = %d, want 1 after default route (wrong adapter was invoked)", n)
	}
	if n := kimiAdapter.callCount(); n != 0 {
		t.Errorf("kimiAdapter.calls = %d, want 0 after default route", n)
	}

	// Explicit routing to kimi-corp.
	resp, err = c.Complete(ctx, Request{Provider: "kimi-corp", Model: "kimi-k2", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Complete via kimi-corp: %v", err)
	}
	if resp.Provider != "kimi-corp" {
		t.Errorf("kimi-corp routing: provider = %q, want %q", resp.Provider, "kimi-corp")
	}
	if n := kimiAdapter.callCount(); n != 1 {
		t.Errorf("kimiAdapter.calls = %d, want 1 after kimi-corp route (wrong adapter was invoked)", n)
	}
	if n := workAdapter.callCount(); n != 1 {
		t.Errorf("workAdapter.calls = %d, want still 1 after kimi-corp route", n)
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

func TestNewFromProviders_OpenAIAutoStyleKeepsOpenAIBehavior(t *testing.T) {
	setupFakeInstanceFactories(t)

	cfg := providercfg.Config{
		Default: "adaptive",
		Instances: []providercfg.InstanceConfig{
			{Name: "adaptive", Type: "openai", APIStyle: providercfg.StyleAuto,
				BaseURL: "https://example.com/v1", APIKey: "key-adaptive"},
		},
	}

	c, err := NewFromProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}

	if got := c.behaviorTagFor("adaptive"); got != "openai" {
		t.Errorf("behaviorTagFor(adaptive) = %q, want %q", got, "openai")
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
