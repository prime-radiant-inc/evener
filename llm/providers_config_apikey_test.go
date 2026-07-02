package llm

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm/providercfg"
)

// NewFromProviders must hand adapters the RESOLVED api key ($ENV references
// expanded), and a missing variable must fail only that instance.
func TestNewFromProviders_ResolvesAPIKeyEnvReferences(t *testing.T) {
	t.Setenv("SERF_TEST_PROVIDER_KEY", "sk-resolved")

	var gotKeys []string
	RegisterInstanceAdapterFactory("apikey-probe", "", func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
		gotKeys = append(gotKeys, inst.APIKey)
		return &fakeAdapter{name: inst.Name}, nil
	})

	cfg := providercfg.Config{
		Default: "a",
		Instances: []providercfg.InstanceConfig{
			{Name: "a", Type: "apikey-probe", APIKey: "$SERF_TEST_PROVIDER_KEY"},
			{Name: "b", Type: "apikey-probe", APIKey: "sk-literal"},
		},
	}
	if _, err := NewFromProviders(cfg); err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}
	if len(gotKeys) != 2 || gotKeys[0] != "sk-resolved" || gotKeys[1] != "sk-literal" {
		t.Errorf("factory keys = %v, want [sk-resolved sk-literal]", gotKeys)
	}
}

func TestNewFromProviders_MissingEnvKeyFailsInstance(t *testing.T) {
	RegisterInstanceAdapterFactory("apikey-probe", "", func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
		return &fakeAdapter{name: inst.Name}, nil
	})
	cfg := providercfg.Config{
		Default: "bad",
		Instances: []providercfg.InstanceConfig{
			{Name: "bad", Type: "apikey-probe", APIKey: "$SERF_TEST_KEY_DEFINITELY_UNSET"},
		},
	}
	_, err := NewFromProviders(cfg)
	if err == nil {
		t.Fatal("NewFromProviders succeeded with an unset $ENV key")
	}
	if !strings.Contains(err.Error(), "SERF_TEST_KEY_DEFINITELY_UNSET") || !strings.Contains(err.Error(), "bad") {
		t.Errorf("error %q should name the variable and the instance", err)
	}

	// Partial init keeps healthy instances and reports the broken one.
	t.Setenv("SERF_TEST_PROVIDER_KEY", "sk-ok")
	cfg.Instances = append(cfg.Instances, providercfg.InstanceConfig{
		Name: "good", Type: "apikey-probe", APIKey: "$SERF_TEST_PROVIDER_KEY",
	})
	cfg.Default = "good"
	c, initErrs, err := NewFromAvailableProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromAvailableProviders: %v", err)
	}
	if len(initErrs) != 1 || !strings.Contains(initErrs[0].Error(), "SERF_TEST_KEY_DEFINITELY_UNSET") {
		t.Errorf("initErrs = %v, want one error for the unset variable", initErrs)
	}
	if names := c.ProviderNames(); len(names) != 1 || names[0] != "good" {
		t.Errorf("ProviderNames = %v, want [good]", names)
	}
}
