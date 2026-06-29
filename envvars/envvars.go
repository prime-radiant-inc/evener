// Package envvars defines Serf's supported environment variables.
package envvars

import (
	"os"
	"strings"
)

type Visibility string

const (
	Public    Visibility = "public"
	Internal  Visibility = "internal"
	Inherited Visibility = "inherited"
	Tooling   Visibility = "tooling"
)

type Var struct {
	Name       string
	Summary    string
	Secret     bool
	Visibility Visibility
}

func (v Var) Getenv() string {
	return os.Getenv(v.Name)
}

func (v Var) LookupEnv() (string, bool) {
	return os.LookupEnv(v.Name)
}

func (v Var) Trimmed() string {
	return strings.TrimSpace(os.Getenv(v.Name))
}

func (v Var) From(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	return getenv(v.Name)
}

func (v Var) FromTrimmed(getenv func(string) string) string {
	return strings.TrimSpace(v.From(getenv))
}

func (v Var) Setenv(value string) error {
	return os.Setenv(v.Name, value)
}

func (v Var) Unsetenv() error {
	return os.Unsetenv(v.Name)
}

func (v Var) Assignment(value string) string {
	return v.Name + "=" + value
}

var (
	SERFAllowedDecisions            = Var{Name: "SERF_ALLOWED_DECISIONS", Summary: "Restricts tool-decision modes allowed by the active profile.", Visibility: Public}
	SERFFluencyModel                = Var{Name: "SERF_FLUENCY_MODEL", Summary: "Default model for the tool-fluency development harness.", Visibility: Tooling}
	SERFHubAddr                     = Var{Name: "SERF_HUB_ADDR", Summary: "Default hub address for serf-tui.", Visibility: Public}
	SERFHubAuthToken                = Var{Name: "SERF_HUB_AUTH_TOKEN", Summary: "Hub capability token for serf-tui.", Secret: true, Visibility: Public}
	SERFHubBin                      = Var{Name: "SERF_HUB_BIN", Summary: "Path to the serf-hub binary used by serf-tui autostart.", Visibility: Public}
	SERFHubEditorURLTemplate        = Var{Name: "SERF_HUB_EDITOR_URL_TEMPLATE", Summary: "Open-in-editor URL template; use {path} for the encoded path.", Visibility: Public}
	SERFHubSpawned                  = Var{Name: "SERF_HUB_SPAWNED", Summary: "Set by serf-hub for spawned serf serve daemons.", Visibility: Internal}
	SERFHubSpawnedCodex             = Var{Name: "SERF_HUB_SPAWNED_CODEX", Summary: "Set by serf-hub for spawned Codex app-server processes.", Visibility: Internal}
	SERFHubToken                    = Var{Name: "SERF_HUB_TOKEN", Summary: "Per-hub bearer token passed to spawned serf serve daemons.", Secret: true, Visibility: Internal}
	SERFLoginHeadless               = Var{Name: "SERF_LOGIN_HEADLESS", Summary: "Overrides OpenAI login flow detection: 1 for device-code, 0 for browser.", Visibility: Public}
	SERFLogRawHTTP                  = Var{Name: "SERF_LOG_RAW_HTTP", Summary: "Includes raw provider HTTP bodies in API logs when set to 1/true/yes/on.", Visibility: Public}
	SERFModel                       = Var{Name: "SERF_MODEL", Summary: "Default model as provider/model when --model is omitted.", Visibility: Public}
	SERFOpenAIResponsesContinuation = Var{Name: "SERF_OPENAI_RESPONSES_CONTINUATION", Summary: "Default OpenAI Responses continuation mode: off|auto. CLI and launch config override it.", Visibility: Public}
	SERFProvider                    = Var{Name: "SERF_PROVIDER", Summary: "Fallback provider for llmcall when --provider and LLM_PROVIDER are unset.", Visibility: Public}
	SERFProvidersConfig             = Var{Name: "SERF_PROVIDERS_CONFIG", Summary: "Path to providers.toml.", Visibility: Public}
	SERFReasoningEffort             = Var{Name: "SERF_REASONING_EFFORT", Summary: "Default reasoning effort: minimal|low|medium|high|xhigh|max|none.", Visibility: Public}
	SERFRecordAppwire               = Var{Name: "SERF_RECORD_APPWIRE", Summary: "Records raw AppWire WebSocket frames to appwire-frames.jsonl for fuzz-corpus harvesting when set to 1/true/yes/on.", Visibility: Tooling}
	SERFRecordHTTP                  = Var{Name: "SERF_RECORD_HTTP", Summary: "Records inbound hub HTTP requests to hub-http.jsonl for fuzz-corpus harvesting when set to 1/true/yes/on.", Visibility: Tooling}
	SERFFuzzCaptureEnv              = Var{Name: "SERF_FUZZ_CAPTURE_ENV", Summary: "Marks a dedicated capture box so serf-fuzz-harvest --keep-values is permitted (real, unscrubbed values; local-only).", Visibility: Tooling}
	SERFRunDir                      = Var{Name: "SERF_RUN_DIR", Summary: "Rendezvous directory passed by serf-hub to spawned daemons.", Visibility: Internal}
	SERFStateDir                    = Var{Name: "SERF_STATE_DIR", Summary: "Overrides the Serf state root.", Visibility: Public}
	SERFTUILogFile                  = Var{Name: "SERF_TUI_LOG_FILE", Summary: "Writes serf-tui startup diagnostics to this file.", Visibility: Public}

	LLMModel    = Var{Name: "LLM_MODEL", Summary: "Model for llmcall when --model is unset; checked before SERF_MODEL.", Visibility: Public}
	LLMProvider = Var{Name: "LLM_PROVIDER", Summary: "Provider for llmcall when --provider is unset; checked before SERF_PROVIDER.", Visibility: Public}

	OpenAIAPIKey                   = Var{Name: "OPENAI_API_KEY", Summary: "OpenAI API key.", Secret: true, Visibility: Public}
	OpenAIBaseURL                  = Var{Name: "OPENAI_BASE_URL", Summary: "OpenAI API-key backend base URL.", Visibility: Public}
	OpenAIChatGPTBaseURL           = Var{Name: "OPENAI_CHATGPT_BASE_URL", Summary: "OpenAI OAuth ChatGPT/Codex backend base URL.", Visibility: Public}
	OpenAIChatGPTClientID          = Var{Name: "OPENAI_CHATGPT_CLIENT_ID", Summary: "OpenAI OAuth client id override for tests and development.", Visibility: Tooling}
	OpenAICompatibleAPIKey         = Var{Name: "OPENAI_COMPATIBLE_API_KEY", Summary: "OpenAI-compatible provider API key.", Secret: true, Visibility: Public}
	OpenAICompatibleBaseURL        = Var{Name: "OPENAI_COMPATIBLE_BASE_URL", Summary: "Required base URL for openai-compatible provider registration.", Visibility: Public}
	OpenAICompatibleProviderQuirks = Var{Name: "OPENAI_COMPATIBLE_PROVIDER_QUIRKS", Summary: "Quirks preset for the openai-compatible adapter.", Visibility: Public}
	OpenAIOrgID                    = Var{Name: "OPENAI_ORG_ID", Summary: "OpenAI organization header for API-key requests.", Visibility: Public}
	OpenAIProjectID                = Var{Name: "OPENAI_PROJECT_ID", Summary: "OpenAI project header for API-key requests.", Visibility: Public}
	AnthropicAPIKey                = Var{Name: "ANTHROPIC_API_KEY", Summary: "Anthropic API key.", Secret: true, Visibility: Public}
	AnthropicBaseURL               = Var{Name: "ANTHROPIC_BASE_URL", Summary: "Anthropic API base URL override.", Visibility: Public}
	GeminiAPIKey                   = Var{Name: "GEMINI_API_KEY", Summary: "Google Gemini API key; checked before GOOGLE_API_KEY.", Secret: true, Visibility: Public}
	GeminiBaseURL                  = Var{Name: "GEMINI_BASE_URL", Summary: "Google Gemini API base URL override.", Visibility: Public}
	GoogleAPIKey                   = Var{Name: "GOOGLE_API_KEY", Summary: "Google Gemini API key fallback.", Secret: true, Visibility: Public}
	GLMAPIKey                      = Var{Name: "GLM_API_KEY", Summary: "GLM API key.", Secret: true, Visibility: Public}
	GLMBaseURL                     = Var{Name: "GLM_BASE_URL", Summary: "GLM API base URL.", Visibility: Public}
	KimiAPIKey                     = Var{Name: "KIMI_API_KEY", Summary: "Kimi API key.", Secret: true, Visibility: Public}
	KimiBaseURL                    = Var{Name: "KIMI_BASE_URL", Summary: "Kimi API base URL.", Visibility: Public}
	KimiCodingAPIKey               = Var{Name: "KIMI_CODING_API_KEY", Summary: "Kimi coding-plan API key.", Secret: true, Visibility: Public}
	KimiCodingBaseURL              = Var{Name: "KIMI_CODING_BASE_URL", Summary: "Kimi coding-plan Anthropic-compatible base URL.", Visibility: Public}
	MinimaxAPIKey                  = Var{Name: "MINIMAX_API_KEY", Summary: "MiniMax API key.", Secret: true, Visibility: Public}
	MinimaxBaseURL                 = Var{Name: "MINIMAX_BASE_URL", Summary: "MiniMax API base URL override.", Visibility: Public}
	OllamaAPIKey                   = Var{Name: "OLLAMA_API_KEY", Summary: "Optional API key for authenticated Ollama proxies or Ollama Cloud.", Secret: true, Visibility: Public}
	OllamaBaseURL                  = Var{Name: "OLLAMA_BASE_URL", Summary: "Ollama OpenAI-compatible base URL; must include /v1.", Visibility: Public}
	OllamaHost                     = Var{Name: "OLLAMA_HOST", Summary: "Ollama canonical host; used when OLLAMA_BASE_URL is unset.", Visibility: Public}
	OpenRouterAPIKey               = Var{Name: "OPENROUTER_API_KEY", Summary: "OpenRouter API key.", Secret: true, Visibility: Public}
	OpenRouterBaseURL              = Var{Name: "OPENROUTER_BASE_URL", Summary: "OpenRouter API base URL.", Visibility: Public}

	XDGCacheHome   = Var{Name: "XDG_CACHE_HOME", Summary: "Base for Serf cache data.", Visibility: Inherited}
	XDGConfigHome  = Var{Name: "XDG_CONFIG_HOME", Summary: "Base for Serf config, skills, plugins, and MCP config discovery.", Visibility: Inherited}
	XDGStateHome   = Var{Name: "XDG_STATE_HOME", Summary: "Base for Serf state when SERF_STATE_DIR is unset.", Visibility: Inherited}
	CargoHome      = Var{Name: "CARGO_HOME", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	Display        = Var{Name: "DISPLAY", Summary: "Used to auto-detect graphical sessions for OpenAI login.", Visibility: Inherited}
	GoModCache     = Var{Name: "GOMODCACHE", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	GoPath         = Var{Name: "GOPATH", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	Home           = Var{Name: "HOME", Summary: "Home directory fallback for state/config paths and path expansion.", Visibility: Inherited}
	HomeDrive      = Var{Name: "HOMEDRIVE", Summary: "Windows home drive fallback.", Visibility: Inherited}
	HomePath       = Var{Name: "HOMEPATH", Summary: "Windows home path fallback.", Visibility: Inherited}
	Lang           = Var{Name: "LANG", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	NVMDir         = Var{Name: "NVM_DIR", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	Path           = Var{Name: "PATH", Summary: "Executable search path inherited by local commands and child processes.", Visibility: Inherited}
	PyenvRoot      = Var{Name: "PYENV_ROOT", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	RustupHome     = Var{Name: "RUSTUP_HOME", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	Shell          = Var{Name: "SHELL", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	SSHConnection  = Var{Name: "SSH_CONNECTION", Summary: "Used to auto-detect headless OpenAI login sessions.", Visibility: Inherited}
	SSHTTY         = Var{Name: "SSH_TTY", Summary: "Used to auto-detect headless OpenAI login sessions.", Visibility: Inherited}
	Term           = Var{Name: "TERM", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	TmpDir         = Var{Name: "TMPDIR", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	User           = Var{Name: "USER", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	UserProfile    = Var{Name: "USERPROFILE", Summary: "Windows user profile fallback.", Visibility: Inherited}
	WaylandDisplay = Var{Name: "WAYLAND_DISPLAY", Summary: "Used to auto-detect graphical sessions and clipboard support.", Visibility: Inherited}
)

func All() []Var {
	return append([]Var(nil), allVars...)
}

func ByVisibility(visibility Visibility) []Var {
	var out []Var
	for _, v := range allVars {
		if v.Visibility == visibility {
			out = append(out, v)
		}
	}
	return out
}

func Find(name string) (Var, bool) {
	for _, v := range allVars {
		if v.Name == name {
			return v, true
		}
	}
	return Var{}, false
}

var allVars = []Var{
	SERFAllowedDecisions,
	SERFFluencyModel,
	SERFHubAddr,
	SERFHubAuthToken,
	SERFHubBin,
	SERFHubEditorURLTemplate,
	SERFHubSpawned,
	SERFHubSpawnedCodex,
	SERFHubToken,
	SERFLoginHeadless,
	SERFLogRawHTTP,
	SERFModel,
	SERFOpenAIResponsesContinuation,
	SERFProvider,
	SERFProvidersConfig,
	SERFReasoningEffort,
	SERFRecordAppwire,
	SERFRecordHTTP,
	SERFFuzzCaptureEnv,
	SERFRunDir,
	SERFStateDir,
	SERFTUILogFile,
	LLMModel,
	LLMProvider,
	OpenAIAPIKey,
	OpenAIBaseURL,
	OpenAIChatGPTBaseURL,
	OpenAIChatGPTClientID,
	OpenAICompatibleAPIKey,
	OpenAICompatibleBaseURL,
	OpenAICompatibleProviderQuirks,
	OpenAIOrgID,
	OpenAIProjectID,
	AnthropicAPIKey,
	AnthropicBaseURL,
	GeminiAPIKey,
	GeminiBaseURL,
	GoogleAPIKey,
	GLMAPIKey,
	GLMBaseURL,
	KimiAPIKey,
	KimiBaseURL,
	KimiCodingAPIKey,
	KimiCodingBaseURL,
	MinimaxAPIKey,
	MinimaxBaseURL,
	OllamaAPIKey,
	OllamaBaseURL,
	OllamaHost,
	OpenRouterAPIKey,
	OpenRouterBaseURL,
	XDGCacheHome,
	XDGConfigHome,
	XDGStateHome,
	CargoHome,
	Display,
	GoModCache,
	GoPath,
	Home,
	HomeDrive,
	HomePath,
	Lang,
	NVMDir,
	Path,
	PyenvRoot,
	RustupHome,
	Shell,
	SSHConnection,
	SSHTTY,
	Term,
	TmpDir,
	User,
	UserProfile,
	WaylandDisplay,
}
