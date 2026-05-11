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
//
// If outputSchemaJSON is non-empty (after trimming whitespace), it is parsed
// as JSON into a map and applied as the communicate tool's output schema via
// agent.WithCommunicateOutputSchema. Parse errors return an error.
//
// SERF_ALLOWED_DECISIONS remains honored independently and stacks on top of
// any supplied output schema.
func SelectProfile(provider, model, outputSchemaJSON string) (agent.ProviderProfile, error) {
	var outputSchema map[string]any
	if trimmed := strings.TrimSpace(outputSchemaJSON); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &outputSchema); err != nil {
			return nil, fmt.Errorf("invalid --output-schema: %w", err)
		}
	}
	allowedDecisions := parseAllowedDecisions(os.Getenv("SERF_ALLOWED_DECISIONS"))

	// Normalize once: registered adapter names are lowercase, so the
	// profile's id must also be lowercase or the runtime won't find a
	// matching provider for mixed-case provider/model values.
	// Also trim surrounding whitespace so sloppy env/config values work the
	// same as canonical provider names.
	provider = strings.ToLower(strings.TrimSpace(provider))

	var raw agent.ProviderProfile
	switch provider {
	case "openai":
		raw = agent.NewOpenAIProfile(model)
	case "anthropic":
		raw = agent.NewAnthropicProfile(model)
	case "google", "gemini":
		raw = agent.NewGeminiProfile(model)
	case "minimax":
		raw = agent.NewMiniMaxProfile(model)
	case "openrouter-anthropic":
		raw = agent.NewOpenRouterAnthropicProfile(model)
	case "kimi", "glm", "openrouter", "ollama":
		ctxWindow := queryModelContextWindow(provider, model)
		raw = agent.NewOpenAICompatProfile(provider, model, ctxWindow)
	default:
		return nil, fmt.Errorf("unknown provider %q: must be openai, anthropic, google, minimax, openrouter-anthropic, kimi, glm, openrouter, or ollama", provider)
	}

	p := agent.WithCommunicateOutputSchema(raw, outputSchema)
	p = agent.WithAllowedDecisions(p, allowedDecisions)
	return p, nil
}

// ModelRef is a provider-qualified model identifier.
type ModelRef struct {
	Provider string
	Model    string
}

func (r ModelRef) Qualified() string {
	if r.Provider == "" || r.Model == "" {
		return strings.Trim(r.Provider+"/"+r.Model, "/")
	}
	return r.Provider + "/" + r.Model
}

// ParseModelRef parses "provider/model" into a ModelRef. Model names may
// contain additional slashes; the provider is the first path segment.
func ParseModelRef(raw string) (ModelRef, error) {
	raw = strings.TrimSpace(raw)
	provider, model, ok := strings.Cut(raw, "/")
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if raw == "" {
		return ModelRef{}, fmt.Errorf("model is required: use provider/model")
	}
	if !ok || provider == "" || model == "" {
		return ModelRef{}, fmt.Errorf("model %q must use provider/model", raw)
	}
	return ModelRef{Provider: provider, Model: model}, nil
}

// ResolveModelRef resolves a provider-qualified model from CLI, env, or resume
// metadata. New invocations require --model/SERF_MODEL to be provider/model;
// resumed sessions keep their persisted provider and bare model.
func ResolveModelRef(modelValue, envModel, resumeProvider, resumeModel string) (ModelRef, error) {
	if strings.TrimSpace(modelValue) != "" {
		return ParseModelRef(modelValue)
	}
	if strings.TrimSpace(envModel) != "" {
		return ParseModelRef(envModel)
	}
	resumeProvider = strings.ToLower(strings.TrimSpace(resumeProvider))
	resumeModel = strings.TrimSpace(resumeModel)
	if resumeProvider != "" && resumeModel != "" {
		return ModelRef{Provider: resumeProvider, Model: resumeModel}, nil
	}
	return ModelRef{}, fmt.Errorf("no model: use --model provider/model or set SERF_MODEL=provider/model")
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
