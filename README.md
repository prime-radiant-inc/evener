# Evener

A non-interactive coding agent. Give it a prompt, it does the work.

Evener uses the LLM's native tool-calling to read files, write files, run commands, and search code in a loop until the work is complete. It supports OpenAI, Anthropic, and Google models.

For how the code is organized — modules, layout, and the build workspace — see [docs/architecture.md](docs/architecture.md). For the runtime contracts subagents, plugins, hooks, and helpers operate under (capability policy, lifecycle hooks, helper isolation, lineage), see [docs/subagent-runtime-contracts.md](docs/subagent-runtime-contracts.md); for background jobs and the job-control tools, see [docs/job-control.md](docs/job-control.md). To confine a session's file, process, and network access with `--sandbox`, see [docs/sandboxing.md](docs/sandboxing.md).

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

## Install

Install the latest release on Linux x64 or macOS Apple silicon:

```bash
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh | sh
```

The release installer downloads the matching GitHub release archive, installs
`evener`, `evener-hub`, `evener-tui`, `evener-doctor`, and `evener-migrate`
under `~/.local/share/evener/bin`, and symlinks them into `~/.local/bin`.

Install a specific tagged release:

```bash
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh | env EVENER_INSTALL_VERSION=v1.2.3 sh
```

Install the latest successful build from `main`:

```bash
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh | env EVENER_INSTALL_VERSION=snapshot sh
```

Override the install prefix, using `sudo` for system-owned paths:

```bash
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh | sudo env PREFIX=/usr/local sh
```

From a source checkout:

```bash
make install
```

This builds `evener`, `evener-hub`, `evener-tui`, `evener-doctor`, and
`evener-migrate`, installs the binaries under `~/.local/share/evener/bin`, and
symlinks them into `~/.local/bin`. `make install-home` is an alias for the
same layout.

System-style install:

```bash
sudo make install-system
```

This uses the same layout under `/usr/local` by default. Override `PREFIX` to
stage elsewhere.

The installer only installs binaries and symlinks. Runtime/config directories
are created by Evener when the relevant binary runs.

Verify the installed commands with:

```bash
evener --version
evener-tui --help
evener-doctor --help
```

Upgrade installed binaries manually:

```bash
evener upgrade
```

`evener upgrade` follows the binary's install channel: release builds upgrade to
the latest release, and snapshot builds upgrade to the latest successful
`main` build. You can override the target with `evener upgrade release`,
`evener upgrade snapshot`, or a tagged version such as `evener upgrade v1.2.3`.
The TUI and web UI also expose a manual `/upgrade` command that calls through
the hub and uses the same channel tracking.

On first use, Evener creates:

- `~/.evener/run` for live daemon rendezvous files.
- `~/.evener/auth-token` for the local Hub/TUI bearer token.
- `${XDG_STATE_HOME:-~/.local/state}/evener/projects/<project-id>/` for saved
  per-project session state. The project ID is readable (derived from the
  canonical project path) and ends with a 10-character base62 suffix.
- `${XDG_CONFIG_HOME:-~/.config}/evener/skills` for standalone user skills.
- `${XDG_CONFIG_HOME:-~/.config}/evener/plugins` for user plugins.

The user skill and plugin directories are extension roots; installing Evener
does not automatically enable their contents. Add standalone skill paths to
`skills_dirs` and plugin paths to `plugin_dirs` in `~/.evener/launch.toml`, or
pass them with the corresponding CLI flags for a single run. Plugin-contained
skills live under that plugin and become available through the plugin path.

Provider credentials are not created by install. Configure them through the Hub
or TUI credentials UI, `~/.evener/credentials.toml`, provider environment
variables such as `OPENAI_API_KEY`, or OpenAI OAuth. See
[docs/environment.md](docs/environment.md) for the complete environment variable
reference.

### Migrating from Serf

Evener was previously named Serf. If you have an existing Serf install, run
`evener-migrate` once, before your first Evener launch:

```bash
evener-migrate
```

It moves `~/.serf` to `~/.evener`, `${XDG_CONFIG_HOME:-~/.config}/serf` to
`.../evener`, `${XDG_STATE_HOME:-~/.local/state}/serf` to `.../evener`, and
any per-project `.serf` directory (in the current directory or a Git
ancestor) to `.evener`. It refuses to overwrite a destination that already
exists, so it's safe to run more than once. Pass `--dry-run` to preview the
moves first, or `--verbose` to see every path it checked.

Re-running it is also how you repair a machine that migrated before a fix
landed: after moving each root (or finding it already moved), `evener-migrate`
walks the destination and rewrites any leftover absolute references to the
old `serf` path it finds inside text files there — for example a plugin
marketplace registry that still points `git pull` at
`.../config/serf/plugins/marketplaces/<name>`. It skips binaries and
anything inside a Git working tree (a plugin marketplace clone, or a nested
project checkout), so it's safe to run against a live install; files with
nothing to rewrite are left untouched.

If you skip this, the first Evener binary you run creates a fresh, empty
`~/.evener` (and XDG config/state directories) — and once that empty
directory exists, `evener-migrate` treats it as already migrated and skips
it, leaving your old Serf data stranded with no further warning. To prevent
that, Evener refuses to start when it finds a legacy Serf directory with no
matching Evener directory yet, and its error names the path and points back
to `evener-migrate`.

## Usage

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

For a shell command that must outlive the current Evener session, use detached
mode. Detached commands are not jobs; redirect their own logs when needed:

```json
{"command":"long-task > /tmp/long-task.log 2>&1","mode":"detached"}
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

- `LLM_PROVIDER` or `EVENER_PROVIDER`
- `LLM_MODEL` or `EVENER_MODEL`

### Provider and model

Evener takes a provider-qualified model in one value: `--model <provider/model>`. Providers: `openai`, `anthropic`, `google`, `minimax`, `openrouter`, `openrouter-anthropic`, `kimi`, `glm`, `ollama`.

Use `--model` or set `EVENER_MODEL` to the same `provider/model` format.

For local models via Ollama, see [docs/ollama.md](docs/ollama.md).

### Environment variables

See [docs/environment.md](docs/environment.md) for the complete list. Common
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
evener --model openai/gpt-5.2 --verbose "fix the bug" 2>events.ndjson
```

NDJSON events include: `SESSION_START`, `ASSISTANT_TEXT_END` (with usage, reasoning, finish_reason), `TOOL_CALL_START` (with arguments), `TOOL_CALL_END`, `WARNING`, `ERROR`, and others.

## Session persistence

Evener auto-saves session state under
`${XDG_STATE_HOME:-~/.local/state}/evener/projects/<project-id>/sessions/`
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

# Resume the most recent session (provider and model from the original session are used)
evener --resume-last

# Resume a specific session
evener --resume 02wLIRxqmq3AUo6vl2OW37

# New prompt, but carry forward a previous session's conversation context
evener --model openai/gpt-5.2 --resume-with 02wLIRxqmq3AUo6vl2OW37 "now add tests"
```

When resuming, the provider and model from the original session are used by default. You can override them with `--model <provider/model>`.

## Evener TUI (Terminal User Interface)

`evener-tui` is a hub-backed terminal dashboard for Evener sessions. It connects to `evener-hub`, lists live and saved sessions, lets you drill into a transcript, and sends session actions through the hub API.

### Build

```bash
make build-tui
```

### Usage

Start the dashboard:

```bash
evener-tui
```

By default `evener-tui` connects to `http://127.0.0.1:9180`. If no local hub is running, it starts `evener-hub` automatically and waits for `/api/health`.

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
| `--log-file <path>` | Write auto-started hub logs to this file |
| `--debug` | Disable the alternate screen |

### Features

- **Dashboard**: Browse live and saved sessions from the hub roster and past-session index
- **Session drill-in**: Open a session transcript from the dashboard
- **Hub actions**: Send input, view tasks/details, interrupt, compact, clear, and switch models through hub endpoints
- **Streaming**: Follow session AppWire streams through the hub
- **Markdown rendering**: Format-aware display of assistant messages
- **Tool inspection**: Collapse/expand tool calls and view arguments

## Evener Hub (Web Orchestrator)

`evener-hub` is a sibling binary that runs alongside `evener serve` daemons and gives you a single browser-based interface for many concurrent sessions.

### Build & run

```bash
make build-hub
evener-hub  # default 127.0.0.1:9180
```

Open `http://127.0.0.1:9180` in your browser.
For production-style setup, credentials, Codex app-server sources, and smoke
checks, see [`cmd/evener-hub/README.md`](cmd/evener-hub/README.md).

### What's there

- **Sidebar** with a Live section (every running session sorted by who needs you) and a Projects section (sessions grouped by working directory; subagents indented under origin; forks immediately following with the `⎇` glyph).
- **Workspace pane** with a two-tier conversation: messages (user pills + assistant body) at the primary reading tier, tool calls and diffs as muted margin annotations.
- **New session** at `/new` — prompt-first, pick model and working dir, click spawn.
- **Edit-to-fork**: hover any prior user message, click `✎ edit`, hit ⌘↵, label the original branch, confirm. The new branch becomes active; the original is preserved as a sibling fork.
- **Aside**: `/aside` (TUI) or the *Aside: fork to side thread* palette command (web UI) forks the current session at its tip into a side thread with the same permissions and config — for asking a distracting question without derailing the main session.
- **Transparent resume**: click any closed session, type, send. The daemon spawns from where it left off — same identity throughout.
- **⌘K search** across live + past sessions.
- **Settings** for theme (light/dark/system), notification preferences (all opt-in), and read-only inspection of providers and MCP setup.

### Configuration

`~/.evener/hub.toml` (optional):

```toml
addr = "127.0.0.1:9180"
spawn_timeout = "30s"
past_results_per_page = 50
# Optional; default is ~/.evener/index.db.
past_index_db = "/Users/you/.evener/index.db"
```

Hub launch model choices come from the Evener launch harness contract
(`evener launch-check --models`), not from a static model roster in `hub.toml`.
Launch defaults live in layered launch config files. For user-wide defaults,
create `~/.evener/launch.toml`:

```toml
app_replay_size = 4096

[env]
OPENAI_API_KEY = "..."
```

### Architecture

Daemons are loopback-only. Each writes a private rendezvous file to `~/.evener/run/<pid>.json`; the hub watches the directory, probes daemons for state, and proxies AppWire/REST so the browser only ever talks to the hub origin. Hub-spawned daemons require the per-hub bearer token recorded in their rendezvous file. Daemon and Hub same-origin guards plus strict Hub CSP defend against DNS-rebinding and cross-origin attacks.

### Operating notes

- **Daemons keep the binary they were spawned from.** Rebuilding `evener` does not update already-running daemons; live sessions continue to run the old code until they shut down. To pick up changes mid-session, end the session (which terminates its daemon), rebuild, and resume — resume reads the new binary. This matches typical daemonized-server behavior and is the same model as restarting a long-lived service after a deploy.
- **Remote hosts**: see `docs/evener-hub-remote-operations.md` for the current deployment runbook, including credential handling, state directories, browser/TUI access, health checks, and Codex app-server sources.
- **Rebuild and restart a launchd-managed hub**: `scripts/deploy-hub.sh` builds this worktree's `evener-hub` and `kickstart -k`s its launchd job, never stopping the old process until the new one is built and healthy — see `scripts/deploy-hub.sh --help`.

Design spec, plans, and notes live under `docs/superpowers/`.

## Linting

The repo enforces a single naming rule across wire formats:

- **JSON tags** must be `snake_case`, except for documented AppWire/Codex protocol carve-outs that require `camelCase`.
- **TOML tags and keys** must be `snake_case`.
- **CLI flags** are `kebab-case` (enforced at the flag registry).

Run the linter via `make lint-naming` or directly with `go run ./cmd/evener-namingcheck`. CI runs it after `go vet`. The check is fast (< 1s on this tree) and exits non-zero on violations. A single field/key can opt out with a `// evener:naming-ignore` (Go) or `# evener:naming-ignore` (TOML) marker on the preceding line — use sparingly, and explain why.

## Acknowledgments

Evener is forked from [Kilroy](https://github.com/danshapiro/kilroy) by Dan Shapiro, originally built as part of the [StrongDM Attractor](https://github.com/strongdm/attractor) project. The unified LLM client, provider adapters, and agentic tool-calling loop all trace their lineage to that work. Kilroy is licensed under the MIT License (see `LICENSE-kilroy`).
