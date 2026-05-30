package providerconfig

import (
	"fmt"
	"sort"
	"strings"
)

// Seed builds a Config from a list of provider names, a default name, and a
// function that resolves the base URL for a given type tag. It never sets
// APIKey. Instances are sorted by Name for determinism.
func Seed(providerNames []string, defaultName string, getBaseURL func(typ string) string) Config {
	instances := make([]InstanceConfig, 0, len(providerNames))
	for _, name := range providerNames {
		var inst InstanceConfig
		switch name {
		case "openai-compatible":
			inst = InstanceConfig{
				Name:     "openai-compatible",
				Type:     "openai",
				APIStyle: StyleChatCompletions,
				BaseURL:  getBaseURL("openai-compatible"),
			}
		case "openai":
			inst = InstanceConfig{
				Name:     "openai",
				Type:     "openai",
				APIStyle: StyleResponses,
			}
		default:
			baseURL := getBaseURL(name)
			inst = InstanceConfig{
				Name:    name,
				Type:    Type(name),
				BaseURL: baseURL,
			}
		}
		instances = append(instances, inst)
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Name < instances[j].Name
	})
	return Config{
		Default:   defaultName,
		Instances: instances,
	}
}

// BaseURLEnvVar returns the environment variable name for the base URL of the
// given provider type tag. Returns "" for unknown types (e.g. ollama, whose base
// URL the materializer resolves from OLLAMA_BASE_URL/OLLAMA_HOST).
func BaseURLEnvVar(typ string) string {
	switch typ {
	case "openai":
		return "OPENAI_BASE_URL"
	case "anthropic":
		return "ANTHROPIC_BASE_URL"
	case "google":
		return "GEMINI_BASE_URL"
	case "kimi":
		return "KIMI_BASE_URL"
	case "glm":
		return "GLM_BASE_URL"
	case "openrouter":
		return "OPENROUTER_BASE_URL"
	case "minimax":
		return "MINIMAX_BASE_URL"
	case "openai-compatible":
		return "OPENAI_COMPATIBLE_BASE_URL"
	default:
		return ""
	}
}

// Marshal emits providers.toml content for cfg. It never emits api_key even
// if InstanceConfig.APIKey is set. The output round-trips through Load.
func Marshal(cfg Config) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "default = %q\n", cfg.Default)
	for _, inst := range cfg.Instances {
		fmt.Fprintf(&b, "\n[instances.%s]\n", inst.Name)
		fmt.Fprintf(&b, "type = %q\n", inst.Type)
		if inst.APIStyle != "" {
			fmt.Fprintf(&b, "api_style = %q\n", inst.APIStyle)
		}
		if inst.BaseURL != "" {
			fmt.Fprintf(&b, "base_url = %q\n", inst.BaseURL)
		}
		if inst.Quirks != "" {
			fmt.Fprintf(&b, "quirks = %q\n", inst.Quirks)
		}
	}
	return []byte(b.String()), nil
}
