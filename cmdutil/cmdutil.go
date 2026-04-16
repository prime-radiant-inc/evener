// Package cmdutil provides shared helpers for serf CLI binaries.
package cmdutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

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
	requiredKeysRaw := os.Getenv("SERF_COMMUNICATE_REQUIRED_DATA_KEYS")
	if strings.TrimSpace(requiredKeysRaw) == "" {
		requiredKeysRaw = os.Getenv("SERF_SUBMIT_RESULT_REQUIRED_DATA_KEYS")
	}
	requiredKeys := parseCommunicateRequiredDataKeys(requiredKeysRaw)
	allowedDecisions := parseAllowedDecisions(os.Getenv("SERF_ALLOWED_DECISIONS"))

	switch strings.ToLower(provider) {
	case "openai":
		p := agent.WithCommunicateRequiredDataKeys(agent.NewOpenAIProfile(model), requiredKeys)
		return agent.WithAllowedDecisions(p, allowedDecisions), nil
	case "anthropic":
		p := agent.WithCommunicateRequiredDataKeys(agent.NewAnthropicProfile(model), requiredKeys)
		return agent.WithAllowedDecisions(p, allowedDecisions), nil
	case "google", "gemini":
		p := agent.WithCommunicateRequiredDataKeys(agent.NewGeminiProfile(model), requiredKeys)
		return agent.WithAllowedDecisions(p, allowedDecisions), nil
	case "minimax":
		p := agent.WithCommunicateRequiredDataKeys(agent.NewMiniMaxProfile(model), requiredKeys)
		return agent.WithAllowedDecisions(p, allowedDecisions), nil
	case "openrouter-anthropic":
		p := agent.WithCommunicateRequiredDataKeys(agent.NewOpenRouterAnthropicProfile(model), requiredKeys)
		return agent.WithAllowedDecisions(p, allowedDecisions), nil
	case "kimi", "glm", "openrouter":
		ctxWindow := queryModelContextWindow(provider, model)
		p := agent.WithCommunicateRequiredDataKeys(agent.NewOpenAICompatProfile(provider, model, ctxWindow), requiredKeys)
		return agent.WithAllowedDecisions(p, allowedDecisions), nil
	default:
		return nil, fmt.Errorf("unknown provider %q: must be openai, anthropic, google, minimax, openrouter-anthropic, kimi, glm, or openrouter", provider)
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
//
// Deprecated: Use ResolveSessionMeta for the new meta-based flow.
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

// ResolveSessionMeta loads a session meta by ID or finds the most recent one.
func ResolveSessionMeta(stateDir, sessionID string, resumeLast bool) (agent.SessionMeta, error) {
	if resumeLast {
		list, err := agent.ListSessionMetas(stateDir)
		if err != nil {
			return agent.SessionMeta{}, fmt.Errorf("list sessions: %w", err)
		}
		if len(list) == 0 {
			return agent.SessionMeta{}, fmt.Errorf("no saved sessions in %s", stateDir)
		}
		return list[0], nil
	}
	meta, err := agent.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		return agent.SessionMeta{}, fmt.Errorf("load session %s: %w", sessionID, err)
	}
	return meta, nil
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

func parseAllowedDecisions(raw string) []string {
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

// providerEnvConfig maps provider names to their env var and base URL info.
var providerEnvConfig = map[string]struct {
	apiKeyEnv  string
	baseURLEnv string
	defaultURL string
}{
	"kimi":       {"KIMI_API_KEY", "KIMI_BASE_URL", "https://api.moonshot.ai/v1"},
	"glm":        {"GLM_API_KEY", "GLM_BASE_URL", "https://api.z.ai/api/paas/v4"},
	"openrouter": {"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"},
}

// queryModelContextWindow queries the provider's /models endpoint for the
// context window of the given model. Returns 0 if the query fails or the
// provider doesn't report context length. Best-effort — the profile falls
// back to the embedded catalog, then to 128K.
func queryModelContextWindow(provider, model string) int {
	cfg, ok := providerEnvConfig[provider]
	if !ok {
		return 0
	}
	apiKey := strings.TrimSpace(os.Getenv(cfg.apiKeyEnv))
	if apiKey == "" {
		return 0
	}
	baseURL := strings.TrimSpace(os.Getenv(cfg.baseURLEnv))
	if baseURL == "" {
		baseURL = cfg.defaultURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return 0
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return 0
	}

	var result struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0
	}

	for _, m := range result.Data {
		if m.ID == model && m.ContextLength > 0 {
			return m.ContextLength
		}
	}
	return 0
}
