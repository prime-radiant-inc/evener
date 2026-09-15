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
   differ, deploys the matching build, restarts the host hub, and verifies the
   restarted hub's build identity through `/api/health` before attaching.

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
  verify the **restarted** hub's build through its `/api/health` identity
  (`version`, `backend_git_sha`, fresh `started_at`) before re-attaching.

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
- **No address, token, or config path is on the argv.** The bridge resolves all
  three on the host from the host's own `hub.toml` (component 02, §Scope), so
  the controller never has to discover them to attach. The manager keeps its
  own hub address only for the restart/health path (`Options.HubAddr`, default
  `127.0.0.1:9180`; see §Preflight).
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
- **Hub address (not probed).** v1 does not discover the host hub's address over
  SSH, and does not pass one to the bridge: the bridge reads the host's own
  `hub.toml` (component 02). The manager's own copy lives in `Options.HubAddr`
  (default `127.0.0.1:9180`; docs/evener-hub.md:96-104, 314-318) and is used
  only by the restart/health path (§5) — today the wiring leaves it at the
  default, so a host hub deliberately started on a non-default port attaches
  fine but cannot be auto-restarted until that option is set.
  `hub.lock` lives at `<hub_state_root>/hub.lock` (`cmd/evener-hub/main.go:175`)
  and names no address or PID.
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

- **On-disk identity (deploy decision).** Compare controller
  `buildinfo.Version()` (`buildinfo.go:12-21`) with the host's `launch-check`
  `version`. `launch-check` reports the binary at `evener_path`/`PATH`, which is
  exactly what a deploy replaces: equal → attach directly; different → the
  controller is the version authority (design §2) and deploys the matching
  build (§4), restarts, then re-attaches. The deploy stamps the controller's own
  buildinfo (`-X buildinfo.GitSHA=…`, `-X buildinfo.BuildTime=…`) into the
  pushed binary, so a deployed binary's `launch-check` version equals the
  controller's `buildinfo.Version()`.
- **Running-hub identity (verification).** The binary on disk is *not* proof of
  what the running process executes: a running hub keeps executing the copy it
  was started from until it is restarted. The authoritative running-build
  signal is the host's own HTTP health endpoint, `GET /api/health`, which
  reports `version` and `backend_git_sha` from `buildinfo` in the live process
  plus `started_at` (`cmd/evener-hub/web_api.go:72-94`; `hubapi.HealthResponse`,
  `hubapi/types.go:10-20`). Post-restart verification therefore requires **all
  three**: an answer, a `started_at` later than the restart, and
  `version`/`backend_git_sha` matching the deployed build. A bare 200 is not
  sufficient — that is the same check the hub's own self-update path relies on
  (`cmd/evener-hub/web_api.go:77-79`).
- **Stop/restart mechanics.** The host hub is detached and holds an advisory
  `flock` on `hub.lock` (`main.go:175`; `hostlock.go:17-37`) — an **`flock`,
  not a PID file**: the lock cannot be read to find or signal the process, and
  breaking it is never allowed. The restart path is:
  1. **Supervised hub** — restart it the way its supervisor expects:
     `launchctl kickstart -k gui/$(id -u)/<label>` on darwin, or
     `systemctl [--user] restart <unit>` on linux. Detection is from the host's
     own listings (`launchctl list`, `systemctl list-units`), matching a label
     or unit whose name names an evener hub. The supervisor command's exit
     status is not the check; `/api/health` is (`scripts/ops/deploy-hub.sh`
     makes the same judgement).
  2. **Ad hoc hub** — the documented recipe
     (`docs/evener-hub-remote-operations.md` §"Restarting an ad hoc Hub",
     `:278-399`): find the PID listening on the hub port
     (`lsof -ti :<port> -sTCP:LISTEN`), recover its exact argv
     (`ps -p <pid> -ww -o command`) and log (`lsof -p <pid> -a -d 1,2`), `kill`
     it (SIGTERM; the graceful drain is capped at ~5s), **wait for the port to
     clear**, then relaunch detached with the recovered argv, appending to the
     recovered log (`nohup <argv> >> <log> 2>&1 </dev/null &`). `SysProcAttr` is
     local-only, which is why the host-side detach is a remote shell idiom.
  3. Either path then verifies through `/api/health` (above) before re-attaching.
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
                        ├─ version != controller?
                        │     ├─ deploy target build (cross-compile → scp/chmod)
                        │     └─ restart host hub (supervisor, else lsof/kill/nohup)
                        │          → /api/health: fresh started_at + matching build
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
  rather than starting a second hub. A hub that is up but reports the wrong
  `version`/`backend_git_sha`, or a `started_at` older than the restart, is the
  same failure: the restart did not take, and the manager must not attach to the
  old process as if it had.
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
   The restart is verified through `GET /api/health` — a fresh `started_at` and
   a `version`/`backend_git_sha` matching the deployed build — and a health
   answer from the *old* process is not accepted.
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
- **How the host hub is stopped for restart (resolved).** No new host-side RPC
  is needed in v1: restart through the host's supervisor when one is detected,
  otherwise the ops doc's ad hoc recipe — find the listener by port with `lsof`,
  recover argv/log from the process, `kill` it, wait for the port to clear,
  relaunch detached. See §5. `hub.lock` stays a pure mutual-exclusion `flock`
  (`main.go:175`; `hostlock.go:17-37`); it is never read for a PID and never
  broken. Residual risk: the ad hoc path calls `lsof`/`ps` on the host, so a
  host without those tools (or a hub listening on a port the controller does not
  know) is restart-refused rather than restarted wrong.
- **Restart deferral policy** (design §6). Version auto-match restart drops live
  browser/controller connections. Decide whether to defer while clients are
  attached, refuse, or restart immediately and let the manager reconnect. The
  keystone leaves this open; component 04 must not pick a default silently.
- **Version identity of the *running* host hub (resolved).** `launch-check`
  reports the on-disk binary and stays the **deploy** decision; the running
  process is identified by `GET /api/health`'s `version` and `backend_git_sha`
  (`cmd/evener-hub/web_api.go:72-94`), which the hub's own self-update flow
  already polls for exactly this reason. The AppWire `ServerInfo.Version` is the
  static `"0.1.0"` constant (`cmd/evener-hub/main.go:42`, wired at
  `cmd/evener-hub/app_rpc.go:232-235`, surfaced at `appwire/types.go:530-533`)
  and must **not** be used to compare builds.
- **Detach idiom on the host (partly resolved).** Supervised hubs restart
  through their supervisor (`launchctl kickstart -k`, `systemctl restart`); an
  ad hoc hub relaunches with `nohup <argv> >> <log> 2>&1 </dev/null &`,
  preserving the recovered log (ops doc §"Restarting an ad hoc Hub", `:388-394`).
  A first-class systemd/launchd unit for hosts that have none is still open.
- **Keepalive ownership.** Whether to rely on `ssh -o ServerAliveInterval`
  alone or add an AppWire-level ping (which would require `StreamTransport` to
  implement `Pinger`, explicitly deferred in
  `appwire/stream_transport.go:17-21`).
- **`evener_path` empty semantics.** Component 03 leaves it opaque; here it must
  mean "resolve `evener` on the remote `PATH`", and a deploy to a non-empty
  `evener_path` may need the directory to exist (or fail with a clear error).
