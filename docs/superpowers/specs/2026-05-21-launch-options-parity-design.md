# Launch Options Parity Design

## Summary

The web and TUI launch surfaces should expose the same launch/session behavior that `serf serve` can receive through hub launch configuration. The goal is not literal flag parity with every `serf serve` flag. The goal is semantic parity for user-facing launch behavior across:

- web per-launch Advanced options
- web launch settings defaults
- TUI per-launch overrides
- TUI launch settings defaults

The implementation should preserve the current launch UX: prompt first, top-level launch chips, and a readable Advanced disclosure. A shared launch option schema should drive behavior, validation, coverage, and serialization; it should not force a generic packed visual layout.

## Scope

In scope:

- All existing `launchconfig.Layer` fields.
- System prompt support promoted into launch config:
  - base system prompt source: default, file, or inline text
  - append to system prompt: none, file, or inline text
- Debug Logging options:
  - `verbose`
  - `trace`
  - `cpuProfile`
  - `exportATIF`
- Non-secret environment fallback display on the per-launch screen only.
- Web and TUI defaults for the same launch-configurable fields.

Out of scope:

- Hub-owned process controls: `addr`, `runDir`, `resume`, `resumeLast`, `stateDir`.
- Other CLI-only behavior flags for this pass: `systemPromptAsUser`, `outputSchema`, `resultToolName`, `shareTaskStore`.
- Moving the project layer from hub state to `.serf/launch.local.toml`; this is already handled by the launch-config path design.

Merge order remains:

```text
global < repo < project < launch
```

The repo layer remains trust-gated and read-only from the UI. The project layer remains local user defaults for the current project, regardless of the later storage migration.

## Launch Option Schema

Add a shared schema in `internal/launchconfig` describing each launch-configurable option. The schema is the contract that prevents web/TUI drift.

Each option should describe:

- field name in `launchconfig.Layer`
- wire field name
- display label
- group
- control kind
- path kind when applicable: dir, file, output-file, command
- repeatability
- defaultable layers
- whether it is valid as a per-launch override
- whether it is debug-only
- non-secret environment fallback metadata
- driver support, where Serf and Codex differ

The schema should support these control kinds:

- model picker
- text
- multiline text
- integer
- boolean
- select
- radio group
- path
- path list
- model list
- MCP server list
- environment key/value list

Expose the schema over AppWire as `serf/launch/schema`. Web should use that AppWire method; a REST wrapper may call the same backend method while the current launch page still uses HTTP endpoints.

## Data Model

Extend `launchconfig.Layer` and its wire/TOML conversions for the new user-facing launch fields:

- `SystemPromptMode`: default, file, inline
- `SystemPromptFile`
- `SystemPromptText`
- `SystemPromptAppendMode`: none, file, inline
- `SystemPromptAppendFile`
- `SystemPromptAppendText`
- `Verbose`
- `TraceFile`
- `CPUProfile`
- `ExportATIFPath`

Existing `SystemPromptAppend []string` should be migrated in behavior to the new single append source. Backward compatibility should read an existing one-entry list as `SystemPromptAppendMode=file` and `SystemPromptAppendFile=<entry>`. Multi-entry legacy append lists should preserve the first entry and report a diagnostic explaining that the UI supports one append source.

`launchconfig.ToArgs` maps these fields to `serf serve` args:

- file system prompt maps to `--system-prompt <path>`
- inline system prompt is materialized by hub into a private temporary file and maps to `--system-prompt <materialized-path>`
- append file maps to `--system-prompt-append <path>`
- append inline text is materialized by hub into a private temporary file and maps to `--system-prompt-append <materialized-path>`
- debug options map to their existing flags where present

Inline prompt materialization should write files under the hub-owned launch/session work area with owner-only permissions, avoid logging the prompt body, and clean up with the launch/session lifecycle. This keeps `serf serve` semantics unchanged, avoids large prompt text in argv/process listings, and lets direct CLI users keep using file-based prompt flags.

## Web Launch UI

The existing launch page structure stays:

- title and subtitle
- top chip row
- prompt textarea
- attachment controls
- Advanced disclosure
- spawn action

The top chip row remains the fast path. Advanced is comprehensive once opened and may require scrolling. Do not optimize for minimum vertical height.

Advanced should render as readable vertical fieldsets, not a packed control grid. Each setting is a vertical row:

- label
- control
- optional hint/status text
- validation message when needed

Recommended groups:

- Agent
- Model
- Limits
- System Prompt
- Resources
- Environment
- Debug Logging

Agent/driver appears first because Serf and Codex are different launch drivers and can change the available options below. Reasoning effort belongs in the Model group with the primary model. It should not be visually grouped with the fast cheap model.

System Prompt should use radio groups:

- system prompt:
  - Serf default
  - Pick a file
  - Fill in text
- append to system prompt:
  - Do not append anything
  - Pick a file
  - Fill in text

The text can be refined during implementation, but it should avoid the word "override" in the user-facing labels.

List fields should render as vertical lists, not chips. Existing values appear first. Add controls appear on a new line after the list. This applies to:

- skill directories
- plugin directories
- MCP config files
- inline MCP servers
- model fallbacks
- environment overrides

Path fields use the existing path picker/autocomplete. Model fields use the existing model picker. Values are validated at add time where validation is available.

Per-launch Advanced may show non-secret environment fallback values, such as `SERF_MODEL` and `SERF_REASONING_EFFORT`, because those values affect the immediate launch. Settings screens must not show environment fallback choices. API tokens and credential env values are never displayed here.

## Web Settings UI

The launch settings screens should expose the same defaultable option schema for global and project layers.

Settings screens:

- edit stored defaults only
- do not show environment fallback choices
- use the same control types as the launch screen
- show resolved/effective values and provenance where existing settings UI already does so
- keep repo layer read-only and trust-gated

The settings UI can be more spacious than spawn Advanced. It still should avoid generated table-like density.

## TUI UI

The TUI should consume the same schema rather than hard-coding an incomplete list of layer rows.

TUI launch settings:

- edit global and project defaults
- show repo trust/read-only status
- support all defaultable schema options
- use path completion for path fields
- use model picker flow for model fields where available
- validate list additions before saving

TUI per-launch overrides:

- use the same option schema as web Advanced
- show driver/agent first
- group reasoning with model
- represent list fields as list editors, not comma-only text for long-term UX
- use multiline text input for inline system prompt and inline append text

The first implementation can reuse existing modal mechanics while replacing the underlying hard-coded field lists with schema-backed list editors and path/model-aware inputs. Raw comma-separated strings are acceptable only as a temporary compatibility bridge for fields whose current TUI editor already uses that shape.

## Validation And Errors

Validation should be consistent across web and TUI by routing through shared backend behavior where practical.

At add/edit time:

- plugin directories must exist and be directories
- skill directories must exist and be directories
- MCP config files must exist and be files
- MCP commands must resolve as commands or executable paths
- system prompt files and append files must exist and be files
- output paths for debug files must have writable parent directories

Existing startup validation still remains the final authority. UI validation is early feedback, not a substitute for `serf serve` startup errors.

Errors should be shown inline near the field that produced them. Spawn failures still surface as hub diagnostics using the detailed error propagation behavior.

## Testing

Backend tests:

- schema contains every intended `launchconfig.Layer` field
- every schema field maps to wire and args behavior where appropriate
- `serf serve` flag coverage is explicitly categorized as launch, debug, hub-owned, or out of scope
- system prompt modes serialize and resolve correctly
- legacy `system_prompt_append` list compatibility
- path validation for system prompt files, append files, resource dirs, MCP configs, and debug output parents

Web tests:

- Advanced renders all schema-backed per-launch fields
- env fallback choices appear only on per-launch Advanced and only for non-secret env vars
- settings screens do not expose env fallback choices
- system prompt radio choices serialize correctly
- append radio choices serialize correctly
- list fields render vertical lists with add controls below existing values
- model/path pickers attach to schema-rendered controls

TUI tests:

- launch settings rows are generated from schema
- default layer edits preserve all supported fields
- path completion is enabled for path fields
- system prompt file/text modes are editable
- list editors reject invalid paths before saving
- reasoning effort appears with model-related settings

## Rollout

Implement in slices:

1. Add schema and coverage tests without changing UI behavior.
2. Add data model fields for system prompt and Debug Logging.
3. Update AppWire wire types and launch args/env handoff.
4. Update web spawn Advanced to schema-backed vertical groups.
5. Update web settings screens to schema-backed defaults.
6. Update TUI settings and per-launch overrides.
7. Remove redundant hard-coded field lists.

Each slice should keep existing launch behavior working.
