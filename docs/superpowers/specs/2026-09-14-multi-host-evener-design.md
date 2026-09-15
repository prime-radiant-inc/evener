# Multi-host evener — high-level design

Status: design, pre-implementation. Spikes validated the transport (see
`2026-09-14-multi-host-spikes-findings.md`). This document is the keystone; the
per-component specs referenced below decompose it into landable PRs.

## 1. Goal

Let any evener hub launch, adopt, and manage sessions on other hosts, so work
can run on a remote machine's files, compute, credentials, and always-on
availability, while one hub presents the fleet. The unit of remoteness is the
**remote hub**: each host runs a full `evener hub`.

## 2. Decisions

These are Jesse's calls, recorded so the specs do not relitigate them.

- **Topology**: any hub can act as a controller for hosts it can see. A static
  per-hub host list in config. No multi-master, no leader election, no shared
  state. A hub can also be a host; cycles are refused at add time.
- **Remote side**: a full `evener hub` per host.
- **Transport**: AppWire JSON-RPC over an SSH channel on stdin/stdout. No HTTP
  port exposed beyond the host's loopback.
- **Lifecycle**: the remote hub runs on demand but **detaches** and persists;
  the controller re-attaches later by bridging stdio to the hub's existing
  loopback `/rpc` with the capability token. Re-attach is a client of the
  running hub, never a second hub (`hostlock` forbids two hubs per machine).
- **Deployment**: push a matching binary over SSH and/or run the installer. The
  controller is the version authority: on attach it auto-matches the host to its
  own build, restarting the host hub; running sessions keep their old binary.
- **Configuration**: host-owned storage. The Hub UI administers remote config by
  proxying the host's own RPCs, plus an explicit "copy credentials to this host"
  action. The controller stores only connection entries.
- **Fleet view**: live fan-out — query each attached host's hub and merge. No
  replicated index.
- **Session targeting**: explicit host picker, local by default.
- **Offline hosts**: shown offline with last-known sessions dormant; actions
  refused until reconnect.
- **Tenancy**: connect as the configured SSH user; a host's sessions seen by the
  controller are exactly that account's.

## 3. Leverage in the current code

- AppWire is JSON-RPC over WebSocket; the hub dials daemons at `ws://<addr>/rpc`
  with a bearer token (`hubcore/prober.go`, `appsource/local_daemon.go`). The
  protocol is already network-capable; the spike proved it crosses hosts.
- Every thread/turn operation routes through an `appsource.Source` registry
  (`cmd/evener-hub/app_rpc.go:24-74`). Production registers exactly one source,
  `"local"`; refs are namespaced by source (`appwire.Ref{SourceID,ThreadID}`).
- The hub is a multi-client AppWire server; its web edge already serves browser
  and TUI. A controller attach is one more client.
- `hostlock` allows one hub per machine (`cmd/evener-hub/internal/hostlock`).
- New sessions already select a source: `hubThreadStart` calls
  `launchSourceID(params.Harness)` (`cmd/evener-hub/app_threadlifecycle.go:53`,
  `:310-319`), where `"evener"` maps to `local` and any other value is treated as
  a source ID. A remote host can therefore be targeted through this existing
  mechanism, or via a new explicit field — component 06 decides which.
- Daemon spawn, run-dir roster discovery, force-stop safety
  (pidfd/`proc_info`, UID, argv, log ownership), and per-host indexing all stay
  as they are and stay host-local.

## 4. Components (each its own spec + PR)

Ordered by dependency; each is independently reviewable and landable.

1. **Stream transport** (`appwire`) — `Transport` over an `io.ReadWriteCloser`,
   newline-delimited JSON framing, frame-size limit. *Spike done.*
2. **Hub attach bridge** (`cmd/evener` or a new subcommand) — a process that runs
   on the host, dials the hub's loopback `/rpc` with the capability token, and
   proxies AppWire between that WebSocket and its own stdin/stdout. stdout
   carries framed AppWire only; all logs go to stderr. *Spike done.*
3. **Host configuration** (`cmd/evener-hub`) — the `[[hosts]]` config schema and
   the in-memory host registry, with cycle rejection.
4. **SSH connection manager** — spawn `ssh`, own the channel, keepalive and
   reconnect, preflight (OS/arch, HOME/XDG, existing version/protocol), deploy
   (push matching binary or run installer), and version auto-match on attach.
5. **Remote hub source** (`appsource.Source`) — maps the controller's source
   calls onto the remote hub's **hub-scoped** RPCs over the channel, translates
   `host:<thread>` ↔ the remote hub's `local:<thread>`, registers one source per
   configured host, and performs the capability probe (launch config, models,
   plugins, credential health, host facts).
6. **Fleet view** (`cmd/evener-hub`, frontend) — enumerate sources, live fan-out
   and merge, offline/dormant state, and the host picker in the new-session form.
7. **Remote administration** — per-host settings pages that proxy the host's own
   RPCs (providers, launch config, plugins, credentials) and the credential-push
   action.

## 5. Interfaces

- **Host config entry** (`hub.toml`): `{ name, ssh, user?, evener_path?, roots[] }`
  — name is the source ID used in refs and URLs.
- **Remote source ID**: the host `name`; refs surface as `name:<sessionID>`.
- **Capability probe** — **not** one round trip: a short sequence of hub-scoped
  RPCs over the already-open channel after attach (`evener/launch/getLayer`,
  `model/list`, `evener/plugin/list`, `evener/auth/list`,
  `evener/instance/list`), plus the component-04 preflight/attach-handshake
  facts for protocol version, hub version, OS/arch, and features. Component 05's
  probe table is authoritative for the exact calls and types.
- **Bridge contract**: stdin/stdout = newline-delimited AppWire `Message` JSON;
  stderr = diagnostics; exit closes the channel.

## 6. Risks and open questions

- **Source-method coverage**: `appsource.Source` was written against
  daemon-scoped calls; some methods may lack a hub-scoped counterpart. Enumerate
  and either compose or add hub RPCs. This is the biggest unknown.
- **Ref translation**: `host:<thread>` ↔ remote `local:<thread>`, including
  sub-thread aliases.
- **Version-match restart** drops live browser/controller connections; decide
  whether to defer while clients are attached.
- **Bridge stdout discipline**: any stray write to stdout corrupts the stream;
  enforce and test.
- **Backpressure and framing limits** over a stream vs WebSocket.
- **Secret handling**: capability token never logged; credential push is
  explicit and per-instance.

## 7. Testing strategy

- Unit: stream transport round-trip and client compatibility (done in spike);
  bridge proxy over an in-memory pipe pair.
- Hub-side: a **scripted remote hub** over a stream transport (no real SSH) to
  exercise `RemoteHubSource`, ref translation, and the capability probe.
- Version/deploy logic: table tests with a fake SSH runner.
- Live: an environment-gated SSH test (`EVENER_SSH_E2E=1` plus an explicit host)
  against a disposable host, never in default `make test`.

## 8. Suggested PR sequence

Land in dependency order, each small and independently reviewable:
stream transport → hub attach bridge → host config schema → SSH connection
manager → remote hub source → fleet view → remote administration.

## Non-goals

Multi-master or election; automatic host discovery; remote *tool execution*
(`agent/execenv`) — a separate concern from where a session runs.
