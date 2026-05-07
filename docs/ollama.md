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
   serf --provider ollama --model llama3.1:8b "summarize the README"
   ```

   No API key is needed for local Ollama. Zero configuration required if
   the daemon is at the default `http://localhost:11434`.

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

Resolution order:

1. If `OLLAMA_BASE_URL` is set, it wins (used as-is, trailing slash stripped).
2. Otherwise, if `OLLAMA_HOST` is set, it's normalized:
   - bare host (`ollama.local`) → `http://ollama.local:11434/v1`
   - host:port (`192.168.1.5:11434`) → `http://192.168.1.5:11434/v1`
   - full URL (`https://ollama.example.com`) → `https://ollama.example.com/v1`
3. Otherwise, the default `http://localhost:11434/v1` is used.

## Examples

### Local default

```bash
serf --provider ollama --model llama3.1:8b "what does main.go do?"
```

### Remote Ollama on your LAN

```bash
OLLAMA_HOST=192.168.1.5:11434 \
  serf --provider ollama --model qwen2.5-coder:7b "fix the failing test"
```

### Ollama behind an authenticated proxy

```bash
OLLAMA_BASE_URL=https://ollama.example.com/v1 \
OLLAMA_API_KEY=$MY_PROXY_TOKEN \
  serf --provider ollama --model llama3.1:70b "task"
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
cannot auto-detect the model's window. It falls back to **128K** by
default, which is generous for most local models. If your model has a
smaller window (many local models default to 4K or 8K), Ollama will
silently truncate older messages — you may need to tune `num_ctx` in the
Ollama Modelfile or via `OLLAMA_NUM_CTX` to match what Serf assumes.

To raise the context window for a specific Ollama model:

```bash
# Create a model variant with a larger context window
ollama show llama3.1:8b --modelfile > /tmp/Modelfile
echo "PARAMETER num_ctx 32768" >> /tmp/Modelfile
ollama create llama3.1:8b-32k -f /tmp/Modelfile

serf --provider ollama --model llama3.1:8b-32k "..."
```

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
