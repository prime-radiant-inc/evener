# Serf

A non-interactive coding agent. Give it a task, it does the work.

Serf uses the LLM's native tool-calling to read files, write files, run commands, and search code in a loop until the task is complete. It supports OpenAI, Anthropic, and Google models.

## Build

```bash
make build
```

Build the standalone one-shot client (no agent loop):

```bash
make build-llmcall
```

Build the multi-session web orchestrator:

```bash
make build-hub
```

## Usage

```
serf --provider <provider> --model <model> [flags] <task>
```

The task can be passed as arguments or piped via stdin:

```bash
# Task as arguments
serf --provider openai --model gpt-5.2 "add input validation to the signup handler"

# Task piped via stdin
echo "refactor auth to use JWT" | serf --provider anthropic --model claude-opus-4-6
```

## llmcall (One-Shot LLM Client)

This repo also includes `llmcall`, a minimal CLI wrapper around the unified `llm` library for single “throwaway” calls.

Properties:

- Exactly one LLM call (no agent loop).
- Tool calls are forbidden (`tool_choice=none`). If the model returns tool calls, `llmcall` fails.
- No system prompt by default. You can optionally provide one, or force-disable with `--no-system`.

Build:

```bash
make build-llmcall
```

Examples:

```bash
./llmcall --provider openai --model gpt-5-mini-2025-08-07 "Write a haiku about build pipelines."

# JSON mode: parses and re-prints as JSON (fails if output isn't valid JSON)
echo 'Return JSON: {"ok": true}' | ./llmcall --provider openai --model gpt-5-mini-2025-08-07 --format json

# JSON Schema mode: enforces + validates structured output
./llmcall --provider openai --model gpt-5-mini-2025-08-07 --schema /path/to/schema.json "Return an object matching the schema."
```

`llmcall` resolves provider/model from env if omitted:

- `LLM_PROVIDER` or `SERF_PROVIDER`
- `LLM_MODEL` or `SERF_MODEL`

### Provider and model (required)

Both `--provider` and `--model` are required. Providers: `openai`, `anthropic`, `google`, `minimax`, `openrouter`, `openrouter-anthropic`, `kimi`, `glm`, `ollama`.

Use flags or set `SERF_PROVIDER` and `SERF_MODEL` environment variables.

For local models via Ollama, see [docs/ollama.md](docs/ollama.md).

### Environment variables

| Variable | Description |
|---|---|
| `SERF_PROVIDER` | Default provider (used when `--provider` is omitted) |
| `SERF_MODEL` | Default model (used when `--model` is omitted) |
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `OLLAMA_BASE_URL` | Ollama base URL (default `http://localhost:11434/v1`) |
| `OLLAMA_HOST` | Ollama host (Ollama's canonical env var; used if `OLLAMA_BASE_URL` is unset) |
| `OLLAMA_API_KEY` | Optional API key for authenticated Ollama proxies / Ollama Cloud |

### Flags

| Flag | Description |
|---|---|
| `--provider <name>` | LLM provider (see above) (required) |
| `--model <name>` | LLM model identifier (required) |
| `--dir <path>` | Working directory (default: current directory) |
| `--output-schema <json>` | Inline JSON Schema replacing the default `communicate.output` schema |
| `--verbose` | Emit NDJSON events to stderr (replaces human-readable output) |
| `--resume <id>` | Resume a previous session by ID |
| `--resume-with <id>` | Start a new task using a previous session's context |
| `--resume-last` | Resume the most recent session |
| `--list-sessions` | List saved sessions and exit |

### Structured output

Pass `--output-schema <json>` to replace the `communicate` tool's `output` field schema with your own. The flag takes an inline JSON string (file paths are not supported).

```bash
serf --provider openai --model gpt-5.2 \
  --output-schema '{"type":"object","properties":{"plan":{"type":"string"}},"required":["plan"],"additionalProperties":false}' \
  "Draft a one-paragraph plan for fixing the flaky test."
```

The supplied schema replaces `output` wholesale — the default `message`/`data`/`artifacts` shape is removed. Provider-specific caveats:

- **OpenAI** rewrites `additionalProperties: true` to `false` and expands `required` to cover every property in the schema (strict mode).
- **Anthropic** strips `anyOf`/`oneOf`/`allOf` at the top level of the output schema.
- **Gemini** drops `additionalProperties` during sanitization.

## Output

**stdout** always receives only the final result text.

**stderr** shows progress in one of two modes:

**Default (human-readable):**
```
[model] gpt-5.2 (openai)
[tool] write_file {"file_path":"/tmp/test.txt","content":"he...
[tool] write_file: done
[assistant] I've created the file for you.
[thinking] (247 chars)
[usage] in=1234 out=567 total=1801
```

**`--verbose` (NDJSON):** Each event is a JSON object on one line, suitable for piping to `jq` or log aggregation:
```bash
serf --provider openai --model gpt-5.2 --verbose "fix the bug" 2>events.ndjson
```

NDJSON events include: `SESSION_START`, `ASSISTANT_TEXT_END` (with usage, reasoning, finish_reason), `TOOL_CALL_START` (with arguments), `TOOL_CALL_END`, `WARNING`, `ERROR`, and others.

## Session persistence

Serf auto-saves session state to `.serf/sessions/` in the working directory after each assistant turn. This enables resuming interrupted work.

```bash
# List saved sessions
serf --list-sessions

# Resume the most recent session (provider and model from the original session are used)
serf --resume-last

# Resume a specific session
serf --resume 01JTEST000000000000000001

# New task, but carry forward a previous session's conversation context
serf --provider openai --model gpt-5.2 --resume-with 01JTEST000000000000000001 "now add tests"
```

When resuming, the provider and model from the original session are used by default. You can override them with `--provider` and `--model`.

## Serf TUI (Terminal User Interface)

`serf-tui` is an interactive terminal interface for Serf, providing a real-time view of agent progress with markdown rendering, tool call inspection, and user input handling.

### Build

```bash
make build-tui
```

### Usage

Start with an embedded agent server (auto-starts Serf):

```bash
serf-tui --provider openai --model gpt-5.2 --dir /path/to/work
```

Or connect to an existing Serf server:

```bash
# In one terminal, start a Serf server
serf serve --provider openai --model gpt-5.2 --port 8080

# In another, connect the TUI
serf-tui --addr localhost:8080
```

### Flags

All flags from the `serf` CLI are supported, plus `--addr` for remote connections:

| Flag | Description |
|---|---|
| `--provider <name>` | LLM provider (see above) |
| `--model <name>` | LLM model identifier |
| `--dir <path>` | Working directory for the agent |
| `--state-dir <path>` | Override runtime state directory |
| `--system-prompt <path>` | Path to a custom system prompt file |
| `--system-prompt-append <path>` | Append to system prompt (repeatable) |
| `--max-rounds <n>` | Max tool rounds per input (0=unlimited, default: 200) |
| `--reasoning-effort <level>` | Reasoning effort: low\|medium\|high\|none |
| `--skills-dir <path>` | Extra skill directory (repeatable) |
| `--mcp <spec>` | MCP server (repeatable, format: name:command args...) |
| `--mcp-config <path>` | Path to .mcp.json file (repeatable) |
| `--plugin-dir <path>` | Plugin directory (repeatable) |
| `--resume <id>` | Resume a previous session by ID |
| `--resume-last` | Resume the most recent session |
| `--addr <host:port>` | Connect to existing server (skip embedded startup) |

### Features

- **Real-time streaming**: Watch agent progress as it unfolds via SSE
- **Markdown rendering**: Format-aware display of assistant messages
- **Tool inspection**: Collapse/expand tool calls and view arguments
- **User input**: Send messages and provide input to the agent
- **Session support**: Full session persistence and resume capabilities

## Serf Hub (Web Orchestrator)

`serf-hub` is a sibling binary that runs alongside `serf serve` daemons and gives you a single browser-based interface for many concurrent sessions.

### Build & run

```bash
make build-hub
serf-hub  # default 127.0.0.1:9180
```

Open `http://127.0.0.1:9180` in your browser.

### What's there

- **Sidebar** with a Live section (every running session sorted by who needs you) and a Projects section (sessions grouped by working directory; subagents indented under origin; forks immediately following with the `⎇` glyph).
- **Workspace pane** with a two-tier conversation: messages (user pills + assistant body) at the primary reading tier, tool calls and diffs as muted margin annotations.
- **New session** at `/new` — prompt-first, pick model and working dir, click spawn.
- **Edit-to-fork**: hover any prior user message, click `✎ edit`, hit ⌘↵, label the original branch, confirm. The new branch becomes active; the original is preserved as a sibling fork.
- **Transparent resume**: click any closed session, type, send. The daemon spawns from where it left off — same identity throughout.
- **⌘K search** across live + past sessions.
- **Settings** for theme (light/dark/system), notification preferences (all opt-in), and read-only inspection of providers and MCP setup.

### Configuration

`~/.serf/hub.toml` (optional):

```toml
addr = "127.0.0.1:9180"
spawn_timeout = "30s"
past_results_per_page = 50

[[providers]]
name = "openai"
models = ["gpt-5", "gpt-5-mini"]

[[providers]]
name = "anthropic"
models = ["claude-opus-4-7", "claude-sonnet-4-6"]
```

### Architecture

Daemons are loopback-only. Each writes a rendezvous file to `~/.serf/run/<pid>.json`; the hub watches the directory, probes daemons for state, and proxies REST + SSE so the browser only ever talks to the hub origin. Same-origin guard + strict CSP defend against DNS-rebinding and cross-origin attacks.

Design spec, plans, and notes live under `docs/superpowers/`.

## Acknowledgments

Serf is forked from [Kilroy](https://github.com/danshapiro/kilroy) by Dan Shapiro, originally built as part of the [StrongDM Attractor](https://github.com/strongdm/attractor) project. The unified LLM client, provider adapters, and agentic tool-calling loop all trace their lineage to that work. Kilroy is licensed under the MIT License (see `LICENSE-kilroy`).
