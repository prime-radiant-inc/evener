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
  state. A hub can also be a host. **Cycle rejection in v1 does not run from the
  config file alone.** Duplicate names and invalid/reserved names are refused at
  load/add time, but no cycle check runs on that path: a config-loaded host
  enters through `Add`, which is `AddWithUpstreams(entry, nil)`, and `[[hosts]]`
  has no upstream field. Only a caller that supplies an **explicit upstream
  list** — a direct `AddWithUpstreams`/`SetUpstreams` call — is checked for a
  self-edge or back-edge, and that is what can yield `ErrHostCycle` (component
  03, §"Host registry"). The multi-hop case A→B→A is likewise *not*
  detected, because a controller sees only its own `[[hosts]]` table and no
  AppWire method reports another hub's host list; that detection is deferred
  with the host-list RPC (component 03, §Open questions; component 05, §Open
  questions item 4).
  **v1 nevertheless enforces a runtime loop guard, independent of topology
  detection.** Because A→B→A cannot be refused from the config alone, the
  fan-out path itself must be bounded: a request that arrives over a remote
  source (a controller that is attached to this hub as a host) is served **only
  from local state**, and any attempt to route or fan that request out to
  another remote source is refused with a typed error. Together with the
  `["local"]` remap that already strips a remote's own nested hosts on the list
  path (component 05, §"Ref translation detail"), this caps fan-out at depth 1
  and terminates any A→B→A chain regardless of what the config can see — the
  caller-identity guard that a later host-list RPC would make config-aware. The
  guard is a v1 requirement; the attach-time upstream-list detection
  (`AddWithUpstreams`/`SetUpstreams`) stays deferred. The guard's **origin
  signal is an explicit, cooperative bridge marker on the connection** — the
  `evener hub attach --stdio` bridge presents `X-Evener-Bridge: 1` (component
  02, §Contract "Bridge marker"), the hub's edge reads it alongside the
  bearer token and stamps a remote-originated role into the request context; a
  token-bearer without the marker is a local session. The token itself cannot
  carry the role, because the attach bridge, the local TUI, CLI scripts, and
  browser sessions all present the *same* host capability token. It is never
  `InitializeParams.ClientInfo`, which is caller-supplied and spoofable. The
  marker is client-asserted and carries no secret, so it is **not** a security
  boundary: it stops an honest A→B→A cycle under the explicit assumption that
  peer hubs are cooperative, while a hostile peer can omit it to be classified
  `local`. Binding the role to a server-verifiable signal (a distinct bridge
  credential or the `ssh`-spawned transport's identity) is a tracked code
  follow-up. The
  local-only rule is enforced at the typed fan-out seam (component 05, §"Ref
  translation detail"), not by an advisory check in a handler.
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
- **Offline hosts**: shown offline with last-known sessions marked
  **stale/offline** (never `Dormant`, which Component 06 defines as a session
  that has never run and renders as "Not started"; component 06, §"Go changes
  item 3"); actions refused until reconnect.
- **Tenancy**: connect as the configured SSH user; a host's sessions seen by the
  controller are exactly that account's.

## 3. Leverage in the current code

- AppWire is JSON-RPC over WebSocket; the hub dials daemons at `ws://<addr>/rpc`
  with a bearer token (`hubcore/prober.go`, `appsource/local_daemon.go`). The
  protocol is already network-capable; the spike proved it crosses hosts.
- Every thread/turn operation routes through an `appsource.Source` registry
  (`cmd/evener-hub/app_rpc.go`). Production registers exactly one source,
  `"local"`; refs are namespaced by source (`appwire.Ref{SourceID,ThreadID}`).
- The hub is a multi-client AppWire server; its web edge already serves browser
  and TUI. A controller attach is one more client.
- `hostlock` allows one hub per machine (`cmd/evener-hub/internal/hostlock`).
- New sessions already select a source through `launchSourceID(params.Harness)`
  (`cmd/evener-hub/app_threadlifecycle.go`), where `"evener"` maps to `local`
  and any other value is treated as a source ID. **That legacy harness-as-source
  path is not the mechanism for host targeting.** Component 06 has settled on
  an explicit `ThreadStartParams.Source` field as the sole host selector when
  set and requires that a harness value naming a configured host source — or any
  other registered non-local source — be refused with `InvalidParams` rather than
  routed or forwarded, because `launchSourceID` silently retargets a spawn
  (component 06, §"Write contract"). The `launchSourceID` fallback itself is
  **retained** for every other harness value and is consulted only when `Source`
  is empty; retiring it outright would make a non-empty harness like `"claude"`
  with an empty `Source` fall through to the local spawner in silence.
  Harness-as-host targeting is therefore refused, not endorsed.
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
   the in-memory host registry, with config-load rejection (duplicates,
   invalid/reserved names) and a cycle-check seam for an explicit upstream list.
4. **SSH connection manager** — spawn `ssh`, own the channel, keepalive and
   reconnect, preflight (OS/arch, HOME/XDG, existing version/protocol), deploy
   (push matching binary or run installer), and version auto-match on attach.
5. **Remote hub source** (`appsource.Source`) — maps the controller's source
   calls onto the remote hub's **hub-scoped** RPCs over the channel, translates
   `host:<thread>` ↔ the remote hub's `local:<thread>`, registers one source per
   configured host, and performs the capability probe (launch config, models,
   plugins, credential health, host facts).
6. **Fleet view** (`cmd/evener-hub`, frontend) — enumerate sources, live fan-out
   and merge, offline/**stale** state (distinct from `Dormant`; component 06,
   §"Go changes item 3"), and the host picker in the new-session form.
7. **Remote administration** — per-host settings pages that proxy the host's own
   RPCs (providers, launch config, plugins, credentials) and the credential-push
   action.

## 5. Interfaces

- **Host config entry** (`hub.toml`): `{ name, ssh, user?, evener_path?,
  roots[], config_path?, addr? }` — name is the source ID used in refs and
  URLs; `config_path`/`addr` are the host's own `hub.toml` and hub loopback
  address, which the bridge and the manager's restart/health path must agree on
  (component 03, §"`config_path` / `addr`").
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
- **Multi-hop cycle detection (deferred)**: v1 cannot see an upstream hub's host
  list, so A→B→A is not refused. From the config alone only duplicates and
  invalid/reserved names are checked; a self-edge or a supplied upstream
  back-edge is checked only when an explicit upstream list is passed to
  `AddWithUpstreams`/`SetUpstreams` (see §2 "Topology"). Closing this needs a
  new hub-scoped host-list method; until then, treat configuration as acyclic
  *by convention* for multi-hop chains.
  **Until then the runtime loop guard of §2 "Topology" is what keeps v1
  terminating:** a remote-originated request is served from local state only,
  and fan-out to a second remote source is refused, so an A→B→A chain cannot
  recurse even though the config still cannot detect it (component 05,
  §Open questions item 3).
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

## Tracked code follow-ups (rounds 7–13)

This spec series is the design record; these are the code deltas its reviews
surfaced and that still need implementing. Each line names the component and the
exact scope. None is a present fact.

- **[01] stream transport** — `appwire/stream_transport.go`: bound `Close`'s
  admitted-write drain with a `streamCloseDrainTimeout` constant, and make the
  accepted-stream contract explicit (`Close` must interrupt a blocked read **and**
  write); a non-conforming stream must not hang shutdown.
- **[02/05] hub edge bridge marker** — `cmd/evener-hub/attach.go` (send
  `X-Evener-Bridge: 1`), `cmd/evener-hub/web.go` +
  `cmd/evener-hub/internal/hubedge/auth_token.go` (read it beside the bearer
  token, classify the connection role, stamp `origin` into request context), and
  bind the role to a **server-verifiable** signal (a distinct bridge credential,
  or the `ssh`-spawned transport's channel identity) so the marker is not merely
  cooperative.
- **[03] host registry** — enforce the 64-source cap (`ErrTooManyHosts`) in
  `New`/`Add`/`AddWithUpstreams`, not only in `LoadConfig`.
- **[03] host config** — `validateHostConfigs` (and the pre-probe path) must
  reject a non-loopback `addr` before any health check or restart; an explicit
  `--config` that is missing/unparseable exits nonzero instead of falling back
  to `DefaultConfig()`.
- **[03] wiring** — `hubcore.WebConfig` gains `RemoteHostClientIfAttached`,
  `RemoteHostFacts`, and `RemoteHostHandshake`, populated from the `sshconn`
  manager and installed on each `RemoteHubSource`.
- **[04] attached-only accessors** — `sshconn.Manager` gains
  `ClientIfAttached`, `HandshakeIfAttached`, and `PreflightIfAttached`: read the
  installed channel under the manager-wide mutex, never dial.
- **[04] run-target resolution** — resolve one canonical **absolute** `run_path`
  per host (`~/` expanded against `Preflight.Home`, a relative configured path
  refused, never the literal word `evener`), threaded through the invocation
  argv, deploy/install target, restart identification, and the health/identity
  check.
- **[04] deploy/restart** — `buildinfo` gains a stamped `ReleaseTag`;
  the installer fallback passes `EVENER_INSTALL_VERSION` and
  `BINDIR`/`EVENER_SHARE_BINDIR` derived from `evener_path` (else `ErrDeploy`),
  and verifies `backend_git_sha` for `snapshot`; `ensureOnce` gains the
  first-attach bootstrap branch (explicit ad hoc argv, `mkdir -p` for the log
  dir, supervisor detection by unit definition); `hubArgvFromCommandLine`
  canonicalizes `argv[0]`; darwin restart uses `gui/<numeric-uid>/<label>`.
- **[04] health probe** — `sshconn/version.go` `waitHealthy` must invoke
  `curl -fsS --noproxy '*' http://<loopback(addr)>/api/health` (explicit scheme
  for bracketed IPv6 literals) on the host.
- **[05] recursive ref translation** — `remote_hub_refs.go` must walk the real
  `JobActivityTree` schema (`JobActivitySession.Ref`/`.Entries[]`; each
  `JobActivityEntry`'s `Job.OwnerRef`/`.TranscriptRef` and
  `Delegate.ChildRef`/`.Child` (recursive) / `.Turns[].OwnerRef`/`.TranscriptRef`),
  plus `Thread.Evener.Diagnostics` and the `evener/job/*` /
  `evener/delegate/updated` transcript refs.
- **[05] loop guard** — `app_rpc.go` records the connection role and threads
  `origin` into the request context; every fan-out path
  (`hubThreadListWithSourceTimeout`, `app_threadlist.go`) refuses a
  remote-originated request to any source other than `local` (depth 1).
- **[05/06] attached-only list** — the non-explicit empty-`SourceIDs`
  `thread/list` fan-out gates on `Manager.ClientIfAttached`/`Attached` and skips
  an unattached source without calling the `Ensure`-backed resolver.
- **[05] remote force-stop** — `forceStopThread` (`app_force_stop.go`) gains the
  non-local branch that resolves the host, translates the ref, and forwards on
  the owning host's client (not via `evener/host/request`).
- **[06] thread/project identity** — `annotateThreadProjects`
  (`app_threadlist.go`) must skip non-local rows and preserve the remote
  `ProjectID`/`ProjectPath`; `identifier.Project` and the navigation projection
  carry the owning source; `refreshRemoteThreadSnapshot`/`remoteThreadFetch`
  resolve through the attached-only lookup.
- **[06] archive/favorite/delete** — `ArchiveParams`/`FavoriteSetParams`/
  `ProjectDeleteParams` gain `Source`; `app_archive.go`/`app_favorite.go` key by
  `(source, id)`; a non-local/unknown source is rejected server-side; an
  idempotent SQLite migration backfills `source = "local"`; the frontend hides
  delete/archive for non-local rows.
- **[06] connect + picker** — add the `evener/host/attach` protocol row and
  handler (`registerMiscHandlers`) calling `sshManager.Ensure` with typed-error
  pass-through; give every offline/never-attached host an enabled Connect/Attach
  affordance.
- **[06] harness targeting** — `hubThreadStart` (`app_threadlifecycle.go`)
  refuses `InvalidParams` for a harness value naming a configured/registered
  non-local source, while retaining the `launchSourceID` fallback for all other
  harness values and consulting it only when `Source` is empty.
- **[06b] frontend discovery routing** — the spawn form's host-dependent
  discovery calls route through `evener/host/request` with the selected host.
- **[07a] proxy allow-list** — extend the exact `evener/host/request` method set
  with the host-dependent discovery methods (`evener/git/head` included) and
  `evener/auth/apiKey/conditionalSet`, kept in sync with component 06 (or covered
  by a scripted-host parity test).
- **[07] notification catalog** — register `evener/host/notification` in
  `appwire/protocol.go` and regenerate the Go/TS bindings.
- **[07] credential push** — add the host-side `evener/auth/apiKey/conditionalSet`
  (`ApiKeyConditionalSetParams{Provider, Value, ExpectedSource,
  ExpectedRevision}` → `ApiKeyConditionalSetResponse{Action, Reason, Status}`),
  which re-resolves `ActiveSource`/revision under `credentialWrite` and refuses a
  no-longer-writable instance; the pusher calls it instead of the racy
  status-then-`apiKey/set` pair, and classifies `ActiveSource == "none"` by auth
  scheme, skipping `AuthNone`.
