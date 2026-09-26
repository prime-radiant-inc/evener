# Component spec 04 — SSH connection manager, deploy, and version auto-match

Parent: `2026-09-14-multi-host-evener-design.md` (§2, §4 item 4, §5, §6, §7).
Siblings: `2026-09-14-multi-host-01-stream-transport.md`,
`2026-09-14-multi-host-02-attach-bridge.md`,
`2026-09-14-multi-host-03-host-config.md`.
Spikes: `2026-09-14-multi-host-spikes-findings.md` (Spikes A and C).

**Citation convention.** Symbols (package, type, method, constant, file) are
authoritative and were verified on the implementation branches
(`multi-host-pr04a-ssh-channel`, `multi-host-pr04b-deploy-restart`,
`multi-host-pr05a..d`, `multi-host-pr06a-fleet-view-go`), all since landed on
`origin/main`. Line numbers are not used for Go or TypeScript sources; where
a non-Go line reference survives (docs, `install.sh`, Makefiles) treat it as a
hint, not as pinning; the reviewer's base is `origin/main`.

## Purpose

Turn one `[[hosts]]` entry (component 03) into a live, owned AppWire channel to
that host's hub, and keep the host running the controller's build. This
component is the only place that runs `ssh`. It

1. spawns `ssh <dest> <attach-bridge>` and hands the child's stdin/stdout to a
   `StreamTransport` (component 01) as the AppWire stream (component 02 bridge);
2. keeps that channel alive (keepalive, reconnect) and re-attaches after a drop;
3. preflights the host non-interactively (OS/arch, `HOME`/XDG, existing
   evener `version`/`protocol`/`launch_flags`);
4. deploys a matching `evener` binary for the host's `GOOS`/`GOARCH` and/or runs
   the installer;
5. on attach, compares the controller's build with the host's and, where a deploy
   path is configured, deploys the matching build, restarts the host hub, and
   verifies the restarted hub's build identity **by running the health probe on
   the host**; with no deploy path the host keeps its build and reports the skew.

It produces a connected AppWire transport + initialized client per host; it does
**not** map that client onto `appsource.Source` (component 05) or render hosts
(component 06).

**Registry membership is not this component's business.** Component 05 registers
one `appsource.Source` per configured host at hub startup and never adds or
removes one (components 03/05/06 agree on this). What this component publishes —
lifecycle events and `Manager.Attached` — is *connection state only*: it feeds
the source's `Online()` flag and navigation invalidation. There is no
register/unregister path anywhere in this component or in 05.

## Scope

- A new internal package (working name `cmd/evener-hub/internal/sshconn`) behind
  a `Runner` seam so process spawning and host commands are unit-testable without
  SSH.
- Spawning and owning the SSH channel:
  `ssh <dest> [<evener_path> hub attach --stdio]`, with the child's stdin/stdout
  as the AppWire byte stream and its stderr as diagnostics.
- Channel lifecycle: detect/report the hub link dropping, keepalive, bounded
  reconnect with backoff, re-attach to an already-running detached hub.
- Preflight over non-interactive SSH: `uname -s`/`uname -m` (or `sw_vers` on
  darwin), `HOME`, XDG resolution, and the host's existing
  `evener launch-check --protocol <controller protocol> --json`.
- Deploy: obtain the matching controller build for the host's `GOOS`/`GOARCH`
  (cross-compile, mirroring `make build-linux`) and push it (`scp`/`ssh cat`) or
  run `install.sh` on the host; `chmod +x`.
- Version auto-match on attach, where a deploy path is configured: compare
  controller `buildinfo.Version()` with the host's **running** hub `version`
  (`/api/health`) and its on-disk `launch-check` `version`; deploy when the
  on-disk binary differs and restart the host hub when the running version
  differs, then verify the **restarted** hub reports the expected `version`
  through `/api/health`, probed on the host, before re-attaching. With no
  deploy path there is nothing to converge: a protocol-compatible host on
  another build is attached and the difference is reported rather than refused
  (§5).
- Client handoff: publish the current client per host so component 05 can rebind
  after a reconnect without ever caching a dead client (§"Client handoff").

## Non-scope

- The bridge itself (`evener hub attach --stdio`) — component 02. This component
  only invokes it.
- The `RemoteHubSource`, ref translation, and the capability probe —
  component 05. This component stops at "an initialized `appwire.Client`".
- The `[[hosts]]` schema, validation, and registry — component 03. This component
  consumes validated entries.
- Fleet rendering, host picker, offline/dormant UI — component 06.
- Remote *tool execution* (`agent/execenv`) — design Non-goals. The SSH channel
  carries AppWire, not arbitrary commands.
- Remote credential storage — design §2 ("host-owned storage"); credential push
  is component 07.

## Contract / interfaces

### Go surface

New package `cmd/evener-hub/internal/sshconn`:

```go
// Manager owns the SSH channels for the configured hosts.
type Manager struct { /* reg *hostreg.Registry, opts Options, runner Runner; per-host locks + live channels */ }

func New(reg *hostreg.Registry, opts Options) *Manager

// Ensure returns a connected, initialized channel for host, spawning ssh,
// preflighting/deploying/version-matching as needed. Idempotent while attached.
// ctx bounds this attempt (preflight + handshake), not the channel's lifetime;
// the receive loop runs on a manager-owned context (see §2).
func (m *Manager) Ensure(ctx context.Context, name string) (*Channel, error)

// Attached reports whether host currently has a live, not-closed channel.
// This is the online signal components 05/06 consume; it never changes registry
// membership.
func (m *Manager) Attached(name string) bool

// ClientIfAttached returns the current initialized AppWire client for host ONLY
// while a live, not-closed channel is installed, and reports false otherwise.
// It never spawns ssh, preflights, deploys, or attaches: an unattached host (or
// one that dropped) yields (nil, false) with no side effect. This is the
// non-dialing lookup a background caller (component 06's 30s snapshot) and the
// notification broker's reconnect rebind (component 05) use in place of Ensure,
// so neither can eagerly attach a dormant host or re-dial a host that dropped
// between a check and the call. It reads the installed channel under the
// manager-wide mutex (m.mu), NOT the per-host gate, so it is safe to call from
// inside OnEvent (which holds the per-host gate); it derives from the same
// installed-and-not-closed predicate the Attached signal uses, so the two can
// never disagree within one observation.
func (m *Manager) ClientIfAttached(name string) (*appwire.Client, bool)

// ChannelIfAttached returns name's installed live channel ONLY while a live,
// not-closed channel is installed, and reports false otherwise. It is the one
// production backing accessor for the attached-only facts handshake:
// cmd/evener-hub/main.go builds both hubcore.WebConfig.RemoteHostFacts
// (remoteHostFactsForChannel) and RemoteHostHandshake
// (remoteHostHandshakeForChannel) from a single ChannelIfAttached lookup, with
// the generation guard `ch.Client() == client` applied at the call site so a
// supervisor reconnect between two lookups cannot splice one generation's facts
// onto another's client. Like ClientIfAttached it takes the manager-wide mutex,
// not the per-host gate, and never dials.
func (m *Manager) ChannelIfAttached(name string) (*Channel, bool)

// HandshakeIfAttached returns the InitializeResponse captured when host's
// current channel attached, ONLY while a live, not-closed channel is installed
// (reports false otherwise). Like ClientIfAttached it takes the manager-wide
// mutex, not the per-host gate, and never dials. It is NOT the production backer
// for hubcore.WebConfig.RemoteHostHandshake: that seam is
// remoteHostHandshakeForChannel, built on ChannelIfAttached with the channel
// generation guard (`ch.Client() == client`) at the call site. This accessor has
// no non-test caller; it is retained for tests and for a caller that does not
// need the generation guard. appwire.Client keeps its Features privately with no
// accessor, so component 05's capability probe reads
// ProtocolVersion/ServerInfo/SourceID/Features from the channel value
// (Channel.Handshake below).
func (m *Manager) HandshakeIfAttached(name string) (appwire.InitializeResponse, bool)

// PreflightIfAttached returns the preflight facts captured when host's current
// channel attached, ONLY while a live, not-closed channel is installed (reports
// false otherwise). Like ClientIfAttached and HandshakeIfAttached it takes the
// manager-wide mutex, not the per-host gate, and never dials. It is NOT the
// production backer for hubcore.WebConfig.RemoteHostFacts: that seam is
// remoteHostFactsForChannel, built on ChannelIfAttached with the channel
// generation guard (`ch.Client() == client`) at the call site, so a supervisor
// reconnect between the attach and the facts read cannot splice one generation's
// preflight onto another's client (component 05's capability probe populates
// HostCapabilities.OS/Arch from it; see Channel.Preflight below). This accessor
// has no non-test caller; it is retained for tests and for a caller that does
// not need the generation guard.
func (m *Manager) PreflightIfAttached(name string) (Preflight, bool)

// Channel is one owned SSH channel + the AppWire client over it.
type Channel struct { /* host, facts, stdio, transport, client, drop/lifecycle channels */ }

func (c *Channel) Client() *appwire.Client      // initialized AppWire client
// Handshake returns the InitializeResponse captured at attach: ProtocolVersion,
// ServerInfo (hub name/version), SourceID ("local"), and Features. appwire.Client
// keeps its own copy of Features privately and exposes no accessor, so component
// 05's capability probe reads the handshake facts from here, not from the client.
func (c *Channel) Handshake() appwire.InitializeResponse
func (c *Channel) Transport() appwire.Transport // StreamTransport over ssh stdio
func (c *Channel) Preflight() Preflight         // GOOS/GOARCH, HOME, resolved config/state roots
func (c *Channel) Host() hostreg.Host           // the registry entry it was built from
func (c *Channel) Close() error                 // kill ssh, close pipes

// Options carries the seams and the connection parameters.
type Options struct {
    Runner              Runner        // nil = production execRunner
    Stderr              io.Writer     // ssh diagnostics; nil = os.Stderr
    Logger              func(format string, args ...any)
    ConnectTimeout      time.Duration
    ServerAliveInterval time.Duration
    ServerAliveCountMax int
    ClientName          string        // AppWire initialize ClientInfo; default "evener-hub"
    ClientVersion       string        // default buildinfo.Version()
    BackoffBase         time.Duration // default 500ms
    BackoffMax          time.Duration // default 30s
    OnEvent             func(Event)   // lifecycle sink, called under the per-host lock: must not block; may read Attached/ClientIfAttached (manager mutex) but never call Ensure (per-host lock, non-reentrant). One field, so the hub installs exactly one fan-out callback here that invokes every registered consumer (component 05's broker rebind, component 06's navigation poke); a consumer is added to that fan-out, never by replacing the field
    BuildBinary         func(ctx context.Context, goos, goarch, out string) error // nil = localBuild
    HubAddr             string        // fallback host hub loopback address when the entry sets no addr; default 127.0.0.1:9180
}

// Event is one lifecycle notification. Attached/Detached mark connection-state
// transitions for Online() and navigation invalidation; they do not add or
// remove sources.
type Event struct {
    Host  string
    Kind  EventKind // EventState | EventAttached | EventDetached | EventFailed
    State State     // disconnected | preflighting | deploying | restarting | attaching | attached | reconnecting
    Err   error
}

// Runner is the process seam. Production is execRunner; tests inject a fake.
type Runner interface {
    // Start spawns a long-lived child; stderr is the caller's diagnostic sink.
    Start(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error)
    // Run executes a one-shot command. Success returns stdout alone (ssh's own
    // stderr chatter must not corrupt a parse); failure returns both streams
    // and a *RunError keeping them apart.
    Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error)
}

// RunError reports a failed one-shot command with ssh's diagnostics (stderr) and
// the remote program's output (stdout) kept separate.
type RunError struct {
    Stdout []byte
    Stderr []byte
    Err    error
}

type Stdio interface {
    Stdin() io.WriteCloser
    Stdout() io.ReadCloser
    Kill() error
    Wait() error
}
```

`Start` is used for the `ssh <dest> <bridge>` channel; `Run` for preflight,
deploy, and restart commands. Both callers pass `argv[0] == "ssh"`; the seam is
the single place that turns `(dest, remote argv)` into local `ssh` argv.

### SSH channel argv

```
ssh -T -o BatchMode=yes -o ConnectTimeout=<n> -o ServerAliveInterval=<n> \
    -o ServerAliveCountMax=<n> -- <dest> <evener_path> hub attach --stdio \
    [--config <config_path>] [--addr <addr>]
```

- **`-T` refuses a PTY.** Every ssh invocation (channel, preflight, deploy,
  restart) passes `-T` (`sshBaseArgv` in `sshconn/runner.go`), so a user's
  `ssh_config` `RequestTTY`/`ForceCommand`-style setting cannot allocate a
  pseudo-terminal for the bridge. A PTY rewrites newlines and folds remote
  diagnostics into the framed stream; `-T` keeps stdout the raw AppWire byte
  stream and stderr the diagnostic channel (component 02's stdout discipline).
  **Implementation status:** shipped — `sshBaseArgv` passes `-T` on every ssh
  invocation (`sshconn/runner.go`), so a user's `ssh_config` cannot allocate a
  PTY for the bridge.
- **`--` ends ssh's own option parsing.** The destination is emitted as
  `-- <dest>` (shipped: `sshDest` in `sshconn/runner.go`). A registry `ssh`
  value that begins with `-` must be read as a hostname and never as an ssh
  option: `-oProxyCommand=...` as a destination would otherwise run a command on
  the controller. `ssh(1)` stops option parsing at `--`. Every ssh invocation in
  this component (channel, preflight, deploy, restart) uses the same prefix.
- `<dest>` is the component-03 `ssh` field (`user@host` or host); if component 03's
  `user` is set it is composed here (`user@host`).
- `<evener_path>` is component 03's `evener_path`. It is resolved **once per
  host** to a single canonical absolute **run target** (`run_path`), and that
  one path is used for every purpose — the remote invocation argv
  (`evenerCommand`), the deploy/install target, restart identification, the
  health/identity check's binary, and attach. The literal word `evener` is
  never the run target, and `PATH` is consulted exactly once, at resolution:
  - `evener_path` set → `run_path` is that path, normalized to a canonical
    **absolute** path. A configured **relative** path is rejected
    (`ErrDeploy`), never resolved against the controller's CWD; a literal
    leading `~/` is expanded against the host's captured home directory
    (`Preflight.Home`) to `<home>/…`. This is required because the configured
    path reaches the remote verbatim — as an SSH argv word and as an installer
    environment value — and a bare `~` is not reliably shell-expanded there
    (the quoting rule below leaves `~` bare only for *unquoted* argv words,
    which does not make an env value expand).
  - `evener_path` empty → `run_path` is the **absolute path the host's own
    `command -v evener` returns** when it resolves to an existing file (the
    `PATH`-selected executable); when `command -v evener` misses, `run_path` is
    `<home>/.local/bin/evener` — the installer's default resolved against the
    host's captured home directory (`Preflight.Home`), **never** the literal
    string `~/.local/bin/evener`. That default is creatable, not a dead end: the
    atomic push creates it when it is missing (§"Push target resolution"), and
    the installer fallback installs it when the fallback is the deploy path
    (§"The installer install path must equal the run target").
  `<home>` is the host's captured home directory (`Preflight.Home`). Every one
  of these paths is **absolute and normalized before use** — nothing relative
  and nothing containing an unexpanded `~` is passed to installation, launch,
  the health/identity check, or restart.
  The *atomic push* writes to the symlink-resolved form of `run_path`
  (§"Push target resolution"), so a `make install` symlink layout is preserved;
  `run_path` itself stays the invocation path. Deploy, restart, health, and
  attach must all name this same resolved binary — a `command -v evener` result
  and the file an installer wrote must not be allowed to disagree.
- **Quoting rule (shipped).** ssh joins its trailing arguments with spaces and
  hands the result to the remote **login shell**, so every remote word must be
  quoted for that shell or a space splits it and a metacharacter executes.
  `shellQuote` (`sshconn/quote.go`) is the one helper:
  - a word consisting only of alphanumerics and ``_ - . / : , @ % + = ~`` is
    passed **bare**;
  - everything else is wrapped in single quotes, with an embedded `'` closed
    and escaped (``'\''``); the empty string becomes `''`.
  `~` is deliberately left bare so the spelling `~/bin/evener` still expands at
  the start of a word; quoting it away would break an unquoted argv's historic
  behaviour for no safety gain.
- **The quoting rule applies to every remote argument, not just `evener_path`:**
  the `hub attach --stdio` words and the `--config`/`--addr` values
  (`evenerCommandArgv` in `runner.go`), the `lsof`/`ps`/`kill` pids and ports,
  the recovered log path, supervisor labels, and the recovered `ps -o command=`
  argv (each word quoted individually by `relaunchCommand`, never handed to
  `sh -c`). `evener_path`, `config_path`, `addr`, and `HOME`-derived
  paths are all registry- or host-derived and must be quoted wherever they reach
  the remote shell; the recovered argv is exactly the case that proves bare
  interpolation is unsafe.
- **The deliberate exception is the raw probe snippet.** `uname -s`/`uname -m`,
  `sw_vers`, `printenv`/parameter-expansion, and the `systemctl`/`launchctl`
  listing for supervisor detection are passed **unquoted and verbatim**
  (`rawCommandArgv` in `runner.go`): they are expressions the remote shell is
  asked to evaluate, not data. Stating the distinction matters — a future editor
  who "fixes" the probe by quoting the snippet breaks it, and one who "fixes"
  argv construction by not quoting injects into the remote shell.
  **The supervisor *restart* command is not in this exception.** Its darwin
  domain argument is `gui/<uid>/<label>` with the numeric uid resolved in
  preflight and the label host-derived data (§"Stop/restart mechanics",
  supervised step): `$(id -u)` is a shell substitution that the quoting rule
  would (correctly) single-quote into a literal, so it is interpolated on the
  controller, never handed to the remote shell.
- **Address, config path, token (corrected contract).** The shipped argv carries
  the connection parameters: `hub attach --stdio [--config <config_path>]
  [--addr <addr>]`, with `--config` **before** `--addr` and every value
  shell-quoted (§"Quoting rule" above). The capability token is never on the
  argv; the bridge reads it from the state root the resolved config names
  (component 02, §Scope).
- **`config_path` and `addr` are coupled, not independently optional.** An entry
  that sets either must set both; `validateHostConfigs` (component 03) rejects a
  half-specified pair with a named sentinel. The two describe one thing — the
  host hub's own configuration — and the two halves consume them from opposite
  ends: the bridge resolves the token's state root and the listen address from
  `config_path`, while this component's restart/health path needs the matching
  `addr`. Allowing them independently lets a half-specified entry attach to the
  custom layout through the bridge while the manager probes and restarts the
  *default* address — a silent split-brain, not a clean failure — which is
  exactly the finding this rule closes. An entry that sets neither uses the
  documented default address on the manager's side, but the bridge still reads
  the host's own `hub.toml` (component 02: `--addr`, else its `hub.toml addr`,
  else `127.0.0.1:9180`), so a host whose config names a non-default address
  **must** set both fields. Absent `addr`, the manager uses `127.0.0.1:9180`
  and refuses a restart it cannot match to the configured address (§5) rather
  than restarting whatever holds the default port; it cannot read the host's
  file to detect the mismatch before probing, which is why the operator sets
  `addr` for a custom-address host. **Implementation status:** shipped. The
  `hostreg` entry stores `ConfigPath` and `Addr` as independent optional fields
  and `channelArgv` passes whichever is present; the restart, port, and health
  probes resolve the address through `Manager.hostAddr` (`sshconn/version.go`,
  `hostAddrFor`; per-host `Addr`, else `Options.HubAddr`, else
  `127.0.0.1:9180`), and `checkHostAddr` (`sshconn/version.go`) refuses a
  non-loopback or malformed address — and a `config_path` with no address —
  before any ssh command runs. The paired `config_path`/`addr` validation
  remains a requirement for the implementing PR.
- `BatchMode=yes` and a connect timeout make the channel strictly
  non-interactive, so a credential prompt fails fast instead of hanging; mirrors
  the spike invocation `spike/client/main.go`
  (`ssh -o BatchMode=yes -o ConnectTimeout=10 <host> <remote>`).
- **Host-key verification is the user's `known_hosts`, and it fails closed.**
  The manager never passes `-o StrictHostKeyChecking=no` (or `accept-new`) and
  never auto-accepts a host key. Verification uses the invoking user's own
  `known_hosts` (plus any `UserKnownHostsFile` the user's `ssh_config` sets); an
  unknown or changed host key makes ssh exit nonzero under `BatchMode=yes` with
  no interactive prompt. Like the auth refusal above, that failure cannot be
  **attributed** to ssh from a completed run — ssh forwards the remote command's
  stderr and exit status — so it stays in the retryable class and is surfaced
  with a named operator hint to add the host key out of band (e.g.
  `ssh-keyscan` or a first interactive `ssh`); it is never a silent downgrade
  and never `StrictHostKeyChecking=no`. (A failed `Start`, which never ran a
  remote command, is the one attributable case.) The capability token crosses
  only the encrypted SSH transport precisely because the host identity is
  verified; a first attach to an unknown host therefore fails with that
  actionable error rather than hanging, and the remedy is documented instead of
  inviting `StrictHostKeyChecking=no`.
- **Port, identity file, and jump hosts go through the user's `ssh_config`.**
  The argv pins the destination as `-- <dest>` (`dest` is a bare host or
  `user@host`, component 03), which deliberately makes `-p`/`-i`/`ProxyJump`
  inexpressible as direct arguments: a value starting with `-` after `--` would
  be read as the destination, and injecting such options before `--` would
  reopen the option-injection hole `--` closes. The supported way to reach a
  non-default port, key, or jump host is therefore the user's `ssh_config`
  (matching `<dest>` or a `Host` alias the operator puts in the `ssh` field),
  which ssh applies itself. This is documented as the supported path, not left
  as an escape hatch, and no `[[hosts]]` field (nor a `dest` spelling) may
  reintroduce raw ssh options.
- stdin/stdout are the framed AppWire stream; stderr is diagnostics
  (spike `spike/client/main.go`, `cmd.Stderr = os.Stderr`).

### Preflight + version contract

The host answers the same `launch-check` contract the local hub already gates on
(`cmd/evener-hub/spawn.go`):

```
ssh <dest> <evener_path> launch-check --protocol <appwire.ProtocolVersion> --json
```

`launchcheck.RunLaunchCheck` emits
`{"protocol","version","launch_flags",...}`
(`cmd/evener/internal/launchcheck/launchcheck.go`, dispatch
`cmd/evener/main.go`). This component parses `protocol` and `version` and
reuses the local gate's rules:

- `protocol` must equal `appwire.ProtocolVersion` (`appwire/types.go`), and the
  gate refuses a mismatch exactly as `validateEvenerLaunchContract` does
  (`spawn.go`). Unlike the local path, the controller is the version authority:
  a refusal here means the host's on-disk binary is not the controller's build,
  so it enters the same deploy + restart flow as a version difference (§5) and
  is re-preflighted afterwards. It is not terminal on its own.
- `launch_flags` must contain `api-log` (`supportedLaunchFlags`,
  `launchcheck.go`; gate `spawn.go`), because the host hub will spawn
  host daemons with the same `--api-log` floor the controller pins
  (`cmd/evener/serve.go`; docs/evener-hub.md:127-141).
- `version` is `buildinfo.Version()` (`launchcheck.go`), i.e. the git SHA of
  the `evener` binary on the host (or `"dev"`). The controller's side of the
  comparison is `buildinfo.Version()` in-process (`buildinfo/buildinfo.go`).
- The preflight also records the host's **numeric effective uid** (`id -u`),
  alongside the effective user name check 3 needs (`entry.User`, else the SSH
  `user@host` form, else `id -un`/`whoami`). The uid is what the supervised
  darwin restart interpolates into `gui/<uid>/<label>`
  (§"Stop/restart mechanics"), so no `$(id -u)` substitution is ever passed to
  the remote shell.

### Channel lifecycle states

`disconnected → preflighting → (deploying → restarting) → attaching → attached →
reconnecting → …`. `Close` is terminal. A dropped link does **not** stop the
remote hub or its daemons (design §2 lifecycle; docs/evener-hub.md:499-502); the
manager transitions to `reconnecting` and the supervisor re-runs the attach
ladder on the same `dest` in **reconnect mode** (`ensureOnce(ctx, host, false)`,
`sshconn/manager.go`), which re-attaches as a client.

**Both attach modes run the same ladder; only the bootstrap start differs.**
`Ensure` calls `ensureOnce` with `explicit=true`; the supervisor's
`reconnectOnce` calls it with `explicit=false`, and everything else is one
path: every attempt re-runs the preflight, re-probes the running hub's
`/api/health` version, and re-applies the deploy/restart decision, so facts are
re-read per attempt and never cached from the attach that opened the channel —
the replacement `Channel`'s `Preflight()`/`Handshake()` describe the reconnect
attempt, not the original attach. The modes differ in exactly one place: the
first-attach bootstrap below (§5). `explicit=true` may start a stopped host's
hub (component 06's first host action); `explicit=false` never starts a hub —
with nothing running and no pending restart the attempt attaches nothing, so
the loop retries under the backoff until the hub returns or a terminal error
ends it. Callers see the difference only through the event stream: an explicit
caller gets the channel or the error back from `Ensure`; a reconnect's callers
observe `EventDetached` and then `EventAttached` once the replacement is
installed (connection state only — the event carries no client), and resolve
the current client per call (§"Client handoff").

`Close` is terminal in the strong sense: once it runs, a later or in-flight
`Ensure` returns **`ErrManagerClosed`** rather than attaching a channel nothing
would supervise (`Manager.Close` in `sshconn/manager.go`; the base context stays
canceled and `publishChannel` refuses after close). Every attach attempt is
**bounded**: a reconnect attempt and a deadline-less initial `Ensure` run under
`attemptLimit()` (`4 × ConnectTimeout + initializeTimeout`, `manager.go`), so one
remote command that never returns cannot hold a host's lock for good and stall
every later reconnect and `Ensure` for that host. `Ensure` is serialized per host
by a per-host mutex, and the mutex is non-reentrant — a caller inside `OnEvent`
must not call it (see the locked-callback bullet below).

The manager is **lazy and event-publishing, not a registry owner**:

- `Ensure` is called on first use (the controller's per-host client accessor
  calls it), so a configured host with no traffic has no SSH channel and its
  source reports `Online() == false` (component 05's `SetHostOnline(Manager.Attached)`).
- **Lifecycle events are emitted under the per-host lock, in order.** `OnEvent`
  is called **synchronously with that host's lock held**, which is what orders a
  `Detached` before any `Attached` that follows it for the same host (and keeps a
  concurrent `Ensure`/supervisor from interleaving transitions). The callback is
  therefore **non-reentrant**: it must record state, or hand the work to another
  goroutine and call `Ensure` from there; calling `Ensure` from inside the
  callback takes the same lock and deadlocks. The read-only lookups `Attached`
  and `ClientIfAttached` are the deliberate exception: they take the
  manager-wide mutex, not this host's gate, so the callback may call them
  directly (§"Client handoff"). Component 06's navigation poke runs inside this
  callback and must stay non-blocking for the same reason.
- Lifecycle events (`EventState`, `EventAttached`, `EventDetached`,
  `EventFailed`, delivered through `Options.OnEvent`) report *connection state*
  only. They drive `Online()`, the component-05 notification-broker rebind
  (`EventAttached`/`EventDetached`), and navigation invalidation. `Options.OnEvent`
  is **one field**: `cmd/evener-hub/main.go` installs a **single fan-out
  callback** on it that invokes every registered consumer — component 05's rebind
  and component 06's navigation poke — so wiring one consumer can never silently
  drop the other. **No consumer adds or
  removes a source in response to them** — component 05 registers one source per
  configured host at startup and never changes the set (components 03/05/06).

## Implementation approach (files/packages, cited seams)

### 1. Process ownership — `runner.go`

- `execRunner.Start` / `Run` build `argv` and call `exec.Command` (the same
  constructor `spawnDaemon` uses, `cmd/evener-hub/spawn.go`), set
  `cmd.Stderr`, and wire `StdinPipe`/`StdoutPipe`. It deliberately does **not**
  use `CommandContext`: the channel's lifetime is the attach, not one request's
  `ctx`, the same reasoning `spawnDaemon` records at `spawn.go`
  ("NOT CommandContext: the spawned daemon must outlive this call's ctx"). `ctx`
  bounds `Start`, not the child; `Channel.Close` (or the reaper) kills the child,
  as the spike's `procStream.Close` does (`spike/client/main.go`).
- The SSH child is a **client** of the remote hub and binds no port; it must not
  take `hostlock` (`cmd/evener-hub/internal/hostlock/hostlock.go`).
- Local detach attributes (`SysProcAttr{Setsid:true}`,
  `cmd/evener-hub/spawn_detach_unix.go`; nil elsewhere,
  `spawn_detach_other.go`) apply to *locally spawned* children. The SSH child
  is not detached — it dies with the controller, which is correct: the remote hub
  is the detached process, not the channel. **Remote** detachment (for the hub
  start command) is a host-side idiom, not `SysProcAttr`; see §3.

### 2. Channel + reconnect — `manager.go`

- `Ensure`:
  1. if attached, return the live channel;
  2. `preflight` (§3, below);
  3. if preflight reports a **verified missing executable**, enter the
     deploy/install path (§4) — a fresh host has no binary, and the installer
     fallback is what creates the default run target — then **re-run preflight**
     on the installed host before version-matching; an arbitrary SSH failure is
     never an install trigger (§3);
  4. version-match (§5);
  5. `Start` the ssh bridge (`ssh ... hub attach --stdio`), wrap the child's
     stdin/stdout in `appwire.NewStreamTransport` (`appwire/stream_transport.go`),
     and build an `appwire.Client` over it.
  6. `Initialize` with `ProtocolVersion: appwire.ProtocolVersion` and a
     controller `ClientInfo`. `appwire.ProtocolVersionMismatchError`
     (`appwire/client.go`) here means the *running* hub speaks another protocol
     while the on-disk binary preflighted clean: restart the hub once and
     re-attach (§5). Only a mismatch that survives that restart is terminal
     `ErrProtocolIncompatible`. The `InitializeResponse` this returns is kept on
     the `Channel` and exposed through `Channel.Handshake()` (above), so
     component 05's capability probe reads protocol, hub version, `SourceID`, and
     features from the channel instead of re-running `initialize` (which is
     `ScopeConnection` and must be the first request on a connection).
- **Context ownership is split, and that split is normative.** `Ensure(ctx, …)`
  uses the caller's `ctx` only for *this attempt* — preflight, the `Initialize`
  handshake, and any deploy/restart — bounded by `attemptLimit()` and, for the
  initial attach, tied to the manager's lifetime (`context.AfterFunc(m.baseCtx,
  cancel)`), so a canceled or timed-out request aborts the attempt rather than
  being ignored. The channel's *lifetime* is manager-owned: `attach` starts the
  client receive loop on the manager's own context (`client.Start(m.baseCtx)`,
  `manager.go`), which outlives every request and is canceled only by
  `Manager.Close` (or by the transport closing when the link dies). Passing the
  caller's request-scoped context to `Client.Start` is the defect this contract
  forbids: it would kill an otherwise reusable connection the moment the request
  that opened it returns.
- Keepalive is **ssh's own, and silence is not failure**. `StreamTransport`
  deliberately does not implement `Pinger` (`appwire/stream_transport.go`), so
  the client keepalive loop skips it; the `ServerAliveInterval`/
  `ServerAliveCountMax` on the argv are the only keepalive and they prove the
  *ssh server* is alive. Liveness is therefore observed through (a) the ssh
  child exiting — the bridge exits when its WebSocket to the remote hub closes,
  and the child's `Wait` runs `Channel.markLost` (`manager.go`), (b) `Recv`
  returning `io.EOF`/error, which `linkMonitor` turns into `markLost`, or (c)
  the ssh-level keepalive declaring the peer dead. **A silent idle hub is not a
  dead link**: a healthy remote hub may send no frame for long stretches, so the
  manager must never treat receive silence on its own as link-down — there is
  **no receive-inactivity deadline**. An application-level liveness probe is
  deliberately not part of this component; if one is ever added it must be an
  active request/response (making `StreamTransport` implement `appwire.Pinger`,
  so a missed pong is decisive), never a passive watchdog that disconnects an
  idle channel.
- Reconnect: on link-down, transition to `reconnecting`, close the old stream,
  and retry `Ensure` with bounded exponential backoff and jitter. Re-attach is a
  fresh bridge/client against the still-running hub; it never starts a second hub
  (`hostlock`; spike finding both m4 lock files held,
  `2026-09-14-multi-host-spikes-findings.md:50-52`).
- The manager exposes attach/detach events and `Attached` as **connection-state
  signals** for component 05's `Online()` and for navigation invalidation. They
  never add or remove a source: component 05 registers every configured host's
  source once at startup (component 03, §"Source registration hook").

### Client handoff (component 04 → 05)

A reconnect creates a new ssh child, a new `StreamTransport`, and a new
`appwire.Client`; the old client is dead. Component 05 must never be left bound
to it, so the handoff is defined as:

1. **The manager owns the binding.** `Ensure(ctx, host)` returns the current
   `*Channel` and is serialized per host (a per-host mutex), and the supervisor
   performs the whole reconnect under that same lock. A caller therefore never
   observes a half-swapped channel, and `Ensure` after a drop returns the *new*
   channel (or fails).
2. **The consumer resolves the client per call.** Component 05 holds a
   `RemoteHubClientFunc`-shaped accessor (`func(ctx, host) (*appwire.Client,
   error)`), not a `*appwire.Client`: the wiring in `cmd/evener-hub/main.go`
   calls `sshManager.Ensure(ctx, host)` and returns `ch.Client()`. Every call
   re-resolves, so a handoff is picked up by the next call with no notification
   handshake. Caching a client in the source is the defect this contract
   forbids.
3. **Derived state is keyed to the client.** The capability probe caches its
   result against the client it ran on (component 05), so a new client
   automatically re-probes; a subscription is bound to the client it was
   created on and its notification stream ends with that client, which is what
   tells the controller relay to re-subscribe.
4. **In-flight work fails, it is not replayed.** Calls issued on the dead client
   return the transport error (mapped to `SessionUnavailable`); the manager does
   not buffer or reissue them. Restoring state — probe refresh, subscriptions —
   is the consumer's job on the next call (component 05, §"Reconnect handoff").

The events are advisory: `EventDetached` is emitted before the reconnect starts
and `EventAttached` once the replacement channel is installed, but a consumer
that ignores them still converges, because resolution happens per call.

**A consumer that must not dial uses `ClientIfAttached`, not `Ensure`.**
`Ensure` attaches; the notification broker's rebind (component 05, §"One
notification consumer per client") and component 06's background snapshot must
instead read `ClientIfAttached(name)`, which returns the freshly installed
client only while a channel is live and `(nil, false)` otherwise. On the
`EventAttached` that installs a replacement channel, the source rebinds its
registered consumers to the new client by reading `ClientIfAttached` — never by
calling `Ensure` (which would attach every configured host and violates the lazy
manager, component 04, §"Channel lifecycle states").

**The rebind is lock-safe from inside `OnEvent` — it cannot self-deadlock.**
`OnEvent` runs with the host's **per-host gate** held (§"Channel lifecycle
states"), so anything the callback calls must not take that same gate.
`ClientIfAttached` deliberately does not: it reads the installed channel under
the **manager-wide mutex** (`Manager.mu`, the `chans` map), a different lock from
the per-host gate (`Manager.locks[name]`). `Attached` and `ClientIfAttached` are
therefore both safe to call from the `EventAttached`/`EventDetached` callback,
and both derive from the same installed-and-not-closed predicate, so an
attachment signal and a client lookup can never disagree. Binding the
replacement `Client` **on the event** rather than deferring it is what makes the
rebind gap-free: deferring to the next call would drop the notifications the
fresh client emits in between (component 05, §"One notification consumer per
client"). The event carries connection state only, not the replacement client,
so `ClientIfAttached` is the handoff; carrying the replacement client in the
event is the equivalent alternative (the callback would then need no lookup at
all). Either form satisfies the contract. What the callback must still never do
is call `Ensure`: that takes the per-host gate and would deadlock.

### 3. Preflight — `preflight.go`

Run over non-interactive SSH (no login shell, no TTY):

- **OS/arch.** `uname -s` and `uname -m`, mapped to `GOOS`/`GOARCH` exactly as
  `install.sh:21-45` maps them (`Linux→linux`, `Darwin→darwin`;
  `x86_64|amd64→amd64`, `arm64|aarch64→arm64`). `sw_vers -productVersion` is an
  optional darwin detail, not used for target selection.
- **HOME / XDG.** Read `$HOME` and test `$XDG_STATE_HOME`/`$XDG_CONFIG_HOME`
  with `printenv`/parameter expansion. The spike found non-interactive SSH
  exports no `XDG_*`, so evener resolves to `~/.config/evener` and
  `~/.local/state/evener` (spike findings:47-49; docs/evener-hub.md:65-68;
  `envvars/envvars.go`). Preflight records the resolved state root so the
  controller knows where the host's `auth-token` and `hub.lock` live; it must not
  invent XDG values the host environment does not have.
- **Existing version/protocol.** The `launch-check` call in §"Contract" above.
- **Missing executable (explicit preflight result).** A fresh host has no
  `evener` at `evener_path`/`run_path`, so the preflight `launch-check` call
  fails for a reason that is *not* a transport failure, and the preflight must
  report it as a defined result instead of a generic `ErrSSHStart`. The
  result is recognized only when it is **verified**, and it must be recognized
  from a **dedicated executable probe with a stable, machine-readable sentinel —
  never from parsing the remote shell's not-found status and human-readable
  text**. The preflight runs a probe for the resolved `run_path` whose answer is
  the exit code alone (`test -x <run_path>`: `0` present, `1` absent;
  `command -v` is the equivalent), so recognition does not depend on the shell
  generation, the host locale, or the wording of "no such file or directory" /
  "not found". The probe runs over the same non-interactive SSH channel and its
  own status stays distinct from ssh's: ssh exits `255` for its own failures, so
  a `255` or a spawn failure is a transport failure, while only the probe's `1`
  is the verified absent result. Because ssh writes its own diagnostics and the
  remote command's stderr to a single stream (the stream-merge rule below), the
  recognition must not consult that merged stream at all; a host without the
  ssh-diagnostic separation must default to the retryable class rather than
  guess from the text. Preflight records the verified result (a
  `Preflight.ExecutableMissing` fact, surfaced as the named
  `ErrExecutableMissing` sentinel where an error is needed) and `Ensure`
  routes it into the deploy/install path (§4), which creates the missing run
  target — the atomic push creates the resolved `run_path`
  (`<home>/.local/bin/evener` when nothing resolves, §"Push target resolution"),
  and the installer fallback installs that default when it is the deploy path;
  preflight then **re-runs** against the installed binary before version-matching or
  attaching. **Arbitrary SSH failures must not trigger installation:** a
  failure to spawn ssh, a connection/auth refusal, an ssh-level `255`, an
  unparseable or empty `launch-check` answer, or a launch-contract/protocol
  refusal all stay in their own classes (`ErrSSHStart`, `ErrSSHAuth`,
  `ErrPreflightDecode`, `ErrLaunchContract`) and never run the installer,
  because an unreachable host would otherwise be treated as an empty one and the
  deploy ladder would push a binary at a host that never answered.
  **Implementation status:** shipped for the classification and the route: a
  `launch-check` whose shell reports a missing command is classified
  `errExecutableMissing` (`sshconn/preflight.go`), and with a deploy path
  configured `preflight` defers it into deploy/install, while an unreachable
  host, an auth refusal, an unparseable answer, or a protocol/launch-contract
  refusal never runs the installer (`sshconn/preflight.go`'s deploy deferral). The
  dedicated `test -x` probe and its ssh-diagnostic separation remain round 19.
- **Roots (probed).** Preflight resolves the host's config root and state root
  from the probed environment with the same chain the host binary uses
  (`resolveRoots`, `cmdutil.StateRootFromLookup`, `envvars/userdirs.ConfigRoot`)
  and records them on `Preflight` (`Preflight.Home`, `.ConfigRoot`,
  `.StateRoot`). Those are the roots a *default-layout* host hub reads, and they
  tell the controller where the host's `auth-token` and `hub.lock` live.
  Non-interactive ssh exports no `XDG_*`, so the defaults are
  `~/.config/evener` and `~/.local/state/evener` (`envProbeScript`).
- **Address / config path (not probed — corrected contract).** The bridge reads
  the host hub's `addr` and the token's state root from the config path it is
  given (component 02), so the two sides must agree on both. The shipped argv
  carries them (`hub attach --stdio [--config <config_path>] [--addr <addr>]`,
  `--config` first). Component 03's `config_path`/`addr` are **coupled** (set
  both or neither; component 03, §"`config_path` / `addr`"); this component
  passes them to the bridge and uses the per-host `addr` for the restart/health
  probe. `Manager.hostAddr` (`version.go`) returns the per-host `Addr` when set,
  else the manager-wide `Options.HubAddr`, else `127.0.0.1:9180`, and the
  restart, port probe, and `/api/health` probe all read it, so the manager no
  longer probes or restarts a custom-addressed hub at the default port **when
  the entry sets `addr`**. Absent the pair, the bridge still reads the host's
  own `hub.toml` (component 02) while the manager falls back to
  `127.0.0.1:9180`, so a host whose config names a non-default address must set
  both; a restart the manager cannot match to the configured address is refused
  (§5) rather than guessed.
  **Implementation status:** shipped — `Manager.hostAddr`
  (`sshconn/version.go`'s `hostAddr`) resolves the per-host `addr` for the restart,
  port probe, and health probe, and `checkHostAddr` refuses a non-loopback or
  malformed address — and a `config_path` with no address — before any ssh
  command runs; the paired `config_path`/`addr` validation remains a
  requirement for the implementing PR.
  `hub.lock` lives at `<hub_state_root>/hub.lock` (`cmd/evener-hub/main.go`)
  and names no address or PID — it cannot be used to find the listener.
- **Target support.** Only `linux/amd64` and `darwin/arm64` ship
  (`.goreleaser.yml:24-30`; `install.sh:39-45`). Any other `os-arch` is a
  terminal, named unsupported-host error — do not attempt a deploy.

### 4. Deploy — `deploy.go`

Two paths, chosen per host (open question: which wins when both are viable):

- **Cross-compile + push (primary).** Build the controller's own tree for the
  host target and push it with `scp` (or `ssh <dest> 'cat > <path>'`) plus
  `chmod +x`. The command *must* stamp build identity — a bare `go build`
  produces a binary whose `launch-check` version is `"dev"`, and version
  auto-match would then compare against a build the host is not running. The
  shipped implementation does it as `localBuild` in
  `cmd/evener-hub/internal/sshconn/deploy.go`:

  ```
  CGO_ENABLED=0 GOOS=<o> GOARCH=<a> \
    go build -ldflags "<buildLdflags()>" -o <stage>/evener ./cmd/evener/
  ```

  with `buildLdflags()` rendering one
  `-X primeradiant.com/evener/buildinfo.<Field>=<value>` per field for
  `GitSHA`, `GitDirty`, `BuildTime`, and `Channel` (the import path is the
  `buildinfoPkg` constant), all **read in process** from `buildinfo.GitSHA`,
  `buildinfo.GitDirty`, `buildinfo.BuildTime`, and `buildinfo.Channel` — never
  re-derived by running `git` and `date` as the Makefile does. That is what
  makes the pushed binary's `buildinfo.Version()` (and therefore its
  `launch-check` `version`) exactly the controller's. Build from the module root
  (`moduleRoot`), because the hub may have been started from another directory.
  The build is a seam: `Options.BuildBinary` (nil = `localBuild`) lets tests
  assert the argv without compiling.

  **Dev builds must not auto-match.** `buildinfo.Version()` returns `"dev"`
  whenever `GitSHA` is empty (`buildinfo/buildinfo.go`), so an unstamped
  controller has no identity to deploy: pushing its own tree would produce a
  binary that cannot be distinguished from what is already on the host. Rule:
  when the controller's own `buildinfo.Version()` is `"dev"`, auto-match is
  *disabled*. With a deploy path the forced deploy still runs and both sides end
  up on the same unstamped build; with no deploy path the host keeps its own
  build and is attached, because a build version is not an attach gate (§5).
  Operators who want auto-match on a dev controller must supply the identity
  (e.g. `-X buildinfo.GitSHA=…`, or a configured binary via `Options.BuildBinary`).

  The same command shape as `make build-linux` (`make/building.mk:41-42`) and
  `scripts/ops/build-runtime-pair.sh:24-28`, minus their git/date stamping.
  The spike proved this end to end for `darwin/arm64`: a cross-compiled Go binary
  runs on Apple Silicon without a manual signing step because the Go linker adds
  an ad-hoc signature, and `scp` + `chmod +x` sufficed
  (`2026-09-14-multi-host-spikes-findings.md:44-46`). Build artifacts go to a
  private staging dir and are removed after the push.
- **Installer (fallback).** Run `install.sh` on the host
  (`EVENER_INSTALL_VERSION` to pin, default prefix `~/.local`,
  `install.sh:7-19,140-152`), or `make install-home` when the host has the repo.
  The installer downloads a release archive and verifies it against
  `checksums.txt` (`install.sh:88-130`) and refuses an unverified archive; it
  requires host network access, which the push path does not.
  **The fallback must be pinned to the controller's exact expected build — and
  a Git SHA is not a release tag.** `install.sh` treats `EVENER_INSTALL_VERSION`
  as a GitHub **release tag** (`install.sh:41-44` builds
  `$repo/releases/download/$version`), while `buildinfo.Version()` is the *short
  Git SHA* (plus `-dirty`) — so passing it verbatim (an earlier rule here) points
  every normal build at a release that does not exist. There is no
  commit-pinned installer mode; the only artifact references install.sh knows
  are a real release tag, its default `latest`, and the mutable `snapshot` tag
  (`.github/workflows/binaries.yml:77-112` force-moves `snapshot` to the newest
  green `main`). The artifact-reference-to-binary-identity mapping is therefore
  driven by the controller's own build channel, `buildinfo.BuildChannel()`
  (`buildinfo/buildinfo.go`):

  - **release** (`Channel == "release"`): pass the stamped release tag. The
    controller build stamps it (`buildinfo.ReleaseTag`, set by `.goreleaser.yml`
    as `-X primeradiant.com/evener/buildinfo.ReleaseTag={{ .Tag }}`), and the
    installer is invoked with `EVENER_INSTALL_VERSION=<buildinfo.ReleaseTag>`. A
    release build carrying **no** stamped tag is refused (`ErrDeploy`) rather
    than passed `buildinfo.Version()`: a Git SHA is never a release tag.
  - **snapshot** (`Channel == "snapshot"`): pass the **mutable** `snapshot` tag,
    accepted as a **best-effort pin**. It is not a provable pin:
    `.github/workflows/binaries.yml` force-moves the tag *and* re-uploads
    `checksums.txt` with `--clobber` on every green `main` build, so neither the
    tag nor the checksum it publishes names the controller's commit. The install
    is verified *afterwards* by the same on-host identity check
    (`deployInstaller`'s `probeLaunchCheck`), and once the tag has moved past
    this controller's commit that check refuses **terminally**
    (`ErrVersionMismatch`, "a moved channel tag cannot be resolved by retrying")
    instead of re-fetching the same artifact forever.

    The trade this accepts is real and is stated here because the earlier text
    in this section rejected exactly it. For a snapshot controller the installer
    writes a binary whose commit is **not provable from the artifact
    reference**, and the proof arrives only *after* the write. So the failure
    mode the old text named remains: once the tag moves past the controller's
    commit, `install.sh` has already replaced the host's `evener`, and the
    deploy reports failure while the host holds a snapshot build from an
    unknown — possibly **newer** — commit. What makes it acceptable is the path
    that does carry a provable identity, and that it is now actually reachable:
    the atomic push path (§4, "Cross-compile + push") cross-compiles the
    controller's own tree and stamps the build in-process, it is wired from a
    production hub by `-deploy-binary` / `-build-source`, and the terminal
    refusal names it as the remedy in the terms the embedder supplied
    (`Options.DeployHelp`): a hub names its own `-deploy-binary` /
    `-build-source` flags, while an embedder with no flags to name keeps the
    library's sentence ("use the atomic push path or `Options.BuildBinary`").
    The `snapshot` tag is not an immutability to lean on; it is a movable
    default whose cost the operator is told how to avoid.
  - **dev / dirty** (`Channel == ""`/"dev", or `GitDirty == "true"`): there is
    no publishable identity to pin, so the installer fallback is **refused**
    (`ErrDeploy`, the same rule as §"Dev builds must not auto-match"); the
    operator must use the atomic push path (§"Push target resolution") or an
    explicit `Options.BuildBinary`.

  `latest` is never passed. After the installer runs, the same on-host
  `/api/health` probe and identity check below must confirm the *pinned* build —
  `version` equal to `buildinfo.Version()`, and `backend_git_sha` equal to
  `buildinfo.GitSHA` where the channel's stamping carries it — before
  attachment; a host that cannot be pinned or resolves the wrong build is a
  **failed verification** (`ErrDeploy`), never an attach.

  **The atomic push path cannot replace the installed binary before the
  artifact's identity is pinned to the controller's build; the installer
  fallback has no such property, and for a snapshot controller it trades it
  away deliberately.** The push path already has this property: the binary is
  cross-compiled by the controller from a source revision it verified
  (`verifyBuildRevision` refuses a dirty tree and an ignored-but-compiled `.go`
  file), stamped with the controller's own buildinfo
  (`-X buildinfo.GitSHA`/`BuildTime`/`ReleaseTag`), streamed into a `mktemp`
  temp file whose byte count must equal the staged file's length before
  `chmod +x` and a single `mv` onto the resolved run target
  (`pushBinaryRemote`/`pushBinary`, `deploy.go` — the round-eleven byte-count
  verification) — a short or dropped transfer fails, the trap removes the temp
  file, and `run_path` holds either the whole verified build or exactly the file
  it already had. The installer fallback has no such property of its own:
  `install.sh` copies the archive's binaries into `share_bindir` and re-points
  the `bindir` symlinks (`install.sh:140-152`), which is neither an atomic swap
  nor an identity check — the `checksums.txt` it verifies belongs to whatever
  the tag resolves to at that moment. It is therefore admitted for `release` —
  an immutable tag, plus `install.sh:88-130`'s sha256 verification of the
  archive against that release's `checksums.txt`, which fails closed and
  installs nothing on any mismatch — and for `snapshot` only as the best-effort
  pin §"Installer (fallback)" describes, where the post-install identity check
  is the whole verification and a tag that has moved past the controller's
  commit is refused terminally. It is refused for `dev` and dirty, which have no
  publishable identity to pin. The post-install `/api/health` identity probe
  stays as the last verification either way, but for a release pin it checks an
  already-replaced file and is never the pin; for a snapshot pin it is the only
  verification there is, which is the trade §"Installer (fallback)" states.

- **Push target resolution (shipped).** A push must install to the absolute path
  of the executable the host will *run*, or version auto-match deploys the new
  build next to the old one and then attaches to the old one. `deployTarget`
  (`deploy.go`) therefore:
  1. when `evener_path` is set, requires its directory to exist on the host
     (`test -d`) and uses the path;
  2. when `evener_path` is empty, resolves the target with the host's own
     `command -v evener` (§"SSH channel argv", the `run_path` rule) — never by
     writing a file literally named `evener` into the remote working directory,
     which is neither the `PATH`-selected executable nor a stable location;
  3. resolves an *existing* target with a portable POSIX-sh resolver
     (`resolvePathScript` + `resolveDeployTarget`), so the atomic `mv` replaces
     the real file a `make install` symlink points at rather than clobbering the
     link and breaking the installed layout. The resolver walks symlinks with
     plain `readlink` in a loop, resolving each link's directory with
     `cd -P … && pwd` and joining a relative target against it, and requires the
     final path to exist (`[ -e "$p" ]`) before printing it. It deliberately
     avoids `readlink -f`: that flag is a GNU/coreutils extension which BSD
     `readlink` (macOS) rejects with `illegal option -- f`, so every
     `darwin/arm64` deploy failed at resolution. A path that does not resolve to
     an existing file prints nothing and exits nonzero;
  4. when the resolve fails **because the target does not exist yet**, a missing
     target is a **creatable** target, not a refusal:
     `resolveDeployOrCreateTarget` (`deploy.go`) canonicalizes the
     not-yet-installed path under its existing parent directory
     (`createTargetCommand` refuses a path that already exists — a directory, a
     dangling symlink — and a path whose parent directory does not exist or does
     not resolve), and the same `mktemp` → byte-count verification → `chmod +x`
     → `mv` push creates it atomically. An empty `evener_path` whose
     `command -v evener` misses therefore resolves to the installer's own
     default `run_path` `<home>/.local/bin/evener` (§"The installer install path
     must equal the run target") — with `mkdir -p` creating that default's
     directory when it is absent — and the push creates the binary there.
     **A fresh host with no evener at all is provisioned by the atomic push
     path whenever a build source (or `Options.BuildBinary`) is configured —
     on every channel, `snapshot`/`dev` included, because the push path needs
     no published artifact — and by the installer fallback otherwise, where
     that fallback is admitted (§"Installer (fallback)").** Only a
     target that cannot be created (an unresolvable or missing parent directory,
     or an existing path that is not a replaceable file) is `ErrDeploy` with no
     write.

- **The installer install path must equal the run target.** `install.sh` writes
  the real binaries under `share_bindir` (`EVENER_SHARE_BINDIR`, default
  `$PREFIX/share/evener/bin`) and symlinks `evener`/`evener-dev` into `bindir`
  (`BINDIR`, default `$PREFIX/bin`, with `PREFIX` defaulting to `$HOME/.local`
  — `install.sh:4,7-16,20,146-152`). Deployment and restart, however, run and
  identity-check the configured `evener_path` (§5). A host with a custom
  `evener_path` therefore **installs cleanly into `~/.local/bin` and then keeps
  probing and relaunching the old binary** at the configured path. Rule: the
  installer fallback must install to the target the manager will run.
  - When `evener_path` is set, pass the installer an explicit
    `BINDIR=<dirname(evener_path)>` (with `EVENER_SHARE_BINDIR` under the same
    prefix) so the symlink lands at `evener_path`. The basename must be
    **`evener`**, and the directory must exist and be writable on the host — the
    same `test -d` precondition `deployTarget` already applies to `evener_path`
    (§"Push target resolution"). **`evener-dev` is not a valid remote hub run
    target.** It is the development/test tooling binary (`cmd/evener-dev/bin`:
    `dev`, `module-lint`, `fuzz-*`, `internalcheck`, `tomlcheck`,
    `transcript-v2-upgrade`) — it has no `hub` subcommand and no `launch-check`,
    so a host whose `evener_path` names it installs "successfully" and then
    fails preflight, health, and restart on a binary that can never serve the
    hub. A configured `evener_path` whose basename is `evener-dev` (or any other
    name the installer does not ship as the runtime binary) is therefore
    **refused with `ErrDeploy`** — "evener-dev is the development tooling binary
    and does not provide the hub command; configure the `evener` binary" —
    before any install, push, or write, and `installableEvenerBasename`'s
    acceptance is narrowed to `evener` with it. There is no stamped,
    hub-capable development artifact in this series: a dev build reaches a host
    only through the atomic push path (§"Push target resolution",
    `Options.BuildBinary`) at an `evener`-named target.
    **Implementation status:** shipped — `installableEvenerBasename`
    (`sshconn/version.go`) accepts only `evener`, and `checkRunTarget`
    (`sshconn/deploy.go`) refuses any other run-target basename terminally
    (`ErrRunTargetUnservable`) before any probe, push, or install.
  - When `evener_path` is empty, the installer targets the host's resolved
    `run_path` (`BINDIR=dirname(run_path)`, the same value the push path
    resolves): when `command -v evener` resolved, that is its directory, and
    when it missed, `run_path` is the installer's own default
    `<home>/.local/bin/evener`. The manager records that one path and checks,
    restarts, and launches it, rather than assuming a separate
    `command -v evener` result that may name a different install.

  If the custom `evener_path` cannot be reproduced by the installer (a basename
  it does not ship, a missing or unwritable directory), the installer fallback
  is **refused** with `ErrDeploy` and the atomic push path (§"Push target
  resolution") is required. The post-install identity check reads the binary at
  the same resolved target, so a successful install that left a different file
  running is a **failed verification**, never an attach.

Both paths must leave the host with either the whole expected build or exactly
the file it already had. The push writes a temp name and `mv`s it into place
(`pushBinaryRemote`/`pushBinary` in `deploy.go`), so an interrupted push never
leaves a truncated `evener`. The installer path's own copy is **not** atomic
(`install -m 0755` into `share_bindir`, then `ln -sfn`, `install.sh:140-152`),
so its admission is tied to what the reference can prove: for a release
reference, re-running the same pinned install converges the same
checksum-verified bytes idempotently, so a torn install is recoverable rather
than silently different, while the `snapshot` reference above is admitted only
as the best-effort pin §4 states, where the post-install check refuses a moved
tag terminally and a torn install is a failed verification. Record the
binary's source (`git SHA`) so the version-match can verify the deploy landed.

### 5. Version auto-match + restart — `version.go`

- **On-disk identity (deploy decision).** Compare controller
  `buildinfo.Version()` (`buildinfo.go`) with the host's `launch-check`
  `version`. `launch-check` reports the binary at `evener_path`/`PATH`, which is
  exactly what a deploy replaces: equal → attach directly; different **and a
  deploy path is configured** → the controller is the version authority
  (design §2) and deploys the matching build (§4), restarts, then re-attaches.
  The deploy stamps the controller's own buildinfo (`-X buildinfo.GitSHA=…`,
  `-X buildinfo.BuildTime=…`) into the pushed binary, so a deployed binary's
  `launch-check` version equals the controller's `buildinfo.Version()`.
  A different version with **no** deploy path is not an attach gate: the host
  answered the controller's `launch-check`, so it speaks the same protocol, and
  it keeps its own build and is attached. The difference is reported
  (`ensureOnce`'s skew notice) rather than refused, and no snapshot pin applies
  to a build this controller did not install. Gating on the version label
  instead refused working hosts — an unstamped `"dev"` host against a stamped
  controller, a host built from another checkout — and told the operator to
  configure a deploy path they did not need. `launch-check` itself refuses any
  protocol but its own (`launchcheck.go:72-74`) and
  `appwire.Client.Initialize` enforces the protocol again at the wire, so the
  protocol is the compatibility contract and the build label is not. A `"dev"`
  controller does not auto-match (§4): an unstamped build has no identity to
  deploy, so a mismatching host is not "matched" by pushing another `dev` binary.
- **Running-hub identity (the restart trigger and the verification).** The
  binary on disk is *not* proof of what the running process executes: a running
  hub keeps executing the copy it was started from until it is restarted. The
  authoritative running-build signal is the host's own HTTP health endpoint,
  `GET /api/health`, which reports `version` and `backend_git_sha` from
  `buildinfo` in the live process plus `started_at` (`cmd/evener-hub/web_api.go`;
  `hubapi.HealthResponse` in `hubapi/types.go`, fields `Version`, `StartedAt`,
  `BackendGitSha`). The shipped `ensureOnce` (`manager.go`) probes it **before**
  deciding anything, through `probeHubVersion`:

  - it reads the on-disk `launch-check` `version` (`facts.Version`) and the
    running hub's `/api/health` `version` (`running`, with `runningKnown == false`
    when nothing answered);
  - it enters the deploy/restart branch when the running hub's `version` differs
    from the controller's `buildinfo.Version()` (`expected`) while the on-disk
    build already matches — a stale process to replace — or when the on-disk
    `facts.Version` differs **and a deploy path is configured** (§4) to install
    the controller's build; the deploy replaces the binary and the restart brings
    up what the deploy wrote, so re-cross-compiling on every reconnect for a
    mismatch a deploy already resolved would be a needless build;
  - an on-disk difference with **no** deploy path attaches instead: the protocol
    decides compatibility (§5, "On-disk identity"), so the host keeps the build it
    has and `ensureOnce` reports the difference as it attaches;
  - when nothing answers the probe (`runningKnown == false`) there is no running
    hub to judge, and the deploy-path condition above is the whole test. Half
    of the remaining case — a host that is merely **not started** — is closed by
    the first-attach bootstrap below; a reconnect never silently restarts a
    process it cannot identify (the rule unchanged by this round).

  **First attach to a stopped host must be able to start the hub.** The probe
  above only *restarts* a hub that is already answering; the bridge is a client
  that must not start one (component 02, §Scope) and `ensureOnce` invents no
  start, so a configured host whose hub is not running can never become usable —
  contradicting component 06's first-host-action guarantee (selecting the host
  attaches it and flips `Online`). The bootstrap branch closes that gap:

  - it runs **only** from an explicit attach request (the component-06 first
    host action — a picker selection or a "Connect" affordance), never from the
    background snapshot walk, which stays attached-only (component 06,
    §"Background snapshot");
  - it first re-probes the listener and refuses to start anything when a hub
    already owns the configured address (`runningKnown == true`), so it cannot
    create a duplicate; the start is the same single-owner contract the restart
    checks enforce (§"Stop/restart mechanics" checks 1–4);
  - it starts the host hub the **same way the restart path does** — the
    identified supervisor's start (`launchctl kickstart -k gui/<uid>/<label>` /
    `systemctl [--user] start <unit>`) when **exactly one** unit matches, and
    the ops doc's detached ad hoc launch of the resolved
    `evener_path`/`command -v evener` with the recovered-or-default log
    (`relaunchCommand`) when **none** does; an **ambiguous** match starts
    nothing and fails with `ErrRestart` (a matched unit may start its own
    instance, so an ad hoc duplicate would split supervision —
    §"Stop/restart mechanics") — and `hub.lock` makes a losing second process
    exit rather than bind;
  - **the cold-bootstrap ad hoc launch needs an explicitly constructed argv,
    because there is no recovered `ps` line to tokenize.** The recovered-line
    form is `relaunchCommand`, which tokenizes the recovered
    `ps -o command=` of the *running* process; a stopped host has no such line.
    The first-attach branch must therefore resolve the executable (the host's
    single `run_path`, §"SSH channel argv") and run it as
    `<exe> hub [--config <config_path>] [--addr <addr>]` — passing the entry's
    paired `config_path`/`addr` when set (component 03) and nothing when not —
    with every argument shell-quoted by the same `shellQuote` rule as the bridge
    argv, launched detached under `nohup` with stdin from `/dev/null` and
    stdout/stderr redirected to a **default log path under the host's state
    root** (e.g. `<stateRoot>/hub.log`, appending when it exists; the ops doc's
    recovered-log path is a restart-only concept). **The log directory must
    exist before the shell sets up redirection.** On a
    cold host that has never run evener, `<stateRoot>` (e.g.
    `~/.local/state/evener`) does not exist, and a POSIX shell fails the
    redirection with `No such file or directory` *before* `<exe> hub` is
    invoked — the hub never starts and `waitHealthy` exhausts its retry budget
    with `ErrRestart`. The first-attach branch must therefore `mkdir -p` the log
    directory (parent included) before backgrounding, or, if it cannot be
    created, fall back to discarding the output (`>/dev/null 2>&1`) rather than
    launching a command whose redirection is guaranteed to fail. Launching bare
    `evener hub` with defaults would make the health probe and attach target
    `127.0.0.1:9180` while the entry's `addr` names a custom port, so every cold
    first attach on a non-default host would abort with `ErrRestart`; an empty
    `evener_path` makes the same explicit argv with the host's resolved
    `run_path` as the absolute executable;
  - it then waits for the same `/api/health` `version == expected` probe
    (`waitHealthy`), with the same bound and `ErrRestart` failure, so a start
    that never becomes healthy attaches nothing and the first host action
    surfaces the failure.

  A host whose `run_path` cannot be established — no configured `evener_path`,
  no `command -v evener`, and no deploy path able to create the default
  `<home>/.local/bin/evener` (no build source/`Options.BuildBinary`, and the
  installer fallback refused or failing) — and with no identified supervisor
  unit, cannot be started: the bootstrap is refused with
  `ErrDeploy`/`ErrRestart`, the host stays offline, and the UI must show that
  refusal (component 06, §"Connecting a configured host").
  **Implementation status:** the bootstrap branch has landed (`ensureOnce` and
  `bootstrapHub`, `sshconn/manager.go`), gated on the explicit-attach flag
  exactly as above: `Ensure` passes `explicit=true`, the supervisor's
  `reconnectOnce` passes `false` (§"Channel lifecycle states"). One shipped
  detail differs from the paragraph above: the ad hoc launch uses the discard
  fallback (`relaunchCommand(hubBootstrapArgv(…), "")` in `bootstrapHub` —
  output to `/dev/null`) rather than the state-root log path the paragraph
  names.

  Post-restart verification is the same probe run under `waitHealthy`
  (`version.go`): it polls the host's `/api/health` until the response reports
  the *expected build identity*, or the bound is exhausted (`ErrRestart`). That
  identity is **passed into** `waitHealthy` as an argument — the expected
  `version` always, and, for a controller on the **snapshot build channel**, the
  expected `backend_git_sha` from `buildinfo.GitSHA` — because a version-only
  probe cannot tell two snapshot builds that share a `version` apart. A bare
  200 — or any non-empty body — is not sufficient, and a body that is not a
  `hubapi.HealthResponse` is not usable evidence (`parseHealthVersion`).

  **Version equality is the fresh-process marker; there is no clock
  comparison.** Because the restart is entered only when the running version
  differed from `expected`, a post-restart response reporting `expected` cannot
  come from the process that was stopped: the old one reported a different
  version. (A stale process can pass only when it *already* reported `expected`,
  which is the idempotent-retry case where the restart re-installs a build the
  running hub already serves.) This replaces the earlier design that captured a
  host-side `date +%s` marker and required `started_at` to be later than it: a
  one-second-resolution marker can be equal to or earlier than a stale process's
  `started_at`, so it cannot distinguish processes, and comparing host-stamped
  `started_at` to the controller's clock adds skew for no benefit. No host-side
  timestamp and no skew tolerance is used or needed. **For a snapshot pin the
  probe rejects a missing or mismatched `backend_git_sha`.** The response carries
  `backend_git_sha`, and `waitHealthy` takes the expected Git SHA alongside the
  expected version (`snapshotPinGitSHA` returns `buildinfo.GitSHA` for a
  snapshot channel and "" otherwise, `version.go`). A response whose
  `backend_git_sha` is empty or not equal to that pin is **not yet healthy** for
  a snapshot pin — the probe keeps polling and fails with `ErrRestart` on
  exhaustion, naming the version that answered against the commit that was
  deployed (`version.go`) — which is the stricter *build*-identity check the
  earlier rule anticipated, with the version-equality rule above unchanged. The
  start path passes the same pin (`waitStartedHealthy`, `version.go`): the
  binary it launches is the one on disk, but "on disk" was accepted by the
  version-equality rule, and a snapshot version cannot tell two builds apart, so
  a start on a stopped host would otherwise attach to a commit this controller
  did not install. What a start deliberately lacks is a predecessor identity to
  compare, not the pin: `bootstrapHub` refuses to start while a listener holds
  the address, so there is no process whose survival could satisfy the wait.

  **What the version check proves — and what it does not.** `waitHealthy`
  proves that *a* process of the expected build is answering on the configured
  address; it does not prove that process is the one this restart launched.
  Version equality is a build-freshness marker, not a process-identity check:
  any process of the expected build owning the address satisfies it, including
  one the controller did not start, and — in the idempotent-retry case where
  the running hub already reported `expected` — the pre-existing process
  alone. The consequence is that the manager can attach to a right-build hub it
  did not recover. Proving *process identity* instead needs a per-launch nonce
  carried in the health/attach handshake, or a post-restart re-validation of
  the listener's pid, argv, and effective user plus a process-start identity;
  the shipped `waitHealthy`/`parseHealthVersion` (`sshconn/version.go`) do
  neither. This is a stated limitation, not a guarantee.

  **The probe must run on the host.** A request to `127.0.0.1:<port>/api/health`
  from the controller's Go process reaches the *controller's* hub, not the
  host's, and the controller has no generic remote-HTTP tool. So the probe is
  one more `Runner.Run` over the same ssh seam as preflight and restart, exactly
  as the shipped `waitHealthy` in `cmd/evener-hub/internal/sshconn/version.go`
  does:

  ```
  ssh <dest> curl -fsS --noproxy '*' http://<loopback(addr)>/api/health
  ```

  where `<loopback(addr)>` is the configured host address (`Manager.hostAddr`:
  the per-host `addr`, else `Options.HubAddr`, else `127.0.0.1:9180`) with the
  same wildcard→loopback normalization the bridge applies (`loopbackAddr`:
  `0.0.0.0`, empty, and `localhost` → `127.0.0.1`; `::` → `[::1]`). The scheme
  is written explicitly: a normalized IPv6 wildcard yields a bracketed literal
  (`[::1]:9180`), and `curl` without a scheme reads the leading `[` as a URL
  globbing range and fails `curl: (3) [globbing] bad range` instead of probing.
  **The probe must bypass the host's proxy environment.** The `curl` runs in the
  host's shell, so a `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY` set there would route
  the loopback health request through a proxy instead of reaching the host's own
  hub (and could leak the loopback URL and health payload off-box) — the same
  hazard component 02's bridge dial avoids with `Proxy: nil`. The probe
  therefore passes `--noproxy '*'` (equivalently, clears the proxy variables for
  the command) so the request always goes directly to the loopback address, and
  this behavior is covered by a test asserting the argv carries `--noproxy '*'`
  (and that a set `HTTP_PROXY` in the host environment is not consulted).
  **The configured address must be a literal loopback or wildcard address.**
  The design's transport invariant is loopback-only ("No HTTP port exposed
  beyond the host's loopback", design §2 "Transport"), but a configured `addr` is
  interpolated into an SSH-side `curl` target, so an unvalidated non-loopback
  value (`10.0.0.5:9180`, or a hostname resolving off-box) would let this health
  probe — and the restart path that shares the address — reach an arbitrary
  reachable host. Both config validation (component 03, §"`config_path` /
  `addr`") and this probe must therefore reject a configured address whose host
  is not `127.0.0.1`/`::1`/`localhost`/`0.0.0.0`/`::`/empty with a named error,
  before any health check or restart. Only the **port** may vary; the earlier
  phrasing that the configured address "reaches a hub on a non-default port or a
  non-loopback bind" is superseded — a non-loopback bind is not a supported
  target. Accepting the wildcard spelling here and in component 03 is a
  transport-scoping decision, **not** a promise that the host hub is
  network-unreachable: it is accepted only because it normalizes to loopback for
  the manager's own dial/probe, and the required network-facing protections for
  a host hub actually bound wildcard are spelled out in component 03,
  §"`addr` host validation, and the exposure contract it must not weaken". The
  response is decoded as `hubapi.HealthResponse` and its `version` checked
  against the deployed identity. Consequences to state plainly:

  - the host must have a working HTTP client (`curl`) — a **hard
    prerequisite**, not a preference. The health probe runs *before* the attach
    bridge exists (this component's order: probe → deploy/restart →
    `waitHealthy` → `Runner.Start hub attach`; §"Data flow"), so an AppWire read
    over the attach bridge cannot substitute for it — with no `curl` there is no
    bridge to read `evener/settings/overview` from. A probe that cannot run is a
    **failed verification**, never an assumed success and never a fallback
    (§"Error handling"); `ensureOnce`/`waitHealthy` surface the failure and
    invent no restart. The earlier "with no `curl`, fall back to
    `evener/settings/overview`" description was unreachable and is superseded.
    (That read — `SettingsHubOverview` with `Version`, `Commit`, `BuildChannel`
    — remains the right probe for a *future* explicit health surface; it is not
    reachable on this path. `appwire.ServerInfo` carries only `Name`/`Version`,
    where `Version` is the static `"0.1.0"` hub constant, so it must never be
    used for this comparison.) There is no `evener hub health` subcommand today.
  - **Implementation status:** this is the shipped 04b contract (on `main`):
    `ensureOnce` probes the running version to drive the restart,
    `waitHealthy`/`parseHealthVersion` parse `/api/health` and require the
    expected `version`, and the probe URL is built from the configured host
    address through `loopbackAddr`. The earlier `date +%s` freshness-marker and
    "accepts any non-empty body" descriptions are superseded.
- **Stop/restart mechanics.** The host hub is detached and holds an advisory
  `flock` on `hub.lock` (`main.go`; `hostlock.go`) — an **`flock`,
  not a PID file**: the lock cannot be read to find or signal the process, and
  breaking it is never allowed. **Identify before killing:** the port alone is
  not identity, so a restart refuses unless all of these hold **and the signal
  re-validates the same identity immediately before signaling**, and
  refusal is `ErrRestart` naming the mismatch with no kill and no relaunch:

  1. exactly **one** process is listening on the configured address (two or more
     candidates means the address is wrong — e.g. a port collision with an
     unrelated service — not that one of them is ours);
  2. its recovered argv is an evener hub invocation — the configured
     `evener_path`/`evener` (or the unit's `ExecStart`) running the hub, not
     merely "something bound to that port";
  3. the process runs as the host's **effective** SSH user — `entry.User` when
     set, otherwise the user parsed from `entry.SSH` in `user@host` form,
     otherwise `id -un`/`whoami` probed during preflight. `user` is optional on
     `HostConfig`, so it is empty for hosts written as `ssh = "jesse@m4.local"`
     or resolved through ambient `~/.ssh/config`; comparing the process user
     against `""` would refuse legitimate hubs with `ErrRestart` and block
     automated restarts. **Implementation status:** the shipped 04b path (on
     `main`) identifies by executable, hub-subcommand position, and `--addr`
     agreement and does not yet compare users; the effective-user rule above is
     the required contract for that check.
  4. the listening socket matches the configured `addr` (host and port)
     **after the same wildcard→loopback normalization the bridge applies**
     (component 02, `loopbackAddr`): `0.0.0.0:<port>` and `:<port>` are compared
     as `127.0.0.1:<port>`, and `::` as `[::1]:<port>` (`net.JoinHostPort`, so
     the IPv6 literal is bracketed). Requiring the literal
     wildcard spelling would refuse every wildcard-bound hub — the common case,
     and the spike's m4 bound `*:9180` — stranding a host whose binary was
     already replaced. The same normalization applies to the supervisor "owns the
     configured address" check below.
  5. the identity is **re-validated at signal time**: the pid, argv, effective
     user, and socket from checks 1–4 are re-read at signal time, in the step
     immediately before the signal, and the signal goes out only on a full
     match. A mismatch — including any field that cannot be re-read — refuses
     with `ErrRestart`, no kill, no relaunch. **Implementation status:** the shipped `restartHub`
     (`sshconn/version.go`) refuses the supervisorless branch with `ErrRestart`
     and never calls `restartBare`; `restartBare` is unreachable from production
     (its only callers are unit tests) and, where it runs, validates only the
     single listener, the recovered argv shape, and the bound-address match, at
     identification time. The required code delta (design §2, [04] tracked
     follow-up) is to make the guarded ad hoc path the supervisorless branch and
     add the at-signal re-read here, so the effective-user comparison (check 3)
     and the at-signal re-read are both pending; the residual identification/
     signal window is the one the 2026-09-26 decision accepts. This shrinks the
     PID-reuse window but does **not** close it: it is still check-then-act, so
     the process can exit and its PID be reused between the re-read and the
     signal. **Decided by Jesse, 2026-09-26** (design §2 "Restart identity pin:
     verify-then-signal accepted"): this verify-then-signal form is the accepted
     answer, and the atomic `pidfd` pin this check previously demanded — a
     `pidfd` opened for the identified process as the target of
     `pidfd_send_signal`, or a host-side helper holding an equivalent pin — is
     **withdrawn**. The residual window is acknowledged, not closed. The bare
     unguarded `kill` remains unacceptable: a target whose identity cannot be
     verified is refused rather than signaled.
     **Platform reality: no atomic-handle form is reachable today, on any
     platform.** A `pidfd` must be opened and signaled by a process running on
     the host, and this component's only host interface is `ssh <dest>
     <command>` shell execution (`Runner`, §"SSH channel argv"); this series
     specifies, provisions, and invokes no host-side restart-identity pin
     helper (the crash-fencing `evener-fence` lease wrapper is a fencing helper,
     not a restart-identity pin). Under the
     withdrawn pin that is no longer a refusal: a **supervisorless** host
     restarts through the guarded verify-then-signal ad hoc path
     (implementation status, check 5: the shipped `restartHub` still refuses
     this branch). A
     **restart-capable deployment still prefers a supervisor** — the supervised
     paths name a unit/label rather than a PID: `launchctl kickstart -k
     gui/<uid>/<label>` on darwin, `systemctl [--user] restart <unit>` on linux
     — and a supervisorless host whose binary must be replaced is deployed to
     (the push path is unaffected) and restarted by the same ad hoc path, not
     left refusing.

  Supervisor detection obeys the same rules: a launchd label or systemd unit is
  accepted only when **exactly one** candidate names an evener hub *and* the hub
  it names owns the configured address. The three outcomes are distinct:
  **exactly one ⇒ use it**; **none ⇒ the supervisorless branch, which runs the
  guarded verify-then-signal ad hoc restart** (a stopped host's cold bootstrap
  is start-only — it has no PID to pin, §"First attach to a stopped host must be
  able to start the hub"); **several ⇒ refuse
  `ErrRestart` with no kill and no relaunch**. An ambiguous match means at
  least one matched unit may start or restart its own instance, so an
  unmanaged launch beside it is split supervision and `hub.lock` contention —
  the exact failure this rule exists to prevent, the same hazard as the
  unresolvable-address case below. "First name that contains `evener` and
  `hub` wins" is not acceptable either, because restarting an unrelated unit
  is the other failure this rule prevents. (Today's implementation matches
  names by substring in `isEvenerHubName`, and `pickSupervisor` already
  refuses ambiguity rather than taking the first match — it returns
  `ErrRestart` for more than one match, "an ambiguous listing is fatal, not a
  fallback" (`sshconn/version.go`, shipped with
  `multi-host-pr04b-deploy-restart`) — while `findHubPID` requires exactly one pid from
  `lsof -ti :<port> -sTCP:LISTEN`; what it does not do is check that the named
  hub *owns the configured address*, and the bare path does not compare the
  effective user. The contract above is stricter, and the definition-matching
  cold-bootstrap path must preserve the same ambiguity refusal.)
  **A stopped host has no listening socket, so "owns the configured address"
  cannot be the only supervisor-detection signal on the cold-bootstrap path.**
  When the hub is stopped the address check always fails, which would force
  every cold bootstrap to fall through to the ad hoc `nohup` launch even when a
  supervisor unit exists — causing split supervision and `hub.lock` contention
  once the supervisor later starts its own instance. Detection must therefore
  match **unit definitions**, not only active units: on systemd, enumerate with
  `systemctl [--user] list-unit-files` (or `list-units --all`) rather than
  `list-units`; on launchd, inspect the installed plists. A definition is a
  match **only when it both (a) launches the resolved hub executable running
  the hub subcommand — the same `run_path` + `hub` tokenization checks 1–4
  apply to a recovered argv — and (b) its own effective address, after the
  loopback normalization above, equals the configured address** (systemd
  `ExecStart`; launchd `ProgramArguments`). "Its own effective address" is the
  explicit `--addr` when the definition carries one, and otherwise **the
  address resolved from the definition's own `--config <path>`**: the manager
  reads that config on the host through the same seam and takes its `addr` — the
  value a hub launched from that definition would actually bind. A supervisor
  launched as `evener hub --config …` with no `--addr` is a valid, common case
  and must not be missed: matching only a literal `--addr` in the definition
  would let the manager start an ad hoc duplicate beside the supervisor, causing
  split supervision and `hub.lock` contention — the exact failure this rule
  exists to prevent. A definition that merely mentions the configured `--addr`
  (e.g. in `Description=`/`Environment=`, or an unrelated process's flag) or
  merely names a path containing `evener`/`hub` is **not** a match: without both
  facts it can select and start an unrelated service. The exactly-one-candidate
  rule still applies, with the same three outcomes: **several matches refuse
  `ErrRestart` and start nothing** (no ad hoc launch chosen on top of an
  ambiguity), and **only no match at all** is the supervisorless branch: its
  restart runs the guarded verify-then-signal ad hoc path (check 5, and the ad
  hoc branch below), and its only no-supervisor launch is the cold-bootstrap
  **start**.
  **When a candidate hub definition exists but its effective address
  cannot be resolved** (no explicit `--addr` and the `--config` is unreadable or
  carries no address), the cold bootstrap **refuses with `ErrRestart` and
  starts nothing** — it must not fall through to the ad hoc launch and risk a
  duplicate — unless trusted explicit supervisor metadata (an operator-set
  label/unit name recorded in the host entry) identifies the unit and supplies
  the match. An inferred substring match may never be used. The
  address-ownership check remains required whenever a listener exists (the
  restart path); on a genuinely stopped host the unit-definition match above
  plus the "refuse to start when a hub already owns the address" pre-check
  (`runningKnown`) is the substitute.

  **Limit: identification and signal are separate host commands, so there is a
  PID-reuse window.** Every check above is its own command over the ssh seam —
  `lsof` for the listener, `ps` for the argv, further probes for user and
  socket — and the `kill` is one more command issued after them. Nothing binds
  the identified process to the signal atomically: between the `ps` read and
  the `kill`, the hub can exit and its PID can be reused by an unrelated
  process, which then receives the SIGTERM. The argv and address checks narrow
  the window but cannot close it, because they describe the process at
  *identification* time, not at *signal* time. Re-checking identity immediately
  before signaling (a guarded compare-and-kill) shrinks the race but is still
  check-then-act — re-reading the start time at signal time is no better,
  because the process can still exit and its PID be reused between that read
  and the signal; the earlier same-remote-command variant was considered and is
  not required. **This residual window is accepted** (design §2 "Restart
  identity pin: verify-then-signal accepted", Jesse 2026-09-26): the shutdown
  preference is a supervisor restart by unit/label, and a supervisorless hub
  uses the guarded verify-then-signal path, which refuses a target it cannot
  verify rather than signaling it. The shipped `restartBare`
  (`sshconn/version.go`) validates the target at identification time — it
  recovers the listener's argv and bound address and refuses `ErrRestart` on
  mismatch before signaling — and never issues a bare `kill` on an unverified
  target.

  The restart path is then:
  1. **Supervised hub** — restart it the way its supervisor expects:
     `launchctl kickstart -k gui/<uid>/<label>` on darwin, or
     `systemctl [--user] restart <unit>` on linux. **`<uid>` is the numeric
     effective uid resolved in preflight** (`id -u`, §"Preflight + version
     contract"), **never a `$(id -u)` shell substitution**: the quoting rule
     above wraps anything outside the bare-safe set in single quotes, so
     `gui/$(id -u)/<label>` would reach `launchctl` as the literal string
     `gui/$(id -u)/<label>` with no expansion and never restart the unit. With
     the numeric uid and a `<label>` validated against the bare-safe set
     (`[A-Za-z0-9_.-]`, no `/`; a label that fails it is never interpolated into
     the remote shell, and the restart **falls through to the guarded ad hoc
     path instead of refusing** — the accepted outcome, design §2 "Restart
     identity pin: verify-then-signal accepted", Jesse 2026-09-26), the
     whole `gui/<uid>/<label>` argument is one bare-safe word
     passed verbatim. Adding `gui/$(id -u)/<label>` to the raw exception list is
     rejected: the label is host-derived data, so passing it raw is exactly the
     injection the quoted path avoids. Detection is from the host's
     own listings (`launchctl list`, `systemctl list-units`) under the
     identification rules above. The supervisor command's exit status is not the
     check; the on-host `/api/health` probe is (`scripts/ops/deploy-hub.sh`
     makes the same judgement).
  2. **Ad hoc hub** — the documented recipe
     (`docs/evener-hub-remote-operations.md` §"Restarting an ad hoc Hub"):
     find the PID listening on the hub port
     (`lsof -ti :<port> -sTCP:LISTEN`), recover its command line
     (`ps -p <pid> -ww -o command=`) and log (`lsof -p <pid> -a -d 1,2`), `kill`
     it (SIGTERM; the graceful drain is capped at ~5s), **wait for the port to
     clear**, then relaunch detached, appending to the recovered log
     (`nohup <quoted argv…> >> <log> 2>&1 </dev/null &`). `SysProcAttr` is
     local-only, which is why the host-side detach is a remote shell idiom.
     **This ad hoc path is the manager-automated restart for a supervisorless
     hub** (design §2 "Restart identity pin: verify-then-signal accepted",
     Jesse 2026-09-26): the manager identifies the listener through the checks
     above, refuses `ErrRestart` on any mismatch, and only then signals and
     relaunches. No atomic process handle is reachable through this component's
     interfaces — a `pidfd` must be opened and signaled by a process on the
     host, the only host interface here is `ssh <dest> <command>`, and no
     host-side restart-identity pin helper is specified, installed, or invoked
     (check 5) — so the
     residual identification/signal window is accepted, and the manager never
     issues a bare unguarded `kill` on an unverified target (check 5 and its
     implementation status; the limit above). The recipe above remains the *operator's* documented procedure for
     a restart out of band. The **start** of a
     stopped hub (the first-attach bootstrap, where no process exists to
     identify or signal) is unaffected: it has no PID to pin, so it keeps the
     detached `nohup` launch.
     The recovered log is used **only** when both fd 1 and fd 2 point at the
     same regular file (`parseLogPath`): a pty, a pipe, `/dev/null`, or two
     different destinations yields no log path. With no recovered log the
     relaunch **discards output** — `nohup <quoted argv…> </dev/null
     >/dev/null 2>&1 &` (`relaunchCommand`) — rather than appending to an empty
     `<log>`. An empty path would make the shell redirection itself fail *after*
     the old hub was already killed, leaving the host with no hub running.
     Every host-derived word in these commands is shell-quoted (the pid, the
     port, the log path), and the recovered command line is **never re-parsed as
     shell code**. `hubArgvFromCommandLine` (`sshconn/version.go`) tokenizes it
     (`tokenizeCommandLine`, honoring single/double quotes and backslash escapes
     and refusing any *unquoted* shell metacharacter), then requires the
     recovered executable to be the **configured target**, not a hardcoded
     basename: resolve `argv[0]` with the same portable POSIX-sh resolver
     `deployTarget` uses (`resolvePathScript`, plain `readlink` + `cd -P … &&
     pwd`, no `readlink -f`) and compare that canonical path to the host's
     canonical `run_path` (§"SSH channel argv"); for a supervised hub the
     unit's `ExecStart` path is the expected value instead. It also requires
     the `hub` subcommand at the actual subcommand
     position (`argv[1] == "hub"`), and no positional after it
     (`evener hub attach`, the client, is not the daemon); matching `hub`
     anywhere in argv would accept `evener serve --model hub`. Anything else is
     refused with `ErrRestart` and **no `kill`**. `path.Base(argv[0]) ==
     "evener"` is **not** the check: `evener_path` is documented as an arbitrary
     executable path (§"Push target resolution"), so a valid custom target (say
     `/opt/evener/current/evener-hub`, or a symlink resolved through several
     hops) is deployed and launched by the manager but can never pass a basename
     comparison — the host passes configuration and deploy, then refuses every
     restart with `ErrRestart`. Canonicalizing both sides and comparing them is
     the identity the rule needs; a basename is neither necessary nor
     sufficient. When the
     recovered argv carries an `--addr`/`-addr`, its port must agree with the
     probed port (`hubAddrFlag`/`hubPort`) or the restart is refused: a process
     that merely holds the port is never killed and relaunched. `relaunchCommand`
     then quotes **each** recovered word individually — the `nohup`, the
     redirection, and the backgrounding are ours, not the host's. `ps` runs with
     `-o command=` so the `COMMAND` header can never become part of the relaunched
     argv, with a header-stripping guard as the defensive half (`stripPSHeader`).
  3. Either path then verifies on the host through `/api/health` (above) before
     re-attaching.
  A relaunch that lands in the stop/start gap loses the lock race (the ops
  doc's "resource temporarily unavailable" message) and is retried under the
  backoff, never forced; the manager must surface "could not stop the old hub"
  rather than start a second one (see §Error handling).
- **Running sessions keep their binary.** Restarting the hub does not restart
  host daemons: "Existing daemons keep the `evener` binary they were spawned
  from. Rebuild and restart sessions to pick up a new binary"
  (docs/evener-hub.md:501-502; same rule for hub self-update,
  docs/evener-hub.md:380-382). The spec inherits this: version auto-match moves
  the **hub**, not live sessions; a session moves builds only when restarted.
- **Restart drops live clients.** Restarting the hub drops live web/controller
  connections — including this controller's bridge and its parent connection.
  The manager must expect its own channel to die during a self-triggered restart
  and reconnect into the new hub (design §6; `02-attach-bridge.md`). Whether to
  defer the restart while clients are attached is an open question.

## Data flow

```
hub.toml [[hosts]] →  hostreg.Registry (component 03)
                   →  Manager.Ensure(name)
                        ├─ Runner.Run: ssh <dest> launch-check --protocol … --json
                        │     → protocol? launch_flags? version? (preflight)
                        ├─ verified missing executable? (fresh host)
                        │     └─ deploy/install (creates the missing run target:
                        │          push, else the installer fallback) → re-run preflight
                        ├─ protocol != controller?    → cannot attach: no deploy
                        │     changes the protocol a binary speaks
                        ├─ version != controller, and a deploy path can fix it?
                        │     ├─ deploy target build (cross-compile → scp/chmod)
                        │     └─ restart host hub (identify → supervisor, else guarded ad hoc restart)
                        │          → Runner.Run on the host: curl /api/health
                        │            → answer + running version == the expected build
                        ├─ version != controller with nothing to deploy?
                        │     → attach on the host's own build: the protocol is the
                        │       compatibility contract, so the difference is reported
                        ├─ Runner.Start: ssh <dest> <evener_path> hub attach --stdio
                        │     ├─ stdin  ← StreamTransport.Send
                        │     ├─ stdout → StreamTransport.Recv
                        │     └─ stderr → controller diagnostics
                        └─ appwire.Client.Initialize(protocol, ClientInfo)
                              → *Channel (Client, Transport)
                                    → component 05: RemoteHubSource over Client
```

On link-down the `→ attached` edge is replaced by `→ reconnecting → Ensure`,
with the remote hub and its daemons still running.

## Error handling

- **SSH connect/start failure** → `ErrSSHStart` wrapping the diagnostic tail
  (ssh's own diagnostics and, on a completed run, the remote command's stderr —
  see the stream-merge rule below); no channel is returned; retried under
  backoff (attaching) or surfaced once (explicit `Ensure`). A **verified
  missing executable** is not this class: it is a defined preflight result that
  routes to deploy/install (§3).
- **Verified missing executable** → `ErrExecutableMissing` (a preflight
  result, §3), routed to deploy/install and followed by a re-preflight;
  **only** this verified case enters installation, so an unreachable host
  (`ErrSSHStart`), an auth refusal, or an unparseable answer never deploys.
  A deploy that then fails surfaces `ErrDeploy` (below); a re-preflight that
  finds the binary still absent or the wrong build is a failed verification,
  never an attach.
- **Non-interactive auth failure is *not* terminal, because it cannot be
  attributed to ssh.** ssh forwards the remote command's own stderr onto the
  same stream as its diagnostics and forwards the remote command's exit status
  unchanged — including 255, the single status ssh(1) uses for its own
  failures — so a completed run's auth-shaped marker or 255 exit may equally be
  the remote program's. The safe side of that ambiguity is to keep it retryable
  (`ErrSSHStart`) under backoff rather than ending the reconnect loop for a host
  that is merely unreachable or returning an application error. `ErrSSHAuth` is
  the sentinel for the one attributable case — a failed `Start` that never
  spawned a remote command, so a marker on the diagnostic stream is necessarily
  ssh's own — and it is **not** terminal either (`isTerminal` excludes it,
  `sshconn/manager.go`); in practice a real spawn failure carries no diagnostic,
  so an unauthenticable host ordinarily lands in the retryable `ErrSSHStart`
  class. An operator-facing hint to install a key/agent on the host may still be
  surfaced alongside the class, but it does not make the error terminal.
- **`RunError.Stderr` merges the two streams, and the contract states that
  limitation.** A failed one-shot `Runner.Run` reports a `*RunError` whose
  `Stdout` and `Stderr` are separate buffers (`sshconn/runner.go`), but `Stderr`
  is **not** ssh's own diagnostics alone: ssh writes its diagnostics and the
  remote command's stderr to the same stream, so an auth marker there cannot be
  attributed to ssh. The classifier therefore attributes an auth marker to ssh
  **only** when no remote command ran (no `*RunError`; `isSSHAuthFailure` /
  `sshDiagnostic`, `sshconn/preflight.go`); a completed run — any exit status,
  including 255 — stays retryable `ErrSSHStart`. This is why the contract is not
  "auth failures are terminal": the merged channel cannot support that
  classification, and the landed 04a path makes every completed command failure
  retryable (component 04a, commit `0fc5587ac`; `sshconn/errors.go` documents
  `ErrSSHAuth` as not terminal and never classified in production). A remote
  program that prints "Permission denied" on stdout or stderr for its own
  reasons must not stop the reconnect loop. A truly terminal auth state is
  available only if ssh's diagnostics are captured through a **separate
  channel** (a diagnostic sink distinct from the remote command's stderr) so the
  attribution is provable; until then the retryable contract stands (tracked as
  a code follow-up in the keystone).
- **Protocol mismatch** (`launch-check --protocol` refusal at `spawn.go`, or
  `appwire.ProtocolVersionMismatchError` at `client.go`) → **the auto-match
  trigger, not a terminal state**. A `launch-check` refusal means the on-disk
  binary is not the controller's build: deploy the matching build, restart,
  re-preflight, and attach (component 05's version-mismatch rule assumes this).
  An `Initialize` mismatch on a channel whose preflight matched means the
  *running* hub is stale while the on-disk binary is right: restart once and
  re-attach. Only a mismatch that survives the restart becomes terminal
  `ErrProtocolIncompatible`. (Implementation status: shipped. `probeLaunchCheck`
  classifies a protocol refusal (`sshconn/preflight.go`); with a deploy
  path configured, `preflight` lets the ladder deploy, re-probe, restart, and
  refuse terminally only when the protocol still mismatches
  (`sshconn/preflight.go`'s deploy deferral; `sshconn/manager.go`, after the restart).)
- **Missing `api-log` launch flag** (`spawn.go`) → `ErrLaunchContract`. A
  too-old host binary cannot be launched, and the version-match deploy is
  exactly its fix, so this is an auto-match trigger first (deploy, restart,
  re-preflight) and terminal only if it survives that. (Implementation status:
  shipped — `preflight` no longer judges the launch flags; `ensureOnce` refuses
  `ErrLaunchContract` only after the deploy/restart path has had its chance
  (`sshconn/manager.go`), and `isTerminal` then stops the retry loop.)
- **Unsupported host os/arch** → terminal `ErrUnsupportedHost`; no build exists
  (`install.sh:39-45`).
- **`launch-check` output unparseable** (`launchcheck.go` JSON; local
  decoder `spawn.go`) → `ErrPreflightDecode`; treat as incompatible host.
- **Deploy failure** (cross-compile nonzero, `scp` nonzero, checksum failure in
  the installer path `install.sh:124-130`) → `ErrDeploy`; the existing binary on
  the host is left untouched (temp-name + atomic `mv`).
- **Unverifiable restart target** → `ErrRestart` and **no restart**: the listener
  is not provably this host's hub (more than one listener, a command line that is
  not a tokenizable `evener hub` invocation, an `--addr` port that disagrees with
  the probed port, a process not owned by the host's effective SSH user, or a
  socket that does not match the configured `addr`), or the address/config path
  is not known well enough to match. A collision or a stale process must never
  be killed or relaunched; the operator fixes the entry (`config_path`/`addr`)
  or the host.
- **Restart failure** → `ErrRestart`; the manager stays `disconnected` and the
  next `Ensure` retries. A hub that fails to start leaves `hub.lock` free, so a
  retry is safe; if the old hub could not be stopped, surface that explicitly
  rather than starting a second hub. A hub that is up but reports a `version`
  other than the expected build is the same failure: the restart did not take,
  and the manager must not attach to the old process as if it had. Likewise a
  health probe that could not run (no `curl`, no answer within the bound) is a
  failed verification, never an assumed success.
- **Link drop after attach** → `reconnecting`, not an error to the caller; the
  channel's `Client` fails in-flight calls, matching AppWire's existing client
  behavior.
- **stdout discipline.** The bridge owns stdout for frames; the manager must never
  write logs into the child's stdin or read the child's stdout outside
  `StreamTransport` (component 02, §"Contract" — stdout carries framed AppWire
  exclusively, and it is a test).

## Testing

- **Fake runner (default `go test`).** Inject a `Runner` that returns canned
  `launch-check` JSON and an in-memory `io.Pipe` pair for `Start`. This is the
  unit seam; production `execRunner` is the untested-by-default part. The
  injection style mirrors the existing function-var seams
  (`cmd/evener-hub/spawn.go`) and `launchCheckLoadClient`
  (`launchcheck.go`).
- **Preflight table tests.** `uname`/`sw_vers`/`HOME`/XDG outputs → resolved
  `GOOS`/`GOARCH`/state root. Include the non-interactive case with no `XDG_*`
  (spike finding) and assert the `~/.config`/`~/.local/state` fallback. PIN the
  os/arch mapping to `install.sh:21-45` with a parity table so the two cannot
  drift.
- **Version-match tests.** Host version == controller → no deploy, attach.
  Host `<` or `>` controller → deploy argv asserted, then restart argv, then
  attach; `--protocol` value asserted to be `appwire.ProtocolVersion`. The
  deploy argv must carry the `-ldflags` stamping `buildinfo.GitSHA` /
  `GitDirty` / `BuildTime` / `Channel` from the controller process (assert the
  rendered flag string, not a real `go build`). A controller whose
  `buildinfo.Version()` is `"dev"` and a host reporting a real SHA → `ErrDeploy`
  with no build and no restart; `"dev"` on both sides → attach with no deploy.
- **Launch-contract tests.** Missing `api-log` in `launch_flags` routes to the
  version-match path; a `launch-check` protocol refusal routes to deploy +
  restart and is not terminal; a protocol mismatch that survives the restart
  returns `ErrProtocolIncompatible` without a bridge `Start`.
- **Restart-target tests.** Fake runner answers `lsof`/`ps` with (a) one listener
  whose argv is an evener hub as the effective SSH user → restart proceeds;
  (b) two listeners on the port; (c) an argv that is not an evener hub;
  (d) a process not owned by the effective SSH user; (e) a socket on another
  address; (f) a command line with an unquoted shell metacharacter or an
  unterminated quote; (g) an evener hub
  whose `--addr` port disagrees with the probed port → every case (b)–(g)
  returns `ErrRestart` with **no** `kill` and no relaunch argv in the runner log.
  Tokenizer unit tests cover quotes/escapes and each refusal.
  Supervisor cases: exactly one evener-named unit → restarted; two candidates →
  `ErrRestart` with no `kill` and no relaunch argv (the ambiguity refusal, never
  "first match" and never an ad hoc launch); zero candidates → the
  supervisorless guarded ad hoc restart, refusing `ErrRestart` with no `kill`
  and no relaunch argv only for a listener whose identity does not verify, and
  the cold-bootstrap **start** is the only no-supervisor launch.
- **Health-verification tests.** Fake runner returns a body reporting the
  previous build's `version` → not accepted; a body reporting the expected
  `version` → accepted; no answer within the bound, or a missing HTTP client →
  `ErrRestart`, never an assumed success. Assert the probe argv is the on-host
  `curl -fsS --noproxy '*' http://<loopback(addr)>/api/health` over `ssh`, not a controller-side HTTP
  call, and that the address is the configured per-host `addr` normalized to
  loopback (a non-default port and a `0.0.0.0`/`::` bind both appear as the
  loopback form, with the explicit `http://` scheme so a bracketed IPv6 literal
  is not read as a globbing range). Assert the argv carries `--noproxy '*'`, so a
  `HTTP_PROXY`/`HTTPS_PROXY` in the host's environment cannot route the loopback
  health request through a proxy. Assert an auto-match that decides to restart does so because
  the **running** `/api/health` version differed, not only the on-disk
  `launch-check` version, and that a probe that answers nothing invents no
  restart.
- **Channel argv tests.** Assert `ssh -T -o BatchMode=yes -o ConnectTimeout=…,
  ServerAliveInterval=…, ServerAliveCountMax=… -- <dest> <evener_path> hub attach
  --stdio`; assert stderr is wired to the diagnostic sink and stdout is not
  touched outside `StreamTransport`. With a per-host `config_path`/`addr` set,
  assert `--config`/`--addr` appear in that order (each value shell-quoted) and
  that the restart/health path uses the same address. Assert `--` precedes the
  destination so a `dest` beginning with `-` is never read as an ssh option.
  Assert **no** ssh invocation carries `StrictHostKeyChecking=no`/`accept-new`
  or any raw `-p`/`-i`/`ProxyJump` option, and that an unknown host key is
  surfaced with the named operator hint (add the key out of band —
  §"Contract", "Host-key verification is the user's `known_hosts`") in the
  **retryable** `ErrSSHStart` class rather than asserted terminal: a completed
  run's stderr cannot be attributed to ssh, so a host-key refusal stays
  retryable and must not end the reconnect/backoff loop.
- **Quoting tests (`quote_test.go` + argv tables).** `shellQuote` leaves an
  all-safe word bare and single-quotes anything else, closing and escaping an
  embedded `'`; `~` stays bare so `~/bin/evener` still expands. Round-trip the
  argv through a shell check that an `evener_path`, `config_path`, and `addr`
  containing a **space** and one containing `;` (and a host-derived log path
  containing a quote) each arrive as exactly one word rather than splitting or
  executing. Assert the probe commands (`uname`, `printenv`, `launchctl list`,
  `systemctl list-units`) are passed unquoted and verbatim, so the deliberate
  exception cannot be "fixed" away. Assert the relaunch command quotes each
  recovered word and contains **no** `sh -c`.
- **Run-target resolution tests.** With `evener_path` empty and a fake runner
  whose `command -v evener` answers an absolute path, the channel argv, the
  deploy/install target, the health/identity check, and the restart
  identification all use that one resolved `run_path` (assert the same path in
  every argv); with `command -v evener` missing the resolution is the
  installer default `<home>/.local/bin/evener` and the atomic push refuses with
  `ErrDeploy` while the installer fallback targets it. With `evener_path` set,
  the invocation uses it (normalized absolute) and the push resolves its
  symlink. A configured relative `evener_path` is refused with `ErrDeploy`, and
  a `~/…` value is expanded against the captured `Preflight.Home` before it
  reaches any argv. A test that the literal word `evener` never appears as a
  run target.
- **Reconnect test.** Fake link drops (`io.EOF` / child exit) → assert backoff
  schedule, a fresh `Start`, and that no `hub` start was issued (re-attach, not
  a second hub).
- **Idle-channel test.** A fake stream that stays open and emits no frame while
  its ssh child is alive → the manager stays `attached` (receive silence alone
  is not link-down, and there is no inactivity deadline). A stream whose child
  exits, or whose `Recv` returns `io.EOF`/error, drops to `reconnecting`.
- **Live, environment-gated.** One test guarded by `EVENER_SSH_E2E=1` *and* an
  explicit host (e.g. `EVENER_SSH_E2E_HOST`) against a disposable host: preflight,
  deploy-if-needed, attach, `initialize`, `thread/list` — the spike's proven
  sequence (`spike/client/main.go`). It must `t.Skip` unless both env vars are
  set, and must never run in default `make test` (design §7). It must also skip
  in `-short`.
- No fuzz target is required; the SSH argv and command construction are
  table-driven and the preflight parsers are pure.

## Acceptance criteria

1. `go test ./cmd/evener-hub/internal/sshconn/... ./cmd/evener-hub/...` passes
   with no host, network, or `ssh` binary required (the fake runner covers the
   unit seam).
2. Default `make test` performs no SSH: the live test skips unless
   `EVENER_SSH_E2E=1` is set (verify with `go test -v` output showing `SKIP`,
   and by no test importing `os/exec` reachable without the env gate).
3. `Ensure` on a host whose `version` differs **and with a deploy path configured**
   deploys the matching
   `GOOS`/`GOARCH` build and restarts the host hub before attaching; on a matching
   version, or with no deploy path to converge it, it attaches without deploying
   (`Runner.Run` argv log asserted).
   The deploy argv stamps the controller's buildinfo (`-ldflags`), and a `"dev"`
   controller does not auto-match. The restart is verified **on the host** by
   `curl …/api/health` run through `Runner.Run` — the running hub must report
   the expected `version` — and a health answer from the *old* process (a
   different `version`), or no answer at all, is not accepted. A running-hub
   version that differs drives the restart even when the on-disk binary already
   matches, and a probe that answers nothing invents no silent restart on a
   reconnect (a first-attach bootstrap start, criterion 15, is the only
   nothing-answering start and runs only from an explicit attach request).
4. The SSH channel argv is exactly the non-interactive form in "Contract",
   with `-T`, `--` before the destination, and every remote word shell-quoted;
   stdin/stdout are the `StreamTransport` and stderr is diagnostics only. A
   `evener_path`/`config_path`/`addr` containing a space or `;` reaches the
   remote shell as one word and executes nothing.
5. A link drop leaves the remote hub and its daemons running and re-attaches as
   a client; `Start` is re-issued but no hub-start command is issued.
6. A host on an unsupported os/arch fails with a named error and no deploy.
7. A protocol mismatch found at preflight or at `Initialize` enters the
   deploy/restart (preflight) or restart (initialize) flow; `ErrProtocolIncompatible`
   is returned only when the mismatch survives it, before any bridge `Start`.
8. The live `EVENER_SSH_E2E=1` test reaches `initialize` + `thread/list` over the
   channel (matches Spike C,
   `2026-09-14-multi-host-spikes-findings.md:27-33`).
9. A restart refuses (`ErrRestart`, no kill, no relaunch) when the listener on
   the configured address is not provably this host's hub: several listeners,
   a command line that is not a tokenizable `evener hub` invocation, an `--addr`
   port that disagrees with the probed port, a process not owned by the host's
   effective SSH user, or a socket mismatch.
10. Lifecycle events never change registry membership: with `OnEvent` wired to a
    counter, attach/detach transitions change `Attached` and emit events, and
    `appsource.Registry.All()` is untouched.
11. A silent but healthy channel stays `attached` (receive silence alone is not
    link-down, and there is no receive-inactivity deadline). Link-down is
    detected from the ssh child's exit or a `Recv` error/`EOF`, and either
    enters `reconnecting`.
12. The restart is entered when a deploy replaced a present hub, or when the
    running `version` differs while the on-disk build already matches — an on-disk
    difference with nothing to deploy attaches instead of restarting — and the
    post-restart verification requires the running hub to report the build the host
    is expected to serve (`expectedServedBuild`: the controller's own when the host
    carries it, the host's own otherwise); no host-side
    timestamp and no clock comparison is used.
13. A wildcard-bound hub (`0.0.0.0:<port>` / `::`) is restart-eligible:
    identification normalizes the configured `addr` to loopback exactly as the
    bridge does, rather than requiring the literal wildcard.
14. A `[[hosts]]` entry that sets exactly one of `config_path`/`addr` fails
    validation; a push deploy with an empty `evener_path` whose
    `command -v evener` resolves uses that as the host's `run_path` and
    replaces the POSIX-sh `resolvePathScript` target (plain `readlink`, no
    `readlink -f`) atomically; a `command -v` miss resolves to the installer's
    default `<home>/.local/bin/evener`, which the push deploy canonicalizes
    under its parent directory (created with `mkdir -p` when absent) and
    creates atomically — a missing target is creatable on every channel, never
    `ErrDeploy` (the installer fallback installs that default only when it is
    the deploy path). A
    configured **relative** `evener_path` is refused with `ErrDeploy` and a
    leading `~/` is expanded against the host's captured home
    (`Preflight.Home`), so no relative or unexpanded-`~` path reaches install,
    launch, health, or restart.
15. A first attach to a host whose hub is not running starts the identified
    supervisor (or the detached ad hoc launch), waits for `/api/health` to report
    the build the started binary actually carries — the controller's when a deploy
    converged the host, the host's own otherwise (`expectedServedBuild`) — and attaches
    only after it matches; an address a hub
    already owns starts nothing, and a hub that never becomes healthy attaches
    nothing and fails with `ErrRestart`. An **ambiguous** unit-definition match
    starts nothing and fails with `ErrRestart` (never an ad hoc duplicate, and
    `hub.lock` contention is not risked). The cold ad hoc launch passes the
    entry's `hub [--config <config_path>] [--addr <addr>]` (each shell-quoted,
    log under `<stateRoot>`), so a host on a non-default port becomes healthy
    rather than failing on the default address.
16. The installer fallback passes an artifact reference derived from
    `buildinfo.BuildChannel()`: the stamped release tag for `release` (whose
    archive `install.sh` verifies by sha256 against that release's
    `checksums.txt` before extracting it; a release build carrying no stamped
    tag is `ErrDeploy`), the mutable `snapshot` tag for `snapshot` (a
    best-effort pin: the post-install version probe refuses terminally,
    `ErrVersionMismatch`, once the tag has moved past this controller's commit,
    rather than re-fetching the same artifact), and `ErrDeploy` for a
    `dev`/`dirty` controller; `buildinfo.Version()` (a short SHA, possibly
    `-dirty`) is never passed as the tag. For a `snapshot` controller the
    installer therefore replaces the installed binary before its commit is
    proven, and the proof arrives only afterwards — the trade §"Installer
    (fallback)" states, and the reason a `snapshot` controller with no build
    source still has a deploy path. The atomic push path keeps the stronger
    property (pinned by construction: locally cross-compiled from a verified
    revision, stamped, byte-count-verified, then `mv`), which is why the
    terminal refusal names it as the remedy.
17. The installer fallback installs to the run target: with `evener_path` set
    it passes `BINDIR=<dirname(evener_path)>` (refusing any basename other than
    `evener` — `evener-dev` is the development tooling binary with no `hub`
    command and no `launch-check`, so it is never a valid run target — or a
    missing directory, with `ErrDeploy`); with `evener_path` empty it passes
    `BINDIR=dirname(run_path)` for the host's one resolved `run_path` —
    `command -v evener` when it resolves, else the installer's default
    `<home>/.local/bin/evener` — and that same `run_path` is the path the manager
    checks, restarts, and launches. No deployment, restart, health, or attach
    step may name a different binary than `run_path`.
18. A restart identifies the hub by the canonical recovered executable
    (symlinks resolved with `resolvePathScript`) compared to the host's single
    resolved `run_path` / unit `ExecStart`, so a valid custom `evener_path`
    basename restarts; a non-hub executable still refuses.
19. The supervised darwin restart passes `gui/<numeric-uid>/<label>` as one
    bare-safe word (uid from preflight); no `$(id -u)` reaches the remote
    shell. A label outside the bare-safe set is never interpolated into the
    remote shell and **falls through to the guarded ad hoc restart instead of
    refusing** — the accepted outcome (design §2 "Restart identity pin:
    verify-then-signal accepted", Jesse 2026-09-26). The fall-through is
    required work, not shipped behavior: the shipped `restartHub` refuses this
    label case too (implementation status, check 5), on the same branch that
    must be wired to the guarded ad hoc path.
20. Restart safety is hardened against the identification/signal PID-reuse
    window by a **guarded verify-then-signal**: the pid, recovered argv (with
    `--config`/`--addr` agreeing with the entry's configured `config_path`/
    `addr` after loopback normalization), effective user, and listening socket
    are re-read before the signal is issued, and the signal goes out only on a
    full match; any mismatch — or any field that cannot be re-read — refuses
    with `ErrRestart`, emitting no signal and no relaunch. A target whose
    identity cannot be verified is refused rather than signaled; a bare `kill
    <pid>` on an unverified target is never acceptable. **Decided by Jesse,
    2026-09-26** (design §2 "Restart identity pin: verify-then-signal
    accepted"): this is the accepted answer, and the host-side **atomic process
    handle** (a `pidfd`, or an equivalent pin helper) is **withdrawn**; the
    residual check-then-act window is acknowledged, not closed. The restart
    still prefers a supervisor (the supervised paths pin by systemd unit /
    launchd label, not by PID), and a supervisorless host restarts through the
    guarded ad hoc path, which must re-read the target at signal time per check
    5. The shipped `restartHub` still refuses the supervisorless branch with
    `ErrRestart` and `restartBare` is unreachable from production, so the
    guarded ad hoc path (implementation status, check 5) is required work, not
    shipped behavior. This criterion makes checks 1–5 of §"Stop/restart
    mechanics" testable end to end.
21. A fresh host whose `run_path` does not exist is a **verified missing
    executable** preflight result (`ErrExecutableMissing`), recognized from the
    **dedicated executable probe's stable sentinel** — `test -x <run_path>`
    exiting `1`, never a parsed `127`/not-found string and never the merged
    diagnostic stream — routed into deploy/install, which creates the missing
    run target: the atomic push creates the resolved path (the installer's
    default `<home>/.local/bin/evener` when nothing resolves, its parent
    directory created when absent), and the installer fallback installs that
    default when it is the deploy path — followed by a re-preflight before
    version-match/attach; it is not surfaced as `ErrSSHStart` and does not
    dead-end preflight. Where no deploy path exists (a `dev`/dirty controller,
    or a `release` build with no stamped tag, whose installer fallback
    `canDeploy()` refuses) the missing executable is refused
    `ErrDeploy`: there is nothing that controller can install, so that
    bootstrap is not a supported path.
    An unreachable host (`ErrSSHStart`), an ssh-level
    `255`, an auth-shaped refusal, an unparseable/empty `launch-check` answer,
    and a launch-contract/protocol refusal do **not** enter deploy/install —
    asserted by a fake-runner table over the probe cases, where only the
    verified absent sentinel for `run_path` produces install argv.

## PR size estimate (LOC)

The scope is large enough to be **two reviewable PRs**, which also matches the
design's "land in dependency order" intent (design §8):

- **04a — channel + preflight + lifecycle (~400–650 LOC + ~250–350 test LOC).**
  `runner.go`, `manager.go`, `preflight.go`, argv construction, keepalive/reconnect,
  the fake runner, preflight/argv/reconnect tests. No deploy, no restart.
- **04b — deploy + version auto-match + restart (~250–450 LOC + ~200–300 test LOC).**
  `deploy.go`, `version.go`, cross-compile/scp/installer paths, restart mechanics,
  the live gated test. Depends on 04a.

Total ≈ **650–1,100 LOC**, in two PRs; no UI and no changes outside
`cmd/evener-hub/internal/sshconn` plus the wiring line that builds the manager
from the component-03 registry.

## Open questions

- **Which deploy path is authoritative.** Cross-compile+push proves self-contained
  but ships only `linux/amd64` and `darwin/arm64` (the two `goreleaser` targets,
  `.goreleaser.yml:24-30`); installer runs on the host but needs host network and
  a release archive. Decide precedence and whether `evener_path` implies
  "already correct, skip deploy".
- **How the host hub is stopped for restart (resolved, with identification).**
  No new host-side RPC is needed in v1: restart through the host's supervisor
  when one is identified unambiguously; when **none** is identified the manager
  runs the guarded verify-then-signal ad hoc restart itself (implementation
  status, check 5: the shipped `restartHub` still refuses this branch), refusing
  `ErrRestart` (no signal, no relaunch) only for a listener whose identity does
  not verify. The ops doc's recipe — find the listener by port with `lsof`,
  verify *what it is* (single listener, evener hub argv, effective SSH user,
  matching address), recover argv/log, `kill` it, wait for the port to clear,
  relaunch detached — remains the **operator's** out-of-band procedure for a
  supervisorless host, and the cold-bootstrap **start** (no process to identify
  or signal) is the manager's only no-supervisor launch. See §5. `hub.lock`
  stays a pure mutual-exclusion `flock` (`main.go`; `hostlock.go`); it is never
  read for a PID and never broken. Residual risk: that recipe calls `lsof`/`ps` on
  the host, so a host without those tools (or a hub the controller cannot match
  to the configured address) is restart-refused rather than restarted wrong —
  which is the intended trade.
- **Per-host config path and address (corrected contract, coupled fields).**
  `hostreg.Host` carries `ConfigPath` and `Addr` (`hostreg/hostreg.go`), and
  `channelArgv` passes whichever is present. `Manager.hostAddr`
  (`sshconn/version.go`, shipped on `main`) reads the per-host `addr` on
  the restart/health path, falling back to `Options.HubAddr` and then the
  default. The contract (component 03, §"`config_path` / `addr`") is that
  the two fields are set together; the remaining implementation choice is how an
  omitted pair resolves —
  "use the config's own `addr` by having the bridge report it" (an extra round
  trip) or "assume the documented default and refuse restart otherwise"
  (today's safer choice, kept for v1).
- **Restart deferral policy** (design §6). Version auto-match restart drops live
  browser/controller connections. Decide whether to defer while clients are
  attached, refuse, or restart immediately and let the manager reconnect. The
  keystone leaves this open; component 04 must not pick a default silently.
- **Version identity of the *running* host hub (resolved).** `launch-check`
  reports the on-disk binary and stays the **deploy** decision; the running
  process is identified by `GET /api/health`'s `version` (`cmd/evener-hub/web_api.go`).
  The running-hub probe (`probeHubVersion`) drives the restart **trigger**, and
  the post-restart check (`waitHealthy`) requires the running `version` to equal
  the deployed build — no `started_at`/clock comparison is used (see §5). The
  `backend_git_sha` field is carried by the response and the hub's own
  self-update flow polls it; `waitHealthy` consults it as the snapshot pin (§5),
  and for every other channel the version-equality check stands. The AppWire
  `ServerInfo.Version` is the static `"0.1.0"` constant
  (`cmd/evener-hub/main.go`, wired at `cmd/evener-hub/app_rpc.go`, surfaced at
  `appwire/types.go`)
  and must **not** be used to compare builds.
- **Detach idiom on the host (partly resolved).** Supervised hubs restart
  through their supervisor (`launchctl kickstart -k`, `systemctl restart`); a
  supervisorless **restart** is the guarded verify-then-signal ad hoc restart,
  which relaunches the hub itself (§"Stop/restart mechanics" check 5). An
  operator relaunches an ad hoc hub out of band with `nohup <argv>
  >> <log> 2>&1 </dev/null &`, preserving the recovered log (ops doc
  §"Restarting an ad hoc Hub"), or discards output (`</dev/null >/dev/null
  2>&1`) when no regular-file log was recovered; the manager's cold-bootstrap
  **start** detaches the same way but logs under `<stateRoot>`
  (§"First attach to a stopped host must be able to start the hub").
  A first-class systemd/launchd unit for hosts that have none is still open.
- **Keepalive ownership (settled).** Keepalive is `ssh -o
  ServerAliveInterval`/`ServerAliveCountMax`; `StreamTransport` deliberately does
  not implement `Pinger` (`appwire/stream_transport.go`), so there is no
  application-level ping. A passive receive-inactivity deadline is **not** part
  of the contract: a healthy hub may be silent for long stretches, and silence
  alone must not be read as link-down. Link-down is the ssh child exiting, a
  `Recv` error/`EOF` (`linkMonitor`/`Channel.markLost`), or the ssh-level
  keepalive firing (§"Channel + reconnect").
- **`evener_path` empty semantics (resolved).** Empty does **not** mean "invoke
  the literal word `evener` via `PATH` at exec time". It is resolved **once**
  per host to one canonical absolute `run_path` (§"SSH channel argv"): the
  host's `command -v evener` result when it exists, else the installer's
  default `<home>/.local/bin/evener`. Every remote invocation, deploy/install,
  restart identification, health/identity check, and attach uses that same
  path. The push deploy additionally resolves the symlink target with the
  POSIX-sh `resolvePathScript` (plain `readlink`, no `readlink -f`;
  `sshconn/deploy.go`, §4) so it never writes a literal `evener` file and
  overwrites the real file a `make install` symlink points at. A configured
  `evener_path` requires its directory to exist on the host and fails
  `ErrDeploy` otherwise.
