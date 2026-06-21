package cmdutil

import (
	"sort"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm/providercfg"
)

// Seed builds a providercfg.Config from a list of provider names, a default
// name, and a function that resolves the base URL for a given type tag. It never
// sets APIKey. Instances are sorted by Name for determinism.
//
// The serf-specific roster (the openai/openai-compatible API-style defaults)
// lives here in cmdutil rather than in the providercfg schema package, which
// stays provider-agnostic.
func Seed(providerNames []string, defaultName string, getBaseURL func(typ string) string) providercfg.Config {
	instances := make([]providercfg.InstanceConfig, 0, len(providerNames))
	for _, name := range providerNames {
		var inst providercfg.InstanceConfig
		switch name {
		case "openai-compatible":
			inst = providercfg.InstanceConfig{
				Name:     "openai-compatible",
				Type:     "openai",
				APIStyle: providercfg.StyleChatCompletions,
				BaseURL:  getBaseURL("openai-compatible"),
			}
		case "openai":
			inst = providercfg.InstanceConfig{
				Name:     "openai",
				Type:     "openai",
				APIStyle: providercfg.StyleResponses,
				BaseURL:  getBaseURL("openai"),
			}
		default:
			baseURL := getBaseURL(name)
			inst = providercfg.InstanceConfig{
				Name:    name,
				Type:    providercfg.Type(name),
				BaseURL: baseURL,
			}
		}
		instances = append(instances, inst)
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Name < instances[j].Name
	})
	return providercfg.Config{
		Default:   defaultName,
		Instances: instances,
	}
}

// BaseURLEnvVar returns the environment variable name for the base URL of the
// given provider type tag. Returns "" for unknown types (e.g. ollama, whose base
// URL the materializer resolves from OLLAMA_BASE_URL/OLLAMA_HOST).
func BaseURLEnvVar(typ string) string {
	if v, ok := envvars.BaseURLVar(typ); ok && typ != "ollama" {
		return v.Name
	}
	return ""
}
