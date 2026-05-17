# Naming conventions for serialized identifiers

This document is the canonical reference for how serf names identifiers
that cross a serialization boundary (JSON payloads, TOML config files,
CLI flags). A linter (see [Enforcement](#enforcement)) enforces it.

It does **not** cover file names on disk, Go identifier naming,
internal-only struct field names (no JSON/TOML tags), browser-side
`localStorage` keys, or environment variable names (already
SHOUTING_SNAKE per shell convention).

## The rule

> **JSON is camelCase. TOML is kebab-case. CLI flags are kebab-case.**

| Surface | Casing | Example |
|---|---|---|
| JSON | `camelCase` | `"reasoningEffort": "medium"` |
| TOML | `kebab-case` | `reasoning-effort = "medium"` |
| CLI flag | `kebab-case` | `--reasoning-effort medium` |

## Why these choices

Two forces pull in opposite directions and the rule reconciles them.

**JSON is camelCase because of codex interop.** Serf speaks the Codex
app-server protocol over JSON-RPC under `internal/appwire/`. That
protocol is camelCase (`clientInfo`, `protocolVersion`, `threadStart`,
`turn/started`). It is third-party and non-negotiable: changing the
casing breaks wire compatibility with codex clients. Once one
JSON-emitting surface is camelCase, making every other JSON surface
camelCase too is the only consistent choice.

**TOML matches the CLI because both are human-edited.** When a user
writes `~/.serf/launch.toml` they have just been writing
`--reasoning-effort` on the command line. Kebab-case is the convention
both Unix CLIs and human-friendly TOML files already prefer, so the same
field reads the same in both places minus the leading `--`.

## Surface map

| Surface | Where | Casing |
|---|---|---|
| Codex AppWire protocol | `internal/appwire/` | camelCase (codex requirement) |
| Hub REST/SSE JSON | `cmd/serf-hub/`, `server/` | camelCase |
| Rendezvous files (`~/.serf/run/*.json`) | `rendezvous/` | camelCase |
| Session save files | `agent/`, `server/` | camelCase |
| Hub config (`~/.serf/hub.toml`) | `cmd/serf-hub/config.go` | kebab-case |
| Launch config (`~/.serf/launch.toml`, `.serf/launch.toml`) | `internal/launchconfig/` | kebab-case |
| Project meta (`~/.serf/projects/<id>/meta.toml`) | `internal/launchconfig/` | kebab-case |
| `serf` CLI flags | `cmd/serf/`, `cmd/serf-hub/`, `cmd/serf-tui/`, `cmd/llmcall/`, `cmd/serfeval/` | kebab-case |

Go struct tags follow the rule directly: `json:"camelCase"` and
`toml:"kebab-case"`. Field names in Go source stay idiomatic Go
(`ReasoningEffort`, not our concern); only the tag value is governed by
this document.

## Same field, three names

The same conceptual setting can have **three different spellings**
across surfaces. That is intentional. Do not "fix" them to match.

| Concept | CLI flag | TOML key | JSON field |
|---|---|---|---|
| Reasoning effort level | `--reasoning-effort` | `reasoning-effort` | `reasoningEffort` |
| Extra plugin directory | `--plugin-dir` (repeatable) | `plugin-dirs` (array) | `pluginDirs` |
| Extra skills directory | `--skills-dir` (repeatable) | `skills-dirs` (array) | `skillsDirs` |
| Path to `.mcp.json` | `--mcp-config` (repeatable) | `mcp-configs` (array) | `mcpConfigs` |
| Max tool rounds per input | `--max-rounds` | `max-rounds` | `maxRounds` |
| Max subagent nesting depth | `--max-subagent-depth` | `max-subagent-depth` | `maxSubagentDepth` |
| SSE ring buffer size | (none) | `sse-ring-size` (under `[serf-launch]`) | `sseRingSize` |
| Suppress `.serf/prompts/` loading | `--no-project-prompts` | `no-project-prompts` | `noProjectPrompts` |

Note that the CLI singular form (`--plugin-dir`, repeatable) often pairs
with a TOML plural array (`plugin-dirs = [...]`). That is also
intentional: it reads naturally in both surfaces.

## Exceptions

- **AppWire / codex protocol types stay camelCase even in JSON sent
  over the wire to non-codex consumers.** Anything under
  `internal/appwire/` is part of the codex app-server contract and is
  exempt from "but you could use snake_case here" arguments. This is
  the reason the JSON-is-camelCase rule was picked.
- **Third-party codex source under `inspo/codex/**` is exempt from all
  checks.** It is vendored reference code, not ours to restyle.
- **Single-word keys are case-invariant** and trivially conform to every
  rule: `model`, `agent`, `addr`, `name`, `version`, `args`, `env`,
  `command`. Do not invent multi-word synonyms just to exercise the
  rule.
- **External API payloads** (OpenAI request bodies, Anthropic request
  bodies, model catalog entries that mirror upstream APIs under `llm/`)
  follow the upstream provider's casing. They are *not* our serialized
  identifiers; they are wire formats owned by the provider. The linter
  must skip these.

## Enforcement

A linter (command name TBD — likely `cmd/serf-namingcheck` or wired
into `go vet`) walks the AST and checks every `json:"..."` and
`toml:"..."` struct tag plus every `flag.*` call site. Violations fail
the build in CI.

The linter has a small allowlist for the exceptions above:

- Files under `inspo/codex/**` are skipped entirely.
- Files under `llm/` that mirror upstream provider request/response
  shapes are skipped (subject to per-file annotation).
- Single-word tag values are accepted under either rule.

Run locally:

```bash
# Placeholder — see the linter command's --help once it lands.
go run ./cmd/serf-namingcheck ./...
```

(TODO: replace with the actual command name and link to its `--help`
output once the linter lands.)

## Adding a new surface

When you add a new TOML config file, JSON payload, or CLI flag:

1. **Pick the right casing from the [surface map](#surface-map).** If
   you are adding a new kind of surface not listed there, ask first —
   do not invent a new policy.
2. **Apply it to every field.** Mixed casing inside one surface is
   worse than the wrong casing applied uniformly.
3. **If the same field needs to appear on multiple surfaces, give it
   three names** per the rule. Add a row to the
   [Same field, three names](#same-field-three-names) table so future
   contributors do not "normalize" the spellings.
4. **Run the linter** before opening the PR.

## See also

- `internal/appwire/types.go` — canonical camelCase JSON examples.
- `internal/launchconfig/types.go` — canonical kebab-case TOML examples
  (once migrated; see TODO below).
- `cmd/serf/main.go` `newRunFlagSet` — canonical kebab-case CLI flag
  examples.

## TODO

- Link this document from a contributor guide once one exists
  (`CONTRIBUTING.md`, `AGENTS.md`, or `CLAUDE.md`). None of these exist
  in the repo today.
- Link to the linter command's `--help` output once the linter lands.
- Migrate existing snake_case TOML tags (`reasoning_effort`,
  `plugin_dirs`, …) and snake_case internal JSON tags (`rendezvous/`,
  `server/`) to the rule. The linter PR should land alongside the
  migration so CI does not turn red on day one.
