# Component spec 04 — SSH connection manager, deploy, and version auto-match

Parent: `2026-09-14-multi-host-evener-design.md` (§2, §4 item 4, §5, §6, §7).
Siblings: `2026-09-14-multi-host-01-stream-transport.md`,
`2026-09-14-multi-host-02-attach-bridge.md`,
`2026-09-14-multi-host-03-host-config.md`.
Spikes: `2026-09-14-multi-host-spikes-findings.md` (Spikes A and C).

**Citation convention.** Symbols (package, type, method, constant, file) are
authoritative and were verified on the implementation branches
(`multi-host-pr04a-ssh-channel`, `multi-host-pr04b-deploy-restart`,
`multi-host-pr05a..d`, `multi-host-pr06a-fleet-view-go`). Line numbers are not
used for Go or TypeScript sources; where a non-Go line reference survives
(docs, `install.sh`, Makefiles) treat it as a hint from the `multi-host-specs`
working tree, not as pinning — the reviewer's base is `origin/main`.

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
5. on attach, compares the controller's build with the host's and, when they
   differ, deploys the matching build, restarts the host hub, and verifies the
   restarted hub's build identity **by running the health probe on the host**
   (never from the controller's own loopback) before attaching.

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
- Version auto-match on attach: compare controller `buildinfo.Version()` with the
  host's reported `version`; deploy + restart the host hub when they differ, then
  verify the **restarted** hub's build through its `/api/health` identity
  (`version`, `backend_git_sha`, fresh `started_at`), probed on the host, before
  re-attaching.
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

// Channel is one owned SSH channel + the AppWire client over it.
type Channel struct { /* host, facts, stdio, transport, client, drop/lifecycle channels */ }

func (c *Channel) Client() *appwire.Client      // initialized AppWire client
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
    OnEvent             func(Event)   // lifecycle sink, called under the per-host lock (non-reentrant: never call Ensure; connection state only)
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
ssh -o BatchMode=yes -o ConnectTimeout=<n> -o ServerAliveInterval=<n> \
    -o ServerAliveCountMax=<n> -- <dest> <evener_path> hub attach --stdio \
    [--config <config_path>] [--addr <addr>]
```

- **`--` ends ssh's own option parsing.** The destination is emitted as
  `-- <dest>` (shipped: `sshDest` in `sshconn/runner.go`). A registry `ssh`
  value that begins with `-` must be read as a hostname and never as an ssh
  option: `-oProxyCommand=...` as a destination would otherwise run a command on
  the controller. `ssh(1)` stops option parsing at `--`. Every ssh invocation in
  this component (channel, preflight, deploy, restart) uses the same prefix.
- `<dest>` is the component-03 `ssh` field (`user@host` or host); if component 03's
  `user` is set it is composed here (`user@host`).
- `<evener_path>` is component 03's `evener_path`; when it is empty the literal
  word `evener` is used for the remote invocation (`evenerCommand`), which the
  remote shell resolves on `PATH`. The *deploy* target is not that literal word:
  §4 resolves it to the absolute executable the host actually runs.
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
  documented host defaults on both sides
  (`~/.config/evener/hub.toml`-class resolution and `127.0.0.1:9180`), and the
  manager then refuses a restart it cannot match to the configured address (§5)
  rather than restarting whatever holds the default port. **Implementation
  status:** the shipped `hostreg` stores `ConfigPath` and `Addr` as independent
  optional fields and `channelArgv` passes whichever is present; the paired
  validation, and having the restart/health path consume the per-host `addr`
  (today's `Options.HubAddr` is manager-wide), are requirements for the
  implementing PR rather than present facts.
- `BatchMode=yes` and a connect timeout make the channel strictly
  non-interactive, so a credential prompt fails fast instead of hanging; mirrors
  the spike invocation `spike/client/main.go`
  (`ssh -o BatchMode=yes -o ConnectTimeout=10 <host> <remote>`).
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

### Channel lifecycle states

`disconnected → preflighting → (deploying → restarting) → attaching → attached →
reconnecting → …`. `Close` is terminal. A dropped link does **not** stop the
remote hub or its daemons (design §2 lifecycle; docs/evener-hub.md:499-502); the
manager transitions to `reconnecting` and re-runs `Ensure` on the same `dest`,
which re-attaches as a client.

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
  callback takes the same lock and deadlocks. Component 06's navigation poke
  runs inside this callback and must stay non-blocking for the same reason.
- Lifecycle events (`EventState`, `EventAttached`, `EventDetached`,
  `EventFailed`, delivered through `Options.OnEvent`) report *connection state*
  only. They drive `Online()` and navigation invalidation. **No consumer adds or
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
  3. version-match (§5);
  4. `Start` the ssh bridge (`ssh ... hub attach --stdio`), wrap the child's
     stdin/stdout in `appwire.NewStreamTransport` (`appwire/stream_transport.go`),
     and build an `appwire.Client` over it.
  5. `Initialize` with `ProtocolVersion: appwire.ProtocolVersion` and a
     controller `ClientInfo`. `appwire.ProtocolVersionMismatchError`
     (`appwire/client.go`) here means the *running* hub speaks another protocol
     while the on-disk binary preflighted clean: restart the hub once and
     re-attach (§5). Only a mismatch that survives that restart is terminal
     `ErrProtocolIncompatible`.
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
  `--config` first), but the restart/health path still falls back to the
  manager-wide `Options.HubAddr` (default `127.0.0.1:9180`) rather than the
  per-host `addr`. Component 03's `config_path`/`addr` are **coupled** (set both
  or neither; component 03, §"`config_path` / `addr`"); this component passes
  them to the bridge and must use the per-host `addr` for the restart/health
  probe in place of the manager-wide default. Absent the pair, both sides use
  the documented defaults and a restart the manager cannot match to the
  configured address is refused (§5) rather than guessed. **Implementation
  status:** the per-host `addr` on the restart/health path is a requirement for
  the implementing PR, not a present fact.
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
  controller has no identity to deploy. Rule: when the controller's own
  `buildinfo.Version()` is `"dev"`, auto-match is *disabled* — attach only to a
  host reporting exactly `"dev"`, and refuse a mismatch with `ErrDeploy`
  (message: the controller build carries no identity to deploy) instead of
  pushing a binary that cannot be distinguished from what is already there.
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

- **Push target resolution (shipped).** A push must install to the absolute path
  of the executable the host will *run*, or version auto-match deploys the new
  build next to the old one and then attaches to the old one. `deployTarget`
  (`deploy.go`) therefore:
  1. when `evener_path` is set, requires its directory to exist on the host
     (`test -d`) and uses the path;
  2. when `evener_path` is empty, resolves the target with the host's own
     `command -v evener` — never by writing a file literally named `evener` into
     the remote working directory, which is neither the `PATH`-selected
     executable nor a stable location;
  3. resolves a symlink target with `readlink -f` (`resolveDeployTarget`), so the
     atomic `mv` replaces the real file a `make install` symlink points at
     rather than clobbering the link and breaking the installed layout.
  A `command -v` miss or a path that does not resolve to a real file is
  `ErrDeploy` with no write.

Both paths must preserve the original file (`install` replaces atomically;
the push writes a temp name and `mv`s it into place — `pushBinary` in
`deploy.go`) so an interrupted deploy never leaves a truncated `evener` on the
host. Record the binary's source (`git SHA`) so the version-match can verify the
deploy landed.

### 5. Version auto-match + restart — `version.go`

- **On-disk identity (deploy decision).** Compare controller
  `buildinfo.Version()` (`buildinfo.go`) with the host's `launch-check`
  `version`. `launch-check` reports the binary at `evener_path`/`PATH`, which is
  exactly what a deploy replaces: equal → attach directly; different → the
  controller is the version authority (design §2) and deploys the matching
  build (§4), restarts, then re-attaches. The deploy stamps the controller's own
  buildinfo (`-X buildinfo.GitSHA=…`, `-X buildinfo.BuildTime=…`) into the
  pushed binary, so a deployed binary's `launch-check` version equals the
  controller's `buildinfo.Version()`. A `"dev"` controller does not auto-match
  (§4): an unstamped build has no identity to deploy, so a mismatching host is
  refused rather than "matched" by pushing another `dev` binary.
- **Running-hub identity (verification).** The binary on disk is *not* proof of
  what the running process executes: a running hub keeps executing the copy it
  was started from until it is restarted. The authoritative running-build
  signal is the host's own HTTP health endpoint, `GET /api/health`, which
  reports `version` and `backend_git_sha` from `buildinfo` in the live process
  plus `started_at` (`cmd/evener-hub/web_api.go`; `hubapi.HealthResponse` in
  `hubapi/types.go`, fields `Version`, `StartedAt`, `BackendGitSha`).
  Post-restart verification therefore requires **all three**: an answer, a
  `started_at` **later than a host-side timestamp captured before the restart**,
  and `version`/`backend_git_sha` matching the deployed build
  (`buildinfo.Version()` / `buildinfo.GitSHA` in the controller process). A bare
  200 — or any non-empty body — is not sufficient; that is the same check the
  hub's own self-update path relies on (`cmd/evener-hub/web_api.go`).

  **Compare against the host's clock, not the controller's.** `started_at` is
  stamped by the *host* process, so comparing it to the controller's
  `time.Now()` makes the check depend on host↔controller clock skew: a host
  whose clock runs behind rejects a good restart (`started_at` looks earlier than
  a controller-side "restart timestamp") and one running ahead accepts the stale
  old process. The restart path must therefore capture a host-side timestamp
  **after the deploy and immediately before the stop** — one more `Runner.Run` on
  the host, e.g. `date +%s`, or a host-side marker such as the boot time — and
  require `started_at` to be strictly later than that host-side value. Both
  operands then come from the same clock, so no tolerance or skew allowance is
  needed, and none should be invented.

  **The probe must run on the host.** A request to `127.0.0.1:<port>/api/health`
  from the controller's Go process reaches the *controller's* hub, not the
  host's, and the controller has no generic remote-HTTP tool. So the probe is
  one more `Runner.Run` over the same ssh seam as preflight and restart, exactly
  as the shipped `waitHealthy` in `cmd/evener-hub/internal/sshconn/version.go`
  does:

  ```
  ssh <dest> curl -fsS localhost:<port>/api/health
  ```

  and the response is decoded as `hubapi.HealthResponse` and checked against the
  host-side pre-restart timestamp and the deployed identity. Consequences to
  state plainly:

  - the host must have a working HTTP client (`curl`); record it as a
    precondition. With no `curl`, the fallback is the AppWire identity read over
    the attach bridge — `evener/settings/overview`, whose `SettingsHubOverview`
    carries `Version`, `Commit`, and `BuildChannel` — which proves *which build*
    the live process runs but cannot prove `started_at` freshness; a mismatch
    there is still decisive proof the restart did not take. (`appwire.ServerInfo`
    carries only `Name`/`Version`, where `Version` is the static `"0.1.0"` hub
    constant, so it must never be used for this comparison.) There is no
    `evener hub health` subcommand today.
  - the implementation gap to close: `waitHealthy` currently accepts any
    non-empty body; it must parse the response and enforce the three checks
    above before re-attaching.
- **Stop/restart mechanics.** The host hub is detached and holds an advisory
  `flock` on `hub.lock` (`main.go`; `hostlock.go`) — an **`flock`,
  not a PID file**: the lock cannot be read to find or signal the process, and
  breaking it is never allowed. **Identify before killing:** the port alone is
  not identity, so a restart refuses unless all of these hold, and refusal is
  `ErrRestart` naming the mismatch with no kill and no relaunch:

  1. exactly **one** process is listening on the configured address (two or more
     candidates means the address is wrong — e.g. a port collision with an
     unrelated service — not that one of them is ours);
  2. its recovered argv is an evener hub invocation — the configured
     `evener_path`/`evener` (or the unit's `ExecStart`) running the hub, not
     merely "something bound to that port";
  3. the process runs as the configured ssh `user`;
  4. the listening socket matches the configured `addr` (host and port)
     **after the same wildcard→loopback normalization the bridge applies**
     (component 02, `loopbackAddr`): `0.0.0.0:<port>` and `:<port>` are compared
     as `127.0.0.1:<port>`, and `::` as `[::1]:<port>` (`net.JoinHostPort`, so
     the IPv6 literal is bracketed). Requiring the literal
     wildcard spelling would refuse every wildcard-bound hub — the common case,
     and the spike's m4 bound `*:9180` — stranding a host whose binary was
     already replaced. The same normalization applies to the supervisor "owns the
     configured address" check below.

  Supervisor detection obeys the same rules: a launchd label or systemd unit is
  accepted only when **exactly one** candidate names an evener hub *and* the hub
  it names owns the configured address; several candidates, or none, fall
  through to the ad hoc path. "First name that contains `evener` and `hub`
  wins" is not acceptable, because restarting an unrelated unit is exactly the
  failure this rule prevents. (Today's implementation matches names by
  substring in `isEvenerHubName` and takes the first pid from `lsof -ti
  :<port> -sTCP:LISTEN` in `findHubPID`; the spec's contract is stricter.)

  The restart path is then:
  1. **Supervised hub** — restart it the way its supervisor expects:
     `launchctl kickstart -k gui/$(id -u)/<label>` on darwin, or
     `systemctl [--user] restart <unit>` on linux. Detection is from the host's
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
     Every host-derived word in these commands is shell-quoted (the pid, the
     port, the log path), and the recovered command line is **never re-parsed as
     shell code**. `hubArgvFromCommandLine` (`sshconn/version.go`) tokenizes it
     (`tokenizeCommandLine`, honoring single/double quotes and backslash escapes
     and refusing any *unquoted* shell metacharacter), then requires
     `path.Base(argv[0]) == "evener"` and a `hub` word in the remaining argv;
     anything else is refused with `ErrRestart` and **no `kill`**. When the
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
                        ├─ protocol/version != controller?
                        │     ├─ deploy target build (cross-compile → scp/chmod)
                        │     └─ restart host hub (identify → supervisor, else lsof/kill/nohup)
                        │          → Runner.Run on the host: curl /api/health
                        │            → answer + fresh started_at + matching build
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

- **SSH connect/start failure** → `ErrSSHStart` wrapping the ssh stderr tail;
  no channel is returned; retried under backoff (attaching) or surfaced once
  (explicit `Ensure`).
- **Non-interactive auth failure** (`BatchMode` refuses a password prompt) →
  terminal, named error telling the operator to install a key/agent on the host;
  never retried in a loop that would spam ssh.
- **Failure diagnosis keeps ssh's stderr separate from the remote program's
  output.** A failed one-shot `Runner.Run` reports a `*RunError` whose `Stdout`
  and `Stderr` are distinct (`sshconn/runner.go`), and authentication is
  classified **only** from ssh's own stderr (`isAuthFailure` on the started
  child's diagnostic sink; `*RunError.Stderr` on a one-shot call). A remote
  program that prints "Permission denied" on stdout or stderr for its own
  reasons must not be read as an auth refusal, because `ErrSSHAuth` is terminal
  and would stop the reconnect loop for a host that is merely returning an
  application error.
- **Protocol mismatch** (`launch-check --protocol` refusal at `spawn.go`, or
  `appwire.ProtocolVersionMismatchError` at `client.go`) → **the auto-match
  trigger, not a terminal state**. A `launch-check` refusal means the on-disk
  binary is not the controller's build: deploy the matching build, restart,
  re-preflight, and attach (component 05's version-mismatch rule assumes this).
  An `Initialize` mismatch on a channel whose preflight matched means the
  *running* hub is stale while the on-disk binary is right: restart once and
  re-attach. Only a mismatch that survives the restart becomes terminal
  `ErrProtocolIncompatible`. (Implementation status: `ensureOnce` compares
  versions only after a successful preflight, and `preflight` returns
  `ErrProtocolIncompatible` on a protocol refusal, so this routing is the
  corrective contract for the implementing PR, not a description of the shipped
  code.)
- **Missing `api-log` launch flag** (`spawn.go`) → `ErrLaunchContract`. A
  too-old host binary cannot be launched, and the version-match deploy is
  exactly its fix, so this is an auto-match trigger first (deploy, restart,
  re-preflight) and terminal only if it survives that. (Implementation status:
  `isTerminal` lists `ErrLaunchContract` and `preflight` returns it before the
  version comparison, so the shipped code stops instead of auto-matching; the
  contract above is the corrective one.)
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
  the probed port, a different user, or a socket that does not match the
  configured `addr`), or the address/config path is not known well enough to
  match. A collision or a stale process must never be killed or relaunched; the
  operator fixes the entry (`config_path`/`addr`) or the host.
- **Restart failure** → `ErrRestart`; the manager stays `disconnected` and the
  next `Ensure` retries. A hub that fails to start leaves `hub.lock` free, so a
  retry is safe; if the old hub could not be stopped, surface that explicitly
  rather than starting a second hub. A hub that is up but reports the wrong
  `version`/`backend_git_sha`, or a `started_at` older than the restart, is the
  same failure: the restart did not take, and the manager must not attach to the
  old process as if it had. Likewise a health probe that could not run (no
  `curl`, no answer within the bound) is a failed verification, never an assumed
  success.
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
  whose argv is an evener hub as the configured user → restart proceeds;
  (b) two listeners on the port; (c) an argv that is not an evener hub;
  (d) a different user; (e) a socket on another address; (f) a command line with
  an unquoted shell metacharacter or an unterminated quote; (g) an evener hub
  whose `--addr` port disagrees with the probed port → every case (b)–(g)
  returns `ErrRestart` with **no** `kill` and no relaunch argv in the runner log.
  Tokenizer unit tests cover quotes/escapes and each refusal.
  Supervisor cases: exactly one evener-named unit → restarted; two candidates →
  ad hoc path, never "first match".
- **Health-verification tests.** Fake runner returns a body from the old build
  (`version`/`backend_git_sha` of the previous deploy, `started_at` before the
  restart) → not accepted; a fresh `started_at` with matching identity →
  accepted; no answer within the bound, or a missing HTTP client → `ErrRestart`,
  never an assumed success. Assert the probe argv is the on-host
  `curl -fsS localhost:<port>/api/health` over `ssh`, not a controller-side HTTP
  call. The comparison is against the **host-side** pre-restart timestamp the
  restart path captured, not the controller's clock: a body whose `started_at`
  precedes that host-side value is rejected even when the reading controller's
  clock would have accepted it, and a body whose `started_at` follows it is
  accepted even when the controller's clock is skewed backwards.
- **Channel argv tests.** Assert `ssh -o BatchMode=yes -o ConnectTimeout=…,
  ServerAliveInterval=…, ServerAliveCountMax=… -- <dest> <evener_path> hub attach
  --stdio`; assert stderr is wired to the diagnostic sink and stdout is not
  touched outside `StreamTransport`. With a per-host `config_path`/`addr` set,
  assert `--config`/`--addr` appear in that order (each value shell-quoted) and
  that the restart/health path uses the same address. Assert `--` precedes the
  destination so a `dest` beginning with `-` is never read as an ssh option.
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
3. `Ensure` on a host whose `version` differs deploys the matching
   `GOOS`/`GOARCH` build and restarts the host hub before attaching; on a
   matching version it attaches with no deploy (`Runner.Run` argv log asserted).
   The deploy argv stamps the controller's buildinfo (`-ldflags`), and a `"dev"`
   controller does not auto-match. The restart is verified **on the host** by
   `curl …/api/health` run through `Runner.Run` — a fresh `started_at` and a
   `version`/`backend_git_sha` matching the deployed build — and a health answer
   from the *old* process, or no answer at all, is not accepted.
4. The SSH channel argv is exactly the non-interactive form in "Contract",
   with `--` before the destination and every remote word shell-quoted;
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
   port that disagrees with the probed port, another user, or a socket mismatch.
10. Lifecycle events never change registry membership: with `OnEvent` wired to a
    counter, attach/detach transitions change `Attached` and emit events, and
    `appsource.Registry.All()` is untouched.
11. A silent but healthy channel stays `attached` (receive silence alone is not
    link-down, and there is no receive-inactivity deadline). Link-down is
    detected from the ssh child's exit or a `Recv` error/`EOF`, and either
    enters `reconnecting`.
12. The restart verification compares `started_at` against the host-side
    timestamp captured before the restart, not the controller's clock, so a
    skewed host clock neither accepts a stale process nor rejects a fresh one.
13. A wildcard-bound hub (`0.0.0.0:<port>` / `::`) is restart-eligible:
    identification normalizes the configured `addr` to loopback exactly as the
    bridge does, rather than requiring the literal wildcard.
14. A `[[hosts]]` entry that sets exactly one of `config_path`/`addr` fails
    validation; a push deploy with an empty `evener_path` resolves the target
    with `command -v evener` + `readlink -f` and replaces that file atomically.

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
  when one is identified unambiguously, otherwise the ops doc's ad hoc recipe —
  find the listener by port with `lsof`, verify *what it is* (single listener,
  evener hub argv, configured user, matching address), recover argv/log, `kill`
  it, wait for the port to clear, relaunch detached. See §5. `hub.lock` stays a
  pure mutual-exclusion `flock` (`main.go`; `hostlock.go`); it is never read for
  a PID and never broken. Residual risk: the ad hoc path calls `lsof`/`ps` on
  the host, so a host without those tools (or a hub the controller cannot match
  to the configured address) is restart-refused rather than restarted wrong —
  which is the intended trade.
- **Per-host config path and address (corrected contract, coupled fields).**
  `hostreg.Host` carries `ConfigPath` and `Addr` (`hostreg/hostreg.go`), and
  `channelArgv` passes whichever is present, but `Options.HubAddr` is still
  manager-wide and the restart/health path does not yet read the per-host
  `addr`. The contract (component 03, §"`config_path` / `addr`") is that the two
  fields are set together and that the restart/health path uses the per-host
  `addr`; the remaining implementation choice is how an omitted pair resolves —
  "use the config's own `addr` by having the bridge report it" (an extra round
  trip) or "assume the documented default and refuse restart otherwise"
  (today's safer choice, kept for v1).
- **Restart deferral policy** (design §6). Version auto-match restart drops live
  browser/controller connections. Decide whether to defer while clients are
  attached, refuse, or restart immediately and let the manager reconnect. The
  keystone leaves this open; component 04 must not pick a default silently.
- **Version identity of the *running* host hub (resolved).** `launch-check`
  reports the on-disk binary and stays the **deploy** decision; the running
  process is identified by `GET /api/health`'s `version` and `backend_git_sha`
  (`cmd/evener-hub/web_api.go`), which the hub's own self-update flow
  already polls for exactly this reason. The AppWire `ServerInfo.Version` is the
  static `"0.1.0"` constant (`cmd/evener-hub/main.go`, wired at
  `cmd/evener-hub/app_rpc.go`, surfaced at `appwire/types.go`)
  and must **not** be used to compare builds.
- **Detach idiom on the host (partly resolved).** Supervised hubs restart
  through their supervisor (`launchctl kickstart -k`, `systemctl restart`); an
  ad hoc hub relaunches with `nohup <argv> >> <log> 2>&1 </dev/null &`,
  preserving the recovered log (ops doc §"Restarting an ad hoc Hub").
  A first-class systemd/launchd unit for hosts that have none is still open.
- **Keepalive ownership (settled).** Keepalive is `ssh -o
  ServerAliveInterval`/`ServerAliveCountMax`; `StreamTransport` deliberately does
  not implement `Pinger` (`appwire/stream_transport.go`), so there is no
  application-level ping. A passive receive-inactivity deadline is **not** part
  of the contract: a healthy hub may be silent for long stretches, and silence
  alone must not be read as link-down. Link-down is the ssh child exiting, a
  `Recv` error/`EOF` (`linkMonitor`/`Channel.markLost`), or the ssh-level
  keepalive firing (§"Channel + reconnect").
- **`evener_path` empty semantics (resolved).** Empty means "resolve `evener` on
  the remote `PATH`" for the invocation; the push deploy resolves the absolute
  install target with `command -v evener` + `readlink -f` (`sshconn/deploy.go`,
  §4) so it never writes a literal `evener` file. A configured `evener_path`
  requires its directory to exist on the host and fails `ErrDeploy` otherwise.
