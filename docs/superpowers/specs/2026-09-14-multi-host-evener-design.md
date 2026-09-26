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
  `["local"]` remap that strips a remote's own nested hosts on the list path
  (component 05, §"Ref translation detail"; shipped — `remapRemoteSourceIDs`
  returns `["local"]` for an empty incoming filter,
  `remote_hub_refs.go:56-67`), this caps
  fan-out at depth 1 and terminates any A→B→A chain regardless of what the
  config can see — the
  caller-identity guard that a later host-list RPC would make config-aware. The
  guard is not thread-fan-out-only: it also covers every **remote dispatch** a
  hub can initiate — the remote-administration proxy, credential push, remote
  force-stop, and host attach (component 07, §"Host-routing origin guard") — so a
  peer hub cannot use the admin surface to make this hub contact a third host.
  The guard is a v1 requirement; the attach-time upstream-list detection
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
- **Host-count cap: withdrawn (Jesse, 2026-09-26).** The 64-source cap
  (`ErrTooManyHosts`) is **not** implemented — not at config load and not in the
  registry add paths. Bounded fan-out is a risk accepted for v1: the cap was the
  mitigation for an over-limit `[[hosts]]` config (component 06 caps the
  manifest's `sources` at 64 including `local`, so more than 63 hosts can fail
  navigation for the whole hub), and the operator's own config is now the only
  bound. Do not implement the cap without a new decision. (Component 03 §Scope
  carries the same record.)
- **Restart identity pin: verify-then-signal accepted; the ad hoc restart path
  is kept (Jesse, 2026-09-26).** The
  guarded verify-then-signal posture stands, supervisor-preferred (restart by
  systemd unit or launchd label where one is identified and safely restartable)
  with the guarded ad hoc path for a supervisorless hub **or where the
  identified launchd label fails the bare-safe gate** (the label is never
  interpolated into the remote shell): a target whose identity cannot be
  verified is refused `ErrRestart` rather than signaled. A fall-through that
  races launchd's respawn cannot double-serve — the address bind and `hub.lock`
  are single-winner and the losing hub exits (`hostlock`). The residual check-then-act window
  is acknowledged, **not** closed, and no host-side restart-identity pin helper
  is planned — a `pidfd` is unreachable through the component's only host
  interface, `ssh <dest> <command>` (the crash-fencing `evener-fence` lease
  wrapper is a fencing helper, not a restart-identity pin). Shipped and restored
  by #2450 reversing #2410: `restartHub` runs the guarded ad hoc path for the
  supervisorless branch (`restartBare` → `waitHealthy` → `clearPendingRestart`);
  the at-signal re-read is not implemented and the unverified-target refusal
  stands. (The [04] tracked-follow-up entry carries the same record.)
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
  Auto-match converges only where a deploy path is configured: a host that
  answered the controller's `launch-check` speaks its protocol, so a
  protocol-compatible host on another build is attached with the difference
  reported rather than refused (component 04, §5).
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
  Harness-as-host targeting is therefore refused, not endorsed: `hubThreadStart`
  refuses a harness naming a registered non-local source (`refuseHarnessNamingHost`,
  `app_threadlifecycle.go:378`) while keeping the fallback for every other
  harness value, and the recipient hub refuses a remote-originated spawn that
  would resolve to another source (`guardRemoteSpawnSource`).
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
- **Unbounded host count (accepted for v1)**: the 64-source cap was withdrawn by
  decision (§2 "Host-count cap: withdrawn"), so nothing rejects a `[[hosts]]`
  list larger than the navigation manifest's 64-source limit. Such a config
  fails navigation for the whole hub until it shrinks, and fan-out cost scales
  with the operator's own host list.
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

## Tracked code follow-ups (rounds 7–22)

This spec series is the design record; these are the code deltas its reviews
surfaced and that still need implementing. Each line names the component and the
exact scope. Entries are marked `landed` as they ship; an unmarked entry is the
original requirement as written, so re-check it against `main` before
implementing — several have landed without their entry being re-marked.

- **[01] stream transport** — `appwire/stream_transport.go`: bound `Close`'s
  admitted-write drain with a `streamCloseDrainTimeout` constant, and make the
  accepted-stream contract explicit (`Close` must interrupt a blocked read **and**
  write); a non-conforming stream must not hang shutdown.
- **[02/05] hub edge bridge marker** — landed except for the last clause:
  `cmd/evener-hub/attach.go` sends `X-Evener-Bridge: 1` and
  `cmd/evener-hub/host_routing_origin.go` + `cmd/evener-hub/web.go` read it
  beside the bearer token, classify the connection role, and stamp `origin` into
  the request context. What remains is to bind the role to a
  **server-verifiable** signal (a distinct bridge credential, or the
  `ssh`-spawned transport's channel identity) so the marker is not merely
  cooperative.
- **[03] host registry — withdrawn (Jesse, 2026-09-26).** This entry required
  the 64-source cap (`ErrTooManyHosts`) to be enforced in
  `New`/`Add`/`AddWithUpstreams`, not only in `LoadConfig`. That requirement is
  **withdrawn, not deferred**: do not implement the cap and do not add the
  sentinel; bounded fan-out is a risk accepted for v1 (§2 "Host-count cap:
  withdrawn"; component 03 §Scope).
- **[03] host config** — `validateHostConfigs` (and the pre-probe path) must
  reject a non-loopback `addr` before any health check or restart; an explicit
  `--config` that is missing/unparseable exits nonzero instead of falling back
  to `DefaultConfig()`.
- **[03] wiring (landed)** — `hubcore.WebConfig` gains `RemoteHostClientIfAttached`,
  `RemoteHostFacts`, and `RemoteHostHandshake`, populated from the `sshconn`
  manager and installed on each `RemoteHubSource`.
- **[04] attached-only accessors (landed, `e166493920`)** — `sshconn.Manager` gains
  `ClientIfAttached`, `HandshakeIfAttached`, and `PreflightIfAttached`: read the
  installed channel under the manager-wide mutex, never dial.
- **[04] run-target resolution** — resolve one canonical **absolute** `run_path`
  per host (`~/` expanded against `Preflight.Home`, a relative configured path
  refused, never the literal word `evener`), threaded through the invocation
  argv, deploy/install target, restart identification, and the health/identity
  check.
- **[04] deploy/restart** — `buildinfo` gains a stamped `ReleaseTag`;
  the installer fallback passes `EVENER_INSTALL_VERSION` and
  `BINDIR`/`EVENER_SHARE_BINDIR` derived from `evener_path` (else `ErrDeploy`);
  `ensureOnce` gains the
  first-attach bootstrap branch (explicit ad hoc argv, `mkdir -p` for the log
  dir, supervisor detection by unit definition); `hubArgvFromCommandLine`
  canonicalizes `argv[0]`; darwin restart uses `gui/<numeric-uid>/<label>`.
  (Corrected round 22: the fallback's `snapshot` arm is **removed** — it is
  admitted only for the immutable, checksum-verified `release` reference, so a
  snapshot controller must use the atomic push path; the expected-`backend_git_sha`
  verification survives for a pushed snapshot build's health probe.)
- **[04] health probe** — `sshconn/version.go` `waitHealthy` must invoke
  `curl -fsS --noproxy '*' http://<loopback(addr)>/api/health` (explicit scheme
  for bracketed IPv6 literals) on the host.
- **[05] recursive ref translation** — `remote_hub_refs.go` must walk the real
  `JobActivityTree` schema (`JobActivitySession.Ref`/`.Entries[]`; each
  `JobActivityEntry`'s `Job.OwnerRef`/`.TranscriptRef` and
  `Delegate.ChildRef`/`.Child` (recursive) / `.Turns[].OwnerRef`/`.TranscriptRef`),
  plus `Thread.Evener.Diagnostics` and the `evener/job/*` /
  `evener/delegate/updated` transcript refs.
- **[05] loop guard (landed)** — `app_rpc.go` records the connection role and threads
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
- **[06] archive/favorite/delete (landed, except the frontend menu-hiding
  clause: the shipped guard refuses the action instead)** — `ArchiveParams`/`FavoriteSetParams`/
  `ProjectDeleteParams` gain `Source`; `app_archive.go`/`app_favorite.go` **accept
  and key a non-local source** by `(source, id)` (they route archive/favorite by
  source and must **not** reject it); only project deletion
  (`evener/project/delete`) rejects a non-local/unknown source server-side; an
  idempotent SQLite migration backfills `source = "local"`; the frontend hides
  **deletion** (not archive/favorite) for non-local rows. (Corrected in round 14:
  the earlier line said a non-local source is rejected server-side and that the
  frontend hides delete *and* archive, which would break remote archive/favorite.)
- **[06] connect + picker** — add the `evener/host/attach` protocol row and
  handler (`registerMiscHandlers`) calling `sshManager.Ensure` with typed-error
  pass-through; give every offline/never-attached host an enabled Connect/Attach
  affordance.
- **[06] harness targeting (landed, `1e4018fa5d`)** — `hubThreadStart`
  (`app_threadlifecycle.go`'s `refuseHarnessNamingHost`) refuses `InvalidParams`
  for a harness value naming a configured/registered non-local source, while
  retaining the `launchSourceID` fallback for all other harness values and
  consulting it only when `Source` is empty.
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
  scheme, skipping `AuthNone`. The `Provider` value is the instance name and is
  passed **only** to the provider-keyed auth methods; `evener/instance/list`
  takes `EmptyParams` (no provider parameter), so the pusher calls it **once**
  with `{}` and joins the returned `InstanceEntry.Name` /
  `AvailableProviders[].ID` against the local store keys.
- **[07] host-routing origin guard (round 14, High)** — enforce the component-05
  loop guard at the one shared host-routing seam (the per-host client accessor or
  a `routeToHost(ctx, hostID, …)` helper) so a **remote-originated** request
  (`origin` non-empty) is refused typed **before any remote dial** for
  `evener/host/request`, `evener/host/pushCredentials`, the non-local
  `evener/thread/forceStop` branch, component 06's `evener/host/attach`, and
  every future remote dispatch; a local-originated request proceeds. Scope:
  `cmd/evener-hub/app_host_admin.go`, `app_host_credentials.go`,
  `app_force_stop.go`, the `evener/host/attach` handler, and the request-context
  `origin` plumbing (`cmd/evener-hub/app_rpc.go`).
- **[01] bounded `Close` (round 14)** — `appwire/stream_transport.go` `Close`
  must bound/interrupt the **underlying `rw.Close()`** as well as the
  admitted-write drain (the underlying close is currently synchronous, so a
  blocking underlying `Close` prevents `streamCloseDrainTimeout` from being
  reached); no step of `Close` may wait unboundedly on a non-conforming stream.
- **[05] pending-escalation ref translation (round 14)** — `remote_hub_refs.go`
  must also rewrite `Thread.Evener.PendingEscalations[].Ref`
  (`SandboxEscalationRequested.Ref`) on every thread snapshot and the top-level
  `Ref` of the `evener/sandbox/escalation/{requested,resolved}` payloads, beside
  the `JobActivityTree`/`EvenerDiagnostics` walk; `ThreadID` stays bare.
- **[05] `JobsListResponse.Data` decoding (round 14; recognition tightened in
  rounds 24–25)** — `evener/jobs/list` defines `JobsListResponse.Data any`
  (`appwire/types.go`), and it stays generic: typing it as
  `appwire.JobActivityTree` fails the stream client's `json.Unmarshal` for a
  legacy flat array, so ref rewriting must first **recognize** an activity-tree
  payload — a JSON object carrying `revision` as a non-negative integer and
  `root` as an object carrying `sessionId` and `ref` as strings (the Go encoder
  emits all of them unconditionally) — and walk only a recognized tree. Every
  payload that fails the test — `{}`, an unknown object, `{"root":{}}`, a legacy
  flat array — is preserved as the value it arrived as. "Unmarshals without
  error" is not recognition: `{}` and unrelated objects decode into a zero-value
  `appwire.JobActivityTree`, so a decode-only test rewrites payloads the source
  does not understand instead of passing them through untouched (and a typed
  recursive walk over a generic map silently rewrites nothing). Tests: empty,
  unknown, `{"root":{}}`, legacy (flat array), minimal tree, and
  forward-compatible tree. Verified through the actual stream client.
- **[06] source-qualified session pins (round 14)** — `hubcore.PinSectionStore`
  (`cmd/evener-hub/internal/hubcore/pin_section.go`, `session_pin.session_id`),
  the `SessionPinAssign`/`SessionPinUnpin` handlers (`app_pin_section.go`), and
  the navigation projection (`navigation_projection.go`
  `PinSectionBySession`/`PinAssignments`) key session pins by `(source, id)`; the
  `SessionRef` resolves through its owning source; an idempotent migration adds a
  `source` column and backfills `source = "local"`.
- **[04] snapshot health identity (round 15)** — `sshconn/version.go`
  `waitHealthy` takes the expected `backend_git_sha` (`buildinfo.GitSHA`) in
  addition to the expected `version`; for a snapshot pin a response whose
  `backend_git_sha` is empty or not equal to the expected SHA is rejected (keep
  polling; `ErrRestart` on exhaustion), so a wrong snapshot cannot pass
  post-restart verification on `version` alone.
- **[01] nonblocking one-shot close state (round 15)** — `appwire/stream_transport.go`
  must replace the blocking `sync.Once.Do` in `doClose()` with a nonblocking
  one-shot close state plus a completion channel: the underlying `rw.Close()`
  runs on its own goroutine, `Close` and `drainWrites()` `select` on the
  completion channel against `streamCloseDrainTimeout`, and write admission is
  gated on the latched close state before taking the serialized-write lock so a
  later `Send` returns `ErrStreamClosed` instead of blocking behind a stranded
  waiter. Extends the round-14 bounded-`Close` item above (moving `rw.Close()` to
  a goroutine under `sync.Once.Do` strands every later caller behind the first
  `Close()`).
- **[05] `HostCapabilities.LaunchResolved` (round 16)** — add the root-keyed field
  `LaunchResolved map[string]appwire.LaunchConfigResolved` to
  `HostCapabilities` (in `appsource`), populated by the per-root
  `evener/launch/resolve` probe and keyed by the root path, so the probe table's
  per-root effective-config call has somewhere to store its result; component 06
  consumes it through `CapabilitySource.HostCapabilities`.
- **[07] `ConfigRevision` exposure (round 16)** — add `ConfigRevision string` to
  `AuthStatusResponse` and `InstanceEntry` (`appwire/types.go`), populated from
  the host's effective credential-configuration revision (the same value the
  host re-resolves under `credentialWrite`), so the controller has a defined
  source for `ApiKeyConditionalSetParams.ExpectedRevision`: it captures the
  value from the read-only `auth/status`/`instance/list` read it already makes
  and echoes it, treating the zero value as "no revision fence" (the source
  fence still applies). Without this field there is nothing to source the
  required value from.
- **[04] cold-bootstrap supervisor match (round 16; corrected round 17)** —
  `sshconn` supervisor detection on the cold-bootstrap path (where no listener
  exists) must accept a systemd unit / launchd plist only when its definition
  **both** launches the resolved `run_path` `hub` invocation **and** its own
  effective address (loopback-normalized) equals the configured address. The
  effective address is the explicit `--addr` when present, else the `addr`
  resolved from the definition's own `--config <path>` (read on the host) — a
  supervisor launched as `evener hub --config …` with no `--addr` must match, or
  the manager starts an unmanaged duplicate. A definition that merely mentions
  `--addr` (e.g. in `Description=`/`Environment=`) or merely contains
  `evener`/`hub` is not a match. Several matches refuse with `ErrRestart` (no
  signal, no relaunch); when none matches it is the supervisorless branch, whose
  restart runs the guarded verify-then-signal ad hoc path (restored 2026-09-26,
  §2 Decisions), and the cold-bootstrap **start** is the only no-supervisor
  launch that does not first signal an identified listener (component 04,
  §"Stop/restart mechanics" check 5);
  **a candidate hub definition whose effective address cannot be
  resolved refuses with `ErrRestart` and starts nothing** rather than risking a
  duplicate, unless trusted explicit supervisor metadata recorded in the host
  entry supplies the match. An inferred substring match may not.
- **[04] guarded compare-and-kill / restart identity pin (round 16; corrected
  round 17; decided 2026-09-26)** — `sshconn/version.go` restart re-reads the
  pid, recovered argv (with `--config`/`--addr` agreeing with the entry's
  configured `config_path`/`addr` after normalization), effective user, and
  listening socket before signaling, refusing `ErrRestart` (no signal, no
  relaunch) on any mismatch or on a field it cannot re-read. The restart prefers the supervisor path wherever a
  supervisor is identified and safely restartable (`systemctl [--user] restart`;
  launchd `kickstart -k`, which pins by label rather than PID), and takes the ad
  hoc verify-then-signal path where no supervisor is identified, or where the
  identified launchd label fails the bare-safe gate. **Decided
  by Jesse, 2026-09-26:** verify-then-signal is the accepted answer, and the
  atomic `pidfd` handle the round-17 correction demanded is **withdrawn** as a
  requirement — no atomic form is reachable through this component's only host
  interface (`ssh <dest> <command>`), and no host-side restart-identity pin
  helper is specified, installed, or invoked (the crash-fencing `evener-fence`
  lease wrapper is a fencing helper, not a restart-identity pin). The residual check-then-act window in the ad hoc path
  remains: the re-read narrows it and does not close it. The refusal rule
  stands, never a fallback to a bare unguarded `kill`: an identity field that
  cannot be re-read refuses `ErrRestart` with no signal, while a supervisor label
  outside the bare-safe set is never interpolated into the remote shell and
  falls through to the guarded ad hoc restart (design §2). Shipped and restored
  (2026-09-26, #2450 reversing #2410): `restartHub` runs the guarded ad hoc path
  for the supervisorless branch (`restartBare` → `waitHealthy` →
  `clearPendingRestart`), which validates at identification time; the at-signal
  re-read is not implemented, and the residual window is the accepted one
  (implementation status, component 04 check 5). Mirrors component-04 acceptance
  criterion 20.
- **[05/06] remote-originated `thread/start` resolution (round 17; landed,
  `1e4018fa5d`)** — at the **receiving** hub, `hubThreadStart`
  (`app_threadlifecycle.go`) and the request-context `origin` plumbing
  (`cmd/evener-hub/app_rpc.go`) must refuse
  `InvalidParams` for a remote-originated (`origin` non-empty) `thread/start`
  whose effective source — a set `ThreadStartParams.Source`, or the legacy
  `launchSourceID(params.Harness)` fallback — is any non-local source, resolving
  only `local`; the controller-side harness refusal cannot see a host configured
  only on the recipient, so without this a preserved harness naming the
  recipient's own host C lets B route the spawn onward to C and bypasses the
  loop guard. Component 05c clearing `Source` on forward is not sufficient by
  itself. Mirrors component-05 acceptance criterion 12 and its §"The receiving
  hub must reject a non-local resolution for a remote-originated `thread/start`".
- **[04] verified-missing-executable install trigger (round 18)** — `sshconn`
  must turn a *verified* missing `run_path` at preflight into a named result
  (`Preflight.ExecutableMissing` / `ErrExecutableMissing`) and route it into
  deploy/install — the installer fallback creates the default
  `<home>/.local/bin/evener` — then **re-run preflight** before version-match
  and attach, so a fresh host bootstraps instead of dead-ending as a retryable
  `ErrSSHStart`. Only the verified not-found may start an install; an
  unreachable host, an auth refusal, an unparseable/empty `launch-check`
  answer, and a launch-contract/protocol refusal must never run the installer.
  Scope: `cmd/evener-hub/internal/sshconn/preflight.go` (classify the not-found,
  surface the result) and `sshconn/manager.go`/`ensureOnce` (the routed branch
  and the re-preflight). Mirrors component-04 acceptance criterion 21.
- **[04] ssh-diagnostic separation for a terminal auth state (round 18)** —
  `RunError.Stderr` (`sshconn/runner.go`) merges ssh's own diagnostics with the
  remote command's stderr, so an auth marker there cannot be attributed to ssh;
  the landed 04a contract therefore keeps every completed command failure
  retryable (`isSSHAuthFailure`/`sshDiagnostic`, `sshconn/preflight.go`;
  `isTerminal`, `sshconn/manager.go`) and classifies `ErrSSHAuth` only for a
  failed `Start`. A genuinely terminal auth state requires capturing ssh's
  diagnostics through a **separate channel** distinct from the remote command's
  stderr (and keeping the status unambiguous), after which `ErrSSHAuth` could be
  made terminal. Scope: `sshconn/runner.go` (`RunError` and the runner's
  diagnostic sink), `preflight.go`, `manager.go`. Until then the retryable
  contract in component 04's §"Error handling" stands.
- **[06] composite-key migration for favorite/archive/session_pin (round 18)** —
  adding and backfilling a `source` column does not fix the local/remote ID
  collision, because the primary keys stay bare: rebuild `favorite` and
  `archive` with `PRIMARY KEY (source, kind, id)` and `session_pin` with
  `PRIMARY KEY (source, session_id)` (create-new/copy-legacy-as-`"local"`/drop/
  rename), and update every statement naming the key — the upsert conflict
  targets, the deletes, the per-section count join, and the readback scans/maps
  (key them `(source, kind, id)` / `(source, session_id)`, not bare). Scope:
  `cmd/evener-hub/internal/hubcore/favorite.go`, `archive.go`,
  `pin_section.go`; the handlers `app_archive.go`, `app_favorite.go`,
  `app_pin_section.go`; and the projection `navigation_projection.go`. Mirrors
  component-06 §"Migration of existing decisions — the uniqueness keys must be
  rebuilt, not just widened" and its session-pin migration.
- **[03] optional hard-loopback `addr` rejection (round 18, not adopted)** — the
  adopted fix for the wildcard finding is the explicit exposure contract
  (component 03, §"`addr` host validation, and the exposure contract it must not
  weaken"), under which `0.0.0.0`/`::` are accepted only as spellings that
  normalize to loopback for the manager's own dial/probe. A deployment that
  wants a *hard* loopback guarantee must instead reject `0.0.0.0`/`::` in
  `validateHostConfigs` **and** refuse to restart or attach a hub actually bound
  wildcard, superseding component 04's restart-identity normalization (check 4)
  and acceptance criterion 13. Scope: `cmd/evener-hub/internal/hostreg`
  validation plus `sshconn/version.go` and `sshconn/preflight.go`.
- **[04] supervisorless restart via verify-then-signal (round 19; widened round
  22; reversed and restored 2026-09-26)** — round 19 read the round-17 atomic-identity pin
  as unimplementable (a `pidfd` must be opened and signaled by a process on the
  host, and this component's only host interface is `ssh <dest> <command>`),
  concluding that a supervisorless hub must refuse `ErrRestart` with no signal
  and that a restart-capable deployment must be supervised. **That conclusion is
  reversed by the 2026-09-26 decision above and the capability is restored:**
  `restartHub` (`sshconn/version.go`) now runs the guarded ad hoc path for the
  supervisorless branch (`restartBare` → `waitHealthy` → `clearPendingRestart`),
  shipped by #2450 reversing #2410; the at-signal re-read (component 04 check 5)
  remains pending and the unverified-target refusal stands. Supervisor
  preference and the accepted residual
  window stand as stated in the [04] item above. The **start** of a stopped hub
  (last-known-state bootstrap, no PID to signal) is unaffected and
  keeps its detached launch. Scope: `sshconn/version.go` (`restartBare` / the
  restart path) and component-04 §"Stop/restart mechanics" + acceptance
  criterion 20. Mirrors component-04 acceptance criterion 20.
- **[04] dedicated executable probe for a missing `run_path` (round 19)** — the
  verified missing-executable result (round 18) must be recognized from a
  dedicated probe with a stable exit-code sentinel (`test -x <run_path>`: `0`
  present, `1` absent; ssh-level `255` is a transport failure) rather than the
  remote shell's `127` / "no such file or directory" text, and requires the
  ssh-diagnostic separation (round 18 item) so the merged diagnostic stream is
  never consulted for the classification. Scope: `sshconn/preflight.go` (the
  probe and `isSSHAuthFailure`/`sshDiagnostic`), `sshconn/runner.go` (the
  distinct diagnostic channel), `sshconn/manager.go` (`isTerminal`). Mirrors
  component-04 criterion 21.
- **[06] shared versioned transactional migration + local canonicalization
  (round 19, extends the round-18 composite-key item)** — `favorite`, `archive`,
  and `session_pin` share `index.db`, so the composite-key rebuild (round 18)
  must be one **centralized, schema-versioned `BEGIN IMMEDIATE` transaction**
  applied before any store serves — a single version record, all three tables
  rebuilt inside the one transaction, concurrent openers serialized on the write
  lock — not three independent `CREATE`/copy/drop/rename paths in separate
  `open` calls. Also define the canonical local-source constant `"local"` and
  normalize every empty/absent/bare source to it in storage, lookups,
  projections, and `SessionRef` resolution, with a behavioral migration test
  that reads a migrated bare local row under the canonical key. Scope:
  `cmd/evener-hub/internal/hubcore/favorite.go`, `archive.go`,
  `pin_section.go` (the versioned migration + the canonical constant); the
  handlers `app_archive.go`, `app_favorite.go`, `app_pin_section.go`; and the
  projection `navigation_projection.go`. Mirrors component-06 §"Migration of
  existing decisions — the uniqueness keys must be rebuilt, not just widened"
  and its canonical-local-source requirement.
- **[04] fail-closed supervisor ambiguity (round 21)** — the cold-bootstrap
  unit-definition match must preserve the shipped `pickSupervisor` refusal:
  **more than one match is `ErrRestart` with no kill and no relaunch**
  ("an ambiguous listing is fatal, not a fallback", `sshconn/version.go`), never
  a fall-through to
  the ad hoc launch, while an identified launchd label that fails the bare-safe
  gate is never interpolated into the remote shell and falls through to the
  guarded ad hoc restart (design §2, restored 2026-09-26). When
  **no** candidate definition matches, a supervisorless *restart* runs the
  guarded verify-then-signal ad hoc path (design §2), and the ad hoc *launch*
  is the cold-bootstrap **start**. Scope:
  `sshconn/version.go` (`pickSupervisor`, `detectSupervisor`,
  `detectSupervisorsFrom`, the restart path's label gate), `sshconn/version_test.go`.
  Mirrors component-04 §"Stop/restart mechanics", its supervisor test case, and
  criterion 19.
- **[05] remote image bytes (round 21)** — a host-qualified controller route
  plus an AppWire image-fetch request on the owning host's client, and a
  rewrite of every image URL the remote hub stamped (`/s/<id>/images/<sha>`,
  `/doc/image?session=<id>&path=…`) in outbound thread translation and
  notification translation through **one shared image visitor** over the
  `Thread`/`Turn`/`ThreadItem` types — both `Images[].URL` and
  `OutputImages[].URL`, on every response and notification carrier, not just
  the thread-snapshot `OutputImages` field — **and the exact host-qualified
  route pair those URLs are rewritten to** (`/s/<host>:<session>/images/<sha>`,
  `/doc/image?session=<host>:<session>&path=<rel>`; the non-`local` route-id
  branch of the existing `/s/` and `/doc/image` handlers), so no
  remote-stamped URL reaches the controller's local handlers (a colliding
  local session id would otherwise be read).
  Scope: `appwire/protocol.go` + `appwire/types.go` (the new hub-scoped method
  and params, regenerated bindings); the host-side handler in
  `cmd/evener-hub/app_rpc.go` (byte resolution mirroring `handleSessionImage`
  and `handleDocImage`); the controller route/handler in `cmd/evener-hub/web.go`,
  `image_serve.go`, `doc_serve.go`; `cmd/evener-hub/internal/appsource/remote_hub_refs.go`
  (`translateOut`, `translateNotification`); the `EnrichThreadFileBackedImages`
  gate in `cmd/evener-hub/app_rpc.go`. Mirrors component-05 §"Image URLs are
  host-scoped and must be rewritten through the controller".
- **[05] remote item-candidate paging (round 21; landed with 05a,
  `4f64b223ba`)** — the spec now *requires*
  `ItemReadCandidateSource` (`ItemCandidatesFromRead`) and
  `ItemCandidateSource` (`ReadItemCandidates`/`ListItemCandidates`) on
  `RemoteHubSource` (controller-minted cursor identity + `RebaseCursor`
  translation of the remote's native cursor), because the hub packer cannot
  emit a continuation cursor without an identity.
  `multi-host-pr05a-remote-hub-source`
  (`remote_hub_source.go`, `remote_hub_source_paging_test.go`) is the shape;
  the read path must not fall back to the legacy packer, which errors with
  `legacy transcript item source cannot page without cursor identity`. Scope:
  `cmd/evener-hub/internal/appsource/remote_hub_source.go` (+ its paging
  tests). Mirrors component-05 §"Remote item paging requires a source-owned
  cursor identity".
- **[06] source-qualified project identity through navigation (round 22, High)**
  — the host-qualified project identity must be **one** string,
  `"<sourceID>:<projectID>"` (the `appwire.Ref` form, `local` canonical), and it
  must reach every surface, not only the projection: the catalog row keys
  (`hubapi.NavigationProjectSummary.Key`, `NavigationProjectPage.Key`), the
  read params (`appwire.NavigationReadParams.ProjectKey`) and the server
  resource key/scope chain (`navigationReadKeyWithFields`, `app_navigation.go`;
  `navigationResourceKey.canonical`, `navigationViewScope`,
  `navigationEntityKey`, `navigationRootContainerKey`,
  `cmd/evener-hub/navigation_cache.go`), the projection lookups
  (`navigation_projection.go` `p.Project`/`ProjectPage`),
  `hubapi.NavigationSessionLocation.ProjectKey`,
  `appwire.NavigationInvalidationTarget.ProjectKey` with the target-key join in
  `navigation_service.go`, the `(source, id)` receipt key
  (`navigationChangeHint.Projects`), and the frontend
  `ResourceKey`/`keyID`/`navigationViewScope`/`targetBase`/`canonicalResourceKey`/
  `selectProjectResource` chain (`stores/navigation/types.ts`,
  `selectors.ts`) plus the mobile `projectKey` route/reveal/readback keys. One
  format/parse pair owns the encoding; `canonicalResourceKey` must normalize a
  bare project key to `local:` exactly as it does a ref. Requires the two-host
  same-key behavior test (distinct rows, reads, view scopes, and invalidation
  targets; a bare key still local). Scope: the files above plus
  `cmd/evener-hub/web_api_tree.go` (`selectNavigationProjects`). Mirrors
  component-06 §"The one source-qualified project identity must be the key on
  every navigation surface" and acceptance criterion 8.
- **[05/06] non-dialing direct remote actions (round 22)** — every
  `RemoteHubSource` call that is not an explicit attach trigger must resolve its
  client through the shipped `sshconn.Manager.ClientIfAttached`
  (`cmd/evener-hub/internal/sshconn/manager.go`) and return
  `appwire.SessionUnavailable("remote hub unavailable: <host>")` when the host
  is not attached, instead of dialing through the `Ensure`-backed
  `RemoteHubClientFunc` (`remote_hub_source.go`) — reads, mutations,
  subscriptions, host-proxy calls, and probes alike. `Ensure` is reached only
  from `evener/host/attach`, from the explicit-host `thread/list` fan-out seam
  (which attaches before calling the source), and from the first-attach
  bootstrap they drive. Scope:
  `cmd/evener-hub/internal/appsource/remote_hub_source.go` (the client resolver
  + its tests), `cmd/evener-hub/app_threadlist.go` (the explicit-host attach
  seam). Mirrors component-05 §"Every other remote call is non-dialing, not just
  the snapshot and the non-explicit list" and component-06 acceptance
  criterion 13.
- **[04] run target must be `evener` (round 22; landed, `84eb525e70`)** —
  `installableEvenerBasename` (`sshconn/version.go`, `multi-host-pr04b-deploy-restart`)
  accepted `evener-dev`, the development/test tooling binary
  (`cmd/evener-dev/bin`) with no `hub` subcommand and no `launch-check`, so a
  host configured with an `evener-dev` run target installed and then failed
  preflight, health, and restart. The required narrowing is in: the acceptance
  is `evener`-only, and an `evener-dev` (or otherwise unshipped) run-target
  basename is refused **terminally** — the distinct `errRunTargetUnservable`
  sentinel `isTerminal` recognises, not the retryable `ErrDeploy` — before any
  install, push, or write. Terminal is
  the right shape because the refusal names an operator configuration defect a
  host cannot recover from: retrying the same misconfigured path can never
  install a hub-servable binary, so a supervisor would re-refuse it forever and
  discard the cause, the same reason the dirty-controller refusal
  (`errControllerDirty`) is terminal. Scope:
  `cmd/evener-hub/internal/sshconn/version.go`,
  `cmd/evener-hub/internal/sshconn/deploy.go` (+ its tests). No stamped,
  hub-capable development artifact is defined by this series. Mirrors
  component-04 §"The installer install path must equal the run target" and
  acceptance criterion 17.
- **[04] installer fallback's reference (round 22; reconsidered and reversed
  2026-09-22)** — this entry previously required the fallback to be admitted
  only for an immutable, checksum-verified-before-unpacking artifact reference
  (`Channel == "release"`), refusing a `snapshot` controller with `ErrDeploy`
  and removing `installerRefFor`'s `snapshot` arm. **That decision was reversed:**
  the shipped rule keeps the arm. `installerRefFor` passes the mutable
  `snapshot` tag as a **best-effort pin**, the install is confirmed afterwards
  by the same on-host identity check (`deployInstaller`'s `probeLaunchCheck`),
  and once the tag has moved past this controller's commit that check refuses
  **terminally** rather than re-fetching the same artifact forever.
  The trade the earlier decision rejected is real and is now stated where the
  rule lives (component-04 §4): for a snapshot controller the installer writes a
  binary whose commit the artifact reference does not prove, and the proof
  arrives after the write, so a moved tag leaves the host holding a snapshot
  build from an unknown, possibly newer commit while the deploy reports failure.
  What makes it acceptable is that the path with a provable identity is now
  reachable from a production hub: the atomic push path cross-compiles and
  stamps the build in-process (`-deploy-binary`/`-build-source`), and the
  terminal refusal names it. The worked-example prose of this entry is kept
  below only as the record of what was considered.
  *Superseded reasoning (kept for the record):* the fallback must be admitted
  only for an **immutable, checksum-verified-before-unpacking** artifact
  reference, i.e. `Channel == "release"` (immutable tag + `install.sh:88-130`'s
  sha256 check against that release's `checksums.txt`); a `snapshot` controller
  refused with `ErrDeploy` (its tag is force-moved and its `checksums.txt`
  re-uploaded with `--clobber`, so the checksum pins nothing about the commit)
  instead of installing first and failing the identity probe afterwards, with
  `installerRefFor` losing its `snapshot` arm and `deployInstaller`'s
  post-install `probeLaunchCheck`/terminal `ErrVersionMismatch` staying as the
  last verification, never as the pin. Mirrors component-04 §"No deploy path may
  replace the installed binary before the artifact's identity is pinned" and
  acceptance criterion 16.
- **[05] `evener/session/image` AppWire method (round 22, extends the round-21
  image item)** — the image proxy needs a fully specified typed contract:
  `MethodEvenerSessionImage = "evener/session/image"` (`ScopeHub`,
  `appwire/protocol.go` + regenerated bindings),
  `SessionImageParams{SessionID, SHA, Path}` with exactly one of `SHA`/`Path`
  set, `SessionImageResponse{MediaType, Size, SHA, Data}`, sha-pattern and
  session-relative-containment validation (`fspaths.ResolveInRoot`), the
  `outputImageMaxBytes` (8 MiB) bound, media type re-derived with
  `supportedOutputImageMedia`, `InvalidParams`/`ResourceNotFound` mapping, and
  no HTTP route. **No `origin` refusal (round 24):** the call arrives over the
  attach bridge, so it is remote-originated by construction — refusing that
  role rejects the one request this path exists to make, while the loop guard
  refuses fan-out from a remote-originated request to a *further* remote source
  and `SessionImageParams` carries no source selector to fan out with, so the
  guard is satisfied without a refusal here. Scope: `appwire/protocol.go`, `appwire/types.go`, the host handler
  (`cmd/evener-hub/app_rpc.go`), the controller route/handler
  (`cmd/evener-hub/web.go`, `image_serve.go`, `doc_serve.go`), and the
  controller-side client call. Mirrors component-05 §"Image URLs are host-scoped
  and must be rewritten through the controller" (its catalog-entry bullet).
