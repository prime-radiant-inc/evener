// Package cmdutil provides shared helpers for serf CLI binaries.
package cmdutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/server"
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

// StringSliceFlag implements flag.Value for a repeatable string flag.
type StringSliceFlag []string

func (f *StringSliceFlag) String() string { return strings.Join(*f, ",") }
func (f *StringSliceFlag) Set(val string) error {
	*f = append(*f, val)
	return nil
}

// ReasoningEffortResolution holds the result of resolving reasoning effort from CLI/env.
type ReasoningEffortResolution struct {
	// Set indicates a CLI/env override was provided (even if it resolves to "").
	Set bool
	// Value is the normalized effort: "low"|"medium"|"high" or "" (meaning none/clear).
	Value string
}

// ResolveReasoningEffort resolves reasoning effort from a CLI flag value and env var.
func ResolveReasoningEffort(cliValue, envValue string) (ReasoningEffortResolution, error) {
	raw := strings.TrimSpace(cliValue)
	set := raw != ""
	if raw == "" {
		raw = strings.TrimSpace(envValue)
		set = raw != ""
	}
	if !set {
		return ReasoningEffortResolution{}, nil
	}

	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "none", "null", "off", "false", "0":
		return ReasoningEffortResolution{Set: true, Value: ""}, nil
	case "low", "medium", "high", "xhigh":
		return ReasoningEffortResolution{Set: true, Value: v}, nil
	default:
		return ReasoningEffortResolution{}, fmt.Errorf("invalid reasoning effort %q (expected low|medium|high|xhigh|none)", raw)
	}
}

// MaxRoundsToConfig converts a --max-rounds CLI value to a SessionConfig value.
//
//	-1 (not specified) → 0 (applyDefaults sets to 200)
//	 0 (unlimited)     → -1 (negative means no limit)
//	>0 (explicit)      → that value
func MaxRoundsToConfig(cliValue int) int {
	switch {
	case cliValue > 0:
		return cliValue
	case cliValue == 0:
		return -1
	default:
		return 0
	}
}

// ResolveSnapshot loads a session snapshot by ID or finds the most recent one.
func ResolveSnapshot(stateDir, sessionID string, resumeLast bool) (agent.SessionSnapshot, error) {
	if resumeLast {
		list, err := agent.ListSessions(stateDir)
		if err != nil {
			return agent.SessionSnapshot{}, fmt.Errorf("list sessions: %w", err)
		}
		if len(list) == 0 {
			return agent.SessionSnapshot{}, fmt.Errorf("no saved sessions in %s", stateDir)
		}
		return list[0], nil
	}
	snap, err := agent.LoadSession(stateDir, sessionID)
	if err != nil {
		return agent.SessionSnapshot{}, fmt.Errorf("load session %s: %w", sessionID, err)
	}
	return snap, nil
}

// ListModelsFunc returns a function suitable for server.SetListModelsFunc that
// fetches models from the given client and provider.
func ListModelsFunc(client *llm.Client, providerID string) func(context.Context) ([]server.ModelsResponseItem, error) {
	return func(ctx context.Context) ([]server.ModelsResponseItem, error) {
		models, err := client.ListModels(ctx, providerID)
		if err != nil {
			return nil, err
		}
		items := make([]server.ModelsResponseItem, len(models))
		for i, m := range models {
			items[i] = server.ModelsResponseItem{
				ID:          m.ID,
				DisplayName: m.DisplayName,
			}
		}
		return items, nil
	}
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
