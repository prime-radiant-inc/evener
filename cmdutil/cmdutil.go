// Package cmdutil provides shared helpers for serf CLI binaries.
package cmdutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"primeradiant.com/serf/agent"
)

// GitOriginURLFromDir runs "git remote get-url origin" in dir and returns the
// URL, or "" if not a git repo or no origin remote is configured.
func GitOriginURLFromDir(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SelectProfile creates the ProviderProfile for the given provider and model.
func SelectProfile(provider, model string) (agent.ProviderProfile, error) {
	requiredKeys := parseCommunicateRequiredDataKeys(os.Getenv("SERF_COMMUNICATE_REQUIRED_DATA_KEYS"))

	switch strings.ToLower(provider) {
	case "openai":
		return agent.WithCommunicateRequiredDataKeys(agent.NewOpenAIProfile(model), requiredKeys), nil
	case "anthropic":
		return agent.WithCommunicateRequiredDataKeys(agent.NewAnthropicProfile(model), requiredKeys), nil
	case "google", "gemini":
		return agent.WithCommunicateRequiredDataKeys(agent.NewGeminiProfile(model), requiredKeys), nil
	default:
		return nil, fmt.Errorf("unknown provider %q: must be openai, anthropic, or google", provider)
	}
}

// ResolveProvider returns the provider from the flag value or SERF_PROVIDER env var.
func ResolveProvider(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("SERF_PROVIDER"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("no provider: use --provider or set SERF_PROVIDER")
}

// ResolveModel returns the model from the flag value or SERF_MODEL env var.
func ResolveModel(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("SERF_MODEL"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("no model: use --model or set SERF_MODEL")
}

func parseCommunicateRequiredDataKeys(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var keys []string
		if err := json.Unmarshal([]byte(raw), &keys); err == nil && len(keys) > 0 {
			return keys
		}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
