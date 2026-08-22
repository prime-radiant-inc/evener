# Evener

A coding agent run through a hub. The `evener-hub` orchestrator serves the
web UI — Evener's default interactive surface — where you start sessions,
watch the agent read files, run commands, and edit code, and steer it with
follow-up messages. The hub tracks many concurrent sessions at once, and
`evener-tui` gives the same hub a terminal dashboard. A non-interactive
command line handles scripting and automation.

**New here? [docs/getting-started.md](docs/getting-started.md) walks from
install to your first session.**

Evener uses the LLM's native tool-calling and supports OpenAI, Anthropic,
Google, and [other providers](docs/llm-providers.md). For how the code is
organized, see [docs/architecture.md](docs/architecture.md). For the runtime
contracts that subagents, plugins, and hooks operate under, see
[docs/subagent-runtime-contracts.md](docs/subagent-runtime-contracts.md). For
background jobs, see [docs/job-control.md](docs/job-control.md). To confine a
session's file, process, and network access with `--sandbox`, see
[docs/sandboxing.md](docs/sandboxing.md). To build, test, and lint this repo,
see [docs/developing-evener/README.md](docs/developing-evener/README.md) — or
run `make help` for every target with a one-line summary.

## Install

Install the latest release on Linux x64 or macOS Apple silicon:

```bash
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh | sh
```

The installer verifies the release archive's SHA-256 checksum and installs
`evener`, `evener-hub`, `evener-tui`, `evener-doctor`, and `evener-migrate`
under `~/.local/share/evener/bin`, symlinked into `~/.local/bin`. Make sure
`~/.local/bin` is on your `PATH`.

From a source checkout:

```bash
make install
```

For tagged releases, snapshot builds, alternate prefixes, and a system-style
install under `/usr/local`, see
[docs/getting-started.md](docs/getting-started.md#install). Verify any install
with `evener --version`.

Upgrade `evener`, `evener-hub`, `evener-tui`, and `evener-doctor` with
`evener upgrade`. The command follows the binary's install channel: release
builds upgrade to the latest release, and snapshot builds upgrade to the latest
successful `main` build. Pass `release`, `snapshot`, or a tag such as `v1.2.3`
to switch tracks. `evener-migrate` is not updated by `evener upgrade`; rerun
the installer, or run `make install` (or `sudo make install-system`), to refresh
it. The TUI and web UI expose the same mechanism through their `/upgrade`
command.

On first use, Evener creates:

- `${XDG_STATE_HOME:-$HOME/.local/state}/evener/run` for live daemon rendezvous
  files and per-daemon logs.
- `${XDG_STATE_HOME:-$HOME/.local/state}/evener/auth-token` for the local hub/TUI
  bearer token.
- `${XDG_STATE_HOME:-$HOME/.local/state}/evener/projects/<project-id>/` for saved
  per-project session state. The project ID is readable (derived from the
  canonical project path) and ends with a 10-character base62 suffix.
- `${XDG_CONFIG_HOME:-$HOME/.config}/evener/skills` for standalone user skills.
- `${XDG_CONFIG_HOME:-$HOME/.config}/evener/plugins` for user plugins.

The user skill and plugin directories are extension roots; installing Evener
does not automatically enable their contents. Add standalone skill paths to
`skills_dirs` and plugin paths to `plugin_dirs` in
`${XDG_CONFIG_HOME:-$HOME/.config}/evener/launch.toml`, or pass them with the
corresponding CLI flags for a single run. Plugin-contained skills live under
that plugin and become available through the plugin path.

Install does not create provider credentials. Hosted/auth-required providers
can be configured through the hub or TUI credentials UI, supported provider
environment variables such as `OPENAI_API_KEY`, or OpenAI OAuth. Local/auth-none
providers such as Ollama may not need credentials. The default credentials file
is `${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml`; when
`EVENER_PROVIDERS_CONFIG` points to a custom `providers.toml`, the credentials
file is beside it. See [docs/developing-evener/environment.md](docs/developing-evener/environment.md)
for the complete environment variable reference.

## Quick start: the hub and web UI

Start the hub:

```bash
evener-hub
```

The hub listens on `127.0.0.1:9180` and prints an authorization URL at
startup:

```
[hub] auth URL (visit once per browser): http://127.0.0.1:9180/auth?token=...
```

Open that URL. It sets a cookie that authorizes the browser; later visits to
`http://127.0.0.1:9180` need no token. For a hosted provider that requires a
credential, add it at `http://127.0.0.1:9180/credentials`; local/auth-none
providers such as Ollama can skip this step. Then open
`http://127.0.0.1:9180/new`, type a prompt, pick a model and working
directory, and click Start. The full walkthrough — resume, forking, and
search included — lives in
[docs/getting-started.md](docs/getting-started.md).

## Evener Hub (Web Orchestrator)

`evener-hub` runs alongside `evener serve` daemons and gives you a single
browser-based interface for many concurrent sessions.

### Build & run

From a source checkout, run `make build-hub`, then `./evener-hub` (default
`127.0.0.1:9180`). Installed users can run `evener-hub` from `PATH`.

### What's there

- **Sidebar** with a Live section (running sessions, excluding archived ones,
  sorted by who needs you) and a Projects section (sessions grouped by
  canonical project identity, not raw working-directory strings; subagents are
  indented under the session that spawned them).
- **Workspace pane** with a two-tier conversation: messages (user pills + assistant body) at the primary reading tier, tool calls and diffs as muted margin annotations.
- **New session** at `/new` — prompt-first, pick model and working dir, click Start.
- **Fork from here**: every message you sent carries a fork button. Click it to branch the session at that point; the original message lands in the composer for you to edit, and the original line is preserved as a sibling fork.
- **Aside**: `/aside` (TUI) or the *Aside: fork to side thread* palette command (web UI) forks the current session at its tip into a side thread with the same permissions and config — for asking a distracting question without derailing the main session.
- **Transparent resume**: click any closed session, type, send. The daemon spawns from where it left off — same identity throughout.
- **⌘K search** (Ctrl+K on Linux) across live + past sessions.
- **Settings** for theme (light/dark/system), notification preferences, and provider and MCP configuration.

### Configuration and operation

The hub uses layered launch configuration, loopback defaults, capability-token
authentication, and XDG state/config roots. For the authoritative configuration,
credential, smoke-check, remote-operation, and deploy guidance, see
[`docs/evener-hub.md`](docs/evener-hub.md) and
[`docs/evener-hub-remote-operations.md`](docs/evener-hub-remote-operations.md).

Design spec, plans, and notes live under `docs/superpowers/`.

## Evener TUI (Terminal User Interface)

`evener-tui` is a hub-backed terminal dashboard for Evener sessions. It connects to `evener-hub`, lists live and saved sessions, lets you drill into a transcript, and sends session actions through the hub API.

### Build

```bash
make build-tui
```

### Usage

Start the dashboard from an installed `PATH` command:

```bash
evener-tui
```

After `make build-tui` in a source checkout, use `./evener-tui` instead.

By default `evener-tui` connects to `http://127.0.0.1:9180`. If no local hub is
running, it starts `evener-hub` automatically and waits for an authenticated
AppWire `/rpc` connection. When `--state-dir` is explicit, it also checks
`/api/health` for state-environment compatibility.

Connect to a specific hub:

```bash
evener-tui --hub-addr http://127.0.0.1:9180
```

Use a specific hub binary or disable auto-start:

```bash
evener-tui --hub-bin /path/to/evener-hub
evener-tui --no-auto-start-hub
```

### Flags

| Flag | Description |
|---|---|
| `--hub-addr <url>` | Hub URL or host:port (default: `127.0.0.1:9180`) |
| `--hub-bin <path>` | Hub binary to auto-start when the local hub is down |
| `--no-auto-start-hub` | Fail instead of starting a missing local hub |
| `--auth-token <token>` | Hub capability token (overrides the token file and env var) |
| `--state-dir <path>` | Override the Evener state directory |
| `--log-file <path>` | Write auto-started hub logs to this file |
| `--debug` | Disable the alternate screen |

### Features

- **Dashboard**: Browse live and saved sessions from the hub roster and past-session index
- **Session drill-in**: Open a session transcript from the dashboard
- **Hub actions**: Send input, view tasks/details, interrupt, compact, clear, and switch models through hub endpoints
- **Streaming**: Follow session AppWire streams through the hub
- **Markdown rendering**: Format-aware display of assistant messages
- **Tool inspection**: Collapse/expand tool calls and view arguments

## Non-interactive CLI

Use the `evener` command directly for one-shot, non-interactive runs —
scripts, CI, and pipelines where no human steers the session. For interactive
work, prefer the hub and web UI above.

```
evener --model <provider/model> [flags] <prompt>
```

The prompt can be passed as arguments or piped via stdin:

```bash
# Prompt as arguments
evener --model openai/gpt-5.2 "add input validation to the signup handler"

# Prompt piped via stdin
echo "refactor auth to use JWT" | evener --model anthropic/claude-opus-4-6
```

Shell commands run by the agent normally end with their Evener session. When
a command must outlive the session, the agent passes `mode: "detached"` to
its shell tool and redirects the command's own logs:

```json
{"command":"long-task > /tmp/long-task.log 2>&1","mode":"detached"}
```

### Provider and model

Evener takes a provider-qualified model in one value: `--model <provider/model>`. Providers: `openai`, `anthropic`, `google`, `minimax`, `openrouter`, `openrouter-anthropic`, `kimi`, `glm`, `ollama`.

Use `--model` or set `EVENER_MODEL` to the same `provider/model` format.

For local models via Ollama, see [docs/ollama.md](docs/ollama.md).

### Environment variables

See [docs/developing-evener/environment.md](docs/developing-evener/environment.md) for the complete list. Common
variables:

| Variable | Description |
|---|---|
| `EVENER_MODEL` | Default model as `provider/model` (used when `--model` is omitted) |
| `EVENER_REASONING_EFFORT` | Default reasoning effort |
| `EVENER_PROVIDERS_CONFIG` | Path to `providers.toml` |
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `OPENROUTER_API_KEY` | OpenRouter API key |
| `OLLAMA_BASE_URL` | Ollama base URL (default `http://localhost:11434/v1`) |
| `OLLAMA_HOST` | Ollama host (Ollama's canonical env var; used if `OLLAMA_BASE_URL` is unset) |
| `OLLAMA_API_KEY` | Optional API key for authenticated Ollama proxies / Ollama Cloud |

### Flags

| Flag | Description |
|---|---|
| `--model <provider/model>` | LLM model identifier (required unless resuming an existing session) |
| `--dir <path>` | Working directory (default: current directory) |
| `--output-schema <json>` | Inline JSON Schema replacing the default `communicate.output` schema |
| `--verbose` | Emit NDJSON events to stderr (replaces human-readable output) |
| `--resume <id>` | Resume a previous session by ID |
| `--resume-with <id>` | Start a new prompt using a previous session's context |
| `--resume-last` | Resume the most recent session |
| `--list-sessions` | List saved sessions and exit |

### Structured output

Pass `--output-schema <json>` to replace the `communicate` tool's `output` field schema with your own. The flag takes an inline JSON string (file paths are not supported).

```bash
evener --model openai/gpt-5.2 \
  --output-schema '{"type":"object","properties":{"plan":{"type":"string"}},"required":["plan"],"additionalProperties":false}' \
  "Draft a one-paragraph plan for fixing the flaky test."
```

The supplied schema replaces `output` wholesale — the default `message`/`data`/`artifacts` shape is removed. Provider-specific caveats:

- **OpenAI** rewrites `additionalProperties: true` to `false` and expands `required` to cover every property in the schema (strict mode).
- **Anthropic** strips `anyOf`/`oneOf`/`allOf` at the top level of the output schema.
- **Gemini** drops `additionalProperties` during sanitization.

### Output

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
evener --model openai/gpt-5.2 --verbose "fix the bug" 2>events.ndjson
```

NDJSON events include: `SESSION_START`, `ASSISTANT_TEXT_END` (with usage, reasoning, finish_reason), `TOOL_CALL_START` (with arguments), `TOOL_CALL_END`, `WARNING`, `ERROR`, and others.

### Session persistence

Evener auto-saves session state under
`${XDG_STATE_HOME:-$HOME/.local/state}/evener/projects/<project-id>/sessions/`
after each assistant turn. This enables resuming interrupted work.

Project IDs are shared by a repository's main checkout and linked worktrees:
they aggregate into the same project bucket. A distinct clone has a different
canonical path and therefore gets a distinct project ID and state bucket.

Session IDs are 22-character UUIDv7 base62 payloads. Domain-specific IDs that
retain a prefix keep that prefix outside the payload (for example, `job_` IDs).

The identifier format change is a clean break. Evener does not migrate or delete
inert old project/session state; remove obsolete state manually after checking
that it is no longer needed. Installation IDs are the sole automatic legacy
replacement: an invalid stored installation ID is replaced when Evener next
needs one.

```bash
# List saved sessions
evener --list-sessions

# Resume the most recent session
evener --resume-last

# Resume a specific session
evener --resume 02wLIRxqmq3AUo6vl2OW37

# New prompt, but carry forward a previous session's conversation context
evener --model openai/gpt-5.2 --resume-with 02wLIRxqmq3AUo6vl2OW37 "now add tests"
```

Resume reuses the original session's provider and model; override them with `--model <provider/model>`.

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

- `LLM_PROVIDER` or `EVENER_PROVIDER`
- `LLM_MODEL` or `EVENER_MODEL`

## Acknowledgments

Evener is forked from [Kilroy](https://github.com/danshapiro/kilroy) by Dan Shapiro, originally built as part of the [StrongDM Attractor](https://github.com/strongdm/attractor) project. The unified LLM client, provider adapters, and agentic tool-calling loop all trace their lineage to that work. Kilroy is licensed under the MIT License (see `LICENSE-kilroy`).
