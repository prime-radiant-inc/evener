# Using Evener with Ollama

[Ollama](https://ollama.com/) lets you run open-weight LLMs locally. Evener's
`ollama` provider speaks the `openai-chat` protocol (the same Chat
Completions wire format every OpenAI-compatible vendor uses) with
`auth = "optional-bearer"` and a host-normalizing rule, `host_rule =
"ollama-host"`, that turns `OLLAMA_HOST`/`OLLAMA_BASE_URL` into a base URL.
Functionally this is the same wire behavior as before; only the plumbing
description changed.

## Quick start

1. **Install Ollama** — https://ollama.com/download
2. **Start the daemon** (the macOS/Windows app does this automatically; on
   Linux run `ollama serve`)
3. **Pull a model that supports tool calling** — Evener relies on native
   function-calling, so the model has to support it:

   ```bash
   ollama pull llama3.1:8b
   # or
   ollama pull qwen2.5-coder:7b
   ```

4. **Run Evener**:

   ```bash
   OLLAMA_HOST=localhost evener --model ollama/llama3.1:8b "summarize the README"
   ```

   No API key, no env vars, nothing else needed if Ollama is at the
   default `http://localhost:11434`. Set `OLLAMA_HOST` or `OLLAMA_BASE_URL`
   to point at a non-default endpoint (see below).

   `ollama` carries **no curated `default_model`** — unlike every other
   implicit provider except `azure` — so it's excluded from the automatic
   default-instance ranking unless you add one, either
   `[providers.ollama] default_model = "..."` in `providers.toml` (which
   makes it eligible like any other instance, ranked at its `default_order`
   position — last) or `default = "ollama"` explicitly. In practice, that
   means `ollama` only becomes the live default when nothing else in the
   environment resolves to a credentialed, default-model-bearing instance,
   or when you name it yourself. **Being the only instance configured isn't
   by itself enough:** if `ollama` is the sole instance and has no
   `default_model`, resolving a bare model id fails with a "no default
   model" error (the same pattern as `azure`), not a silent fallback to
   `ollama` — you still have to address it as `ollama/<model>`, or add a
   `default_model`.

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

These three variables and their resolution order are unchanged. Resolution
order for the base URL:

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

```bash
OLLAMA_HOST=192.168.1.5:11434 \
  evener --model ollama/qwen2.5-coder:7b "fix the failing test"
```

### Ollama behind an authenticated proxy

```bash
OLLAMA_BASE_URL=https://ollama.example.com/v1 \
OLLAMA_API_KEY=$MY_PROXY_TOKEN \
  evener --model ollama/llama3.1:70b "task"
```

### One-shot calls with `llmcall`

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

Models known to support tools well in Ollama include:

- `llama3.1` / `llama3.2` (8B and up)
- `qwen2.5-coder` (7B and up)
- `mistral-small`
- `firefunction-v2`

Smaller models (≤7B) often produce malformed tool calls or get stuck
re-emitting the same call. For real coding-agent workloads, larger models
(13B+) are usually worth it.

## Context length

Ollama's `/v1/models` still doesn't report `context_length`, so Evener still
can't auto-detect a live model's real window from the API.

Every live-only model on `ollama` (or a pseudo-provider) now budgets against
a **provider-level default of `context_window = 131072`**, so compaction
still fires for a live-only model whose listing reports no window. The
per-model catalog that used to ship a table like `8192` for `llama3.1` (with
tag-stripping lookup) is gone entirely — there's no bundled per-model table
anymore.

The override the old catalog-based approach couldn't offer now exists: pin
the real window on a model row in `providers.toml`:

```toml
[providers.ollama.models."llama3.1*"]
context_window = 8192
```

This is a **flag-day narrowing**: anyone who relied on the bundled
`llama3.1` → 8192 default being picked up automatically needs to add this
pin themselves now, or compaction won't fire until 131072 tokens instead of
8192 (see
["Upgrading from the old schema"](llm-provider-config-and-launch.md#upgrading-from-the-old-schema)
for the full list of flag-day changes).

## Troubleshooting

**`connection refused`** — the Ollama daemon isn't running, or it's bound
to a different host/port. Check with `curl http://localhost:11434/api/tags`.

**`model 'X' not found`** — pull it first: `ollama pull X`.

**Bare text instead of tool calls** — the model isn't honoring tool
schemas. Try a larger or instruction-tuned model. Evener retries up to 3
times before giving up.

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
- [`developing-evener/environment.md`](developing-evener/environment.md#provider-configuration) —
  the `OLLAMA_HOST`/`OLLAMA_BASE_URL`/`OLLAMA_API_KEY` env vars alongside
  every other provider's.
