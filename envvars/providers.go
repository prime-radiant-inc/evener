package envvars

import "strings"

type ProviderEnv struct {
	Name            string
	AuthModes       []string
	APIKeyVars      []Var
	InjectAPIKeyVar Var
	BaseURLVars     []Var
	DefaultBaseURL  string
	RequiresBaseURL bool
}

func Provider(name string) (ProviderEnv, bool) {
	name = strings.ToLower(name)
	for _, p := range providers {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderEnv{}, false
}

func Providers() []ProviderEnv {
	return append([]ProviderEnv(nil), providers...)
}

func APIKeyVars(provider string) []Var {
	p, ok := Provider(provider)
	if !ok {
		return nil
	}
	return append([]Var(nil), p.APIKeyVars...)
}

func InjectAPIKeyVar(provider string) (Var, bool) {
	p, ok := Provider(provider)
	if !ok || p.InjectAPIKeyVar.Name == "" {
		return Var{}, false
	}
	return p.InjectAPIKeyVar, true
}

func BaseURLVar(providerType string) (Var, bool) {
	p, ok := Provider(providerType)
	if !ok || len(p.BaseURLVars) == 0 {
		return Var{}, false
	}
	return p.BaseURLVars[0], true
}

func AuthModes(provider string) []string {
	p, ok := Provider(provider)
	if !ok {
		return nil
	}
	return append([]string(nil), p.AuthModes...)
}

var providers = []ProviderEnv{
	{
		Name:            "openai",
		AuthModes:       []string{"apiKey", "oauth"},
		APIKeyVars:      []Var{OpenAIAPIKey},
		InjectAPIKeyVar: OpenAIAPIKey,
		BaseURLVars:     []Var{OpenAIBaseURL},
		DefaultBaseURL:  "https://api.openai.com/v1",
	},
	{
		Name:            "anthropic",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{AnthropicAPIKey},
		InjectAPIKeyVar: AnthropicAPIKey,
		BaseURLVars:     []Var{AnthropicBaseURL},
	},
	{
		Name:            "google",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{GeminiAPIKey, GoogleAPIKey},
		InjectAPIKeyVar: GeminiAPIKey,
		BaseURLVars:     []Var{GeminiBaseURL},
	},
	{
		Name:            "gemini",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{GeminiAPIKey, GoogleAPIKey},
		InjectAPIKeyVar: GeminiAPIKey,
		BaseURLVars:     []Var{GeminiBaseURL},
	},
	{
		Name:            "minimax",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{MinimaxAPIKey},
		InjectAPIKeyVar: MinimaxAPIKey,
		BaseURLVars:     []Var{MinimaxBaseURL},
	},
	{
		Name:            "openrouter",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{OpenRouterAPIKey},
		InjectAPIKeyVar: OpenRouterAPIKey,
		BaseURLVars:     []Var{OpenRouterBaseURL},
		DefaultBaseURL:  "https://openrouter.ai/api/v1",
	},
	{
		Name:            "openrouter-anthropic",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{OpenRouterAPIKey},
		InjectAPIKeyVar: OpenRouterAPIKey,
		BaseURLVars:     []Var{OpenRouterBaseURL},
		DefaultBaseURL:  "https://openrouter.ai/api/v1",
	},
	{
		Name:            "kimi",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{KimiAPIKey},
		InjectAPIKeyVar: KimiAPIKey,
		BaseURLVars:     []Var{KimiBaseURL},
		DefaultBaseURL:  "https://api.moonshot.ai/v1",
	},
	{
		Name:            "kimi-anthropic",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{KimiCodingAPIKey},
		InjectAPIKeyVar: KimiCodingAPIKey,
		BaseURLVars:     []Var{KimiCodingBaseURL},
		DefaultBaseURL:  "https://api.kimi.com/coding",
	},
	{
		Name:            "glm",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{GLMAPIKey},
		InjectAPIKeyVar: GLMAPIKey,
		BaseURLVars:     []Var{GLMBaseURL},
		DefaultBaseURL:  "https://api.z.ai/api/paas/v4",
	},
	{
		Name:            "openai-compatible",
		AuthModes:       []string{"apiKey"},
		APIKeyVars:      []Var{OpenAICompatibleAPIKey},
		InjectAPIKeyVar: OpenAICompatibleAPIKey,
		BaseURLVars:     []Var{OpenAICompatibleBaseURL},
		RequiresBaseURL: true,
	},
	{
		Name:        "ollama",
		AuthModes:   []string{"none"},
		APIKeyVars:  []Var{OllamaAPIKey},
		BaseURLVars: []Var{OllamaBaseURL, OllamaHost},
		DefaultBaseURL:  "http://localhost:11434/v1",
	},
}
