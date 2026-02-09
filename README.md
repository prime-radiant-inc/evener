# Serf

A non-interactive coding agent. Give it a task, it does the work.

Serf uses the LLM's native tool-calling to read files, write files, run commands, and search code in a loop until the task is complete. It supports OpenAI, Anthropic, and Google models.

## Build

```bash
make build
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

### Provider and model (required)

Both `--provider` and `--model` are required. Providers: `openai`, `anthropic`, `google`.

Use flags or set `SERF_PROVIDER` and `SERF_MODEL` environment variables.

### Environment variables

| Variable | Description |
|---|---|
| `SERF_PROVIDER` | Default provider (used when `--provider` is omitted) |
| `SERF_MODEL` | Default model (used when `--model` is omitted) |
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Google Gemini API key |

### Flags

| Flag | Description |
|---|---|
| `--provider <name>` | LLM provider: openai, anthropic, google (required) |
| `--model <name>` | LLM model identifier (required) |
| `--dir <path>` | Working directory (default: current directory) |
| `--verbose` | Emit NDJSON events to stderr (replaces human-readable output) |
| `--resume <id>` | Resume a previous session by ID |
| `--resume-with <id>` | Start a new task using a previous session's context |
| `--resume-last` | Resume the most recent session |
| `--list-sessions` | List saved sessions and exit |

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

## Relationship to Kilroy

Serf is the low-level coding agent — one model, one task, one session. [Kilroy](README-kilroy.md) is a pipeline orchestrator that can run multi-step Attractor graphs, where each node may invoke a coding agent like serf. They share the same unified LLM client (`internal/llm/`) and agent loop (`internal/agent/`).
