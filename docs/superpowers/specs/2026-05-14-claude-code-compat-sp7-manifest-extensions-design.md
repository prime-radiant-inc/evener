# SP7 — Plugin Manifest Extensions (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
Sibling specs: SP1 (`...-sp1-config-loader-design.md`)

## 1. Goal

SP7 brings four A-tier plugin manifest features into serf:

1. **`userConfig`** — declared in `plugin.json`, prompted on plugin enable, stored (split between plain config and a secure store), and exposed back to plugin code through two channels: `${user_config.KEY}` string substitution and `CLAUDE_PLUGIN_OPTION_<KEY>` environment variables.
2. **`bin/`** — each enabled plugin's `bin/` directory is prepended to the Bash tool's `PATH`, scoped per `ExecCommand` invocation. No global mutation.
3. **Plugin-root `settings.json`** — honored for the `agent` key (sets a plugin-supplied subagent as the session's main thread). All other keys log a warn-once.
4. **`skills` custom paths** — manifest-declared paths load additively alongside the default `skills/`.

SP7 also implements the **warn-once-per-plugin-per-field** mechanism used by this sub-project and by the parent design for unsupported manifest fields and unsupported plugin-root `settings.json` keys.

SP7 **provides** the resolved per-plugin user-config values and the substitution/env helpers. SP6 (MCP) and SP5 (hooks) **consume** them at spawn time. SP7 does not modify those spawn sites.

### Non-goals

- Implementing `userConfig` for `channels` (`channels` itself is out of scope; SP7 only does the top-level `userConfig`).
- Wiring `${user_config.KEY}` substitution into MCP/LSP/hook configs. That is SP5/SP6.
- Re-implementing the plugin loader. `LoadPlugin` in `agent/plugin.go` keeps its current shape; SP7 adds three new fields to `LoadedPlugin` and a small lookup function.
- Setting up the `agent` subagent execution path. The default-agent mechanism already exists in serf; SP7 just routes the manifest value into it.
- OS keychain implementation under a build tag for first delivery. The interface is defined and stubbed; the fallback file path is the only path required to pass tests. A follow-up adds the keychain backend.

## 2. Public API Surface

All new symbols live in package `agent`, except the secure-store backends which live in `agent/internal/securestore/`. Naming follows the `LoadX` / `DiscoverX` style already in use.

```go
// PluginManifest gains four new fields. None are required; absence is benign.
type PluginManifest struct {
    // ...existing fields unchanged...

    // Skills is the manifest "skills" field. May be string, []string, or
    // omitted. Paths are relative to the plugin root and start with "./".
    // Loading is additive: the default "skills/" directory is always scanned.
    Skills json.RawMessage `json:"skills,omitempty"`

    // UserConfig declares the keys prompted on enable and substituted at
    // runtime. Key order is preserved through json.RawMessage decoding so the
    // prompt UX can render fields in declaration order.
    UserConfig json.RawMessage `json:"userConfig,omitempty"`

    // OutputStyles, LSPServers, Experimental, Channels, Dependencies are
    // recognized for the warn-once mechanism only — SP7 does not interpret
    // them. Capturing them as RawMessage lets validation report
    // "unsupported field" without parsing.
    OutputStyles json.RawMessage `json:"outputStyles,omitempty"`
    LSPServers   json.RawMessage `json:"lspServers,omitempty"`
    Experimental json.RawMessage `json:"experimental,omitempty"`
    Channels     json.RawMessage `json:"channels,omitempty"`
    Dependencies json.RawMessage `json:"dependencies,omitempty"`
}

// UserConfigOption is one declared user-config field after parsing.
type UserConfigOption struct {
    Key         string   // map key from manifest, lower_snake_case enforced
    Type        UserConfigType
    Title       string   // required
    Description string   // required
    Sensitive   bool
    Required    bool
    Default     any      // type-dependent; nil when unset
    Multiple    bool     // string-type only
    Min         *float64 // number-type only
    Max         *float64 // number-type only
}

type UserConfigType string

const (
    UserConfigString    UserConfigType = "string"
    UserConfigNumber    UserConfigType = "number"
    UserConfigBoolean   UserConfigType = "boolean"
    UserConfigDirectory UserConfigType = "directory"
    UserConfigFile      UserConfigType = "file"
)

// ParseUserConfig decodes the manifest's userConfig blob into an ordered
// slice. Empty input returns (nil, nil). Validation rules per §3.
func ParseUserConfig(raw json.RawMessage) ([]UserConfigOption, error)

// LoadedPlugin gains four new fields. Existing fields unchanged.
type LoadedPlugin struct {
    // ...existing fields...

    UserConfigOptions []UserConfigOption // empty if manifest omits userConfig
    DefaultAgent      string             // from plugin-root settings.json; "" if unset
    BinDir            string             // absolute path to <root>/bin if it exists and is a dir; "" otherwise
    Warnings          []PluginWarning    // unsupported fields/keys, deduplicated per §11
}

// PluginWarning is one diagnostic emitted at load time. It is *not* an error.
// Captured on LoadedPlugin so the session can print it once at startup and
// also surface it in `serf plugin list --json`.
type PluginWarning struct {
    Field   string // e.g. "outputStyles", "settings.json:subagentStatusLine"
    Message string
}

// ResolvedUserConfig is one plugin's effective user-config values after
// applying defaults, persisted plain values, and secure-store lookups.
// Lookup is the single accessor SP5/SP6 use.
type ResolvedUserConfig struct {
    PluginID string                 // "plugin@marketplace" or just "plugin" for ad-hoc
    values   map[string]string      // primitives stringified; multiples joined per §6
    options  map[string]UserConfigOption
}

// Lookup returns the resolved value for key. Returns (value, true) on hit.
// Returns ("", false) when the key was never declared. Required-but-missing
// keys still return ("", true) so substitution leaves a clear empty hole
// rather than the literal "${user_config.KEY}".
func (r *ResolvedUserConfig) Lookup(key string) (string, bool)

// ExpandUserConfig substitutes ${user_config.KEY} occurrences in s using r.
// Unknown keys (never declared) are left untouched, with a stderr warning
// the first time a given (plugin, key) pair is hit. Sensitive values are
// substituted but the warning text scrubs them.
func ExpandUserConfig(s string, r *ResolvedUserConfig) string

// UserConfigEnvVars returns a map of CLAUDE_PLUGIN_OPTION_<KEY>=<value>
// pairs to be merged into a subprocess's environment by the caller. Keys
// are uppercased and non-[A-Z0-9_] characters become '_'.
func UserConfigEnvVars(r *ResolvedUserConfig) map[string]string

// PluginConfigStore is the persistence layer for plain (non-sensitive)
// user-config values. It reads and writes the pluginConfigs section of
// the global serf config.json. Project-tier overrides are out of scope for
// SP7; user-config is a per-user secret-or-preference scope.
type PluginConfigStore interface {
    // Load returns the persisted plain values for pluginID, or an empty
    // map if absent. Reading must tolerate the file being missing.
    Load(pluginID string) (map[string]any, error)

    // Save replaces the persisted plain values for pluginID. Writes
    // atomically via tmp-file + rename.
    Save(pluginID string, values map[string]any) error
}

// SecureStore is the persistence layer for sensitive values. Implementations:
//   - KeychainStore: macOS/Linux/Windows OS keychain (added under build tag
//     in a follow-up; not delivered in SP7's first cut).
//   - FileStore:     ~/.config/serf/credentials.json mode 0600. Default
//                     for SP7 v1.
type SecureStore interface {
    Get(pluginID, key string) (string, bool, error)
    Set(pluginID, key, value string) error
    Delete(pluginID, key string) error
}

// NewSecureStore returns the platform-appropriate SecureStore. SP7 v1
// returns FileStore unconditionally. The selection logic is the only
// place that needs to change when the keychain backend lands.
func NewSecureStore() (SecureStore, error)

// PromptForUserConfig collects values for opts via the given prompter and
// persists them. Plain values go to plainStore; sensitive values go to
// secureStore. Returns the resolved values for immediate use.
func PromptForUserConfig(
    prompter UserConfigPrompter,
    pluginID string,
    opts []UserConfigOption,
    plainStore PluginConfigStore,
    secureStore SecureStore,
) (*ResolvedUserConfig, error)

// UserConfigPrompter is the surface-specific UX. CLI, serf-tui, and
// serf-hub each ship their own implementation. A non-interactive
// prompter (NonInteractivePrompter) reads from --plugin-option flags.
type UserConfigPrompter interface {
    // Prompt is called once per option in declaration order. The prompter
    // returns the raw user-entered string. Empty string means "user
    // accepted the default" if a default exists; otherwise it is treated
    // as no value (required-check fires).
    Prompt(opt UserConfigOption) (string, error)
}

// ResolveUserConfig assembles a ResolvedUserConfig from persisted state
// without prompting. Used at session start, after enable-time prompting
// has already happened (or to detect that it hasn't and a prompt is
// needed). Required-but-missing keys are reported as MissingRequiredKeys.
func ResolveUserConfig(
    pluginID string,
    opts []UserConfigOption,
    plainStore PluginConfigStore,
    secureStore SecureStore,
) (resolved *ResolvedUserConfig, missing []string, err error)

// PluginBinPATH returns the PATH-string prefix to prepend for the Bash
// tool's exec env, given a set of plugins. Each plugin's BinDir is
// included if non-empty. Order matches the order of plugins in the input.
func PluginBinPATH(plugins []LoadedPlugin) string

// SerfConfig (defined in SP1) gains one field:
//
//   PluginConfigs map[string]PluginConfig `json:"pluginConfigs,omitempty"`
//
// where PluginConfig is { Options map[string]any `json:"options"` }.
// SP7 is the first owner of this field; SP1's loader keeps it as
// json.RawMessage and SP7 adds the typed accessor. The merge rule mirrors
// mcpServers (map replace by key at the plugin-id level).
```

`UserConfigPrompter` is intentionally one method. Multi-field UX (forms in serf-hub) builds a form from the option list before calling `Prompt` per field, or — for richer UIs — implements `Prompt` as a no-op and runs its own loop, then constructs the `ResolvedUserConfig` directly. The interface stays small so non-interactive callers (`NonInteractivePrompter`) are trivial.

## 3. `userConfig` Schema

Every `userConfig` entry is a JSON object keyed by an identifier. The identifier must match `^[a-z][a-z0-9_]*$` (lower_snake_case). This is stricter than what Claude Code accepts ("valid identifiers"), but it gives a deterministic mapping to `${user_config.KEY}`, `CLAUDE_PLUGIN_OPTION_<KEY>`, and keychain paths without case ambiguity.

### 3.1 Per-field shape

```json
{
  "userConfig": {
    "api_endpoint": {
      "type": "string",
      "title": "API endpoint",
      "description": "Your team's API endpoint",
      "required": true,
      "default": "https://api.example.com"
    },
    "api_token": {
      "type": "string",
      "title": "API token",
      "description": "API authentication token",
      "sensitive": true,
      "required": true
    },
    "request_timeout_ms": {
      "type": "number",
      "title": "Request timeout (ms)",
      "description": "Per-call timeout in milliseconds",
      "default": 5000,
      "min": 100,
      "max": 60000
    },
    "verbose": {
      "type": "boolean",
      "title": "Verbose logging",
      "description": "Emit debug-level logs",
      "default": false
    },
    "workspace": {
      "type": "directory",
      "title": "Workspace path",
      "description": "Directory the plugin operates on",
      "required": true
    },
    "credentials_file": {
      "type": "file",
      "title": "Credentials file",
      "description": "Path to a JSON credentials file",
      "sensitive": false
    },
    "allowed_hosts": {
      "type": "string",
      "title": "Allowed hosts",
      "description": "One host per entry",
      "multiple": true,
      "default": ["api.example.com"]
    }
  }
}
```

### 3.2 Per-type field rules

| Type | `default` allowed | `multiple` allowed | `min`/`max` allowed | Stringification for substitution |
|---|---|---|---|---|
| `string` | string (or array of strings if `multiple`) | yes | no | the value, or `multiple` joined by ASCII space |
| `number` | number (no `multiple`) | no | yes | Go `strconv.FormatFloat(v, 'f', -1, 64)`; integer values render without decimal |
| `boolean` | boolean | no | no | `"true"` or `"false"` |
| `directory` | string | no | no | the absolute path, expanded for `~/` |
| `file` | string | no | no | the absolute path, expanded for `~/` |

Rules that hold across all types:

- `title` and `description` are required and must be non-empty strings. SP1 already errors when the file doesn't parse; SP7 errors when these fields are missing or wrong-typed at the `userConfig` parse site.
- `sensitive` is `false` by default. When `true`, the value is *only* stored in the secure store. It does not appear in `config.json`.
- `required` is `false` by default. A required key with no value (after defaults) blocks plugin enable in interactive surfaces; in non-interactive mode, it is a startup error unless `--plugin-option <plugin>.<key>=<value>` provides the value.
- `default` is type-checked against `type`. A `boolean` field with `"default": "yes"` errors at parse time.
- Unknown per-option fields warn-once at parse time (the warning is captured on `LoadedPlugin.Warnings`, not aborting the load).

### 3.3 Parse errors

`ParseUserConfig` returns errors using the same shape SP1 uses: `userConfig.<key>: <field>: <reason>`. Concrete examples:

| Condition | Error text |
|---|---|
| Top-level `userConfig` not an object | `userConfig: must be an object` |
| Key contains uppercase letters | `userConfig.<key>: identifier must match [a-z][a-z0-9_]*` |
| Missing `type` | `userConfig.<key>: field "type": required` |
| Unknown `type` | `userConfig.<key>: field "type": must be one of string, number, boolean, directory, file` |
| `multiple: true` on non-string type | `userConfig.<key>: field "multiple": only valid when type is "string"` |
| `min`/`max` on non-number type | `userConfig.<key>: field "min": only valid when type is "number"` |
| `default` type-mismatch | `userConfig.<key>: field "default": expected <type>, got <observed>` |
| Missing `title` or `description` | `userConfig.<key>: field "<title|description>": required` |

## 4. Prompt Flow

### 4.1 When the prompt fires

Three triggers, in priority order. The first that applies for a given (plugin, key) wins:

1. **`serf plugin install` and `serf plugin enable` (SP4 commands).** Interactive prompt for every declared key whose value is not yet persisted. This is the primary path.
2. **Session start, `ResolveUserConfig` reports `missing` for a required key, *and* the surface is interactive.** The surface offers an inline prompt before completing session init. Skipping leaves the key empty and produces a startup warning.
3. **Session start, non-interactive surface.** A missing required key is a hard error printed to `stderr` with the suggestion `re-run with --plugin-option <plugin>.<key>=<value> or run \`serf plugin enable <plugin>\``. Optional keys silently fall through to their defaults.

The trigger picks the prompt source. Once values are persisted, subsequent sessions skip prompting entirely.

### 4.2 UX per surface

| Surface | Prompter implementation | Mode |
|---|---|---|
| `serf` CLI (interactive) | `CLIPrompter` — line-oriented `bufio.Scanner` on stdin, `golang.org/x/term.ReadPassword` for `sensitive: true` | inline, blocking |
| `serf-tui` | `TUIPrompter` — modal form rendered by the existing tui-form widget set | inline, blocking, with masked input for sensitive fields |
| `serf-hub` web UI | `HubPrompter` — HTTP form bound to the plugin-enable workflow; submits to a per-plugin endpoint that calls `PromptForUserConfig` server-side with a static `MapPrompter` wrapping the form payload | out-of-band; the session waits for the user to complete the form before plugin init completes |
| Non-interactive (`-p`, piped input, scripts) | `NonInteractivePrompter` — reads from a pre-parsed `map[plugin]map[key]string` populated from `--plugin-option <plugin>.<key>=<value>` flags | no prompts; missing required keys error out |

`--plugin-option` may repeat. The key form is `<plugin>.<option>=<value>` where `<plugin>` matches an `enabledPlugins` key (`<name>` or `<name>@<marketplace>`). Mismatched plugin names produce a single warning, not an error, so users can re-use a script across machines with different marketplace names.

### 4.3 Cancel and abort

If the prompter returns an error wrapping `ErrPromptCanceled` (Ctrl-C in CLI, modal-close in TUI, navigation-away in Hub), `PromptForUserConfig` returns the error without persisting anything partial. The caller decides whether to skip the plugin (interactive `serf plugin enable` reports the cancel and exits non-zero) or abort startup (session-init treats it as fatal).

### 4.4 Re-prompt on schema change

When a plugin update adds a new required `userConfig` key, the persisted state lacks it, so `ResolveUserConfig` reports it as missing on next session start. The trigger sequence in §4.1 then re-fires. No special migration code is needed.

A removed key keeps its persisted value indefinitely. Cleanup is out of scope for SP7 — `serf plugin uninstall` can call `PluginConfigStore.Save(pluginID, nil)` and `SecureStore.Delete` for known keys, but the housekeeping itself is SP4's job to schedule.

## 5. Storage Layout

### 5.1 Plain (non-sensitive) values

Plain values live in the global serf config at `~/.config/serf/config.json`, under a new top-level field `pluginConfigs`:

```json
{
  "pluginConfigs": {
    "code-review@anthropics": {
      "options": {
        "api_endpoint":       "https://api.example.com",
        "request_timeout_ms": 5000,
        "verbose":            false,
        "allowed_hosts":      ["api.example.com", "ci.example.com"]
      }
    },
    "linter@anthropics": {
      "options": { "workspace": "/Users/jesse/projects/foo" }
    }
  }
}
```

Keys at the top level of `pluginConfigs` are plugin IDs. The ID is `<name>@<marketplace>` when the plugin came from a marketplace and just `<name>` when it came from `--plugin-dir`. The merge rule mirrors `mcpServers`: replace by key at the plugin-id level. This makes it safe for a user to manually edit one plugin's options without disturbing another's.

Sensitive keys never appear here. `PluginConfigStore.Save` filters them out before serializing.

### 5.2 Sensitive values

Sensitive values live in one of two backends, picked by `NewSecureStore`:

- **`KeychainStore`** (future, behind a build tag). Service name is `serf`. Account name is `<pluginID>/<key>`. Example: `serf` / `code-review@anthropics/api_token`.
- **`FileStore`** (SP7 v1). Path is `~/.config/serf/credentials.json`, mode `0600`. Format:

```json
{
  "credentials": {
    "code-review@anthropics": {
      "api_token": "ghp_xxxxxxxxxxxxxxxxxxxx"
    },
    "linter@anthropics": {}
  }
}
```

The file is created lazily on the first `Set`. Reads when the file is missing return `("", false, nil)` — absence is not an error. Writes go through a `<path>.tmp` + `rename` to keep the file consistent under crash. The directory is `MkdirAll`-ed with mode `0700`; if it already exists with a wider mode, SP7 leaves it alone and logs once.

### 5.3 Plugin-ID normalization

A plugin's `pluginID` is the string the user typed into `enabledPlugins`. It is *not* re-derived from `LoadedPlugin.Manifest.Name`, because two marketplaces can ship plugins with the same `name`. The session passes the ID into `ResolveUserConfig` alongside the loaded plugin.

For keychain account names and config keys, the ID is taken verbatim. The only normalization is for environment-variable names (see §7).

## 6. Substitution Interface

### 6.1 The resolver

```go
resolved, missing, err := ResolveUserConfig(pluginID, opts, plainStore, secureStore)
```

The resolver walks `opts` in declaration order. For each option:

1. Look up the persisted plain value (or sensitive value, for `sensitive: true`).
2. If absent, apply `default`.
3. If still absent, record the key in `missing`.

The resulting `ResolvedUserConfig` holds the stringified value per §3.2's table. Multi-valued string options are joined by a single ASCII space, which matches the convention plugin authors expect when shelling out (`some-tool --hosts a.example.com b.example.com`).

`Lookup` returns `("", false)` only when the key was never declared. A declared-but-empty key returns `("", true)` so substitution can replace it cleanly.

### 6.2 ExpandUserConfig

`ExpandUserConfig(s string, r *ResolvedUserConfig) string` walks `s` and replaces every `${user_config.<key>}` token with the resolved value. The pattern is `\$\{user_config\.([a-z][a-z0-9_]*)\}` — anchored to the same identifier shape `ParseUserConfig` enforces. Unknown keys (never declared) leave the literal token in place and emit a one-time stderr warning per `(pluginID, key)` pair.

`ExpandUserConfig` does no shell quoting. Callers responsible for shell-context substitution must escape after substitution, just like they do for `${CLAUDE_PLUGIN_ROOT}`.

### 6.3 Caching

`ResolveUserConfig` is cheap (one config read, one secure-store read per sensitive key) but is called per plugin per session. SP7 caches results per `(pluginID, generation)` where `generation` increments on any `PromptForUserConfig` write or `ConfigChange` event. The cache lives on the `Session` value, not on package-level state, so per-session isolation in tests is automatic.

### 6.4 Error semantics

`Lookup` does not return an error — by the time `ResolvedUserConfig` is constructed, errors have either aborted startup or been routed to `missing`. `ExpandUserConfig` likewise never errors; it cannot fail in a way the caller could meaningfully recover from at the substitution site.

`PromptForUserConfig` returns errors for: prompter failure (`ErrPromptCanceled` or wrapped underlying error), validation failure on a typed value (e.g. `min`/`max` violation), secure-store write failure (keychain unavailable AND fallback file unwritable — see §12), and plain-store write failure.

## 7. `CLAUDE_PLUGIN_OPTION_<KEY>` Environment Injection

### 7.1 Naming

For each resolved key, the env-var name is `CLAUDE_PLUGIN_OPTION_<KEY_UPPER>`, where `<KEY_UPPER>` is the manifest key uppercased. Because identifiers are already restricted to `[a-z][a-z0-9_]*` (§3), uppercasing produces a valid env-var name with no further sanitization needed. If a future Claude Code spec change relaxes the identifier rule to allow `-` or `.`, replace those characters with `_` at this boundary.

### 7.2 Where they appear

`UserConfigEnvVars(r)` produces the full map. SP7 itself does not inject this map anywhere. It is the consumer's responsibility:

- **MCP server subprocesses** — SP6 merges the map into the spawn `env` for each plugin-owned MCP server. The merge is additive; serf-set vars (`CLAUDE_PROJECT_DIR`, `CLAUDE_PLUGIN_ROOT`, `CLAUDE_PLUGIN_DATA`) win on collisions.
- **`command`-type hook subprocesses** — SP5 merges the map into the env passed to `ExecCommand` for each plugin-owned hook.
- **`agent`-type hooks** — SP5 includes them in the env exposed to the subagent's tool-call subprocesses.

SP7 ships the helper and the tests that verify the helper's output. SP6 and SP5 ship the wiring tests that verify the helper's output reaches the subprocess.

### 7.3 Sensitive values in env vars

Sensitive values flow into env vars unchanged. This matches Claude Code's behavior — a plugin needs access to its own secrets at runtime, and the env-var hop is the safest channel (it does not survive child-of-child unless re-exported, and serf does not log spawn envs).

What serf does *not* do: place sensitive values in `args` of a spawned process (visible in `ps`), or in any file other than the credentials file. The `CLAUDE_PLUGIN_OPTION_*` channel is the only egress.

## 8. `bin/` PATH Injection

### 8.1 Mechanism

When `LoadPlugin` runs, it stats `<root>/bin`. If the directory exists, `LoadedPlugin.BinDir` is its absolute path. Otherwise the field is empty.

The shell tool registration in `agent/session.go` (the `"shell"` registration at lines 2930–2942) gains a step before `env.ExecCommand`:

```go
extraEnv := map[string]string{}
if pluginPATH := PluginBinPATH(s.plugins); pluginPATH != "" {
    extraEnv["PATH"] = pluginPATH + string(os.PathListSeparator) + os.Getenv("PATH")
}
res, err := env.ExecCommand(ctx, cmd, timeout, "", extraEnv)
```

`filteredEnvWithPolicy` already honors caller-supplied `PATH` in `extra` (it appends to the inherited env in `EnvPolicyDefault`, which means the last `PATH=...` line wins for a Unix shell). `injectLocalVenvPath` runs after and only modifies a `PATH=` line, so its venv prefix is layered on top of the plugin prefix.

`PluginBinPATH` joins existing-only directories with `os.PathListSeparator`. Plugins are walked in load order. No deduplication beyond filesystem-identity; plugins with overlapping bin dirs are an authoring error.

### 8.2 Scoping

The injection touches only the env passed to one `ExecCommand` call. It does not touch:

- `os.Setenv` (never called).
- Any tool other than the shell tool. `read_file`, `grep`, etc. that internally exec sub-processes (e.g. the `rg` invocation in `Grep`) keep their existing env. This matches Claude Code's "Bash tool only" rule.
- Hook subprocesses or MCP server spawns — those receive their own PATH from §7.

A plugin without a `bin/` directory contributes nothing to `PluginBinPATH`. A plugin with a `bin/` directory containing no files contributes the empty directory; this is harmless.

### 8.3 Symlink and traversal handling

`<root>/bin` is stat-ed without symlink resolution. `LoadPlugin` already calls `filepath.EvalSymlinks` on the plugin root, so `<root>/bin` is already past one symlink hop. SP7 does not walk into the directory; the Bash shell will resolve executables at exec time using its own logic.

If a plugin's `bin/` contains a symlink that escapes the plugin's cache directory, that is the marketplace operator's problem to detect during install (SP4). SP7 trusts what `LoadPlugin` returns.

## 9. Plugin-Root `settings.json`

Claude Code's reference page documents two supported keys: `agent` and `subagentStatusLine`. SP7 supports only the first in A-tier.

### 9.1 Schema honored

```json
{
  "agent": "code-reviewer"
}
```

`agent` must be the bare name (not namespaced). At load time, SP7 verifies the named agent exists in `LoadedPlugin.Agents`; if not, the field is logged as a `PluginWarning` and the value is dropped. On success, the resolved name (namespaced as `<plugin>:<agent>`) is written to `LoadedPlugin.DefaultAgent`.

The session-init step that already wires the active subagent reads `DefaultAgent` from each loaded plugin in load order and lets the last non-empty value win — same precedence rule as everything else in the config.

### 9.2 Schema warned-once

Any other key inside the plugin's `settings.json` (`subagentStatusLine`, `permissions`, `theme`, etc.) produces one `PluginWarning` per plugin per key. The warning surface is the same as for unsupported manifest fields (§11).

### 9.3 File location and missing-file behavior

The file is `<root>/settings.json` (not `.claude-plugin/settings.json`). Absent file is benign and produces neither warning nor error. Malformed JSON is a startup error per the convention SP1 establishes — silent fallback would hide typos.

## 10. `skills` Custom Paths

### 10.1 Schema honored

```json
{ "skills": "./custom/skills/" }
```

```json
{ "skills": ["./custom/skills/", "./extra/skills/"] }
```

Single string or array of strings. Each path is relative to the plugin root and must start with `./` (Claude Code's rule). Absolute paths are an error: `userConfig|skills: paths must be relative and start with "./"`.

### 10.2 Loading semantics

Additive. `discoverPluginSkills` in `agent/plugin.go` already calls `resolveComponentDirs(pluginDir, "skills", override)` which scans the default `skills/` and *appends* override-supplied directories. SP7 just hooks up the override:

```go
var skillsOverride any
if len(manifest.Skills) > 0 {
    _ = json.Unmarshal(manifest.Skills, &skillsOverride) // tolerates string or []any
}
lp.Skills = discoverPluginSkills(resolved, manifest.Name, skillsOverride)
```

(Right now `discoverPluginSkills` takes no override. SP7 changes its signature; the existing call site at `plugin.go:192` updates accordingly.)

The `resolveComponentDirs` function already does the right additive thing for the `skills` case — comments at `plugin.go:240`: *"Custom paths supplement defaults, they don't replace them."* So SP7's wiring is mechanical.

### 10.3 Path validation

Paths that traverse outside the plugin root (e.g. `./../shared/skills/`) are rejected at parse time with `skills: paths must not traverse outside the plugin root`. Validation runs after `filepath.Clean` on `filepath.Join(pluginDir, override)` and asserts the result is `pluginDir` or a descendant. This is the same `ensureUnderRoot` check that `LocalExecutionEnvironment.resolveWrite` uses.

## 11. Warn-Once Mechanism

### 11.1 What triggers a warning

Three categories:

| Trigger | Field text in `PluginWarning` |
|---|---|
| Unknown manifest top-level field (`outputStyles`, `experimental.themes`, `experimental.monitors`, `channels`, `dependencies`, `lspServers`) | the field name as it appears in JSON |
| Unknown plugin-root `settings.json` key other than `agent` (`subagentStatusLine`, etc.) | `settings.json:<key>` |
| Unknown per-option field inside a `userConfig` entry | `userConfig.<key>:<field>` |

The set of *recognized* fields is hardcoded. Adding a new field is a one-line change.

### 11.2 Deduplication

Deduplication is per-plugin-per-field. Within a single `LoadPlugin` call the same field can only trigger once, because each parse site only sees the field once. Across plugins, two different plugins each shipping `outputStyles` each produce one warning.

To survive session reloads (`/reload-plugins`, hot config change), warnings emit at most once per *process lifetime* per `(pluginID, field)`. A package-level `sync.Map` tracks seen pairs. Tests reset it via an unexported `resetWarningsForTest` helper.

### 11.3 Where warnings go

Each warning is captured on `LoadedPlugin.Warnings` so the structured output of `serf plugin list --json` can surface them. At session startup, the session prints each plugin's new-since-last-print warnings to `stderr`, formatted as:

```
serf: plugin "<plugin-id>": ignoring unsupported field "<field>"
```

The serf-hub web UI surfaces them in the plugin-detail card. The TUI surfaces them in a one-line collapsible.

## 12. Error Contracts

| Operation | Error condition | Behavior |
|---|---|---|
| `ParseUserConfig` | malformed `userConfig` blob | return error; aborts `LoadPlugin` |
| `LoadPlugin` (skills/bin/settings.json) | malformed plugin-root `settings.json` | error, aborts startup |
| `LoadPlugin` (skills/bin/settings.json) | bin directory exists but is not a directory | leave `BinDir=""`, emit warning, do not abort |
| `PromptForUserConfig` | prompter returns `ErrPromptCanceled` | return wrapped error; persist nothing |
| `PromptForUserConfig` | typed-value validation fails (e.g. `number` out of bounds) | re-prompt the same key in interactive surfaces; hard error in non-interactive |
| `PromptForUserConfig` | secure-store `Set` fails | return error; persist nothing; the partial plain values written so far are rolled back via tmp-file discard |
| `NewSecureStore` | (future) keychain unavailable | log once, fall back to `FileStore`, return `FileStore` |
| `NewSecureStore` | fallback file path unwritable (no `$HOME`, no `~/.config/serf/` writable) | return error; session-init reports `secure store unavailable: <err>` and refuses to enable any plugin with a `sensitive: true` userConfig key |
| `ResolveUserConfig` | required key missing, non-interactive surface | populate `missing` slice; caller (session init) errors out with the §4.1 message |
| `ResolveUserConfig` | required key missing, interactive surface | populate `missing` slice; caller re-enters prompt flow |
| `ExpandUserConfig` | (never errors) | unknown `${user_config.<key>}` left literal + one-time warning |
| `UserConfigEnvVars` | (never errors) | sensitive values pass through; SP7 trusts the caller's spawn site |

The `secure store unavailable` error is the only path that can refuse to start the session. Every other condition either errors out at install/enable time (where the user is present to fix it) or degrades gracefully at session start.

## 13. Package and File Layout

New files:

- `agent/plugin_userconfig.go` — `UserConfigOption`, `UserConfigType`, `ParseUserConfig`, `ResolvedUserConfig`, `ResolveUserConfig`, `ExpandUserConfig`, `UserConfigEnvVars`, `PromptForUserConfig`.
- `agent/plugin_userconfig_test.go` — unit tests for parse, resolve, expand, env-var generation.
- `agent/plugin_bin.go` — `PluginBinPATH` and the `BinDir` discovery helper used by `LoadPlugin`.
- `agent/plugin_bin_test.go` — unit + integration tests for the PATH-prepend path, exercising a real `ExecCommand` against a `bin/` fixture.
- `agent/plugin_warnings.go` — `PluginWarning`, the `sync.Map`-backed dedup, the `resetWarningsForTest` test helper.
- `agent/plugin_warnings_test.go`.
- `agent/internal/securestore/securestore.go` — `SecureStore` interface and `FileStore` implementation.
- `agent/internal/securestore/securestore_test.go` — tests for `FileStore` (atomic write, mode-0600 assertion, missing-file tolerance, key namespacing).
- `agent/plugin_config_store.go` — `PluginConfigStore` interface plus a `ConfigJSONStore` implementation that reads/writes the `pluginConfigs` field of `~/.config/serf/config.json`.
- `agent/plugin_config_store_test.go`.

Existing files modified:

- `agent/plugin.go` — extend `PluginManifest` with the new fields (§2), populate the new `LoadedPlugin` fields, thread the `skills` override into `discoverPluginSkills`, stat `<root>/bin`, parse `<root>/settings.json`.
- `agent/skills.go` / the existing `discoverPluginSkills` helper — accept an override argument; default-only callers pass `nil`.
- `agent/session.go` — wire `PluginBinPATH` into the shell tool registration; call `ResolveUserConfig` per plugin during `initPlugins`; stash resolved configs on `s.pluginUserConfigs` for SP5/SP6 to consume.
- `agent/config.go` (SP1) — add the `PluginConfigs` field on `SerfConfig`. SP1's parser already keeps unknown JSON as `RawMessage`; this is the typed accessor.
- `cmd/serf/plugin/install.go` (SP4) — call `PromptForUserConfig` after install/enable.
- `cmd/serf/main.go`, `cmd/serf-tui/embedded.go`, `cmd/serf-hub/web.go` — register the surface-specific `UserConfigPrompter` with the session.

Tests live next to the code they cover; integration fixtures live under `agent/testdata/plugins/<scenario>/`. Specifically, `agent/testdata/plugins/userconfig-basic/` is the smallest plugin that exercises one of each `userConfig` type.

## 14. Testing Strategy

TDD. Tests are written before implementation. No mocked filesystem; tests use `t.TempDir()` and real files. Keychain backend is *not* exercised in SP7 v1 — `NewSecureStore` returns `FileStore` and that is the integration target.

### 14.1 `ParseUserConfig`

Table-driven. One row per per-field validation rule from §3.2 and §3.3.

| # | Case | Input | Expect |
|---|---|---|---|
| 1 | Empty/absent | `nil` | `(nil, nil)` |
| 2 | Empty object | `{}` | `(empty slice, nil)` |
| 3 | One of each type | full §3.1 example | five options in declaration order |
| 4 | Identifier rule violation | `{"API_TOKEN":{...}}` | error `userConfig.API_TOKEN: identifier` |
| 5 | Missing `type` | `{"x":{"title":"X","description":"x"}}` | error `field "type": required` |
| 6 | Unknown `type` | `{"x":{"type":"int",...}}` | error `field "type": must be one of` |
| 7 | `multiple` on non-string | `{"x":{"type":"number","multiple":true,...}}` | error |
| 8 | `min` on non-number | `{"x":{"type":"string","min":1,...}}` | error |
| 9 | `default` type mismatch | `{"x":{"type":"boolean","default":"yes",...}}` | error |
| 10 | Missing `title` | omit `title` | error |
| 11 | Missing `description` | omit `description` | error |
| 12 | Unknown per-option field | extra `"footy":1` field | no error; a `PluginWarning` is captured |
| 13 | Declaration order preserved | three keys in non-alphabetic order | slice order matches input |
| 14 | `multiple` default as array | `{"hosts":{"type":"string","multiple":true,"default":["a","b"],...}}` | `Default` is `[]any{"a","b"}` |
| 15 | `min`/`max` round-trip | number key with both | values stored on the option |

### 14.2 `ResolveUserConfig`

Each test uses an in-memory `PluginConfigStore` and a `FileStore` rooted at `t.TempDir()`.

| # | Case | Stored state | Expect |
|---|---|---|---|
| 1 | All keys persisted | plain has every non-sensitive; secure has every sensitive | resolved values match; `missing` empty |
| 2 | Some keys absent, defaults apply | absent keys have `default` | resolved values come from defaults; `missing` empty |
| 3 | Required key absent, no default | one required key absent | `missing` contains it |
| 4 | Sensitive key in plain store is ignored | plain has the sensitive key | resolver reads from secure store; plain-store value ignored |
| 5 | Multi-valued string joins | `multiple: true`, value `["a","b"]` | `Lookup` returns `"a b"` |
| 6 | Boolean stringification | `true` | `Lookup` returns `"true"` |
| 7 | Number stringification (integer) | `5000` | `Lookup` returns `"5000"` (no decimal) |
| 8 | Number stringification (float) | `1.5` | `Lookup` returns `"1.5"` |
| 9 | Tilde expansion for directory/file | `default: "~/projects"` | `Lookup` returns expanded absolute path |
| 10 | Unknown key | `r.Lookup("does_not_exist")` | `("", false)` |
| 11 | Declared-but-empty key | required key resolved via prompt to `""` | `Lookup` returns `("", true)` |

### 14.3 `ExpandUserConfig`

| # | Case | Input | Expect |
|---|---|---|---|
| 1 | Single substitution | `"--endpoint=${user_config.api_endpoint}"` | replaced with stored value |
| 2 | Multiple substitutions | one string, two tokens | both replaced |
| 3 | Unknown key | `${user_config.absent}` | literal preserved; one stderr warning |
| 4 | Unknown key twice | same token twice in one call, again across two calls | warning emitted exactly once per `(pluginID, key)` |
| 5 | Adjacent tokens | `${user_config.a}${user_config.b}` | both replaced, no whitespace inserted |
| 6 | Sensitive value substituted, not logged | `${user_config.api_token}` | value substituted; debug log scrubs to `***` |
| 7 | Token-like text not matching the pattern | `${user_config.A}` (uppercase) | literal preserved (uppercase not a valid key) |

### 14.4 `UserConfigEnvVars`

| # | Case | Input | Expect |
|---|---|---|---|
| 1 | All types present | resolved with one of each type | map keys are uppercased; values are stringified per §3.2 |
| 2 | Empty resolved | no values | empty map |
| 3 | Sensitive value preserved | `api_token=sekret` | `CLAUDE_PLUGIN_OPTION_API_TOKEN=sekret` |
| 4 | No leakage of unrelated keys | resolver scoped to plugin A | map contains only A's keys |

### 14.5 `PromptForUserConfig`

Uses a `MapPrompter` (an in-test `UserConfigPrompter` that returns canned values per key) to keep the tests hermetic.

| # | Case | Setup | Expect |
|---|---|---|---|
| 1 | Happy path | prompter returns valid values for all keys | plain store has non-sensitive keys; secure store has sensitive keys; `ResolvedUserConfig` populated |
| 2 | Default accepted (empty string) | prompter returns `""`; key has a default | persisted value is the default |
| 3 | Required-without-default left empty | prompter returns `""`; key is required, no default | error before persisting anything |
| 4 | `min`/`max` violation | prompter returns `99999` for `max: 60000` | error |
| 5 | Prompter cancel | prompter returns `ErrPromptCanceled` | error; no writes occurred |
| 6 | Secure-store write failure | inject a broken `SecureStore` | error; plain-store rollback (tmp file removed) |
| 7 | Re-run after schema additions | persisted state lacks a newly added required key | resolver reports it as missing; prompt fires only for the new key |

### 14.6 `FileStore`

| # | Case | Setup | Expect |
|---|---|---|---|
| 1 | Missing file → empty | no file present | `Get` returns `("", false, nil)` |
| 2 | Set writes file with mode 0600 | one `Set` call | file exists; `os.Stat` reports `0o600` |
| 3 | Parent dir created with 0700 | `~/.config/serf/` absent | created with `0o700` |
| 4 | Round-trip | `Set` then `Get` | value matches |
| 5 | Delete | `Set` then `Delete` then `Get` | returns `("", false, nil)` |
| 6 | Atomic write under crash | mid-write panic via injected `io.Writer` | original file intact, tmp removed |
| 7 | Two plugins isolated | `Set(pluginA, ...)` then `Set(pluginB, ...)` | both readable; one removal does not affect the other |

### 14.7 `PluginBinPATH` and shell-tool integration

| # | Case | Setup | Expect |
|---|---|---|---|
| 1 | No plugins | empty | `""` |
| 2 | Plugin without bin dir | `BinDir=""` | `""` |
| 3 | One plugin with bin | bin dir exists, contains one executable | string equals bin-dir absolute path |
| 4 | Two plugins, order preserved | both have bin dirs | colon-joined in load order |
| 5 | End-to-end shell exec | plugin fixture with `bin/my-tool` (echoes "ok"); register one plugin; invoke shell tool with `command: "my-tool"` | exit 0, stdout contains "ok" |
| 6 | Plugin bin does NOT escape to non-shell tools | exec via `Grep` or `read_file` is unaffected | the plugin's binary is not on `PATH` for those tools |
| 7 | Plugin bin scope is per-call | second `ExecCommand` call after first does not inherit | each call gets PATH freshly assembled |

Test #5 uses a real `LocalExecutionEnvironment` rooted at `t.TempDir()` with a plugin fixture under `<tmp>/plugin-with-bin/`. The fixture's `bin/my-tool` is `#!/bin/sh\necho ok`. Skips on Windows (only Unix-style PATH is exercised).

### 14.8 Plugin-root `settings.json`

| # | Case | File contents | Expect |
|---|---|---|---|
| 1 | Absent file | no file | `DefaultAgent=""`, no warning |
| 2 | `agent` set, agent exists | `{"agent":"reviewer"}` and `agents/reviewer.md` exists | `DefaultAgent="<plugin>:reviewer"` |
| 3 | `agent` set, agent missing | `{"agent":"nope"}` | `DefaultAgent=""`, one warning |
| 4 | `subagentStatusLine` set | `{"subagentStatusLine":{...}}` | `DefaultAgent=""`, one warning with `settings.json:subagentStatusLine` |
| 5 | Malformed JSON | `{` | `LoadPlugin` errors |
| 6 | Unknown top-level key | `{"theme":"dark"}` | one warning |

### 14.9 `skills` custom paths

| # | Case | Manifest | Filesystem | Expect |
|---|---|---|---|---|
| 1 | Default only | omit `skills` | `skills/foo/SKILL.md` | `foo` discovered |
| 2 | Additive single path | `"skills":"./extra"` | `skills/foo/SKILL.md` and `extra/bar/SKILL.md` | both discovered |
| 3 | Additive array | `"skills":["./extra","./more"]` | three skills across three dirs | all three discovered |
| 4 | Path traversal rejected | `"skills":"./../escape"` | (irrelevant) | `LoadPlugin` errors |
| 5 | Absolute path rejected | `"skills":"/abs/path"` | (irrelevant) | `LoadPlugin` errors |
| 6 | Override path absent | `"skills":"./missing/"` | only default present | default still scanned; one warning per missing override |

### 14.10 Warn-once dedup

| # | Case | Setup | Expect |
|---|---|---|---|
| 1 | Same plugin loaded twice same process | call `LoadPlugin` twice on same fixture with `outputStyles` set | one stderr warning total |
| 2 | Two plugins each with `outputStyles` | distinct names | two stderr warnings |
| 3 | `resetWarningsForTest` between tests | reset, then re-load | warning fires again |
| 4 | All categories captured | one fixture with manifest-level, settings.json-level, and userConfig-level unknown fields | three warnings on `LoadedPlugin.Warnings` |

### 14.11 Coverage gate

Every exported function in §2 has at least one direct test row. Every error path in §12 has a row in §14. Every per-type rule in §3.2 has a row in §14.1 or §14.2. `go test ./agent/... -run 'UserConfig|PluginBin|PluginWarning|SecureStore'` is green. `go test ./agent/internal/securestore/...` is green.

## 15. Open Questions

### 15.1 Prompt UX per surface — settled here

Three surfaces, three prompter implementations:

- **CLI**: inline prompts, line-by-line, masked input for `sensitive: true`. Implemented in SP4's `serf plugin install/enable` flow. Default for `serf` invoked without `serf-tui` or `serf-hub`.
- **`serf-tui`**: modal form rendered by the existing tui-form widget; submits all fields at once.
- **`serf-hub`**: web form on a per-plugin endpoint, gated by the existing session-auth path; submission returns to the plugin-enable flow.

Non-interactive sessions read from `--plugin-option <plugin>.<key>=<value>` (may repeat). Missing required keys with no flag value produce a startup error that names the plugin, the key, and the suggested command to fix it.

### 15.2 Sensitive-value key collision across plugins — settled here

Multiple plugins each declaring `api_token` are isolated by plugin ID. Keychain account names are `<pluginID>/<key>`; file-store entries are nested under `credentials.<pluginID>.<key>`. The plugin ID is the `enabledPlugins` key (`<name>@<marketplace>` or `<name>`), not the manifest `name`.

### 15.3 Dependencies on other sub-specs (NOT resolved here)

- **SP1** — must add `PluginConfigs map[string]json.RawMessage` to `SerfConfig` and merge it by-key. SP7 ships the typed accessor.
- **SP4** — must call `PromptForUserConfig` at the end of `install` and `enable`. SP7 ships the function; SP4 owns the CLI surface.
- **SP5** — must merge `UserConfigEnvVars(r)` into hook subprocess envs for the owning plugin's hooks, and pass `ExpandUserConfig(hookCmd, r)` through before exec. SP7 ships the helpers.
- **SP6** — same as SP5 for MCP server `command`, `args`, `env`, `url`, `headers`. SP7 ships the helpers.
- **SP8** — wires the per-surface `UserConfigPrompter` into the session entry points, and triggers session-start re-prompt for missing required keys when the surface is interactive.

### 15.4 Future: keychain backend

`KeychainStore` will live in `agent/internal/securestore/keychain_<os>.go`, build-tagged. The interface is in place; `NewSecureStore` is the only switch. A migration helper (`FileStore` → `KeychainStore`) can move existing values forward by reading the file, writing to keychain, then deleting the file entries. That helper is not in SP7's scope.
