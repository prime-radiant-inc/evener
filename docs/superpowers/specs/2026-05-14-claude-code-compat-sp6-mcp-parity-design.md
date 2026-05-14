# SP6 — MCP Parity (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
Sibling: `docs/superpowers/specs/2026-05-14-claude-code-compat-sp1-config-loader-design.md`

## 1. Goal

SP6 closes the three documented gaps between Claude Code's MCP behavior and serf's
existing implementation in `agent/mcp_config.go` and `agent/mcp_manager.go`. It
teaches the config loader to accept `streamable-http` as an alias for the `http`
transport type, injects `CLAUDE_PROJECT_DIR` into the environment of every
spawned stdio MCP server, and extends the variable-expansion pass that already
handles `${VAR}` and `${VAR:-default}` to also resolve `${CLAUDE_PROJECT_DIR}`,
`${CLAUDE_PLUGIN_ROOT}`, `${CLAUDE_PLUGIN_DATA}`, and `${user_config.KEY}`.
SP6 owns only the consumption of `user_config.KEY` values — the storage,
prompt-on-enable flow, and sensitive-value handling stay with SP7, which exposes
a lookup function SP6 calls during expansion. No other MCP semantics change:
existing `mcp.json` files, plugin-bundled MCP configs, and CLI inline `--mcp`
specs continue to load and run unchanged.

## 2. Public API Surface

All changes stay in package `agent`. No new exported types. One renamed
helper, one new helper, one struct grown by no fields.

### 2.1 `MCPServerConfig` (unchanged shape, normalized values)

The type from `agent/mcp_config.go` keeps its existing fields. `Type` is
normalized to one of `"stdio"`, `"sse"`, `"http"` at load time; the wire alias
`streamable-http` is collapsed before the value reaches `transportForConfig`.
Downstream code never sees `"streamable-http"`.

### 2.2 `ExpansionContext` (new, unexported)

```go
// expansionContext supplies the values consulted by expandVars when it
// encounters a name-bearing placeholder. All fields are optional: an empty
// value means "this kind of placeholder is not available in this context",
// and any reference to such a name fails with the same "not set" error
// expandEnvVars uses today for unset OS env vars.
type expansionContext struct {
    ProjectDir  string                  // value substituted for ${CLAUDE_PROJECT_DIR}
    PluginRoot  string                  // value substituted for ${CLAUDE_PLUGIN_ROOT}
    PluginData  string                  // value substituted for ${CLAUDE_PLUGIN_DATA}
    UserConfig  userConfigLookup        // nil means ${user_config.*} is unavailable
}

// userConfigLookup returns the resolved value for a single user_config key
// bound to the plugin whose config is being expanded. Returns ok==false if
// the key is undefined; callers treat that the same as an unset env var.
// Implemented by SP7. SP6 calls only the function; storage, prompt flow,
// keychain integration, and CLAUDE_PLUGIN_OPTION_* injection are SP7.
type userConfigLookup func(key string) (value string, ok bool)
```

`expansionContext` is unexported because no caller outside `agent` needs to
construct one — the package's loaders build the right context internally.

### 2.3 Functions

`expandEnvVars(s string) (string, error)` is **renamed** to
`expandVars(s string, ctx expansionContext) (string, error)` and grows the
context parameter. The old name keeps a thin shim during the changeover (one
release window; removed once SP7 lands) that calls `expandVars(s,
expansionContext{})`. Behavior on a zero-value context is identical to the
current function for inputs that only contain `${OS_VAR}` and
`${OS_VAR:-default}` placeholders.

`serverJSONToConfig(name string, sj mcpServerJSON) (MCPServerConfig, error)`
gains an `ExpansionContext` parameter:

```go
func serverJSONToConfig(name string, sj mcpServerJSON, ctx expansionContext) (MCPServerConfig, error)
```

`LoadMCPConfigFile(path string) ([]MCPServerConfig, error)` keeps its
signature. Internally it constructs an `expansionContext` whose `ProjectDir`
comes from `resolveProjectDir(nil)` (see §4) and whose `PluginRoot`,
`PluginData`, and `UserConfig` are zero — the top-level `mcp.json` is not
bound to a plugin.

`DiscoverMCPConfigs(env ExecutionEnvironment, extraFiles, inlineSpecs []string) ([]MCPServerConfig, error)`
keeps its signature. It now threads `env` through to a single
`resolveProjectDir(env)` call, then uses that `ProjectDir` value when
loading each layer.

`loadPluginMCPFile(path, pluginDir string)` and `discoverPluginMCPConfigs`
in `agent/plugin.go` already pre-expand `${CLAUDE_PLUGIN_ROOT}` via a literal
`strings.ReplaceAll`. SP6 replaces both call sites with a single
`expandVars` call seeded with `expansionContext{PluginRoot: pluginDir,
PluginData: pluginDataDir(pluginName), UserConfig: sp7Lookup(pluginName)}`.
The `expandPluginRoot` helper becomes a no-op shim and is removed in the
same change once tests confirm parity.

`transportForConfig(cfg MCPServerConfig) (mcp.Transport, error)` adds a
single line: in the `"stdio"` arm it merges `CLAUDE_PROJECT_DIR` into the
spawned process environment using `cfg.Env` plus the resolved project
directory, then calls `mergeEnv` as before. Details in §5.

### 2.4 New helper: `resolveProjectDir`

```go
// resolveProjectDir picks the value substituted for ${CLAUDE_PROJECT_DIR}
// and injected as CLAUDE_PROJECT_DIR into stdio MCP servers. Precedence
// matches the high-level spec: session project root > git root > cwd.
// env may be nil; the function then falls back to os.Getwd() and skips
// the git-root probe.
func resolveProjectDir(env ExecutionEnvironment) string
```

Implementation is purely additive; no existing function changes behavior
when `resolveProjectDir` is not called.

## 3. Type Aliasing: `streamable-http` → `http`

The MCP specification names the transport `streamable-http`. Claude Code
accepts both `http` and `streamable-http` in `.mcp.json`. SP6 follows that
contract.

Rule: when parsing `mcpServerJSON.Type`, after trimming surrounding
whitespace, **if the value equals `"streamable-http"` exactly, replace it
with `"http"`**. The replacement happens once, in `serverJSONToConfig`,
before any downstream code reads `MCPServerConfig.Type`.

Case sensitivity: **strict, lowercase only**. `"Streamable-HTTP"`,
`"STREAMABLE-HTTP"`, and `"streamableHttp"` all fail with the existing
`unknown MCP transport type` error. This matches the existing handling of
`"http"`, `"sse"`, and `"stdio"`, which are also lowercase-only — there is
no precedent in the file for case-insensitive type names, and silently
accepting alternative casings would obscure typos in user configs.

Field-shape implications: none. An `http` server and a `streamable-http`
server require the same fields (`url`, optional `headers`) and reject the
same fields (`command`, `args`, `env`). The alias is purely a name
collapse; the post-normalize value is `"http"` and the existing
`transportForConfig` `"http"` arm (which already constructs
`mcp.StreamableClientTransport`) handles it without further change. This
keeps the alias one line of code and zero behavior drift.

Error message for an unknown type continues to quote the original
user-supplied value, so a malformed type like `"streamable_http"` reports
itself, not the post-normalize string. Concretely: the alias check runs
inside `serverJSONToConfig`, and the unknown-type error stays in
`transportForConfig`; that arrangement means by the time control reaches
`transportForConfig`, the only legal values are `"stdio"`, `"sse"`, and
`"http"`, and any other value already failed at parse time with the
original spelling.

## 4. Env Injection: `CLAUDE_PROJECT_DIR`

### 4.1 Value

`CLAUDE_PROJECT_DIR` is the absolute path serf chooses to call the
"project root" for one session. Picking it is `resolveProjectDir(env)`,
which walks three sources, lowest-precedence last:

1. **Session project root override.** If the `ExecutionEnvironment`
   exposes a method named `ProjectDir() string` (added in a small,
   forward-looking extension of the `ExecutionEnvironment` interface in a
   later SP — out of scope here) and that method returns a non-empty
   value, use it. SP6 type-asserts for the method via `interface{
   ProjectDir() string }`; environments that do not implement it are
   skipped. No existing environment implements this method today, so the
   override is dormant until a future SP wires it up.
2. **Git root.** Call the existing `gitRootOrEmpty(env, env.WorkingDirectory())`.
   If it returns a non-empty path, use that.
3. **Working directory.** Use `env.WorkingDirectory()`. If `env` is nil,
   use `os.Getwd()`; if even that fails (cwd unreachable), use `""`.

### 4.2 Behavior when no value can be determined

`resolveProjectDir` returns the empty string. SP6 then:

- **Does not inject** `CLAUDE_PROJECT_DIR` into the spawned env (so the
  variable falls through from the parent process if it was set there;
  otherwise it stays unset for the child).
- **Returns an error** if a config string contains `${CLAUDE_PROJECT_DIR}`
  without a `:-default` and `resolveProjectDir` came back empty. The
  error matches the existing unset-variable format produced by
  `expandVars`, so users get the same "use `:-default`" hint they already
  get for OS env vars. This matches the Claude Code documentation, which
  explicitly says project- or user-scoped `.mcp.json` should use
  `${CLAUDE_PROJECT_DIR:-.}` because the variable is set in the *server's*
  environment, not in Claude Code's own. Plugin-provided configs
  substitute directly because `resolveProjectDir` runs before expansion
  and seeds the value.

### 4.3 Injection point

In the `"stdio"` (and default) arm of `transportForConfig`, build the env
map as:

```
extra := map[string]string{}  // start empty
if projectDir != "" {
    extra["CLAUDE_PROJECT_DIR"] = projectDir
}
for k, v := range cfg.Env {
    extra[k] = v               // cfg.Env wins over the auto-injection
}
cmd.Env = mergeEnv(extra)
```

The override order means a user who explicitly sets
`"env": {"CLAUDE_PROJECT_DIR": "..."}` in their config keeps full control.
`mergeEnv` is already idempotent in the face of duplicates.

The injection runs unconditionally for stdio transports, even when
`cfg.Env` is empty. To preserve the current "no override of process env
unless something is being merged in" guarantee, the call path skips
`mergeEnv` entirely (and leaves `cmd.Env == nil`, the Go default of
"inherit parent") only when both `cfg.Env` is empty *and* `projectDir`
is empty. Any other combination calls `mergeEnv` exactly as it does
today.

`http` and `sse` transports do **not** receive `CLAUDE_PROJECT_DIR`
injection — these are out-of-process HTTP endpoints and the env var has
no meaningful surface there. Claude Code's docs match this scoping.

### 4.4 Precedence for `transportForConfig`'s project-dir source

`transportForConfig(cfg MCPServerConfig)` takes one argument today and
does not see the `ExecutionEnvironment`. SP6 adds a second parameter:

```go
func transportForConfig(cfg MCPServerConfig, projectDir string) (mcp.Transport, error)
```

`NewMCPManager` resolves `projectDir` once (from the
`ExecutionEnvironment` passed to whichever caller built the manager) and
threads it in. Tests can pass any string they want; integration tests
pass real `t.TempDir()` paths.

## 5. Expansion Algorithm

The single pass currently implemented by `expandEnvVars` is preserved
verbatim — same `${...}` scanner, same `:-default` handling, same error
on unterminated `${`. SP6 only extends the **name resolution** step
between "we parsed a name" and "we look it up."

### 5.1 Supported names

| Placeholder | Resolved value |
|---|---|
| `${CLAUDE_PROJECT_DIR}` | `ctx.ProjectDir` |
| `${CLAUDE_PLUGIN_ROOT}` | `ctx.PluginRoot` |
| `${CLAUDE_PLUGIN_DATA}` | `ctx.PluginData` |
| `${user_config.KEY}` | `ctx.UserConfig("KEY")` if non-nil |
| `${SOME_OTHER_NAME}` | `os.LookupEnv("SOME_OTHER_NAME")` |
| any of the above with `:-default` suffix | resolved value if "set," otherwise `default` |

### 5.2 Resolution order inside `expandVars`

For each placeholder, in order:

1. Strip optional `:-default` suffix; remember it.
2. Match the name against the four reserved prefixes/exact names listed
   above.
   - `CLAUDE_PROJECT_DIR`: "set" iff `ctx.ProjectDir != ""`.
   - `CLAUDE_PLUGIN_ROOT`: "set" iff `ctx.PluginRoot != ""`.
   - `CLAUDE_PLUGIN_DATA`: "set" iff `ctx.PluginData != ""`.
   - `user_config.KEY` (string literal `user_config.` prefix, the rest is
     the key): "set" iff `ctx.UserConfig != nil` and the lookup returns
     `ok == true`.
3. If no reserved name matched, fall through to `os.LookupEnv(name)`.
4. If "not set" and there is a default, substitute the default.
5. If "not set" and there is no default, return the existing
   "environment variable %q is not set (use ${%s:-default} ...)" error.

The order matters in exactly one case: a user defines an OS env var
literally named `CLAUDE_PROJECT_DIR` while also running in a session
where serf has a different project root. **Serf's resolved value wins**;
the OS env var is shadowed. Rationale: the whole point of the auto-
injection is to give MCP servers a stable project pointer. Letting the
parent shell override it would defeat the purpose. Users who want the
parent value still have it — they can `${CLAUDE_PROJECT_DIR:-"$OTHER"}`,
or simply read `os.environ["CLAUDE_PROJECT_DIR"]` *inside the spawned
server*, where the value is exactly what serf injected (which itself can
be the parent's value if `resolveProjectDir` chose to propagate it; this
case is documented but not algorithmically privileged).

The same precedence applies to `CLAUDE_PLUGIN_ROOT` and
`CLAUDE_PLUGIN_DATA` for plugin-bound configs. For non-plugin configs
those names are unset by `ctx`, and the resolver falls through to the
parent OS env, which is the existing behavior — no regression.

### 5.3 Order of expansion across fields

Each string field (`command`, every element of `args`, every value in
`env`, `url`, every value in `headers`) is independently passed to
`expandVars` once. There is **no recursive expansion**: the output of
one `${...}` substitution is not re-scanned. Today's behavior is the
same; SP6 preserves it explicitly so a `user_config` value that happens
to contain a literal `${OTHER}` is taken as the user intended.

Within one string, placeholders are resolved left-to-right by the
scanner already in `expandEnvVars`. Order does not affect the final
string because each resolution is independent.

### 5.4 Behavior when a key is undefined

- Reserved name, context value empty, no `:-default`: error, identical
  format to the existing unset-env-var error. Error name is the full
  placeholder text (`user_config.MY_KEY`, not just `MY_KEY`), so the
  hint `${user_config.MY_KEY:-default}` is copy-pasteable.
- Reserved name, context value empty, with `:-default`: substitute the
  default.
- `user_config.KEY` referenced when `ctx.UserConfig == nil` (i.e. config
  is not plugin-bound): treated the same as "unset, no default" unless a
  `:-default` is present. Error text explicitly names the placeholder so
  users see "user_config references only work inside plugin-provided
  MCP configs."
- Unknown OS env var, no default: existing error.

### 5.5 Worked example

Plugin `acme-tools` provides:

```json
{
  "mcpServers": {
    "issues": {
      "type": "streamable-http",
      "url": "${user_config.JIRA_URL:-https://example.atlassian.net}/mcp",
      "headers": {
        "Authorization": "Bearer ${user_config.JIRA_TOKEN}",
        "X-Project": "${CLAUDE_PROJECT_DIR}"
      }
    },
    "db": {
      "command": "${CLAUDE_PLUGIN_ROOT}/bin/db-mcp",
      "args": ["--state", "${CLAUDE_PLUGIN_DATA}/db.sqlite"],
      "env": {
        "API_KEY": "${user_config.DB_KEY}",
        "FALLBACK": "${SOMETHING:-x}"
      }
    }
  }
}
```

With `ctx.ProjectDir = "/Users/jesse/work/serf"`,
`ctx.PluginRoot = "/cache/acme-tools/0.1.0"`,
`ctx.PluginData = "/data/acme-tools"`, and a SP7-provided lookup
returning `JIRA_TOKEN=t`, `DB_KEY=k` (but no `JIRA_URL`):

- `issues.Type` becomes `http`.
- `issues.URL` becomes `https://example.atlassian.net/mcp` (default
  kicks in).
- `issues.Headers.Authorization` becomes `Bearer t`.
- `issues.Headers["X-Project"]` becomes `/Users/jesse/work/serf`.
- `db.Command` becomes `/cache/acme-tools/0.1.0/bin/db-mcp`.
- `db.Args[1]` becomes `/data/acme-tools/db.sqlite`.
- `db.Env["API_KEY"]` becomes `k`.
- `db.Env["FALLBACK"]` becomes `x` (since `SOMETHING` is unset).
- When `db` is spawned, the merged process env additionally contains
  `CLAUDE_PROJECT_DIR=/Users/jesse/work/serf` (injected by
  `transportForConfig` because the existing `db.Env` did not specify
  it).

## 6. Interface Required from SP7

SP7 owns user_config storage, prompt-on-enable, sensitive-value
storage in OS keychain, and `CLAUDE_PLUGIN_OPTION_*` env injection. SP6
consumes only resolved values, via a function literal:

```go
// userConfigLookup is the contract SP6 imports from SP7. Implementations
// must return ok==false for any undefined or never-prompted key. Sensitive
// values are returned in plaintext — SP7 is responsible for whatever
// keychain or file decryption happens before this function returns.
// The lookup function is bound to one plugin; callers pass the function
// itself, never a plugin name.
type userConfigLookup func(key string) (value string, ok bool)
```

SP7's concrete constructor signature is its choice. SP6's contract is
that the plugin loader can call:

```go
lookup := sp7.UserConfigLookupForPlugin(pluginName)
```

and pass `lookup` into `expansionContext.UserConfig` when loading that
plugin's MCP configs.

For non-plugin configs (top-level `mcp.json`, `--mcp-config`, `--mcp`
inline specs), `expansionContext.UserConfig` is `nil`. SP6 enforces this
boundary: any `${user_config.*}` reference in those scopes is an error
unless a `:-default` is supplied.

Until SP7 lands, SP6 ships with a no-op `userConfigLookup` (returns
`(_, false)` for every key) wired into the plugin loader. Plugins that
reference `${user_config.K}` without a default will then fail to load
with the documented error. This is acceptable because: (a) no such
plugin exists in the marketplaces serf integrates today; (b) SP7 lands
before SP8's end-to-end test runs.

## 7. Backward Compatibility

Every existing config string that loads today must still load.

- Existing `mcp.json` files with `type: "http"`, `"sse"`, `"stdio"`, or
  no `type`: unchanged. `streamable-http` is added, not substituted.
- Existing files that use `${VAR}` and `${VAR:-default}`: unchanged. The
  new resolver falls through to `os.LookupEnv` for any name not in the
  reserved set, which is exactly today's behavior.
- Existing plugin-bundled `.mcp.json` files that use
  `${CLAUDE_PLUGIN_ROOT}` via the current `expandPluginRoot`
  `ReplaceAll`: unchanged. The new code path computes the same
  substitution, just through `expandVars` instead of `strings.ReplaceAll`.
- CLI inline `--mcp name:cmd args...`: unchanged. The inline parser
  doesn't call `expandVars` today and doesn't need to.
- The `mcpServerJSON` JSON tags are unchanged. No new field is required
  in the file format. `streamable-http` is opt-in via the existing
  `type` field.
- `MergeMCPConfigs` semantics unchanged. SP6 does not interact with
  layering.

No deprecation warnings are emitted for `type: "http"`. Claude Code
treats them as equivalent; we do too.

## 8. Error Contracts

| Condition | Error |
|---|---|
| `type` is an unknown value (after alias collapse) | `unknown MCP transport type "<original>"` (existing) |
| Stdio server with no command | `stdio transport requires a command` (existing) |
| HTTP/SSE server with no URL | `http transport requires a url` / `sse transport requires a url` (existing) |
| `${CLAUDE_PROJECT_DIR}` referenced and `resolveProjectDir` returned `""`, no `:-default` | `environment variable "CLAUDE_PROJECT_DIR" is not set (use ${CLAUDE_PROJECT_DIR:-default} to provide a default)` |
| `${CLAUDE_PLUGIN_ROOT}` referenced in a non-plugin config, no default | same shape with name `CLAUDE_PLUGIN_ROOT` |
| `${CLAUDE_PLUGIN_DATA}` referenced in a non-plugin config, no default | same shape with name `CLAUDE_PLUGIN_DATA` |
| `${user_config.KEY}` referenced in a non-plugin config, no default | `environment variable "user_config.KEY" is not set (use ${user_config.KEY:-default} to provide a default)` — error string explicitly preserves the dotted form |
| `${user_config.KEY}` referenced in a plugin config, key undefined, no default | same shape |

Errors emerge from `expandVars` and are wrapped by `serverJSONToConfig`
with the field name (`expanding command`, `expanding arg[0]`,
`expanding env "FOO"`, etc.) as the current code already does. SP6 does
not change those wrappers.

Failure at expansion time is final: the affected MCP server is dropped
from the resulting `[]MCPServerConfig`, the whole load returns an error,
and the caller surfaces it. This matches today's "fail-fast on bad
expansion" behavior and is consistent with SP1's "malformed config
aborts startup" rule.

## 9. Package and File Layout

Changes are confined to two files plus their tests. No new files,
no new packages.

| File | Change |
|---|---|
| `agent/mcp_config.go` | Rename `expandEnvVars` → `expandVars`; add `expansionContext`, `userConfigLookup`; collapse `streamable-http`; thread `expansionContext` through `serverJSONToConfig` and `LoadMCPConfigFile`. Add `resolveProjectDir`. |
| `agent/mcp_manager.go` | Add second `projectDir` parameter to `transportForConfig`. Inject `CLAUDE_PROJECT_DIR` into the stdio env map. Pass `projectDir` through from `NewMCPManager`. |
| `agent/plugin.go` | Replace `expandPluginRoot` (literal `strings.ReplaceAll`) with `expandVars(s, expansionContext{PluginRoot: pluginDir, PluginData: pluginDataDir(name), UserConfig: sp7Lookup(name)})`. Remove `expandPluginRoot` once tests confirm parity. |
| `agent/mcp_config_test.go` | Tests in §10.1, §10.2. |
| `agent/mcp_manager_test.go` | Tests in §10.3. |
| `agent/plugin_test.go` | One regression test confirming plugin loader still expands `${CLAUDE_PLUGIN_ROOT}` after the helper is replaced. |
| `agent/testdata/mcp/sp6/` (new directory) | Fixture JSON files for the table-driven tests. |

`NewMCPManager`'s public signature gains `env ExecutionEnvironment` only
if no caller already supplies one in scope; review of the four call sites
(`cmd/serf`, `cmd/serf-tui`, `cmd/serf-hub`, `cmd/serfeval`) is part of
the implementation step. Worst case, an extra parameter threads through;
no existing call site will become invalid in a way TDD does not catch.

## 10. Testing Strategy

TDD: every test below is written before its corresponding implementation
line. No mocked filesystem. No mocked spawned process for the env-
injection integration test — use a real stub stdio MCP server. Match
existing patterns from `agent/mcp_config_test.go` and
`agent/mcp_manager_test.go`.

### 10.1 `expandVars` — table-driven unit tests

`TestExpandVars` exercises every reserved name and every error path. One
row per name × outcome × default-presence.

| # | Input string | `expansionContext` | Expected output | Expected error substring |
|---|---|---|---|---|
| 1 | `"plain"` | zero | `"plain"` | — |
| 2 | `"${A}"` | `os.Setenv("A","v")` | `"v"` | — |
| 3 | `"${A:-d}"` | `A` unset | `"d"` | — |
| 4 | `"${A}"` | `A` unset, no default | `""` | `"A" is not set` |
| 5 | `"${CLAUDE_PROJECT_DIR}"` | `ProjectDir:"/x"` | `"/x"` | — |
| 6 | `"${CLAUDE_PROJECT_DIR}"` | empty, OS env `CLAUDE_PROJECT_DIR=/shell` | `"/x"` from context wins — set `ProjectDir` to `"/x"` and OS env to `/shell`; assert `/x` | — |
| 7 | `"${CLAUDE_PROJECT_DIR}"` | empty, no OS env, no default | `""` | `"CLAUDE_PROJECT_DIR" is not set` |
| 8 | `"${CLAUDE_PROJECT_DIR:-.}"` | empty | `"."` | — |
| 9 | `"${CLAUDE_PLUGIN_ROOT}"` | `PluginRoot:"/p"` | `"/p"` | — |
| 10 | `"${CLAUDE_PLUGIN_ROOT}"` | empty | `""` | `"CLAUDE_PLUGIN_ROOT" is not set` |
| 11 | `"${CLAUDE_PLUGIN_DATA}"` | `PluginData:"/d"` | `"/d"` | — |
| 12 | `"${user_config.K}"` | lookup returns `"v",true` | `"v"` | — |
| 13 | `"${user_config.K}"` | lookup returns `_,false` | `""` | `"user_config.K" is not set` |
| 14 | `"${user_config.K}"` | `UserConfig: nil` | `""` | `"user_config.K" is not set` |
| 15 | `"${user_config.K:-fallback}"` | `UserConfig: nil` | `"fallback"` | — |
| 16 | `"prefix-${CLAUDE_PROJECT_DIR}-suffix-${A}"` | `ProjectDir:"x"`, OS `A=y` | `"prefix-x-suffix-y"` | — |
| 17 | `"${"` (unterminated) | zero | `"${"` (literal) | — (matches existing) |
| 18 | `"${a${b}}"` (no recursion) | OS `b=Z` | `"${aZ}"` | — (existing left-to-right; documents non-recursion) |

Row 6 captures the resolution-order rule from §5.2 (context beats OS
env). Use `t.Setenv` so the OS env mutation is scoped.

### 10.2 `LoadMCPConfigFile` and `serverJSONToConfig`

Extend the existing table in `TestLoadMCPConfigFile_*` with rows:

| Case | Fixture | Expected |
|---|---|---|
| `type: "streamable-http"` collapses to `"http"` | `{"mcpServers":{"x":{"type":"streamable-http","url":"https://e.test"}}}` | `cfg.Type == "http"`, `cfg.URL == "https://e.test"` |
| `type: "Streamable-HTTP"` rejected | same fixture, capitalized | error `unknown MCP transport type "Streamable-HTTP"` |
| `type: "streamable_http"` rejected (underscore) | underscore variant | error names the original spelling |
| `${CLAUDE_PROJECT_DIR}` in command resolves | fixture with `command: "${CLAUDE_PROJECT_DIR}/bin/x"` loaded with `expansionContext{ProjectDir:"/tmp/p"}` | `cfg.Command == "/tmp/p/bin/x"` |
| `${CLAUDE_PROJECT_DIR}` in url resolves | fixture with HTTP type and `url: "${CLAUDE_PROJECT_DIR}/api"` | `cfg.URL == "/tmp/p/api"` (loaded via the dedicated loader entry point that takes a context) |
| `${CLAUDE_PROJECT_DIR}` unset, no default | fixture with the placeholder, context `ProjectDir:""`, no OS env | error from `expanding command` or `expanding url` |
| `${user_config.TOKEN}` in headers resolves | plugin-style fixture with `Authorization: "Bearer ${user_config.TOKEN}"`, context with lookup returning `"abc"` | header value `"Bearer abc"` |
| `${user_config.TOKEN}` in non-plugin context errors | top-level mcp.json fixture | error `"user_config.TOKEN" is not set` |
| Plugin path: `${CLAUDE_PLUGIN_ROOT}` resolves | reuse existing plugin .mcp.json fixture; assert post-SP6 path produces the same `cfg.Command` as before | parity with current expectation |

The "loaded with `expansionContext{...}`" rows require a test-only entry
point. Add an unexported helper in the test file (not in production
code) that calls the same parser path with a fixed context. The
production `LoadMCPConfigFile` continues to construct its context from
`resolveProjectDir(nil)`, which is `os.Getwd()`-driven and harder to
pin in tests; the helper exists so tests can assert on substitution
without relying on cwd.

### 10.3 Env injection — integration test

`TestMCPManager_InjectsProjectDir` uses a real stub stdio MCP server.
Existing tests in `agent/mcp_real_test.go` already spawn external
processes; reuse that scaffolding.

Setup:

1. `t.TempDir()` → `dir`.
2. Write a tiny shell script that, when invoked, reads
   `CLAUDE_PROJECT_DIR` from its env and writes it to a sentinel file
   next to the script, then exits 0. The script need not implement MCP
   handshake fully — we only need to capture the env. (If integration
   requires a real MCP loop, swap in a small Go program in
   `agent/internal/testsupport/` that does the same.)
3. Configure `MCPServerConfig{Type:"stdio", Command:"<script>"}` and
   call `NewMCPManager` with a real `LocalExecutionEnvironment` rooted
   at `dir`.
4. Even if the manager fails to complete handshake, the script will
   have run; assert the sentinel file's contents.

Assert: sentinel content equals the value `resolveProjectDir` would
produce for that environment — when `dir` is inside a git repo
(initialize one with `git init`), that is the git root; when not, it
is `dir`.

Negative test: `TestMCPManager_RespectsExplicitProjectDirOverride`.
Same setup, but `cfg.Env` sets `CLAUDE_PROJECT_DIR=/explicit`. Assert
sentinel content is `/explicit`, proving cfg.Env overrides injection
(per §4.3).

Skip both tests with `t.Skip` if `git` or `bash` is absent, matching
the existing test conventions.

### 10.4 `resolveProjectDir` — unit test

`TestResolveProjectDir` table:

| # | Setup | Expected |
|---|---|---|
| 1 | env returns cwd, cwd inside a git repo | git root |
| 2 | env returns cwd, cwd outside any git repo | cwd |
| 3 | env is nil | `os.Getwd()` value |
| 4 | env implements `ProjectDir() string` and returns `/forced` | `/forced` |
| 5 | env implements `ProjectDir()` returning `""`, falls through | git root or cwd, per #1/#2 |
| 6 | `os.Getwd()` errors and env is nil | `""` (no panic) |

Use real `t.TempDir()` directories and shell out to `git init` for
cases #1/#5. Define a tiny test-only `ExecutionEnvironment`
implementation that adds the `ProjectDir()` method for #4/#5.

### 10.5 Coverage gate

- Every reserved name in §5.1 appears in §10.1.
- Every error in §8 appears as a table row in §10.1 or §10.2.
- §10.3 confirms the env-injection wiring under a real spawn.
- `go test ./agent/... -run 'MCP|ExpandVars|ResolveProjectDir'` is green.

## 11. Open Questions

### 11.1 `list_changed` MCP notification handling

**Verified.** `github.com/modelcontextprotocol/go-sdk@v1.3.0` (the version
in `go.mod`) exposes `ClientOptions.ToolListChangedHandler`,
`PromptListChangedHandler`, `ResourceListChangedHandler`, and
`ResourceUpdatedNotificationHandler` (see
`go-sdk/mcp/client.go:128` and `client.go:940`). The notifications are
delivered to the handler if registered. Today's serf code passes `nil`
for `ClientOptions` to `mcp.NewClient` (`agent/mcp_manager.go:38–41`),
so the handler is unset and `list_changed` notifications are silently
ignored. The discovered tool list captured at `NewMCPManager` time is
the only one serf ever sees within a session.

**Decision.** Out of SP6's A-tier scope. Capture as a B-tier follow-up:
"SP6.A — dynamic tool-list refresh." Implementation sketch (for the
follow-up, not this spec): register a `ToolListChangedHandler` that
re-calls `session.ListTools`, diffs against the namespaced toolset, and
patches the `ToolRegistry`. Diff is non-trivial because the LLM-side
`ToolDefinition` slice is built once at session init and consumed by
`session.go`; the refresh would need an invalidation channel into the
session loop. Defer until SP8 or later.

### 11.2 `roots/list` server-initiated request

**Verified.** Serf does **not** implement `roots/list`. The go-sdk
defines `RootsListChangedHandler` on `ServerOptions` (server-side) and
the `mcp.RootsListChangedRequest` type, but the client-side response to
a server-initiated `roots/list` request is not auto-wired by passing
options to `NewClient`. The Claude Code MCP docs note that a server can
call `roots/list` to discover the launch directory.

**Decision.** Out of SP6's A-tier scope. Capture as a B-tier follow-up:
"SP6.B — implement `roots/list` response." Without it, MCP servers that
rely on `roots/list` (vs. reading `CLAUDE_PROJECT_DIR` from their env)
will not see the project root from serf sessions. The high-level spec
already lists `roots/list` as deferred.

### 11.3 Should top-level `mcp.json` allow `${user_config.*}`?

Claude Code's docs are silent on whether `user_config` is a plugins-only
mechanism. SP6's choice (§5.4) is plugin-only: top-level `mcp.json`
references to `${user_config.K}` error out unless defaulted. This is
the strict reading of the documented schema. If real-world configs need
the relaxed behavior, SP7 can add a global user-config store with a
different placeholder prefix; SP6 does not block that decision.
