# Environment variables

Set variables in the shell or supervisor that starts Evener on the execution host. Browser settings cannot change an already-running process's inherited environment. Restart the relevant process after changing its service environment.

This user reference is derived from the public declarations in [envvars/envvars.go](../envvars/envvars.go). Internal coordination, development-only switches, and retired-interface controls are intentionally outside this guide. The registry remains the authority for names and visibility; subsystem guides own precedence and reload behavior.

## Use variables safely

Do not paste secrets into shell arguments, history, logs, prompts, or launch `[env]` tables. Prefer the masked [provider connection form](connecting-a-provider.md) or a secret manager that injects the process environment. Never diagnose access by dumping `env`, `printenv`, or an entire credential file.

A variable listed here does not automatically create an instance. Curated implicit providers have their own admission rules, and cloud instances also need resource/region variables. See the [implicit provider table](llm-providers.md#the-implicit-provider-list).

## Path and fallback rules

- `EVENER_PROVIDERS_CONFIG` **unset** selects the default providers file; **set to a path** selects that file; **set to an empty string** disables the user provider layer. Empty is not the default path.
- `EVENER_CREDENTIALS_CONFIG` selects a credential store independently. Without a nonempty override, the store is the providers file's sibling (or the default config-root store).
- `EVENER_STATE_DIR` selects an invocation's project/session directory, not the user state root. Use `XDG_STATE_HOME` to select the state-root base.
- `EVENER_MODEL`, `EVENER_REASONING_EFFORT`, and `EVENER_OPENAI_RESPONSES_CONTINUATION` are fallbacks. Explicit CLI or hub launch settings win; see [Configuration](configuration.md).
- `EVENER_OFFLINE=1` suppresses models.dev refresh. It does not block model requests, live endpoint checks, tools, or MCP networking.
- Provider base URLs generally include their version path. DeepSeek is an exception. `OLLAMA_BASE_URL` wins over normalized `OLLAMA_HOST`; see [Ollama](ollama.md#environment-variables).

## Public settings

`llmcall` in the table below is a separate executable for a single model call,
without an agent loop or tool calls. It is not an `evener` subcommand and is not
included in the standard release installer. From a source checkout, `make
build-llmcall` builds `./llmcall`; run that path or install it separately on your
`PATH`. See [`cmd/llmcall`](../cmd/llmcall/main.go) and the
[build target](../make/building.mk).

“Secret” marks the registry's secret classification, not a promise that other values are safe to publish. Paths, account identifiers, custom URLs, and error messages can still be private.

| Variable | Secret | Purpose |
| --- | --- | --- |
| `EVENER_ALLOWED_DECISIONS` | no | Restricts tool-decision modes allowed by the active profile. |
| `EVENER_HOST_TEMP_BASES` | no | OS path list of absolute directories that replaces /tmp and /var/tmp as the world-usable host temp bases session temp containers are created in and the startup crashed-scratch sweep walks; each must be a world-writable sticky directory. Set but empty or malformed is refused, never replaced by the defaults. |
| `EVENER_LOGIN_HEADLESS` | no | Overrides OpenAI login flow detection: 1 for device-code, 0 for browser. |
| `EVENER_MODEL` | no | Default model as provider/model when --model is omitted. |
| `EVENER_OFFLINE` | no | Set to 1 to keep the provider registry offline: no models.dev refresh, embedded snapshot only. |
| `EVENER_OPENAI_RESPONSES_CONTINUATION` | no | Default OpenAI Responses continuation mode: off&#124;auto. CLI and launch config override it. |
| `EVENER_PROVIDER` | no | Fallback provider for llmcall when --provider and LLM_PROVIDER are unset. |
| `EVENER_PROVIDERS_CONFIG` | no | Path to providers.toml. |
| `EVENER_CREDENTIALS_CONFIG` | no | Path to credentials.toml; unset means the sibling of providers.toml. |
| `EVENER_REASONING_EFFORT` | no | Default reasoning effort: minimal&#124;low&#124;medium&#124;high&#124;xhigh&#124;max&#124;none. |
| `EVENER_SESSION_ORIGIN` | no | Marks a session's launch origin (e.g. "test" for agentic-testing runs). |
| `EVENER_STATE_DIR` | no | Overrides the per-invocation project/session state directory (evener run --state-dir, hub-spawned daemons, evener-doctor); does not override the Evener state root (see XDG_STATE_HOME). |
| `LLM_MODEL` | no | Model for llmcall when --model is unset; checked before EVENER_MODEL. |
| `LLM_PROVIDER` | no | Provider for llmcall when --provider is unset; checked before EVENER_PROVIDER. |
| `OPENAI_API_KEY` | yes | OpenAI API key. |
| `OPENAI_BASE_URL` | no | OpenAI API-key backend base URL. |
| `OPENAI_CODEX_BASE_URL` | no | OpenAI OAuth Codex backend base URL. |
| `OPENAI_ORG_ID` | no | OpenAI organization header for API-key requests. |
| `OPENAI_PROJECT_ID` | no | OpenAI project header for API-key requests. |
| `ANTHROPIC_API_KEY` | yes | Anthropic API key. |
| `ANTHROPIC_BASE_URL` | no | Anthropic API base URL override. |
| `GEMINI_API_KEY` | yes | Google Gemini API key; checked before GOOGLE_API_KEY. |
| `GOOGLE_API_KEY` | yes | Google Gemini API key fallback. |
| `GOOGLE_BASE_URL` | no | Google Gemini API base URL override. |
| `GROQ_API_KEY` | yes | Groq API key. |
| `GROQ_BASE_URL` | no | Groq API base URL override. |
| `XAI_API_KEY` | yes | xAI API key. |
| `XAI_BASE_URL` | no | xAI API base URL override. |
| `CEREBRAS_API_KEY` | yes | Cerebras API key. |
| `CEREBRAS_BASE_URL` | no | Cerebras API base URL override. |
| `MISTRAL_API_KEY` | yes | Mistral API key. |
| `MISTRAL_BASE_URL` | no | Mistral API base URL override. |
| `TOGETHER_API_KEY` | yes | Together AI API key. |
| `TOGETHERAI_BASE_URL` | no | Together AI API base URL override. |
| `DEEPSEEK_API_KEY` | yes | DeepSeek API key. |
| `DEEPSEEK_BASE_URL` | no | DeepSeek API base URL override. |
| `ZHIPU_API_KEY` | yes | Z.ai (Zhipu) API key, used by both the zai and zai-coding-plan instances. |
| `ZAI_BASE_URL` | no | Z.ai API base URL override. |
| `ZAI_CODING_PLAN_BASE_URL` | no | Z.ai coding-plan base URL override. |
| `MOONSHOT_API_KEY` | yes | Moonshot AI API key. |
| `MOONSHOTAI_BASE_URL` | no | Moonshot AI API base URL override. |
| `KIMI_API_KEY` | yes | Kimi coding-plan API key (models.dev's convention). |
| `KIMI_FOR_CODING_BASE_URL` | no | Kimi coding-plan base URL override. |
| `MINIMAX_API_KEY` | yes | MiniMax API key. |
| `MINIMAX_BASE_URL` | no | MiniMax API base URL override. |
| `OPENROUTER_API_KEY` | yes | OpenRouter API key. |
| `OPENROUTER_BASE_URL` | no | OpenRouter API base URL. |
| `OLLAMA_API_KEY` | yes | Optional API key for authenticated Ollama proxies or Ollama Cloud. |
| `OLLAMA_BASE_URL` | no | Ollama base URL; wins over OLLAMA_HOST. |
| `OLLAMA_HOST` | no | Ollama canonical host; used when OLLAMA_BASE_URL is unset. |
| `AWS_BEARER_TOKEN_BEDROCK` | yes | Amazon Bedrock bearer token. |
| `AWS_REGION` | no | AWS region the Bedrock endpoint is built from. |
| `AZURE_API_KEY` | yes | Azure OpenAI API key. |
| `AZURE_RESOURCE_NAME` | no | Azure OpenAI resource name the endpoint is built from. |
| `AZURE_COGNITIVE_SERVICES_RESOURCE_NAME` | no | Azure AI Services resource name the endpoint is built from. |
| `GOOGLE_VERTEX_PROJECT` | no | Google Vertex project the endpoint is built from. |
| `GOOGLE_VERTEX_LOCATION` | no | Google Vertex location the endpoint host is built from. |
| `GOOGLE_APPLICATION_CREDENTIALS` | no | Path to the Google application-default credentials file; when unset, the well-known gcloud path is used. |
| `GOOGLE_VERTEX_API_KEY` | yes | Google Cloud API key for Vertex AI express mode; the google-vertex-express instance. |
| `GOOGLE_VERTEX_EXPRESS_BASE_URL` | no | Vertex AI express-mode base URL override (default https://aiplatform.googleapis.com/v1). |
| `CLOUDFLARE_ACCOUNT_ID` | no | Cloudflare account id the Workers AI endpoint is built from. |
| `DATABRICKS_HOST` | no | Databricks workspace host the endpoint is built from. |
| `INFOMANIAK_PRODUCT_ID` | no | Infomaniak product id the endpoint is built from. |
| `NEON_AI_GATEWAY_BASE_URL` | no | Neon AI gateway base URL. |
| `SNOWFLAKE_ACCOUNT` | no | Snowflake account the Cortex endpoint is built from. |
| `ANTHROPIC_COMPATIBLE_API_KEY` | yes | API key for the anthropic-compatible instance. |
| `ANTHROPIC_COMPATIBLE_BASE_URL` | no | Required base URL for the anthropic-compatible instance. |
| `GOOGLE_COMPATIBLE_API_KEY` | yes | API key for the google-compatible instance. |
| `GOOGLE_COMPATIBLE_BASE_URL` | no | Required base URL for the google-compatible instance. |
| `OPENAI_COMPATIBLE_API_KEY` | yes | API key for the openai-compatible instance. |
| `OPENAI_COMPATIBLE_BASE_URL` | no | Required base URL for the openai-compatible instance. |

## Inherited host settings

These are host conventions, not a second Evener configuration file:

| Variable | Use |
| --- | --- |
| `HOME` | Fallback for user directories |
| `XDG_CONFIG_HOME` | Config base, default `$HOME/.config`; Evener appends `/evener` |
| `XDG_STATE_HOME` | State base, default `$HOME/.local/state`; Evener appends `/evener` |
| `XDG_CACHE_HOME` | Cache base |
| `PATH`, `SHELL` | Executable lookup and shell; session command environments can use the resolved login-shell PATH |
| `GOCACHE`, `GOMODCACHE`, `GOPATH`, `GOENV` | Go tooling and sandbox cache resolution |
| `CARGO_HOME`, `RUSTUP_HOME`, `NVM_DIR`, `PYENV_ROOT` | Toolchain paths inherited by core command environments |
| `SSH_CONNECTION`, `SSH_TTY`, `DISPLAY`, `WAYLAND_DISPLAY` | Headless/graphical login detection |
| `TMPDIR` | Session-dependent temporary directory, not necessarily private when sandbox is off |

Evener supplies `EVENER_SCRATCH_DIR` to agent subprocesses for private session intermediates. An unsandboxed command's `TMPDIR` can be a world-readable, world-writable temporary container; do not put secrets there. Detached jobs do not retain session scratch. See [Sandboxing](sandboxing.md) and [job control](job-control.md).

## Credential references are not shell exports

Provider `api_key` and `credential_headers` can reference `$NAME`, `${NAME:-default}`, or approved `$(command)` expressions. This expansion has its own grammar, timeout, and cache. Literal stored keys are not expanded. Ordinary headers and transport variables must not run credential commands. See [credential expressions](llm-provider-config-and-launch.md#command-expressions-in-credential-values).

When upgrading an old configuration, check [provider migration](llm-provider-config-and-launch.md#upgrading-from-the-old-schema). For example, `KIMI_API_KEY` means the coding plan, `MOONSHOT_API_KEY` means the platform, and `ZHIPU_API_KEY` replaces old GLM naming. Unsupported old names do not become aliases merely because they remain exported in your shell.
