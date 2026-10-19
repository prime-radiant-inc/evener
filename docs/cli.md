# CLI reference

Use `evener` to run one task, inspect configuration, or start a server. These instructions track **main**; an installed release can have different flags. Run `evener --version` and `evener --help` to identify your binary.

For a bounded scripting recipe, see [Automate a task](automation.md). Configure a provider first using [Get started](getting-started.md).

## Run one task

From the repository you want Evener to inspect, replace `provider/model` with an available model from your configured provider:

```sh
evener --model provider/model --timeout 2m --max-rounds 8 --sandbox read-only --sandbox-net off \
  'Summarize this repository. Do not change files or start background services.'
```

Fresh CLI tasks require `--model provider/model` or `EVENER_MODEL=provider/model`; the flag takes precedence. A registry default or browser model selection does not supply the CLI model. Sandbox support depends on the host; a failed sandbox setup is an error, not permission to retry without protection.

**The default sandbox is off and network access is on.** A prompt is not an access control. Review project instructions, hooks, plugins and MCP servers before unattended execution. Run in a disposable checkout with only the credentials the task needs.

### Input

Syntax: `evener [flags] [--] prompt...`. Put flags before the prompt: Go's flag parser stops at the first non-flag argument. Arguments are joined with spaces and trimmed. A nonempty argument prompt takes precedence over stdin. Without one, Evener reads non-terminal stdin to EOF and trims it. Use `--` before a prompt beginning with `-`; a first argument matching a command selects that command instead.

```sh
printf '%s\n' 'Explain the public API without modifying files.' |
  evener --model provider/model --timeout 2m --max-rounds 8 --sandbox read-only --sandbox-net off
```

Bare `evener` in an interactive terminal lists the top-level commands and points to `evener hub` for the browser interface, then exits 1. It does not start the hub, open a browser, or launch a terminal UI. An empty scripted prompt without a resume or listing option prints usage and exits 1. `--list-sessions` does not read stdin. Do not leave a pipe open: stdin is read before the run timeout starts.

### Output and exits

| Channel or status | Contract |
| --- | --- |
| stdout | Final root-session result followed by a newline, after managed work drains and the session closes. Session listing and subcommands have their own output. |
| stderr | Progress, warnings and errors. `--verbose` emits events as NDJSON here, but startup notices and errors can still be plain text. Do not treat the entire stream as guaranteed JSON. |
| 0 | The invocation returned without an error, or help/version completed. This does not prove that the requested artifact is correct. |
| 1 | One-shot runtime/setup failure, empty input, or an error-returning subcommand failure. |
| 2 | One-shot flag parsing and certain early option-validation failures. Subcommands can use their own usage exits. |
| 128 + signal | A repeated SIGINT/SIGTERM forces exit. The first signal requests graceful cancellation and session close. |

Hub, doctor and migrate propagate their own nonzero exit codes. Do not interpret every nonzero code as the same failure. On one-shot failure, the final result is not printed; preserve stderr and inspect saved state before retrying.

`--output-schema` takes an **inline JSON Schema**, not a filename. It replaces the schema of the result tool's `output` field; it is not a global JSON logging switch. Validate the actual stdout and the resulting artifacts. Root results and delegate terminal messages have different semantics: see [Subagent runtime contracts](subagent-runtime-contracts.md#delegate-report-preservation).

### Deadlines and background work

`--timeout` bounds the one-shot run, including managed-job draining; zero disables it. It is not a hard process-kill deadline for startup stdin reads or a wedged close. A supervisor can send SIGTERM, allow cleanup, then send a second signal to force exit. Forced exit can lose final persistence or exports.

`--provider-idle-timeout` measures lack of provider response bytes, not total request duration. `--max-rounds` limits tool rounds per input, not wall time, total spend or the whole delegate tree. After the initial input finishes, the CLI drains session-owned jobs and processes their completions before printing its result. Detached processes are outside that lifecycle; see [Job control](job-control.md).

## Resume or inspect a saved session

```sh
evener --list-sessions
evener --resume SESSION_ID --timeout 2m 'Continue and report what remains.'
evener --resume-with SESSION_ID --timeout 2m 'Review the earlier findings independently.'
```

Replace `SESSION_ID` with an ID from the listing. `--resume` continues the saved session; `--resume-with` creates a new session using the old context. `--resume-last` selects the most recent session. Use explicit IDs in scripts to avoid resuming unrelated work. Plugin selection cannot be changed with `--resume` or `--resume-last`; `--ask-responder` cannot be combined with any resume mode.

## Commands

Each command owns its flags. Run `evener COMMAND --help` before scripting a subcommand; one-shot options are not universal options.

| Command | Use |
| --- | --- |
| `serve` | Run one session's HTTP/AppWire daemon. Default address: `127.0.0.1:9131`. |
| `hub` | Run the web orchestrator and route client requests to sessions. Includes `attach` for a stdio bridge. |
| `launch-check` | Validate a provider/model launch contract before starting work. |
| `models` | `list`, `inspect`, `refresh`: inspect the model registry. |
| `providers` | `list`, `probe`, `add`: inspect or author provider instances. A probe can contact a provider. |
| `openai` | `login`, `logout`, `status`: manage OpenAI OAuth credentials. |
| `plugin` | Manage marketplaces and plugins. See [Plugins](plugins.md). |
| `doctor` | Read-only forensic inspection of sessions, jobs and watches. |
| `migrate` | Migrate stored user data. Back up state before a migration. |
| `upgrade` | Upgrade installed binaries. Review the target before changing a working installation. |
| `install` | Install on a remote macOS or Linux host over SSH. |

For client startup and authentication, see [Integrations](integrations.md). `serve` also accepts daemon-specific options such as `--run-dir`, `--app-replay-size` (default 1000), `--non-interactive`, `--model-fallback` (repeatable) and `--daemon-idle-timeout` (default 0, automatic retirement disabled). It does not accept every one-shot option, including `--timeout` or `--ask-responder`.

## Upgrade binaries

Syntax: `evener upgrade [flags] [release|snapshot|vTAG]`. Put flags before the
target. `evener upgrade release` selects the latest release;
`evener upgrade snapshot` switches to the snapshot channel. Either command can
switch channels. `evener upgrade vTAG` installs a specific published release tag;
replace `vTAG` with the actual tag. With no target, snapshot builds track
snapshots and other builds select the latest release. Run `evener upgrade -h`
for installation-path flags, and review the target before replacing binaries.

## Doctor session selectors

Doctor reads saved evidence without running a model. Discover sessions first:

```sh
evener doctor sessions --json
evener doctor locate SESSION_ID
evener doctor transcript SESSION_ID --format outline
evener doctor transcript SESSION_ID --range last:10
evener doctor jobs SESSION_ID
```

Replace `SESSION_ID` with an ID from `sessions`, or copy its full session ref.
The selector grammar is:

| Form | Resolution |
| --- | --- |
| `SESSION_ID` | Search all buckets under the selected state root. |
| `local:SESSION_ID` | Also search all buckets; it does not mean the current working directory. |
| `proj:PROJECT_ID:SESSION_ID` | Select one project bucket, resolving duplicate IDs across buckets. |

`PROJECT_ID` is the stored bucket key, not a repository path. Copy it from
discovery output rather than deriving it. The final colon separates the session
ID, so project keys may contain colons. Project keys cannot be empty, `.` or `..`,
or contain `/`, `\`, or NUL. Session IDs must pass the session-ID grammar;
use the emitted ID unchanged. Empty selectors and `current` are rejected because
Doctor has no current session. If a bare or `local:` ID is ambiguous, use the
full `proj:` ref.

For example, after replacing both placeholders with discovered values:

```sh
evener doctor locate 'proj:PROJECT_ID:SESSION_ID'
evener doctor watches 'proj:PROJECT_ID:SESSION_ID' --json
evener doctor tree 'proj:PROJECT_ID:SESSION_ID'
```

Use `--state-dir PATH` to inspect another state root. Without it, Doctor uses
`EVENER_STATE_DIR`, then `XDG_STATE_HOME`, then `~/.local/state`. Use the same
root for discovery and subsequent reads. `sessions`, `turnids`, `audit`, and
`plugins` do not take a session selector. Run `evener doctor SUBCOMMAND -h` for
that command's flags. Transcripts and API logs can contain secrets and private
task content; review them before sharing.

### Doctor subcommands

| Subcommand | Reads |
| --- | --- |
| `locate` | Resolves a selector to its transcript, API log, meta, jobs, and mutation files. |
| `transcript` | Renders logical turns; `--count TOOL` counts structural calls, `--health` prints per-session metrics. |
| `apilog` | Provider attempts: tokens, latency, empties, errors, cache spikes; `--summary` and `--health` condense it, `--validate` scans integrity. |
| `jobs` | Every job the session ran, with status, reason, exit code, output bytes, and timings. |
| `mutations` | The client-mutation journal and durable input queue. |
| `watches` | Distinct deliveries, provenance, and breaker telemetry; `--self-loops` narrows to runaway fuses. |
| `tree` | Parent, delegate, and observer relationships across buckets; `--observers` and `--depth N` adjust the view. |
| `reconstruct` | Stages a lost transcript from an archive and surviving metadata without changing live state. |
| `sessions` | Enumerates sessions by last activity, for batch study. |
| `turnids` | Sweeps every session for reserved turn IDs minted inside the transcript's entry-index namespace. |
| `audit` | Runs a named runbook's checks over `--sessions` or `--since`, emitting deduplicated findings. |
| `plugins` | Plugin-store health: registry and disk drift, marketplace health, component validity, auto-upgrade sanity. |

User-facing recovery steps for the common cases are in
[Troubleshooting](troubleshooting.md#inspect-a-saved-session).

## One-shot options

The following reference follows `newRunFlagSet` in [`cmd/evener/main.go`](../cmd/evener/main.go). An omitted string option is resolved by its subsystem; there is no universal configuration precedence. See [Configuration](configuration.md) for persisted settings.

**Parser default** is the value the flag parser supplies when you omit an option,
not necessarily the value the session uses. For a new one-shot session:

- `--max-rounds`: omitted `-1` resolves to **unlimited**, as does explicit `0`.
  A positive value sets a cap. The [conversion](../cmdutil/cmdutil.go) and
  [session defaults](../agent/session_config.go) define the current behavior.
- `--max-subagent-depth`: omitted `-1` leaves the session default of **2**.
- `--max-concurrent-delegates`: omitted `-1` leaves the session default of **50**.
  For these two delegation limits, explicit `0` also selects the default; it
  does not mean unlimited or disabled.
- `--max-retained-terminal`: omitted `-1` or explicit `0` selects **2048**
  retained terminal delegate records per session.
- `--vision-model`: omitted `""` uses the **active session model** for image
  descriptions. It does not disable vision; use `off` to disable that side channel.

Read each option's meaning separately: `-1`, `0`, and `""` have no universal
runtime meaning across flags.

| Option | Parser default | Meaning |
| --- | --- | --- |
| `--model` | `""` | LLM model identifier (provider/model) |
| `--fast-cheap-model` | `""` | auxiliary model for side calls (naming, summarization, web fetch); 'provider/model' may use a different provider than --model, or a bare 'model' for the active provider |
| `--vision-model` | `""` | vision side-channel model: 'off' disables image description, 'provider/model' or bare 'model' routes it (default: the session model) |
| `--dir` | `""` | working directory (default: current directory) |
| `--system-prompt` | `""` | path to a custom system prompt file |
| `--state-dir` | `""` | override runtime state directory (default: XDG-computed) |
| `--disable-memory` | `false` | disable native memory for this session, including on resume |
| `--resume` | `""` | resume a previous session by id |
| `--resume-with` | `""` | start a new prompt using a previous session's id as context |
| `--resume-last` | `false` | resume the most recent session |
| `--list-sessions` | `false` | list saved sessions and exit |
| `--max-rounds` | `-1` | max tool rounds per input (0=unlimited, effective default: unlimited) |
| `--max-subagent-depth` | `-1` | max subagent nesting depth (default: 2) |
| `--max-concurrent-delegates` | `-1` | max concurrently running delegate turns per session tree (default: 50) |
| `--max-retained-terminal` | `-1` | max retained terminal delegate records per session (default: 2048) |
| `--share-task-store` | `false` | share task list between parent and child sessions |
| `--result-tool-name` | `""` | override the result tool name (default: communicate) |
| `--reasoning-effort` | `""` | reasoning effort level: minimal\|low\|medium\|high\|xhigh\|max\|none |
| `--export-atif` | `""` | export ATIF v1.7 trajectory to this path on session close |
| `--export-atif-provider-handles` | `""` | ATIF provider handle export mode: redacted\|raw-local (default: redacted) |
| `--context-strategy` | `""` | context management strategy: compact\|session-log\|ooda (default: compact) |
| `--output-schema` | `""` | inline JSON Schema document applied to the communicate tool's output field (replaces the default schema) |
| `--verbose` | `false` | emit NDJSON events to stderr |
| `--agent` | `""` | agent persona name: default (default), explorer, or another available agent name |
| `--skills-dir` | `omitted` | extra skill directory (repeatable) |
| `--mcp` | `omitted` | MCP server spec (repeatable, format: name:command args...) |
| `--mcp-config` | `omitted` | path to .mcp.json file (repeatable) |
| `--plugin-dir` | `omitted` | plugin directory (repeatable) |
| `--enabled-plugins` | `omitted` | comma-separated plugin names to enable (empty selects none) |
| `--no-default-marketplaces` | `false` | do not seed the default plugin marketplaces on first run |
| `--system-prompt-as-user` | `false` | deliver system prompt as first user message instead of system instructions |
| `--provider-idle-timeout` | `""` | Provider response-byte idle duration (default: 10m; no total request limit) |
| `--openai-responses-continuation` | `""` | OpenAI Responses continuation mode: off\|auto (default: off) |
| `--cpu-profile` | `""` | write CPU profile to this file path |
| `--trace` | `""` | write execution trace to this file path |
| `--system-prompt-append` | `omitted` | path to append to system prompt file (repeatable) |
| `--sandbox` | `"off"` | sandbox mode: off (default), read-only, workspace-write, or restricted |
| `--sandbox-net` | `"on"` | sandbox network egress on\|off (default on; only applies with a non-off --sandbox mode) |
| `--api-log` | `"off"` | durable API request logging on\|off (default off; on records every provider request and response to `<state-dir>/sessions/<id>.api.jsonl`) |
| `--timeout` | `0` | overall one-shot run timeout (0 disables; rate-limit retries use their finite fallback) |
| `--ask-responder` | `""` | Command run through sh -c to answer ask_user, questions JSON on stdin and reply on stdout. Enables interactive behavior, with at most three responder rounds per input. |

Negative numeric parser defaults defer to runtime defaults described above. Boolean flags default to false unless shown otherwise. Repeatable path options add entries rather than accepting a JSON array.

`--ask-responder` executes a shell command and can disclose questions to it. The command must validate its input, protect secrets and fail closed when it cannot answer. Do not use an unconditional approval script.

Sources: [`main.go`](../cmd/evener/main.go), [`run.go`](../cmd/evener/run.go), [`serve.go`](../cmd/evener/serve.go), and [`cliprompt`](../cmd/evener/internal/cliprompt/cliprompt.go).
