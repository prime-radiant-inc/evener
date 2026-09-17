# Component spec 08 — Host management UI (add-host dialog, Connect, deploy/restart surface)

Status: not started. This spec is the hand-off for the implementing session.

Depends on: PR #1603 (the attached-only seams and the shared origin guard).
The symbols this spec cites that do not yet exist on main —
`sshManager.ChannelIfAttached`, the `RemoteHostClientIfAttached`/
`RemoteHostHandshake` seams, the attach classifier, **and the shared origin
guard** (bridge origin metadata plus the rule that remote-originated,
peer-forwarded requests never reach controller-side dialing or SSH-affecting
handlers) — land with #1603 and MUST be on main before this component starts.
`evener/host/attach` itself and the spawn picker's Connect trigger also ship in
#1603 (round three); this component does not extend that method. 08a extends
the shipped picker/Connect surface with the live host set (persistence +
hot-apply + `list`/`add`/`update`/`remove`); 08b adds the separate
`status`/`plan`/`deploy`/`restart`/`operations` methods; 08c adds the Hosts
settings section. Do not start this component until #1603 is landed.

Related: `2026-09-14-multi-host-evener-design.md` (topology), `…-03-host-config.md`
(host registry), `…-04-ssh-connection-manager.md` (attach + deploy/restart
machinery), `…-06-fleet-view.md` (picker, rail badges, online flags),
`…-07-remote-admin.md` (admin proxy). Components 01–07 are landed on main; this
component adds the user-facing management surface they deliberately left out.

## Purpose

Three user stories, all impossible today:

1. **Add a remote server from the UI.** Hosts are declared only as `[[hosts]]`
   entries in the controller's `hub.toml`, read once at startup
   (`cmd/evener-hub/config.go:62`, `Hosts []HostConfig`). There is no
   config-write path anywhere in the hub, no add/remove/update methods, and no
   hot-apply: today "add a host" means edit a file and restart the controller.
2. **Connect on demand.** Attachment is lazy (component 05/06 ruling: first use
   dials), and #1603's round three adds the `evener/host/attach` method plus a
   spawn-picker trigger so the federation is operable. This component adds the
   management surface: a Hosts section with per-host state and a Connect
   action, and the deploy/restart controls.
3. **Deploy / restart from the UI.** Component 04b's machinery (version
   auto-match, deploy, restart, bootstrap-on-bare-host) exists inside
   `sshconn.Manager` but is only reachable as a side effect of `Ensure`. No
   method triggers it, no surface reports it.

The spawn host picker (06b) is hidden while zero remote hosts are configured, so
a user with no `[[hosts]]` entry sees no federation UI at all. This component is
what makes the federation fully operable without a terminal.

## Scope

- Controller-side hub methods: `evener/host/list`, `evener/host/add`,
  `evener/host/update`, `evener/host/remove`, `evener/host/status`,
  `evener/host/plan`, `evener/host/deploy`, `evener/host/restart`, and
  `evener/host/operations`, plus the local `evener/host/running` probe
  handler and the `evener/host/teardown-retry` repair mutation.
  `evener/host/attach` ships with #1603 and is
  unchanged here — relied on, not re-specified.
- Durable persistence of host entries with a hot-apply path: the running hub
  picks up add/update/remove without a restart (registry, sshconn manager,
  source registry, web config, host-admin controller, navigation manifest).
- A controller-side **operation store** for deploy/restart: durable records,
  idempotency, per-host serialization, crash recovery, progress, and an
  operations read API (polling is the guaranteed read path).
- A new **Hosts** settings section in the hub web UI: host list with live
  state, add/edit dialog, remove-with-confirm, Connect / Deploy / Restart
  actions, deploy confirmation showing what will be installed where.
- AppWire protocol catalog entries for every new method/type, the regenerated
  TypeScript client, and frontend settings-section registration — the methods
  and the section are unreachable without these, so they are in scope of the
  implementing PRs, not an afterthought.

## Non-scope

- The remote-admin proxy (`app_host_admin.go`) and its forwarded allow-list are
  unchanged in *interface*: the methods above are **controller-local** — they
  act on the controller's config and its sshconn manager, not on a remote
  hub's surface. They must NOT be added to `remoteHostAdminMethods` (negative
  test required) — **and the allow-list is not the security boundary**: every
 `evener/host/*` request (all twelve methods, `attach` included) passes
  through the shared origin guard — **landed by #1603 at the dial seam
  (`guardRemoteHostDial`) and the remote-client dispatch seam
  (`guardRemoteDispatch`), and extended by 08a to the common
  request-ingress/router boundary as a pre-admission hook — remote-originated,
  peer-forwarded requests are rejected before admission**, before dedup,
 before token validation, before any handler logic runs. The mutating handlers (`attach`,
 `plan`, `deploy`, `restart`, `add`, `update`, `remove`, `teardown-retry`) are unreachable
 remotely outright; the reads (`list`/`status`/`operations`/`running`) are guarded
  equally because they disclose the controller's topology and operation
  state. (`attach`'s guard routing ships and is test-pinned with #1603; this
 component pins the other eleven.) **The one direction-scoped exception is
 the `evener/host/running` peer probe: a controller-originated request issued
 only through `sshManager.ChannelIfAttached(name)` over a live channel peered
 by the #1603 handshake, admitted on the remote only over that same attached
 session (see `plan`). The exception never admits a browser- or
 forwarded-origin request, the probe is never forwarded onward to a third hub,
 and a missing or unverified handshake stays an unauthenticated-probe refusal
 — so the A→B→A chaining the #1603 guard closes stays closed.** The proxy's
 *host set* does become live
  (see data flow).
- Detach/disconnect of an individual host (no `evener/host/detach`; `Manager.Close`
  remains whole-manager). See open questions.
- Multi-tenancy / auth changes: the methods ride the hub's existing
  admin-mutation admission; no new capability type.
- The native client; hub web only.
- Fixing the hub's `ServerInfo.Version` constant (`"0.1.0"` at
  `cmd/evener-hub/main.go:46`, recorded follow-up from PR #1603 round two):
  version display in this component reads the preflight facts, which already
  report the remote's real build; the constant is a separate owner decision.
- Any change to lazy attachment semantics: the attached-only seams (#1603) stay
  exactly as landed. `evener/host/list`, `status`, and `operations` never dial —
  test-pinned — and `evener/host/attach` remains the only trigger of the
  attach/bootstrap cycle besides an explicit `SourceID` (`plan`'s stale-facts
 refresh is one of the two deliberate non-attach SSH uses — the other is the
 deploy/restart worker's post-operation preflight, the same one-shot preflight
 (see worker lifetime): bounded one-shot preflight
  command executions over the manager's transport that never initialize a
  channel or touch a supervisor; see `plan`).

## Contract / interfaces

### Method surface

All are hub-side handlers registered like every other hub method (the
router-vs-catalog test pins registration), classified as mutations where they
mutate, admission-gated like the hub's other settings mutations, **and
origin-guarded — the shared origin guard #1603 lands at the dial/dispatch
seams, extended by 08a to the common request-ingress/router boundary as a
pre-admission hook, so remote-originated requests are rejected before
admission, before dedup, before token validation, before any handler runs
(see Non-scope)** — and each gets
its AppWire protocol catalog entry + regenerated TypeScript client with the
exact shapes in Protocol types, below.

**Host mutations are serialized by one process-wide lock** (all of
`add`/`update`/`remove` take it for their entire staged commit), so concurrent
read-modify-write on the sidecar cannot lose updates.
**Mutation idempotency:** every `add`/`update`/`remove` accepts an optional
opaque `mutationId` (non-empty, at most 128 bytes, no required structure —
the same rules as client operation IDs). The staged commit persists a durable
mutation receipt — the scoped key, the outcome, the resulting row, and the
resulting generation (row schema in Persistence + hot-apply, below) — in the
same atomic sidecar write as the mutation itself, and a replay carrying a
known key returns the recorded receipt without re-applying: a retried `add`
cannot duplicate, a retried `remove` cannot fail not-found, and a retried
`update` cannot double-apply or double-bump the generation. **Receipt scope
— extending the round-nine receipt with the operation store's scoping:** the
dedup key is (mutationId, host name, mutation kind, host generation at
commit), mirroring the operation store's (host, kind, client operation ID,
generation) scope. A replay matches only a receipt of the same name, kind,
and current generation. A mutationId colliding with a current-generation
receipt of a different name or kind is refused with the typed
`conflicting-mutation-id` error — never a hit, never a re-apply under the
colliding key (the operation store's conflicting-operation-ID rule, applied
to mutations). A key whose only matches are superseded-generation receipts
commits fresh and overwrites. A mutation sent without a
key whose response is lost reconciles read-after-unknown through `list`
before any retry — `add` compares the listed entry hash for the name,
`update` compares the listed row, `remove` treats a missing name or a
`removed: true` row as committed.

- `evener/host/list` (read, never dials, lock-free): every configured host — the full
  effective `HostConfig` fields plus live state: `attached`
  (`sshManager.ChannelIfAttached(name)`), preflight facts when known (installed
  version, OS/arch), last attach error, whether the manager is currently
  mid-`Ensure`, and the **origin marker** (declared in `hub.toml` vs declared
  in the sidecar — the effective source after merge). **Last-known facts,
  their ages, and attach errors come from a manager-owned store keyed by
  host generation (see `status`): the `ChannelIfAttached` lookup reports
  only the current channel, so offline rows would otherwise go blank.**
 **`list` never takes the mutation lock and never prunes durably: expiry is
 in-memory filtering only (expired tombstones are omitted from the response;
 see the expiry mechanism in data flow). Durable pruning of expired
 tombstones happens on the mutation path — every sidecar mutation prunes
 expired entries in its atomic write under the mutation lock — never on the
 read path, so the read-only contract holds and `list` cannot contend with
 `add`/`update`/`remove`.**
  **Removed-but-retained
  hosts (tombstones) appear as `list` rows with `removed: true`** and their
retained-row count — one surface, no separate retained view. Removed rows are
rendered from the tombstone's retained effective `HostConfig` (all seven
fields `HostRow` requires) plus the explicit tombstone values
(`attached: false`, `midEnsure: false`, the removed entry's origin and
generation — see Protocol types); only the facts/error optionals stay absent
per the absent-when-unknown rule.
- `evener/host/add` (mutation): body = one host entry. Validate with the
  component-03 rules (name validation, the `[[hosts]]` field validation at
  `cmd/evener-hub/config.go:177`). **Enforces the component-03 host-count
  limit: at most 63 remote hosts (the `local` entry is the 64th manifest
  source — one host over the cap fails navigation for the entire hub, not
  just the extra host). The staged commit validates the merged post-change
  live set against the cap under the mutation lock before the sidecar
  persist, and the same check runs on the boot load and the registry
  `Add`/`Update` paths, so no sidecar or runtime commit can push the
  registry over it; over-cap is refused with the typed `ErrTooManyHosts`.**
  **Refuses any name already declared in
  `hub.toml` or held as a live sidecar entry — the duplicate refusal applies
  to live entries only: a name present solely as a tombstone is accepted
 (re-add) past the remnant gate — a re-add naming a host with an open
 teardown remnant is refused until `teardown-retry` completes that
 generation's teardown (see the remnant gate in persistence) — and the same
 staged commit purges that tombstone in its atomic
 sidecar write (see data flow)** — no duplicate live entry can exist (the
  boot-time behavior for a hand-created duplicate is a hard startup error;
  see persistence). Persist, hot-apply. Surfaced validation errors are the
  dialog's inline errors.
- `evener/host/update` (mutation): **requires the target name to be held as a
  live sidecar entry**; a `hub.toml`-declared name is refused with the "edit the file"
  explanation; a name absent from both — or present solely as a tombstone —
  is refused as not-found. **Refused
  with the typed busy error while the host's per-host gate is held** by an
  in-flight deploy/restart/`Ensure` (see the operation store). Mutates every
  field except `name` — **names are immutable** (they key source IDs, cached
  rows, manager state, and file entries; renaming is remove + add, documented
  in the UI). Update never inserts a new name; only `add` can. **An update
  that changes what a live supervisor or channel is bound to rebinds them to
  the new entry (or tears them down) as part of its staged commit — commit
  first, then rebind/teardown, gate released last (see the operation store).**
  **Every update advances the host's registry generation and, as part of the
  same staged commit, clears or generation-keys all name-keyed resolved state
  — resolved deploy targets, deployment state, last-known facts entries,
  supervisor bindings, and channel handles — before rebinding, so the next
  operation can never serve the pre-update configuration.**
- `evener/host/remove` (mutation): **live sidecar entries only** (same
  refusal for `hub.toml` names as update; a name present solely as a
  tombstone is refused as not-found, never re-removed). **Refused with the same typed
  busy error while the host's per-host gate is held — remove never waits
  and never cancels**: it proceeds only on a free gate, so its staged
  supervisor/channel teardown cannot race a push or a `waitHealthy`, and a
  refused remove leaves the in-flight operation to finish and record its
  normal terminal state; a successful remove marks that host's operation
  records `host-removed` (readable history, never a dedup match; the mark
  lands with 08b's store — and a crash between the sidecar commit and the
  mark has the mark reconstructed from the tombstone at boot; see the
  operation store). Persist + hot-apply. Removing an attached host
  stops its supervisor and detaches. The removed host's cached snapshot rows
  are **not** silently dropped: the source's last-known-good snapshot is
  retained as an explicit **tombstone** (see data flow), and the UI warns
  when the host has live remote threads (host data on the remote is
  untouched).
- `evener/host/attach` (mutation): **shipped with #1603 round three** (wraps
  the dialing closure, errors classified through the #1603 attach classifier:
  ssh-start/deadline chains → typed `SessionUnavailable`, caller-context
  cancellation stays raw; idempotent). Unchanged by this component.
- `evener/host/status` (read, never dials, mints nothing): one host's `list`
  row plus the **deploy plan inputs**: controller build (`buildinfo`), remote
  installed version (from the last-known preflight facts — **stale facts are
  marked with their age**), the deploy decision inputs, the resolved remote
  target path (as of those facts), whether a restart follows, and any
  plan-time refusal known from the last preflight (`errControllerDirty` is
  terminal; missing curl, unwritable target, unit findings per 04b).
  **Last-known snapshot:** the manager owns a per-host last-known store —
  the latest preflight facts with their capture timestamp plus the latest
  attach error with its timestamp, keyed by the host's registry generation
  and updated on every successful preflight and every attach outcome —
  and `list`/`status` read it read-only, never dialing and never
  constructing facts from the channel. Removal clears the outgoing
  generation's entry only after its replacement lifecycle handles are
  drained (see hot-apply), and re-add starts its new generation with a
  cleared entry alongside the name-keyed cache clearing (see data flow) —
  facts from the removed incarnation can never surface under the new one.
  Successful deploy/restart workers publish their verified post-operation
  refresh here — keyed to the operation's pinned generation, so a concurrent
  mutation cannot misattribute it — before marking `complete` (see worker
  lifetime), so `status` reports the new installed version once the operation
  reads `complete`.
  This is the pure read behind the host row's informational detail —
  the deploy confirmation renders `plan`'s minted response, never `status`
  (see UI).
- `evener/host/plan` (mutation — it mints durable controller state, so it is
  classified and admitted as one): body `{name}`. Builds a **fresh plan from
  the current facts and configuration** and returns the plan plus a
  **controller-minted confirmation token**: single-use, **expiring (token
  TTL: 10 minutes by default, owner-adjustable)**, bound to
  (host name, the host's registry generation at mint time, a hash of the resolved host entry, **a fingerprint (content
  hash) of the controller's `hub.toml` file bytes as they are on disk at
  plan time**, the preflight revision the plan was built from, the resolved
  target path, controller revision, the probed running revision (see the
  running-state rule below), nonce). The `hub.toml` fingerprint is
  what makes a manual edit of that file invalidate outstanding tokens at
  deploy time: the initial full load of `hub.toml` is at startup, but the
  mutation staged-commit path re-reads the file bytes on every mutation
  (validation read, final check, post-rename reconcile — see persistence)
  and `plan`/`deploy` re-hash them at mint and execution time (see
  `deploy`), so a hand edit is visible without a restart.
  **Generation binding:** the token is bound to the durable incarnation — the
  per-name generation of Host generations, below — and `deploy` validates the
  generation like every other binding. Live `remove` drops every outstanding
  token row for the name as part of its staged commit, and re-add mints a new
  generation that starts with zero valid tokens, so a token minted before a
  remove/re-add cycle can never validate after it even if the re-added entry
  is byte-identical.
  **Outstanding-token rule: minting a new token for a host immediately
  supersedes any earlier unconsumed token for that host. Superseded rows do
  not accumulate:** the same atomic store write that persists the new token
  deletes the host's earlier unconsumed token rows — predecessors are already
  invalid by the rule above, so mint replaces rather than accumulates;
  expired-token reaping (lazy + boot, below) covers only tokens that expire
  unconsumed and unsuperseded. `plan`
  **always try-acquires the host's per-host gate first — before checking
  facts and before minting — and fails fast with the typed busy error if
  held**, so a fresh-facts plan cannot mint while a deploy/restart/`Ensure`
  holds the gate. While holding the gate it rechecks attachment and the
  registry generation of the resolved entry (a detach or mutation landing
  between resolution and acquisition is a typed stale-entry refusal, never
  a plan against the superseded entry), and it keeps the gate through token
  persistence (mint + durable write), releasing only before returning. If
  the known preflight facts are stale (older than the **plan freshness
  bound — 5 minutes by default, owner-adjustable**), `plan` **first
  refreshes them: a re-run of the same one-shot SSH preflight the deploy
  path already uses (`Manager.preflight`, `sshconn/preflight.go` — the
  `uname`/env-probe/`id -u`/`launch-check` command sequence executed over
  the manager's SSH transport), exposed as a thin deadline-bounded entry
  point and run while already holding the gate, bounded by the same attempt
  bound that caps the preflight phase of `ensureOnce`** — then builds the
  plan from the refreshed facts, so an online (attached) host can always
  obtain a token without a re-attach/bootstrap cycle. The
  refresh is an SSH command execution, not an attach: it never initializes
  an AppWire channel, never starts or rebinds a supervisor, and never enters
  the deploy/restart decision ladder — `ChannelIfAttached` is not involved
  (an attached channel cannot run SSH shell commands, and the channel's
  captured preflight is a cache of attach-time facts, not a fresh read). If
  the host is not attached or the refresh fails or times out, `plan` returns
  **no token** and names the staleness — the UI must Connect first (the
  attached precondition is deliberate: `plan` must not become a hidden
  prober for never-connected hosts); a token is only ever minted from fresh
  facts. **Running-state verification:** `plan` mints only for an attached
  host, so while still holding the gate it probes the running hub's identity
  and health through the attached channel with the `evener/host/running`
  method (catalog entry + request/response in Protocol types, below) — a
  read-only request issued only through `sshManager.ChannelIfAttached(name)`
  over the live channel (never a dial, never preflight) — and records the
  response's `buildRevision` as `runningVersion` and its `healthy` flag as
  `runningHealthy`, both as `HostPlan` fields, with the running revision
  bound into the confirmation token alongside the other bindings above.
  The remote handler ships in the same 08b as `plan`: every hub serves
  `evener/host/running` locally (its own revision from the same source as
  the `controllerBuild` plan input, plus its own health) and admits it only
  over an attached session peered by the #1603 handshake — the probe
  inherits the channel's authentication and adds no new capability
  (classified as a read, never forwarded onward, never entering the deploy
  ladder) — this is the direction-scoped peer-probe exception to the r7-M4
  ingress guard named in Non-scope: the remote's ingress admits
  `evener/host/running` only over the attached session peered by the #1603
  handshake (r8-M4 provenance — controller-originated over the live channel),
  and rejects browser-origin and forwarded requests to it exactly like every
  other `evener/host/*` request. A missing or unverified handshake is an unauthenticated probe.
  `restartFollows` is computed from the fresh preflight facts confirmed
  against the probed running version: the plan says a restart follows only
  when the running hub is actually outdated. If the probe read fails or is
  unauthenticated — including a remote whose hub predates the handler
  (named distinctly as handler-absent; such remotes take the one-time
  migration path below, since the token-bound deploy path cannot itself
  deliver the first probe-capable binary) — `plan` mints nothing and names
  the failure in the same no-token shape as the stale-facts refusal, with the
  discriminating `reason` field (see Protocol types) — a plan never ships
  without verified running state. **One-time migration for handler-absent
  remotes:** the first contact with a pre-handler remote takes an explicit
  version-gated bootstrap plan — `plan` returns the handler-absent no-token
  shape (never a token), and the UI offers the documented manual upgrade step
  (install a probe-capable binary out-of-band, then re-run `plan`); once the
  remote serves `evener/host/running`, the normal token-bound deploy path
  delivers all later upgrades. There is no token-bound in-band upgrade of a
  remote that cannot yet prove its running build.** The handshake version, ping liveness, and
  preflight on-disk facts feed the decision ladder but never substitute for
  the probe: none of them proves which build the live process runs.
  The token is generated by the controller, never constructed client-side —
  this is the enforcement behind "the user always sees what will be installed
  where". **Token storage and lifecycle:** minted tokens persist as rows in
  the operation-store file itself (same atomic temp+rename+fsync writes as
  operation records, same schema-validation posture at boot — a corrupt or
  schema-invalid store invalidates every outstanding token via the same hard
  startup error). Expired tokens are reaped lazily (on any token
  validate/consume pass for that host) and by the same boot pass that runs
  the interrupted transition and the tombstone-derived `host-removed`
  marking: any token past its TTL at boot is dropped, never revived, and any
  token whose host resolves at boot to removed (tombstoned) is dropped with
  it — the tombstone reconciliation runs before token revival is even
  considered, so no pre-restart token survives for a host that no longer
  exists. After the operator deletes the unvalidatable store file (the
  recovery path for the hard startup error above), the store starts with
  zero outstanding tokens; otherwise only unexpired,
  binding-intact tokens for live hosts remain valid past the restart.
  **Live removal revokes immediately:** `remove`'s staged commit drops every
  outstanding token row for the name alongside the tombstone write (same
  commit, same mutation lock), and the re-added generation starts with none —
  combined with the generation binding above, no pre-remove token can validate
  after a remove/re-add cycle. Token validation additionally requires the
  token's generation to equal the registry's current generation for the name.
- `evener/host/deploy` (mutation): **processing order is fixed.** (1) Dedup
  first: if the client operation ID matches an existing durable operation
  record **of the same host, kind, and current host generation** (a
  `host-removed` record never matches, and neither does a record pinned to
  a superseded generation — see the operation store), return that record —
  no token validation, no consumption (this
  makes the single-use token and idempotent retry coexist: a replay after a
  lost response succeeds without a fresh token). **A client operation ID
  colliding with a current-generation record of a different host or kind,
  or with a current-generation `host-removed` record, is refused with a
  typed conflicting-operation-ID error — never a hit, never a new operation
  under the colliding ID. Only current-generation records participate: a
  colliding ID whose only matches are superseded-generation records —
  including `host-removed` records of a removed incarnation — is not a
  conflict and opens a fresh operation.**
  (2) Otherwise validate the token: **missing,
  mismatched, superseded, or expired → refusal**. (3) Acquire the host's
  per-host gate — **fail fast with the typed busy error if held** — and
  under it **re-resolve the target, re-read the host entry, and re-hash the
  `hub.toml` fingerprint at execution time and reject if any differs from
  the token's bindings — and re-probe the running build/health over the
  attached channel and reject if it differs from the token-bound running
  revision** (a sidecar edit, a manual `hub.toml` edit — caught
  by the fingerprint, the only way a hand edit is visible without a restart
  — a facts refresh, a target change, or a running-state change between plan
  and deploy invalidates the plan; the UI re-plans). This re-read is the gate protocol's
  post-acquisition check (see the operation store). (4) **Consume the token
  by creating the pending operation record — a single atomic durable write
  that names the token's nonce, so consumption and record creation are one
  event**; return its id. **The operation holds its host's gate from record
  creation to terminal state, pinning the validated entry and target for
  the push's lifetime** — `update`/`remove` on that host fail fast
  meanwhile (see the operation store). Never blocks the RPC on the push:
  the RPC returns the record id once the atomic consume-and-create write
  lands, and the push runs on a worker under the controller-lifetime
  context (see the operation store), never the RPC context — a client
  disconnect cannot cancel it.
  Wraps the 04b
  deploy path (`Manager.deploy` / `deployTarget` /
  `resolveDeployOrCreateTarget`) with its guards intact and surfaced
  verbatim: source/revision verification (`verifyBuildSource`,
  `verifyBuildRevision`), terminal dirty-controller refusal, push integrity,
  resolved-target persistence. **Planned-restart execution:** when the
  token-bound plan says a restart follows, the deploy worker performs that
  restart after the push (the same 04b restart path `restart` wraps) and
  verifies it — the post-operation facts refresh below must report the new
  running version — before marking `complete`. A plan that says no restart
  follows requires none. This is the execution half of the round-seven
  post-operation facts refresh: the refresh stays required, and it now has a
  precondition — the planned restart must actually have run — so `deploy`
  can no longer report success with the running hub still outdated. The
  planned restart drops the channel exactly like a standalone `restart` and
  follows the same operation-owned detach/restart/reattach sequence under
  the same gate (see `restart`), so the post-operation probe always runs
  over a worker-owned channel.
- `evener/host/restart` (mutation): same operation model (**(host, kind)**-
  scoped operation-ID dedup first (generation-scoped like `deploy` — see the
  operation store), busy-fail gate acquisition, atomic record
  creation — but no token: restart has no install step, no target, and no
  plan to re-verify; instead restart binds the current `hub.toml`
  content-hash fingerprint (the same fingerprint `plan` binds into
  confirmation tokens — see `plan`) at resolution and re-checks it under the
  gate alongside the post-acquisition entry re-read — a manual file edit
  between resolution and acquisition is a typed stale-entry refusal with a
  retry instruction, never a restart under the superseded file; a mutation
  landing between restart's resolution and its gate acquisition is likewise
  a typed stale-entry refusal, never a restart of the superseded entry),
  wraps the 04b restart path (user vs system unit decision,
  `waitHealthy` proven replacement). Restart drops the attached AppWire
  channel by construction, and no supervisor/`Ensure` reattach can cover the
  worker — both need the gate the operation holds through terminal
  verification — so the reattach is operation-owned: the worker retains its
  host's gate across the drop and re-runs the `evener/host/attach` dialing
  closure for the same generation-pinned entry (no gate release, no gate
  handoff, no supervisor involvement — both stay gated out while the gate
  is held), then runs the channel re-probe the post-operation refresh
  requires over the reattached channel. A restart issued while the host has
  no attached channel runs the same operation-owned attach first under the
  already-held gate and names the attach-first path in the record, so
  verification always has a channel and initially unattached hosts need no
  separate SSH verification path.
- `evener/host/plan`/`deploy`/`restart`/`status` on a name present solely as
  a tombstone — or absent from both files — is the same typed not-found as
  `update`/`remove`: only `list` surfaces tombstone-only names (as
  `removed: true` rows) and only the `add` re-add path accepts them.
- `evener/host/operations` (read): list/detail of controller-side operations
  (see below). Never dials. **Polling this method is the guaranteed read path
  for operation progress**; the host-notification stream, where extended to
  carry controller-originated events, is best-effort only.

### Long-running operations (the operation store)

Deploy and restart take minutes and must not hold an AppWire RPC open. The
existing host-notification stream carries remote configuration notifications
and the existing jobs surface is session-scoped, so neither can carry durable
controller-side operation records; this component defines a
**controller-side durable operation store**:

- Record: operation id (controller-assigned), client operation ID, host, kind
  (deploy/restart), state (`pending`/`running`/`complete`/`failed`/
  `interrupted`), progress entries (timestamped, bounded), terminal result,
  timestamps, and a **`host-removed` mark** (set when the host is removed —
  live at remove time, or reconstructed from the sidecar tombstones at boot
  (see crash recovery); still-readable history that never matches a dedup
  lookup either way). **Client
  operation IDs are opaque — non-empty, at most 128 bytes, no required
  internal structure — and deduplication is keyed by (host, kind, client
  operation ID, host generation at record creation), never the ID alone:**
  records pin the generation of the incarnation they ran against, and a
  dedup lookup matches only records whose generation equals the registry's
  current generation for the name — a re-add starts its new generation with
  a clean dedup slate, so client operation-ID reuse after a remove/re-add
  cycle opens a fresh operation instead of colliding with the removed
  incarnation's history. **Precedence is generation-first — extending the
  round-eight generation-aware dedup: the dedup/conflict scan ignores
  records pinned to a superseded generation entirely, and the
  `host-removed`-record conflict rule applies only to records of the
  current (live) generation.** A same-key replay returns the
  existing record; an ID colliding with a current-generation record of a
  different host or kind — or with a current-generation `host-removed`
  record — is refused with the
  typed conflicting-operation-ID error, so a reused ID can never hand back
  an unrelated operation's progress while the caller's actual operation
  never starts; an ID whose only matches are superseded-generation records
  (including `host-removed` records of a removed incarnation) opens fresh.
- **Durability:** every operation-store write is atomic (temp + rename +
  fsync), and token consumption and record creation are one such write (see
  `deploy`). **A corrupt or schema-invalid store file at boot is a hard
  startup error naming the file** (recovery is deleting it: operation
  history is lost, nothing else is). **File posture (bearer tokens persist in
  the store): the store lives in a private state dir; the store file and its
  temp files carry mode `0600` — replacements preserve the mode, and startup
  validation refuses to load a store readable beyond its owner.**
  **Store-wide serialization:** atomic writes alone do not serialize
  read-modify-write. Every operation-store read-modify-write path — token
  mint, token validate-and-consume, record create/update, and any dedup
  lookup that leads to a write — holds **one store-wide mutex across the
  read and the atomic write** (or runs inside a single-writer transaction
  with the same span), so concurrent `deploy`/`restart`/`plan` on different
  hosts cannot interleave reads and lose records or token consumption.
  Per-host gates serialize same-host operations; the store mutex serializes
  the shared file. **Lock order is fixed: the process-wide mutation lock is
  outermost, the store mutex innermost — a path holding the mutation lock
  (e.g. `remove`'s token-row purge) may acquire the store mutex, but no
  path holding the store mutex ever acquires the mutation lock** (gate
  holders' post-acquisition re-reads are lock-free registry reads), so the
  two locks cannot deadlock.
- **Crash recovery:** at startup, before the store serves any request, every
  record still in `pending`/`running` transitions to `interrupted` (a
  terminal unknown outcome) with a note naming the crash; a retry with the
  same operation ID gets the `interrupted` record back, and a new operation
  ID starts a fresh operation. **The same boot pass, after the sidecar is
  loaded and after the interrupted transition, applies every loaded
  tombstone to the store: each tombstone (removed host name) marks that
  host's records `host-removed`, but only records whose pinned generation
  is at most the tombstoned name's generation high-water mark — i.e.
  records of the removed incarnation, never a newer live one. (For a
  tombstone colliding with a live re-add this reads as "predates the
  current generation"; for a tombstone with no re-add the removed
  incarnation's generation equals the high-water mark, so `<=` is what
  marks exactly those records — and what recovers a crash between
  `remove`'s sidecar commit and its live mark.) Tombstones colliding with a
  live `hub.toml` host (or a live sidecar entry from a re-add) still keep
 their mark for the pre-collision records; **the collision is then treated as
 a new incarnation — the live host is assigned a generation strictly above
 the tombstone high-water mark before the historical marks apply — and the
 merge rule discards
 the colliding tombstone, so the pre-collision `host-removed` records stay at
 or below the high-water mark while the live incarnation's current-generation
 records sit strictly above it, unmarked and out of the never-match rule
 (live records stay unmarked, and operation-ID reuse against the live
 generation opens fresh).** This replaces the
  round-5 mark-before-discard order: mark-before-discard mislabeled the
  live name's history and made op-ID reuse a conflicting-reuse refusal, so
  the mark is now generation-scoped — consistent with the round-7
  generation-bound tokens, which use the same per-name generations.** A remove's
  sidecar commit and its
  operation-store mark are two separate durable writes (different files), so
  a crash between them leaves the mark to be reconstructed here — the
  never-match rule holds via the live mark OR this boot reconciliation.
  Records reach the store only through the
  atomic consume-and-create write, so no crash window can consume a token
  without leaving a recoverable record. No record can stay stuck forever.
- **Per-host operation gate:** at most one deploy/restart per host at a time;
  the gate is shared with `Ensure`-triggered deploys and supervisor activity
  so an auto-deploy and a user deploy cannot interleave (the 04b
  `errControllerDirty` posture applies across all of them). **Acquisition is
  try-acquire — nothing waits on a held gate: a new `deploy`/`restart`, a
  `plan` (which always acquires before checking facts or minting), or an
  `update`/`remove` that finds the gate held fails fast with the typed busy
  error naming the in-flight operation.**
  **Holder classes:** when the gate is held by a deploy/restart operation the
  busy error names that operation (its operation id — open/wait-able); when it
  is held by `plan`'s mint window or by `Ensure`-triggered work — which hold
  no operation-store record — the busy error is the typed transient form
  (`host busy (plan/ensure in progress)`) carrying no operation reference, and
  the UI shows retry-with-backoff with no open/wait affordance.
  **The gate pins the token-bound host entry and resolved target for an
  operation's lifetime: while a deploy/restart holds its host's gate,
  `update` and `remove` of that host fail fast** (and `add` cannot collide —
  the held host exists, so its name is refused as a duplicate), so the "sees
  what will be installed where" binding cannot be broken mid-push.
  **Gate/mutation-lock ordering:** `update`/`remove` check the gate under
  the process-wide mutation lock and fail fast if it is held, and a gate is
  not grantable to a new operation while the mutation lock is held — a
  mutation that observes a free gate owns it through the staged commit, and
  no operation can start under a mutation in flight. **Post-acquisition
  re-read:** an operation resolves and validates against the host entry
  *before* acquiring the gate, and a mutation can legally commit in that
  window (the gate is grantable whenever the mutation lock is free), so
  every gate holder — `deploy`, `restart`, or `Ensure`-triggered work —
  re-reads the registry entry immediately after acquisition and verifies
  its hash/generation still matches what it resolved; a mismatch is a typed
  stale-entry refusal (`deploy`'s token-binding check is this rule with the
  token's bindings as the reference; `restart` refuses; `Ensure` re-resolves
  from the live registry) — never a proceed on the superseded entry.
  **Mutation rebind ordering:** `update`/`remove` rebind or cancel the
  supervisors and channels bound to the superseded entry as part of the
  staged commit — the commit lands first (sidecar persist + runtime swap),
  the rebind/teardown completes, and the gate is released LAST, so no gate
  waiter can acquire a half-rebound host and no supervisor reconnect can
  race a half-swapped entry. This gate is the
  "safe against in-flight `Ensure`" mechanism of the 08a manager surface.
- **Worker lifetime:** the push/`waitHealthy` work runs on an async worker
  under a controller-lifetime context, never the RPC context: the RPC
  context is used only for admission and the atomic consume-and-create
  record write, and the RPC returns once that write lands — a client
  disconnect cannot cancel a persisted pending/running operation. Only
  controller shutdown cancels workers: shutdown cancellation transitions
  the operation to `interrupted` (with a note naming the shutdown) and
  releases its host's gate, so no gate or record is left unresolved.
  **Post-operation facts refresh:** a deploy/restart worker that reaches
  terminal success first performs a verified post-operation preflight (the
  same one-shot SSH preflight as `plan`'s refresh, deadline-bounded) and
  re-probes the running build/health over the attached channel —
  extending the round-seven post-operation facts refresh and the round-eight
  running-state rule: the refresh's preflight facts alone never prove the
  live process caught up, so the probe confirmation is what the worker
  verifies before marking `complete` — and publishes the refreshed facts to
  the generation-scoped last-known store
  (see `status`) — keyed to the operation's pinned generation, so a
  concurrent mutation cannot misattribute them — and only then marks the
  operation `complete`. A restart worker additionally confirms the probe
  reports the post-restart build; a deploy worker whose plan said a restart
  follows confirms the planned restart ran and the probe reports the new
  version. A worker that cannot verify the refresh or the probe records the
  failure verbatim in the operation record instead of marking clean success.
- Progress is readable via `evener/host/operations` (guaranteed). If the
  implementing session extends the host-notification stream with a
  controller-originated best-effort event class, the UI may render from it
  but MUST fall back to polling; polling-only is a fully valid implementation.
- The UI renders progress → terminal state. Never a synchronous RPC.

### Persistence + hot-apply

- **Persistence target:** the controller's `hub.toml` is hand-authored with
  comments; a TOML re-marshal would strip them. The UI writes a managed sidecar
  in the same config dir (e.g. `hub.hosts.json`), loaded after `hub.toml`.
  **Config-path retention:** the hub supports `--config` paths
  (`cmd/evener-hub/main.go:186` — `deps.loadConfig(opts.configPath)`), but
  neither the runtime `Config` nor `WebConfig` retains the selected path, so
  08a carries the canonical config path (absolute, resolved at startup) through
  startup into the web configuration; both the sidecar path (same dir as the
  selected `hub.toml`) and the `hub.toml` fingerprint bytes (read from that
  same path at plan/mint, deploy/validate, and mutation stage/final-check
  time) derive from it — UI mutations against a `--config` hub land beside
  the selected file and invalidate against the same file. **File posture:**
  the config dir's private state holds mode `0600` for the sidecar and the
  operation store — temp files created `0600`, atomic renames preserving the
  mode, and startup validation refusing to load a sidecar/store readable
  beyond its owner (the bearer confirmation tokens persist in the store).
  **Merge rules:** `add`/`update` refuse any name already declared in
  `hub.toml` (the file is authoritative for its own names); `remove` deletes
  only sidecar entries; a `hub.toml`-declared host can never be shadowed or
  UI-removed — its `list` entry says "declared in hub.toml". The sidecar is
  the UI's only writable source. **Every sidecar mutation re-reads the
  current `hub.toml` bytes under the mutation lock and validates the staged
  change against them — reusing the same `hub.toml` content-hash fingerprint
  the plan/deploy path binds into confirmation tokens (see `plan`), or an
  equivalent fingerprint comparison: if the file changed since startup (or
  since the last mutation), a staged name now colliding with a live
  `hub.toml` entry is refused, and a collision already persisted into the
  sidecar by an earlier racing edit is rebased (the sidecar live entry
  dropped, the mutation retried against the re-read file) rather than
  committed as a duplicate. **Final check:** between staging the new sidecar
  bytes and the atomic rename — still under the same mutation lock — the
  commit re-reads the `hub.toml` bytes once more and compares the content-hash
  fingerprint against the validation read; if the file changed in between
  (an external editor landing between validation and rename), the staged
  bytes are discarded and the stage-validate sequence retries against the
  new file (bounded retries, then a typed concurrent-edit refusal) — no
  sidecar commit is *intended* to land against a superseded file read —
  but the re-read + rename is not an atomic compare-and-swap, so the
  guarantee is reconciliation, not prevention: after the rename the commit
  re-reads `hub.toml` once more, and if the file changed across the rename,
  the mutation reconciles forward (a newly colliding live entry drops the
  committed sidecar duplicate in a follow-up atomic write under the same
  lock; other changes are picked up by the fingerprint-bound invalidation
  at the next mutation or token validation) rather than claiming no commit
  against a superseded read ever lands. This extends the round-4/5
  fingerprint mechanism and narrows the round-eight final-check guarantee:
  the same content hash that `plan` binds into confirmation tokens still
  gates the commit, but the commit point can no longer promise the file was
  untouched — only that any race is detected and reconciled.** **A name found in
  both `hub.toml` and the
  sidecar's live entries at load time is a hard startup error naming both
  locations** — the
  refuse rule is enforced at write time AND the boot merge rejects the
  hand-edited collision. Writes are atomic (temp + rename). **The sidecar
  file also carries the tombstone records (see data flow), so a removal's
  entry-delete + tombstone-write is one atomic write and the tombstone set
  cannot diverge from the host set; a tombstone whose name matches a live
  host at boot is discarded — the live host wins. A sidecar that fails
  schema validation or is corrupt at boot is a hard startup error naming
  the file — the same posture as the duplicate-name collision (recovery:
  fix or delete the file; only sidecar state is lost).** **The sidecar file
  also carries the mutation receipts and teardown-remnant records (sibling
  of the idempotency receipts): `mutationReceipts` maps the scoped receipt
  key (mutationId, host name, mutation kind, commit generation) to
 `{outcome, row, generation, committedAt}`; `teardownRemnants` maps the
 server-generated opaque `remnantId` to `{host, kind, seam, pendingTeardown,
 generation, mutationKey, committedAt}`. Both sections
  ride the same atomic temp+rename writes as entries and tombstones (the
  commit, the `teardown-retry` clearance, and the re-add purge below are all
  single writes), and the same hard-startup-error posture on corrupt or
  schema-invalid content. Retention:** receipts and remnants are per-host and
  per-generation — re-add purges that name's superseded-generation receipts
 and — only after that name's open remnants are resolved (see the remnant
 gate below) — its stale
 remnants are dropped in the same atomic write that mints the new generation, so the
  sections stay bounded by the live host set plus at most one superseded
 generation per name. **Remnant gate (extending the round-eight commit
 point): re-add of a name with an open remnant is refused with the typed
 `teardown-unknown-key`-adjacent busy form — the operator first resumes the
 named teardown through `evener/host/teardown-retry` (which runs it to
 completion for that generation), and only then does the re-add mint the new
 generation and purge the cleared remnant — so a new incarnation can never
 start while the old lifecycle still owns supervisors, channels, or fan-outs.
 Retention expiry likewise never purges a tombstone whose name still holds an
 open remnant (see the expiry mechanism in data flow).** Tombstone-purge semantics decide the rest (see L2):
  while a tombstone is retained its name's receipts stay readable for
  post-remove forensics; purging the tombstone drops that name's receipts and
  remnants with it — cross-name replay after the purge commits fresh and
  overwrites by the scoping rule above.
- **Staged commit (order matters):** add/update/remove never mutate live
  state incrementally. Under the process-wide mutation lock: (1) stage the
  complete change — new registry value, the *description* of the manager
  deltas (including the planned supervisor/channel teardown for removals),
  source-registry rows, host-admin-controller host set and its notification
  fan-outs, web-config host view, and the new sidecar bytes; (2) **persist
  the sidecar first** — stashing a durable copy of the prior sidecar bytes
  (including the decision-source validation state — the archive/favorite
  host-set acceptance rewired from the startup `RemoteHosts` snapshot to the
  live set, preferably through a live-registry callback, so newly added hosts
  validate and removed hosts stop validating as part of the same swap),
  (same config dir) before the atomic rename, so the swap is compensable;
  (3) **swap the runtime to the staged set** — rebinding or wiring the
  live handles to the new values, and only then executing the planned
  teardowns (supervisor/channel stops, fan-out cancellations) as the
  post-commit rebind phase (see the mutation rebind ordering in the
  operation store); (4) if the swap itself fails, **compensate
  fully before responding: restore the prior sidecar bytes from the stash
  (atomic rename), then revert the runtime to the previous set**, then
  report exactly which step failed — a typed swap-failure response is sent
  only after both restores. **The commit point is the start of the
  post-commit rebind phase: once the first planned teardown executes, the
  mutation is committed and there is no compensation path back — a failure
  at or after the commit point is reported as a committed-with-
  teardown-failure with the seam named, and recovery is forward (retry the
  teardown / re-apply), never a restore of the prior bytes — through the
  teardown-repair operation, never a blind full-mutation retry (replaying
  the original mutationId stays a no-op receipt return, so the API has a
  repair path that is not the replay): the same atomic sidecar write that
  persists the committed receipt also persists a durable teardown-remnant
  record — the mutation's scoped receipt key plus the named pending teardown
 (handles, seam, generation) **plus a server-generated opaque `remnantId`
 (non-empty, at most 128 bytes, unique per remnant — never derived from the
 mutationId, so an ID-less failure still gets a retry handle and a reused
 mutationId can never collide with an earlier remnant)** — and every
 committed-with-teardown-failure response carries that `remnantId` alongside
 the committed receipt — and the new `evener/host/teardown-retry`
 mutation (params `{remnantId: string}`, response `{host: HostRow}`)
 resumes ONLY that named teardown: it looks up the remnant by the opaque
 `remnantId` (unknown ID or already-cleared remnant → typed
 `teardown-unknown-key` not-found; only the single current-generation
 remnant is eligible — a remnant whose generation no longer equals the
 registry's current generation for the name is not-found, never resumed
 under a re-added incarnation), try-acquires
  the host's per-host gate (held → typed busy, same classes as `restart`),
  re-reads the live entry and refuses stale-entry if the host generation
  moved since the commit, runs the named teardown to completion, clears the
  remnant in the same atomic sidecar write, and returns the live row. Extending the
  round-eight commit-point work with a stable representation: the response
  carries the stable `committed-with-teardown-failure` outcome naming the
 seam **and the `remnantId`**, the mutation's durable receipt (see mutation idempotency, above) is
 persisted as committed with the pending teardown named, and the UI renders
  the committed host row with a teardown-retry affordance — never a generic
  failure affordance, never a blind full-mutation retry. The one
  sentence that changes from rounds 3–7's settled text is the scope of the
  guarantee above: the "failed mutations leave disk+runtime in the old
  state" promise now covers pre-commit failures only** (no teardown runs
  before the commit point, so compensation never has to recreate a canceled
  lifecycle handle or a drained channel — restoring sidecar bytes after a
  teardown cannot rebuild the destroyed handles)
  and a retry behaves as a fresh mutation. **Failure and crash rules
  agree:** every sidecar write in the
  sequence is a temp+rename, so the canonical file always holds either the
  complete old or the complete new bytes; a crash at any point of the
  commit (including mid-compensation) is reconciled at boot — **the disk
  wins** — while a typed failure never diverges from what a restart would
  apply. Within a live process, staging failures change nothing. The stash
  is deleted once the commit reaches either outcome; a stash left by a
  crash is ignored (safe to prune) at boot.
- **Per-host lifecycle handles:** every live host owns cancellable handles —
  its remote-source subscription, its host-admin fan-out (request
  forwarding plus notification fan-out), and its supervisor/channel binding
  — started when the host is added and cancelled/drained as part of the
  post-commit rebind phase on update/remove, before the host's registry
  entry is considered gone. Rollback (step (4) above) reverts to the prior
  handle set with the prior runtime, so a compensated mutation leaves no
  server-lifetime goroutine retrying or emitting for a removed host.
- Hot-apply mechanics touch the seams built once at startup today:
  `hostRegistryEntries(cfg)` → `hostreg.New` → `sshconn.New` → the
  `RemoteHost*` fields in `hubcore.WebConfig` (`main.go:405-488`),
  `newHubSourceRegistry`, **and the host-admin controller (07a), whose
  per-host request forwarding and notification fan-outs are initialized from
  the startup snapshot — they must consume the live host set and start/stop
  fan-outs as part of the staged commit (and its rollback — the stashed
  prior sidecar bytes included; see the staged commit)**. The live
  host-set surface (registry `Add`/`Update`/`Remove` with the existing cycle
  and validation rules, manager add/update/remove safe against in-flight
  `Ensure` and running supervisors) is 08a's core work.
  **Decision-source validation** (the archive/favorite
  `validateDecisionSource` host-set check, which today walks the startup
  `cfg.RemoteHosts` snapshot) consumes the live host set as part of the swap
  — preferably through a live-registry callback — so a newly added host is
  accepted and a removed host is refused immediately after the commit; 08a
  tests pin both directions at that boundary.

### UI

- **Hosts settings section** (new `panes/settings/sections/hosts/*`, plus its
  registration in the settings section map): host rows with state chip (online
  / offline / connecting / removed-retained), installed version + controller
  version, OS/arch, origin marker, actions: Connect, Deploy, Restart, Edit,
  Remove (each with the confirm pattern used elsewhere in settings), and the
  Add button.
- **Add / Edit dialog:** fields exactly the `HostConfig` schema — `name`,
  `ssh`, `user`, `evener_path`, `config_path`, `addr`, `roots` (multi-line;
  `config.go:32-46`) — all seven fields, none invented, none hidden; each with
  its validation message mapped from the backend response. Edit does not
  offer `name` (immutable).
- **Connect state machine:** offline → connecting (in-flight, driven by the
  `attach` response or the host-notification stream) → online (the sources
  manifest's online flag flips; the rail and picker update through the
  existing 06b/07b paths) or failed (typed error shown, retry). The
  spawn-picker Connect trigger shipped with #1603 stays as-is; this section is
  the management view of the same action.
- **Deploy confirmation:** opening the dialog calls `evener/host/plan` and
  renders the confirmation from **`plan`'s response — the controller-minted,
  token-bound plan**: target host, controller revision, resolved remote
  target path, whether a restart follows, the running build, facts freshness
  (from the plan's server-generated `factsCapturedAt`/`factsAgeSec`),
  and any plan-time
  refusal (terminal ones disable the button). The deploy confirmation
  submits exactly the displayed plan: `evener/host/deploy` with that plan's
  token and a client operation ID — never a plan the user has not seen. If
  `deploy` rejects the token as stale (expired, superseded, or
  binding-mismatched), the UI re-plans and re-renders the confirmation from
 the new response before any retry. No-token responses from `plan` branch on
 the `reason` field: `stale-facts` and `unattached` direct the UI to Connect
 first; `probe-failed` on an attached host surfaces the probe failure with
 retry (never a Connect loop); `handler-absent` on an attached host surfaces
 the one-time migration step (never Connect-first). `status` stays the read-only informational surface
  behind the host row — it mints nothing and is never the confirmation's
  source. Progress renders from `operations` polling (with best-effort
  stream events if implemented); terminal failure surfaces the verbatim 04b
  error; a retry after a lost response reuses the same operation ID.
- **Stores:** follow the 07b host-store pattern (`hostInstancesStore` /
  per-host request sequences / connection-generation guards as fixed in
  #1605). No shared mutable module state.

## Implementation approach (files/packages, cited seams)

- `cmd/evener-hub/internal/hostreg` — live registry: `Add` exists; add
  `Update`/`Remove` with the same cycle/validation rules (including the
  63-remote-host cap — over-cap `Add`/`Update` fail `ErrTooManyHosts`),
  safe for concurrent use.
- `cmd/evener-hub/internal/sshconn` — `Manager` gains the live host-set
  surface (add/update/remove entry, each safe against in-flight `Ensure` and
  running supervisors) and thin exported entry points for deploy/restart
  reusing the 04b internals (`deploy`, `ensureDecision`, `waitHealthy`,
  `bootstrapHub`), plus an exported deadline-bounded preflight re-read (the
  existing one-shot SSH command sequence re-run on demand, no channel
  involvement — `plan`'s facts-refresh mechanism; see `plan`);
  `Ensure`/`Attached`/`ChannelIfAttached`/facts unchanged.
- `cmd/evener-hub/config.go` — sidecar load/merge (hard-error duplicate rule)
  + atomic write.
- `cmd/evener-hub/app_host_manage.go` (new) — the handlers, router
  registration, mutation classification (`plan` is a mutation — it mints
  durable state; `list`/`status`/`operations` are reads), **guarded by the
  shared origin guard — landed by #1603 at the dial + dispatch seams,
  extended by 08a to the common request-ingress/router boundary as a
  pre-admission hook running before admission (see Non-scope) rather than
  wrapping handlers one by one, with ordering tests proving
  guard-before-admission/dedup/token-validation)**, explicitly NOT in
  `remoteHostAdminMethods` (negative assertion in the allow-list tests).
- **AppWire protocol catalog** entries for every new method + request/response
  types, and the **regenerated TypeScript client** — both in the same PR as
  the handlers, so the frontend can consume them — with the exact shapes in
  Protocol types, below, field-for-field, or the client drifts from the
  contract.
### Protocol types

Every new method gets its AppWire protocol catalog entry
(`appwire/protocol.go`, `ScopeHub`) plus request/response structs
(`appwire/types.go`) and the regenerated TypeScript client — in the same PR
as the handlers. `evener/host/attach` keeps its shipped shapes
(`HostAttachParams{host}`, `HostAttachResponse` identity fields) unchanged;
every new type below follows the same conventions (`name` names the host in
every host-management request field and in `HostRow` — distinct from
`attach`'s shipped `HostAttachParams{host}`, which is unchanged;
lowerCamel JSON, optional facts/error fields absent — never null — when
unknown). (`HostPlan.host`/`OperationRecord.host` — the plan/token and
operation-record bindings, not request fields — keep `host`.)

- `evener/host/list`: params `{}`; response `{hosts: HostRow[]}`. `HostRow`
  is the full effective `HostConfig` fields (`name`, `ssh`, `user`,
  `evener_path`, `config_path`, `addr`, `roots`) plus live state:
  `attached: bool`, `installedVersion?: string`,
  `installedVersionAgeSec?: number`, `osArch?: string`,
  `lastAttachError?: string`, `lastAttachErrorAgeSec?: number`,
  `midEnsure: bool`, `origin: "hub.toml" | "sidecar"`, `removed: bool`,
  `retainedRows?: number` (tombstone rows only), `generation: number`.
  **Tombstone values:** a tombstone row renders from retained effective
  `HostConfig` with `attached: false`, `midEnsure: false`, and the removed
  entry's `origin` and `generation`; `installedVersion?`,
  `installedVersionAgeSec?`, `osArch?`, `lastAttachError?`, and
  `lastAttachErrorAgeSec?` stay absent (never null) per the absent-when-unknown
  rule — every other `HostRow` field carries the explicit value above, so no
  non-optional field is left unknown.
- `evener/host/add`: params are one full host entry (all seven `HostConfig`
  fields; `name` required) plus optional `mutationId: string` (opaque,
  non-empty, at most 128 bytes — the idempotency key; see the mutation
  idempotency rule above); response `{host: HostRow}`.
- `evener/host/update`: params `{name: string, entry: <the six non-name
  `HostConfig` fields>, mutationId?: string}`; response `{host: HostRow}`.
- `evener/host/remove`: params `{name: string, mutationId?: string}` (defined once —
 the same optional idempotency key as `add`/`update`); response `{name: string,
 removed: true, retainedRows: number}`. A replay carrying a known
  key returns the recorded receipt without re-applying.
- `evener/host/status`: params `{name: string}`; response is the host's
  `HostRow` plus the deploy plan inputs: `controllerBuild: string`,
  `resolvedTargetPath?: string`, `restartFollows: bool`,
  `factsRevision?: string`, `factsAgeSec?: number`, `planRefusal?:
  {terminal: bool, message: string}`.
- `evener/host/plan`: params `{name: string}`; response is either `{plan:
  HostPlan, token: string}` or `{staleFacts:
  {message: string, attached: bool, reason: "stale-facts" | "unattached" |
  "probe-failed" | "handler-absent"}}` — the `reason` discriminates the
  failure: `stale-facts` (known facts too old and no refresh was attempted or
  it was refused), `unattached` (host not attached — Connect first),
  `probe-failed` (attached, but the `evener/host/running` probe read failed or
  was unauthenticated), `handler-absent` (attached, but the remote predates the
  handler — take the one-time migration path). `HostPlan` is `{host, generation,
  targetPath, controllerRevision, restartFollows, factsRevision,
  hubTomlFingerprint, factsCapturedAt: string (RFC3339), factsAgeSec: number,
  runningVersion: string, runningHealthy: bool}` — the token's bindings plus
  the server-generated facts capture timestamp and age behind the deploy
  confirmation's freshness line. `plan`/`token`
  are absent — never null — on the stale-facts response, per the absent-
  when-unknown rule above.
- `evener/host/running` (controller-side method, same-scope 08b — catalog
  entry + TypeScript client with the handler): params `{}`; response
  `{buildRevision: string, healthy: bool}`. Served locally by every hub —
  `buildRevision` from the same source as the `controllerBuild` plan input,
  `healthy` the hub's own health — admitted only over an attached session
 peered by the #1603 handshake (read classification; never forwarded onward
 to a third hub; browser-origin and forwarded requests refused exactly like
 every other `evener/host/*` request — the direction-scoped peer-probe
 exception of Non-scope and `plan`). The
 `plan` probe calls it through `sshManager.ChannelIfAttached(name)`; its
 response fields are what `plan` records as `HostPlan.runningVersion` /
 `runningHealthy`.
- `evener/host/teardown-retry` (mutation): params `{remnantId: string}`;
  response `{host: HostRow}` — resumes and clears the named remnant, or typed
  not-found / busy / stale-entry (see the commit point).
- Mutation conflict: `conflicting-mutation-id` rides the same AppWire error
  envelope as `conflicting-operation-id` and is refused the same way.
- `evener/host/deploy`: params `{name: string, token: string, operationId:
  string}` (client operation ID: opaque, non-empty, at most 128 bytes);
  response `{id: string, clientOperationId: string, state: OperationState}`
  (`id` is the controller-assigned record id): the freshly created record
  reports `"pending"`, and a dedup hit returns the existing record's actual
  state (`pending`/`running`/`complete`/`failed`/`interrupted`) — the same
  `OperationRecord.state` the `operations` method returns.
- `evener/host/restart`: params `{name: string, operationId: string}`;
  response `{id: string, clientOperationId: string, state: OperationState}`
  (fresh create: `"pending"`; dedup hit: the existing record's state, as
  for `deploy`).
- `evener/host/operations`: params `{name?: string, operationId?: string,
  state?: OperationState}` (all optional filters; empty params lists all);
  response `{operations: OperationRecord[]}`. `OperationRecord` is `{id,
  clientOperationId, host, kind: "deploy" | "restart", state:
  "pending" | "running" | "complete" | "failed" | "interrupted", progress:
  ProgressEntry[], result?: {ok: bool, message: string}, createdAt: string,
  updatedAt: string, hostRemoved: bool}`. `ProgressEntry` is `{ts: string
  (RFC3339), message: string}`, bounded per record.
- Typed errors ride the existing AppWire error envelope with stable `code`
  strings: `host-not-found`, `host-busy-operation` (names the operation id),
  `host-busy-transient` (no operation reference), `stale-entry` (entry,
  target, generation, or `hub.toml`-fingerprint mismatch),
  `token-missing` / `token-mismatched` / `token-superseded` /
  `token-expired`, `conflicting-operation-id`, `conflicting-mutation-id`, `too-many-hosts`
  (`ErrTooManyHosts`), `swap-failed` (names the seam),
  `committed-with-teardown-failure` (names the seam; the mutation is durable
  and the pending teardown is named for forward retry — not a failed
  mutation), `teardown-unknown-key` (unknown or already-cleared remnant),
  `session-unavailable`
  (the #1603 attach classifier); `interrupted` is a terminal record state
  (outcome unknown), not a thrown error.
- The **operation store**: a small durable store (records keyed by operation
  id, **dedup index on client operation ID scoped by (host, kind, host
  generation), with the
  typed conflicting-reuse refusal and the `host-removed` never-match rule**,
  atomic temp+rename writes, startup reconciliation to `interrupted` plus
  the tombstone-derived `host-removed` pass) + optional host-notification
  publisher extension. Where it
  lives on disk follows the hub's existing durable-record conventions (the
  implementing session picks the closest existing store pattern and names it
  in the PR).
- `cmd/evener-hub/main.go` — replace the startup snapshot of host entries
  with the live view seam (registry, manager, sources, web config, **and the
  host-admin controller's host set/fan-outs**); keep every existing
  `RemoteHost*` wiring.
- Frontend: `settings/sections/hosts/*` + settings registration, host-manager
  store, `operations` polling, notification subscription reuse. The spawn
  picker is untouched (its Connect trigger ships with #1603).
- PR sequence: **08a** persistence + hot-apply + registry/manager/admin-
  controller surface (including the per-host gate shared with `Ensure`) +
  `list/add/update/remove` (with catalog + client
  regeneration); **08b** `status`/`plan` (token) / `deploy` / `restart` /
  `operations` + the operation store; **08c** the frontend (settings section,
  dialogs, deploy confirmation). 08b stacks on 08a; 08c stacks on 08b.

## Data flow (remove + tombstone)

`refreshRemoteThreadSnapshot` enumerates the registered sources; removing one
would otherwise drop its rows from the next snapshot. Removal instead writes
an **explicit tombstone record for the source** (name, the removed entry's
effective `HostConfig` — all seven fields `HostRow` requires, so `list` can
render the removed row without a live entry — last-known-good rows,
removal timestamp): the rows stay in the navigation tree marked **stale**,
and the tombstone is **consumed by the tree and the action-capability
logic** — removed-host rows must be visibly non-actionable
(markers in the tree/rail; actions disabled), not merely `Complete == false`,
because liveness alone does not make an unknown source's rows stale in the
current navigation projection. **Tombstoned hosts are not manifest
sources:** removal deletes the source-registry entry, so the navigation
manifest's `sources` array drops the removed host on its next read — the
manifest element schema is exactly `{id,label,kind,online}`
(`hubapi.Source`, `hubapi/types.go`; the component-06 read contract pins
those four fields and its enumeration is the registered-source set), with
no `removed`/`stale` field to carry a tombstone, so no schema change is in
scope. The tombstone's surviving surfaces are the stale tree rows and the
`list` row (`removed: true`) — the Hosts list reads tombstones from the
controller, never from the sources manifest. The tombstone is purged when
the same host name is re-added, or after a retention period (owner-set;
open question 2 — **the owner knob is the retention period**). **Expiry
mechanism (explicit, no background timer):** the controller evaluates expiry
 lazily — `list` filters in memory with no lock (see `list`), and every
 sidecar mutation prunes durably in its atomic write under the
 mutation lock — and drops each tombstone whose `removal timestamp + retention
 period` has passed: the read path omits it from the response without taking
 the lock, and the next mutation-path
 atomic sidecar write under the lock prunes it durably (plus that name's
 receipts and remnants, per the retention rule in persistence). **Expiry
 never purges a tombstone whose name still holds an open teardown remnant —
 the mutation-path prune skips remnant-gated names, and the in-memory filter
 keeps rendering them — so the failed teardown's generation-specific handles
 are completed through `teardown-retry` before the gate releases (see the
 remnant gate in persistence).** The data-flow
 purge sentence stands — re-add (past the remnant gate) and retention-expiry
 (past the same gate) both prune — and open
question 2 now asks only for the default value, not the mechanism.

**Tombstones are durable:** they persist as a section of the managed sidecar
itself (same file, same atomic writes — see persistence), so a removal's
delete-entry + write-tombstone is one write, boot restores them alongside the
host entries, and they survive controller restarts; re-add and
retention-expiry purges rewrite the same file atomically under the mutation
lock.

**Host generations:** every `add` (including re-add) mints, and every
`update` advances, a per-name
generation that is **durable: persisted in the managed sidecar alongside the
host entries (including the per-name high-water mark for removed names) in
the same atomic write as the entry mutation** — `add` mints generation 1 for
a never-seen name, `update` advances the live generation by one, and re-add
mints a generation strictly greater than any generation that name has ever
carried, so a re-added byte-identical entry still invalidates every
pre-remove token. The boot load restores persisted generations before the
store serves any request, so the generation check — the token's generation
must equal the registry's current generation — survives restarts: an
update-then-restart keeps outstanding tokens valid (the bumped generation is
on disk), and no restart silently invalidates or re-validates anything.
**Store-side mirror — extending the round-eight persisted sidecar
generations:** the per-name generation high-water mark is mirrored into the
operation store itself (same atomic store writes as records), and boot takes
the maximum of the sidecar mark and the store mirror as the name's restored
generation — so deleting a corrupt sidecar and re-adding an identical host
cannot restart its generation at 1 and adopt the old incarnation's records
or tokens: the re-add mints above the mirrored high-water mark instead.
Deleting both durable files is the only clean-slate path, and the spec
names it as such — there is no silent history adoption either way.
**Boot-merge collision (extending the round-ten wording): when a retained
tombstone collides at boot with a newly live `hub.toml` host (or a live
sidecar entry from a re-add), the live host is treated as a new incarnation:
its restored generation is set strictly above the tombstone's high-water mark
before any historical receipts/remnants apply, so old `host-removed` records
stay at or below the mark and live records stay unmarked — never a promotion
of a pre-collision record into the current generation, never a block on valid
operation-ID reuse.**
`hub.toml`-declared hosts carry a
stable generation as long as their effective entry is unchanged — since the
sibling reconciling commit above detects external `hub.toml` edits after the
fact, any detected change to a `hub.toml`-declared host's effective entry
— detected by the sidecar staged commit's `hub.toml` re-reads (validation,
final check, post-rename reconcile, all under the mutation lock) or by a
`plan`/`deploy` fingerprint check, never by a background watcher —
advances that host's generation and clears or
rebinds its name-keyed cached state — the detecting path adopts the re-read
entry into the running registry first, then bumps the generation, then
clears or rebinds: resolved deploy targets, deployment
state, last-known facts entries, supervisor bindings, channel handles, and
outstanding tokens — so a stale snapshot or preflight facts read from the
old configuration can never pass a generation check against the new one.
The UI cannot remove or re-add these hosts; only the entry-change rule moves
their generation. Token bindings
reference these generations (see `plan`): validation requires the token's
generation to equal the registry's current generation for the name. Snapshot
publications carry the generation of the source they were read from, and a
publication whose generation no longer matches the registry's current
generation for that name is rejected; **re-add also clears the name-keyed
caches** (snapshot rows and last-known preflight facts) as part of its staged
commit — an in-flight refresh from the removed incarnation or a stale
name-keyed cache entry can never republish rows for the new host.

## Error handling

- Attach failures: typed `SessionUnavailable` via the #1603 classifier; the
  caller-context error stays raw; the UI renders the classification, not the
  raw chain.
- Deploy: dedup-first processing order is fixed; **conflicting operation-ID
  reuse → typed refusal**; invalid/expired/superseded
  tokens → refusal, always; **execution-time re-resolution mismatch — entry,
  target, generation, or `hub.toml` fingerprint, on `deploy` or `restart` → typed
  stale-entry refusal with a re-plan/retry instruction**; **a held per-host
  gate → typed busy refusal — naming the in-flight operation when a
  deploy/restart record holds the gate, transient `host busy (plan/ensure in
  progress)` with no operation reference otherwise**; plan-time refusals surface verbatim
  (`errControllerDirty` is terminal — the UI must not offer retry-anything;
  unmet prerequisites shown before confirmation); runtime failures land in
  the operation record verbatim; `interrupted` records tell the user the
  outcome is unknown and a new operation may be started.
- Host busy: a held per-host gate fails `update`, `remove`, `plan`, and any
  new `deploy`/`restart` fast with a typed busy error: the
  operation-held form names the in-flight operation, and the UI surfaces
  "operation in progress" with open-or-wait; the transient
  plan/`Ensure`-held form carries no operation reference, and the UI shows
  "host busy (plan/ensure in progress)" with retry and no open/wait
  affordance. A refused mutation leaves the running
  operation untouched — it finishes and records its normal terminal state.
- Hot-apply failures: staging failures change nothing; a failed swap
  compensates by restoring the prior sidecar bytes and reverting the runtime
  (both before the response), and the error carries which seam
  failed (registry / manager / source / admin controller / web view /
  persistence / decision-source validation).
- `evener/host/*` on an unknown host name: typed not-found.

## Testing

- 08a: registry live-update tests (including the add-time cycle rules),
  manager add/remove-vs-supervisor tests (removing an attached host stops
  its supervisor, closes its channel, and drains its per-host lifecycle
  handles before the entry is gone), sidecar merge/atomicity tests
  including the refuse rules, **the 63-remote-host cap (63 live remotes
  add; the 64th fails `ErrTooManyHosts`)**, the boot-time hard-error duplicate, **swap-failure
  compensation (prior sidecar bytes restored, runtime reverted, retry
  re-applies cleanly)**, and **the corrupt/schema-invalid sidecar boot hard
  error**, the host-admin controller live-set tests (forwarded requests reach
  newly added hosts; removed hosts' fan-outs stop), **update-rebind tests (a
  running supervisor's next reconnect uses the updated entry; the rebind
  completes before the gate is released)**, tombstone tests (list
  attached → remove → rows persist and are marked stale/non-actionable in
  tree + capabilities while the manifest's `sources` array drops the host;
  **tombstones survive a controller restart;
  re-add purges, clears the name-keyed caches, and mints a new generation —
  publication from an obsolete generation is rejected**),
  **decision-source live-set tests (a newly added host is accepted by
  archive/favorite validation immediately after the commit; a removed host is
  refused; validation reads the live set, never the startup snapshot)**,
  **ingress-boundary ordering tests (a remote-originated request is refused
  before admission, before dedup, before token validation — proven by ordering
  tests, not by handler wrapping)**,
  **update-generation tests (update advances the generation and
  clears/generation-keys resolved targets, facts, and bindings before
  rebinding)**,
  **remote-origin rejection for `list`/`add`/`update`/`remove`** (the #1603
 origin guard refuses peer-forwarded requests before admission; `running`
 included — only the direction-scoped attached-session peer probe passes), and a
  wiring test mirroring the 05a registration tests.
  **Mutation-idempotency tests (the operation store's cross-name rule,
  applied to mutations): cross-name/cross-kind `mutationId` replay is the
  typed `conflicting-mutation-id` refusal; same-key replay after remove/
  re-add opens fresh (superseded-generation receipts are not a conflict).**
 **Commit-point tests: a committed-with-teardown-failure persists the
 remnant with its opaque `remnantId` in the response, replaying the original
 `mutationId` stays a no-op receipt return, and `teardown-retry` completes
 only the named teardown (unknown or
 cleared ID → `teardown-unknown-key` not-found; moved generation →
 stale-entry; remnant-gated re-add refused until the retry completes).**
 **Lock-free `list` tests: `list` takes no mutation lock and prunes nothing
 durably; expiry filtering is in-memory only and the durable prune lands on
 the next mutation-path write. Remnant-gate tests: re-add and retention expiry
 skip names with open remnants. Config-path tests: a `--config` startup
 carries the canonical path into the web config and the sidecar + fingerprint
 derive from it. Collision tests: a tombstone/live collision restores the
 live generation strictly above the high-water mark with pre-collision
 records marked and live records unmarked. File-posture tests: sidecar and
 store temp files are `0600`, renames preserve the mode, and startup refuses
 a file readable beyond its owner.**
- 08b: handler tests per method (validation, admission, classification incl.
  `plan`-as-mutation, **remote-origin rejection for
  `status`/`plan`/`deploy`/`restart`/`operations`/`running`/`teardown-retry`**,
  the token matrix:
  missing/mismatched/expired/superseded/consumed-then-replayed-with-new-op-ID,
  **generation-bound (a token minted under generation N is refused after
  remove/re-add even for a byte-identical entry; live remove drops
  outstanding tokens; re-add starts with none)**,
  **token persistence (mint is a durable store write held under the gate;
  expiry reaped lazily and at boot; tombstoned hosts' tokens dropped),**
  **post-operation refresh (a completed deploy/restart publishes verified
  fresh facts to the generation-scoped last-known store; `status` reports the
  new version)**,
  **busy-holder classes (an operation-held gate names the operation; a
  plan/`Ensure`-held gate returns the transient form with no operation
  reference and the UI shows retry, not open/wait)**,
  **protocol shapes (catalog entries and the regenerated client match the
  Protocol types section field-for-field)**,
  **the running probe (`evener/host/running` handler: local revision +
  health, attached-session admission only — unauthenticated probe refusal;
 browser/forwarded requests refused; never forwarded onward (no A→B→A chain);
  the gate-held `plan` probe call; `HostPlan.runningVersion` /
  `runningHealthy` placement; handler-absent named for pre-handler
 remotes with the one-time manual-upgrade migration path; no-token `reason`
 discriminates `stale-facts` | `unattached` | `probe-failed` |
 `handler-absent` and the UI branches on it — no Connect loop for attached
 probe failures)**,
  **restart reattach (the worker retains the gate across the channel drop,
  re-runs the attach closure for the pinned entry, and re-probes over the
  reattached channel; an initially unattached restart attach-firsts under
  the same gate and names it in the record)**,
  **the stale-facts gated refresh: an attached host refreshes via the
  bounded one-shot SSH preflight (no channel initialization, no supervisor,
  no attach state machine) then mints; an unattached host gets the
 no-token refusal naming Connect; the deploy/restart worker's post-operation
 preflight is channel-free under the same pin**, execution-time re-resolution mismatch
  **including the `hub.toml` fingerprint (a manual edit between plan and
  deploy refuses; a manual edit between restart's resolution and its gate
  acquisition refuses) and the post-acquisition entry re-read (`restart`
  and `Ensure`-triggered work refuse or re-resolve when a mutation lands
  between resolution and gate acquisition)**, **(host, kind, generation)-
  scoped operation-ID dedup including the interrupted-record path, the
  clean-slate re-add path (op-ID reuse after remove/re-add opens fresh),
  and the conflicting-reuse refusals: same ID different host,
  deploy-vs-restart, and a current-generation `host-removed` record
  (IDs used up only by a removed incarnation are cleanly reusable — the
  re-add path above)**, per-host
  gate serialization
  vs an in-flight `Ensure` deploy, **worker lifetime (a client disconnect
  after record creation leaves the operation running; controller shutdown
  records `interrupted` and releases the gate)**, **the busy refusals
  (`update`/`remove`/`plan`/new-deploy on a held gate) and the atomic
  consume-and-create write (a replayed deploy after a crash in that window
  finds a record, never a silently consumed token)**, startup reconciliation
  of pending/running → interrupted **plus the tombstone-derived
  `host-removed` pass (a crash between a remove's sidecar commit and its
  live mark still never-matches at boot)**, **the corrupt operation-store
  boot hard
  error**), and the not-in-forwarded-allow-list assertion.
- 08c: `make test-web`, browser gate (`env -u DBUS_SESSION_BUS_ADDRESS make
  test-web-browser`), `make lint-generated`; per-flow tests in the section's
  test files; the never-dial invariant of `list`/`status`/`operations`
  test-pinned, with `list`/`status` serving last-known facts, ages, and
  attach errors from the manager store for offline hosts.
- **Live E2E (strongly recommended, never yet exercised):** the campaign
  landed 04b without a live deploy against a real host. 08b/08c should run
  one add → connect → deploy → restart → spawn-remote cycle against a
  disposable host before declaring the component done; that requires a host
  Jesse designates.

## Acceptance criteria

1. A user with zero hosts configured adds one from the UI, sees it connect,
   and the spawn picker appears with the host selectable — no file editing,
   no controller restart.
2. An offline configured host has a working Connect action in the Hosts
   section (the picker's trigger shipped with #1603); success flips its
   online state everywhere (rail, picker, manifest) through the existing
   paths.
3. Deploy from the UI: the confirmation renders the controller-minted plan
   (controller revision + resolved remote target path) and cannot proceed
   without a fresh token; an edit between plan and deploy is rejected and
   re-planned — including a manual `hub.toml` edit, caught by the
   fingerprint; an edit attempted while the deploy runs is refused with a
   busy error and the push is unaffected; after confirm, the host reports
   the new version in `status` via the worker's required post-operation facts
   refresh (published to the generation-scoped last-known store before the
   operation marks `complete`), and the version-skew signal (facts vs
   controller build) is truthful; a replayed deploy with the same operation
   ID returns the same record; a controller crash mid-deploy surfaces as
   `interrupted`, never a stuck operation.
4. Hand-edited `hub.toml` hosts keep working exactly as today; UI add/update
   refuse their names; the origin marker shows which file owns each entry; a
   hand-created duplicate name across files is a hard startup error.
5. Removing a sidecar host retains its last-known-good rows as an explicitly
   stale, non-actionable tombstone (tree + action capabilities, test-pinned)
   while the manifest's `sources` array drops the host; the tombstone
   survives a controller restart; re-adding the same name
   purges it, clears its name-keyed caches, and mints a new generation, so
   stale rows from the old incarnation cannot republish; `list` shows it
   with `removed: true`.
6. No lazy-attachment regression: with no attach call, no explicit source,
   no read dials anything — `list`/`status`/`operations` never attach
   (test-pinned) — and `plan`'s facts refresh and the deploy/restart worker's
   post-operation preflight, the surface's two deliberate
   non-attach SSH uses, are channel-free by construction (no initialize, no
   supervisor, no attach state machine; test-pinned).
7. Standard gates green on every PR (go/build/vet, package races, full hub
   suite, module-lint; web + browser + lint-generated for 08c).

## PR size estimate (LOC)

- 08a: ~800–1100 (registry/manager/config/admin-controller/wiring/catalog/
  client) + tests.
- 08b: ~700–1000 (handlers + token + operation store + reconciliation) +
  tests.
- 08c: ~800–1100 (section, dialogs, stores, polling) + tests.

## Open questions

1. **Sidecar vs rewrite of `hub.toml`** — spec'd as sidecar with the refuse
   rules and hard-error duplicate; the owner may still prefer in-place
   `hub.toml` rewrite accepting comment loss.
2. **Tombstone retention period** — re-add purges by name; what is the
   default retention for never-re-added hosts (e.g. 7 days)?
3. **Who may add hosts** — reuse the settings-mutation admission as-is, or a
   distinct grant? Spec assumes as-is.
4. **Per-host detach** — out of scope here; natural follow-up once remove
   semantics settle.
5. **`ServerInfo.Version` constant** — owner follow-up from #1603 r2; does not
   block this component (version display reads preflight facts).
