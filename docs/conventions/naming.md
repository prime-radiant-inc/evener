# Naming conventions for serialized identifiers

This document is the canonical reference for how serf names identifiers
that cross a serialization boundary (JSON payloads, TOML config files,
CLI flags). A linter (see [Enforcement](#enforcement)) enforces it.

It does **not** cover file names on disk, Go identifier naming,
internal-only struct field names (no JSON/TOML tags), browser-side
`localStorage` keys, or environment variable names (already
SHOUTING_SNAKE per shell convention).

## The rule

> **JSON is snake_case. TOML is snake_case. CLI flags are kebab-case.**

| Surface | Casing | Example |
|---|---|---|
| JSON | `snake_case` | `"reasoning_effort": "medium"` |
| TOML | `snake_case` | `reasoning_effort = "medium"` |
| CLI flag | `kebab-case` | `--reasoning-effort medium` |

## Why these choices

The center of gravity is upstream LLM providers. Four of the five we
talk to use snake_case on the wire: OpenAI (`finish_reason`),
Anthropic (`has_more`, `last_id`), Ollama, and OpenRouter. Only Google
Gemini uses camelCase (`displayName`). Serf transcripts and audit
logs sit right next to those provider responses; matching snake_case
keeps the eye honest when reading raw JSON. The codebase already
leans this way: 203 snake-case JSON tags vs 169 camelCase, the latter
concentrated under `internal/appwire/` and the Google adapter.

TOML matches JSON so the same conceptual field reads identically in a
session save file and a launch config. Only the CLI diverges, because
hyphens are the universal Unix flag style.

An earlier draft proposed camelCase JSON everywhere on the strength of
codex interop. That underweighted the providers, which collectively
outnumber codex. This rev backs off.

## Surface map

| Surface | Where | Casing |
|---|---|---|
| Codex AppWire protocol | `internal/appwire/`, `internal/appsource/`, `internal/appserver/`, `server/appwire_*.go` | camelCase (codex requirement) |
| Provider request/response shapes | `llm/providers/*/` | per provider (snake_case for OpenAI/Anthropic/Ollama/OpenRouter; camelCase for Google) |
| Hub REST/SSE JSON | `cmd/serf-hub/`, `internal/hubapi/`, `server/` | snake_case |
| Rendezvous files (`~/.serf/run/*.json`) | `rendezvous/` | snake_case |
| Session save files | `agent/session.go`, `agent/transcript.go` | snake_case |
| Hub config (`~/.serf/hub.toml`) | `cmd/serf-hub/config.go` | snake_case |
| Launch config (`~/.serf/launch.toml`, `.serf/launch.toml`) | `internal/launchconfig/` | snake_case |
| Project meta (`~/.serf/projects/<id>/meta.toml`) | `internal/launchconfig/` | snake_case |
| `serf` CLI flags | `cmd/serf/`, `cmd/serf-hub/`, `cmd/serf-tui/`, `cmd/llmcall/`, `cmd/serfeval/` | kebab-case |

Go struct tags follow the rule directly: `json:"snake_case"` and
`toml:"snake_case"`. Field names in Go source stay idiomatic Go
(`ReasoningEffort`, not our concern); only the tag value is governed by
this document.

## Same field, three names

The same conceptual setting can appear on all three surfaces. Because
JSON and TOML now agree, only the CLI's hyphens differ.

| Concept | CLI flag | TOML key / JSON field |
|---|---|---|
| Reasoning effort level | `--reasoning-effort` | `reasoning_effort` |
| Extra plugin directory | `--plugin-dir` (repeatable) | `plugin_dirs` (array) |
| Extra skills directory | `--skills-dir` (repeatable) | `skills_dirs` (array) |
| Path to `.mcp.json` | `--mcp-config` (repeatable) | `mcp_config_files` (array) |
| Max tool rounds per input | `--max-rounds` | `max_rounds` |
| Max subagent nesting depth | `--max-subagent-depth` | `max_subagent_depth` |
| SSE ring buffer size | (none) | `sse_ring_size` |
| Suppress `.serf/prompts/` loading | `--no-project-prompts` | `no_project_prompts` |

Note the CLI singular form (`--plugin-dir`, repeatable) pairs with a
TOML/JSON plural array (`plugin_dirs = [...]`). That is intentional:
it reads naturally in both surfaces.

## Exceptions

Two carve-outs, each forced by an upstream we don't own:

- **Codex/appwire wire protocol code is camelCase.** Any code that
  participates in the codex/appwire wire protocol uses camelCase,
  matching the protocol definition under `internal/appwire/`. That
  covers:
  - `internal/appwire/` — the protocol definition itself
    (`clientInfo`, `protocolVersion`, `turnId`, `itemId`).
  - `internal/appsource/` — clients of the appwire/codex protocol;
    `CodexSource` serializes the codex wire format.
  - `internal/appserver/` — the appwire server implementation, which
    speaks the same wire format back to clients.
  - `server/appwire_*.go` — the hub's appwire runtime glue, which
    threads appwire payloads through the hub.
  Casing here is non-negotiable: changing it breaks wire compatibility
  with codex clients.
- **`llm/providers/*/` follows each provider's wire format.** OpenAI,
  Anthropic, Ollama, and OpenRouter use snake_case (`finish_reason`,
  `display_name`, `has_more`). Google Gemini uses camelCase
  (`displayName`). These are wire formats owned by the provider.

Plus the usual trivial cases: `inspo/codex/**` is skipped entirely
(vendored reference code), and single-word keys (`model`, `name`,
`addr`, `version`) are case-invariant.

**One known mixed-casing surface**: `POST /api/spawn` accepts a
snake_case body at the top level (`prompt`, `working_dir`,
`access_mode`, `launch_overrides`) BUT the `launch_overrides`
sub-object is the appwire `LaunchConfigLayer` type, which is
camelCase (`pluginDirs`, `skillsDirs`, `reasoningEffort`,
`mcpConfigs`) because it lives under the codex-forced carve-out
above. So a single request body legitimately contains both casings:

```json
{
  "prompt": "...",
  "working_dir": "/tmp",
  "launch_overrides": {
    "pluginDirs": ["..."],
    "reasoningEffort": "medium"
  }
}
```

This is intentional. Two fixes were considered: duplicating the
struct as a snake-tagged mirror with translation at the boundary
(rejected — keeps two source-of-truth structs in sync forever), or
accepting both casings via custom UnmarshalJSON on the appwire type
(rejected — adds reader complexity for one endpoint). The cost of
either fix outweighs the convenience of internal consistency in one
POST body. If you write `/api/spawn` payloads by hand, expect this
boundary; the JS spawn form, the TUI, and Toil all handle it
correctly.

## Enforcement

`cmd/serf-namingcheck` walks the AST and checks every `json:"..."`
and `toml:"..."` struct tag plus every TOML key. The linter enforces
the snake-default rule and applies the path carve-outs above
(appwire-adjacent code — `internal/appwire/`, `internal/appsource/`,
`internal/appserver/`, `server/appwire_*.go` — requires camelCase;
`llm/providers/*/` is skipped entirely). A single field can opt out
with a `// serf:naming-ignore` (Go) or `# serf:naming-ignore` (TOML)
marker on the immediately preceding line; pair every opt-out with a
comment explaining why.

Run locally:

```bash
go run ./cmd/serf-namingcheck ./...
# or
make lint-naming
```

## Adding a new surface

When you add a new TOML config file, JSON payload, or CLI flag:

1. **Default is snake_case JSON, snake_case TOML, kebab-case CLI.**
   Only deviate if an upstream protocol forces your hand; if so, add a
   row to [Exceptions](#exceptions).
2. **Apply it to every field.** Mixed casing inside one surface is
   worse than the wrong casing applied uniformly.
3. **If the same field crosses CLI and JSON/TOML, add a row** to the
   [Same field, three names](#same-field-three-names) table so future
   contributors don't "normalize" the CLI hyphens away.
4. **Run the linter** before opening the PR.

## See also

- `agent/session.go`, `agent/transcript.go` — snake_case JSON examples
  (`session_id`, `working_dir`, `plugin_dirs`).
- `internal/launchconfig/types.go` — snake_case TOML examples
  (`reasoning_effort`, `max_rounds`, `sse_ring_size`).
- `internal/appwire/types.go` — camelCase JSON examples for the codex
  carve-out (`clientInfo`, `protocolVersion`, `pluginDirs`).
- `cmd/serf/main.go` — kebab-case CLI flag examples
  (`--reasoning-effort`, `--max-rounds`).

## Migration status

The tree matches the rule end-to-end. Only two JSON tags outside the
appwire/providers carve-outs intentionally stay camelCase:

- `mcpServers` in `agent/mcp_config.go` and `agent/plugin.go` —
  mirrors the Claude `.mcp.json` format. Marked
  `// serf:naming-ignore` with a pointer back to the upstream format.

Hub REST/SSE shapes and TUI-internal types that previously leaked
camelCase have all migrated: REST request bodies use `turn_id`, the
TUI now reuses the appwire-defined `ToolOutputDeltaParams` and
`NotificationRef` types directly instead of locally redeclaring the
wire shape with camelCase tags.
