# Component spec 03 — Host configuration (schema + registry)

Parent: `2026-09-14-multi-host-evener-design.md` (§3, §4 item 3, §5 item 1).
Sibling: `2026-09-14-multi-host-02-attach-bridge.md`.
Spikes: `2026-09-14-multi-host-spikes-findings.md`.

**Implementation status.** The `hostreg` package, `validateHostConfigs`, the
`hub.toml` `[[hosts]]` decoding (`cmd/evener-hub/config.go`;
`cmd/evener-hub/internal/hostreg`), the `sshconn` connection manager
(`cmd/evener-hub/internal/sshconn`), and the `hubcore.WebConfig` `RemoteHost*`
wiring (`cmd/evener-hub/main.go:549-581`) are all on `main`. The deltas this
spec still lists as requirements are marked inline.

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
- **Recorded decision (Jesse, 2026-09-26): no 64-source cap.** This bullet
  previously required a host list larger than the navigation manifest's
  `sources` cap to be rejected at config load and in the registry add paths
  (`New`/`Add`/`AddWithUpstreams`) with a named `ErrTooManyHosts`, because
  component 06 caps the manifest at 64 sources including `local`. That
  requirement is **withdrawn, not deferred**: no count check is implemented
  (`validateHostConfigs` builds a throwaway `hostreg.Registry`; no
  `ErrTooManyHosts` exists in the tree) and none is to be added. Bounded fan-out
  is a **risk accepted for v1**: an operator who configures more than 63 hosts
  can drive navigation past that cap, which fails navigation for the whole hub
  until the config shrinks. Do not implement the cap without a new decision.
- An in-memory `hostreg.Registry` built from the validated list, rejecting
  duplicates at add time (a self-edge is rejected only when an explicit upstream
  list is supplied; see §"Source registration hook" and "Open questions" for the
  cycle and multi-hop deferral).
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
evener_path = "/usr/local/bin/evener"    # optional; remote binary path
roots       = ["/Users/jesse/src"]       # optional; remote working roots
config_path = "/etc/evener/hub.toml"     # optional; the host's hub.toml
addr        = "127.0.0.1:9180"           # optional; the host hub's loopback address
key_path    = "~/.ssh/id_m4"              # optional; SSH identity file for this host
```

- `name` → `appwire.Ref.SourceID`; refs surface as `name:<sessionID>`
  (design §5, `appwire/refs.go`).
- `ssh` is the destination token passed to `ssh`; component 04 owns how it is
  used. If `user` is set and `ssh` already carries a user, that is a validation
  error (ambiguous authority) — see "Error handling". Non-default ports and
  jump hosts — and identity files, unless the entry sets `key_path` — are
  expressed through the user's `ssh_config`, never by smuggling options into
  `ssh` (component 04, §"SSH channel argv"):
  `ssh` is a destination, not an option string.
- `evener_path` and `roots` are advisory inputs to components 04/05; this
  component only stores and validates their shape (`roots` entries must be
  non-empty after trim; no path validation here).
- `key_path` (optional) is the SSH private-key file the controller dials with.
  It is a component-08 addition to the stored schema: the machine-managed
  `hub.toml` carries it so a UI-added host's key path round-trips through a
  rewrite (component 08, §6).
- **`config_path` / `addr` — the connection parameters both halves must
  agree on (corrected contract).** The bridge resolves the host hub's address
  and capability-token state root from the `hub.toml` it reads; component 04's
  restart/health path needs the same address. These two fields are what
  component 04 passes to `hub attach --stdio [--config <config_path>]
  [--addr <addr>]` — `--config` **before** `--addr`, every value shell-quoted
  (component 04, §"SSH channel argv") — and what it must use for the
  restart/health probe in place of a manager-wide default.
- **They are coupled: set both or neither.** A host's config file and its listen
  address describe one layout, so `validateHostConfigs` rejects an entry that
  sets exactly one of them with a named sentinel (component 04, §"Address,
  config path, token"). Setting only one is a split-brain: the bridge attaches
  to the custom config's address while the manager probes/restarts the default
  address (or the reverse) after the binary has already been replaced.
  **Setting neither is only safe on the documented default.** Component 02
  resolves the bridge's address as `--addr`, else the host's own `hub.toml`
  `addr`, else `127.0.0.1:9180` — so the bridge's "default" is whatever the
  host's config names, which may be custom. The manager does not read that file
  (component 04, §"Address, config path" — "not probed") and its own fallback is
  the literal `127.0.0.1:9180`. An entry that sets neither therefore asserts the
  host hub uses the documented default address; a host whose `hub.toml` (its
  default `~/.config` file included) names a **non-default** listen address
  **must** set both `config_path` and `addr`, or the manager probes and restarts
  the default address — which on the host belongs to nothing this entry
  describes and could be an unrelated hub bound there. Absent `addr`, the
  manager uses `127.0.0.1:9180` and refuses a restart its identity checks cannot
  match (component 04, §"Address, config path, token"; §5)
  rather than restarting whatever holds the default port. Because
  `validateHostConfigs` runs on the controller and cannot see the host's file,
  this half is an operator contract, not a load-time check. **Implementation
  status:** the shipped `hostreg.Host`
  carries `ConfigPath` and `Addr` (`hostreg/hostreg.go`), and `channelArgv`
  passes whichever is present; the paired validation is the implementing PR's
  requirement, not a present fact.
  **`addr` host validation, and the exposure contract it must not weaken.**
  The host hub's listen address is consumed by component 04 as an SSH-side
  `curl` target (health probe) and by the restart identity check, so an
  unvalidated non-loopback value would let those paths reach an arbitrary
  reachable host. `validateHostConfigs` must therefore reject an `addr` whose
  host part is not `127.0.0.1`, `::1`, `localhost`, `0.0.0.0`, `::`, or empty,
  with a named error, so a non-loopback value can never be passed to the health
  check or restart (component 04, §"5. Version auto-match + restart"). Only the
  host spelling (a loopback literal, or the wildcard that normalizes to it —
  below) and the port vary between hosts.
  **The wildcard spelling is a transport-scoping decision, not an exposure
  grant.** Design §2's "No HTTP port exposed beyond the host's loopback"
  describes what **this feature exposes**, and the feature exposes nothing
  network-facing: the controller's only paths to the host hub are the SSH
  channel and the two on-host loopback probes (component 04's `curl`, component
  02's bridge dial), and this feature never binds, opens, or dials a
  non-loopback address of its own. `0.0.0.0`/`::` are accepted because they
  name the host's loopback address on every interface and are a common
  pre-existing host binding; the manager normalizes the spelling to
  `127.0.0.1`/`[::1]` for its own dial and probe (`loopbackAddr`, component 02)
  and never uses the wildcard as a network address. Accepting the spelling does
  **not** promise that the host hub is unreachable from the network — a hub
  actually bound wildcard *is* an HTTP port exposed beyond loopback, and that
  exposure is the host operator's own configuration, which this feature neither
  creates nor requires. The operator contract where a host hub binds wildcard
  is therefore explicit: (a) the hub's capability/auth token is still required
  on every request, so the wildcard bind exposes an authenticated surface, not
  an open one; (b) the host operator is responsible for a firewall or binding
  that restricts that port to trusted networks, since the feature neither
  configures nor verifies one; and (c) no controller-side path may ever use the
  wildcard port — every controller→hub path is the SSH channel or an on-host
  loopback probe, and the controller never dials the host's address directly.
  A deployment that wants a **hard** loopback guarantee must instead reject
  `0.0.0.0`/`::` outright and refuse to restart or attach a hub actually bound
  wildcard; that stronger option supersedes component 04's restart identity
  normalization and acceptance criterion 13 and is recorded as an optional
  hardening in the keystone's code follow-ups, not adopted here.
  **Implementation status:** the shipped `hostreg.Host.Addr` is stored
  unvalidated; this check is the implementing PR's requirement.

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
    ConfigPath string   `toml:"config_path"` // optional; host hub.toml the bridge must read
    Addr       string   `toml:"addr"`        // optional; host hub loopback host:port
}
```

and a field on `Config` after `Providers` (`config.go`):

```go
Hosts []HostConfig `toml:"hosts"`
```

Nothing else in `Config` changes; `DefaultConfig()` (`config.go`) gains no
`Hosts` default (absent config ⇒ zero entries).

### Host registry

New internal package `cmd/evener-hub/internal/hostreg` (mirrors the existing
internal seam packages `appsource` and `hostlock`; keeps the graph logic
unit-testable without a hub). Exported surface:

```go
// Host is one validated [[hosts]] entry. It is hostreg's own dependency-neutral
// copy of the hub's HostConfig fields: hostreg must not import the hub package,
// so the two types are distinct and the hub converts at the boundary.
type Host struct {
    Name       string
    SSH        string
    User       string
    EvenerPath string
    Roots      []string
    ConfigPath string
    Addr       string
}

type Registry struct { /* mu sync.RWMutex; hosts map[string]Host; edges ... */ }

func New(entries []Host) (*Registry, error) // validates + builds
func (r *Registry) Add(entry Host) error    // = AddWithUpstreams(entry, nil)
func (r *Registry) AddWithUpstreams(entry Host, upstreamHostNames []string) error
func (r *Registry) SetUpstreams(name string, upstreamHostNames []string) error
func (r *Registry) Get(name string) (Host, bool)
func (r *Registry) All() []Host                    // sorted by name, like appsource.Registry.All
```

`SetUpstreams` is the shipped seam for the one case `AddWithUpstreams` cannot
cover: `New` registers every config-loaded host through `Add` (no edges), so an
edge learned after startup must be attached to an already-registered host, and
`AddWithUpstreams` would only report `ErrDuplicateHost`.

**The constructor takes no self-identity.** `New(entries []Host)` is exactly
that signature: there is no `self`/`host_id` parameter, no option, and no
setter. Cycle detection does not need one — see "Registry + cycle rejection"
below. (An earlier revision of this spec claimed the identity was injected into
the constructor "so it is testable"; that was wrong: the code has no such
parameter, and the check is rooted at the candidate being added.)

**Type boundary (why `hostreg.Host` is not `hub.HostConfig`).** `HostConfig`
stays in `cmd/evener-hub/config.go`, where it is a TOML decode target on
`Config`; `hostreg` declares its own `Host` and imports only `appwire`. The
import direction is therefore `hub → hostreg` and nothing else — no cycle and no
type mismatch. `LoadConfig` validates by converting `cfg.Hosts` into
`[]hostreg.Host` and calling `hostreg.New` on that throwaway list
(`validateHostConfigs`), and the hub converts the same way, field for field
  (`Name`, `SSH`, `User`, `EvenerPath`, `ConfigPath`, `Addr`, `Roots` — see
  "`config_path` / `addr`"), when it builds the live registry at
startup. That conversion is the only place the two types meet.

`All()` is the hook surface: component 05 iterates it to `appsource.Registry.Add`
one source per host (`cmd/evener-hub/internal/appsource/registry.go`).

### Source registration hook

This component supplies the data and the iteration point; it does **not** define
the source. `cmd/evener-hub/main.go` builds the `hostreg.Registry` from the
converted `cfg.Hosts` (§"Host registry") and hands the converted entries to the
source registry through `hubcore.WebConfig`: `newHubSourceRegistry`
(`cmd/evener-hub/app_rpc.go`) iterates `cfg.RemoteHosts` and adds one source per
entry, wiring `cfg.RemoteHostClient` / `cfg.RemoteHostFacts` /
`cfg.RemoteHostOnline` / `cfg.RemoteHostClientIfAttached` /
`cfg.RemoteHostHandshake` (component 05). This spec fixes only:

- the registry is built once at hub startup from the validated `cfg.Hosts`;
- component 05 registers **one source per configured host at startup**, from the
  configured entries, whether or not an SSH channel exists yet
  (`registry.go`). Nothing is added to or removed from the
  `appsource.Registry` on attach/detach;
- consequently `sources.All()` always equals the configured host list, and an
  unattached host is **present but offline**: its `RemoteHubSource` reports
  `Online() == false` and fails calls with `SessionUnavailable`. Connectivity is
  the source's state, never the source's presence — component 06's fleet view
  depends on that (see `06-fleet-view.md`, §"Read contract").

## Implementation approach (files, seams cited)

1. **Schema + parse** — `cmd/evener-hub/config.go`.
   - Add `HostConfig` beside `ProviderConfig` (`config.go`) and
     `Hosts []HostConfig` to `Config` (`config.go`). `ProviderConfig` is
     the existing precedent for an array-of-tables (`Providers
     []ProviderConfig `toml:"providers"``, `config.go`), so BurntSushi/toml
     decodes `[[hosts]]` the same way.
   - Validation runs inside `LoadConfig` (`config.go`) right after
     `applyConfigDefaults` (`config.go`), before the function returns, so a
     bad host list fails hub start exactly like the existing
     `validateMobileBaseURL` call (`config.go`). Add a new
     `validateHostConfigs(cfg.Hosts) error` helper next to
     `validateMobileBaseURL` (`config.go`).
   - Defaults: `DefaultConfig()` (`config.go`) has no entry; a file
     without `[[hosts]]` yields an empty slice. `LoadConfig` already returns
     `DefaultConfig()` with a nil error when the file is missing
     (`config.go`) — a hub with no `hub.toml` starts with no remote hosts.

2. **Name validation against the ref grammar** — reuse the real grammar,
   don't re-declare it. `appwire/refs.go` exports the wrapper over that
   grammar:

   ```go
   var refPartPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

   func ValidRefPart(s string) bool { return refPartPattern.MatchString(s) }
   ```

   `appwire.ValidRefPart` ships (`appwire/refs.go`) and is the raw grammar
   only — one source of truth, and it keeps `hostreg` free of hub imports,
   which the type boundary above requires. `hostreg.ValidateName`
   (`hostreg.go`) consumes it — `New`, `Add`, and `AddWithUpstreams` all
   validate through it, and the config path reaches it through
   `validateHostConfigs` → `hostreg.New` (`config.go`) — and layers the
   reserved-name, `"."`, and `..` checks on top. Those stricter rules are ours
   because names also appear in URL paths and as filesystem segments:
   `ParseRef` rejects `..` only in the *thread* part (`refs.go`), the source
   part is checked against the pattern alone, and both `"."` and `..` *match*
   `[A-Za-z0-9._~-]+`. `ValidateName` therefore rejects any `name` equal to
   `"."` or containing `..`, and any `name` that is empty or `"local"`.

3. **Registry + cycle rejection** — new package
   `cmd/evener-hub/internal/hostreg`.
   - `New` validates every entry through the shared name validator and rejects
     duplicates (`ErrDuplicateHost`) before constructing.
   - `Add` takes an entry and:
     - rejects a duplicate `name` (`ErrDuplicateHost`);
     - rejects `name == "local"` (`ErrReservedName`);
     - rejects an invalid `name` (`ErrInvalidName`);
     - rejects a self-edge and any back-edge that would close a cycle
       (`ErrHostCycle`) **when an upstream list is supplied**. Per the shipped
       `checkCycleLocked`: the registry stores directed edges keyed by each host
       name, and the DFS starts at the **candidate being added**, seeded with
       the candidate itself on the current path. It refuses when it reaches the
       candidate again (self-edge or back-edge) or a node already on the current
       path (a cycle among the upstreams). `Add` passes no upstreams, so it
       cannot produce `ErrHostCycle`; only `AddWithUpstreams`/`SetUpstreams`
       can. No hub identity is involved, which is why `New` needs no `self`
       parameter (finding 13).
   - **Self / cross-hub cycle identity.** The config file alone cannot see
     another hub's host list, so a pure config load can only catch
     intra-config duplicates; the A→B→A case is knowable only when B's host
     list is learned over the channel. The registry therefore
     exposes the cycle check as `Add` plus an attach-time variant whose
     upstream list component 05 supplies from the attach handshake:
     `AddWithUpstreams(entry Host, upstreamHostNames []string) error`.
     **Deferred:** v1 callers pass no upstreams — `Add` is
     `AddWithUpstreams(entry, nil)` — because no AppWire method reports a hub's
     configured hosts, so component 05 cannot yet produce `upstreamHostNames`
     (it records the same deferral in `05-remote-hub-source.md` §Open
     questions). The exported check is the seam a later host-list RPC attaches
     to; until then attach-time A→B→A detection does not happen. There is no hub
     identity token in the code today and none is needed by `New`: the DFS is
     rooted at the candidate (§"Host registry"), and upstream names are a call
     argument, never constructor state.
   - **What v1 actually promises (qualified).** The parent design's "cycles are
     refused at add time" must be read narrowly: **no cycle check runs from the
     config file alone.** Duplicate names and invalid/reserved names fail at
     load/add time, but a self-edge (`m4` upstream `m4`) is caught *only* when an
     explicit upstream list is supplied, because `Add` is
     `AddWithUpstreams(entry, nil)` and `[[hosts]]` has no upstream field. It is
     **not** multi-hop A→B→A detection either: the config file cannot see
     another hub's host list, so a controller A configured with host B and B
     configured with host A loads cleanly on both sides. Multi-hop detection
     requires the deferred host-list RPC (component 05, §Open questions item 4)
     that supplies `upstreamHostNames`; record it as deferred, not as enforced.

     **v1 performs no cycle detection at all from the config alone, and that must
     be stated plainly rather than implied.** `Add` is `AddWithUpstreams(entry,
     nil)`, every config-loaded host enters through `New`→`Add`, and no caller
     supplies upstream names today, so `checkCycleLocked` (hence `ErrHostCycle`)
     is reachable only by a direct call to `AddWithUpstreams`/`SetUpstreams` with
     an explicit upstream list. Tests must therefore assert `ErrHostCycle`
     through those two methods, **not** through `Add`: a config-load or `Add`
     test that expects `ErrHostCycle` cannot pass against the shipped code. The
     `AddWithUpstreams`/`SetUpstreams` seam is live API exercised by tests, but
     it is dead in production until the host-list RPC lands (component 05,
     §Open questions item 4); until then a multi-hop cycle is not refused and
     configuration is acyclic by convention only.

4. **Wiring** (design settled; **shipped**):
   `cmd/evener-hub/main.go` converts `cfg.Hosts` into `[]hostreg.Host` and calls
   `hostreg.New` once at startup, then builds the component-04 manager over that
   registry (`sshconn.New(hostRegistry, sshconn.Options{...})`). The same
   converted entries travel through `hubcore.WebConfig` as `RemoteHosts`,
   together with the component-04 seams `RemoteHostClient` (returns the current
   `ch.Client()`), `RemoteHostFacts` (a non-dialing, attached-only lookup that
   reads one `sshManager.ChannelIfAttached` value and takes the preflight from
   that channel only when its client is the exact generation the probe
   resolved; component 04, §"Go surface"), and
   `RemoteHostOnline` (`sshManager.Attached`), `RemoteHostClientIfAttached`
   (`sshManager.ClientIfAttached` — the non-dialing, attached-only client
   lookup component 05's notification rebind and component 06's snapshot use),
   and `RemoteHostHandshake` (a non-dialing, attached-only lookup built on the
   same `sshManager.ChannelIfAttached` primitive as `RemoteHostFacts`: the
   `remoteHostHandshakeForChannel` closure answers the attach handshake facts
   component 05's capability probe reads for
   `ProtocolVersion`/`ServerInfo`/`SourceID`/`Features`, refusing when the
   channel's client is not the probe's; component 04, §"Go surface"; component
   05, §"Capability probe"). Both facts seams read a single
   `sshManager.ChannelIfAttached` value and apply the generation guard at the
   call site (`ch.Client() == client`), so a reconnect between the probe's
   client lookup and its facts read can never produce a mixed-generation
   snapshot. That guard deliberately lives in the `remoteHostFactsForChannel` /
   `remoteHostHandshakeForChannel` closures, **not** inside an accessor:
   `Manager.PreflightIfAttached` and `Manager.HandshakeIfAttached` exist but have
   no production caller. `newHubSourceRegistry`
   (`cmd/evener-hub/app_rpc.go`) registers one source per `RemoteHosts` entry at
   startup (§"Source registration hook"). No other `WebConfig` field is touched.
   (The earlier revision's `Hosts *hostreg.Registry` field is not the
   implemented shape.) **Implementation status:** shipped. The `hostreg` package
   and `hostreg.New`, the `sshconn` manager, and the `hubcore.WebConfig`
   `RemoteHosts` / `RemoteHostClient` / `RemoteHostFacts` / `RemoteHostOnline`
   fields landed with components 03/04; the attached-only additions this section
   names (`Manager.ChannelIfAttached` / `Manager.ClientIfAttached`, wired
   through `RemoteHostFacts` / `RemoteHostHandshake` /
   `RemoteHostClientIfAttached`) landed with the attached-only enforcement PR.
   `RemoteHostFacts` and `RemoteHostHandshake` are non-dialing and
   generation-guarded (`ChannelIfAttached` + the call-site client-identity
   check, refusing with `SessionUnavailable`), so the capability probe cannot
   attach a dormant host through them nor cache two connections' facts as one
   snapshot; `RemoteHostClient` remains the `Ensure`-backed dial reserved for the
   explicit attach triggers (an explicit host in `thread/list`'s `SourceIDs`,
   and component 06's `evener/host/attach` Connect action).

## Data flow

```
hub.toml [[hosts]]  →  LoadConfig (config.go toml.Decode)
                    →  validateHostConfigs (new; refs.go grammar)
                    →  cfg.Hosts
                    →  hostreg.New([]hostreg.Host{...})  [startup, once; boundary conversion]
                    →  Registry.All()  →  component 05 registers one Source per host
                                              (all configured hosts, attached or not)
                                              (appsource.Registry.Add, registry.go)
                    →  sources.All()   →  existing fleet/tree fan-out
                                        →  an unattached host's source reports offline
```

The last hop already exists and is the reason this component needs no UI work:
`apiTreeSources` in `cmd/evener-hub/web_api_tree.go` skips `source.ID() ==
"local"` and treats every other source as remote, folding its threads into the
tree and last-known-good cache (`refreshRemoteThreadSnapshot`,
`navigationSnapshotInputs`). So once component 05 registers a source named
`m4`, the fleet view sees `m4:<sessionID>` refs with no new config plumbing.

## Error handling

- Missing `hub.toml` → `DefaultConfig()` and nil error (`config.go`);
  hub starts with zero hosts. Compatibility requirement satisfied.
- **An explicitly supplied `--config` that is missing or invalid is an error,
  never a silent default.** `LoadConfig` (`cmd/evener-hub/config.go`) returns
  `DefaultConfig()` with a nil error whenever the file does not exist, so today
  an explicit `--config /nonexistent` silently yields defaults. The fallback
  must apply **only** to the implicit default path: when the operator passes
  `--config <path>` (hub or `hub attach --stdio`, component 02) and that file is
  missing or unparseable, the process exits nonzero with a diagnostic naming the
  path. Otherwise a custom-layout host attaches to the wrong state root/token
  and `addr` and reports a misleading connection failure. Tracking
  explicitness belongs at the flag layer (`cmd/evener-hub/main.go`,
  `cmd/evener-hub/attach.go`) or in a `LoadConfig` variant; the shipped
  signature cannot distinguish the two. Recorded as a code follow-up.
- Parse error → `"parse config: %w"` (`config.go`), unchanged.
- Invalid host entry → `LoadConfig` returns
  `fmt.Errorf("validate hosts: %w", err)` before returning `cfg`; hub startup
  aborts with `[hub] config: ...` (`cmd/evener-hub/main.go`). Errors are
  named sentinels (`ErrInvalidName`, `ErrDuplicateHost`, `ErrReservedName`,
  `ErrHostCycle`) so tests assert with `errors.Is`.
- `user` set while `ssh` already contains `user@` → `ErrAmbiguousSSHUser`.
- Empty `ssh` after trim → `ErrMissingSSH`.
- `roots` entries empty after trim → `ErrEmptyRoot`.
- More than 63 remote hosts → **not rejected** (recorded decision, §Scope: the
  cap is withdrawn). The manifest's 64-source limit
  (`cmd/evener-hub/navigation_projection.go`) is a hard downstream limit, so an
  over-limit config breaks navigation for every host; that is the accepted v1
  risk, not a validation error.
- Exactly one of `config_path`/`addr` set → a named error (the two are coupled;
  see §"`config_path` / `addr`").
- A cycle refused at add time leaves the registry unchanged (candidate not
  inserted); `Add` is atomic under its mutex.

## Testing

Unit tests, all without a hub or network:

- `config_test.go` (package `hub`): table test over `hub.toml` bodies — absent
  `[[hosts]]`; one valid host; duplicate names; `name = "local"`; `name = ".."`
  and `name = "a..b"`; `name` with `/`, space, `:`; empty `ssh`; `user` +
  `ssh = "u@h"`; multiple hosts. Assert `LoadConfig` returns the default config
  for the missing-file case (`config.go` behavior).
- `hostreg` package tests: `New` rejects a duplicate; `Add` rejects each named
  error; a self-edge (`m4` upstream `m4`) and a two-node cycle (`a` upstream of
  `b`, then adding `a` with upstream `b`) return `ErrHostCycle` **through
  `AddWithUpstreams`/`SetUpstreams`**, not `Add` (which passes no upstreams, so
  it can never reach `checkCycleLocked`; see §"Host registry"); `All()` is
  name-sorted (mirroring `appsource.Registry.All`, `registry.go`); the registry
  is safe for concurrent `Get`/`All`.
- **No host-count boundary test** (recorded decision, §Scope): the 64-source cap
  is not implemented, so a 63/64-entry count test has nothing to assert.
- A ref-grammar parity test: for a corpus of names, assert
  `hostreg` accepts exactly the names `appwire.ParseRef(name+":x")` accepts,
  minus the `.`, `..`, and `local` cases we deliberately exclude. This pins
  the grammar to `refs.go` rather than a duplicated regex.
- No fuzz target is required for this PR; the parser is exercised indirectly by
  the existing `config` loading and is table-driven here.

## Acceptance criteria

1. `hub.toml` with no `[[hosts]]` (and no `hub.toml` at all) loads without
   error and yields zero hosts.
2. `[[hosts]]` decodes into `Config.Hosts` via the existing
   `LoadConfig`/`toml.Decode` path (`config.go`).
3. An invalid `name` (fails `appwire.ValidRefPart`, equals `"."`, contains
   `..`, or equals `local`) makes `LoadConfig` fail with a wrapped sentinel
   naming the entry.
4. Duplicate names fail. A cycle is refused with `ErrHostCycle` only through an
   explicit upstream list — `hostreg.AddWithUpstreams` (or `SetUpstreams` for an
   already-registered host) — and does not mutate the registry.
   `hostreg.Add` passes no upstreams, so it cannot produce a cycle and must not
   be tested as if it could; v1 performs no cycle detection from the config
   alone.
5. `Registry.All()` returns validated entries sorted by name, and component 05
   can register one `appsource.Source` per entry through
   `appsource.Registry.Add` (`registry.go`).
6. `go test ./cmd/evener-hub/... ./cmd/evener-hub/internal/hostreg/...` passes,
   including the existing `LoadConfig` tests
   (`cmd/evener-hub/cov_small_faults_pass5_fuzz_test.go`).
7. `config_path`/`addr` decode into `HostConfig`/`hostreg.Host` when present and
   leave both zero when absent; component 04 receives them and passes them to
   the bridge and the restart/health path (cross-checked by that component's
   argv test).
8. Registry membership is independent of connectivity: with a host configured
   and its channel attached or dropped, `appsource.Registry.All()` is byte-for-
   byte the same set of source IDs.
9. **Withdrawn** (recorded decision, §Scope): no `ErrTooManyHosts`, at config
   load or on a programmatic add; an over-limit `[[hosts]]` list loads and is
   the operator's configuration.

## PR size estimate (LOC)

- `HostConfig` + `Config.Hosts` + `validateHostConfigs`: ~60–90 LOC.
- `appwire.ValidRefPart` export (shipped: `appwire/refs.go`, consumed by
  `hostreg.ValidateName`): already landed, no work left in this budget.
- `internal/hostreg`: ~120–180 LOC (types, mutex map, cycle DFS, sentinels).
- Wiring in `web.go`/`main.go`/`WebConfig`: ~20–40 LOC.
- Tests: ~200–300 LOC.

Total ≈ **400–600 LOC**, one reviewable PR with no network, SSH, or UI surface.

## Open questions

- **Hub identity for cycle detection (deferred, and not needed by the
  constructor).** The code has no stable hub identity token (`ServerName` is a
  test-only `appserver.ServerConfig` field; the hub binds `cfg.Addr`,
  `cmd/evener-hub/main.go`). Detecting A→B→A needs a comparable identity for
  "this hub" *plus* B's host list. Options for when that RPC lands: use the
  configured `ssh` destination as identity, add an explicit `self`/`host_id`
  field, or detect the cycle structurally at attach time (B's host list contains
  our name). None of this changes `New`'s signature: v1 passes no upstream
  names, and the DFS is candidate-rooted. Component 05 records the same
  deferral.
- **Where the registry is built (settled).** `main.go`, before the web server,
  passed through `hubcore.WebConfig` as `RemoteHosts`/`RemoteHostClient`/
  `RemoteHostFacts`/`RemoteHostOnline`/`RemoteHostClientIfAttached`/
  `RemoteHostHandshake` — the wiring described in §Implementation approach item
  4, shipped on `main` (`cmd/evener-hub/main.go:549-581`).
  `newHubSourceRegistry` consumes `cfg.RemoteHosts`; `NewWebServer` never reads
  `hub.toml`.
- **Is `roots` validated or opaque?** This spec only trims/validates non-empty.
  Whether roots must be absolute or exist on the remote is component 05's
  preflight concern; leave them opaque here.
- **`evener_path` empty vs set (resolved).** Empty is **not** "invoke the
  literal word `evener` via `PATH`"; component 04 resolves it **once per host**
  to one canonical absolute `run target` (`run_path`, component 04 §"SSH
  channel argv") — the host's own `command -v evener` result when it exists,
  else the installer's default `~/.local/bin/evener` — and uses that same path
  for invocation, deploy/install, restart identification, health, and attach.
  Component 04's push deploy additionally resolves the symlink target with its
  POSIX-sh resolver (plain `readlink`, never `readlink -f`, which BSD/macOS
  rejects) before the atomic `mv` (`sshconn/deploy.go`), so the deployed build
  lands at the real file the host runs rather than at a literal file named
  `evener`. A configured path means "the binary lives here" (its directory must
  exist on the host). No default is baked into the schema.
