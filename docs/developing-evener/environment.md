# Environment Variables

Evener's supported environment variables are defined in `envvars`. Runtime code,
help text, and tests should refer to those rows instead of hard-coding names.

## Evener Commands

| Variable | Description |
|---|---|
| `EVENER_ALLOWED_DECISIONS` | Restricts tool-decision modes allowed by the active profile. |
| `EVENER_CREDENTIALS_CONFIG` | Path to `credentials.toml`. Unset means the sibling of the resolved providers-config path. |
| `EVENER_HUB_ADDR` | Default hub address for `evener tui`. |
| `EVENER_HUB_AUTH_TOKEN` | Hub capability token for `evener tui`. |
| `EVENER_HUB_BIN` | Path to the `evener hub` binary used by `evener tui` autostart. |
| `EVENER_LOGIN_HEADLESS` | Overrides OpenAI login flow detection: `1` for device-code, `0` for browser. |
| `EVENER_MODEL` | Default model as `provider/model` when `--model` is omitted. |
| `EVENER_OPENAI_RESPONSES_CONTINUATION` | Default OpenAI Responses continuation mode: `off` or `auto`. The default is `off`; `--openai-responses-continuation` and hub launch settings override it. On resume, an explicit launch value layers over the persisted session snapshot. `auto` is reserved for future continuation enablement and may allow provider-side storage/retention and affect provider-token/cost behavior. |
| `EVENER_PROVIDER` | Fallback provider for `llmcall` when `--provider` and `LLM_PROVIDER` are unset. |
| `EVENER_PROVIDERS_CONFIG` | Path to `providers.toml`. Unset means the default path; set and empty (`EVENER_PROVIDERS_CONFIG=`) means no user layer at all. |
| `EVENER_REASONING_EFFORT` | Default reasoning effort: `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, or `none`. |
| `EVENER_SESSION_ORIGIN` | Marks a session's launch origin (e.g. `test`) so the hub groups agentic-test runs. |
| `EVENER_STATE_DIR` | Overrides the per-invocation project/session state directory (`evener run --state-dir`, hub-spawned daemons, `evener doctor`); does not override the Evener state root (see `XDG_STATE_HOME`). |
| `EVENER_TUI_LOG_FILE` | Writes `evener tui` startup diagnostics to this file. |
| `LLM_MODEL` | Model for `llmcall` when `--model` is unset; checked before `EVENER_MODEL`. |
| `LLM_PROVIDER` | Provider for `llmcall` when `--provider` is unset; checked before `EVENER_PROVIDER`. |

## Evener Internals

These are set or consumed by Evener-managed processes. Users normally should not
set them by hand.

| Variable | Description |
|---|---|
| `EVENER_HUB_SPAWNED` | Set by `evener hub` for spawned `evener serve` daemons. |
| `EVENER_HUB_SPAWNED_CODEX` | Set by `evener hub` for spawned Codex app-server processes. |
| `EVENER_HUB_TOKEN` | Per-hub bearer token passed to spawned `evener serve` daemons. |
| `EVENER_RUN_DIR` | Rendezvous directory passed by `evener hub` to spawned daemons. |
| `EVENER_SCRATCH_DIR` | Evener-provided private scratch directory for one live session. It may be deleted when the session closes or Evener restarts; move durable artifacts into the workspace or another durable location. |

## Provider Configuration

| Variable | Description |
|---|---|
| `OPENAI_API_KEY` | OpenAI API key (`openai` instance). |
| `OPENAI_BASE_URL` | OpenAI API-key backend base URL (default `https://api.openai.com/v1`). |
| `OPENAI_CODEX_BASE_URL` | OpenAI ChatGPT/Codex backend base URL for the `openai-codex` instance (default `https://chatgpt.com/backend-api/codex`). Replaces `OPENAI_CHATGPT_BASE_URL`. |
| `OPENAI_COMPATIBLE_API_KEY` | API key for the `openai-compatible` pseudo-provider instance. |
| `OPENAI_COMPATIBLE_BASE_URL` | Base URL for the `openai-compatible` pseudo-provider instance; its presence is what makes that instance exist. |
| `OPENAI_ORG_ID` | OpenAI org header (`OpenAI-Organization`) on the `openai` instance; dropped when unset. |
| `OPENAI_PROJECT_ID` | OpenAI project header (`OpenAI-Project`) on the `openai` instance; dropped when unset. |
| `ANTHROPIC_API_KEY` | Anthropic API key (`anthropic` instance). |
| `ANTHROPIC_BASE_URL` | Anthropic API base URL override (default `https://api.anthropic.com/v1`). |
| `ANTHROPIC_COMPATIBLE_API_KEY` | API key for the `anthropic-compatible` pseudo-provider instance. |
| `ANTHROPIC_COMPATIBLE_BASE_URL` | Base URL for the `anthropic-compatible` pseudo-provider instance; its presence is what makes that instance exist. |
| `GEMINI_API_KEY` | Google Gemini API key; checked before `GOOGLE_API_KEY` (`google` instance). |
| `GOOGLE_API_KEY` | Google Gemini API key fallback. |
| `GOOGLE_BASE_URL` | Google Gemini API base URL override (default `https://generativelanguage.googleapis.com/v1beta`). Replaces `GEMINI_BASE_URL`. |
| `GOOGLE_COMPATIBLE_API_KEY` | API key for the `google-compatible` pseudo-provider instance. |
| `GOOGLE_COMPATIBLE_BASE_URL` | Base URL for the `google-compatible` pseudo-provider instance; its presence is what makes that instance exist. |
| `GOOGLE_VERTEX_PROJECT` | GCP project for the `google-vertex`/`google-vertex-anthropic` instances; required for either to exist. |
| `GOOGLE_VERTEX_LOCATION` | GCP location for the `google-vertex`/`google-vertex-anthropic` instances; required for either to exist. |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to a GCP service-account file for Application Default Credentials; the well-known ADC file also works without it. |
| `ZHIPU_API_KEY` | z.ai/Zhipu API key, used by both the `zai` and `zai-coding-plan` instances. Replaces `GLM_API_KEY`. |
| `ZAI_BASE_URL` | z.ai base URL override (default `https://api.z.ai/api/paas/v4`). Replaces `GLM_BASE_URL`. |
| `ZAI_CODING_PLAN_BASE_URL` | z.ai coding-plan base URL override (default `https://api.z.ai/api/coding/paas/v4`), for the `zai-coding-plan` instance. |
| `MOONSHOT_API_KEY` | Moonshot's platform API key (`moonshotai` instance). New name for what `KIMI_API_KEY` used to mean. |
| `MOONSHOTAI_BASE_URL` | Moonshot platform base URL override (default `https://api.moonshot.ai/v1`). |
| `KIMI_API_KEY` | Kimi coding-plan API key (`kimi-for-coding` instance, anthropic protocol). Meaning changed at the flag day — previously Moonshot's platform key. |
| `KIMI_FOR_CODING_BASE_URL` | Kimi coding-plan base URL override (default `https://api.kimi.com/coding/v1`). Replaces `KIMI_CODING_BASE_URL`. |
| `MINIMAX_API_KEY` | MiniMax API key (`minimax` instance). |
| `MINIMAX_BASE_URL` | MiniMax API base URL override (default `https://api.minimax.io/anthropic/v1`). |
| `GROQ_API_KEY` | Groq API key (`groq` instance). |
| `GROQ_BASE_URL` | Groq base URL override (default `https://api.groq.com/openai/v1`). |
| `XAI_API_KEY` | xAI API key (`xai` instance). |
| `XAI_BASE_URL` | xAI base URL override (default `https://api.x.ai/v1`). |
| `CEREBRAS_API_KEY` | Cerebras API key (`cerebras` instance). |
| `CEREBRAS_BASE_URL` | Cerebras base URL override (default `https://api.cerebras.ai/v1`). |
| `MISTRAL_API_KEY` | Mistral API key (`mistral` instance). |
| `MISTRAL_BASE_URL` | Mistral base URL override (default `https://api.mistral.ai/v1`). |
| `TOGETHER_API_KEY` | Together AI API key (`togetherai` instance). Note the name mismatch: the key variable is `TOGETHER_API_KEY`, not `TOGETHERAI_API_KEY` — the latter does not work. |
| `TOGETHERAI_BASE_URL` | Together AI base URL override (default `https://api.together.ai/v1`; the registry id is `togetherai`). |
| `DEEPSEEK_API_KEY` | DeepSeek API key (`deepseek` instance). |
| `DEEPSEEK_BASE_URL` | DeepSeek base URL override (default `https://api.deepseek.com`, no version segment — a documented models.dev exception). |
| `OPENROUTER_API_KEY` | OpenRouter API key (`openrouter` instance). |
| `OPENROUTER_BASE_URL` | OpenRouter API base URL override (default `https://openrouter.ai/api/v1`). |
| `AWS_BEARER_TOKEN_BEDROCK` | Bedrock bearer token (`amazon-bedrock` instance). |
| `AWS_REGION` | AWS region for the `amazon-bedrock` instance; required for it to exist. |
| `AZURE_RESOURCE_NAME` | Azure OpenAI/Foundry resource name; required for the `azure` instance to exist. |
| `AZURE_API_KEY` | Azure OpenAI API key (`azure` instance). Azure has no curated default model, so a working `azure` instance for a real deployment normally still needs an explicit `providers.toml` entry (see `docs/llm-providers.md`). |
| `OLLAMA_API_KEY` | Optional API key for authenticated Ollama proxies or Ollama Cloud. Unchanged. |
| `OLLAMA_BASE_URL` | Ollama OpenAI-compatible base URL; must include `/v1`. Unchanged. |
| `OLLAMA_HOST` | Ollama canonical host; used when `OLLAMA_BASE_URL` is unset. Unchanged. |

## Inherited Environment

Evener reads or preserves these standard environment variables for path
resolution, graphical/headless detection, clipboard detection, and child
process environments.

| Variable | Description |
|---|---|
| `XDG_CACHE_HOME` | Base for Evener cache data. |
| `XDG_CONFIG_HOME` | Base for Evener config, skills, plugins, and MCP config discovery. |
| `XDG_STATE_HOME` | Base for the Evener state root (`$XDG_STATE_HOME/evener`); also the fallback in the per-invocation state-dir override chain when `EVENER_STATE_DIR` is unset. |
| `CARGO_HOME` | Inherited by core-only command environments. |
| `DISPLAY` | Used to auto-detect graphical sessions for OpenAI login. |
| `GOMODCACHE` | Inherited by core-only command environments; the sandbox environment floor redirects it into the session scratch directory under the session-private cache strategy (see [docs/sandboxing.md](../sandboxing.md#caches-are-contained-never-poisoned)). |
| `GOPATH` | Inherited by core-only command environments. |
| `HOME` | Home directory fallback for state/config paths and path expansion. |
| `HOMEDRIVE` | Windows home drive fallback. |
| `HOMEPATH` | Windows home path fallback. |
| `LANG` | Inherited by core-only command environments. |
| `NVM_DIR` | Inherited by core-only command environments. |
| `PATH` | Executable search path for local commands and child processes; a session/daemon env overrides it with the resolved login-shell PATH when available, else the inherited process PATH. |
| `PYENV_ROOT` | Inherited by core-only command environments. |
| `RUSTUP_HOME` | Inherited by core-only command environments. |
| `SHELL` | Inherited by core-only command environments. |
| `SSH_CONNECTION` | Used to auto-detect headless OpenAI login sessions. |
| `SSH_TTY` | Used to auto-detect headless OpenAI login sessions. |
| `TERM` | Inherited by core-only command environments. |
| `TMPDIR` | Inherited by core-only command environments; a session/daemon env overrides it to the session scratch directory (see `EVENER_SCRATCH_DIR`). |
| `USER` | Inherited by core-only command environments. |
| `USERPROFILE` | Windows user profile fallback. |
| `WAYLAND_DISPLAY` | Used to auto-detect graphical sessions and clipboard support. |

## Tooling

| Variable | Description |
|---|---|
| `EVENER_FLUENCY_MODEL` | Default model for the tool-fluency development harness. |
| `EVENER_RECORD_APPWIRE` | Records raw AppWire WebSocket frames to `appwire-frames.jsonl` (under the state root) for fuzz-corpus harvesting when set to `1`, `true`, `yes`, or `on`. Default off; no behavior change when unset. |
| `EVENER_RECORD_HTTP` | Records inbound hub HTTP requests to `hub-http.jsonl` (under the state root) for fuzz-corpus harvesting when set to `1`, `true`, `yes`, or `on`. Default off; no behavior change when unset. |
| `EVENER_FUZZ_RECORD` | Master switch enabling the AppWire and hub HTTP fuzz-corpus recorders by default when set to `1`, `true`, `yes`, or `on`. A per-recorder variable (`EVENER_RECORD_APPWIRE`/`EVENER_RECORD_HTTP`) overrides it. Intended for local development; unset everywhere else. Provider attempts are recorded independently in each attached session API log. |
| `EVENER_FUZZ_CAPTURE_ENV` | Marks a dedicated capture box so `evener fuzz-harvest --keep-values` is permitted (real, unscrubbed values; local-only, never committed). Ignored for a personal evener state root source. |
| `OPENAI_CHATGPT_CLIENT_ID` | OpenAI OAuth client id override for tests and development. |
