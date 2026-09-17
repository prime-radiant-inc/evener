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
  `evener/host/operations`. `evener/host/attach` ships with #1603 and is
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
  `evener/host/*` handler (all ten methods, `attach` included) is routed
  through #1603's shared origin guard and **rejects remote-originated,
  peer-forwarded requests before any handler logic runs** — before admission,
  before dedup, before token validation. The mutating handlers (`attach`,
  `plan`, `deploy`, `restart`, `add`, `update`, `remove`) are unreachable
  remotely outright; the reads (`list`/`status`/`operations`) are guarded
  equally because they disclose the controller's topology and operation
  state. (`attach`'s guard routing ships and is test-pinned with #1603; this
  component pins the other nine.) The proxy's *host set* does become live
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
  refresh is the one deliberate non-attach SSH use: bounded one-shot preflight
  command executions over the manager's transport that never initialize a
  channel or touch a supervisor; see `plan`).

## Contract / interfaces

### Method surface

All are hub-side handlers registered like every other hub method (the
router-vs-catalog test pins registration), classified as mutations where they
mutate, admission-gated like the hub's other settings mutations, **and
origin-guarded — #1603's shared origin guard rejects remote-originated
requests before any of the above runs (see Non-scope)** — and each gets
its AppWire protocol catalog entry + regenerated TypeScript client.

**Host mutations are serialized by one process-wide lock** (all of
`add`/`update`/`remove` take it for their entire staged commit), so concurrent
read-modify-write on the sidecar cannot lose updates.

- `evener/host/list` (read, never dials): every configured host — the full
  effective `HostConfig` fields plus live state: `attached`
  (`sshManager.ChannelIfAttached(name)`), preflight facts when known (installed
  version, OS/arch), last attach error, whether the manager is currently
  mid-`Ensure`, and the **origin marker** (declared in `hub.toml` vs declared
  in the sidecar — the effective source after merge). **Last-known facts,
  their ages, and attach errors come from a manager-owned store keyed by
  host generation (see `status`): the `ChannelIfAttached` lookup reports
  only the current channel, so offline rows would otherwise go blank.**
  **Removed-but-retained
  hosts (tombstones) appear as `list` rows with `removed: true`** and their
  retained-row count — one surface, no separate retained view.
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
  (re-add), and the same staged commit purges that tombstone in its atomic
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
  This is the pure read behind the host row's informational detail —
  the deploy confirmation renders `plan`'s minted response, never `status`
  (see UI).
- `evener/host/plan` (mutation — it mints durable controller state, so it is
  classified and admitted as one): body `{name}`. Builds a **fresh plan from
  the current facts and configuration** and returns the plan plus a
  **controller-minted confirmation token**: single-use, **expiring (token
  TTL: 10 minutes by default, owner-adjustable)**, bound to
  (host name, a hash of the resolved host entry, **a fingerprint (content
  hash) of the controller's `hub.toml` file bytes as they are on disk at
  plan time**, the preflight revision the plan was built from, the resolved
  target path, controller revision, nonce). The `hub.toml` fingerprint is
  what makes a manual edit of that file invalidate outstanding tokens at
  deploy time even though the running process loads it only at startup (see
  `deploy`).
  **Outstanding-token rule: minting a new token for a host immediately
  supersedes any earlier unconsumed token for that host.** `plan`
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
  facts.
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
- `evener/host/deploy` (mutation): **processing order is fixed.** (1) Dedup
  first: if the client operation ID matches an existing durable operation
  record **of the same host and kind** (a `host-removed` record never
  matches), return that record — no token validation, no consumption (this
  makes the single-use token and idempotent retry coexist: a replay after a
  lost response succeeds without a fresh token). **A client operation ID
  colliding with a record of a different host or kind, or with a
  `host-removed` record, is refused with a typed conflicting-operation-ID
  error — never a hit, never a new operation under the colliding ID.**
  (2) Otherwise validate the token: **missing,
  mismatched, superseded, or expired → refusal**. (3) Acquire the host's
  per-host gate — **fail fast with the typed busy error if held** — and
  under it **re-resolve the target, re-read the host entry, and re-hash the
  `hub.toml` fingerprint at execution time and reject if any differs from
  the token's bindings** (a sidecar edit, a manual `hub.toml` edit — caught
  by the fingerprint, the only way a hand edit is visible without a restart
  — a facts refresh, or a target change between plan and deploy invalidates
  the plan; the UI re-plans). This re-read is the gate protocol's
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
  resolved-target persistence.
- `evener/host/restart` (mutation): same operation model (**(host, kind)**-
  scoped operation-ID dedup first, busy-fail gate acquisition, atomic record
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
  `waitHealthy` proven replacement).
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
  operation ID), never the ID alone:** a same-key replay returns the
  existing record; an ID colliding with a different-key record — different
  host, different kind, or a `host-removed` record — is refused with the
  typed conflicting-operation-ID error, so a reused ID can never hand back
  an unrelated operation's progress while the caller's actual operation
  never starts.
- **Durability:** every operation-store write is atomic (temp + rename +
  fsync), and token consumption and record creation are one such write (see
  `deploy`). **A corrupt or schema-invalid store file at boot is a hard
  startup error naming the file** (recovery is deleting it: operation
  history is lost, nothing else is).
- **Crash recovery:** at startup, before the store serves any request, every
  record still in `pending`/`running` transitions to `interrupted` (a
  terminal unknown outcome) with a note naming the crash; a retry with the
  same operation ID gets the `interrupted` record back, and a new operation
  ID starts a fresh operation. **The same boot pass, after the sidecar is
  loaded and after the interrupted transition, applies every loaded
  tombstone to the store: each tombstone (removed host name) marks that
  host's records `host-removed` — reconciled and marked BEFORE the merge
  rule discards any tombstone colliding with a live `hub.toml` host (a
  reintroduced name keeps its durable removed-incarnation mark even though
  the live entry wins and the colliding tombstone is dropped).** A remove's
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
- Progress is readable via `evener/host/operations` (guaranteed). If the
  implementing session extends the host-notification stream with a
  controller-originated best-effort event class, the UI may render from it
  but MUST fall back to polling; polling-only is a fully valid implementation.
- The UI renders progress → terminal state. Never a synchronous RPC.

### Persistence + hot-apply

- **Persistence target:** the controller's `hub.toml` is hand-authored with
  comments; a TOML re-marshal would strip them. The UI writes a managed sidecar
  in the same config dir (e.g. `hub.hosts.json`), loaded after `hub.toml`.
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
  committed as a duplicate.** **A name found in both `hub.toml` and the
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
  fix or delete the file; only sidecar state is lost).**
- **Staged commit (order matters):** add/update/remove never mutate live
  state incrementally. Under the process-wide mutation lock: (1) stage the
  complete change — new registry value, the *description* of the manager
  deltas (including the planned supervisor/channel teardown for removals),
  source-registry rows, host-admin-controller host set and its notification
  fan-outs, web-config host view, and the new sidecar bytes; (2) **persist
  the sidecar first** — stashing a durable copy of the prior sidecar bytes
  (same config dir) before the atomic rename, so the swap is compensable;
  (3) **swap the runtime to the staged set** — rebinding or wiring the
  live handles to the new values, and only then executing the planned
  teardowns (supervisor/channel stops, fan-out cancellations) as the
  post-commit rebind phase (see the mutation rebind ordering in the
  operation store); (4) if the swap itself fails, **compensate
  fully before responding: restore the prior sidecar bytes from the stash
  (atomic rename), then revert the runtime to the previous set**, then
  report exactly which step failed — a typed swap-failure response is sent
  only after both restores, so **a reported failure always means disk and
  runtime agree on the pre-change state** (no teardown runs before the
  commit, so compensation never has to restore a torn-down supervisor)
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
  target path, whether a restart follows, facts freshness, and any plan-time
  refusal (terminal ones disable the button). The deploy confirmation
  submits exactly the displayed plan: `evener/host/deploy` with that plan's
  token and a client operation ID — never a plan the user has not seen. If
  `deploy` rejects the token as stale (expired, superseded, or
  binding-mismatched), the UI re-plans and re-renders the confirmation from
  the new response before any retry. Stale-facts responses from `plan` (only
  possible when the host is unattached or its refresh failed) direct the UI
  to Connect first. `status` stays the read-only informational surface
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
  durable state; `list`/`status`/`operations` are reads), **each handler
  wrapped in #1603's shared origin guard (remote-originated requests are
  rejected before admission — see Non-scope)**, explicitly NOT in
  `remoteHostAdminMethods` (negative assertion in the allow-list tests).
- **AppWire protocol catalog** entries for every new method + request/response
  types, and the **regenerated TypeScript client** — both in the same PR as
  the handlers, so the frontend can consume them.
- The **operation store**: a small durable store (records keyed by operation
  id, **dedup index on client operation ID scoped by (host, kind), with the
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
an **explicit tombstone record for the source** (name, last-known-good rows,
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
open question).

**Tombstones are durable:** they persist as a section of the managed sidecar
itself (same file, same atomic writes — see persistence), so a removal's
delete-entry + write-tombstone is one write, boot restores them alongside the
host entries, and they survive controller restarts; re-add and
retention-expiry purges rewrite the same file atomically under the mutation
lock.

**Host generations:** every `add` (including re-add) mints a per-name
generation in the live registry (in-memory; restarts rebuild every cache, so
the counter needs no separate persistence — `hub.toml`-declared hosts carry a
stable generation, since the UI cannot remove or re-add them). Snapshot
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
  target, or `hub.toml` fingerprint, on `deploy` or `restart` → typed
  stale-entry refusal with a re-plan/retry instruction**; **a held per-host
  gate → typed busy
  refusal naming the in-flight operation**; plan-time refusals surface verbatim
  (`errControllerDirty` is terminal — the UI must not offer retry-anything;
  unmet prerequisites shown before confirmation); runtime failures land in
  the operation record verbatim; `interrupted` records tell the user the
  outcome is unknown and a new operation may be started.
- Host busy: a held per-host gate fails `update`, `remove`, `plan`, and any
  new `deploy`/`restart` fast with a typed busy error naming the in-flight
  operation; the UI surfaces "operation in progress" and offers to open or
  wait on the running operation. A refused mutation leaves the running
  operation untouched — it finishes and records its normal terminal state.
- Hot-apply failures: staging failures change nothing; a failed swap
  compensates by restoring the prior sidecar bytes and reverting the runtime
  (both before the response), and the error carries which seam
  failed (registry / manager / source / admin controller / web view /
  persistence).
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
  **remote-origin rejection for `list`/`add`/`update`/`remove`** (the #1603
  origin guard refuses peer-forwarded requests before admission), and a
  wiring test mirroring the 05a registration tests.
- 08b: handler tests per method (validation, admission, classification incl.
  `plan`-as-mutation, **remote-origin rejection for
  `status`/`plan`/`deploy`/`restart`/`operations`**, the token matrix:
  missing/mismatched/expired/superseded/consumed-then-replayed-with-new-op-ID,
  **token persistence (mint is a durable store write held under the gate;
  expiry reaped lazily and at boot; tombstoned hosts' tokens dropped),**
  **the stale-facts gated refresh: an attached host refreshes via the
  bounded one-shot SSH preflight (no channel initialization, no supervisor,
  no attach state machine) then mints; an unattached host gets the
  no-token refusal naming Connect**, execution-time re-resolution mismatch
  **including the `hub.toml` fingerprint (a manual edit between plan and
  deploy refuses; a manual edit between restart's resolution and its gate
  acquisition refuses) and the post-acquisition entry re-read (`restart`
  and `Ensure`-triggered work refuse or re-resolve when a mutation lands
  between resolution and gate acquisition)**, **(host, kind)-scoped
  operation-ID dedup including the interrupted-record path and the
  conflicting-reuse refusals: same ID different host, deploy-vs-restart,
  and a removed host's `host-removed` record**, per-host gate serialization
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
   the new version in `status`, and the version-skew signal (facts vs
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
   (test-pinned) — and `plan`'s facts refresh, the surface's one deliberate
   non-attach SSH use, is channel-free by construction (no initialize, no
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
