# Using Evener with Ollama

[Ollama](https://ollama.com/) lets you run open-weight LLMs locally. Evener's
`ollama` provider speaks the `openai-chat` protocol (the same Chat
Completions wire format every OpenAI-compatible vendor uses) with
`auth = "optional-bearer"` and a host-normalizing rule, `host_rule =
"ollama-host"`, that turns `OLLAMA_HOST`/`OLLAMA_BASE_URL` into a base URL.
Run the server on the Evener execution host or an endpoint reachable from it. A browser running on your laptop does not make `localhost` refer to that laptop from a remote session.

## Quick start

1. **Install Ollama** — https://ollama.com/download
2. **Start the daemon** (the macOS/Windows app does this automatically; on
   Linux run `ollama serve`)
3. **Pull a model that supports tool calling** — Evener relies on native
   function-calling, so the model has to support it:

   ```bash
   ollama pull llama3.1:8b
   ```

4. **Check the local endpoint**, from the host that will run Evener:

   ```sh
   curl -fsS http://localhost:11434/v1/models
   ```

   Confirm the pulled model ID appears. This checks listing, not tool-calling quality. Configure the real context window as described below before a long task.

5. **Run Evener** from a project directory containing a README:

   ```bash
   evener --sandbox read-only --model ollama/llama3.1:8b "summarize the README"
   ```

   No API key, no env vars, nothing else needed if Ollama is at the
   default `http://localhost:11434`. Set `OLLAMA_HOST` or `OLLAMA_BASE_URL`
   to point at a non-default endpoint (see below).

   `ollama` has no curated `default_model`. Use the full `ollama/<model>` reference even if it is your only connection. To make a local model the default deliberately, merge this complete document into `providers.toml`, substituting a model you have pulled:

   ```toml
   default = "ollama"

   [providers.ollama]
   default_model = "llama3.1:8b"
   ```

   Setting only `default = "ollama"` does not invent a default model. Other credentialed instances with defaults can win automatic ranking when no explicit default is set.

## How it works

`protocol = "openai-chat"` — the same Chat Completions protocol other
implicit providers use. `auth = "optional-bearer"` sends a bearer token when
a key resolves, and nothing otherwise. `host_rule = "ollama-host"` is the
one normalizer described below. Models, tool calls, multimodal images (for
models that support them), and `/v1/models` listing all flow through that
protocol's normal code path.

## Environment variables

| Variable | Purpose | Default |
|---|---|---|
| `OLLAMA_BASE_URL` | Full URL including `/v1` | `http://localhost:11434/v1` |
| `OLLAMA_HOST` | Ollama's canonical env var (`host`, `host:port`, or full URL); used when `OLLAMA_BASE_URL` is unset | unset |
| `OLLAMA_API_KEY` | API key for authenticated proxies or Ollama Cloud | unset |

Resolution order for the base URL:

1. If `OLLAMA_BASE_URL` is set, it wins (used as-is, trailing slash
   stripped).
2. Otherwise, if `OLLAMA_HOST` is set, it's normalized:
   - bare host (`ollama.local`) → `http://ollama.local:11434/v1`
   - the bare host `ollama.com` (Ollama Cloud) is the one exception to that
     rule → `https://ollama.com:443/v1` (https, port 443, not the usual
     11434)
   - host:port (`192.168.1.5:11434`) → `http://192.168.1.5:11434/v1`
   - `localhost` → `http://localhost:11434/v1`
   - a bare IPv6 literal (`::1`) → `http://[::1]:11434/v1`
   - full URL (`https://ollama.example.com`) → `https://ollama.example.com/v1`
   - a URL whose path already ends in `/v1` (e.g.
     `https://proxy/ollama/v1`) is preserved verbatim
3. Otherwise the default `http://localhost:11434/v1` is used.

## Examples

### Local default

```bash
OLLAMA_HOST=localhost evener --model ollama/llama3.1:8b "what does main.go do?"
```

### Remote Ollama on your LAN

This sends prompts and tool results over the network. Use a trusted private network or an authenticated encrypted proxy; do not expose an unauthenticated Ollama listener publicly.

```bash
OLLAMA_HOST=192.168.1.5:11434 \
  evener --sandbox workspace-write --model ollama/llama3.1:8b "fix the failing test"
```

### Ollama behind an authenticated proxy

```bash
OLLAMA_BASE_URL=https://ollama.example.com/v1 \
OLLAMA_API_KEY="$MY_PROXY_TOKEN" \
  evener --model ollama/llama3.1:70b "task"
```

### One-shot calls with `llmcall`

`llmcall` is a separate single-call executable, not an `evener` subcommand.
See its [build and installation note](environment-variables.md#public-settings).

```bash
llmcall --provider ollama --model llama3.1:8b "Write a haiku about goroutines."
```

### Listing locally available models

```bash
curl -s http://localhost:11434/v1/models | jq -r '.data[].id'
```

## Choosing a model

Evener drives every step through native tool calling. Models without
function-calling support will fail to make progress.

Check the installed model's tool-calling support in Ollama's model metadata. Model tags, templates, quantization, and hardware affect results; a successful `/v1/models` listing does not prove that a model can complete a coding task. Start with a read-only task, inspect its tool calls, and verify its answer against the files.

## Context length

When a live model's listing supplies no context length, Evener cannot infer
its server-configured window. Live-only models on `ollama` and the protocol-only
pseudo-providers use a **provider-level default of `context_window = 131072`**
unless live metadata or a user setting supplies a window. Evener does not use
a bundled Ollama per-model table or strip tags to find a context-window default.

Pin the actual server-configured window on a model row in `providers.toml`. This complete example declares 8192 tokens; change it to match your server. It changes Evener's budget, not Ollama's `num_ctx`:

```toml
[providers.ollama.models."llama3.1*"]
context_window = 8192
```

If the server's window is smaller than Evener's budget, compaction can start
too late to prevent a context-limit error. Check the configured window before
a long task. For changes from older bundled defaults, see
[provider migration](llm-provider-config-and-launch.md#upgrading-from-the-old-schema).

## Troubleshooting

**`connection refused`** — the Ollama daemon isn't running, or it's bound
to a different host/port. Check with `curl http://localhost:11434/api/tags`.

**`model 'X' not found`** — pull it first: `ollama pull X`.

**Bare text instead of tool calls** — the model isn't honoring tool
schemas. Check its template and tool support, then try a model suited to tool use. Repeating the task without changing the model setup may repeat the failure.

**Truncated long conversations** — the model's `num_ctx` is smaller than
the window Evener is budgeting against. Pin the real window with
`[providers.ollama.models."<glob>"] context_window = ...`; see
[Context length](#context-length) above.

**Slow responses** — local inference is bound by your hardware. Use a
quantized model (`:q4_K_M` etc.), a smaller model, or run Ollama on a
machine with a GPU and point Evener at it via `OLLAMA_HOST`.

## See also

- [`llm-providers.md`](llm-providers.md) — the registry: layers, instances,
  and how `ollama` fits the implicit-provider table.
- [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md) —
  credentials, OAuth, and how config reaches spawned sessions.
- [Environment variables](environment-variables.md) —
  the `OLLAMA_HOST`/`OLLAMA_BASE_URL`/`OLLAMA_API_KEY` env vars alongside
  every other provider's.
