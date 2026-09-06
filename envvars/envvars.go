// Package envvars defines Evener's supported environment variables.
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
	EVENERAllowedDecisions            = Var{Name: "EVENER_ALLOWED_DECISIONS", Summary: "Restricts tool-decision modes allowed by the active profile.", Visibility: Public}
	EVENERFluencyModel                = Var{Name: "EVENER_FLUENCY_MODEL", Summary: "Default model for the tool-fluency development harness.", Visibility: Tooling}
	EVENERHubAddr                     = Var{Name: "EVENER_HUB_ADDR", Summary: "Default hub address for evener-tui.", Visibility: Public}
	EVENERHubAuthToken                = Var{Name: "EVENER_HUB_AUTH_TOKEN", Summary: "Hub capability token for evener-tui.", Secret: true, Visibility: Public}
	EVENERHubBin                      = Var{Name: "EVENER_HUB_BIN", Summary: "Path to the evener binary (for the hub subcommand) used by evener tui autostart.", Visibility: Public}
	EVENERHubSpawned                  = Var{Name: "EVENER_HUB_SPAWNED", Summary: "Set by evener-hub for spawned evener serve daemons.", Visibility: Internal}
	EVENERHubToken                    = Var{Name: "EVENER_HUB_TOKEN", Summary: "Per-hub bearer token passed to spawned evener serve daemons.", Secret: true, Visibility: Internal}
	EVENERLoginHeadless               = Var{Name: "EVENER_LOGIN_HEADLESS", Summary: "Overrides OpenAI login flow detection: 1 for device-code, 0 for browser.", Visibility: Public}
	EVENERModel                       = Var{Name: "EVENER_MODEL", Summary: "Default model as provider/model when --model is omitted.", Visibility: Public}
	EVENEROffline                     = Var{Name: "EVENER_OFFLINE", Summary: "Set to 1 to keep the provider registry offline: no models.dev refresh, embedded snapshot only.", Visibility: Public}
	EVENEROpenAIResponsesContinuation = Var{Name: "EVENER_OPENAI_RESPONSES_CONTINUATION", Summary: "Default OpenAI Responses continuation mode: off|auto. CLI and launch config override it.", Visibility: Public}
	EVENERProvider                    = Var{Name: "EVENER_PROVIDER", Summary: "Fallback provider for llmcall when --provider and LLM_PROVIDER are unset.", Visibility: Public}
	EVENERProvidersConfig             = Var{Name: "EVENER_PROVIDERS_CONFIG", Summary: "Path to providers.toml.", Visibility: Public}
	EVENERCredentialsConfig           = Var{Name: "EVENER_CREDENTIALS_CONFIG", Summary: "Path to credentials.toml; unset means the sibling of providers.toml.", Visibility: Public}
	EVENERReasoningEffort             = Var{Name: "EVENER_REASONING_EFFORT", Summary: "Default reasoning effort: minimal|low|medium|high|xhigh|max|none.", Visibility: Public}
	EVENERRecordAppwire               = Var{Name: "EVENER_RECORD_APPWIRE", Summary: "Records raw AppWire WebSocket frames to appwire-frames.jsonl for fuzz-corpus harvesting when set to 1/true/yes/on; overrides EVENER_FUZZ_RECORD for this recorder.", Visibility: Tooling}
	EVENERRecordHTTP                  = Var{Name: "EVENER_RECORD_HTTP", Summary: "Records inbound hub HTTP requests to hub-http.jsonl for fuzz-corpus harvesting when set to 1/true/yes/on; overrides EVENER_FUZZ_RECORD for this recorder.", Visibility: Tooling}
	EVENERFuzzRecord                  = Var{Name: "EVENER_FUZZ_RECORD", Summary: "Master switch: enables the AppWire and HTTP fuzz-corpus recorders by default when set to 1/true/yes/on. A per-recorder var (EVENER_RECORD_APPWIRE/EVENER_RECORD_HTTP) overrides it. Intended for local dev; unset everywhere else.", Visibility: Tooling}
	EVENERFuzzCaptureEnv              = Var{Name: "EVENER_FUZZ_CAPTURE_ENV", Summary: "Marks a dedicated capture box so evener-fuzz-harvest --keep-values is permitted (real, unscrubbed values; local-only).", Visibility: Tooling}
	EVENERRunDir                      = Var{Name: "EVENER_RUN_DIR", Summary: "Rendezvous directory passed by evener-hub to spawned daemons.", Visibility: Internal}
	EVENERScratchDir                  = Var{Name: "EVENER_SCRATCH_DIR", Summary: "Session-scoped private scratch directory provided to agent subprocesses.", Visibility: Internal}
	EVENERSessionOrigin               = Var{Name: "EVENER_SESSION_ORIGIN", Summary: "Marks a session's launch origin (e.g. \"test\" for agentic-testing runs).", Visibility: Public}
	EVENERStateDir                    = Var{Name: "EVENER_STATE_DIR", Summary: "Overrides the per-invocation project/session state directory (evener run --state-dir, hub-spawned daemons, evener-doctor); does not override the Evener state root (see XDG_STATE_HOME).", Visibility: Public}
	EVENERTUILogFile                  = Var{Name: "EVENER_TUI_LOG_FILE", Summary: "Writes evener-tui startup diagnostics to this file.", Visibility: Public}

	LLMModel    = Var{Name: "LLM_MODEL", Summary: "Model for llmcall when --model is unset; checked before EVENER_MODEL.", Visibility: Public}
	LLMProvider = Var{Name: "LLM_PROVIDER", Summary: "Provider for llmcall when --provider is unset; checked before EVENER_PROVIDER.", Visibility: Public}

	OpenAIAPIKey          = Var{Name: "OPENAI_API_KEY", Summary: "OpenAI API key.", Secret: true, Visibility: Public}
	OpenAIBaseURL         = Var{Name: "OPENAI_BASE_URL", Summary: "OpenAI API-key backend base URL.", Visibility: Public}
	OpenAICodexBaseURL    = Var{Name: "OPENAI_CODEX_BASE_URL", Summary: "OpenAI OAuth Codex backend base URL.", Visibility: Public}
	OpenAIChatGPTClientID = Var{Name: "OPENAI_CHATGPT_CLIENT_ID", Summary: "OpenAI OAuth client id override for tests and development.", Visibility: Tooling}
	OpenAIOrgID           = Var{Name: "OPENAI_ORG_ID", Summary: "OpenAI organization header for API-key requests.", Visibility: Public}
	OpenAIProjectID       = Var{Name: "OPENAI_PROJECT_ID", Summary: "OpenAI project header for API-key requests.", Visibility: Public}

	AnthropicAPIKey  = Var{Name: "ANTHROPIC_API_KEY", Summary: "Anthropic API key.", Secret: true, Visibility: Public}
	AnthropicBaseURL = Var{Name: "ANTHROPIC_BASE_URL", Summary: "Anthropic API base URL override.", Visibility: Public}
	GeminiAPIKey     = Var{Name: "GEMINI_API_KEY", Summary: "Google Gemini API key; checked before GOOGLE_API_KEY.", Secret: true, Visibility: Public}
	GoogleAPIKey     = Var{Name: "GOOGLE_API_KEY", Summary: "Google Gemini API key fallback.", Secret: true, Visibility: Public}
	GoogleBaseURL    = Var{Name: "GOOGLE_BASE_URL", Summary: "Google Gemini API base URL override.", Visibility: Public}

	GroqAPIKey           = Var{Name: "GROQ_API_KEY", Summary: "Groq API key.", Secret: true, Visibility: Public}
	GroqBaseURL          = Var{Name: "GROQ_BASE_URL", Summary: "Groq API base URL override.", Visibility: Public}
	XAIAPIKey            = Var{Name: "XAI_API_KEY", Summary: "xAI API key.", Secret: true, Visibility: Public}
	XAIBaseURL           = Var{Name: "XAI_BASE_URL", Summary: "xAI API base URL override.", Visibility: Public}
	CerebrasAPIKey       = Var{Name: "CEREBRAS_API_KEY", Summary: "Cerebras API key.", Secret: true, Visibility: Public}
	CerebrasBaseURL      = Var{Name: "CEREBRAS_BASE_URL", Summary: "Cerebras API base URL override.", Visibility: Public}
	MistralAPIKey        = Var{Name: "MISTRAL_API_KEY", Summary: "Mistral API key.", Secret: true, Visibility: Public}
	MistralBaseURL       = Var{Name: "MISTRAL_BASE_URL", Summary: "Mistral API base URL override.", Visibility: Public}
	TogetherAPIKey       = Var{Name: "TOGETHER_API_KEY", Summary: "Together AI API key.", Secret: true, Visibility: Public}
	TogetherAIBaseURL    = Var{Name: "TOGETHERAI_BASE_URL", Summary: "Together AI API base URL override.", Visibility: Public}
	DeepSeekAPIKey       = Var{Name: "DEEPSEEK_API_KEY", Summary: "DeepSeek API key.", Secret: true, Visibility: Public}
	DeepSeekBaseURL      = Var{Name: "DEEPSEEK_BASE_URL", Summary: "DeepSeek API base URL override.", Visibility: Public}
	ZhipuAPIKey          = Var{Name: "ZHIPU_API_KEY", Summary: "Z.ai (Zhipu) API key, used by both the zai and zai-coding-plan instances.", Secret: true, Visibility: Public}
	ZAIBaseURL           = Var{Name: "ZAI_BASE_URL", Summary: "Z.ai API base URL override.", Visibility: Public}
	ZAICodingPlanBaseURL = Var{Name: "ZAI_CODING_PLAN_BASE_URL", Summary: "Z.ai coding-plan base URL override.", Visibility: Public}
	MoonshotAPIKey       = Var{Name: "MOONSHOT_API_KEY", Summary: "Moonshot AI API key.", Secret: true, Visibility: Public}
	MoonshotAIBaseURL    = Var{Name: "MOONSHOTAI_BASE_URL", Summary: "Moonshot AI API base URL override.", Visibility: Public}
	KimiAPIKey           = Var{Name: "KIMI_API_KEY", Summary: "Kimi coding-plan API key (models.dev's convention).", Secret: true, Visibility: Public}
	KimiForCodingBaseURL = Var{Name: "KIMI_FOR_CODING_BASE_URL", Summary: "Kimi coding-plan base URL override.", Visibility: Public}
	MinimaxAPIKey        = Var{Name: "MINIMAX_API_KEY", Summary: "MiniMax API key.", Secret: true, Visibility: Public}
	MinimaxBaseURL       = Var{Name: "MINIMAX_BASE_URL", Summary: "MiniMax API base URL override.", Visibility: Public}
	OpenRouterAPIKey     = Var{Name: "OPENROUTER_API_KEY", Summary: "OpenRouter API key.", Secret: true, Visibility: Public}
	OpenRouterBaseURL    = Var{Name: "OPENROUTER_BASE_URL", Summary: "OpenRouter API base URL.", Visibility: Public}
	OllamaAPIKey         = Var{Name: "OLLAMA_API_KEY", Summary: "Optional API key for authenticated Ollama proxies or Ollama Cloud.", Secret: true, Visibility: Public}
	OllamaBaseURL        = Var{Name: "OLLAMA_BASE_URL", Summary: "Ollama base URL; wins over OLLAMA_HOST.", Visibility: Public}
	OllamaHost           = Var{Name: "OLLAMA_HOST", Summary: "Ollama canonical host; used when OLLAMA_BASE_URL is unset.", Visibility: Public}

	AWSBearerTokenBedrock              = Var{Name: "AWS_BEARER_TOKEN_BEDROCK", Summary: "Amazon Bedrock bearer token.", Secret: true, Visibility: Public}
	AWSRegion                          = Var{Name: "AWS_REGION", Summary: "AWS region the Bedrock endpoint is built from.", Visibility: Public}
	AzureAPIKey                        = Var{Name: "AZURE_API_KEY", Summary: "Azure OpenAI API key.", Secret: true, Visibility: Public}
	AzureResourceName                  = Var{Name: "AZURE_RESOURCE_NAME", Summary: "Azure OpenAI resource name the endpoint is built from.", Visibility: Public}
	AzureCognitiveServicesResourceName = Var{Name: "AZURE_COGNITIVE_SERVICES_RESOURCE_NAME", Summary: "Azure AI Services resource name the endpoint is built from.", Visibility: Public}
	GoogleVertexProject                = Var{Name: "GOOGLE_VERTEX_PROJECT", Summary: "Google Vertex project the endpoint is built from.", Visibility: Public}
	GoogleVertexLocation               = Var{Name: "GOOGLE_VERTEX_LOCATION", Summary: "Google Vertex location the endpoint host is built from.", Visibility: Public}
	GoogleApplicationCredentials       = Var{Name: "GOOGLE_APPLICATION_CREDENTIALS", Summary: "Path to the Google application-default credentials file; when unset, the well-known gcloud path is used.", Visibility: Public}
	GoogleVertexAPIKey                 = Var{Name: "GOOGLE_VERTEX_API_KEY", Summary: "Google Cloud API key for Vertex AI express mode; the google-vertex-express instance.", Secret: true, Visibility: Public}
	GoogleVertexExpressBaseURL         = Var{Name: "GOOGLE_VERTEX_EXPRESS_BASE_URL", Summary: "Vertex AI express-mode base URL override (default https://aiplatform.googleapis.com/v1).", Visibility: Public}
	CloudflareAccountID                = Var{Name: "CLOUDFLARE_ACCOUNT_ID", Summary: "Cloudflare account id the Workers AI endpoint is built from.", Visibility: Public}
	DatabricksHost                     = Var{Name: "DATABRICKS_HOST", Summary: "Databricks workspace host the endpoint is built from.", Visibility: Public}
	InfomaniakProductID                = Var{Name: "INFOMANIAK_PRODUCT_ID", Summary: "Infomaniak product id the endpoint is built from.", Visibility: Public}
	NeonAIGatewayBaseURL               = Var{Name: "NEON_AI_GATEWAY_BASE_URL", Summary: "Neon AI gateway base URL.", Visibility: Public}
	SnowflakeAccount                   = Var{Name: "SNOWFLAKE_ACCOUNT", Summary: "Snowflake account the Cortex endpoint is built from.", Visibility: Public}

	AnthropicCompatibleAPIKey  = Var{Name: "ANTHROPIC_COMPATIBLE_API_KEY", Summary: "API key for the anthropic-compatible instance.", Secret: true, Visibility: Public}
	AnthropicCompatibleBaseURL = Var{Name: "ANTHROPIC_COMPATIBLE_BASE_URL", Summary: "Required base URL for the anthropic-compatible instance.", Visibility: Public}
	GoogleCompatibleAPIKey     = Var{Name: "GOOGLE_COMPATIBLE_API_KEY", Summary: "API key for the google-compatible instance.", Secret: true, Visibility: Public}
	GoogleCompatibleBaseURL    = Var{Name: "GOOGLE_COMPATIBLE_BASE_URL", Summary: "Required base URL for the google-compatible instance.", Visibility: Public}
	OpenAICompatibleAPIKey     = Var{Name: "OPENAI_COMPATIBLE_API_KEY", Summary: "API key for the openai-compatible instance.", Secret: true, Visibility: Public}
	OpenAICompatibleBaseURL    = Var{Name: "OPENAI_COMPATIBLE_BASE_URL", Summary: "Required base URL for the openai-compatible instance.", Visibility: Public}

	XDGCacheHome   = Var{Name: "XDG_CACHE_HOME", Summary: "Base for Evener cache data.", Visibility: Inherited}
	XDGConfigHome  = Var{Name: "XDG_CONFIG_HOME", Summary: "Base for Evener config, skills, plugins, and MCP config discovery.", Visibility: Inherited}
	XDGStateHome   = Var{Name: "XDG_STATE_HOME", Summary: "Base for the Evener state root ($XDG_STATE_HOME/evener); also the fallback in the per-invocation state-dir override chain when EVENER_STATE_DIR is unset.", Visibility: Inherited}
	CargoHome      = Var{Name: "CARGO_HOME", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	Display        = Var{Name: "DISPLAY", Summary: "Used to auto-detect graphical sessions for OpenAI login.", Visibility: Inherited}
	GoModCache     = Var{Name: "GOMODCACHE", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	GoPath         = Var{Name: "GOPATH", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	Home           = Var{Name: "HOME", Summary: "Home directory fallback for state/config paths and path expansion.", Visibility: Inherited}
	HomeDrive      = Var{Name: "HOMEDRIVE", Summary: "Windows home drive fallback.", Visibility: Inherited}
	HomePath       = Var{Name: "HOMEPATH", Summary: "Windows home path fallback.", Visibility: Inherited}
	Lang           = Var{Name: "LANG", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	NVMDir         = Var{Name: "NVM_DIR", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	Path           = Var{Name: "PATH", Summary: "Executable search path for local commands and child processes; a session/daemon env overrides it with the resolved login-shell PATH when available, else the inherited process PATH.", Visibility: Inherited}
	PyenvRoot      = Var{Name: "PYENV_ROOT", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	RustupHome     = Var{Name: "RUSTUP_HOME", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	Shell          = Var{Name: "SHELL", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	SSHConnection  = Var{Name: "SSH_CONNECTION", Summary: "Used to auto-detect headless OpenAI login sessions.", Visibility: Inherited}
	SSHTTY         = Var{Name: "SSH_TTY", Summary: "Used to auto-detect headless OpenAI login sessions.", Visibility: Inherited}
	Term           = Var{Name: "TERM", Summary: "Inherited by core-only command environments.", Visibility: Inherited}
	TmpDir         = Var{Name: "TMPDIR", Summary: "Inherited by core-only command environments; a session/daemon env overrides it to the session scratch directory (see EVENER_SCRATCH_DIR).", Visibility: Inherited}
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
	EVENERAllowedDecisions,
	EVENERFluencyModel,
	EVENERHubAddr,
	EVENERHubAuthToken,
	EVENERHubBin,
	EVENERHubSpawned,
	EVENERHubToken,
	EVENERLoginHeadless,
	EVENERModel,
	EVENEROffline,
	EVENEROpenAIResponsesContinuation,
	EVENERProvider,
	EVENERProvidersConfig,
	EVENERCredentialsConfig,
	EVENERReasoningEffort,
	EVENERRecordAppwire,
	EVENERRecordHTTP,
	EVENERFuzzRecord,
	EVENERFuzzCaptureEnv,
	EVENERRunDir,
	EVENERScratchDir,
	EVENERSessionOrigin,
	EVENERStateDir,
	EVENERTUILogFile,
	LLMModel,
	LLMProvider,
	OpenAIAPIKey,
	OpenAIBaseURL,
	OpenAICodexBaseURL,
	OpenAIChatGPTClientID,
	OpenAIOrgID,
	OpenAIProjectID,
	AnthropicAPIKey,
	AnthropicBaseURL,
	GeminiAPIKey,
	GoogleAPIKey,
	GoogleBaseURL,
	GroqAPIKey,
	GroqBaseURL,
	XAIAPIKey,
	XAIBaseURL,
	CerebrasAPIKey,
	CerebrasBaseURL,
	MistralAPIKey,
	MistralBaseURL,
	TogetherAPIKey,
	TogetherAIBaseURL,
	DeepSeekAPIKey,
	DeepSeekBaseURL,
	ZhipuAPIKey,
	ZAIBaseURL,
	ZAICodingPlanBaseURL,
	MoonshotAPIKey,
	MoonshotAIBaseURL,
	KimiAPIKey,
	KimiForCodingBaseURL,
	MinimaxAPIKey,
	MinimaxBaseURL,
	OpenRouterAPIKey,
	OpenRouterBaseURL,
	OllamaAPIKey,
	OllamaBaseURL,
	OllamaHost,
	AWSBearerTokenBedrock,
	AWSRegion,
	AzureAPIKey,
	AzureResourceName,
	AzureCognitiveServicesResourceName,
	GoogleVertexProject,
	GoogleVertexLocation,
	GoogleApplicationCredentials,
	GoogleVertexAPIKey,
	GoogleVertexExpressBaseURL,
	CloudflareAccountID,
	DatabricksHost,
	InfomaniakProductID,
	NeonAIGatewayBaseURL,
	SnowflakeAccount,
	AnthropicCompatibleAPIKey,
	AnthropicCompatibleBaseURL,
	GoogleCompatibleAPIKey,
	GoogleCompatibleBaseURL,
	OpenAICompatibleAPIKey,
	OpenAICompatibleBaseURL,
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

// recordTruthy reports whether an env value selects recording (1/true/yes/on,
// case-insensitive, trimmed).
func recordTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// RecorderEnabled reports whether a fuzz-corpus recorder gated by the per-recorder
// variable `specific` should run. An explicitly-set per-recorder value always wins
// (so it can force one recorder on or off); when that variable is unset, the
// recorder follows the EVENER_FUZZ_RECORD master switch. With nothing set, recording
// is off — the safe default for shipped binaries, CI, and production.
func RecorderEnabled(specific Var) bool {
	if raw, ok := specific.LookupEnv(); ok {
		return recordTruthy(raw)
	}
	return recordTruthy(EVENERFuzzRecord.Getenv())
}
