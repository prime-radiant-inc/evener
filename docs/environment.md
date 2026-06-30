# Environment Variables

Serf's supported environment variables are defined in `envvars`. Runtime code,
help text, and tests should refer to those rows instead of hard-coding names.

## Serf Commands

| Variable | Description |
|---|---|
| `SERF_ALLOWED_DECISIONS` | Restricts tool-decision modes allowed by the active profile. |
| `SERF_HUB_ADDR` | Default hub address for `serf-tui`. |
| `SERF_HUB_AUTH_TOKEN` | Hub capability token for `serf-tui`. |
| `SERF_HUB_BIN` | Path to the `serf-hub` binary used by `serf-tui` autostart. |
| `SERF_HUB_EDITOR_URL_TEMPLATE` | Open-in-editor URL template; use `{path}` for the encoded path. |
| `SERF_LOGIN_HEADLESS` | Overrides OpenAI login flow detection: `1` for device-code, `0` for browser. |
| `SERF_LOG_RAW_HTTP` | Includes raw provider HTTP bodies in API logs when set to `1`, `true`, `yes`, or `on`. |
| `SERF_MODEL` | Default model as `provider/model` when `--model` is omitted. |
| `SERF_OPENAI_RESPONSES_CONTINUATION` | Default OpenAI Responses continuation mode: `off` or `auto`. The default is `off`; `--openai-responses-continuation` and hub launch settings override it. On resume, an explicit launch value layers over the persisted session snapshot. `auto` is reserved for future continuation enablement and may allow provider-side storage/retention and affect provider-token/cost behavior. |
| `SERF_PROVIDER` | Fallback provider for `llmcall` when `--provider` and `LLM_PROVIDER` are unset. |
| `SERF_PROVIDERS_CONFIG` | Path to `providers.toml`. |
| `SERF_REASONING_EFFORT` | Default reasoning effort: `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, or `none`. |
| `SERF_STATE_DIR` | Overrides the Serf state root. |
| `SERF_TUI_LOG_FILE` | Writes `serf-tui` startup diagnostics to this file. |
| `LLM_MODEL` | Model for `llmcall` when `--model` is unset; checked before `SERF_MODEL`. |
| `LLM_PROVIDER` | Provider for `llmcall` when `--provider` is unset; checked before `SERF_PROVIDER`. |

## Hub Internals

These are set or consumed by Serf-managed processes. Users normally should not
set them by hand.

| Variable | Description |
|---|---|
| `SERF_HUB_SPAWNED` | Set by `serf-hub` for spawned `serf serve` daemons. |
| `SERF_HUB_SPAWNED_CODEX` | Set by `serf-hub` for spawned Codex app-server processes. |
| `SERF_HUB_TOKEN` | Per-hub bearer token passed to spawned `serf serve` daemons. |
| `SERF_RUN_DIR` | Rendezvous directory passed by `serf-hub` to spawned daemons. |

## Provider Configuration

| Variable | Description |
|---|---|
| `OPENAI_API_KEY` | OpenAI API key. |
| `OPENAI_BASE_URL` | OpenAI API-key backend base URL. |
| `OPENAI_CHATGPT_BASE_URL` | OpenAI OAuth ChatGPT/Codex backend base URL. |
| `OPENAI_COMPATIBLE_API_KEY` | OpenAI-compatible provider API key. |
| `OPENAI_COMPATIBLE_BASE_URL` | Required base URL for openai-compatible provider registration. |
| `OPENAI_COMPATIBLE_PROVIDER_QUIRKS` | Quirks preset for the openai-compatible adapter. |
| `OPENAI_ORG_ID` | OpenAI organization header for API-key requests. |
| `OPENAI_PROJECT_ID` | OpenAI project header for API-key requests. |
| `ANTHROPIC_API_KEY` | Anthropic API key. |
| `ANTHROPIC_BASE_URL` | Anthropic API base URL override. |
| `GEMINI_API_KEY` | Google Gemini API key; checked before `GOOGLE_API_KEY`. |
| `GEMINI_BASE_URL` | Google Gemini API base URL override. |
| `GOOGLE_API_KEY` | Google Gemini API key fallback. |
| `GLM_API_KEY` | GLM API key. |
| `GLM_BASE_URL` | GLM API base URL. |
| `KIMI_API_KEY` | Kimi API key. |
| `KIMI_BASE_URL` | Kimi API base URL. |
| `KIMI_CODING_API_KEY` | Kimi coding-plan API key. |
| `KIMI_CODING_BASE_URL` | Kimi coding-plan Anthropic-compatible base URL. |
| `MINIMAX_API_KEY` | MiniMax API key. |
| `MINIMAX_BASE_URL` | MiniMax API base URL override. |
| `OLLAMA_API_KEY` | Optional API key for authenticated Ollama proxies or Ollama Cloud. |
| `OLLAMA_BASE_URL` | Ollama OpenAI-compatible base URL; must include `/v1`. |
| `OLLAMA_HOST` | Ollama canonical host; used when `OLLAMA_BASE_URL` is unset. |
| `OPENROUTER_API_KEY` | OpenRouter API key. |
| `OPENROUTER_BASE_URL` | OpenRouter API base URL. |

## Inherited Environment

Serf reads or preserves these standard environment variables for path
resolution, graphical/headless detection, clipboard detection, and child
process environments.

| Variable | Description |
|---|---|
| `XDG_CACHE_HOME` | Base for Serf cache data. |
| `XDG_CONFIG_HOME` | Base for Serf config, skills, plugins, and MCP config discovery. |
| `XDG_STATE_HOME` | Base for Serf state when `SERF_STATE_DIR` is unset. |
| `CARGO_HOME` | Inherited by core-only command environments. |
| `DISPLAY` | Used to auto-detect graphical sessions for OpenAI login. |
| `GOMODCACHE` | Inherited by core-only command environments. |
| `GOPATH` | Inherited by core-only command environments. |
| `HOME` | Home directory fallback for state/config paths and path expansion. |
| `HOMEDRIVE` | Windows home drive fallback. |
| `HOMEPATH` | Windows home path fallback. |
| `LANG` | Inherited by core-only command environments. |
| `NVM_DIR` | Inherited by core-only command environments. |
| `PATH` | Executable search path inherited by local commands and child processes. |
| `PYENV_ROOT` | Inherited by core-only command environments. |
| `RUSTUP_HOME` | Inherited by core-only command environments. |
| `SHELL` | Inherited by core-only command environments. |
| `SSH_CONNECTION` | Used to auto-detect headless OpenAI login sessions. |
| `SSH_TTY` | Used to auto-detect headless OpenAI login sessions. |
| `TERM` | Inherited by core-only command environments. |
| `TMPDIR` | Inherited by core-only command environments. |
| `USER` | Inherited by core-only command environments. |
| `USERPROFILE` | Windows user profile fallback. |
| `WAYLAND_DISPLAY` | Used to auto-detect graphical sessions and clipboard support. |

## Tooling

| Variable | Description |
|---|---|
| `SERF_FLUENCY_MODEL` | Default model for the tool-fluency development harness. |
| `SERF_RECORD_APPWIRE` | Records raw AppWire WebSocket frames to `appwire-frames.jsonl` (under the state root) for fuzz-corpus harvesting when set to `1`, `true`, `yes`, or `on`. Default off; no behavior change when unset. |
| `SERF_RECORD_HTTP` | Records inbound hub HTTP requests to `hub-http.jsonl` (under the state root) for fuzz-corpus harvesting when set to `1`, `true`, `yes`, or `on`. Default off; no behavior change when unset. |
| `SERF_FUZZ_RECORD` | Master switch enabling all fuzz-corpus recorders (provider `api-raw.jsonl`, AppWire frames, hub HTTP) by default when set to `1`, `true`, `yes`, or `on`. A per-recorder variable (`SERF_LOG_RAW_HTTP`/`SERF_RECORD_APPWIRE`/`SERF_RECORD_HTTP`) overrides it. Intended for local dev; unset everywhere else, so recording is off by default in shipped binaries, CI, and production. |
| `SERF_FUZZ_CAPTURE_ENV` | Marks a dedicated capture box so `serf-fuzz-harvest --keep-values` is permitted (real, unscrubbed values; local-only, never committed). Ignored for a personal `~/.serf` source. |
| `OPENAI_CHATGPT_CLIENT_ID` | OpenAI OAuth client id override for tests and development. |
