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
| Codex AppWire protocol | `internal/appwire/`, `internal/appserver/`, `internal/appsource/` | camelCase (codex requirement) |
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

- **`internal/appwire/` (plus `internal/appserver/` and
  `internal/appsource/`) is camelCase.** These packages speak the
  Codex app-server protocol over JSON-RPC (`clientInfo`,
  `protocolVersion`, `turnId`, `itemId`). Casing is non-negotiable:
  changing it breaks wire compatibility with codex clients. AppWire
  fields surfaced through hub REST endpoints keep their camelCase
  spellings end-to-end so consumers don't have to translate.
- **`llm/providers/*/` follows each provider's wire format.** OpenAI,
  Anthropic, Ollama, and OpenRouter use snake_case (`finish_reason`,
  `display_name`, `has_more`). Google Gemini uses camelCase
  (`displayName`). These are wire formats owned by the provider.

Plus the usual trivial cases: `inspo/codex/**` is skipped entirely
(vendored reference code), and single-word keys (`model`, `name`,
`addr`, `version`) are case-invariant.

## Enforcement

`cmd/serf-namingcheck` walks the AST and checks every `json:"..."`
and `toml:"..."` struct tag plus every TOML key. The linter enforces
the snake-default rule and applies the path carve-outs above
(`internal/appwire/` requires camelCase; `llm/providers/*/` is skipped
entirely). A single field can opt out with a
`// serf:naming-ignore` (Go) or `# serf:naming-ignore` (TOML) marker
on the immediately preceding line; pair every opt-out with a comment
explaining why.

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

The tree mostly matches the new rule. About 14 camelCase JSON tags
remain outside the carve-out paths, and all of them mirror an upstream
format:

- `turnId`, `itemId`, `callId`, `threadId` in
  `server/appwire_runtime.go`, `cmd/serf-hub/web.go`,
  `cmd/serf-tui/hub_model.go`, `internal/hubapi/types.go` — AppWire
  fields surfaced end-to-end through hub APIs.
- `mcpServers` in `agent/mcp_config.go` and `agent/plugin.go` —
  mirrors the Claude `.mcp.json` format.

Keep these camelCase and mark them `// serf:naming-ignore` with a
pointer back to the upstream type. No bulk rename needed.
