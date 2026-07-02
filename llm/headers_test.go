package llm

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/llm/providercfg"
)

func TestMergeHeaders(t *testing.T) {
	if got := MergeHeaders(nil, nil); got != nil {
		t.Errorf("MergeHeaders(nil,nil) = %v, want nil", got)
	}
	// Override wins on collision; base survives when not overridden.
	got := MergeHeaders(
		map[string]string{"User-Agent": "serf-default", "X-Base": "b"},
		map[string]string{"User-Agent": "user-set"},
	)
	want := map[string]string{"User-Agent": "user-set", "X-Base": "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MergeHeaders = %#v, want %#v", got, want)
	}
}

// TestMergeHeaders_CaseInsensitiveCollision guards against the base and
// override maps disagreeing only on header-name case (e.g. "User-Agent" vs
// "user-agent") producing two coexisting map keys: HTTP header names are
// case-insensitive, so that would leave the winner at request time to depend
// on nondeterministic map iteration order. Keys must canonicalize to one
// entry with the override's value.
func TestMergeHeaders_CaseInsensitiveCollision(t *testing.T) {
	got := MergeHeaders(
		map[string]string{"User-Agent": "base-value"},
		map[string]string{"user-agent": "override-value"},
	)
	want := map[string]string{"User-Agent": "override-value"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MergeHeaders = %#v, want %#v (single canonical key, override wins)", got, want)
	}
}

// captureHeadersFactory swaps the instance factory registry for one that records
// the headers each instance was constructed with, so header resolution at the
// newFromProviders choke point is observable.
func captureHeadersFactory(t *testing.T, captured map[string]map[string]string) {
	t.Helper()
	instanceFactoriesMu.Lock()
	saved := make(map[instanceFactoryKey]InstanceAdapterFactory, len(instanceFactories))
	for k, v := range instanceFactories {
		saved[k] = v
	}
	factory := func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
		captured[inst.Name] = inst.Headers
		return &fakeAdapter{name: inst.Name}, nil
	}
	instanceFactories = map[instanceFactoryKey]InstanceAdapterFactory{
		{typ: "anthropic"}: factory,
		{typ: "kimi"}:      factory,
	}
	instanceFactoriesMu.Unlock()
	t.Cleanup(func() {
		instanceFactoriesMu.Lock()
		instanceFactories = saved
		instanceFactoriesMu.Unlock()
	})
}

func TestNewFromProviders_ResolvesHeaderEnvRefs(t *testing.T) {
	t.Setenv("SERF_TEST_HDR_TOKEN", "resolved-secret")
	captured := map[string]map[string]string{}
	captureHeadersFactory(t, captured)

	cfg := providercfg.Config{Default: "ant", Instances: []providercfg.InstanceConfig{
		{Name: "ant", Type: "anthropic", Headers: map[string]string{
			"X-Gateway":     "portkey",
			"Authorization": "Bearer $SERF_TEST_HDR_TOKEN",
		}},
	}}
	if _, err := NewFromProviders(cfg); err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}
	got := captured["ant"]
	if got["Authorization"] != "Bearer resolved-secret" {
		t.Errorf("Authorization = %q, want resolved", got["Authorization"])
	}
	if got["X-Gateway"] != "portkey" {
		t.Errorf("X-Gateway = %q, want portkey", got["X-Gateway"])
	}
}

func TestNewFromProviders_MissingHeaderVar_FailsInstance(t *testing.T) {
	captured := map[string]map[string]string{}
	captureHeadersFactory(t, captured)

	cfg := providercfg.Config{Default: "ant", Instances: []providercfg.InstanceConfig{
		{Name: "ant", Type: "anthropic", Headers: map[string]string{"X-Key": "$SERF_TEST_HDR_ABSENT"}},
		{Name: "kimi", Type: "kimi"},
	}}

	// Hard-fail path.
	if _, err := NewFromProviders(cfg); err == nil {
		t.Fatal("NewFromProviders: expected error for missing header var")
	}

	// Partial-init path: the bad instance is skipped, the healthy one remains.
	c, initErrs, err := NewFromAvailableProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromAvailableProviders: %v", err)
	}
	if len(initErrs) != 1 {
		t.Fatalf("initErrs = %v, want exactly one", initErrs)
	}
	names := c.ProviderNames()
	if len(names) != 1 || names[0] != "kimi" {
		t.Errorf("ProviderNames = %v, want [kimi]", names)
	}
}
