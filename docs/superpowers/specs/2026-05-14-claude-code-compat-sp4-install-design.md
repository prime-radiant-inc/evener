# SP4 — Plugin Install, Uninstall, Update (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
Depends on: SP1 (config loader), SP3 (marketplace resolution)

## 1. Goal

SP4 owns the plugin lifecycle: install, uninstall, update, list, enable, disable. It maintains the on-disk install state under `~/.config/serf/plugins/`, mirrors Claude Code's `installed_plugins.json` shape, and exposes a `serf plugin` subcommand tree that mirrors `claude plugin`'s CLI surface so existing scripts and muscle memory transfer.

SP4 does not parse marketplace catalogs, does not fetch plugin source bytes, and does not load plugin contents at session startup. It calls SP3 to resolve a `(plugin, marketplace)` pair to a fetched payload directory and records the result. SP1 supplies the merged `SerfConfig`; SP4 updates the `enabledPlugins` field on disk in the appropriate scope's `config.json`. SP8 reads that field at session startup and asks SP4 for the cache path of every enabled plugin.

## 2. Public API Surface

All install logic lives in package `internal/plugins`. The CLI lives in `cmd/serf/plugin`. Symbol naming follows the existing `agent/mcp_config.go` triad: `Load…`, `Merge…`, `Discover…`.

### 2.1 Registry types

```go
// Registry is the parsed contents of installed_plugins.json. It is the single
// source of truth for which plugins are installed on this machine.
type Registry struct {
    // Version is the schema version. Always 2 for the schema this package
    // writes. We accept 1 on read for forward-compat with a hypothetical
    // future migration; SP4 ships with version 2 only.
    Version int

    // Plugins maps "plugin@marketplace" to an ordered list of install
    // entries — one per scope the plugin is installed at. The order is
    // user → project → local (deferred); writing preserves insertion order.
    Plugins map[string][]InstallEntry
}

// InstallEntry is one installation of one plugin at one scope.
type InstallEntry struct {
    Scope        Scope     // user | project | local
    InstallPath  string    // absolute path to cache/<mkt>/<plugin>/<version>/
    Version      string    // resolved version (see §9)
    InstalledAt  time.Time // first install at this scope
    LastUpdated  time.Time // last successful install/update at this scope
    GitCommitSha string    // commit SHA of the source at install time, "" if unknown
}

type Scope string

const (
    ScopeUser    Scope = "user"
    ScopeProject Scope = "project"
    // ScopeLocal and ScopeManaged are reserved; SP4 v1 accepts them in
    // installed_plugins.json round-trips but rejects them as inputs to
    // install/uninstall/enable/disable with a clear error.
    ScopeLocal   Scope = "local"
    ScopeManaged Scope = "managed"
)
```

### 2.2 Registry IO

```go
// LoadRegistry reads installed_plugins.json from registryPath. Missing file
// returns an empty Registry{Version: 2} and a nil error. Malformed file is a
// hard error annotated with the file path.
func LoadRegistry(registryPath string) (Registry, error)

// SaveRegistry writes r to registryPath atomically: marshal to JSON, write to
// <path>.tmp.<pid>, fsync, rename over <path>. The directory is created if
// missing (mode 0755). The caller holds the registry file lock (see §11).
func SaveRegistry(registryPath string, r Registry) error

// DefaultRegistryPath returns "~/.config/serf/plugins/installed_plugins.json"
// for the current user. Honors XDG_CONFIG_HOME the same way SP1 does.
func DefaultRegistryPath() string
```

### 2.3 Install operations

```go
// Installer is the entry point for every mutating operation. It owns the
// registry file lock and the cache root. Construct once per CLI invocation.
type Installer struct {
    RegistryPath string                  // typically DefaultRegistryPath()
    CacheRoot    string                  // typically ~/.config/serf/plugins/cache
    GlobalConfig string                  // ~/.config/serf/config.json
    ProjectRoot  string                  // git-root or "" if no project
    Marketplaces MarketplaceResolver     // SP3-provided, see §2.4
    Now          func() time.Time        // injectable for tests
    Stderr       io.Writer               // for human-readable progress
}

// InstallRequest names one plugin to install at one scope.
type InstallRequest struct {
    Plugin      string  // bare plugin name or "plugin@marketplace"
    Marketplace string  // overrides "@marketplace" suffix when set
    Scope       Scope   // ScopeUser default
    Version     string  // optional explicit version to fetch (semver or git ref)
    Pin         bool    // record {version:"..."} in enabledPlugins instead of true
    Force       bool    // re-install even if cache dir already populated
}

// InstallResult describes one completed install. Used by the CLI to render
// human-readable output and by --json mode.
type InstallResult struct {
    Plugin      string
    Marketplace string
    Scope       Scope
    Version     string
    InstallPath string
    Enabled     bool   // true if this op also flipped enabledPlugins on
    AlreadyAt   bool   // true if Force was false and target version was present
}

// Install fetches, validates, registers, and (by default) enables one plugin.
// Returns InstallResult on success; on failure, rolls back any partial state
// per §4.6 and returns a wrapped error.
func (i *Installer) Install(ctx context.Context, req InstallRequest) (InstallResult, error)

// UninstallRequest names one installed plugin to remove.
type UninstallRequest struct {
    Plugin      string  // bare or "plugin@marketplace"
    Marketplace string  // overrides suffix
    Scope       Scope   // ScopeUser default
    KeepData    bool    // reserved; SP4 has no data dir yet (see §15)
}

// Uninstall disables (removes from enabledPlugins at the chosen scope), removes
// the registry entry for that scope, and — if no other scope still installs
// this plugin@marketplace at the same version — removes the cache directory.
func (i *Installer) Uninstall(ctx context.Context, req UninstallRequest) error

// UpdateRequest re-fetches the current head of the plugin's source. If the
// resolved version equals the installed version, the call is a no-op unless
// Force is set.
type UpdateRequest struct {
    Plugin      string
    Marketplace string
    Scope       Scope
    Force       bool
}

// Update installs the new version side-by-side, swaps the registry entry, and
// garbage-collects the previous version's cache dir if no other scope or
// plugin@marketplace pair references it.
func (i *Installer) Update(ctx context.Context, req UpdateRequest) (InstallResult, error)

// UpdateAll updates every installed plugin at the given scope, continuing past
// per-plugin failures. Returns the per-plugin results and a multi-error.
func (i *Installer) UpdateAll(ctx context.Context, scope Scope) ([]InstallResult, error)

// EnableRequest and DisableRequest toggle the enabledPlugins field at a scope.
// Both require the plugin to already be installed at that scope.
type EnableRequest  struct{ Plugin, Marketplace string; Scope Scope; Pin bool }
type DisableRequest struct{ Plugin, Marketplace string; Scope Scope }

func (i *Installer) Enable(ctx context.Context, req EnableRequest) error
func (i *Installer) Disable(ctx context.Context, req DisableRequest) error

// List returns every entry in the registry, optionally filtered by scope.
// Pure read — no lock held.
func (i *Installer) List(ctx context.Context, scope Scope) ([]ListEntry, error)

// ListEntry is the rendered view of one (plugin@marketplace, scope) tuple.
// It carries the registry data plus the enabled bit from config.json.
type ListEntry struct {
    Plugin       string
    Marketplace  string
    Scope        Scope
    Version      string
    InstallPath  string
    InstalledAt  time.Time
    LastUpdated  time.Time
    GitCommitSha string
    Enabled      bool
}
```

### 2.4 Borrowed from SP3

SP3 owns marketplace parsing and source fetching. SP4 consumes a single interface; SP3 supplies the production implementation and the test stub.

```go
// MarketplaceResolver locates a marketplace and resolves a plugin entry to a
// fetchable source. SP3 owns the implementation. SP4 owns this interface so
// the install package can stub it in tests without pulling in SP3.
type MarketplaceResolver interface {
    // Resolve returns the source descriptor and the marketplace-declared
    // version (may be ""). pluginName and marketplaceName are both required;
    // if the caller passed "name@mkt" it is split before this call.
    Resolve(ctx context.Context, pluginName, marketplaceName string) (PluginSource, error)
}

// PluginSource is the abstracted, source-type-erased view of a plugin's bytes.
// SP3 produces it; SP4 calls Fetch and validates the result.
type PluginSource interface {
    // Fetch copies the plugin payload into destDir. destDir must exist, be
    // empty, and live under CacheRoot. Implementations must be atomic from
    // SP4's standpoint: on error, leave destDir empty so SP4's rollback step
    // can rmdir it cleanly.
    Fetch(ctx context.Context, destDir string) error

    // Version returns the version SP3 was able to resolve for this source.
    // Precedence: explicit "version" in plugin.json (post-fetch) wins, but
    // SP3 may also know a marketplace-declared version. SP4 reconciles per §9.
    DeclaredVersion() string

    // CommitSHA returns the git commit SHA at the source's current ref, or
    // "" for non-git sources (directory not in a repo). SP4 uses this as a
    // version fallback per §9.
    CommitSHA() string
}
```

`MarketplaceResolver` is the only SP3 type SP4 depends on. If SP3's spec lands with a different name, SP4 renames here before implementation; coordination point flagged in §15.

## 3. installed_plugins.json Schema

The schema is identical to Claude Code's, byte for byte where it matters. We accept Claude Code's example files as drop-in test fixtures.

### 3.1 Top-level

```json
{
  "version": 2,
  "plugins": {
    "plugin-name@marketplace-name": [ ...entries... ]
  }
}
```

| Field      | Type            | Required | Notes |
|---|---|---|---|
| `version`  | integer         | yes      | Schema version. SP4 writes `2`. Reads accept `1` and `2`; `1` migrates in-memory by no-op. |
| `plugins`  | object          | yes      | Keys are `plugin@marketplace`; values are arrays of install entries. |

### 3.2 Install entry (per scope)

```json
{
  "scope": "user",
  "installPath": "/Users/.../plugins/cache/<mkt>/<plugin>/<version>",
  "version": "1.2.0",
  "installedAt": "2026-05-14T17:33:11.512Z",
  "lastUpdated": "2026-05-14T17:33:11.512Z",
  "gitCommitSha": "6d3752c000e2b3d0e6137bd7adb04895d6f40f14"
}
```

| Field          | Type        | Required | Notes |
|---|---|---|---|
| `scope`        | string enum | yes | `user`, `project`, `local`, `managed`. SP4 v1 writes only `user` or `project`. |
| `installPath`  | string      | yes | Absolute path to the cache dir for this version. Always points inside `CacheRoot`. |
| `version`      | string      | yes | Resolved version per §9. `"unknown"` is legal. |
| `installedAt`  | RFC 3339 string | yes | First install at this scope. Never updated after install. |
| `lastUpdated`  | RFC 3339 string | yes | Last successful install or update at this scope. Equals `installedAt` until first update. |
| `gitCommitSha` | string      | no  | SHA of the source HEAD at install time. Empty string for `directory` sources not in a git repo. Omittable from JSON; reads tolerate absence. |

### 3.3 Invariants

- Every `installPath` must point at a directory under `CacheRoot`. Any entry violating this is rejected at load time and logged.
- The array under `plugins["x@y"]` has at most one entry per scope value. Duplicates are a load-time error.
- `installPath` always encodes the version as the final path component, so two scopes installing the same `(plugin, marketplace, version)` triple share an `installPath`.
- `version == "unknown"` is reserved for `directory` sources outside a git repo. Any other source must produce either a `plugin.json` version or a non-empty `gitCommitSha`.

### 3.4 Atomic write contract

`SaveRegistry`:

1. Marshal `r` to a stable byte order — keys sorted alphabetically, indent 2 spaces, trailing newline. Determinism matters for diffability if the user ever inspects the file.
2. Write to `<path>.tmp.<pid>.<rand>`. The randomness lets two concurrent writers (which §11 forbids, but defense-in-depth) not collide on the tmp name.
3. `fsync` the tmp file.
4. `os.Rename(tmp, path)`. On POSIX this is atomic relative to readers.
5. `fsync` the parent directory.

Any failure between steps 2 and 4 leaves the previous registry intact and the tmp file dangling. The caller's deferred lock release runs regardless; the tmp file is GC'd by `pruneStaleTmp` on the next `LoadRegistry` (matching age threshold: 1 hour, see §11).

## 4. Install Algorithm

Pre-condition: caller holds the registry file lock (§11). `Installer.Install` acquires and releases the lock around the whole sequence.

```
Install(req) =
  1. parsePluginSpec    — split "name@marketplace" → (name, marketplace)
  2. resolveMarketplace — SP3 lookup, get PluginSource
  3. computeVersion     — apply §9 rules using DeclaredVersion + CommitSHA
  4. createCacheDir     — mkdir CacheRoot/<mkt>/<plugin>/<version>/
  5. fetch              — PluginSource.Fetch into cache dir
  6. validate           — plugin.json sanity (§4.5)
  7. register           — upsert (plugin@mkt, scope) entry in Registry, atomic save
  8. enable (optional)  — set enabledPlugins[plugin@mkt]=true or {version} in config.json
  9. report             — return InstallResult
```

### 4.1 parsePluginSpec

Split on the last `@` in `req.Plugin`. If `req.Marketplace` is set and the spec has a suffix, the explicit field wins; mismatched suffix vs flag is a hard error (`plugin spec "x@a" but --marketplace "b"`). If neither side provides a marketplace, error: `plugin "x" requires a marketplace; pass plugin@marketplace or --marketplace <name>`. SP4 v1 does not search across marketplaces — Claude Code's bare-name resolution is deferred.

### 4.2 resolveMarketplace

Call `i.Marketplaces.Resolve(ctx, pluginName, marketplaceName)`. Any error propagates with the prefix `resolving plugin "<plugin>@<marketplace>": <err>`. If the marketplace is unknown to SP3, the user-facing error matches Claude Code's wording: `plugin "<plugin>@<marketplace>" is not in any known marketplace; run 'serf plugin marketplace add ...'`.

### 4.3 computeVersion

See §9 for the full rule. Conceptually:

```
v := req.Version
if v == "" {
    v = source.DeclaredVersion()   // empty unless plugin.json/marketplace declared one
}
if v == "" {
    v = source.CommitSHA()         // empty for non-git directory sources
}
if v == "" {
    v = "unknown"
}
```

`req.Version != ""` is honored verbatim and recorded as the version, but only after we verify post-fetch that the plugin actually checks out at that ref or that `plugin.json.version == req.Version`. If they disagree, the install rolls back and the error names both values.

### 4.4 createCacheDir

Target path: `CacheRoot/<marketplace>/<plugin>/<version>/`. Path components are sanitized: a marketplace or plugin name containing path separators or `..` is rejected (`invalid plugin name "..foo"`).

If the target directory already exists and is non-empty:

- If `req.Force == false` and the same `(plugin, marketplace, scope, version)` is already in the registry, short-circuit: return `InstallResult{AlreadyAt: true}` after re-enabling at the requested scope if needed.
- If `req.Force == false` and the directory exists but no registry entry references it (orphan), wipe the directory and proceed. Log one line: `removing orphan cache dir <path>`.
- If `req.Force == true`, wipe and proceed regardless.

If the target directory does not exist, create it (and any missing parents) at mode 0755.

### 4.5 validate

After `PluginSource.Fetch` returns, SP4 performs minimal structural validation. SP7 owns full `plugin.json` schema validation; SP4 only checks the bytes are syntactically a JSON object and contain the fields SP4 itself needs.

Required post-fetch checks:

- The cache dir is non-empty (at least one file). An empty fetch is a hard error: SP3 misbehaved.
- If `plugin.json` exists at the cache-dir root, it parses as a JSON object. SP4 reads `plugin.json.version` only; everything else passes through to SP7.
- If `req.Version` is set and `plugin.json.version` is set, they must match.

`plugin.json` is not strictly required at install time — some marketplaces ship plugins without one and rely on the directory structure alone. The validation is "if it exists, it must parse." A missing `plugin.json` causes the version to fall back to commit SHA per §9.

### 4.6 register, rollback contract

After validation succeeds:

1. `LoadRegistry` (under lock — caller already holds it).
2. Upsert the entry: replace any prior entry with the same `(plugin@marketplace, scope)` tuple. Preserve `installedAt` from the prior entry if it exists; set `lastUpdated` to `i.Now()`. For first installs, both timestamps equal `i.Now()`.
3. `SaveRegistry`.

Rollback covers every failure in steps 1–8 of §4:

- Failure in steps 1–3 (no filesystem changes yet): return error directly.
- Failure in steps 4–5 (cache dir created, fetch partial or failed): `os.RemoveAll` the cache dir. On the rmtree failure, log it but still return the original install error.
- Failure in step 6 (validation): `os.RemoveAll` the cache dir as above.
- Failure in step 7 (registry write): `os.RemoveAll` the cache dir. The registry is unchanged because `SaveRegistry` is atomic — a failure leaves the prior file in place.
- Failure in step 8 (enable): the install itself is complete and recorded; we do not unwind. Return the install success plus a wrapped error mentioning the enable failure. The CLI surfaces both.

Step 8 (enable) is non-atomic with steps 1–7 by design: enable is a separate operation users can retry without re-fetching. SP4 makes a best-effort write but tolerates failure.

### 4.7 enable (optional during install)

By default, `Install` also enables the plugin at the same scope. Pass `req.Pin == true` to write `{version: "..."}` instead of `true` in `enabledPlugins`; otherwise `true` is written. See §7 for the underlying enable algorithm and §14.1 for the open-question resolution.

A `--no-enable` flag at the CLI suppresses step 8 entirely. Used by scripts that install many plugins and want to flip them on atomically afterwards.

## 5. Uninstall Algorithm

```
Uninstall(req) =
  1. parsePluginSpec
  2. requireInstalled    — entry must exist at scope, else error
  3. disable             — remove from enabledPlugins at scope in config.json
  4. unregister          — remove the (plugin@mkt, scope) entry; atomic save
  5. gcCacheDir          — if no other scope references the same installPath,
                           os.RemoveAll(installPath); also rmdir empty parents
                           up to but not including CacheRoot
```

Step 3 is idempotent: a missing `enabledPlugins` entry is not an error. Step 4 is the source of truth for "installed?"; if step 4 succeeds, the plugin is uninstalled even if the config.json write in step 3 fails. We log the step-3 failure but proceed.

Step 5 garbage-collects the cache dir conservatively. Two scopes can share the same `installPath` (same version installed at user and project scope). We delete only when no registry entry of any scope still points at that path. Parent directory cleanup walks upward, stopping at the first non-empty parent or at `CacheRoot`.

Failures in step 5 do not abort uninstall — the plugin is no longer installed once the registry is updated. We log and continue. A leftover cache dir is harmless; the next `serf plugin install` for that version reuses it (via the "already at" short-circuit).

`--keep-data` is reserved for SP7's plugin data dir feature and is a no-op in SP4 v1 (no data dirs exist). The flag is accepted at the CLI for forward compatibility but logs a warning the first time per session: `--keep-data is reserved; serf does not maintain plugin data directories yet`.

`--prune` (Claude Code's auto-remove of orphaned dependencies) is **rejected** at the CLI with a clear error: `--prune is not supported; serf v1 does not auto-install plugin dependencies`. Plugin dependencies are deferred per the parent spec.

## 6. Update Algorithm

`serf plugin update` re-fetches the source and installs side-by-side at the new version, then swaps enable.

```
Update(req) =
  1. parsePluginSpec
  2. requireInstalled       — entry must exist at scope
  3. resolveMarketplace     — re-query SP3 for the current source
  4. computeNewVersion      — apply §9 to the re-fetched source
  5. if newVersion == oldVersion && !Force: no-op, return AlreadyAt=true
  6. createCacheDir         — new version's path
  7. fetch + validate
  8. swapRegistry           — replace entry's installPath/version/gitCommitSha,
                              update lastUpdated, preserve installedAt
                              (atomic save)
  9. updateEnabledIfPinned  — if config.json's enabledPlugins is {version:"..."},
                              rewrite to the new version; if it's `true`, leave alone
 10. gcOldCacheDir          — if no other registry entry references oldInstallPath
```

Rollback: failures in steps 6–7 wipe the new cache dir. Failures in step 8 leave the old install untouched (atomic save protects the registry). Failures in step 9 are logged; the install is still considered updated because the registry advanced. Failures in step 10 are logged.

Step 9 is the only place SP4 mutates `enabledPlugins` outside of explicit enable/disable. The choice is deliberate: a user who pinned a version is asking for reproducibility, and an update intentionally walks the pin forward. Users who want immutable pins should not run `update`.

### 6.1 update --all

`serf plugin update --all [--scope <scope>]` updates every installed plugin at the given scope (default `user`). Iteration is sequential, in the same order keys are sorted in `installed_plugins.json` (alphabetical by `plugin@marketplace`).

**Stance (resolves §14.2):** continue past failures. Each plugin's update is independent. Per-plugin errors collect into a `multierr`. The CLI prints a per-plugin status line as each finishes (`✓ plugin@mkt: 1.2.0 → 1.3.0`, `✗ plugin@mkt: <err>`) and a summary at the end (`updated 7 of 9 plugins`). Exit code: 0 if all succeed, 1 if any failed. JSON mode emits an array of `UpdateResult` entries with embedded errors.

Rationale: a single broken upstream marketplace should not stop the user from updating the other eight plugins. This matches `apt upgrade` and `brew upgrade` ergonomics.

## 7. Enable / Disable Algorithm

Both write directly to one tier of `config.json`, never to merged state.

```
Enable(req) =
  1. parsePluginSpec
  2. requireInstalled — installation at the requested scope must exist
  3. configPath := pathForScope(req.Scope)
  4. cfg := loadRawConfig(configPath)  // empty if file absent
  5. cfg.enabledPlugins["plugin@marketplace"] =
        req.Pin ? {"version": <installed-version>} : true
  6. writeRawConfig(configPath, cfg)    // atomic, mkdir-p of parent

Disable(req) = same but step 5 deletes the key. If the key is absent,
              the call is a successful no-op.
```

`pathForScope`:

- `ScopeUser` → `~/.config/serf/config.json` (i.e. SP4's `i.GlobalConfig`).
- `ScopeProject` → `<i.ProjectRoot>/.serf/config.json`; requires `i.ProjectRoot != ""`. If empty, error: `--scope project requires a git repository`.
- `ScopeLocal`, `ScopeManaged` → rejected as unsupported in v1.

`loadRawConfig` and `writeRawConfig` read/write the raw JSON object, preserving any fields SP4 does not own. Their implementation lives in `internal/plugins/config_rewrite.go`. They do not parse hooks/mcpServers/permissions semantically — they treat the JSON as a `map[string]json.RawMessage` with a destructured `enabledPlugins` field, exactly mirroring SP1's read shape but for the writeback direction.

Atomicity: same tmp+rename dance as `SaveRegistry`. If the file did not previously exist, `writeRawConfig` creates a minimal `{ "enabledPlugins": { ... } }`.

`requireInstalled` checks SP4's registry, not the cache dir. The install/enable decoupling means a user can install at user scope and then `serf plugin enable plugin@mkt --scope project` to flip on for the project — but only if the registry shows the plugin installed at project scope. Cross-scope enable (enable at project, installed at user only) is rejected with a clear error suggesting `serf plugin install --scope project ...`.

## 8. CLI Surface

The subcommand tree lives at `cmd/serf/plugin/install.go` and is registered under the root `serf plugin` group alongside SP3's `serf plugin marketplace`. Cobra-style flags, matching the rest of the serf CLI.

### 8.1 Commands

```
serf plugin install <plugin>[@<marketplace>] [flags]
serf plugin uninstall <plugin>[@<marketplace>] [flags]    (aliases: remove, rm)
serf plugin update <plugin>[@<marketplace>] [flags]
serf plugin update --all [flags]
serf plugin enable <plugin>[@<marketplace>] [flags]
serf plugin disable <plugin>[@<marketplace>] [flags]
serf plugin list [flags]
```

### 8.2 Flags

| Flag | Applies to | Default | Description |
|---|---|---|---|
| `-s, --scope <user\|project>` | install, uninstall, update, enable, disable, list | `user` | Target scope. `local` and `managed` are rejected with "not yet supported in serf". |
| `--marketplace <name>` | install | "" | Marketplace name when not encoded in the plugin spec. Mismatch with `@suffix` is an error. |
| `--version <ver>` | install | "" | Pin install to a specific version. Verified against `plugin.json.version` post-fetch. |
| `--pin` | install, enable | false | Write `{"version": "..."}` to `enabledPlugins` instead of `true`. |
| `--no-enable` | install | false | Install and register only; do not flip enabledPlugins. |
| `--force` | install, update | false | Re-fetch and overwrite even if the target version is already cached. |
| `--all` | update | false | Update every installed plugin at the scope. Mutually exclusive with a `<plugin>` argument. |
| `--keep-data` | uninstall | false | Accepted but no-op in v1; warns once per session. |
| `--prune` | uninstall | false | **Rejected** with a clear error in v1. |
| `--json` | list, update --all | false | Machine-readable output. |
| `--available` | list | false | Requires `--json`. Includes available-but-not-installed plugins from known marketplaces. Calls SP3 to enumerate. |

`--scope project` requires the cwd to be inside a git repo; otherwise the command exits 2 with `--scope project requires a git repository (no .git found from <cwd>)`.

### 8.3 Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | At least one plugin operation failed (per-plugin error, registry write error, etc.). |
| 2 | Usage error (bad flag, missing arg, ambiguous spec, unsupported scope). |
| 3 | I/O failure on `installed_plugins.json` or `config.json` (file lock contention, permission denied, disk full). |
| 4 | Marketplace resolution failed (SP3 returned not-found or fetch error). |

The CLI runs a single registry lock per invocation. Lock contention exits 3 after waiting `LockTimeout` (default 30s, see §11).

### 8.4 Output format

Human (default):

```
$ serf plugin install formatter@my-marketplace
Resolving plugin "formatter" from marketplace "my-marketplace"...
Fetching version 1.2.0 (sha 6d3752c)...
Installed formatter@my-marketplace 1.2.0 to user scope.
Enabled.
```

`--json` for `install` / `update` / `update --all`:

```json
{
  "ok": true,
  "results": [
    {
      "plugin": "formatter",
      "marketplace": "my-marketplace",
      "scope": "user",
      "version": "1.2.0",
      "installPath": "/Users/.../plugins/cache/my-marketplace/formatter/1.2.0",
      "enabled": true,
      "alreadyAt": false
    }
  ]
}
```

`--json` for `list`:

```json
{
  "plugins": [
    { "plugin": "...", "marketplace": "...", "scope": "user",
      "version": "1.2.0", "enabled": true,
      "installPath": "...", "installedAt": "...",
      "lastUpdated": "...", "gitCommitSha": "..." }
  ]
}
```

Errors in `--json` mode are written to stdout as `{"ok": false, "error": "..."}` with the appropriate exit code. Stderr is reserved for human-readable progress text.

## 9. Version Resolution

Version is the cache key. Two installations of the same `(plugin, marketplace)` at the same resolved version share the same cache dir.

Resolution rules, evaluated in order. First non-empty result wins.

1. **`--version <v>` flag on install.** Pinned by the user. Post-fetch verification: if the source provides a `plugin.json` and that `plugin.json` has a `version` field, the two strings must match exactly. SP4 does not interpret semver here — exact equality only. Mismatch rolls back and errors.
2. **`plugin.json.version`** in the fetched cache dir. The plugin author's stated version.
3. **Marketplace-declared version** via `PluginSource.DeclaredVersion()`. SP3 reads `marketplace.json`'s per-plugin `version` field, if present.
4. **`PluginSource.CommitSHA()`** — first 12 chars of the git commit at the source's resolved ref. Truncation matches Claude Code's observed behavior (`f458cee31a75` for a SHA-versioned plugin).
5. **`"unknown"`** — the literal string. Reserved for `directory` sources outside any git repo.

### 9.1 Edge cases

| Case | Resolution |
|---|---|
| No `plugin.json` at all | Skip rule 2; fall through. SP3 still produces `CommitSHA` for git sources. |
| `plugin.json` exists but no `version` field | Skip rule 2; fall through. |
| `plugin.json` exists but `version` is not a string | Hard error: `plugin.json: "version" must be a string, got <type>`. Roll back. |
| `directory` source, dir is a git repo | Rule 4 applies, version is short SHA. |
| `directory` source, dir is not a git repo | Rule 5 applies, version is `"unknown"`. SP3 reports `CommitSHA()==""`. |
| `directory` source, in git, with uncommitted changes | Rule 4 applies; `CommitSHA` is HEAD's SHA. SP4 does not detect dirty state. Adding `--allow-dirty` is deferred. |
| `git` / `github` / `git-subdir` source, fetch fails | The `Install` call errors at step 5; rollback wipes the empty cache dir. No version is recorded. |
| `git` source, repo cloned but no commits (impossibly rare) | SP3 returns `CommitSHA()==""`; rule 5 applies. SP4 logs a warning. |
| Two installs disagree on version | Each gets its own cache dir under the version string they resolve to. Disk usage scales with versions; SP4 makes no attempt to dedupe content-equal cache dirs. |

The "`unknown`" version is mutable: every subsequent install or update at "unknown" version re-fetches and overwrites the same cache dir. The directory is, in effect, a single mutable slot. This matches Claude Code's observed behavior (`/Users/jesse/.claude/plugins/cache/claude-plugins-official/agent-sdk-dev/unknown/` is a single directory with `lastUpdated` advancing over time even though `version` stays `"unknown"`).

## 10. Concurrency

### 10.1 Registry file lock

A single advisory file lock at `<RegistryPath>.lock` serializes every mutating operation. The lock is held for the entirety of `Install`, `Uninstall`, `Update`, `Enable`, `Disable`. `List` does not acquire the lock — it tolerates a stale read because the JSON loader either reads a complete prior snapshot or a complete new snapshot (atomic rename guarantees there is no in-between).

Implementation: `golang.org/x/sys/unix` flock on POSIX, `LockFileEx` on Windows. We already have a precedent in the codebase if the existing `.serf/` session lock pattern is reused; otherwise add `github.com/gofrs/flock` as a dependency.

Lock acquisition uses an exponential backoff up to `LockTimeout` (default 30s, configurable via `SERF_PLUGIN_LOCK_TIMEOUT_MS`). On timeout, the CLI exits 3 with `another serf plugin operation is in progress (locked: <lockpath>)`.

### 10.2 What happens when two installs race

Two `serf plugin install` invocations on the same machine race for the lock. Second waits. When second acquires, it re-reads the registry (first's writes are visible) and proceeds. If both target the same `(plugin, marketplace, scope, version)` tuple, the second short-circuits via "already at" — no double-fetch.

Two installs of *different* `(plugin, marketplace)` pairs still serialize. SP4 v1 does not parallelize fetches. This matches Claude Code and keeps the failure model simple.

### 10.3 Stale lock files

A crashed process can leave the lock file behind. On POSIX, the kernel releases the flock when the holding process exits, so the file's presence is harmless — the next acquirer succeeds immediately. We never `os.Remove` the lock file as part of normal operation.

### 10.4 Cross-machine concurrency

Out of scope. `installed_plugins.json` is per-machine. If a project mounts the same `~/.config/serf/` over NFS (rare), flock semantics across NFS are not guaranteed; SP4 assumes a local filesystem. Cross-machine reproducibility is the job of `enabledPlugins` in committed `.serf/config.json`, not the registry.

## 11. Error Contracts

All exported `Installer` methods return errors that start with `serf plugin <subcommand>:` so the CLI can wrap them once for display without duplicating context.

| Class | Triggered by | User-facing message |
|---|---|---|
| Bad spec | `parsePluginSpec` failure | `serf plugin install: plugin spec "x" requires a marketplace; pass plugin@marketplace or --marketplace <name>` |
| Unsupported scope | `--scope local` or `--scope managed` | `serf plugin install: scope "local" is not yet supported in serf` |
| Missing project | `--scope project` outside a git repo | `serf plugin install: --scope project requires a git repository (no .git found from <cwd>)` |
| Marketplace miss | SP3 returns not-found | `serf plugin install: plugin "x@y" is not in any known marketplace; run 'serf plugin marketplace add ...'` |
| Source fetch failure | `PluginSource.Fetch` errors | `serf plugin install: fetching "x@y": <wrapped err>` |
| Validation failure | post-fetch invariant violated | `serf plugin install: validating "x@y" version "v": <reason>` |
| Version mismatch | `--version` vs `plugin.json.version` | `serf plugin install: --version "1.0.0" does not match plugin.json version "1.0.1"` |
| Already installed | Install short-circuit | (not an error; `InstallResult.AlreadyAt == true`) |
| Not installed | Uninstall/enable/disable/update target missing | `serf plugin <op>: plugin "x@y" is not installed at scope "user"` |
| Registry write failure | I/O error during atomic save | `serf plugin <op>: writing installed_plugins.json: <wrapped err>` |
| Config write failure | I/O error writing config.json | `serf plugin <op>: writing <path>: <wrapped err>` (install reports success + this warning; uninstall reports success + this warning) |
| Lock timeout | Could not acquire registry lock within `LockTimeout` | `serf plugin <op>: another serf plugin operation is in progress` |
| Multi-error (update --all) | One or more sub-updates failed | `serf plugin update: 2 of 9 plugins failed: <joined>` |

Wrapping convention: every error is `fmt.Errorf("...: %w", inner)` so callers can `errors.Is` / `errors.As` for the underlying cause. The CLI uses `errors.As` to map specific error types to specific exit codes.

## 12. Package and File Layout

New files:

```
internal/plugins/
├── registry.go             # Registry types, LoadRegistry, SaveRegistry, DefaultRegistryPath
├── registry_test.go        # Round-trip, atomic write, malformed-file tests
├── install.go              # Installer struct, Install/Uninstall/Update/Enable/Disable/List
├── install_test.go         # Integration tests against a stub MarketplaceResolver
├── config_rewrite.go       # loadRawConfig, writeRawConfig (preserves unknown keys)
├── config_rewrite_test.go
├── version.go              # computeVersion logic; pure function
├── version_test.go
├── locks.go                # file-lock helpers (flock wrapper, LockTimeout)
└── testdata/
    ├── installed_plugins_v2.json     # real Claude Code example, byte-for-byte
    ├── installed_plugins_malformed.json
    ├── plugin_with_version.json
    ├── plugin_no_version.json
    └── plugin_invalid_version.json

cmd/serf/plugin/
├── install.go              # Cobra command setup, flag wiring
├── install_test.go         # CLI-level tests; exec the binary or call Cobra Run
└── render.go               # Human + --json output formatting

agent/testdata/marketplaces/
├── tiny-directory/         # fixture: a marketplace.json + a plugin dir
└── ...                     # shared with SP3 (SP3 owns these)

internal/plugins/sources/   # SP3-owned; SP4 only imports the interface
```

SP4 does not modify any existing file directly. Wiring is SP8's job: `cmd/serf/main.go` adds the `plugin` subgroup, and SP4's `Installer` is constructed at command-time from CLI flags + `agent.LocalExecutionEnvironment` for cwd/git-root lookup.

Shared with SP3: `internal/plugins/registry.go` houses both `installed_plugins.json` (SP4) and `known_marketplaces.json` (SP3) types because they share the atomic-write infrastructure. The file is co-owned; SP4 lands the `installed_plugins.json` half first, SP3 lands the marketplace half. They sit in the same file so reviewers see the shared invariants in one place.

## 13. Testing Strategy

TDD: every test row below lands before the corresponding implementation. No mocked filesystem — every test uses `t.TempDir()` and writes real files. Stub `MarketplaceResolver` in `internal/plugins/sources/stub.go` (SP3 ships this; SP4 imports it). Stub `PluginSource.Fetch` copies from an in-repo fixture directory under `internal/plugins/testdata/`.

### 13.1 `registry.go` tests

`TestLoadRegistry` — table-driven on file contents.

| # | Case | File content | Expect |
|---|---|---|---|
| 1 | Absent file | (not written) | empty `Registry{Version: 2}`, nil err |
| 2 | Empty plugins map | `{"version":2,"plugins":{}}` | non-nil empty map, nil err |
| 3 | Real Claude Code example | `testdata/installed_plugins_v2.json` | all entries parsed; round-trip via `SaveRegistry` re-reads identical struct |
| 4 | Version 1 (forward-compat) | `{"version":1,"plugins":{}}` | accepted, no err |
| 5 | Version 99 (forward-incompat) | `{"version":99,"plugins":{}}` | err `unsupported installed_plugins.json schema version 99` |
| 6 | Malformed JSON | `{` | err contains file path |
| 7 | Entry with `installPath` outside CacheRoot | crafted | err contains `installPath outside cache root` |
| 8 | Two entries with same scope under one plugin key | crafted | err contains `duplicate scope "user"` |
| 9 | Entry missing required `installPath` | crafted | err contains `installPath` |
| 10 | Entry with absent `gitCommitSha` field | crafted | parses; `GitCommitSha == ""` |
| 11 | Trailing whitespace + newline tolerated | `{...}\n\n` | parses |

`TestSaveRegistry` — pure round-trip + atomicity.

| # | Case | Expect |
|---|---|---|
| 1 | Save → Load returns identical struct | byte-equal JSON not required (we control key order, so byte-equality also passes) |
| 2 | Save with sorted keys | output's `plugins` keys are alphabetical |
| 3 | Atomic write: kill mid-rename simulated by injecting a rename failure | original file is untouched |
| 4 | Save creates parent directory | `~/.config/serf/plugins/` mkdir-p works |
| 5 | Save preserves indent: 2 spaces, trailing newline | byte-level assertion |

### 13.2 `version.go` tests

`TestComputeVersion` — pure function table over `(reqVersion, declaredVersion, commitSHA, hasPluginJson, pluginJsonVersion)` tuples.

| # | Inputs | Expect |
|---|---|---|
| 1 | reqVersion "1.0.0", pluginJsonVersion "1.0.0" | "1.0.0", no err |
| 2 | reqVersion "1.0.0", pluginJsonVersion "1.0.1" | err `does not match` |
| 3 | reqVersion "", pluginJsonVersion "2.1.0" | "2.1.0" |
| 4 | reqVersion "", declared "1.2.3", no plugin.json | "1.2.3" |
| 5 | reqVersion "", no declared, commitSHA "abcdef1234567890" | "abcdef123456" (12 char trunc) |
| 6 | reqVersion "", no declared, commitSHA "" | "unknown" |
| 7 | reqVersion "", plugin.json present but no version field | falls through; uses declared/SHA/unknown |
| 8 | pluginJsonVersion is not a string (e.g. number) | err `must be a string` |

### 13.3 `install.go` tests

`TestInstall_HappyPath` — install a stub plugin, verify cache dir, registry, and enabledPlugins all updated.

| # | Case | Expect |
|---|---|---|
| 1 | Fresh install at user scope | cache dir created; registry has one entry; global config.json has `enabledPlugins["x@y"]: true` |
| 2 | Re-install same version, Force=false | InstallResult.AlreadyAt == true; mtime of cache dir unchanged |
| 3 | Re-install same version, Force=true | cache dir's contents are re-fetched; registry `lastUpdated` advances |
| 4 | Install at project scope | project's `.serf/config.json` populated; global config.json untouched |
| 5 | Install with `--pin` | `enabledPlugins["x@y"] == {"version": "1.0.0"}` not `true` |
| 6 | Install with `--no-enable` | registry updated; enabledPlugins absent |
| 7 | Install with explicit `--version "1.0.0"` matching plugin.json | success, version recorded as "1.0.0" |
| 8 | Install with `--version "1.0.0"` mismatching plugin.json "1.0.1" | err; cache dir gone; registry unchanged |
| 9 | Install from a directory source not in a git repo | version recorded as "unknown"; `gitCommitSha == ""` |
| 10 | Install from a directory source in a git repo | version is 12-char SHA; gitCommitSha is full SHA |
| 11 | Two scopes install same version | one cache dir; two registry entries; shared installPath |
| 12 | Two scopes install different versions | two cache dirs; two registry entries; different installPaths |

`TestInstall_RollbackOnFailure` — every failure path leaves no orphan state.

| # | Failure injected | Expect |
|---|---|---|
| 1 | `PluginSource.Fetch` returns error | cache dir does not exist after call returns; registry unchanged |
| 2 | Post-fetch validation fails (invalid plugin.json version type) | cache dir removed; registry unchanged |
| 3 | Registry write fails (read-only path injected) | cache dir removed; registry unchanged |
| 4 | Enable step fails (read-only config.json) | install reports success + enable warning; cache dir present; registry has entry |

`TestUninstall` — covers happy path + edge cases.

| # | Case | Expect |
|---|---|---|
| 1 | Uninstall existing user-scope plugin | registry entry gone; enabledPlugins key removed; cache dir gone |
| 2 | Uninstall plugin that is also installed at project scope | user entry gone; project entry intact; cache dir intact (shared) |
| 3 | Uninstall non-installed plugin | err `not installed at scope "user"` |
| 4 | Uninstall with config.json write failure | uninstall succeeds; warning logged |
| 5 | `--keep-data` flag | logs warning once; otherwise identical to plain uninstall |
| 6 | `--prune` flag | rejected with usage error |

`TestUpdate` — happy path + no-op short-circuit + GC.

| # | Case | Expect |
|---|---|---|
| 1 | Update when source has advanced | new cache dir created; registry advanced; old cache dir GC'd |
| 2 | Update when source unchanged | AlreadyAt=true; no cache changes |
| 3 | Update with `--force` and unchanged source | re-fetched; lastUpdated advances |
| 4 | Update with pinned enabledPlugins (`{version: "1.0.0"}`) | enabledPlugins rewritten to new version |
| 5 | Update with bare-true enabledPlugins | enabledPlugins left as `true` |
| 6 | Update when second scope still references old version | old cache dir NOT GC'd |

`TestUpdateAll` — multi-plugin behavior.

| # | Case | Expect |
|---|---|---|
| 1 | All 3 plugins succeed | results length 3; exit code 0; sorted by name |
| 2 | 1 of 3 fails (resolver returns err for one) | results length 3 with one error embedded; overall err non-nil; other 2 succeeded |
| 3 | Lock contention from a parallel goroutine | second waits; both complete |

`TestEnableDisable`.

| # | Case | Expect |
|---|---|---|
| 1 | Enable installed plugin | config.json entry set to `true` |
| 2 | Enable with `--pin` | entry set to `{version: <installed>}` |
| 3 | Enable non-installed plugin | err `not installed at scope` |
| 4 | Disable installed plugin | entry removed |
| 5 | Disable already-disabled plugin | no-op, no err |
| 6 | Enable at project scope with no git repo | usage err |
| 7 | Enable preserves unknown top-level config.json fields | adjacent `hooks`, `mcpServers` round-trip unchanged |

`TestList`.

| # | Case | Expect |
|---|---|---|
| 1 | Empty registry | empty slice, no err |
| 2 | Two plugins, one enabled in global, one in project | both returned; `Enabled` reflects each scope's config.json |
| 3 | Plugin installed at user, enabled at project only | one ListEntry with Scope=user, Enabled=false (enable is per-scope) |

### 13.4 Concurrency tests

`TestLock_Serializes` — spawn two goroutines that each call `Install` for different plugins; assert they do not overlap. Use a slow stub `Fetch` (sleep 100ms) and assert wall-clock duration ≥ 200ms.

`TestLock_Timeout` — hold the lock from goroutine A for 2s; call `Install` from goroutine B with `LockTimeout=500ms`; B errors with lock-timeout message.

### 13.5 CLI tests (`cmd/serf/plugin/install_test.go`)

Exercise flag parsing, exit codes, and rendering. Stub the `Installer` interface (we extract `Installer` into a small interface for this purpose).

| # | Case | Expect |
|---|---|---|
| 1 | `serf plugin install x@y` | exit 0; human-readable output mentions "Installed" |
| 2 | `serf plugin install x` (no @) | exit 2; error mentions marketplace required |
| 3 | `serf plugin install x@y --scope managed` | exit 2; error mentions "not yet supported" |
| 4 | `serf plugin install x@y --json` | stdout is parseable JSON with `ok:true` |
| 5 | `serf plugin install x@y` when resolver fails | exit 4; error mentions marketplace |
| 6 | `serf plugin update --all --json` with mixed results | exit 1; JSON results array has both ok and error entries |
| 7 | `serf plugin uninstall x@y --prune` | exit 2; error names `--prune` |
| 8 | `serf plugin list --available` without `--json` | exit 2 |
| 9 | `serf plugin list --json` | parseable JSON `plugins` array |
| 10 | `serf plugin uninstall x@y --keep-data` | exit 0; one-shot warning printed to stderr |

### 13.6 Coverage gate

- Every exported `Installer` method has at least one happy-path and one error-path test.
- Every error in §11 has a triggering test row.
- Every rule in §9 has a row in `TestComputeVersion`.
- Every CLI flag in §8.2 has a test row in §13.5.
- `go test ./internal/plugins/... ./cmd/serf/plugin/...` is green.

## 14. Open Questions Settled Here

### 14.1 `enabledPlugins` value: `true` vs `{version: "..."}`

**Decision.** `true` is the default. `--pin` on `install` or `enable` writes `{version: <resolved-version>}`. `update` walks pinned versions forward; users who want immutable pins should not run `update`.

**Rationale.** Three considerations dominate:

1. **Reproducibility scope.** `installed_plugins.json` lives in `~/.config/serf/` and is per-machine. Committing it would be awkward — it contains absolute paths, machine-specific timestamps, and a `gitCommitSha` that depends on when each user happened to install. The thing users actually commit and share is `.serf/config.json`, where `enabledPlugins` lives. So the question is really "do we want `.serf/config.json` to be sufficient to reproduce an install on a teammate's machine?"
2. **Default ergonomics.** Most users will be the only one on their project, or will not care about exact version pinning. Bare `true` keeps the file diff-friendly and matches Claude Code's most common config form.
3. **Opt-in pinning.** Users who do want reproducibility (CI, shared `.serf/config.json` in a monorepo) get it via `--pin` at install time. The resulting `enabledPlugins["x@y"] = {"version": "1.2.0"}` reproduces the same version on any machine that re-runs the install during session startup (SP8's job).

A `--pin-all` migration helper (`serf plugin pin --all`) that rewrites every bare `true` to a pinned object is a small, deferred follow-up — not part of v1.

### 14.2 `serf plugin update --all` — continue or stop on first failure

**Decision.** Continue past failures. Per-plugin errors collect; the CLI prints one status line per plugin as it finishes and a summary at the end. Exit code 0 if all succeed, 1 if any failed. `--json` returns the full results array.

**Rationale.** Three options:

1. **Stop on first error.** Matches `make`'s default. Rejected: one broken upstream marketplace blocks updates for unrelated plugins, and the failing plugin's failure mode (e.g., a transient network error) is exactly the kind that the user wants to retry while the rest of the world keeps moving.
2. **Continue on error** (chosen). Matches `apt upgrade`, `brew upgrade`, `pip install --upgrade-all`. User retries the failed subset.
3. **Parallelize and continue.** Tempting but multiplies failure modes and complicates the lock model. Deferred.

The CLI surfaces failures clearly: each `✗ plugin@mkt: <err>` line is on its own; the final summary names the count. `--json` makes scripting reliable.

## 15. Other Open Questions (Not Resolved Here)

- **SP3 type names.** SP4 calls SP3's resolver via the `MarketplaceResolver` and `PluginSource` interfaces defined in §2.4. If SP3's spec names them differently, SP4 renames before implementation. Coordination point.
- **Plugin data directories.** Claude Code's `${CLAUDE_PLUGIN_DATA}` (persistent state surviving updates) is owned by SP7 if/when serf adopts it. `--keep-data` is wired through SP4's CLI for forward compat but is a no-op in v1.
- **Bare-name resolution.** Claude Code's `claude plugin install <name>` (no `@mkt`) searches across known marketplaces. SP4 v1 requires the explicit `@mkt` suffix. A follow-up can layer cross-marketplace search on top.
- **`local` and `managed` scopes.** Rejected as inputs in v1. Round-tripped in registry reads for compat. A future spec adds them when serf grows a `.serf/settings.local.json` and a managed-settings mechanism.
- **`plugin prune`.** Claude Code's dependency auto-remove. Rejected in v1 because the parent spec defers plugin dependencies.
- **Cache GC grace period.** Claude Code keeps orphaned cache dirs for 7 days. SP4 v1 removes immediately on uninstall/update. Concurrent serf sessions that loaded the old version keep using the in-memory path until the session ends. A future spec can introduce the 7-day delay if mid-session updates become common.
- **Trust prompts for project-declared marketplaces.** SP3 owns. SP4 calls SP3 to resolve; if SP3 refuses on trust grounds, SP4 surfaces the error.
