# Component spec 03 — Host configuration (schema + registry)

Parent: `2026-09-14-multi-host-evener-design.md` (§3, §4 item 3, §5 item 1).
Sibling: `2026-09-14-multi-host-02-attach-bridge.md`.
Spikes: `2026-09-14-multi-host-spikes-findings.md`.

## Purpose

Define how a controller hub learns which remote hosts it may manage: the
`[[hosts]]` table in `hub.toml`, its validation against the AppWire ref grammar,
and the in-memory registry that the rest of the hub reads. This is the data
layer only — it produces validated host entries and a registration hook. It does
not open connections, does not spawn SSH, and does not implement a source.

## Scope

- Add `Hosts []HostConfig` to the hub's `Config` and its TOML decoding path
  (`cmd/evener-hub/config.go`).
- A `HostConfig` entry `{ name, ssh, user?, evener_path?, roots[] }` where
  `name` is the source ID surfaced in refs (`name:<sessionID>`) and URLs.
- Validation of `name` against the ref grammar, reserve `local`, reject `..`,
  reject duplicates.
- An in-memory `HostRegistry` built from the validated list, with add-time
  cycle rejection.
- A registration hook the hub calls once at startup so component 05 (remote hub
  source) can attach a source per host.

## Non-scope

- The `RemoteHubSource` itself, its RPC mapping, and ref translation
  (`host:<thread>` ↔ remote `local:<thread>`) — component 05.
- SSH process spawn, keepalive, reconnect, deploy, version match — component 04.
- The UI host picker and fleet rendering — component 06; it already consumes any
  non-`local` source (see "Data flow").
- Hub-owned storage of remote credentials or per-host config — design §2
  ("host-owned storage"); only the connection entry lives here.

## Contract / interfaces

### `hub.toml` schema

```toml
[[hosts]]
name        = "m4"              # required; source ID used in refs and URLs
ssh         = "m4.local"        # required; ssh destination (host or user@host)
user        = "jesse"           # optional; override the ssh user
evener_path = "/usr/local/bin/evener"  # optional; remote binary path
roots       = ["/Users/jesse/src"]     # optional; remote working roots
```

- `name` → `appwire.Ref.SourceID`; refs surface as `name:<sessionID>`
  (design §5, `appwire/refs.go:11-20`).
- `ssh` is the destination token passed to `ssh`; component 04 owns how it is
  used. If `user` is set and `ssh` already carries a user, that is a validation
  error (ambiguous authority) — see "Error handling".
- `evener_path` and `roots` are advisory inputs to components 04/05; this
  component only stores and validates their shape (`roots` entries must be
  non-empty after trim; no path validation here).

### Go types

Added to `cmd/evener-hub/config.go`, beside `ProviderConfig`:

```go
// HostConfig is one [[hosts]] entry: a controller's connection entry for a
// remote hub. Name is the source ID used in refs and URLs.
type HostConfig struct {
    Name       string   `toml:"name"`
    SSH        string   `toml:"ssh"`
    User       string   `toml:"user"`
    EvenerPath string   `toml:"evener_path"`
    Roots      []string `toml:"roots"`
}
```

and a field on `Config` after `Providers` (`config.go:40`):

```go
Hosts []HostConfig `toml:"hosts"`
```

Nothing else in `Config` changes; `DefaultConfig()` (`config.go:64-77`) gains no
`Hosts` default (absent config ⇒ zero entries).

### Host registry

New internal package `cmd/evener-hub/internal/hostreg` (mirrors the existing
internal seam packages `appsource` and `hostlock`; keeps the graph logic
unit-testable without a hub). Exported surface:

```go
type Host struct { /* HostConfig fields, validated */ }

type Registry struct { /* mu sync.RWMutex; hosts map[string]Host; edges ... */ }

func New(entries []HostConfig) (*Registry, error) // validates + builds
func (r *Registry) Add(entry HostConfig) error    // add-time validation; ErrHostCycle etc.
func (r *Registry) Get(name string) (Host, bool)
func (r *Registry) All() []Host                    // sorted by name, like appsource.Registry.All
```

`All()` is the hook surface: component 05 iterates it to `appsource.Registry.Add`
one source per host (`cmd/evener-hub/internal/appsource/registry.go:20-24`).

### Source registration hook

This component supplies the data and the iteration point; it does **not** define
the source. In `cmd/evener-hub/web.go`, where the source registry is built
(`web.go:75`, `sources := newHubSourceRegistry(cfg)`), the hub will also build
`hostreg.New(cfg.Hosts)` and hand it to component 05's constructor. The exact
call shape belongs to the 05 spec; this spec fixes only:

- the registry is built once at hub startup from the validated `cfg.Hosts`;
- component 05 reads `Registry.All()` and is responsible for `Add`/`Remove` on
  the `appsource.Registry` as hosts attach/detach (`registry.go:20-30`);
- no host entry produces a source until component 05 registers one, so an
  unattached host is simply absent from `sources.All()`.

## Implementation approach (files, seams cited)

1. **Schema + parse** — `cmd/evener-hub/config.go`.
   - Add `HostConfig` beside `ProviderConfig` (`config.go:21-25`) and
     `Hosts []HostConfig` to `Config` (`config.go:29-61`). `ProviderConfig` is
     the existing precedent for an array-of-tables (`Providers
     []ProviderConfig `toml:"providers"``, `config.go:40`), so BurntSushi/toml
     decodes `[[hosts]]` the same way.
   - Validation runs inside `LoadConfig` (`config.go:113-137`) right after
     `applyConfigDefaults` (`config.go:132`), before the function returns, so a
     bad host list fails hub start exactly like the existing
     `validateMobileBaseURL` call (`config.go:133-135`). Add a new
     `validateHostConfigs(cfg.Hosts) error` helper next to
     `validateMobileBaseURL` (`config.go:142-175`).
   - Defaults: `DefaultConfig()` (`config.go:64-77`) has no entry; a file
     without `[[hosts]]` yields an empty slice. `LoadConfig` already returns
     `DefaultConfig()` with a nil error when the file is missing
     (`config.go:116-122`) — a hub with no `hub.toml` starts with no remote hosts.

2. **Name validation against the ref grammar** — reuse the real grammar,
   don't re-declare it. `appwire/refs.go:9`:

   ```go
   var refPartPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
   ```

   `refPartPattern` is unexported, so either export a helper in `appwire`
   (e.g. `func ValidRefPart(s string) bool`) or validate via the public
   `appwire.ParseRef` on a synthetic `name + ":x"` ref (`refs.go:23-35`).
   **Important gap this component must close:** `ParseRef` only rejects `..` in
   the *thread* part (`refs.go:31`); the source part is checked against the
   pattern alone (`refs.go:28`), and `..` *matches* `[A-Za-z0-9._~-]+`. So
   `validateHostConfigs` must additionally reject any `name` containing `..`
   and any `name` that is empty or `"local"`. Document that the stricter `..`
   rule is ours because names also appear in URL paths.
   Exporting `appwire.ValidRefPart` is the smaller diff and keeps one source of
   truth; the synthetic-`ParseRef` route avoids touching `appwire` at all. Pick
   one in review; the spec recommends exporting the helper.

3. **Registry + cycle rejection** — new package
   `cmd/evener-hub/internal/hostreg`.
   - `New` validates every entry through the shared name validator and rejects
     duplicates (`ErrDuplicateHost`) before constructing.
   - `Add` takes an entry and:
     - rejects a duplicate `name` (`ErrDuplicateHost`);
     - rejects `name == "local"` (`ErrReservedName`);
     - rejects an invalid `name` (`ErrInvalidName`);
     - rejects a self-edge and any back-edge that would close a cycle
       (`ErrHostCycle`). The registry stores directed edges keyed by the
       candidate's `name` and its upstream identities; a DFS from the candidate
       refuses if it reaches this hub's own identity or a node already on the
       current path.
   - **Self / cross-hub cycle identity.** The config file alone cannot see
     another hub's host list, so a pure config load can only catch
     intra-config duplicates and self-reference; the A→B→A case is knowable
     only when B's host list is learned over the channel. The registry therefore
     exposes the cycle check as `Add` plus an attach-time variant whose
     upstream list component 05 supplies from the attach handshake:
     `AddWithUpstreams(entry HostConfig, upstreamHostNames []string) error`.
     The 05 spec owns *producing* `upstreamHostNames`; this spec owns the check.
     (No hub identity token exists in the code today — see Open questions; the
     registry takes this hub's identity as an injected string so it is testable.)

4. **Wiring** — `cmd/evener-hub/web.go:75` builds the source registry inside
   `NewWebServer`; the host registry is built there or in `main.go` immediately
   before, from `cfg.Hosts`, and passed to component 05's constructor.
   `WebConfig` (`cmd/evener-hub/internal/hubcore/config.go:35-93`) gains a
   `Hosts *hostreg.Registry` (or the component-05 constructor takes it
   directly); the 05 spec decides which. No other `WebConfig` field is touched.

## Data flow

```
hub.toml [[hosts]]  →  LoadConfig (config.go:123 toml.Decode)
                    →  validateHostConfigs (new; refs.go:9 grammar)
                    →  cfg.Hosts
                    →  hostreg.New(cfg.Hosts)          [startup, once]
                    →  Registry.All()  →  component 05 registers a Source per host
                                              (appsource.Registry.Add, registry.go:20-24)
                    →  sources.All()   →  existing fleet/tree fan-out
```

The last hop already exists and is the reason this component needs no UI work:
`cmd/evener-hub/web_api_tree.go:467-470` and `:716-719` skip
`source.ID() == "local"` and treat every other source as remote, folding its
threads into the tree and last-known-good cache. So once component 05 registers
a source named `m4`, the fleet view sees `m4:<sessionID>` refs with no new
config plumbing.

## Error handling

- Missing `hub.toml` → `DefaultConfig()` and nil error (`config.go:116-122`);
  hub starts with zero hosts. Compatibility requirement satisfied.
- Parse error → `"parse config: %w"` (`config.go:129-131`), unchanged.
- Invalid host entry → `LoadConfig` returns
  `fmt.Errorf("validate hosts: %w", err)` before returning `cfg`; hub startup
  aborts with `[hub] config: ...` (`cmd/evener-hub/main.go:158-161`). Errors are
  named sentinels (`ErrInvalidName`, `ErrDuplicateHost`, `ErrReservedName`,
  `ErrHostCycle`) so tests assert with `errors.Is`.
- `user` set while `ssh` already contains `user@` → `ErrAmbiguousSSHUser`.
- Empty `ssh` after trim → `ErrMissingSSH`.
- `roots` entries empty after trim → `ErrEmptyRoot`.
- A cycle refused at add time leaves the registry unchanged (candidate not
  inserted); `Add` is atomic under its mutex.

## Testing

Unit tests, all without a hub or network:

- `config_test.go` (package `hub`): table test over `hub.toml` bodies — absent
  `[[hosts]]`; one valid host; duplicate names; `name = "local"`; `name = ".."`
  and `name = "a..b"`; `name` with `/`, space, `:`; empty `ssh`; `user` +
  `ssh = "u@h"`; multiple hosts. Assert `LoadConfig` returns the default config
  for the missing-file case (`config.go:116-122` behavior).
- `hostreg` package tests: `New` rejects a duplicate; `Add` rejects each named
  error; a self-edge (`m4` upstream `m4`) and a two-node cycle (`a` upstream of
  `b`, then adding `a` with upstream `b`) return `ErrHostCycle`; `All()` is
  name-sorted (mirroring `appsource.Registry.All`, `registry.go:39-52`);
  the registry is safe for concurrent `Get`/`All`.
- A ref-grammar parity test: for a corpus of names, assert
  `hostreg` accepts exactly the names `appwire.ParseRef(name+":x")` accepts,
  minus the `..` and `local` cases we deliberately exclude. This pins the
  grammar to `refs.go:9` rather than a duplicated regex.
- No fuzz target is required for this PR; the parser is exercised indirectly by
  the existing `config` loading and is table-driven here.

## Acceptance criteria

1. `hub.toml` with no `[[hosts]]` (and no `hub.toml` at all) loads without
   error and yields zero hosts.
2. `[[hosts]]` decodes into `Config.Hosts` via the existing
   `LoadConfig`/`toml.Decode` path (`config.go:113-137`).
3. An invalid `name` (fails `refPartPattern`, contains `..`, or equals `local`)
   makes `LoadConfig` fail with a wrapped sentinel naming the entry.
4. Duplicate names fail; a cycle added through `hostreg.Add` fails with
   `ErrHostCycle` and does not mutate the registry.
5. `Registry.All()` returns validated entries sorted by name, and component 05
   can register one `appsource.Source` per entry through
   `appsource.Registry.Add` (`registry.go:20-24`).
6. `go test ./cmd/evener-hub/... ./cmd/evener-hub/internal/hostreg/...` passes,
   including the existing `LoadConfig` tests
   (`cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go:70-93`).

## PR size estimate (LOC)

- `HostConfig` + `Config.Hosts` + `validateHostConfigs`: ~60–90 LOC.
- `appwire.ValidRefPart` export (or synthetic-`ParseRef` helper): ~10–20 LOC.
- `internal/hostreg`: ~120–180 LOC (types, mutex map, cycle DFS, sentinels).
- Wiring in `web.go`/`main.go`/`WebConfig`: ~20–40 LOC.
- Tests: ~200–300 LOC.

Total ≈ **400–600 LOC**, one reviewable PR with no network, SSH, or UI surface.

## Open questions

- **Hub identity for cycle detection.** The code has no stable hub identity
  token (`ServerName` is a test-only `appserver.ServerConfig` field; the hub
  binds `cfg.Addr`, `cmd/evener-hub/main.go:340-345`). Detecting A→B→A needs a
  comparable identity for "this hub". Options: use the configured `ssh`
  destination as identity, add an explicit `self`/`host_id` field, or detect
  the cycle structurally at attach time (B's host list contains our name). The
  03 spec injects the identity; the 05 spec must fix where it comes from.
- **Where the registry is built.** `web.go:75` (inside `NewWebServer`) versus
  `main.go` before `newWebServer` (`main.go:372`). Building it in `main.go`
  keeps `NewWebServer` free of `hub.toml` knowledge; building it in `web.go`
  matches how `newHubSourceRegistry` is already wired. Recommendation:
  `main.go`, passed through `WebConfig`.
- **Is `roots` validated or opaque?** This spec only trims/validates non-empty.
  Whether roots must be absolute or exist on the remote is component 05's
  preflight concern; leave them opaque here.
- **`evener_path` empty vs set.** Empty means "resolve `evener` on the remote
  PATH" (component 04). Confirm no default should be baked into the schema.
