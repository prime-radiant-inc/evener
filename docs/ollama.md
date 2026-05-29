# Using Serf with Ollama

[Ollama](https://ollama.com/) lets you run open-weight LLMs locally. Serf
ships with a built-in `ollama` provider that talks to Ollama's
OpenAI-compatible Chat Completions endpoint at `/v1/chat/completions`.

## Quick start

1. **Install Ollama** — https://ollama.com/download
2. **Start the daemon** (the macOS/Windows app does this automatically; on
   Linux run `ollama serve`)
3. **Pull a model that supports tool calling** — Serf relies on native
   function-calling, so the model has to support it:

   ```bash
   ollama pull llama3.1:8b
   # or
   ollama pull qwen2.5-coder:7b
   ```

4. **Run Serf**:

   ```bash
   OLLAMA_HOST=localhost serf --model ollama/llama3.1:8b "summarize the README"
   ```

   No API key, no env vars, nothing else needed if Ollama is at the
   default `http://localhost:11434`. Set `OLLAMA_HOST` or `OLLAMA_BASE_URL`
   to point at a non-default endpoint (see below).

   Ollama is always registered as an addressable provider, but it never
   becomes Serf's silent default — you must address it explicitly with a
   provider-qualified model such as `--model ollama/llama3.1:8b` or
   `SERF_MODEL=ollama/llama3.1:8b`.

## How it works

The `ollama` provider is a thin wrapper around Serf's `openai-compatible`
adapter. It posts standard OpenAI Chat Completions requests to
`<base-url>/chat/completions` and parses the streaming SSE response.

Models, tool calls, multimodal images (for models that support them), and
`/v1/models` listing all flow through the existing OpenAI-compatible code
path.

## Environment variables

| Variable | Purpose | Default |
|---|---|---|
| `OLLAMA_BASE_URL` | Full URL including `/v1` | `http://localhost:11434/v1` |
| `OLLAMA_HOST` | Ollama's canonical env var (`host`, `host:port`, or full URL) | unset |
| `OLLAMA_API_KEY` | API key for authenticated proxies or Ollama Cloud | unset |

**Always registered:** the `ollama` provider is always available through
provider-qualified model names such as `ollama/llama3.1:8b`. It is never
auto-elected as Serf's default provider — that role goes to whichever
conventional API-key provider you have configured (OpenAI, Anthropic,
etc.). So leaving all OLLAMA_* env vars unset is fine; it just means
Ollama answers on `http://localhost:11434/v1`.

Resolution order for the base URL:

1. If `OLLAMA_BASE_URL` is set, it wins (used as-is, trailing slash stripped).
2. Otherwise, if `OLLAMA_HOST` is set, it's normalized:
   - bare host (`ollama.local`) → `http://ollama.local:11434/v1`
   - host:port (`192.168.1.5:11434`) → `http://192.168.1.5:11434/v1`
   - full URL (`https://ollama.example.com`) → `https://ollama.example.com/v1`
   - URL whose path already ends in `/v1` (e.g. `https://proxy/ollama/v1`) is preserved verbatim
3. Otherwise (only `OLLAMA_API_KEY` set), the default `http://localhost:11434/v1` is used.

## Examples

### Local default

```bash
OLLAMA_HOST=localhost serf --model ollama/llama3.1:8b "what does main.go do?"
```

### Remote Ollama on your LAN

```bash
OLLAMA_HOST=192.168.1.5:11434 \
  serf --model ollama/qwen2.5-coder:7b "fix the failing test"
```

### Ollama behind an authenticated proxy

```bash
OLLAMA_BASE_URL=https://ollama.example.com/v1 \
OLLAMA_API_KEY=$MY_PROXY_TOKEN \
  serf --model ollama/llama3.1:70b "task"
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

Serf drives every step through native tool calling. Models without
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

Ollama's `/v1/models` endpoint does not report `context_length`, so Serf
cannot auto-detect the model's window from the API. Resolution order:

1. The embedded model catalog. Many common Ollama models are catalogued
   under `ollama/<name>` (e.g. `ollama/llama3.1` → 8192). Tagged variants
   like `llama3.1:8b` fall back to the untagged base entry.
2. **128K** generic default for unknown models.

So, for example, `--model ollama/llama3.1` or `--model
ollama/llama3.1:8b` picks up the catalog's 8192 token window, while a
model Serf has never heard of gets the 128K fallback. The catalog is
conservative — it reflects each model family's typical default, not
whatever you may have configured locally with `num_ctx`.

**Limitation:** Serf has no way to detect your actual configured
`num_ctx`. The catalog is static. So if you bump `num_ctx` in a custom
Modelfile, Serf will not know — and there is currently no override
flag. Two failure modes:

- **Catalog says 8K, you have 32K configured:** Serf compacts too
  aggressively and you lose useful context that Ollama would have
  happily kept.
- **Catalog says 128K (unknown model), you have 8K configured:** Serf
  doesn't compact; Ollama silently truncates older messages and the
  agent loses earlier turns without noticing.

There's no clean workaround today. If you build a custom variant with
a larger window, naming it under the same family
(`llama3.1:8b-32k`) won't help — the tag-stripping catalog lookup
will still resolve to `ollama/llama3.1` and pin you at 8192. Naming it
something the catalog has never heard of (`my-llama-32k`) gets you the
128K fallback, which over-shoots your real 32K window and exposes you
to the silent-truncation failure mode above. For now, prefer Ollama
models whose stock context window already matches your needs.

## Troubleshooting

**`connection refused`** — the Ollama daemon isn't running, or it's bound
to a different host/port. Check with `curl http://localhost:11434/api/tags`.

**`model 'X' not found`** — pull it first: `ollama pull X`.

**Bare text instead of tool calls** — the model isn't honoring tool
schemas. Try a larger or instruction-tuned model. Serf retries up to 3
times before giving up.

**Truncated long conversations** — the model's `num_ctx` is smaller than
its advertised context window. See the **Context length** section above.

**Slow responses** — local inference is bound by your hardware. Use a
quantized model (`:q4_K_M` etc.), a smaller model, or run Ollama on a
machine with a GPU and point Serf at it via `OLLAMA_HOST`.

## See also

- [`llm-providers.md`](llm-providers.md) — how the `ollama` provider fits the
  overall LLM provider architecture (it's a thin wrapper over the
  OpenAI-compatible Chat Completions adapter).
- [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md) —
  the `OLLAMA_HOST`/`OLLAMA_BASE_URL` env vars and how credentials/config reach
  spawned sessions.
