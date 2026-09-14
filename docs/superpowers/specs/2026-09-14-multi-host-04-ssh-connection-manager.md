# Component spec 04 — SSH connection manager, deploy, and version auto-match

Parent: `2026-09-14-multi-host-evener-design.md` (§2, §4 item 4, §5, §6, §7).
Siblings: `2026-09-14-multi-host-01-stream-transport.md`,
`2026-09-14-multi-host-02-attach-bridge.md`,
`2026-09-14-multi-host-03-host-config.md`.
Spikes: `2026-09-14-multi-host-spikes-findings.md` (Spikes A and C).

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
   differ, deploys the matching build and restarts the host hub.

It produces a connected AppWire transport + initialized client per host; it does
**not** map that client onto `appsource.Source` (component 05) or render hosts
(component 06).

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
  re-attach.

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
type Manager struct { /* cfg, registry, runner, clock, logger */ }

func New(reg *hostreg.Registry, opts Options) *Manager

// Ensure returns a connected, initialized channel for host, spawning ssh,
// preflighting/deploying/version-matching as needed. Idempotent while attached.
func (m *Manager) Ensure(ctx context.Context, name string) (*Channel, error)

// Channel is one owned SSH channel + the AppWire client over it.
type Channel struct { /* cmd, stream, client, dest, host */ }

func (c *Channel) Client() *appwire.Client      // initialized AppWire client
func (c *Channel) Transport() appwire.Transport // StreamTransport over ssh stdio
func (c *Channel) Close() error                 // kill ssh, close pipes

// Runner is the process seam. Production is execRunner; tests inject a fake.
type Runner interface {
    // Start spawns a long-lived child; stderr is the caller's diagnostic sink.
    Start(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error)
    // Run executes a one-shot command and returns combined stdout.
    Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error)
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
ssh -o BatchMode=yes -o ConnectTimeout=<n> <dest> <evener_path> hub attach --stdio
```

- `<dest>` is the component-03 `ssh` field (`user@host` or host); if component 03's
  `user` is set it is composed here (`user@host`).
- `<evener_path>` is component 03's `evener_path`, defaulting to `evener` on the
  remote `PATH`.
- `BatchMode=yes` and a connect timeout make the channel strictly
  non-interactive, so a credential prompt fails fast instead of hanging; mirrors
  the spike invocation `spike/client/main.go:51`
  (`ssh -o BatchMode=yes -o ConnectTimeout=10 <host> <remote>`).
- stdin/stdout are the framed AppWire stream; stderr is diagnostics
  (spike `spike/client/main.go:52`, `cmd.Stderr = os.Stderr`).

### Preflight + version contract

The host answers the same `launch-check` contract the local hub already gates on
(`cmd/evener-hub/spawn.go:772-809`):

```
ssh <dest> <evener_path> launch-check --protocol <appwire.ProtocolVersion> --json
```

`launchcheck.RunLaunchCheck` emits
`{"protocol","version","launch_flags",...}`
(`cmd/evener/internal/launchcheck/launchcheck.go:42-54`, dispatch
`cmd/evener/main.go:407-408`). This component parses `protocol` and `version` and
reuses the local gate's rules:

- `protocol` must equal `appwire.ProtocolVersion` (`appwire/types.go:25`); a
  mismatch is refused before any bridge is spawned, exactly as
  `validateEvenerLaunchContract` does (`spawn.go:802-804`).
- `launch_flags` must contain `api-log` (`supportedLaunchFlags`,
  `launchcheck.go:40`; gate `spawn.go:805-807`), because the host hub will spawn
  host daemons with the same `--api-log` floor the controller pins
  (`cmd/evener/serve.go:348`; docs/evener-hub.md:127-141).
- `version` is `buildinfo.Version()` (`launchcheck.go:75`), i.e. the git SHA of
  the `evener` binary on the host (or `"dev"`). The controller's side of the
  comparison is `buildinfo.Version()` in-process (`buildinfo/buildinfo.go:12-21`).

### Channel lifecycle states

`disconnected → preflighting → (deploying → restarting) → attaching → attached →
reconnecting → …`. `Close` is terminal. A dropped link does **not** stop the
remote hub or its daemons (design §2 lifecycle; docs/evener-hub.md:499-502); the
manager transitions to `reconnecting` and re-runs `Ensure` on the same `dest`,
which re-attaches as a client.

## Implementation approach (files/packages, cited seams)

### 1. Process ownership — `runner.go`

- `execRunner.Start` / `Run` build `argv` and call `exec.Command` (the same
  constructor `spawnDaemon` uses, `cmd/evener-hub/spawn.go:381`), set
  `cmd.Stderr`, and wire `StdinPipe`/`StdoutPipe`. It deliberately does **not**
  use `CommandContext`: the channel's lifetime is the attach, not one request's
  `ctx`, the same reasoning `spawnDaemon` records at `spawn.go:378-381`
  ("NOT CommandContext: the spawned daemon must outlive this call's ctx"). `ctx`
  bounds `Start`, not the child; `Channel.Close` (or the reaper) kills the child,
  as the spike's `procStream.Close` does (`spike/client/main.go:26-36`).
- The SSH child is a **client** of the remote hub and binds no port; it must not
  take `hostlock` (`cmd/evener-hub/internal/hostlock/hostlock.go:17-37`).
- Local detach attributes (`SysProcAttr{Setsid:true}`,
  `cmd/evener-hub/spawn_detach_unix.go:12-14`; nil elsewhere,
  `spawn_detach_other.go:9`) apply to *locally spawned* children. The SSH child
  is not detached — it dies with the controller, which is correct: the remote hub
  is the detached process, not the channel. **Remote** detachment (for the hub
  start command) is a host-side idiom, not `SysProcAttr`; see §3.

### 2. Channel + reconnect — `manager.go`

- `Ensure`:
  1. if attached, return the live channel;
  2. `preflight` (§2, below);
  3. version-match (§3);
  4. `Start` the ssh bridge (`ssh ... hub attach --stdio`), wrap the child's
     stdin/stdout in `appwire.NewStreamTransport` (`appwire/stream_transport.go:32`),
     and build an `appwire.Client` over it.
  5. `Initialize` with `ProtocolVersion: appwire.ProtocolVersion` and a
     controller `ClientInfo`; treat `appwire.ProtocolVersionMismatchError`
     (`appwire/client.go:338-345`; raise site `client.go:347-358`) as a
     hard incompatibility, not a transient error.
- Keepalive: `StreamTransport` does not implement `Pinger`
  (`appwire/stream_transport.go:17-21`), so the client keepalive loop skips it.
  Liveness therefore comes from (a) the ssh child/NIC failing, (b) `Recv`
  returning `io.EOF`/error, and (c) an `ssh`-level keepalive. The manager sets
  `ServerAliveInterval`/`ServerAliveCountMax` on the ssh argv and treats child
  exit or a stalled `Recv` as link-down.
- Reconnect: on link-down, transition to `reconnecting`, close the old stream,
  and retry `Ensure` with bounded exponential backoff and jitter. Re-attach is a
  fresh bridge/client against the still-running hub; it never starts a second hub
  (`hostlock`; spike finding both m4 lock files held,
  `2026-09-14-multi-host-spikes-findings.md:50-52`).
- The manager exposes attach/detach events so component 05 can
  add/remove the corresponding source (the 03 spec fixes `Registry.All()` as the
  iteration point).

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
  `envvars/envvars.go:153-154`). Preflight records the resolved state root so the
  controller knows where the host's `auth-token` and `hub.lock` live; it must not
  invent XDG values the host environment does not have.
- **Existing version/protocol.** The `launch-check` call in §"Contract" above.
- **Hub liveness / addr.** Detect whether a hub is already running on the host and
  at which loopback address, so the bridge (component 02) can be pointed at it.
  `hub.lock` lives at `<hub_state_root>/hub.lock` (`cmd/evener-hub/main.go:175`);
  the default addr is `127.0.0.1:9180` (docs/evener-hub.md:96-104, 314-318).
  Component 02 has an open question on address discovery (`02-attach-bridge.md`
  Open questions); this component passes the discovered address to the bridge via
  the bridge's own env/flag, not by guessing.
- **Target support.** Only `linux/amd64` and `darwin/arm64` ship
  (`.goreleaser.yml:24-30`; `install.sh:39-45`). Any other `os-arch` is a
  terminal, named unsupported-host error — do not attempt a deploy.

### 4. Deploy — `deploy.go`

Two paths, chosen per host (open question: which wins when both are viable):

- **Cross-compile + push (primary).** Build the controller's own tree for the
  host target with `CGO_ENABLED=0 GOOS=<o> GOARCH=<a> go build -o <stage>/evener
  ./cmd/evener/` — the same command shape `make build-linux`
  (`make/building.mk:41-42`) and `scripts/ops/build-runtime-pair.sh:24-28` use —
  then push with `scp` (or `ssh <dest> 'cat > <path>'`) and `chmod +x`.
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

Both paths must preserve the original file (`install` replaces atomically;
`scp` to a temp name + `mv` into place) so an interrupted deploy never leaves a
truncated `evener` on the host. Record the binary's source (`git SHA`) so the
version-match can verify the deploy landed.

### 5. Version auto-match + restart — `version.go`

- On attach, compare controller `buildinfo.Version()` (`buildinfo.go:12-21`) with
  the host's `launch-check` `version`. Equal → attach directly.
- Different → the controller is the version authority (design §2):
  1. deploy the matching build for the host target (§4);
  2. restart the host hub;
  3. re-attach.
- Restart mechanics: the host hub is detached and holds `hub.lock`
  (`main.go:175`; `hostlock.go:17-37`), so restart is "stop the running hub,
  release the lock, start the new binary detached." No `hostlock`-breaking or
  second-hub shortcut is allowed. The remote start must detach on the host side
  (e.g. `setsid`/`nohup ... </dev/null >>log 2>&1 &`), because `SysProcAttr` is
  local-only; how a controller cleanly stops the *old* hub and waits for the
  lock to release is an open question (see below).
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
                        ├─ version != controller?
                        │     ├─ deploy target build (cross-compile → scp/chmod)
                        │     └─ restart host hub (stop old, setsid new)
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
- **Protocol mismatch** (`launch-check --protocol` refusal at `spawn.go:802-804`,
  or `appwire.ProtocolVersionMismatchError` at `client.go:347-358`) → terminal
  `ErrProtocolIncompatible`; no deploy is attempted, since protocol and binary
  version are separate gates.
- **Missing `api-log` launch flag** (`spawn.go:805-807`) → terminal
  `ErrLaunchContract`; a too-old host binary cannot be launched, and the fix is
  the version-match deploy — so this is the trigger for auto-match, not a bare
  failure.
- **Unsupported host os/arch** → terminal `ErrUnsupportedHost`; no build exists
  (`install.sh:39-45`).
- **`launch-check` output unparseable** (`launchcheck.go:101-104` JSON; local
  decoder `spawn.go:799-801`) → `ErrPreflightDecode`; treat as incompatible host.
- **Deploy failure** (cross-compile nonzero, `scp` nonzero, checksum failure in
  the installer path `install.sh:124-130`) → `ErrDeploy`; the existing binary on
  the host is left untouched (temp-name + atomic `mv`).
- **Restart failure** → `ErrRestart`; the manager stays `disconnected` and the
  next `Ensure` retries. A hub that fails to start leaves `hub.lock` free, so a
  retry is safe; if the old hub could not be stopped, surface that explicitly
  rather than starting a second hub.
- **Link drop after attach** → `reconnecting`, not an error to the caller; the
  channel's `Client` fails in-flight calls, matching AppWire's existing client
  behavior.
- **stdout discipline.** The bridge owns stdout for frames; the manager must never
  write logs into the child's stdin or read the child's stdout outside
  `StreamTransport` (component 02's hard rule, `02-attach-bridge.md:32-34`).

## Testing

- **Fake runner (default `go test`).** Inject a `Runner` that returns canned
  `launch-check` JSON and an in-memory `io.Pipe` pair for `Start`. This is the
  unit seam; production `execRunner` is the untested-by-default part. The
  injection style mirrors the existing function-var seams
  (`cmd/evener-hub/spawn.go:35-42`) and `launchCheckLoadClient`
  (`launchcheck.go:19-21`).
- **Preflight table tests.** `uname`/`sw_vers`/`HOME`/XDG outputs → resolved
  `GOOS`/`GOARCH`/state root. Include the non-interactive case with no `XDG_*`
  (spike finding) and assert the `~/.config`/`~/.local/state` fallback. PIN the
  os/arch mapping to `install.sh:21-45` with a parity table so the two cannot
  drift.
- **Version-match tests.** Host version == controller → no deploy, attach.
  Host `<` or `>` controller → deploy argv asserted, then restart argv, then
  attach; `--protocol` value asserted to be `appwire.ProtocolVersion`.
- **Launch-contract tests.** Missing `api-log` in `launch_flags` routes to the
  version-match path; a protocol mismatch short-circuits before any `Start`.
- **Channel argv tests.** Assert `ssh -o BatchMode=yes -o ConnectTimeout=…,
  ServerAliveInterval=…, ServerAliveCountMax=… <dest> <evener_path> hub attach
  --stdio`; assert stderr is wired to the diagnostic sink and stdout is not
  touched outside `StreamTransport`.
- **Reconnect test.** Fake link drops (`io.EOF` / child exit) → assert backoff
  schedule, a fresh `Start`, and that no `hub` start was issued (re-attach, not
  a second hub).
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
4. The SSH channel argv is exactly the non-interactive form in "Contract",
   stdin/stdout are the `StreamTransport` and stderr is diagnostics only.
5. A link drop leaves the remote hub and its daemons running and re-attaches as
   a client; `Start` is re-issued but no hub-start command is issued.
6. A host on an unsupported os/arch fails with a named error and no deploy.
7. A protocol mismatch fails with `ErrProtocolIncompatible` before any bridge
   process is started.
8. The live `EVENER_SSH_E2E=1` test reaches `initialize` + `thread/list` over the
   channel (matches Spike C,
   `2026-09-14-multi-host-spikes-findings.md:27-33`).

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
- **How the host hub is stopped for restart.** The controller can `kill` the
  recorded PID, but nothing exposes it over SSH today; the on-disk `hub.lock` is a
  held `flock`, not a PID. Options: read `hub.lock`'s sibling state (none exists),
  run the host binary's own stop path, or add a host-side "stop hub" RPC. This is
  the largest unknown in 04b.
- **Restart deferral policy** (design §6). Version auto-match restart drops live
  browser/controller connections. Decide whether to defer while clients are
  attached, refuse, or restart immediately and let the manager reconnect. The
  keystone leaves this open; component 04 must not pick a default silently.
- **Version identity of the *running* host hub.** `launch-check` reports the
  version of the `evener` binary on the host `PATH`/`evener_path`, which is the
  same binary the hub runs (hub is a subcommand), but nothing proves the running
  hub was started from that exact file. The hub's AppWire `ServerInfo.Version`
  is the static `cmd/evener-hub` `Version` constant `"0.1.0"`
  (`cmd/evener-hub/main.go:42`, wired at `cmd/evener-hub/app_rpc.go:232-235`,
  surfaced at `appwire/types.go:223-229,526-529`), not the git SHA — so it cannot
  be used to compare builds. Confirm whether the running hub must expose its build
  SHA (e.g. via health or a hub RPC) or whether preflight-on-PATH is trusted.
- **Detach idiom on the host.** `setsid` vs `nohup` vs a launchd/systemd unit on
  the host; design says "detaches and persists" but the mechanism differs per host
  OS (macOS already has `scripts/ops/deploy-hub.sh:32-38` for a launchd hub).
- **Keepalive ownership.** Whether to rely on `ssh -o ServerAliveInterval`
  alone or add an AppWire-level ping (which would require `StreamTransport` to
  implement `Pinger`, explicitly deferred in
  `appwire/stream_transport.go:17-21`).
- **`evener_path` empty semantics.** Component 03 leaves it opaque; here it must
  mean "resolve `evener` on the remote `PATH`", and a deploy to a non-empty
  `evener_path` may need the directory to exist (or fail with a clear error).
