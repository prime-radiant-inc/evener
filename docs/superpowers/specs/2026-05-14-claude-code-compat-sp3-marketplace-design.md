# SP3 — Marketplace Management (Detailed Design)

Date: 2026-05-14
Status: DEFERRED (2026-05-14 scope reduction). Go-side marketplace tooling is not in the initial release. Marketplace fetching is handled by the agent via the SP-B manage-plugins skill, using its existing Bash/git/WebFetch tools. This spec stands as the design when CLI-driven marketplace tooling is reintroduced.
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
Companion spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-sp1-config-loader-design.md`

## 1. Goal

SP3 turns a marketplace reference into a usable on-disk catalog of plugin entries. It owns three concerns: cloning or copying the marketplace itself into `~/.config/serf/plugins/marketplaces/<name>/`, parsing the resulting `.claude-plugin/marketplace.json` into typed Go values, and presenting the catalog through the `serf plugin marketplace` subcommand tree. SP3 supports all four source types both for the marketplace container (`directory`, `github`, `url`, `git-subdir`) and for the plugin entries inside it (same four). SP3 maintains a registry file (`known_marketplaces.json`) under the user config, enforces explicit trust on project-declared marketplaces via `trusted_projects.json`, and exposes a single Go package that SP4 will call to resolve a plugin spec to its source. SP3 does not install plugins, does not read or merge the parent `config.json` (SP1 owns that), and does not route `serf plugin install` (SP4 owns that). It does ship the shared `cmd/serf/plugin/` scaffolding that SP4 plugs into.

## 2. Public API Surface

All new symbols live in package `internal/plugins`. Names follow the verb-noun rhythm of `agent/mcp_config.go` and `agent/plugin.go`. SP3 exports four type groups, one for the marketplace file shape, one for plugin sources, one for the registry, and one for the CLI handlers.

### 2.1 Marketplace file types

```go
// Marketplace is the parsed contents of one .claude-plugin/marketplace.json.
// Field shapes mirror the documented Claude Code schema. Optional fields
// surface as zero values; SP3 distinguishes "absent" from "empty" only where
// validation demands it.
type Marketplace struct {
    Name        string              // required, kebab-case
    Owner       Owner               // required
    Description string              // optional
    Version     string              // optional (marketplace manifest version)
    Metadata    MarketplaceMetadata // optional
    Plugins     []PluginEntry       // may be empty
    Schema      string              // "$schema", ignored at load time but preserved
}

type Owner struct {
    Name  string // required
    Email string // optional
}

type MarketplaceMetadata struct {
    Description string // accepted under metadata for backward compatibility
    Version     string // accepted under metadata for backward compatibility
    PluginRoot  string // base path prefix for relative plugin sources
}

// PluginEntry mirrors one element of marketplace.json's "plugins" array.
// Unknown manifest fields are preserved in Extra so SP4 and SP7 can read
// component-configuration fields (skills, commands, agents, hooks,
// mcpServers, lspServers) without SP3 having to know their shapes.
type PluginEntry struct {
    Name        string
    Source      PluginSource // resolved (relative paths joined with metadata.pluginRoot)
    Description string
    Version     string
    Author      Owner
    Homepage    string
    Repository  string
    License     string
    Keywords    []string
    Category    string
    Tags        []string
    Strict      *bool             // pointer so absent != false; default is true
    Extra       map[string]json.RawMessage // fields SP3 does not interpret
}
```

### 2.2 Source types

A single discriminated union represents every source flavour Claude Code documents. The same type is used both for the marketplace container source and for plugin entry sources; the only difference is which fields are legal in which context. Validation enforces that.

```go
// SourceKind enumerates the four supported source types. The string form
// matches Claude Code's "source": "..." discriminator verbatim.
type SourceKind string

const (
    SourceDirectory SourceKind = "directory"
    SourceGitHub    SourceKind = "github"
    SourceURL       SourceKind = "url"
    SourceGitSubdir SourceKind = "git-subdir"
)

// MarketplaceSource describes where the marketplace.json itself lives.
// Used by `serf plugin marketplace add`, persisted in known_marketplaces.json,
// and read by `update`. SHA pinning is not supported here (per Claude Code
// docs: marketplace sources accept ref but not sha).
type MarketplaceSource struct {
    Kind SourceKind

    Path string // directory: absolute path on disk
    Repo string // github:    owner/repo
    URL  string // url, git-subdir: full git URL
    Path2 string `json:"path,omitempty"` // git-subdir: subdirectory path inside repo
    Ref  string // github, url, git-subdir: branch or tag

    // Sparse, when non-empty, limits the marketplace checkout to these paths
    // via `git sparse-checkout set`. Matches Claude Code's --sparse flag.
    Sparse []string
}

// PluginSource is the per-plugin source. Same kinds as MarketplaceSource,
// plus an `Sha` field that is legal here but not on the marketplace itself.
// Relative directory paths have been resolved against the marketplace's
// metadata.pluginRoot at parse time; consumers see absolute paths.
type PluginSource struct {
    Kind SourceKind

    Path string // directory: absolute path on disk (post pluginRoot join)
    Repo string // github:    owner/repo
    URL  string // url, git-subdir: full git URL
    Subpath string // git-subdir: subdirectory path inside repo
    Ref  string // optional
    Sha  string // optional, 40-char hex
}
```

### 2.3 Public functions

```go
// ParseMarketplace reads .claude-plugin/marketplace.json at root and returns
// a fully validated Marketplace. Relative plugin sources are resolved against
// root + metadata.pluginRoot before return. Validation errors include the
// JSON pointer of the offending field, e.g.
// `marketplace.json: plugins[3].source: missing "repo" for source "github"`.
func ParseMarketplace(root string) (Marketplace, error)

// FetchMarketplace downloads or copies a marketplace source into
// installDir and returns the absolute path containing .claude-plugin/.
// installDir is created if absent; an existing installDir is updated
// in place (see source-type sections in §4 for per-kind semantics).
//
// The supplied Fetcher set abstracts git and HTTP so tests can stub
// transport without forging a fake filesystem.
func FetchMarketplace(src MarketplaceSource, installDir string, f Fetchers) (string, error)

// FetchPluginSource downloads or copies a plugin source into installDir.
// SP4 owns the call site; SP3 ships the implementation because it owns
// the source-kind dispatch table.
func FetchPluginSource(src PluginSource, installDir string, f Fetchers) error

// Fetchers bundles the transport seams. Production builds construct one
// from the real git binary and net/http; tests inject stubs.
type Fetchers struct {
    Git  GitClient   // shells out to `git` by default
    HTTP HTTPClient  // wraps net/http.Client by default
}
```

### 2.4 Registry

```go
// Registry is the parsed contents of known_marketplaces.json plus its on-disk
// path. The zero value is an empty registry not yet bound to a file; use
// OpenRegistry to load from disk.
type Registry struct {
    Path    string                // absolute path to known_marketplaces.json
    Entries map[string]RegistryEntry // keyed by marketplace name
}

// RegistryEntry is one row in known_marketplaces.json. Source duplicates the
// data needed to re-fetch on `update`; InstallDir is the on-disk location of
// the cloned marketplace; AddedAt is recorded for `list` output.
type RegistryEntry struct {
    Source     MarketplaceSource
    InstallDir string    // absolute path
    AddedAt    time.Time // UTC
    Origin     Origin    // who added the entry
}

// Origin distinguishes user-driven adds from project-config adds. SP3 uses
// it to decide which entries need trust prompts and which `remove` is allowed
// to delete unprompted.
type Origin int

const (
    OriginCLI     Origin = iota // added by `serf plugin marketplace add`
    OriginProject               // added by .serf/config.json after trust prompt
)

// OpenRegistry loads known_marketplaces.json from path. Missing file returns
// an empty Registry (Path set, Entries empty), no error. Malformed file is
// a hard error annotated with the path.
func OpenRegistry(path string) (Registry, error)

// Save writes the registry atomically (tmp file + rename, mode 0644 on the
// final file). The directory is created if absent.
func (r Registry) Save() error

// Add inserts or replaces an entry. Returns an error only if name violates
// the kebab-case rule that ParseMarketplace already enforces; this method
// is the second guard for entries that bypass parsing (e.g. a future
// `--from-source` flag).
func (r *Registry) Add(name string, entry RegistryEntry) error

// Remove deletes an entry by name. Returns ErrUnknownMarketplace if absent.
func (r *Registry) Remove(name string) error
```

### 2.5 Trust store

```go
// TrustStore is the parsed contents of trusted_projects.json. Keys are
// absolute project paths; values are the marketplace names trusted from that
// project. A name being present means the user said "yes" to that specific
// marketplace from that specific project.
type TrustStore struct {
    Path    string
    Entries map[string]map[string]bool // projectPath -> set of marketplace names
}

func OpenTrustStore(path string) (TrustStore, error)
func (t TrustStore) Save() error
func (t *TrustStore) Trust(projectPath, marketplace string)
func (t TrustStore) IsTrusted(projectPath, marketplace string) bool
```

### 2.6 CLI handlers

```go
// Subcommands wires `serf plugin marketplace add|remove|list|update`. SP4
// adds `install|uninstall|update|list|enable|disable` to the same parent
// scaffolding (Dispatch, below).
package marketplacecmd

// Dispatch routes args[0] to the right subcommand. Returns
// (handled, exitCode, err). handled=false means args[0] is not a marketplace
// subcommand and the caller should keep dispatching.
func Dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) (handled bool, exitCode int, err error)
```

The parent `serf plugin` router (shared with SP4) lives at `cmd/serf/plugin/plugin.go` and switches on `args[1]` between `marketplace` and the SP4-owned verbs. The top-level `dispatchCLICommand` in `cmd/serf/main.go` gains one new case (`"plugin"`).

## 3. The `marketplace.json` Schema

The schema is the one Claude Code documents at https://code.claude.com/docs/en/plugin-marketplaces (fetched and saved verbatim into the conversation that produced this spec). What follows is the structural contract SP3 will accept; semantic divergences from Claude Code are called out as such.

### 3.1 Top-level shape

```json
{
  "$schema": "https://anthropic.com/claude-code/marketplace.schema.json",
  "name": "company-tools",
  "description": "Internal Anthropic-style plugin catalog",
  "version": "1.4.0",
  "owner": {
    "name": "DevTools Team",
    "email": "devtools@example.com"
  },
  "metadata": {
    "description": "Legacy slot; ignored if top-level description is set",
    "version":     "Legacy slot; ignored if top-level version is set",
    "pluginRoot":  "./plugins"
  },
  "plugins": [ /* see §3.3 */ ]
}
```

Required fields: `name`, `owner.name`, `plugins`. Everything else is optional. `plugins` may be an empty array; SP3 surfaces that as a warning on `serf plugin marketplace add` ("marketplace has no plugins") but it is not an error. Unknown top-level fields are preserved in `Marketplace.Extra` for forward compatibility; one stderr warning is emitted per unknown field, prefixed with the file path.

Reserved-name enforcement (`claude-plugins-official`, `anthropic-marketplace`, etc.) is **not** carried over from Claude Code. Rationale: Claude Code uses those reservations to police impersonation in its centrally-listed marketplaces. Serf has no such central listing in v1; reintroducing the block would surprise users who already added an Anthropic marketplace. SP3 records this as a known divergence (§11).

### 3.2 `metadata.pluginRoot`

`metadata.pluginRoot` is an optional string that prefixes every relative plugin source path. SP3 resolves it once at parse time:

- If a plugin's `source` is a `string` beginning with `./`, the resolved directory path is `filepath.Join(marketplaceRoot, metadata.pluginRoot, source[2:])`.
- If `metadata.pluginRoot` is also relative (e.g. `"./plugins"`), it is itself first joined to `marketplaceRoot`.
- If the plugin source is an object (any of the three remote types), `metadata.pluginRoot` does not apply and is ignored.
- A plugin source containing `..` is rejected at parse time with `marketplace.json: plugins[i].source: path may not contain ".."`.

This matches Claude Code's behaviour: `metadata.pluginRoot` lets a marketplace shorten boilerplate by writing `"source": "formatter"` instead of `"source": "./plugins/formatter"`.

### 3.3 Plugin entries: the four source shapes

#### 3.3.1 `directory` (relative path string)

```json
{
  "name": "code-formatter",
  "source": "./plugins/formatter",
  "description": "Automatic code formatting on save",
  "version": "2.1.0",
  "author": { "name": "DevTools Team" }
}
```

The discriminator is the JSON type of the `source` field: a string means `directory`. The string must begin with `./` and must not contain `..`. After resolving against the marketplace root (and `metadata.pluginRoot` if set), the path must point inside the marketplace tree. SP3 does not require the directory to exist at parse time — that check is the install-time responsibility of SP4 — but `serf plugin marketplace add` does run a presence check and prints a warning if the directory is missing, because a directory source that is missing the moment the marketplace is added is almost certainly a typo.

#### 3.3.2 `github`

```json
{
  "name": "deployment-tools",
  "source": {
    "source": "github",
    "repo": "company/deploy-plugin",
    "ref": "v2.0.0",
    "sha": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"
  }
}
```

Required: `source: "github"`, `repo` matching `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`. Optional: `ref` (branch or tag), `sha` (full 40-hex commit). If both are set, the fetcher pins to `sha` after a checkout of `ref` (the docs treat them as additive: `ref` for discovery, `sha` for exactness). SP3 normalises `repo` to its `https://github.com/<owner>/<repo>.git` form internally.

#### 3.3.3 `url`

```json
{
  "name": "git-plugin",
  "source": {
    "source": "url",
    "url": "https://gitlab.com/team/plugin.git",
    "ref": "main",
    "sha": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"
  }
}
```

Required: `source: "url"`, `url`. The URL must parse as either `https://`, `http://` (warned but accepted, see §8), or `git@host:path`. The `.git` suffix is not required. `ref` and `sha` follow the same rules as the github type.

#### 3.3.4 `git-subdir`

```json
{
  "name": "my-plugin",
  "source": {
    "source": "git-subdir",
    "url": "https://github.com/acme-corp/monorepo.git",
    "path": "tools/claude-plugin",
    "ref": "v2.0.0",
    "sha": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"
  }
}
```

Required: `source: "git-subdir"`, `url`, `path`. The `url` field accepts the same forms as `url` plus GitHub `owner/repo` shorthand, which SP3 expands to the full https form. `path` must be a forward-slash-separated subdirectory inside the repo and must not contain `..`. `ref` and `sha` are optional.

### 3.4 Other plugin-entry fields

The fields documented for plugin manifests — `description`, `version`, `author`, `homepage`, `repository`, `license`, `keywords`, `category`, `tags`, `strict`, plus the component-config fields (`skills`, `commands`, `agents`, `hooks`, `mcpServers`, `lspServers`) — are accepted at marketplace level too. SP3 destructures only `name`, `source`, the standard-metadata block, `category`, `tags`, and `strict`; the component-config fields are stashed in `PluginEntry.Extra` and handed to SP4/SP7 unchanged.

`strict` defaults to `true` per docs. SP3 represents the field as `*bool` so SP4 can tell absent (use plugin.json) from explicit-false (marketplace entry is the whole definition).

### 3.5 Marketplace-source shapes

The marketplace itself is fetched from one of four sources. The CLI parses these from positional `<source>` syntax (§5.1); the persisted form mirrors Claude Code:

```json
// directory
{ "source": "directory", "path": "/abs/path/to/marketplace" }

// github
{ "source": "github", "repo": "acme-corp/plugins", "ref": "v2.0" }

// url (any git URL, not necessarily GitHub)
{ "source": "url", "url": "https://gitlab.com/team/plugins.git", "ref": "main" }

// git-subdir
{ "source": "git-subdir", "url": "https://github.com/acme-corp/monorepo.git",
  "path": ".claude-plugin", "ref": "main" }
```

Per Claude Code docs, marketplace sources accept `ref` but not `sha`. SP3 enforces this at parse time.

## 4. Source-Type Fetchers

Each fetcher is one function with one job. They live in `internal/plugins/sources/`, one file per kind. All four accept `installDir` and return either an error or `(nil)` for success; on success the marketplace contents are present under `installDir` with `installDir/.claude-plugin/marketplace.json` readable.

### 4.1 `directory`

`sources/directory.go: FetchDirectory(src, installDir)`.

Behaviour:

1. Stat `src.Path`. If missing or not a directory, return `ErrDirectorySource`.
2. Stat `filepath.Join(src.Path, ".claude-plugin", "marketplace.json")`. If missing, return `ErrMissingMarketplaceJSON`.
3. If `installDir == src.Path`, no-op. (This is the path the CLI takes for `serf plugin marketplace add /abs/path` once we decide §11.2's question.)
4. Otherwise, *symlink* `installDir` → `src.Path`. Rationale: a local directory source is by definition under user control and not shared cache state; symlinking gives the user a live view, matches Claude Code's behaviour for local sources, and sidesteps the "should I rsync changes?" question on `update`.
5. If `installDir` exists and is a symlink to a different path, replace it. If it exists and is a real directory, return `ErrInstallDirOccupied`.

`update` for a directory source is a no-op (the symlink already points at the live source).

### 4.2 `github`

`sources/github.go: FetchGitHub(src, installDir, git)`.

Resolves to `https://github.com/<repo>.git` and delegates to the shared git-clone routine in `sources/git.go`:

1. If `installDir` does not exist: `git clone --depth 1 --branch <ref> <https-url> <installDir>` (omit `--branch` if `ref` is empty; use the default branch).
2. If `installDir` exists and is the same repo: `git -C <installDir> fetch --depth 1 origin <ref-or-default>` then `git -C <installDir> reset --hard FETCH_HEAD`.
3. Validate `<installDir>/.claude-plugin/marketplace.json` exists post-fetch.

Per-source SHA pinning is not legal for the marketplace itself, so no `--sha` step here. For plugin sources (§4 plugin variant) the same code path runs `git -C <installDir> checkout <sha>` after the fetch.

Authentication: SP3 does not handle credentials directly. The shelled-out `git` inherits the user's shell environment, which Claude Code's docs already document as the right mechanism (gh credential helper, ssh-agent, etc.). The auto-update token environment variables Claude Code documents (`GITHUB_TOKEN`, etc.) are out of scope for v1; SP3's `update` runs interactively only.

### 4.3 `url`

`sources/url.go: FetchURL(src, installDir, git)`.

Identical to `github` except the URL is taken from `src.URL` verbatim (after light normalisation: an `owner/repo` shorthand on a non-github URL is rejected; an `ssh://` URL is passed through).

### 4.4 `git-subdir`

`sources/gitsubdir.go: FetchGitSubdir(src, installDir, git)`.

Uses git's sparse-checkout to fetch only the requested subdirectory, matching Claude Code's bandwidth-saving guarantee for monorepos:

1. If `installDir` does not exist:
   ```
   git clone --no-checkout --depth 1 --filter=blob:none <url> <installDir>
   git -C <installDir> sparse-checkout set --cone <path>
   git -C <installDir> checkout <ref-or-default>
   ```
2. If `installDir` exists: `git -C <installDir> fetch --depth 1 --filter=blob:none origin <ref-or-default>` then `git -C <installDir> reset --hard FETCH_HEAD`.
3. The marketplace.json must live at `<installDir>/<path>/.claude-plugin/marketplace.json`. SP3 records the resolved marketplace root (i.e. `<installDir>/<path>`) as the `InstallDir` in the registry; the surrounding clone is an implementation detail.

For plugin sources with `git-subdir`, the same routine applies, with the final post-checkout step `git -C <installDir> checkout <sha>` if `Sha` is set. The `Marketplace.Plugins[i].Source.Subpath` field tells SP4 where to find the plugin within the resulting checkout.

### 4.5 Cross-cutting

`git` operations all run with a 120-second timeout (configurable via the env var `SERF_PLUGIN_GIT_TIMEOUT_MS`, mirroring Claude Code's `CLAUDE_CODE_PLUGIN_GIT_TIMEOUT_MS`). The fetcher captures stderr and surfaces it on failure.

Network errors during `update` do not wipe the existing checkout. If `git fetch` fails, the prior install is left in place and the error is returned. This is the v1 equivalent of Claude Code's `CLAUDE_CODE_PLUGIN_KEEP_MARKETPLACE_ON_FAILURE=1` — SP3 makes that the only mode, since silent cache wipes are how users lose offline access.

## 5. CLI Surface

### 5.1 `serf plugin marketplace add <source> [flags]`

The `<source>` argument follows Claude Code's grammar:

| Argument form | Parsed as |
|---|---|
| `owner/repo` | `github` source |
| `owner/repo@<ref>` | `github` source with `ref=<ref>` |
| `https://example.com/...git` or `git@...` | `url` source |
| `https://example.com/...git#<ref>` | `url` source with `ref=<ref>` |
| `./path` or `/abs/path` | `directory` source |
| any other `https://` / `http://` URL | not supported in v1; print "remote marketplace.json URLs are not yet supported (issue #TODO)" |

Note: the Claude Code docs allow `git-subdir` style addressing via the `--sparse` flag for monorepos. SP3 follows that pattern.

Flags:

| Flag | Meaning |
|---|---|
| `--scope=user\|project` | Where to declare. `user` writes to `~/.config/serf/plugins/known_marketplaces.json` (default). `project` writes to `<git-root>/.serf/config.json`'s `marketplaces` block; SP1 picks it up next session. |
| `--sparse <path>` (repeatable) | Turns the source into a `git-subdir`-style sparse checkout for monorepos. Equivalent to setting `Sparse` on the marketplace source. |
| `--name <name>` | Override the directory name under `~/.config/serf/plugins/marketplaces/`. Defaults to the parsed marketplace's `name` field. |
| `--trust-marketplace <name>` | Bypass the trust prompt for `<name>`. Only meaningful for `--scope=project` adds; ignored otherwise. |
| `--json` | Emit machine-readable output (see §5.5). |

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Marketplace added (or already present with matching source). |
| 1 | Generic failure (fetch, parse, registry write). |
| 2 | Argument or flag error. |
| 3 | Trust declined (project scope only). |

Behaviour:

1. Parse the positional source into a `MarketplaceSource`.
2. Compute `installDir = ~/.config/serf/plugins/marketplaces/<name>/` (name resolved either from `--name`, the trailing path segment, or, for github, the repo name).
3. Call `FetchMarketplace`. On error, abort without touching the registry.
4. Call `ParseMarketplace(installDir)`. On error, delete `installDir` (it was empty before; rollback is removing the clone) and abort.
5. If parsed `name` conflicts with an existing registry entry whose source differs, abort with `marketplace <name> already added from a different source (use 'serf plugin marketplace remove <name>' first)`.
6. Write the registry entry (atomic tmp+rename), print the parsed name + plugin count.

### 5.2 `serf plugin marketplace remove <name>`

`<name>` is the marketplace's `name` field, not the source string (matches Claude Code).

Behaviour:

1. Look up the entry in the registry.
2. If absent, exit 1 with `unknown marketplace: <name>`.
3. Delete the on-disk install directory (a symlink for `directory` sources, a real tree for git sources).
4. Remove the registry entry (atomic).

Like Claude Code, removing a marketplace renders any plugins installed from it unavailable. SP4 will handle the install-side cleanup; SP3 only knows about the marketplace.

Exit codes: 0 on success, 1 on unknown marketplace or filesystem error.

### 5.3 `serf plugin marketplace list [--json]`

Reads the registry and prints one line per entry. The default format is fixed-column human-readable:

```
NAME              SOURCE                                            ADDED                PLUGINS
company-tools     github://company/plugins@main                     2026-05-12 14:03 UTC 12
local-tools       directory:///Users/jesse/code/my-marketplace      2026-04-30 09:11 UTC 3
```

`PLUGINS` is the count from the marketplace's parsed JSON. If parsing the on-disk marketplace fails, the row prints `PLUGINS=?` and the error is logged to stderr but the command still exits 0.

`--json` emits an array of objects with `name`, `source`, `installDir`, `addedAt`, `plugins` (array of `{name, source}` summaries), and `origin` ("cli" or "project").

### 5.4 `serf plugin marketplace update [name]`

Without a name, refreshes every entry whose source supports updates (everything except `directory`, which is a symlink and always up to date).

With a name, refreshes only that entry. Unknown name exits 1.

For each entry to refresh:

1. Re-run `FetchMarketplace` with the persisted `MarketplaceSource`.
2. Re-parse and reprint changes (`+ added plugin foo`, `- removed plugin bar`, `~ bumped plugin baz: a1b2c3 -> d4e5f6`).
3. On fetch error, leave the existing cache in place and exit 1; the registry entry is untouched.

Exit codes: 0 if all requested updates succeeded, 1 if any failed.

### 5.5 JSON output convention

All four subcommands accept `--json` and emit a single top-level JSON object to stdout. Schema:

```json
{ "ok": true, "data": {...} }
{ "ok": false, "error": { "code": "fetch_failed", "message": "..." } }
```

Error codes: `fetch_failed`, `parse_failed`, `unknown_marketplace`, `name_conflict`, `trust_declined`, `argument_error`. SP4 reuses the same envelope.

### 5.6 Shared `cmd/serf/plugin/` scaffolding

```
cmd/serf/plugin/
├── plugin.go        // top-level "serf plugin <verb>" dispatcher
├── marketplace.go   // owned by SP3; "marketplace add|remove|list|update"
└── install.go       // owned by SP4; "install|uninstall|list|enable|disable|update"
```

`plugin.go` (SP3 ships this) defines a `Dispatch(args []string, ...)` that switches on `args[0]`:

```
serf plugin marketplace add ...  -> marketplace.Dispatch(args[1:], ...)
serf plugin install ...          -> install.Dispatch(args, ...)
serf plugin help                 -> printHelp()
```

`cmd/serf/main.go`'s `dispatchCLICommand` gains:

```go
case "plugin":
    return true, "serf plugin", plugin.Dispatch(args[1:], stdin, stdout, stderr)
```

That single addition is the only change SP3 makes to existing files.

## 6. Registry File Format

Path: `~/.config/serf/plugins/known_marketplaces.json`. UTF-8, 2-space indented, trailing newline. Schema:

```json
{
  "schemaVersion": 1,
  "entries": {
    "company-tools": {
      "source": {
        "kind": "github",
        "repo": "company/plugins",
        "ref": "main"
      },
      "installDir": "/Users/jesse/.config/serf/plugins/marketplaces/company-tools",
      "addedAt": "2026-05-12T14:03:00Z",
      "origin": "cli"
    },
    "local-tools": {
      "source": {
        "kind": "directory",
        "path": "/Users/jesse/code/my-marketplace"
      },
      "installDir": "/Users/jesse/.config/serf/plugins/marketplaces/local-tools",
      "addedAt": "2026-04-30T09:11:00Z",
      "origin": "cli"
    }
  }
}
```

`schemaVersion` is `1`. On load, an unrecognised value is a hard error (`unsupported registry schemaVersion 2: upgrade serf`); a missing `schemaVersion` is treated as `1` for v1.

### 6.1 Atomic writes

Following `agent/task_store.go:save`:

```go
data, err := json.MarshalIndent(state, "", "  ")
// MkdirAll the parent at 0o755.
// Write to <path>.tmp at 0o644.
// os.Rename(<path>.tmp, <path>); on failure, os.Remove the tmp.
```

A `flock`-style advisory lock (`<path>.lock`, mode 0o600) wraps the read-modify-write cycle. Lock acquisition has a 5-second timeout; on timeout, exit with `registry locked by another process (PID from <path>.lock)`.

### 6.2 Corruption

If `OpenRegistry` finds JSON it cannot parse, it does **not** silently zero the file. It returns an error tagged `registry_corrupt`, the CLI prints `registry corrupt: <path>; back up and remove this file to recover (your installed plugins will need to be re-added)`, and exits 1. Rationale: silent reset would discard the user's installed-marketplace list with no recourse; a loud error gives the user the chance to inspect the file (it is JSON, recoverable by hand).

## 7. Trust Flow

Trust applies to marketplaces declared in a project's `.serf/config.json` (i.e. SP1's `Marketplaces` map at project tier). User-scope adds via the CLI are implicitly trusted: the user typed the command.

### 7.1 Trigger

Trust is checked the first time a session encounters a project-declared marketplace not already trusted. The check runs in this order:

1. SP1's `DiscoverSerfConfig` produces a merged `SerfConfig`. Project-tier `marketplaces` entries are tagged in `Sources` with `TierProject`.
2. SP3's session-init hook (called by SP8 in v1; SP3 ships the function `EnforceTrustOnConfig(cfg SerfConfig, env ExecutionEnvironment, ui TrustPrompter) error`) iterates the project-tier marketplace entries.
3. For each one, look up `<projectPath, marketplaceName>` in `TrustStore`. If trusted, continue.
4. If not trusted, call `ui.AskTrust(projectPath, marketplaceName, source)`.

### 7.2 Prompt text (interactive CLI)

```
Project .serf/config.json declares marketplace "company-tools":
  source: github://company/plugins@main

Adding this marketplace will let it ship hooks and MCP servers that
run with serf's full privileges. Only trust marketplaces from sources
you control or have audited.

Trust this marketplace for this project? [y/N/always]:
```

- `y`: trust for this session only (no persistence).
- `always` (or `a`): trust permanently — write to `trusted_projects.json`.
- `n` or anything else: refuse. The marketplace is recorded as untrusted; SP3 does not clone it; `serf plugin marketplace list` shows it with `(untrusted)` and a hint.

### 7.3 Persistence format

`~/.config/serf/plugins/trusted_projects.json`:

```json
{
  "schemaVersion": 1,
  "projects": {
    "/Users/jesse/code/myrepo": {
      "company-tools": { "trustedAt": "2026-05-14T10:22:00Z" }
    }
  }
}
```

Atomic write semantics identical to the registry (§6.1).

### 7.4 Non-interactive surfaces

This is the open question §11.1 resolves. Decision:

- **CLI (`serf plugin ...`, `serf` foreground task mode):** prompts inline on stderr/stdin.
- **`serf-tui` / `serf-hub` / `serf serve`:** non-interactive. Project marketplaces remain *listed but uncloned* until explicitly trusted. The session emits a single warning event (`marketplace_trust_required`) and proceeds without those marketplaces' plugins.
- **Scripted CLI (no tty on stdin):** identical to the non-interactive surfaces. To trust from CI/scripts, use one of:
  - `--trust-marketplace <name>` flag on `serf plugin marketplace add` and on `serf` (top-level), repeatable. Implies a "this session only" trust.
  - `SERF_TRUST_MARKETPLACES=<name>,<name>` env var. Same semantics — session-scoped, not persisted.
  - `serf plugin marketplace trust <name>` (a fifth subcommand, SP3 ships it). Persists to `trusted_projects.json` from the command line; the inverse is `serf plugin marketplace untrust <name>`.

Rationale: the `trust` subcommand makes scripting clean (one command, exit code) and the env var covers the "I am running this in a container and have no persistent home directory" case. The flag covers the "run-once with confirmation suppressed" case.

### 7.5 Bypassing the prompt with foreknowledge

`SERF_TRUST_ALL_MARKETPLACES=1` exists but is **off** by default and logs a single startup warning when on. Documented as "for ephemeral sandboxed environments where you have already audited the project."

## 8. Error Contracts

Every public function returns an error annotated with the relevant file or source. The CLI maps errors to the `--json` codes in §5.5.

| Failure mode | Where surfaced | Error text |
|---|---|---|
| `marketplace.json` missing after fetch | `ParseMarketplace` | `marketplace.json not found at <path>; expected <path>/.claude-plugin/marketplace.json` |
| Malformed JSON | `ParseMarketplace` | `parsing marketplace.json at <path>: <wrapped err>` |
| Missing required field | `ParseMarketplace` | `<path>: <pointer>: missing required field "name"` etc. |
| Bad kebab-case marketplace name | `ParseMarketplace` | `<path>: name "<value>" must be kebab-case` |
| Source discriminator missing | `ParseMarketplace` | `<path>: plugins[i].source: missing "source" field` |
| Source discriminator unknown | `ParseMarketplace` | `<path>: plugins[i].source: unknown source type "<value>"` |
| Path source contains `..` | `ParseMarketplace` | `<path>: plugins[i].source: path may not contain ".."` |
| Path source not starting with `./` | `ParseMarketplace` | `<path>: plugins[i].source: relative path must start with "./"` |
| github repo bad format | `ParseMarketplace` | `<path>: plugins[i].source: repo "<value>" must be "owner/repo"` |
| git-subdir missing `path` | `ParseMarketplace` | `<path>: plugins[i].source: missing "path" for source "git-subdir"` |
| `git clone` non-zero exit | `Fetch{GitHub,URL,GitSubdir}` | `cloning <url>: git exited <code>: <stderr first line>` |
| `git fetch` non-zero exit | `Fetch*` (update path) | `fetching <url>: git exited <code>: <stderr first line>` |
| Network timeout (git) | `Fetch*` | `git operation timed out after <duration>: <command>` |
| HTTP non-2xx (future URL-served marketplace.json) | n/a in v1 | reserved code `fetch_failed` |
| Registry parse failure | `OpenRegistry` | `parsing known_marketplaces.json at <path>: <wrapped err>` |
| Registry version unsupported | `OpenRegistry` | `unsupported registry schemaVersion <n>: upgrade serf` |
| Registry write failure | `Registry.Save` | `writing known_marketplaces.json at <path>: <wrapped err>` |
| Lock contention | `Registry.Save` | `registry locked by another process; retry shortly` |
| Trust declined | `EnforceTrustOnConfig` | `marketplace <name> declared by project not trusted` |
| Install dir collision | `FetchMarketplace` | `install directory <path> exists and is not empty; remove it or pick another --name` |

The CLI prefixes user-facing error output with `serf plugin marketplace: ` — matching the existing `fmt.Fprintf(stderr, "%s: %v\n", label, err)` convention in `cmd/serf/main.go:28`.

`http://` URLs (non-TLS) parse successfully but emit a one-time warning: `warning: <url> is not HTTPS; marketplaces fetched over plain HTTP may be tampered with`. They are not blocked — some self-hosted environments still serve over HTTP — but the warning is loud.

## 9. Package and File Layout

```
internal/plugins/
├── marketplace.go        // Marketplace, PluginEntry, ParseMarketplace
├── marketplace_test.go
├── registry.go           // Registry, OpenRegistry, Save, Add, Remove
├── registry_test.go
├── trust.go              // TrustStore, OpenTrustStore, Save, EnforceTrustOnConfig
├── trust_test.go
├── source.go             // SourceKind, MarketplaceSource, PluginSource, parse helpers
├── source_test.go
├── fetch.go              // Fetchers, FetchMarketplace, FetchPluginSource dispatch
├── fetch_test.go
└── sources/
    ├── directory.go
    ├── directory_test.go
    ├── github.go
    ├── github_test.go
    ├── url.go
    ├── url_test.go
    ├── gitsubdir.go
    ├── gitsubdir_test.go
    └── git.go             // shared low-level git wrapper used by github/url/gitsubdir

cmd/serf/plugin/
├── plugin.go             // top-level dispatcher, shared with SP4
├── plugin_test.go
├── marketplace.go        // CLI handlers for add|remove|list|update|trust|untrust
└── marketplace_test.go

cmd/serf/main.go          // +1 case in dispatchCLICommand
```

Existing files touched: only `cmd/serf/main.go`, additively (one switch case).

`testdata/`:

```
internal/plugins/testdata/
├── marketplaces/
│   ├── valid_full/.claude-plugin/marketplace.json
│   ├── valid_minimal/.claude-plugin/marketplace.json
│   ├── all_four_source_types/.claude-plugin/marketplace.json
│   ├── with_plugin_root/.claude-plugin/marketplace.json
│   ├── unknown_source/.claude-plugin/marketplace.json
│   ├── missing_name/.claude-plugin/marketplace.json
│   ├── dotdot_path/.claude-plugin/marketplace.json
│   └── duplicate_plugins/.claude-plugin/marketplace.json
└── registry/
    ├── empty.json
    ├── one_entry.json
    ├── two_entries.json
    ├── unsupported_version.json
    └── corrupt.json
```

## 10. Testing Strategy

TDD: every test in this section is written before the corresponding production code. No mocked filesystem; every test uses `t.TempDir()`. Real `git` binary on PATH; tests are gated with `testing.Short()` skipping the slow ones and an env-var (`SERF_LIVE_NET_TESTS`) gating any test that touches a real remote.

### 10.1 `ParseMarketplace` — table-driven (`marketplace_test.go`)

| # | Case | Fixture | Expect |
|---|---|---|---|
| 1 | Full valid marketplace | `valid_full` | `Marketplace.Name == "company-tools"`, plugins parsed, owner populated |
| 2 | Minimal valid marketplace | `valid_minimal` | name + owner + empty plugins; warning logged "no plugins" |
| 3 | Directory plugin source resolved | `valid_full` | `plugins[0].Source.Kind == SourceDirectory`, `Path` absolute, under root |
| 4 | All four source types in one file | `all_four_source_types` | each kind decoded, fields preserved |
| 5 | `metadata.pluginRoot` prefixes relative paths | `with_plugin_root` | resolved Path == `<root>/plugins/foo` |
| 6 | Top-level description preferred over metadata.description | inline | top-level wins |
| 7 | Top-level version preferred over metadata.version | inline | top-level wins |
| 8 | `$schema` preserved | `valid_full` | `Marketplace.Schema != ""` |
| 9 | Strict default | inline plugin with no `strict` field | `entry.Strict == nil` |
| 10 | Strict explicit false | inline | `*entry.Strict == false` |
| 11 | Missing top-level name | `missing_name` | error contains `missing required field "name"` |
| 12 | Bad kebab-case name | inline `"Name": "Bad Name"` | error contains `must be kebab-case` |
| 13 | Plugins entry missing name | inline | error contains `plugins[0]: missing required field "name"` |
| 14 | Plugins entry missing source | inline | error contains `plugins[0]: missing required field "source"` |
| 15 | Unknown source type | `unknown_source` | error contains `unknown source type "npm"` (npm is deferred per parent §) |
| 16 | Path contains `..` | `dotdot_path` | error contains `may not contain ".."` |
| 17 | Path missing `./` prefix | inline `"source": "plugins/foo"` | error contains `must start with "./"` |
| 18 | github repo bad shape | inline `"repo": "no-slash"` | error contains `must be "owner/repo"` |
| 19 | git-subdir missing `path` | inline | error contains `missing "path" for source "git-subdir"` |
| 20 | Duplicate plugin name in same marketplace | `duplicate_plugins` | error contains `duplicate plugin name` |
| 21 | Unknown top-level field warning | inline `"experimentalChannels"` | parses; one stderr warning |
| 22 | Owner name required | inline `"owner": {"email": "x"}` | error contains `owner.name` |
| 23 | Plugins component-config fields preserved | inline plugin with `"hooks"` | `entry.Extra["hooks"]` non-empty |

### 10.2 Source-type fetchers (`sources/*_test.go`)

For each fetcher, a unit test exercises the success path and the major failure paths. Network is replaced with a per-test git daemon (one is enough — `git daemon` and `file://` URLs are both fine, see §11.2). `testing.Short()` skips the slow tests; presence of `git` on PATH is checked with `exec.LookPath`, and absent `git` triggers `t.Skip`.

`sources/directory_test.go`:

| # | Case | Expect |
|---|---|---|
| 1 | Source path is a real dir with marketplace.json | symlink created, returns no error |
| 2 | Source path missing | `ErrDirectorySource` |
| 3 | Source path present but no marketplace.json | `ErrMissingMarketplaceJSON` |
| 4 | installDir already symlinked at same target | no-op success |
| 5 | installDir already symlinked at different target | replaced |
| 6 | installDir is a real directory | `ErrInstallDirOccupied` |

`sources/github_test.go` (uses local file:// URL with a synthesised github URL the fetcher rewrites internally):

| # | Case | Expect |
|---|---|---|
| 1 | Fresh clone of repo with marketplace.json | success, file present |
| 2 | Existing checkout, fetch+reset on `update` | new commit visible |
| 3 | `ref` pinning checks out the tagged commit | HEAD == tag |
| 4 | Fetch failure does not wipe existing checkout | dir intact, error returned |
| 5 | Marketplace.json missing post-fetch | error |
| 6 | Timeout honoured | error contains `timed out` |

`sources/url_test.go`: same five cases as github, with a `file://` URL.

`sources/gitsubdir_test.go`:

| # | Case | Expect |
|---|---|---|
| 1 | Sparse clone fetches only requested subdir | only `<path>` present on disk |
| 2 | Plugin source variant with `Sha` | HEAD == sha |
| 3 | Subdir missing in repo | error |

### 10.3 Registry (`registry_test.go`)

| # | Case | Expect |
|---|---|---|
| 1 | Open missing file | empty Registry, no error |
| 2 | Open malformed file | `registry_corrupt` error |
| 3 | Open unsupported schemaVersion | error contains `unsupported registry schemaVersion` |
| 4 | Round-trip: Save -> Open | identical struct |
| 5 | Add + Save + Open shows entry | yes |
| 6 | Remove unknown entry | `ErrUnknownMarketplace` |
| 7 | Atomic write: kill between tmp and rename leaves prior file intact | yes (simulated by hooking `os.Rename` via interface or by running the write twice with a fault-injection seam) |
| 8 | Concurrent saves: two goroutines, both succeed serialised | final file has both entries |
| 9 | Lock timeout returns lock-contention error | error |
| 10 | Save creates parent directory | yes |

### 10.4 Trust store (`trust_test.go`)

| # | Case | Expect |
|---|---|---|
| 1 | Open missing file | empty store |
| 2 | Trust + Save + Open | persisted |
| 3 | `IsTrusted` false for unknown project | false |
| 4 | `IsTrusted` true for trusted project+marketplace | true |
| 5 | Untrust removes entry | gone |
| 6 | Atomic-write round-trip | yes |

### 10.5 CLI handlers (`cmd/serf/plugin/marketplace_test.go`)

Each subcommand has at least one happy path and one error path. The CLI tests construct a `Fetchers` with stubs in process, point `XDG_CONFIG_HOME` at `t.TempDir()`, and assert on stdout/stderr text and exit code.

| # | Subcommand | Case | Expect |
|---|---|---|---|
| 1 | `add` | `--source ./testdata/marketplaces/valid_full` | registry has entry, install symlink exists, exit 0 |
| 2 | `add` | `--source owner/repo` against stub git | clone happens, registry written, exit 0 |
| 3 | `add` | Bad source | exit 2, message |
| 4 | `add` | Marketplace already added with different source | exit 1, suggests `remove` |
| 5 | `add` | Project scope writes to `.serf/config.json` | file updated, no registry change |
| 6 | `remove` | Known entry | install dir gone, registry trimmed, exit 0 |
| 7 | `remove` | Unknown entry | exit 1 |
| 8 | `list` | Two entries | output rows match |
| 9 | `list` | `--json` | parses as expected schema |
| 10 | `update` | All entries | re-fetch invoked per entry |
| 11 | `update` | `<name>` | only that entry refetched |
| 12 | `update` | Fetch error on one entry | exit 1, other entries still attempted |
| 13 | `trust` / `untrust` | Round-trips through trust store | yes |

### 10.6 End-to-end (`marketplace_e2e_test.go`)

Gated on `SERF_LIVE_NET_TESTS=1`. Adds the real `obra/superpowers-marketplace` (github source), lists, updates, removes. The test is `t.Skip`ed by default to keep `go test ./...` hermetic.

### 10.7 Conventions

- No `t.Skip` on filesystem fixtures — they are checked in.
- `t.Skip("git not on PATH")` when `exec.LookPath("git")` fails. The CI image has git.
- Every test that writes to `XDG_CONFIG_HOME` calls `t.Setenv("XDG_CONFIG_HOME", t.TempDir())` so leaks across tests are impossible.
- Capture stderr via a swapped `*log.Logger` if one is wired into `internal/plugins`; otherwise pass `io.Writer` explicitly into the handlers and supply a `bytes.Buffer`.

### 10.8 Coverage gate

Every exported function in §2 has at least one direct test. Every error in §8 has a test case it surfaces from. Every fetcher in §4 has both a success and a failure case. `go test ./internal/plugins/... ./cmd/serf/plugin/...` is green.

## 11. Open Questions Settled Here

### 11.1 Trust prompt UX in non-interactive surfaces

**Decision.** CLI `serf plugin marketplace add` and foreground `serf` (when stdin is a tty) prompt inline as described in §7.2. Non-interactive surfaces — `serf-tui`, `serf-hub`, `serf serve`, and any CLI invocation without a tty on stdin — *refuse* untrusted project marketplaces: they are listed but not cloned, a `marketplace_trust_required` event is emitted, and the session proceeds without their plugins. To trust without a prompt, scripts pass one of: the repeatable `--trust-marketplace <name>` flag on `serf plugin marketplace add` and on the top-level `serf` command (session-scoped); the `SERF_TRUST_MARKETPLACES=<name>,<name>` env var (session-scoped); or the dedicated `serf plugin marketplace trust <name>` subcommand (persisted to `trusted_projects.json`).

**Rationale.** A blocking prompt inside `serf serve` would deadlock an HTTP-driven session. Refusing-until-trusted is the safe default; the env var covers ephemeral CI; the explicit `trust` subcommand covers operators who want a one-time persistent decision without an interactive run. `SERF_TRUST_ALL_MARKETPLACES=1` exists as an emergency hatch for sandboxed environments and logs a startup warning.

### 11.2 Network access during tests

**Decision.** Unit tests for the git-based fetchers use `file://` URLs pointing at a `t.TempDir()`-built bare repo. `git init --bare` + `git push` from a working copy in the same temp dir gives every test a deterministic, hermetic remote with no network or daemon. The git binary already speaks `file://`; no fixture process is needed. End-to-end tests that hit real remotes (github.com, gitlab.com) are gated on `SERF_LIVE_NET_TESTS=1` and `t.Skip`ed by default. `testing.Short()` additionally skips the slowest fetch tests so `go test -short ./...` stays fast.

**Rationale.** `file://` URLs are the simplest hermetic substrate that exercises the same `git clone`/`git fetch` code paths as a real remote, with zero infrastructure. A `git daemon` subprocess works but adds startup latency and port-bind flakiness on shared CI runners. An HTTP stub is the right shape for SP3's future "marketplace.json via plain URL" feature (deferred); reusing `file://` for git operations keeps the v1 test surface tight.

## 12. Out of Scope (Tracked for Later)

- `npm` source type. Parent spec (§Marketplace and Install) defers it; SP3 surfaces `unknown source type "npm"` as a parse error today.
- `autoUpdate` field on marketplaces. Parent spec defers it; SP3 parses it into `Marketplace.Extra` without acting.
- Reserved-name enforcement (`anthropic-plugins`, etc.). See §3.1.
- `extraKnownMarketplaces` and `strictKnownMarketplaces` enterprise-style policy. SP3's trust flow is the v1 substitute.
- `CLAUDE_CODE_PLUGIN_SEED_DIR` (pre-populated marketplace cache). Future ticket; the registry schema reserves `origin: "seed"` for it.
- Background auto-updates with `GITHUB_TOKEN` etc. `serf plugin marketplace update` is foreground-only in v1.
- `serf plugin validate` (Claude Code's `claude plugin validate`). Useful but separable; tracked separately.
