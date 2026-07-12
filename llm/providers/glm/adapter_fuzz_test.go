package glm

import (
	"slices"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func FuzzGLMFactories(f *testing.F) {
	f.Add(uint8(0), "", "")
	f.Add(uint8(1), "  https://glm.invalid/v4  ", "instance")
	f.Add(uint8(2), "https://glm.invalid/custom", "configured")
	f.Add(uint8(2), "", "default-base")

	f.Fuzz(func(t *testing.T, mode uint8, baseURL, name string) {
		baseURL = strings.ReplaceAll(baseURL, "\x00", "")
		name = strings.ReplaceAll(name, "\x00", "")
		switch mode % 3 {
		case 0:
			t.Setenv(envvars.GLMAPIKey.Name, "")
			_, _ = llm.NewFromEnv()
		case 1:
			t.Setenv(envvars.GLMAPIKey.Name, "fuzz-key")
			t.Setenv(envvars.GLMBaseURL.Name, baseURL)
			client, err := llm.NewFromEnv()
			if err != nil {
				t.Fatalf("NewFromEnv: %v", err)
			}
			if !slices.Contains(client.ProviderNames(), "glm") {
				t.Fatal("GLM environment factory did not register glm")
			}
		case 2:
			if name == "" {
				name = "glm-fuzz"
			}
			client, err := llm.NewFromProviders(providercfg.Config{Instances: []providercfg.InstanceConfig{
				{Name: name, Type: providerName, BaseURL: baseURL, APIKey: "fuzz-key"},
			}})
			if err != nil {
				t.Fatalf("NewFromProviders: %v", err)
			}
			if !slices.Contains(client.ProviderNames(), name) {
				t.Fatalf("GLM instance factory did not register %q", name)
			}
		}
	})
}
