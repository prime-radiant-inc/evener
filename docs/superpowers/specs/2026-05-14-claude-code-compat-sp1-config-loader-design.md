# SP1 — Config Loader (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`

## 1. Goal

SP1 turns the unified Claude Code-style `config.json` schema into a single typed value that the rest of serf reads. It parses up to three tiers of config — global at `~/.config/serf/config.json`, project at `<git-root>/.serf/config.json`, and zero-or-more `--config <path>` files from the CLI — and merges them per Claude Code's documented precedence rules. SP1 owns the five top-level fields (`marketplaces`, `enabledPlugins`, `hooks`, `mcpServers`, `permissions`), validates them structurally, surfaces errors with file path + offending field, and exposes the merged result. SP1 does not interpret the values inside `hooks`, `mcpServers`, `marketplaces`, or `permissions` beyond the merge — downstream sub-projects (SP2/SP3/SP5/SP6) own that semantics.

## 2. Public API Surface

All new symbols live in package `agent`. Names follow the `LoadMCPConfigFile` / `MergeMCPConfigs` / `DiscoverMCPConfigs` triad in `agent/mcp_config.go`.

```go
// SerfConfig is the parsed contents of one config.json file. Each field is the
// raw JSON from the file; SP1 does not interpret inner shapes. Empty file or
// missing field yields a zero value for that field (nil map / nil slice).
type SerfConfig struct {
    // Marketplaces keyed by marketplace name. Value shape owned by SP3.
    Marketplaces map[string]json.RawMessage

    // EnabledPlugins keyed by "plugin@marketplace". Value shape owned by SP4.
    EnabledPlugins map[string]json.RawMessage

    // Hooks keyed by event name (e.g. "PreToolUse"). Value is an ordered array
    // of hook entries. SP5 owns the array-element shape.
    Hooks map[string][]json.RawMessage

    // MCPServers keyed by server name. SP6 owns the value shape; SP1 leaves
    // it as RawMessage so downstream parsing/validation/expansion can reuse
    // the existing mcpServerJSON path.
    MCPServers map[string]json.RawMessage

    // Permissions is a single object with allow/deny/defaultMode. SP2 owns
    // the rule strings and defaultMode values.
    Permissions PermissionsConfig

    // Sources records every file that contributed to this value, in merge
    // order (lowest-precedence first). Used in error messages and in
    // ConfigChange events later.
    Sources []ConfigSource
}

// PermissionsConfig is the only field SP1 destructures, because SP1 owns its
// merge rule (allow/deny concatenate; defaultMode scalar-overwrites).
type PermissionsConfig struct {
    Allow       []string
    Deny        []string
    DefaultMode string // "" means "unset"; SP2 picks a default if empty.
}

// ConfigSource is one file that contributed to a SerfConfig.
type ConfigSource struct {
    Path string     // absolute path
    Tier ConfigTier // Global, Project, or CLI
    // CLIIndex is the position in the --config list (0-based) for Tier==CLI.
    // -1 for non-CLI tiers. Used to settle hook ordering ties.
    CLIIndex int
}

type ConfigTier int

const (
    TierGlobal ConfigTier = iota
    TierProject
    TierCLI
)

// LoadSerfConfigFile parses one config.json. Missing file is not an error
// (returns zero SerfConfig, nil). Malformed JSON or a structurally invalid
// field is an error annotated with path + offending field.
func LoadSerfConfigFile(path string) (SerfConfig, error)

// MergeSerfConfigs merges layers low-precedence-first. Returns a single
// SerfConfig whose Sources slice is the union, in order. See §4 for rules.
func MergeSerfConfigs(layers ...SerfConfig) SerfConfig

// DiscoverSerfConfig loads global -> project -> each --config in order and
// returns the merged result. Mirrors DiscoverMCPConfigs. cliPaths is the
// repeated --config flag in CLI order. env supplies cwd and git-root lookup.
func DiscoverSerfConfig(env ExecutionEnvironment, cliPaths []string) (SerfConfig, error)

// globalSerfConfigPath mirrors globalMCPConfigPath. Unexported.
func globalSerfConfigPath() string
```

Validation errors are plain `error` values; their text always starts with the offending file path. See §6.

## 3. File-Format Details

The schema is identical at every tier. All fields are optional. An empty file `{}` is legal and produces a zero SerfConfig.

```json
{
  "marketplaces": {
    "anthropics": { "source": { "type": "github", "repo": "anthropics/claude-code-plugins" }, "autoUpdate": false }
  },
  "enabledPlugins": {
    "code-review@anthropics": true,
    "linter@anthropics":      { "version": "1.2.3" }
  },
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [ { "type": "command", "command": "guard.sh" } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "summarize.sh" } ] }
    ]
  },
  "mcpServers": {
    "github": { "command": "gh-mcp", "args": ["--token", "abc"] }
  },
  "permissions": {
    "allow":       [ "Bash(ls:*)", "Skill(*)" ],
    "deny":        [ "Bash(rm:-rf*)" ],
    "defaultMode": "default"
  }
}
```

Required structure (validation, not semantic):

- Top-level must be a JSON object; non-object is a hard error.
- `marketplaces`, `enabledPlugins`, `mcpServers`: must be JSON objects when present.
- `hooks`: must be a JSON object whose values are arrays; non-array values are a hard error (this is the one shape SP5 cares about that we can cheaply enforce here).
- `permissions`: must be a JSON object when present.
  - `permissions.allow`, `permissions.deny`: arrays of strings when present.
  - `permissions.defaultMode`: string when present. SP1 does not validate the value (SP2 owns the set).
- Unknown top-level keys: warn once to `stderr` and ignore. Same for unknown keys inside `permissions`. Both warnings name the file path.

Optional vs required summary:

| Field | Required? | Default |
|---|---|---|
| `marketplaces` | optional | `{}` |
| `enabledPlugins` | optional | `{}` |
| `hooks` | optional | `{}` |
| `mcpServers` | optional | `{}` |
| `permissions` | optional | `{Allow: nil, Deny: nil, DefaultMode: ""}` |
| `permissions.allow` | optional | `nil` |
| `permissions.deny` | optional | `nil` |
| `permissions.defaultMode` | optional | `""` |

## 4. Layered-Merge Algorithm

Layers arrive low-precedence-first: global → project → `--config A` → `--config B` → … . `MergeSerfConfigs` reduces them in a single pass. Rules per field:

**`hooks` — array concatenate.** For each event name, the merged array is `low[event] ++ high[event]` in layer order. Within a layer, the order from the file is preserved. This resolves the open question (§9.1).

**`mcpServers` — map replace by key.** For each key, the highest-precedence layer that defines it wins. Reusing `MergeMCPConfigs` semantics keeps SP6 aligned with `mcp.json` behavior.

**`marketplaces` — map replace by key.** Same as `mcpServers`. SP3 may further constrain (e.g. project tier requires trust before its keys count), but that gating happens after merge.

**`enabledPlugins` — map replace by key.** Same as `mcpServers`. SP4 interprets the value.

**`permissions.allow` — concatenate.** `low.allow ++ high.allow`, in layer order. Duplicates are preserved; SP2 deduplicates if it cares.

**`permissions.deny` — concatenate.** Same as `allow`.

**`permissions.defaultMode` — scalar overwrite.** The highest-precedence non-empty string wins. An explicit empty string in a higher tier does not clobber a lower tier's value (matches Claude Code's "unset = don't touch").

**`Sources` — concatenate in layer order.** The result records every file that contributed, lowest-precedence first.

### Worked Example

Global `~/.config/serf/config.json`:

```json
{
  "hooks": { "PreToolUse": [ {"matcher":"Bash","hooks":[{"type":"command","command":"g-pre.sh"}]} ] },
  "mcpServers": { "github": {"command":"gh-mcp"} },
  "permissions": { "allow": ["Bash(ls:*)"], "defaultMode": "default" }
}
```

Project `.serf/config.json`:

```json
{
  "hooks": { "PreToolUse": [ {"matcher":"Bash","hooks":[{"type":"command","command":"p-pre.sh"}]} ] },
  "mcpServers": { "github": {"command":"gh-mcp-fork"}, "jira": {"command":"jira-mcp"} },
  "permissions": { "allow": ["Skill(*)"], "deny": ["Bash(rm:*)"] }
}
```

CLI `--config local.json`:

```json
{
  "hooks": { "PreToolUse": [ {"hooks":[{"type":"command","command":"cli-pre.sh"}]} ] },
  "permissions": { "defaultMode": "acceptEdits" }
}
```

Merged result:

- `hooks.PreToolUse` = `[g-pre.sh, p-pre.sh, cli-pre.sh]` (concat in tier order)
- `mcpServers.github.command` = `"gh-mcp-fork"` (project replaced global)
- `mcpServers.jira` = present (added by project)
- `permissions.allow` = `["Bash(ls:*)", "Skill(*)"]` (concat)
- `permissions.deny` = `["Bash(rm:*)"]` (concat with empty global)
- `permissions.defaultMode` = `"acceptEdits"` (CLI overrode project's unset, which itself didn't override global's "default")

## 5. Validation Behavior

Validation runs inline in `LoadSerfConfigFile`. Errors fail fast — `DiscoverSerfConfig` returns the first error it hits and does not silently drop a malformed tier.

Validation rules and the errors they produce:

| Condition | Error text format |
|---|---|
| `os.ReadFile` failure other than `ErrNotExist` | `reading serf config <path>: <wrapped err>` |
| `json.Unmarshal` of top-level object fails | `parsing serf config <path>: <wrapped err>` |
| Top-level is not a JSON object | `serf config <path>: top-level must be a JSON object` |
| `hooks` is not an object | `serf config <path>: field "hooks": must be an object of event-name to array` |
| `hooks.<event>` value is not an array | `serf config <path>: field "hooks.<event>": must be an array` |
| `marketplaces` / `enabledPlugins` / `mcpServers` not an object | `serf config <path>: field "<name>": must be an object` |
| `permissions` not an object | `serf config <path>: field "permissions": must be an object` |
| `permissions.allow` / `permissions.deny` not an array of strings | `serf config <path>: field "permissions.<allow|deny>": must be an array of strings` |
| `permissions.defaultMode` not a string | `serf config <path>: field "permissions.defaultMode": must be a string` |

Missing file produces no error and no warning — matches the existing `mcp.json` "absence is fine" convention.

Unknown top-level keys and unknown keys inside `permissions` produce one warning to `stderr` per occurrence, prefixed `serf config <path>: ignoring unknown field "<name>"`. They do not abort the load.

`DiscoverSerfConfig` wraps any underlying error with the tier label, e.g. `--config <path>: serf config <path>: field "hooks.PreToolUse": must be an array`. The wrap is purely for the CLI-tier breadcrumb; the inner message already has the file path.

## 6. Error Contracts

`LoadSerfConfigFile`:

- Returns `(SerfConfig, error)`.
- `error == nil` iff the file was either absent or successfully validated.
- Absent file: returns zero `SerfConfig` and `nil` error.
- Permission denied, I/O error, malformed JSON, validation failure: returns zero `SerfConfig` and a non-nil error.

`MergeSerfConfigs`:

- Pure function. No errors. Order of `layers` is the only thing that matters.

`DiscoverSerfConfig`:

- Returns the merged `SerfConfig` plus an error.
- Fails fast on the first tier that errors. Tier ordering — global → project → each `--config` — is fixed.
- A missing global or project file is not an error. A missing `--config <path>` IS an error (the user pointed at it explicitly).

There is no "warn and continue" mode. A malformed config aborts session startup. Rationale: silent fallback would let typos disable hooks or permissions without anyone noticing.

## 7. Package and File Layout

New files in `agent/`:

- `agent/config.go` — types, `LoadSerfConfigFile`, `MergeSerfConfigs`, `DiscoverSerfConfig`, `globalSerfConfigPath`. Mirrors `agent/mcp_config.go` shape.
- `agent/config_test.go` — table-driven unit tests for parse, merge, and discovery (§8).
- `agent/testdata/config/<scenario>/` — JSON fixtures for the discovery tests. Each scenario directory contains the global, project, and CLI files needed for one row of the table.

No existing file is modified by SP1 itself. Wiring `DiscoverSerfConfig` into the CLI entry points (`cmd/serf/main.go`, `cmd/serf-tui/embedded.go`, `cmd/serf-hub/web.go`, `cmd/serfeval/main.go`) is SP8's job — SP1 ships only the loader and its tests.

## 8. Testing Strategy

TDD: write all of §8 first, then implement §2–6 until tests pass. No mocked filesystem; use `t.TempDir()`. Fixtures are real JSON files written by the test or pre-seeded under `agent/testdata/config/`.

### 8.1 `LoadSerfConfigFile`

Table-driven `TestLoadSerfConfigFile`. Each row: name, file body (or "absent"), expected `SerfConfig` shape, expected error substring (`""` for none).

| # | Case | File body | Expect |
|---|---|---|---|
| 1 | Absent file | (not written) | zero SerfConfig, no error |
| 2 | Empty object | `{}` | zero SerfConfig, no error |
| 3 | All five fields populated | full example from §3 | all fields populated; `Sources[0].Path` is the file |
| 4 | Hooks present, mcpServers absent | `{"hooks":{"PreToolUse":[...]}}` | hooks populated; `MCPServers == nil` |
| 5 | Permissions partial (allow only) | `{"permissions":{"allow":["Bash(ls:*)"]}}` | `Allow == ["Bash(ls:*)"]`, `Deny == nil`, `DefaultMode == ""` |
| 6 | Hook event with empty array | `{"hooks":{"PreToolUse":[]}}` | `Hooks["PreToolUse"] != nil && len == 0` |
| 7 | Top-level not an object | `[]` | error contains `top-level must be a JSON object` |
| 8 | Malformed JSON | `{` | error contains `parsing serf config` |
| 9 | `hooks` not an object | `{"hooks":[]}` | error contains `field "hooks"` |
| 10 | `hooks.<event>` not array | `{"hooks":{"PreToolUse":{}}}` | error contains `field "hooks.PreToolUse"` |
| 11 | `marketplaces` not object | `{"marketplaces":[]}` | error contains `field "marketplaces"` |
| 12 | `enabledPlugins` not object | `{"enabledPlugins":[]}` | error contains `field "enabledPlugins"` |
| 13 | `mcpServers` not object | `{"mcpServers":[]}` | error contains `field "mcpServers"` |
| 14 | `permissions` not object | `{"permissions":[]}` | error contains `field "permissions"` |
| 15 | `permissions.allow` not array | `{"permissions":{"allow":"x"}}` | error contains `field "permissions.allow"` |
| 16 | `permissions.allow` array of non-strings | `{"permissions":{"allow":[1]}}` | error contains `field "permissions.allow"` |
| 17 | `permissions.deny` not array | `{"permissions":{"deny":"x"}}` | error contains `field "permissions.deny"` |
| 18 | `permissions.defaultMode` not string | `{"permissions":{"defaultMode":42}}` | error contains `field "permissions.defaultMode"` |
| 19 | Unknown top-level field | `{"themes":{}}` | no error; one stderr warning containing `unknown field "themes"` |
| 20 | Unknown `permissions` subfield | `{"permissions":{"foo":1}}` | no error; one stderr warning containing `unknown field "permissions.foo"` |
| 21 | Permission denied on file | unreadable file via 0000 mode | error contains `reading serf config` |
| 22 | All five top-level fields error message names the path | `<tmp>/config.json` malformed | error text starts with `<tmp>/config.json` (suffix-match the full path) |

Rows 19–20 capture stderr via a swapped logger or a small `bytes.Buffer` sink. If serf already standardizes on a logger, route warnings through it; otherwise `log.New(os.Stderr, ...)` and inject the writer for tests.

### 8.2 `MergeSerfConfigs`

Table-driven `TestMergeSerfConfigs`. Pure-function tests, no filesystem.

| # | Case | Inputs | Expect |
|---|---|---|---|
| 1 | Zero layers | `()` | zero SerfConfig |
| 2 | One layer | global with all fields | identical to input |
| 3 | Hooks concat in layer order | global has `[A]`, project has `[B]`, CLI has `[C]` for `PreToolUse` | `[A,B,C]` |
| 4 | Hooks per-event independence | global `PreToolUse:[A]`, project `Stop:[B]` | both keys present, no cross-contamination |
| 5 | mcpServers replace-by-key | global `github:{...g}`, project `github:{...p}` | `github` equals project value |
| 6 | mcpServers add-by-key | global `github`, project `jira` | both present |
| 7 | marketplaces replace-by-key | symmetric to #5 | project value wins |
| 8 | enabledPlugins replace-by-key | symmetric to #5 | project value wins |
| 9 | permissions.allow concat | global `[a]`, project `[b]`, CLI `[c]` | `[a,b,c]` |
| 10 | permissions.deny concat | symmetric to #9 | `[a,b,c]` |
| 11 | permissions.defaultMode scalar overwrite | global `"default"`, project `""`, CLI `"acceptEdits"` | `"acceptEdits"` |
| 12 | permissions.defaultMode lower preserved when higher empty | global `"default"`, project `""`, CLI `""` | `"default"` |
| 13 | Sources concatenate in order | three layers with distinct `Sources[0].Path` | merged `Sources` has all three, lowest first |
| 14 | Duplicate allow entries preserved | global `["x"]`, project `["x"]` | `["x","x"]` (SP2 dedupes, not SP1) |

### 8.3 `DiscoverSerfConfig`

Integration-style but still hermetic — every path lives under `t.TempDir()`. Uses a `LocalExecutionEnvironment` rooted at a synthetic git repo (`git init` via `os/exec`; `t.Skip` if `git` is absent, matching the SP3/SP4 fixture rule).

To redirect global lookup, `globalSerfConfigPath` reads `XDG_CONFIG_HOME` — tests set `t.Setenv("XDG_CONFIG_HOME", tmp)` and create `<tmp>/serf/config.json`.

| # | Case | Setup | Expect |
|---|---|---|---|
| 1 | All three tiers absent | no files written | zero SerfConfig, no error |
| 2 | Only global present | `<xdg>/serf/config.json` populated | merged = global |
| 3 | Only project present (inside git repo) | `<repo>/.serf/config.json` | merged = project |
| 4 | Only `--config` present | one CLI path passed | merged = CLI |
| 5 | All three tiers present, hooks concat | distinct hook entries per tier | merged hooks = `[global, project, cli]` for the event |
| 6 | Multiple `--config` in order | two CLI paths | merged hooks = `[g, p, cli0, cli1]` |
| 7 | Project file ignored when cwd not in a git repo | `.serf/config.json` in non-repo dir | project tier skipped, no error |
| 8 | Malformed global file aborts | global is `{` | error contains global path |
| 9 | Malformed project file aborts | project is `{` | error contains project path |
| 10 | Malformed CLI file aborts | CLI path is `{` | error contains `--config <path>` and the file path |
| 11 | Missing CLI file is an error | `--config /nonexistent` | non-nil error |
| 12 | Sources order reflects merge order | all tiers populated | `Sources` is `[global, project, cli0, cli1]` |
| 13 | Project tier discovered via git root, not cwd | cwd is `<repo>/sub/dir`, config at `<repo>/.serf/config.json` | project tier merged |

### 8.4 Fixtures

`agent/testdata/config/` holds reusable JSON blobs that several tests share:

- `testdata/config/full.json` — the §3 example.
- `testdata/config/hooks_only.json` — only `hooks.PreToolUse` with one entry.
- `testdata/config/permissions_only.json` — only `permissions`.
- `testdata/config/malformed.json` — literal `{` to trigger the parse-error path.

Tests that need per-scenario combinations write inline with `os.WriteFile`. The shared fixtures exist so happy-path tests can read by path and stay readable.

### 8.5 Coverage gate

Every exported function in §2 has at least one direct test row. Every error path in §5 has a row in §8.1. Every merge rule in §4 has a row in §8.2. `go test ./agent/... -run SerfConfig` is green.

## 9. Open Questions Settled Here

### 9.1 Hook ordering across config tiers

**Decision.** Hooks fire in tier order, lowest precedence first: global → project → `--config` (in CLI order, left to right) → plugin-provided. Within a tier, hooks fire in the order they appear in the source file. `MergeSerfConfigs` therefore concatenates per-event arrays in that same order, and SP5 walks the merged array head-to-tail.

**Rationale.** Three options were considered:

1. **Highest precedence first** (CLI → project → global). Models "the most local intent runs first." Rejected: surprising for `PostToolUse`-style hooks where global "always log" wrappers expect to see the inner per-project hook's output already on disk.
2. **Lowest precedence first** (chosen). Matches the mental model that more-local tiers *extend* less-local ones rather than *preempting* them. Also matches the array-concat semantics of `permissions.allow` — both fields "add to" the lower tier, they do not reorder it.
3. **Sort by matcher specificity.** Rejected: requires SP5-level understanding of matcher syntax (dual-mode regex/exact), which SP1 explicitly does not own.

Plugin-provided hooks append at the very end so plugin authors can rely on user config running first. This places user override potential ahead of plugin behavior — a hook that wants to short-circuit plugin behavior with `"permissionDecision":"deny"` only has to be declared in the user's config.

**Tie-breaking inside a tier:** source-file order. Within a single file, JSON object iteration over events is not order-sensitive (different events run independently), but the array under a single event preserves its on-disk order. Go's `encoding/json` preserves array order for `json.RawMessage` slices, so no extra work is needed.

### 9.2 Dependencies on other sub-specs (NOT resolved here)

The following are flagged so the implementing session does not waste cycles guessing. Each gets resolved in the sub-spec it's tagged to.

- **SP2 — `permissions.defaultMode` value set.** SP1 stores it as a string. SP2 validates it against `default | acceptEdits | bypassPermissions | plan | …`.
- **SP3 — marketplace value shape.** SP1 keeps `marketplaces` as `map[string]json.RawMessage`. SP3 parses the `source`, `autoUpdate`, trust prompt rules.
- **SP4 — `enabledPlugins` value semantics.** SP1 keeps it as `RawMessage` so `true` and `{"version":"..."}` are both legal at this layer. SP4 settles whether `true` is permitted long-term.
- **SP5 — hook entry shape.** SP1 enforces only that `hooks.<event>` is an array. SP5 validates `{matcher, hooks: [{type, command, args, async, …}]}` and runs the merged array head-to-tail per §9.1.
- **SP6 — `mcpServers` value shape and expansion.** SP1 keeps entries as `RawMessage`. SP6 reuses the existing `mcpServerJSON` decode plus `streamable-http` aliasing and `${CLAUDE_PROJECT_DIR}` / `${user_config.*}` expansion. The pre-existing `MergeMCPConfigs` continues to handle inline `--mcp` and `--mcp-config` overrides; SP1's contribution feeds in as one earlier layer.
- **SP8 — wiring.** SP8 calls `DiscoverSerfConfig` from each entry point and threads the result through session init. SP1 must export the function with a stable signature; SP8 owns where it gets called.
