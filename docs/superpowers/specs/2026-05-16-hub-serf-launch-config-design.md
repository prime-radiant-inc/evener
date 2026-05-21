# Hub-Mediated Serf Launch Configuration

Date: 2026-05-16
Status: design approved; ready for implementation plan

## 1. Goal

`serf-hub` launches `serf serve` subprocesses on behalf of the web UI and the
TUI. Today the hub passes a fixed, curated set of flags
(`--addr`, `--dir`, `--state-dir`, `--run-dir`, `--model`, `--agent`,
`--reasoning-effort`, `--sse-ring-size`) and inherits credentials from its own
process environment. There is no way to configure skills directories, plugin
directories, MCP servers, MCP config files, max-rounds, context strategy,
or any other launch-time knob from either UI. Credentials are out-of-band.

This design adds:

1. A layered, schema-validated launch configuration that the hub uses to build
   `serf serve` argv and env.
2. RPCs for the web UI and the TUI to read the resolved config, edit the
   writable layers, and override per-launch.
3. A hub-managed credentials file editable from the UI.
4. A trust-on-first-use mechanism for in-repo `.serf/launch.toml` files.

No back-compat for the existing `[serf_launch]` shape in `hub.toml` — the
schema is rewritten cleanly.

## 2. Non-Goals

- **Encryption at rest** for credentials. Threat model is filesystem
  permissions, same as `~/.ssh/id_rsa`. A passphrase would block headless /
  remote operation and provides false comfort.
- **Keyring backends.** The storage layer is small and pluggable; revisit if a
  user asks.
- **MCP-mediated OAuth.** Reserved shape `mcpServer/oauth/login` and
  `mcpServer/oauthLogin/completed` documented in §5.3 (mirroring Codex's
  names exactly), not implemented in v1.
- **Raw TOML editing in the UI.** Both UIs render form-based editors.
- **Auto-walking parent directories** to find a project layer. A project layer
  applies if and only if `cwd` exactly matches a registered project.
- **Per-turn mid-thread overrides.** `serf serve` is one daemon per thread; the
  equivalent of per-turn override for us is *resume with overrides*, which
  already exists.
- **Reconciling with claude-code-compat SP1's `config.json`.** SP1 owns serf's
  internal `mcpServers`/`hooks`/`permissions` config loading. This design owns
  hub-side launch arguments. Both can apply at runtime: serf merges its own
  SP1-loaded MCPs with `--mcp` flags passed by the hub. No data flow between
  the two layers.

## 3. Architecture

A new package `internal/launchconfig` becomes the single source of truth for
"what argv and env should hub pass to `serf serve`":

```
                       ┌──────────────────────────────┐
                       │  internal/launchconfig       │
                       │                              │
   global              │  Resolver:                   │
   (~/.serf/           │    Resolve(cwd) → Resolved   │
    launch.toml)       │    with per-layer provenance │
                       │                              │
   in-repo             │  Editor:                     │
   (.serf/             │    Get/Set per writable      │
    launch.toml)       │    layer                     │
                       │                              │
   hub-side per-proj   │  Trust:                      │
   (~/.serf/projects/  │    TrustRepo(cwd, hash)      │
    <id>/launch.toml)  │                              │
                       │                              │
   per-launch          │  ToArgs(Resolved, overrides) │
   overrides    ──────►│    → ([]string, []string)    │
                       └──────────────────────────────┘
                                       │
                                       ▼
                        cmd/serf-hub/spawn.go
                        (buildSpawnArgs delegates here)
```

Layer precedence, most-general to most-specific:

1. **Global**: `~/.serf/launch.toml`. Hub-writable via UI.
2. **In-repo**: `<cwd>/.serf/launch.toml`. Read-only from the UI's perspective,
   gated by trust-on-first-use.
3. **Local per-project**: `<cwd>/.serf/launch.local.toml`. Hub-writable via
   UI and intended to stay out of version control. Legacy
   `~/.serf/projects/<id>/launch.toml` files are read only as a fallback
   when the local file is absent.
4. **Per-launch overrides**: passed in `ThreadStartParams.launchOverrides`.
   Ephemeral; never written to disk.

RPCs in `cmd/serf-hub/app_rpc.go` are added under the existing `serf/` prefix
that already houses serf-specific extensions to the Codex-shaped baseline
(see `2026-05-12-codex-app-server-protocol-and-serf-extensions.md`). Launch
config gets a new sub-namespace `serf/launch/...`; credentials reuse and
extend the existing `serf/auth/...` family (already provider-aware in
`internal/appwire/types.go:439` but wired for OpenAI only today).

Credentials are a separate concern from launch config — they don't layer,
they're hub-global, and the protocol family for them already exists. Launch
config and credentials are deliberately *not* unified into a single
namespace.

Per-project trust state lives at:

```
~/.serf/projects/<id>/
  meta.toml         # cwd, created_at, trust state
```

Personal per-project launch defaults live in
`<cwd>/.serf/launch.local.toml`. Legacy
`~/.serf/projects/<id>/launch.toml` files are read as a fallback when the
local file is absent.

## 4. Schema

### 4.1 Launch layer TOML

Each layer file uses the same shape; only writability and trust rules differ.

```toml
schema = 1

model = "openai/gpt-5"            # scalar
agent = "default"                 # scalar
reasoning_effort = "medium"       # scalar — low|medium|high|xhigh|none
context_strategy = "compact"      # scalar — compact|recall|session-log|ooda
max_rounds = 200                  # scalar
max_subagent_depth = 1            # scalar
no_project_prompts = false        # scalar
sse_ring_size = 4096              # scalar (global layer only)

skills_dirs          = ["..."]    # list, append
plugin_dirs          = ["..."]    # list, append
mcp_configs          = ["..."]    # list of paths, append
system_prompt_append = ["..."]    # list of paths, append

[[mcps]]                          # list, append
name = "github"
command = "gh-mcp"
args = ["--token-from-env", "GITHUB_TOKEN"]

[env]                             # map, last-write-wins per key, layered
FOO = "bar"
```

Pointer-like semantics for scalars: omit the key to mean "not set at this
layer." Zero values are never injected by the resolver as defaults — only
explicit values pass through.

### 4.2 Merge rules

- **Scalars**: most-specific present value wins. An omitted key at a higher
  layer does not blank a lower layer's value.
- **Lists** (`skills_dirs`, `plugin_dirs`, `mcp_configs`,
  `system_prompt_append`, `mcps`): concatenate global → in-repo →
  hub-side-project → per-launch. No deduplication. No escape hatch.
- **Map** (`env`): merge by key; most-specific layer wins per key.

`mcps` entries are *not* deduplicated by `name` at the resolver layer.
Duplicates emit a `LaunchConfigDiagnostic` (severity warn) but pass through
to serf, which is the authoritative validator — `launch-check` will surface
the conflict and the spawn fails with a clear error.

### 4.3 Field availability per layer

| Field                          | global | in-repo | hub-side proj | per-launch |
|--------------------------------|:------:|:-------:|:-------------:|:----------:|
| `sse_ring_size`                |   ✓    |    ✗    |       ✗       |     ✗      |
| `env.*` containing credentials |   ✗    |    ✗    |       ✗       |     ✗      |
| everything else                |   ✓    |    ✓    |       ✓       |     ✓      |

The credential-key blocklist (`*_API_KEY`, `*_TOKEN`, `*_SECRET`,
`*_PASSWORD`, `*_CREDENTIAL`) is enforced at parse time. Violations:

- During `Resolve()` of an existing file: surfaced as a
  `LaunchConfigDiagnostic` on the response, layer omitted from contributing.
- During `SetLayer()`: rejected with `appwire.InvalidParams`.

Forces credentials through the `serf/auth/*` API instead of leaking into a
file someone might commit.

### 4.4 Path resolution

- Paths in the global file: absolute. Hub rejects relative paths.
- Paths in the in-repo file: relative to the repo root. The resolver computes
  `filepath.Rel(repoRoot, path)`; if the result starts with `..` or is
  absolute, the entry is rejected with a diagnostic and skipped.
- Paths in the local per-project file: absolute.
- Paths in per-launch overrides: absolute. The hub validates that the caller
  has the right to point at them by requiring them to exist and be readable
  by the hub user; symlinks are followed but not validated transitively.

### 4.5 Credentials file

`~/.serf/credentials.toml`, chmod 600, hub-owned:

```toml
schema = 1

[providers.anthropic]
api_key = "sk-ant-..."

[providers.gemini]
api_key = "..."

[providers.openrouter]
api_key = "..."

# openai is typically absent: OAuth flows still write
# ~/.serf/auth/openai.json (the existing file). A user-pasted
# api_key here takes precedence over the OAuth file.
```

**Injection order at spawn time** (highest priority first):

1. Per-launch `env` overrides.
2. `[providers.<name>].api_key` from `credentials.toml`.
3. Inherited environment (parent hub process).
4. Provider-specific on-disk state (`~/.serf/auth/openai.json` for OpenAI
   OAuth).

Hub refuses to start if `~/.serf` or `credentials.toml` is group- or
world-readable. Writes are atomic via temp-file + rename.

## 5. Wire API

### 5.1 Types in `internal/appwire/types.go`

```go
type LaunchConfigLayer struct {
    Model              string            `json:"model,omitempty"`
    Agent              string            `json:"agent,omitempty"`
    ReasoningEffort    string            `json:"reasoningEffort,omitempty"`
    ContextStrategy    string            `json:"contextStrategy,omitempty"`
    MaxRounds          *int              `json:"maxRounds,omitempty"`
    MaxSubagentDepth   *int              `json:"maxSubagentDepth,omitempty"`
    NoProjectPrompts   *bool             `json:"noProjectPrompts,omitempty"`
    SSERingSize        *int              `json:"sseRingSize,omitempty"`  // global only
    SkillsDirs         []string          `json:"skillsDirs,omitempty"`
    PluginDirs         []string          `json:"pluginDirs,omitempty"`
    MCPConfigs         []string          `json:"mcpConfigs,omitempty"`
    SystemPromptAppend []string          `json:"systemPromptAppend,omitempty"`
    MCPs               []MCPServerSpec   `json:"mcps,omitempty"`
    Env                map[string]string `json:"env,omitempty"`
}

type MCPServerSpec struct {
    Name    string   `json:"name"`
    Command string   `json:"command"`
    Args    []string `json:"args,omitempty"`
}

type LaunchConfigResolved struct {
    Effective   LaunchConfigLayer            `json:"effective"`
    Layers      map[string]LaunchConfigLayer `json:"layers"`      // "global","repo","project","launch" — only contributing layers
    Provenance  map[string]string            `json:"provenance"`  // field name → topmost contributing layer
    Repo        *RepoLaunchConfigStatus      `json:"repo,omitempty"`
    Diagnostics []LaunchConfigDiagnostic     `json:"diagnostics,omitempty"`
}

type RepoLaunchConfigStatus struct {
    Path    string `json:"path"`     // canonical path to .serf/launch.toml
    Hash    string `json:"hash"`     // sha256 of normalized contents
    Trust   string `json:"trust"`    // "trusted" | "untrusted" | "absent" | "changed" | "rejected"
    Preview string `json:"preview,omitempty"`  // raw TOML when untrusted/changed
}

type LaunchConfigDiagnostic struct {
    Layer   string `json:"layer"`
    Field   string `json:"field"`
    Message string `json:"message"`
}
```

Scalars use pointer types so the wire can distinguish "not set" from "set to
zero." This is required for layering semantics.

### 5.2 RPC methods

**Launch configuration** — new sub-namespace `serf/launch/`:

| Method                  | Params                                   | Result                  |
|-------------------------|------------------------------------------|-------------------------|
| `serf/launch/resolve`   | `{cwd}`                                  | `LaunchConfigResolved`  |
| `serf/launch/getLayer`  | `{cwd, layer}` where `layer ∈ {global, project}` | `LaunchConfigLayer` |
| `serf/launch/setLayer`  | `{cwd, layer, config}`                   | `LaunchConfigResolved`  |
| `serf/launch/trustRepo` | `{cwd, hash}`                            | `LaunchConfigResolved`  |

**Credentials** — extends the existing `serf/auth/*` family. New methods are
marked **(new)**; existing methods are extended (gradually wired up beyond
OpenAI):

| Method                   | Params                            | Result                       | Status |
|--------------------------|-----------------------------------|------------------------------|--------|
| `serf/auth/list`         | `{}`                              | `AuthListResponse`           | new    |
| `serf/auth/status`       | `{provider}`                      | `AuthStatusResponse`         | existing |
| `serf/auth/login/start`  | `{provider}` (OAuth flow)         | `AuthLoginStartResponse`     | existing |
| `serf/auth/login/complete` | `{provider, flowId, redirectUrl}` | `AuthLoginCompleteResponse` | existing |
| `serf/auth/apiKey/set`   | `{provider, value}`               | `AuthStatusResponse`         | new    |
| `serf/auth/logout`       | `{provider}`                      | `AuthLogoutResponse`         | existing |

```go
type AuthListResponse struct {
    Providers []AuthStatusResponse `json:"providers"`
}

// AuthStatusResponse already exists in types.go:443 — extended with one
// field. The rest of the shape (Provider, Supported, SignedIn,
// ActiveSource, HasStoredOAuth, Email, NeedsRefresh, NeedsLogin, Error)
// stays as-is.
type AuthStatusResponse struct {
    // ...existing fields...
    AuthModes []string `json:"authModes"` // subset of ["apiKey","oauth","none"]
}

type AuthApiKeySetParams struct {
    Provider string `json:"provider"`
    Value    string `json:"value"`
}
```

`serf/auth/list` returns one `AuthStatusResponse` per supported provider so
the UI can render the credentials page from a single call.

`serf/auth/apiKey/set` writes to `~/.serf/credentials.toml`. Returns the
new `AuthStatusResponse` for the provider — same shape the UI already
parses from `serf/auth/status` — so the client can update state without a
second call.

`serf/auth/login/start` and `serf/auth/login/complete` remain the OAuth
path. Together with `serf/auth/apiKey/set`, this matches Codex's
`account/login/start` with discriminated `type: "apiKey" | "chatgpt"`
semantically, while keeping Go-friendly distinct methods and preserving
the existing OAuth wire shape.

### 5.3 Notifications

Emitted over the existing SSE event stream:

- `serf/auth/updated`: `{provider, activeSource}` — fired on any successful
  `serf/auth/apiKey/set`, `serf/auth/logout`, or OAuth completion. Mirrors
  Codex's `account/updated`.
- `serf/launch/updated`: `{cwd, layer}` — fired on `serf/launch/setLayer`
  and `serf/launch/trustRepo`. Listeners refetch via `serf/launch/resolve`.

**Reserved but not implemented in v1** (forward compatibility for MCP
OAuth — mirror of Codex's `mcpServer/oauth/login`):

- `mcpServer/oauth/login` (top-level, matching Codex's name) →
  `{name, authUrl, flowId}`
- `mcpServer/oauthLogin/completed` notification — `{name, success, error?}`

### 5.4 `ThreadStartParams` extension

```go
type ThreadStartParams struct {
    // ...existing fields kept unchanged: harness, cwd, prompt, items,
    //    modelProvider, model, profile, reasoningEffort...
    LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}
```

When both the legacy scalar fields (`model`, `profile`, `reasoningEffort`)
and `launchOverrides` are set, the explicit scalar wins. New clients should
set everything through `launchOverrides`.

### 5.5 Hub-internal types

`SpawnRequest` and `ResumeRequest` (in `cmd/serf-hub/spawn.go`) are rewritten
to carry a `Resolved LaunchConfigResolved` rather than the current ad-hoc
scalar list. `buildSpawnArgs` becomes `launchconfig.ToArgs(resolved)`; the
child env construction in `buildSerfChildEnv` is replaced by
`launchconfig.ToEnv(resolved, credentials)` which applies the priority
ordering in §4.5 and merges the layered `env` map on top. Both `Spawn` and
`Resume` flow through the same resolver, so resumed sessions pick up
current config layers rather than the layers that were active at original
spawn.

## 6. UI Surfaces

### 6.1 Web (Hub's embedded UI)

**`/new` enhancements** (existing route):

```
┌─ New thread ────────────────────────────────────┐
│ Working dir: [/home/me/proj            ▼]       │
│ Model:       [openai/gpt-5            ▼]        │
│ Agent:       [default                 ▼]        │
│ Reasoning:   [medium                  ▼]        │
│                                                 │
│ ▶ Advanced (uses defaults from /settings)       │
│                                                 │
│ [Start thread]                                  │
└─────────────────────────────────────────────────┘
```

When expanded, the Advanced section shows form inputs for every
`LaunchConfigLayer` field except `sse_ring_size`. A "Show resolved config"
toggle calls `serf/launch/resolve` and renders the layer-by-layer
breakdown with provenance.

**`/settings`** (new route):

- **Global tab**: edits `~/.serf/launch.toml`. Form-based.
- **Project tab**: edits `<cwd>/.serf/launch.local.toml`. Only shown when a
  project is selected. Form-based.
- **In-repo tab**: read-only view of `.serf/launch.toml`. Banner appears
  when `trust ∈ {untrusted, changed}` with a preview and an "Apply and
  trust" button calling `serf/launch/trustRepo`. When `trust = rejected`,
  the tab is reachable but quiet — the preview shows with a muted
  "previously rejected; trust to apply" note instead of a banner. Tab
  hidden when `trust = absent`.

**`/credentials`** (new route):

```
┌─ Provider credentials ──────────────────────────────┐
│ OpenAI    ✓ Configured via OAuth (expires 2026-06)  │
│           [Refresh] [Sign out]                      │
│                                                     │
│ Anthropic ✓ Configured via env (ANTHROPIC_API_KEY)  │
│           [Replace with stored key] [Clear]         │
│                                                     │
│ Gemini    ✗ Not configured                          │
│           [Set API key] [Sign in...]                │
│                                                     │
│ Ollama    — No credentials needed                   │
└─────────────────────────────────────────────────────┘
```

Source labels (`env`, `file`, `oauth`, `absent`, `none`) come straight from
`serf/auth/list`. The UI never displays the key value. A
`credentials/updated` SSE event reloads the panel.

### 6.2 TUI

The TUI already has a command palette (`cmd/serf-tui/command_palette.go`).
Two new commands:

- **`:settings`** — opens a modal panel with the same three tabs as the web
  `/settings` route. Form-based, arrow keys + tab.
- **`:credentials`** — opens the credentials panel. For OAuth, prints the
  URL and the redirect-paste prompt (mirrors the existing CLI
  `serf openai login` flow).

For per-launch overrides, the composer panel gets `Ctrl-L` to open a small
modal that lets you set per-launch fields just for *this* `:new`. Same
partial `LaunchConfigLayer` payload as the web Advanced panel.

When in-repo config is `untrusted` or `changed`, the TUI surfaces a
status-bar warning (`⚠ .serf/launch.toml present but untrusted — :settings
→ In-repo to review`) and proceeds with global + hub-side-project only.
When `rejected`, the TUI is silent — the user already said no — but
`:settings` → In-repo remains reachable.

## 7. Trust-on-First-Use

`meta.toml` under `~/.serf/projects/<id>/`:

```toml
schema = 1
cwd = "/home/jesse/git/prime-radiant/serf"
created_at = "2026-05-16T..."

[trust]
hash = "sha256:abc..."
decision = "trusted"            # "trusted" | "rejected"
decided_at = "2026-05-16T..."
```

**Hash**: SHA-256 of the canonicalized TOML — parsed via the TOML library,
re-serialized with sorted keys — so whitespace edits don't re-prompt but
semantic changes do.

**Trust states** in `RepoLaunchConfigStatus.Trust`:

- `absent`: no `.serf/launch.toml` in repo.
- `untrusted`: file present, no decision recorded.
- `trusted`: file present, hash matches recorded trusted hash.
- `changed`: file present, hash differs from recorded hash.
- `rejected`: user explicitly said no; layer skipped on every spawn until
  re-trusted via `/settings`.

`serf/launch/trustRepo` requires the client to echo the hash
it saw, preventing a TOCTOU race where the file changes between display and
approval. A hash mismatch returns `appwire.Conflict("file changed since
review")`.

Hub re-reads `.serf/launch.toml` and rehashes on each `serf/launch/resolve`
call and on each spawn. Cheap; avoids fs watchers in v1.

## 8. Testing

- **`internal/launchconfig` unit tests**: layer merge with every field type
  (scalar / list / map), credential-blocklist enforcement, in-repo `..` path
  rejection, TOFU hash stability across whitespace-only edits, `ToArgs()`
  snapshot tests covering every flag combination.
- **`cmd/serf-hub` RPC tests**: each new RPC gets happy-path, invalid-cwd,
  permission-denied, and concurrent-write coverage. Follows the existing
  pattern in `app_rpc_test.go`.
- **`cmd/serf-hub/spawn_test.go` updates**: assert the spawned `serf serve`
  argv matches the merged config; credentials are injected in the
  priority order in §4.5; rejected in-repo trust skips the layer; provider
  credential validation honors `credentials.toml`.
- **End-to-end** (`cmd/serf-hub/e2e_test.go` pattern): temp `HOME` with
  global + hub-side + in-repo configs, spawn a real `serf launch-check`
  through the hub, assert the resolved config matches what `serf` sees via
  `launch-check --json`.
- **TUI** (`cmd/serf-tui/`): extend `tmux_e2e_test.go` and panel tests for
  `:settings`, `:credentials`, and `Ctrl-L`. Use the existing
  `embedded_test.go` pattern of pointing the TUI at an in-process hub
  fixture.
- **Web** (`cmd/serf-hub/web_test.go` + `jstest/`): add tests for the
  Advanced panel, Settings tabs, and Credentials route. Mock the SSE
  channel to verify `credentials/updated` reloads the panel.

## 9. Open questions resolved by this design

- "Where do plugin dirs / skills / MCPs / max-rounds / etc. come from?" →
  Layered launch config; resolver merges into `serf serve` argv.
- "How does the UI configure provider credentials?" → extensions to the
  existing `serf/auth/*` family, backed by `~/.serf/credentials.toml`.
- "Can a repo ship a launch config?" → Yes, via `.serf/launch.toml`,
  TOFU-gated.
- "How do power users still hand-edit?" → Files are plain TOML; UI is just
  one writer.

## 10. Out of scope; deferred to follow-up specs

- MCP-mediated OAuth (`mcps.startOAuth` / `mcps/updated`).
- Bulk import/export of layers as "presets."
- Auto-walking parent directories to find a project layer.
- Encryption at rest / keyring backends for credentials.
- Reconciliation with claude-code-compat SP1's `config.json` for MCPs.
