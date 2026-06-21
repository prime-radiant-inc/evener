// Package cmdutil provides shared helpers for serf CLI binaries.
package cmdutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/kimicoding"
	"primeradiant.com/serf/server"
)

// GitOriginURLFromDir runs "git remote get-url origin" in dir and returns the
// URL, or "" if not a git repo or no origin remote is configured.
func GitOriginURLFromDir(dir string) string {
	cmd := exec.CommandContext(context.Background(), "git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isOpenAICompatTag reports whether a profile's behavior tag identifies an
// OpenAI-compatible provider — the family whose context window the live
// /models lookup can refine. openrouter-anthropic is deliberately excluded:
// it routes Anthropic models and is not an openai-compat profile.
func isOpenAICompatTag(behaviorTag string) bool {
	switch behaviorTag {
	case "openai-compatible", "kimi", "glm", "openrouter", "ollama":
		return true
	}
	return false
}

// ResolveProfileWithLiveWindow resolves an instance ref ("instanceName/model")
// to a *Profile via provider.ResolveProfileFromConfig, then — for
// openai-compat providers — refines the context window with a best-effort live
// query to the provider's /models endpoint.
//
// The agent library resolver stays network-free (it sources the window from the
// embedded catalog); the live lookup lives here, in the app layer that already
// owns queryModelContextWindow. The query keys on the resolved profile's
// behavior tag (the provider TYPE: kimi/glm/openrouter), NOT on the ref's
// instance-name segment, which may be a user-assigned name. When the lookup
// returns 0 (no credentials, offline, or a provider that does not report a
// context length) the catalog-derived window is preserved.
//
// This is the default resolution path for both the initial profile
// (buildInitialProfile) and cross-provider switches (BuildResolveProfile /
// Session.SetModel).
func ResolveProfileWithLiveWindow(cfg providercfg.Config, ref string) (*provider.Profile, error) {
	p, err := provider.ResolveProfileFromConfig(cfg, ref)
	if err != nil {
		return nil, err
	}
	if isOpenAICompatTag(p.BehaviorTag()) {
		// Query the instance's own endpoint: an instance may set base_url in
		// providers.toml (e.g. the Kimi coding plan at api.kimi.com/coding/v1)
		// that the provider-type default does not know about.
		baseURL, apiKey := instanceEndpoint(cfg, p.ID())
		if window := queryModelContextWindow(p.BehaviorTag(), p.Model(), baseURL, apiKey); window > 0 {
			p = provider.WithContextWindow(p, window)
		}
	}
	return p, nil
}

// instanceEndpoint returns the base URL and inline api key configured for the
// instance named name, or empty strings when not found.
func instanceEndpoint(cfg providercfg.Config, name string) (baseURL, apiKey string) {
	for _, inst := range cfg.Instances {
		if inst.Name == name {
			return strings.TrimSpace(inst.BaseURL), strings.TrimSpace(inst.APIKey)
		}
	}
	return "", ""
}

// ResolveProfileForProvider resolves a bare provider/model pair to a
// *Profile WITHOUT any live network lookup. It synthesizes a
// single-instance providercfg from the provider string (reusing the same
// type/api-style roster as the seeded no-config path) and resolves it via
// provider.ResolveProfileFromConfig.
//
// This is the network-free path used by the launch-check validation probe when
// no providers.toml exists: it must confirm that provider/model names a known
// provider without credentials and without issuing the live /models query.
func ResolveProfileForProvider(providerType, model string) (*provider.Profile, error) {
	cfg := Seed([]string{providerType}, providerType, func(string) string { return "" })
	return provider.ResolveProfileFromConfig(cfg, providerType+"/"+model)
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
		return ModelRef{}, errors.New("model is required: use provider/model")
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
	return ModelRef{}, fmt.Errorf("no model: use --model provider/model or set %s=provider/model", envvars.SERFModel.Name)
}

// ResolveResumeModelRef resolves the model for an explicit session resume.
// Unlike fresh startup, persisted resume metadata wins over SERF_MODEL so an
// inherited environment variable cannot silently change the resumed session's
// model. An explicit CLI --model still overrides the persisted model.
func ResolveResumeModelRef(modelValue, envModel, resumeProvider, resumeModel string) (ModelRef, error) {
	if strings.TrimSpace(modelValue) != "" {
		return ParseModelRef(modelValue)
	}
	resumeProvider = strings.ToLower(strings.TrimSpace(resumeProvider))
	resumeModel = strings.TrimSpace(resumeModel)
	if resumeProvider != "" && resumeModel != "" {
		return ModelRef{Provider: resumeProvider, Model: resumeModel}, nil
	}
	if strings.TrimSpace(envModel) != "" {
		return ParseModelRef(envModel)
	}
	return ModelRef{}, fmt.Errorf("no model: use --model provider/model or set %s=provider/model", envvars.SERFModel.Name)
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

	v := llm.NormalizeReasoningEffort(raw)
	switch v {
	case "":
		// A disable alias (none/null/off/false/0): explicitly clear the effort.
		return ReasoningEffortResolution{Set: true, Value: ""}, nil
	case "minimal", "low", "medium", "high", "xhigh", "max":
		// "xhigh" and "max" are aliases for the top tier (OpenRouter/OpenAI say
		// "xhigh"; Anthropic/serf say "max"). Both are accepted; the per-model
		// clamp maps them to the level the chosen model actually advertises.
		return ReasoningEffortResolution{Set: true, Value: v}, nil
	default:
		return ReasoningEffortResolution{}, fmt.Errorf("invalid reasoning effort %q (expected minimal|low|medium|high|xhigh|max|none)", raw)
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

// ResolveSessionMeta loads a session meta by ID or finds the most recent one.
func ResolveSessionMeta(stateDir, sessionID string, resumeLast bool) (schema.SessionMeta, error) {
	if resumeLast {
		list, err := schema.ListSessionMetas(stateDir)
		if err != nil {
			return schema.SessionMeta{}, fmt.Errorf("list sessions: %w", err)
		}
		if len(list) == 0 {
			return schema.SessionMeta{}, fmt.Errorf("no saved sessions in %s", stateDir)
		}
		return list[0], nil
	}
	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		return schema.SessionMeta{}, fmt.Errorf("load session %s: %w", sessionID, err)
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

// ParseAllowedDecisions parses the SERF_ALLOWED_DECISIONS value into a slice
// of decision keys. It accepts JSON arrays and comma-separated values.
func ParseAllowedDecisions(raw string) []string { return parseAllowedDecisions(raw) }

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

// queryModelContextWindow queries the provider's /models endpoint for the
// context window of the given model. Returns 0 if the query fails or the
// provider doesn't report context length. Best-effort — the profile falls
// back to the embedded catalog, then to 128K.
//
// It is a package var so tests can stub the live lookup without issuing real
// HTTP requests. `provider` must be the provider TYPE / behavior tag
// (kimi/glm/openrouter), not an instance name — the lookup keys on
// providerEnvConfig by that tag.
var queryModelContextWindow = func(provider, model, instanceBaseURL, instanceAPIKey string) int {
	if provider != "kimi" && provider != "glm" && provider != "openrouter" {
		return 0
	}
	env, ok := envvars.Provider(provider)
	if !ok || len(env.APIKeyVars) == 0 {
		return 0
	}
	// Prefer the instance's configured key/base URL (providers.toml) over the
	// provider-type env var / default, so an instance with a custom endpoint
	// (the Kimi coding plan) is queried at its real /models.
	apiKey := strings.TrimSpace(instanceAPIKey)
	if apiKey == "" {
		apiKey = env.APIKeyVars[0].Trimmed()
	}
	if apiKey == "" {
		return 0
	}
	baseURL := strings.TrimSpace(instanceBaseURL)
	if baseURL == "" && len(env.BaseURLVars) > 0 {
		baseURL = env.BaseURLVars[0].Trimmed()
	}
	if baseURL == "" {
		baseURL = env.DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if provider == "kimi" {
		// Kimi For Coding gates its endpoints behind a coding-agent User-Agent
		// allowlist; announce it so the /models query survives if the gate is
		// extended to /models (today the catalog backstops the window anyway).
		req.Header.Set("User-Agent", kimicoding.UserAgent)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return 0
	}
	defer func() { _ = resp.Body.Close() }()

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
